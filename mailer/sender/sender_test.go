package sender

import (
	"context"
	"testing"

	"github.com/kncept/Emissary/mailer/email"
)

func TestLogSender(t *testing.T) {
	s := NewLogSender("test@example.com")

	if s.Name() != "LogSender" {
		t.Errorf("Name() = %q, want %q", s.Name(), "LogSender")
	}

	if s.From() != "test@example.com" {
		t.Errorf("From() = %q, want %q", s.From(), "test@example.com")
	}

	// Should not error, just log
	err := s.Send(context.Background(), &email.Email{
		From:    email.Address{Address: "from@example.com"},
		To:      []email.Address{{Address: "to@example.com"}},
		Subject: "Test",
		Text:    "Body",
	})
	if err != nil {
		t.Errorf("Send() error = %v", err)
	}
}

func TestLogSender_DefaultFrom(t *testing.T) {
	s := NewLogSender("")

	if s.From() != "noreply@localhost" {
		t.Errorf("From() = %q, want %q", s.From(), "noreply@localhost")
	}
}

func TestAwsSesSender_InvalidConfig(t *testing.T) {
	// Test that NewAwsSesSender fails gracefully without AWS credentials
	// This is more of an integration test, so we just verify the constructor doesn't panic
	_, err := NewAwsSesSender(context.Background(), "us-east-1", "")
	if err == nil {
		t.Log("NewAwsSesSender succeeded (may have default credentials)")
	} else {
		t.Logf("NewAwsSesSender error (expected without credentials): %v", err)
	}
}