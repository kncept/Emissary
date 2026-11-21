package sender

import (
	"context"

	"github.com/kncept/Emissary/mailer/email"
)

var _ Sender = (*InMemoryRecording)(nil)

// InMemoryRecording is a Sender implementation for tests. It keeps sent
// messages in memory instead of delivering them, so tests can assert what
// would have been sent through the real provider. It is not intended for
// production use.
type InMemoryRecording struct {
	// Sent holds the messages passed to Send, in the order they were sent.
	// Tests may read it directly; it is deliberately a plain slice so the
	// recording behaviour stays obvious and easy to inspect.
	Sent []*email.UserEmail

	// Bounces holds the calls made to SendBounce, in call order.
	Bounces []Bounce
}

// Bounce records one SendBounce call for tests.
type Bounce struct {
	OriginalMessageID string
	BounceSender      string
	Recipient         string
	BounceType        string
	OriginalMessage   *email.UserEmail
}

// NewInMemoryRecording returns a ready-to-use recording sender for tests.
func NewInMemoryRecording() *InMemoryRecording {
	return &InMemoryRecording{}
}

func (s *InMemoryRecording) Name() string { return "InMemoryRecording" }

func (s *InMemoryRecording) Send(_ context.Context, message *email.UserEmail) error {
	s.Sent = append(s.Sent, message)
	return nil
}

func (s *InMemoryRecording) SendBounce(_ context.Context, originalMessageID, bounceSender, recipient, bounceType string, originalMessage *email.UserEmail) error {
	s.Bounces = append(s.Bounces, Bounce{
		OriginalMessageID: originalMessageID,
		BounceSender:      bounceSender,
		Recipient:         recipient,
		BounceType:        bounceType,
		OriginalMessage:   originalMessage,
	})
	return nil
}
