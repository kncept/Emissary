package sender

import (
	"context"
	"testing"

	"github.com/kncept/Emissary/mailer/email"
)

func TestLogSender(t *testing.T) {
	s := NewLogSender()

	if s.Name() != "LogSender" {
		t.Errorf("Name() = %q, want %q", s.Name(), "LogSender")
	}

	// Should not error, just log
	err := s.Send(context.Background(), &email.UserEmail{
		From:    email.Address{Address: "from@example.com"},
		To:      []email.Address{{Address: "to@example.com"}},
		Subject: "Test",
		Text:    "Body",
	})
	if err != nil {
		t.Errorf("Send() error = %v", err)
	}
}

func TestInMemoryRecording(t *testing.T) {
	s := NewInMemoryRecording()

	if s.Name() != "InMemoryRecording" {
		t.Errorf("Name() = %q, want %q", s.Name(), "InMemoryRecording")
	}
	if len(s.Sent) != 0 {
		t.Fatalf("initial Sent = %+v, want empty", s.Sent)
	}

	first := &email.UserEmail{Subject: "first"}
	second := &email.UserEmail{Subject: "second"}
	for _, message := range []*email.UserEmail{first, second} {
		if err := s.Send(context.Background(), message); err != nil {
			t.Fatalf("Send() error = %v", err)
		}
	}

	if len(s.Sent) != 2 || s.Sent[0] != first || s.Sent[1] != second {
		t.Fatalf("Sent = %+v, want [first second] in order", s.Sent)
	}

	if err := s.SendBounce(context.Background(), "message-id", "mailer-daemon@example.com", "to@example.com", "UserUnknown", first); err != nil {
		t.Fatalf("SendBounce() error = %v", err)
	}
	if len(s.Bounces) != 1 {
		t.Fatalf("Bounces = %+v, want one bounce", s.Bounces)
	}
	bounce := s.Bounces[0]
	if bounce.OriginalMessageID != "message-id" || bounce.Recipient != "to@example.com" || bounce.BounceType != "UserUnknown" || bounce.OriginalMessage != first {
		t.Fatalf("Bounce = %+v, want recorded call", bounce)
	}
}

func TestAwsSesSender_InvalidConfig(t *testing.T) {
	// Test that NewAwsSesSender fails gracefully without AWS credentials
	// This is more of an integration test, so we just verify the constructor doesn't panic
	_, err := NewAwsSesSender(context.Background())
	if err == nil {
		t.Log("NewAwsSesSender succeeded (may have default credentials)")
	} else {
		t.Logf("NewAwsSesSender error (expected without credentials): %v", err)
	}
}
