package pushsubscription

import (
	"context"
	"encoding/json"
	"fmt"
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
)

type capturedPush struct {
	header http.Header
	body   []byte
}

type pushHarness struct {
	handler *Handler
	store   *storage.MemoryStorage
	server  *httptest.Server

	mu     sync.Mutex
	pushes []capturedPush
}

func newPushHarness(t *testing.T) *pushHarness {
	t.Helper()
	h := &pushHarness{}
	h.server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		h.mu.Lock()
		h.pushes = append(h.pushes, capturedPush{header: r.Header.Clone(), body: body})
		h.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(h.server.Close)

	h.store = storage.NewMemoryStorage()
	h.handler = New(&base.Deps{Storage: h.store, HTTPClient: h.server.Client()})
	// The local TLS server listens on loopback, which production validation
	// correctly rejects. Tests exercise the rest of the validation rules.
	h.handler.allowPrivateURLs = true
	return h
}

func (h *pushHarness) ctx(sid string) context.Context {
	return auth.ContextWithClaims(context.Background(), &auth.Claims{
		Subject: "alice@example.com",
		SID:     sid,
	})
}

func (h *pushHarness) lastPush(t *testing.T) (capturedPush, struct {
	Type string `json:"@type"`
	ID   string `json:"pushSubscriptionId"`
	Code string `json:"verificationCode"`
}) {
	t.Helper()
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.pushes) == 0 {
		t.Fatal("no verification push was received")
	}
	push := h.pushes[len(h.pushes)-1]
	var payload struct {
		Type string `json:"@type"`
		ID   string `json:"pushSubscriptionId"`
		Code string `json:"verificationCode"`
	}
	if err := json.Unmarshal(push.body, &payload); err != nil {
		t.Fatalf("decode verification body %q: %v", push.body, err)
	}
	return push, payload
}

type setResponse struct {
	Created      map[string]json.RawMessage `json:"created"`
	Updated      map[string]json.RawMessage `json:"updated"`
	Destroyed    []string                   `json:"destroyed"`
	NotCreated   map[string]setError        `json:"notCreated"`
	NotUpdated   map[string]setError        `json:"notUpdated"`
	NotDestroyed map[string]setError        `json:"notDestroyed"`
}

func (h *pushHarness) set(t *testing.T, ctx context.Context, args string) (setResponse, json.RawMessage) {
	t.Helper()
	raw, err := h.handler.PushSubscriptionSet(ctx, "", json.RawMessage(args))
	if err != nil {
		t.Fatalf("PushSubscriptionSet() error = %v", err)
	}
	var result setResponse
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("decode PushSubscriptionSet() response: %v (raw %s)", err, raw)
	}
	return result, raw
}

type getResponse struct {
	List     []json.RawMessage `json:"list"`
	NotFound []string          `json:"notFound"`
}

func (h *pushHarness) get(t *testing.T, ctx context.Context, args string) (getResponse, json.RawMessage) {
	t.Helper()
	raw, err := h.handler.PushSubscriptionGet(ctx, "", json.RawMessage(args))
	if err != nil {
		t.Fatalf("PushSubscriptionGet() error = %v", err)
	}
	var result getResponse
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("decode PushSubscriptionGet() response: %v (raw %s)", err, raw)
	}
	return result, raw
}

func subscriptionID(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	var value struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &value); err != nil || value.ID == "" {
		t.Fatalf("subscription has no id: %s (%v)", raw, err)
	}
	return value.ID
}

func createArgs(url string) string {
	return `{"create":{"c1":{"deviceClientId":"device-1","url":"` + url + `","keys":{"p256dh":"AQID","auth":"BAUG"},"expires":"2027-01-01T00:00:00Z","types":["Email"]}}}`
}

func TestPushSubscriptionCreateSendsVerificationAndHidesPrivateProperties(t *testing.T) {
	h := newPushHarness(t)
	ctx := h.ctx("session-a")
	result, raw := h.set(t, ctx, createArgs(h.server.URL+"/push"))

	created, ok := result.Created["c1"]
	if !ok || len(result.NotCreated) != 0 {
		t.Fatalf("create result = %+v, want c1", result)
	}
	id := subscriptionID(t, created)
	if strings.Contains(string(raw), "url") || strings.Contains(string(raw), "p256dh") || strings.Contains(string(raw), "BAUG") {
		t.Fatalf("set response leaked private properties: %s", raw)
	}

	push, payload := h.lastPush(t)
	if payload.Type != "PushVerification" || payload.ID != id || payload.Code == "" {
		t.Fatalf("verification payload = %+v", payload)
	}
	if push.header.Get("Content-Type") != "application/json" || push.header.Get("TTL") != verificationTTL {
		t.Errorf("verification headers = %v, want application/json and TTL", push.header)
	}

	stored, err := h.store.LoadPushSubscriptions(ctx, CredentialID(ctx))
	if err != nil || len(stored) != 1 {
		t.Fatalf("stored subscriptions = %+v, %v", stored, err)
	}
	if stored[0].URL != h.server.URL+"/push" || stored[0].Keys == nil || stored[0].VerificationCode != payload.Code {
		t.Errorf("stored subscription = %+v, want private fields retained server-side", stored[0])
	}
}

func TestPushSubscriptionGetScopesAndFilters(t *testing.T) {
	h := newPushHarness(t)
	ctxA, ctxB := h.ctx("session-a"), h.ctx("session-b")
	resultA, _ := h.set(t, ctxA, createArgs(h.server.URL+"/a"))
	resultB, _ := h.set(t, ctxB, createArgs(h.server.URL+"/b"))
	idA := subscriptionID(t, resultA.Created["c1"])
	idB := subscriptionID(t, resultB.Created["c1"])

	listA, rawA := h.get(t, ctxA, `{}`)
	if len(listA.List) != 1 || len(listA.NotFound) != 0 || subscriptionID(t, listA.List[0]) != idA {
		t.Fatalf("get(A) = %+v, want only %q", listA, idA)
	}
	if strings.Contains(string(rawA), "https://") || strings.Contains(string(rawA), "p256dh") {
		t.Fatalf("get response leaked private properties: %s", rawA)
	}
	listB, _ := h.get(t, ctxB, `{}`)
	if len(listB.List) != 1 || subscriptionID(t, listB.List[0]) != idB {
		t.Fatalf("get(B) = %+v, want only %q", listB, idB)
	}

	selected, _ := h.get(t, ctxA, `{"properties":["deviceClientId"]}`)
	var viewed map[string]json.RawMessage
	if err := json.Unmarshal(selected.List[0], &viewed); err != nil {
		t.Fatalf("decode selected subscription: %v", err)
	}
	if _, ok := viewed["verificationCode"]; ok {
		t.Errorf("selected response kept verificationCode: %s", selected.List[0])
	}

	byID, _ := h.get(t, ctxA, `{"ids":["`+idA+`","missing"]}`)
	if len(byID.List) != 1 || len(byID.NotFound) != 1 || byID.NotFound[0] != "missing" {
		t.Fatalf("get(ids) = %+v", byID)
	}

	if _, err := h.handler.PushSubscriptionGet(ctxA, "", json.RawMessage(`{"properties":["url"]}`)); err == nil || !strings.HasPrefix(err.Error(), "forbidden:") {
		t.Errorf("explicit url property error = %v, want forbidden", err)
	}
	if _, err := h.handler.PushSubscriptionGet(ctxA, "", json.RawMessage(`{"properties":["keys"]}`)); err == nil || !strings.HasPrefix(err.Error(), "forbidden:") {
		t.Errorf("explicit keys property error = %v, want forbidden", err)
	}
	if _, err := h.handler.PushSubscriptionGet(ctxA, "", json.RawMessage(`{"properties":["bogus"]}`)); err == nil || !strings.HasPrefix(err.Error(), "invalidArguments:") {
		t.Errorf("unknown property error = %v, want invalidArguments", err)
	}
	if _, err := h.handler.PushSubscriptionGet(ctxA, "", json.RawMessage(`{"accountId":"alice@example.com"}`)); err == nil || !strings.HasPrefix(err.Error(), "invalidArguments:") {
		t.Errorf("accountId argument error = %v, want invalidArguments", err)
	}
}

func TestPushSubscriptionUpdateRequiresVerificationCode(t *testing.T) {
	h := newPushHarness(t)
	ctx := h.ctx("session-a")
	created, _ := h.set(t, ctx, createArgs(h.server.URL+"/push"))
	id := subscriptionID(t, created.Created["c1"])
	_, payload := h.lastPush(t)

	wrong, _ := h.set(t, ctx, `{"update":{"`+id+`":{"verificationCode":"guess"}}}`)
	if wrong.NotUpdated[id].Type != "invalidProperties" {
		t.Fatalf("wrong code result = %+v, want invalidProperties", wrong.NotUpdated)
	}

	good, _ := h.set(t, ctx, `{"update":{"`+id+`":{"verificationCode":"`+payload.Code+`"}}}`)
	if _, ok := good.Updated[id]; !ok {
		t.Fatalf("correct code result = %+v, want update", good)
	}
	stored, err := h.store.LoadPushSubscriptions(ctx, CredentialID(ctx))
	if err != nil || len(stored) != 1 || !stored[0].Verified {
		t.Fatalf("stored subscription = %+v, %v, want verified", stored, err)
	}

	for _, args := range []string{
		`{"update":{"` + id + `":{"url":"https://evil.example/push"}}}`,
		`{"update":{"` + id + `":{"keys":null}}}`,
		`{"update":{"` + id + `":{"deviceClientId":"other"}}}`,
	} {
		result, _ := h.set(t, ctx, args)
		if result.NotUpdated[id].Type != "invalidProperties" {
			t.Errorf("immutable update %s = %+v, want invalidProperties", args, result.NotUpdated)
		}
	}

	extended, _ := h.set(t, ctx, `{"update":{"`+id+`":{"expires":"2027-06-01T00:00:00Z"}}}`)
	if _, ok := extended.Updated[id]; !ok {
		t.Fatalf("expires update = %+v, want success", extended.Updated)
	}
	cleared, _ := h.set(t, ctx, `{"update":{"`+id+`":{"expires":null}}}`)
	if _, ok := cleared.Updated[id]; !ok {
		t.Fatalf("expires clear = %+v, want success", cleared.Updated)
	}
}

func TestPushSubscriptionDestroyAndCredentialRevocation(t *testing.T) {
	h := newPushHarness(t)
	ctx := h.ctx("session-a")
	created, _ := h.set(t, ctx, createArgs(h.server.URL+"/push"))
	id := subscriptionID(t, created.Created["c1"])

	destroyed, _ := h.set(t, ctx, `{"destroy":["`+id+`","missing"]}`)
	if len(destroyed.Destroyed) != 1 || destroyed.Destroyed[0] != id || destroyed.NotDestroyed["missing"].Type != "notFound" {
		t.Fatalf("destroy result = %+v", destroyed)
	}
	list, _ := h.get(t, ctx, `{}`)
	if len(list.List) != 0 {
		t.Fatalf("get after destroy = %+v, want empty", list)
	}

	created, _ = h.set(t, ctx, createArgs(h.server.URL+"/push"))
	if err := DestroyForCredential(ctx, h.store); err != nil {
		t.Fatalf("DestroyForCredential() error = %v", err)
	}
	list, _ = h.get(t, ctx, `{}`)
	if len(list.List) != 0 {
		t.Fatalf("get after credential revocation = %+v, want empty", list)
	}
}

func TestPushSubscriptionConcurrentSetsDoNotLoseUpdates(t *testing.T) {
	h := newPushHarness(t)
	ctx := h.ctx("session-a")
	h.set(t, ctx, createArgs(h.server.URL+"/push"))
	first, _ := h.get(t, ctx, `{}`)
	id := subscriptionID(t, first.List[0])

	var wg sync.WaitGroup
	for _, args := range []string{
		`{"update":{"` + id + `":{"expires":"2027-01-01T00:00:00Z"}}}`,
		`{"update":{"` + id + `":{"expires":"2027-06-01T00:00:00Z"}}}`,
	} {
		wg.Add(1)
		go func(a string) {
			defer wg.Done()
			_, err := h.handler.PushSubscriptionSet(ctx, "", json.RawMessage(a))
			if err != nil {
				t.Errorf("concurrent Set() error = %v", err)
			}
		}(args)
	}
	wg.Wait()

	stored, err := h.store.LoadPushSubscriptions(ctx, CredentialID(ctx))
	if err != nil || len(stored) != 1 {
		t.Fatalf("stored subscriptions = %+v, %v, want one", stored, err)
	}
}

func TestPushSubscriptionDestroysSubscriptionsWhenSessionExpires(t *testing.T) {
	h := newPushHarness(t)
	ctx := h.ctx("session-a")
	h.set(t, ctx, createArgs(h.server.URL+"/push"))

	// Simulate expiry by wiring a checker that rejects the session from now on.
	h.handler.deps.CheckSession = func(context.Context, string) bool { return false }
	list, _ := h.get(t, ctx, `{}`)
	if len(list.List) != 0 {
		t.Fatalf("get after session expiry = %+v, want empty", list)
	}
	stored, err := h.store.LoadPushSubscriptions(ctx, CredentialID(ctx))
	if err != nil || len(stored) != 0 {
		t.Fatalf("subscriptions after session expiry = %+v, %v", stored, err)
	}
}

func TestPushSubscriptionRejectsUnsafeURLs(t *testing.T) {
	h := New(&base.Deps{})
	for _, target := range []string{
		"https://127.0.0.1/push",
		"https://localhost/push",
		"https://[::1]/push",
		"https://10.0.0.1/push",
		"https://192.168.1.1/push",
		"https://172.16.0.1/push",
		"https://169.254.169.254/latest/meta-data/",
		"http://example.com/push",
		"https://user:pass@example.com/push",
	} {
		if err := h.validatePushURL(context.Background(), target); err == nil {
			t.Errorf("validatePushURL(%q) succeeded, want rejection", target)
		}
	}
}

func TestPushSubscriptionCreateRateLimit(t *testing.T) {
	h := newPushHarness(t)
	ctx := h.ctx("session-a")
	for i := 0; i < maxPushCreatesPerMinute; i++ {
		result, _ := h.set(t, ctx, createArgs(fmt.Sprintf("%s/push%d", h.server.URL, i)))
		if _, ok := result.Created["c1"]; !ok {
			t.Fatalf("create %d failed: %+v", i, result.NotCreated)
		}
	}
	result, _ := h.set(t, ctx, createArgs(h.server.URL+"/overflow"))
	if result.NotCreated["c1"].Type != "rateLimit" {
		t.Fatalf("create beyond rate limit = %+v, want rateLimit", result.NotCreated)
	}
}

func TestPushSubscriptionCreateCap(t *testing.T) {
	h := newPushHarness(t)
	ctx := h.ctx("session-a")
	credential := CredentialID(ctx)
	subs := make([]storage.PushSubscription, maxPushSubscriptionsPerCredential)
	for i := range subs {
		subs[i] = storage.PushSubscription{
			ID:             fmt.Sprintf("sub-%d", i),
			CredentialID:   credential,
			DeviceClientID: fmt.Sprintf("device-%d", i),
			URL:            h.server.URL + "/push",
		}
	}
	if err := h.store.SavePushSubscriptions(ctx, credential, subs); err != nil {
		t.Fatalf("SavePushSubscriptions() error = %v", err)
	}
	result, _ := h.set(t, ctx, createArgs(h.server.URL+"/push"))
	if result.NotCreated["c1"].Type != "rateLimit" {
		t.Fatalf("create at cap = %+v, want rateLimit", result.NotCreated)
	}
}

func TestPushSubscriptionSetPrunesExpiredSubscriptions(t *testing.T) {
	h := newPushHarness(t)
	ctx := h.ctx("session-a")
	credential := CredentialID(ctx)
	if err := h.store.SavePushSubscriptions(ctx, credential, []storage.PushSubscription{{
		ID:             "expired",
		CredentialID:   credential,
		DeviceClientID: "old-device",
		URL:            h.server.URL + "/expired",
		Expires:        time.Now().Add(-time.Hour).UTC().Format(time.RFC3339),
	}}); err != nil {
		t.Fatalf("SavePushSubscriptions() error = %v", err)
	}

	result, _ := h.set(t, ctx, `{}`)
	if len(result.Created) != 0 || len(result.NotCreated) != 0 {
		t.Fatalf("Set() with expired subscription = %+v", result)
	}
	stored, err := h.store.LoadPushSubscriptions(ctx, credential)
	if err != nil || len(stored) != 0 {
		t.Fatalf("stored subscriptions after Set() = %+v, %v", stored, err)
	}
}

func TestPushSubscriptionGetPrunesExpiredSubscriptions(t *testing.T) {
	h := newPushHarness(t)
	ctx := h.ctx("session-a")
	credential := CredentialID(ctx)
	err := h.store.SavePushSubscriptions(ctx, credential, []storage.PushSubscription{{
		ID:               "expired",
		CredentialID:     credential,
		DeviceClientID:   "old-device",
		URL:              h.server.URL + "/expired",
		VerificationCode: "old-code",
		Expires:          time.Now().Add(-time.Hour).UTC().Format(time.RFC3339),
	}})
	if err != nil {
		t.Fatalf("SavePushSubscriptions() error = %v", err)
	}

	list, _ := h.get(t, ctx, `{}`)
	if len(list.List) != 0 || len(list.NotFound) != 0 {
		t.Fatalf("get with expired subscription = %+v, want empty", list)
	}
	stored, err := h.store.LoadPushSubscriptions(ctx, credential)
	if err != nil || len(stored) != 0 {
		t.Fatalf("stored subscriptions after expiry = %+v, %v", stored, err)
	}
}
