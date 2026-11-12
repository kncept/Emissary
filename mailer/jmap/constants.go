package jmap

// MethodName constants for all JMAP methods (RFC 8621)
const (
	// Core
	MethodCoreEcho = "Core/echo"

	// Mailbox
	MethodMailboxGet          = "Mailbox/get"
	MethodMailboxSet          = "Mailbox/set"
	MethodMailboxChanges      = "Mailbox/changes"
	MethodMailboxQuery        = "Mailbox/query"
	MethodMailboxQueryChanges = "Mailbox/queryChanges"

	// Thread
	MethodThreadGet     = "Thread/get"
	MethodThreadChanges = "Thread/changes"

	// Email
	MethodEmailGet          = "Email/get"
	MethodEmailSet          = "Email/set"
	MethodEmailChanges      = "Email/changes"
	MethodEmailQuery        = "Email/query"
	MethodEmailQueryChanges = "Email/queryChanges"
	MethodEmailCopy         = "Email/copy"
	MethodEmailImport       = "Email/import"
	MethodEmailParse        = "Email/parse"

	// SearchSnippet
	MethodSearchSnippetGet = "SearchSnippet/get"

	// Identity
	MethodIdentityGet     = "Identity/get"
	MethodIdentitySet     = "Identity/set"
	MethodIdentityChanges = "Identity/changes"

	// EmailSubmission
	MethodEmailSubmissionGet          = "EmailSubmission/get"
	MethodEmailSubmissionSet          = "EmailSubmission/set"
	MethodEmailSubmissionChanges      = "EmailSubmission/changes"
	MethodEmailSubmissionQuery        = "EmailSubmission/query"
	MethodEmailSubmissionQueryChanges = "EmailSubmission/queryChanges"

	// VacationResponse
	MethodVacationResponseGet     = "VacationResponse/get"
	MethodVacationResponseSet     = "VacationResponse/set"
	MethodVacationResponseChanges = "VacationResponse/changes"

	// PushSubscription
	MethodPushSubscriptionGet     = "PushSubscription/get"
	MethodPushSubscriptionSet     = "PushSubscription/set"
	MethodPushSubscriptionChanges = "PushSubscription/changes"
)

// AllMethods returns a list of all JMAP method names.
func AllMethods() []string {
	return []string{
		MethodCoreEcho,
		MethodMailboxGet, MethodMailboxSet, MethodMailboxChanges, MethodMailboxQuery, MethodMailboxQueryChanges,
		MethodThreadGet, MethodThreadChanges,
		MethodEmailGet, MethodEmailSet, MethodEmailChanges, MethodEmailQuery, MethodEmailQueryChanges,
		MethodEmailCopy, MethodEmailImport, MethodEmailParse,
		MethodSearchSnippetGet,
		MethodIdentityGet, MethodIdentitySet, MethodIdentityChanges,
		MethodEmailSubmissionGet, MethodEmailSubmissionSet, MethodEmailSubmissionChanges,
		MethodEmailSubmissionQuery, MethodEmailSubmissionQueryChanges,
		MethodVacationResponseGet, MethodVacationResponseSet, MethodVacationResponseChanges,
		MethodPushSubscriptionGet, MethodPushSubscriptionSet, MethodPushSubscriptionChanges,
	}
}