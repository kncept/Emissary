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
	threads, err := h.getThreads(ctx, accountID)
	if err != nil {
		return nil, err
	}
	state, err := h.syncThreadState(ctx, accountID, threads)
	if err != nil {
		return nil, err
	}
	return base.HandleGet(ctx, accountID, args, base.GetSpec[Thread]{
		Allowed: base.ArgumentSet("accountId", "ids", "properties"),
		Model:   Thread{},
		Load:    func(context.Context) ([]Thread, error) { return threads, nil },
		ID:      func(t Thread) string { return t.ID },
		State:   func(context.Context) (string, error) { return state, nil },
		Render: func(_ context.Context, t Thread, selection *base.Selection, _ json.RawMessage) (json.RawMessage, error) {
			return selection.Filter(t)
		},
	})
}

// SyncState refreshes the Thread state from the stored messages.
func (h *Handler) SyncState(ctx context.Context, accountID string) (string, error) {
	threads, err := h.getThreads(ctx, accountID)
	if err != nil {
		return "", err
	}
	return h.syncThreadState(ctx, accountID, threads)
}

func (h *Handler) ThreadChanges(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error) {
	return base.HandleChanges(ctx, accountID, args, base.ChangesSpec{
		Allowed: base.ArgumentSet("accountId", "sinceState", "maxChanges"),
		Sync: func(ctx context.Context) error {
			threads, err := h.getThreads(ctx, accountID)
			if err != nil {
				return err
			}
			_, err = h.syncThreadState(ctx, accountID, threads)
			return err
		},
		Changes: func(ctx context.Context, sinceState string, maxChanges *int) (map[string]any, error) {
			return h.deps.Changes.ChangesFrom(ctx, accountID, base.StateThread, sinceState, maxChanges)
		},
	})
}

// getThreads derives threads from the stored messages using the shared
// RFC 8621 section 3 grouping kernel. The tracker state remains the durable
// source of Thread/changes; this only rebuilds the fingerprint snapshot.
func (h *Handler) getThreads(ctx context.Context, accountID string) ([]Thread, error) {
	emails, err := h.deps.Storage.ListEmails(ctx, accountID, "")
	if err != nil {
		// Never sync an empty snapshot caused by a storage failure: that
		// would look like every thread was destroyed.
		return nil, err
	}
	groups, _, err := base.GroupThreads(ctx, h.deps, accountID, emails)
	if err != nil {
		return nil, err
	}
	threads := make([]Thread, 0, len(groups))
	for _, group := range groups {
		threads = append(threads, Thread{ID: group.ID, EmailIDs: group.EmailIDs})
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
