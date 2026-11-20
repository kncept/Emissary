package jmap

import (
	"github.com/kncept/Emissary/mailer/jmap/blob"
	"github.com/kncept/Emissary/mailer/jmap/core"
	"github.com/kncept/Emissary/mailer/jmap/email"
	"github.com/kncept/Emissary/mailer/jmap/emailsubmission"
	"github.com/kncept/Emissary/mailer/jmap/identity"
	"github.com/kncept/Emissary/mailer/jmap/internal/base"
	"github.com/kncept/Emissary/mailer/jmap/mailbox"
	"github.com/kncept/Emissary/mailer/jmap/pushsubscription"
	"github.com/kncept/Emissary/mailer/jmap/searchsnippet"
	"github.com/kncept/Emissary/mailer/jmap/thread"
	"github.com/kncept/Emissary/mailer/jmap/vacationresponse"
)

// MethodName constants are re-exported from the subpackage that owns each
// object type so callers do not need to know about the internal split.
const (
	MethodCoreEcho = core.MethodEcho

	MethodMailboxGet          = mailbox.MethodGet
	MethodMailboxSet          = mailbox.MethodSet
	MethodMailboxChanges      = mailbox.MethodChanges
	MethodMailboxQuery        = mailbox.MethodQuery
	MethodMailboxQueryChanges = mailbox.MethodQueryChanges

	MethodThreadGet     = thread.MethodGet
	MethodThreadChanges = thread.MethodChanges

	MethodEmailGet          = email.MethodGet
	MethodEmailSet          = email.MethodSet
	MethodEmailChanges      = email.MethodChanges
	MethodEmailQuery        = email.MethodQuery
	MethodEmailQueryChanges = email.MethodQueryChanges
	MethodEmailCopy         = email.MethodCopy
	MethodEmailImport       = email.MethodImport
	MethodEmailParse        = email.MethodParse

	MethodSearchSnippetGet = searchsnippet.MethodGet

	MethodIdentityGet     = identity.MethodGet
	MethodIdentitySet     = identity.MethodSet
	MethodIdentityChanges = identity.MethodChanges

	MethodEmailSubmissionGet          = emailsubmission.MethodGet
	MethodEmailSubmissionSet          = emailsubmission.MethodSet
	MethodEmailSubmissionChanges      = emailsubmission.MethodChanges
	MethodEmailSubmissionQuery        = emailsubmission.MethodQuery
	MethodEmailSubmissionQueryChanges = emailsubmission.MethodQueryChanges

	MethodVacationResponseGet     = vacationresponse.MethodGet
	MethodVacationResponseSet     = vacationresponse.MethodSet
	MethodVacationResponseChanges = vacationresponse.MethodChanges

	MethodPushSubscriptionGet = pushsubscription.MethodGet
	MethodPushSubscriptionSet = pushsubscription.MethodSet

	MethodBlobCopy = blob.MethodCopy

	// stateEmail is used by the historical change-tracking tests.
	stateEmail = base.StateEmail
)

// AllMethods returns a list of all JMAP method names.
func AllMethods() []string {
	groups := [][]string{
		core.Methods(),
		mailbox.Methods(),
		thread.Methods(),
		email.Methods(),
		searchsnippet.Methods(),
		identity.Methods(),
		emailsubmission.Methods(),
		vacationresponse.Methods(),
		pushsubscription.Methods(),
		blob.Methods(),
	}
	all := make([]string, 0)
	for _, group := range groups {
		all = append(all, group...)
	}
	return all
}
