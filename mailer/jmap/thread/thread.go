// Package thread implements the Thread methods from RFC 8621 section 3.
package thread

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/kncept/Emissary/mailer/jmap/internal/base"
)

const (
	MethodGet     = "Thread/get"
	MethodChanges = "Thread/changes"
)

// Methods returns the method names implemented by this package.
func Methods() []string { return []string{MethodGet, MethodChanges} }

// MethodHandler is the contract the dispatcher expects from Thread handlers.
type MethodHandler interface {
	ThreadGet(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error)
	ThreadChanges(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error)
}

// Handler implements the Thread methods.
type Handler struct{ deps *base.Deps }

// New creates a Thread method handler.
func New(deps *base.Deps) *Handler { return &Handler{deps: deps} }

// Thread represents a JMAP Thread object.
type Thread struct {
	ID       string   `json:"id"`
	EmailIDs []string `json:"emailIds"`
}

var _ MethodHandler = (*Handler)(nil)

func (h *Handler) ThreadGet(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error) {
	var req struct {
		IDs []string `json:"ids,omitempty"`
	}
	if err := json.Unmarshal(args, &req); err != nil {
		return nil, err
	}
	properties, err := base.ParseSelection(args, "properties", Thread{}, "")
	if err != nil {
		return nil, err
	}

	threads, err := h.getThreads(ctx, accountID)
	if err != nil {
		return nil, err
	}
	state, err := h.syncThreadState(ctx, accountID, threads)
	if err != nil {
		return nil, err
	}
	var list []Thread
	var notFound []string

	if len(req.IDs) > 0 {
		threadMap := make(map[string]Thread)
		for _, t := range threads {
			threadMap[t.ID] = t
		}
		for _, id := range req.IDs {
			if t, ok := threadMap[id]; ok {
				list = append(list, t)
			} else {
				notFound = append(notFound, id)
			}
		}
	} else {
		list = threads
	}

	selected, err := properties.Filter(list)
	if err != nil {
		return nil, err
	}
	resp := map[string]interface{}{
		"accountId": accountID,
		"state":     state,
		"list":      selected,
		"notFound":  notFound,
	}
	return json.Marshal(resp)
}

func (h *Handler) ThreadChanges(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error) {
	var req struct {
		SinceState string `json:"sinceState"`
		MaxChanges *int   `json:"maxChanges,omitempty"`
	}
	if err := json.Unmarshal(args, &req); err != nil {
		return nil, err
	}

	threads, err := h.getThreads(ctx, accountID)
	if err != nil {
		return nil, err
	}
	if _, err := h.syncThreadState(ctx, accountID, threads); err != nil {
		return nil, err
	}
	result, err := h.deps.Changes.ChangesFrom(ctx, accountID, base.StateThread, req.SinceState, req.MaxChanges)
	if err != nil {
		return nil, err
	}
	return json.Marshal(result)
}

// getThreads derives threads from the stored messages. A real implementation
// would group by the References/In-Reply-To headers.
func (h *Handler) getThreads(ctx context.Context, accountID string) ([]Thread, error) {
	emails, err := h.deps.Storage.ListEmails(ctx, accountID, "")
	if err != nil {
		// Never sync an empty snapshot caused by a storage failure: that
		// would look like every thread was destroyed.
		return nil, err
	}

	threads := make([]Thread, 0, len(emails))
	for _, e := range emails {
		threads = append(threads, Thread{
			ID:       e.Ref.ID,
			EmailIDs: []string{e.Ref.ID},
		})
	}
	return threads, nil
}

func (h *Handler) syncThreadState(ctx context.Context, accountID string, threads []Thread) (string, error) {
	snapshot := make(map[string]string, len(threads))
	for _, t := range threads {
		snapshot[t.ID] = strings.Join(t.EmailIDs, ",")
	}
	if err := h.deps.Changes.Sync(ctx, accountID, map[base.StateKind]map[string]string{
		base.StateThread: snapshot,
	}); err != nil {
		return "", err
	}
	return h.deps.Changes.State(ctx, accountID, base.StateThread)
}
