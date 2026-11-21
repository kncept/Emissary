package receiver

import (
	"context"
	"errors"
	"testing"

	"github.com/kncept/Emissary/mailer/components/metadata"
	"github.com/kncept/Emissary/mailer/components/sender"
	"github.com/kncept/Emissary/mailer/components/storage"
)

type deliveryRecorder struct {
	calls []deliveryCall
	err   error
}

type deliveryCall struct {
	accountID string
	messageID string
}

func (d *deliveryRecorder) Delivered(_ context.Context, accountID, messageID string) error {
	d.calls = append(d.calls, deliveryCall{accountID: accountID, messageID: messageID})
	return d.err
}

func TestDeliveryAdvancesEmailDeliveryState(t *testing.T) {
	recorder := &deliveryRecorder{}
	r, err := NewReceiver(ReceiverConfig{
		Domain:        "example.com",
		UserManager:   fakeUserManager{},
		Sender:        sender.NewInMemoryRecording(),
		Storage:       storage.NewMemoryStorage(),
		Metadata:      metadata.NewMemoryMetadata(),
		EmailDelivery: recorder,
	})
	if err != nil {
		t.Fatal(err)
	}
	raw := []byte(receivedHeader("alice@example.com") +
		"From: sender@example.com\r\nTo: alice@example.com\r\n" +
		"Date: Fri, 08 Mar 2024 15:30:00 +0100\r\nSubject: push\r\n\r\nbody\r\n")
	if err := r.ProcessEvent(context.Background(), &fakeS3Event{raw: raw}); err != nil {
		t.Fatalf("ProcessEvent() error = %v", err)
	}
	if len(recorder.calls) != 1 {
		t.Fatalf("Delivered calls = %d, want 1", len(recorder.calls))
	}
	if recorder.calls[0].accountID != "alice@example.com" || recorder.calls[0].messageID == "" {
		t.Fatalf("unexpected delivery call: %+v", recorder.calls[0])
	}
}

func TestDeliveryStateFailureDoesNotRetryDelivery(t *testing.T) {
	recorder := &deliveryRecorder{err: errors.New("state store unavailable")}
	r, err := NewReceiver(ReceiverConfig{
		Domain:        "example.com",
		UserManager:   fakeUserManager{},
		Sender:        sender.NewInMemoryRecording(),
		Storage:       storage.NewMemoryStorage(),
		Metadata:      metadata.NewMemoryMetadata(),
		EmailDelivery: recorder,
	})
	if err != nil {
		t.Fatal(err)
	}
	raw := []byte(receivedHeader("alice@example.com") +
		"From: sender@example.com\r\nTo: alice@example.com\r\n" +
		"Date: Fri, 08 Mar 2024 15:30:00 +0100\r\nSubject: push\r\n\r\nbody\r\n")
	if err := r.ProcessEvent(context.Background(), &fakeS3Event{raw: raw}); err != nil {
		t.Fatalf("ProcessEvent() must not fail when push state fails: %v", err)
	}
	if len(recorder.calls) != 1 {
		t.Fatalf("Delivered calls = %d, want 1", len(recorder.calls))
	}
}
