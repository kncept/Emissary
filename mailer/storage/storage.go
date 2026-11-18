package storage

import (
	"context"
	"errors"

	"github.com/kncept/Emissary/mailer/email"
)

// ErrNotFound is returned when an email does not exist in the backing store.
var ErrNotFound = errors.New("email not found")

// Storage is the mail source. New providers can be added by implementing
// this interface and selecting them with STORAGE.
type Storage interface {
	Name() string

	// ListAccountEmails lists messages in a JMAP account's mailbox. accountID
	// is the account id (the user's email address) and mailboxID is the JMAP
	// mailbox id (for example "inbox"). An empty mailboxID lists every mailbox
	// belonging to the account. Implementations must never read outside the
	// account's own storage.
	ListAccountEmails(ctx context.Context, accountID, mailboxID string) ([]*email.UserEmail, error)
	GetEmail(ctx context.Context, id string) (*email.UserEmail, error)
	// AddEmail adds a new email to the store. Returns ErrNotFound if
	// an email with the same ID already exists.
	AddEmail(ctx context.Context, email *email.UserEmail) error
	// DeleteEmail removes the message with the given id. It returns
	// ErrNotFound when no such message exists.
	DeleteEmail(ctx context.Context, id string) error
}
