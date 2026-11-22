package mailbox

import (
	"context"
	"testing"

	"github.com/kncept/Emissary/mailer/components/metadata"
	"github.com/kncept/Emissary/mailer/components/storage"
	mailemail "github.com/kncept/Emissary/mailer/email"
	"github.com/kncept/Emissary/mailer/jmap/internal/base"
)

// countingStorage records the ListEmails calls made by the mailbox hydrator.
// Embedding the interface keeps every other Storage method from the real store.
type countingStorage struct {
	storage.Storage
	listCalls  int
	mailboxIDs []string
}

func (s *countingStorage) ListEmails(ctx context.Context, accountID, mailboxID string) ([]*mailemail.ListedEmail, error) {
	s.listCalls++
	s.mailboxIDs = append(s.mailboxIDs, mailboxID)
	return s.Storage.ListEmails(ctx, accountID, mailboxID)
}

// TestGetMailboxesSharesOneListingPerRequest guards the JMAP batch behaviour:
// Mailbox/query and Mailbox/get must reuse one account-wide listing instead of
// listing every folder several times each.
func TestGetMailboxesSharesOneListingPerRequest(t *testing.T) {
	store := storage.NewMemoryStorage()
	counting := &countingStorage{Storage: store}
	h := New(&base.Deps{
		Storage:  counting,
		Metadata: metadata.NewMemoryMetadata(),
		Changes:  base.NewTracker(store),
	})

	ctx := base.WithRequestCache(context.Background())
	for _, mailboxID := range []string{"inbox", "junk"} {
		if _, err := store.AddEmail(ctx, mailboxTestAccount, mailboxID,
			&mailemail.UserEmail{Subject: mailboxID}, nil); err != nil {
			t.Fatalf("AddEmail(%q) error = %v", mailboxID, err)
		}
	}

	first := h.getMailboxes(ctx, mailboxTestAccount)
	second := h.getMailboxes(ctx, mailboxTestAccount)
	if counting.listCalls != 1 {
		t.Fatalf("ListEmails calls = %d, want 1 for the whole JMAP request", counting.listCalls)
	}
	if len(counting.mailboxIDs) != 1 || counting.mailboxIDs[0] != "" {
		t.Fatalf("ListEmails mailbox ids = %q, want one account-wide listing", counting.mailboxIDs)
	}

	counts := make(map[string]int, len(first))
	for _, mb := range first {
		counts[mb.ID] = mb.TotalEmails
	}
	if counts["inbox"] != 1 || counts["junk"] != 1 || counts["trash"] != 0 {
		t.Errorf("totalEmails by mailbox = %v, want inbox=1 junk=1 trash=0", counts)
	}
	if len(second) != len(first) {
		t.Fatalf("second getMailboxes returned %d mailboxes, want %d", len(second), len(first))
	}

	// A fresh request must see fresh data rather than the previous snapshot.
	freshCtx := base.WithRequestCache(context.Background())
	_ = h.getMailboxes(freshCtx, mailboxTestAccount)
	if counting.listCalls != 2 {
		t.Errorf("ListEmails calls after new request = %d, want 2", counting.listCalls)
	}
}
