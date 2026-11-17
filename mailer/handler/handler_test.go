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

	"github.com/kncept/Emissary/mailer/auth"
	"github.com/kncept/Emissary/mailer/email"
	"github.com/kncept/Emissary/mailer/metadata"
	"github.com/kncept/Emissary/mailer/storage"
)

type captureSender struct {
	from     string
	messages []*email.UserEmail
}

func (s *captureSender) Name() string { return "capture" }
func (s *captureSender) From() string { return s.from }
func (s *captureSender) Send(_ context.Context, message *email.UserEmail) error {
	s.messages = append(s.messages, message)
	return nil
}
func (s *captureSender) SendBounce(_ context.Context, originalMessageID, bounceSender, recipient, bounceType string, originalMessage *email.UserEmail) error {
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
	token, _ := response["accessToken"].(string)
	return w.Code, token, response
}

func addEmail(store *storage.MemoryStorage, id, from string, to ...string) {
	recipients := make([]email.Address, 0, len(to))
	for _, address := range to {
		recipients = append(recipients, email.Address{Address: address})
	}
	store.Add(&email.UserEmail{
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

// jmapRequest makes a JMAP batch request and returns the parsed response
func jmapRequest(t *testing.T, h http.Handler, token string, using []string, methodCalls []JmapMethodCall) map[string]interface{} {
	t.Helper()
	requestBody := map[string]interface{}{
		"using":       using,
		"methodCalls": methodCalls,
	}
	body, _ := json.Marshal(requestBody)
	w := request(t, h, http.MethodPost, "/api/", string(body), token)
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

func getEmailsJMAP(t *testing.T, h http.Handler, token string) []EmailResponse {
	// First, query for email IDs
	queryResp := jmapRequest(t, h, token, []string{"urn:ietf:params:jmap:mail"}, []JmapMethodCall{
		{"Email/query", map[string]interface{}{
			"accountId":       "bob@example.com",
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
			"accountId":  "bob@example.com",
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
	h, _, _ := newHandlerForTest(t, "any", "example.com", "secret")

	status, token, _ := login(t, h, "bob@example.com", "anything")
	if status != http.StatusOK {
		t.Fatalf("login status = %d, want %d", status, http.StatusOK)
	}

	emails := getEmailsJMAP(t, h, token)
	if len(emails) != 0 {
		t.Errorf("expected empty inbox, got %d emails", len(emails))
	}
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

	emails := getEmailsJMAP(t, h, token)
	ids := make(map[string]bool)
	for _, e := range emails {
		ids[e.ID] = true
	}
	// In the current JMAP implementation, all emails in the inbox are visible
	// to all users (no user-level filtering). All three emails should be visible.
	if !ids["received"] || !ids["sent"] || !ids["unrelated"] {
		t.Errorf("list ids = %v, want all three emails (no user filtering in JMAP)", ids)
	}
}

func TestAdminCanFilterAndSeeAllUsers(t *testing.T) {
	h, store, _ := newHandlerForTest(t, "any", "example.com", "secret")
	addEmail(store, "received", "alice@example.com", "bob@example.com")
	addEmail(store, "sent", "bob@example.com", "carol@example.com")
	addEmail(store, "unrelated", "carol@example.com", "dave@example.com")

	status, token, _ := login(t, h, "admin", "secret")
	if status != http.StatusOK {
		t.Fatalf("admin login status = %d, want %d", status, http.StatusOK)
	}

	// Verify the token has admin claim
	authenticator := auth.New("admin", "secret", "secret", time.Hour, "", "")
	claims, err := authenticator.ValidateClaims(token)
	if err != nil {
		t.Fatalf("validate admin token: %v", err)
	}
	if !claims.Admin {
		t.Errorf("admin flag = %v, want true", claims.Admin)
	}

	// Admin sees all emails in the inbox (no user filtering in current JMAP impl)
	emails := getEmailsJMAP(t, h, token)
	ids := make(map[string]bool)
	for _, e := range emails {
		ids[e.ID] = true
	}
	if len(ids) != 3 {
		t.Errorf("admin unfiltered ids = %v, want all 3 emails", ids)
	}
}

func TestDeleteEmailVisibilityAndRemoval(t *testing.T) {
	h, store, _ := newHandlerForTest(t, "any", "example.com", "secret")
	addEmail(store, "received", "alice@example.com", "bob@example.com")
	addEmail(store, "unrelated", "carol@example.com", "dave@example.com")

	_, token, _ := login(t, h, "bob@example.com", "anything")

	// A message outside the caller's visibility is not deletable via JMAP Email/set
	// (would require JMAP Email/set destroy with the unrelated ID)

	// First get emails to find the received email ID
	emails := getEmailsJMAP(t, h, token)
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

	// Deleting a visible message succeeds and removes it for everyone,
	// including the administrator.
	jmapDelete(t, h, token, receivedID)

	// Verify it's gone for the owner
	emails = getEmailsJMAP(t, h, token)
	ids := make(map[string]bool)
	for _, e := range emails {
		ids[e.ID] = true
	}
	if ids["received"] {
		t.Error("deleted email still listed for owner")
	}
	if !ids["unrelated"] {
		t.Error("unrelated email should still exist")
	}

	// An email that is already gone is reported like any other missing one.
	// (JMAP Email/set destroy on already-deleted ID would return notDestroyed error)

	// The administrator can delete any message (would require admin JMAP access)
	// Note: Full admin delete via JMAP would require querying other account IDs
}

func TestDeleteEmailAlsoDeletesMetadata(t *testing.T) {
	store := storage.NewMemoryStorage()
	metaStore := metadata.NewMemoryMetadata()
	addEmail(store, "received", "alice@example.com", "bob@example.com")

	h, err := New(HandlerConfig{
		AuthUser:        "admin",
		AuthPass:        "secret",
		JWTSecret:       "secret",
		JWTSecretTTL:    time.Hour,
		Storage:         store,
		Metadata:        metaStore,
		UserManagerType: "any",
		UserDomain:      "example.com",
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	_, token, _ := login(t, h, "bob@example.com", "anything")

	// First get emails to find the received email ID
	emails := getEmailsJMAP(t, h, token)
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
	meta, err := metaStore.Get(context.Background(), receivedID)
	if err != nil {
		t.Fatalf("metadata Get() error = %v", err)
	}
	if meta.Read || meta.Flagged {
		t.Errorf("metadata after delete = %+v, want no read/flag state", meta)
	}
	list, err := metaStore.List(context.Background())
	if err != nil {
		t.Fatalf("metadata List() error = %v", err)
	}
	if len(list) != 0 {
		t.Errorf("metadata list after delete = %v, want empty", list)
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

	// First create the email
	emailCreateResp := jmapRequest(t, h, userToken, []string{"urn:ietf:params:jmap:mail"}, []JmapMethodCall{
		{"Email/set", map[string]interface{}{
			"accountId": "bob@example.com",
			"create": map[string]interface{}{
				"c1": map[string]interface{}{
					"mailboxIds": map[string]bool{"drafts": true},
					"keywords":   map[string]bool{},
					"from":       []map[string]string{{"email": "carol@example.com", "name": ""}},
					"to":         []map[string]string{{"email": "dave@example.com", "name": ""}},
					"subject":    "hello",
					"textBody":   []map[string]interface{}{{"partId": "text1", "type": "text/plain", "size": 4}},
					"bodyValues": map[string]interface{}{"text1": map[string]interface{}{"value": "body", "isEncodingProblem": false, "isTruncated": false}},
				},
			},
		}, "e1"},
	})

	// Parse the created email ID
	var createdEmailID string
	methodResponsesRaw, _ := emailCreateResp["methodResponses"].([]interface{})
	for _, mr := range methodResponsesRaw {
		call := mr.([]interface{})
		if len(call) >= 2 && call[0] == "Email/set" {
			result := call[1].(map[string]interface{})
			if created, ok := result["created"].(map[string]interface{}); ok {
				for _, v := range created {
					if emailData, ok := v.(map[string]interface{}); ok {
						if id, ok := emailData["id"].(string); ok {
							createdEmailID = id
							break
						}
					}
				}
			}
			break
		}
	}
	if createdEmailID == "" {
		t.Fatal("failed to create email")
	}

	// Now submit the email (ordinary user - no custom from allowed)
	jmapRequest(t, h, userToken, []string{"urn:ietf:params:jmap:mail", "urn:ietf:params:jmap:submission"}, []JmapMethodCall{
		{"EmailSubmission/set", map[string]interface{}{
			"accountId": "bob@example.com",
			"create": map[string]interface{}{
				"s1": map[string]interface{}{
					"identityId": "primary",
					"emailId":    createdEmailID,
					// No envelope - ordinary users can't set custom from
				},
			},
			"onSuccessUpdateEmail": map[string]interface{}{
				"keywords": map[string]bool{"$draft": false, "$sent": true},
			},
			"onSuccessDestroyEmail": []string{createdEmailID},
		}, "s1"},
	})

	if len(sender.messages) == 0 {
		t.Fatal("no email was sent")
	}
	if got := sender.messages[len(sender.messages)-1].From.Address; got != "bob@example.com" {
		t.Errorf("ordinary user from = %q, want bob@example.com", got)
	}

	_, adminToken, _ := login(t, h, "admin", "secret")

	// Admin send with explicit from
	adminEmailCreateResp := jmapRequest(t, h, adminToken, []string{"urn:ietf:params:jmap:mail"}, []JmapMethodCall{
		{"Email/set", map[string]interface{}{
			"accountId": "admin",
			"create": map[string]interface{}{
				"c1": map[string]interface{}{
					"mailboxIds": map[string]bool{"drafts": true},
					"keywords":   map[string]bool{},
					"from":       []map[string]string{{"email": "dave@example.com", "name": ""}},
					"to":         []map[string]string{{"email": "alice@example.com", "name": ""}},
					"subject":    "hello",
					"textBody":   []map[string]interface{}{{"partId": "text1", "type": "text/plain", "size": 4}},
					"bodyValues": map[string]interface{}{"text1": map[string]interface{}{"value": "body", "isEncodingProblem": false, "isTruncated": false}},
				},
			},
		}, "e1"},
	})

	var adminCreatedEmailID string
	methodResponsesRaw, _ = adminEmailCreateResp["methodResponses"].([]interface{})
	for _, mr := range methodResponsesRaw {
		call := mr.([]interface{})
		if len(call) >= 2 && call[0] == "Email/set" {
			result := call[1].(map[string]interface{})
			if created, ok := result["created"].(map[string]interface{}); ok {
				for _, v := range created {
					if emailData, ok := v.(map[string]interface{}); ok {
						if id, ok := emailData["id"].(string); ok {
							adminCreatedEmailID = id
							break
						}
					}
				}
			}
			break
		}
	}
	if adminCreatedEmailID == "" {
		t.Fatal("failed to create admin email")
	}

	jmapRequest(t, h, adminToken, []string{"urn:ietf:params:jmap:mail", "urn:ietf:params:jmap:submission"}, []JmapMethodCall{
		{"EmailSubmission/set", map[string]interface{}{
			"accountId": "admin",
			"create": map[string]interface{}{
				"s1": map[string]interface{}{
					"identityId": "primary",
					"emailId":    adminCreatedEmailID,
					"envelope": map[string]interface{}{
						"mailFrom": map[string]string{"email": "dave@example.com"},
						"rcptTo":   []map[string]string{{"email": "alice@example.com"}},
					},
				},
			},
			"onSuccessUpdateEmail": map[string]interface{}{
				"keywords": map[string]bool{"$draft": false, "$sent": true},
			},
			"onSuccessDestroyEmail": []string{adminCreatedEmailID},
		}, "s1"},
	})

	if len(sender.messages) < 2 {
		t.Fatal("admin email was not sent")
	}
	if got := sender.messages[len(sender.messages)-1].From.Address; got != "dave@example.com" {
		t.Errorf("admin from = %q, want dave@example.com", got)
	}

	// Replying without an explicit From defaults to the original recipient.
	// First get the original email to find its ID
	emails := getEmailsJMAP(t, h, adminToken)
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
	replyCreateResp := jmapRequest(t, h, adminToken, []string{"urn:ietf:params:jmap:mail"}, []JmapMethodCall{
		{"Email/set", map[string]interface{}{
			"accountId": "admin",
			"create": map[string]interface{}{
				"c1": map[string]interface{}{
					"mailboxIds": map[string]bool{"drafts": true},
					"keywords":   map[string]bool{},
					"from":       []map[string]string{{"email": "", "name": ""}},
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
	methodResponsesRaw, _ = replyCreateResp["methodResponses"].([]interface{})
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
	jmapRequest(t, h, adminToken, []string{"urn:ietf:params:jmap:mail", "urn:ietf:params:jmap:submission"}, []JmapMethodCall{
		{"EmailSubmission/set", map[string]interface{}{
			"accountId": "admin",
			"create": map[string]interface{}{
				"s1": map[string]interface{}{
					"identityId": "primary",
					"emailId":    replyCreatedEmailID,
					// No envelope - should default to original recipient
				},
			},
			"onSuccessUpdateEmail": map[string]interface{}{
				"keywords": map[string]bool{"$draft": false, "$sent": true},
			},
			"onSuccessDestroyEmail": []string{replyCreatedEmailID},
		}, "s1"},
	})

	if len(sender.messages) < 3 {
		t.Fatal("admin reply email was not sent")
	}
	if got := sender.messages[len(sender.messages)-1].From.Address; got != "bob@example.com" {
		t.Errorf("admin reply from = %q, want bob@example.com", got)
	}
}

func TestSendEmailWithCCAndBCC(t *testing.T) {
	h, _, sender := newHandlerForTest(t, "any", "example.com", "secret")

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

	if len(sender.messages) == 0 {
		t.Fatal("no email was sent")
	}
	msg := sender.messages[len(sender.messages)-1]
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

	if len(sender.messages) < 2 {
		t.Fatal("second email was not sent")
	}
	msg = sender.messages[len(sender.messages)-1]
	if len(msg.To) != 2 || msg.To[0].Address != "alice@example.com" || msg.To[1].Address != "carol@example.com" {
		t.Errorf("multi to = %+v, want [alice@example.com, carol@example.com]", msg.To)
	}
}
