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
	"strings"
	"time"

	"github.com/kncept/Emissary/mailer/session"
	"github.com/kncept/Emissary/mailer/usermanager"
)

var (
	ErrInvalidCredentials = errors.New("invalid username or password")
	ErrInvalidToken       = errors.New("invalid token")
	ErrExpiredToken       = errors.New("token expired")
)

const (
	defaultTokenTTL = 24 * time.Hour
	accessTokenTTL  = 9 * time.Hour
	refreshTokenTTL = 30 * 24 * time.Hour
	sessionIDBytes  = 16
)

type jwtHeader struct {
	Algorithm string `json:"alg"`
	Type      string `json:"typ"`
}

// Claims is the verified content of an Emissary JWT. Subject is the user id;
// Admin is present (and true) only for the built-in administrator.
// EmissaryUserManagement indicates the user can manage other users (requires Emissary-mastered backend).
type Claims struct {
	Subject                string `json:"sub"`
	SID                    string `json:"sid,omitempty"`
	Admin                  bool   `json:"admin,omitempty"`
	EmissaryUserManagement bool   `json:"EmissaryUserManagement,omitempty"`
	Issue                  int    `json:"issue,omitempty"`
	IssuedAt               int64  `json:"iat"`
	ExpiresAt              int64  `json:"exp"`
	Audience               string `json:"aud,omitempty"`
	Issuer                 string `json:"iss,omitempty"`
}

// jwtClaims is kept as an alias for compatibility with older callers.
type jwtClaims = Claims

// Authenticator validates users through a pluggable user manager and issues
// HS256 JWTs.
type Authenticator struct {
	users        usermanager.UserManager
	sessionStore session.SessionStore
	secret       []byte
	ttl          time.Duration // legacy, not used for new tokens
	now          func() time.Time
	audience     string
	issuer       string
}

// New preserves the original single-account constructor. It is equivalent to
// NewWithUserManager with the "none" user manager.
func New(username, password, secret string, ttl time.Duration, audience, issuer string) *Authenticator {
	users := usermanager.NewNone(usermanager.Config{
		AdminUsername: username,
		AdminPassword: password,
	})
	return NewWithUserManager(users, nil, secret, ttl, audience, issuer)
}

// NewWithUserManager creates an authenticator backed by users.
func NewWithUserManager(users usermanager.UserManager, sessionStore session.SessionStore, secret string, ttl time.Duration, audience, issuer string) *Authenticator {
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
		users:        users,
		sessionStore: sessionStore,
		secret:       []byte(secret),
		ttl:          ttl,
		now:          time.Now,
		audience:     audience,
		issuer:       issuer,
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
	result, err := a.issueTokens(context.Background(), user)
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
			return nil, err
		}
		return nil, ErrInvalidCredentials
	}

	return a.issueTokens(ctx, user)
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

	// Increment the issue number
	newIssue := sess.Issue + 1
	if err := a.sessionStore.UpdateIssue(ctx, sess.ID, sess.Issue, newIssue); err != nil {
		return nil, ErrInvalidToken
	}

	// Update session with new issue
	sess.Issue = newIssue

	return a.issueTokens(ctx, user)
}

// issueTokens creates a new session (or updates existing) and issues both tokens.
func (a *Authenticator) issueTokens(ctx context.Context, user *usermanager.User) (*LoginResult, error) {
	now := a.now().UTC()

	// Generate session ID
	sidBytes := make([]byte, sessionIDBytes)
	if _, err := rand.Read(sidBytes); err != nil {
		return nil, err
	}
	sid := base64.RawURLEncoding.EncodeToString(sidBytes)

	// Only an Emissary-mastered user manager can manage its own users, and
	// only administrators of that store receive the management claim.
	var emissaryUserMgmt bool
	if _, ok := a.users.(usermanager.EmissaryAuthManager); ok {
		emissaryUserMgmt = user.Admin
	}

	// Create or update session
	sess := &session.Session{
		ID:        sid,
		UserEmail: user.ID,
		Issue:     1,
		CreatedAt: now,
		ExpiresAt: now.Add(refreshTokenTTL),
	}
	if err := a.sessionStore.Create(ctx, sess); err != nil {
		return nil, err
	}

	// Issue access token (9 hours)
	accessExpiresAt := now.Add(accessTokenTTL)
	accessToken, err := a.signToken(Claims{
		Subject:                user.ID,
		SID:                    sid,
		Admin:                  user.Admin,
		EmissaryUserManagement: emissaryUserMgmt,
		IssuedAt:               now.Unix(),
		ExpiresAt:              accessExpiresAt.Unix(),
		Audience:               a.audience,
		Issuer:                 a.issuer,
	})
	if err != nil {
		return nil, err
	}

	// Issue refresh token (30 days) with issue number
	refreshExpiresAt := now.Add(refreshTokenTTL)
	refreshToken, err := a.signToken(Claims{
		Subject:   user.ID,
		SID:       sid,
		Issue:     1,
		IssuedAt:  now.Unix(),
		ExpiresAt: refreshExpiresAt.Unix(),
		Audience:  a.audience,
		Issuer:    a.issuer,
	})
	if err != nil {
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

	return claims, nil
}

// Validate verifies a token and returns its subject. It is retained for
// callers that only need the user id; use ValidateClaims for the admin flag.
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

	// Access tokens should not have an issue number
	if claims.Issue != 0 {
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
	if err := json.Unmarshal(headerData, &header); err != nil || header.Algorithm != "HS256" {
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
	if claims.Subject == "" || claims.ExpiresAt == 0 {
		return nil, ErrInvalidToken
	}

	now := a.now().UTC().Unix()

	// Validate expiration
	if now >= claims.ExpiresAt {
		return nil, ErrExpiredToken
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
