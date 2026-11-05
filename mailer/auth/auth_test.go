package auth

import (
	"testing"
	"time"
)

func TestAuthenticator_Authenticate(t *testing.T) {
	a := New("user", "pass", "secret", time.Hour)

	tests := []struct {
		name     string
		username string
		password string
		want     bool
	}{
		{"correct credentials", "user", "pass", true},
		{"wrong username", "wrong", "pass", false},
		{"wrong password", "user", "wrong", false},
		{"empty username", "", "pass", false},
		{"empty password", "user", "", false},
		{"both empty", "", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := a.Authenticate(tt.username, tt.password)
			if got != tt.want {
				t.Errorf("Authenticate() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestAuthenticator_IssueAndValidate(t *testing.T) {
	a := New("user", "pass", "secret", time.Hour)

	token, expiresAt, err := a.Issue("user")
	if err != nil {
		t.Fatalf("Issue() error = %v", err)
	}

	if token == "" {
		t.Error("Issue() returned empty token")
	}

	if expiresAt.IsZero() {
		t.Error("Issue() returned zero expiresAt")
	}

	subject, err := a.Validate(token)
	if err != nil {
		t.Fatalf("Validate() error = %v", err)
	}

	if subject != "user" {
		t.Errorf("Validate() subject = %q, want %q", subject, "user")
	}
}

func TestAuthenticator_Validate_ExpiredToken(t *testing.T) {
	// Create an authenticator with a short TTL, then manipulate time to make token expired
	a := New("user", "pass", "secret", time.Hour)

	// Create token with custom time in the past
	past := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
	a.now = func() time.Time { return past }
	token, _, err := a.Issue("user")
	if err != nil {
		t.Fatalf("Issue() error = %v", err)
	}

	// Now validate with current time (which is 2 hours later, past the 1-hour TTL)
	future := past.Add(2 * time.Hour)
	a.now = func() time.Time { return future }

	_, err = a.Validate(token)
	if err == nil {
		t.Error("Validate() should return error for expired token")
	}
	if err != ErrExpiredToken {
		t.Errorf("Validate() error = %v, want %v", err, ErrExpiredToken)
	}
}

func TestAuthenticator_Validate_InvalidToken(t *testing.T) {
	a := New("user", "pass", "secret", time.Hour)

	tests := []struct {
		name  string
		token string
	}{
		{"empty", ""},
		{"malformed", "not.a.jwt"},
		{"wrong signature", "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiJ1c2VyIiwiaWF0IjoxNzAwMDAwMDAwLCJleHAiOjE3MDAwMDAwMDB9.wrongsig"},
		{"wrong secret", func() string {
			a2 := New("user", "pass", "different-secret", time.Hour)
			tkn, _, _ := a2.Issue("user")
			return tkn
		}()},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := a.Validate(tt.token)
			if err == nil {
				t.Errorf("Validate(%q) should return error", tt.token)
			}
		})
	}
}

func TestAuthenticator_ConstantTimeCompare(t *testing.T) {
	a := New("user", "pass", "secret", time.Hour)

	// Test that timing attacks are mitigated by using constant-time comparison
	// We can't easily test timing, but we can verify the function works correctly
	if !a.Authenticate("user", "pass") {
		t.Error("Authenticate failed for correct credentials")
	}
	if a.Authenticate("user", "pass\x00") {
		t.Error("Authenticate should fail for different length password")
	}
}

func TestAuthenticator_CustomTimeSource(t *testing.T) {
	now := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
	a := New("user", "pass", "secret", time.Hour)
	a.now = func() time.Time { return now }

	token, expiresAt, err := a.Issue("user")
	if err != nil {
		t.Fatalf("Issue() error = %v", err)
	}

	if !expiresAt.Equal(now.Add(time.Hour)) {
		t.Errorf("expiresAt = %v, want %v", expiresAt, now.Add(time.Hour))
	}

	// Validate should work with the same time source
	subject, err := a.Validate(token)
	if err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if subject != "user" {
		t.Errorf("subject = %q, want %q", subject, "user")
	}
}