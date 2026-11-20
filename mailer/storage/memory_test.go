package storage

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/kncept/Emissary/mailer/email"
)

func addTestEmail(s *MemoryStorage, id, accountID, mailboxID string, message *email.UserEmail) {
	s.Add(&email.ListedEmail{
		Ref:   &email.EmailRef{ID: id, AccountID: accountID, MailboxID: mailboxID},
		Email: message,
	})
}

func TestMemoryStorage_AddAndGet(t *testing.T) {
	s := NewMemoryStorage()

	e := &email.UserEmail{
		From:    email.Address{Address: "from@example.com"},
		To:      []email.Address{{Address: "to@example.com"}},
		Subject: "Test",
		Date:    time.Now(),
		Text:    "Body",
	}

	addTestEmail(s, "test-1", "user@example.com", "inbox", e)

	got, err := s.GetEmail(context.Background(), "user@example.com", "test-1")
	if err != nil {
		t.Fatalf("GetEmail() error = %v", err)
	}
	if got.Ref.ID != "test-1" {
		t.Errorf("ID = %q, want %q", got.Ref.ID, "test-1")
	}
	if got.Email.Subject != e.Subject {
		t.Errorf("Subject = %q, want %q", got.Email.Subject, e.Subject)
	}
}

func TestMemoryStorage_AddEmailGeneratesIDAndUsesInbox(t *testing.T) {
	s := NewMemoryStorage()
	message := &email.UserEmail{Subject: "New", Text: "Body"}

	id, err := s.AddEmail(context.Background(), "alice@example.com", "inbox", message, nil)
	if err != nil {
		t.Fatalf("AddEmail() error = %v", err)
	}
	if id == "" {
		t.Fatal("AddEmail() returned an empty id")
	}

	listed, err := s.GetEmail(context.Background(), "alice@example.com", id)
	if err != nil {
		t.Fatalf("GetEmail() error = %v", err)
	}
	if listed.Ref.AccountID != "alice@example.com" || listed.Ref.MailboxID != "inbox" {
		t.Errorf("ref = %+v, want account alice@example.com and inbox", listed.Ref)
	}
	if listed.Email.Subject != "New" {
		t.Errorf("Subject = %q, want New", listed.Email.Subject)
	}
}

func TestMemoryStorage_GetNotFound(t *testing.T) {
	s := NewMemoryStorage()

	_, err := s.GetEmail(context.Background(), "user@example.com", "nonexistent")
	if err == nil {
		t.Error("GetEmail() should return ErrNotFound for missing email")
	}
	if err != ErrNotFound {
		t.Errorf("GetEmail() error = %v, want %v", err, ErrNotFound)
	}
}

func TestMemoryStorage_GetEmailRawRendersMessage(t *testing.T) {
	s := NewMemoryStorage()
	addTestEmail(s, "test-1", "user@example.com", "inbox", &email.UserEmail{
		From:    email.Address{Name: "Alice", Address: "alice@example.com"},
		To:      []email.Address{{Address: "bob@example.com"}},
		Subject: "Hello",
		Text:    "Body",
	})

	raw, err := s.GetEmailRaw(context.Background(), "user@example.com", "test-1")
	if err != nil {
		t.Fatalf("GetEmailRaw() error = %v", err)
	}
	message := string(raw)
	for _, want := range []string{"From: \"Alice\" <alice@example.com>", "To: bob@example.com", "Subject: Hello", "\r\n\r\nBody"} {
		if !strings.Contains(message, want) {
			t.Errorf("GetEmailRaw() = %q, want it to contain %q", message, want)
		}
	}
	if _, err := s.GetEmailRaw(context.Background(), "user@example.com", "missing"); err != ErrNotFound {
		t.Errorf("GetEmailRaw() error = %v, want ErrNotFound", err)
	}
}

func TestMemoryStorage_ListEmails(t *testing.T) {
	s := NewMemoryStorage()
	now := time.Now()
	addTestEmail(s, "alice-inbox", "alice@example.com", "inbox", &email.UserEmail{Date: now})
	addTestEmail(s, "alice-sent", "alice@example.com", "sent", &email.UserEmail{Date: now})
	addTestEmail(s, "bob-inbox", "bob@example.com", "inbox", &email.UserEmail{Date: now})

	aliceInbox, err := s.ListEmails(context.Background(), "ALICE@example.com", "inbox")
	if err != nil {
		t.Fatalf("ListEmails() error = %v", err)
	}
	if len(aliceInbox) != 1 || aliceInbox[0].Ref.ID != "alice-inbox" {
		t.Fatalf("alice inbox = %v, want only alice-inbox", aliceInbox)
	}

	aliceAll, err := s.ListEmails(context.Background(), "alice@example.com", "")
	if err != nil {
		t.Fatalf("ListEmails() error = %v", err)
	}
	if len(aliceAll) != 2 {
		t.Fatalf("alice all mailboxes = %v, want 2 emails", aliceAll)
	}

	bobInbox, err := s.ListEmails(context.Background(), "bob@example.com", "inbox")
	if err != nil {
		t.Fatalf("ListEmails() error = %v", err)
	}
	if len(bobInbox) != 1 || bobInbox[0].Ref.ID != "bob-inbox" {
		t.Fatalf("bob inbox = %v, want only bob-inbox", bobInbox)
	}

	if _, err := s.ListEmails(context.Background(), "", "inbox"); err == nil {
		t.Error("ListEmails() with empty accountID should fail")
	}
}

func TestMemoryStorage_ListEmailsSortsByDate(t *testing.T) {
	s := NewMemoryStorage()

	now := time.Now()
	addTestEmail(s, "1", "user@example.com", "inbox", &email.UserEmail{
		From:    email.Address{Address: "a@example.com"},
		Subject: "First",
		Date:    now.Add(-time.Hour),
		Text:    "Body 1",
	})
	addTestEmail(s, "2", "user@example.com", "inbox", &email.UserEmail{
		From:    email.Address{Address: "b@example.com"},
		Subject: "Second",
		Date:    now,
		Text:    "Body 2",
	})
	addTestEmail(s, "3", "user@example.com", "inbox", &email.UserEmail{
		From:    email.Address{Address: "c@example.com"},
		Subject: "Third",
		Date:    now.Add(-2 * time.Hour),
		Text:    "Body 3",
	})

	emails, err := s.ListEmails(context.Background(), "user@example.com", "")
	if err != nil {
		t.Fatalf("ListEmails() error = %v", err)
	}

	if len(emails) != 3 {
		t.Fatalf("ListEmails() returned %d emails, want 3", len(emails))
	}

	// Should be sorted by date descending (newest first)
	if emails[0].Ref.ID != "2" {
		t.Errorf("First email ID = %q, want %q", emails[0].Ref.ID, "2")
	}
	if emails[1].Ref.ID != "1" {
		t.Errorf("Second email ID = %q, want %q", emails[1].Ref.ID, "1")
	}
	if emails[2].Ref.ID != "3" {
		t.Errorf("Third email ID = %q, want %q", emails[2].Ref.ID, "3")
	}
}

func TestMemoryStorage_ListEmailsSameDateSortByID(t *testing.T) {
	s := NewMemoryStorage()

	now := time.Now()
	addTestEmail(s, "a", "user@example.com", "inbox", &email.UserEmail{From: email.Address{Address: "a@example.com"}, Subject: "A", Date: now, Text: "A"})
	addTestEmail(s, "b", "user@example.com", "inbox", &email.UserEmail{From: email.Address{Address: "b@example.com"}, Subject: "B", Date: now, Text: "B"})
	addTestEmail(s, "c", "user@example.com", "inbox", &email.UserEmail{From: email.Address{Address: "c@example.com"}, Subject: "C", Date: now, Text: "C"})

	emails, err := s.ListEmails(context.Background(), "user@example.com", "")
	if err != nil {
		t.Fatalf("ListEmails() error = %v", err)
	}

	// When dates are equal, sort by ID descending
	if emails[0].Ref.ID != "c" || emails[1].Ref.ID != "b" || emails[2].Ref.ID != "a" {
		t.Errorf("Order = %v, want [c, b, a]", emails)
	}
}

func TestMemoryStorage_ConcurrentAccess(t *testing.T) {
	s := NewMemoryStorage()
	done := make(chan bool, 100)

	// Add emails concurrently
	for i := 0; i < 50; i++ {
		go func(id string) {
			addTestEmail(s, id, "user@example.com", "inbox", &email.UserEmail{
				From:    email.Address{Address: "from@example.com"},
				Subject: "Test " + id,
				Date:    time.Now(),
				Text:    "Body",
			})
			done <- true
		}("email-" + string(rune('0'+i%10)))
	}

	// List emails concurrently
	for i := 0; i < 50; i++ {
		go func() {
			s.ListEmails(context.Background(), "user@example.com", "")
			done <- true
		}()
	}

	// Wait for all goroutines
	for i := 0; i < 100; i++ {
		<-done
	}
}

func TestMemoryStorage_AddNilOrEmptyID(t *testing.T) {
	s := NewMemoryStorage()

	s.Add(nil)
	s.Add(&email.ListedEmail{Ref: &email.EmailRef{ID: ""}, Email: &email.UserEmail{Subject: "Empty ID"}})

	emails, _ := s.ListEmails(context.Background(), "user@example.com", "")
	if len(emails) != 0 {
		t.Errorf("ListEmails() returned %d emails, want 0", len(emails))
	}
}

func TestMemoryStorage_CloneIsolation(t *testing.T) {
	s := NewMemoryStorage()

	e := &email.UserEmail{
		From:    email.Address{Address: "from@example.com"},
		To:      []email.Address{{Address: "to@example.com"}},
		Subject: "Test",
		Date:    time.Now(),
		Text:    "Body",
	}

	addTestEmail(s, "test-1", "user@example.com", "inbox", e)

	// Modify original
	e.Subject = "Modified"

	// Retrieved email should not be affected
	got, _ := s.GetEmail(context.Background(), "user@example.com", "test-1")
	if got.Email.Subject != "Test" {
		t.Errorf("Got modified email: Subject = %q, want %q", got.Email.Subject, "Test")
	}
}

func TestMemoryStorage_DeleteEmail(t *testing.T) {
	s := NewMemoryStorage()

	addTestEmail(s, "test-1", "user@example.com", "inbox", &email.UserEmail{
		From:    email.Address{Address: "from@example.com"},
		To:      []email.Address{{Address: "to@example.com"}},
		Subject: "Test",
		Date:    time.Now(),
		Text:    "Body",
	})

	if err := s.DeleteEmail(context.Background(), "user@example.com", "test-1"); err != nil {
		t.Fatalf("DeleteEmail() error = %v", err)
	}

	if _, err := s.GetEmail(context.Background(), "user@example.com", "test-1"); err != ErrNotFound {
		t.Errorf("GetEmail() after delete error = %v, want ErrNotFound", err)
	}
	emails, err := s.ListEmails(context.Background(), "user@example.com", "")
	if err != nil {
		t.Fatalf("ListEmails() error = %v", err)
	}
	if len(emails) != 0 {
		t.Errorf("ListEmails() after delete returned %d emails, want 0", len(emails))
	}

	// Deleting an absent email is reported, and deleting twice does not resurrect it.
	if err := s.DeleteEmail(context.Background(), "user@example.com", "test-1"); err != ErrNotFound {
		t.Errorf("DeleteEmail() absent error = %v, want ErrNotFound", err)
	}
	if err := s.DeleteEmail(context.Background(), "user@example.com", "never-existed"); err != ErrNotFound {
		t.Errorf("DeleteEmail() nonexistent error = %v, want ErrNotFound", err)
	}
}
