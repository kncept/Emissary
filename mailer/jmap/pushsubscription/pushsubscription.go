// Package pushsubscription implements the account-less PushSubscription
// methods from RFC 8620 section 7.2. It does not implement EventSource or
// StateChange delivery; it only manages subscriptions and their mandatory
// verification handshake.
package pushsubscription

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"sync"
	"time"

	"github.com/kncept/Emissary/mailer/auth"
	"github.com/kncept/Emissary/mailer/components/storage"
	"github.com/kncept/Emissary/mailer/jmap/internal/base"
)

const (
	MethodGet = "PushSubscription/get"
	MethodSet = "PushSubscription/set"

	// verificationTTL is the RFC 8030 Time-To-Live sent with every push. The
	// value is in seconds and gives clients enough time to receive a
	// verification code while keeping stale messages from lingering.
	verificationTTL = "60"
)

// Methods returns the method names implemented by this package.
func Methods() []string { return []string{MethodGet, MethodSet} }

// MethodHandler is the contract the dispatcher expects from PushSubscription
// handlers.
type MethodHandler interface {
	PushSubscriptionGet(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error)
	PushSubscriptionSet(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error)
}

// Handler implements the PushSubscription methods.
type Handler struct {
	deps *base.Deps

	// allowPrivateURLs is only set by tests that use a local TLS server. The
	// production handler always enforces RFC 8620 section 8.6's externally
	// resolvable URL requirement.
	allowPrivateURLs bool

	// mu guards createTimes and setLocks.
	mu          sync.Mutex
	createTimes map[string][]time.Time
	setLocks    map[string]*sync.Mutex
}

// New creates a PushSubscription method handler.
func New(deps *base.Deps) *Handler {
	return &Handler{
		deps:        deps,
		createTimes: make(map[string][]time.Time),
		setLocks:    make(map[string]*sync.Mutex),
	}
}

// lockCredential serializes read-modify-write cycles for one credential so
// concurrent /get and /set calls cannot overwrite each other's changes.
func (h *Handler) lockCredential(credentialID string) func() {
	h.mu.Lock()
	lock, ok := h.setLocks[credentialID]
	if !ok {
		lock = &sync.Mutex{}
		h.setLocks[credentialID] = lock
	}
	h.mu.Unlock()
	lock.Lock()
	return lock.Unlock
}

var _ MethodHandler = (*Handler)(nil)

// view is the JMAP representation returned to clients. URL and keys are never
// included: they can contain device-private data and RFC 8620 forbids
// returning their values. The any fields allow explicit JSON nulls.
type view struct {
	ID               string `json:"id"`
	DeviceClientID   string `json:"deviceClientId"`
	VerificationCode any    `json:"verificationCode"`
	Expires          any    `json:"expires"`
	Types            any    `json:"types"`
}

func toView(sub storage.PushSubscription) view {
	v := view{ID: sub.ID, DeviceClientID: sub.DeviceClientID}
	if sub.VerificationCode != "" {
		v.VerificationCode = sub.VerificationCode
	}
	if sub.Expires != "" {
		v.Expires = sub.Expires
	}
	if sub.Types != nil {
		v.Types = sub.Types
	}
	return v
}

type setError struct {
	Type        string `json:"type"`
	Description string `json:"description"`
}

func setErrorOf(typ, description string) setError {
	return setError{Type: typ, Description: description}
}

// CredentialID derives a storage-safe identifier for the credentials that
// authenticated the request. A session id scopes subscriptions to one login
// session; a session-less token falls back to its subject. The hash avoids
// putting raw session ids or email addresses into object keys.
func CredentialID(ctx context.Context) string {
	claims := auth.ClaimsFromContext(ctx)
	seed := "anonymous"
	if claims != nil {
		switch {
		case claims.SID != "":
			seed = "sid:" + claims.SID
		case claims.Subject != "":
			seed = "sub:" + claims.Subject
		}
	}
	sum := sha256.Sum256([]byte(seed))
	return base64.RawURLEncoding.EncodeToString(sum[:16])
}

// DestroyForCredential removes every subscription tied to the current
// credentials. It is called when those credentials are revoked (logout), as
// required by RFC 8620 section 7.2.
func DestroyForCredential(ctx context.Context, store storage.PushSubscriptionStore) error {
	if store == nil {
		return nil
	}
	return store.DeletePushSubscriptions(ctx, CredentialID(ctx))
}

func randomToken(size int) (string, error) {
	buf := make([]byte, size)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// erase overwrites the private fields of a destroyed or expired subscription
// before it is dropped from the persisted slice.
func erase(sub *storage.PushSubscription) {
	sub.URL = ""
	sub.VerificationCode = ""
	if sub.Keys != nil {
		sub.Keys.P256DH = ""
		sub.Keys.Auth = ""
		sub.Keys = nil
	}
	sub.Types = nil
}

func expired(sub storage.PushSubscription, now time.Time) bool {
	if sub.Expires == "" {
		return false
	}
	expires, err := time.Parse(time.RFC3339, sub.Expires)
	return err != nil || !now.Before(expires)
}

// load removes expired subscriptions and persists the pruned set. A server
// MUST NOT make further requests to an expired URL, and may destroy the
// subscription outright.
func (h *Handler) load(ctx context.Context, credentialID string) ([]storage.PushSubscription, error) {
	// RFC 8620 section 7.2 requires subscriptions to be destroyed when their
	// credentials expire. With session-bound credentials the session store is
	// the authority: once the session is gone, drop every subscription scoped
	// to it even if no explicit logout happened.
	if claims := auth.ClaimsFromContext(ctx); claims != nil && claims.SID != "" &&
		h.deps.CheckSession != nil && !h.deps.CheckSession(ctx, claims.SID) {
		if err := h.deps.Storage.DeletePushSubscriptions(ctx, credentialID); err != nil {
			return nil, err
		}
		return []storage.PushSubscription{}, nil
	}

	subs, err := h.deps.Storage.LoadPushSubscriptions(ctx, credentialID)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	kept := make([]storage.PushSubscription, 0, len(subs))
	changed := false
	for i := range subs {
		if expired(subs[i], now) {
			// Erase the stored element itself, not a range-copy, so private
			// URL and key bytes do not linger in the backing slice.
			erase(&subs[i])
			changed = true
			continue
		}
		kept = append(kept, subs[i])
	}
	if changed {
		if err := h.deps.Storage.SavePushSubscriptions(ctx, credentialID, kept); err != nil {
			return nil, err
		}
	}
	return kept, nil
}

func validateArguments(args json.RawMessage, allowed map[string]bool) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(args, &raw); err != nil {
		return base.Errorf("invalidArguments", "arguments must be an object")
	}
	for name := range raw {
		if !allowed[name] {
			return base.Errorf("invalidArguments", "unknown argument %q", name)
		}
	}
	return nil
}
