package jmap

import (
	"context"
	"errors"

	"github.com/kncept/Emissary/mailer/components/metadata"
	"github.com/kncept/Emissary/mailer/components/sender"
	"github.com/kncept/Emissary/mailer/components/storage"
	"github.com/kncept/Emissary/mailer/components/usermanager"
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
	deps *base.Deps

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
		deps:                    deps,
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

// AccountSupportsCapability reports the capabilities the Session advertises
// for an account. Every Emissary account supports the same data-type
// capabilities, so this is the single source shared with the dispatcher's
// accountNotSupportedByMethod check.
func (h *Handler) AccountSupportsCapability(_ context.Context, _ string, capability string) bool {
	return IsSupportedCapability(capability)
}

// RefreshState synchronises every storage-backed type state before push reads
// it. Each refresh is best effort: a single failing store must not suppress
// notifications for the other types.
func (h *Handler) RefreshState(ctx context.Context, accountID string) error {
	refreshers := []func(context.Context, string) error{
		func(ctx context.Context, accountID string) error {
			_, err := h.emailMethods.SyncState(ctx, accountID)
			return err
		},
		func(ctx context.Context, accountID string) error {
			_, err := h.threadMethods.SyncState(ctx, accountID)
			return err
		},
		func(ctx context.Context, accountID string) error {
			_, err := h.mailboxMethods.SyncState(ctx, accountID)
			return err
		},
		func(ctx context.Context, accountID string) error {
			_, err := h.identityMethods.SyncState(ctx, accountID)
			return err
		},
		func(ctx context.Context, accountID string) error {
			_, err := h.vacationMethods.SyncState(ctx, accountID)
			return err
		},
	}
	var errs []error
	for _, refresh := range refreshers {
		if err := refresh(ctx, accountID); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// States exposes the shared tracker's current type states to the push hub.
func (h *Handler) States(ctx context.Context, accountID string) (map[base.StateKind]string, error) {
	return h.deps.Changes.States(ctx, accountID)
}

// SetSessionChecker wires session-expiry detection into the PushSubscription
// handlers. The HTTP composition root calls this with the configured session
// store; unit tests that omit it keep their existing behaviour.
func (h *Handler) SetSessionChecker(checker base.SessionChecker) {
	if h != nil && h.deps != nil {
		h.deps.CheckSession = checker
	}
}

var _ MethodHandler = (*Handler)(nil)
var _ ImplicitSetHandler = (*Handler)(nil)

// FromValidator is re-exported for callers that supply an identity validator.
type FromValidator = base.FromValidator

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
