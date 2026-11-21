// Package base holds the protocol-agnostic pieces shared by the JMAP method
// subpackages. It deliberately knows nothing about individual JMAP object
// types: each subpackage builds its own snapshots and calls into the tracker
// with plain maps.
package base

import (
	"context"
	"strings"

	"github.com/kncept/Emissary/mailer/auth"
	"github.com/kncept/Emissary/mailer/components/metadata"
	"github.com/kncept/Emissary/mailer/components/sender"
	"github.com/kncept/Emissary/mailer/components/storage"
	"github.com/kncept/Emissary/mailer/components/usermanager"
)

// FromValidator checks that a From address may be used for sending. Callers
// that have no identity source (unit tests, local development) may omit it.
type FromValidator interface {
	Validate(ctx context.Context, from string) error
}

// Deps collects the application services every JMAP method subpackage needs.
// A single instance is shared by all subpackages so they also share one change
// tracker and therefore agree on state tokens.
type Deps struct {
	Storage       storage.Storage
	Metadata      metadata.MetadataStore
	UserMgr       usermanager.UserManager
	Sender        sender.Sender
	FromValidator FromValidator
	Changes       *Tracker
}

// NewDeps builds the shared dependencies for a JMAP handler.
func NewDeps(store storage.Storage, meta metadata.MetadataStore, userMgr usermanager.UserManager, mailSender sender.Sender, validators ...FromValidator) *Deps {
	deps := &Deps{
		Storage:  store,
		Metadata: meta,
		UserMgr:  userMgr,
		Sender:   mailSender,
		Changes:  NewTracker(store),
	}
	for _, validator := range validators {
		if validator != nil {
			deps.FromValidator = validator
			break
		}
	}
	return deps
}

type claimsContextKey struct{}

// ContextWithClaims attaches the authenticated caller's claims to ctx.
func ContextWithClaims(ctx context.Context, claims *auth.Claims) context.Context {
	return context.WithValue(ctx, claimsContextKey{}, claims)
}

// ClaimsFromContext returns the authenticated caller's claims, if present.
func ClaimsFromContext(ctx context.Context) *auth.Claims {
	claims, _ := ctx.Value(claimsContextKey{}).(*auth.Claims)
	return claims
}

// LockedFrom returns the From address that mail created or submitted for
// accountID must use: the authenticated user's own address (the JWT sub), or
// the target account when a caller with EmissaryUserManagement masquerades as
// a different account. masquerading reports whether the target account differs
// from the caller's own. There is no configured From address and a
// client-supplied From is never honoured.
func LockedFrom(ctx context.Context, accountID string) (from string, masquerading bool) {
	claims := ClaimsFromContext(ctx)
	userAccount := accountID
	if claims != nil && claims.Subject != "" {
		userAccount = claims.Subject
	}
	masquerading = claims != nil && claims.EmissaryUserManagement && !strings.EqualFold(accountID, userAccount)
	from = userAccount
	if masquerading {
		from = accountID
	}
	return
}
