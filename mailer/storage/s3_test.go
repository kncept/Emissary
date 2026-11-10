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
	keys := make([]string, 0, len(f.objects))
	for key := range f.objects {
		if strings.HasPrefix(key, prefix) {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)

	contents := make([]types.Object, 0, len(keys))
	for _, key := range keys {
		key := key
		contents = append(contents, types.Object{Key: &key})
	}
	return &s3.ListObjectsV2Output{Contents: contents}, nil
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

func TestS3StorageListAllEmailsReadsSESMessageKeys(t *testing.T) {
	const key = "4s5k9q1abc-amazon-ses-message-id"
	fake := newFakeS3(map[string]string{key: testMessage})
	store := &S3Storage{client: fake, bucket: "mail-bucket"}

	emails, err := store.ListAllEmails(context.Background())
	if err != nil {
		t.Fatalf("ListAllEmails() error = %v", err)
	}
	if len(emails) != 1 {
		t.Fatalf("ListAllEmails() returned %d emails, want 1", len(emails))
	}

	got := emails[0]
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

func TestS3StorageListAllEmailsHonorsPrefix(t *testing.T) {
	fake := newFakeS3(map[string]string{
		"emails/one":  testMessage,
		"root-object": testMessage,
	})
	store := &S3Storage{client: fake, bucket: "mail-bucket", prefix: "emails"}

	emails, err := store.ListAllEmails(context.Background())
	if err != nil {
		t.Fatalf("ListAllEmails() error = %v", err)
	}
	if len(emails) != 1 || emails[0].ID != "emails/one" {
		t.Fatalf("ListAllEmails() = %v, want only emails/one", emails)
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
