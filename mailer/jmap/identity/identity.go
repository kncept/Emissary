// Package identity implements the Identity methods from RFC 8621 section 6.
package identity

import (
	"context"
	"encoding/json"

	"github.com/kncept/Emissary/mailer/jmap/internal/base"
	"github.com/kncept/Emissary/mailer/jmap/internal/model"
)

const (
	MethodGet     = "Identity/get"
	MethodSet     = "Identity/set"
	MethodChanges = "Identity/changes"
)

// Methods returns the method names implemented by this package.
func Methods() []string { return []string{MethodGet, MethodSet, MethodChanges} }

// MethodHandler is the contract the dispatcher expects from Identity handlers.
type MethodHandler interface {
	IdentityGet(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error)
	IdentitySet(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error)
	IdentityChanges(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error)
}

// Handler implements the Identity methods.
type Handler struct{ deps *base.Deps }

// New creates an Identity method handler.
func New(deps *base.Deps) *Handler { return &Handler{deps: deps} }

// Identity represents a JMAP Identity object.
type Identity struct {
	ID            string               `json:"id"`
	Name          string               `json:"name"`
	Email         string               `json:"email"`
	ReplyTo       []model.EmailAddress `json:"replyTo,omitempty"`
	Bcc           []model.EmailAddress `json:"bcc,omitempty"`
	TextSignature string               `json:"textSignature"`
	HTMLSignature string               `json:"htmlSignature"`
	MayDelete     bool                 `json:"mayDelete"`
}

var _ MethodHandler = (*Handler)(nil)

func (h *Handler) IdentityGet(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error) {
	var req struct {
		AccountID string   `json:"accountId"`
		IDs       []string `json:"ids,omitempty"`
	}
	if err := json.Unmarshal(args, &req); err != nil {
		return nil, err
	}

	// Get user info to build identity
	user, err := h.deps.UserMgr.Lookup(ctx, accountID)
	if err != nil {
		return nil, err
	}

	identity := Identity{
		ID:            "primary",
		Name:          user.Email,
		Email:         user.Email,
		TextSignature: "",
		HTMLSignature: "",
		MayDelete:     false,
	}

	var list []Identity
	var notFound []string

	if len(req.IDs) > 0 {
		for _, id := range req.IDs {
			if id == "primary" {
				list = append(list, identity)
			} else {
				notFound = append(notFound, id)
			}
		}
	} else {
		list = append(list, identity)
	}

	resp := map[string]interface{}{
		"accountId": accountID,
		"state":     h.deps.Changes.State(accountID, base.StateIdentity),
		"list":      list,
		"notFound":  notFound,
	}
	return json.Marshal(resp)
}

func (h *Handler) IdentitySet(_ context.Context, _ string, _ json.RawMessage) (json.RawMessage, error) {
	return base.NotImplemented(MethodSet)
}

func (h *Handler) IdentityChanges(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error) {
	var req struct {
		AccountID  string `json:"accountId"`
		SinceState string `json:"sinceState"`
		MaxChanges *int   `json:"maxChanges,omitempty"`
	}
	if err := json.Unmarshal(args, &req); err != nil {
		return nil, err
	}

	result, err := h.deps.Changes.ChangesFrom(accountID, base.StateIdentity, req.SinceState, req.MaxChanges)
	if err != nil {
		return nil, err
	}
	return json.Marshal(result)
}
