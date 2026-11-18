package metadata

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFileMetadataDirectoryStoresOneFilePerEmail(t *testing.T) {
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "metadata")
	store, err := NewFileMetadata(dir)
	if err != nil {
		t.Fatalf("NewFileMetadata() error = %v", err)
	}
	if !store.directory {
		t.Fatal("NewFileMetadata(dir) did not select directory mode")
	}

	userID := "alice@example.com"
	mailID := "users/alice@example.com/inbox/message.eml"
	meta := NewEmailMetadata(userID, mailID, KeywordSeen)
	meta.ReceivedAt = FormatReceivedAt(time.Date(2026, time.November, 18, 1, 2, 3, 0, time.UTC))
	if err := store.Save(ctx, meta); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	recordPath := filepath.Join(dir, userID, "users", "alice@example.com", "inbox", "message.eml.json")
	if _, err := os.Stat(recordPath); err != nil {
		t.Fatalf("metadata record %q missing: %v", recordPath, err)
	}

	reopened, err := NewFileMetadata(dir)
	if err != nil {
		t.Fatalf("reopen error = %v", err)
	}
	got, err := reopened.Get(ctx, userID, mailID)
	if err != nil || got.ReceivedAt != meta.ReceivedAt || !got.IsRead() {
		t.Fatalf("Get() = %+v, %v", got, err)
	}

	missing, err := reopened.Get(ctx, userID, "unknown")
	if err != nil || missing.ID != "unknown" || missing.ReceivedAt != "" {
		t.Errorf("Get(missing) = %+v, %v", missing, err)
	}

	if err := reopened.Save(ctx, NewEmailMetadata(userID, "email-2", KeywordFlagged)); err != nil {
		t.Fatalf("Save(email-2) error = %v", err)
	}
	list, err := reopened.List(ctx, userID)
	if err != nil || len(list) != 2 {
		t.Fatalf("List() = %v, %v", list, err)
	}
	if list[0].ID != "email-2" || list[1].ID != mailID {
		t.Errorf("List() ids = %q, %q", list[0].ID, list[1].ID)
	}

	// Different user sees nothing.
	other, err := reopened.List(ctx, "bob@example.com")
	if err != nil || len(other) != 0 {
		t.Errorf("List(other) = %v, %v", other, err)
	}

	if err := reopened.Delete(ctx, userID, mailID); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if _, err := os.Stat(recordPath); !os.IsNotExist(err) {
		t.Errorf("record still exists after delete: %v", err)
	}
	if err := reopened.Delete(ctx, userID, mailID); err != nil {
		t.Errorf("Delete() again = %v", err)
	}
	list, err = reopened.List(ctx, userID)
	if err != nil || len(list) != 1 || list[0].ID != "email-2" {
		t.Errorf("List() after delete = %v, %v", list, err)
	}
}

func TestFileMetadataDirectoryRejectsTraversal(t *testing.T) {
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "metadata")
	store, err := NewFileMetadata(dir)
	if err != nil {
		t.Fatalf("NewFileMetadata() error = %v", err)
	}
	userID := "alice@example.com"
	if err := store.Save(ctx, NewEmailMetadata(userID, "../../outside")); err == nil {
		t.Error("Save() traversal id succeeded")
	}
	if _, err := store.Get(ctx, userID, "../../outside"); err == nil {
		t.Error("Get() traversal id succeeded")
	}
	if err := store.Delete(ctx, userID, "../../outside"); err == nil {
		t.Error("Delete() traversal id succeeded")
	}
}

func TestFileMetadataLegacyFileStillWorks(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "metadata.json")
	store, err := NewFileMetadata(path)
	if err != nil {
		t.Fatalf("NewFileMetadata() error = %v", err)
	}
	if store.directory {
		t.Fatal("NewFileMetadata(.json) selected directory mode")
	}
	if err := store.Save(ctx, NewEmailMetadata("alice@example.com", "email-1", KeywordSeen)); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if len(data) == 0 || data[0] != '[' {
		t.Errorf("legacy file does not hold a JSON array: %q", data)
	}
	reopened, err := NewFileMetadata(path)
	if err != nil {
		t.Fatalf("reopen error = %v", err)
	}
	meta, err := reopened.Get(ctx, "alice@example.com", "email-1")
	if err != nil || !meta.IsRead() {
		t.Errorf("Get() = %+v, %v", meta, err)
	}
	other, err := reopened.List(ctx, "bob@example.com")
	if err != nil || len(other) != 0 {
		t.Errorf("List(other) = %v, %v", other, err)
	}
}
