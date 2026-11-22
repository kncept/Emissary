package receiver

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/kncept/Emissary/mailer/components/metadata"
	"github.com/kncept/Emissary/mailer/components/sender"
	"github.com/kncept/Emissary/mailer/components/storage"
	"github.com/kncept/Emissary/mailer/components/usermanager"
	"github.com/kncept/Emissary/mailer/email"
)

// vacationTestUserManager is a UserManager that also owns a durable vacation
// store, like the Emissary-mastered backends.
type vacationTestUserManager struct {
	fakeUserManager
	store *usermanager.MemoryVacationStore
}

func (m *vacationTestUserManager) GetVacationResponse(ctx context.Context, email string) (usermanager.VacationResponse, error) {
	return m.store.GetVacationResponse(ctx, email)
}

func (m *vacationTestUserManager) SaveVacationResponse(ctx context.Context, email string, v usermanager.VacationResponse) error {
	return m.store.SaveVacationResponse(ctx, email, v)
}

func newVacationReceiver(t *testing.T, vacation usermanager.VacationResponse) (*Receiver, *sender.InMemoryRecording, *vacationTestUserManager) {
	t.Helper()
	users := &vacationTestUserManager{store: usermanager.NewMemoryVacationStore()}
	if err := users.SaveVacationResponse(context.Background(), "alice@example.com", vacation); err != nil {
		t.Fatalf("SaveVacationResponse() error = %v", err)
	}
	mailSender := sender.NewInMemoryRecording()
	r, err := NewReceiver(ReceiverConfig{
		Domain:      "example.com",
		UserManager: users,
		Sender:      mailSender,
		Storage:     storage.NewMemoryStorage(),
		Metadata:    metadata.NewMemoryMetadata(),
	})
	if err != nil {
		t.Fatalf("NewReceiver() error = %v", err)
	}
	return r, mailSender, users
}

func vacationSettings() usermanager.VacationResponse {
	subject := "On leave"
	text := "I am away until Monday."
	return usermanager.VacationResponse{
		IsEnabled: true,
		Subject:   &subject,
		TextBody:  &text,
	}
}

func vacationMessage(extraHeaders, from, to string) []byte {
	raw := receivedHeader(to) + "From: " + from + "\r\nTo: " + to + "\r\n"
	if extraHeaders != "" {
		raw += extraHeaders
	}
	return []byte(raw + "Message-ID: <incoming@example.net>\r\nSubject: Hello\r\n\r\nbody\r\n")
}

func deliverVacationMessage(t *testing.T, r *Receiver, raw []byte) {
	t.Helper()
	if err := r.ProcessEvent(context.Background(), &fakeS3Event{raw: raw}); err != nil {
		t.Fatalf("ProcessEvent() error = %v", err)
	}
}

// TestVacationReplyForMailIdentityDelivery covers Phase 4c's extension of
// auto-replies to DeliverToMailIdentity. The mailbox owner's shared
// VacationStore settings must drive a reply even though there is no user
// record for that address.
func TestVacationReplyForMailIdentityDelivery(t *testing.T) {
	const mailbox = "alias@example.com"
	store := usermanager.NewMemoryVacationStore()
	if err := store.SaveVacationResponse(context.Background(), mailbox, vacationSettings()); err != nil {
		t.Fatalf("SaveVacationResponse() error = %v", err)
	}
	identity := BaseMailIdentityConfig()
	identity["example.com"] = HandlerRules{
		MailIdentity: mailbox,
		Rules:        []HandlerRule{DeliverToMailIdentity},
	}
	mailSender := sender.NewInMemoryRecording()
	r, err := NewReceiver(ReceiverConfig{
		Domain:         "example.com",
		UserManager:    usermanager.NewNone(usermanager.Config{AdminUsername: "admin", AdminPassword: "password"}),
		Sender:         mailSender,
		Storage:        storage.NewMemoryStorage(),
		Metadata:       metadata.NewMemoryMetadata(),
		VacationStore:  store,
		IdentityConfig: identity,
	})
	if err != nil {
		t.Fatalf("NewReceiver() error = %v", err)
	}

	deliverVacationMessage(t, r, vacationMessage("", "sender@example.net", mailbox))
	if len(mailSender.Sent) != 1 {
		t.Fatalf("vacation replies = %d, want 1: %+v", len(mailSender.Sent), mailSender.Sent)
	}
	reply := mailSender.Sent[0]
	if reply.From.Address != mailbox || reply.Subject != "On leave" {
		t.Errorf("reply = %+v, want from %q with subject %q", reply, mailbox, "On leave")
	}
}

func TestVacationReplySentOncePerSenderWithRFC3834Headers(t *testing.T) {
	r, mailSender, users := newVacationReceiver(t, vacationSettings())

	deliverVacationMessage(t, r, vacationMessage("", "sender@example.net", "alice@example.com"))
	if len(mailSender.Sent) != 1 {
		t.Fatalf("vacation replies = %d, want 1", len(mailSender.Sent))
	}
	reply := mailSender.Sent[0]
	if reply.From.Address != "alice@example.com" || len(reply.To) != 1 || reply.To[0].Address != "sender@example.net" {
		t.Errorf("reply addresses from=%v to=%v", reply.From, reply.To)
	}
	if reply.Subject != "On leave" {
		t.Errorf("reply subject = %q, want On leave", reply.Subject)
	}
	if reply.Text != "I am away until Monday." {
		t.Errorf("reply text = %q", reply.Text)
	}
	if reply.AutoSubmitted != "auto-replied" {
		t.Errorf("AutoSubmitted = %q, want auto-replied", reply.AutoSubmitted)
	}
	if len(reply.InReplyTo) != 1 || reply.InReplyTo[0] != "<incoming@example.net>" {
		t.Errorf("InReplyTo = %v", reply.InReplyTo)
	}
	wire := string(email.RenderHeaders(reply, email.HeaderPolicy{}))
	if !strings.Contains(wire, "Auto-Submitted: auto-replied") {
		t.Errorf("rendered reply missing Auto-Submitted header: %s", wire)
	}

	// The same sender must not get a second reply within the interval.
	deliverVacationMessage(t, r, vacationMessage("", "sender@example.net", "alice@example.com"))
	if len(mailSender.Sent) != 1 {
		t.Errorf("duplicate vacation replies = %d, want 1", len(mailSender.Sent))
	}

	// A different sender receives their own first reply.
	deliverVacationMessage(t, r, vacationMessage("", "other@example.org", "alice@example.com"))
	if len(mailSender.Sent) != 2 {
		t.Errorf("replies to two senders = %d, want 2", len(mailSender.Sent))
	}

	// Reply state is persisted in the vacation store.
	stored, err := users.GetVacationResponse(context.Background(), "alice@example.com")
	if err != nil {
		t.Fatalf("GetVacationResponse() error = %v", err)
	}
	if !stored.RecentlyResponded("sender@example.net", time.Now().UTC(), time.Hour) {
		t.Error("sender reply was not recorded")
	}
}

func TestVacationReplySuppressedForAutomatedMail(t *testing.T) {
	tests := []struct {
		name    string
		headers string
		from    string
	}{
		{
			name:    "auto-submitted reply",
			headers: "Auto-Submitted: auto-replied\r\n",
			from:    "sender@example.net",
		},
		{
			name:    "auto-generated notification",
			headers: "Auto-Submitted: auto-generated\r\n",
			from:    "sender@example.net",
		},
		{
			name:    "bulk precedence",
			headers: "Precedence: bulk\r\n",
			from:    "sender@example.net",
		},
		{
			name:    "mailing list",
			headers: "List-Id: <list@example.net>\r\n",
			from:    "sender@example.net",
		},
		{
			name:    "mailer daemon",
			headers: "",
			from:    "MAILER-DAEMON@example.net",
		},
		{
			name:    "owner address",
			headers: "",
			from:    "owner-list@example.net",
		},
		{
			name:    "null return path",
			headers: "Return-Path: <>\r\n",
			from:    "sender@example.net",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, mailSender, _ := newVacationReceiver(t, vacationSettings())
			deliverVacationMessage(t, r, vacationMessage(tt.headers, tt.from, "alice@example.com"))
			if len(mailSender.Sent) != 0 {
				t.Errorf("sent %d replies for %s, want 0: %+v", len(mailSender.Sent), tt.name, mailSender.Sent)
			}
		})
	}
}

func TestVacationReplyRequiresExplicitRecipient(t *testing.T) {
	r, mailSender, _ := newVacationReceiver(t, vacationSettings())
	raw := receivedHeader("alice@example.com") +
		"From: sender@example.net\r\nTo: hidden-list@example.net\r\nSubject: Hello\r\n\r\nbody\r\n"
	deliverVacationMessage(t, r, []byte(raw))
	if len(mailSender.Sent) != 0 {
		t.Errorf("sent reply to message not addressed to recipient: %+v", mailSender.Sent)
	}
}

func TestVacationReplyRespectsDateRange(t *testing.T) {
	past := time.Now().UTC().Add(-48 * time.Hour)
	future := time.Now().UTC().Add(-24 * time.Hour)
	settings := vacationSettings()
	settings.FromDate = &past
	settings.ToDate = &future

	r, mailSender, _ := newVacationReceiver(t, settings)
	deliverVacationMessage(t, r, vacationMessage("", "sender@example.net", "alice@example.com"))
	if len(mailSender.Sent) != 0 {
		t.Errorf("sent reply outside date range: %+v", mailSender.Sent)
	}
}

func TestVacationReplyDisabledByDefault(t *testing.T) {
	r, mailSender, _ := newVacationReceiver(t, usermanager.VacationResponse{})
	deliverVacationMessage(t, r, vacationMessage("", "sender@example.net", "alice@example.com"))
	if len(mailSender.Sent) != 0 {
		t.Errorf("sent reply while disabled: %+v", mailSender.Sent)
	}
}
