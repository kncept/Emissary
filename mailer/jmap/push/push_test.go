package push

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kncept/Emissary/mailer/auth"
	"github.com/kncept/Emissary/mailer/components/storage"
	"github.com/kncept/Emissary/mailer/jmap/internal/base"
	"github.com/kncept/Emissary/mailer/jmap/pushsubscription"
)

type memoryState struct {
	mu     sync.Mutex
	values map[base.StateKind]string
}

func newMemoryState() *memoryState {
	values := make(map[base.StateKind]string)
	for _, kind := range base.PushStateKinds {
		values[kind] = string(kind) + "-0"
	}
	return &memoryState{values: values}
}

func (m *memoryState) RefreshState(context.Context, string) error { return nil }

func (m *memoryState) States(context.Context, string) (map[base.StateKind]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[base.StateKind]string, len(m.values))
	for kind, token := range m.values {
		out[kind] = token
	}
	return out, nil
}

func (m *memoryState) set(kind base.StateKind, token string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.values[kind] = token
}

func pushContext(sid string) context.Context {
	return auth.ContextWithClaims(context.Background(), &auth.Claims{
		Subject: "alice@example.com",
		SID:     sid,
	})
}

func newTestHub(source *memoryState, store storage.PushSubscriptionStore) *Hub {
	hub := NewHub(source, source, store)
	hub.pollInterval = 10 * time.Millisecond
	hub.coalesce = 0
	hub.pingInterval = func(seconds int) time.Duration { return time.Duration(seconds) * time.Millisecond }
	return hub
}

func waitFor(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("timed out waiting for condition")
}

func TestEventSourceArgumentParsing(t *testing.T) {
	types, all, err := parseEventTypes("Email, EmailDelivery")
	if err != nil || all || len(types) != 2 || types[0] != "Email" || types[1] != "EmailDelivery" {
		t.Fatalf("parseEventTypes = %v, %v, %v", types, all, err)
	}
	if _, all, err := parseEventTypes("*"); err != nil || !all {
		t.Fatalf("parseEventTypes(*) = %v, %v", all, err)
	}
	if got, err := parseCloseAfter(""); err != nil || got != closeAfterNo {
		t.Fatalf("parseCloseAfter(default) = %q, %v", got, err)
	}
	if _, err := parseCloseAfter("always"); err == nil {
		t.Fatal("parseCloseAfter accepted an invalid value")
	}
	for input, want := range map[string]int{
		"":     defaultPing,
		"0":    0,
		"1":    minPingSeconds,
		"60":   60,
		"1000": maxPingSeconds,
	} {
		got, err := parsePing(input)
		if err != nil || got != want {
			t.Errorf("parsePing(%q) = %d, %v; want %d", input, got, err, want)
		}
	}
	for _, input := range []string{"-1", "abc"} {
		if _, err := parsePing(input); err == nil {
			t.Errorf("parsePing(%q) unexpectedly succeeded", input)
		}
	}
}

func TestEventSourceSendsMissedChangesOnReconnect(t *testing.T) {
	state := newMemoryState()
	hub := newTestHub(state, nil)
	ctx := pushContext("sid-1")
	before, err := hub.Snapshot(ctx, "alice@example.com")
	if err != nil {
		t.Fatal(err)
	}
	state.set(base.StateEmail, "email-1")

	req := httptest.NewRequestWithContext(ctx, http.MethodGet,
		"/eventsource?types=Email&closeafter=state&ping=0", nil)
	req.Header.Set("Last-Event-ID", encodeEventID(before))
	w := httptest.NewRecorder()
	hub.ServeHTTP(w, req)

	body := w.Body.String()
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, body)
	}
	if !strings.Contains(body, "event: state\n") || !strings.Contains(body, "id: ") {
		t.Fatalf("missing state event or id: %s", body)
	}
	if !strings.Contains(body, `"Email":"email-1"`) {
		t.Fatalf("catch-up event missing Email token: %s", body)
	}
	if strings.Contains(body, `"Mailbox"`) || strings.Contains(body, "event: ping") {
		t.Fatalf("catch-up event leaked other types or a ping: %s", body)
	}
}

func TestEventSourceStreamsChangesAndClosesAfterState(t *testing.T) {
	state := newMemoryState()
	hub := newTestHub(state, nil)
	ctx := pushContext("sid-2")
	if err := hub.Refresh(ctx, "alice@example.com"); err != nil {
		t.Fatal(err)
	}

	type result struct {
		body string
		code int
	}
	results := make(chan result, 1)
	go func() {
		req := httptest.NewRequestWithContext(ctx, http.MethodGet,
			"/eventsource?types=Email,EmailDelivery&closeafter=state&ping=0", nil)
		w := httptest.NewRecorder()
		hub.ServeHTTP(w, req)
		results <- result{body: w.Body.String(), code: w.Code}
	}()
	waitFor(t, func() bool { return hub.subscriberCount() == 1 })

	state.set(base.StateEmail, "email-7")
	state.set(base.StateEmailDelivery, "email-delivery-3")
	state.set(base.StateMailbox, "mailbox-9")
	if err := hub.Refresh(ctx, "alice@example.com"); err != nil {
		t.Fatal(err)
	}

	select {
	case got := <-results:
		if got.code != http.StatusOK {
			t.Fatalf("status = %d", got.code)
		}
		if !strings.Contains(got.body, `"Email":"email-7"`) ||
			!strings.Contains(got.body, `"EmailDelivery":"email-delivery-3"`) {
			t.Fatalf("event missing subscribed types: %s", got.body)
		}
		if strings.Contains(got.body, `"Mailbox"`) {
			t.Fatalf("event includes unsubscribed type: %s", got.body)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("event source did not receive the state event")
	}
	if hub.subscriberCount() != 0 {
		t.Fatal("subscriber was not removed after the stream ended")
	}
}

func TestEventSourcePingHasNoEventID(t *testing.T) {
	state := newMemoryState()
	hub := newTestHub(state, nil)
	ctx, cancel := context.WithTimeout(pushContext("sid-3"), 2*time.Second)
	defer cancel()

	req := httptest.NewRequestWithContext(ctx, http.MethodGet,
		"/eventsource?types=Email&closeafter=no&ping=10", nil)
	w := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		hub.ServeHTTP(w, req)
		close(done)
	}()
	waitFor(t, func() bool { return strings.Contains(w.Body.String(), "event: ping") })
	if body := w.Body.String(); !strings.Contains(body, `{"interval":10}`) {
		t.Fatalf("ping payload missing interval: %s", body)
	}
	if strings.Contains(w.Body.String(), "event: state") {
		t.Fatalf("unexpected state event: %s", w.Body.String())
	}
	// A ping must not update the client's last event id.
	lines := strings.Split(w.Body.String(), "\n")
	for _, line := range lines {
		if strings.HasPrefix(line, "id: ") {
			t.Fatalf("ping set an event id: %s", w.Body.String())
		}
	}
	cancel()
	<-done
}

func TestEventSourceRejectsInvalidArguments(t *testing.T) {
	hub := newTestHub(newMemoryState(), nil)
	ctx := pushContext("sid-4")
	for _, target := range []string{
		"/eventsource?closeafter=maybe",
		"/eventsource?ping=-5",
		"/eventsource?ping=abc",
	} {
		req := httptest.NewRequestWithContext(ctx, http.MethodGet, target, nil)
		w := httptest.NewRecorder()
		hub.ServeHTTP(w, req)
		if w.Code != http.StatusBadRequest {
			t.Errorf("GET %s status = %d, want 400", target, w.Code)
		}
	}
}

func TestVerifiedWebhookReceivesFilteredStateChange(t *testing.T) {
	type received struct {
		body string
		ttl  string
	}
	posts := make(chan received, 4)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		posts <- received{body: string(body), ttl: r.Header.Get("TTL")}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	store := storage.NewMemoryStorage()
	state := newMemoryState()
	hub := newTestHub(state, store)
	hub.SetHTTPClient(server.Client())
	hub.validateURL = func(context.Context, string) error { return nil }
	ctx := pushContext("sid-webhook")
	credential := pushsubscription.CredentialID(ctx)
	if err := store.SavePushSubscriptions(ctx, credential, []storage.PushSubscription{{
		ID:       "sub-1",
		URL:      server.URL,
		Verified: true,
		Types:    []string{TypeEmail},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := hub.Refresh(ctx, "alice@example.com"); err != nil {
		t.Fatal(err)
	}
	hub.Remember(ctx, "alice@example.com")

	state.set(base.StateEmail, "email-2")
	state.set(base.StateMailbox, "mailbox-2")
	if err := hub.Refresh(ctx, "alice@example.com"); err != nil {
		t.Fatal(err)
	}

	select {
	case got := <-posts:
		if got.ttl != "60" {
			t.Errorf("TTL = %q, want 60", got.ttl)
		}
		var change StateChange
		if err := json.Unmarshal([]byte(got.body), &change); err != nil {
			t.Fatalf("decode webhook body: %v (%s)", err, got.body)
		}
		if change.Type != "StateChange" || change.Changed["alice@example.com"][TypeEmail] != "email-2" {
			t.Fatalf("unexpected StateChange: %+v", change)
		}
		if _, ok := change.Changed["alice@example.com"][TypeMailbox]; ok {
			t.Fatalf("webhook included unsubscribed type: %s", got.body)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("webhook did not receive a StateChange")
	}

	// An unverified subscription must never receive a notification.
	if err := store.SavePushSubscriptions(ctx, credential, []storage.PushSubscription{{
		ID: "sub-2", URL: server.URL, Verified: false,
	}}); err != nil {
		t.Fatal(err)
	}
	state.set(base.StateEmail, "email-3")
	if err := hub.Refresh(ctx, "alice@example.com"); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-posts:
		t.Fatalf("unverified subscription received a push: %s", got.body)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestRefreshCoalescesRapidChanges(t *testing.T) {
	posts := make(chan StateChange, 4)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var change StateChange
		_ = json.NewDecoder(r.Body).Decode(&change)
		posts <- change
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	store := storage.NewMemoryStorage()
	state := newMemoryState()
	hub := newTestHub(state, store)
	hub.coalesce = time.Hour
	hub.SetHTTPClient(server.Client())
	hub.validateURL = func(context.Context, string) error { return nil }
	ctx := pushContext("sid-coalesce")
	if err := store.SavePushSubscriptions(ctx, pushsubscription.CredentialID(ctx), []storage.PushSubscription{{
		ID: "sub-1", URL: server.URL, Verified: true,
	}}); err != nil {
		t.Fatal(err)
	}
	if err := hub.Refresh(ctx, "alice@example.com"); err != nil {
		t.Fatal(err)
	}
	hub.Remember(ctx, "alice@example.com")

	state.set(base.StateEmail, "email-a")
	if err := hub.Refresh(ctx, "alice@example.com"); err != nil {
		t.Fatal(err)
	}
	state.set(base.StateMailbox, "mailbox-b")
	if err := hub.Refresh(ctx, "alice@example.com"); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-posts:
		t.Fatalf("rate-limited event was pushed too early: %+v", got)
	case <-time.After(50 * time.Millisecond):
	}

	hub.coalesce = 0
	state.set(base.StateThread, "thread-c")
	if err := hub.Refresh(ctx, "alice@example.com"); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-posts:
		changed := got.Changed["alice@example.com"]
		for name, token := range map[string]string{
			TypeEmail: "email-a", TypeMailbox: "mailbox-b", TypeThread: "thread-c",
		} {
			if changed[name] != token {
				t.Errorf("coalesced %s = %q, want %q (%+v)", name, changed[name], token, changed)
			}
		}
	case <-time.After(3 * time.Second):
		t.Fatal("coalesced event was never pushed")
	}
}

func TestWebhookRateLimitBackoffCoalescesPendingStates(t *testing.T) {
	var attempts int
	posts := make(chan StateChange, 4)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		var change StateChange
		_ = json.NewDecoder(r.Body).Decode(&change)
		posts <- change
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	store := storage.NewMemoryStorage()
	state := newMemoryState()
	hub := newTestHub(state, store)
	hub.SetHTTPClient(server.Client())
	hub.validateURL = func(context.Context, string) error { return nil }
	ctx := pushContext("sid-429")
	credential := pushsubscription.CredentialID(ctx)
	if err := store.SavePushSubscriptions(ctx, credential, []storage.PushSubscription{{
		ID: "sub-1", URL: server.URL, Verified: true,
	}}); err != nil {
		t.Fatal(err)
	}
	if err := hub.Refresh(ctx, "alice@example.com"); err != nil {
		t.Fatal(err)
	}
	hub.Remember(ctx, "alice@example.com")

	state.set(base.StateEmail, "email-429")
	if err := hub.Refresh(ctx, "alice@example.com"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return attempts == 1 })

	state.set(base.StateMailbox, "mailbox-429")
	if err := hub.Refresh(ctx, "alice@example.com"); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-posts:
		t.Fatalf("webhook was called during backoff: %+v", got)
	case <-time.After(50 * time.Millisecond):
	}
	if attempts != 1 {
		t.Fatalf("webhook attempts = %d, want 1", attempts)
	}

	// Advance time by moving the backoff deadline into the past.
	hub.mu.Lock()
	hub.backoff[credential] = time.Now().Add(-time.Second)
	hub.mu.Unlock()
	state.set(base.StateThread, "thread-429")
	if err := hub.Refresh(ctx, "alice@example.com"); err != nil {
		t.Fatal(err)
	}

	select {
	case got := <-posts:
		changed := got.Changed["alice@example.com"]
		for name, token := range map[string]string{
			TypeEmail: "email-429", TypeMailbox: "mailbox-429", TypeThread: "thread-429",
		} {
			if changed[name] != token {
				t.Errorf("pending %s = %q, want %q (%+v)", name, changed[name], token, changed)
			}
		}
	case <-time.After(3 * time.Second):
		t.Fatal("webhook was not retried after backoff")
	}
}
