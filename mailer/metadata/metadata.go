package metadata

import "context"

// Metadata tracks per-email state that is not part of the message itself.
// The default implementation is in-memory; providers are selected with
// METADATA.
type Metadata interface {
	Name() string

	IsRead(ctx context.Context, emailID string) (bool, error)
	MarkRead(ctx context.Context, emailID string) error
	MarkUnread(ctx context.Context, emailID string) error
}
