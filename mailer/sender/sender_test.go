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
