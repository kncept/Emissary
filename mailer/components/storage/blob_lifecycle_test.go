package storage

import (
	"context"
	"errors"
	"testing"
	"time"
)

func lifecycleStore(now *time.Time, quota int64) *BlobLifecycle {
	return &BlobLifecycle{
		Store: NewMemoryStorage(),
		Policy: BlobPolicy{
			QuotaBytes:  quota,
			MinLifetime: DefaultBlobMinLifetime,
			Now:         func() time.Time { return *now },
		},
	}
}

func blobExists(t *testing.T, store BlobStore, accountID, blobID string) bool {
	t.Helper()
	_, data, err := store.GetBlob(context.Background(), accountID, blobID)
	if errors.Is(err, ErrNotFound) {
		return false
	}
	if err != nil {
		t.Fatalf("GetBlob(%q) error = %v", blobID, err)
	}
	return data != nil
}

func TestBlobLifecycleMinimumLifetimeAndReferences(t *testing.T) {
	ctx := context.Background()
	start := time.Date(2026, time.November, 17, 12, 0, 0, 0, time.UTC)
	now := start
	lifecycle := lifecycleStore(&now, 1<<20)

	meta, err := lifecycle.Upload(ctx, "alice@example.com", "alice@example.com", "text/plain", []byte("expiring"))
	if err != nil {
		t.Fatalf("Upload() error = %v", err)
	}
	if meta.UploaderID != "alice@example.com" || !meta.CreatedAt.Equal(start) {
		t.Errorf("upload metadata = %+v, want uploader alice at %v", meta, start)
	}

	now = start.Add(30 * time.Minute)
	if err := lifecycle.Collect(ctx, meta.AccountID); err != nil {
		t.Fatalf("Collect() before one hour error = %v", err)
	}
	if !blobExists(t, lifecycle.Store, meta.AccountID, meta.ID) {
		t.Fatal("unreferenced blob was deleted before the one-hour minimum")
	}

	now = start.Add(2 * time.Hour)
	if err := lifecycle.Collect(ctx, meta.AccountID); err != nil {
		t.Fatalf("Collect() after one hour error = %v", err)
	}
	if blobExists(t, lifecycle.Store, meta.AccountID, meta.ID) {
		t.Fatal("expired unreferenced blob was not collected")
	}

	pinned, err := lifecycle.Upload(ctx, "alice@example.com", "alice@example.com", "text/plain", []byte("referenced"))
	if err != nil {
		t.Fatalf("Upload() pinned blob error = %v", err)
	}
	if _, err := lifecycle.AddRef(ctx, pinned.AccountID, pinned.ID); err != nil {
		t.Fatalf("AddRef() error = %v", err)
	}
	if err := lifecycle.Collect(ctx, pinned.AccountID); err != nil {
		t.Fatalf("Collect() with pinned blob error = %v", err)
	}
	if !blobExists(t, lifecycle.Store, pinned.AccountID, pinned.ID) {
		t.Fatal("pinned blob was collected")
	}
	if err := lifecycle.Store.DeleteBlob(ctx, pinned.AccountID, pinned.ID); !errors.Is(err, ErrBlobReferenced) {
		t.Errorf("DeleteBlob() with a reference error = %v, want ErrBlobReferenced", err)
	}
	if _, err := lifecycle.ReleaseRef(ctx, pinned.AccountID, pinned.ID); err != nil {
		t.Fatalf("ReleaseRef() error = %v", err)
	}
	if err := lifecycle.Store.DeleteBlob(ctx, pinned.AccountID, pinned.ID); err != nil {
		t.Fatalf("DeleteBlob() after release error = %v", err)
	}
}

func TestBlobLifecycleQuotaEvictsOldestUnreferencedBlobs(t *testing.T) {
	ctx := context.Background()
	start := time.Date(2026, time.November, 17, 12, 0, 0, 0, time.UTC)
	now := start
	lifecycle := lifecycleStore(&now, 10)
	accountID := "alice@example.com"

	first, err := lifecycle.Upload(ctx, accountID, "alice@example.com", "text/plain", []byte("aaaa"))
	if err != nil {
		t.Fatalf("Upload(first) error = %v", err)
	}
	now = start.Add(time.Minute)
	second, err := lifecycle.Upload(ctx, accountID, "alice@example.com", "text/plain", []byte("bbbbbb"))
	if err != nil {
		t.Fatalf("Upload(second) error = %v", err)
	}
	now = start.Add(2 * time.Minute)
	third, err := lifecycle.Upload(ctx, accountID, "alice@example.com", "text/plain", []byte("cccc"))
	if err != nil {
		t.Fatalf("Upload(third) error = %v", err)
	}

	// The quota is 10 bytes: the oldest 4-byte blob is evicted to make room
	// for the third upload, but the 6-byte blob is newer and stays.
	if blobExists(t, lifecycle.Store, accountID, first.ID) {
		t.Error("oldest unreferenced blob was not evicted for quota")
	}
	if !blobExists(t, lifecycle.Store, accountID, second.ID) || !blobExists(t, lifecycle.Store, accountID, third.ID) {
		t.Error("quota eviction removed more blobs than necessary")
	}

	if _, err := lifecycle.Upload(ctx, accountID, "alice@example.com", "text/plain", []byte("12345678901")); !errors.Is(err, ErrBlobQuota) {
		t.Errorf("oversized upload error = %v, want ErrBlobQuota", err)
	}
}

func TestBlobLifecycleQuotaSkipsReferencedBlobs(t *testing.T) {
	ctx := context.Background()
	start := time.Date(2026, time.November, 17, 12, 0, 0, 0, time.UTC)
	now := start
	lifecycle := lifecycleStore(&now, 10)
	accountID := "alice@example.com"

	pinned, err := lifecycle.Upload(ctx, accountID, "alice@example.com", "text/plain", []byte("aaaa"))
	if err != nil {
		t.Fatalf("Upload(pinned) error = %v", err)
	}
	if _, err := lifecycle.AddRef(ctx, accountID, pinned.ID); err != nil {
		t.Fatalf("AddRef() error = %v", err)
	}
	now = start.Add(time.Minute)
	older, err := lifecycle.Upload(ctx, accountID, "alice@example.com", "text/plain", []byte("bbbbbb"))
	if err != nil {
		t.Fatalf("Upload(older) error = %v", err)
	}
	now = start.Add(2 * time.Minute)
	newer, err := lifecycle.Upload(ctx, accountID, "alice@example.com", "text/plain", []byte("cccccc"))
	if err != nil {
		t.Fatalf("Upload(newer) error = %v", err)
	}

	if !blobExists(t, lifecycle.Store, accountID, pinned.ID) {
		t.Error("referenced blob was evicted for quota")
	}
	if blobExists(t, lifecycle.Store, accountID, older.ID) {
		t.Error("newer blob was evicted while an older unreferenced blob existed")
	}
	if !blobExists(t, lifecycle.Store, accountID, newer.ID) {
		t.Error("newest blob was evicted for quota")
	}
}

func TestBlobLifecycleReuploadResetsExpiry(t *testing.T) {
	ctx := context.Background()
	start := time.Date(2026, time.November, 17, 12, 0, 0, 0, time.UTC)
	now := start
	lifecycle := lifecycleStore(&now, 1<<20)
	data := []byte("same bytes")

	meta, err := lifecycle.Upload(ctx, "alice@example.com", "alice@example.com", "text/plain", data)
	if err != nil {
		t.Fatalf("Upload() error = %v", err)
	}
	now = start.Add(90 * time.Minute)
	again, err := lifecycle.Upload(ctx, "alice@example.com", "alice@example.com", "text/plain", data)
	if err != nil {
		t.Fatalf("re-Upload() error = %v", err)
	}
	if again.ID != meta.ID || again.CreatedAt != now {
		t.Errorf("re-Upload() = %+v, want id %q and time %v", again, meta.ID, now)
	}

	now = start.Add(2 * time.Hour)
	if err := lifecycle.Collect(ctx, meta.AccountID); err != nil {
		t.Fatalf("Collect() error = %v", err)
	}
	if !blobExists(t, lifecycle.Store, meta.AccountID, meta.ID) {
		t.Fatal("re-uploaded blob did not get a fresh expiry time")
	}
	stored, _, err := lifecycle.Store.GetBlob(ctx, meta.AccountID, meta.ID)
	if err != nil || stored.UploaderID != "alice@example.com" {
		t.Errorf("uploader metadata = %q, %v", stored.UploaderID, err)
	}
}
