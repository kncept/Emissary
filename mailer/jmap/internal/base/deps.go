// Package base holds the protocol-agnostic pieces shared by the JMAP method
// subpackages. It deliberately knows nothing about individual JMAP object
// types: each subpackage builds its own snapshots and calls into the tracker
// with plain maps.
package base

import (
	"context"
	"net/http"
	"strings"
	"time"

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

// SessionChecker reports whether a login session is still active. The JMAP
// composition root wires it to the session store so account-less PushSubscription
// handlers can destroy subscriptions when their credentials expire (RFC 8620
// section 7.2). It is optional for unit tests that do not use sessions.
type SessionChecker func(ctx context.Context, sid string) bool

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
	// HTTPClient is used for outbound JMAP push verification requests. Tests
	// may replace it with a client configured for a local TLS server.
	HTTPClient *http.Client
	// CheckSession, when set, lets PushSubscription handlers detect expired
	// login sessions and destroy their subscriptions.
	CheckSession SessionChecker
}

// NewDeps builds the shared dependencies for a JMAP handler.
func NewDeps(store storage.Storage, meta metadata.MetadataStore, userMgr usermanager.UserManager, mailSender sender.Sender, validators ...FromValidator) *Deps {
	deps := &Deps{
		Storage:    store,
		Metadata:   meta,
		UserMgr:    userMgr,
		Sender:     mailSender,
		Changes:    NewTracker(store),
		HTTPClient: &http.Client{Timeout: 10 * time.Second},
	}
	for _, validator := range validators {
		if validator != nil {
			deps.FromValidator = validator
			break
		}
	}
	return deps
}

// CanAccessAccount reports whether the caller may operate on accountID. Users
// without user-management privileges may only access their own account, as in
// the JMAP dispatcher's per-call account check.
func CanAccessAccount(ctx context.Context, accountID string) bool {
	claims := auth.ClaimsFromContext(ctx)
	return claims == nil || claims.EmissaryUserManagement || claims.Subject == accountID
}

// LockedFrom returns the From address that mail created or submitted for
// accountID must use: the authenticated user's own address (the JWT sub), or
// the target account when a caller with EmissaryUserManagement masquerades as
// a different account. masquerading reports whether the target account differs
// from the caller's own. There is no configured From address and a
// client-supplied From is never honoured.
func LockedFrom(ctx context.Context, accountID string) (from string, masquerading bool) {
	claims := auth.ClaimsFromContext(ctx)
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
