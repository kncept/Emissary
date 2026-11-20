package mailbox

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/kncept/Emissary/mailer/jmap/internal/base"
)

func (h *Handler) MailboxGet(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error) {
	var req struct {
		AccountID string   `json:"accountId"`
		IDs       []string `json:"ids,omitempty"`
	}
	if err := json.Unmarshal(args, &req); err != nil {
		return nil, err
	}
	properties, err := base.ParseSelection(args, "properties", Mailbox{}, "")
	if err != nil {
		return nil, err
	}

	mailboxes := h.getMailboxes(ctx, accountID)
	var list []Mailbox
	var notFound []string
	state, err := h.syncMailboxState(ctx, accountID, mailboxes)
	if err != nil {
		return nil, err
	}

	if len(req.IDs) > 0 {
		mailboxMap := make(map[string]Mailbox)
		for _, mb := range mailboxes {
			mailboxMap[mb.ID] = mb
		}
		for _, id := range req.IDs {
			if mb, ok := mailboxMap[id]; ok {
				list = append(list, mb)
			} else {
				notFound = append(notFound, id)
			}
		}
	} else {
		list = mailboxes
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

func (h *Handler) MailboxSet(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error) {
	// For now, mailboxes are static - return error for create/update/destroy
	var req struct {
		AccountID string                 `json:"accountId"`
		Create    map[string]interface{} `json:"create,omitempty"`
		Update    map[string]interface{} `json:"update,omitempty"`
		Destroy   []string               `json:"destroy,omitempty"`
	}
	if err := json.Unmarshal(args, &req); err != nil {
		return nil, err
	}

	created := make(map[string]interface{})
	updated := make(map[string]interface{})
	destroyed := []string{}
	notCreated := make(map[string]interface{})
	notUpdated := make(map[string]interface{})
	notDestroyed := make(map[string]interface{})

	// All operations fail as mailboxes are static
	for id := range req.Create {
		notCreated[id] = map[string]string{"type": "forbidden", "description": "Cannot create mailboxes"}
	}
	for id := range req.Update {
		notUpdated[id] = map[string]string{"type": "forbidden", "description": "Cannot update mailboxes"}
	}
	for _, id := range req.Destroy {
		notDestroyed[id] = map[string]string{"type": "forbidden", "description": "Cannot destroy mailboxes"}
	}

	state, err := h.deps.Changes.State(ctx, accountID, base.StateMailbox)
	if err != nil {
		return nil, err
	}
	resp := map[string]interface{}{
		"accountId":    accountID,
		"oldState":     state,
		"newState":     state,
		"created":      created,
		"updated":      updated,
		"destroyed":    destroyed,
		"notCreated":   notCreated,
		"notUpdated":   notUpdated,
		"notDestroyed": notDestroyed,
	}
	return json.Marshal(resp)
}

func (h *Handler) MailboxChanges(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error) {
	var req struct {
		AccountID  string `json:"accountId"`
		SinceState string `json:"sinceState"`
		MaxChanges *int   `json:"maxChanges,omitempty"`
	}
	if err := json.Unmarshal(args, &req); err != nil {
		return nil, err
	}

	if _, err := h.syncMailboxState(ctx, accountID, h.getMailboxes(ctx, accountID)); err != nil {
		return nil, err
	}
	result, err := h.deps.Changes.ChangesFrom(ctx, accountID, base.StateMailbox, req.SinceState, req.MaxChanges)
	if err != nil {
		return nil, err
	}
	return json.Marshal(result)
}

// mailboxFilter is shared by Mailbox/query and Mailbox/queryChanges so the
// queryState token always describes the same result set.
type mailboxFilter struct {
	ParentID     *string `json:"parentId,omitempty"`
	Name         *string `json:"name,omitempty"`
	Role         *string `json:"role,omitempty"`
	HasAnyRole   *bool   `json:"hasAnyRole,omitempty"`
	IsSubscribed *bool   `json:"isSubscribed,omitempty"`
}

// queryMailboxIDs refreshes the object state and returns the full filtered
// result set, before pagination.
func (h *Handler) queryMailboxIDs(ctx context.Context, accountID string, filter *mailboxFilter) ([]string, error) {
	mailboxes := h.getMailboxes(ctx, accountID)
	if _, err := h.syncMailboxState(ctx, accountID, mailboxes); err != nil {
		return nil, err
	}
	if filter != nil {
		filtered := make([]Mailbox, 0, len(mailboxes))
		for _, mb := range mailboxes {
			match := true
			if filter.ParentID != nil && (mb.ParentID == nil || *mb.ParentID != *filter.ParentID) {
				match = false
			}
			if filter.Name != nil && !strings.Contains(strings.ToLower(mb.Name), strings.ToLower(*filter.Name)) {
				match = false
			}
			if filter.Role != nil && (mb.Role == nil || *mb.Role != *filter.Role) {
				match = false
			}
			if filter.HasAnyRole != nil && *filter.HasAnyRole && (mb.Role == nil || *mb.Role == "") {
				match = false
			}
			if filter.IsSubscribed != nil && mb.IsSubscribed != *filter.IsSubscribed {
				match = false
			}
			if match {
				filtered = append(filtered, mb)
			}
		}
		mailboxes = filtered
	}
	ids := make([]string, len(mailboxes))
	for i, mb := range mailboxes {
		ids[i] = mb.ID
	}
	return ids, nil
}

func (h *Handler) MailboxQuery(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error) {
	var req struct {
		AccountID    string         `json:"accountId"`
		Filter       *mailboxFilter `json:"filter,omitempty"`
		Sort         []interface{}  `json:"sort,omitempty"`
		Position     *int           `json:"position,omitempty"`
		Anchor       *string        `json:"anchor,omitempty"`
		AnchorOffset *int           `json:"anchorOffset,omitempty"`
		Limit        *int           `json:"limit,omitempty"`
	}
	if err := json.Unmarshal(args, &req); err != nil {
		return nil, err
	}
	queryKey, err := base.QueryKey(req.Filter, req.Sort)
	if err != nil {
		return nil, err
	}

	allIDs, err := h.queryMailboxIDs(ctx, accountID, req.Filter)
	if err != nil {
		return nil, err
	}
	start, end := base.PageBounds(len(allIDs), req.Position, req.Limit)
	ids := append([]string{}, allIDs[start:end]...)
	queryState, err := h.deps.Changes.SyncQuery(ctx, accountID, base.StateMailboxQuery, queryKey, allIDs)
	if err != nil {
		return nil, err
	}

	resp := map[string]interface{}{
		"accountId":  accountID,
		"queryState": queryState,
		"ids":        ids,
		"total":      len(allIDs),
	}
	return json.Marshal(resp)
}

func (h *Handler) MailboxQueryChanges(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error) {
	var req struct {
		AccountID       string         `json:"accountId"`
		SinceQueryState string         `json:"sinceQueryState"`
		Filter          *mailboxFilter `json:"filter,omitempty"`
		Sort            []interface{}  `json:"sort,omitempty"`
		MaxChanges      *int           `json:"maxChanges,omitempty"`
		UpToID          *string        `json:"upToId,omitempty"`
	}
	if err := json.Unmarshal(args, &req); err != nil {
		return nil, err
	}
	queryKey, err := base.QueryKey(req.Filter, req.Sort)
	if err != nil {
		return nil, err
	}

	ids, err := h.queryMailboxIDs(ctx, accountID, req.Filter)
	if err != nil {
		return nil, err
	}
	result, err := h.deps.Changes.QueryChangesFrom(ctx, accountID, base.StateMailboxQuery, queryKey, req.SinceQueryState, req.MaxChanges, ids)
	if err != nil {
		return nil, err
	}
	return json.Marshal(result)
}
