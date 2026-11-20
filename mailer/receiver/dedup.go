// Package receiver's dedup support records which recipients of a staged
// message have already been handled. Markers are derived from the staging
// object key and the recipient, so a retry of an S3 event can skip work that
// completed before an unrelated failure without delivering mail twice.
package receiver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
)

// DedupStore tracks successful handling of one recipient of a staging object.
// IDs are opaque values produced by DedupID. Implementations must treat a
// missing ID as unseen.
type DedupStore interface {
	Seen(ctx context.Context, id string) (bool, error)
	Record(ctx context.Context, id string) error
}

// DedupClearer is an optional extension for stores that can discard markers
// once the staging object has been deleted. Stale markers are harmless (they
// only suppress reprocessing of an object that no longer exists), so receiver
// does not require every DedupStore to implement it.
type DedupClearer interface {
	Clear(ctx context.Context, ids ...string) error
}

// DedupID derives an opaque dedup id from a staging key and a recipient. The
// staging key is the unique SES message id; including the recipient gives each
// delivery/bounce its own marker, so a partial failure only retries the
// recipients that did not complete. A hash keeps arbitrary SES keys and email
// addresses safe to use as object names or table keys.
func DedupID(key, recipient string) string {
	sum := sha256.Sum256([]byte(key + "\x00" + strings.ToLower(recipient)))
	return hex.EncodeToString(sum[:])
}

// MemoryDedup is a process-local DedupStore. It is the default for tests and
// local development; Lambda deployments use S3Dedup so markers survive new
// containers and retries.
type MemoryDedup struct {
	mu   sync.RWMutex
	seen map[string]struct{}
}

var _ DedupStore = (*MemoryDedup)(nil)
var _ DedupClearer = (*MemoryDedup)(nil)

func NewMemoryDedup() *MemoryDedup {
	return &MemoryDedup{seen: make(map[string]struct{})}
}

func (m *MemoryDedup) Seen(_ context.Context, id string) (bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, ok := m.seen[id]
	return ok, nil
}

func (m *MemoryDedup) Record(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.seen[id] = struct{}{}
	return nil
}

func (m *MemoryDedup) Clear(_ context.Context, ids ...string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, id := range ids {
		delete(m.seen, id)
	}
	return nil
}

// dedupObjectPrefix keeps marker objects out of the bucket root, where the
// support API and the receiver both expect only raw staged messages. Anything
// containing a slash is ignored as an inbound email key.
const dedupObjectPrefix = "_processed/"

// DedupS3Client is the subset of S3 used to persist dedup markers. Both the
// raw S3 client and storage.S3API satisfy it.
type DedupS3Client interface {
	HeadObject(ctx context.Context, params *s3.HeadObjectInput, optFns ...func(*s3.Options)) (*s3.HeadObjectOutput, error)
	PutObject(ctx context.Context, params *s3.PutObjectInput, optFns ...func(*s3.Options)) (*s3.PutObjectOutput, error)
	DeleteObject(ctx context.Context, params *s3.DeleteObjectInput, optFns ...func(*s3.Options)) (*s3.DeleteObjectOutput, error)
}

// S3Dedup stores markers as small objects under _processed/ in the mail
// bucket. No extra table or configuration is needed: the receiver already has
// access to that bucket for staging reads and deletes.
type S3Dedup struct {
	client DedupS3Client
	bucket string
}

var _ DedupStore = (*S3Dedup)(nil)
var _ DedupClearer = (*S3Dedup)(nil)

func NewS3Dedup(client DedupS3Client, bucket string) *S3Dedup {
	return &S3Dedup{client: client, bucket: bucket}
}

func (s *S3Dedup) Seen(ctx context.Context, id string) (bool, error) {
	_, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(dedupObjectPrefix + id),
	})
	if err == nil {
		return true, nil
	}
	if isMissingMarker(err) {
		return false, nil
	}
	return false, fmt.Errorf("failed to check dedup marker %s: %w", id, err)
}

func (s *S3Dedup) Record(ctx context.Context, id string) error {
	_, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(dedupObjectPrefix + id),
		Body:   strings.NewReader(""),
	})
	if err != nil {
		return fmt.Errorf("failed to record dedup marker %s: %w", id, err)
	}
	return nil
}

func (s *S3Dedup) Clear(ctx context.Context, ids ...string) error {
	var errs []error
	for _, id := range ids {
		_, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{
			Bucket: aws.String(s.bucket),
			Key:    aws.String(dedupObjectPrefix + id),
		})
		if err != nil {
			errs = append(errs, fmt.Errorf("failed to clear dedup marker %s: %w", id, err))
		}
	}
	return errors.Join(errs...)
}

// isMissingMarker reports the 404-style errors HeadObject returns for an
// absent key. Both typed SDK errors and generic API errors are accepted so
// fakes and different SDK versions behave the same way.
func isMissingMarker(err error) bool {
	var notFound *types.NotFound
	if errors.As(err, &notFound) {
		return true
	}
	var noSuchKey *types.NoSuchKey
	if errors.As(err, &noSuchKey) {
		return true
	}
	var apiErr smithy.APIError
	return errors.As(err, &apiErr) && (apiErr.ErrorCode() == "NotFound" || apiErr.ErrorCode() == "NoSuchKey")
}
