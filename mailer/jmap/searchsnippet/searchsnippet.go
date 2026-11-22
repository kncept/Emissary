// Package searchsnippet implements the SearchSnippet method from RFC 8621
// section 4.7.
package searchsnippet

import (
	"context"
	"encoding/json"
	"strings"

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

type snippet struct {
	EmailID string  `json:"emailId"`
	Subject *string `json:"subject"`
	Preview *string `json:"preview"`
}

// SearchSnippetGet returns one SearchSnippet per known email id. Emissary does
// not compute highlighted search snippets, so subject and preview are always
// null — which RFC 8621 explicitly permits. The method still validates every
// id against storage so an unknown email is reported in notFound.
func (h *Handler) SearchSnippetGet(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error) {
	var req struct {
		EmailIDs []string        `json:"emailIds"`
		Filter   json.RawMessage `json:"filter,omitempty"`
	}
	if err := base.RejectUnknownArguments(args, base.ArgumentSet(
		"accountId", "emailIds", "filter")); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(args, &req); err != nil {
		return nil, err
	}
	// RFC 8621 allows a filter here; Emissary does not highlight matches, and
	// Phase 3 forbids silently ignoring a supplied argument.
	if len(req.Filter) > 0 && string(req.Filter) != "null" {
		return nil, base.Errorf("unsupportedFilter", "SearchSnippet filter is not supported")
	}

	list := make([]snippet, 0, len(req.EmailIDs))
	var notFound []string
	for _, id := range req.EmailIDs {
		listed, err := h.deps.Storage.GetEmail(ctx, accountID, id)
		if err != nil || listed == nil || listed.Ref == nil || !strings.EqualFold(listed.Ref.AccountID, accountID) {
			notFound = append(notFound, id)
			continue
		}
		list = append(list, snippet{EmailID: id, Subject: nil, Preview: nil})
	}
	return base.GetResult{
		AccountID: accountID,
		List:      list,
		NotFound:  notFound,
	}.Marshal()
}

var _ MethodHandler = (*Handler)(nil)
