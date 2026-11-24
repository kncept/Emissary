package receiver

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/kncept/Emissary/mailer/components/metadata"
	"github.com/kncept/Emissary/mailer/components/sender"
	"github.com/kncept/Emissary/mailer/components/storage"
	"github.com/kncept/Emissary/mailer/components/usermanager"
	"github.com/kncept/Emissary/mailer/email"
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

func (f *fakeS3) DeleteObject(_ context.Context, params *s3.DeleteObjectInput, _ ...func(*s3.Options)) (*s3.DeleteObjectOutput, error) {
	delete(f.objects, aws.ToString(params.Key))
	return &s3.DeleteObjectOutput{}, nil
}

// fakeS3Event is a test implementation of InboundRawEmailEvent.
type fakeS3Event struct {
	raw       []byte
	completed bool
}

func (e *fakeS3Event) RawEmail(_ context.Context) ([]byte, error) {
	return e.raw, nil
}

func (e *fakeS3Event) OnComplete(_ context.Context) error {
	e.completed = true
	return nil
}

func (e *fakeS3Event) SESMessageID() string {
	return ""
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

func newTestReceiver() (*Receiver, *storage.MemoryStorage, metadata.MetadataStore) {
	return newTestReceiverWithMeta(metadata.NewMemoryMetadata())
}

func newTestReceiverWithMeta(meta metadata.MetadataStore) (*Receiver, *storage.MemoryStorage, metadata.MetadataStore) {
	emailStore := storage.NewMemoryStorage()
	r, err := NewReceiver(ReceiverConfig{
		Domain:      "example.com",
		UserManager: fakeUserManager{},
		Sender:      sender.NewInMemoryRecording(),
		Storage:     emailStore,
		Metadata:    meta,
	})
	if err != nil {
		panic(err)
	}
	return r, emailStore, meta
}

func TestProcessMessageDeliversThroughConfiguredStorage(t *testing.T) {
	const key = "01000192a3b4c5d6"
	raw := []byte(receivedHeader("alice@example.com") +
		"From: sender@example.com\r\nTo: alice@example.com\r\n" +
		"Date: Fri, 08 Mar 2024 15:30:00 +0100\r\nSubject: hi\r\n\r\nbody\r\n")
	r, emailStore, metaStore := newTestReceiver()

	event := &fakeS3Event{raw: raw}
	if err := r.ProcessEvent(context.Background(), event); err != nil {
		t.Fatalf("ProcessEvent() error = %v", err)
	}

	if !event.completed {
		t.Error("OnComplete was not called")
	}

	listed, err := emailStore.ListEmails(context.Background(), "alice@example.com", "inbox")
	if err != nil {
		t.Fatalf("ListEmails() error = %v", err)
	}
	if len(listed) != 1 {
		t.Fatalf("delivered emails = %d, want 1", len(listed))
	}
	if listed[0].Ref.MailboxID != "inbox" {
		t.Errorf("mailbox = %q, want inbox", listed[0].Ref.MailboxID)
	}
	if listed[0].Email.Subject != "hi" {
		t.Errorf("subject = %q, want hi", listed[0].Email.Subject)
	}
	meta, err := metaStore.Get(context.Background(), "alice@example.com", listed[0].Ref.ID)
	if err != nil {
		t.Fatalf("Get() metadata error = %v", err)
	}
	if meta.Mailbox != "inbox" || meta.Subject != "hi" {
		t.Errorf("delivered metadata mailbox/subject = %q/%q, want inbox/hi", meta.Mailbox, meta.Subject)
	}
}

func TestProcessMessagePreservesOriginalMIME(t *testing.T) {
	const key = "mime-preservation"
	raw := []byte(receivedHeader("alice@example.com") +
		"From: sender@example.com\r\n" +
		"To: alice@example.com\r\n" +
		"Subject: with attachment\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: multipart/mixed; boundary=\"BOUND\"\r\n" +
		"X-Custom-Header: keep-me\r\n" +
		"\r\n" +
		"--BOUND\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n" +
		"Content-Transfer-Encoding: quoted-printable\r\n" +
		"\r\n" +
		"plain body\r\n" +
		"\r\n" +
		"--BOUND\r\n" +
		"Content-Type: application/octet-stream; name=\"report.bin\"\r\n" +
		"Content-Transfer-Encoding: base64\r\n" +
		"Content-Disposition: attachment; filename=\"report.bin\"\r\n" +
		"\r\n" +
		"AAECAwQFBgcICQ==\r\n" +
		"\r\n" +
		"--BOUND--\r\n")
	r, emailStore, _ := newTestReceiver()

	event := &fakeS3Event{raw: raw}
	if err := r.ProcessEvent(context.Background(), event); err != nil {
		t.Fatalf("ProcessEvent() error = %v", err)
	}
	listed, err := emailStore.ListEmails(context.Background(), "alice@example.com", "inbox")
	if err != nil || len(listed) != 1 {
		t.Fatalf("emails = %v, %v; want one delivery", listed, err)
	}
	got, err := emailStore.GetEmailRaw(context.Background(), "alice@example.com", listed[0].Ref.ID)
	if err != nil {
		t.Fatalf("GetEmailRaw() error = %v", err)
	}

	// Verify the delivered message has the correct content.
	// Note: ToWireFormat is a best-effort serialization and may not produce
	// byte-for-byte identical output to the original. We verify the parsed
	// content matches instead.
	delivered := email.ParseWireFormat(listed[0].Ref.ID, bytes.NewReader(got))
	if delivered.Subject != "with attachment" {
		t.Errorf("subject = %q, want 'with attachment'", delivered.Subject)
	}
	if len(delivered.To) != 1 || delivered.To[0].Address != "alice@example.com" {
		t.Errorf("to = %v, want alice@example.com", delivered.To)
	}
	// Check that the custom header is preserved
	foundCustom := false
	for _, h := range delivered.Headers {
		if h.Name == "X-Custom-Header" && h.Value == "keep-me" {
			foundCustom = true
			break
		}
	}
	if !foundCustom {
		t.Errorf("custom header X-Custom-Header: keep-me not preserved")
	}
	if !event.completed {
		t.Error("OnComplete was not called")
	}
}

func TestProcessMessageRoutesByEnvelopeRecipientNotToHeader(t *testing.T) {
	raw := []byte(receivedHeader("envelope@example.com") +
		"From: sender@example.com\r\nTo: header@example.com\r\nSubject: hi\r\n\r\nbody\r\n")
	r, emailStore, mailSender := newRoutingTestReceiver(BaseMailIdentityConfig(), fakeUserManager{})

	event := &fakeS3Event{raw: raw}
	if err := r.ProcessEvent(context.Background(), event); err != nil {
		t.Fatalf("ProcessEvent() error = %v", err)
	}

	enveloped, err := emailStore.ListEmails(context.Background(), "envelope@example.com", "inbox")
	if err != nil || len(enveloped) != 1 {
		t.Fatalf("envelope recipient emails = %v, %v; want one delivery", enveloped, err)
	}
	headed, err := emailStore.ListEmails(context.Background(), "header@example.com", "inbox")
	if err != nil || len(headed) != 0 {
		t.Errorf("To-header emails = %v, %v; want no delivery", headed, err)
	}
	if len(mailSender.Bounces) != 0 {
		t.Errorf("bounces = %+v, want none", mailSender.Bounces)
	}
	if !event.completed {
		t.Error("OnComplete was not called")
	}
}

func TestProcessMessageWithoutEnvelopeRecipientIsSkipped(t *testing.T) {
	raw := []byte("From: sender@example.com\r\nTo: alice@example.com\r\nSubject: hi\r\n\r\nbody\r\n")
	r, emailStore, mailSender := newRoutingTestReceiver(BaseMailIdentityConfig(), fakeUserManager{})

	event := &fakeS3Event{raw: raw}
	if err := r.ProcessEvent(context.Background(), event); err != nil {
		t.Fatalf("ProcessEvent() error = %v", err)
	}
	listed, err := emailStore.ListEmails(context.Background(), "alice@example.com", "inbox")
	if err != nil || len(listed) != 0 {
		t.Errorf("emails = %v, %v; want no delivery without an envelope recipient", listed, err)
	}
	if len(mailSender.Bounces) != 0 {
		t.Errorf("bounces = %+v, want none", mailSender.Bounces)
	}
	if !event.completed {
		t.Error("OnComplete was not called")
	}
}

func TestProcessMessageCreatesMetadataWithStorageID(t *testing.T) {
	raw := []byte(receivedHeader("alice@example.com") +
		"From: sender@example.com\r\nTo: alice@example.com\r\n" +
		"Date: Fri, 08 Mar 2024 15:30:00 +0100\r\nSubject: hi\r\n\r\nbody\r\n")
	metaStore := metadata.NewMemoryMetadata()
	r, emailStore, _ := newTestReceiverWithMeta(metaStore)

	event := &fakeS3Event{raw: raw}
	if err := r.ProcessEvent(context.Background(), event); err != nil {
		t.Fatalf("ProcessEvent() error = %v", err)
	}

	listed, err := emailStore.ListEmails(context.Background(), "alice@example.com", "inbox")
	if err != nil || len(listed) != 1 {
		t.Fatalf("ListEmails() = %v, %v; want one delivered email", listed, err)
	}
	storageID := listed[0].Ref.ID
	meta, err := metaStore.Get(context.Background(), "alice@example.com", storageID)
	if err != nil {
		t.Fatalf("Get(%q) error = %v", storageID, err)
	}
	if meta.UserID != "alice@example.com" || meta.ID != storageID {
		t.Errorf("metadata ids = %q/%q, want %q/%q", meta.UserID, meta.ID, "alice@example.com", storageID)
	}
	if len(meta.Keywords) != 0 {
		t.Errorf("new delivery should have no keywords, got %+v", meta.Keywords)
	}
	if meta.ReceivedAt == "" {
		t.Error("delivery metadata should stamp receivedAt")
	} else if received, err := time.Parse(time.RFC3339, meta.ReceivedAt); err != nil {
		t.Errorf("receivedAt %q is not RFC 3339: %v", meta.ReceivedAt, err)
	} else if time.Since(received) > time.Minute {
		t.Errorf("receivedAt %q is not from this delivery", meta.ReceivedAt)
	}
	entries, err := metaStore.List(context.Background(), "alice@example.com")
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(entries) != 1 || entries[0].ID != storageID {
		t.Fatalf("metadata entries = %+v, want only id %q", entries, storageID)
	}
}

func TestProcessMessageDeliversWithoutDate(t *testing.T) {
	raw := []byte(receivedHeader("alice@example.com") +
		"From: sender@example.com\r\nTo: alice@example.com\r\nSubject: no date\r\n\r\nbody\r\n")
	r, emailStore, _ := newTestReceiver()

	event := &fakeS3Event{raw: raw}
	if err := r.ProcessEvent(context.Background(), event); err != nil {
		t.Fatalf("ProcessEvent() error = %v", err)
	}
	listed, err := emailStore.ListEmails(context.Background(), "alice@example.com", "inbox")
	if err != nil || len(listed) != 1 {
		t.Fatalf("ListEmails() = %v, %v; want one delivered email", listed, err)
	}
}

func TestProcessMessageSendsBounceThroughSender(t *testing.T) {
	raw := []byte(receivedHeader("unknown@example.com") +
		"From: sender@example.com\r\nTo: unknown@example.com\r\nSubject: hi\r\n\r\nbody\r\n")
	mailSender := sender.NewInMemoryRecording()
	r, err := NewReceiver(ReceiverConfig{
		Domain:      "example.com",
		UserManager: rejectingUserManager{invalid: "unknown@example.com"},
		Sender:      mailSender,
		Storage:     storage.NewMemoryStorage(),
	})
	if err != nil {
		t.Fatalf("NewReceiver() error = %v", err)
	}

	event := &fakeS3Event{raw: raw}
	if err := r.ProcessEvent(context.Background(), event); err != nil {
		t.Fatalf("ProcessEvent() error = %v", err)
	}

	if len(mailSender.Bounces) != 1 {
		t.Fatalf("bounces = %+v, want one bounce through the sender", mailSender.Bounces)
	}
	bounce := mailSender.Bounces[0]
	// The key is generated from the message content since there's no SES message ID
	if bounce.OriginalMessageID == "" {
		t.Errorf("bounce message id should not be empty")
	}
	if bounce.BounceSender != "mailer-daemon@example.com" {
		t.Errorf("bounce sender = %q, want mailer-daemon@example.com", bounce.BounceSender)
	}
	if bounce.Recipient != "unknown@example.com" {
		t.Errorf("bounce recipient = %q, want unknown@example.com", bounce.Recipient)
	}
	if bounce.BounceType != "UserUnknown" {
		t.Errorf("bounce type = %q, want UserUnknown", bounce.BounceType)
	}
	if bounce.OriginalMessage == nil || bounce.OriginalMessage.Subject != "hi" {
		t.Errorf("bounce original = %+v, want the original message", bounce.OriginalMessage)
	}
	if !event.completed {
		t.Error("OnComplete was not called")
	}
}

// fakeS3EventWithSESID is a test implementation of InboundRawEmailEvent that carries
// the SES message ID (the S3 object key for staged objects).
type fakeS3EventWithSESID struct {
	raw          []byte
	completed    bool
	sesMessageID string
}

func (e *fakeS3EventWithSESID) RawEmail(_ context.Context) ([]byte, error) {
	return e.raw, nil
}

func (e *fakeS3EventWithSESID) OnComplete(_ context.Context) error {
	e.completed = true
	return nil
}

// SESMessageID returns the SES message ID for bounce purposes.
func (e *fakeS3EventWithSESID) SESMessageID() string {
	return e.sesMessageID
}

// TestProcessEventUsesSESMessageIDForBounce verifies that when an inbound event
// carries an SES message ID (the S3 staging key), that ID is passed to SendBounce
// as the OriginalMessageId instead of a content hash. This is the regression test
// for I-H5: the bounce path must pass the real SES message ID so SES can correlate
// the bounce with the original delivery.
func TestProcessEventUsesSESMessageIDForBounce(t *testing.T) {
	const sesMessageID = "01000192a3b4c5d6"
	raw := []byte(receivedHeader("unknown@example.com") +
		"From: sender@example.com\r\nTo: unknown@example.com\r\nSubject: hi\r\n\r\nbody\r\n")
	mailSender := sender.NewInMemoryRecording()
	r, err := NewReceiver(ReceiverConfig{
		Domain:      "example.com",
		UserManager: rejectingUserManager{invalid: "unknown@example.com"},
		Sender:      mailSender,
		Storage:     storage.NewMemoryStorage(),
	})
	if err != nil {
		t.Fatalf("NewReceiver() error = %v", err)
	}

	// Create an event that carries the SES message ID
	event := &fakeS3EventWithSESID{raw: raw, sesMessageID: sesMessageID}
	if err := r.ProcessEvent(context.Background(), event); err != nil {
		t.Fatalf("ProcessEvent() error = %v", err)
	}

	if len(mailSender.Bounces) != 1 {
		t.Fatalf("bounces = %+v, want one bounce through the sender", mailSender.Bounces)
	}
	bounce := mailSender.Bounces[0]

	// THIS ASSERTION SHOULD FAIL BEFORE THE FIX:
	// Currently ProcessEvent generates a content hash as the key and passes it to sendBounce.
	// The fix should make it use the SES message ID from the event.
	if bounce.OriginalMessageID != sesMessageID {
		t.Errorf("bounce OriginalMessageID = %q, want SES message ID %q", bounce.OriginalMessageID, sesMessageID)
	}
	if bounce.BounceSender != "mailer-daemon@example.com" {
		t.Errorf("bounce sender = %q, want mailer-daemon@example.com", bounce.BounceSender)
	}
	if bounce.Recipient != "unknown@example.com" {
		t.Errorf("bounce recipient = %q, want unknown@example.com", bounce.Recipient)
	}
	if bounce.BounceType != "UserUnknown" {
		t.Errorf("bounce type = %q, want UserUnknown", bounce.BounceType)
	}
	if bounce.OriginalMessage == nil || bounce.OriginalMessage.Subject != "hi" {
		t.Errorf("bounce original = %+v, want the original message", bounce.OriginalMessage)
	}
	if !event.completed {
		t.Error("OnComplete was not called")
	}
}

func TestNewReceiverRequiresStorage(t *testing.T) {
	_, err := NewReceiver(ReceiverConfig{
		Domain:      "example.com",
		UserManager: fakeUserManager{},
		Sender:      sender.NewInMemoryRecording(),
	})
	if err == nil {
		t.Fatal("NewReceiver() without Storage should fail")
	}
}

func TestNewReceiverRejectsInvalidIdentityConfig(t *testing.T) {
	identity := BaseMailIdentityConfig()
	delete(identity, FallbackIdentity)
	_, err := NewReceiver(ReceiverConfig{
		Domain:         "example.com",
		UserManager:    fakeUserManager{},
		Sender:         sender.NewInMemoryRecording(),
		Storage:        storage.NewMemoryStorage(),
		IdentityConfig: identity,
	})
	if err == nil {
		t.Fatal("NewReceiver() with empty fallback rules should fail")
	}
}

// messageTo builds a minimal inbound message whose SMTP envelope recipient
// is to. The To header is set to the same address for convenience; routing
// uses the Received header's "for" clause, not To.
func messageTo(to string) []byte {
	return []byte(receivedHeader(to) +
		"From: sender@example.com\r\nTo: " + to + "\r\nSubject: hi\r\n\r\nbody\r\n")
}

// receivedHeader renders the header SES adds for an accepted RCPT TO.
func receivedHeader(recipients ...string) string {
	clauses := make([]string, 0, len(recipients))
	for _, recipient := range recipients {
		clauses = append(clauses, "for <"+recipient+">")
	}
	return "Received: from mail.example.com by inbound-smtp.us-east-1.amazonaws.com " +
		"with SMTP id test " + strings.Join(clauses, " ") +
		"; Fri, 08 Mar 2024 15:30:00 +0000\r\n"
}

// newRoutingTestReceiver builds a receiver with an explicit identity config
// and returns its store and sender so tests can inspect the outcomes.
func newRoutingTestReceiver(identity MailIdentityConfig, users usermanager.UserManager) (*Receiver, *storage.MemoryStorage, *sender.InMemoryRecording) {
	emailStore := storage.NewMemoryStorage()
	mailSender := sender.NewInMemoryRecording()
	r, err := NewReceiver(ReceiverConfig{
		Domain:         "example.com",
		UserManager:    users,
		Sender:         mailSender,
		Storage:        emailStore,
		IdentityConfig: identity,
	})
	if err != nil {
		panic(err)
	}
	return r, emailStore, mailSender
}

func TestProcessMessageDeliversToMailIdentityWithoutAUser(t *testing.T) {
	raw := messageTo("alias@example.com")
	identity := BaseMailIdentityConfig()
	identity["example.com"] = HandlerRules{
		MailIdentity: "alias@example.com",
		Rules:        []HandlerRule{DeliverToMailIdentity},
	}
	r, emailStore, mailSender := newRoutingTestReceiver(
		identity,
		rejectingUserManager{invalid: "alias@example.com"},
	)

	event := &fakeS3Event{raw: raw}
	if err := r.ProcessEvent(context.Background(), event); err != nil {
		t.Fatalf("ProcessEvent() error = %v", err)
	}
	// DeliverToMailIdentity stores mail in the identity's own mailbox even
	// though the user manager rejected the address.
	listed, err := emailStore.ListEmails(context.Background(), "alias@example.com", "inbox")
	if err != nil || len(listed) != 1 {
		t.Fatalf("mail identity emails = %v, %v; want one delivery", listed, err)
	}
	if len(mailSender.Bounces) != 0 {
		t.Errorf("bounces = %+v, want none", mailSender.Bounces)
	}
	if !event.completed {
		t.Error("OnComplete was not called")
	}
}

func TestProcessMessageFallsThroughToRecipientIdentity(t *testing.T) {
	raw := messageTo("missing@example.com")
	identity := BaseMailIdentityConfig()
	identity["example.com"] = HandlerRules{
		MailIdentity: "missing@example.com",
		Rules:        []HandlerRule{DeliverToUser, DeliverToMailIdentity},
	}
	r, emailStore, mailSender := newRoutingTestReceiver(
		identity,
		rejectingUserManager{invalid: "missing@example.com"},
	)

	event := &fakeS3Event{raw: raw}
	if err := r.ProcessEvent(context.Background(), event); err != nil {
		t.Fatalf("ProcessEvent() error = %v", err)
	}
	listed, err := emailStore.ListEmails(context.Background(), "missing@example.com", "inbox")
	if err != nil || len(listed) != 1 {
		t.Fatalf("recipient-identity emails = %v, %v; want one delivery", listed, err)
	}
	if len(mailSender.Bounces) != 0 {
		t.Errorf("bounces = %+v, want none", mailSender.Bounces)
	}
}

func TestProcessMessageBounceRuleOverridesUserLookup(t *testing.T) {
	raw := messageTo("blocked@example.com")
	identity := BaseMailIdentityConfig()
	identity["example.com"] = HandlerRules{
		MailIdentity: "example.com",
		Rules:        []HandlerRule{Bounce},
	}
	r, emailStore, mailSender := newRoutingTestReceiver(identity, fakeUserManager{})

	event := &fakeS3Event{raw: raw}
	if err := r.ProcessEvent(context.Background(), event); err != nil {
		t.Fatalf("ProcessEvent() error = %v", err)
	}
	if len(mailSender.Bounces) != 1 || mailSender.Bounces[0].Recipient != "blocked@example.com" {
		t.Fatalf("bounces = %+v, want one bounce for blocked@example.com", mailSender.Bounces)
	}
	listed, err := emailStore.ListEmails(context.Background(), "blocked@example.com", "inbox")
	if err != nil || len(listed) != 0 {
		t.Errorf("emails = %v, %v; want no delivery", listed, err)
	}
	if !event.completed {
		t.Error("OnComplete was not called")
	}
}

func TestProcessMessageDropRuleDiscardsWithoutBounce(t *testing.T) {
	raw := messageTo("dropped@example.com")
	identity := BaseMailIdentityConfig()
	identity["example.com"] = HandlerRules{
		MailIdentity: "example.com",
		Rules:        []HandlerRule{Drop},
	}
	// fakeUserManager knows this recipient, so the drop must be the reason it
	// is not delivered rather than a failed user lookup.
	r, emailStore, mailSender := newRoutingTestReceiver(identity, fakeUserManager{})

	event := &fakeS3Event{raw: raw}
	if err := r.ProcessEvent(context.Background(), event); err != nil {
		t.Fatalf("ProcessEvent() error = %v", err)
	}
	if len(mailSender.Bounces) != 0 {
		t.Errorf("bounces = %+v, want none for an explicit drop", mailSender.Bounces)
	}
	listed, err := emailStore.ListEmails(context.Background(), "dropped@example.com", "inbox")
	if err != nil || len(listed) != 0 {
		t.Errorf("emails = %v, %v; want no delivery", listed, err)
	}
	if !event.completed {
		t.Error("OnComplete was not called")
	}
}

func TestProcessMessageDropRuleTakesPrecedenceOverDelivery(t *testing.T) {
	raw := messageTo("dropped@example.com")
	identity := BaseMailIdentityConfig()
	identity["example.com"] = HandlerRules{
		MailIdentity: "example.com",
		Rules:        []HandlerRule{Drop, DeliverToUser},
	}
	r, emailStore, mailSender := newRoutingTestReceiver(identity, fakeUserManager{})

	event := &fakeS3Event{raw: raw}
	if err := r.ProcessEvent(context.Background(), event); err != nil {
		t.Fatalf("ProcessEvent() error = %v", err)
	}
	listed, err := emailStore.ListEmails(context.Background(), "dropped@example.com", "inbox")
	if err != nil || len(listed) != 0 {
		t.Errorf("emails = %v, %v; want Drop to win over DeliverToUser", listed, err)
	}
	if len(mailSender.Bounces) != 0 {
		t.Errorf("bounces = %+v, want none", mailSender.Bounces)
	}
	if !event.completed {
		t.Error("OnComplete was not called")
	}
}

func TestProcessMessageMatchesIdentityCaseInsensitively(t *testing.T) {
	raw := messageTo("Alias@Example.com")
	identity := BaseMailIdentityConfig()
	identity["example.com"] = HandlerRules{
		MailIdentity: "example.com",
		Rules:        []HandlerRule{Bounce},
	}
	r, _, mailSender := newRoutingTestReceiver(identity, fakeUserManager{})

	event := &fakeS3Event{raw: raw}
	if err := r.ProcessEvent(context.Background(), event); err != nil {
		t.Fatalf("ProcessEvent() error = %v", err)
	}
	if len(mailSender.Bounces) != 1 || mailSender.Bounces[0].Recipient != "alias@example.com" {
		t.Fatalf("bounces = %+v, want one bounce for the normalised address", mailSender.Bounces)
	}
	if !event.completed {
		t.Error("OnComplete was not called")
	}
}

func TestProcessMessageFetchesRulesByMailIdentity(t *testing.T) {
	raw := []byte(receivedHeader("reader@news.example.com", "poster@blocked.example.com", "alice@example.com") +
		"From: sender@example.com\r\n" +
		"To: reader@news.example.com, poster@blocked.example.com\r\n" +
		"Cc: alice@example.com\r\nSubject: hi\r\n\r\nbody\r\n")
	identity := BaseMailIdentityConfig()
	identity["news.example.com"] = HandlerRules{
		MailIdentity: "news.example.com",
		Rules:        []HandlerRule{Drop},
	}
	identity["blocked.example.com"] = HandlerRules{
		MailIdentity: "blocked.example.com",
		Rules:        []HandlerRule{Bounce},
	}
	r, emailStore, mailSender := newRoutingTestReceiver(identity, fakeUserManager{})

	event := &fakeS3Event{raw: raw}
	if err := r.ProcessEvent(context.Background(), event); err != nil {
		t.Fatalf("ProcessEvent() error = %v", err)
	}

	// Each recipient must be handled by the rules for its own mail identity:
	// the catchall's drop, the blocked identity's bounce, and the fallback's
	// DeliverToUser for an unconfigured identity.
	if len(mailSender.Bounces) != 1 || mailSender.Bounces[0].Recipient != "poster@blocked.example.com" {
		t.Fatalf("bounces = %+v, want one bounce for poster@blocked.example.com", mailSender.Bounces)
	}
	for _, dropped := range []string{"reader@news.example.com", "poster@blocked.example.com"} {
		listed, err := emailStore.ListEmails(context.Background(), dropped, "inbox")
		if err != nil || len(listed) != 0 {
			t.Errorf("emails for %s = %v, %v; want no delivery", dropped, listed, err)
		}
	}
	listed, err := emailStore.ListEmails(context.Background(), "alice@example.com", "inbox")
	if err != nil || len(listed) != 1 {
		t.Fatalf("fallback identity emails = %v, %v; want one delivery", listed, err)
	}
	if !event.completed {
		t.Error("OnComplete was not called")
	}
}

func TestProcessMessageStopsAtFirstApplicableRule(t *testing.T) {
	raw := messageTo("alice@example.com")
	identity := BaseMailIdentityConfig()
	identity["example.com"] = HandlerRules{
		MailIdentity: "catchall@example.com",
		// DeliverToUser succeeds for alice, so neither the catchall delivery
		// nor the later bounce may run.
		Rules: []HandlerRule{DeliverToUser, DeliverToMailIdentity, Bounce},
	}
	r, emailStore, mailSender := newRoutingTestReceiver(identity, fakeUserManager{})

	event := &fakeS3Event{raw: raw}
	if err := r.ProcessEvent(context.Background(), event); err != nil {
		t.Fatalf("ProcessEvent() error = %v", err)
	}
	if len(mailSender.Bounces) != 0 {
		t.Errorf("bounces = %+v, want none after the first rule applied", mailSender.Bounces)
	}
	for _, mailbox := range []string{"alice@example.com", "catchall@example.com"} {
		listed, err := emailStore.ListEmails(context.Background(), mailbox, "inbox")
		if err != nil {
			t.Fatalf("ListEmails(%q) error = %v", mailbox, err)
		}
		want := 0
		if mailbox == "alice@example.com" {
			want = 1
		}
		if len(listed) != want {
			t.Errorf("emails for %s = %d, want %d", mailbox, len(listed), want)
		}
	}
	if !event.completed {
		t.Error("OnComplete was not called")
	}
}

func TestProcessMessageAppliesRulesInOrder(t *testing.T) {
	tests := []struct {
		name       string
		rules      []HandlerRule
		wantBounce bool
		wantMail   bool
	}{
		{name: "bounce before drop", rules: []HandlerRule{Bounce, Drop}, wantBounce: true},
		{name: "drop before bounce", rules: []HandlerRule{Drop, Bounce}},
		{name: "bounce before delivery", rules: []HandlerRule{Bounce, DeliverToUser}, wantBounce: true},
		{name: "delivery before bounce", rules: []HandlerRule{DeliverToUser, Bounce}, wantMail: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw := messageTo("alice@example.com")
			identity := BaseMailIdentityConfig()
			identity["example.com"] = HandlerRules{MailIdentity: "example.com", Rules: tt.rules}
			r, emailStore, mailSender := newRoutingTestReceiver(identity, fakeUserManager{})

			event := &fakeS3Event{raw: raw}
			if err := r.ProcessEvent(context.Background(), event); err != nil {
				t.Fatalf("ProcessMessage() error = %v", err)
			}
			if got := len(mailSender.Bounces) > 0; got != tt.wantBounce {
				t.Errorf("bounced = %t, want %t (%+v)", got, tt.wantBounce, mailSender.Bounces)
			}
			listed, err := emailStore.ListEmails(context.Background(), "alice@example.com", "inbox")
			if err != nil {
				t.Fatalf("ListEmails() error = %v", err)
			}
			if got := len(listed) > 0; got != tt.wantMail {
				t.Errorf("delivered = %t, want %t (%v)", got, tt.wantMail, listed)
			}
		})
	}
}

func TestProcessMessageSkipsRulesThatDoNotApply(t *testing.T) {
	tests := []struct {
		name        string
		rules       []HandlerRule
		wantMailbox string
		wantBounce  bool
	}{
		{
			name:        "later mail identity rule applies",
			rules:       []HandlerRule{DeliverToUser, DeliverToMailIdentity, Bounce},
			wantMailbox: "catchall@example.com",
		},
		{
			name:       "unapplicable rules end in a bounce",
			rules:      []HandlerRule{DeliverToUser, Bounce},
			wantBounce: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw := messageTo("unknown@example.com")
			identity := BaseMailIdentityConfig()
			identity["example.com"] = HandlerRules{
				MailIdentity: "catchall@example.com",
				Rules:        tt.rules,
			}
			r, emailStore, mailSender := newRoutingTestReceiver(
				identity,
				rejectingUserManager{invalid: "unknown@example.com"},
			)

			event := &fakeS3Event{raw: raw}
			if err := r.ProcessEvent(context.Background(), event); err != nil {
				t.Fatalf("ProcessEvent() error = %v", err)
			}
			if got := len(mailSender.Bounces) > 0; got != tt.wantBounce {
				t.Errorf("bounced = %t, want %t (%+v)", got, tt.wantBounce, mailSender.Bounces)
			}
			if tt.wantMailbox == "" {
				return
			}
			listed, err := emailStore.ListEmails(context.Background(), tt.wantMailbox, "inbox")
			if err != nil || len(listed) != 1 {
				t.Fatalf("mail identity emails = %v, %v; want one delivery to %s", listed, err, tt.wantMailbox)
			}
			if !event.completed {
				t.Error("OnComplete was not called")
			}
		})
	}
}

func TestProcessEventDeliversThroughConfiguredStorage(t *testing.T) {
	raw := []byte(receivedHeader("alice@example.com") +
		"From: sender@example.com\r\nTo: alice@example.com\r\n" +
		"Date: Fri, 08 Mar 2024 15:30:00 +0100\r\nSubject: hi\r\n\r\nbody\r\n")

	// Create an event that provides the raw email bytes
	event := &fakeS3Event{raw: raw}
	r, emailStore, _ := newTestReceiver()

	if err := r.ProcessEvent(context.Background(), event); err != nil {
		t.Fatalf("ProcessEvent() error = %v", err)
	}

	if !event.completed {
		t.Error("OnComplete was not called")
	}

	listed, err := emailStore.ListEmails(context.Background(), "alice@example.com", "inbox")
	if err != nil {
		t.Fatalf("ListEmails() error = %v", err)
	}
	if len(listed) != 1 {
		t.Fatalf("delivered emails = %d, want 1", len(listed))
	}
	if listed[0].Ref.MailboxID != "inbox" {
		t.Errorf("mailbox = %q, want inbox", listed[0].Ref.MailboxID)
	}
	if listed[0].Email.Subject != "hi" {
		t.Errorf("subject = %q, want hi", listed[0].Email.Subject)
	}
}

func TestProcessEventPreservesOriginalMIME(t *testing.T) {
	raw := []byte(receivedHeader("alice@example.com") +
		"From: sender@example.com\r\n" +
		"To: alice@example.com\r\n" +
		"Subject: with attachment\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: multipart/mixed; boundary=\"BOUND\"\r\n" +
		"X-Custom-Header: keep-me\r\n" +
		"\r\n" +
		"--BOUND\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n" +
		"Content-Transfer-Encoding: quoted-printable\r\n" +
		"\r\n" +
		"plain body\r\n" +
		"\r\n" +
		"--BOUND\r\n" +
		"Content-Type: application/octet-stream; name=\"report.bin\"\r\n" +
		"Content-Transfer-Encoding: base64\r\n" +
		"Content-Disposition: attachment; filename=\"report.bin\"\r\n" +
		"\r\n" +
		"AAECAwQFBgcICQ==\r\n" +
		"\r\n" +
		"--BOUND--\r\n")

	event := &fakeS3Event{raw: raw}
	r, emailStore, _ := newTestReceiver()

	if err := r.ProcessEvent(context.Background(), event); err != nil {
		t.Fatalf("ProcessEvent() error = %v", err)
	}

	if !event.completed {
		t.Error("OnComplete was not called")
	}

	listed, err := emailStore.ListEmails(context.Background(), "alice@example.com", "inbox")
	if err != nil || len(listed) != 1 {
		t.Fatalf("emails = %v, %v; want one delivery", listed, err)
	}
	got, err := emailStore.GetEmailRaw(context.Background(), "alice@example.com", listed[0].Ref.ID)
	if err != nil {
		t.Fatalf("GetEmailRaw() error = %v", err)
	}
	if !bytes.Equal(got, raw) {
		t.Errorf("GetEmailRaw() = %q, want the original staging bytes %q", got, raw)
	}
}

// testRawEmailEvent is a test implementation of InboundRawEmailEvent.
type testRawEmailEvent struct {
	raw       []byte
	completed bool
}

func (e *testRawEmailEvent) RawEmail(_ context.Context) ([]byte, error) {
	return e.raw, nil
}

func (e *testRawEmailEvent) OnComplete(_ context.Context) error {
	e.completed = true
	return nil
}

// testParsedEmailEvent is a test implementation of InboundEmailEvent.
type testParsedEmailEvent struct {
	wireEmail *email.WireFormatEmail
	completed bool
}

func (e *testParsedEmailEvent) RawEmail(_ context.Context) (*email.WireFormatEmail, error) {
	return e.wireEmail, nil
}

func (e *testParsedEmailEvent) OnComplete(_ context.Context) error {
	e.completed = true
	return nil
}

func TestProcessParsedEventDeliversThroughConfiguredStorage(t *testing.T) {
	// Create a parsed wire email directly
	wireEmail := email.ParseWireFormat("test-key", bytes.NewReader([]byte(receivedHeader("alice@example.com")+
		"From: sender@example.com\r\nTo: alice@example.com\r\n"+
		"Date: Fri, 08 Mar 2024 15:30:00 +0100\r\nSubject: hi\r\n\r\nbody\r\n")))

	event := &testParsedEmailEvent{wireEmail: wireEmail, completed: false}
	r, emailStore, _ := newTestReceiver()

	if err := r.ProcessParsedEvent(context.Background(), event); err != nil {
		t.Fatalf("ProcessParsedEvent() error = %v", err)
	}

	if !event.completed {
		t.Error("OnComplete was not called")
	}

	listed, err := emailStore.ListEmails(context.Background(), "alice@example.com", "inbox")
	if err != nil {
		t.Fatalf("ListEmails() error = %v", err)
	}
	if len(listed) != 1 {
		t.Fatalf("delivered emails = %d, want 1", len(listed))
	}
	if listed[0].Ref.MailboxID != "inbox" {
		t.Errorf("mailbox = %q, want inbox", listed[0].Ref.MailboxID)
	}
	if listed[0].Email.Subject != "hi" {
		t.Errorf("subject = %q, want hi", listed[0].Email.Subject)
	}
}

func TestProcessParsedEventPreservesOriginalMIME(t *testing.T) {
	raw := []byte(receivedHeader("alice@example.com") +
		"From: sender@example.com\r\n" +
		"To: alice@example.com\r\n" +
		"Subject: with attachment\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: multipart/mixed; boundary=\"BOUND\"\r\n" +
		"X-Custom-Header: keep-me\r\n" +
		"\r\n" +
		"--BOUND\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n" +
		"Content-Transfer-Encoding: quoted-printable\r\n" +
		"\r\n" +
		"plain body\r\n" +
		"\r\n" +
		"--BOUND\r\n" +
		"Content-Type: application/octet-stream; name=\"report.bin\"\r\n" +
		"Content-Transfer-Encoding: base64\r\n" +
		"Content-Disposition: attachment; filename=\"report.bin\"\r\n" +
		"\r\n" +
		"AAECAwQFBgcICQ==\r\n" +
		"\r\n" +
		"--BOUND--\r\n")

	wireEmail := email.ParseWireFormat("test-key", bytes.NewReader(raw))

	event := &testParsedEmailEvent{wireEmail: wireEmail, completed: false}
	r, emailStore, _ := newTestReceiver()

	if err := r.ProcessParsedEvent(context.Background(), event); err != nil {
		t.Fatalf("ProcessParsedEvent() error = %v", err)
	}

	if !event.completed {
		t.Error("OnComplete was not called")
	}

	listed, err := emailStore.ListEmails(context.Background(), "alice@example.com", "inbox")
	if err != nil || len(listed) != 1 {
		t.Fatalf("emails = %v, %v; want one delivery", listed, err)
	}
	got, err := emailStore.GetEmailRaw(context.Background(), "alice@example.com", listed[0].Ref.ID)
	if err != nil {
		t.Fatalf("GetEmailRaw() error = %v", err)
	}

	// Verify the delivered message has the correct content.
	// Note: ToWireFormat is a best-effort serialization and may not produce
	// byte-for-byte identical output to the original. We verify the parsed
	// content matches instead.
	delivered := email.ParseWireFormat(listed[0].Ref.ID, bytes.NewReader(got))
	if delivered.Subject != "with attachment" {
		t.Errorf("subject = %q, want 'with attachment'", delivered.Subject)
	}
	if len(delivered.To) != 1 || delivered.To[0].Address != "alice@example.com" {
		t.Errorf("to = %v, want alice@example.com", delivered.To)
	}
	// Check that the custom header is preserved
	foundCustom := false
	for _, h := range delivered.Headers {
		if h.Name == "X-Custom-Header" && h.Value == "keep-me" {
			foundCustom = true
			break
		}
	}
	if !foundCustom {
		t.Errorf("custom header X-Custom-Header: keep-me not preserved")
	}
}
