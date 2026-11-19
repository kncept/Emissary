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
	"github.com/kncept/Emissary/mailer/email"
	"github.com/kncept/Emissary/mailer/metadata"
	"github.com/kncept/Emissary/mailer/storage"
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

func newTestReceiver(client S3Client) (*Receiver, *storage.MemoryStorage, metadata.MetadataStore) {
	return newTestReceiverWithMeta(client, metadata.NewMemoryMetadata())
}

func newTestReceiverWithMeta(client S3Client, meta metadata.MetadataStore) (*Receiver, *storage.MemoryStorage, metadata.MetadataStore) {
	emailStore := storage.NewMemoryStorage()
	r, err := NewReceiver(ReceiverConfig{
		Bucket:      "bucket",
		Domain:      "example.com",
		UserManager: fakeUserManager{},
		Sender:      &fakeSender{},
		SESClient:   client,
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
	raw := "From: sender@example.com\r\nTo: alice@example.com\r\n" +
		"Date: Fri, 08 Mar 2024 15:30:00 +0100\r\nSubject: hi\r\n\r\nbody\r\n"
	client := &fakeS3{objects: map[string][]byte{key: []byte(raw)}}
	r, emailStore, _ := newTestReceiver(client)

	if err := r.ProcessMessage(context.Background(), key); err != nil {
		t.Fatalf("ProcessMessage() error = %v", err)
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
	if _, ok := client.objects[key]; ok {
		t.Error("staging object was not deleted")
	}
}

func TestProcessMessageCreatesMetadataWithStorageID(t *testing.T) {
	const key = "01000192a3b4c5d8"
	raw := "From: sender@example.com\r\nTo: alice@example.com\r\n" +
		"Date: Fri, 08 Mar 2024 15:30:00 +0100\r\nSubject: hi\r\n\r\nbody\r\n"
	client := &fakeS3{objects: map[string][]byte{key: []byte(raw)}}
	metaStore := metadata.NewMemoryMetadata()
	r, emailStore, _ := newTestReceiverWithMeta(client, metaStore)

	if err := r.ProcessMessage(context.Background(), key); err != nil {
		t.Fatalf("ProcessMessage() error = %v", err)
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
	const key = "01000192abcdef"
	raw := "From: sender@example.com\r\nTo: alice@example.com\r\nSubject: no date\r\n\r\nbody\r\n"
	client := &fakeS3{objects: map[string][]byte{key: []byte(raw)}}
	r, emailStore, _ := newTestReceiver(client)

	if err := r.ProcessMessage(context.Background(), key); err != nil {
		t.Fatalf("ProcessMessage() error = %v", err)
	}
	listed, err := emailStore.ListEmails(context.Background(), "alice@example.com", "inbox")
	if err != nil || len(listed) != 1 {
		t.Fatalf("ListEmails() = %v, %v; want one delivered email", listed, err)
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
		Storage:     storage.NewMemoryStorage(),
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

func TestNewReceiverRequiresStorage(t *testing.T) {
	_, err := NewReceiver(ReceiverConfig{
		Bucket:      "bucket",
		Domain:      "example.com",
		UserManager: fakeUserManager{},
		Sender:      &fakeSender{},
		SESClient:   &fakeS3{objects: map[string][]byte{}},
	})
	if err == nil {
		t.Fatal("NewReceiver() without Storage should fail")
	}
}

func TestNewReceiverRejectsInvalidIdentityConfig(t *testing.T) {
	identity := BaseMailIdentityConfig()
	delete(identity, FallbackIdentity)
	_, err := NewReceiver(ReceiverConfig{
		Bucket:         "bucket",
		Domain:         "example.com",
		UserManager:    fakeUserManager{},
		Sender:         &fakeSender{},
		SESClient:      &fakeS3{objects: map[string][]byte{}},
		Storage:        storage.NewMemoryStorage(),
		IdentityConfig: identity,
	})
	if err == nil {
		t.Fatal("NewReceiver() with empty fallback rules should fail")
	}
}

// messageTo builds a minimal inbound message addressed to to.
func messageTo(to string) []byte {
	return []byte("From: sender@example.com\r\nTo: " + to + "\r\nSubject: hi\r\n\r\nbody\r\n")
}

// newRoutingTestReceiver builds a receiver with an explicit identity config
// and returns its store and sender so tests can inspect the outcomes.
func newRoutingTestReceiver(identity MailIdentityConfig, users usermanager.UserManager, client S3Client) (*Receiver, *storage.MemoryStorage, *fakeSender) {
	emailStore := storage.NewMemoryStorage()
	mailSender := &fakeSender{}
	r, err := NewReceiver(ReceiverConfig{
		Bucket:         "bucket",
		Domain:         "example.com",
		UserManager:    users,
		Sender:         mailSender,
		SESClient:      client,
		Storage:        emailStore,
		IdentityConfig: identity,
	})
	if err != nil {
		panic(err)
	}
	return r, emailStore, mailSender
}

func TestProcessMessageDeliversToMailIdentityWithoutAUser(t *testing.T) {
	const key = "routing-mail-identity"
	identity := BaseMailIdentityConfig()
	identity["alias@example.com"] = HandlerRules{
		MailIdentity: "alias@example.com",
		Rules:        []HandlerRule{DeliverToMailIdentity},
	}
	client := &fakeS3{objects: map[string][]byte{key: messageTo("alias@example.com")}}
	r, emailStore, mailSender := newRoutingTestReceiver(
		identity,
		rejectingUserManager{invalid: "alias@example.com"},
		client,
	)

	if err := r.ProcessMessage(context.Background(), key); err != nil {
		t.Fatalf("ProcessMessage() error = %v", err)
	}
	// DeliverToMailIdentity stores mail in the identity's own mailbox even
	// though the user manager rejected the address.
	listed, err := emailStore.ListEmails(context.Background(), "alias@example.com", "inbox")
	if err != nil || len(listed) != 1 {
		t.Fatalf("mail identity emails = %v, %v; want one delivery", listed, err)
	}
	if len(mailSender.bounces) != 0 {
		t.Errorf("bounces = %+v, want none", mailSender.bounces)
	}
}

func TestProcessMessageFallsThroughToRecipientIdentity(t *testing.T) {
	const key = "routing-fallthrough"
	identity := BaseMailIdentityConfig()
	identity["missing@example.com"] = HandlerRules{
		MailIdentity: "missing@example.com",
		Rules:        []HandlerRule{DeliverToUser, DeliverToMailIdentity},
	}
	client := &fakeS3{objects: map[string][]byte{key: messageTo("missing@example.com")}}
	r, emailStore, mailSender := newRoutingTestReceiver(
		identity,
		rejectingUserManager{invalid: "missing@example.com"},
		client,
	)

	if err := r.ProcessMessage(context.Background(), key); err != nil {
		t.Fatalf("ProcessMessage() error = %v", err)
	}
	listed, err := emailStore.ListEmails(context.Background(), "missing@example.com", "inbox")
	if err != nil || len(listed) != 1 {
		t.Fatalf("recipient-identity emails = %v, %v; want one delivery", listed, err)
	}
	if len(mailSender.bounces) != 0 {
		t.Errorf("bounces = %+v, want none", mailSender.bounces)
	}
}

func TestProcessMessageBounceRuleOverridesUserLookup(t *testing.T) {
	const key = "routing-bounce"
	identity := BaseMailIdentityConfig()
	identity["blocked@example.com"] = HandlerRules{
		MailIdentity: "blocked@example.com",
		Rules:        []HandlerRule{Bounce},
	}
	client := &fakeS3{objects: map[string][]byte{key: messageTo("blocked@example.com")}}
	r, emailStore, mailSender := newRoutingTestReceiver(identity, fakeUserManager{}, client)

	if err := r.ProcessMessage(context.Background(), key); err != nil {
		t.Fatalf("ProcessMessage() error = %v", err)
	}
	if len(mailSender.bounces) != 1 || mailSender.bounces[0].recipient != "blocked@example.com" {
		t.Fatalf("bounces = %+v, want one bounce for blocked@example.com", mailSender.bounces)
	}
	listed, err := emailStore.ListEmails(context.Background(), "blocked@example.com", "inbox")
	if err != nil || len(listed) != 0 {
		t.Errorf("emails = %v, %v; want no delivery", listed, err)
	}
}

func TestProcessMessageMatchesIdentityCaseInsensitively(t *testing.T) {
	const key = "routing-case"
	identity := BaseMailIdentityConfig()
	identity["alias@example.com"] = HandlerRules{
		MailIdentity: "alias@example.com",
		Rules:        []HandlerRule{Bounce},
	}
	client := &fakeS3{objects: map[string][]byte{key: messageTo("Alias@Example.com")}}
	r, _, mailSender := newRoutingTestReceiver(identity, fakeUserManager{}, client)

	if err := r.ProcessMessage(context.Background(), key); err != nil {
		t.Fatalf("ProcessMessage() error = %v", err)
	}
	if len(mailSender.bounces) != 1 || mailSender.bounces[0].recipient != "alias@example.com" {
		t.Fatalf("bounces = %+v, want one bounce for the normalised address", mailSender.bounces)
	}
}
