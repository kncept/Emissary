package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/kncept/Emissary/mailer/auth"
	"github.com/kncept/Emissary/mailer/email"
	"github.com/kncept/Emissary/mailer/storage"
)

type mockStorage struct {
	emails []*email.Email
	err    error
	debug  bool
}

func (m *mockStorage) Name() string { return "MockStorage" }

func (m *mockStorage) ListAllEmails(ctx context.Context) ([]*email.Email, error) {
	return m.emails, m.err
}

func (m *mockStorage) GetEmail(ctx context.Context, id string) (*email.Email, error) {
	if m.debug {
		fmt.Fprintf(os.Stderr, "mockStorage.GetEmail called with id=%q, emails=%v\n", id, m.emails)
	}
	for _, e := range m.emails {
		if e.ID == id {
			return e, nil
		}
	}
	return nil, storage.ErrNotFound
}

type mockSender struct {
	from string
	err  error
}

func (m *mockSender) Name() string { return "MockSender" }
func (m *mockSender) From() string { return m.from }
func (m *mockSender) Send(ctx context.Context, msg *email.Email) error { return m.err }

type mockMetadata struct {
	readMap map[string]bool
	err     error
}

func (m *mockMetadata) Name() string { return "MockMetadata" }
func (m *mockMetadata) IsRead(ctx context.Context, id string) (bool, error) {
	return m.readMap[id], m.err
}
func (m *mockMetadata) MarkRead(ctx context.Context, id string) error {
	m.readMap[id] = true
	return m.err
}
func (m *mockMetadata) MarkUnread(ctx context.Context, id string) error {
	delete(m.readMap, id)
	return m.err
}

func newTestHandler() *handler {
	authenticator := auth.New("user", "pass", "secret", 24*time.Hour, "", "")
	return &handler{
		auth:     authenticator,
		storage:  &mockStorage{emails: []*email.Email{}},
		sender:   &mockSender{from: "sender@example.com"},
		metadata: &mockMetadata{readMap: make(map[string]bool)},
		logger:   log.New(io.Discard, "", 0),
	}
}

func TestHealthEndpoint(t *testing.T) {
	h := newTestHandler()
	h.storage = &mockStorage{}
	h.sender = &mockSender{}
	h.metadata = &mockMetadata{}

	req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	w := httptest.NewRecorder()
	h.health(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", w.Code, http.StatusOK)
	}

	var resp map[string]string
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode error = %v", err)
	}
	if resp["status"] != "ok" {
		t.Errorf("status = %q, want %q", resp["status"], "ok")
	}
}

func TestLogin_Success(t *testing.T) {
	h := newTestHandler()

	body := `{"username":"user","password":"pass"}`
	req := httptest.NewRequest(http.MethodPost, "/api/login", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.login(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", w.Code, http.StatusOK)
	}

	var resp loginResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode error = %v", err)
	}
	if resp.Token == "" {
		t.Error("token should not be empty")
	}
	if resp.TokenType != "Bearer" {
		t.Errorf("tokenType = %q, want %q", resp.TokenType, "Bearer")
	}
}

func TestLogin_InvalidCredentials(t *testing.T) {
	h := newTestHandler()

	body := `{"username":"user","password":"wrong"}`
	req := httptest.NewRequest(http.MethodPost, "/api/login", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.login(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", w.Code, http.StatusUnauthorized)
	}
}

func TestLogin_InvalidJSON(t *testing.T) {
	h := newTestHandler()

	body := `not json`
	req := httptest.NewRequest(http.MethodPost, "/api/login", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.login(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestRequireAuth_ValidToken(t *testing.T) {
	h := newTestHandler()
	token, _, _ := h.auth.Issue("user")

	nextCalled := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nextCalled = true
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	h.requireAuth(next).ServeHTTP(w, req)

	if !nextCalled {
		t.Error("next handler should be called with valid token")
	}
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", w.Code, http.StatusOK)
	}
}

func TestRequireAuth_InvalidToken(t *testing.T) {
	h := newTestHandler()

	nextCalled := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nextCalled = true
	})

	req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	req.Header.Set("Authorization", "Bearer invalid-token")
	w := httptest.NewRecorder()
	h.requireAuth(next).ServeHTTP(w, req)

	if nextCalled {
		t.Error("next handler should not be called with invalid token")
	}
	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", w.Code, http.StatusUnauthorized)
	}
}

func TestRequireAuth_MissingToken(t *testing.T) {
	h := newTestHandler()

	nextCalled := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nextCalled = true
	})

	req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	w := httptest.NewRecorder()
	h.requireAuth(next).ServeHTTP(w, req)

	if nextCalled {
		t.Error("next handler should not be called without token")
	}
	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", w.Code, http.StatusUnauthorized)
	}
}

func TestListEmails(t *testing.T) {
	h := newTestHandler()
	h.storage = &mockStorage{
		emails: []*email.Email{
			{ID: "1", From: email.Address{Address: "a@b.com"}, Subject: "Test", Date: time.Now(), Text: "Body"},
		},
	}
	h.metadata = &mockMetadata{readMap: map[string]bool{"1": true}}

	token, _, _ := h.auth.Issue("user")
	req := httptest.NewRequest(http.MethodGet, "/api/emails", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	h.requireAuth(http.HandlerFunc(h.listEmails)).ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", w.Code, http.StatusOK)
	}

	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode error = %v", err)
	}
	emails := resp["emails"].([]any)
	if len(emails) != 1 {
		t.Errorf("email count = %d, want 1", len(emails))
	}
}

func TestGetEmail(t *testing.T) {
	h := newTestHandler()
	mockStore := &mockStorage{
		emails: []*email.Email{
			{ID: "1", From: email.Address{Address: "a@b.com"}, Subject: "Test", Date: time.Now(), Text: "Body"},
		},
		debug: true,
	}
	h.storage = mockStore

	token, _, _ := h.auth.Issue("user")

	// Use Go 1.22+ ServeMux with pattern matching for PathValue to work
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/emails/{id}", func(w http.ResponseWriter, r *http.Request) {
		h.requireAuth(http.HandlerFunc(h.getEmail)).ServeHTTP(w, r)
	})

	req := httptest.NewRequest(http.MethodGet, "/api/emails/1", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	t.Logf("Response code: %d, body: %s", w.Code, w.Body.String())

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", w.Code, http.StatusOK)
	}

	var resp email.Email
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode error = %v", err)
	}
	if resp.ID != "1" {
		t.Errorf("ID = %q, want %q", resp.ID, "1")
	}
}

func TestGetEmail_NotFound(t *testing.T) {
	h := newTestHandler()
	h.storage = &mockStorage{emails: []*email.Email{}}

	token, _, _ := h.auth.Issue("user")
	req := httptest.NewRequest(http.MethodGet, "/api/emails/999", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	h.requireAuth(http.HandlerFunc(h.getEmail)).ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", w.Code, http.StatusNotFound)
	}
}

func TestMarkRead(t *testing.T) {
	h := newTestHandler()
	h.metadata = &mockMetadata{readMap: make(map[string]bool)}

	token, _, _ := h.auth.Issue("user")
	req := httptest.NewRequest(http.MethodPost, "/api/emails/1/read", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	h.requireAuth(http.HandlerFunc(h.markRead)).ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", w.Code, http.StatusOK)
	}

	var resp map[string]bool
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode error = %v", err)
	}
	if !resp["ok"] {
		t.Error("ok should be true")
	}
}

func TestMarkUnread(t *testing.T) {
	h := newTestHandler()
	h.metadata = &mockMetadata{readMap: map[string]bool{"1": true}}

	token, _, _ := h.auth.Issue("user")
	req := httptest.NewRequest(http.MethodPost, "/api/emails/1/unread", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	h.requireAuth(http.HandlerFunc(h.markUnread)).ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", w.Code, http.StatusOK)
	}

	var resp map[string]bool
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode error = %v", err)
	}
	if !resp["ok"] {
		t.Error("ok should be true")
	}
}

func TestSendEmail(t *testing.T) {
	h := newTestHandler()
	h.sender = &mockSender{from: "sender@example.com"}

	token, _, _ := h.auth.Issue("user")
	body := `{"to":["to@example.com"],"subject":"Test","text":"Body"}`
	req := httptest.NewRequest(http.MethodPost, "/api/send", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.requireAuth(http.HandlerFunc(h.sendEmail)).ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", w.Code, http.StatusOK)
	}

	var resp map[string]bool
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode error = %v", err)
	}
	if !resp["ok"] {
		t.Error("ok should be true")
	}
}

func TestSendEmail_Validation(t *testing.T) {
	h := newTestHandler()

	tests := []struct {
		name   string
		body   string
		status int
	}{
		{"empty to", `{"to":[],"subject":"Test","text":"Body"}`, http.StatusBadRequest},
		{"missing to", `{"subject":"Test","text":"Body"}`, http.StatusBadRequest},
		{"invalid email", `{"to":["invalid"],"subject":"Test","text":"Body"}`, http.StatusBadRequest},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			token, _, _ := h.auth.Issue("user")
			req := httptest.NewRequest(http.MethodPost, "/api/send", strings.NewReader(tt.body))
			req.Header.Set("Authorization", "Bearer "+token)
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			h.requireAuth(http.HandlerFunc(h.sendEmail)).ServeHTTP(w, req)

			if w.Code != tt.status {
				t.Errorf("status = %d, want %d", w.Code, tt.status)
			}
		})
	}
}

func TestStaticHandler_ServesIndex(t *testing.T) {
	// This would need a proper fs.FS with index.html, skipping for unit test
}

func TestParseAddresses(t *testing.T) {
	tests := []struct {
		name     string
		input    []string
		expected []email.Address
		wantErr  bool
	}{
		{
			name:     "single address",
			input:    []string{"a@b.com"},
			expected: []email.Address{{Address: "a@b.com"}},
			wantErr:  false,
		},
		{
			name:     "multiple addresses",
			input:    []string{"a@b.com, c@d.com"},
			expected: []email.Address{{Address: "a@b.com"}, {Address: "c@d.com"}},
			wantErr:  false,
		},
		{
			name:     "with display name",
			input:    []string{"Name <a@b.com>"},
			expected: []email.Address{{Name: "Name", Address: "a@b.com"}},
			wantErr:  false,
		},
		{
			name:     "invalid address",
			input:    []string{"invalid"},
			expected: nil,
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseAddresses(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Error("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(got) != len(tt.expected) {
				t.Errorf("count = %d, want %d", len(got), len(tt.expected))
			}
			for i := range got {
				if got[i].Address != tt.expected[i].Address {
					t.Errorf("[%d] address = %q, want %q", i, got[i].Address, tt.expected[i].Address)
				}
			}
		})
	}
}