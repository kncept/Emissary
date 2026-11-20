package storage

import (
	"context"
	"errors"

	"github.com/kncept/Emissary/mailer/email"
)

// ErrNotFound is returned when an email does not exist in the backing store.
var ErrNotFound = errors.New("email not found")

// DraftStorage holds JMAP drafts in a per-session scratch area that survives
// across Lambda invocations (S3 prefix _storage/<sid>/ in production). Draft
// ids are relative to the session folder.
type DraftStorage interface {
	// PutDraft stores a draft for a session under creationID and returns its
	// session-relative id.
	PutDraft(ctx context.Context, sessionID, creationID string, message *email.UserEmail) (string, error)
	// GetDraft loads a draft by its session-relative id. It returns
	// ErrNotFound when the draft does not exist.
	GetDraft(ctx context.Context, sessionID, id string) (*email.UserEmail, error)
	// DeleteDraft removes a draft. It is idempotent: a missing draft is not
	// an error.
	DeleteDraft(ctx context.Context, sessionID, id string) error
}

// SessionStateStore persists small per-session JSON state, such as the JMAP
// creation-id map, in the same durable scratch area as drafts.
type SessionStateStore interface {
	LoadCreatedIDs(ctx context.Context, sessionID string) (map[string]string, error)
	SaveCreatedIDs(ctx context.Context, sessionID string, ids map[string]string) error
}

// Storage is the mail source. New providers can be added by implementing
// this interface and selecting them with STORAGE.
type Storage interface {
	Name() string

	// ListEmails lists messages in a JMAP account's mailbox. accountID is the
	// account id (the user's email address) and mailboxID is the JMAP mailbox
	// id (for example "inbox"). An empty mailboxID lists every mailbox
	// belonging to the account. Implementations must never read outside the
	// account's own storage.
	ListEmails(ctx context.Context, accountID, mailboxID string) ([]*email.ListedEmail, error)
	// GetEmail returns a message by id, together with the ref describing the
	// account and mailbox that own it.
	GetEmail(ctx context.Context, id string) (*email.ListedEmail, error)
	// GetEmailRaw returns the original RFC 5322 message bytes for id, without
	// parsing them. It returns ErrNotFound when no such message exists. Stores
	// that only keep a parsed representation return a best-effort rendering.
	GetEmailRaw(ctx context.Context, id string) ([]byte, error)
	// AddEmail stores a new message in the account's inbox and returns the
	// generated id. The caller uses the returned id to create its metadata.
	// raw is the original RFC 5322 bytes read from the wire (for example the
	// SES staging object). When non-nil it must be stored verbatim so that
	// GetEmailRaw returns every header, MIME part and transfer encoding
	// unchanged. A nil raw asks the store to render a message from the parsed
	// fields, which is the case for messages created through JMAP.
	AddEmail(ctx context.Context, accountID string, message *email.UserEmail, raw []byte) (string, error)
	// DeleteEmail removes the message with the given id. It returns
	// ErrNotFound when no such message exists.
	DeleteEmail(ctx context.Context, id string) error

	DraftStorage
	SessionStateStore
}
