package handler

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"

	"github.com/kncept/Emissary/mailer/receiver"
)

// S3API is the subset of the S3 client used for staging operations.
type S3API interface {
	GetObject(ctx context.Context, params *s3.GetObjectInput, optFns ...func(*s3.Options)) (*s3.GetObjectOutput, error)
	DeleteObject(ctx context.Context, params *s3.DeleteObjectInput, optFns ...func(*s3.Options)) (*s3.DeleteObjectOutput, error)
}

var _ receiver.InboundRawEmailEvent = (*S3Event)(nil)

type S3Event struct {
	s3Client S3API
	bucket   string
	key      string
}

// OnComplete implements [receiver.InboundRawEmailEvent].
func (s *S3Event) OnComplete(ctx context.Context) error {
	_, err := s.s3Client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(s.key),
	})
	if err != nil {
		return fmt.Errorf("failed to delete staging object %s: %w", s.key, err)
	}
	slog.Debug("Support: deleted staging object", "key", s.key)
	return nil
}

// RawEmail implements [receiver.InboundRawEmailEvent].
func (s *S3Event) RawEmail(ctx context.Context) ([]byte, error) {
	obj, err := s.s3Client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(s.key),
	})
	if err != nil {
		// Check if object doesn't exist (already processed or deleted)
		var noSuchKey *types.NoSuchKey
		if errors.As(err, &noSuchKey) {
			slog.Debug("Support: staging object not found (already processed?)", "key", s.key)
			return nil, fmt.Errorf("not found: %s", s.key)
		}
		return nil, fmt.Errorf("failed to get object %s: %w", s.key, err)
	}
	defer obj.Body.Close()
	return io.ReadAll(obj.Body)
}

func NewRawS3Event(s3Client S3API, bucket, key string) receiver.InboundRawEmailEvent {
	return &S3Event{
		s3Client: s3Client,
		bucket:   bucket,
		key:      key,
	}
}