package storage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"path"
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
	CopyObject(ctx context.Context, params *s3.CopyObjectInput, optFns ...func(*s3.Options)) (*s3.CopyObjectOutput, error)
}

type S3Storage struct {
	client S3API
	bucket string
	prefix string
}

// RootObject is one object directly under the bucket root. The support API
// reports these as unprocessed staging messages. RootObject/ListRoot are the
// one deliberately admin-facing capability on the storage interface.
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

// accountS3Prefix returns the account root prefix in the bucket.
func (s *S3Storage) accountS3Prefix(accountID string) (string, error) {
	accountID = strings.ToLower(strings.TrimSpace(accountID))
	if accountID == "" {
		return "", fmt.Errorf("accountID is required")
	}
	return s.objectKey(email.MailboxKeyRoot + accountID + "/"), nil
}

func (s *S3Storage) mailboxDirS3Prefix(accountID, mailboxID string) (string, error) {
	root, err := s.accountS3Prefix(accountID)
	if err != nil {
		return "", err
	}
	mailboxID = NormalizeMailboxID(mailboxID)
	if mailboxID == "" {
		return "", fmt.Errorf("mailboxID is required")
	}
	return root + mailboxID + "/", nil
}

func (s *S3Storage) mailboxRecordKey(accountID, mailboxID string) (string, error) {
	dir, err := s.mailboxDirS3Prefix(accountID, mailboxID)
	if err != nil {
		return "", err
	}
	return dir + mailboxRecordName, nil
}

func (s *S3Storage) ensureDefaultMailboxes(ctx context.Context, accountID string) error {
	for _, mb := range DefaultMailboxes() {
		dir, err := s.mailboxDirS3Prefix(accountID, mb.ID)
		if err != nil {
			return err
		}
		// Ensure folder marker exists for empty mailboxes/listability.
		_, _ = s.client.PutObject(ctx, &s3.PutObjectInput{
			Bucket: aws.String(s.bucket),
			Key:    aws.String(dir),
			Body:   bytes.NewReader(nil),
		})
		recKey, err := s.mailboxRecordKey(accountID, mb.ID)
		if err != nil {
			return err
		}
		if exists, err := s.ObjectExists(ctx, recKey); err == nil && !exists {
			body, err := json.Marshal(mb)
			if err != nil {
				return err
			}
			if _, err := s.client.PutObject(ctx, &s3.PutObjectInput{
				Bucket: aws.String(s.bucket),
				Key:    aws.String(recKey),
				Body:   bytes.NewReader(body),
			}); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *S3Storage) readMailboxRecord(ctx context.Context, accountID, mailboxID string) (Mailbox, error) {
	mailboxID = NormalizeMailboxID(mailboxID)
	key, err := s.mailboxRecordKey(accountID, mailboxID)
	if err != nil {
		return Mailbox{}, ErrNotFound
	}
	raw, err := s.getRawByExactKey(ctx, key)
	if err != nil {
		if isObjectNotFound(err) {
			// Infer from folder existence or default role mailbox.
			dir, derr := s.mailboxDirS3Prefix(accountID, mailboxID)
			if derr == nil {
				// Treat default mailboxes as existing even if empty bucket state.
				for _, def := range DefaultMailboxes() {
					if def.ID == mailboxID {
						_ = s.ensureDefaultMailboxes(ctx, accountID)
						return def, nil
					}
				}
				// Look for any object under the folder.
				out, lerr := s.client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
					Bucket:  aws.String(s.bucket),
					Prefix:  aws.String(dir),
					MaxKeys: aws.Int32(1),
				})
				if lerr == nil && len(out.Contents) > 0 {
					name := mailboxID
					if name != "" {
						name = strings.ToUpper(name[:1]) + name[1:]
					}
					return Mailbox{ID: mailboxID, Name: name, IsSubscribed: true}, nil
				}
			}
			return Mailbox{}, ErrNotFound
		}
		return Mailbox{}, err
	}
	var mb Mailbox
	if err := json.Unmarshal(raw, &mb); err != nil {
		return Mailbox{}, err
	}
	mb.ID = mailboxID
	if strings.TrimSpace(mb.Name) == "" {
		mb.Name = mb.ID
	}
	return mb, nil
}

func (s *S3Storage) writeMailboxRecord(ctx context.Context, accountID string, mailbox Mailbox) error {
	mailbox.ID = NormalizeMailboxID(mailbox.ID)
	key, err := s.mailboxRecordKey(accountID, mailbox.ID)
	if err != nil {
		return err
	}
	body, err := json.Marshal(mailbox)
	if err != nil {
		return err
	}
	_, err = s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
		Body:   bytes.NewReader(body),
	})
	return err
}

func (s *S3Storage) ListMailboxes(ctx context.Context, accountID string) ([]Mailbox, error) {
	root, err := s.accountS3Prefix(accountID)
	if err != nil {
		return nil, err
	}
	if err := s.ensureDefaultMailboxes(ctx, accountID); err != nil {
		return nil, err
	}
	seen := make(map[string]Mailbox)
	paginator := s3.NewListObjectsV2Paginator(s.client, &s3.ListObjectsV2Input{
		Bucket:    aws.String(s.bucket),
		Prefix:    aws.String(root),
		Delimiter: aws.String("/"),
	})
	for paginator.HasMorePages() {
		out, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to list mailboxes under %q: %w", root, err)
		}
		for _, cp := range out.CommonPrefixes {
			if cp.Prefix == nil {
				continue
			}
			rest := strings.TrimPrefix(*cp.Prefix, root)
			id := NormalizeMailboxID(strings.TrimSuffix(rest, "/"))
			if id == "" {
				continue
			}
			mb, err := s.readMailboxRecord(ctx, accountID, id)
			if err != nil {
				continue
			}
			seen[id] = mb
		}
	}
	for _, def := range DefaultMailboxes() {
		if _, ok := seen[def.ID]; !ok {
			seen[def.ID] = def
		}
	}
	mailboxes := make([]Mailbox, 0, len(seen))
	for _, mb := range seen {
		mailboxes = append(mailboxes, mb)
	}
	SortMailboxes(mailboxes)
	return mailboxes, nil
}

func (s *S3Storage) GetMailbox(ctx context.Context, accountID, mailboxID string) (Mailbox, error) {
	if err := s.ensureDefaultMailboxes(ctx, accountID); err != nil {
		return Mailbox{}, err
	}
	return s.readMailboxRecord(ctx, accountID, mailboxID)
}

func (s *S3Storage) CreateMailbox(ctx context.Context, accountID string, mailbox Mailbox) (Mailbox, error) {
	mailbox, err := NormalizeMailbox(mailbox)
	if err != nil {
		return Mailbox{}, err
	}
	if err := s.ensureDefaultMailboxes(ctx, accountID); err != nil {
		return Mailbox{}, err
	}
	if _, err := s.readMailboxRecord(ctx, accountID, mailbox.ID); err == nil {
		return Mailbox{}, ErrMailboxExists
	}
	if err := ValidateMailboxParent(mailbox, func(parentID string) bool {
		_, err := s.readMailboxRecord(ctx, accountID, parentID)
		return err == nil
	}); err != nil {
		return Mailbox{}, err
	}
	dir, err := s.mailboxDirS3Prefix(accountID, mailbox.ID)
	if err != nil {
		return Mailbox{}, err
	}
	if _, err := s.client.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(dir), Body: bytes.NewReader(nil)}); err != nil {
		return Mailbox{}, err
	}
	if err := s.writeMailboxRecord(ctx, accountID, mailbox); err != nil {
		return Mailbox{}, err
	}
	return mailbox, nil
}

func (s *S3Storage) UpdateMailbox(ctx context.Context, accountID string, mailbox Mailbox) (Mailbox, error) {
	mailbox, err := NormalizeMailbox(mailbox)
	if err != nil {
		return Mailbox{}, err
	}
	if err := s.ensureDefaultMailboxes(ctx, accountID); err != nil {
		return Mailbox{}, err
	}
	if _, err := s.readMailboxRecord(ctx, accountID, mailbox.ID); err != nil {
		return Mailbox{}, ErrNotFound
	}
	if err := ValidateMailboxParent(mailbox, func(parentID string) bool {
		_, err := s.readMailboxRecord(ctx, accountID, parentID)
		return err == nil
	}); err != nil {
		return Mailbox{}, err
	}
	if err := s.writeMailboxRecord(ctx, accountID, mailbox); err != nil {
		return Mailbox{}, err
	}
	return mailbox, nil
}

func (s *S3Storage) DeleteMailbox(ctx context.Context, accountID, mailboxID string) error {
	mailboxID = NormalizeMailboxID(mailboxID)
	if mailboxID == "" {
		return ErrNotFound
	}
	if err := s.ensureDefaultMailboxes(ctx, accountID); err != nil {
		return err
	}
	dir, err := s.mailboxDirS3Prefix(accountID, mailboxID)
	if err != nil {
		return err
	}
	// Check children.
	mailboxes, err := s.ListMailboxes(ctx, accountID)
	if err != nil {
		return err
	}
	found := false
	for _, mb := range mailboxes {
		if mb.ID == mailboxID {
			found = true
		}
		if mb.ParentID != nil && *mb.ParentID == mailboxID {
			return ErrMailboxNotEmpty
		}
	}
	if !found {
		return ErrNotFound
	}
	// Check for messages and collect deletable objects.
	var toDelete []types.ObjectIdentifier
	paginator := s3.NewListObjectsV2Paginator(s.client, &s3.ListObjectsV2Input{Bucket: aws.String(s.bucket), Prefix: aws.String(dir)})
	for paginator.HasMorePages() {
		out, err := paginator.NextPage(ctx)
		if err != nil {
			return err
		}
		for _, obj := range out.Contents {
			if obj.Key == nil {
				continue
			}
			key := *obj.Key
			if email.IsDeliveredMessageKey(key) {
				return ErrMailboxNotEmpty
			}
			base := path.Base(key)
			if base == mailboxRecordName || strings.HasSuffix(key, "/") {
				toDelete = append(toDelete, types.ObjectIdentifier{Key: obj.Key})
			}
		}
	}
	for _, obj := range toDelete {
		_, _ = s.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(s.bucket), Key: obj.Key})
	}
	// Also try deleting folder marker/record directly.
	recKey, _ := s.mailboxRecordKey(accountID, mailboxID)
	_, _ = s.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(recKey)})
	_, _ = s.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(dir)})
	return nil
}

// ListEmails implements the Storage interface. It limits ListObjectsV2 to the
// account's mailbox prefix so messages belonging to other accounts (and
// unprocessed SES staging objects at the bucket root) are never read.
func (s *S3Storage) ListEmails(ctx context.Context, accountID, mailboxID string) ([]*email.ListedEmail, error) {
	mailboxID = NormalizeMailboxID(mailboxID)
	var prefix string
	if mailboxID == "" {
		root, err := s.accountS3Prefix(accountID)
		if err != nil {
			return nil, err
		}
		prefix = root
	} else {
		p, err := s.mailboxDirS3Prefix(accountID, mailboxID)
		if err != nil {
			return nil, err
		}
		prefix = p
	}
	_ = s.ensureDefaultMailboxes(ctx, accountID)
	slog.Debug("S3: listing mail", "bucket", s.bucket, "prefix", prefix)

	var emails []*email.ListedEmail
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
			if obj.Key == nil || !email.IsDeliveredMessageKey(*obj.Key) {
				continue
			}
			message, err := s.getEmailByKey(ctx, *obj.Key)
			if err != nil {
				slog.Warn("S3: skipping unreadable mail object", "key", *obj.Key, "err", err)
				continue
			}
			emails = append(emails, message)
		}
	}
	SortListedEmails(emails)
	slog.Debug("S3: listed mail", "prefix", prefix, "count", len(emails))
	return emails, nil
}

func (s *S3Storage) listPrefix() string {
	if s.prefix == "" {
		return ""
	}
	return s.prefix + "/"
}

// objectKey maps a logical storage key to the physical key in the configured
// bucket prefix. All S3 message operations go through this one place.
func (s *S3Storage) objectKey(logicalKey string) string {
	return s.listPrefix() + logicalKey
}

func (s *S3Storage) getRawByExactKey(ctx context.Context, key string) ([]byte, error) {
	output, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get object %q: %w", key, err)
	}
	defer output.Body.Close()
	body, err := email.ReadLimited(output.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read object %q: %w", key, err)
	}
	return body, nil
}

func (s *S3Storage) getEmailByKey(ctx context.Context, key string) (*email.ListedEmail, error) {
	body, err := s.getRawByExactKey(ctx, key)
	if err != nil {
		return nil, err
	}
	return listedEmailFromRaw(key, body)
}

func (s *S3Storage) resolveEmailKey(ctx context.Context, accountID, id string) (string, error) {
	candidates, err := emailIDCandidates(accountID, id, s.objectKey, nil)
	if err != nil {
		return "", err
	}
	// A full delivered key needs no mailbox scan.
	if len(candidates.Keys) > 0 {
		return s.probeEmailKeys(ctx, candidates.Keys)
	}

	mailboxes, err := s.ListMailboxes(ctx, candidates.AccountID)
	if err != nil {
		return "", err
	}
	mailboxIDs := make([]string, 0, len(mailboxes))
	for _, mb := range mailboxes {
		mailboxIDs = append(mailboxIDs, mb.ID)
	}
	candidates, err = emailIDCandidates(accountID, id, s.objectKey, mailboxIDs)
	if err != nil {
		return "", err
	}
	return s.probeEmailKeys(ctx, candidates.Keys)
}

// probeEmailKeys checks each candidate in order. A genuine backend error
// aborts the lookup; only a missing object moves on to the next candidate.
func (s *S3Storage) probeEmailKeys(ctx context.Context, keys []string) (string, error) {
	for _, key := range keys {
		if key == "" {
			continue
		}
		_, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
		if err == nil {
			return key, nil
		}
		if !isObjectNotFound(err) {
			return "", err
		}
	}
	return "", ErrNotFound
}

// GetEmail implements the Storage interface.
func (s *S3Storage) GetEmail(ctx context.Context, accountID, id string) (*email.ListedEmail, error) {
	key, err := s.resolveEmailKey(ctx, accountID, id)
	if err != nil {
		return nil, err
	}
	msg, err := s.getEmailByKey(ctx, key)
	if err != nil {
		if isObjectNotFound(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return msg, nil
}

// GetEmailRaw implements the Storage interface.
func (s *S3Storage) GetEmailRaw(ctx context.Context, accountID, id string) ([]byte, error) {
	key, err := s.resolveEmailKey(ctx, accountID, id)
	if err != nil {
		return nil, err
	}
	raw, err := s.getRawByExactKey(ctx, key)
	if err != nil {
		if isObjectNotFound(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return raw, nil
}

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

// Name returns the storage name.
func (s *S3Storage) Name() string {
	return "S3Storage"
}

// AddEmail implements the Storage interface. Messages go to mailboxID (default
// inbox). The returned id is the stable message token.
func (s *S3Storage) AddEmail(ctx context.Context, accountID, mailboxID string, message *email.UserEmail, raw []byte) (string, error) {
	mailboxID = NormalizeMailboxID(mailboxID)
	if mailboxID == "" {
		mailboxID = "inbox"
	}
	if strings.TrimSpace(accountID) == "" {
		return "", errors.New("accountID is required")
	}
	if message == nil {
		return "", errors.New("invalid email")
	}
	if err := s.ensureDefaultMailboxes(ctx, accountID); err != nil {
		return "", err
	}
	if _, err := s.readMailboxRecord(ctx, accountID, mailboxID); err != nil {
		return "", ErrNotFound
	}

	token, stored, err := newDeliveredToken(message, raw)
	if err != nil {
		return "", err
	}
	logicalKey := email.DeliveredMessageKey(accountID, mailboxID, token)
	key := s.objectKey(logicalKey)

	slog.Debug("S3: storing email", "key", key, "account", accountID, "mailbox", mailboxID, "rawBytes", len(stored))
	if _, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
		Body:   bytes.NewReader(stored),
	}); err != nil {
		slog.Error("S3: failed to store email", "key", key, "err", err)
		return "", fmt.Errorf("failed to add email %q: %w", key, err)
	}

	return email.NormalizeEmailID(token)
}

func (s *S3Storage) MoveEmail(ctx context.Context, accountID, id, destMailboxID string) (*email.ListedEmail, error) {
	destMailboxID = NormalizeMailboxID(destMailboxID)
	if destMailboxID == "" {
		return nil, ErrNotFound
	}
	if err := s.ensureDefaultMailboxes(ctx, accountID); err != nil {
		return nil, err
	}
	if _, err := s.readMailboxRecord(ctx, accountID, destMailboxID); err != nil {
		return nil, ErrNotFound
	}
	srcKey, err := s.resolveEmailKey(ctx, accountID, id)
	if err != nil {
		return nil, err
	}
	token, ok := email.DeliveredMessageToken(srcKey)
	if !ok {
		return nil, ErrNotFound
	}
	destLogical := email.DeliveredMessageKey(accountID, destMailboxID, token)
	destKey := s.objectKey(destLogical)
	if destKey == srcKey {
		return s.getEmailByKey(ctx, srcKey)
	}
	// Copy then delete.
	_, err = s.client.CopyObject(ctx, &s3.CopyObjectInput{
		Bucket:     aws.String(s.bucket),
		CopySource: aws.String(s.bucket + "/" + srcKey),
		Key:        aws.String(destKey),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to move email %q to %q: %w", srcKey, destKey, err)
	}
	if _, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(srcKey)}); err != nil {
		return nil, fmt.Errorf("failed to delete source after move %q: %w", srcKey, err)
	}
	return s.getEmailByKey(ctx, destKey)
}

// DeleteEmail implements the Storage interface.
func (s *S3Storage) DeleteEmail(ctx context.Context, accountID, id string) error {
	key, err := s.resolveEmailKey(ctx, accountID, id)
	if err != nil {
		return err
	}
	_, err = s.client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
	if err != nil {
		if isObjectNotFound(err) {
			return ErrNotFound
		}
		return fmt.Errorf("failed to check object %q: %w", key, err)
	}
	if _, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)}); err != nil {
		return fmt.Errorf("failed to delete object %q: %w", key, err)
	}
	return nil
}
