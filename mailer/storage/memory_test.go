package storage

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/kncept/Emissary/mailer/email"
)

func TestMemoryStorage_AddAndGet(t *testing.T) {
	s := NewMemoryStorage()

	e := &email.UserEmail{
		ID:      "test-1",
		From:    email.Address{Address: "from@example.com"},
		To:      []email.Address{{Address: "to@example.com"}},
		Subject: "Test",
		Date:    time.Now(),
		Text:    "Body",
	}

	s.Add(e)

	got, err := s.GetEmail(context.Background(), "test-1")
	if err != nil {
		t.Fatalf("GetEmail() error = %v", err)
	}
	if got.ID != e.ID {
		t.Errorf("ID = %q, want %q", got.ID, e.ID)
	}
	if got.Subject != e.Subject {
		t.Errorf("Subject = %q, want %q", got.Subject, e.Subject)
	}
}

func TestMemoryStorage_GetNotFound(t *testing.T) {
	s := NewMemoryStorage()

	_, err := s.GetEmail(context.Background(), "nonexistent")
	if err == nil {
		t.Error("GetEmail() should return ErrNotFound for missing email")
	}
	if err != ErrNotFound {
		t.Errorf("GetEmail() error = %v, want %v", err, ErrNotFound)
	}
}

func TestMemoryStorage_GetEmailRawRendersMessage(t *testing.T) {
	s := NewMemoryStorage()
	s.Add(&email.UserEmail{
		ID:      "test-1",
		From:    email.Address{Name: "Alice", Address: "alice@example.com"},
		To:      []email.Address{{Address: "bob@example.com"}},
		Subject: "Hello",
		Text:    "Body",
	})

	raw, err := s.GetEmailRaw(context.Background(), "test-1")
	if err != nil {
		t.Fatalf("GetEmailRaw() error = %v", err)
	}
	message := string(raw)
	for _, want := range []string{"From: \"Alice\" <alice@example.com>", "To: bob@example.com", "Subject: Hello", "\r\n\r\nBody"} {
		if !strings.Contains(message, want) {
			t.Errorf("GetEmailRaw() = %q, want it to contain %q", message, want)
		}
	}
	if _, err := s.GetEmailRaw(context.Background(), "missing"); err != ErrNotFound {
		t.Errorf("GetEmailRaw() error = %v, want ErrNotFound", err)
	}
}

func TestMemoryStorage_ListAccountEmails(t *testing.T) {
	s := NewMemoryStorage()
	now := time.Now()
	s.Add(&email.UserEmail{ID: "alice-inbox", AccountID: "alice@example.com", MailboxID: "inbox", Date: now})
	s.Add(&email.UserEmail{ID: "alice-sent", AccountID: "alice@example.com", MailboxID: "sent", Date: now})
	s.Add(&email.UserEmail{ID: "bob-inbox", AccountID: "bob@example.com", MailboxID: "inbox", Date: now})

	aliceInbox, err := s.ListAccountEmails(context.Background(), "ALICE@example.com", "inbox")
	if err != nil {
		t.Fatalf("ListAccountEmails() error = %v", err)
	}
	if len(aliceInbox) != 1 || aliceInbox[0].ID != "alice-inbox" {
		t.Fatalf("alice inbox = %v, want only alice-inbox", aliceInbox)
	}

	aliceAll, err := s.ListAccountEmails(context.Background(), "alice@example.com", "")
	if err != nil {
		t.Fatalf("ListAccountEmails() error = %v", err)
	}
	if len(aliceAll) != 2 {
		t.Fatalf("alice all mailboxes = %v, want 2 emails", aliceAll)
	}

	bobInbox, err := s.ListAccountEmails(context.Background(), "bob@example.com", "inbox")
	if err != nil {
		t.Fatalf("ListAccountEmails() error = %v", err)
	}
	if len(bobInbox) != 1 || bobInbox[0].ID != "bob-inbox" {
		t.Fatalf("bob inbox = %v, want only bob-inbox", bobInbox)
	}

	if _, err := s.ListAccountEmails(context.Background(), "", "inbox"); err == nil {
		t.Error("ListAccountEmails() with empty accountID should fail")
	}
}

func TestMemoryStorage_ListAccountEmailsSortsByDate(t *testing.T) {
	s := NewMemoryStorage()

	now := time.Now()
	s.Add(&email.UserEmail{
		ID:        "1",
		AccountID: "user@example.com",
		From:      email.Address{Address: "a@example.com"},
		Subject:   "First",
		Date:      now.Add(-time.Hour),
		Text:      "Body 1",
	})
	s.Add(&email.UserEmail{
		ID:        "2",
		AccountID: "user@example.com",
		From:      email.Address{Address: "b@example.com"},
		Subject:   "Second",
		Date:      now,
		Text:      "Body 2",
	})
	s.Add(&email.UserEmail{
		ID:        "3",
		AccountID: "user@example.com",
		From:      email.Address{Address: "c@example.com"},
		Subject:   "Third",
		Date:      now.Add(-2 * time.Hour),
		Text:      "Body 3",
	})

	emails, err := s.ListAccountEmails(context.Background(), "user@example.com", "")
	if err != nil {
		t.Fatalf("ListAccountEmails() error = %v", err)
	}

	if len(emails) != 3 {
		t.Fatalf("ListAccountEmails() returned %d emails, want 3", len(emails))
	}

	// Should be sorted by date descending (newest first)
	if emails[0].ID != "2" {
		t.Errorf("First email ID = %q, want %q", emails[0].ID, "2")
	}
	if emails[1].ID != "1" {
		t.Errorf("Second email ID = %q, want %q", emails[1].ID, "1")
	}
	if emails[2].ID != "3" {
		t.Errorf("Third email ID = %q, want %q", emails[2].ID, "3")
	}
}

func TestMemoryStorage_ListAccountEmailsSameDateSortByID(t *testing.T) {
	s := NewMemoryStorage()

	now := time.Now()
	s.Add(&email.UserEmail{ID: "a", AccountID: "user@example.com", From: email.Address{Address: "a@example.com"}, Subject: "A", Date: now, Text: "A"})
	s.Add(&email.UserEmail{ID: "b", AccountID: "user@example.com", From: email.Address{Address: "b@example.com"}, Subject: "B", Date: now, Text: "B"})
	s.Add(&email.UserEmail{ID: "c", AccountID: "user@example.com", From: email.Address{Address: "c@example.com"}, Subject: "C", Date: now, Text: "C"})

	emails, err := s.ListAccountEmails(context.Background(), "user@example.com", "")
	if err != nil {
		t.Fatalf("ListAccountEmails() error = %v", err)
	}

	// When dates are equal, sort by ID descending
	if emails[0].ID != "c" || emails[1].ID != "b" || emails[2].ID != "a" {
		t.Errorf("Order = %v, want [c, b, a]", emails)
	}
}

func TestMemoryStorage_ConcurrentAccess(t *testing.T) {
	s := NewMemoryStorage()
	done := make(chan bool, 100)

	// Add emails concurrently
	for i := 0; i < 50; i++ {
		go func(id string) {
			s.Add(&email.UserEmail{
				ID:      id,
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
			s.ListAccountEmails(context.Background(), "user@example.com", "")
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
	s.Add(&email.UserEmail{ID: "", Subject: "Empty ID"})

	emails, _ := s.ListAccountEmails(context.Background(), "user@example.com", "")
	if len(emails) != 0 {
		t.Errorf("ListAccountEmails() returned %d emails, want 0", len(emails))
	}
}

func TestMemoryStorage_CloneIsolation(t *testing.T) {
	s := NewMemoryStorage()

	e := &email.UserEmail{
		ID:      "test-1",
		From:    email.Address{Address: "from@example.com"},
		To:      []email.Address{{Address: "to@example.com"}},
		Subject: "Test",
		Date:    time.Now(),
		Text:    "Body",
	}

	s.Add(e)

	// Modify original
	e.Subject = "Modified"

	// Retrieved email should not be affected
	got, _ := s.GetEmail(context.Background(), "test-1")
	if got.Subject != "Test" {
		t.Errorf("Got modified email: Subject = %q, want %q", got.Subject, "Test")
	}
}

func TestMemoryStorage_DeleteEmail(t *testing.T) {
	s := NewMemoryStorage()

	s.Add(&email.UserEmail{
		ID:      "test-1",
		From:    email.Address{Address: "from@example.com"},
		To:      []email.Address{{Address: "to@example.com"}},
		Subject: "Test",
		Date:    time.Now(),
		Text:    "Body",
	})

	if err := s.DeleteEmail(context.Background(), "test-1"); err != nil {
		t.Fatalf("DeleteEmail() error = %v", err)
	}

	if _, err := s.GetEmail(context.Background(), "test-1"); err != ErrNotFound {
		t.Errorf("GetEmail() after delete error = %v, want ErrNotFound", err)
	}
	emails, err := s.ListAccountEmails(context.Background(), "user@example.com", "")
	if err != nil {
		t.Fatalf("ListAccountEmails() error = %v", err)
	}
	if len(emails) != 0 {
		t.Errorf("ListAccountEmails() after delete returned %d emails, want 0", len(emails))
	}

	// Deleting an absent email is reported, and deleting twice does not resurrect it.
	if err := s.DeleteEmail(context.Background(), "test-1"); err != ErrNotFound {
		t.Errorf("DeleteEmail() absent error = %v, want ErrNotFound", err)
	}
	if err := s.DeleteEmail(context.Background(), "never-existed"); err != ErrNotFound {
		t.Errorf("DeleteEmail() nonexistent error = %v, want ErrNotFound", err)
	}
}
