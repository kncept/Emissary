package sender

import (
	"context"

	"github.com/kncept/Emissary/mailer/email"
)

// Sender is the outbound side of the service. New providers can be added by
// implementing this interface and selecting them with EMISSARY_SENDER.
type Sender interface {
	Name() string
	From() string
	Send(ctx context.Context, message *email.Email) error
}
