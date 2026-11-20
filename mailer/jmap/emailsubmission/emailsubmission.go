// Package emailsubmission implements the EmailSubmission methods from RFC 8621
// section 7.
package emailsubmission

import (
	"context"
	"encoding/json"

	"github.com/kncept/Emissary/mailer/jmap/internal/base"
)

const (
	MethodGet          = "EmailSubmission/get"
	MethodSet          = "EmailSubmission/set"
	MethodChanges      = "EmailSubmission/changes"
	MethodQuery        = "EmailSubmission/query"
	MethodQueryChanges = "EmailSubmission/queryChanges"
)

// Methods returns the method names implemented by this package.
func Methods() []string {
	return []string{MethodGet, MethodSet, MethodChanges, MethodQuery, MethodQueryChanges}
}

// MethodHandler is the contract the dispatcher expects from EmailSubmission
// handlers.
type MethodHandler interface {
	EmailSubmissionGet(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error)
	EmailSubmissionSet(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error)
	EmailSubmissionChanges(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error)
	EmailSubmissionQuery(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error)
	EmailSubmissionQueryChanges(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error)
}

// ImplicitSetHandler is the optional extension the dispatcher uses to emit the
// extra Email/set response required by RFC 8621 section 7.5.
type ImplicitSetHandler interface {
	EmailSubmissionSetWithImplicit(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, json.RawMessage, error)
}

// EmailSetter is the slice of Email/set needed for the implicit update after a
// successful submission. It is injected instead of importing the email
// subpackage to keep the dependency direction toward the composition root.
type EmailSetter interface {
	EmailSet(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error)
}

// Handler implements the EmailSubmission methods.
type Handler struct {
	deps     *base.Deps
	emailSet EmailSetter
}

// New creates an EmailSubmission method handler. emailSet performs the
// implicit Email/set call after a successful submission.
func New(deps *base.Deps, emailSet EmailSetter) *Handler {
	return &Handler{deps: deps, emailSet: emailSet}
}

var _ MethodHandler = (*Handler)(nil)
var _ ImplicitSetHandler = (*Handler)(nil)
