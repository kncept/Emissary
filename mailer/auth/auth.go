package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"
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

type jwtClaims struct {
	Subject   string `json:"sub"`
	IssuedAt  int64  `json:"iat"`
	ExpiresAt int64  `json:"exp"`
}

// Authenticator validates the single configured account and issues HS256 JWTs.
type Authenticator struct {
	username string
	password string
	secret   []byte
	ttl      time.Duration
	now      func() time.Time
}

func New(username, password, secret string, ttl time.Duration) *Authenticator {
	if ttl <= 0 {
		ttl = defaultTokenTTL
	}
	return &Authenticator{
		username: username,
		password: password,
		secret:   []byte(secret),
		ttl:      ttl,
		now:      time.Now,
	}
}

// Authenticate compares credentials in constant time.
func (a *Authenticator) Authenticate(username, password string) bool {
	return equal(username, a.username) && equal(password, a.password)
}

// Issue creates a signed token for the configured user.
func (a *Authenticator) Issue(username string) (string, time.Time, error) {
	now := a.now().UTC()
	expiresAt := now.Add(a.ttl)

	header, err := json.Marshal(jwtHeader{Algorithm: "HS256", Type: "JWT"})
	if err != nil {
		return "", time.Time{}, err
	}
	claims, err := json.Marshal(jwtClaims{
		Subject:   username,
		IssuedAt:  now.Unix(),
		ExpiresAt: expiresAt.Unix(),
	})
	if err != nil {
		return "", time.Time{}, err
	}

	encoding := base64.RawURLEncoding
	signingInput := encoding.EncodeToString(header) + "." + encoding.EncodeToString(claims)
	signature := a.sign(signingInput)
	return signingInput + "." + encoding.EncodeToString(signature), expiresAt, nil
}

// Validate verifies a token and returns its subject.
func (a *Authenticator) Validate(token string) (string, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return "", ErrInvalidToken
	}

	encoding := base64.RawURLEncoding
	signature, err := encoding.DecodeString(parts[2])
	if err != nil {
		return "", ErrInvalidToken
	}
	if !hmac.Equal(signature, a.sign(parts[0]+"."+parts[1])) {
		return "", ErrInvalidToken
	}

	headerData, err := encoding.DecodeString(parts[0])
	if err != nil {
		return "", ErrInvalidToken
	}
	var header jwtHeader
	if err := json.Unmarshal(headerData, &header); err != nil || header.Algorithm != "HS256" {
		return "", ErrInvalidToken
	}

	claimsData, err := encoding.DecodeString(parts[1])
	if err != nil {
		return "", ErrInvalidToken
	}
	var claims jwtClaims
	if err := json.Unmarshal(claimsData, &claims); err != nil {
		return "", ErrInvalidToken
	}
	if claims.Subject == "" || claims.ExpiresAt == 0 {
		return "", ErrInvalidToken
	}
	if a.now().UTC().Unix() >= claims.ExpiresAt {
		return "", ErrExpiredToken
	}
	return claims.Subject, nil
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
