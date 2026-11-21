package auth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/kncept/Emissary/mailer/components/session"
	"github.com/kncept/Emissary/mailer/components/usermanager"
)

type disabledUserManager struct{}

func (disabledUserManager) Name() string { return "disabled" }

func (disabledUserManager) Authenticate(context.Context, string, string) (*usermanager.User, error) {
	return nil, usermanager.ErrUserDisabled
}

func (disabledUserManager) Lookup(context.Context, string) (*usermanager.User, error) {
	return nil, usermanager.ErrUserDisabled
}

func (disabledUserManager) Autocomplete(context.Context, string) ([]string, error) {
	return nil, nil
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
	a := NewWithUserManager(disabledUserManager{}, nil, "secret", time.Hour, "", "", "")

	_, err := a.Login(context.Background(), "user@example.com", "password")
	if !errors.Is(err, usermanager.ErrUserDisabled) {
		t.Errorf("Login() error = %v, want ErrUserDisabled", err)
	}
}

type masteredUserManager struct {
	user *usermanager.EmissaryUser
}

func (m masteredUserManager) Name() string { return "mastered" }

func (m masteredUserManager) Authenticate(context.Context, string, string) (*usermanager.User, error) {
	return &m.user.User, nil
}

func (m masteredUserManager) Lookup(context.Context, string) (*usermanager.User, error) {
	return &m.user.User, nil
}

func (masteredUserManager) Autocomplete(context.Context, string) ([]string, error) {
	return nil, nil
}

func (masteredUserManager) List(context.Context, string, int) ([]*usermanager.EmissaryUser, string, error) {
	return nil, "", nil
}

func (masteredUserManager) Create(context.Context, *usermanager.EmissaryUser) error { return nil }
func (masteredUserManager) Save(context.Context, *usermanager.EmissaryUser) error   { return nil }

func (m masteredUserManager) Get(context.Context, string) (*usermanager.EmissaryUser, error) {
	return m.user, nil
}

func (masteredUserManager) Delete(context.Context, string) error { return nil }
func (masteredUserManager) ChangePassword(context.Context, string, string, string) error {
	return nil
}

func TestAuthenticator_EmissaryUserManagementClaim(t *testing.T) {
	for _, tc := range []struct {
		name          string
		userManager   bool
		systemManager bool
		wantMgmt      bool
		wantSysMgmt   bool
	}{
		{"user manager only", true, false, true, false},
		{"system manager only", false, true, false, true},
		{"both managers", true, true, true, true},
		{"ordinary user", false, false, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := NewWithUserManager(masteredUserManager{user: &usermanager.EmissaryUser{
				User: usermanager.User{
					Email:         "user@example.com",
					SystemManager: tc.systemManager,
				},
				UserManager: tc.userManager,
			}}, nil, "secret", time.Hour, "", "", "admin@example.com")

			result, err := a.Login(context.Background(), "user@example.com", "password")
			if err != nil {
				t.Fatalf("Login() error = %v", err)
			}
			claims, err := a.ValidateClaims(result.AccessToken)
			if err != nil {
				t.Fatalf("ValidateClaims() error = %v", err)
			}
			if claims.EmissaryUserManagement != tc.wantMgmt {
				t.Errorf("EmissaryUserManagement = %v, want %v", claims.EmissaryUserManagement, tc.wantMgmt)
			}
			if claims.SystemManager != tc.wantSysMgmt {
				t.Errorf("SystemManager = %v, want %v", claims.SystemManager, tc.wantSysMgmt)
			}
		})
	}
}

func TestAuthenticator_BuiltInAdminClaims(t *testing.T) {
	const admin = "admin@example.com"

	t.Run("mastered backend", func(t *testing.T) {
		a := NewWithUserManager(masteredUserManager{user: &usermanager.EmissaryUser{
			User: usermanager.User{Email: admin, SystemManager: true},
		}}, nil, "secret", time.Hour, "", "", admin)

		result, err := a.Login(context.Background(), admin, "password")
		if err != nil {
			t.Fatalf("Login() error = %v", err)
		}
		claims, err := a.ValidateClaims(result.AccessToken)
		if err != nil {
			t.Fatalf("ValidateClaims() error = %v", err)
		}
		if !claims.SystemManager {
			t.Error("SystemManager = false, want true for built-in admin")
		}
		if !claims.EmissaryUserManagement {
			t.Error("EmissaryUserManagement = false, want true when backend is mastered")
		}
	})

	t.Run("non-mastered backend", func(t *testing.T) {
		a := NewWithUserManager(usermanager.NewNone(usermanager.Config{
			AdminUsername: admin,
			AdminPassword: "password",
		}), nil, "secret", time.Hour, "", "", admin)

		result, err := a.Login(context.Background(), admin, "password")
		if err != nil {
			t.Fatalf("Login() error = %v", err)
		}
		claims, err := a.ValidateClaims(result.AccessToken)
		if err != nil {
			t.Fatalf("ValidateClaims() error = %v", err)
		}
		if !claims.SystemManager {
			t.Error("SystemManager = false, want true for built-in admin")
		}
		if !claims.EmissaryUserManagement {
			t.Error("EmissaryUserManagement = false, want true for built-in admin")
		}
	})
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
	a := NewWithUserManager(usermanager.NewNone(usermanager.Config{AdminUsername: "user", AdminPassword: "pass"}), nil, "secret", 9*time.Hour, "", "", "user")

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

func TestAuthenticator_IssueUser_SystemManagerClaim(t *testing.T) {
	a := New("admin", "pass", "secret", time.Hour, "", "")

	// The built-in administrator is a System Manager.
	adminResult, err := a.Login(context.Background(), "admin", "pass")
	if err != nil {
		t.Fatalf("Login() error = %v", err)
	}
	adminToken := adminResult.AccessToken
	claims, err := a.ValidateClaims(adminToken)
	if err != nil {
		t.Fatalf("ValidateClaims() error = %v", err)
	}
	if claims.Subject != "admin" || !claims.SystemManager {
		t.Errorf("admin claims = %+v, want subject admin and systemManager true", claims)
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

	// The obsolete admin claim must not be emitted; System Manager and User
	// Manager are separate privilege claims.
	parts := strings.Split(adminToken, ".")
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(payload, &raw); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if _, ok := raw["admin"]; ok {
		t.Errorf("obsolete admin claim must not be emitted, got %v", raw)
	}
	if raw["systemManager"] != true {
		t.Errorf("systemManager claim = %v, want true", raw["systemManager"])
	}
}

func decodeTokenClaims(t *testing.T, token string) Claims {
	t.Helper()
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("token %q is not a JWT", token)
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("decode token payload: %v", err)
	}
	var claims Claims
	if err := json.Unmarshal(payload, &claims); err != nil {
		t.Fatalf("unmarshal token claims: %v", err)
	}
	return claims
}

func TestAuthenticator_Refresh(t *testing.T) {
	a := New("user", "pass", "secret", time.Hour, "", "")
	ctx := context.Background()
	now := time.Date(2099, 1, 1, 12, 0, 0, 0, time.UTC)
	a.SetNow(func() time.Time { return now })

	first, err := a.Login(ctx, "user", "pass")
	if err != nil {
		t.Fatalf("Login() error = %v", err)
	}
	firstAccess := decodeTokenClaims(t, first.AccessToken)
	firstRefresh := decodeTokenClaims(t, first.RefreshToken)
	if firstRefresh.SID != firstAccess.SID {
		t.Errorf("refresh sid = %q, want %q", firstRefresh.SID, firstAccess.SID)
	}
	if firstRefresh.Issue != 1 {
		t.Errorf("refresh issue = %d, want 1", firstRefresh.Issue)
	}
	if firstRefresh.NotBefore != now.Add(refreshTokenNBF).Unix() {
		t.Errorf("refresh nbf = %d, want %d", firstRefresh.NotBefore, now.Add(refreshTokenNBF).Unix())
	}

	// The refresh token is not usable during its eight-hour not-before window.
	if _, err := a.Refresh(ctx, first.RefreshToken); !errors.Is(err, ErrNotYetValid) {
		t.Errorf("early Refresh() error = %v, want ErrNotYetValid", err)
	}

	now = now.Add(refreshTokenNBF)
	second, err := a.Refresh(ctx, first.RefreshToken)
	if err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}
	if second.AccessToken == first.AccessToken || second.RefreshToken == first.RefreshToken {
		t.Error("Refresh() reused a token from the previous pair")
	}
	secondRefresh := decodeTokenClaims(t, second.RefreshToken)
	if secondRefresh.SID != firstRefresh.SID {
		t.Errorf("refresh sid changed: %q -> %q", firstRefresh.SID, secondRefresh.SID)
	}
	if secondRefresh.Issue != 2 {
		t.Errorf("refresh issue = %d, want 2", secondRefresh.Issue)
	}
	if secondRefresh.NotBefore != now.Add(refreshTokenNBF).Unix() {
		t.Errorf("refresh nbf = %d, want %d", secondRefresh.NotBefore, now.Add(refreshTokenNBF).Unix())
	}
	sess, err := a.SessionStore().Get(ctx, firstRefresh.SID)
	if err != nil {
		t.Fatalf("session lookup: %v", err)
	}
	if sess.Issue != 2 {
		t.Errorf("session issue = %d, want 2", sess.Issue)
	}

	if _, err := a.Refresh(ctx, first.RefreshToken); err == nil {
		t.Error("Refresh() accepted a spent refresh token")
	}

	now = now.Add(refreshTokenNBF)
	third, err := a.Refresh(ctx, second.RefreshToken)
	if err != nil {
		t.Fatalf("second Refresh() error = %v", err)
	}
	thirdRefresh := decodeTokenClaims(t, third.RefreshToken)
	if thirdRefresh.SID != firstRefresh.SID {
		t.Errorf("refresh sid changed on second refresh: %q", thirdRefresh.SID)
	}
	if thirdRefresh.Issue != 3 {
		t.Errorf("refresh issue = %d, want 3", thirdRefresh.Issue)
	}
}

func TestAuthenticator_RefreshRejectsAccessToken(t *testing.T) {
	a := New("user", "pass", "secret", time.Hour, "", "")
	result, err := a.Login(context.Background(), "user", "pass")
	if err != nil {
		t.Fatalf("Login() error = %v", err)
	}
	if _, err := a.Refresh(context.Background(), result.AccessToken); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("Refresh() with access token error = %v, want ErrInvalidToken", err)
	}
	if _, err := a.ValidateClaims(result.RefreshToken); err == nil {
		t.Error("ValidateClaims() accepted a refresh token")
	}
}

func TestAuthenticator_LogoutRevokesRefreshToken(t *testing.T) {
	a := New("user", "pass", "secret", time.Hour, "", "")
	ctx := context.Background()
	now := time.Date(2099, 1, 1, 12, 0, 0, 0, time.UTC)
	a.SetNow(func() time.Time { return now })

	result, err := a.Login(ctx, "user", "pass")
	if err != nil {
		t.Fatalf("Login() error = %v", err)
	}
	claims, err := a.ValidateClaims(result.AccessToken)
	if err != nil {
		t.Fatalf("ValidateClaims() error = %v", err)
	}

	if err := a.Logout(ctx, claims.SID); err != nil {
		t.Fatalf("Logout() error = %v", err)
	}
	if _, err := a.SessionStore().Get(ctx, claims.SID); !errors.Is(err, session.ErrSessionNotFound) {
		t.Errorf("session still present after Logout(): %v", err)
	}
	now = now.Add(refreshTokenNBF)
	if _, err := a.Refresh(ctx, result.RefreshToken); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("Refresh() after Logout() error = %v, want ErrInvalidToken", err)
	}
}

func TestAuthenticator_ValidateClaimsRejectsRefreshOnlyClaims(t *testing.T) {
	a := New("user", "pass", "secret", time.Hour, "", "")
	now := time.Now()
	for name, claims := range map[string]Claims{
		"issue":     {Subject: "user", SID: "s", Issue: 1, IssuedAt: now.Add(-time.Hour).Unix(), ExpiresAt: now.Add(time.Hour).Unix()},
		"notBefore": {Subject: "user", SID: "s", IssuedAt: now.Add(-time.Hour).Unix(), NotBefore: now.Add(-time.Minute).Unix(), ExpiresAt: now.Add(time.Hour).Unix()},
	} {
		t.Run(name, func(t *testing.T) {
			token, err := a.signToken(claims)
			if err != nil {
				t.Fatalf("signToken: %v", err)
			}
			if _, err := a.ValidateClaims(token); !errors.Is(err, ErrInvalidToken) {
				t.Errorf("ValidateClaims() error = %v, want ErrInvalidToken", err)
			}
		})
	}
}

func TestAuthenticator_LogoutRejectsEmptySessionID(t *testing.T) {
	a := New("user", "pass", "secret", time.Hour, "", "")
	if err := a.Logout(context.Background(), ""); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("Logout() error = %v, want ErrInvalidToken", err)
	}
}

func signedTokenWithHeader(t *testing.T, a *Authenticator, header jwtHeader, claims Claims) string {
	t.Helper()
	headerData, err := json.Marshal(header)
	if err != nil {
		t.Fatalf("marshal header: %v", err)
	}
	claimsData, err := json.Marshal(claims)
	if err != nil {
		t.Fatalf("marshal claims: %v", err)
	}
	encoding := base64.RawURLEncoding
	input := encoding.EncodeToString(headerData) + "." + encoding.EncodeToString(claimsData)
	return input + "." + encoding.EncodeToString(a.sign(input))
}

func TestAuthenticator_ValidatesEveryJWTClaim(t *testing.T) {
	a := New("user", "pass", "secret", time.Hour, "aud", "iss")
	now := time.Date(2099, 1, 1, 12, 0, 0, 0, time.UTC)
	a.SetNow(func() time.Time { return now })

	valid := Claims{
		Subject:   "user",
		SID:       "session-1",
		IssuedAt:  now.Add(-time.Minute).Unix(),
		ExpiresAt: now.Add(time.Hour).Unix(),
		Audience:  "aud",
		Issuer:    "iss",
	}
	validToken, err := a.signToken(valid)
	if err != nil {
		t.Fatalf("signToken: %v", err)
	}
	if _, err := a.validateToken(validToken); err != nil {
		t.Fatalf("valid token rejected: %v", err)
	}

	modified := func(fn func(*Claims)) string {
		claims := valid
		fn(&claims)
		token, err := a.signToken(claims)
		if err != nil {
			t.Fatalf("signToken: %v", err)
		}
		return token
	}

	tampered := validToken[:len(validToken)-1]
	if validToken[len(validToken)-1] == 'A' {
		tampered += "B"
	} else {
		tampered += "A"
	}

	for _, tc := range []struct {
		name  string
		token string
		want  error
	}{
		{"bad signature", tampered, ErrInvalidToken},
		{"bad algorithm", signedTokenWithHeader(t, a, jwtHeader{Algorithm: "HS512", Type: "JWT"}, valid), ErrInvalidToken},
		{"bad type", signedTokenWithHeader(t, a, jwtHeader{Algorithm: "HS256", Type: "NOT-JWT"}, valid), ErrInvalidToken},
		{"missing subject", modified(func(c *Claims) { c.Subject = "" }), ErrInvalidToken},
		{"missing session id", modified(func(c *Claims) { c.SID = "" }), ErrInvalidToken},
		{"missing issued at", modified(func(c *Claims) { c.IssuedAt = 0 }), ErrInvalidToken},
		{"missing expiry", modified(func(c *Claims) { c.ExpiresAt = 0 }), ErrInvalidToken},
		{"issued in future", modified(func(c *Claims) {
			c.IssuedAt = now.Add(time.Hour).Unix()
			c.ExpiresAt = now.Add(2 * time.Hour).Unix()
		}), ErrInvalidToken},
		{"expired", modified(func(c *Claims) {
			c.IssuedAt = now.Add(-2 * time.Hour).Unix()
			c.ExpiresAt = now.Add(-time.Minute).Unix()
		}), ErrExpiredToken},
		{"not before in future", modified(func(c *Claims) { c.NotBefore = now.Add(time.Minute).Unix() }), ErrNotYetValid},
		{"not before after expiry", modified(func(c *Claims) { c.NotBefore = now.Add(2 * time.Hour).Unix() }), ErrInvalidToken},
		{"wrong audience", modified(func(c *Claims) { c.Audience = "other" }), ErrInvalidToken},
		{"wrong issuer", modified(func(c *Claims) { c.Issuer = "other" }), ErrInvalidToken},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := a.validateToken(tc.token)
			if !errors.Is(err, tc.want) {
				t.Errorf("validateToken() error = %v, want %v", err, tc.want)
			}
		})
	}
}

// createTokenWithCustomIat creates a token with a custom iat claim for testing
func createTokenWithCustomIat(a *Authenticator, subject string, iat, exp int64) string {
	header, _ := json.Marshal(jwtHeader{Algorithm: "HS256", Type: "JWT"})
	claimsData, _ := json.Marshal(jwtClaims{
		Subject:   subject,
		SID:       "test-session",
		IssuedAt:  iat,
		ExpiresAt: exp,
	})

	encoding := base64.RawURLEncoding
	signingInput := encoding.EncodeToString(header) + "." + encoding.EncodeToString(claimsData)
	signature := a.sign(signingInput)
	return signingInput + "." + encoding.EncodeToString(signature)
}
