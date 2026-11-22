package sender

import (
	"context"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/ses"
	"github.com/kncept/Emissary/mailer/email"
)

type fakeSES struct {
	input *ses.SendRawEmailInput
}

func (f *fakeSES) SendRawEmail(_ context.Context, params *ses.SendRawEmailInput, _ ...func(*ses.Options)) (*ses.SendRawEmailOutput, error) {
	f.input = params
	return &ses.SendRawEmailOutput{MessageId: awsString("test-message-id")}, nil
}

func (f *fakeSES) SendBounce(_ context.Context, _ *ses.SendBounceInput, _ ...func(*ses.Options)) (*ses.SendBounceOutput, error) {
	return &ses.SendBounceOutput{MessageId: awsString("test-bounce-id")}, nil
}

func awsString(s string) *string { return &s }

func TestSESEnvelopeSource(t *testing.T) {
	tests := []struct {
		name          string
		autoSubmitted string
		wantSource    string
	}{
		{name: "ordinary mail", wantSource: "alice@example.com"},
		{name: "auto-replied", autoSubmitted: "auto-replied", wantSource: ""},
		{name: "auto-generated", autoSubmitted: "auto-generated", wantSource: ""},
		{name: "explicit no", autoSubmitted: "no", wantSource: "alice@example.com"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			message := &email.UserEmail{
				From:          email.Address{Address: "alice@example.com"},
				To:            []email.Address{{Address: "bob@example.com"}},
				Subject:       "Hello",
				Text:          "Body",
				AutoSubmitted: tt.autoSubmitted,
			}
			if got := sesEnvelopeSource(message); got != tt.wantSource {
				t.Errorf("sesEnvelopeSource() = %q, want %q", got, tt.wantSource)
			}
		})
	}
}

// TestSESAutoReplyNullReversePath asserts the SES SendRawEmail envelope uses a
// null reverse path (empty Source, i.e. MAIL FROM:<>) for automatic replies,
// while the visible From header still carries the sender's address so replies
// from the recipient go to the right mailbox.
func TestSESAutoReplyNullReversePath(t *testing.T) {
	fake := &fakeSES{}
	s := &AwsSesSender{client: fake}
	message := &email.UserEmail{
		From:          email.Address{Address: "alice@example.com"},
		To:            []email.Address{{Address: "bob@example.com"}},
		Subject:       "Auto: Hello",
		Text:          "This is an automatic reply.",
		AutoSubmitted: "auto-replied",
	}
	if err := s.Send(context.Background(), message); err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	if fake.input == nil {
		t.Fatal("Send() did not call SendRawEmail")
	}
	if fake.input.Source == nil || *fake.input.Source != "" {
		t.Errorf("Source = %v, want empty string (null reverse path)", fake.input.Source)
	}
	wire := string(fake.input.RawMessage.Data)
	if !strings.Contains(wire, "From:") || !strings.Contains(wire, "alice@example.com") {
		t.Errorf("raw message lost the From header: %q", wire)
	}
	if !strings.Contains(wire, "Auto-Submitted: auto-replied") {
		t.Errorf("raw message lost Auto-Submitted: %q", wire)
	}
}

func TestSESOrdinaryMailEnvelopeSource(t *testing.T) {
	fake := &fakeSES{}
	s := &AwsSesSender{client: fake}
	message := &email.UserEmail{
		From:    email.Address{Address: "alice@example.com"},
		To:      []email.Address{{Address: "bob@example.com"}},
		Subject: "Hello",
		Text:    "Body",
	}
	if err := s.Send(context.Background(), message); err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	if fake.input == nil || fake.input.Source == nil || *fake.input.Source != "alice@example.com" {
		t.Errorf("Source = %v, want alice@example.com", fake.input)
	}
}
