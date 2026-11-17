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
	authenticator := auth.New("user", "pass", "secret", 24*time.Hour, "", "")

	cfg := handler.HandlerConfig{
		AuthUser:     "user",
		AuthPass:     "pass",
		JWTSecret:    "secret",
		JWTSecretTTL: 24 * time.Hour,
		MetadataType: "memory",
		Storage:      storage.NewMemoryStorage(),
		JWTAudience:  "",
		JWTIssuer:    "",
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
	accessToken, _ := resp["accessToken"].(string)
	if accessToken == "" {
		t.Fatal("expected a non-empty accessToken in the response")
	}
	if resp["tokenType"] != "Bearer" {
		t.Errorf("tokenType = %v, want %q", resp["tokenType"], "Bearer")
	}
	if _, ok := resp["success"]; ok {
		t.Error("login response should not contain a legacy success flag")
	}
	if _, err := authenticator.Validate(accessToken); err != nil {
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
	// Test JMAP endpoint which requires auth
	w := doRequest(h, http.MethodPost, "/api/", `{"using":["urn:ietf:params:jmap:mail"],"methodCalls":[["Email/query",{"accountId":"user","filter":{"inMailbox":"inbox"}}, "q1"]]}`, token)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", w.Code, http.StatusOK)
	}
}

func TestAuthMiddleware_InvalidToken(t *testing.T) {
	h, _ := newTestHandler()
	w := doRequest(h, http.MethodPost, "/api/", `{"using":["urn:ietf:params:jmap:mail"],"methodCalls":[["Email/query",{"accountId":"user","filter":{"inMailbox":"inbox"}}, "q1"]]}`, "invalid-token")

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", w.Code, http.StatusUnauthorized)
	}
}

func TestAuthMiddleware_MissingToken(t *testing.T) {
	h, _ := newTestHandler()
	w := doRequest(h, http.MethodPost, "/api/", `{"using":["urn:ietf:params:jmap:mail"],"methodCalls":[["Email/query",{"accountId":"user","filter":{"inMailbox":"inbox"}}, "q1"]]}`, "")

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", w.Code, http.StatusUnauthorized)
	}
}

