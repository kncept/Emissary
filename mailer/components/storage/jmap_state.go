package storage

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// ErrJMAPStateConflict reports that the stored JMAP state changed between a
// LoadJMAPState and the matching SaveJMAPState. The change tracker retries the
// operation with the freshly loaded state instead of overwriting a newer
// version; that keeps state counters monotonic when several Lambda invocations
// handle the same account concurrently.
var ErrJMAPStateConflict = errors.New("jmap state changed concurrently")

// JMAPStateStore persists the opaque per-account JMAP change tracker blob.
// It is deliberately byte oriented: the storage layer does not need to know
// the structure of a state token or change log.
//
// revision is an opaque optimistic-concurrency token returned by Load. Save
// must reject a stale revision with ErrJMAPStateConflict, and an empty
// revision means the record must not exist yet.
type JMAPStateStore interface {
	LoadJMAPState(ctx context.Context, accountID string) (data []byte, revision string, err error)
	SaveJMAPState(ctx context.Context, accountID string, data []byte, revision string) (newRevision string, err error)
}

// jmapStateRoot is the prefix used for state blobs. It is kept outside the
// users/ tree so mailbox listings never treat a state file as delivered mail.
const jmapStateRoot = "_jmap-state"

// maxStateBytes bounds the change tracker blob read back from S3. A corrupted
// or hostile object must not be able to exhaust an invocation's memory.
const maxStateBytes = 8 << 20

// jmapStateKey encodes the account id as one URL-safe path segment. Account ids
// are normally email addresses, but vendor ids may contain slashes, so they
// must never be interpolated into a key verbatim.
func jmapStateKey(accountID string) (string, error) {
	if strings.TrimSpace(accountID) == "" {
		return "", errors.New("accountID is required")
	}
	encoded := base64.RawURLEncoding.EncodeToString([]byte(accountID))
	return jmapStateRoot + "/" + encoded + "/state.json", nil
}

// fileStateRevision hashes the stored bytes so a filesystem store can use the
// same optimistic-concurrency contract as S3 (which uses ETags).
func fileStateRevision(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func jmapStateSaveError(key string, err error) error {
	return fmt.Errorf("failed to store JMAP state %q: %w", key, err)
}
