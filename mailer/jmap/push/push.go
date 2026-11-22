// Package push implements RFC 8620 section 7: StateChange events delivered
// over an EventSource stream or to verified PushSubscription webhooks.
//
// Emissary also runs as a collection of Lambda functions, where in-process
// subscribers cannot see changes made by another invocation. State is
// therefore read from the durable change tracker: connected streams poll the
// tracker, and a JMAP request flushes any changes it made before its response
// is written. Changes may be coalesced and dropped without losing data; the
// next /get or /changes call still discovers them.
package push

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/kncept/Emissary/mailer/components/storage"
	"github.com/kncept/Emissary/mailer/jmap/internal/base"
	"github.com/kncept/Emissary/mailer/jmap/pushsubscription"
)

// JMAP type names used as keys in a TypeState object (RFC 8620 section 7.1).
const (
	TypeEmail           = "Email"
	TypeEmailDelivery   = "EmailDelivery"
	TypeMailbox         = "Mailbox"
	TypeThread          = "Thread"
	TypeIdentity        = "Identity"
	TypeEmailSubmission = "EmailSubmission"
	TypeVacation        = "VacationResponse"
	TypeBlob            = "Blob"
)

const (
	// DefaultPollInterval bounds the latency of an EventSource that learns
	// about changes made by another process (typically the inbound Lambda).
	DefaultPollInterval = 2 * time.Second

	// DefaultCoalesceWindow is the shortest interval between state events.
	// Several changes to the same type within one window are sent once.
	DefaultCoalesceWindow = 500 * time.Millisecond

	// DefaultMaxConnections limits streams per credential; a client should
	// use one connection (RFC 8620 section 7.3).
	DefaultMaxConnections = 8

	// pushTTL is the RFC 8030 TTL, in seconds, attached to webhook POSTs.
	pushTTL = "60"

	// backoffAfterRateLimit is applied when a push endpoint answers 429.
	backoffAfterRateLimit = time.Minute
)

var typeNames = map[base.StateKind]string{
	base.StateEmail:         TypeEmail,
	base.StateEmailDelivery: TypeEmailDelivery,
	base.StateMailbox:       TypeMailbox,
	base.StateThread:        TypeThread,
	base.StateIdentity:      TypeIdentity,
	base.StateSubmission:    TypeEmailSubmission,
	base.StateVacation:      TypeVacation,
	base.StateBlob:          TypeBlob,
}

// StateChange is the object pushed to clients (RFC 8620 section 7.1).
// "changed" maps each affected account to its TypeState: the current state
// token of each type that changed since the previous notification.
type StateChange struct {
	Type    string                       `json:"@type"`
	Changed map[string]map[string]string `json:"changed"`
}

// NewStateChange wraps one account's type states in an RFC 8620 StateChange.
func NewStateChange(accountID string, types map[string]string) StateChange {
	return StateChange{
		Type:    "StateChange",
		Changed: map[string]map[string]string{accountID: types},
	}
}

// StateSource refreshes type states from the underlying stores. It is
// implemented by the JMAP handler, which shares the same state tracker.
type StateSource interface {
	RefreshState(ctx context.Context, accountID string) error
}

// StateReader reports the current type-state tokens for an account. It is
// implemented by the JMAP handler, which delegates to the shared tracker.
type StateReader interface {
	States(ctx context.Context, accountID string) (map[base.StateKind]string, error)
}

// HubStore is the durable storage the hub needs: subscriptions plus the
// revision-aware state store used for cold-start-safe delivery state. The full
// storage.Storage satisfies it, so production wiring passes one shared store.
type HubStore interface {
	storage.PushSubscriptionStore
	storage.JMAPStateStore
}

type subscriber struct {
	id         uint64
	accountID  string
	credential string
	types      map[string]bool
	allTypes   bool
	events     chan StateChange
	done       chan struct{}
}

// Hub publishes type-state changes to EventSource subscribers and webhook
// subscriptions. All mutable fields are guarded by mu; network calls are made
// outside the lock.
type Hub struct {
	reader  StateSource
	states  StateReader
	store   storage.PushSubscriptionStore
	durable durableState
	client  *http.Client
	// validateURL is a seam for tests; production uses the same SSRF checks
	// applied when the subscription was created.
	validateURL  func(ctx context.Context, value string) error
	sessionAlive func(ctx context.Context, sid string) bool
	pingInterval func(int) time.Duration

	pollInterval  time.Duration
	coalesce      time.Duration
	maxConnection int

	// subs and credentials are process-local caches. subs holds live SSE
	// channels (which cannot be shared across instances); credentials is
	// reloaded from the durable credential index on every Refresh. Delivery
	// state (last, backoff, pending) is durable too.
	mu          sync.Mutex
	nextID      uint64
	closed      bool
	subs        map[uint64]*subscriber
	credentials map[string]map[string]struct{}
	last        map[string]map[base.StateKind]string
	deferred    map[string]map[base.StateKind]string
	published   map[string]time.Time
}

// NewHub creates a push hub. reader may be nil in tests that only exercise
// state delivery; production wiring always supplies a JMAP handler.
func NewHub(reader StateSource, tracker StateReader, store HubStore) *Hub {
	var durable durableState
	if store != nil {
		durable.store = store
	}
	return &Hub{
		reader:        reader,
		states:        tracker,
		store:         store,
		durable:       durable,
		client:        &http.Client{Timeout: 10 * time.Second},
		validateURL:   pushsubscription.ValidateURL,
		pingInterval:  func(seconds int) time.Duration { return time.Duration(seconds) * time.Second },
		pollInterval:  DefaultPollInterval,
		coalesce:      DefaultCoalesceWindow,
		maxConnection: DefaultMaxConnections,
		subs:          make(map[uint64]*subscriber),
		credentials:   make(map[string]map[string]struct{}),
		last:          make(map[string]map[base.StateKind]string),
		deferred:      make(map[string]map[base.StateKind]string),
		published:     make(map[string]time.Time),
	}
}

// SetHTTPClient replaces the client used for webhook POSTs. Tests use it to
// exercise delivery against a local TLS server while production keeps the
// default timeout and redirect policy.
func (h *Hub) SetHTTPClient(client *http.Client) {
	if client != nil {
		h.client = client
	}
}

// Remember records which credentials may receive changes for an account.
// Webhook subscriptions are scoped to credentials rather than accounts, so
// this index lets a JMAP request deliver the changes that request produced.
// The index is durable: an instance that did not serve the original request
// loads it on Refresh and still finds the subscription in shared storage.
func (h *Hub) Remember(ctx context.Context, accountID string) {
	if accountID == "" {
		return
	}
	credential := pushsubscription.CredentialID(ctx)
	if err := h.durable.rememberCredential(ctx, accountID, credential); err != nil {
		slog.Warn("JMAP push: could not persist credential association", "account", accountID, "err", err)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.credentials[accountID] == nil {
		h.credentials[accountID] = make(map[string]struct{})
	}
	h.credentials[accountID][credential] = struct{}{}
}

// loadDurableCredentials merges the durable credential index into the local
// cache. The cache is never authoritative: every Refresh reloads the index.
func (h *Hub) loadDurableCredentials(ctx context.Context, accountID string) error {
	credentials, err := h.durable.loadCredentials(ctx, accountID)
	if err != nil {
		return err
	}
	if len(credentials) == 0 {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.credentials[accountID] == nil {
		h.credentials[accountID] = make(map[string]struct{})
	}
	for _, credential := range credentials {
		h.credentials[accountID][credential] = struct{}{}
	}
	return nil
}

// loadDurableLast seeds the in-memory baseline from the durable store. It is
// deliberately reloaded on every Refresh so a baseline published by another
// instance is never re-emitted as a duplicate by this one.
func (h *Hub) loadDurableLast(ctx context.Context, accountID string) error {
	tokens, err := h.durable.loadLast(ctx, accountID)
	if err != nil {
		return err
	}
	if tokens == nil {
		return nil
	}
	h.mu.Lock()
	h.last[accountID] = decodeTokens(tokens)
	h.mu.Unlock()
	return nil
}

func (h *Hub) persistDurableLast(ctx context.Context, accountID string, states map[base.StateKind]string) {
	if err := h.durable.saveLast(ctx, accountID, encodeTokens(states)); err != nil {
		slog.Warn("JMAP push: could not persist baseline", "account", accountID, "err", err)
	}
}

// Refresh computes the type states that have changed for an account and
// publishes them. It is safe to call on every JMAP request and on a timer.
func (h *Hub) Refresh(ctx context.Context, accountID string) error {
	if accountID == "" || h.states == nil {
		return nil
	}
	if h.reader != nil {
		if err := h.reader.RefreshState(ctx, accountID); err != nil {
			slog.Warn("JMAP push: state refresh failed", "account", accountID, "err", err)
		}
	}
	states, err := h.states.States(ctx, accountID)
	if err != nil {
		return err
	}
	if err := h.loadDurableLast(ctx, accountID); err != nil {
		return err
	}
	if err := h.loadDurableCredentials(ctx, accountID); err != nil {
		return err
	}
	// Whatever this refresh decides, the resulting baseline becomes the
	// durable one so a cold start never re-publishes an already-delivered
	// change and never loses the baseline for a change made while down.
	defer h.persistDurableLast(ctx, accountID, states)

	// A session-less access token scopes subscriptions by subject, which is
	// the account id itself. Record that association durably so changes
	// discovered from state alone (notably inbound delivery) reach webhooks.
	subjectCredential := pushsubscription.CredentialFromSubject(accountID)
	if err := h.durable.rememberCredential(ctx, accountID, subjectCredential); err != nil {
		slog.Warn("JMAP push: could not persist subject credential", "account", accountID, "err", err)
	}
	h.mu.Lock()
	if h.credentials[accountID] == nil {
		h.credentials[accountID] = make(map[string]struct{})
	}
	h.credentials[accountID][subjectCredential] = struct{}{}
	previous, seen := h.last[accountID]
	if !seen {
		h.last[accountID] = states
		// Start the rate-limit window at the baseline so a burst immediately
		// after connecting is coalesced instead of sent as several events.
		h.published[accountID] = time.Now()
		h.mu.Unlock()
		return nil
	}
	changed := make(map[base.StateKind]string)
	for kind, token := range states {
		if previous[kind] != token {
			changed[kind] = token
		}
	}
	for kind, token := range h.deferred[accountID] {
		changed[kind] = token
	}
	h.last[accountID] = states

	now := time.Now()
	if last, ok := h.published[accountID]; ok && now.Sub(last) < h.coalesce {
		if h.deferred[accountID] == nil {
			h.deferred[accountID] = make(map[base.StateKind]string)
		}
		for kind, token := range changed {
			h.deferred[accountID][kind] = token
		}
		h.mu.Unlock()
		return nil
	}
	delete(h.deferred, accountID)
	h.published[accountID] = now

	types := make(map[string]string, len(changed))
	for kind, token := range changed {
		if name, ok := typeNames[kind]; ok {
			types[name] = token
		}
	}
	if len(types) == 0 {
		h.mu.Unlock()
		return nil
	}
	type delivery struct {
		sub   *subscriber
		types map[string]string
	}
	deliveries := make([]delivery, 0, len(h.subs))
	for _, sub := range h.subs {
		if sub.accountID != accountID {
			continue
		}
		selected := subscriberTypes(sub, types)
		if len(selected) > 0 {
			deliveries = append(deliveries, delivery{sub: sub, types: selected})
		}
	}
	for _, item := range deliveries {
		event := NewStateChange(accountID, item.types)
		select {
		case item.sub.events <- event:
		default:
			mergeSubscriberEvent(item.sub.events, event)
		}
	}
	credentials := make([]string, 0, len(h.credentials[accountID]))
	for credential := range h.credentials[accountID] {
		credentials = append(credentials, credential)
	}
	h.mu.Unlock()

	h.deliverWebhooks(ctx, credentials, accountID, types)
	return nil
}

// subscriberTypes narrows a change to the types this stream subscribed to.
func subscriberTypes(sub *subscriber, types map[string]string) map[string]string {
	if sub.allTypes {
		return types
	}
	selected := make(map[string]string, len(sub.types))
	for name, token := range types {
		if sub.types[name] {
			selected[name] = token
		}
	}
	return selected
}

// mergeSubscriberEvent replaces the pending event with the union of both
// events so no type state is lost when updates are coalesced.
func mergeSubscriberEvent(events chan StateChange, next StateChange) {
	for {
		select {
		case previous := <-events:
			for account, states := range next.Changed {
				if previous.Changed == nil {
					previous.Changed = make(map[string]map[string]string)
				}
				if previous.Changed[account] == nil {
					previous.Changed[account] = make(map[string]string)
				}
				for name, token := range states {
					previous.Changed[account][name] = token
				}
			}
			next = previous
		default:
			events <- next
			return
		}
	}
}

func (h *Hub) deliverWebhooks(ctx context.Context, credentials []string, accountID string, types map[string]string) {
	for _, credential := range credentials {
		h.deliverToCredential(ctx, credential, accountID, types)
	}
}

func (h *Hub) deliverToCredential(ctx context.Context, credential, accountID string, types map[string]string) {
	if h.store == nil {
		return
	}
	inputTypes := types
	effective := make(map[string]string, len(inputTypes))
	for name, token := range inputTypes {
		effective[name] = token
	}
	backedOff := false
	err := h.durable.updateWebhook(ctx, credential, func(record *durableWebhook) (bool, error) {
		// A CAS conflict re-runs this closure, so derive everything from
		// inputTypes rather than from a previously mutated map.
		backedOff = false
		effective = make(map[string]string, len(inputTypes))
		for name, token := range inputTypes {
			effective[name] = token
		}
		if record.BackoffUntil.After(time.Now()) {
			mergePending(record, accountID, inputTypes)
			backedOff = true
			return true, nil
		}
		if pending := record.Pending[accountID]; len(pending) > 0 {
			for name, token := range pending {
				effective[name] = token
			}
			delete(record.Pending, accountID)
			return true, nil
		}
		return false, nil
	})
	if err != nil {
		slog.Warn("JMAP push: could not update webhook state", "credential", credential, "err", err)
		return
	}
	if backedOff {
		return
	}

	subs, err := h.store.LoadPushSubscriptions(ctx, credential)
	if err != nil {
		slog.Warn("JMAP push: could not load subscriptions", "err", err)
		return
	}
	for _, sub := range subs {
		selected := subscriptionTypes(sub, accountID, effective)
		if len(selected) == 0 {
			continue
		}
		if err := h.postStateChange(ctx, sub.URL, NewStateChange(accountID, selected)); err != nil {
			slog.Warn("JMAP push: webhook delivery failed", "subscription", sub.ID, "err", err)
			var rateLimited *rateLimitError
			if errors.As(err, &rateLimited) {
				// Backoff and pending changes are durable so a cold start
				// neither hammers the endpoint nor drops the queued change.
				if err := h.durable.updateWebhook(ctx, credential, func(record *durableWebhook) (bool, error) {
					record.BackoffUntil = time.Now().Add(backoffAfterRateLimit)
					mergePending(record, accountID, selected)
					return true, nil
				}); err != nil {
					slog.Warn("JMAP push: could not persist webhook backoff", "credential", credential, "err", err)
				}
				break
			}
		}
	}
}

func mergePending(record *durableWebhook, accountID string, types map[string]string) {
	if record.Pending == nil {
		record.Pending = make(map[string]map[string]string)
	}
	if record.Pending[accountID] == nil {
		record.Pending[accountID] = make(map[string]string)
	}
	for name, token := range types {
		record.Pending[accountID][name] = token
	}
}

func subscriptionTypes(sub storage.PushSubscription, accountID string, types map[string]string) map[string]string {
	if !sub.Verified || sub.URL == "" {
		return nil
	}
	if sub.Expires != "" {
		expires, err := time.Parse(time.RFC3339, sub.Expires)
		if err != nil || !time.Now().Before(expires) {
			return nil
		}
	}
	selected := make(map[string]string, len(types))
	for name, token := range types {
		if len(sub.Types) == 0 {
			selected[name] = token
			continue
		}
		for _, wanted := range sub.Types {
			if wanted == name {
				selected[name] = token
				break
			}
		}
	}
	return selected
}

type rateLimitError struct{ status int }

func (e *rateLimitError) Error() string { return fmt.Sprintf("push endpoint returned %d", e.status) }

func (h *Hub) postStateChange(ctx context.Context, target string, change StateChange) error {
	// Subscriptions were validated when created; revalidate on delivery so a
	// DNS rebinding or a stale record cannot turn push into an SSRF vector.
	if h.validateURL != nil {
		if err := h.validateURL(ctx, target); err != nil {
			return err
		}
	}
	body, err := json.Marshal(change)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("TTL", pushTTL)
	client := *h.client
	client.CheckRedirect = func(redirect *http.Request, via []*http.Request) error {
		if len(via) >= 3 {
			return errors.New("too many push redirects")
		}
		if h.validateURL != nil {
			return h.validateURL(context.Background(), redirect.URL.String())
		}
		return nil
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusTooManyRequests {
		return &rateLimitError{status: resp.StatusCode}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("push endpoint returned %s", resp.Status)
	}
	return nil
}

// encodeEventID encodes the complete visible type state so a client's
// Last-Event-ID can be compared on the next connection (RFC 8620 7.3).
func encodeEventID(states map[base.StateKind]string) string {
	visible := make(map[string]string, len(states))
	for kind, token := range states {
		if name, ok := typeNames[kind]; ok {
			visible[name] = token
		}
	}
	raw, err := json.Marshal(visible)
	if err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

// decodeEventID reverses encodeEventID; malformed headers are ignored.
func decodeEventID(value string) map[string]string {
	if value == "" {
		return nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return nil
	}
	var decoded map[string]string
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil
	}
	return decoded
}

// subscriberCount is used by tests to verify connection cleanup.
func (h *Hub) subscriberCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.subs)
}

// SetSessionChecker wires session-revocation detection into live streams.
func (h *Hub) SetSessionChecker(checker func(ctx context.Context, sid string) bool) {
	h.sessionAlive = checker
}

// subscribe registers a stream for one account. The returned subscriber is
// owned by the caller until unsubscribe is called.
func (h *Hub) subscribe(ctx context.Context, accountID string, types []string, allTypes bool) (*subscriber, error) {
	credential := pushsubscription.CredentialID(ctx)
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return nil, errors.New("push hub is shut down")
	}
	active := 0
	for _, sub := range h.subs {
		if sub.credential == credential {
			active++
		}
	}
	if active >= h.maxConnection {
		return nil, fmt.Errorf("too many event source connections (max %d)", h.maxConnection)
	}
	h.nextID++
	sub := &subscriber{
		id:         h.nextID,
		accountID:  accountID,
		credential: credential,
		types:      make(map[string]bool, len(types)),
		allTypes:   allTypes,
		events:     make(chan StateChange, 1),
		done:       make(chan struct{}),
	}
	for _, name := range types {
		sub.types[name] = true
	}
	h.subs[sub.id] = sub
	return sub, nil
}

func (h *Hub) unsubscribe(sub *subscriber) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if current, ok := h.subs[sub.id]; ok && current == sub {
		delete(h.subs, sub.id)
		close(sub.done)
	}
}

// Snapshot refreshes storage-backed states and returns every type token for
// the account. It is used on connection to resolve a Last-Event-ID header.
func (h *Hub) Snapshot(ctx context.Context, accountID string) (map[base.StateKind]string, error) {
	if h.reader != nil {
		if err := h.reader.RefreshState(ctx, accountID); err != nil {
			return nil, err
		}
	}
	if h.states == nil {
		return nil, nil
	}
	return h.states.States(ctx, accountID)
}

// EventID encodes the current visible state as an SSE id.
func (h *Hub) EventID(ctx context.Context, accountID string) (string, error) {
	if h.states == nil {
		return "", nil
	}
	states, err := h.states.States(ctx, accountID)
	if err != nil {
		return "", err
	}
	return encodeEventID(states), nil
}

// Start periodically refreshes accounts that have been seen, so webhook-only
// clients and idle streams still receive changes made by another process.
func (h *Hub) Start(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(h.pollInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				h.mu.Lock()
				if h.closed {
					h.mu.Unlock()
					return
				}
				accounts := make([]string, 0, len(h.last))
				for accountID := range h.last {
					accounts = append(accounts, accountID)
				}
				for _, sub := range h.subs {
					seen := false
					for _, accountID := range accounts {
						if accountID == sub.accountID {
							seen = true
							break
						}
					}
					if !seen {
						accounts = append(accounts, sub.accountID)
					}
				}
				h.mu.Unlock()
				for _, accountID := range accounts {
					if err := h.Refresh(ctx, accountID); err != nil {
						slog.Warn("JMAP push: periodic refresh failed", "account", accountID, "err", err)
					}
				}
			}
		}
	}()
}

// CloseCredential ends EventSource streams belonging to one credential. It is
// called when the credentials are revoked (logout).
func (h *Hub) CloseCredential(ctx context.Context) {
	credential := pushsubscription.CredentialID(ctx)
	h.mu.Lock()
	defer h.mu.Unlock()
	for id, sub := range h.subs {
		if sub.credential == credential {
			close(sub.done)
			delete(h.subs, id)
		}
	}
	for account, credentials := range h.credentials {
		delete(credentials, credential)
		if len(credentials) == 0 {
			delete(h.credentials, account)
		}
	}
}

// Shutdown ends every stream. It is used by tests and server shutdown.
func (h *Hub) Shutdown() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closed = true
	for id, sub := range h.subs {
		close(sub.done)
		delete(h.subs, id)
	}
}
