package storage

import (
	"context"
	"errors"
	"io"
	"sort"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

const testMessage = "From: alice@example.com\r\n" +
	"To: bob@example.com\r\n" +
	"Subject: Hello\r\n" +
	"Date: Mon, 09 Nov 2026 10:00:00 +0000\r\n" +
	"\r\n" +
	"Message body\r\n"

type fakeS3 struct {
	objects   map[string]string
	requested []string
}

func newFakeS3(objects map[string]string) *fakeS3 {
	return &fakeS3{objects: objects}
}

func (f *fakeS3) ListObjectsV2(_ context.Context, input *s3.ListObjectsV2Input, _ ...func(*s3.Options)) (*s3.ListObjectsV2Output, error) {
	prefix := aws.ToString(input.Prefix)
	delimiter := aws.ToString(input.Delimiter)
	keys := make([]string, 0, len(f.objects))
	for key := range f.objects {
		if strings.HasPrefix(key, prefix) {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)

	contents := make([]types.Object, 0, len(keys))
	commonPrefixes := make([]types.CommonPrefix, 0)
	seenPrefixes := make(map[string]bool)
	for _, key := range keys {
		if delimiter != "" {
			rest := strings.TrimPrefix(key, prefix)
			if idx := strings.Index(rest, delimiter); idx >= 0 {
				commonPrefix := prefix + rest[:idx+len(delimiter)]
				if !seenPrefixes[commonPrefix] {
					seenPrefixes[commonPrefix] = true
					commonPrefixes = append(commonPrefixes, types.CommonPrefix{Prefix: aws.String(commonPrefix)})
				}
				continue
			}
		}
		key := key
		contents = append(contents, types.Object{Key: &key})
	}
	return &s3.ListObjectsV2Output{Contents: contents, CommonPrefixes: commonPrefixes}, nil
}

func (f *fakeS3) GetObject(_ context.Context, input *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	key := aws.ToString(input.Key)
	f.requested = append(f.requested, key)

	body, ok := f.objects[key]
	if !ok {
		return nil, &types.NoSuchKey{}
	}
	return &s3.GetObjectOutput{Body: io.NopCloser(strings.NewReader(body))}, nil
}

func (f *fakeS3) PutObject(_ context.Context, input *s3.PutObjectInput, _ ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	body, err := io.ReadAll(input.Body)
	if err != nil {
		return nil, err
	}
	f.objects[aws.ToString(input.Key)] = string(body)
	return &s3.PutObjectOutput{}, nil
}

func (f *fakeS3) DeleteObject(_ context.Context, input *s3.DeleteObjectInput, _ ...func(*s3.Options)) (*s3.DeleteObjectOutput, error) {
	delete(f.objects, aws.ToString(input.Key))
	return &s3.DeleteObjectOutput{}, nil
}

func (f *fakeS3) HeadObject(_ context.Context, input *s3.HeadObjectInput, _ ...func(*s3.Options)) (*s3.HeadObjectOutput, error) {
	key := aws.ToString(input.Key)
	body, ok := f.objects[key]
	if !ok {
		return nil, &types.NotFound{}
	}
	return &s3.HeadObjectOutput{ContentLength: aws.Int64(int64(len(body)))}, nil
}

func TestS3StorageListRootReturnsObjectsAndPrefixes(t *testing.T) {
	fake := newFakeS3(map[string]string{
		"staging-one":             testMessage,
		"staging-two":             testMessage,
		"users/alice/inbox/a.eml": testMessage,
		"users/bob/inbox/b.eml":   testMessage,
	})
	store := &S3Storage{client: fake, bucket: "mail-bucket"}

	objects, prefixes, err := store.ListRoot(context.Background())
	if err != nil {
		t.Fatalf("ListRoot() error = %v", err)
	}

	keys := make([]string, 0, len(objects))
	for _, object := range objects {
		keys = append(keys, object.Key)
	}
	sort.Strings(keys)
	if strings.Join(keys, ",") != "staging-one,staging-two" {
		t.Errorf("ListRoot() object keys = %v, want [staging-one staging-two]", keys)
	}
	if len(prefixes) != 1 || prefixes[0] != "users/" {
		t.Errorf("ListRoot() prefixes = %v, want [users/]", prefixes)
	}
}

func TestS3StorageObjectExists(t *testing.T) {
	fake := newFakeS3(map[string]string{"present": testMessage})
	store := &S3Storage{client: fake, bucket: "mail-bucket"}

	exists, err := store.ObjectExists(context.Background(), "present")
	if err != nil {
		t.Fatalf("ObjectExists(present) error = %v", err)
	}
	if !exists {
		t.Error("ObjectExists(present) = false, want true")
	}

	exists, err = store.ObjectExists(context.Background(), "missing")
	if err != nil {
		t.Fatalf("ObjectExists(missing) error = %v", err)
	}
	if exists {
		t.Error("ObjectExists(missing) = true, want false")
	}
}

func TestS3StorageListAccountEmailsUsesMailboxPrefixAndFullKey(t *testing.T) {
	const aliceKey = "users/alice@example.com/inbox/20261117T120000Z_ses-id.eml"
	fake := newFakeS3(map[string]string{
		"ses-staging-id": testMessage,
		aliceKey:         testMessage,
		"users/alice@example.com/sent/20261117_z.eml": testMessage,
		"users/bob@example.com/inbox/20261117_b.eml":  testMessage,
	})
	store := &S3Storage{client: fake, bucket: "mail-bucket"}

	emails, err := store.ListAccountEmails(context.Background(), "Alice@Example.com", "inbox")
	if err != nil {
		t.Fatalf("ListAccountEmails() error = %v", err)
	}
	if len(emails) != 1 {
		t.Fatalf("ListAccountEmails() returned %d emails, want 1", len(emails))
	}
	if emails[0].ID != aliceKey {
		t.Errorf("ID = %q, want full delivered key %q", emails[0].ID, aliceKey)
	}
	if emails[0].AccountID != "alice@example.com" || emails[0].MailboxID != "inbox" {
		t.Errorf("account/mailbox = %q/%q, want alice@example.com/inbox", emails[0].AccountID, emails[0].MailboxID)
	}
	if len(fake.requested) != 1 || fake.requested[0] != aliceKey {
		t.Errorf("requested keys = %v, want only [%s]", fake.requested, aliceKey)
	}
}

func TestS3StorageListAccountEmailsHonorsConfiguredPrefix(t *testing.T) {
	const key = "emails/users/bob@example.com/inbox/20261117_ses-id.eml"
	fake := newFakeS3(map[string]string{
		key:                                     testMessage,
		"users/bob@example.com/inbox/other.eml": testMessage,
	})
	store := &S3Storage{client: fake, bucket: "mail-bucket", prefix: "emails"}

	emails, err := store.ListAccountEmails(context.Background(), "bob@example.com", "inbox")
	if err != nil {
		t.Fatalf("ListAccountEmails() error = %v", err)
	}
	if len(emails) != 1 || emails[0].ID != key {
		t.Fatalf("ListAccountEmails() = %v, want only %s", emails, key)
	}
}

func TestS3StorageListAccountEmailsRequiresAccount(t *testing.T) {
	store := &S3Storage{client: newFakeS3(nil), bucket: "mail-bucket"}
	if _, err := store.ListAccountEmails(context.Background(), "", "inbox"); err == nil {
		t.Error("ListAccountEmails() with empty accountID should fail")
	}
}

func TestS3StorageListAccountEmailsSkipsSystemFilesAndNonEmlObjects(t *testing.T) {
	const aliceKey = "users/alice@example.com/inbox/20261117T120000Z_ses-id.eml"
	fake := newFakeS3(map[string]string{
		aliceKey: testMessage,
		// System records and objects that are not delivered messages must be
		// ignored even when no mailbox filter is supplied.
		"users/alice@example.com/user.json":               `{"id":"alice@example.com"}`,
		"users/alice@example.com/inbox/":                  "",
		"users/alice@example.com/inbox/notes.txt":         testMessage,
		"users/alice@example.com/20261117_no-mailbox.eml": testMessage,
	})
	store := &S3Storage{client: fake, bucket: "mail-bucket"}

	emails, err := store.ListAccountEmails(context.Background(), "alice@example.com", "")
	if err != nil {
		t.Fatalf("ListAccountEmails() error = %v", err)
	}
	if len(emails) != 1 || emails[0].ID != aliceKey {
		t.Fatalf("ListAccountEmails() = %v, want only %s", emails, aliceKey)
	}
	for _, key := range fake.requested {
		if key != aliceKey {
			t.Errorf("GetObject requested unexpected key %q", key)
		}
	}
}

func TestS3StorageGetEmailReadsSESMessageKey(t *testing.T) {
	const key = "4s5k9q1abc-amazon-ses-message-id"
	fake := newFakeS3(map[string]string{key: testMessage})
	store := &S3Storage{client: fake, bucket: "mail-bucket"}

	got, err := store.GetEmail(context.Background(), key)
	if err != nil {
		t.Fatalf("GetEmail() error = %v", err)
	}
	if got.ID != key {
		t.Errorf("ID = %q, want %q", got.ID, key)
	}
	if got.Subject != "Hello" {
		t.Errorf("Subject = %q, want %q", got.Subject, "Hello")
	}
	if got.From.Address != "alice@example.com" {
		t.Errorf("From = %q, want %q", got.From.Address, "alice@example.com")
	}

	for _, requested := range fake.requested {
		if requested == key+".eml" {
			t.Fatalf("GetEmail requested %q; SES keys have no .eml extension", requested)
		}
	}
}

func TestS3StorageGetEmailHonorsPrefix(t *testing.T) {
	fake := newFakeS3(map[string]string{
		"emails/one":  testMessage,
		"root-object": testMessage,
	})
	store := &S3Storage{client: fake, bucket: "mail-bucket", prefix: "emails"}

	got, err := store.GetEmail(context.Background(), "one")
	if err != nil {
		t.Fatalf("GetEmail() error = %v", err)
	}
	if got.ID != "emails/one" {
		t.Fatalf("GetEmail().ID = %q, want emails/one", got.ID)
	}
}

func TestS3StorageGetEmailDoesNotAppendEmlSuffix(t *testing.T) {
	fake := newFakeS3(map[string]string{"legacy.eml": testMessage})
	store := &S3Storage{client: fake, bucket: "mail-bucket"}

	if _, err := store.GetEmail(context.Background(), "legacy"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetEmail() error = %v, want ErrNotFound", err)
	}
	if len(fake.requested) != 1 || fake.requested[0] != "legacy" {
		t.Errorf("requested keys = %v, want only [legacy]", fake.requested)
	}
}

func TestS3StorageGetEmailReturnsErrNotFound(t *testing.T) {
	store := &S3Storage{client: newFakeS3(nil), bucket: "mail-bucket"}

	_, err := store.GetEmail(context.Background(), "missing")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("GetEmail() error = %v, want ErrNotFound", err)
	}
}

func TestS3StorageDeleteEmailRemovesObject(t *testing.T) {
	const key = "4s5k9q1abc-amazon-ses-message-id"
	fake := newFakeS3(map[string]string{key: testMessage})
	store := &S3Storage{client: fake, bucket: "mail-bucket"}

	if err := store.DeleteEmail(context.Background(), key); err != nil {
		t.Fatalf("DeleteEmail() error = %v", err)
	}

	if _, err := store.GetEmail(context.Background(), key); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetEmail() after delete error = %v, want ErrNotFound", err)
	}
}

func TestS3StorageDeleteEmailResolvesPrefixedId(t *testing.T) {
	fake := newFakeS3(map[string]string{
		"emails/one":  testMessage,
		"root-object": testMessage,
	})
	store := &S3Storage{client: fake, bucket: "mail-bucket", prefix: "emails"}

	// A bare id is resolved against the configured prefix.
	if err := store.DeleteEmail(context.Background(), "one"); err != nil {
		t.Fatalf("DeleteEmail() error = %v", err)
	}

	// The store only resolves ids under its prefix; check the untouched
	// root object in the backing bucket directly.
	if _, ok := fake.objects["root-object"]; !ok {
		t.Error("root-object should not have been deleted")
	}
	if _, ok := fake.objects["emails/one"]; ok {
		t.Error("emails/one should have been deleted")
	}
}

func TestS3StorageDeleteEmailReturnsErrNotFound(t *testing.T) {
	fake := newFakeS3(map[string]string{"present": testMessage})
	store := &S3Storage{client: fake, bucket: "mail-bucket"}

	if err := store.DeleteEmail(context.Background(), "missing"); !errors.Is(err, ErrNotFound) {
		t.Errorf("DeleteEmail() missing error = %v, want ErrNotFound", err)
	}
	// Nothing else was touched.
	if _, err := store.GetEmail(context.Background(), "present"); err != nil {
		t.Errorf("GetEmail() error = %v, want nil", err)
	}
}
