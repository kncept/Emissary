package jmap

import (
	"context"
	"encoding/json"

	"github.com/kncept/Emissary/mailer/jmap/blob"
	"github.com/kncept/Emissary/mailer/jmap/core"
	"github.com/kncept/Emissary/mailer/jmap/email"
	"github.com/kncept/Emissary/mailer/jmap/emailsubmission"
	"github.com/kncept/Emissary/mailer/jmap/identity"
	"github.com/kncept/Emissary/mailer/jmap/mailbox"
	"github.com/kncept/Emissary/mailer/jmap/pushsubscription"
	"github.com/kncept/Emissary/mailer/jmap/searchsnippet"
	"github.com/kncept/Emissary/mailer/jmap/thread"
	"github.com/kncept/Emissary/mailer/jmap/vacationresponse"
)

// MethodHandler is the interface for handling individual JMAP method calls.
// It is the union of the method contracts owned by each object subpackage.
type MethodHandler interface {
	core.MethodHandler
	mailbox.MethodHandler
	thread.MethodHandler
	email.MethodHandler
	searchsnippet.MethodHandler
	identity.MethodHandler
	emailsubmission.MethodHandler
	vacationresponse.MethodHandler
	pushsubscription.MethodHandler
	blob.MethodHandler
}

// ImplicitSetHandler is re-exported for dispatchers and tests that need the
// optional second response emitted by EmailSubmission/set.
type ImplicitSetHandler = emailsubmission.ImplicitSetHandler

// UnimplementedHandler is a compatibility shim that panics for every method.
// New code should implement the smaller per-object MethodHandler interfaces
// instead.
type UnimplementedHandler struct{}

func (h *UnimplementedHandler) panic(method string) {
	panic("unimplemented: " + method)
}

func (h *UnimplementedHandler) CoreEcho(context.Context, string, json.RawMessage) (json.RawMessage, error) {
	h.panic(core.MethodEcho)
	return nil, nil
}

func (h *UnimplementedHandler) MailboxGet(context.Context, string, json.RawMessage) (json.RawMessage, error) {
	h.panic(mailbox.MethodGet)
	return nil, nil
}
func (h *UnimplementedHandler) MailboxSet(context.Context, string, json.RawMessage) (json.RawMessage, error) {
	h.panic(mailbox.MethodSet)
	return nil, nil
}
func (h *UnimplementedHandler) MailboxChanges(context.Context, string, json.RawMessage) (json.RawMessage, error) {
	h.panic(mailbox.MethodChanges)
	return nil, nil
}
func (h *UnimplementedHandler) MailboxQuery(context.Context, string, json.RawMessage) (json.RawMessage, error) {
	h.panic(mailbox.MethodQuery)
	return nil, nil
}
func (h *UnimplementedHandler) MailboxQueryChanges(context.Context, string, json.RawMessage) (json.RawMessage, error) {
	h.panic(mailbox.MethodQueryChanges)
	return nil, nil
}

func (h *UnimplementedHandler) ThreadGet(context.Context, string, json.RawMessage) (json.RawMessage, error) {
	h.panic(thread.MethodGet)
	return nil, nil
}
func (h *UnimplementedHandler) ThreadChanges(context.Context, string, json.RawMessage) (json.RawMessage, error) {
	h.panic(thread.MethodChanges)
	return nil, nil
}

func (h *UnimplementedHandler) EmailGet(context.Context, string, json.RawMessage) (json.RawMessage, error) {
	h.panic(email.MethodGet)
	return nil, nil
}
func (h *UnimplementedHandler) EmailSet(context.Context, string, json.RawMessage) (json.RawMessage, error) {
	h.panic(email.MethodSet)
	return nil, nil
}
func (h *UnimplementedHandler) EmailChanges(context.Context, string, json.RawMessage) (json.RawMessage, error) {
	h.panic(email.MethodChanges)
	return nil, nil
}
func (h *UnimplementedHandler) EmailQuery(context.Context, string, json.RawMessage) (json.RawMessage, error) {
	h.panic(email.MethodQuery)
	return nil, nil
}
func (h *UnimplementedHandler) EmailQueryChanges(context.Context, string, json.RawMessage) (json.RawMessage, error) {
	h.panic(email.MethodQueryChanges)
	return nil, nil
}
func (h *UnimplementedHandler) EmailCopy(context.Context, string, json.RawMessage) (json.RawMessage, error) {
	h.panic(email.MethodCopy)
	return nil, nil
}
func (h *UnimplementedHandler) EmailImport(context.Context, string, json.RawMessage) (json.RawMessage, error) {
	h.panic(email.MethodImport)
	return nil, nil
}
func (h *UnimplementedHandler) EmailParse(context.Context, string, json.RawMessage) (json.RawMessage, error) {
	h.panic(email.MethodParse)
	return nil, nil
}

func (h *UnimplementedHandler) SearchSnippetGet(context.Context, string, json.RawMessage) (json.RawMessage, error) {
	h.panic(searchsnippet.MethodGet)
	return nil, nil
}

func (h *UnimplementedHandler) IdentityGet(context.Context, string, json.RawMessage) (json.RawMessage, error) {
	h.panic(identity.MethodGet)
	return nil, nil
}
func (h *UnimplementedHandler) IdentitySet(context.Context, string, json.RawMessage) (json.RawMessage, error) {
	h.panic(identity.MethodSet)
	return nil, nil
}
func (h *UnimplementedHandler) IdentityChanges(context.Context, string, json.RawMessage) (json.RawMessage, error) {
	h.panic(identity.MethodChanges)
	return nil, nil
}

func (h *UnimplementedHandler) EmailSubmissionGet(context.Context, string, json.RawMessage) (json.RawMessage, error) {
	h.panic(emailsubmission.MethodGet)
	return nil, nil
}
func (h *UnimplementedHandler) EmailSubmissionSet(context.Context, string, json.RawMessage) (json.RawMessage, error) {
	h.panic(emailsubmission.MethodSet)
	return nil, nil
}
func (h *UnimplementedHandler) EmailSubmissionChanges(context.Context, string, json.RawMessage) (json.RawMessage, error) {
	h.panic(emailsubmission.MethodChanges)
	return nil, nil
}
func (h *UnimplementedHandler) EmailSubmissionQuery(context.Context, string, json.RawMessage) (json.RawMessage, error) {
	h.panic(emailsubmission.MethodQuery)
	return nil, nil
}
func (h *UnimplementedHandler) EmailSubmissionQueryChanges(context.Context, string, json.RawMessage) (json.RawMessage, error) {
	h.panic(emailsubmission.MethodQueryChanges)
	return nil, nil
}

func (h *UnimplementedHandler) VacationResponseGet(context.Context, string, json.RawMessage) (json.RawMessage, error) {
	h.panic(vacationresponse.MethodGet)
	return nil, nil
}
func (h *UnimplementedHandler) VacationResponseSet(context.Context, string, json.RawMessage) (json.RawMessage, error) {
	h.panic(vacationresponse.MethodSet)
	return nil, nil
}

func (h *UnimplementedHandler) PushSubscriptionGet(context.Context, string, json.RawMessage) (json.RawMessage, error) {
	h.panic(pushsubscription.MethodGet)
	return nil, nil
}
func (h *UnimplementedHandler) PushSubscriptionSet(context.Context, string, json.RawMessage) (json.RawMessage, error) {
	h.panic(pushsubscription.MethodSet)
	return nil, nil
}

func (h *UnimplementedHandler) BlobCopy(context.Context, string, json.RawMessage) (json.RawMessage, error) {
	h.panic(blob.MethodCopy)
	return nil, nil
}

var _ MethodHandler = (*UnimplementedHandler)(nil)
