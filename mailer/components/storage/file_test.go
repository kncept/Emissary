package storage

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kncept/Emissary/mailer/email"
)

func newTestFileStorage(t *testing.T) (*FileStorage, string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "mail")
	store, err := NewFileStorage(root)
	if err != nil {
		t.Fatalf("NewFileStorage() error = %v", err)
	}
	return store, root
}

func fileTestMessage() *email.UserEmail {
	return &email.UserEmail{
		From:    email.Address{Address: "sender@example.com"},
		To:      []email.Address{{Address: "alice@example.com"}},
		Subject: "hello filesystem",
		Text:    "body text",
		Date:    time.Date(2026, time.November, 18, 1, 2, 3, 0, time.UTC),
	}
}

func TestFileStorageAddListGetDelete(t *testing.T) {
	ctx := context.Background()
	store, root := newTestFileStorage(t)
	if store.Name() != "filesystem" {
		t.Errorf("Name() = %q, want filesystem", store.Name())
	}

	id, err := store.AddEmail(ctx, "Alice@Example.com", "inbox", fileTestMessage(), nil)
	if err != nil {
		t.Fatalf("AddEmail() error = %v", err)
	}
	if strings.ContainsAny(id, "/+=") || strings.Contains(id, ".") {
		t.Errorf("AddEmail() id = %q, want URL-safe opaque id", id)
	}
	token, err := email.EmailStorageToken(id)
	if err != nil {
		t.Fatalf("EmailStorageToken(%q) error = %v", id, err)
	}
	path := filepath.Join(root, "users", "alice@example.com", "inbox", token)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("message file %q missing: %v", path, err)
	}

	listed, err := store.GetEmail(ctx, "alice@example.com", id)
	if err != nil {
		t.Fatalf("GetEmail() error = %v", err)
	}
	if listed.Ref.AccountID != "alice@example.com" || listed.Ref.MailboxID != "inbox" || listed.Ref.ID != id {
		t.Errorf("ref = %+v, want account alice@example.com inbox id %s", listed.Ref, id)
	}
	if listed.Email.Subject != "hello filesystem" {
		t.Errorf("subject = %q", listed.Email.Subject)
	}

	raw, err := store.GetEmailRaw(ctx, "alice@example.com", id)
	if err != nil || !strings.Contains(string(raw), "Subject: hello filesystem") {
		t.Errorf("GetEmailRaw() = %q, %v", raw, err)
	}

	// A user record and mailbox/session scratch files are not messages.
	if err := os.WriteFile(filepath.Join(root, "users", "alice@example.com", "user.json"), []byte("{}"), 0644); err != nil {
		t.Fatalf("write user.json: %v", err)
	}
	for _, tc := range []struct {
		account, mailbox string
		want             int
	}{
		{"alice@example.com", "inbox", 1},
		{"alice@example.com", "", 1},
		{"alice@example.com", "sent", 0},
		{"bob@example.com", "inbox", 0},
	} {
		emails, err := store.ListEmails(ctx, tc.account, tc.mailbox)
		if err != nil {
			t.Fatalf("ListEmails(%q, %q) error = %v", tc.account, tc.mailbox, err)
		}
		if len(emails) != tc.want {
			t.Errorf("ListEmails(%q, %q) = %d, want %d", tc.account, tc.mailbox, len(emails), tc.want)
		}
	}

	if err := store.DeleteEmail(ctx, "alice@example.com", id); err != nil {
		t.Fatalf("DeleteEmail() error = %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("message file still exists after delete: %v", err)
	}
	if _, err := store.GetEmail(ctx, "alice@example.com", id); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetEmail() after delete = %v, want ErrNotFound", err)
	}
	if err := store.DeleteEmail(ctx, "alice@example.com", id); !errors.Is(err, ErrNotFound) {
		t.Errorf("DeleteEmail() again = %v, want ErrNotFound", err)
	}
}

func TestFileStoragePersistsAcrossReopen(t *testing.T) {
	ctx := context.Background()
	store, root := newTestFileStorage(t)
	id, err := store.AddEmail(ctx, "alice@example.com", "inbox", fileTestMessage(), nil)
	if err != nil {
		t.Fatalf("AddEmail() error = %v", err)
	}

	reopened, err := NewFileStorage(root)
	if err != nil {
		t.Fatalf("reopen error = %v", err)
	}
	if _, err := reopened.GetEmail(ctx, "alice@example.com", id); err != nil {
		t.Fatalf("GetEmail() after reopen = %v", err)
	}
}

func TestFileStorageDraftsAndSessionState(t *testing.T) {
	ctx := context.Background()
	store, root := newTestFileStorage(t)

	id, err := store.PutDraft(ctx, "session-1", "draft-1", fileTestMessage())
	if err != nil {
		t.Fatalf("PutDraft() error = %v", err)
	}
	if id != "drafts/draft-1.json" {
		t.Errorf("PutDraft() id = %q", id)
	}
	if _, err := os.Stat(filepath.Join(root, "_storage", "session-1", "drafts", "draft-1.json")); err != nil {
		t.Fatalf("draft file missing: %v", err)
	}
	draft, err := store.GetDraft(ctx, "session-1", id)
	if err != nil || draft.Subject != "hello filesystem" {
		t.Fatalf("GetDraft() = %+v, %v", draft, err)
	}
	if err := store.DeleteDraft(ctx, "session-1", id); err != nil {
		t.Fatalf("DeleteDraft() error = %v", err)
	}
	if _, err := store.GetDraft(ctx, "session-1", id); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetDraft() after delete = %v", err)
	}
	if err := store.DeleteDraft(ctx, "session-1", id); err != nil {
		t.Errorf("DeleteDraft() again = %v, want nil", err)
	}

	ids, _, err := store.LoadCreatedIDs(ctx, "session-1")
	if err != nil || len(ids) != 0 {
		t.Fatalf("LoadCreatedIDs() = %v, %v", ids, err)
	}
	if _, err := store.SaveCreatedIDs(ctx, "session-1", map[string]string{"c1": id}, ""); err != nil {
		t.Fatalf("SaveCreatedIDs() error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "_storage", "session-1", createdIDsFile)); err != nil {
		t.Fatalf("created ids file missing: %v", err)
	}
	ids, _, err = store.LoadCreatedIDs(ctx, "session-1")
	if err != nil || ids["c1"] != id {
		t.Errorf("LoadCreatedIDs() = %v, %v", ids, err)
	}
}

func TestFileStorageCreatedIDsRevisionAcrossInstances(t *testing.T) {
	ctx := context.Background()
	store, root := newTestFileStorage(t)

	revision, err := store.SaveCreatedIDs(ctx, "session-1", map[string]string{"create1": "id-1"}, "")
	if err != nil {
		t.Fatalf("SaveCreatedIDs() error = %v", err)
	}
	restarted, err := NewFileStorage(root)
	if err != nil {
		t.Fatalf("NewFileStorage() error = %v", err)
	}
	ids, loadedRevision, err := restarted.LoadCreatedIDs(ctx, "session-1")
	if err != nil || loadedRevision != revision || ids["create1"] != "id-1" {
		t.Fatalf("restart load = %v / %q / %v", ids, loadedRevision, err)
	}
	if _, err := store.SaveCreatedIDs(ctx, "session-1", map[string]string{"stale": "x"}, ""); !errors.Is(err, ErrSessionStateConflict) {
		t.Fatalf("stale create error = %v, want ErrSessionStateConflict", err)
	}
	if _, err := restarted.SaveCreatedIDs(ctx, "session-1", map[string]string{"create1": "id-1", "create2": "id-2"}, loadedRevision); err != nil {
		t.Fatalf("conditional update error = %v", err)
	}
	ids, _, err = store.LoadCreatedIDs(ctx, "session-1")
	if err != nil || ids["create1"] != "id-1" || ids["create2"] != "id-2" {
		t.Fatalf("final ids = %v / %v", ids, err)
	}
}

func TestFileStorageRejectsTraversal(t *testing.T) {
	ctx := context.Background()
	store, root := newTestFileStorage(t)
	outside := filepath.Join(filepath.Dir(root), "secret.eml")
	if err := os.WriteFile(outside, []byte("secret"), 0644); err != nil {
		t.Fatalf("write secret: %v", err)
	}

	for _, id := range []string{"../secret.eml", "users/alice/../../secret.eml", "..\\secret.eml"} {
		if _, err := store.GetEmail(ctx, "alice@example.com", id); err == nil {
			t.Errorf("GetEmail(%q) succeeded", id)
		}
		if _, err := store.GetEmailRaw(ctx, "alice@example.com", id); err == nil {
			t.Errorf("GetEmailRaw(%q) succeeded", id)
		}
		if err := store.DeleteEmail(ctx, "alice@example.com", id); err == nil {
			t.Errorf("DeleteEmail(%q) succeeded", id)
		}
	}
	if _, err := store.AddEmail(ctx, "../evil", "inbox", fileTestMessage(), nil); err == nil {
		t.Error("AddEmail with traversal account succeeded")
	}
	if _, err := store.PutDraft(ctx, "../evil", "d1", fileTestMessage()); err == nil {
		t.Error("PutDraft with traversal session succeeded")
	}
}

func TestFileStorageBlobLivesUnderReservedDirectory(t *testing.T) {
	ctx := context.Background()
	store, root := newTestFileStorage(t)
	data := []byte("reserved directory bytes")

	meta, err := store.PutBlob(ctx, "Alice@Example.com", "text/plain", data)
	if err != nil {
		t.Fatalf("PutBlob() error = %v", err)
	}
	path := filepath.Join(root, "users", "alice@example.com", "_blobs", meta.ID)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("blob file %q missing: %v", path, err)
	}
	if _, err := os.Stat(path + blobMetadataSuffix); err != nil {
		t.Fatalf("blob metadata %q missing: %v", path+blobMetadataSuffix, err)
	}

	mailboxes, err := store.ListMailboxes(ctx, "alice@example.com")
	if err != nil {
		t.Fatalf("ListMailboxes() error = %v", err)
	}
	for _, mailbox := range mailboxes {
		if mailbox.ID == "_blobs" {
			t.Error("ListMailboxes() exposed _blobs as a mailbox")
		}
	}
}
