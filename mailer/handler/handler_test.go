package handler

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kncept/Emissary/mailer/email"
	"github.com/kncept/Emissary/mailer/storage"
)

type captureSender struct {
	from     string
	messages []*email.Email
}

func (s *captureSender) Name() string { return "capture" }
func (s *captureSender) From() string { return s.from }
func (s *captureSender) Send(_ context.Context, message *email.Email) error {
	s.messages = append(s.messages, message)
	return nil
}

func newHandlerForTest(t *testing.T, managerType, domain, adminPassword string) (http.Handler, *storage.MemoryStorage, *captureSender) {
	t.Helper()
	store := storage.NewMemoryStorage()
	sender := &captureSender{from: "default@example.com"}
	h, err := New(HandlerConfig{
		AuthUser:        "admin",
		AuthPass:        adminPassword,
		JWTSecret:       "secret",
		JWTSecretTTL:    time.Hour,
		MetadataType:    "memory",
		Storage:         store,
		UserManagerType: managerType,
		UserDomain:      domain,
		Sender:          sender,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return h, store, sender
}

func request(t *testing.T, h http.Handler, method, target, body, token string) *httptest.ResponseRecorder {
	t.Helper()
	var reader *strings.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	var req *http.Request
	if reader != nil {
		req = httptest.NewRequest(method, target, reader)
	} else {
		req = httptest.NewRequest(method, target, nil)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

func login(t *testing.T, h http.Handler, username, password string) (int, string, map[string]any) {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"username": username, "password": password})
	w := request(t, h, http.MethodPost, "/api/auth/login", string(body), "")
	var response map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode login response: %v (body %s)", err, w.Body.String())
	}
	token, _ := response["token"].(string)
	return w.Code, token, response
}

func addEmail(store *storage.MemoryStorage, id, from string, to ...string) {
	recipients := make([]email.Address, 0, len(to))
	for _, address := range to {
		recipients = append(recipients, email.Address{Address: address})
	}
	store.Add(&email.Email{
		ID:      id,
		From:    email.Address{Address: from},
		To:      recipients,
		Subject: "subject " + id,
		Date:    time.Now(),
		Text:    "body " + id,
	})
}

func responseIDs(t *testing.T, w *httptest.ResponseRecorder) map[string]bool {
	t.Helper()
	var emails []EmailResponse
	if err := json.Unmarshal(w.Body.Bytes(), &emails); err != nil {
		t.Fatalf("decode email list: %v (body %s)", err, w.Body.String())
	}
	ids := make(map[string]bool, len(emails))
	for _, message := range emails {
		ids[message.ID] = true
	}
	return ids
}

func TestUserModeFiltersEmailsByOwner(t *testing.T) {
	h, store, _ := newHandlerForTest(t, "any", "example.com", "secret")
	addEmail(store, "received", "alice@example.com", "bob@example.com")
	addEmail(store, "sent", "bob@example.com", "carol@example.com")
	addEmail(store, "unrelated", "carol@example.com", "dave@example.com")

	status, token, _ := login(t, h, "bob@example.com", "anything")
	if status != http.StatusOK {
		t.Fatalf("login status = %d, want %d", status, http.StatusOK)
	}

	w := request(t, h, http.MethodGet, "/api/emails", "", token)
	if w.Code != http.StatusOK {
		t.Fatalf("list status = %d, want %d", w.Code, http.StatusOK)
	}
	ids := responseIDs(t, w)
	if !ids["received"] || !ids["sent"] {
		t.Errorf("list ids = %v, want received and sent", ids)
	}
	if ids["unrelated"] {
		t.Error("ordinary user must not see unrelated email")
	}

	if w := request(t, h, http.MethodGet, "/api/emails/unrelated", "", token); w.Code != http.StatusNotFound {
		t.Errorf("get unrelated status = %d, want 404", w.Code)
	}
	if w := request(t, h, http.MethodPost, "/api/emails/unrelated/read", `{"read":true}`, token); w.Code != http.StatusNotFound {
		t.Errorf("mark unrelated status = %d, want 404", w.Code)
	}
}

func TestAdminCanFilterAndSeeAllUsers(t *testing.T) {
	h, store, _ := newHandlerForTest(t, "any", "example.com", "secret")
	addEmail(store, "received", "alice@example.com", "bob@example.com")
	addEmail(store, "sent", "bob@example.com", "carol@example.com")
	addEmail(store, "unrelated", "carol@example.com", "dave@example.com")

	status, token, response := login(t, h, "admin", "secret")
	if status != http.StatusOK {
		t.Fatalf("admin login status = %d, want %d", status, http.StatusOK)
	}
	if response["admin"] != true {
		t.Errorf("admin flag = %v, want true", response["admin"])
	}

	ids := responseIDs(t, request(t, h, http.MethodGet, "/api/emails", "", token))
	if len(ids) != 3 {
		t.Errorf("admin unfiltered ids = %v, want all three", ids)
	}

	ids = responseIDs(t, request(t, h, http.MethodGet, "/api/emails?user=bob@example.com", "", token))
	if len(ids) != 2 || !ids["received"] || !ids["sent"] {
		t.Errorf("admin filtered ids = %v, want received and sent", ids)
	}

	if w := request(t, h, http.MethodGet, "/api/emails/unrelated", "", token); w.Code != http.StatusOK {
		t.Errorf("admin get unrelated status = %d, want 200", w.Code)
	}
}

func TestJWTIncludesUserIDAndAdminOnlyForAdmin(t *testing.T) {
	h, _, _ := newHandlerForTest(t, "any", "example.com", "secret")

	_, adminToken, _ := login(t, h, "admin", "secret")
	_, userToken, _ := login(t, h, "alice@example.com", "")

	adminClaims := decodeClaims(t, adminToken)
	if adminClaims["sub"] != "admin" || adminClaims["admin"] != true {
		t.Errorf("admin claims = %v, want sub admin and admin true", adminClaims)
	}

	userClaims := decodeClaims(t, userToken)
	if userClaims["sub"] != "alice@example.com" {
		t.Errorf("user sub = %v, want alice@example.com", userClaims["sub"])
	}
	if _, ok := userClaims["admin"]; ok {
		t.Errorf("ordinary user claims should not include admin: %v", userClaims)
	}
}

func decodeClaims(t *testing.T, token string) map[string]any {
	t.Helper()
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("token has %d parts, want 3", len(parts))
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("decode token payload: %v", err)
	}
	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil {
		t.Fatalf("unmarshal token payload: %v", err)
	}
	return claims
}

func TestBlankAdminPasswordDisablesAdminLogin(t *testing.T) {
	h, _, _ := newHandlerForTest(t, "any", "example.com", "")

	if status, _, _ := login(t, h, "admin", ""); status != http.StatusUnauthorized {
		t.Errorf("admin login with blank configured password status = %d, want 401", status)
	}
	if status, _, _ := login(t, h, "bob@example.com", "anything"); status != http.StatusOK {
		t.Errorf("ordinary login status = %d, want 200", status)
	}
}

func TestSendEmailFromRules(t *testing.T) {
	h, store, sender := newHandlerForTest(t, "any", "example.com", "secret")
	addEmail(store, "original", "alice@example.com", "bob@example.com")

	_, userToken, _ := login(t, h, "bob@example.com", "anything")
	w := request(t, h, http.MethodPost, "/api/emails/send",
		`{"from":"carol@example.com","to":"dave@example.com","subject":"hello","body":"body"}`, userToken)
	if w.Code != http.StatusOK {
		t.Fatalf("user send status = %d, want 200 (body %s)", w.Code, w.Body.String())
	}
	if got := sender.messages[len(sender.messages)-1].From.Address; got != "bob@example.com" {
		t.Errorf("ordinary user from = %q, want bob@example.com", got)
	}

	_, adminToken, _ := login(t, h, "admin", "secret")
	w = request(t, h, http.MethodPost, "/api/emails/send",
		`{"from":"dave@example.com","to":"alice@example.com","subject":"hello","body":"body"}`, adminToken)
	if w.Code != http.StatusOK {
		t.Fatalf("admin send status = %d, want 200 (body %s)", w.Code, w.Body.String())
	}
	if got := sender.messages[len(sender.messages)-1].From.Address; got != "dave@example.com" {
		t.Errorf("admin from = %q, want dave@example.com", got)
	}

	// Replying without an explicit From defaults to the original recipient.
	w = request(t, h, http.MethodPost, "/api/emails/send",
		`{"to":"alice@example.com","subject":"re","body":"body","replyToId":"original"}`, adminToken)
	if w.Code != http.StatusOK {
		t.Fatalf("admin reply status = %d, want 200 (body %s)", w.Code, w.Body.String())
	}
	if got := sender.messages[len(sender.messages)-1].From.Address; got != "bob@example.com" {
		t.Errorf("admin reply from = %q, want bob@example.com", got)
	}
}
