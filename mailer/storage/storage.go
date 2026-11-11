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

	ListAllEmails(ctx context.Context) ([]*email.UserEmail, error)
	GetEmail(ctx context.Context, id string) (*email.UserEmail, error)
	// DeleteEmail removes the message with the given id. It returns
	// ErrNotFound when no such message exists.
	DeleteEmail(ctx context.Context, id string) error
}
