package sender

import (
	"context"
	"log/slog"

	"github.com/kncept/Emissary/mailer/email"
)

var _ Sender = (*LogSender)(nil)

// LogSender is a development sender that writes messages to the process log
// instead of calling an external provider.
type LogSender struct{}

func NewLogSender() *LogSender {
	return &LogSender{}
}

func (s *LogSender) Name() string {
	return "LogSender"
}

func (s *LogSender) Send(_ context.Context, message *email.UserEmail) error {
	// This sender exists to make outbound mail visible in the log, so the
	// message itself is the Info record rather than a diagnostic trace.
	slog.Info("LogSender: outbound email",
		"from", message.From.Address,
		"reply_to", addressStrings(message.ReplyTo),
		"to", addressStrings(message.To),
		"subject", message.Subject)
	return nil
}

func (s *LogSender) SendBounce(_ context.Context, originalMessageID, bounceSender, recipient, bounceType string, originalMessage *email.UserEmail) error {
	slog.Info("LogSender: bounce email",
		"original_message_id", originalMessageID,
		"bounce_sender", bounceSender,
		"recipient", recipient,
		"bounce_type", bounceType,
		"subject", originalMessage.Subject)
	return nil
}
