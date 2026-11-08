package sender

import (
	"context"
	"log"

	"github.com/kncept/Emissary/mailer/email"
)

var _ Sender = (*LogSender)(nil)

// LogSender is a development sender that writes messages to the process log
// instead of calling an external provider.
type LogSender struct {
	from string
}

func NewLogSender(from string) *LogSender {
	if from == "" {
		from = "noreply@localhost"
	}
	return &LogSender{from: from}
}

func (s *LogSender) Name() string {
	return "LogSender"
}

func (s *LogSender) From() string {
	return s.from
}

func (s *LogSender) Send(_ context.Context, message *email.UserEmail) error {
	from := s.from
	if message.From.Address != "" {
		from = message.From.Address
	}
	log.Printf("outbound email from=%s to=%v subject=%q", from, addressStrings(message.To), message.Subject)
	return nil
}
