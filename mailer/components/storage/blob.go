package storage

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/kncept/Emissary/mailer/email"
)

// blobDirName is an account-local directory for immutable JMAP blobs. It
// starts with an underscore so mailbox listings can skip it.
const blobDirName = "_blobs"

const (
	blobMetadataSuffix = ".json"
	defaultBlobType    = "application/octet-stream"
	emailBlobType      = "message/rfc822"

	// DefaultBlobMinLifetime is the minimum time an unreferenced blob must
	// remain downloadable after upload, as required by RFC 8620 section 6.
	// Early deletion is only allowed when the unreferenced-blob quota forces
	// it, oldest blob first.
	DefaultBlobMinLifetime = time.Hour
)

// Blob lifecycle errors. ErrBlobReferenced is returned by DeleteBlob while a
// JMAP object still references the blob; ErrBlobQuota is returned by the
// lifecycle layer when an upload cannot fit in the uploader's unreferenced
// quota even after expired blobs have been evicted.
var (
	ErrBlobReferenced = errors.New("blob is still referenced")
	ErrBlobQuota      = errors.New("unreferenced blob quota exceeded")
)

// Blob is the storage-layer view of an immutable blob. EmailBacked is true
// when the bytes came from a delivered email rather than the generic _blobs
// area; JMAP methods use it to avoid deleting a message when asked to destroy
// a blob.
//
// UploaderID, CreatedAt and Refs are lifecycle metadata only: the bytes and
// the id derived from them never change. UploaderID is the JWT subject that
// uploaded the bytes (RFC 8620 requires unreferenced blobs to remain private
// to their uploader, even in a shared account). Refs counts references held
// by JMAP objects; while it is positive the blob is pinned against garbage
// collection and explicit deletion.
type Blob struct {
	ID          string    `json:"id"`
	AccountID   string    `json:"accountId"`
	Type        string    `json:"type"`
	Size        int64     `json:"size"`
	EmailBacked bool      `json:"-"`
	UploaderID  string    `json:"uploaderId,omitempty"`
	CreatedAt   time.Time `json:"createdAt,omitempty"`
	Refs        int       `json:"refs,omitempty"`
}

// Uploader returns the account that owns an unreferenced blob for quota and
// access-control purposes. Legacy blobs written before uploader tracking was
// added belong to the account that stores them.
func (b Blob) Uploader() string {
	if b.UploaderID != "" {
		return strings.ToLower(b.UploaderID)
	}
	return strings.ToLower(b.AccountID)
}

// IsPinned reports whether a JMAP object still references the blob.
func (b Blob) IsPinned() bool { return b.Refs > 0 }

// BlobStore holds account-scoped immutable blobs. IDs are content digests, so
// putting the same bytes twice in one account is idempotent. Callers must not
// mutate bytes after storing them.
type BlobStore interface {
	// PutBlob stores bytes on behalf of the account itself (the legacy path
	// used by Blob/copy and tests). It uses time.Now as the upload time.
	PutBlob(ctx context.Context, accountID, contentType string, data []byte) (Blob, error)
	// UploadBlob records the uploading user and time as well as the bytes.
	// Re-uploading identical bytes keeps the id and the reference count but
	// refreshes the timestamp, which resets the unreferenced-blob expiry.
	UploadBlob(ctx context.Context, accountID, uploaderID, contentType string, data []byte, now time.Time) (Blob, error)
	GetBlob(ctx context.Context, accountID, blobID string) (Blob, []byte, error)
	// OpenBlob returns metadata and a reader for the blob bytes. Callers must
	// close the reader. It is the streaming counterpart used by downloads.
	OpenBlob(ctx context.Context, accountID, blobID string) (Blob, io.ReadCloser, error)
	// GetBlobMeta reads lifecycle metadata without loading the bytes.
	GetBlobMeta(ctx context.Context, accountID, blobID string) (Blob, error)
	// ListBlobs returns metadata for every generic blob in an account.
	ListBlobs(ctx context.Context, accountID string) ([]Blob, error)
	// AddBlobRefs adjusts the reference count by delta (which may be
	// negative). A count never goes below zero. A pinned blob cannot be
	// deleted until its references have been released.
	AddBlobRefs(ctx context.Context, accountID, blobID string, delta int) (Blob, error)
	DeleteBlob(ctx context.Context, accountID, blobID string) error
}

// BlobID derives the opaque JMAP id used for a blob's bytes.
func BlobID(data []byte) string {
	sum := sha256.Sum256(data)
	return email.EncodeID(string(sum[:]))
}

func canonicalBlobAccount(accountID string) (string, error) {
	accountID = strings.ToLower(strings.TrimSpace(accountID))
	if accountID == "" {
		return "", errors.New("accountID is required")
	}
	return accountID, nil
}

func normalizeBlobID(blobID string) (string, error) {
	if !idPattern.MatchString(blobID) {
		return "", ErrNotFound
	}
	return blobID, nil
}

func blobDirKey(accountID string) (string, error) {
	account, err := canonicalBlobAccount(accountID)
	if err != nil {
		return "", err
	}
	root, err := accountRoot(account)
	if err != nil {
		return "", err
	}
	return root + blobDirName + "/", nil
}

func blobObjectKey(accountID, blobID string) (string, error) {
	dir, err := blobDirKey(accountID)
	if err != nil {
		return "", err
	}
	id, err := normalizeBlobID(blobID)
	if err != nil {
		return "", err
	}
	return dir + id, nil
}

func blobMetadataObjectKey(accountID, blobID string) (string, error) {
	key, err := blobObjectKey(accountID, blobID)
	if err != nil {
		return "", err
	}
	return key + blobMetadataSuffix, nil
}

func normalizeBlobType(contentType string) string {
	contentType = strings.TrimSpace(contentType)
	if contentType == "" {
		return defaultBlobType
	}
	return contentType
}

func validateBlobData(data []byte) error {
	if int64(len(data)) > email.MaxPartSize() {
		return fmt.Errorf("blob exceeds maximum size of %d bytes", email.MaxPartSize())
	}
	return nil
}

// canonicalBlobUploader normalises the JWT subject recorded as a blob's
// uploader. An empty uploader falls back to the account, which keeps blobs
// written by Blob/copy and older sidecars attributable to their own account.
func canonicalBlobUploader(uploaderID, accountID string) string {
	uploaderID = strings.ToLower(strings.TrimSpace(uploaderID))
	if uploaderID != "" {
		return uploaderID
	}
	return strings.ToLower(strings.TrimSpace(accountID))
}

func newBlobMetadata(accountID, uploaderID, contentType string, data []byte, now time.Time) Blob {
	return Blob{
		ID:         BlobID(data),
		AccountID:  strings.ToLower(strings.TrimSpace(accountID)),
		Type:       normalizeBlobType(contentType),
		Size:       int64(len(data)),
		UploaderID: canonicalBlobUploader(uploaderID, accountID),
		CreatedAt:  now.UTC(),
	}
}

// refreshBlobUpload preserves the immutable fields and reference count of an
// existing blob while recording a new uploader and reset expiry time.
func refreshBlobUpload(existing Blob, uploaderID string, now time.Time) Blob {
	existing.UploaderID = canonicalBlobUploader(uploaderID, existing.AccountID)
	existing.CreatedAt = now.UTC()
	return existing
}

// sortBlobsOldestFirst orders blobs by upload time, then id, so quota eviction
// is deterministic even when several blobs were uploaded in the same instant.
func sortBlobsOldestFirst(blobs []Blob) {
	sort.Slice(blobs, func(i, j int) bool {
		if !blobs[i].CreatedAt.Equal(blobs[j].CreatedAt) {
			return blobs[i].CreatedAt.Before(blobs[j].CreatedAt)
		}
		return blobs[i].ID < blobs[j].ID
	})
}

func adjustBlobRefs(meta Blob, delta int) Blob {
	refs := meta.Refs + delta
	if refs < 0 {
		refs = 0
	}
	meta.Refs = refs
	return meta
}
