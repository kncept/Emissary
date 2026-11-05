package storage

import (
	"context"
	"errors"

	"github.com/kncept/Emissary/mailer/email"
)

// ErrNotFound is returned when an email does not exist in the backing store.
var ErrNotFound = errors.New("email not found")

// Storage is the read side of the mail source. New providers can be added by
// implementing this interface and selecting them with EMISSARY_STORAGE.
type Storage interface {
	Name() string

	ListAllEmails(ctx context.Context) ([]*email.Email, error)
	GetEmail(ctx context.Context, id string) (*email.Email, error)
}
