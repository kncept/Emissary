package receiver

import (
	"bytes"
	"context"
	"io"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/kncept/Emissary/mailer/email"
	"github.com/kncept/Emissary/mailer/usermanager"
)

type fakeS3 struct {
	objects map[string][]byte
}

func (f *fakeS3) GetObject(_ context.Context, params *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	key := aws.ToString(params.Key)
	body, ok := f.objects[key]
	if !ok {
		return nil, &types.NoSuchKey{Message: aws.String("no such key")}
	}
	return &s3.GetObjectOutput{Body: io.NopCloser(bytes.NewReader(body))}, nil
}

func (f *fakeS3) PutObject(_ context.Context, params *s3.PutObjectInput, _ ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	body, err := io.ReadAll(params.Body)
	if err != nil {
		return nil, err
	}
	f.objects[aws.ToString(params.Key)] = body
	return &s3.PutObjectOutput{}, nil
}

func (f *fakeS3) DeleteObject(_ context.Context, params *s3.DeleteObjectInput, _ ...func(*s3.Options)) (*s3.DeleteObjectOutput, error) {
	delete(f.objects, aws.ToString(params.Key))
	return &s3.DeleteObjectOutput{}, nil
}

type fakeUserManager struct{}

func (fakeUserManager) Name() string { return "fake" }
func (fakeUserManager) Authenticate(context.Context, string, string) (*usermanager.User, error) {
	return nil, usermanager.ErrInvalidCredentials
}
func (fakeUserManager) Lookup(_ context.Context, username string) (*usermanager.User, error) {
	return &usermanager.User{Email: username}, nil
}
func (fakeUserManager) Autocomplete(context.Context, string) ([]string, error) { return nil, nil }

type bounceCall struct {
	messageID    string
	bounceSender string
	recipient    string
	bounceType   string
	original     *email.UserEmail
}

type fakeSender struct {
	bounces []bounceCall
}

func (*fakeSender) Name() string                                 { return "fake" }
func (*fakeSender) From() string                                 { return "mailer@example.com" }
func (*fakeSender) Send(context.Context, *email.UserEmail) error { return nil }
func (s *fakeSender) SendBounce(_ context.Context, originalMessageID, bounceSender, recipient, bounceType string, original *email.UserEmail) error {
	s.bounces = append(s.bounces, bounceCall{originalMessageID, bounceSender, recipient, bounceType, original})
	return nil
}

// rejectingUserManager treats one address as unknown so the receiver bounces it.
type rejectingUserManager struct {
	fakeUserManager
	invalid string
}

func (m rejectingUserManager) Lookup(_ context.Context, username string) (*usermanager.User, error) {
	if strings.EqualFold(username, m.invalid) {
		return nil, usermanager.ErrUserNotFound
	}
	return m.fakeUserManager.Lookup(context.Background(), username)
}

func newTestReceiver(client S3Client) *Receiver {
	r, err := NewReceiver(ReceiverConfig{
		Bucket:      "bucket",
		Domain:      "example.com",
		UserManager: fakeUserManager{},
		Sender:      &fakeSender{},
		SESClient:   client,
	})
	if err != nil {
		panic(err)
	}
	return r
}

func TestProcessMessageSavesWithISO8601BasicTimestamp(t *testing.T) {
	const key = "01000192a3b4c5d6"
	// 15:30 +0100 is 14:30 UTC: the key must use UTC, without separators or a Z suffix.
	raw := "From: sender@example.com\r\nTo: alice@example.com\r\n" +
		"Date: Fri, 08 Mar 2024 15:30:00 +0100\r\nSubject: hi\r\n\r\nbody\r\n"
	client := &fakeS3{objects: map[string][]byte{key: []byte(raw)}}

	if err := newTestReceiver(client).ProcessMessage(context.Background(), key); err != nil {
		t.Fatalf("ProcessMessage() error = %v", err)
	}

	want := "users/alice@example.com/inbox/20240308T143000_" + key + ".eml"
	if _, ok := client.objects[want]; !ok {
		t.Fatalf("message was not saved under %q; keys = %v", want, deliveredKeys(client.objects))
	}
	if _, ok := client.objects[key]; ok {
		t.Error("staging object was not deleted")
	}
}

func TestProcessMessageFallbackTimestampUsesISO8601BasicFormat(t *testing.T) {
	const key = "01000192abcdef"
	raw := "From: sender@example.com\r\nTo: alice@example.com\r\nSubject: no date\r\n\r\nbody\r\n"
	client := &fakeS3{objects: map[string][]byte{key: []byte(raw)}}

	if err := newTestReceiver(client).ProcessMessage(context.Background(), key); err != nil {
		t.Fatalf("ProcessMessage() error = %v", err)
	}

	keys := deliveredKeys(client.objects)
	if len(keys) != 1 {
		t.Fatalf("got %d delivered keys, want 1: %v", len(keys), keys)
	}
	pattern := regexp.MustCompile(`^users/alice@example.com/inbox/(\d{8}T\d{6})_` + key + `\.eml$`)
	match := pattern.FindStringSubmatch(keys[0])
	if match == nil {
		t.Fatalf("delivered key %q does not use ISO 8601 basic UTC timestamp", keys[0])
	}
	if _, err := time.Parse(email.DeliveredTimestampFormat, match[1]); err != nil {
		t.Fatalf("timestamp %q is not a valid %s timestamp: %v", match[1], email.DeliveredTimestampFormat, err)
	}
}

func TestProcessMessageSendsBounceThroughSender(t *testing.T) {
	const key = "01000192a3b4c5d7"
	raw := "From: sender@example.com\r\nTo: unknown@example.com\r\nSubject: hi\r\n\r\nbody\r\n"
	client := &fakeS3{objects: map[string][]byte{key: []byte(raw)}}
	mailSender := &fakeSender{}
	r, err := NewReceiver(ReceiverConfig{
		Bucket:      "bucket",
		Domain:      "example.com",
		UserManager: rejectingUserManager{invalid: "unknown@example.com"},
		Sender:      mailSender,
		SESClient:   client,
	})
	if err != nil {
		t.Fatalf("NewReceiver() error = %v", err)
	}
	if err := r.ProcessMessage(context.Background(), key); err != nil {
		t.Fatalf("ProcessMessage() error = %v", err)
	}

	if len(mailSender.bounces) != 1 {
		t.Fatalf("bounces = %+v, want one bounce through the sender", mailSender.bounces)
	}
	bounce := mailSender.bounces[0]
	if bounce.messageID != key {
		t.Errorf("bounce message id = %q, want %q", bounce.messageID, key)
	}
	if bounce.bounceSender != "mailer-daemon@example.com" {
		t.Errorf("bounce sender = %q, want mailer-daemon@example.com", bounce.bounceSender)
	}
	if bounce.recipient != "unknown@example.com" {
		t.Errorf("bounce recipient = %q, want unknown@example.com", bounce.recipient)
	}
	if bounce.bounceType != "UserUnknown" {
		t.Errorf("bounce type = %q, want UserUnknown", bounce.bounceType)
	}
	if bounce.original == nil || bounce.original.Subject != "hi" {
		t.Errorf("bounce original = %+v, want the original message", bounce.original)
	}
}

func deliveredKeys(objects map[string][]byte) []string {
	var keys []string
	for key := range objects {
		if strings.HasPrefix(key, "users/") {
			keys = append(keys, key)
		}
	}
	return keys
}
