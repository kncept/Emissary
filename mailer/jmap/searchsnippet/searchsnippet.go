// Package searchsnippet implements the SearchSnippet method from RFC 8621
// section 4.7.
package searchsnippet

import (
	"context"
	"encoding/json"

	"github.com/kncept/Emissary/mailer/jmap/internal/base"
)

// MethodGet is the SearchSnippet/get method name.
const MethodGet = "SearchSnippet/get"

// Methods returns the method names implemented by this package.
func Methods() []string { return []string{MethodGet} }

// MethodHandler is the contract the dispatcher expects from SearchSnippet
// handlers.
type MethodHandler interface {
	SearchSnippetGet(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error)
}

// Handler implements the SearchSnippet methods.
type Handler struct{ deps *base.Deps }

// New creates a SearchSnippet method handler.
func New(deps *base.Deps) *Handler { return &Handler{deps: deps} }

// SearchSnippetGet is not implemented yet.
func (h *Handler) SearchSnippetGet(_ context.Context, _ string, _ json.RawMessage) (json.RawMessage, error) {
	return base.NotImplemented(MethodGet)
}

var _ MethodHandler = (*Handler)(nil)
