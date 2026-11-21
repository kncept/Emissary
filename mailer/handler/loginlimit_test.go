package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kncept/Emissary/mailer/components/sender"
	"github.com/kncept/Emissary/mailer/components/storage"
)

func TestLoginLimiterFixedWindow(t *testing.T) {
	now := time.Date(2099, 1, 1, 12, 0, 0, 0, time.UTC)
	limiter := newLoginLimiter(2, time.Minute)
	limiter.now = func() time.Time { return now }

	for i := 0; i < 2; i++ {
		if ok, _ := limiter.Allow("ip:1"); !ok {
			t.Fatalf("attempt %d was unexpectedly denied", i+1)
		}
		limiter.Record("ip:1")
	}
	ok, retry := limiter.Allow("ip:1")
	if ok {
		t.Error("attempt past the limit was allowed")
	}
	if retry <= 0 || retry > time.Minute {
		t.Errorf("Allow() retry = %v, want a positive delay within the window", retry)
	}
	if ok, _ := limiter.Allow("ip:2"); !ok {
		t.Error("another IP was denied while the first was at its limit")
	}

	now = now.Add(time.Minute)
	if ok, _ := limiter.Allow("ip:1"); !ok {
		t.Error("attempt after the window elapsed was denied")
	}

	limiter.Record("user:alice")
	limiter.Record("user:alice")
	limiter.Reset("user:alice")
	if ok, _ := limiter.Allow("user:alice"); !ok {
		t.Error("Reset() did not clear the account bucket")
	}
}

func TestLoginEndpointRateLimitsFailures(t *testing.T) {
	users := &testUserManager{adminUsername: "admin", adminPassword: "secret", domain: "example.com"}
	h, err := newTestHandler(t, handlerTestConfig{
		AuthUser:             "admin",
		AuthPass:             "secret",
		JWTSecret:            "secret",
		JWTSecretTTL:         time.Hour,
		LoginRateLimit:       2,
		LoginRateLimitWindow: time.Minute,
		MetadataType:         "memory",
		Storage:              storage.NewMemoryStorage(),
		UserManager:          users,
		UserDomain:           "example.com",
		Sender:               sender.NewInMemoryRecording(),
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	login := func(username, password, forwardedFor string) *httptest.ResponseRecorder {
		t.Helper()
		body, _ := json.Marshal(map[string]string{"username": username, "password": password})
		req := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(string(body)))
		req.Header.Set("Content-Type", "application/json")
		if forwardedFor != "" {
			req.Header.Set("X-Forwarded-For", forwardedFor)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w
	}

	for i := 0; i < 2; i++ {
		if w := login("admin", "wrong", ""); w.Code != http.StatusUnauthorized {
			t.Fatalf("failed login %d status = %d, body = %s, want 401", i+1, w.Code, w.Body.String())
		}
	}
	w := login("admin", "wrong", "")
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("third failed login status = %d, body = %s, want 429", w.Code, w.Body.String())
	}
	if w.Header().Get("Retry-After") == "" {
		t.Error("429 response is missing Retry-After")
	}
	// Spoofing X-Forwarded-For cannot bypass the per-account bucket.
	if w := login("admin", "wrong", "1.2.3.4"); w.Code != http.StatusTooManyRequests {
		t.Errorf("spoofed-IP login status = %d, body = %s, want 429", w.Code, w.Body.String())
	}
	// A different account from the same IP still hits the per-IP bucket.
	if w := login("alice@example.com", "wrong", ""); w.Code != http.StatusTooManyRequests {
		t.Errorf("same-IP other account status = %d, body = %s, want 429", w.Code, w.Body.String())
	}
	// A different IP can still try another account.
	if w := login("nobody@other.invalid", "wrong", "5.6.7.8"); w.Code != http.StatusUnauthorized {
		t.Errorf("other-IP login status = %d, body = %s, want 401", w.Code, w.Body.String())
	}

	// A successful login clears the account bucket (but not the IP bucket).
	if w := login("alice@example.com", "anything", "5.6.7.8"); w.Code != http.StatusOK {
		t.Fatalf("successful login status = %d, body = %s, want 200", w.Code, w.Body.String())
	}
	if w := login("alice@example.com", "anything", "5.6.7.8"); w.Code != http.StatusOK {
		t.Errorf("login after reset status = %d, body = %s, want 200", w.Code, w.Body.String())
	}
	if w := login("alice@example.com", "anything", ""); w.Code != http.StatusTooManyRequests {
		t.Errorf("same-IP login after reset status = %d, body = %s, want 429", w.Code, w.Body.String())
	}
}
