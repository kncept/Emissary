package auth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/kncept/Emissary/mailer/components/session"
	"github.com/kncept/Emissary/mailer/components/usermanager"
)

var (
	ErrInvalidCredentials = errors.New("invalid username or password")
	ErrInvalidToken       = errors.New("invalid token")
	ErrExpiredToken       = errors.New("token expired")
	ErrNotYetValid        = errors.New("token not yet valid")
)

const (
	defaultTokenTTL = 24 * time.Hour
	accessTokenTTL  = 9 * time.Hour
	refreshTokenTTL = 30 * 24 * time.Hour
	// A refresh token cannot be exchanged until it is eight hours old. The
	// access token lives nine hours, so there is always a window to refresh.
	refreshTokenNBF = 8 * time.Hour
	sessionIDBytes  = 16
)

type jwtHeader struct {
	Algorithm string `json:"alg"`
	Type      string `json:"typ"`
}

// Claims is the verified content of an Emissary JWT. Subject is the user id,
// which is the user's email address.
// SystemManager indicates the user has system management privileges (support
// tools). EmissaryUserManagement indicates the user can manage other users and
// masquerade as them (requires an Emissary-mastered backend).
type Claims struct {
	Subject                string `json:"sub"`
	SID                    string `json:"sid,omitempty"`
	SystemManager          bool   `json:"systemManager,omitempty"`
	EmissaryUserManagement bool   `json:"EmissaryUserManagement,omitempty"`
	Issue                  int    `json:"issue,omitempty"`
	IssuedAt               int64  `json:"iat"`
	NotBefore              int64  `json:"nbf,omitempty"`
	ExpiresAt              int64  `json:"exp"`
	Audience               string `json:"aud,omitempty"`
	Issuer                 string `json:"iss,omitempty"`
}

// jwtClaims is kept as an alias for compatibility with older callers.
type jwtClaims = Claims

// Authenticator validates users through a pluggable user manager and issues
// HS256 JWTs.
type Authenticator struct {
	users         usermanager.UserManager
	sessionStore  session.SessionStore
	secret        []byte
	ttl           time.Duration // legacy, not used for new tokens
	now           func() time.Time
	audience      string
	issuer        string
	adminUsername string
}

// New preserves the original single-account constructor. It is equivalent to
// NewWithUserManager with the "none" user manager.
func New(username, password, secret string, ttl time.Duration, audience, issuer string) *Authenticator {
	users := usermanager.NewNone(usermanager.Config{
		AdminUsername: username,
		AdminPassword: password,
	})
	return NewWithUserManager(users, nil, secret, ttl, audience, issuer, username)
}

// NewWithUserManager creates an authenticator backed by users.
func NewWithUserManager(users usermanager.UserManager, sessionStore session.SessionStore, secret string, ttl time.Duration, audience, issuer string, adminUsername string) *Authenticator {
	if ttl <= 0 {
		ttl = defaultTokenTTL
	}
	if users == nil {
		users = usermanager.NewNone(usermanager.Config{})
	}
	if sessionStore == nil {
		sessionStore = session.NewMemorySessionStore()
	}
	return &Authenticator{
		users:         users,
		sessionStore:  sessionStore,
		secret:        []byte(secret),
		ttl:           ttl,
		now:           time.Now,
		audience:      audience,
		issuer:        issuer,
		adminUsername: adminUsername,
	}
}

// UserManager returns the configured user manager.
func (a *Authenticator) UserManager() usermanager.UserManager {
	return a.users
}

// SessionStore returns the configured session store.
func (a *Authenticator) SessionStore() session.SessionStore {
	return a.sessionStore
}

// SetNow overrides the time source used to sign and verify tokens. A nil
// source restores the system clock. It is primarily useful for deterministic
// tests of token lifetimes.
func (a *Authenticator) SetNow(now func() time.Time) {
	if now == nil {
		now = time.Now
	}
	a.now = now
}

// Authenticate reports whether the credentials are valid. It is retained for
// callers that only need a boolean; use AuthenticateUser when the user details
// are required.
func (a *Authenticator) Authenticate(username, password string) bool {
	_, err := a.AuthenticateUser(context.Background(), username, password)
	return err == nil
}

// AuthenticateUser validates credentials and returns the authenticated user.
func (a *Authenticator) AuthenticateUser(ctx context.Context, username, password string) (*usermanager.User, error) {
	return a.users.Authenticate(ctx, username, password)
}

// Issue creates a signed token for the configured user. It is retained for
// callers that only know the subject; issueTokens should be preferred.
func (a *Authenticator) Issue(username string) (string, time.Time, error) {
	user, err := a.users.Lookup(context.Background(), username)
	if err != nil {
		return "", time.Time{}, err
	}
	result, err := a.newSession(context.Background(), user)
	if err != nil {
		return "", time.Time{}, err
	}
	return result.AccessToken, result.ExpiresAt, nil
}

// LoginResult contains the tokens issued on successful login.
type LoginResult struct {
	AccessToken  string
	RefreshToken string
	TokenType    string
	ExpiresAt    time.Time
}

// Login authenticates the user and issues an access token and refresh token.
func (a *Authenticator) Login(ctx context.Context, username, password string) (*LoginResult, error) {
	user, err := a.users.Authenticate(ctx, username, password)
	if err != nil {
		if errors.Is(err, usermanager.ErrUserDisabled) {
			slog.Warn("Login rejected: user is disabled", "username", username)
			return nil, err
		}
		return nil, ErrInvalidCredentials
	}

	result, err := a.newSession(ctx, user)
	if err != nil {
		return nil, err
	}
	slog.Debug("Login succeeded", "username", username)
	return result, nil
}

// Refresh validates the refresh token and issues new access and refresh tokens.
func (a *Authenticator) Refresh(ctx context.Context, refreshToken string) (*LoginResult, error) {
	claims, err := a.ValidateRefreshToken(refreshToken)
	if err != nil {
		return nil, err
	}

	// Get the session from the store
	sess, err := a.sessionStore.Get(ctx, claims.SID)
	if err != nil {
		return nil, ErrInvalidToken
	}

	// Verify the issue number matches
	if claims.Issue != sess.Issue {
		return nil, ErrInvalidToken
	}

	// Verify the subject matches
	if claims.Subject != sess.UserEmail {
		return nil, ErrInvalidToken
	}

	// Look up the user
	user, err := a.users.Lookup(ctx, claims.Subject)
	if err != nil {
		return nil, ErrInvalidToken
	}

	// Never consume an issue number if the remaining session is too short to
	// carry a refresh token with its eight-hour not-before window.
	now := a.now().UTC()
	if !now.Add(refreshTokenNBF).Before(sess.ExpiresAt) {
		return nil, ErrInvalidToken
	}

	// Increment the issue number
	newIssue := sess.Issue + 1
	if err := a.sessionStore.UpdateIssue(ctx, sess.ID, sess.Issue, newIssue); err != nil {
		return nil, ErrInvalidToken
	}

	// Reuse the same session: its id is stable for the session's lifetime and
	// the issue number is what advances with every refresh.
	sess.Issue = newIssue

	result, err := a.issueTokenPair(ctx, user, sess)
	if err != nil {
		return nil, err
	}
	slog.Debug("Refresh token issued", "username", user.Email)
	return result, nil
}

// Logout revokes the session named in the access token's sid claim. Since the
// session id is stable across refreshes, this invalidates every refresh token
// in that session; access tokens remain valid until they expire because they
// are stateless.
func (a *Authenticator) Logout(ctx context.Context, sid string) error {
	if sid == "" {
		return ErrInvalidToken
	}
	if err := a.sessionStore.Delete(ctx, sid); err != nil {
		return err
	}
	slog.Debug("Logout revoked session")
	return nil
}

// RevokeUserSessions revokes every session belonging to email, preventing any
// of its refresh tokens from being exchanged. It is called whenever a password
// is set or reset so previously issued sessions cannot survive the change.
func (a *Authenticator) RevokeUserSessions(ctx context.Context, email string) error {
	if err := a.sessionStore.DeleteByUser(ctx, email); err != nil {
		return err
	}
	slog.Info("Revoked all sessions for user", "username", email)
	return nil
}

// newSession starts a session for a newly authenticated user and issues its
// first token pair. The session id never changes afterwards; only the refresh
// issue number advances.
func (a *Authenticator) newSession(ctx context.Context, user *usermanager.User) (*LoginResult, error) {
	now := a.now().UTC()

	sidBytes := make([]byte, sessionIDBytes)
	if _, err := rand.Read(sidBytes); err != nil {
		return nil, err
	}
	sid := base64.RawURLEncoding.EncodeToString(sidBytes)

	sess := &session.Session{
		ID:        sid,
		UserEmail: user.Email,
		Issue:     1,
		CreatedAt: now,
		ExpiresAt: now.Add(refreshTokenTTL),
	}
	if err := a.sessionStore.Create(ctx, sess); err != nil {
		slog.Error("Failed to create login session", "username", user.Email, "err", err)
		return nil, err
	}

	return a.issueTokenPair(ctx, user, sess)
}

// issueTokenPair signs an access token and the current refresh token for sess.
// The refresh token carries the session's current issue number, an eight-hour
// not-before time, and expires with the session.
func (a *Authenticator) issueTokenPair(ctx context.Context, user *usermanager.User, sess *session.Session) (*LoginResult, error) {
	now := a.now().UTC()
	if !now.Add(refreshTokenNBF).Before(sess.ExpiresAt) {
		return nil, ErrInvalidToken
	}

	// Determine claims based on user type:
	// - The built-in admin always has system management and user management
	//   privileges, regardless of the configured user manager backend.
	// - Other users get both flags from their Emissary-mastered record (when
	//   one is configured).
	isBuiltInAdmin := a.adminUsername != "" && strings.EqualFold(user.Email, a.adminUsername)
	systemManager := isBuiltInAdmin
	emissaryUserMgmt := isBuiltInAdmin

	if em, ok := a.users.(usermanager.EmissaryAuthManager); ok && !isBuiltInAdmin {
		if eu, err := em.Get(ctx, user.Email); err == nil && eu != nil {
			emissaryUserMgmt = eu.UserManager
			systemManager = eu.SystemManager
		}
	}

	accessExpiresAt := now.Add(accessTokenTTL)
	accessToken, err := a.signToken(Claims{
		Subject:                user.Email,
		SID:                    sess.ID,
		SystemManager:          systemManager,
		EmissaryUserManagement: emissaryUserMgmt,
		IssuedAt:               now.Unix(),
		ExpiresAt:              accessExpiresAt.Unix(),
		Audience:               a.audience,
		Issuer:                 a.issuer,
	})
	if err != nil {
		slog.Error("Failed to sign access token", "username", user.Email, "err", err)
		return nil, err
	}

	refreshToken, err := a.signToken(Claims{
		Subject:   user.Email,
		SID:       sess.ID,
		Issue:     sess.Issue,
		IssuedAt:  now.Unix(),
		NotBefore: now.Add(refreshTokenNBF).Unix(),
		ExpiresAt: sess.ExpiresAt.Unix(),
		Audience:  a.audience,
		Issuer:    a.issuer,
	})
	if err != nil {
		slog.Error("Failed to sign refresh token", "username", user.Email, "err", err)
		return nil, err
	}

	return &LoginResult{
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		TokenType:    "Bearer",
		ExpiresAt:    accessExpiresAt,
	}, nil
}

// ValidateRefreshToken validates a refresh token and returns its claims.
func (a *Authenticator) ValidateRefreshToken(token string) (*Claims, error) {
	claims, err := a.validateToken(token)
	if err != nil {
		return nil, err
	}

	// Refresh tokens must have an issue number
	if claims.Issue == 0 {
		return nil, ErrInvalidToken
	}

	// Refresh tokens must have a session ID
	if claims.SID == "" {
		return nil, ErrInvalidToken
	}

	// Refresh tokens must carry the eight-hour not-before time. This also
	// stops an access token (which has no issue or nbf) being used here.
	if claims.NotBefore == 0 {
		return nil, ErrInvalidToken
	}

	return claims, nil
}

// Validate verifies a token and returns its subject. It is retained for
// callers that only need the user id; use ValidateClaims for permission claims.
func (a *Authenticator) Validate(token string) (string, error) {
	claims, err := a.ValidateClaims(token)
	if err != nil {
		return "", err
	}
	return claims.Subject, nil
}

// ValidateClaims verifies an access token and returns all of its claims.
func (a *Authenticator) ValidateClaims(token string) (*Claims, error) {
	claims, err := a.validateToken(token)
	if err != nil {
		return nil, err
	}

	// Access tokens are neither refresh tokens nor time-gated.
	if claims.Issue != 0 || claims.NotBefore != 0 {
		return nil, ErrInvalidToken
	}

	return claims, nil
}

func (a *Authenticator) validateToken(token string) (*Claims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, ErrInvalidToken
	}

	encoding := base64.RawURLEncoding
	signature, err := encoding.DecodeString(parts[2])
	if err != nil {
		return nil, ErrInvalidToken
	}
	if !hmac.Equal(signature, a.sign(parts[0]+"."+parts[1])) {
		return nil, ErrInvalidToken
	}

	headerData, err := encoding.DecodeString(parts[0])
	if err != nil {
		return nil, ErrInvalidToken
	}
	var header jwtHeader
	if err := json.Unmarshal(headerData, &header); err != nil || header.Algorithm != "HS256" || header.Type != "JWT" {
		return nil, ErrInvalidToken
	}

	claimsData, err := encoding.DecodeString(parts[1])
	if err != nil {
		return nil, ErrInvalidToken
	}
	var claims Claims
	if err := json.Unmarshal(claimsData, &claims); err != nil {
		return nil, ErrInvalidToken
	}
	if claims.Subject == "" || claims.SID == "" || claims.IssuedAt == 0 || claims.ExpiresAt == 0 {
		return nil, ErrInvalidToken
	}
	if claims.IssuedAt >= claims.ExpiresAt || (claims.NotBefore != 0 && claims.NotBefore >= claims.ExpiresAt) {
		return nil, ErrInvalidToken
	}

	now := a.now().UTC().Unix()

	// Validate expiration
	if now >= claims.ExpiresAt {
		return nil, ErrExpiredToken
	}

	// A refresh token cannot be exchanged before its nbf time. No skew is
	// allowed here: "no early refreshes" means exactly that.
	if claims.NotBefore > now {
		return nil, ErrNotYetValid
	}

	// Validate issued at (iat) - reject tokens from the future (with small skew tolerance)
	if claims.IssuedAt > now+60 { // Allow 60 seconds clock skew
		return nil, ErrInvalidToken
	}

	// Validate audience if configured
	if a.audience != "" && claims.Audience != a.audience {
		return nil, ErrInvalidToken
	}

	// Validate issuer if configured
	if a.issuer != "" && claims.Issuer != a.issuer {
		return nil, ErrInvalidToken
	}

	return &claims, nil
}

func (a *Authenticator) signToken(claims Claims) (string, error) {
	header, err := json.Marshal(jwtHeader{Algorithm: "HS256", Type: "JWT"})
	if err != nil {
		return "", err
	}
	claimsData, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}

	encoding := base64.RawURLEncoding
	signingInput := encoding.EncodeToString(header) + "." + encoding.EncodeToString(claimsData)
	signature := a.sign(signingInput)
	return signingInput + "." + encoding.EncodeToString(signature), nil
}

func (a *Authenticator) sign(input string) []byte {
	mac := hmac.New(sha256.New, a.secret)
	mac.Write([]byte(input))
	return mac.Sum(nil)
}

func equal(left, right string) bool {
	leftHash := sha256.Sum256([]byte(left))
	rightHash := sha256.Sum256([]byte(right))
	return subtle.ConstantTimeCompare(leftHash[:], rightHash[:]) == 1
}
