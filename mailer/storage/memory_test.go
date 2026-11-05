package storage

import (
	"context"
	"testing"
	"time"

	"github.com/kncept/Emissary/mailer/email"
)

func TestMemoryStorage_AddAndGet(t *testing.T) {
	s := NewMemoryStorage()

	e := &email.Email{
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

func TestMemoryStorage_ListAllEmails(t *testing.T) {
	s := NewMemoryStorage()

	now := time.Now()
	s.Add(&email.Email{
		ID:      "1",
		From:    email.Address{Address: "a@example.com"},
		Subject: "First",
		Date:    now.Add(-time.Hour),
		Text:    "Body 1",
	})
	s.Add(&email.Email{
		ID:      "2",
		From:    email.Address{Address: "b@example.com"},
		Subject: "Second",
		Date:    now,
		Text:    "Body 2",
	})
	s.Add(&email.Email{
		ID:      "3",
		From:    email.Address{Address: "c@example.com"},
		Subject: "Third",
		Date:    now.Add(-2 * time.Hour),
		Text:    "Body 3",
	})

	emails, err := s.ListAllEmails(context.Background())
	if err != nil {
		t.Fatalf("ListAllEmails() error = %v", err)
	}

	if len(emails) != 3 {
		t.Fatalf("ListAllEmails() returned %d emails, want 3", len(emails))
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

func TestMemoryStorage_ListAllEmails_SameDateSortByID(t *testing.T) {
	s := NewMemoryStorage()

	now := time.Now()
	s.Add(&email.Email{ID: "a", From: email.Address{Address: "a@example.com"}, Subject: "A", Date: now, Text: "A"})
	s.Add(&email.Email{ID: "b", From: email.Address{Address: "b@example.com"}, Subject: "B", Date: now, Text: "B"})
	s.Add(&email.Email{ID: "c", From: email.Address{Address: "c@example.com"}, Subject: "C", Date: now, Text: "C"})

	emails, err := s.ListAllEmails(context.Background())
	if err != nil {
		t.Fatalf("ListAllEmails() error = %v", err)
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
			s.Add(&email.Email{
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
			s.ListAllEmails(context.Background())
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
	s.Add(&email.Email{ID: "", Subject: "Empty ID"})

	emails, _ := s.ListAllEmails(context.Background())
	if len(emails) != 0 {
		t.Errorf("ListAllEmails() returned %d emails, want 0", len(emails))
	}
}

func TestMemoryStorage_CloneIsolation(t *testing.T) {
	s := NewMemoryStorage()

	e := &email.Email{
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