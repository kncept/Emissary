package mailbox

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"

	"github.com/kncept/Emissary/mailer/jmap/internal/base"
	"github.com/kncept/Emissary/mailer/storage"
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
		mailboxMap := make(map[string]Mailbox, len(mailboxes))
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

type mailboxCreate struct {
	Name         *string `json:"name,omitempty"`
	ParentID     *string `json:"parentId,omitempty"`
	Role         *string `json:"role,omitempty"`
	SortOrder    *int    `json:"sortOrder,omitempty"`
	IsSubscribed *bool   `json:"isSubscribed,omitempty"`
}

type mailboxPatch struct {
	Name         *string     `json:"name,omitempty"`
	ParentID     interface{} `json:"parentId,omitempty"` // string, null, or absent
	SortOrder    *int        `json:"sortOrder,omitempty"`
	IsSubscribed *bool       `json:"isSubscribed,omitempty"`
	Role         interface{} `json:"role,omitempty"` // not changeable except maybe to same/null in limited cases; treat carefully
}

func (h *Handler) MailboxSet(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error) {
	var req struct {
		AccountID string                     `json:"accountId"`
		IfInState *string                    `json:"ifInState,omitempty"`
		Create    map[string]json.RawMessage `json:"create,omitempty"`
		Update    map[string]json.RawMessage `json:"update,omitempty"`
		Destroy   []string                   `json:"destroy,omitempty"`
	}
	if err := json.Unmarshal(args, &req); err != nil {
		return nil, err
	}

	oldState, err := h.deps.Changes.State(ctx, accountID, base.StateMailbox)
	if err != nil {
		return nil, err
	}
	// ifInState is advisory here; if provided and mismatched, reject whole set.
	if req.IfInState != nil && *req.IfInState != oldState {
		return nil, base.Errorf("stateMismatch", "mailbox state mismatch")
	}

	created := make(map[string]interface{})
	updated := make(map[string]interface{})
	destroyed := []string{}
	notCreated := make(map[string]interface{})
	notUpdated := make(map[string]interface{})
	notDestroyed := make(map[string]interface{})

	// Process creates in deterministic order for predictable ids.
	createIDs := make([]string, 0, len(req.Create))
	for id := range req.Create {
		createIDs = append(createIDs, id)
	}
	sort.Strings(createIDs)
	// Track created ids within this call so parent references can resolve.
	createdServerIDs := make(map[string]string)
	for _, cid := range createIDs {
		var mc mailboxCreate
		if err := json.Unmarshal(req.Create[cid], &mc); err != nil {
			notCreated[cid] = setError("invalidProperties", "Invalid create object")
			continue
		}
		name := ""
		if mc.Name != nil {
			name = strings.TrimSpace(*mc.Name)
		}
		if name == "" {
			notCreated[cid] = setError("invalidProperties", "name is required")
			continue
		}
		// Role mailboxes are predefined; clients must not create mailboxes with a role.
		if mc.Role != nil && strings.TrimSpace(*mc.Role) != "" {
			notCreated[cid] = setError("invalidProperties", "cannot create a mailbox with a role")
			continue
		}
		var parentID *string
		if mc.ParentID != nil {
			p := storage.NormalizeMailboxID(*mc.ParentID)
			if real, ok := createdServerIDs[p]; ok {
				p = real
			}
			if p != "" {
				notCreated[cid] = setError("invalidProperties", "only top-level mailboxes are supported")
				continue
			}
		}
		sortOrder := 0
		if mc.SortOrder != nil {
			sortOrder = *mc.SortOrder
		}
		isSubscribed := true
		if mc.IsSubscribed != nil {
			isSubscribed = *mc.IsSubscribed
		}
		// Generate a stable-ish server id from name; ensure uniqueness.
		serverID := storage.NormalizeMailboxID(name)
		if serverID == "" {
			serverID = "mailbox"
		}
		// avoid collisions with existing or role mailboxes
		baseID := serverID
		for i := 1; ; i++ {
			_, err := h.deps.Storage.GetMailbox(ctx, accountID, serverID)
			if err != nil {
				break
			}
			serverID = baseID + "-" + strings.TrimSpace(strings.ReplaceAll(string(rune('a'+(i%26))), " ", ""))
			if i > 25 {
				serverID = baseID + "-" + cid
				break
			}
		}
		mb, err := h.deps.Storage.CreateMailbox(ctx, accountID, storage.Mailbox{
			ID:           serverID,
			Name:         name,
			ParentID:     parentID,
			Role:         nil,
			SortOrder:    sortOrder,
			IsSubscribed: isSubscribed,
		})
		if err != nil {
			switch {
			case errors.Is(err, storage.ErrMailboxExists):
				notCreated[cid] = setError("invalidProperties", "mailbox already exists")
			case errors.Is(err, storage.ErrNotFound):
				notCreated[cid] = setError("invalidProperties", "parent mailbox not found")
			default:
				notCreated[cid] = setError("serverFail", err.Error())
			}
			continue
		}
		createdServerIDs[cid] = mb.ID
		j := h.toJMAPMailbox(ctx, accountID, mb)
		created[cid] = map[string]interface{}{
			"id":           j.ID,
			"sortOrder":    j.SortOrder,
			"totalEmails":  j.TotalEmails,
			"unreadEmails": j.UnreadEmails,
			"totalThreads": j.TotalThreads,
			"unreadThreads": j.UnreadThreads,
			"myRights":     j.MyRights,
			"isSubscribed": j.IsSubscribed,
		}
	}

	// Updates
	for id, raw := range req.Update {
		var patch mailboxPatch
		if err := json.Unmarshal(raw, &patch); err != nil {
			notUpdated[id] = setError("invalidProperties", "Invalid update patch")
			continue
		}
		current, err := h.deps.Storage.GetMailbox(ctx, accountID, id)
		if err != nil {
			notUpdated[id] = setError("notFound", "Mailbox not found")
			continue
		}
		role := ""
		if current.Role != nil {
			role = strings.ToLower(*current.Role)
		}
		// Role mailboxes cannot have their role changed; ignore role patches that try to change.
		if patch.Role != nil {
			// Reject explicit role changes.
			switch v := patch.Role.(type) {
			case string:
				if strings.ToLower(v) != role {
					notUpdated[id] = setError("invalidProperties", "cannot change mailbox role")
					continue
				}
			case nil:
				if role != "" {
					notUpdated[id] = setError("invalidProperties", "cannot remove mailbox role")
					continue
				}
			default:
				notUpdated[id] = setError("invalidProperties", "invalid role")
				continue
			}
		}
		if patch.Name != nil {
			name := strings.TrimSpace(*patch.Name)
			if name == "" {
				notUpdated[id] = setError("invalidProperties", "name must not be empty")
				continue
			}
			// Role mailboxes may not be renamed.
			if role != "" {
				notUpdated[id] = setError("invalidProperties", "cannot rename a mailbox with a role")
				continue
			}
			current.Name = name
		}
		if patch.ParentID != nil {
			if role != "" {
				notUpdated[id] = setError("invalidProperties", "cannot move a mailbox with a role")
				continue
			}
			switch v := patch.ParentID.(type) {
			case nil:
				current.ParentID = nil
			case string:
				p := storage.NormalizeMailboxID(v)
				if real, ok := createdServerIDs[p]; ok {
					p = real
				}
				if p == "" {
					current.ParentID = nil
				} else {
					notUpdated[id] = setError("invalidProperties", "only top-level mailboxes are supported")
					continue
				}
			default:
				notUpdated[id] = setError("invalidProperties", "invalid parentId")
				continue
			}
		}
		if patch.SortOrder != nil {
			current.SortOrder = *patch.SortOrder
		}
		if patch.IsSubscribed != nil {
			current.IsSubscribed = *patch.IsSubscribed
		}
		if _, err := h.deps.Storage.UpdateMailbox(ctx, accountID, current); err != nil {
			switch {
			case errors.Is(err, storage.ErrNotFound):
				notUpdated[id] = setError("notFound", "Mailbox not found")
			default:
				notUpdated[id] = setError("invalidProperties", err.Error())
			}
			continue
		}
		updated[id] = nil
	}

	// Destroys
	for _, id := range req.Destroy {
		current, err := h.deps.Storage.GetMailbox(ctx, accountID, id)
		if err != nil {
			notDestroyed[id] = setError("notFound", "Mailbox not found")
			continue
		}
		if current.Role != nil && strings.TrimSpace(*current.Role) != "" {
			notDestroyed[id] = setError("forbidden", "Cannot destroy a mailbox with a role")
			continue
		}
		// Ensure empty (messages and children).
		if err := h.deps.Storage.DeleteMailbox(ctx, accountID, id); err != nil {
			switch {
			case errors.Is(err, storage.ErrNotFound):
				notDestroyed[id] = setError("notFound", "Mailbox not found")
			case errors.Is(err, storage.ErrMailboxNotEmpty):
				notDestroyed[id] = setError("mailboxHasChild", "Mailbox is not empty")
			default:
				notDestroyed[id] = setError("serverFail", err.Error())
			}
			continue
		}
		destroyed = append(destroyed, id)
	}

	newState, err := h.syncMailboxState(ctx, accountID, h.getMailboxes(ctx, accountID))
	if err != nil {
		newState = oldState
	}
	resp := map[string]interface{}{
		"accountId":    accountID,
		"oldState":     oldState,
		"newState":     newState,
		"created":      created,
		"updated":      updated,
		"destroyed":    destroyed,
		"notCreated":   notCreated,
		"notUpdated":   notUpdated,
		"notDestroyed": notDestroyed,
	}
	return json.Marshal(resp)
}

func setError(typ, description string) map[string]string {
	return map[string]string{"type": typ, "description": description}
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
			if filter.ParentID != nil {
				if *filter.ParentID == "" {
					if mb.ParentID != nil {
						match = false
					}
				} else if mb.ParentID == nil || *mb.ParentID != *filter.ParentID {
					match = false
				}
			}
			if filter.Name != nil && !strings.Contains(strings.ToLower(mb.Name), strings.ToLower(*filter.Name)) {
				match = false
			}
			if filter.Role != nil {
				if *filter.Role == "" {
					if mb.Role != nil && *mb.Role != "" {
						match = false
					}
				} else if mb.Role == nil || *mb.Role != *filter.Role {
					match = false
				}
			}
			if filter.HasAnyRole != nil {
				hasRole := mb.Role != nil && *mb.Role != ""
				if *filter.HasAnyRole != hasRole {
					match = false
				}
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
