package jmap

import (
	"context"
	"encoding/json"
)

// MethodHandler defines the interface for handling individual JMAP method calls.
// Each method corresponds to a JMAP method name (e.g., "Mailbox/get", "Email/set").
type MethodHandler interface {
	// Core methods
	CoreEcho(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error)

	// Mailbox methods
	MailboxGet(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error)
	MailboxSet(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error)
	MailboxChanges(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error)
	MailboxQuery(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error)
	MailboxQueryChanges(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error)

	// Thread methods
	ThreadGet(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error)
	ThreadChanges(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error)

	// Email methods
	EmailGet(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error)
	EmailSet(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error)
	EmailChanges(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error)
	EmailQuery(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error)
	EmailQueryChanges(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error)
	EmailCopy(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error)
	EmailImport(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error)
	EmailParse(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error)

	// SearchSnippet methods
	SearchSnippetGet(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error)

	// Identity methods
	IdentityGet(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error)
	IdentitySet(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error)
	IdentityChanges(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error)

	// EmailSubmission methods
	EmailSubmissionGet(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error)
	EmailSubmissionSet(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error)
	EmailSubmissionChanges(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error)
	EmailSubmissionQuery(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error)
	EmailSubmissionQueryChanges(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error)

	// VacationResponse methods
	VacationResponseGet(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error)
	VacationResponseSet(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error)
	VacationResponseChanges(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error)

	// PushSubscription methods
	PushSubscriptionGet(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error)
	PushSubscriptionSet(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error)
	PushSubscriptionChanges(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error)
}

// UnimplementedHandler is a default MethodHandler that panics for all methods.
type UnimplementedHandler struct{}

func (h *UnimplementedHandler) panic(method string) {
	panic("unimplemented: " + method)
}

// Core methods
func (h *UnimplementedHandler) CoreEcho(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error) {
	h.panic("Core/echo")
	return nil, nil
}

// Mailbox methods
func (h *UnimplementedHandler) MailboxGet(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error) {
	h.panic("Mailbox/get")
	return nil, nil
}
func (h *UnimplementedHandler) MailboxSet(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error) {
	h.panic("Mailbox/set")
	return nil, nil
}
func (h *UnimplementedHandler) MailboxChanges(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error) {
	h.panic("Mailbox/changes")
	return nil, nil
}
func (h *UnimplementedHandler) MailboxQuery(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error) {
	h.panic("Mailbox/query")
	return nil, nil
}
func (h *UnimplementedHandler) MailboxQueryChanges(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error) {
	h.panic("Mailbox/queryChanges")
	return nil, nil
}

// Thread methods
func (h *UnimplementedHandler) ThreadGet(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error) {
	h.panic("Thread/get")
	return nil, nil
}
func (h *UnimplementedHandler) ThreadChanges(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error) {
	h.panic("Thread/changes")
	return nil, nil
}

// Email methods
func (h *UnimplementedHandler) EmailGet(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error) {
	h.panic("Email/get")
	return nil, nil
}
func (h *UnimplementedHandler) EmailSet(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error) {
	h.panic("Email/set")
	return nil, nil
}
func (h *UnimplementedHandler) EmailChanges(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error) {
	h.panic("Email/changes")
	return nil, nil
}
func (h *UnimplementedHandler) EmailQuery(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error) {
	h.panic("Email/query")
	return nil, nil
}
func (h *UnimplementedHandler) EmailQueryChanges(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error) {
	h.panic("Email/queryChanges")
	return nil, nil
}
func (h *UnimplementedHandler) EmailCopy(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error) {
	h.panic("Email/copy")
	return nil, nil
}
func (h *UnimplementedHandler) EmailImport(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error) {
	h.panic("Email/import")
	return nil, nil
}
func (h *UnimplementedHandler) EmailParse(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error) {
	h.panic("Email/parse")
	return nil, nil
}

// SearchSnippet methods
func (h *UnimplementedHandler) SearchSnippetGet(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error) {
	h.panic("SearchSnippet/get")
	return nil, nil
}

// Identity methods
func (h *UnimplementedHandler) IdentityGet(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error) {
	h.panic("Identity/get")
	return nil, nil
}
func (h *UnimplementedHandler) IdentitySet(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error) {
	h.panic("Identity/set")
	return nil, nil
}
func (h *UnimplementedHandler) IdentityChanges(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error) {
	h.panic("Identity/changes")
	return nil, nil
}

// EmailSubmission methods
func (h *UnimplementedHandler) EmailSubmissionGet(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error) {
	h.panic("EmailSubmission/get")
	return nil, nil
}
func (h *UnimplementedHandler) EmailSubmissionSet(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error) {
	h.panic("EmailSubmission/set")
	return nil, nil
}
func (h *UnimplementedHandler) EmailSubmissionChanges(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error) {
	h.panic("EmailSubmission/changes")
	return nil, nil
}
func (h *UnimplementedHandler) EmailSubmissionQuery(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error) {
	h.panic("EmailSubmission/query")
	return nil, nil
}
func (h *UnimplementedHandler) EmailSubmissionQueryChanges(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error) {
	h.panic("EmailSubmission/queryChanges")
	return nil, nil
}

// VacationResponse methods
func (h *UnimplementedHandler) VacationResponseGet(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error) {
	h.panic("VacationResponse/get")
	return nil, nil
}
func (h *UnimplementedHandler) VacationResponseSet(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error) {
	h.panic("VacationResponse/set")
	return nil, nil
}
func (h *UnimplementedHandler) VacationResponseChanges(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error) {
	h.panic("VacationResponse/changes")
	return nil, nil
}

// PushSubscription methods
func (h *UnimplementedHandler) PushSubscriptionGet(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error) {
	h.panic("PushSubscription/get")
	return nil, nil
}
func (h *UnimplementedHandler) PushSubscriptionSet(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error) {
	h.panic("PushSubscription/set")
	return nil, nil
}
func (h *UnimplementedHandler) PushSubscriptionChanges(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error) {
	h.panic("PushSubscription/changes")
	return nil, nil
}

// Ensure UnimplementedHandler implements MethodHandler
var _ MethodHandler = (*UnimplementedHandler)(nil)