package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	smithyhttp "github.com/aws/smithy-go/transport/http"
	"github.com/kncept/Emissary/mailer/email"
)

// s3API is the subset of the S3 client used by S3Storage. Keeping it as an
// interface makes the storage layer testable without contacting AWS.
type s3API interface {
	GetObject(ctx context.Context, params *s3.GetObjectInput, optFns ...func(*s3.Options)) (*s3.GetObjectOutput, error)
	PutObject(ctx context.Context, params *s3.PutObjectInput, optFns ...func(*s3.Options)) (*s3.PutObjectOutput, error)
	DeleteObject(ctx context.Context, params *s3.DeleteObjectInput, optFns ...func(*s3.Options)) (*s3.DeleteObjectOutput, error)
	ListObjectsV2(ctx context.Context, params *s3.ListObjectsV2Input, optFns ...func(*s3.Options)) (*s3.ListObjectsV2Output, error)
}

type S3Storage struct {
	client s3API
	bucket string
	prefix string
}

func NewS3Storage(bucket string) (*S3Storage, error) {
	cfg, err := config.LoadDefaultConfig(context.TODO())
	if err != nil {
		return nil, fmt.Errorf("failed to load AWS config: %w", err)
	}
	return &S3Storage{
		client: s3.NewFromConfig(cfg),
		bucket: bucket,
	}, nil
}

// ListAllEmails implements the Storage interface.
func (s *S3Storage) ListAllEmails(ctx context.Context) ([]*email.UserEmail, error) {
	var emails []*email.UserEmail
	paginator := s3.NewListObjectsV2Paginator(s.client, &s3.ListObjectsV2Input{
		Bucket: aws.String(s.bucket),
		Prefix: aws.String(s.listPrefix()),
	})

	for paginator.HasMorePages() {
		output, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to list objects: %w", err)
		}

		for _, obj := range output.Contents {
			if obj.Key == nil || strings.HasSuffix(*obj.Key, "/") {
				continue
			}
			// Read the object by its exact key. SES stores inbound messages
			// under the raw message ID (no extension), so deriving a different
			// key here would make every message fail to load and be skipped.
			message, err := s.getEmailByKey(ctx, *obj.Key)
			if err != nil {
				log.Printf("s3: skipping object %q: %v", *obj.Key, err)
				continue
			}
			emails = append(emails, message)
		}
	}
	return emails, nil
}

func (s *S3Storage) listPrefix() string {
	if s.prefix == "" {
		return ""
	}
	return s.prefix + "/"
}

// emailKeyCandidates returns the S3 keys to try for an id. Callers may pass a
// full object key or an id stored under the configured prefix.
func (s *S3Storage) emailKeyCandidates(id string) []string {
	id = strings.TrimPrefix(id, "/")
	if id == "" {
		return nil
	}

	candidates := make([]string, 0, 2)
	add := func(key string) {
		if key == "" {
			return
		}
		for _, existing := range candidates {
			if existing == key {
				return
			}
		}
		candidates = append(candidates, key)
	}

	add(id)
	if s.prefix != "" {
		add(s.prefix + "/" + id)
	}
	return candidates
}

// GetEmail implements the Storage interface.
func (s *S3Storage) GetEmail(ctx context.Context, id string) (*email.UserEmail, error) {
	for _, key := range s.emailKeyCandidates(id) {
		message, err := s.getEmailByKey(ctx, key)
		if err == nil {
			return message, nil
		}
		// A missing candidate is expected while probing alternate layouts.
		// Surface anything else (permissions, throttling, network, ...).
		if !isObjectNotFound(err) {
			return nil, err
		}
	}
	return nil, ErrNotFound
}

func (s *S3Storage) getEmailByKey(ctx context.Context, key string) (*email.UserEmail, error) {
	output, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get object %q: %w", key, err)
	}
	defer output.Body.Close()

	body, err := io.ReadAll(output.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read object %q: %w", key, err)
	}

	parsedEmail, err := email.Parse(key, strings.NewReader(string(body)))
	if err != nil {
		return nil, fmt.Errorf("failed to parse email %q: %w", key, err)
	}
	return parsedEmail, nil
}

func isObjectNotFound(err error) bool {
	var noSuchKey *types.NoSuchKey
	if errors.As(err, &noSuchKey) {
		return true
	}
	var responseError *smithyhttp.ResponseError
	return errors.As(err, &responseError) && responseError.HTTPStatusCode() == http.StatusNotFound
}

// Name returns the storage name.
func (s *S3Storage) Name() string {
	return "S3Storage"
}

// ListEmails is kept for backward compatibility.
func (s *S3Storage) ListEmails(ctx context.Context) ([]Email, error) {
	emails, err := s.ListAllEmails(ctx)
	if err != nil {
		return nil, err
	}

	var result []Email
	for _, e := range emails {
		// Convert to legacy Email type
		from := ""
		if len(e.From.Address) > 0 {
			from = e.From.Address
		}
		to := ""
		if len(e.To) > 0 {
			to = e.To[0].Address
		}
		result = append(result, Email{
			ID:        e.ID,
			From:      from,
			To:        to,
			Subject:   e.Subject,
			Body:      e.Text,
			Timestamp: e.Date.Format(time.RFC3339),
		})
	}
	return result, nil
}

// Email is the legacy email type for backward compatibility.
type Email struct {
	ID        string
	From      string
	To        string
	Subject   string
	Body      string
	Timestamp string
	Raw       string
}

func (s *S3Storage) GetEmailLegacy(ctx context.Context, key string) (Email, error) {
	output, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return Email{}, fmt.Errorf("failed to get object: %w", err)
	}
	defer output.Body.Close()

	body, err := io.ReadAll(output.Body)
	if err != nil {
		return Email{}, fmt.Errorf("failed to read body: %w", err)
	}

	return parseEmail(key, string(body)), nil
}

func (s *S3Storage) PutEmail(ctx context.Context, email Email) error {
	key := email.ID
	if s.prefix != "" {
		key = s.prefix + "/" + key
	}
	_, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
		Body:   strings.NewReader(email.Raw),
	})
	return err
}

func parseEmail(key, raw string) Email {
	lines := strings.Split(raw, "\n")
	email := Email{
		ID:  key,
		Raw: raw,
	}

	bodyStarted := false
	var bodyLines []string
	for _, line := range lines {
		if !bodyStarted && line == "" {
			bodyStarted = true
			continue
		}
		if !bodyStarted {
			if strings.HasPrefix(strings.ToLower(line), "from:") {
				email.From = strings.TrimSpace(line[5:])
			} else if strings.HasPrefix(strings.ToLower(line), "to:") {
				email.To = strings.TrimSpace(line[3:])
			} else if strings.HasPrefix(strings.ToLower(line), "subject:") {
				email.Subject = strings.TrimSpace(line[8:])
			} else if strings.HasPrefix(strings.ToLower(line), "date:") {
				email.Timestamp = strings.TrimSpace(line[5:])
			}
		} else {
			bodyLines = append(bodyLines, line)
		}
	}
	email.Body = strings.Join(bodyLines, "\n")
	return email
}

func (s *S3Storage) DeleteEmail(ctx context.Context, key string) error {
	_, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	return err
}

type SESClient struct {
	client *s3.Client
}

func NewSESClient(region string) (*SESClient, error) {
	cfg, err := config.LoadDefaultConfig(context.TODO(), config.WithRegion(region))
	if err != nil {
		return nil, fmt.Errorf("failed to load AWS config: %w", err)
	}
	return &SESClient{client: s3.NewFromConfig(cfg)}, nil
}

func (s *SESClient) SendEmail(ctx context.Context, from, to, subject, body string) error {
	return fmt.Errorf("SES sending not fully implemented - use AWS SES v2 SDK")
}
