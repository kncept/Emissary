package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kncept/Emissary/mailer/auth"
	"github.com/kncept/Emissary/mailer/handler"
	"github.com/kncept/Emissary/mailer/storage"
)

func newTestHandler() (http.Handler, *auth.Authenticator) {
	authenticator := auth.New("user", "pass", "secret", 24*time.Hour)

	cfg := handler.HandlerConfig{
		AuthUser:     "user",
		AuthPass:     "pass",
		JWTSecret:    "secret",
		JWTSecretTTL: 24 * time.Hour,
		MetadataType: "memory",
		Storage:      storage.NewMemoryStorage(),
	}

	h, err := handler.New(cfg)
	if err != nil {
		panic(err)
	}

	return h, authenticator
}

func doRequest(r http.Handler, method, target, body string, token string) *httptest.ResponseRecorder {
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, target, reader)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestLogin_IssuesJWT(t *testing.T) {
	h, authenticator := newTestHandler()
	w := doRequest(h, http.MethodPost, "/api/auth/login",
		`{"username":"user","password":"pass"}`, "")

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (body %s)", w.Code, http.StatusOK, w.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	token, _ := resp["token"].(string)
	if token == "" {
		t.Fatal("expected a non-empty token in the response")
	}
	if resp["tokenType"] != "Bearer" {
		t.Errorf("tokenType = %v, want %q", resp["tokenType"], "Bearer")
	}
	if _, ok := resp["success"]; ok {
		t.Error("login response should not contain a legacy success flag")
	}
	if _, err := authenticator.Validate(token); err != nil {
		t.Errorf("issued token does not validate: %v", err)
	}
}

func TestLogin_InvalidCredentials(t *testing.T) {
	h, _ := newTestHandler()
	w := doRequest(h, http.MethodPost, "/api/auth/login",
		`{"username":"user","password":"wrong"}`, "")

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", w.Code, http.StatusUnauthorized)
	}
}

func TestLogin_InvalidJSON(t *testing.T) {
	h, _ := newTestHandler()
	w := doRequest(h, http.MethodPost, "/api/auth/login",
		`not json`, "")

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestAuthMiddleware_ValidToken(t *testing.T) {
	h, authenticator := newTestHandler()
	token, _, _ := authenticator.Issue("user")
	w := doRequest(h, http.MethodGet, "/api/emails", "", token)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", w.Code, http.StatusOK)
	}
}

func TestAuthMiddleware_InvalidToken(t *testing.T) {
	h, _ := newTestHandler()
	w := doRequest(h, http.MethodGet, "/api/emails", "", "invalid-token")

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", w.Code, http.StatusUnauthorized)
	}
}

func TestAuthMiddleware_MissingToken(t *testing.T) {
	h, _ := newTestHandler()
	w := doRequest(h, http.MethodGet, "/api/emails", "", "")

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", w.Code, http.StatusUnauthorized)
	}
}

func TestAuthCheck_ValidToken(t *testing.T) {
	h, authenticator := newTestHandler()
	token, _, _ := authenticator.Issue("user")
	w := doRequest(h, http.MethodGet, "/api/auth/check", "", token)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
	var resp struct {
		Authenticated bool   `json:"authenticated"`
		Username      string `json:"username"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !resp.Authenticated || resp.Username != "user" {
		t.Errorf("got %+v, want authenticated user %q", resp, "user")
	}
}

func TestAuthCheck_NoToken(t *testing.T) {
	h, _ := newTestHandler()
	w := doRequest(h, http.MethodGet, "/api/auth/check", "", "")

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
	var resp struct {
		Authenticated bool `json:"authenticated"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Authenticated {
		t.Error("expected authenticated=false when no token is sent")
	}
}