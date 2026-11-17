package sender

import (
	"context"

	"github.com/kncept/Emissary/mailer/email"
)

// Sender is the outbound side of the service. New providers can be added by
// implementing this interface and selecting them with SENDER.
type Sender interface {
	Name() string
	From() string
	Send(ctx context.Context, message *email.UserEmail) error
	// SendBounce sends a bounce message for a failed delivery.
	// originalMessageID is the SES message ID of the original message.
	// bounceSender is the address to use as the bounce sender (e.g., mailer-daemon@domain).
	// recipient is the address that failed.
	// bounceType is the type of bounce (e.g., "UserUnknown", "ContentRejected").
	// originalMessage is the original message that failed delivery.
	SendBounce(ctx context.Context, originalMessageID, bounceSender, recipient, bounceType string, originalMessage *email.UserEmail) error
}
