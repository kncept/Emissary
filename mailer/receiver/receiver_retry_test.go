package receiver

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/kncept/Emissary/mailer/email"
	"github.com/kncept/Emissary/mailer/sender"
	"github.com/kncept/Emissary/mailer/storage"
)

// failOnceStorage fails delivery for selected accounts until they are removed
// from failFor, simulating a temporary storage outage.
type failOnceStorage struct {
	storage.Storage
	failFor map[string]bool
}

func (s *failOnceStorage) AddEmail(ctx context.Context, accountID string, message *email.UserEmail, raw []byte) (string, error) {
	if s.failFor[accountID] {
		return "", errors.New("storage temporarily unavailable")
	}
	return s.Storage.AddEmail(ctx, accountID, message, raw)
}

// erroringBounceSender fails bounces for selected recipients.
type erroringBounceSender struct {
	*sender.InMemoryRecording
	failFor map[string]bool
}

func (s *erroringBounceSender) SendBounce(ctx context.Context, originalMessageID, bounceSender, recipient, bounceType string, originalMessage *email.UserEmail) error {
	if s.failFor[recipient] {
		return errors.New("bounce transport unavailable")
	}
	return s.InMemoryRecording.SendBounce(ctx, originalMessageID, bounceSender, recipient, bounceType, originalMessage)
}

// conditionalDeleteS3 fails DeleteObject until deleteErr is cleared, modelling
// an S3 outage after delivery succeeded.
type conditionalDeleteS3 struct {
	*fakeS3
	deleteErr error
}

func (f *conditionalDeleteS3) DeleteObject(ctx context.Context, params *s3.DeleteObjectInput, optFns ...func(*s3.Options)) (*s3.DeleteObjectOutput, error) {
	if f.deleteErr != nil {
		return nil, f.deleteErr
	}
	return f.fakeS3.DeleteObject(ctx, params, optFns...)
}

// erroringDedup is a DedupStore whose reads or writes can fail independently.
type erroringDedup struct {
	seen      map[string]bool
	seenErr   error
	recordErr error
}

func newErroringDedup() *erroringDedup {
	return &erroringDedup{seen: make(map[string]bool)}
}

func (d *erroringDedup) Seen(_ context.Context, id string) (bool, error) {
	if d.seenErr != nil {
		return false, d.seenErr
	}
	return d.seen[id], nil
}

func (d *erroringDedup) Record(_ context.Context, id string) error {
	if d.recordErr != nil {
		return d.recordErr
	}
	d.seen[id] = true
	return nil
}

func newRetryReceiver(client S3Client, store storage.Storage, mailSender sender.Sender, dedup DedupStore) (*Receiver, error) {
	return NewReceiver(ReceiverConfig{
		Bucket:      "bucket",
		Domain:      "example.com",
		UserManager: fakeUserManager{},
		Sender:      mailSender,
		SESClient:   client,
		Storage:     store,
		Dedup:       dedup,
	})
}

func TestProcessMessageKeepsStagingOnDeliveryFailure(t *testing.T) {
	const key = "delivery-failure"
	client := &fakeS3{objects: map[string][]byte{key: messageTo("alice@example.com")}}
	emailStore := &failOnceStorage{
		Storage: storage.NewMemoryStorage(),
		failFor: map[string]bool{"alice@example.com": true},
	}
	r, err := newRetryReceiver(client, emailStore, sender.NewInMemoryRecording(), NewMemoryDedup())
	if err != nil {
		t.Fatalf("NewReceiver() error = %v", err)
	}

	err = r.ProcessMessage(context.Background(), key)
	if err == nil || !strings.Contains(err.Error(), "delivery to alice@example.com failed") {
		t.Fatalf("ProcessMessage() error = %v, want a delivery failure", err)
	}
	if _, ok := client.objects[key]; !ok {
		t.Error("staging object was deleted despite a delivery failure")
	}
	listed, err := emailStore.ListEmails(context.Background(), "alice@example.com", "inbox")
	if err != nil || len(listed) != 0 {
		t.Errorf("emails = %v, %v; want no delivery", listed, err)
	}
}

func TestProcessMessageKeepsStagingOnBounceFailure(t *testing.T) {
	const key = "bounce-failure"
	client := &fakeS3{objects: map[string][]byte{key: messageTo("unknown@example.com")}}
	mailSender := &erroringBounceSender{
		InMemoryRecording: sender.NewInMemoryRecording(),
		failFor:           map[string]bool{"unknown@example.com": true},
	}
	r, err := NewReceiver(ReceiverConfig{
		Bucket:      "bucket",
		Domain:      "example.com",
		UserManager: rejectingUserManager{invalid: "unknown@example.com"},
		Sender:      mailSender,
		SESClient:   client,
		Storage:     storage.NewMemoryStorage(),
		Dedup:       NewMemoryDedup(),
	})
	if err != nil {
		t.Fatalf("NewReceiver() error = %v", err)
	}

	err = r.ProcessMessage(context.Background(), key)
	if err == nil || !strings.Contains(err.Error(), "bounce to unknown@example.com failed") {
		t.Fatalf("ProcessMessage() error = %v, want a bounce failure", err)
	}
	if _, ok := client.objects[key]; !ok {
		t.Error("staging object was deleted despite a bounce failure")
	}

	// The failed bounce was not marked handled, so a retry can send it.
	delete(mailSender.failFor, "unknown@example.com")
	if err := r.ProcessMessage(context.Background(), key); err != nil {
		t.Fatalf("ProcessMessage() retry error = %v", err)
	}
	if len(mailSender.Bounces) != 1 {
		t.Errorf("bounces = %+v, want one successful retry", mailSender.Bounces)
	}
	if _, ok := client.objects[key]; ok {
		t.Error("staging object was not deleted after a successful retry")
	}
}

func TestProcessMessageRetryAfterDeleteFailureDoesNotRedeliver(t *testing.T) {
	const key = "delete-failure"
	client := &conditionalDeleteS3{
		fakeS3:    &fakeS3{objects: map[string][]byte{key: messageTo("alice@example.com")}},
		deleteErr: errors.New("S3 unavailable"),
	}
	r, emailStore, _ := newTestReceiver(client)

	err := r.ProcessMessage(context.Background(), key)
	if err == nil || !strings.Contains(err.Error(), "failed to delete staging object") {
		t.Fatalf("ProcessMessage() error = %v, want a delete failure", err)
	}
	listed, err := emailStore.ListEmails(context.Background(), "alice@example.com", "inbox")
	if err != nil || len(listed) != 1 {
		t.Fatalf("emails after first attempt = %v, %v; want one delivery", listed, err)
	}

	client.deleteErr = nil
	if err := r.ProcessMessage(context.Background(), key); err != nil {
		t.Fatalf("ProcessMessage() retry error = %v", err)
	}
	listed, err = emailStore.ListEmails(context.Background(), "alice@example.com", "inbox")
	if err != nil || len(listed) != 1 {
		t.Fatalf("emails after retry = %v, %v; want one delivery, not a duplicate", listed, err)
	}
	if _, ok := client.objects[key]; ok {
		t.Error("staging object was not deleted on retry")
	}
}

func TestProcessMessageRetryOnlyReprocessesFailedRecipients(t *testing.T) {
	const key = "partial-failure"
	raw := []byte(receivedHeader("alice@example.com", "bob@example.com") +
		"From: sender@example.com\r\nSubject: hi\r\n\r\nbody\r\n")
	client := &fakeS3{objects: map[string][]byte{key: raw}}
	emailStore := &failOnceStorage{
		Storage: storage.NewMemoryStorage(),
		failFor: map[string]bool{"alice@example.com": true},
	}
	r, err := newRetryReceiver(client, emailStore, sender.NewInMemoryRecording(), NewMemoryDedup())
	if err != nil {
		t.Fatalf("NewReceiver() error = %v", err)
	}

	if err := r.ProcessMessage(context.Background(), key); err == nil {
		t.Fatal("ProcessMessage() error = nil, want the alice delivery failure")
	}
	if _, ok := client.objects[key]; !ok {
		t.Fatal("staging object was deleted despite a failed recipient")
	}
	bob, err := emailStore.ListEmails(context.Background(), "bob@example.com", "inbox")
	if err != nil || len(bob) != 1 {
		t.Fatalf("bob's emails = %v, %v; want one delivery", bob, err)
	}

	delete(emailStore.failFor, "alice@example.com")
	if err := r.ProcessMessage(context.Background(), key); err != nil {
		t.Fatalf("ProcessMessage() retry error = %v", err)
	}
	for _, account := range []string{"alice@example.com", "bob@example.com"} {
		listed, err := emailStore.ListEmails(context.Background(), account, "inbox")
		if err != nil || len(listed) != 1 {
			t.Errorf("emails for %s = %v, %v; want exactly one delivery", account, listed, err)
		}
	}
}

func TestProcessMessageDedupLookupFailureKeepsStaging(t *testing.T) {
	const key = "dedup-read-failure"
	client := &fakeS3{objects: map[string][]byte{key: messageTo("alice@example.com")}}
	dedup := newErroringDedup()
	dedup.seenErr = errors.New("dedup store unavailable")
	emailStore := storage.NewMemoryStorage()
	r, err := newRetryReceiver(client, emailStore, sender.NewInMemoryRecording(), dedup)
	if err != nil {
		t.Fatalf("NewReceiver() error = %v", err)
	}

	err = r.ProcessMessage(context.Background(), key)
	if err == nil || !strings.Contains(err.Error(), "failed to check dedup state") {
		t.Fatalf("ProcessMessage() error = %v, want a dedup lookup failure", err)
	}
	if _, ok := client.objects[key]; !ok {
		t.Error("staging object was deleted after a dedup lookup failure")
	}
	listed, err := emailStore.ListEmails(context.Background(), "alice@example.com", "inbox")
	if err != nil || len(listed) != 0 {
		t.Errorf("emails = %v, %v; want no delivery", listed, err)
	}
}

func TestProcessMessageDedupRecordFailureKeepsStaging(t *testing.T) {
	const key = "dedup-write-failure"
	client := &fakeS3{objects: map[string][]byte{key: messageTo("alice@example.com")}}
	dedup := newErroringDedup()
	dedup.recordErr = errors.New("dedup store unavailable")
	r, err := newRetryReceiver(client, storage.NewMemoryStorage(), sender.NewInMemoryRecording(), dedup)
	if err != nil {
		t.Fatalf("NewReceiver() error = %v", err)
	}

	err = r.ProcessMessage(context.Background(), key)
	if err == nil || !strings.Contains(err.Error(), "failed to record dedup state") {
		t.Fatalf("ProcessMessage() error = %v, want a dedup write failure", err)
	}
	if _, ok := client.objects[key]; !ok {
		t.Error("staging object was deleted after a dedup write failure")
	}
}
