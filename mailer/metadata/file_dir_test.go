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

	meta := NewEmailMetadata("users/alice@example.com/inbox/message.eml", KeywordSeen)
	meta.ReceivedAt = FormatReceivedAt(time.Date(2026, time.November, 18, 1, 2, 3, 0, time.UTC))
	if err := store.Save(ctx, meta); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	recordPath := filepath.Join(dir, "users", "alice@example.com", "inbox", "message.eml.json")
	if _, err := os.Stat(recordPath); err != nil {
		t.Fatalf("metadata record %q missing: %v", recordPath, err)
	}

	reopened, err := NewFileMetadata(dir)
	if err != nil {
		t.Fatalf("reopen error = %v", err)
	}
	got, err := reopened.Get(ctx, meta.ID)
	if err != nil || got.ReceivedAt != meta.ReceivedAt || !got.IsRead() {
		t.Fatalf("Get() = %+v, %v", got, err)
	}

	missing, err := reopened.Get(ctx, "unknown")
	if err != nil || missing.ID != "unknown" || missing.ReceivedAt != "" {
		t.Errorf("Get(missing) = %+v, %v", missing, err)
	}

	if err := reopened.Save(ctx, NewEmailMetadata("email-2", KeywordFlagged)); err != nil {
		t.Fatalf("Save(email-2) error = %v", err)
	}
	list, err := reopened.List(ctx)
	if err != nil || len(list) != 2 {
		t.Fatalf("List() = %v, %v", list, err)
	}
	if list[0].ID != "email-2" || list[1].ID != meta.ID {
		t.Errorf("List() ids = %q, %q", list[0].ID, list[1].ID)
	}

	if err := reopened.Delete(ctx, meta.ID); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if _, err := os.Stat(recordPath); !os.IsNotExist(err) {
		t.Errorf("record still exists after delete: %v", err)
	}
	if err := reopened.Delete(ctx, meta.ID); err != nil {
		t.Errorf("Delete() again = %v", err)
	}
	list, err = reopened.List(ctx)
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
	if err := store.Save(ctx, NewEmailMetadata("../../outside")); err == nil {
		t.Error("Save() traversal id succeeded")
	}
	if _, err := store.Get(ctx, "../../outside"); err == nil {
		t.Error("Get() traversal id succeeded")
	}
	if err := store.Delete(ctx, "../../outside"); err == nil {
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
	if err := store.Save(ctx, NewEmailMetadata("email-1", KeywordSeen)); err != nil {
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
	meta, err := reopened.Get(ctx, "email-1")
	if err != nil || !meta.IsRead() {
		t.Errorf("Get() = %+v, %v", meta, err)
	}
}
