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

// S3API is the subset of the S3 client used by S3Storage. Keeping it as an
// interface makes the storage layer testable without contacting AWS.
type S3API interface {
	GetObject(ctx context.Context, params *s3.GetObjectInput, optFns ...func(*s3.Options)) (*s3.GetObjectOutput, error)
	PutObject(ctx context.Context, params *s3.PutObjectInput, optFns ...func(*s3.Options)) (*s3.PutObjectOutput, error)
	DeleteObject(ctx context.Context, params *s3.DeleteObjectInput, optFns ...func(*s3.Options)) (*s3.DeleteObjectOutput, error)
	HeadObject(ctx context.Context, params *s3.HeadObjectInput, optFns ...func(*s3.Options)) (*s3.HeadObjectOutput, error)
	ListObjectsV2(ctx context.Context, params *s3.ListObjectsV2Input, optFns ...func(*s3.Options)) (*s3.ListObjectsV2Output, error)
}

type S3Storage struct {
	client S3API
	bucket string
	prefix string
}

// RootObject is one object directly under the bucket root. The support API
// reports these as unprocessed staging messages.
type RootObject struct {
	Key          string
	Size         int64
	LastModified time.Time
	ETag         string
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

// BucketName returns the configured bucket name.
func (s *S3Storage) BucketName() string { return s.bucket }

// Client returns the configured S3 operations. Administrative features (such
// as the support API) reuse the same client rather than building another.
func (s *S3Storage) Client() S3API { return s.client }

// ListRoot lists the immediate contents of the bucket: objects at the root
// (SES staging messages) and the top-level common prefixes (normally users/).
func (s *S3Storage) ListRoot(ctx context.Context) ([]RootObject, []string, error) {
	var objects []RootObject
	var prefixes []string

	paginator := s3.NewListObjectsV2Paginator(s.client, &s3.ListObjectsV2Input{
		Bucket:    aws.String(s.bucket),
		Delimiter: aws.String("/"),
	})
	for paginator.HasMorePages() {
		output, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to list bucket root: %w", err)
		}

		for _, obj := range output.Contents {
			if obj.Key == nil {
				continue
			}
			root := RootObject{Key: *obj.Key}
			if obj.Size != nil {
				root.Size = *obj.Size
			}
			if obj.LastModified != nil {
				root.LastModified = *obj.LastModified
			}
			if obj.ETag != nil {
				root.ETag = *obj.ETag
			}
			objects = append(objects, root)
		}
		for _, prefix := range output.CommonPrefixes {
			if prefix.Prefix == nil {
				continue
			}
			prefixes = append(prefixes, *prefix.Prefix)
		}
	}

	if objects == nil {
		objects = []RootObject{}
	}
	if prefixes == nil {
		prefixes = []string{}
	}
	return objects, prefixes, nil
}

// ObjectExists reports whether an object with the exact key exists.
func (s *S3Storage) ObjectExists(ctx context.Context, key string) (bool, error) {
	_, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err == nil {
		return true, nil
	}
	if isObjectNotFound(err) {
		return false, nil
	}
	return false, fmt.Errorf("failed to check object %q: %w", key, err)
}

// ListAccountEmails implements the Storage interface. It limits ListObjectsV2
// to the account's mailbox prefix so messages belonging to other accounts (and
// unprocessed SES staging objects at the bucket root) are never read. Each
// message is parsed from the exact object key, so the returned ID is the full
// delivered key including the timestamp and .eml extension.
func (s *S3Storage) ListAccountEmails(ctx context.Context, accountID, mailboxID string) ([]*email.UserEmail, error) {
	mailboxPrefix := email.MailboxKeyPrefix(accountID, mailboxID)
	if mailboxPrefix == "" {
		return nil, fmt.Errorf("accountID is required")
	}
	prefix := s.listPrefix() + mailboxPrefix

	var emails []*email.UserEmail
	paginator := s3.NewListObjectsV2Paginator(s.client, &s3.ListObjectsV2Input{
		Bucket: aws.String(s.bucket),
		Prefix: aws.String(prefix),
	})

	for paginator.HasMorePages() {
		output, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to list objects in %q: %w", prefix, err)
		}

		for _, obj := range output.Contents {
			// Delivered messages live in a per-user mailbox subfolder and end
			// in .eml. Skip system files such as users/<account>/user.json,
			// directory markers and anything else that is not a message.
			if obj.Key == nil || !email.IsDeliveredMessageKey(*obj.Key) {
				continue
			}
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
	body, err := s.getRawByKey(ctx, key)
	if err != nil {
		return nil, err
	}

	parsedEmail, err := email.Parse(key, strings.NewReader(string(body)))
	if err != nil {
		return nil, fmt.Errorf("failed to parse email %q: %w", key, err)
	}
	return parsedEmail, nil
}

// getRawByKey reads the exact bytes of an S3 object. Storage-level prefixes
// are handled by the caller via emailKeyCandidates.
func (s *S3Storage) getRawByKey(ctx context.Context, key string) ([]byte, error) {
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
	return body, nil
}

// GetEmailRaw implements the Storage interface. It returns the delivered .eml
// bytes verbatim (including MIME headers and encodings) rather than a parsed
// or reconstructed message.
func (s *S3Storage) GetEmailRaw(ctx context.Context, id string) ([]byte, error) {
	for _, key := range s.emailKeyCandidates(id) {
		raw, err := s.getRawByKey(ctx, key)
		if err == nil {
			return raw, nil
		}
		if !isObjectNotFound(err) {
			return nil, err
		}
	}
	return nil, ErrNotFound
}

func isObjectNotFound(err error) bool {
	var noSuchKey *types.NoSuchKey
	if errors.As(err, &noSuchKey) {
		return true
	}
	// HeadObject reports a missing key as NotFound rather than NoSuchKey.
	var notFound *types.NotFound
	if errors.As(err, &notFound) {
		return true
	}
	var responseError *smithyhttp.ResponseError
	return errors.As(err, &responseError) && responseError.HTTPStatusCode() == http.StatusNotFound
}

// Name returns the storage name.
func (s *S3Storage) Name() string {
	return "S3Storage"
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

// AddEmail implements the Storage interface.
// S3Storage is primarily for receiving emails; adding emails is not fully supported.
// This implementation returns an error indicating the operation is not supported.
func (s *S3Storage) AddEmail(ctx context.Context, email *email.UserEmail) error {
	return errors.New("AddEmail not supported on S3Storage")
}

// DeleteEmail implements the Storage interface. It resolves the id the same
// way GetEmail does and deletes every matching object, so it returns
// ErrNotFound only when no candidate key exists.
func (s *S3Storage) DeleteEmail(ctx context.Context, id string) error {
	candidates := s.emailKeyCandidates(id)
	if len(candidates) == 0 {
		return ErrNotFound
	}

	deleted := false
	for _, key := range candidates {
		// DeleteObject succeeds even when the key does not exist, so the
		// object's presence is checked first to keep ErrNotFound meaningful.
		_, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{
			Bucket: aws.String(s.bucket),
			Key:    aws.String(key),
		})
		if err != nil {
			if isObjectNotFound(err) {
				continue
			}
			return fmt.Errorf("failed to check object %q: %w", key, err)
		}
		_, err = s.client.DeleteObject(ctx, &s3.DeleteObjectInput{
			Bucket: aws.String(s.bucket),
			Key:    aws.String(key),
		})
		if err != nil {
			return fmt.Errorf("failed to delete object %q: %w", key, err)
		}
		deleted = true
	}
	if !deleted {
		return ErrNotFound
	}
	return nil
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
