package auth

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/kncept/Emissary/mailer/usermanager"
)

var (
	ErrInvalidCredentials = errors.New("invalid username or password")
	ErrInvalidToken       = errors.New("invalid token")
	ErrExpiredToken       = errors.New("token expired")
)

const defaultTokenTTL = 24 * time.Hour

type jwtHeader struct {
	Algorithm string `json:"alg"`
	Type      string `json:"typ"`
}

// Claims is the verified content of an Emissary JWT. Subject is the user id;
// Admin is present (and true) only for the built-in administrator.
type Claims struct {
	Subject string `json:"sub"`
	// SID mirrors the subject for clients that refer to the user id as "sid".
	SID       string `json:"sid,omitempty"`
	Admin     bool   `json:"admin,omitempty"`
	IssuedAt  int64  `json:"iat"`
	ExpiresAt int64  `json:"exp"`
	Audience  string `json:"aud,omitempty"`
	Issuer    string `json:"iss,omitempty"`
}

// jwtClaims is kept as an alias for compatibility with older callers.
type jwtClaims = Claims

// Authenticator validates users through a pluggable user manager and issues
// HS256 JWTs.
type Authenticator struct {
	users    usermanager.Manager
	secret   []byte
	ttl      time.Duration
	now      func() time.Time
	audience string
	issuer   string
}

// New preserves the original single-account constructor. It is equivalent to
// NewWithUserManager with the "none" user manager.
func New(username, password, secret string, ttl time.Duration, audience, issuer string) *Authenticator {
	users := usermanager.NewNone(usermanager.Config{
		AdminUsername: username,
		AdminPassword: password,
	})
	return NewWithUserManager(users, secret, ttl, audience, issuer)
}

// NewWithUserManager creates an authenticator backed by users.
func NewWithUserManager(users usermanager.Manager, secret string, ttl time.Duration, audience, issuer string) *Authenticator {
	if ttl <= 0 {
		ttl = defaultTokenTTL
	}
	if users == nil {
		users = usermanager.NewNone(usermanager.Config{})
	}
	return &Authenticator{
		users:    users,
		secret:   []byte(secret),
		ttl:      ttl,
		now:      time.Now,
		audience: audience,
		issuer:   issuer,
	}
}

// UserManager returns the configured user manager.
func (a *Authenticator) UserManager() usermanager.Manager {
	return a.users
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
// callers that only know the subject; IssueUser should be preferred.
func (a *Authenticator) Issue(username string) (string, time.Time, error) {
	return a.IssueUser(&usermanager.User{
		ID:       username,
		Username: username,
		Admin:    a.users.IsAdmin(username),
	})
}

// IssueUser creates a signed token for user.
func (a *Authenticator) IssueUser(user *usermanager.User) (string, time.Time, error) {
	now := a.now().UTC()
	expiresAt := now.Add(a.ttl)

	header, err := json.Marshal(jwtHeader{Algorithm: "HS256", Type: "JWT"})
	if err != nil {
		return "", time.Time{}, err
	}
	claims := Claims{
		Subject:   user.ID,
		SID:       user.ID,
		Admin:     user.Admin,
		IssuedAt:  now.Unix(),
		ExpiresAt: expiresAt.Unix(),
	}
	if a.audience != "" {
		claims.Audience = a.audience
	}
	if a.issuer != "" {
		claims.Issuer = a.issuer
	}
	claimsData, err := json.Marshal(claims)
	if err != nil {
		return "", time.Time{}, err
	}

	encoding := base64.RawURLEncoding
	signingInput := encoding.EncodeToString(header) + "." + encoding.EncodeToString(claimsData)
	signature := a.sign(signingInput)
	return signingInput + "." + encoding.EncodeToString(signature), expiresAt, nil
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

// ValidateClaims verifies a token and returns all of its claims.
func (a *Authenticator) ValidateClaims(token string) (*Claims, error) {
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
