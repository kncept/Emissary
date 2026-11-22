package auth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"github.com/kncept/Emissary/mailer/components/usermanager"
)

// Account ID errors are mapped to JMAP method/HTTP errors by their callers.
var (
	ErrAccountAuthenticationRequired = errors.New("authentication required")
	ErrAccountForbidden              = errors.New("insufficient permissions to access account")
	ErrAccountNotFound               = errors.New("account not found")
)

// accountIDPrefix keeps account ids distinguishable from opaque object ids
// while remaining inside the RFC 8620 Id grammar.
const accountIDPrefix = "acct_"

// AccountIDForEmail derives the stable, opaque JMAP account id for a user. The
// email is normalised before hashing so case differences never create two
// accounts. The frontend implements the same rule for masquerade targets.
func AccountIDForEmail(email string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(email))))
	return accountIDPrefix + base64.RawURLEncoding.EncodeToString(sum[:])
}

// EffectiveAccountID returns the opaque account id for the claims. Older
// tokens do not carry the accountId claim; deriving it keeps them valid until
// they expire.
func (c *Claims) EffectiveAccountID() string {
	if c == nil {
		return ""
	}
	if c.AccountID != "" {
		return c.AccountID
	}
	if c.Subject == "" {
		return ""
	}
	return AccountIDForEmail(c.Subject)
}

// ResolveAccountID authorises a JMAP account identifier and returns the
// internal user id (the email address) used by storage. The public identifier
// is always the opaque hash; an unprivileged caller may only resolve their own
// account, while a user manager may masquerade as an existing account.
//
// For compatibility with pre-Phase-1 clients, the caller's own email address
// is also accepted as an alias. It is never advertised or returned.
func ResolveAccountID(ctx context.Context, claims *Claims, requested string, users usermanager.UserManager) (string, error) {
	if claims == nil || claims.Subject == "" {
		return "", ErrAccountAuthenticationRequired
	}
	requested = strings.TrimSpace(requested)
	ownAccount := claims.EffectiveAccountID()
	if requested == "" || requested == ownAccount {
		return claims.Subject, nil
	}
	if strings.EqualFold(requested, strings.TrimSpace(claims.Subject)) {
		return claims.Subject, nil
	}
	if !claims.EmissaryUserManagement {
		return "", ErrAccountForbidden
	}
	if users == nil {
		return "", ErrAccountNotFound
	}

	// Emissary-mastered user managers can enumerate accounts, so an opaque id
	// can be mapped back to the email without exposing the hash as a lookup
	// key in storage.
	if managed, ok := users.(usermanager.EmissaryAuthManager); ok {
		cursor := ""
		for {
			page, next, err := managed.List(ctx, cursor, 200)
			if err != nil {
				return "", fmt.Errorf("list accounts: %w", err)
			}
			for _, user := range page {
				if user != nil && AccountIDForEmail(user.Email) == requested {
					return user.Email, nil
				}
			}
			if next == "" || next == cursor {
				break
			}
			cursor = next
		}
		return "", ErrAccountNotFound
	}

	// External directories expose Lookup by email only. Accept an email
	// identifier there so administrator masquerade keeps working.
	if strings.Contains(requested, "@") {
		user, err := users.Lookup(ctx, requested)
		if err == nil && user != nil {
			if user.Email == "" {
				return requested, nil
			}
			return user.Email, nil
		}
	}
	return "", ErrAccountNotFound
}
