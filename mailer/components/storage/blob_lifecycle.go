package storage

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// BlobPolicy holds the RFC 8620 section 6 lifecycle settings. QuotaBytes is
// the per-uploader quota for *unreferenced* blobs; it is separate from the
// account's mail quota, as the RFC recommends. A non-positive value disables
// quota enforcement. MinLifetime is the minimum time an unreferenced blob
// survives; zero selects DefaultBlobMinLifetime (one hour).
type BlobPolicy struct {
	QuotaBytes  int64
	MinLifetime time.Duration
	Now         func() time.Time
}

// BlobLifecycle applies the unreferenced-blob policy on top of a BlobStore.
// It is deliberately kept out of the individual backends: S3, filesystem and
// memory all get the same quota, expiry and reference rules.
type BlobLifecycle struct {
	Store  BlobStore
	Policy BlobPolicy
}

// NewBlobLifecycle builds a lifecycle manager. quotaBytes is normally the
// advertised maxSizeUpload (also the maximum total attachment size for one
// message in this server), which meets the RFC's recommended minimum for the
// separate unreferenced-blob quota.
func NewBlobLifecycle(store BlobStore, quotaBytes int64) *BlobLifecycle {
	return &BlobLifecycle{
		Store:  store,
		Policy: BlobPolicy{QuotaBytes: quotaBytes, MinLifetime: DefaultBlobMinLifetime},
	}
}

func (l *BlobLifecycle) now() time.Time {
	if l != nil && l.Policy.Now != nil {
		return l.Policy.Now().UTC()
	}
	return time.Now().UTC()
}

func (l *BlobLifecycle) minLifetime() time.Duration {
	if l != nil && l.Policy.MinLifetime > 0 {
		return l.Policy.MinLifetime
	}
	return DefaultBlobMinLifetime
}

// Upload stores bytes for an uploader, enforcing the separate unreferenced
// blob quota. When an upload would exceed the quota, unreferenced blobs owned
// by the same uploader are deleted oldest first until there is room, as
// required by RFC 8620. Referenced blobs are never evicted. Re-uploading the
// same bytes reuses the id and resets its expiry time.
func (l *BlobLifecycle) Upload(ctx context.Context, accountID, uploaderID, contentType string, data []byte) (Blob, error) {
	if l == nil || l.Store == nil {
		return Blob{}, errors.New("blob lifecycle is not configured")
	}
	if err := validateBlobData(data); err != nil {
		return Blob{}, err
	}
	now := l.now()
	quota := l.Policy.QuotaBytes
	if quota > 0 {
		_, err := l.Store.GetBlobMeta(ctx, accountID, BlobID(data))
		if err != nil && !errors.Is(err, ErrNotFound) {
			return Blob{}, err
		}
		// A re-upload does not consume additional storage regardless of who
		// repeats it: the bytes are already in this account and only their
		// uploader and expiry metadata are refreshed.
		reupload := err == nil
		if !reupload && int64(len(data)) > quota {
			return Blob{}, ErrBlobQuota
		}
		if !reupload {
			blobs, err := l.Store.ListBlobs(ctx, accountID)
			if err != nil {
				return Blob{}, err
			}
			uploader := canonicalBlobUploader(uploaderID, accountID)
			used := int64(0)
			candidates := make([]Blob, 0, len(blobs))
			for _, blob := range blobs {
				if blob.IsPinned() || blob.Uploader() != uploader || blob.ID == BlobID(data) {
					continue
				}
				used += blob.Size
				candidates = append(candidates, blob)
			}
			if used+int64(len(data)) > quota {
				sortBlobsOldestFirst(candidates)
				for _, blob := range candidates {
					if used+int64(len(data)) <= quota {
						break
					}
					if err := l.Store.DeleteBlob(ctx, accountID, blob.ID); err != nil && !errors.Is(err, ErrNotFound) {
						return Blob{}, fmt.Errorf("evict unreferenced blob %q: %w", blob.ID, err)
					}
					used -= blob.Size
				}
			}
			if used+int64(len(data)) > quota {
				return Blob{}, ErrBlobQuota
			}
		}
	}
	return l.Store.UploadBlob(ctx, accountID, uploaderID, contentType, data, now)
}

// Collect deletes unreferenced blobs that have outlived the minimum lifetime.
// It never touches pinned blobs, and quota eviction in Upload may remove older
// blobs sooner when the uploader is out of quota.
func (l *BlobLifecycle) Collect(ctx context.Context, accountID string) error {
	if l == nil || l.Store == nil {
		return errors.New("blob lifecycle is not configured")
	}
	blobs, err := l.Store.ListBlobs(ctx, accountID)
	if err != nil {
		return err
	}
	now, minLifetime := l.now(), l.minLifetime()
	for _, blob := range blobs {
		if blob.IsPinned() || now.Sub(blob.CreatedAt) < minLifetime {
			continue
		}
		if err := l.Store.DeleteBlob(ctx, accountID, blob.ID); err != nil && !errors.Is(err, ErrNotFound) {
			return fmt.Errorf("collect blob %q: %w", blob.ID, err)
		}
	}
	return nil
}

// AddRef pins a blob so lifecycle cleanup cannot delete it while a JMAP
// method is still using it.
func (l *BlobLifecycle) AddRef(ctx context.Context, accountID, blobID string) (Blob, error) {
	return l.Store.AddBlobRefs(ctx, accountID, blobID, 1)
}

// ReleaseRef drops a pin held by a JMAP method.
func (l *BlobLifecycle) ReleaseRef(ctx context.Context, accountID, blobID string) (Blob, error) {
	return l.Store.AddBlobRefs(ctx, accountID, blobID, -1)
}
