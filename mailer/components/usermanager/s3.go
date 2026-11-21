package usermanager

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	smithyhttp "github.com/aws/smithy-go/transport/http"

	"github.com/kncept/Emissary/mailer/email"
)

// userRecordName is the object that stores a user's record, directly under
// the user's mailbox prefix.
const userRecordName = "user.json"

// s3Client is the subset of the S3 client used by S3Manager. It is an
// interface so the implementation can be tested without AWS.
type s3Client interface {
	GetObject(ctx context.Context, params *s3.GetObjectInput, optFns ...func(*s3.Options)) (*s3.GetObjectOutput, error)
	PutObject(ctx context.Context, params *s3.PutObjectInput, optFns ...func(*s3.Options)) (*s3.PutObjectOutput, error)
	DeleteObject(ctx context.Context, params *s3.DeleteObjectInput, optFns ...func(*s3.Options)) (*s3.DeleteObjectOutput, error)
	HeadObject(ctx context.Context, params *s3.HeadObjectInput, optFns ...func(*s3.Options)) (*s3.HeadObjectOutput, error)
	ListObjectsV2(ctx context.Context, params *s3.ListObjectsV2Input, optFns ...func(*s3.Options)) (*s3.ListObjectsV2Output, error)
}

// S3Manager is the Emissary-mastered user manager backed by the S3 bucket
// used by the mail storage backend. Each user is stored as a plain JSON
// write-out of the EmissaryUser at users/<email>/user.json, the same
// users/<email> layout the storage backend uses for the user's mailboxes.
// A mailbox directory without a user.json is not a user: the record object
// is what makes the account.
type S3Manager struct {
	admin  adminAuth
	bucket string
	region string

	mu     sync.Mutex
	client s3Client
}

var _ EmissaryAuthManager = (*S3Manager)(nil)

// listRecord pairs a user with the object key it was read from, so a List
// page can resume just after the last user it returned.
type listRecord struct {
	key  string
	user *EmissaryUser
}

// NewS3 builds the S3-backed user manager for the bucket in cfg (the mail
// storage bucket).
func NewS3(cfg Config) *S3Manager {
	return &S3Manager{
		admin:  newAdminAuth(cfg),
		bucket: cfg.S3Bucket,
		region: cfg.Region,
	}
}

func (m *S3Manager) Name() string { return "s3" }

// userFileKey returns the S3 object key that stores a user's record. It
// reuses the storage backend's mailbox layout, so the record lives at
// users/<email>/user.json next to the user's mailboxes.
func userFileKey(id string) string {
	return email.MailboxKeyPrefix(id, "") + userRecordName
}

// isUserRecordKey reports whether key is a user record, i.e.
// users/<email>/user.json. Mailbox messages live one level deeper
// (users/<email>/<mailbox>/...), so a message file named user.json can
// never match.
func isUserRecordKey(key string) bool {
	rest, ok := strings.CutPrefix(key, email.MailboxKeyRoot)
	if !ok {
		return false
	}
	id, name, found := strings.Cut(rest, "/")
	return found && id != "" && name == userRecordName
}

func (m *S3Manager) Authenticate(ctx context.Context, username, password string) (*User, error) {
	return authenticateUser(ctx, m, m.admin, username, password)
}

func (m *S3Manager) Lookup(ctx context.Context, username string) (*User, error) {
	return lookupUser(ctx, m, m.admin, username)
}

func (m *S3Manager) List(ctx context.Context, cursor string, pageSize int) ([]*EmissaryUser, string, error) {
	startAfter, pageSize, err := pageParams(cursor, pageSize, true)
	if err != nil {
		return nil, "", err
	}

	client, err := m.getClient(ctx)
	if err != nil {
		return nil, "", err
	}

	// Each record is tracked with its object key. The cursor is the key of
	// the last user returned, resumed with StartAfter (exclusive), so pages
	// never skip or repeat a record even when a page holds more keys than
	// user records (mailboxes share the users/ listing prefix).
	records := make([]listRecord, 0, pageSize)
	after := startAfter
	for {
		input := &s3.ListObjectsV2Input{
			Bucket:  aws.String(m.bucket),
			Prefix:  aws.String(email.MailboxKeyRoot),
			MaxKeys: aws.Int32(int32(maxUserPageSize)),
		}
		if after != "" {
			input.StartAfter = aws.String(after)
		}

		output, err := client.ListObjectsV2(ctx, input)
		if err != nil {
			return nil, "", fmt.Errorf("failed to list users: %w", err)
		}

		// Scan the whole page so records after the page cut are re-listed
		// on the next call instead of being skipped.
		for _, obj := range output.Contents {
			if obj.Key == nil || !isUserRecordKey(*obj.Key) {
				continue
			}
			user, err := m.getUserByKey(ctx, client, *obj.Key)
			if err != nil {
				if errors.Is(err, ErrUserNotFound) {
					// The record disappeared between the list and the get.
					continue
				}
				return nil, "", err
			}
			records = append(records, listRecord{key: *obj.Key, user: user})
		}

		if len(records) >= pageSize {
			users := make([]*EmissaryUser, 0, pageSize)
			for _, record := range records[:pageSize] {
				users = append(users, record.user)
			}
			// A cursor is only needed when records provably remain: more
			// were listed on this page after the last one returned, or the
			// page was truncated and later pages may hold records.
			nextCursor := ""
			if len(records) > pageSize || aws.ToBool(output.IsTruncated) {
				nextCursor = encodeUserCursor(records[pageSize-1].key)
			}
			return users, nextCursor, nil
		}

		if !aws.ToBool(output.IsTruncated) {
			break
		}
		token := aws.ToString(output.NextContinuationToken)
		if token == "" {
			break
		}
		after = token
	}

	users := make([]*EmissaryUser, 0, len(records))
	for _, record := range records {
		users = append(users, record.user)
	}
	return users, "", nil
}

func (m *S3Manager) Autocomplete(ctx context.Context, q string) ([]string, error) {
	return autocompleteFromList(ctx, m, q)
}

func (m *S3Manager) Create(ctx context.Context, user *EmissaryUser) error {
	if err := prepareForCreate(user); err != nil {
		return err
	}
	applyCreateSchemaVersion(user)
	id := user.Email

	body, err := json.Marshal(user)
	if err != nil {
		return fmt.Errorf("failed to marshal user: %w", err)
	}

	client, err := m.getClient(ctx)
	if err != nil {
		return err
	}

	key := userFileKey(id)
	exists, err := objectExists(ctx, client, m.bucket, key)
	if err != nil {
		return err
	}
	if exists {
		return ErrUserExists
	}

	_, err = client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(m.bucket),
		Key:    aws.String(key),
		Body:   bytes.NewReader(body),
	})
	if err != nil {
		return fmt.Errorf("failed to create user: %w", err)
	}
	return nil
}

func (m *S3Manager) Save(ctx context.Context, user *EmissaryUser) error {
	if err := prepareForSave(user); err != nil {
		return err
	}
	id := user.Email

	body, err := json.Marshal(user)
	if err != nil {
		return fmt.Errorf("failed to marshal user: %w", err)
	}

	client, err := m.getClient(ctx)
	if err != nil {
		return err
	}

	key := userFileKey(id)
	exists, err := objectExists(ctx, client, m.bucket, key)
	if err != nil {
		return err
	}
	if !exists {
		return ErrUserNotFound
	}

	_, err = client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(m.bucket),
		Key:    aws.String(key),
		Body:   bytes.NewReader(body),
	})
	if err != nil {
		return fmt.Errorf("failed to save user: %w", err)
	}
	return nil
}

func (m *S3Manager) Get(ctx context.Context, email string) (*EmissaryUser, error) {
	id := normalizeUserID(email)
	if id == "" {
		return nil, ErrUserNotFound
	}

	client, err := m.getClient(ctx)
	if err != nil {
		return nil, err
	}
	return m.getUserByKey(ctx, client, userFileKey(id))
}

// getUserByKey reads and decodes the user record at key. A missing object is
// ErrUserNotFound: a mailbox directory without a user.json is not a user.
func (m *S3Manager) getUserByKey(ctx context.Context, client s3Client, key string) (*EmissaryUser, error) {
	output, err := client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(m.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		if isObjectNotFound(err) {
			return nil, ErrUserNotFound
		}
		return nil, fmt.Errorf("failed to get user %q: %w", key, err)
	}
	defer output.Body.Close()

	body, err := io.ReadAll(output.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read user %q: %w", key, err)
	}

	var user EmissaryUser
	if err := json.Unmarshal(body, &user); err != nil {
		return nil, fmt.Errorf("failed to decode user %q: %w", key, err)
	}
	if user.Rules == nil {
		user.Rules = []string{}
	}
	return &user, nil
}

func (m *S3Manager) Delete(ctx context.Context, email string) error {
	id := normalizeUserID(email)
	if id == "" {
		return ErrUserNotFound
	}

	client, err := m.getClient(ctx)
	if err != nil {
		return err
	}

	key := userFileKey(id)
	exists, err := objectExists(ctx, client, m.bucket, key)
	if err != nil {
		return err
	}
	if !exists {
		return ErrUserNotFound
	}

	// DeleteObject succeeds even when the key does not exist, so the
	// object's presence is checked first to keep ErrNotFound meaningful.
	_, err = client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(m.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return fmt.Errorf("failed to delete user: %w", err)
	}
	return nil
}

func (m *S3Manager) ChangePassword(ctx context.Context, email, oldPassword, newPassword string) error {
	return changePassword(ctx, m, email, oldPassword, newPassword)
}

func (m *S3Manager) getClient(ctx context.Context) (s3Client, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.client != nil {
		return m.client, nil
	}

	var options []func(*awsconfig.LoadOptions) error
	if strings.TrimSpace(m.region) != "" {
		options = append(options, awsconfig.WithRegion(m.region))
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx, options...)
	if err != nil {
		return nil, fmt.Errorf("failed to load AWS config: %w", err)
	}
	m.client = s3.NewFromConfig(cfg)
	return m.client, nil
}

// objectExists reports whether the object exists, distinguishing a missing
// object from any other S3 failure.
func objectExists(ctx context.Context, client s3Client, bucket, key string) (bool, error) {
	_, err := client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(bucket),
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

// isObjectNotFound reports whether err is S3's "the object is not there".
// GetObject returns NoSuchKey, HeadObject returns NotFound; both can also
// surface as a plain HTTP 404 response error.
func isObjectNotFound(err error) bool {
	var noSuchKey *types.NoSuchKey
	if errors.As(err, &noSuchKey) {
		return true
	}
	var notFound *types.NotFound
	if errors.As(err, &notFound) {
		return true
	}
	var responseError *smithyhttp.ResponseError
	return errors.As(err, &responseError) && responseError.HTTPStatusCode() == http.StatusNotFound
}
