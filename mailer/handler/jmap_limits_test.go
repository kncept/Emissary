package handler

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/kncept/Emissary/mailer/sender"
	"github.com/kncept/Emissary/mailer/storage"
)

// newJMAPLimitHandler builds a real handler with a deliberately small body cap
// so the size limit can be exercised without allocating megabytes.
func newJMAPLimitHandler(t *testing.T, maxFileSize int64) http.Handler {
	t.Helper()
	h, err := New(HandlerConfig{
		AuthUser:     "admin",
		AuthPass:     "secret",
		JWTSecret:    "secret",
		JWTSecretTTL: time.Hour,
		MetadataType: "memory",
		Storage:      storage.NewMemoryStorage(),
		UserDomain:   "example.com",
		Sender:       sender.NewInMemoryRecording(),
		UserManager: &testUserManager{
			adminUsername: "admin",
			adminPassword: "secret",
			domain:        "example.com",
		},
		MaxFileSize: maxFileSize,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return h
}

func jmapToken(t *testing.T, h http.Handler) string {
	t.Helper()
	status, token, response := login(t, h, "admin", "secret")
	if status != http.StatusOK {
		t.Fatalf("login status = %d, want 200 (body %v)", status, response)
	}
	return token
}

func TestJMAPSessionAdvertisesConfiguredLimits(t *testing.T) {
	const limit = 4096
	h := newJMAPLimitHandler(t, limit)
	token := jmapToken(t, h)

	w := request(t, h, http.MethodGet, "/.well-known/jmap", "", token)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", w.Code, w.Body.String())
	}
	var session struct {
		State        string `json:"state"`
		Capabilities struct {
			Core map[string]any `json:"urn:ietf:params:jmap:core"`
			Mail map[string]any `json:"urn:ietf:params:jmap:mail"`
		} `json:"capabilities"`
		Accounts map[string]struct {
			AccountCapabilities struct {
				Core map[string]any `json:"urn:ietf:params:jmap:core"`
			} `json:"accountCapabilities"`
		} `json:"accounts"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &session); err != nil {
		t.Fatalf("decode session: %v (body %s)", err, w.Body.String())
	}
	for name, core := range map[string]map[string]any{
		"capabilities":        session.Capabilities.Core,
		"accountCapabilities": session.Accounts["admin"].AccountCapabilities.Core,
	} {
		if core["maxSizeRequest"] != float64(limit) {
			t.Errorf("%s.maxSizeRequest = %v, want %d", name, core["maxSizeRequest"], limit)
		}
		if core["maxSizeUpload"] != float64(limit) {
			t.Errorf("%s.maxSizeUpload = %v, want %d", name, core["maxSizeUpload"], limit)
		}
	}
	if session.Capabilities.Mail["maxSizeAttachmentsPerEmail"] != float64(limit) {
		t.Errorf("maxSizeAttachmentsPerEmail = %v, want %d", session.Capabilities.Mail["maxSizeAttachmentsPerEmail"], limit)
	}
	if session.State == "" || session.State == "state123" {
		t.Errorf("session state = %q, want a stable non-placeholder token", session.State)
	}

	// The session state must not change between identical requests.
	w = request(t, h, http.MethodGet, "/.well-known/jmap", "", token)
	var second struct {
		State string `json:"state"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &second); err != nil {
		t.Fatalf("decode second session: %v", err)
	}
	if second.State != session.State {
		t.Errorf("session state changed from %q to %q without any data changing", session.State, second.State)
	}
}

func TestJMAPRejectsOversizedRequestBody(t *testing.T) {
	const limit = 128
	h := newJMAPLimitHandler(t, limit)
	token := jmapToken(t, h)

	within := `{"using":["urn:ietf:params:jmap:core"],"calls":[],"id":"small"}`
	if len(within) > limit {
		t.Fatalf("test fixture is %d bytes, larger than the %d byte cap", len(within), limit)
	}
	w := request(t, h, http.MethodPost, "/api/jmap/", within, token)
	if w.Code != http.StatusOK {
		t.Fatalf("small request status = %d, want 200 (body %s)", w.Code, w.Body.String())
	}

	oversized := `{"using":["urn:ietf:params:jmap:core"],"calls":[],"id":"` + strings.Repeat("x", limit) + `"}`
	w = request(t, h, http.MethodPost, "/api/jmap/", oversized, token)
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized request status = %d, want 413 (body %s)", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Content-Type"); got != "application/problem+json" {
		t.Errorf("Content-Type = %q, want application/problem+json", got)
	}
	var problem struct {
		Type   string `json:"type"`
		Status int    `json:"status"`
		Limit  string `json:"limit"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &problem); err != nil {
		t.Fatalf("decode problem details: %v (body %s)", err, w.Body.String())
	}
	if problem.Type != "urn:ietf:params:jmap:error:limit" {
		t.Errorf("type = %q, want urn:ietf:params:jmap:error:limit", problem.Type)
	}
	if problem.Status != http.StatusRequestEntityTooLarge {
		t.Errorf("status = %d, want %d", problem.Status, http.StatusRequestEntityTooLarge)
	}
	if problem.Limit != "maxSizeRequest" {
		t.Errorf("limit = %q, want maxSizeRequest", problem.Limit)
	}
}
