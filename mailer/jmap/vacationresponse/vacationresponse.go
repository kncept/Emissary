// Package vacationresponse implements the VacationResponse methods from RFC
// 8621 section 8.
package vacationresponse

import (
	"context"
	"encoding/json"

	"github.com/kncept/Emissary/mailer/jmap/internal/base"
)

const (
	MethodGet     = "VacationResponse/get"
	MethodSet     = "VacationResponse/set"
	MethodChanges = "VacationResponse/changes"
)

// Methods returns the method names implemented by this package.
func Methods() []string { return []string{MethodGet, MethodSet, MethodChanges} }

// MethodHandler is the contract the dispatcher expects from VacationResponse
// handlers.
type MethodHandler interface {
	VacationResponseGet(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error)
	VacationResponseSet(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error)
	VacationResponseChanges(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error)
}

// Handler implements the VacationResponse methods.
type Handler struct{ deps *base.Deps }

// New creates a VacationResponse method handler.
func New(deps *base.Deps) *Handler { return &Handler{deps: deps} }

// VacationResponseGet is not implemented yet.
func (h *Handler) VacationResponseGet(_ context.Context, _ string, _ json.RawMessage) (json.RawMessage, error) {
	return base.NotImplemented(MethodGet)
}

// VacationResponseSet is not implemented yet.
func (h *Handler) VacationResponseSet(_ context.Context, _ string, _ json.RawMessage) (json.RawMessage, error) {
	return base.NotImplemented(MethodSet)
}

// VacationResponseChanges is not implemented yet.
func (h *Handler) VacationResponseChanges(_ context.Context, _ string, _ json.RawMessage) (json.RawMessage, error) {
	return base.NotImplemented(MethodChanges)
}

var _ MethodHandler = (*Handler)(nil)
