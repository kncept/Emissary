package base

import (
	"testing"
	"time"

	mailemail "github.com/kncept/Emissary/mailer/email"
)

func TestThreadIDIsStableDigestNotEmailAlias(t *testing.T) {
	msg := &mailemail.UserEmail{
		From:      mailemail.Address{Address: "sender@example.com"},
		To:        []mailemail.Address{{Address: "recipient@example.com"}},
		Subject:   "Thread fixture",
		Date:      time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		Text:      "body",
		MessageID: "<fixture@example.com>",
	}
	id := ThreadID("email-1", msg)
	if id == "" || id == "email-1" {
		t.Fatalf("ThreadID() = %q, want a stable digest distinct from the email id", id)
	}
	if again := ThreadID("email-1", msg); again != id {
		t.Fatalf("ThreadID() is not stable: %q then %q", id, again)
	}
	if other := ThreadID("email-2", msg); other == id {
		t.Fatal("ThreadID() reused a thread for two emails despite 1:1 grouping")
	}
	// Without threading headers the digest must still not be the email id.
	msg.MessageID = ""
	if fallback := ThreadID("email-1", msg); fallback == "" || fallback == "email-1" {
		t.Fatalf("ThreadID() without Message-ID = %q, want a digest", fallback)
	}
}

func TestPageBoundsClampsUntrustedArguments(t *testing.T) {
	position, limit := -5, 10
	start, end := PageBounds(7, &position, &limit)
	if start != 0 || end != 7 {
		t.Fatalf("PageBounds() = (%d, %d), want (0, 7)", start, end)
	}

	position, limit = 100, 2
	start, end = PageBounds(7, &position, &limit)
	if start != 7 || end != 7 {
		t.Fatalf("PageBounds() = (%d, %d), want (7, 7)", start, end)
	}

	position, limit = 3, -2
	start, end = PageBounds(7, &position, &limit)
	if start != 3 || end != 3 {
		t.Fatalf("PageBounds() = (%d, %d), want (3, 3)", start, end)
	}
}
