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

// Capability URNs advertised by the Session object. Keep this list in sync
// with handleJMAPSession: a capability may only be accepted in `using` when
// the Session advertises it.
const (
	CapabilityCore             = "urn:ietf:params:jmap:core"
	CapabilityMail             = "urn:ietf:params:jmap:mail"
	CapabilitySubmission       = "urn:ietf:params:jmap:submission"
	CapabilityVacationResponse = "urn:ietf:params:jmap:vacationresponse"
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

	MethodVacationResponseGet = vacationresponse.MethodGet
	MethodVacationResponseSet = vacationresponse.MethodSet

	MethodPushSubscriptionGet = pushsubscription.MethodGet
	MethodPushSubscriptionSet = pushsubscription.MethodSet

	MethodBlobCopy = blob.MethodCopy

	// stateEmail is used by the historical change-tracking tests.
	stateEmail = base.StateEmail
)

// methodCapabilities is the runtime mirror of the x-jmap-methods extension in
// api/jmap.yaml. A method may only be dispatched when its capability appears
// in the Request's `using` array (RFC 8620 section 3.3).
var methodCapabilities = map[string]string{
	MethodCoreEcho:            CapabilityCore,
	MethodBlobCopy:            CapabilityCore,
	MethodPushSubscriptionGet: CapabilityCore,
	MethodPushSubscriptionSet: CapabilityCore,

	MethodMailboxGet:          CapabilityMail,
	MethodMailboxSet:          CapabilityMail,
	MethodMailboxChanges:      CapabilityMail,
	MethodMailboxQuery:        CapabilityMail,
	MethodMailboxQueryChanges: CapabilityMail,
	MethodThreadGet:           CapabilityMail,
	MethodThreadChanges:       CapabilityMail,
	MethodEmailGet:            CapabilityMail,
	MethodEmailSet:            CapabilityMail,
	MethodEmailChanges:        CapabilityMail,
	MethodEmailQuery:          CapabilityMail,
	MethodEmailQueryChanges:   CapabilityMail,
	MethodEmailCopy:           CapabilityMail,
	MethodEmailImport:         CapabilityMail,
	MethodEmailParse:          CapabilityMail,
	MethodSearchSnippetGet:    CapabilityMail,

	MethodIdentityGet:                 CapabilitySubmission,
	MethodIdentitySet:                 CapabilitySubmission,
	MethodIdentityChanges:             CapabilitySubmission,
	MethodEmailSubmissionGet:          CapabilitySubmission,
	MethodEmailSubmissionSet:          CapabilitySubmission,
	MethodEmailSubmissionChanges:      CapabilitySubmission,
	MethodEmailSubmissionQuery:        CapabilitySubmission,
	MethodEmailSubmissionQueryChanges: CapabilitySubmission,

	MethodVacationResponseGet: CapabilityVacationResponse,
	MethodVacationResponseSet: CapabilityVacationResponse,
}

// AllCapabilities returns the capability URNs advertised by the Session. The
// OpenAPI x-jmap-capabilities manifest is checked against this list so the
// Session and the specification cannot drift apart.
func AllCapabilities() []string {
	return []string{
		CapabilityCore,
		CapabilityMail,
		CapabilitySubmission,
		CapabilityVacationResponse,
	}
}

// MethodCapability returns the capability that must be opted into before the
// named method may be dispatched. Unknown methods are left to the dispatcher's
// unknownMethod path.
func MethodCapability(name string) (string, bool) {
	capability, ok := methodCapabilities[name]
	return capability, ok
}

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
