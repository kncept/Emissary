// Package blob implements the Blob methods from RFC 8621 section 9.
package blob

import (
	"context"
	"encoding/json"

	"github.com/kncept/Emissary/mailer/jmap/internal/base"
)

// MethodCopy is the Blob/copy method name.
const MethodCopy = "Blob/copy"

// Methods returns the method names implemented by this package.
func Methods() []string { return []string{MethodCopy} }

// MethodHandler is the contract the dispatcher expects from Blob handlers.
type MethodHandler interface {
	BlobCopy(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error)
}

// Handler implements the Blob methods.
type Handler struct{ deps *base.Deps }

// New creates a Blob method handler.
func New(deps *base.Deps) *Handler { return &Handler{deps: deps} }

// BlobCopy is not implemented yet.
func (h *Handler) BlobCopy(_ context.Context, _ string, _ json.RawMessage) (json.RawMessage, error) {
	return base.NotImplemented(MethodCopy)
}

var _ MethodHandler = (*Handler)(nil)
