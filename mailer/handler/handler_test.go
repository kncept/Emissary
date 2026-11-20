package handler

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/kncept/Emissary/mailer/auth"
	"github.com/kncept/Emissary/mailer/email"
	"github.com/kncept/Emissary/mailer/metadata"
	"github.com/kncept/Emissary/mailer/sender"
	"github.com/kncept/Emissary/mailer/session"
	"github.com/kncept/Emissary/mailer/storage"
	"github.com/kncept/Emissary/mailer/usermanager"
)

// EmailResponse is the test DTO used to decode email results from JMAP responses.
type EmailResponse struct {
	ID        string `json:"id"`
	From      string `json:"from"`
	To        string `json:"to"`
	Subject   string `json:"subject"`
	Body      string `json:"body"`
	Timestamp string `json:"timestamp"`
	Read      bool   `json:"read"`
	Flagged   bool   `json:"flagged"`
}

// failingDeleteMetadata delegates everything except Delete, which always
// fails, so a destroy can be tested against a metadata backend error.
type failingDeleteMetadata struct {
	metadata.MetadataStore
	err error
}

func (m *failingDeleteMetadata) Delete(_ context.Context, _, _ string) error { return m.err }

type testUserManager struct {
	adminUsername string
	adminPassword string
	domain        string
}

func (m *testUserManager) Name() string { return "test" }

func (m *testUserManager) Authenticate(_ context.Context, username, password string) (*usermanager.User, error) {
	if strings.EqualFold(strings.TrimSpace(username), m.adminUsername) {
		if m.adminPassword == "" || password != m.adminPassword {
			return nil, usermanager.ErrInvalidCredentials
		}
		return m.adminUser(), nil
	}
	return m.user(username)
}

func (m *testUserManager) Lookup(_ context.Context, username string) (*usermanager.User, error) {
	if strings.EqualFold(strings.TrimSpace(username), m.adminUsername) {
		return m.adminUser(), nil
	}
	return m.user(username)
}

func (m *testUserManager) Autocomplete(_ context.Context, q string) ([]string, error) {
	q = strings.TrimSpace(q)
	if q == "" {
		return []string{}, nil
	}
	return []string{q}, nil
}

func (m *testUserManager) adminUser() *usermanager.User {
	return &usermanager.User{Email: strings.ToLower(m.adminUsername), SystemManager: true}
}

func (m *testUserManager) user(username string) (*usermanager.User, error) {
	address := strings.ToLower(strings.TrimSpace(username))
	if address == "" || strings.Count(address, "@") != 1 || !strings.HasSuffix(address, "@"+m.domain) {
		return nil, usermanager.ErrInvalidCredentials
	}
	return &usermanager.User{Email: address}, nil
}

func newHandlerForTest(t *testing.T, adminPassword string) (http.Handler, *storage.MemoryStorage, *sender.InMemoryRecording) {
	t.Helper()
	store := storage.NewMemoryStorage()
	mailSender := sender.NewInMemoryRecording()
	h, err := New(HandlerConfig{
		AuthUser:     "admin",
		AuthPass:     adminPassword,
		JWTSecret:    "secret",
		JWTSecretTTL: time.Hour,
		MetadataType: "memory",
		Storage:      store,
		UserManager: &testUserManager{
			adminUsername: "admin",
			adminPassword: adminPassword,
			domain:        "example.com",
		},
		UserDomain: "example.com",
		Sender:     mailSender,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return h, store, mailSender
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
	token, _ := response["accessToken"].(string)
	return w.Code, token, response
}

func addEmail(store *storage.MemoryStorage, id, from string, to ...string) {
	recipients := make([]email.Address, 0, len(to))
	for _, address := range to {
		recipients = append(recipients, email.Address{Address: address})
	}
	accountID := ""
	if len(recipients) > 0 {
		accountID = recipients[0].Address
	}
	store.Add(&email.ListedEmail{
		Ref: &email.EmailRef{ID: id, AccountID: accountID, MailboxID: "inbox"},
		Email: &email.UserEmail{
			From:    email.Address{Address: from},
			To:      recipients,
			Subject: "subject " + id,
			Date:    time.Now(),
			Text:    "body " + id,
		},
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

// jmapRequest makes a JMAP batch request and returns the parsed response. An
// optional createdIds map is included in the request when supplied, including
// an empty map (which must still produce a createdIds property in the reply).
func jmapRequest(t *testing.T, h http.Handler, token string, using []string, methodCalls []JmapMethodCall, createdIDs ...map[string]string) map[string]interface{} {
	t.Helper()
	requestBody := map[string]interface{}{
		"using":       using,
		"methodCalls": methodCalls,
	}
	if len(createdIDs) > 0 {
		requestBody["createdIds"] = createdIDs[0]
	}
	body, _ := json.Marshal(requestBody)
	w := request(t, h, http.MethodPost, "/api/jmap/", string(body), token)
	if w.Code != http.StatusOK {
		t.Fatalf("JMAP request failed with status %d: %s", w.Code, w.Body.String())
	}
	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode JMAP response: %v", err)
	}
	return resp
}

type JmapMethodCall []interface{} // [methodName, args, clientId]

// submissionNotCreated returns the notCreated map from an EmailSubmission/set
// response in a JMAP batch result.
func submissionNotCreated(t *testing.T, resp map[string]interface{}) map[string]interface{} {
	t.Helper()
	responses, _ := resp["methodResponses"].([]interface{})
	for _, raw := range responses {
		call, ok := raw.([]interface{})
		if !ok || len(call) < 2 || call[0] != "EmailSubmission/set" {
			continue
		}
		result, _ := call[1].(map[string]interface{})
		notCreated, _ := result["notCreated"].(map[string]interface{})
		return notCreated
	}
	return nil
}

func getEmailsJMAP(t *testing.T, h http.Handler, token, accountID string) []EmailResponse {
	// First, query for email IDs
	queryResp := jmapRequest(t, h, token, []string{"urn:ietf:params:jmap:mail"}, []JmapMethodCall{
		{"Email/query", map[string]interface{}{
			"accountId":       accountID,
			"filter":          map[string]string{"inMailbox": "inbox"},
			"sort":            []interface{}{map[string]interface{}{"property": "receivedAt", "isAscending": false}},
			"collapseThreads": false,
		}, "q1"},
	})

	methodResponsesRaw, ok := queryResp["methodResponses"]
	if !ok {
		return []EmailResponse{}
	}
	methodResponses := methodResponsesRaw.([]interface{})
	var queryResult map[string]interface{}
	for _, mr := range methodResponses {
		call := mr.([]interface{})
		if len(call) >= 2 && call[0] == "Email/query" {
			queryResult = call[1].(map[string]interface{})
			break
		}
	}

	if queryResult == nil {
		return []EmailResponse{}
	}

	idsRaw, ok := queryResult["ids"]
	if !ok {
		return []EmailResponse{}
	}
	ids := idsRaw.([]interface{})
	idStrings := make([]string, len(ids))
	for i, id := range ids {
		idStrings[i] = id.(string)
	}

	if len(idStrings) == 0 {
		return []EmailResponse{}
	}

	// Now get the emails with the IDs
	getResp := jmapRequest(t, h, token, []string{"urn:ietf:params:jmap:mail"}, []JmapMethodCall{
		{"Email/get", map[string]interface{}{
			"accountId":  accountID,
			"ids":        idStrings,
			"properties": []string{"id", "from", "to", "subject", "preview", "receivedAt", "keywords", "mailboxIds"},
		}, "g1"},
	})

	methodResponsesRaw, ok = getResp["methodResponses"]
	if !ok {
		return []EmailResponse{}
	}
	methodResponses = methodResponsesRaw.([]interface{})
	var getRespObj map[string]interface{}
	for _, mr := range methodResponses {
		call := mr.([]interface{})
		if len(call) >= 2 && call[0] == "Email/get" {
			getRespObj = call[1].(map[string]interface{})
			break
		}
	}

	if getRespObj == nil {
		return []EmailResponse{}
	}

	listRaw, ok := getRespObj["list"]
	if !ok {
		return []EmailResponse{}
	}
	list, ok := listRaw.([]interface{})
	if !ok {
		return []EmailResponse{}
	}
	emails := make([]EmailResponse, len(list))
	for i, item := range list {
		e := item.(map[string]interface{})
		from := ""
		if fromArr, ok := e["from"].([]interface{}); ok && len(fromArr) > 0 {
			if f, ok := fromArr[0].(map[string]interface{}); ok {
				from = f["email"].(string)
			}
		}
		to := ""
		if toArr, ok := e["to"].([]interface{}); ok && len(toArr) > 0 {
			if t, ok := toArr[0].(map[string]interface{}); ok {
				to = t["email"].(string)
			}
		}
		keywords := map[string]bool{}
		if k, ok := e["keywords"].(map[string]interface{}); ok {
			for kk, vv := range k {
				keywords[kk] = vv.(bool)
			}
		}
		emails[i] = EmailResponse{
			ID:        e["id"].(string),
			From:      from,
			To:        to,
			Subject:   e["subject"].(string),
			Body:      e["preview"].(string),
			Timestamp: e["receivedAt"].(string),
			Read:      keywords["$seen"],
			Flagged:   keywords["$flagged"],
		}
	}
	return emails
}

func jmapSetKeywords(t *testing.T, h http.Handler, token, emailID string, keywords map[string]bool) {
	jmapRequest(t, h, token, []string{"urn:ietf:params:jmap:mail"}, []JmapMethodCall{
		{"Email/set", map[string]interface{}{
			"accountId": "bob@example.com",
			"update": map[string]interface{}{
				emailID: map[string]interface{}{"keywords": keywords},
			},
		}, "s1"},
	})
}

func jmapDelete(t *testing.T, h http.Handler, token, emailID string) {
	jmapRequest(t, h, token, []string{"urn:ietf:params:jmap:mail"}, []JmapMethodCall{
		{"Email/set", map[string]interface{}{
			"accountId": "bob@example.com",
			"destroy":   []string{emailID},
		}, "s1"},
	})
}

func TestListEmailsEmptyInboxReturnsArray(t *testing.T) {
	h, _, _ := newHandlerForTest(t, "secret")

	status, token, _ := login(t, h, "bob@example.com", "anything")
	if status != http.StatusOK {
		t.Fatalf("login status = %d, want %d", status, http.StatusOK)
	}

	emails := getEmailsJMAP(t, h, token, "bob@example.com")
	if len(emails) != 0 {
		t.Errorf("expected empty inbox, got %d emails", len(emails))
	}
}

func TestUserReadsOnlyOwnMailbox(t *testing.T) {
	h, store, _ := newHandlerForTest(t, "secret")
	addEmail(store, "received", "alice@example.com", "bob@example.com")
	addEmail(store, "sent", "bob@example.com", "carol@example.com")
	addEmail(store, "unrelated", "carol@example.com", "dave@example.com")

	status, token, _ := login(t, h, "bob@example.com", "anything")
	if status != http.StatusOK {
		t.Fatalf("login status = %d, want %d", status, http.StatusOK)
	}

	emails := getEmailsJMAP(t, h, token, "bob@example.com")
	ids := make(map[string]bool)
	for _, e := range emails {
		ids[e.ID] = true
	}
	// JMAP reads come from the requesting account's mailbox only.
	if len(ids) != 1 || !ids["received"] {
		t.Errorf("list ids = %v, want only bob's received email", ids)
	}
}

func TestUserManagerMasqueradesViaAccountID(t *testing.T) {
	h, store, _ := newHandlerForTest(t, "secret")
	addEmail(store, "received", "alice@example.com", "bob@example.com")
	addEmail(store, "sent", "bob@example.com", "carol@example.com")
	addEmail(store, "unrelated", "carol@example.com", "dave@example.com")

	token := signedSupportToken(t, map[string]any{
		"sub":                    "admin",
		"EmissaryUserManagement": true,
	})

	cases := map[string]string{
		"bob@example.com":   "received",
		"carol@example.com": "sent",
		"dave@example.com":  "unrelated",
		"admin":             "",
	}
	for accountID, wantID := range cases {
		emails := getEmailsJMAP(t, h, token, accountID)
		ids := make(map[string]bool)
		for _, e := range emails {
			ids[e.ID] = true
		}
		if wantID == "" {
			if len(ids) != 0 {
				t.Errorf("account %s ids = %v, want empty mailbox", accountID, ids)
			}
			continue
		}
		if len(ids) != 1 || !ids[wantID] {
			t.Errorf("account %s ids = %v, want only %s", accountID, ids, wantID)
		}
	}
}

func TestUserManagerClaimAllowsMasqueradeWithoutSystemManagerClaim(t *testing.T) {
	h, store, _ := newHandlerForTest(t, "secret")
	addEmail(store, "received", "alice@example.com", "bob@example.com")

	userManagerToken := signedSupportToken(t, map[string]any{
		"sub":                    "manager@example.com",
		"EmissaryUserManagement": true,
	})
	emails := getEmailsJMAP(t, h, userManagerToken, "bob@example.com")
	if len(emails) != 1 || emails[0].ID != "received" {
		t.Errorf("User Manager masquerade emails = %v, want bob's received email", emails)
	}

	systemManagerToken := signedSupportToken(t, map[string]any{
		"sub":           "manager@example.com",
		"systemManager": true,
	})
	emails = getEmailsJMAP(t, h, systemManagerToken, "bob@example.com")
	if len(emails) != 0 {
		t.Errorf("System Manager without User Manager could masquerade: emails = %v", emails)
	}

	legacyAdminToken := signedSupportToken(t, map[string]any{
		"sub":   "manager@example.com",
		"admin": true,
	})
	emails = getEmailsJMAP(t, h, legacyAdminToken, "bob@example.com")
	if len(emails) != 0 {
		t.Errorf("legacy admin-only token could masquerade: emails = %v", emails)
	}
}

func TestAutocompleteRequiresUserManagerClaim(t *testing.T) {
	h, _, _ := newHandlerForTest(t, "secret")

	userManagerToken := signedSupportToken(t, map[string]any{"EmissaryUserManagement": true})
	if w := request(t, h, http.MethodGet, "/api/user/autocomplete?q=alice", "", userManagerToken); w.Code != http.StatusOK {
		t.Errorf("User Manager autocomplete status = %d, want %d", w.Code, http.StatusOK)
	}

	systemManagerToken := signedSupportToken(t, map[string]any{"systemManager": true})
	if w := request(t, h, http.MethodGet, "/api/user/autocomplete?q=alice", "", systemManagerToken); w.Code != http.StatusForbidden {
		t.Errorf("System Manager without User Manager autocomplete status = %d, want %d", w.Code, http.StatusForbidden)
	}

	legacyAdminToken := signedSupportToken(t, map[string]any{"admin": true})
	if w := request(t, h, http.MethodGet, "/api/user/autocomplete?q=alice", "", legacyAdminToken); w.Code != http.StatusForbidden {
		t.Errorf("legacy admin-only autocomplete status = %d, want %d", w.Code, http.StatusForbidden)
	}
}

func TestNonAdminCannotReadAnotherAccount(t *testing.T) {
	h, store, _ := newHandlerForTest(t, "secret")
	addEmail(store, "private", "alice@example.com", "bob@example.com")

	_, token, _ := login(t, h, "bob@example.com", "anything")
	resp := jmapRequest(t, h, token, []string{"urn:ietf:params:jmap:mail"}, []JmapMethodCall{
		{"Email/query", map[string]interface{}{
			"accountId": "carol@example.com",
		}, "q1"},
	})
	methodResponses, _ := resp["methodResponses"].([]interface{})
	if len(methodResponses) != 1 {
		t.Fatalf("methodResponses = %v, want one error response", methodResponses)
	}
	call, ok := methodResponses[0].([]interface{})
	if !ok || len(call) < 2 {
		t.Fatalf("method response = %v, want [name, args]", methodResponses[0])
	}
	errObj, ok := call[1].(map[string]interface{})
	if !ok || errObj["type"] == nil {
		t.Fatalf("response = %v, want an error", call[1])
	}
}

func TestDeleteEmailVisibilityAndRemoval(t *testing.T) {
	h, store, _ := newHandlerForTest(t, "secret")
	addEmail(store, "received", "alice@example.com", "bob@example.com")
	addEmail(store, "unrelated", "carol@example.com", "dave@example.com")

	_, token, _ := login(t, h, "bob@example.com", "anything")

	// A message outside the caller's visibility is not deletable via JMAP Email/set
	// (would require JMAP Email/set destroy with the unrelated ID)

	// First get emails to find the received email ID
	emails := getEmailsJMAP(t, h, token, "bob@example.com")
	var receivedID string
	for _, e := range emails {
		if e.ID == "received" {
			receivedID = e.ID
			break
		}
	}
	if receivedID == "" {
		t.Fatal("received email not found")
	}

	// The unrelated message belongs to another account and is not visible here.
	ids := make(map[string]bool)
	for _, e := range emails {
		ids[e.ID] = true
	}
	if ids["unrelated"] {
		t.Error("unrelated email from another account was visible to bob")
	}

	// Deleting a message in bob's mailbox removes it from that mailbox only.
	jmapDelete(t, h, token, receivedID)

	emails = getEmailsJMAP(t, h, token, "bob@example.com")
	ids = make(map[string]bool)
	for _, e := range emails {
		ids[e.ID] = true
	}
	if ids["received"] {
		t.Error("deleted email still listed for owner")
	}

	// The other account's message is untouched.
	if _, err := store.GetEmail(context.Background(), "dave@example.com", "unrelated"); err != nil {
		t.Errorf("unrelated email should still exist: %v", err)
	}
}

func TestDeleteEmailAlsoDeletesMetadata(t *testing.T) {
	store := storage.NewMemoryStorage()
	metaStore := metadata.NewMemoryMetadata()
	addEmail(store, "received", "alice@example.com", "bob@example.com")

	h, err := New(HandlerConfig{
		AuthUser:     "admin",
		AuthPass:     "secret",
		JWTSecret:    "secret",
		JWTSecretTTL: time.Hour,
		Storage:      store,
		Metadata:     metaStore,
		UserManager: &testUserManager{
			adminUsername: "admin",
			adminPassword: "secret",
			domain:        "example.com",
		},
		UserDomain: "example.com",
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	_, token, _ := login(t, h, "bob@example.com", "anything")

	// First get emails to find the received email ID
	emails := getEmailsJMAP(t, h, token, "bob@example.com")
	var receivedID string
	for _, e := range emails {
		if e.ID == "received" {
			receivedID = e.ID
			break
		}
	}
	if receivedID == "" {
		t.Fatal("received email not found")
	}

	// Mark read and flagged via JMAP
	jmapSetKeywords(t, h, token, receivedID, map[string]bool{"$seen": true, "$flagged": true})

	// Delete via JMAP
	jmapDelete(t, h, token, receivedID)

	// Verify metadata is cleared
	meta, err := metaStore.Get(context.Background(), "bob@example.com", receivedID)
	if err != nil {
		t.Fatalf("metadata Get() error = %v", err)
	}
	if len(meta.Keywords) != 0 {
		t.Errorf("metadata after delete = %+v, want no keywords", meta)
	}
	list, err := metaStore.List(context.Background(), "bob@example.com")
	if err != nil {
		t.Fatalf("metadata List() error = %v", err)
	}
	if len(list) != 0 {
		t.Errorf("metadata list after delete = %v, want empty", list)
	}
}

func TestDeleteEmailKeepsMessageWhenMetadataDeleteFails(t *testing.T) {
	store := storage.NewMemoryStorage()
	metaStore := &failingDeleteMetadata{
		MetadataStore: metadata.NewMemoryMetadata(),
		err:           errors.New("metadata backend unavailable"),
	}
	addEmail(store, "received", "alice@example.com", "bob@example.com")

	h, err := New(HandlerConfig{
		AuthUser:     "admin",
		AuthPass:     "secret",
		JWTSecret:    "secret",
		JWTSecretTTL: time.Hour,
		Storage:      store,
		Metadata:     metaStore,
		UserManager: &testUserManager{
			adminUsername: "admin",
			adminPassword: "secret",
			domain:        "example.com",
		},
		UserDomain: "example.com",
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	_, token, _ := login(t, h, "bob@example.com", "anything")
	resp := jmapRequest(t, h, token, []string{"urn:ietf:params:jmap:mail"}, []JmapMethodCall{
		{"Email/set", map[string]interface{}{
			"accountId": "bob@example.com",
			"destroy":   []string{"received"},
		}, "s1"},
	})
	calls, _ := resp["methodResponses"].([]interface{})
	if len(calls) != 1 {
		t.Fatalf("methodResponses = %v, want one response", calls)
	}
	call, _ := calls[0].([]interface{})
	if call[0] != "Email/set" {
		t.Fatalf("method = %v, want Email/set", call[0])
	}
	args, _ := call[1].(map[string]interface{})
	if len(args["destroyed"].([]interface{})) != 0 {
		t.Errorf("destroyed = %v, want empty when metadata deletion fails", args["destroyed"])
	}
	notDestroyed, _ := args["notDestroyed"].(map[string]interface{})
	failure, ok := notDestroyed["received"].(map[string]interface{})
	if !ok || failure["type"] != "serverFail" {
		t.Errorf("notDestroyed = %v, want a serverFail for the email", notDestroyed)
	}

	// The message must remain so the client can retry the whole destroy once
	// the metadata backend recovers.
	if _, err := store.GetEmail(context.Background(), "bob@example.com", "received"); err != nil {
		t.Errorf("email was removed despite metadata delete failure: %v", err)
	}
}

func TestJWTIncludesUserIDAndSystemManagerOnlyForAdmin(t *testing.T) {
	h, _, _ := newHandlerForTest(t, "secret")

	_, adminToken, _ := login(t, h, "admin", "secret")
	_, userToken, _ := login(t, h, "alice@example.com", "")

	adminClaims := decodeClaims(t, adminToken)
	if adminClaims["sub"] != "admin" || adminClaims["systemManager"] != true {
		t.Errorf("admin claims = %v, want sub admin and systemManager true", adminClaims)
	}
	if _, ok := adminClaims["admin"]; ok {
		t.Errorf("obsolete admin claim must not be emitted: %v", adminClaims)
	}

	userClaims := decodeClaims(t, userToken)
	if userClaims["sub"] != "alice@example.com" {
		t.Errorf("user sub = %v, want alice@example.com", userClaims["sub"])
	}
	if _, ok := userClaims["systemManager"]; ok {
		t.Errorf("ordinary user claims should not include systemManager: %v", userClaims)
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
	h, _, _ := newHandlerForTest(t, "")

	if status, _, _ := login(t, h, "admin", ""); status != http.StatusUnauthorized {
		t.Errorf("admin login with blank configured password status = %d, want 401", status)
	}
	if status, _, _ := login(t, h, "bob@example.com", "anything"); status != http.StatusOK {
		t.Errorf("ordinary login status = %d, want 200", status)
	}
}

// TestSendEmailFromRules verifies that the From address is locked to the
// authenticated account (or the masquerade target account for a user
// manager) and that a client-supplied From is ignored.
func TestSendEmailFromRules(t *testing.T) {
	h, store, mailSender := newHandlerForTest(t, "secret")
	addEmail(store, "original", "alice@example.com", "bob@example.com")

	_, userToken, _ := login(t, h, "bob@example.com", "anything")

	// Bob's draft requests a forged From; the server stores and sends his
	// own address instead.
	createdEmailID := createDraftForSend(t, h, userToken, "bob@example.com", "carol@example.com", "dave@example.com")

	// Submit the email (ordinary user - From is locked to their own address)
	jmapRequest(t, h, userToken, []string{"urn:ietf:params:jmap:mail", "urn:ietf:params:jmap:submission"}, []JmapMethodCall{
		{"EmailSubmission/set", map[string]interface{}{
			"accountId": "bob@example.com",
			"create": map[string]interface{}{
				"s1": map[string]interface{}{
					"identityId": "primary",
					"emailId":    createdEmailID,
				},
			},
			"onSuccessUpdateEmail": map[string]interface{}{
				"keywords": map[string]bool{"$draft": false, "$sent": true},
			},
			"onSuccessDestroyEmail": []string{createdEmailID},
		}, "s1"},
	})

	if len(mailSender.Sent) == 0 {
		t.Fatal("no email was sent")
	}
	if got := mailSender.Sent[len(mailSender.Sent)-1].From.Address; got != "bob@example.com" {
		t.Errorf("ordinary user from = %q, want bob@example.com", got)
	}

	_, adminToken, _ := login(t, h, "admin", "secret")

	// A user manager masquerades as a different account: the From is locked
	// to the target account, not the caller.
	adminCreatedEmailID := createDraftForSend(t, h, adminToken, "carol@example.com", "admin@example.com", "alice@example.com")

	jmapRequest(t, h, adminToken, []string{"urn:ietf:params:jmap:mail", "urn:ietf:params:jmap:submission"}, []JmapMethodCall{
		{"EmailSubmission/set", map[string]interface{}{
			"accountId": "carol@example.com",
			"create": map[string]interface{}{
				"s1": map[string]interface{}{
					"identityId": "primary",
					"emailId":    adminCreatedEmailID,
				},
			},
			"onSuccessUpdateEmail": map[string]interface{}{
				"keywords": map[string]bool{"$draft": false, "$sent": true},
			},
			"onSuccessDestroyEmail": []string{adminCreatedEmailID},
		}, "s1"},
	})

	if len(mailSender.Sent) < 2 {
		t.Fatal("masqueraded email was not sent")
	}
	if got := mailSender.Sent[len(mailSender.Sent)-1].From.Address; got != "carol@example.com" {
		t.Errorf("masqueraded from = %q, want carol@example.com", got)
	}

	// Replying without an explicit From stays locked to the user's own
	// address. First get the original email to find its ID.
	emails := getEmailsJMAP(t, h, userToken, "bob@example.com")
	var originalID string
	for _, e := range emails {
		if e.ID == "original" {
			originalID = e.ID
			break
		}
	}
	if originalID == "" {
		t.Fatal("original email not found in admin inbox")
	}

	// Create reply email
	replyCreateResp := jmapRequest(t, h, userToken, []string{"urn:ietf:params:jmap:mail"}, []JmapMethodCall{
		{"Email/set", map[string]interface{}{
			"accountId": "bob@example.com",
			"create": map[string]interface{}{
				"c1": map[string]interface{}{
					"mailboxIds": map[string]bool{"drafts": true},
					"keywords":   map[string]bool{},
					"from":       []map[string]string{{"email": "alice@example.com", "name": ""}},
					"to":         []map[string]string{{"email": "alice@example.com", "name": ""}},
					"subject":    "re",
					"inReplyTo":  []string{originalID},
					"references": []string{originalID},
					"textBody":   []map[string]interface{}{{"partId": "text1", "type": "text/plain", "size": 4}},
					"bodyValues": map[string]interface{}{"text1": map[string]interface{}{"value": "body", "isEncodingProblem": false, "isTruncated": false}},
				},
			},
		}, "e1"},
	})

	var replyCreatedEmailID string
	methodResponsesRaw, _ := replyCreateResp["methodResponses"].([]interface{})
	for _, mr := range methodResponsesRaw {
		call := mr.([]interface{})
		if len(call) >= 2 && call[0] == "Email/set" {
			result := call[1].(map[string]interface{})
			if created, ok := result["created"].(map[string]interface{}); ok {
				for _, v := range created {
					if emailData, ok := v.(map[string]interface{}); ok {
						if id, ok := emailData["id"].(string); ok {
							replyCreatedEmailID = id
							break
						}
					}
				}
			}
			break
		}
	}
	if replyCreatedEmailID == "" {
		t.Fatal("failed to create reply email")
	}

	// Submit reply
	jmapRequest(t, h, userToken, []string{"urn:ietf:params:jmap:mail", "urn:ietf:params:jmap:submission"}, []JmapMethodCall{
		{"EmailSubmission/set", map[string]interface{}{
			"accountId": "bob@example.com",
			"create": map[string]interface{}{
				"s1": map[string]interface{}{
					"identityId": "primary",
					"emailId":    replyCreatedEmailID,
					// No envelope - From stays locked to bob
				},
			},
			"onSuccessUpdateEmail": map[string]interface{}{
				"keywords": map[string]bool{"$draft": false, "$sent": true},
			},
			"onSuccessDestroyEmail": []string{replyCreatedEmailID},
		}, "s1"},
	})

	if len(mailSender.Sent) < 3 {
		t.Fatal("reply email was not sent")
	}
	if got := mailSender.Sent[len(mailSender.Sent)-1].From.Address; got != "bob@example.com" {
		t.Errorf("reply from = %q, want bob@example.com", got)
	}
}

// createDraftForSend stores a draft through JMAP and returns its id.
func createDraftForSend(t *testing.T, h http.Handler, token, accountID, from, to string) string {
	t.Helper()
	resp := jmapRequest(t, h, token, []string{"urn:ietf:params:jmap:mail"}, []JmapMethodCall{
		{"Email/set", map[string]interface{}{
			"accountId": accountID,
			"create": map[string]interface{}{
				"c1": map[string]interface{}{
					"mailboxIds": map[string]bool{"drafts": true},
					"keywords":   map[string]bool{"$draft": true},
					"from":       []map[string]string{{"email": from, "name": ""}},
					"to":         []map[string]string{{"email": to, "name": ""}},
					"subject":    "from verification",
					"textBody":   []map[string]interface{}{{"partId": "text1", "type": "text/plain", "size": 4}},
					"bodyValues": map[string]interface{}{"text1": map[string]interface{}{"value": "body"}},
				},
			},
		}, "e1"},
	})
	responses, _ := resp["methodResponses"].([]interface{})
	for _, raw := range responses {
		call, ok := raw.([]interface{})
		if !ok || len(call) < 2 || call[0] != "Email/set" {
			continue
		}
		result, _ := call[1].(map[string]interface{})
		created, _ := result["created"].(map[string]interface{})
		for _, v := range created {
			if data, ok := v.(map[string]interface{}); ok {
				if id, ok := data["id"].(string); ok {
					return id
				}
			}
		}
	}
	t.Fatalf("failed to create draft: %v", resp)
	return ""
}

// TestSendEmailFromVerification verifies that a forged From in a draft is
// rewritten to the sender's own address at creation time, so submission
// always sends as the authenticated account (or the masquerade target).
func TestSendEmailFromVerification(t *testing.T) {
	h, store, capture := newHandlerForTest(t, "secret")
	_, bobToken, _ := login(t, h, "bob@example.com", "anything")
	_, adminToken, _ := login(t, h, "admin", "secret")

	// An ordinary user's forged From is replaced with their own address,
	// and the submission succeeds as that address.
	bobDraft := createDraftForSend(t, h, bobToken, "bob@example.com", "carol@example.com", "dave@example.com")
	// The stored draft itself carries the locked From.
	bobClaims := decodeClaims(t, bobToken)
	bobSID, _ := bobClaims["sid"].(string)
	storedDraft, err := store.GetDraft(context.Background(), bobSID, bobDraft)
	if err != nil || storedDraft == nil {
		t.Fatalf("failed to load stored draft: %v", err)
	}
	if got := storedDraft.From.Address; got != "bob@example.com" {
		t.Errorf("stored draft From = %q, want bob@example.com", got)
	}
	resp := jmapRequest(t, h, bobToken, []string{"urn:ietf:params:jmap:mail", "urn:ietf:params:jmap:submission"}, []JmapMethodCall{
		{"EmailSubmission/set", map[string]interface{}{
			"accountId": "bob@example.com",
			"create": map[string]interface{}{
				"s1": map[string]interface{}{"identityId": "primary", "emailId": bobDraft},
			},
		}, "s1"},
	})
	if notCreated := submissionNotCreated(t, resp); len(notCreated) > 0 {
		t.Fatalf("submission failed: %v", notCreated)
	}
	if len(capture.Sent) != 1 || capture.Sent[0].From.Address != "bob@example.com" {
		t.Fatalf("expected one message from bob@example.com, got %+v", capture.Sent)
	}

	// A user with masquerade permission sends as the target account.
	adminDraft := createDraftForSend(t, h, adminToken, "carol@example.com", "admin@example.com", "dave@example.com")
	resp = jmapRequest(t, h, adminToken, []string{"urn:ietf:params:jmap:mail", "urn:ietf:params:jmap:submission"}, []JmapMethodCall{
		{"EmailSubmission/set", map[string]interface{}{
			"accountId": "carol@example.com",
			"create": map[string]interface{}{
				"s1": map[string]interface{}{"identityId": "primary", "emailId": adminDraft},
			},
		}, "s1"},
	})
	if notCreated := submissionNotCreated(t, resp); len(notCreated) > 0 {
		t.Fatalf("masquerading submission failed: %v", notCreated)
	}
	if len(capture.Sent) != 2 || capture.Sent[1].From.Address != "carol@example.com" {
		t.Fatalf("expected second message from carol@example.com, got %+v", capture.Sent)
	}
}

// TestSendEmailWithCreationReference reproduces the SPA send flow: a single
// batch creates a draft with creation id "create1" and then submits it using
// the JMAP creation reference "#create1" instead of the real (server-assigned)
// id. Before creation references were resolved this failed with notFound.
func TestSendEmailWithCreationReference(t *testing.T) {
	h, store, capture := newHandlerForTest(t, "secret")
	_, bobToken, _ := login(t, h, "bob@example.com", "anything")
	bobClaims := decodeClaims(t, bobToken)
	bobSID, _ := bobClaims["sid"].(string)
	if bobSID == "" {
		t.Fatal("login token has no session id")
	}

	resp := jmapRequest(t, h, bobToken,
		[]string{"urn:ietf:params:jmap:mail", "urn:ietf:params:jmap:submission"},
		[]JmapMethodCall{
			{"Email/set", map[string]interface{}{
				"accountId": "bob@example.com",
				"create": map[string]interface{}{
					"create1": map[string]interface{}{
						"mailboxIds": map[string]bool{"drafts": true},
						"keywords":   map[string]bool{"$draft": true},
						"from":       []map[string]string{{"email": "bob@example.com", "name": ""}},
						"to":         []map[string]string{{"email": "dave@example.com", "name": ""}},
						"subject":    "creation reference",
						"textBody":   []map[string]interface{}{{"partId": "text1", "type": "text/plain", "size": 4}},
						"bodyValues": map[string]interface{}{"text1": map[string]interface{}{"value": "body"}},
					},
				},
			}, "emailCreate"},
			{"EmailSubmission/set", map[string]interface{}{
				"accountId": "bob@example.com",
				"create": map[string]interface{}{
					"submission1": map[string]interface{}{
						"identityId": "primary",
						"emailId":    "#create1",
					},
				},
				"onSuccessUpdateEmail": map[string]interface{}{
					"#submission1": map[string]interface{}{"keywords/$draft": nil},
				},
			}, "submissionCreate"},
		}, map[string]string{})

	responses, _ := resp["methodResponses"].([]interface{})
	for _, raw := range responses {
		call, _ := raw.([]interface{})
		if len(call) < 2 {
			continue
		}
		result, _ := call[1].(map[string]interface{})
		if call[0] == "error" {
			t.Fatalf("unexpected JMAP error: %v", result)
		}
		for _, key := range []string{"notCreated", "notUpdated", "notDestroyed"} {
			if problems, _ := result[key].(map[string]interface{}); len(problems) != 0 {
				t.Fatalf("%s returned %s: %v", call[0], key, problems)
			}
		}
	}

	if len(capture.Sent) != 1 {
		t.Fatalf("sent %d messages, want 1: %+v", len(capture.Sent), capture.Sent)
	}
	if got := capture.Sent[0].From.Address; got != "bob@example.com" {
		t.Errorf("sent From = %q, want bob@example.com", got)
	}

	createdIDs, _ := resp["createdIds"].(map[string]interface{})
	emailID, ok := createdIDs["create1"].(string)
	if !ok || emailID == "" || strings.HasPrefix(emailID, "#") {
		t.Fatalf("response createdIds = %v, want create1 mapped to a real id", createdIDs)
	}
	if got := createdIDs["submission1"]; got != emailID {
		t.Errorf("submission1 createdId = %v, want %q", got, emailID)
	}

	// onSuccessUpdateEmail must produce a second Email/set response with the
	// same method call id (RFC 8621 section 7.5), keyed by the Email id.
	if len(responses) != 3 {
		t.Fatalf("methodResponses = %d, want 3 (Email/set, EmailSubmission/set, implicit Email/set)", len(responses))
	}
	last, _ := responses[2].([]interface{})
	if len(last) < 3 || last[0] != "Email/set" || last[2] != "submissionCreate" {
		t.Fatalf("implicit response = %v, want Email/set with client id submissionCreate", last)
	}
	lastResult, _ := last[1].(map[string]interface{})
	updated, _ := lastResult["updated"].(map[string]interface{})
	if _, ok := updated[emailID]; !ok {
		t.Errorf("implicit Email/set updated = %v, want entry for %q", updated, emailID)
	}

	// The draft is session-scoped and must be removed after a successful send.
	if _, err := store.GetDraft(context.Background(), bobSID, emailID); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("GetDraft() error = %v, want %v", err, storage.ErrNotFound)
	}
}

// TestJMAPResultReference exercises RFC 8620 result references end to end.
// Core/echo returns its arguments unchanged, so the second call can reference
// the ids array from the first response without a separate round trip.
func TestJMAPResultReference(t *testing.T) {
	h, _, _ := newHandlerForTest(t, "secret")
	_, token, _ := login(t, h, "bob@example.com", "anything")
	using := []string{"urn:ietf:params:jmap:core"}

	resp := jmapRequest(t, h, token, using, []JmapMethodCall{
		{"Core/echo", map[string]interface{}{"ids": []string{"id-a", "id-b"}}, "echo1"},
		{"Core/echo", map[string]interface{}{
			"#ids": map[string]interface{}{
				"resultOf": "echo1",
				"name":     "Core/echo",
				"path":     "/ids/*",
			},
		}, "echo2"},
	})
	responses, _ := resp["methodResponses"].([]interface{})
	if len(responses) != 2 {
		t.Fatalf("methodResponses = %d, want 2: %v", len(responses), responses)
	}
	for i, raw := range responses {
		call, _ := raw.([]interface{})
		if len(call) < 2 || call[0] == "error" {
			t.Fatalf("methodResponses[%d] = %v, want Core/echo", i, raw)
		}
	}
	second, _ := responses[1].([]interface{})
	args, _ := second[1].(map[string]interface{})
	ids, _ := args["ids"].([]interface{})
	if len(ids) != 2 || ids[0] != "id-a" || ids[1] != "id-b" {
		t.Errorf("resolved ids = %v, want [id-a id-b]", ids)
	}
	if _, ok := args["#ids"]; ok {
		t.Errorf("#ids argument was not replaced: %v", args)
	}

	// A reference that cannot be resolved fails the method call, not the
	// whole batch.
	resp = jmapRequest(t, h, token, using, []JmapMethodCall{
		{"Core/echo", map[string]interface{}{"ids": []string{"id-a"}}, "echo1"},
		{"Core/echo", map[string]interface{}{
			"#ids": map[string]interface{}{
				"resultOf": "echo1",
				"name":     "Core/echo",
				"path":     "/missing/*",
			},
		}, "echo2"},
	})
	responses, _ = resp["methodResponses"].([]interface{})
	if len(responses) != 2 {
		t.Fatalf("methodResponses = %d, want 2: %v", len(responses), responses)
	}
	call, _ := responses[1].([]interface{})
	args, _ = call[1].(map[string]interface{})
	if call[0] != "error" || args["type"] != "invalidResultReference" {
		t.Errorf("second response = %v, want invalidResultReference error", responses[1])
	}
}

// TestJMAPCreatedIDsOnlyWhenRequested checks RFC 8620 section 3.4: a batch
// that creates records only receives createdIds if the request supplied it.
func TestJMAPCreatedIDsOnlyWhenRequested(t *testing.T) {
	h, _, _ := newHandlerForTest(t, "secret")
	_, token, _ := login(t, h, "bob@example.com", "anything")
	using := []string{"urn:ietf:params:jmap:mail"}

	createCall := JmapMethodCall{"Email/set", map[string]interface{}{
		"accountId": "bob@example.com",
		"create": map[string]interface{}{
			"create1": map[string]interface{}{
				"mailboxIds": map[string]bool{"drafts": true},
				"keywords":   map[string]bool{"$draft": true},
				"from":       []map[string]string{{"email": "bob@example.com", "name": ""}},
				"to":         []map[string]string{{"email": "dave@example.com", "name": ""}},
				"subject":    "conditional created ids",
				"textBody":   []map[string]interface{}{{"partId": "text1", "type": "text/plain", "size": 4}},
				"bodyValues": map[string]interface{}{"text1": map[string]interface{}{"value": "body"}},
			},
		},
	}, "e1"}

	resp := jmapRequest(t, h, token, using, []JmapMethodCall{createCall})
	if _, ok := resp["createdIds"]; ok {
		t.Errorf("response contains createdIds when the request omitted it: %v", resp["createdIds"])
	}
	responses, _ := resp["methodResponses"].([]interface{})
	if len(responses) != 1 {
		t.Fatalf("methodResponses = %d, want 1: %v", len(responses), responses)
	}
	call, _ := responses[0].([]interface{})
	result, _ := call[1].(map[string]interface{})
	created, _ := result["created"].(map[string]interface{})
	if len(created) != 1 {
		t.Fatalf("Email/set created = %v, want one record", created)
	}

	// Supplying an empty createdIds object opts in to receiving the map.
	resp = jmapRequest(t, h, token, using, []JmapMethodCall{createCall}, map[string]string{})
	createdIDs, ok := resp["createdIds"].(map[string]interface{})
	if !ok || createdIDs["create1"] == "" {
		t.Fatalf("createdIds = %v, want create1 mapped to a real id", resp["createdIds"])
	}

	// Even a read-only batch must echo createdIds. Use a fresh server so no
	// ids persisted from earlier requests in this test are included.
	fresh, _, _ := newHandlerForTest(t, "secret")
	_, freshToken, _ := login(t, fresh, "bob@example.com", "anything")
	resp = jmapRequest(t, fresh, freshToken, []string{"urn:ietf:params:jmap:core"}, []JmapMethodCall{
		{"Core/echo", map[string]interface{}{}, "echo1"},
	}, map[string]string{})
	createdIDs, ok = resp["createdIds"].(map[string]interface{})
	if !ok || len(createdIDs) != 0 {
		t.Fatalf("createdIds = %v, want an empty object echoed", resp["createdIds"])
	}
}

func TestSendEmailWithCCAndBCC(t *testing.T) {
	h, _, mailSender := newHandlerForTest(t, "secret")

	_, userToken, _ := login(t, h, "bob@example.com", "anything")

	// Test sending with CC and BCC using JMAP
	// First create the email
	ccEmailCreateResp := jmapRequest(t, h, userToken, []string{"urn:ietf:params:jmap:mail"}, []JmapMethodCall{
		{"Email/set", map[string]interface{}{
			"accountId": "bob@example.com",
			"create": map[string]interface{}{
				"c1": map[string]interface{}{
					"mailboxIds": map[string]bool{"drafts": true},
					"keywords":   map[string]bool{},
					"from":       []map[string]string{{"email": "", "name": ""}},
					"to":         []map[string]string{{"email": "dave@example.com", "name": ""}},
					"cc":         []map[string]string{{"email": "eve@example.com", "name": ""}, {"email": "frank@example.com", "name": ""}},
					"bcc":        []map[string]string{{"email": "george@example.com", "name": ""}},
					"subject":    "test cc bcc",
					"textBody":   []map[string]interface{}{{"partId": "text1", "type": "text/plain", "size": 4}},
					"bodyValues": map[string]interface{}{"text1": map[string]interface{}{"value": "body", "isEncodingProblem": false, "isTruncated": false}},
				},
			},
		}, "e1"},
	})

	var ccCreatedEmailID string
	methodResponsesRaw, _ := ccEmailCreateResp["methodResponses"].([]interface{})
	for _, mr := range methodResponsesRaw {
		call := mr.([]interface{})
		if len(call) >= 2 && call[0] == "Email/set" {
			result := call[1].(map[string]interface{})
			if created, ok := result["created"].(map[string]interface{}); ok {
				for _, v := range created {
					if emailData, ok := v.(map[string]interface{}); ok {
						if id, ok := emailData["id"].(string); ok {
							ccCreatedEmailID = id
							break
						}
					}
				}
			}
			break
		}
	}
	if ccCreatedEmailID == "" {
		t.Fatal("failed to create cc email")
	}

	// Now submit the email
	jmapRequest(t, h, userToken, []string{"urn:ietf:params:jmap:mail", "urn:ietf:params:jmap:submission"}, []JmapMethodCall{
		{"EmailSubmission/set", map[string]interface{}{
			"accountId": "bob@example.com",
			"create": map[string]interface{}{
				"s1": map[string]interface{}{
					"identityId": "primary",
					"emailId":    ccCreatedEmailID,
					"envelope": map[string]interface{}{
						"mailFrom": map[string]string{"email": ""},
						"rcptTo":   []map[string]string{{"email": "dave@example.com"}, {"email": "eve@example.com"}, {"email": "frank@example.com"}, {"email": "george@example.com"}},
					},
				},
			},
			"onSuccessUpdateEmail": map[string]interface{}{
				"keywords": map[string]bool{"$draft": false, "$sent": true},
			},
			"onSuccessDestroyEmail": []string{ccCreatedEmailID},
		}, "s1"},
	})

	if len(mailSender.Sent) == 0 {
		t.Fatal("no email was sent")
	}
	msg := mailSender.Sent[len(mailSender.Sent)-1]
	if len(msg.To) != 1 || msg.To[0].Address != "dave@example.com" {
		t.Errorf("to = %+v, want [dave@example.com]", msg.To)
	}
	if len(msg.Cc) != 2 || msg.Cc[0].Address != "eve@example.com" || msg.Cc[1].Address != "frank@example.com" {
		t.Errorf("cc = %+v, want [eve@example.com, frank@example.com]", msg.Cc)
	}
	if len(msg.Bcc) != 1 || msg.Bcc[0].Address != "george@example.com" {
		t.Errorf("bcc = %+v, want [george@example.com]", msg.Bcc)
	}

	// Test sending with multiple TO addresses
	// First create the email
	multiEmailCreateResp := jmapRequest(t, h, userToken, []string{"urn:ietf:params:jmap:mail"}, []JmapMethodCall{
		{"Email/set", map[string]interface{}{
			"accountId": "bob@example.com",
			"create": map[string]interface{}{
				"c1": map[string]interface{}{
					"mailboxIds": map[string]bool{"drafts": true},
					"keywords":   map[string]bool{},
					"from":       []map[string]string{{"email": "", "name": ""}},
					"to":         []map[string]string{{"email": "alice@example.com", "name": ""}, {"email": "carol@example.com", "name": ""}},
					"subject":    "multi-to",
					"textBody":   []map[string]interface{}{{"partId": "text1", "type": "text/plain", "size": 4}},
					"bodyValues": map[string]interface{}{"text1": map[string]interface{}{"value": "body", "isEncodingProblem": false, "isTruncated": false}},
				},
			},
		}, "e1"},
	})

	var multiCreatedEmailID string
	methodResponsesRaw, _ = multiEmailCreateResp["methodResponses"].([]interface{})
	for _, mr := range methodResponsesRaw {
		call := mr.([]interface{})
		if len(call) >= 2 && call[0] == "Email/set" {
			result := call[1].(map[string]interface{})
			if created, ok := result["created"].(map[string]interface{}); ok {
				for _, v := range created {
					if emailData, ok := v.(map[string]interface{}); ok {
						if id, ok := emailData["id"].(string); ok {
							multiCreatedEmailID = id
							break
						}
					}
				}
			}
			break
		}
	}
	if multiCreatedEmailID == "" {
		t.Fatal("failed to create multi-to email")
	}

	// Now submit the email
	jmapRequest(t, h, userToken, []string{"urn:ietf:params:jmap:mail", "urn:ietf:params:jmap:submission"}, []JmapMethodCall{
		{"EmailSubmission/set", map[string]interface{}{
			"accountId": "bob@example.com",
			"create": map[string]interface{}{
				"s1": map[string]interface{}{
					"identityId": "primary",
					"emailId":    multiCreatedEmailID,
					"envelope": map[string]interface{}{
						"mailFrom": map[string]string{"email": ""},
						"rcptTo":   []map[string]string{{"email": "alice@example.com"}, {"email": "carol@example.com"}},
					},
				},
			},
			"onSuccessUpdateEmail": map[string]interface{}{
				"keywords": map[string]bool{"$draft": false, "$sent": true},
			},
			"onSuccessDestroyEmail": []string{multiCreatedEmailID},
		}, "s1"},
	})

	if len(mailSender.Sent) < 2 {
		t.Fatal("second email was not sent")
	}
	msg = mailSender.Sent[len(mailSender.Sent)-1]
	if len(msg.To) != 2 || msg.To[0].Address != "alice@example.com" || msg.To[1].Address != "carol@example.com" {
		t.Errorf("multi to = %+v, want [alice@example.com, carol@example.com]", msg.To)
	}
}

func TestJMAPAccountIdValidationNonAdmin(t *testing.T) {
	h, _, _ := newHandlerForTest(t, "secret")

	// Login as bob (non-admin)
	_, bobToken, _ := login(t, h, "bob@example.com", "anything")

	// Try to access alice's account - should fail
	resp := jmapRequest(t, h, bobToken, []string{"urn:ietf:params:jmap:mail"}, []JmapMethodCall{
		{"Email/query", map[string]interface{}{
			"accountId": "alice@example.com",
			"filter":    map[string]string{"inMailbox": "inbox"},
		}, "q1"},
	})

	// Should get an error response
	methodResponsesRaw, ok := resp["methodResponses"].([]interface{})
	if !ok {
		t.Fatalf("no methodResponses in response: %v", resp)
	}
	call := methodResponsesRaw[0].([]interface{})
	if call[0] != "error" {
		t.Errorf("expected error response, got %v", call[0])
	}
	errorArgs := call[1].(map[string]interface{})
	if errorArgs["type"] != "forbidden" {
		t.Errorf("error type = %v, want forbidden", errorArgs["type"])
	}
}

func TestJMAPAccountIdValidationAdmin(t *testing.T) {
	h, store, _ := newHandlerForTest(t, "secret")
	addEmail(store, "alice-email", "sender@example.com", "alice@example.com")

	// Login as admin
	_, adminToken, _ := login(t, h, "admin", "secret")

	// Admin should be able to access alice's account
	resp := jmapRequest(t, h, adminToken, []string{"urn:ietf:params:jmap:mail"}, []JmapMethodCall{
		{"Email/query", map[string]interface{}{
			"accountId": "alice@example.com",
			"filter":    map[string]string{"inMailbox": "inbox"},
		}, "q1"},
	})

	// Should succeed
	methodResponsesRaw, ok := resp["methodResponses"].([]interface{})
	if !ok {
		t.Fatalf("no methodResponses in response: %v", resp)
	}
	call := methodResponsesRaw[0].([]interface{})
	if call[0] == "error" {
		t.Errorf("admin should be able to access alice's account, got error: %v", call[1])
	}
	if call[0] != "Email/query" {
		t.Errorf("expected Email/query response, got %v", call[0])
	}
}

func TestRawEmailEndpoint(t *testing.T) {
	h, store, _ := newHandlerForTest(t, "secret")
	const id = "users/alice@example.com/inbox/20261109T100000_ses-id.eml"
	addEmail(store, id, "sender@example.com", "alice@example.com")
	rawURL := "/api/email/raw?id=" + url.QueryEscape(id)

	t.Run("requires authentication", func(t *testing.T) {
		w := request(t, h, http.MethodGet, rawURL, "", "")
		if w.Code != http.StatusUnauthorized {
			t.Errorf("status = %d, want 401", w.Code)
		}
	})

	t.Run("returns the raw message to the owning account", func(t *testing.T) {
		_, token, _ := login(t, h, "alice@example.com", "")
		w := request(t, h, http.MethodGet, rawURL, "", token)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body %s)", w.Code, w.Body.String())
		}
		if got := w.Header().Get("Content-Type"); got != "message/rfc822" {
			t.Errorf("Content-Type = %q, want message/rfc822", got)
		}
		body := w.Body.String()
		for _, want := range []string{"From: sender@example.com", "To: alice@example.com", "Subject: subject " + id, "body " + id} {
			if !strings.Contains(body, want) {
				t.Errorf("raw body = %q, want it to contain %q", body, want)
			}
		}
	})

	t.Run("rejects another account", func(t *testing.T) {
		_, token, _ := login(t, h, "bob@example.com", "")
		w := request(t, h, http.MethodGet, rawURL, "", token)
		if w.Code != http.StatusForbidden {
			t.Errorf("status = %d, want 403 (body %s)", w.Code, w.Body.String())
		}
	})

	t.Run("allows a user manager", func(t *testing.T) {
		_, token, _ := login(t, h, "admin", "secret")
		w := request(t, h, http.MethodGet, rawURL, "", token)
		if w.Code != http.StatusOK {
			t.Errorf("status = %d, want 200 (body %s)", w.Code, w.Body.String())
		}
	})

	t.Run("returns 404 for a missing message", func(t *testing.T) {
		_, token, _ := login(t, h, "alice@example.com", "")
		missing := "/api/email/raw?id=" + url.QueryEscape("users/alice@example.com/inbox/missing.eml")
		w := request(t, h, http.MethodGet, missing, "", token)
		if w.Code != http.StatusNotFound {
			t.Errorf("status = %d, want 404 (body %s)", w.Code, w.Body.String())
		}
	})

	t.Run("requires an id", func(t *testing.T) {
		_, token, _ := login(t, h, "alice@example.com", "")
		w := request(t, h, http.MethodGet, "/api/email/raw", "", token)
		if w.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400 (body %s)", w.Code, w.Body.String())
		}
	})
}

// newHandlerWithClockForTest builds the real handler with an authenticator
// whose clock can be advanced, for tests of token lifetimes.
func newHandlerWithClockForTest(t *testing.T, adminPassword string, now func() time.Time) (http.Handler, *auth.Authenticator) {
	t.Helper()
	users := &testUserManager{
		adminUsername: "admin",
		adminPassword: adminPassword,
		domain:        "example.com",
	}
	store := storage.NewMemoryStorage()
	sessions := session.NewMemorySessionStore()
	mailSender := sender.NewInMemoryRecording()
	authenticator := auth.NewWithUserManager(users, sessions, "secret", time.Hour, "", "", "admin")
	authenticator.SetNow(now)
	cfg := HandlerConfig{
		AuthUser:     "admin",
		AuthPass:     adminPassword,
		JWTSecret:    "secret",
		JWTSecretTTL: time.Hour,
		MetadataType: "memory",
		Storage:      store,
		UserManager:  users,
		UserDomain:   "example.com",
		Sender:       mailSender,
	}
	return newHandler(authenticator, store, metadata.NewMemoryMetadata(), sessions, mailSender, users, cfg), authenticator
}

func decodeAuthClaims(t *testing.T, token string) auth.Claims {
	t.Helper()
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("token %q is not a JWT", token)
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("decode token payload: %v", err)
	}
	var claims auth.Claims
	if err := json.Unmarshal(payload, &claims); err != nil {
		t.Fatalf("unmarshal token claims: %v", err)
	}
	return claims
}

func refreshRequest(t *testing.T, h http.Handler, refreshToken string) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"refreshToken": refreshToken})
	return request(t, h, http.MethodPost, "/api/auth/refresh", string(body), "")
}

func TestRefreshEndpoint(t *testing.T) {
	now := time.Date(2099, 1, 1, 12, 0, 0, 0, time.UTC)
	h, _ := newHandlerWithClockForTest(t, "secret", func() time.Time { return now })

	status, _, response := login(t, h, "admin", "secret")
	if status != http.StatusOK {
		t.Fatalf("login status = %d, want 200 (body %v)", status, response)
	}
	firstRefresh, _ := response["refreshToken"].(string)
	if firstRefresh == "" {
		t.Fatal("login response has no refreshToken")
	}

	t.Run("early refresh is rejected", func(t *testing.T) {
		w := refreshRequest(t, h, firstRefresh)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("status = %d, want 401 (body %s)", w.Code, w.Body.String())
		}
	})

	now = now.Add(8 * time.Hour)
	w := refreshRequest(t, h, firstRefresh)
	if w.Code != http.StatusOK {
		t.Fatalf("refresh status = %d, want 200 (body %s)", w.Code, w.Body.String())
	}
	var refreshed map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &refreshed); err != nil {
		t.Fatalf("decode refresh response: %v (body %s)", err, w.Body.String())
	}
	nextAccess, _ := refreshed["accessToken"].(string)
	nextRefresh, _ := refreshed["refreshToken"].(string)
	if nextAccess == "" || nextRefresh == "" {
		t.Fatalf("refresh response missing tokens: %v", refreshed)
	}
	if nextAccess == response["accessToken"] || nextRefresh == firstRefresh {
		t.Errorf("refresh reused a token from the previous pair: %v", refreshed)
	}

	claims := decodeAuthClaims(t, nextRefresh)
	originalClaims := decodeAuthClaims(t, firstRefresh)
	if claims.SID != originalClaims.SID {
		t.Errorf("session id changed: %q -> %q", originalClaims.SID, claims.SID)
	}
	if claims.Issue != originalClaims.Issue+1 {
		t.Errorf("issue = %d, want %d", claims.Issue, originalClaims.Issue+1)
	}

	t.Run("new access token works and session id is unchanged", func(t *testing.T) {
		w := request(t, h, http.MethodGet, "/.well-known/jmap", "", nextAccess)
		if w.Code != http.StatusOK {
			t.Errorf("status = %d, want 200 (body %s)", w.Code, w.Body.String())
		}
	})

	t.Run("spent refresh token is rejected", func(t *testing.T) {
		w := refreshRequest(t, h, firstRefresh)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("status = %d, want 401 (body %s)", w.Code, w.Body.String())
		}
	})

	t.Run("current refresh token works after its not-before", func(t *testing.T) {
		now = now.Add(8 * time.Hour)
		w := refreshRequest(t, h, nextRefresh)
		if w.Code != http.StatusOK {
			t.Errorf("status = %d, want 200 (body %s)", w.Code, w.Body.String())
		}
	})

	t.Run("missing refresh token is rejected", func(t *testing.T) {
		w := request(t, h, http.MethodPost, "/api/auth/refresh", `{}`, "")
		if w.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400 (body %s)", w.Code, w.Body.String())
		}
	})

	t.Run("invalid refresh token is rejected", func(t *testing.T) {
		w := refreshRequest(t, h, "not-a-jwt")
		if w.Code != http.StatusUnauthorized {
			t.Errorf("status = %d, want 401 (body %s)", w.Code, w.Body.String())
		}
	})
}

func TestLogoutEndpoint(t *testing.T) {
	now := time.Date(2099, 1, 1, 12, 0, 0, 0, time.UTC)
	h, authenticator := newHandlerWithClockForTest(t, "secret", func() time.Time { return now })

	t.Run("requires authentication", func(t *testing.T) {
		w := request(t, h, http.MethodPost, "/api/auth/logout", "", "")
		if w.Code != http.StatusUnauthorized {
			t.Errorf("status = %d, want 401 (body %s)", w.Code, w.Body.String())
		}
	})

	status, accessToken, response := login(t, h, "admin", "secret")
	if status != http.StatusOK {
		t.Fatalf("login status = %d, want 200 (body %v)", status, response)
	}
	refreshToken, _ := response["refreshToken"].(string)
	claims, err := authenticator.ValidateClaims(accessToken)
	if err != nil {
		t.Fatalf("ValidateClaims() error = %v", err)
	}

	w := request(t, h, http.MethodPost, "/api/auth/logout", "", accessToken)
	if w.Code != http.StatusOK {
		t.Fatalf("logout status = %d, want 200 (body %s)", w.Code, w.Body.String())
	}
	var logoutResponse map[string]bool
	if err := json.Unmarshal(w.Body.Bytes(), &logoutResponse); err != nil {
		t.Fatalf("decode logout response: %v (body %s)", err, w.Body.String())
	}
	if logoutResponse["success"] != true {
		t.Errorf("logout success = %v, want true", logoutResponse["success"])
	}
	if _, err := authenticator.SessionStore().Get(context.Background(), claims.SID); err == nil {
		t.Error("session still present after logout")
	}

	t.Run("refresh token is revoked", func(t *testing.T) {
		now = now.Add(8 * time.Hour)
		w := refreshRequest(t, h, refreshToken)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("status = %d, want 401 (body %s)", w.Code, w.Body.String())
		}
	})

	t.Run("access token is still valid until expiry", func(t *testing.T) {
		w := request(t, h, http.MethodGet, "/.well-known/jmap", "", accessToken)
		if w.Code != http.StatusOK {
			t.Errorf("status = %d, want 200 (body %s)", w.Code, w.Body.String())
		}
	})

	t.Run("repeat logout succeeds", func(t *testing.T) {
		w := request(t, h, http.MethodPost, "/api/auth/logout", "", accessToken)
		if w.Code != http.StatusOK {
			t.Errorf("status = %d, want 200 (body %s)", w.Code, w.Body.String())
		}
	})
}
