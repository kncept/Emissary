// Package pushsubscription implements the PushSubscription methods from RFC
// 8620 section 7.2.
package pushsubscription

import (
	"context"
	"encoding/json"

	"github.com/kncept/Emissary/mailer/jmap/internal/base"
)

const (
	MethodGet = "PushSubscription/get"
	MethodSet = "PushSubscription/set"
)

// Methods returns the method names implemented by this package.
func Methods() []string { return []string{MethodGet, MethodSet} }

// MethodHandler is the contract the dispatcher expects from PushSubscription
// handlers.
type MethodHandler interface {
	PushSubscriptionGet(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error)
	PushSubscriptionSet(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error)
}

// Handler implements the PushSubscription methods.
type Handler struct{ deps *base.Deps }

// New creates a PushSubscription method handler.
func New(deps *base.Deps) *Handler { return &Handler{deps: deps} }

// PushSubscriptionGet is not implemented yet.
func (h *Handler) PushSubscriptionGet(_ context.Context, _ string, _ json.RawMessage) (json.RawMessage, error) {
	return base.NotImplemented(MethodGet)
}

// PushSubscriptionSet is not implemented yet.
func (h *Handler) PushSubscriptionSet(_ context.Context, _ string, _ json.RawMessage) (json.RawMessage, error) {
	return base.NotImplemented(MethodSet)
}

var _ MethodHandler = (*Handler)(nil)
