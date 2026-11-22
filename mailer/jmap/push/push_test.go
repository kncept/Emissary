package push

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
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

func newTestHub(source *memoryState, store HubStore) *Hub {
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

// TestWebhookRejectsUnsafeRedirectRFC8620_8_6 proves delivery revalidates
// redirect targets with the same SSRF policy as the original URL. A webhook
// that bounces to a blocked host must not reach it.
func TestWebhookRejectsUnsafeRedirectRFC8620_8_6(t *testing.T) {
	var targetHits, redirectHits int32
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&targetHits, 1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer target.Close()
	redirector := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&redirectHits, 1)
		http.Redirect(w, r, target.URL+"/stolen", http.StatusFound)
	}))
	defer redirector.Close()

	store := storage.NewMemoryStorage()
	state := newMemoryState()
	hub := newTestHub(state, store)
	hub.SetHTTPClient(redirector.Client())
	hub.validateURL = func(_ context.Context, value string) error {
		if strings.HasPrefix(value, target.URL) {
			return errors.New("blocked redirect target")
		}
		return nil
	}
	ctx := pushContext("sid-redirect")
	credential := pushsubscription.CredentialID(ctx)
	if err := store.SavePushSubscriptions(ctx, credential, []storage.PushSubscription{{
		ID: "sub-redirect", URL: redirector.URL, Verified: true, Types: []string{TypeEmail},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := hub.Refresh(ctx, "alice@example.com"); err != nil {
		t.Fatal(err)
	}
	hub.Remember(ctx, "alice@example.com")

	state.set(base.StateEmail, "email-redirect")
	if err := hub.Refresh(ctx, "alice@example.com"); err != nil {
		t.Fatal(err)
	}
	if got := atomic.LoadInt32(&redirectHits); got != 1 {
		t.Fatalf("webhook POSTs to redirector = %d, want 1", got)
	}
	if got := atomic.LoadInt32(&targetHits); got != 0 {
		t.Fatalf("unsafe redirect was followed %d times, want 0", got)
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

	// Advance time by moving the durable backoff deadline into the past.
	if err := hub.durable.updateWebhook(ctx, credential, func(record *durableWebhook) (bool, error) {
		record.BackoffUntil = time.Now().Add(-time.Second)
		return true, nil
	}); err != nil {
		t.Fatal(err)
	}
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

// TestEventSourceStreamOutlivesRESTTimeoutRFC8620_7_3 proves the dedicated
// streaming origin can hold closeafter=no beyond the REST integration's 30 s
// ceiling, with pings that never advance the event id and a later state event
// that does.
func TestEventSourceStreamOutlivesRESTTimeoutRFC8620_7_3(t *testing.T) {
	state := newMemoryState()
	hub := NewHub(state, state, nil)
	hub.pollInterval = 10 * time.Millisecond
	hub.coalesce = 0
	// ping=10 is ten real seconds here, so three pings prove the stream was
	// still open at t=30s.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hub.ServeHTTP(w, r.WithContext(pushContext("sid-long-lived")))
	}))
	defer server.Close()

	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		server.URL+"/eventsource/stream?types=Email&closeafter=no&ping=10", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("connect to EventSource: %v", err)
	}
	defer resp.Body.Close()
	if got := resp.Header.Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("Content-Type = %q, want text/event-stream", got)
	}

	reader := bufio.NewReader(resp.Body)
	readEvent := func() (name, id, data string) {
		t.Helper()
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				t.Fatalf("read event (stream closed early): %v", err)
			}
			line = strings.TrimRight(line, "\r\n")
			switch {
			case line == "":
				if name != "" {
					return
				}
			case strings.HasPrefix(line, "event: "):
				name = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "id: "):
				id = strings.TrimPrefix(line, "id: ")
			case strings.HasPrefix(line, "data: "):
				data += strings.TrimPrefix(line, "data: ")
			}
		}
	}

	for ping := 1; ping <= 3; ping++ {
		name, id, data := readEvent()
		if name != "ping" || !strings.Contains(data, `"interval":10`) {
			t.Fatalf("event %d = %q %q, want a 10s ping", ping, name, data)
		}
		if id != "" {
			t.Fatalf("ping %d set an event id: %q", ping, id)
		}
	}
	if elapsed := time.Since(start); elapsed < 30*time.Second {
		t.Fatalf("stream only stayed open %v, want past 30s", elapsed)
	}

	state.set(base.StateEmail, "email-long-lived")
	if err := hub.Refresh(ctx, "alice@example.com"); err != nil {
		t.Fatal(err)
	}
	name, id, data := readEvent()
	if name != "state" || id == "" || !strings.Contains(data, `"Email":"email-long-lived"`) {
		t.Fatalf("state event = %q id=%q data=%q", name, id, data)
	}
}

// TestEventSourceCoalescedResyncRFC8620_7_3 checks the two reconnect cases:
// a cold start does not replay history, while Last-Event-ID replays several
// missed state tokens in one coalesced StateChange.
func TestEventSourceCoalescedResyncRFC8620_7_3(t *testing.T) {
	state := newMemoryState()
	hub := newTestHub(state, nil)
	ctx := pushContext("sid-resync")
	before, err := hub.Snapshot(ctx, "alice@example.com")
	if err != nil {
		t.Fatal(err)
	}

	for kind, token := range map[base.StateKind]string{
		base.StateEmail: "email-2", base.StateMailbox: "mailbox-2", base.StateEmailDelivery: "delivery-2",
	} {
		state.set(kind, token)
	}

	t.Run("cold start", func(t *testing.T) {
		// No Last-Event-ID means no catch-up: the stream stays open waiting
		// for future changes. Bound the context so the handler returns, then
		// verify it never replayed the states changed above.
		streamCtx, cancel := context.WithTimeout(ctx, 150*time.Millisecond)
		defer cancel()
		req := httptest.NewRequestWithContext(streamCtx, http.MethodGet,
			"/eventsource/stream?types=*&closeafter=state&ping=0", nil)
		w := httptest.NewRecorder()
		hub.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
		}
		if strings.Contains(w.Body.String(), "event: state") {
			t.Fatalf("cold start replayed history: %s", w.Body.String())
		}
	})

	t.Run("last event id replays one coalesced change", func(t *testing.T) {
		req := httptest.NewRequestWithContext(ctx, http.MethodGet,
			"/eventsource/stream?types=*&closeafter=state&ping=0", nil)
		req.Header.Set("Last-Event-ID", encodeEventID(before))
		w := httptest.NewRecorder()
		hub.ServeHTTP(w, req)
		body := w.Body.String()
		if strings.Count(body, "event: state\n") != 1 {
			t.Fatalf("want one coalesced state event, got %q", body)
		}
		for _, token := range []string{`"Email":"email-2"`, `"Mailbox":"mailbox-2"`, `"EmailDelivery":"delivery-2"`} {
			if !strings.Contains(body, token) {
				t.Errorf("resync missing %s: %s", token, body)
			}
		}
		if !strings.Contains(body, "id: ") {
			t.Errorf("resync event has no id: %s", body)
		}
	})
}
