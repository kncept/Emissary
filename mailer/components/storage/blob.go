package storage

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"

	"github.com/kncept/Emissary/mailer/email"
)

// blobDirName is an account-local directory for immutable JMAP blobs. It
// starts with an underscore so mailbox listings can skip it.
const blobDirName = "_blobs"

const (
	blobMetadataSuffix = ".json"
	defaultBlobType    = "application/octet-stream"
	emailBlobType      = "message/rfc822"
)

// Blob is the storage-layer view of an immutable blob. EmailBacked is true
// when the bytes came from a delivered email rather than the generic _blobs
// area; JMAP methods use it to avoid deleting a message when asked to destroy
// a blob.
type Blob struct {
	ID          string `json:"id"`
	AccountID   string `json:"accountId"`
	Type        string `json:"type"`
	Size        int64  `json:"size"`
	EmailBacked bool   `json:"-"`
}

// BlobStore holds account-scoped immutable blobs. IDs are content digests, so
// putting the same bytes twice in one account is idempotent. Callers must not
// mutate bytes after storing them.
type BlobStore interface {
	PutBlob(ctx context.Context, accountID, contentType string, data []byte) (Blob, error)
	GetBlob(ctx context.Context, accountID, blobID string) (Blob, []byte, error)
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
