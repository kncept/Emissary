package push

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kncept/Emissary/mailer/components/storage"
	"github.com/kncept/Emissary/mailer/jmap/internal/base"
	"github.com/kncept/Emissary/mailer/jmap/pushsubscription"
)

// webhookCapture records StateChange POSTs for the durability tests.
type webhookCapture struct {
	server *httptest.Server
	posts  chan StateChange
}

func newWebhookCapture(t *testing.T) *webhookCapture {
	t.Helper()
	capture := &webhookCapture{posts: make(chan StateChange, 8)}
	capture.server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var change StateChange
		if err := json.Unmarshal(body, &change); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		capture.posts <- change
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(capture.server.Close)
	return capture
}

func durableTestHub(t *testing.T, state *memoryState, store storage.Storage, capture *webhookCapture) *Hub {
	t.Helper()
	hub := newTestHub(state, store)
	hub.SetHTTPClient(capture.server.Client())
	hub.validateURL = func(context.Context, string) error { return nil }
	return hub
}

func saveVerifiedSubscription(t *testing.T, ctx context.Context, store storage.Storage, credential, url string, types ...string) {
	t.Helper()
	if len(types) == 0 {
		types = []string{TypeEmail}
	}
	if err := store.SavePushSubscriptions(ctx, credential, []storage.PushSubscription{{
		ID: "sub-1", URL: url, Verified: true, Types: types,
	}}); err != nil {
		t.Fatal(err)
	}
}

func expectWebhook(t *testing.T, posts <-chan StateChange, wantToken string) {
	t.Helper()
	select {
	case change := <-posts:
		if change.Type != "StateChange" || change.Changed["alice@example.com"][TypeEmail] != wantToken {
			t.Fatalf("unexpected StateChange: %+v", change)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("webhook did not receive the StateChange")
	}
}

func expectNoWebhook(t *testing.T, posts <-chan StateChange) {
	t.Helper()
	select {
	case change := <-posts:
		t.Fatalf("unexpected duplicate StateChange: %+v", change)
	case <-time.After(150 * time.Millisecond):
	}
}

// TestHubRestartDoesNotDuplicateWebhook proves the published baseline is
// durable: a fresh hub over the same store must not re-emit an already
// delivered change on its first refresh.
func TestHubRestartDoesNotDuplicateWebhook(t *testing.T) {
	store, err := storage.NewFileStorage(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	capture := newWebhookCapture(t)
	state := newMemoryState()
	ctx := pushContext("sid-restart")
	account := "alice@example.com"
	saveVerifiedSubscription(t, ctx, store, pushsubscription.CredentialID(ctx), capture.server.URL)

	first := durableTestHub(t, state, store, capture)
	if err := first.Refresh(ctx, account); err != nil {
		t.Fatal(err)
	}
	first.Remember(ctx, account)
	state.set(base.StateEmail, "email-2")
	if err := first.Refresh(ctx, account); err != nil {
		t.Fatal(err)
	}
	expectWebhook(t, capture.posts, "email-2")
	first.Shutdown()

	// Cold start: a new hub with no in-memory state over the same file store.
	second := durableTestHub(t, state, store, capture)
	if err := second.Refresh(ctx, account); err != nil {
		t.Fatal(err)
	}
	expectNoWebhook(t, capture.posts)
}

// TestSubscriptionFromInstanceAReceivesChangeInInstanceB proves a webhook
// created in one instance is honoured by a fresh instance. A subject-scoped
// credential lets the second hub associate the stored subscription with the
// account without having seen the original JMAP request.
func TestSubscriptionFromInstanceAReceivesChangeInInstanceB(t *testing.T) {
	store, err := storage.NewFileStorage(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	capture := newWebhookCapture(t)
	state := newMemoryState()
	ctx := pushContext("") // subject-scoped: credential == CredentialFromSubject(account)
	account := "alice@example.com"
	saveVerifiedSubscription(t, ctx, store, pushsubscription.CredentialFromSubject(account), capture.server.URL)

	instanceA := durableTestHub(t, state, store, capture)
	if err := instanceA.Refresh(ctx, account); err != nil {
		t.Fatal(err)
	}
	instanceA.Shutdown()

	// The change happens after A is gone; only B observes it.
	state.set(base.StateEmail, "email-from-b")
	instanceB := durableTestHub(t, state, store, capture)
	if err := instanceB.Refresh(ctx, account); err != nil {
		t.Fatal(err)
	}
	expectWebhook(t, capture.posts, "email-from-b")
}

// TestSessionScopedSubscriptionSurvivesRestart covers a subscription created
// with a session (SID) token. The fresh instance has no request context at all;
// it discovers the credential from the durable index written by Remember.
func TestSessionScopedSubscriptionSurvivesRestart(t *testing.T) {
	store, err := storage.NewFileStorage(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	capture := newWebhookCapture(t)
	state := newMemoryState()
	ctx := pushContext("sid-scoped")
	account := "alice@example.com"
	saveVerifiedSubscription(t, ctx, store, pushsubscription.CredentialID(ctx), capture.server.URL)

	first := durableTestHub(t, state, store, capture)
	first.Remember(ctx, account)
	if err := first.Refresh(ctx, account); err != nil {
		t.Fatal(err)
	}
	first.Shutdown()

	// No SID in this context: the second instance has never seen the login.
	state.set(base.StateEmail, "email-sid-scoped")
	second := durableTestHub(t, state, store, capture)
	if err := second.Refresh(context.Background(), account); err != nil {
		t.Fatal(err)
	}
	expectWebhook(t, capture.posts, "email-sid-scoped")
}

// TestWebhookBackoffSurvivesRestart proves a rate-limited endpoint is not
// hammered by a cold-started hub: backoff and the pending change are durable.
func TestWebhookBackoffSurvivesRestart(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()

	store, err := storage.NewFileStorage(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	state := newMemoryState()
	ctx := pushContext("sid-429-restart")
	account := "alice@example.com"
	credential := pushsubscription.CredentialID(ctx)
	saveVerifiedSubscription(t, ctx, store, credential, server.URL)

	first := newTestHub(state, store)
	first.SetHTTPClient(server.Client())
	first.validateURL = func(context.Context, string) error { return nil }
	if err := first.Refresh(ctx, account); err != nil {
		t.Fatal(err)
	}
	first.Remember(ctx, account)
	state.set(base.StateEmail, "email-429-restart")
	if err := first.Refresh(ctx, account); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return attempts.Load() == 1 })
	first.Shutdown()

	second := newTestHub(state, store)
	second.SetHTTPClient(server.Client())
	second.validateURL = func(context.Context, string) error { return nil }
	second.Remember(ctx, account)
	state.set(base.StateMailbox, "mailbox-429-restart")
	if err := second.Refresh(ctx, account); err != nil {
		t.Fatal(err)
	}
	// The durable backoff suppresses the second POST despite the restart.
	if got := attempts.Load(); got != 1 {
		t.Fatalf("webhook attempts after restart = %d, want 1", got)
	}
}

// TestPendingStateFlushesAfterColdStart proves the queued change is not lost
// when the instance that was rate-limited disappears: the next instance merges
// it into the first successful delivery once the backoff expires.
func TestPendingStateFlushesAfterColdStart(t *testing.T) {
	var rateLimited atomic.Bool
	rateLimited.Store(true)
	posts := make(chan StateChange, 4)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if rateLimited.Load() {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		var change StateChange
		if err := json.Unmarshal(body, &change); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		posts <- change
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	store, err := storage.NewFileStorage(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	state := newMemoryState()
	ctx := pushContext("sid-pending-restart")
	account := "alice@example.com"
	credential := pushsubscription.CredentialID(ctx)
	saveVerifiedSubscription(t, ctx, store, credential, server.URL, TypeEmail, TypeMailbox)

	first := newTestHub(state, store)
	first.SetHTTPClient(server.Client())
	first.validateURL = func(context.Context, string) error { return nil }
	first.Remember(ctx, account)
	if err := first.Refresh(ctx, account); err != nil {
		t.Fatal(err)
	}
	state.set(base.StateEmail, "email-pending")
	if err := first.Refresh(ctx, account); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		var record durableWebhook
		data, _, err := store.LoadJMAPState(ctx, webhookStateKey(credential))
		return err == nil && json.Unmarshal(data, &record) == nil &&
			record.Pending[account][TypeEmail] == "email-pending"
	})
	first.Shutdown()

	// Simulate the backoff window elapsing while no instance is running.
	second := newTestHub(state, store)
	if err := second.durable.updateWebhook(ctx, credential, func(record *durableWebhook) (bool, error) {
		record.BackoffUntil = time.Now().Add(-time.Second)
		return true, nil
	}); err != nil {
		t.Fatal(err)
	}
	rateLimited.Store(false)
	second.SetHTTPClient(server.Client())
	second.validateURL = func(context.Context, string) error { return nil }
	state.set(base.StateMailbox, "mailbox-after-pending")
	if err := second.Refresh(ctx, account); err != nil {
		t.Fatal(err)
	}

	select {
	case change := <-posts:
		got := change.Changed[account]
		if got[TypeEmail] != "email-pending" || got[TypeMailbox] != "mailbox-after-pending" {
			t.Fatalf("StateChange = %+v, want the pending Email token and the new Mailbox token", change)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("pending state was not delivered after the restart")
	}
}
