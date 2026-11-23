package receiver

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/kncept/Emissary/mailer/components/sender"
	"github.com/kncept/Emissary/mailer/components/storage"
	"github.com/kncept/Emissary/mailer/email"
)

// failOnceStorage fails delivery for selected accounts until they are removed
// from failFor, simulating a temporary storage outage.
type failOnceStorage struct {
	storage.Storage
	failFor map[string]bool
}

func (s *failOnceStorage) AddEmail(ctx context.Context, accountID, mailboxID string, message *email.UserEmail, raw []byte) (string, error) {
	if s.failFor[accountID] {
		return "", errors.New("storage temporarily unavailable")
	}
	return s.Storage.AddEmail(ctx, accountID, mailboxID, message, raw)
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

func newRetryReceiver(store storage.Storage, mailSender sender.Sender, dedup DedupStore) (*Receiver, error) {
	return NewReceiver(ReceiverConfig{
		Domain:      "example.com",
		UserManager: fakeUserManager{},
		Sender:      mailSender,
		Storage:     store,
		Dedup:       dedup,
	})
}

func TestProcessMessageKeepsStagingOnDeliveryFailure(t *testing.T) {
	raw := messageTo("alice@example.com")
	emailStore := &failOnceStorage{
		Storage: storage.NewMemoryStorage(),
		failFor: map[string]bool{"alice@example.com": true},
	}
	r, err := newRetryReceiver(emailStore, sender.NewInMemoryRecording(), NewMemoryDedup())
	if err != nil {
		t.Fatalf("NewReceiver() error = %v", err)
	}

	event := &fakeS3Event{raw: raw}
	err = r.ProcessEvent(context.Background(), event)
	if err == nil || !strings.Contains(err.Error(), "delivery to alice@example.com failed") {
		t.Fatalf("ProcessEvent() error = %v, want a delivery failure", err)
	}
	if event.completed {
		t.Error("OnComplete was called despite a delivery failure")
	}
	listed, err := emailStore.ListEmails(context.Background(), "alice@example.com", "inbox")
	if err != nil || len(listed) != 0 {
		t.Errorf("emails = %v, %v; want no delivery", listed, err)
	}
}

func TestProcessMessageKeepsStagingOnBounceFailure(t *testing.T) {
	raw := messageTo("unknown@example.com")
	mailSender := &erroringBounceSender{
		InMemoryRecording: sender.NewInMemoryRecording(),
		failFor:           map[string]bool{"unknown@example.com": true},
	}
	r, err := NewReceiver(ReceiverConfig{
		Domain:      "example.com",
		UserManager: rejectingUserManager{invalid: "unknown@example.com"},
		Sender:      mailSender,
		Storage:     storage.NewMemoryStorage(),
		Dedup:       NewMemoryDedup(),
	})
	if err != nil {
		t.Fatalf("NewReceiver() error = %v", err)
	}

	event := &fakeS3Event{raw: raw}
	err = r.ProcessEvent(context.Background(), event)
	if err == nil || !strings.Contains(err.Error(), "bounce to unknown@example.com failed") {
		t.Fatalf("ProcessEvent() error = %v, want a bounce failure", err)
	}
	if event.completed {
		t.Error("OnComplete was called despite a bounce failure")
	}

	// The failed bounce was not marked handled, so a retry can send it.
	delete(mailSender.failFor, "unknown@example.com")
	event2 := &fakeS3Event{raw: raw}
	if err := r.ProcessEvent(context.Background(), event2); err != nil {
		t.Fatalf("ProcessEvent() retry error = %v", err)
	}
	if len(mailSender.Bounces) != 1 {
		t.Errorf("bounces = %+v, want one successful retry", mailSender.Bounces)
	}
	if !event2.completed {
		t.Error("OnComplete was not called after successful retry")
	}
}

func TestProcessMessageRetryAfterDeleteFailureDoesNotRedeliver(t *testing.T) {
	raw := messageTo("alice@example.com")
	emailStore := storage.NewMemoryStorage()
	r, err := newRetryReceiver(emailStore, sender.NewInMemoryRecording(), NewMemoryDedup())
	if err != nil {
		t.Fatalf("NewReceiver() error = %v", err)
	}

	// First attempt - should succeed in delivery but we'll simulate a failure
	// in OnComplete by using a custom event
	event := &testEventWithFailingComplete{raw: raw}
	err = r.ProcessEvent(context.Background(), event)
	if err == nil || !strings.Contains(err.Error(), "failed to complete event") {
		t.Fatalf("ProcessEvent() error = %v, want a complete failure", err)
	}
	listed, err := emailStore.ListEmails(context.Background(), "alice@example.com", "inbox")
	if err != nil || len(listed) != 1 {
		t.Fatalf("emails after first attempt = %v, %v; want one delivery", listed, err)
	}

	// Second attempt - should not redeliver because dedup markers are recorded
	event2 := &fakeS3Event{raw: raw}
	if err := r.ProcessEvent(context.Background(), event2); err != nil {
		t.Fatalf("ProcessEvent() retry error = %v", err)
	}
	listed, err = emailStore.ListEmails(context.Background(), "alice@example.com", "inbox")
	if err != nil || len(listed) != 1 {
		t.Fatalf("emails after retry = %v, %v; want one delivery, not a duplicate", listed, err)
	}
}

// testEventWithFailingComplete is a test event that fails OnComplete.
type testEventWithFailingComplete struct {
	raw []byte
}

func (e *testEventWithFailingComplete) RawEmail(_ context.Context) ([]byte, error) {
	return e.raw, nil
}

func (e *testEventWithFailingComplete) OnComplete(_ context.Context) error {
	return errors.New("complete failed")
}

func (e *testEventWithFailingComplete) SESMessageID() string {
	return ""
}

func TestProcessMessageRetryOnlyReprocessesFailedRecipients(t *testing.T) {
	raw := []byte(receivedHeader("alice@example.com", "bob@example.com") +
		"From: sender@example.com\r\nSubject: hi\r\n\r\nbody\r\n")
	emailStore := &failOnceStorage{
		Storage: storage.NewMemoryStorage(),
		failFor: map[string]bool{"alice@example.com": true},
	}
	r, err := newRetryReceiver(emailStore, sender.NewInMemoryRecording(), NewMemoryDedup())
	if err != nil {
		t.Fatalf("NewReceiver() error = %v", err)
	}

	event := &fakeS3Event{raw: raw}
	if err := r.ProcessEvent(context.Background(), event); err == nil {
		t.Fatal("ProcessEvent() error = nil, want the alice delivery failure")
	}
	if event.completed {
		t.Error("OnComplete was called despite a failed recipient")
	}
	bob, err := emailStore.ListEmails(context.Background(), "bob@example.com", "inbox")
	if err != nil || len(bob) != 1 {
		t.Fatalf("bob's emails = %v, %v; want one delivery", bob, err)
	}

	delete(emailStore.failFor, "alice@example.com")
	event2 := &fakeS3Event{raw: raw}
	if err := r.ProcessEvent(context.Background(), event2); err != nil {
		t.Fatalf("ProcessEvent() retry error = %v", err)
	}
	for _, account := range []string{"alice@example.com", "bob@example.com"} {
		listed, err := emailStore.ListEmails(context.Background(), account, "inbox")
		if err != nil || len(listed) != 1 {
			t.Errorf("emails for %s = %v, %v; want exactly one delivery", account, listed, err)
		}
	}
}

func TestProcessMessageDedupLookupFailureKeepsStaging(t *testing.T) {
	raw := messageTo("alice@example.com")
	dedup := newErroringDedup()
	dedup.seenErr = errors.New("dedup store unavailable")
	emailStore := storage.NewMemoryStorage()
	r, err := newRetryReceiver(emailStore, sender.NewInMemoryRecording(), dedup)
	if err != nil {
		t.Fatalf("NewReceiver() error = %v", err)
	}

	event := &fakeS3Event{raw: raw}
	err = r.ProcessEvent(context.Background(), event)
	if err == nil || !strings.Contains(err.Error(), "failed to check dedup state") {
		t.Fatalf("ProcessEvent() error = %v, want a dedup lookup failure", err)
	}
	if event.completed {
		t.Error("OnComplete was called despite a dedup lookup failure")
	}
	listed, err := emailStore.ListEmails(context.Background(), "alice@example.com", "inbox")
	if err != nil || len(listed) != 0 {
		t.Errorf("emails = %v, %v; want no delivery", listed, err)
	}
}

func TestProcessMessageDedupRecordFailureKeepsStaging(t *testing.T) {
	raw := messageTo("alice@example.com")
	dedup := newErroringDedup()
	dedup.recordErr = errors.New("dedup store unavailable")
	r, err := newRetryReceiver(storage.NewMemoryStorage(), sender.NewInMemoryRecording(), dedup)
	if err != nil {
		t.Fatalf("NewReceiver() error = %v", err)
	}

	event := &fakeS3Event{raw: raw}
	err = r.ProcessEvent(context.Background(), event)
	if err == nil || !strings.Contains(err.Error(), "failed to record dedup state") {
		t.Fatalf("ProcessEvent() error = %v, want a dedup write failure", err)
	}
	if event.completed {
		t.Error("OnComplete was called despite a dedup write failure")
	}
}
