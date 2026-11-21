package delivery

import (
	"context"
	"testing"

	"github.com/kncept/Emissary/mailer/components/storage"
	"github.com/kncept/Emissary/mailer/jmap/internal/base"
)

func TestDeliveredAdvancesDurableEmailDeliveryState(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemoryStorage()
	n := New(store)
	if err := n.Delivered(ctx, "alice@example.com", "message-1"); err != nil {
		t.Fatalf("Delivered() error = %v", err)
	}

	// A separate tracker process sees the advanced token from the store.
	tracker := base.NewTracker(store)
	token, err := tracker.State(ctx, "alice@example.com", base.StateEmailDelivery)
	if err != nil {
		t.Fatal(err)
	}
	if token != "email-delivery-1" {
		t.Fatalf("EmailDelivery state = %q, want email-delivery-1", token)
	}
}
