package jmap

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

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
	testSender := sender.NewLogSender("test@example.com")

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
	testSender := sender.NewLogSender("test@example.com")

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
	testSender := sender.NewLogSender("test@example.com")

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
		sender.NewLogSender("test@example.com"))

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
