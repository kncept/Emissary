package jmap

import (
	"context"

	"github.com/kncept/Emissary/mailer/auth"
	"github.com/kncept/Emissary/mailer/jmap/blob"
	"github.com/kncept/Emissary/mailer/jmap/core"
	"github.com/kncept/Emissary/mailer/jmap/email"
	"github.com/kncept/Emissary/mailer/jmap/emailsubmission"
	"github.com/kncept/Emissary/mailer/jmap/identity"
	"github.com/kncept/Emissary/mailer/jmap/internal/base"
	"github.com/kncept/Emissary/mailer/jmap/internal/model"
	"github.com/kncept/Emissary/mailer/jmap/mailbox"
	"github.com/kncept/Emissary/mailer/jmap/pushsubscription"
	"github.com/kncept/Emissary/mailer/jmap/searchsnippet"
	"github.com/kncept/Emissary/mailer/jmap/thread"
	"github.com/kncept/Emissary/mailer/jmap/vacationresponse"
	"github.com/kncept/Emissary/mailer/components/metadata"
	"github.com/kncept/Emissary/mailer/components/sender"
	"github.com/kncept/Emissary/mailer/components/storage"
	"github.com/kncept/Emissary/mailer/components/usermanager"
)

// Distinct local names let the composition root embed all method handlers
// without their promoted methods colliding with the embedded field names.
type (
	coreMethods             = *core.Handler
	mailboxMethods          = *mailbox.Handler
	threadMethods           = *thread.Handler
	emailMethods            = *email.Handler
	searchSnippetMethods    = *searchsnippet.Handler
	identityMethods         = *identity.Handler
	submissionMethods       = *emailsubmission.Handler
	vacationMethods         = *vacationresponse.Handler
	pushSubscriptionMethods = *pushsubscription.Handler
	blobMethods             = *blob.Handler
)

// Handler assembles the per-object method handlers into a single MethodHandler.
// Each subpackage owns its object types and logic; this package only wires them
// to the shared dependencies and the dispatcher.
type Handler struct {
	coreMethods
	mailboxMethods
	threadMethods
	emailMethods
	searchSnippetMethods
	identityMethods
	submissionMethods
	vacationMethods
	pushSubscriptionMethods
	blobMethods
}

// NewHandler creates a JMAP method handler backed by the application services.
// An optional FromValidator restricts submissions to verified identities.
func NewHandler(store storage.Storage, meta metadata.MetadataStore, userMgr usermanager.UserManager, mailSender sender.Sender, validators ...FromValidator) *Handler {
	deps := base.NewDeps(store, meta, userMgr, mailSender, validators...)
	emails := email.New(deps)
	return &Handler{
		coreMethods:             core.New(),
		mailboxMethods:          mailbox.New(deps),
		threadMethods:           thread.New(deps),
		emailMethods:            emails,
		searchSnippetMethods:    searchsnippet.New(deps),
		identityMethods:         identity.New(deps),
		submissionMethods:       emailsubmission.New(deps, emails),
		vacationMethods:         vacationresponse.New(deps),
		pushSubscriptionMethods: pushsubscription.New(deps),
		blobMethods:             blob.New(deps),
	}
}

var _ MethodHandler = (*Handler)(nil)
var _ ImplicitSetHandler = (*Handler)(nil)

// FromValidator is re-exported for callers that supply an identity validator.
type FromValidator = base.FromValidator

// ContextWithClaims attaches the authenticated caller's claims to ctx.
func ContextWithClaims(ctx context.Context, claims *auth.Claims) context.Context {
	return base.ContextWithClaims(ctx, claims)
}

// ClaimsFromContext returns the authenticated caller's claims, if present.
func ClaimsFromContext(ctx context.Context) *auth.Claims {
	return base.ClaimsFromContext(ctx)
}

// Type aliases keep the root package's published model available to callers
// while the implementations live in the object-specific subpackages.
type (
	EmailFilter    = email.EmailFilter
	Mailbox        = mailbox.Mailbox
	MailboxRights  = mailbox.MailboxRights
	JMAPEmail      = email.JMAPEmail
	EmailAddress   = model.EmailAddress
	EmailBodyPart  = email.EmailBodyPart
	EmailBodyValue = email.EmailBodyValue
	EmailHeader    = email.EmailHeader
	Thread         = thread.Thread
	Identity       = identity.Identity
)

// applyKeywordPatch is kept as an internal alias so the historical test suite
// can exercise the email subpackage's patch logic through the facade.
var applyKeywordPatch = email.ApplyKeywordPatch

// countUnreadInMailbox preserves the facade's historical internal hook. The
// implementation lives in the mailbox subpackage.
func (h *Handler) countUnreadInMailbox(ctx context.Context, accountID, mailboxID string) int {
	return h.mailboxMethods.CountUnreadInMailbox(ctx, accountID, mailboxID)
}
