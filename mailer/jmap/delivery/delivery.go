// Package delivery advances the RFC 8620 EmailDelivery type state when the
// inbound pipeline stores a message. It lives under jmap so it can use the
// internal state tracker while remaining importable by the receiver and the
// configuration vendor, which run in a different Lambda process.
package delivery

import (
	"context"

	"github.com/kncept/Emissary/mailer/components/storage"
	"github.com/kncept/Emissary/mailer/jmap/internal/base"
)

// Notifier records a successful delivery for an account.
type Notifier interface {
	Delivered(ctx context.Context, accountID, messageID string) error
}

// Tracker persists delivery changes in the same durable store as JMAP's other
// type states, so the frontend process sees them even when delivery happened
// in another invocation.
type Tracker struct {
	changes *base.Tracker
}

var _ Notifier = (*Tracker)(nil)

// New creates a delivery notifier backed by the configured state store.
func New(store storage.JMAPStateStore) *Tracker {
	if store == nil {
		return nil
	}
	return &Tracker{changes: base.NewTracker(store)}
}

// Delivered advances EmailDelivery and records the delivered message id.
func (t *Tracker) Delivered(ctx context.Context, accountID, messageID string) error {
	if t == nil || t.changes == nil {
		return nil
	}
	return t.changes.Record(ctx, accountID, base.StateEmailDelivery, messageID, base.ChangeCreate)
}
