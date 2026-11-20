// Package core implements the Core JMAP methods (RFC 8620).
package core

import (
	"context"
	"encoding/json"
)

// MethodEcho is the Core/echo method name.
const MethodEcho = "Core/echo"

// Methods returns the method names implemented by this package.
func Methods() []string { return []string{MethodEcho} }

// MethodHandler is the contract the dispatcher expects from Core handlers.
type MethodHandler interface {
	CoreEcho(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error)
}

// Handler implements the Core methods.
type Handler struct{}

// New creates a Core method handler.
func New() *Handler { return &Handler{} }

// CoreEcho returns its arguments unchanged.
func (h *Handler) CoreEcho(_ context.Context, _ string, args json.RawMessage) (json.RawMessage, error) {
	return args, nil
}

var _ MethodHandler = (*Handler)(nil)
