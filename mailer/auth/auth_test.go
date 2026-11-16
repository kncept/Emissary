package auth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/kncept/Emissary/mailer/usermanager"
)

type disabledUserManager struct{}

func (disabledUserManager) Name() string { return "disabled" }

func (disabledUserManager) Authenticate(context.Context, string, string) (*usermanager.User, error) {
	return nil, usermanager.ErrUserDisabled
}

func (disabledUserManager) Lookup(context.Context, string) (*usermanager.User, error) {
	return nil, usermanager.ErrUserDisabled
}

func TestAuthenticator_Authenticate(t *testing.T) {
	a := New("user", "pass", "secret", time.Hour, "", "")

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

func TestAuthenticator_LoginReturnsDisabledUserError(t *testing.T) {
	a := NewWithUserManager(disabledUserManager{}, nil, "secret", time.Hour, "", "")

	_, err := a.Login(context.Background(), "user@example.com", "password")
	if !errors.Is(err, usermanager.ErrUserDisabled) {
		t.Errorf("Login() error = %v, want ErrUserDisabled", err)
	}
}

type masteredUserManager struct {
	user usermanager.User
}

func (m masteredUserManager) Name() string { return "mastered" }

func (m masteredUserManager) Authenticate(context.Context, string, string) (*usermanager.User, error) {
	user := m.user
	return &user, nil
}

func (m masteredUserManager) Lookup(context.Context, string) (*usermanager.User, error) {
	user := m.user
	return &user, nil
}

func (masteredUserManager) List(context.Context, string, int) ([]*usermanager.EmissaryUser, string, error) {
	return nil, "", nil
}

func (masteredUserManager) Create(context.Context, *usermanager.EmissaryUser) error { return nil }
func (masteredUserManager) Save(context.Context, *usermanager.EmissaryUser) error   { return nil }

func (masteredUserManager) Get(context.Context, string) (*usermanager.EmissaryUser, error) {
	return nil, usermanager.ErrUserNotFound
}

func (masteredUserManager) Delete(context.Context, string) error { return nil }
func (masteredUserManager) ChangePassword(context.Context, string, string, string) error {
	return nil
}

func TestAuthenticator_EmissaryUserManagementClaim(t *testing.T) {
	for _, tc := range []struct {
		name  string
		admin bool
		want  bool
	}{
		{"admin", true, true},
		{"ordinary", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := NewWithUserManager(masteredUserManager{user: usermanager.User{
				ID:    "user@example.com",
				Email: "user@example.com",
				Admin: tc.admin,
			}}, nil, "secret", time.Hour, "", "")

			result, err := a.Login(context.Background(), "user@example.com", "password")
			if err != nil {
				t.Fatalf("Login() error = %v", err)
			}
			claims, err := a.ValidateClaims(result.AccessToken)
			if err != nil {
				t.Fatalf("ValidateClaims() error = %v", err)
			}
			if claims.EmissaryUserManagement != tc.want {
				t.Errorf("EmissaryUserManagement = %v, want %v", claims.EmissaryUserManagement, tc.want)
			}
		})
	}
}

func TestAuthenticator_IssueAndValidate(t *testing.T) {
	a := New("user", "pass", "secret", time.Hour, "", "")

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
	// Use a custom TTL of 1 hour for access tokens for this test
	a := NewWithUserManager(usermanager.NewNone(usermanager.Config{AdminUsername: "user", AdminPassword: "pass"}), nil, "secret", 9*time.Hour, "", "")

	// Create token with custom time in the past
	past := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
	a.now = func() time.Time { return past }
	token, _, err := a.Issue("user")
	if err != nil {
		t.Fatalf("Issue() error = %v", err)
	}

	// Now validate with current time (which is 10 hours later, past the 9-hour TTL)
	future := past.Add(10 * time.Hour)
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
	a := New("user", "pass", "secret", time.Hour, "", "")

	tests := []struct {
		name  string
		token string
	}{
		{"empty", ""},
		{"malformed", "not.a.jwt"},
		{"wrong signature", "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiJ1c2VyIiwiaWF0IjoxNzAwMDAwMDAwLCJleHAiOjE3MDAwMDAwMDB9.wrongsig"},
		{"wrong secret", func() string {
			a2 := New("user", "pass", "different-secret", time.Hour, "", "")
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
	a := New("user", "pass", "secret", time.Hour, "", "")

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
	a := New("user", "pass", "secret", time.Hour, "", "")
	a.now = func() time.Time { return now }

	token, expiresAt, err := a.Issue("user")
	if err != nil {
		t.Fatalf("Issue() error = %v", err)
	}

	// Access token TTL is now fixed at 9 hours
	if !expiresAt.Equal(now.Add(9 * time.Hour)) {
		t.Errorf("expiresAt = %v, want %v", expiresAt, now.Add(9*time.Hour))
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

func TestAuthenticator_Validate_FutureToken(t *testing.T) {
	// Token issued in the future should be rejected
	now := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
	a := New("user", "pass", "secret", time.Hour, "", "")
	a.now = func() time.Time { return now }

	// Create a token with iat in the future (2 minutes ahead)
	future := now.Add(2 * time.Minute)
	token := createTokenWithCustomIat(a, "user", future.Unix(), now.Add(time.Hour).Unix())

	_, err := a.Validate(token)
	if err == nil {
		t.Error("Validate() should return error for future token")
	}
	if err != ErrInvalidToken {
		t.Errorf("Validate() error = %v, want %v", err, ErrInvalidToken)
	}
}

func TestAuthenticator_Validate_AudienceMismatch(t *testing.T) {
	a := New("user", "pass", "secret", time.Hour, "expected-audience", "")

	token, _, err := a.Issue("user")
	if err != nil {
		t.Fatalf("Issue() error = %v", err)
	}

	// Validate with different audience
	a2 := New("user", "pass", "secret", time.Hour, "different-audience", "")
	_, err = a2.Validate(token)
	if err == nil {
		t.Error("Validate() should return error for audience mismatch")
	}
}

func TestAuthenticator_Validate_IssuerMismatch(t *testing.T) {
	a := New("user", "pass", "secret", time.Hour, "", "expected-issuer")

	token, _, err := a.Issue("user")
	if err != nil {
		t.Fatalf("Issue() error = %v", err)
	}

	// Validate with different issuer
	a2 := New("user", "pass", "secret", time.Hour, "", "different-issuer")
	_, err = a2.Validate(token)
	if err == nil {
		t.Error("Validate() should return error for issuer mismatch")
	}
}

func TestAuthenticator_Validate_AudienceAndIssuerMatch(t *testing.T) {
	a := New("user", "pass", "secret", time.Hour, "my-audience", "my-issuer")

	token, _, err := a.Issue("user")
	if err != nil {
		t.Fatalf("Issue() error = %v", err)
	}

	subject, err := a.Validate(token)
	if err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if subject != "user" {
		t.Errorf("subject = %q, want %q", subject, "user")
	}
}

func TestAuthenticator_IssueUser_AdminClaim(t *testing.T) {
	a := New("admin", "pass", "secret", time.Hour, "", "")

	// Test admin login
	adminResult, err := a.Login(context.Background(), "admin", "pass")
	if err != nil {
		t.Fatalf("Login() error = %v", err)
	}
	adminToken := adminResult.AccessToken
	claims, err := a.ValidateClaims(adminToken)
	if err != nil {
		t.Fatalf("ValidateClaims() error = %v", err)
	}
	if claims.Subject != "admin" || !claims.Admin {
		t.Errorf("admin claims = %+v, want subject admin and admin true", claims)
	}

	// Test that we can't login as non-admin user with "none" user manager
	// (The "none" user manager only has the admin user)
	_, err = a.Login(context.Background(), "alice@example.com", "pass")
	if err == nil {
		t.Error("Login() should fail for non-existent user with 'none' user manager")
	}
	if err != ErrInvalidCredentials {
		t.Errorf("Login() error = %v, want %v", err, ErrInvalidCredentials)
	}

	// The admin claim must not be emitted for ordinary users (if they existed).
	// We can't easily test this with the "none" user manager, but we can verify
	// the admin token has the admin claim.
	parts := strings.Split(adminToken, ".")
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(payload, &raw); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if !raw["admin"].(bool) {
		t.Errorf("admin claim should be present for admin user, got %v", raw)
	}
}

// createTokenWithCustomIat creates a token with a custom iat claim for testing
func createTokenWithCustomIat(a *Authenticator, subject string, iat, exp int64) string {
	header, _ := json.Marshal(jwtHeader{Algorithm: "HS256", Type: "JWT"})
	claimsData, _ := json.Marshal(jwtClaims{
		Subject:   subject,
		IssuedAt:  iat,
		ExpiresAt: exp,
	})

	encoding := base64.RawURLEncoding
	signingInput := encoding.EncodeToString(header) + "." + encoding.EncodeToString(claimsData)
	signature := a.sign(signingInput)
	return signingInput + "." + encoding.EncodeToString(signature)
}
