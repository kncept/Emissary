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

	mailboxes := h.getMailboxes(ctx, accountID)
	var list []Mailbox
	var notFound []string
	state := h.syncMailboxState(ctx, accountID, mailboxes)

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

	resp := map[string]interface{}{
		"accountId": accountID,
		"state":     state,
		"list":      list,
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

	state := h.deps.Changes.State(accountID, base.StateMailbox)
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

	h.syncMailboxState(ctx, accountID, h.getMailboxes(ctx, accountID))
	result, err := h.deps.Changes.ChangesFrom(accountID, base.StateMailbox, req.SinceState, req.MaxChanges)
	if err != nil {
		return nil, err
	}
	return json.Marshal(result)
}

func (h *Handler) MailboxQuery(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error) {
	var req struct {
		AccountID string `json:"accountId"`
		Filter    *struct {
			ParentID     *string `json:"parentId,omitempty"`
			Name         *string `json:"name,omitempty"`
			Role         *string `json:"role,omitempty"`
			HasAnyRole   *bool   `json:"hasAnyRole,omitempty"`
			IsSubscribed *bool   `json:"isSubscribed,omitempty"`
		} `json:"filter,omitempty"`
		Sort         []interface{} `json:"sort,omitempty"`
		Position     *int          `json:"position,omitempty"`
		Anchor       *string       `json:"anchor,omitempty"`
		AnchorOffset *int          `json:"anchorOffset,omitempty"`
		Limit        *int          `json:"limit,omitempty"`
	}
	if err := json.Unmarshal(args, &req); err != nil {
		return nil, err
	}

	mailboxes := h.getMailboxes(ctx, accountID)
	state := h.syncMailboxState(ctx, accountID, mailboxes)

	// Apply filter if provided
	if req.Filter != nil {
		filtered := make([]Mailbox, 0)
		for _, mb := range mailboxes {
			match := true
			if req.Filter.ParentID != nil && (mb.ParentID == nil || *mb.ParentID != *req.Filter.ParentID) {
				match = false
			}
			if req.Filter.Name != nil && !strings.Contains(strings.ToLower(mb.Name), strings.ToLower(*req.Filter.Name)) {
				match = false
			}
			if req.Filter.Role != nil && (mb.Role == nil || *mb.Role != *req.Filter.Role) {
				match = false
			}
			if req.Filter.HasAnyRole != nil && *req.Filter.HasAnyRole && (mb.Role == nil || *mb.Role == "") {
				match = false
			}
			if req.Filter.IsSubscribed != nil && mb.IsSubscribed != *req.Filter.IsSubscribed {
				match = false
			}
			if match {
				filtered = append(filtered, mb)
			}
		}
		mailboxes = filtered
	}

	start, end := base.PageBounds(len(mailboxes), req.Position, req.Limit)
	result := mailboxes[start:end]
	ids := make([]string, len(result))
	for i, mb := range result {
		ids[i] = mb.ID
	}

	resp := map[string]interface{}{
		"accountId":  accountID,
		"queryState": state,
		"ids":        ids,
		"total":      len(mailboxes),
	}
	return json.Marshal(resp)
}

func (h *Handler) MailboxQueryChanges(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error) {
	var req struct {
		AccountID       string  `json:"accountId"`
		SinceQueryState string  `json:"sinceQueryState"`
		MaxChanges      *int    `json:"maxChanges,omitempty"`
		UpToID          *string `json:"upToId,omitempty"`
	}
	if err := json.Unmarshal(args, &req); err != nil {
		return nil, err
	}

	h.syncMailboxState(ctx, accountID, h.getMailboxes(ctx, accountID))
	result, err := h.deps.Changes.ChangesFrom(accountID, base.StateMailboxQuery, req.SinceQueryState, req.MaxChanges)
	if err != nil {
		return nil, err
	}
	return json.Marshal(base.QueryChangesResponse(result))
}
