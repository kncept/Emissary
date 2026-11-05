package storage

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
	"github.com/kncept/Emissary/mailer/email"
)

var _ Storage = (*S3Storage)(nil)

// S3Storage reads raw messages from an S3 bucket. SES receipt rules normally
// write incoming mail to S3, which is then exposed to the web client here.
type S3Storage struct {
	client *s3.Client
	bucket string
	prefix string
}

// NewS3Storage creates an S3-backed store. The AWS region and credentials come
// from the standard AWS environment/configuration chain unless region is set.
func NewS3Storage(ctx context.Context, region, bucket, prefix, endpoint string) (*S3Storage, error) {
	if bucket == "" {
		return nil, errors.New("S3 bucket is not configured (set EMISSARY_S3_BUCKET)")
	}

	options := make([]func(*awsconfig.LoadOptions) error, 0, 1)
	if region != "" {
		options = append(options, awsconfig.WithRegion(region))
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx, options...)
	if err != nil {
		return nil, fmt.Errorf("load AWS config: %w", err)
	}

	client := s3.NewFromConfig(cfg, func(options *s3.Options) {
		if endpoint != "" {
			options.BaseEndpoint = aws.String(endpoint)
			options.UsePathStyle = true
		}
	})

	return &S3Storage{
		client: client,
		bucket: bucket,
		prefix: strings.TrimPrefix(prefix, "/"),
	}, nil
}

func (s *S3Storage) Name() string {
	return "S3Storage"
}

func (s *S3Storage) ListAllEmails(ctx context.Context) ([]*email.Email, error) {
	paginator := s3.NewListObjectsV2Paginator(s.client, &s3.ListObjectsV2Input{
		Bucket: aws.String(s.bucket),
		Prefix: aws.String(s.prefix),
	})

	emails := make([]*email.Email, 0)
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("list S3 objects: %w", err)
		}
		for _, object := range page.Contents {
			if object.Key == nil || strings.HasSuffix(*object.Key, "/") {
				continue
			}
			message, err := s.getEmail(ctx, *object.Key)
			if err != nil {
				// A bucket may contain objects that are not raw email. Skip those
				// rather than making the entire mailbox unavailable.
				log.Printf("s3 storage: skipping %q: %v", *object.Key, err)
				continue
			}
			emails = append(emails, message)
		}
	}

	sort.Slice(emails, func(i, j int) bool {
		if emails[i].Date.Equal(emails[j].Date) {
			return emails[i].ID > emails[j].ID
		}
		return emails[i].Date.After(emails[j].Date)
	})
	return emails, nil
}

func (s *S3Storage) GetEmail(ctx context.Context, id string) (*email.Email, error) {
	key, err := email.DecodeID(id)
	if err != nil {
		key = id
	}
	message, err := s.getEmail(ctx, key)
	if err != nil {
		if isNotFound(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return message, nil
}

func (s *S3Storage) getEmail(ctx context.Context, key string) (*email.Email, error) {
	output, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return nil, fmt.Errorf("get S3 object %q: %w", key, err)
	}
	defer output.Body.Close()

	message, err := email.Parse(email.EncodeID(key), output.Body)
	if err != nil {
		return nil, fmt.Errorf("parse email %q: %w", key, err)
	}
	if output.ContentLength != nil {
		message.Size = *output.ContentLength
	}
	return message, nil
}

func isNotFound(err error) bool {
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.ErrorCode() {
		case "NoSuchKey", "NotFound", "404":
			return true
		}
	}
	return false
}
