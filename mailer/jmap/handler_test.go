package jmap

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/kncept/Emissary/mailer/auth"
	"github.com/kncept/Emissary/mailer/email"
	"github.com/kncept/Emissary/mailer/metadata"
	"github.com/kncept/Emissary/mailer/sender"
	"github.com/kncept/Emissary/mailer/storage"
	"github.com/kncept/Emissary/mailer/usermanager"
)

func addTestEmail(store *storage.MemoryStorage, id, accountID, mailboxID string, message *email.UserEmail) {
	store.Add(&email.ListedEmail{
		Ref:   &email.EmailRef{ID: id, AccountID: accountID, MailboxID: mailboxID},
		Email: message,
	})
}

// TestHandler_EmailGet tests the Email/get method
func TestHandler_EmailGet(t *testing.T) {
	ctx := context.Background()

	// Create test storage with an email
	memStorage := storage.NewMemoryStorage()
	addTestEmail(memStorage, "test1", "test", "inbox", &email.UserEmail{
		From:    email.Address{Name: "Test Sender", Address: "sender@example.com"},
		To:      []email.Address{{Name: "Test Recipient", Address: "recipient@example.com"}},
		Subject: "Test Subject",
		Date:    time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC),
		Text:    "Test email body",
		Size:    100,
	})

	// Create test metadata
	memMeta := metadata.NewMemoryMetadata()
	if err := memMeta.Save(ctx, metadata.NewEmailMetadata("test", "test1", metadata.KeywordFlagged)); err != nil {
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
		id := string(rune('a' + i - 1))
		addTestEmail(memStorage, id, "test", "inbox", &email.UserEmail{
			From:    email.Address{Name: "Sender", Address: "sender@example.com"},
			To:      []email.Address{{Name: "Recipient", Address: "recipient@example.com"}},
			Subject: "Test Subject " + id,
			Date:    time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC),
			Text:    "Test email body",
			Size:    100,
		})
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

func TestEmailQueryListsMessagesWithoutMetadata(t *testing.T) {
	ctx := context.Background()
	const accountID = "alice@example.com"
	store := storage.NewMemoryStorage()
	messageDate := time.Date(2026, time.November, 19, 4, 16, 0, 0, time.UTC)
	for _, id := range []string{"with-meta", "without-meta"} {
		addTestEmail(store, id, accountID, "inbox", &email.UserEmail{Subject: id, Date: messageDate})
	}

	// Only one delivered message has a metadata record; the other represents
	// a receiver delivery that persisted the message but failed to save its
	// metadata. Listing is driven by stored messages, so both must appear.
	metaStore := metadata.NewMemoryMetadata()
	if err := metaStore.Save(ctx, metadata.NewEmailMetadata(accountID, "with-meta")); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	handler := NewHandler(store, metaStore,
		usermanager.NewNone(usermanager.Config{AdminUsername: "admin", AdminPassword: "password"}),
		sender.NewLogSender())

	result, err := handler.EmailQuery(ctx, accountID, json.RawMessage(
		`{"accountId":"alice@example.com","calculateTotal":true}`))
	if err != nil {
		t.Fatalf("EmailQuery() error = %v", err)
	}
	var resp struct {
		IDs   []string `json:"ids"`
		Total int      `json:"total"`
	}
	if err := json.Unmarshal(result, &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if len(resp.IDs) != 2 || resp.Total != 2 {
		t.Fatalf("EmailQuery() ids = %v, total = %d, want both messages", resp.IDs, resp.Total)
	}
	got := make(map[string]bool, len(resp.IDs))
	for _, id := range resp.IDs {
		got[id] = true
	}
	for _, id := range []string{"with-meta", "without-meta"} {
		if !got[id] {
			t.Errorf("EmailQuery() missing id %q in %v", id, resp.IDs)
		}
	}
}

func TestEmailQuerySortsByStoredReceivedAt(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemoryStorage()
	messageDate := time.Date(2026, time.November, 17, 12, 0, 0, 0, time.UTC)
	addTestEmail(store, "older", "alice@example.com", "inbox", &email.UserEmail{Subject: "older", Date: messageDate})
	addTestEmail(store, "newer", "alice@example.com", "inbox", &email.UserEmail{Subject: "newer", Date: messageDate})

	metaStore := metadata.NewMemoryMetadata()
	for id, receivedAt := range map[string]string{
		"older": "2026-11-17T12:00:00Z",
		"newer": "2026-11-18T12:00:00Z",
	} {
		meta := metadata.NewEmailMetadata("alice@example.com", id)
		meta.ReceivedAt = receivedAt
		if err := metaStore.Save(ctx, meta); err != nil {
			t.Fatalf("Save(%q) error = %v", id, err)
		}
	}
	handler := NewHandler(store, metaStore,
		usermanager.NewNone(usermanager.Config{AdminUsername: "admin", AdminPassword: "password"}),
		sender.NewLogSender())

	result, err := handler.EmailQuery(ctx, "alice@example.com", json.RawMessage(`{"accountId":"alice@example.com"}`))
	if err != nil {
		t.Fatalf("EmailQuery() error = %v", err)
	}
	var resp struct {
		IDs []string `json:"ids"`
	}
	if err := json.Unmarshal(result, &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if len(resp.IDs) != 2 || resp.IDs[0] != "newer" || resp.IDs[1] != "older" {
		t.Errorf("ids = %v, want [newer older] by receivedAt", resp.IDs)
	}
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
	addTestEmail(memStorage, aliceInbox, "alice@example.com", "inbox", &email.UserEmail{Subject: "Alice inbox", Date: receivedAt})
	addTestEmail(memStorage, aliceSent, "alice@example.com", "sent", &email.UserEmail{Subject: "Alice sent", Date: receivedAt})
	addTestEmail(memStorage, bobInbox, "bob@example.com", "inbox", &email.UserEmail{Subject: "Bob inbox", Date: receivedAt})

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
	if len(draft.Email.ReplyTo) != 1 || draft.Email.ReplyTo[0].Address != "bob@example.com" {
		t.Fatalf("stored Reply-To = %+v, want [bob@example.com]", draft.Email.ReplyTo)
	}
}

func TestEmailSetCreateIgnoresClientReceivedAt(t *testing.T) {
	ctx := context.Background()
	metaStore := metadata.NewMemoryMetadata()
	handler := NewHandler(storage.NewMemoryStorage(), metaStore,
		usermanager.NewNone(usermanager.Config{AdminUsername: "admin", AdminPassword: "password"}),
		sender.NewLogSender())

	args := json.RawMessage(`{"accountId":"alice@example.com","create":{"c1":{
		"mailboxIds":{"inbox":true},
		"from":[{"email":"alice@example.com"}],
		"to":[{"email":"bob@example.com"}],
		"subject":"subject",
		"receivedAt":"1999-01-01T00:00:00Z"}}}`)
	result, err := handler.EmailSet(ctx, "alice@example.com", args)
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
	meta, err := metaStore.Get(ctx, "alice@example.com", resp.Created["c1"].ID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if meta.ReceivedAt == "1999-01-01T00:00:00Z" {
		t.Error("client-supplied receivedAt was accepted")
	}
	received, err := time.Parse(time.RFC3339, meta.ReceivedAt)
	if err != nil {
		t.Fatalf("receivedAt %q is not RFC 3339: %v", meta.ReceivedAt, err)
	}
	if time.Since(received) > time.Minute {
		t.Errorf("receivedAt %q was not stamped at creation", meta.ReceivedAt)
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
	if draft.Email.Text != "hello" || draft.Email.HTML != "<p>hello</p>" {
		t.Fatalf("stored bodies = text %q, html %q", draft.Email.Text, draft.Email.HTML)
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

func TestEmailSubmissionSetForwardsHTMLBody(t *testing.T) {
	const accountID = "alice@example.com"
	store := storage.NewMemoryStorage()
	addTestEmail(store, "draft1", accountID, "inbox", &email.UserEmail{
		From:    email.Address{Address: accountID},
		To:      []email.Address{{Address: "bob@example.com"}},
		Subject: "HTML mail",
		Text:    "hello",
		HTML:    "<p>hello</p>",
	})
	capture := sender.NewInMemoryRecording()
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
	if len(capture.Sent) != 1 {
		t.Fatalf("sent %d messages, want 1", len(capture.Sent))
	}
	msg := capture.Sent[0]
	if msg.Text != "hello" || msg.HTML != "<p>hello</p>" {
		t.Fatalf("sent bodies = text %q, html %q", msg.Text, msg.HTML)
	}
}

// TestEmailSubmissionSetCarriesThreadHeaders checks that In-Reply-To and
// References from the submitted draft reach the sender, so delivered replies
// stay linked to their thread.
func TestEmailSubmissionSetCarriesThreadHeaders(t *testing.T) {
	const accountID = "alice@example.com"
	store := storage.NewMemoryStorage()
	addTestEmail(store, "draft1", accountID, "inbox", &email.UserEmail{
		From:       email.Address{Address: accountID},
		To:         []email.Address{{Address: "bob@example.com"}},
		Subject:    "RE: Hello",
		Text:       "my reply",
		MessageID:  "<reply-1@example.com>",
		InReplyTo:  []string{"<original-1@example.com>"},
		References: []string{"<first@example.com>", "<original-1@example.com>"},
	})
	capture := sender.NewInMemoryRecording()
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
	if len(capture.Sent) != 1 {
		t.Fatalf("sent %d messages, want 1", len(capture.Sent))
	}
	msg := capture.Sent[0]
	if len(msg.InReplyTo) != 1 || msg.InReplyTo[0] != "<original-1@example.com>" {
		t.Errorf("In-Reply-To = %v, want [<original-1@example.com>]", msg.InReplyTo)
	}
	if len(msg.References) != 2 || msg.References[0] != "<first@example.com>" || msg.References[1] != "<original-1@example.com>" {
		t.Errorf("References = %v, want [<first@example.com> <original-1@example.com>]", msg.References)
	}
	if msg.MessageID != "<reply-1@example.com>" {
		t.Errorf("Message-ID = %q, want <reply-1@example.com>", msg.MessageID)
	}
}

// TestEmailSetCreateLocksFromAndGeneratesMessageID checks that a created draft
// takes its From from the authenticated user (or the masquerade target)
// regardless of the client's "from", and gets a Message-ID at create time.
func TestEmailSetCreateLocksFromAndGeneratesMessageID(t *testing.T) {
	const (
		caller = "alice@example.com"
		target = "bob@example.com"
	)

	t.Run("own account ignores the client From", func(t *testing.T) {
		store := storage.NewMemoryStorage()
		handler := NewHandler(store, metadata.NewMemoryMetadata(),
			usermanager.NewNone(usermanager.Config{AdminUsername: "admin", AdminPassword: "password"}),
			sender.NewInMemoryRecording())
		ctx := ContextWithClaims(context.Background(), &auth.Claims{Subject: caller})
		args := json.RawMessage(fmt.Sprintf(
			`{"accountId":%q,"create":{"c1":{"from":[{"name":"","email":"carol@example.com"}],"to":[{"name":"","email":"dave@example.com"}],"subject":"s","bodyValues":{"text1":{"value":"hi"}},"textBody":[{"partId":"text1","size":2}]}}}`,
			caller))
		result, err := handler.EmailSet(ctx, caller, args)
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
		id, ok := resp.Created["c1"]
		if !ok {
			t.Fatalf("created = %+v, want c1", resp.Created)
		}
		listed, err := store.GetEmail(ctx, id.ID)
		if err != nil || listed.Email == nil {
			t.Fatalf("GetEmail() = %+v, %v", listed, err)
		}
		if got := listed.Email.From.Address; got != caller {
			t.Errorf("draft From = %q, want %q", got, caller)
		}
		if listed.Email.MessageID == "" || !strings.HasSuffix(listed.Email.MessageID, "@example.com>") {
			t.Errorf("draft Message-ID = %q, want a generated id on the caller's domain", listed.Email.MessageID)
		}
	})

	t.Run("masquerade uses the target account", func(t *testing.T) {
		store := storage.NewMemoryStorage()
		handler := NewHandler(store, metadata.NewMemoryMetadata(),
			usermanager.NewNone(usermanager.Config{AdminUsername: "admin", AdminPassword: "password"}),
			sender.NewInMemoryRecording())
		ctx := ContextWithClaims(context.Background(), &auth.Claims{Subject: caller, EmissaryUserManagement: true})
		args := json.RawMessage(fmt.Sprintf(
			`{"accountId":%q,"create":{"c1":{"from":[{"name":"","email":%q}],"to":[{"name":"","email":"dave@example.com"}],"subject":"s","bodyValues":{"text1":{"value":"hi"}},"textBody":[{"partId":"text1","size":2}]}}}`,
			target, caller))
		result, err := handler.EmailSet(ctx, target, args)
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
		id, ok := resp.Created["c1"]
		if !ok {
			t.Fatalf("created = %+v, want c1", resp.Created)
		}
		listed, err := store.GetEmail(ctx, id.ID)
		if err != nil || listed.Email == nil {
			t.Fatalf("GetEmail() = %+v, %v", listed, err)
		}
		if got := listed.Email.From.Address; got != target {
			t.Errorf("draft From = %q, want %q", got, target)
		}
		if !strings.HasSuffix(listed.Email.MessageID, "@example.com>") {
			t.Errorf("draft Message-ID = %q, want one on the target's domain", listed.Email.MessageID)
		}
	})
}

// TestEmailGetExposesThreadHeaders checks that Email/get reports the
// messageId, inReplyTo and references properties for replies and forwards.
func TestEmailGetExposesThreadHeaders(t *testing.T) {
	const accountID = "alice@example.com"
	store := storage.NewMemoryStorage()
	addTestEmail(store, "email-1", accountID, "inbox", &email.UserEmail{
		From:       email.Address{Address: "bob@example.com"},
		Subject:    "Hello",
		Text:       "body",
		MessageID:  "<original-1@example.com>",
		InReplyTo:  []string{"<first@example.com>"},
		References: []string{"<first@example.com>", "<original-1@example.com>"},
	})
	handler := NewHandler(store, metadata.NewMemoryMetadata(),
		usermanager.NewNone(usermanager.Config{AdminUsername: "admin", AdminPassword: "password"}),
		sender.NewInMemoryRecording())

	args := json.RawMessage(fmt.Sprintf(
		`{"accountId":%q,"ids":["email-1"]}`, accountID))
	result, err := handler.EmailGet(context.Background(), accountID, args)
	if err != nil {
		t.Fatalf("EmailGet() error = %v", err)
	}
	var resp struct {
		List []struct {
			MessageID  []string `json:"messageId"`
			InReplyTo  []string `json:"inReplyTo"`
			References []string `json:"references"`
		} `json:"list"`
	}
	if err := json.Unmarshal(result, &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if len(resp.List) != 1 {
		t.Fatalf("list = %+v, want one email", resp.List)
	}
	got := resp.List[0]
	if len(got.MessageID) != 1 || got.MessageID[0] != "<original-1@example.com>" {
		t.Errorf("messageId = %v, want [<original-1@example.com>]", got.MessageID)
	}
	if len(got.InReplyTo) != 1 || got.InReplyTo[0] != "<first@example.com>" {
		t.Errorf("inReplyTo = %v, want [<first@example.com>]", got.InReplyTo)
	}
	if len(got.References) != 2 || got.References[0] != "<first@example.com>" || got.References[1] != "<original-1@example.com>" {
		t.Errorf("references = %v, want [<first@example.com> <original-1@example.com>]", got.References)
	}
}

// TestEmailSubmissionSetLocksFrom exercises the locked-From rule at the JMAP
// layer: submissions always send as the caller's own account, or as the target
// account when a User Manager masquerades as it. Client-supplied senders
// (draft From header and envelope mailFrom) are ignored.
func TestEmailSubmissionSetLocksFrom(t *testing.T) {
	const (
		caller     = "alice@example.com"
		target     = "bob@example.com"
		forgedFrom = "carol@example.com"
		nonEmail   = "admin"
	)

	tests := []struct {
		name      string
		claims    *auth.Claims
		accountID string
		draftFrom string
		mailFrom  string
		wantError string
		wantFrom  string
	}{
		{
			name:      "sends as the caller's own account",
			claims:    &auth.Claims{Subject: caller},
			accountID: caller,
			wantFrom:  caller,
		},
		{
			name:      "ignores a forged draft From header",
			claims:    &auth.Claims{Subject: caller},
			accountID: caller,
			draftFrom: forgedFrom,
			wantFrom:  caller,
		},
		{
			name:      "ignores the envelope mailFrom",
			claims:    &auth.Claims{Subject: caller},
			accountID: caller,
			mailFrom:  forgedFrom,
			wantFrom:  caller,
		},
		{
			name:      "user manager masquerades as the target account",
			claims:    &auth.Claims{Subject: caller, EmissaryUserManagement: true},
			accountID: target,
			draftFrom: caller,
			wantFrom:  target,
		},
		{
			name:      "system manager alone does not masquerade",
			claims:    &auth.Claims{Subject: caller, SystemManager: true},
			accountID: caller,
			draftFrom: forgedFrom,
			wantFrom:  caller,
		},
		{
			name:      "no claims falls back to the account",
			accountID: caller,
			draftFrom: forgedFrom,
			wantFrom:  caller,
		},
		{
			name:      "account without an email address cannot send",
			claims:    &auth.Claims{Subject: nonEmail},
			accountID: nonEmail,
			wantError: "invalidProperties",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := storage.NewMemoryStorage()
			draftFrom := tc.draftFrom
			if draftFrom == "" {
				draftFrom = tc.accountID
			}
			addTestEmail(store, "draft1", tc.accountID, "inbox", &email.UserEmail{
				From:    email.Address{Address: draftFrom},
				To:      []email.Address{{Address: "dave@example.com"}},
				Subject: "subject",
				Text:    "body",
			})
			capture := sender.NewInMemoryRecording()
			handler := NewHandler(store, metadata.NewMemoryMetadata(),
				usermanager.NewNone(usermanager.Config{AdminUsername: "admin", AdminPassword: "password"}),
				capture)

			ctx := context.Background()
			if tc.claims != nil {
				ctx = ContextWithClaims(ctx, tc.claims)
			}
			create := `{"identityId":"primary","emailId":"draft1"}`
			if tc.mailFrom != "" {
				create = fmt.Sprintf(
					`{"identityId":"primary","emailId":"draft1","envelope":{"mailFrom":{"email":%q},"rcptTo":[{"email":"dave@example.com"}]}}`,
					tc.mailFrom)
			}
			args := json.RawMessage(fmt.Sprintf(
				`{"accountId":%q,"create":{"s1":%s}}`,
				tc.accountID, create))
			result, err := handler.EmailSubmissionSet(ctx, tc.accountID, args)
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
				problem, ok := resp.NotCreated["s1"]
				if !ok || problem.Type != tc.wantError {
					t.Fatalf("notCreated = %+v, want type %q", resp.NotCreated, tc.wantError)
				}
				if len(capture.Sent) != 0 {
					t.Fatalf("message was sent despite rejected From: %+v", capture.Sent)
				}
				return
			}

			if len(resp.Created) != 1 || len(capture.Sent) != 1 {
				t.Fatalf("created = %+v, sent = %+v, want one of each", resp.Created, capture.Sent)
			}
			if got := capture.Sent[0].From.Address; got != tc.wantFrom {
				t.Errorf("sent From = %q, want %q", got, tc.wantFrom)
			}
		})
	}
}

// TestEmailSubmissionSetEnvelopeValidation checks that an envelope which is
// invalid or does not cover every recipient rejects the whole submission. The
// message must not be sent and no created entry may be produced; a continue in
// a validation loop must not fall through to the send path.
func TestEmailSubmissionSetEnvelopeValidation(t *testing.T) {
	const accountID = "alice@example.com"

	emailAddrs := func(addrs []string) []email.Address {
		out := make([]email.Address, 0, len(addrs))
		for _, addr := range addrs {
			out = append(out, email.Address{Address: addr})
		}
		return out
	}
	jsonAddrs := func(addrs []string) string {
		parts := make([]string, 0, len(addrs))
		for _, addr := range addrs {
			parts = append(parts, fmt.Sprintf(`{"email":%q}`, addr))
		}
		return "[" + strings.Join(parts, ",") + "]"
	}

	tests := []struct {
		name      string
		to        []string
		cc        []string
		bcc       []string
		rcptTo    []string
		wantError string // empty means the submission should be accepted
	}{
		{
			name:   "valid envelope sends",
			to:     []string{"bob@example.com"},
			cc:     []string{"carol@example.com"},
			bcc:    []string{"dave@example.com"},
			rcptTo: []string{"bob@example.com", "carol@example.com", "dave@example.com"},
		},
		{
			name:      "To recipient missing from envelope",
			to:        []string{"bob@example.com"},
			rcptTo:    []string{"carol@example.com"},
			wantError: "forbidden",
		},
		{
			name:      "Cc recipient missing from envelope",
			to:        []string{"bob@example.com"},
			cc:        []string{"carol@example.com"},
			rcptTo:    []string{"bob@example.com"},
			wantError: "forbidden",
		},
		{
			name:      "Bcc recipient missing from envelope",
			to:        []string{"bob@example.com"},
			bcc:       []string{"carol@example.com"},
			rcptTo:    []string{"bob@example.com"},
			wantError: "forbidden",
		},
		{
			name:      "invalid recipient address in envelope",
			to:        []string{"bob@example.com"},
			rcptTo:    []string{"not-an-address", "bob@example.com"},
			wantError: "invalidRecipients",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := storage.NewMemoryStorage()
			addTestEmail(store, "draft1", accountID, "inbox", &email.UserEmail{
				From:    email.Address{Address: accountID},
				To:      emailAddrs(tc.to),
				Cc:      emailAddrs(tc.cc),
				Bcc:     emailAddrs(tc.bcc),
				Subject: "envelope check",
				Text:    "body",
			})
			capture := sender.NewInMemoryRecording()
			handler := NewHandler(store, metadata.NewMemoryMetadata(),
				usermanager.NewNone(usermanager.Config{AdminUsername: "admin", AdminPassword: "password"}),
				capture)

			ctx := ContextWithClaims(context.Background(), &auth.Claims{Subject: accountID})
			args := json.RawMessage(fmt.Sprintf(
				`{"accountId":%q,"create":{"s1":{"identityId":"primary","emailId":"draft1","envelope":{"mailFrom":{"email":%q},"rcptTo":%s}}}}`,
				accountID, accountID, jsonAddrs(tc.rcptTo)))
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
				problem, ok := resp.NotCreated["s1"]
				if !ok || problem.Type != tc.wantError {
					t.Fatalf("notCreated = %+v, want type %q", resp.NotCreated, tc.wantError)
				}
				if _, created := resp.Created["s1"]; created {
					t.Error("submission was reported as created despite envelope validation failure")
				}
				if len(capture.Sent) != 0 {
					t.Fatalf("sent %d messages despite envelope validation failure: %+v", len(capture.Sent), capture.Sent)
				}
				return
			}

			if len(resp.Created) != 1 || len(capture.Sent) != 1 {
				t.Fatalf("created = %+v, sent = %d, want one of each on the valid path", resp.Created, len(capture.Sent))
			}
		})
	}
}

// TestEmailSubmissionSetMasqueradeReplyTo checks that a submission masquerading
// as another account always uses the From address for replies.
func TestEmailSubmissionSetMasqueradeReplyTo(t *testing.T) {
	const (
		caller = "alice@example.com"
		target = "bob@example.com"
	)

	tests := []struct {
		name        string
		replyTo     []string
		wantError   string
		wantReplyTo []string
	}{
		{name: "default Reply-To to From", wantReplyTo: []string{target}},
		{name: "matching Reply-To", replyTo: []string{target}, wantReplyTo: []string{target}},
		{name: "conflicting Reply-To", replyTo: []string{caller}, wantError: "invalidProperties"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := storage.NewMemoryStorage()
			replyTo := make([]email.Address, 0, len(tc.replyTo))
			for _, addr := range tc.replyTo {
				replyTo = append(replyTo, email.Address{Address: addr})
			}
			addTestEmail(store, "draft1", target, "inbox", &email.UserEmail{
				From:    email.Address{Address: target},
				ReplyTo: replyTo,
				To:      []email.Address{{Address: "dave@example.com"}},
				Subject: "subject",
				Text:    "body",
			})
			capture := sender.NewInMemoryRecording()
			handler := NewHandler(store, metadata.NewMemoryMetadata(),
				usermanager.NewNone(usermanager.Config{AdminUsername: "admin", AdminPassword: "password"}),
				capture)

			ctx := ContextWithClaims(context.Background(), &auth.Claims{
				Subject:                caller,
				EmissaryUserManagement: true,
			})
			args := json.RawMessage(fmt.Sprintf(
				`{"accountId":%q,"create":{"s1":{"identityId":"primary","emailId":"draft1"}}}`,
				target))
			result, err := handler.EmailSubmissionSet(ctx, target, args)
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
				if len(capture.Sent) != 0 {
					t.Fatalf("message was sent despite Reply-To mismatch: %+v", capture.Sent)
				}
				return
			}

			if len(resp.Created) != 1 || len(capture.Sent) != 1 {
				t.Fatalf("created = %+v, sent = %+v, want one of each", resp.Created, capture.Sent)
			}
			if got := capture.Sent[0].From.Address; got != target {
				t.Errorf("sent From = %q, want %q", got, target)
			}
			got := capture.Sent[0].ReplyTo
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

func TestToJMAPEmailStitchesAllKeywords(t *testing.T) {
	ctx := context.Background()
	const accountID = "alice@example.com"
	store := storage.NewMemoryStorage()
	addTestEmail(store, "email-1", accountID, "inbox", &email.UserEmail{Subject: "test"})

	allKeywords := []string{
		metadata.KeywordDraft,
		metadata.KeywordSeen,
		metadata.KeywordFlagged,
		metadata.KeywordAnswered,
		metadata.KeywordForwarded,
		metadata.KeywordJunk,
		metadata.KeywordNotJunk,
		metadata.KeywordPhishing,
	}
	metaStore := metadata.NewMemoryMetadata()
	if err := metaStore.Save(ctx, metadata.NewEmailMetadata("alice@example.com", "email-1", allKeywords...)); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	handler := NewHandler(store, metaStore,
		usermanager.NewNone(usermanager.Config{AdminUsername: "admin", AdminPassword: "password"}),
		sender.NewLogSender())

	result, err := handler.EmailGet(ctx, accountID, json.RawMessage(
		`{"accountId":"alice@example.com","ids":["email-1"]}`))
	if err != nil {
		t.Fatalf("EmailGet() error = %v", err)
	}
	var resp struct {
		List []struct {
			Keywords map[string]bool `json:"keywords"`
		} `json:"list"`
	}
	if err := json.Unmarshal(result, &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if len(resp.List) != 1 {
		t.Fatalf("Email/get returned %d messages, want 1", len(resp.List))
	}
	keywords := resp.List[0].Keywords
	for _, keyword := range allKeywords {
		if !keywords[keyword] {
			t.Errorf("keyword %q missing from stitched response: %+v", keyword, keywords)
		}
	}
}

func TestEmailGetUsesStoredReceivedAt(t *testing.T) {
	ctx := context.Background()
	const accountID = "alice@example.com"
	const receivedAt = "2026-11-18T04:05:06Z"
	messageDate := time.Date(2026, time.November, 17, 23, 0, 0, 0, time.UTC)

	store := storage.NewMemoryStorage()
	addTestEmail(store, "email-1", accountID, "inbox", &email.UserEmail{Subject: "test", Date: messageDate})
	metaStore := metadata.NewMemoryMetadata()
	meta := metadata.NewEmailMetadata("alice@example.com", "email-1")
	meta.ReceivedAt = receivedAt
	if err := metaStore.Save(ctx, meta); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	handler := NewHandler(store, metaStore,
		usermanager.NewNone(usermanager.Config{AdminUsername: "admin", AdminPassword: "password"}),
		sender.NewLogSender())

	result, err := handler.EmailGet(ctx, accountID, json.RawMessage(
		`{"accountId":"alice@example.com","ids":["email-1"]}`))
	if err != nil {
		t.Fatalf("EmailGet() error = %v", err)
	}
	var resp struct {
		List []struct {
			ReceivedAt string `json:"receivedAt"`
			SentAt     string `json:"sentAt"`
		} `json:"list"`
	}
	if err := json.Unmarshal(result, &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if len(resp.List) != 1 {
		t.Fatalf("Email/get returned %d messages, want 1", len(resp.List))
	}
	if resp.List[0].ReceivedAt != receivedAt {
		t.Errorf("receivedAt = %q, want stored %q", resp.List[0].ReceivedAt, receivedAt)
	}
	if want := metadata.FormatReceivedAt(messageDate); resp.List[0].SentAt != want {
		t.Errorf("sentAt = %q, want message Date %q", resp.List[0].SentAt, want)
	}
}

func TestEmailGetMissingMetadataDoesNotFail(t *testing.T) {
	ctx := context.Background()
	const accountID = "alice@example.com"
	store := storage.NewMemoryStorage()
	messageDate := time.Date(2026, time.November, 1, 9, 30, 0, 0, time.UTC)
	addTestEmail(store, "legacy-1", accountID, "inbox", &email.UserEmail{Subject: "legacy", Date: messageDate})

	// No metadata was ever saved for this message.
	handler := NewHandler(store, metadata.NewMemoryMetadata(),
		usermanager.NewNone(usermanager.Config{AdminUsername: "admin", AdminPassword: "password"}),
		sender.NewLogSender())
	result, err := handler.EmailGet(ctx, accountID, json.RawMessage(
		`{"accountId":"alice@example.com","ids":["legacy-1"]}`))
	if err != nil {
		t.Fatalf("EmailGet() error = %v", err)
	}
	var resp struct {
		List []struct {
			ID         string          `json:"id"`
			ReceivedAt string          `json:"receivedAt"`
			SentAt     string          `json:"sentAt"`
			Keywords   map[string]bool `json:"keywords"`
		} `json:"list"`
	}
	if err := json.Unmarshal(result, &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if len(resp.List) != 1 || resp.List[0].ID != "legacy-1" {
		t.Fatalf("Email/get response = %+v, want the message", resp)
	}
	if len(resp.List[0].Keywords) != 0 {
		t.Errorf("keywords = %+v, want empty for missing metadata", resp.List[0].Keywords)
	}
	// Legacy records have no stored receive time, so fall back to Date rather
	// than fabricating "now" at read time.
	wantDate := metadata.FormatReceivedAt(messageDate)
	if resp.List[0].ReceivedAt != wantDate {
		t.Errorf("receivedAt = %q, want legacy fallback %q", resp.List[0].ReceivedAt, wantDate)
	}
	if resp.List[0].SentAt != wantDate {
		t.Errorf("sentAt = %q, want %q", resp.List[0].SentAt, wantDate)
	}
}

func TestApplyKeywordPatch(t *testing.T) {
	tests := []struct {
		name    string
		patch   map[string]interface{}
		want    []string
		wantOK  bool
		wantErr string
	}{
		{name: "add", patch: map[string]interface{}{"keywords/$seen": true}, want: []string{metadata.KeywordSeen, metadata.KeywordFlagged}, wantOK: true},
		{name: "remove", patch: map[string]interface{}{"keywords/$flagged": nil}, want: []string{metadata.KeywordSeen}, wantOK: true},
		{name: "false is invalid", patch: map[string]interface{}{"keywords/$seen": false}, wantErr: "invalidProperties"},
		{name: "replace", patch: map[string]interface{}{"keywords": map[string]interface{}{"$junk": true, "$seen": false}}, want: []string{metadata.KeywordJunk}, wantOK: true},
		{name: "normalize", patch: map[string]interface{}{"keywords/$ANSWERED": true}, want: []string{metadata.KeywordSeen, metadata.KeywordFlagged, metadata.KeywordAnswered}, wantOK: true},
		{name: "unknown property ignored", patch: map[string]interface{}{"mailboxIds": map[string]interface{}{"inbox": true}}, want: []string{metadata.KeywordSeen, metadata.KeywordFlagged}, wantOK: true},
		{name: "receivedAt is read-only", patch: map[string]interface{}{"receivedAt": "1999-01-01T00:00:00Z"}, want: []string{metadata.KeywordSeen, metadata.KeywordFlagged}, wantOK: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			current := metadata.NewEmailMetadata("alice@example.com", "email-1", metadata.KeywordSeen, metadata.KeywordFlagged)
			got, ok, errType := applyKeywordPatch(current, tc.patch)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v (error type %q)", ok, tc.wantOK, errType)
			}
			if !ok {
				if errType != tc.wantErr {
					t.Errorf("error type = %q, want %q", errType, tc.wantErr)
				}
				return
			}
			if len(got.Keywords) != len(tc.want) {
				t.Fatalf("keywords = %+v, want %v", got.Keywords, tc.want)
			}
			for _, keyword := range tc.want {
				if !got.HasKeyword(keyword) {
					t.Errorf("keyword %q missing from %+v", keyword, got.Keywords)
				}
			}
			if got.ReceivedAt != current.ReceivedAt {
				t.Errorf("receivedAt = %q, want unchanged %q", got.ReceivedAt, current.ReceivedAt)
			}
			if current.HasKeyword(metadata.KeywordFlagged) != true {
				t.Error("original metadata was mutated")
			}
		})
	}
}

func TestEmailSetKeywordUpdates(t *testing.T) {
	ctx := context.Background()
	const accountID = "alice@example.com"
	store := storage.NewMemoryStorage()
	const id = "users/alice@example.com/inbox/20261117T120000_a.eml"
	addTestEmail(store, id, accountID, "inbox", &email.UserEmail{Subject: "test"})
	metaStore := metadata.NewMemoryMetadata()
	handler := NewHandler(store, metaStore,
		usermanager.NewNone(usermanager.Config{AdminUsername: "admin", AdminPassword: "password"}),
		sender.NewLogSender())

	update := func(patch map[string]interface{}) (map[string]interface{}, map[string]interface{}) {
		t.Helper()
		args, err := json.Marshal(map[string]interface{}{
			"accountId": accountID,
			"update":    map[string]interface{}{id: patch},
		})
		if err != nil {
			t.Fatalf("marshal args: %v", err)
		}
		result, err := handler.EmailSet(ctx, accountID, args)
		if err != nil {
			t.Fatalf("EmailSet() error = %v", err)
		}
		var resp struct {
			Updated    map[string]interface{} `json:"updated"`
			NotUpdated map[string]interface{} `json:"notUpdated"`
		}
		if err := json.Unmarshal(result, &resp); err != nil {
			t.Fatalf("unmarshal response: %v", err)
		}
		return resp.Updated, resp.NotUpdated
	}

	updated, notUpdated := update(map[string]interface{}{
		"keywords/$seen":      true,
		"keywords/$flagged":   true,
		"keywords/$forwarded": true,
	})
	if len(updated) != 1 || len(notUpdated) != 0 {
		t.Fatalf("updated = %+v, notUpdated = %+v", updated, notUpdated)
	}
	meta, err := metaStore.Get(ctx, "alice@example.com", id)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	for _, keyword := range []string{metadata.KeywordSeen, metadata.KeywordFlagged, metadata.KeywordForwarded} {
		if !meta.HasKeyword(keyword) {
			t.Errorf("keyword %q not set after update: %+v", keyword, meta.Keywords)
		}
	}

	// null clears a keyword; false is invalid.
	updated, notUpdated = update(map[string]interface{}{"keywords/$seen": nil})
	if len(updated) != 1 || len(notUpdated) != 0 {
		t.Fatalf("updated = %+v, notUpdated = %+v", updated, notUpdated)
	}
	meta, _ = metaStore.Get(ctx, "alice@example.com", id)
	if meta.HasKeyword(metadata.KeywordSeen) {
		t.Error("$seen still set after null patch")
	}
	_, notUpdated = update(map[string]interface{}{"keywords/$seen": false})
	if problem, ok := notUpdated[id].(map[string]interface{}); !ok || problem["type"] != "invalidProperties" {
		t.Errorf("notUpdated = %+v, want invalidProperties", notUpdated)
	}

	// A message belonging to another account cannot be updated.
	updated, notUpdated = updateForAccount(t, handler, "bob@example.com", id)
	if len(updated) != 0 || len(notUpdated) != 1 {
		t.Fatalf("cross-account updated = %+v, notUpdated = %+v", updated, notUpdated)
	}
}

func updateForAccount(t *testing.T, handler *Handler, accountID, id string) (map[string]interface{}, map[string]interface{}) {
	t.Helper()
	args, err := json.Marshal(map[string]interface{}{
		"accountId": accountID,
		"update":    map[string]interface{}{id: map[string]interface{}{"keywords/$seen": true}},
	})
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	result, err := handler.EmailSet(context.Background(), accountID, args)
	if err != nil {
		t.Fatalf("EmailSet() error = %v", err)
	}
	var resp struct {
		Updated    map[string]interface{} `json:"updated"`
		NotUpdated map[string]interface{} `json:"notUpdated"`
	}
	if err := json.Unmarshal(result, &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	return resp.Updated, resp.NotUpdated
}

func TestEmailSetRejectsMalformedAddressEntries(t *testing.T) {
	tests := []struct {
		name     string
		property string
		entry    string
	}{
		{"to email is not a string", "to", `{"email":123}`},
		{"to email is missing", "to", `{}`},
		{"cc email is not a string", "cc", `{"email":123}`},
		{"bcc email is not a string", "bcc", `{"email":123}`},
		{"to entry is not an object", "to", `123`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := NewHandler(storage.NewMemoryStorage(), metadata.NewMemoryMetadata(),
				usermanager.NewNone(usermanager.Config{AdminUsername: "admin", AdminPassword: "password"}),
				sender.NewLogSender())
			args := json.RawMessage(fmt.Sprintf(
				`{"accountId":"alice@example.com","create":{"c1":{"%s":[%s]}}}`, tt.property, tt.entry))

			_, err := handler.EmailSet(context.Background(), "alice@example.com", args)
			if err == nil || !strings.Contains(err.Error(), "invalidArguments") {
				t.Fatalf("EmailSet() error = %v, want an invalidArguments error", err)
			}
		})
	}
}

func TestMailboxQueryClampsNegativePagination(t *testing.T) {
	const accountID = "alice@example.com"
	handler := NewHandler(storage.NewMemoryStorage(), metadata.NewMemoryMetadata(),
		usermanager.NewNone(usermanager.Config{AdminUsername: "admin", AdminPassword: "password"}),
		sender.NewLogSender())

	idsFor := func(t *testing.T, args string) []string {
		t.Helper()
		result, err := handler.MailboxQuery(context.Background(), accountID, json.RawMessage(args))
		if err != nil {
			t.Fatalf("MailboxQuery(%s) error = %v", args, err)
		}
		var resp struct {
			IDs []string `json:"ids"`
		}
		if err := json.Unmarshal(result, &resp); err != nil {
			t.Fatalf("unmarshal response: %v", err)
		}
		return resp.IDs
	}

	baseline := idsFor(t, `{"accountId":"alice@example.com"}`)
	if len(baseline) == 0 {
		t.Fatal("MailboxQuery() returned no ids")
	}
	if got := idsFor(t, `{"accountId":"alice@example.com","position":-5}`); fmt.Sprint(got) != fmt.Sprint(baseline) {
		t.Errorf("negative position ids = %v, want %v", got, baseline)
	}
	if got := idsFor(t, `{"accountId":"alice@example.com","limit":-1}`); len(got) != 0 {
		t.Errorf("negative limit ids = %v, want none", got)
	}
	if got := idsFor(t, `{"accountId":"alice@example.com","position":-1,"limit":1}`); len(got) != 1 || got[0] != baseline[0] {
		t.Errorf("negative position with limit ids = %v, want [%s]", got, baseline[0])
	}
}

func TestEmailQueryClampsNegativePagination(t *testing.T) {
	const accountID = "alice@example.com"
	store := storage.NewMemoryStorage()
	for _, id := range []string{"e1", "e2"} {
		addTestEmail(store, id, accountID, "inbox", &email.UserEmail{Subject: id})
	}
	handler := NewHandler(store, metadata.NewMemoryMetadata(),
		usermanager.NewNone(usermanager.Config{AdminUsername: "admin", AdminPassword: "password"}),
		sender.NewLogSender())

	idsFor := func(t *testing.T, args string) []string {
		t.Helper()
		result, err := handler.EmailQuery(context.Background(), accountID, json.RawMessage(args))
		if err != nil {
			t.Fatalf("EmailQuery(%s) error = %v", args, err)
		}
		var resp struct {
			IDs []string `json:"ids"`
		}
		if err := json.Unmarshal(result, &resp); err != nil {
			t.Fatalf("unmarshal response: %v", err)
		}
		return resp.IDs
	}

	baseline := idsFor(t, `{"accountId":"alice@example.com"}`)
	if len(baseline) != 2 {
		t.Fatalf("EmailQuery() ids = %v, want two messages", baseline)
	}
	if got := idsFor(t, `{"accountId":"alice@example.com","position":-5}`); fmt.Sprint(got) != fmt.Sprint(baseline) {
		t.Errorf("negative position ids = %v, want %v", got, baseline)
	}
	if got := idsFor(t, `{"accountId":"alice@example.com","limit":-1}`); len(got) != 0 {
		t.Errorf("negative limit ids = %v, want none", got)
	}
	if got := idsFor(t, `{"accountId":"alice@example.com","position":-1,"limit":1}`); len(got) != 1 || got[0] != baseline[0] {
		t.Errorf("negative position with limit ids = %v, want [%s]", got, baseline[0])
	}
}

func TestCountUnreadInMailboxUsesKeywords(t *testing.T) {
	ctx := context.Background()
	const accountID = "alice@example.com"
	store := storage.NewMemoryStorage()
	for _, id := range []string{"unread", "seen", "draft"} {
		addTestEmail(store, id, accountID, "inbox", &email.UserEmail{Subject: id})
	}
	metaStore := metadata.NewMemoryMetadata()
	if err := metaStore.Save(ctx, metadata.NewEmailMetadata("alice@example.com", "seen", metadata.KeywordSeen)); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	if err := metaStore.Save(ctx, metadata.NewEmailMetadata("alice@example.com", "draft", metadata.KeywordDraft)); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	handler := NewHandler(store, metaStore,
		usermanager.NewNone(usermanager.Config{AdminUsername: "admin", AdminPassword: "password"}),
		sender.NewLogSender())

	if got := handler.countUnreadInMailbox(ctx, accountID, "inbox"); got != 1 {
		t.Errorf("countUnreadInMailbox() = %d, want 1", got)
	}
}
