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
	"github.com/kncept/Emissary/mailer/metadata"
	"github.com/kncept/Emissary/mailer/sender"
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
		Sender:      sender.NewInMemoryRecording(),
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
	raw := receivedHeader("alice@example.com") +
		"From: sender@example.com\r\nTo: alice@example.com\r\n" +
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

func TestProcessMessageRoutesByEnvelopeRecipientNotToHeader(t *testing.T) {
	const key = "envelope-not-to"
	raw := receivedHeader("envelope@example.com") +
		"From: sender@example.com\r\nTo: header@example.com\r\nSubject: hi\r\n\r\nbody\r\n"
	client := &fakeS3{objects: map[string][]byte{key: []byte(raw)}}
	r, emailStore, mailSender := newRoutingTestReceiver(BaseMailIdentityConfig(), fakeUserManager{}, client)

	if err := r.ProcessMessage(context.Background(), key); err != nil {
		t.Fatalf("ProcessMessage() error = %v", err)
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
}

func TestProcessMessageWithoutEnvelopeRecipientIsSkipped(t *testing.T) {
	const key = "no-envelope"
	// A locally crafted message with only a To header has no RCPT TO record.
	raw := "From: sender@example.com\r\nTo: alice@example.com\r\nSubject: hi\r\n\r\nbody\r\n"
	client := &fakeS3{objects: map[string][]byte{key: []byte(raw)}}
	r, emailStore, mailSender := newRoutingTestReceiver(BaseMailIdentityConfig(), fakeUserManager{}, client)

	if err := r.ProcessMessage(context.Background(), key); err != nil {
		t.Fatalf("ProcessMessage() error = %v", err)
	}
	listed, err := emailStore.ListEmails(context.Background(), "alice@example.com", "inbox")
	if err != nil || len(listed) != 0 {
		t.Errorf("emails = %v, %v; want no delivery without an envelope recipient", listed, err)
	}
	if len(mailSender.Bounces) != 0 {
		t.Errorf("bounces = %+v, want none", mailSender.Bounces)
	}
	if _, ok := client.objects[key]; ok {
		t.Error("staging object was not deleted")
	}
}

func TestProcessMessageCreatesMetadataWithStorageID(t *testing.T) {
	const key = "01000192a3b4c5d8"
	raw := receivedHeader("alice@example.com") +
		"From: sender@example.com\r\nTo: alice@example.com\r\n" +
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
	raw := receivedHeader("alice@example.com") +
		"From: sender@example.com\r\nTo: alice@example.com\r\nSubject: no date\r\n\r\nbody\r\n"
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
	raw := receivedHeader("unknown@example.com") +
		"From: sender@example.com\r\nTo: unknown@example.com\r\nSubject: hi\r\n\r\nbody\r\n"
	client := &fakeS3{objects: map[string][]byte{key: []byte(raw)}}
	mailSender := sender.NewInMemoryRecording()
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

	if len(mailSender.Bounces) != 1 {
		t.Fatalf("bounces = %+v, want one bounce through the sender", mailSender.Bounces)
	}
	bounce := mailSender.Bounces[0]
	if bounce.OriginalMessageID != key {
		t.Errorf("bounce message id = %q, want %q", bounce.OriginalMessageID, key)
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
}

func TestNewReceiverRequiresStorage(t *testing.T) {
	_, err := NewReceiver(ReceiverConfig{
		Bucket:      "bucket",
		Domain:      "example.com",
		UserManager: fakeUserManager{},
		Sender:      sender.NewInMemoryRecording(),
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
		Sender:         sender.NewInMemoryRecording(),
		SESClient:      &fakeS3{objects: map[string][]byte{}},
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
func newRoutingTestReceiver(identity MailIdentityConfig, users usermanager.UserManager, client S3Client) (*Receiver, *storage.MemoryStorage, *sender.InMemoryRecording) {
	emailStore := storage.NewMemoryStorage()
	mailSender := sender.NewInMemoryRecording()
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
	identity["example.com"] = HandlerRules{
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
	if len(mailSender.Bounces) != 0 {
		t.Errorf("bounces = %+v, want none", mailSender.Bounces)
	}
}

func TestProcessMessageFallsThroughToRecipientIdentity(t *testing.T) {
	const key = "routing-fallthrough"
	identity := BaseMailIdentityConfig()
	identity["example.com"] = HandlerRules{
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
	if len(mailSender.Bounces) != 0 {
		t.Errorf("bounces = %+v, want none", mailSender.Bounces)
	}
}

func TestProcessMessageBounceRuleOverridesUserLookup(t *testing.T) {
	const key = "routing-bounce"
	identity := BaseMailIdentityConfig()
	identity["example.com"] = HandlerRules{
		MailIdentity: "example.com",
		Rules:        []HandlerRule{Bounce},
	}
	client := &fakeS3{objects: map[string][]byte{key: messageTo("blocked@example.com")}}
	r, emailStore, mailSender := newRoutingTestReceiver(identity, fakeUserManager{}, client)

	if err := r.ProcessMessage(context.Background(), key); err != nil {
		t.Fatalf("ProcessMessage() error = %v", err)
	}
	if len(mailSender.Bounces) != 1 || mailSender.Bounces[0].Recipient != "blocked@example.com" {
		t.Fatalf("bounces = %+v, want one bounce for blocked@example.com", mailSender.Bounces)
	}
	listed, err := emailStore.ListEmails(context.Background(), "blocked@example.com", "inbox")
	if err != nil || len(listed) != 0 {
		t.Errorf("emails = %v, %v; want no delivery", listed, err)
	}
}

func TestProcessMessageDropRuleDiscardsWithoutBounce(t *testing.T) {
	const key = "routing-drop"
	identity := BaseMailIdentityConfig()
	identity["example.com"] = HandlerRules{
		MailIdentity: "example.com",
		Rules:        []HandlerRule{Drop},
	}
	// fakeUserManager knows this recipient, so the drop must be the reason it
	// is not delivered rather than a failed user lookup.
	client := &fakeS3{objects: map[string][]byte{key: messageTo("dropped@example.com")}}
	r, emailStore, mailSender := newRoutingTestReceiver(identity, fakeUserManager{}, client)

	if err := r.ProcessMessage(context.Background(), key); err != nil {
		t.Fatalf("ProcessMessage() error = %v", err)
	}
	if len(mailSender.Bounces) != 0 {
		t.Errorf("bounces = %+v, want none for an explicit drop", mailSender.Bounces)
	}
	listed, err := emailStore.ListEmails(context.Background(), "dropped@example.com", "inbox")
	if err != nil || len(listed) != 0 {
		t.Errorf("emails = %v, %v; want no delivery", listed, err)
	}
	if _, ok := client.objects[key]; ok {
		t.Error("staging object was not deleted")
	}
}

func TestProcessMessageDropRuleTakesPrecedenceOverDelivery(t *testing.T) {
	const key = "routing-drop-first"
	identity := BaseMailIdentityConfig()
	identity["example.com"] = HandlerRules{
		MailIdentity: "example.com",
		Rules:        []HandlerRule{Drop, DeliverToUser},
	}
	client := &fakeS3{objects: map[string][]byte{key: messageTo("dropped@example.com")}}
	r, emailStore, mailSender := newRoutingTestReceiver(identity, fakeUserManager{}, client)

	if err := r.ProcessMessage(context.Background(), key); err != nil {
		t.Fatalf("ProcessMessage() error = %v", err)
	}
	listed, err := emailStore.ListEmails(context.Background(), "dropped@example.com", "inbox")
	if err != nil || len(listed) != 0 {
		t.Errorf("emails = %v, %v; want Drop to win over DeliverToUser", listed, err)
	}
	if len(mailSender.Bounces) != 0 {
		t.Errorf("bounces = %+v, want none", mailSender.Bounces)
	}
}

func TestProcessMessageMatchesIdentityCaseInsensitively(t *testing.T) {
	const key = "routing-case"
	identity := BaseMailIdentityConfig()
	identity["example.com"] = HandlerRules{
		MailIdentity: "example.com",
		Rules:        []HandlerRule{Bounce},
	}
	client := &fakeS3{objects: map[string][]byte{key: messageTo("Alias@Example.com")}}
	r, _, mailSender := newRoutingTestReceiver(identity, fakeUserManager{}, client)

	if err := r.ProcessMessage(context.Background(), key); err != nil {
		t.Fatalf("ProcessMessage() error = %v", err)
	}
	if len(mailSender.Bounces) != 1 || mailSender.Bounces[0].Recipient != "alias@example.com" {
		t.Fatalf("bounces = %+v, want one bounce for the normalised address", mailSender.Bounces)
	}
}

func TestProcessMessageFetchesRulesByMailIdentity(t *testing.T) {
	const key = "routing-identity-lookup"
	identity := BaseMailIdentityConfig()
	identity["news.example.com"] = HandlerRules{
		MailIdentity: "news.example.com",
		Rules:        []HandlerRule{Drop},
	}
	identity["blocked.example.com"] = HandlerRules{
		MailIdentity: "blocked.example.com",
		Rules:        []HandlerRule{Bounce},
	}
	raw := []byte(receivedHeader("reader@news.example.com", "poster@blocked.example.com", "alice@example.com") +
		"From: sender@example.com\r\n" +
		"To: reader@news.example.com, poster@blocked.example.com\r\n" +
		"Cc: alice@example.com\r\nSubject: hi\r\n\r\nbody\r\n")
	client := &fakeS3{objects: map[string][]byte{key: raw}}
	r, emailStore, mailSender := newRoutingTestReceiver(identity, fakeUserManager{}, client)

	if err := r.ProcessMessage(context.Background(), key); err != nil {
		t.Fatalf("ProcessMessage() error = %v", err)
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
}

func TestProcessMessageStopsAtFirstApplicableRule(t *testing.T) {
	const key = "routing-stop-first"
	identity := BaseMailIdentityConfig()
	identity["example.com"] = HandlerRules{
		MailIdentity: "catchall@example.com",
		// DeliverToUser succeeds for alice, so neither the catchall delivery
		// nor the later bounce may run.
		Rules: []HandlerRule{DeliverToUser, DeliverToMailIdentity, Bounce},
	}
	client := &fakeS3{objects: map[string][]byte{key: messageTo("alice@example.com")}}
	r, emailStore, mailSender := newRoutingTestReceiver(identity, fakeUserManager{}, client)

	if err := r.ProcessMessage(context.Background(), key); err != nil {
		t.Fatalf("ProcessMessage() error = %v", err)
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
			key := "routing-order"
			identity := BaseMailIdentityConfig()
			identity["example.com"] = HandlerRules{MailIdentity: "example.com", Rules: tt.rules}
			client := &fakeS3{objects: map[string][]byte{key: messageTo("alice@example.com")}}
			r, emailStore, mailSender := newRoutingTestReceiver(identity, fakeUserManager{}, client)

			if err := r.ProcessMessage(context.Background(), key); err != nil {
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
			key := "routing-try-rules"
			identity := BaseMailIdentityConfig()
			identity["example.com"] = HandlerRules{
				MailIdentity: "catchall@example.com",
				Rules:        tt.rules,
			}
			client := &fakeS3{objects: map[string][]byte{key: messageTo("unknown@example.com")}}
			r, emailStore, mailSender := newRoutingTestReceiver(
				identity,
				rejectingUserManager{invalid: "unknown@example.com"},
				client,
			)

			if err := r.ProcessMessage(context.Background(), key); err != nil {
				t.Fatalf("ProcessMessage() error = %v", err)
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
		})
	}
}
