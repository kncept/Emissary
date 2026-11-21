package receiver

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// S3API is the subset of the S3 client used to stage raw inbound messages.
// Both cmd/s3-receiver and the support API use this adapter instead of
// keeping their own copies of the delete/read operations.
type S3API interface {
	GetObject(ctx context.Context, params *s3.GetObjectInput, optFns ...func(*s3.Options)) (*s3.GetObjectOutput, error)
	DeleteObject(ctx context.Context, params *s3.DeleteObjectInput, optFns ...func(*s3.Options)) (*s3.DeleteObjectOutput, error)
}

var _ InboundRawEmailEvent = (*S3Event)(nil)

// S3Event adapts one staged S3 object to InboundRawEmailEvent.
type S3Event struct {
	s3Client S3API
	bucket   string
	key      string
}

// NewRawS3Event returns an event backed by one object in an S3 bucket.
func NewRawS3Event(client S3API, bucket, key string) InboundRawEmailEvent {
	return &S3Event{s3Client: client, bucket: bucket, key: key}
}

// RawEmail implements InboundRawEmailEvent.
func (s *S3Event) RawEmail(ctx context.Context) ([]byte, error) {
	obj, err := s.s3Client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(s.key),
	})
	if err != nil {
		var noSuchKey *types.NoSuchKey
		if errors.As(err, &noSuchKey) {
			slog.Debug("Receiver: staging object not found (already processed?)", "bucket", s.bucket, "key", s.key)
			return nil, fmt.Errorf("not found: %s", s.key)
		}
		return nil, fmt.Errorf("failed to get object %s: %w", s.key, err)
	}
	defer obj.Body.Close()
	return io.ReadAll(obj.Body)
}

// OnComplete implements InboundRawEmailEvent.
func (s *S3Event) OnComplete(ctx context.Context) error {
	_, err := s.s3Client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(s.key),
	})
	if err != nil {
		return fmt.Errorf("failed to delete staging object %s: %w", s.key, err)
	}
	slog.Debug("Receiver: deleted staging object", "bucket", s.bucket, "key", s.key)
	return nil
}
