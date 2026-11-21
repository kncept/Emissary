package storage

import (
	"bytes"
	"context"
	"errors"
	"sort"
	"testing"
	"time"

	"github.com/kncept/Emissary/mailer/email"
)

type contractBackend struct {
	name string
	open func(*testing.T) Storage
}

// contractBackends is the single matrix used by the storage contract tests.
// Every invariant below must hold for all three implementations.
func contractBackends() []contractBackend {
	return []contractBackend{
		{
			name: "filesystem",
			open: func(t *testing.T) Storage {
				store, _ := newTestFileStorage(t)
				return store
			},
		},
		{
			name: "s3",
			open: func(t *testing.T) Storage {
				return &S3Storage{client: newFakeS3(nil), bucket: "mail-bucket"}
			},
		},
		{
			name: "memory",
			open: func(*testing.T) Storage {
				return NewMemoryStorage()
			},
		},
	}
}

func contractAccountID() string { return "contract@example.com" }

func contractPtr(value string) *string { return &value }

func addContractEmail(t *testing.T, store Storage, accountID, mailboxID, subject string, date time.Time, raw []byte) string {
	t.Helper()
	id, err := store.AddEmail(context.Background(), accountID, mailboxID, &email.UserEmail{
		From:    email.Address{Address: "sender@example.com"},
		To:      []email.Address{{Address: accountID}},
		Subject: subject,
		Text:    "contract body",
		Date:    date,
	}, raw)
	if err != nil {
		t.Fatalf("AddEmail(%q, %q) error = %v", mailboxID, subject, err)
	}
	if id == "" {
		t.Fatalf("AddEmail(%q, %q) returned empty id", mailboxID, subject)
	}
	return id
}

func assertContractRaw(t *testing.T, store Storage, accountID, id string, want []byte) {
	t.Helper()
	got, err := store.GetEmailRaw(context.Background(), accountID, id)
	if err != nil {
		t.Fatalf("GetEmailRaw(%q) error = %v", id, err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("GetEmailRaw(%q) = %q, want original bytes %q", id, got, want)
	}
}

func TestStorageContractMailboxes(t *testing.T) {
	ctx := context.Background()
	accountID := contractAccountID()

	for _, backend := range contractBackends() {
		t.Run(backend.name, func(t *testing.T) {
			store := backend.open(t)

			defaults, err := store.ListMailboxes(ctx, accountID)
			if err != nil {
				t.Fatalf("ListMailboxes() error = %v", err)
			}
			if len(defaults) != len(DefaultMailboxes()) {
				t.Fatalf("ListMailboxes() returned %d defaults, want %d", len(defaults), len(DefaultMailboxes()))
			}
			for _, want := range DefaultMailboxes() {
				found := false
				for _, got := range defaults {
					if got.ID == want.ID {
						found = true
						if got.Role == nil || *got.Role != *want.Role {
							t.Errorf("default mailbox %q role = %v, want %q", want.ID, got.Role, *want.Role)
						}
						break
					}
				}
				if !found {
					t.Errorf("default mailbox %q missing from %+v", want.ID, defaults)
				}
			}

			created, err := store.CreateMailbox(ctx, accountID, Mailbox{
				ID:        "  Projects  ",
				Name:      "  Team Projects  ",
				ParentID:  contractPtr("  INBOX "),
				SortOrder: 9,
			})
			if err != nil {
				t.Fatalf("CreateMailbox() error = %v", err)
			}
			if created.ID != "projects" || created.Name != "Team Projects" {
				t.Errorf("CreateMailbox() = %+v, want normalized id/name", created)
			}
			if created.ParentID == nil || *created.ParentID != "inbox" {
				t.Errorf("CreateMailbox() parent = %v, want inbox", created.ParentID)
			}

			if _, err := store.CreateMailbox(ctx, accountID, Mailbox{ID: "projects", Name: "Duplicate"}); !errors.Is(err, ErrMailboxExists) {
				t.Errorf("duplicate CreateMailbox() error = %v, want ErrMailboxExists", err)
			}
			if _, err := store.CreateMailbox(ctx, accountID, Mailbox{ID: " ", Name: "Blank"}); err == nil || err.Error() != "invalid mailbox" {
				t.Errorf("CreateMailbox() blank id error = %v, want invalid mailbox", err)
			}
			if _, err := store.CreateMailbox(ctx, accountID, Mailbox{ID: "orphan", Name: "Orphan", ParentID: contractPtr("missing")}); !errors.Is(err, ErrNotFound) {
				t.Errorf("CreateMailbox() missing parent error = %v, want ErrNotFound", err)
			}
			if _, err := store.CreateMailbox(ctx, accountID, Mailbox{ID: "self", Name: "Self", ParentID: contractPtr("self")}); err == nil || err.Error() != "mailbox cannot be its own parent" {
				t.Errorf("CreateMailbox() self parent error = %v, want mailbox cannot be its own parent", err)
			}

			updated, err := store.UpdateMailbox(ctx, accountID, Mailbox{
				ID:        "projects",
				Name:      "  Renamed Projects  ",
				ParentID:  contractPtr("  SENT "),
				SortOrder: 4,
			})
			if err != nil {
				t.Fatalf("UpdateMailbox() error = %v", err)
			}
			if updated.Name != "Renamed Projects" || updated.ParentID == nil || *updated.ParentID != "sent" {
				t.Errorf("UpdateMailbox() = %+v, want normalized name and sent parent", updated)
			}
			if _, err := store.UpdateMailbox(ctx, accountID, Mailbox{ID: "projects", Name: "Projects", ParentID: contractPtr("projects")}); err == nil || err.Error() != "mailbox cannot be its own parent" {
				t.Errorf("UpdateMailbox() self parent error = %v, want mailbox cannot be its own parent", err)
			}
			if _, err := store.UpdateMailbox(ctx, accountID, Mailbox{ID: "missing", Name: "Missing"}); !errors.Is(err, ErrNotFound) {
				t.Errorf("UpdateMailbox() missing error = %v, want ErrNotFound", err)
			}

			mailboxes, err := store.ListMailboxes(ctx, accountID)
			if err != nil {
				t.Fatalf("ListMailboxes() after update error = %v", err)
			}
			found := false
			for _, mb := range mailboxes {
				if mb.ID == "projects" {
					found = true
					if mb.Name != "Renamed Projects" || mb.ParentID == nil || *mb.ParentID != "sent" {
						t.Errorf("listed mailbox = %+v, want updated record", mb)
					}
				}
			}
			if !found {
				t.Error("updated mailbox projects missing from ListMailboxes()")
			}
		})
	}
}

func TestStorageContractListEmailsOrdering(t *testing.T) {
	ctx := context.Background()
	accountID := contractAccountID()
	base := time.Date(2026, time.November, 9, 12, 0, 0, 0, time.UTC)

	for _, backend := range contractBackends() {
		t.Run(backend.name, func(t *testing.T) {
			store := backend.open(t)
			specs := []struct {
				name    string
				subject string
				date    time.Time
			}{
				{name: "old", subject: "Old", date: base.Add(-2 * time.Hour)},
				{name: "mid", subject: "Mid", date: base.Add(-time.Hour)},
				{name: "new", subject: "New", date: base},
				{name: "tie-a", subject: "Tie A", date: base},
				{name: "tie-b", subject: "Tie B", date: base},
			}
			ids := make(map[string]string, len(specs))
			for _, spec := range specs {
				ids[spec.name] = addContractEmail(t, store, accountID, "inbox", spec.subject, spec.date, nil)
			}

			// All backends standardize on Email.Date descending, with Ref.ID
			// descending as the deterministic tie-breaker. The three newest
			// messages share a timestamp, so compute their expected order from
			// the ids the stores returned.
			tied := []string{ids["new"], ids["tie-a"], ids["tie-b"]}
			sort.Slice(tied, func(i, j int) bool { return tied[i] > tied[j] })
			want := append(tied, ids["mid"], ids["old"])

			got, err := store.ListEmails(ctx, accountID, "inbox")
			if err != nil {
				t.Fatalf("ListEmails() error = %v", err)
			}
			if len(got) != len(want) {
				t.Fatalf("ListEmails() returned %d messages, want %d", len(got), len(want))
			}
			for i := range want {
				if got[i].Ref.ID != want[i] {
					t.Errorf("ListEmails()[%d].ID = %q, want %q (date desc, id desc)", i, got[i].Ref.ID, want[i])
				}
			}
		})
	}
}

func TestStorageContractResolveEmailByID(t *testing.T) {
	ctx := context.Background()
	accountID := contractAccountID()
	base := time.Date(2026, time.November, 9, 12, 0, 0, 0, time.UTC)

	for _, backend := range contractBackends() {
		t.Run(backend.name, func(t *testing.T) {
			store := backend.open(t)
			id := addContractEmail(t, store, accountID, "inbox", "Resolve me", base, nil)
			fullKey := email.DeliveredMessageKey(accountID, "inbox", id)

			for _, tc := range []struct {
				name string
				id   string
			}{
				{name: "stable token", id: id},
				{name: "full key", id: fullKey},
				{name: "leading slash full key", id: "/" + fullKey},
			} {
				t.Run(tc.name, func(t *testing.T) {
					got, err := store.GetEmail(ctx, accountID, tc.id)
					if err != nil {
						t.Fatalf("GetEmail(%q) error = %v", tc.id, err)
					}
					if got.Ref.ID != id || got.Email.Subject != "Resolve me" {
						t.Errorf("GetEmail(%q) = %+v, want stable id %q", tc.id, got, id)
					}
				})
			}

			if _, err := store.GetEmail(ctx, "other@example.com", fullKey); !errors.Is(err, ErrNotFound) {
				t.Errorf("GetEmail() with another account error = %v, want ErrNotFound", err)
			}
		})
	}
}

func TestStorageContractAddAndMoveEmail(t *testing.T) {
	ctx := context.Background()
	accountID := contractAccountID()
	base := time.Date(2026, time.November, 9, 12, 0, 0, 0, time.UTC)

	for _, backend := range contractBackends() {
		t.Run(backend.name, func(t *testing.T) {
			store := backend.open(t)
			// Render the raw bytes from the same message so every backend
			// parses the same subject back after the move.
			raw := email.RawFromUserEmail(&email.UserEmail{
				From:    email.Address{Address: "sender@example.com"},
				To:      []email.Address{{Address: accountID}},
				Subject: "Move me",
				Text:    "contract body",
				Date:    base,
			})
			id := addContractEmail(t, store, accountID, "inbox", "Move me", base, raw)
			assertContractRaw(t, store, accountID, id, raw)

			moved, err := store.MoveEmail(ctx, accountID, id, "sent")
			if err != nil {
				t.Fatalf("MoveEmail() error = %v", err)
			}
			if moved.Ref.ID != id || moved.Ref.MailboxID != "sent" || moved.Email.Subject != "Move me" {
				t.Errorf("MoveEmail() = %+v, want same id in sent", moved)
			}

			inbox, err := store.ListEmails(ctx, accountID, "inbox")
			if err != nil {
				t.Fatalf("ListEmails(inbox) error = %v", err)
			}
			if len(inbox) != 0 {
				t.Errorf("ListEmails(inbox) = %d messages, want 0 after move", len(inbox))
			}
			sent, err := store.ListEmails(ctx, accountID, "sent")
			if err != nil {
				t.Fatalf("ListEmails(sent) error = %v", err)
			}
			if len(sent) != 1 || sent[0].Ref.ID != id {
				t.Fatalf("ListEmails(sent) = %+v, want moved message %q", sent, id)
			}
			assertContractRaw(t, store, accountID, id, raw)

			if _, err := store.MoveEmail(ctx, accountID, id, "missing"); !errors.Is(err, ErrNotFound) {
				t.Errorf("MoveEmail() to missing mailbox error = %v, want ErrNotFound", err)
			}
			if _, err := store.MoveEmail(ctx, accountID, "missing.eml", "sent"); !errors.Is(err, ErrNotFound) {
				t.Errorf("MoveEmail() missing message error = %v, want ErrNotFound", err)
			}
		})
	}
}
