package jmap

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/kncept/Emissary/mailer/auth"
	"github.com/kncept/Emissary/mailer/email"
	"github.com/kncept/Emissary/mailer/metadata"
	"github.com/kncept/Emissary/mailer/sender"
	"github.com/kncept/Emissary/mailer/storage"
	"github.com/kncept/Emissary/mailer/usermanager"
)

// TestHandler_EmailGet tests the Email/get method
func TestHandler_EmailGet(t *testing.T) {
	ctx := context.Background()

	// Create test storage with an email
	memStorage := storage.NewMemoryStorage()
	testEmail := &email.UserEmail{
		ID:        "test1",
		AccountID: "test",
		MailboxID: "inbox",
		From:      email.Address{Name: "Test Sender", Address: "sender@example.com"},
		To:        []email.Address{{Name: "Test Recipient", Address: "recipient@example.com"}},
		Subject:   "Test Subject",
		Date:      time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC),
		Text:      "Test email body",
		Size:      100,
	}
	memStorage.Add(testEmail)

	// Create test metadata
	memMeta := metadata.NewMemoryMetadata()
	if err := memMeta.Save(ctx, metadata.EmailMetadata{ID: "test1", Read: false, Flagged: true}); err != nil {
		t.Fatalf("Failed to save metadata: %v", err)
	}

	// Create test user manager
	userMgr := usermanager.NewNone(usermanager.Config{
		AdminUsername: "admin",
		AdminPassword: "password",
	})
	testSender := sender.NewLogSender()

	handler := NewHandler(memStorage, memMeta, userMgr, testSender)

	// Test Email/get
	args := json.RawMessage(`{"accountId": "test", "ids": ["test1"]}`)
	result, err := handler.EmailGet(ctx, "test", args)
	if err != nil {
		t.Fatalf("EmailGet failed: %v", err)
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(result, &resp); err != nil {
		t.Fatalf("Failed to unmarshal response: %v", err)
	}

	list := resp["list"].([]interface{})
	if len(list) != 1 {
		t.Fatalf("Expected 1 email, got %d", len(list))
	}

	emailData := list[0].(map[string]interface{})
	if emailData["id"] != "test1" {
		t.Errorf("Expected id=test1, got %v", emailData["id"])
	}
	if emailData["subject"] != "Test Subject" {
		t.Errorf("Expected subject='Test Subject', got %v", emailData["subject"])
	}

	// Check keywords
	keywords := emailData["keywords"].(map[string]interface{})
	if keywords["$flagged"] != true {
		t.Errorf("Expected $flagged=true, got %v", keywords["$flagged"])
	}
	// $seen should not be present when false (JMAP omits false keywords)
	if _, ok := keywords["$seen"]; ok {
		t.Errorf("Expected $seen to be omitted when false, got %v", keywords["$seen"])
	}

	t.Logf("EmailGet result: %s", string(result))
}

// TestHandler_MailboxGet tests the Mailbox/get method
func TestHandler_MailboxGet(t *testing.T) {
	ctx := context.Background()

	memStorage := storage.NewMemoryStorage()
	memMeta := metadata.NewMemoryMetadata()
	userMgr := usermanager.NewNone(usermanager.Config{
		AdminUsername: "admin",
		AdminPassword: "password",
	})
	testSender := sender.NewLogSender()

	handler := NewHandler(memStorage, memMeta, userMgr, testSender)

	args := json.RawMessage(`{"accountId": "test"}`)
	result, err := handler.MailboxGet(ctx, "test", args)
	if err != nil {
		t.Fatalf("MailboxGet failed: %v", err)
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(result, &resp); err != nil {
		t.Fatalf("Failed to unmarshal response: %v", err)
	}

	list := resp["list"].([]interface{})
	if len(list) != 6 {
		t.Fatalf("Expected 6 mailboxes, got %d", len(list))
	}

	// Check mailbox roles
	roles := make(map[string]bool)
	for _, mb := range list {
		mbMap := mb.(map[string]interface{})
		if role, ok := mbMap["role"]; ok && role != nil {
			roles[role.(string)] = true
		}
	}

	expectedRoles := []string{"inbox", "sent", "drafts", "trash", "archive", "junk"}
	for _, role := range expectedRoles {
		if !roles[role] {
			t.Errorf("Missing mailbox role: %s", role)
		}
	}

	t.Logf("MailboxGet result: %s", string(result))
}

// TestHandler_EmailQuery tests the Email/query method
func TestHandler_EmailQuery(t *testing.T) {
	ctx := context.Background()

	memStorage := storage.NewMemoryStorage()
	// Add multiple test emails
	for i := 1; i <= 5; i++ {
		testEmail := &email.UserEmail{
			ID:        string(rune('a' + i - 1)),
			AccountID: "test",
			MailboxID: "inbox",
			From:      email.Address{Name: "Sender", Address: "sender@example.com"},
			To:        []email.Address{{Name: "Recipient", Address: "recipient@example.com"}},
			Subject:   "Test Subject " + string(rune('a'+i-1)),
			Date:      time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC),
			Text:      "Test email body",
			Size:      100,
		}
		memStorage.Add(testEmail)
	}

	memMeta := metadata.NewMemoryMetadata()
	userMgr := usermanager.NewNone(usermanager.Config{
		AdminUsername: "admin",
		AdminPassword: "password",
	})
	testSender := sender.NewLogSender()

	handler := NewHandler(memStorage, memMeta, userMgr, testSender)

	// Test Email/query with limit
	args := json.RawMessage(`{"accountId": "test", "limit": 3}`)
	result, err := handler.EmailQuery(ctx, "test", args)
	if err != nil {
		t.Fatalf("EmailQuery failed: %v", err)
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(result, &resp); err != nil {
		t.Fatalf("Failed to unmarshal response: %v", err)
	}

	ids := resp["ids"].([]interface{})
	if len(ids) != 3 {
		t.Errorf("Expected 3 ids, got %d", len(ids))
	}

	t.Logf("EmailQuery result: %s", string(result))
}

// TestHandler_EmailQueryScopesToAccountAndMailbox verifies that JMAP reads are
// limited to the requested account's mailbox, including delivered .eml IDs.
func TestHandler_EmailQueryScopesToAccountAndMailbox(t *testing.T) {
	ctx := context.Background()
	memStorage := storage.NewMemoryStorage()
	const (
		aliceInbox = "users/alice@example.com/inbox/20261117T120000_a.eml"
		aliceSent  = "users/alice@example.com/sent/20261117T130000_s.eml"
		bobInbox   = "users/bob@example.com/inbox/20261117T140000_b.eml"
	)
	receivedAt := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	for _, msg := range []*email.UserEmail{
		{ID: aliceInbox, AccountID: "alice@example.com", MailboxID: "inbox", Subject: "Alice inbox", Date: receivedAt},
		{ID: aliceSent, AccountID: "alice@example.com", MailboxID: "sent", Subject: "Alice sent", Date: receivedAt},
		{ID: bobInbox, AccountID: "bob@example.com", MailboxID: "inbox", Subject: "Bob inbox", Date: receivedAt},
	} {
		memStorage.Add(msg)
	}

	handler := NewHandler(memStorage, metadata.NewMemoryMetadata(),
		usermanager.NewNone(usermanager.Config{AdminUsername: "admin", AdminPassword: "password"}),
		sender.NewLogSender())

	queryIDs := func(t *testing.T, accountID, mailboxID string) []string {
		t.Helper()
		args := map[string]interface{}{"accountId": accountID}
		if mailboxID != "" {
			args["filter"] = map[string]string{"inMailbox": mailboxID}
		}
		raw, err := json.Marshal(args)
		if err != nil {
			t.Fatalf("marshal args: %v", err)
		}
		result, err := handler.EmailQuery(ctx, accountID, raw)
		if err != nil {
			t.Fatalf("EmailQuery(%q, %q) error = %v", accountID, mailboxID, err)
		}
		var resp struct {
			IDs []string `json:"ids"`
		}
		if err := json.Unmarshal(result, &resp); err != nil {
			t.Fatalf("unmarshal response: %v", err)
		}
		return resp.IDs
	}

	assertIDs := func(t *testing.T, got, want []string) {
		t.Helper()
		if len(got) != len(want) {
			t.Fatalf("ids = %v, want %v", got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("ids = %v, want %v", got, want)
			}
		}
	}

	assertIDs(t, queryIDs(t, "alice@example.com", "inbox"), []string{aliceInbox})
	assertIDs(t, queryIDs(t, "bob@example.com", "inbox"), []string{bobInbox})
	assertIDs(t, queryIDs(t, "ALICE@example.com", ""), []string{aliceInbox, aliceSent})

	// Email/get must not expose another account's message either.
	getArgs := json.RawMessage(fmt.Sprintf(`{"accountId":"alice@example.com","ids":[%q]}`, bobInbox))
	getResult, err := handler.EmailGet(ctx, "alice@example.com", getArgs)
	if err != nil {
		t.Fatalf("EmailGet() error = %v", err)
	}
	var getResp struct {
		List     []JMAPEmail `json:"list"`
		NotFound []string    `json:"notFound"`
	}
	if err := json.Unmarshal(getResult, &getResp); err != nil {
		t.Fatalf("unmarshal Email/get response: %v", err)
	}
	if len(getResp.List) != 0 || len(getResp.NotFound) != 1 || getResp.NotFound[0] != bobInbox {
		t.Fatalf("Email/get response = %+v, want bob's id in notFound", getResp)
	}
}

func TestEmailSetStoresReplyTo(t *testing.T) {
	store := storage.NewMemoryStorage()
	handler := NewHandler(store, metadata.NewMemoryMetadata(),
		usermanager.NewNone(usermanager.Config{AdminUsername: "admin", AdminPassword: "password"}),
		sender.NewLogSender())

	args := json.RawMessage(`{"accountId":"alice@example.com","create":{"c1":{
		"mailboxIds":{"drafts":true},
		"from":[{"email":"alice@example.com"}],
		"replyTo":[{"email":"bob@example.com"}],
		"to":[{"email":"dave@example.com"}],
		"subject":"subject"}}}`)
	result, err := handler.EmailSet(context.Background(), "alice@example.com", args)
	if err != nil {
		t.Fatalf("EmailSet() error = %v", err)
	}
	var resp struct {
		Created map[string]struct {
			ID string `json:"id"`
		} `json:"created"`
	}
	if err := json.Unmarshal(result, &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	created := resp.Created["c1"]
	draft, err := store.GetEmail(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("GetEmail(%q) error = %v", created.ID, err)
	}
	if len(draft.ReplyTo) != 1 || draft.ReplyTo[0].Address != "bob@example.com" {
		t.Fatalf("stored Reply-To = %+v, want [bob@example.com]", draft.ReplyTo)
	}
}

func TestEmailSetStoresHTMLBodyAndEmailGetReturnsIt(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemoryStorage()
	handler := NewHandler(store, metadata.NewMemoryMetadata(),
		usermanager.NewNone(usermanager.Config{AdminUsername: "admin", AdminPassword: "password"}),
		sender.NewLogSender())

	args := json.RawMessage(`{"accountId":"alice@example.com","create":{"c1":{
		"mailboxIds":{"drafts":true},
		"from":[{"email":"alice@example.com"}],
		"to":[{"email":"bob@example.com"}],
		"subject":"HTML mail",
		"textBody":[{"partId":"text1","type":"text/plain","size":5}],
		"htmlBody":[{"partId":"html1","type":"text/html","size":16}],
		"bodyValues":{
			"text1":{"value":"hello"},
			"html1":{"value":"<p>hello</p>"}
		}}}}`)
	result, err := handler.EmailSet(ctx, "alice@example.com", args)
	if err != nil {
		t.Fatalf("EmailSet() error = %v", err)
	}
	var createdResp struct {
		Created map[string]struct {
			ID string `json:"id"`
		} `json:"created"`
	}
	if err := json.Unmarshal(result, &createdResp); err != nil {
		t.Fatalf("unmarshal create response: %v", err)
	}
	id := createdResp.Created["c1"].ID

	draft, err := store.GetEmail(ctx, id)
	if err != nil {
		t.Fatalf("GetEmail(%q) error = %v", id, err)
	}
	if draft.Text != "hello" || draft.HTML != "<p>hello</p>" {
		t.Fatalf("stored bodies = text %q, html %q", draft.Text, draft.HTML)
	}

	getResult, err := handler.EmailGet(ctx, "alice@example.com", json.RawMessage(
		`{"accountId":"alice@example.com","ids":["`+id+`"],"fetchTextBodyValues":true,"fetchHTMLBodyValues":true}`))
	if err != nil {
		t.Fatalf("EmailGet() error = %v", err)
	}
	var getResp struct {
		List []JMAPEmail `json:"list"`
	}
	if err := json.Unmarshal(getResult, &getResp); err != nil {
		t.Fatalf("unmarshal get response: %v", err)
	}
	if len(getResp.List) != 1 {
		t.Fatalf("Email/get returned %d messages, want 1", len(getResp.List))
	}
	msg := getResp.List[0]
	if len(msg.TextBody) != 1 || msg.TextBody[0].PartID == nil || *msg.TextBody[0].PartID != "text1" {
		t.Fatalf("textBody = %+v, want part text1", msg.TextBody)
	}
	if len(msg.HTMLBody) != 1 || msg.HTMLBody[0].PartID == nil || *msg.HTMLBody[0].PartID != "html1" {
		t.Fatalf("htmlBody = %+v, want part html1", msg.HTMLBody)
	}
	if got := msg.BodyValues["html1"].Value; got != "<p>hello</p>" {
		t.Fatalf("html1 body value = %q, want %q", got, "<p>hello</p>")
	}
}

type recordingSender struct {
	messages []*email.UserEmail
}

func (s *recordingSender) Name() string { return "recording" }
func (s *recordingSender) Send(_ context.Context, message *email.UserEmail) error {
	s.messages = append(s.messages, message)
	return nil
}
func (s *recordingSender) SendBounce(_ context.Context, _, _, _, _ string, _ *email.UserEmail) error {
	return nil
}

func TestEmailSubmissionSetForwardsHTMLBody(t *testing.T) {
	const accountID = "alice@example.com"
	store := storage.NewMemoryStorage()
	store.Add(&email.UserEmail{
		ID:        "draft1",
		AccountID: accountID,
		From:      email.Address{Address: accountID},
		To:        []email.Address{{Address: "bob@example.com"}},
		Subject:   "HTML mail",
		Text:      "hello",
		HTML:      "<p>hello</p>",
	})
	capture := &recordingSender{}
	handler := NewHandler(store, metadata.NewMemoryMetadata(),
		usermanager.NewNone(usermanager.Config{AdminUsername: "admin", AdminPassword: "password"}),
		capture)

	ctx := ContextWithClaims(context.Background(), &auth.Claims{Subject: accountID})
	args := json.RawMessage(fmt.Sprintf(
		`{"accountId":%q,"create":{"s1":{"identityId":"primary","emailId":"draft1"}}}`,
		accountID))
	if _, err := handler.EmailSubmissionSet(ctx, accountID, args); err != nil {
		t.Fatalf("EmailSubmissionSet() error = %v", err)
	}
	if len(capture.messages) != 1 {
		t.Fatalf("sent %d messages, want 1", len(capture.messages))
	}
	msg := capture.messages[0]
	if msg.Text != "hello" || msg.HTML != "<p>hello</p>" {
		t.Fatalf("sent bodies = text %q, html %q", msg.Text, msg.HTML)
	}
}

// TestEmailSubmissionSetFromVerification exercises the From rules directly at
// the JMAP layer: the address must be the caller's own account, unless the
// caller has the masquerade permission.
func TestEmailSubmissionSetFromVerification(t *testing.T) {
	const (
		accountID = "alice@example.com"
		draftFrom = "bob@example.com"
	)

	tests := []struct {
		name          string
		claims        *auth.Claims
		from          string
		wantForbidden bool
		wantFrom      string
	}{
		{
			name:     "own address",
			claims:   &auth.Claims{Subject: accountID},
			from:     accountID,
			wantFrom: accountID,
		},
		{
			name:          "forged address",
			claims:        &auth.Claims{Subject: accountID},
			from:          draftFrom,
			wantForbidden: true,
		},
		{
			name:     "user manager may masquerade",
			claims:   &auth.Claims{Subject: accountID, EmissaryUserManagement: true},
			from:     draftFrom,
			wantFrom: draftFrom,
		},
		{
			name:          "system manager alone may not masquerade",
			claims:        &auth.Claims{Subject: accountID, SystemManager: true},
			from:          draftFrom,
			wantForbidden: true,
		},
		{
			name:     "no claims falls back to account",
			from:     accountID,
			wantFrom: accountID,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := storage.NewMemoryStorage()
			store.Add(&email.UserEmail{
				ID:        "draft1",
				AccountID: accountID,
				From:      email.Address{Address: tc.from},
				To:        []email.Address{{Address: "dave@example.com"}},
				Subject:   "subject",
				Text:      "body",
			})
			capture := &recordingSender{}
			handler := NewHandler(store, metadata.NewMemoryMetadata(),
				usermanager.NewNone(usermanager.Config{AdminUsername: "admin", AdminPassword: "password"}),
				capture)

			ctx := context.Background()
			if tc.claims != nil {
				ctx = ContextWithClaims(ctx, tc.claims)
			}
			args := json.RawMessage(fmt.Sprintf(
				`{"accountId":%q,"create":{"s1":{"identityId":"primary","emailId":"draft1"}}}`,
				accountID))
			result, err := handler.EmailSubmissionSet(ctx, accountID, args)
			if err != nil {
				t.Fatalf("EmailSubmissionSet() error = %v", err)
			}
			var resp struct {
				Created    map[string]json.RawMessage `json:"created"`
				NotCreated map[string]struct {
					Type string `json:"type"`
				} `json:"notCreated"`
			}
			if err := json.Unmarshal(result, &resp); err != nil {
				t.Fatalf("unmarshal response: %v", err)
			}

			if tc.wantForbidden {
				problem, ok := resp.NotCreated["s1"]
				if !ok || problem.Type != "forbidden" {
					t.Fatalf("notCreated = %+v, want forbidden", resp.NotCreated)
				}
				if len(capture.messages) != 0 {
					t.Fatalf("message was sent despite forbidden From: %+v", capture.messages)
				}
				return
			}

			if len(resp.Created) != 1 || len(capture.messages) != 1 {
				t.Fatalf("created = %+v, sent = %+v, want one of each", resp.Created, capture.messages)
			}
			if got := capture.messages[0].From.Address; got != tc.wantFrom {
				t.Errorf("sent From = %q, want %q", got, tc.wantFrom)
			}
		})
	}
}

// TestEmailSubmissionSetMasqueradeReplyTo checks that a submission sent on
// behalf of another account always uses the From address for replies.
func TestEmailSubmissionSetMasqueradeReplyTo(t *testing.T) {
	const (
		accountID = "alice@example.com"
		from      = "bob@example.com"
	)

	tests := []struct {
		name        string
		replyTo     []string
		wantError   string
		wantReplyTo []string
	}{
		{name: "default Reply-To to From", wantReplyTo: []string{from}},
		{name: "matching Reply-To", replyTo: []string{from}, wantReplyTo: []string{from}},
		{name: "conflicting Reply-To", replyTo: []string{"alice@example.com"}, wantError: "invalidProperties"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := storage.NewMemoryStorage()
			replyTo := make([]email.Address, 0, len(tc.replyTo))
			for _, addr := range tc.replyTo {
				replyTo = append(replyTo, email.Address{Address: addr})
			}
			store.Add(&email.UserEmail{
				ID:        "draft1",
				AccountID: accountID,
				From:      email.Address{Address: from},
				ReplyTo:   replyTo,
				To:        []email.Address{{Address: "dave@example.com"}},
				Subject:   "subject",
				Text:      "body",
			})
			capture := &recordingSender{}
			handler := NewHandler(store, metadata.NewMemoryMetadata(),
				usermanager.NewNone(usermanager.Config{AdminUsername: "admin", AdminPassword: "password"}),
				capture)

			ctx := ContextWithClaims(context.Background(), &auth.Claims{
				Subject:                accountID,
				EmissaryUserManagement: true,
			})
			args := json.RawMessage(fmt.Sprintf(
				`{"accountId":%q,"create":{"s1":{"identityId":"primary","emailId":"draft1"}}}`,
				accountID))
			result, err := handler.EmailSubmissionSet(ctx, accountID, args)
			if err != nil {
				t.Fatalf("EmailSubmissionSet() error = %v", err)
			}
			var resp struct {
				Created    map[string]json.RawMessage `json:"created"`
				NotCreated map[string]struct {
					Type string `json:"type"`
				} `json:"notCreated"`
			}
			if err := json.Unmarshal(result, &resp); err != nil {
				t.Fatalf("unmarshal response: %v", err)
			}

			if tc.wantError != "" {
				if got := resp.NotCreated["s1"].Type; got != tc.wantError {
					t.Fatalf("notCreated type = %q, want %q (%+v)", got, tc.wantError, resp.NotCreated)
				}
				if len(capture.messages) != 0 {
					t.Fatalf("message was sent despite Reply-To mismatch: %+v", capture.messages)
				}
				return
			}

			if len(resp.Created) != 1 || len(capture.messages) != 1 {
				t.Fatalf("created = %+v, sent = %+v, want one of each", resp.Created, capture.messages)
			}
			got := capture.messages[0].ReplyTo
			if len(got) != len(tc.wantReplyTo) {
				t.Fatalf("Reply-To = %+v, want %v", got, tc.wantReplyTo)
			}
			for i := range got {
				if got[i].Address != tc.wantReplyTo[i] {
					t.Errorf("Reply-To[%d] = %q, want %q", i, got[i].Address, tc.wantReplyTo[i])
				}
			}
		})
	}
}
