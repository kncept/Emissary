package receiver

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/kncept/Emissary/mailer/components/metadata"
	"github.com/kncept/Emissary/mailer/components/sender"
	"github.com/kncept/Emissary/mailer/components/storage"
	"github.com/kncept/Emissary/mailer/components/usermanager"
	"github.com/kncept/Emissary/mailer/jmap"
)

// TestJMAPSetVacationDrivesReceiverAutoReply covers the shared VacationStore
// seam end to end: VacationResponse/set through the JMAP handler writes into
// the same store the inbound receiver reads, so a settings change is visible
// to auto-reply processing without a process restart.
func TestJMAPSetVacationDrivesReceiverAutoReply(t *testing.T) {
	const accountID = "alice@example.com"
	ctx := context.Background()
	sharedStore := usermanager.NewMemoryVacationStore()
	users := usermanager.NewNone(usermanager.Config{AdminUsername: "admin", AdminPassword: "password"})

	// Configure the vacation response through the JMAP handler, replacing its
	// default store with the one the receiver will share.
	jmapHandler := jmap.NewHandler(storage.NewMemoryStorage(), metadata.NewMemoryMetadata(), users, sender.NewInMemoryRecording())
	jmapHandler.SetVacationStore(sharedStore)
	setArgs := json.RawMessage(`{
		"accountId":"alice@example.com",
		"update":{"singleton":{
			"isEnabled":true,
			"subject":"On leave",
			"textBody":"I am away until Monday."
		}}
	}`)
	if _, err := jmapHandler.VacationResponseSet(ctx, accountID, setArgs); err != nil {
		t.Fatalf("VacationResponse/set error = %v", err)
	}

	// The receiver reads from the same store: no separate JMAP/receiver state.
	mailSender := sender.NewInMemoryRecording()
	r, err := NewReceiver(ReceiverConfig{
		Domain:        "example.com",
		UserManager:   users,
		Sender:        mailSender,
		Storage:       storage.NewMemoryStorage(),
		Metadata:      metadata.NewMemoryMetadata(),
		VacationStore: sharedStore,
	})
	if err != nil {
		t.Fatalf("NewReceiver() error = %v", err)
	}

	deliverVacationMessage(t, r, vacationMessage("", "sender@example.net", accountID))
	if len(mailSender.Sent) != 1 {
		t.Fatalf("vacation replies = %d, want 1: %+v", len(mailSender.Sent), mailSender.Sent)
	}
	reply := mailSender.Sent[0]
	if reply.From.Address != accountID || len(reply.To) != 1 || reply.To[0].Address != "sender@example.net" {
		t.Errorf("reply addresses from=%v to=%v", reply.From, reply.To)
	}
	if reply.Subject != "On leave" || reply.Text != "I am away until Monday." {
		t.Errorf("reply subject=%q text=%q, want JMAP-set values", reply.Subject, reply.Text)
	}
	if reply.AutoSubmitted != "auto-replied" {
		t.Errorf("AutoSubmitted = %q, want auto-replied", reply.AutoSubmitted)
	}
}
