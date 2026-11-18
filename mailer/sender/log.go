package sender

import (
	"context"
	"log"

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
	log.Printf("outbound email from=%s replyTo=%v to=%v subject=%q", message.From.Address, addressStrings(message.ReplyTo), addressStrings(message.To), message.Subject)
	return nil
}

func (s *LogSender) SendBounce(_ context.Context, originalMessageID, bounceSender, recipient, bounceType string, originalMessage *email.UserEmail) error {
	log.Printf("bounce email: originalMessageID=%s bounceSender=%s recipient=%s bounceType=%s subject=%q", originalMessageID, bounceSender, recipient, bounceType, originalMessage.Subject)
	return nil
}
