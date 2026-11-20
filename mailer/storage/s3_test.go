package storage

import (
	"context"
	"errors"
	"io"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/kncept/Emissary/mailer/email"
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

func TestS3StorageAddEmailStoresInInboxAndReturnsFullKey(t *testing.T) {
	fake := newFakeS3(map[string]string{})
	store := &S3Storage{client: fake, bucket: "mail-bucket"}
	message := &email.UserEmail{
		From:    email.Address{Address: "alice@example.com"},
		To:      []email.Address{{Address: "bob@example.com"}},
		Subject: "Hello",
		Date:    time.Date(2026, time.November, 9, 10, 0, 0, 0, time.UTC),
		Text:    "Message body",
	}

	id, err := store.AddEmail(context.Background(), "Bob@Example.com", message, nil)
	if err != nil {
		t.Fatalf("AddEmail() error = %v", err)
	}

	pattern := regexp.MustCompile(`^users/bob@example.com/inbox/20261109T100000_[0-9a-f]{32}\.eml$`)
	if !pattern.MatchString(id) {
		t.Fatalf("AddEmail() id = %q, want delivered inbox key with generated id", id)
	}
	raw, ok := fake.objects[id]
	if !ok {
		t.Fatalf("AddEmail() did not store object %q; keys = %v", id, fake.objects)
	}
	if !strings.Contains(raw, "Subject: Hello") || !strings.Contains(raw, "Message body") {
		t.Errorf("stored raw message = %q, want subject and body", raw)
	}

	listed, err := store.GetEmail(context.Background(), id)
	if err != nil {
		t.Fatalf("GetEmail(%q) error = %v", id, err)
	}
	if listed.Ref.ID != id || listed.Ref.AccountID != "bob@example.com" || listed.Ref.MailboxID != "inbox" {
		t.Errorf("ref = %+v, want id %q in bob@example.com/inbox", listed.Ref, id)
	}
}

func TestS3StorageAddEmailHonorsConfiguredPrefix(t *testing.T) {
	fake := newFakeS3(map[string]string{})
	store := &S3Storage{client: fake, bucket: "mail-bucket", prefix: "emails"}
	message := &email.UserEmail{Subject: "Hello", Text: "Message body"}

	id, err := store.AddEmail(context.Background(), "bob@example.com", message, nil)
	if err != nil {
		t.Fatalf("AddEmail() error = %v", err)
	}
	if !strings.HasPrefix(id, "emails/users/bob@example.com/inbox/") {
		t.Fatalf("AddEmail() id = %q, want configured prefix", id)
	}
	if _, ok := fake.objects[id]; !ok {
		t.Fatalf("AddEmail() did not store object %q", id)
	}
}

func TestS3StorageAddEmailValidatesInput(t *testing.T) {
	store := &S3Storage{client: newFakeS3(map[string]string{}), bucket: "mail-bucket"}

	if _, err := store.AddEmail(context.Background(), "", &email.UserEmail{Subject: "Hello"}, nil); err == nil {
		t.Error("AddEmail() with empty accountID should fail")
	}
	if _, err := store.AddEmail(context.Background(), "bob@example.com", nil, nil); err == nil {
		t.Error("AddEmail() with nil message should fail")
	}
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

func TestS3StorageListEmailsUsesMailboxPrefixAndFullKey(t *testing.T) {
	const aliceKey = "users/alice@example.com/inbox/20261117T120000_ses-id.eml"
	fake := newFakeS3(map[string]string{
		"ses-staging-id": testMessage,
		aliceKey:         testMessage,
		"users/alice@example.com/sent/20261117_z.eml": testMessage,
		"users/bob@example.com/inbox/20261117_b.eml":  testMessage,
	})
	store := &S3Storage{client: fake, bucket: "mail-bucket"}

	emails, err := store.ListEmails(context.Background(), "Alice@Example.com", "inbox")
	if err != nil {
		t.Fatalf("ListEmails() error = %v", err)
	}
	if len(emails) != 1 {
		t.Fatalf("ListEmails() returned %d emails, want 1", len(emails))
	}
	if emails[0].Ref.ID != aliceKey {
		t.Errorf("ID = %q, want full delivered key %q", emails[0].Ref.ID, aliceKey)
	}
	if emails[0].Ref.AccountID != "alice@example.com" || emails[0].Ref.MailboxID != "inbox" {
		t.Errorf("account/mailbox = %q/%q, want alice@example.com/inbox", emails[0].Ref.AccountID, emails[0].Ref.MailboxID)
	}
	if len(fake.requested) != 1 || fake.requested[0] != aliceKey {
		t.Errorf("requested keys = %v, want only [%s]", fake.requested, aliceKey)
	}
}

func TestS3StorageListEmailsHonorsConfiguredPrefix(t *testing.T) {
	const key = "emails/users/bob@example.com/inbox/20261117_ses-id.eml"
	fake := newFakeS3(map[string]string{
		key:                                     testMessage,
		"users/bob@example.com/inbox/other.eml": testMessage,
	})
	store := &S3Storage{client: fake, bucket: "mail-bucket", prefix: "emails"}

	emails, err := store.ListEmails(context.Background(), "bob@example.com", "inbox")
	if err != nil {
		t.Fatalf("ListEmails() error = %v", err)
	}
	if len(emails) != 1 || emails[0].Ref.ID != key {
		t.Fatalf("ListEmails() = %v, want only %s", emails, key)
	}
}

func TestS3StorageListEmailsRequiresAccount(t *testing.T) {
	store := &S3Storage{client: newFakeS3(nil), bucket: "mail-bucket"}
	if _, err := store.ListEmails(context.Background(), "", "inbox"); err == nil {
		t.Error("ListEmails() with empty accountID should fail")
	}
}

func TestS3StorageListEmailsSkipsSystemFilesAndNonEmlObjects(t *testing.T) {
	const aliceKey = "users/alice@example.com/inbox/20261117T120000_ses-id.eml"
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

	emails, err := store.ListEmails(context.Background(), "alice@example.com", "")
	if err != nil {
		t.Fatalf("ListEmails() error = %v", err)
	}
	if len(emails) != 1 || emails[0].Ref.ID != aliceKey {
		t.Fatalf("ListEmails() = %v, want only %s", emails, aliceKey)
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
	if got.Ref.ID != key {
		t.Errorf("ID = %q, want %q", got.Ref.ID, key)
	}
	if got.Email.Subject != "Hello" {
		t.Errorf("Subject = %q, want %q", got.Email.Subject, "Hello")
	}
	if got.Email.From.Address != "alice@example.com" {
		t.Errorf("From = %q, want %q", got.Email.From.Address, "alice@example.com")
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
	if got.Ref.ID != "emails/one" {
		t.Fatalf("GetEmail().ID = %q, want emails/one", got.Ref.ID)
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

func TestS3StorageGetEmailRawReturnsExactBytes(t *testing.T) {
	const key = "users/alice@example.com/inbox/20261109T100000_ses-id.eml"
	const raw = "From: =?utf-8?B?QWxpY2U=?= <alice@example.com>\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n" +
		"Content-Transfer-Encoding: base64\r\n" +
		"\r\n" +
		"SGVsbG8gd29ybGQ=\r\n"
	fake := newFakeS3(map[string]string{key: raw})
	store := &S3Storage{client: fake, bucket: "mail-bucket"}

	got, err := store.GetEmailRaw(context.Background(), key)
	if err != nil {
		t.Fatalf("GetEmailRaw() error = %v", err)
	}
	if string(got) != raw {
		t.Errorf("GetEmailRaw() = %q, want exact raw bytes %q", got, raw)
	}
	if len(fake.requested) != 1 || fake.requested[0] != key {
		t.Errorf("requested keys = %v, want only [%s]", fake.requested, key)
	}
}

func TestS3StorageGetEmailRawHonorsPrefix(t *testing.T) {
	fake := newFakeS3(map[string]string{"emails/one": testMessage})
	store := &S3Storage{client: fake, bucket: "mail-bucket", prefix: "emails"}

	got, err := store.GetEmailRaw(context.Background(), "one")
	if err != nil {
		t.Fatalf("GetEmailRaw() error = %v", err)
	}
	if string(got) != testMessage {
		t.Errorf("GetEmailRaw() = %q, want %q", got, testMessage)
	}
}

func TestS3StorageGetEmailRawReturnsErrNotFound(t *testing.T) {
	store := &S3Storage{client: newFakeS3(nil), bucket: "mail-bucket"}

	if _, err := store.GetEmailRaw(context.Background(), "missing"); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetEmailRaw() error = %v, want ErrNotFound", err)
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

func TestS3StorageSessionDraftsAndCreatedIDs(t *testing.T) {
	fake := newFakeS3(map[string]string{})
	store := &S3Storage{client: fake, bucket: "mail-bucket"}
	ctx := context.Background()
	message := &email.UserEmail{
		Subject: "draft subject",
		Text:    "draft body",
	}

	id, err := store.PutDraft(ctx, "session-1", "create1", message)
	if err != nil {
		t.Fatalf("PutDraft() error = %v", err)
	}
	if id != "drafts/create1.json" {
		t.Errorf("draft id = %q, want drafts/create1.json", id)
	}
	objectKey := "_storage/session-1/drafts/create1.json"
	if _, ok := fake.objects[objectKey]; !ok {
		t.Fatalf("draft was not stored under %q: %v", objectKey, fake.objects)
	}

	loaded, err := store.GetDraft(ctx, "session-1", id)
	if err != nil {
		t.Fatalf("GetDraft() error = %v", err)
	}
	if loaded.Subject != message.Subject || loaded.Text != message.Text {
		t.Errorf("loaded draft = %+v, want subject %q and text %q", loaded, message.Subject, message.Text)
	}

	if err := store.SaveCreatedIDs(ctx, "session-1", map[string]string{"create1": id}); err != nil {
		t.Fatalf("SaveCreatedIDs() error = %v", err)
	}
	createdKey := "_storage/session-1/created-ids.json"
	if _, ok := fake.objects[createdKey]; !ok {
		t.Fatalf("created ids were not stored under %q: %v", createdKey, fake.objects)
	}
	ids, err := store.LoadCreatedIDs(ctx, "session-1")
	if err != nil {
		t.Fatalf("LoadCreatedIDs() error = %v", err)
	}
	if got := ids["create1"]; got != id {
		t.Errorf("created id = %q, want %q", got, id)
	}

	if err := store.DeleteDraft(ctx, "session-1", id); err != nil {
		t.Fatalf("DeleteDraft() error = %v", err)
	}
	if _, ok := fake.objects[objectKey]; ok {
		t.Error("draft object still exists after DeleteDraft()")
	}
	if _, err := store.GetDraft(ctx, "session-1", id); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetDraft() after delete error = %v, want ErrNotFound", err)
	}
}

func TestS3StorageSessionKeysRejectTraversal(t *testing.T) {
	fake := newFakeS3(map[string]string{})
	store := &S3Storage{client: fake, bucket: "mail-bucket"}
	message := &email.UserEmail{}

	if _, err := store.PutDraft(context.Background(), "../bad", "create1", message); err == nil {
		t.Error("PutDraft() with traversal session id succeeded")
	}
	if _, err := store.PutDraft(context.Background(), "session-1", "../create1", message); err == nil {
		t.Error("PutDraft() with traversal creation id succeeded")
	}
	if _, err := store.GetDraft(context.Background(), "session-1", "_storage/session-1/../../outside.json"); err == nil {
		t.Error("GetDraft() with traversal id succeeded")
	}
}
