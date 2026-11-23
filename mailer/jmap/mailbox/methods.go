package mailbox

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sort"
	"strings"

	"github.com/kncept/Emissary/mailer/components/storage"
	"github.com/kncept/Emissary/mailer/jmap/internal/base"
)

// maxMailboxNameBytes mirrors the maxSizeMailboxName capability advertised by
// the Session (RFC 8621 section 1.3.1). The RFC counts UTF-8 octets, not runes.
const maxMailboxNameBytes = 255

func (h *Handler) MailboxGet(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error) {
	mailboxes := h.getMailboxes(ctx, accountID)
	state, err := h.syncMailboxState(ctx, accountID, mailboxes)
	if err != nil {
		return nil, err
	}
	return base.HandleGet(ctx, accountID, args, base.GetSpec[Mailbox]{
		Allowed: base.ArgumentSet("accountId", "ids", "properties"),
		Model:   Mailbox{},
		Load:    func(context.Context) ([]Mailbox, error) { return mailboxes, nil },
		ID:      func(mb Mailbox) string { return mb.ID },
		State:   func(context.Context) (string, error) { return state, nil },
		Render: func(_ context.Context, mb Mailbox, selection *base.Selection, _ json.RawMessage) (json.RawMessage, error) {
			return selection.Filter(mb)
		},
	})
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
	return base.HandleSet(ctx, accountID, args, h.setSpec(accountID))
}

// setSpec owns the per-request state needed by Mailbox/set callbacks. It is
// created fresh for every call so creation-id references never leak between
// JMAP requests.
func (h *Handler) setSpec(accountID string) base.SetSpec {
	// Track created ids within this call so parent references can resolve.
	createdServerIDs := make(map[string]string)
	// RFC 8621 section 2: sibling Mailboxes MUST NOT share a name. Names
	// created earlier in the same /set call count as existing siblings too.
	createdNames := make(map[string]bool)
	// onDestroyRemoveEmails is a method-level argument, not part of any
	// create/update/destroy record, so the kernel's Extra hook captures it
	// once before the per-record callbacks run.
	var onDestroyRemoveEmails bool
	return base.SetSpec{
		Allowed: base.ArgumentSet("accountId", "ifInState", "create", "update", "destroy", "onDestroyRemoveEmails"),
		Extra: func(args json.RawMessage) error {
			var req struct {
				OnDestroyRemoveEmails bool `json:"onDestroyRemoveEmails"`
			}
			if err := json.Unmarshal(args, &req); err != nil {
				return base.Errorf("invalidArguments", "Mailbox/set arguments must be an object")
			}
			onDestroyRemoveEmails = req.OnDestroyRemoveEmails
			return nil
		},
		State: func(ctx context.Context) (string, error) {
			return h.syncMailboxState(ctx, accountID, h.getMailboxes(ctx, accountID))
		},
		// A /get or /query earlier in the same batch may have cached the
		// mailbox list. Discard that snapshot before HandleSet reads newState,
		// so the tracker diffs the post-mutation data and advances the state
		// (RFC 8620 section 5.3, RFC 8621 section 2.2). Without this, newState
		// repeats oldState and Mailbox/changes reports no update.
		Finalize: func(ctx context.Context) error {
			base.InvalidateCache(ctx, mailboxListCacheKey{accountID: accountID})
			return nil
		},
		Create: func(ctx context.Context, cid string, raw json.RawMessage) (json.RawMessage, *base.SetError, error) {
			fail := func(setErr base.SetError) (json.RawMessage, *base.SetError, error) {
				return nil, &setErr, nil
			}
			// RFC 8620 section 5: unknown or immutable create properties are
			// rejected as invalidProperties SetErrors naming the offending keys.
			invalid := base.ValidateCreateProperties(
				map[string]json.RawMessage{cid: raw},
				base.ArgumentSet("name", "parentId", "role", "sortOrder", "isSubscribed"),
				base.ArgumentSet("id"))
			if names, bad := invalid[cid]; bad {
				return fail(base.InvalidProperties(base.SetErrorNames(names), names...))
			}
			var mc mailboxCreate
			if err := json.Unmarshal(raw, &mc); err != nil {
				return fail(base.NewSetError("invalidProperties", "Invalid create object"))
			}
			name := ""
			if mc.Name != nil {
				name = strings.TrimSpace(*mc.Name)
			}
			if name == "" {
				return fail(base.NewSetError("invalidProperties", "name is required"))
			}
			parentID := ""
			if mc.ParentID != nil {
				parentID = storage.NormalizeMailboxID(*mc.ParentID)
			}
			if err := h.checkMailboxNameAvailable(ctx, accountID, name, parentID); err != nil {
				return fail(*err)
			}
			nameKey := strings.ToLower(parentID) + "\x00" + strings.ToLower(name)
			if createdNames[nameKey] {
				return fail(base.AlreadyExists("A mailbox with this name already exists under this parent", ""))
			}
			if len([]byte(name)) > maxMailboxNameBytes {
				return fail(base.InvalidProperties("name exceeds maxSizeMailboxName", "name"))
			}
			// Role mailboxes are predefined; clients must not create mailboxes with a role.
			if mc.Role != nil && strings.TrimSpace(*mc.Role) != "" {
				return fail(base.NewSetError("invalidProperties", "cannot create a mailbox with a role"))
			}
			if mc.ParentID != nil {
				p := storage.NormalizeMailboxID(*mc.ParentID)
				if real, ok := createdServerIDs[p]; ok {
					p = real
				}
				if p != "" {
					return fail(base.NewSetError("invalidProperties", "only top-level mailboxes are supported"))
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
				ParentID:     nil,
				Role:         nil,
				SortOrder:    sortOrder,
				IsSubscribed: isSubscribed,
			})
			if err != nil {
				switch {
				case errors.Is(err, storage.ErrMailboxExists):
					return fail(base.NewSetError("invalidProperties", "mailbox already exists"))
				case errors.Is(err, storage.ErrNotFound):
					return fail(base.NewSetError("invalidProperties", "parent mailbox not found"))
				default:
					return fail(base.NewSetError("serverFail", err.Error()))
				}
			}
			createdServerIDs[cid] = mb.ID
			createdNames[nameKey] = true
			// A newly created mailbox cannot contain messages yet.
			j := h.toJMAPMailbox(ctx, accountID, mb, nil)
			result, err := json.Marshal(map[string]any{
				"id":            j.ID,
				"sortOrder":     j.SortOrder,
				"totalEmails":   j.TotalEmails,
				"unreadEmails":  j.UnreadEmails,
				"totalThreads":  j.TotalThreads,
				"unreadThreads": j.UnreadThreads,
				"myRights":      j.MyRights,
				"isSubscribed":  j.IsSubscribed,
			})
			return result, nil, err
		},
		Update: func(ctx context.Context, id string, raw json.RawMessage) (json.RawMessage, *base.SetError, error) {
			fail := func(setErr base.SetError) (json.RawMessage, *base.SetError, error) {
				return nil, &setErr, nil
			}
			current, err := h.deps.Storage.GetMailbox(ctx, accountID, id)
			if err != nil {
				return fail(base.NewSetError("notFound", "Mailbox not found"))
			}
			// RFC 8620 section 5.3: validate the PatchObject structure (JSON
			// Pointer escapes, no array traversal, existing parents, no prefix
			// conflicts) before applying any semantic changes.
			patchCurrent, _ := json.Marshal(map[string]any{
				"name": current.Name, "parentId": current.ParentID, "role": current.Role,
				"sortOrder": current.SortOrder, "isSubscribed": current.IsSubscribed,
			})
			if _, err := base.ApplyPatch(patchCurrent, raw); err != nil {
				return fail(base.NewSetError("invalidPatch", err.Error()))
			}
			var patch mailboxPatch
			if err := json.Unmarshal(raw, &patch); err != nil {
				return fail(base.NewSetError("invalidPatch", "Invalid update patch"))
			}
			role := ""
			if current.Role != nil {
				role = strings.ToLower(*current.Role)
			}
			// Role mailboxes cannot have their role changed.
			if patch.Role != nil {
				switch v := patch.Role.(type) {
				case string:
					if strings.ToLower(v) != role {
						return fail(base.NewSetError("invalidProperties", "cannot change mailbox role"))
					}
				case nil:
					if role != "" {
						return fail(base.NewSetError("invalidProperties", "cannot remove mailbox role"))
					}
				default:
					return fail(base.NewSetError("invalidProperties", "invalid role"))
				}
			}
			if patch.Name != nil {
				name := strings.TrimSpace(*patch.Name)
				if name == "" {
					return fail(base.InvalidProperties("name must not be empty", "name"))
				}
				if len([]byte(name)) > maxMailboxNameBytes {
					return fail(base.InvalidProperties("name exceeds maxSizeMailboxName", "name"))
				}
				if role != "" {
					return fail(base.NewSetError("invalidProperties", "cannot rename a mailbox with a role"))
				}
				current.Name = name
			}
			if patch.ParentID != nil {
				if role != "" {
					return fail(base.NewSetError("invalidProperties", "cannot move a mailbox with a role"))
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
						return fail(base.NewSetError("invalidProperties", "only top-level mailboxes are supported"))
					}
				default:
					return fail(base.NewSetError("invalidProperties", "invalid parentId"))
				}
			}
			if patch.SortOrder != nil {
				current.SortOrder = *patch.SortOrder
			}
			if patch.IsSubscribed != nil {
				current.IsSubscribed = *patch.IsSubscribed
			}
			if _, err := h.deps.Storage.UpdateMailbox(ctx, accountID, current); err != nil {
				if errors.Is(err, storage.ErrNotFound) {
					return fail(base.NewSetError("notFound", "Mailbox not found"))
				}
				return fail(base.NewSetError("invalidProperties", err.Error()))
			}
			// Phase 3 will add updatedProperties; preserve the current
			// response shape until that task lands.
			return json.RawMessage("null"), nil, nil
		},
		Destroy: func(ctx context.Context, id string) *base.SetError {
			fail := func(setErr base.SetError) *base.SetError { return &setErr }
			current, err := h.deps.Storage.GetMailbox(ctx, accountID, id)
			if err != nil {
				return fail(base.NewSetError("notFound", "Mailbox not found"))
			}
			if current.Role != nil && strings.TrimSpace(*current.Role) != "" {
				return fail(base.NewSetError("forbidden", "Cannot destroy a mailbox with a role"))
			}
			// RFC 8621 section 2.5 distinguishes a non-empty mailbox by
			// *why* it cannot be removed, so children and emails are checked
			// separately before the storage-wide "not empty" error is used.
			mailboxes, err := h.deps.Storage.ListMailboxes(ctx, accountID)
			if err != nil {
				return fail(base.NewSetError("serverFail", "Could not list mailboxes"))
			}
			for _, mb := range mailboxes {
				if mb.ParentID != nil && *mb.ParentID == id {
					return fail(base.NewSetError("mailboxHasChild", "Mailbox still has child mailboxes"))
				}
			}
			emails, err := h.deps.Storage.ListEmails(ctx, accountID, id)
			if err != nil {
				return fail(base.NewSetError("serverFail", "Could not list emails in mailbox"))
			}
			if len(emails) > 0 && !onDestroyRemoveEmails {
				return fail(base.NewSetError("mailboxHasEmail", "Mailbox still has emails"))
			}
			// With maxMailboxesPerEmail=1 an email in this mailbox belongs to
			// no other mailbox, so removing the mailbox means destroying it.
			for _, listed := range emails {
				if listed == nil || listed.Ref == nil || !strings.EqualFold(listed.Ref.AccountID, accountID) {
					return fail(base.NewSetError("notFound", "Email not found"))
				}
				emailID := listed.Ref.ID
				originalMeta := base.EmailMetadata(ctx, h.deps, accountID, emailID)
				if err := h.deps.Metadata.Delete(ctx, accountID, emailID); err != nil {
					slog.Error("JMAP: failed to delete email metadata during Mailbox/set", "account", accountID, "id", emailID, "err", err)
					return fail(base.NewSetError("serverFail", "Failed to delete email metadata"))
				}
				if err := h.deps.Storage.DeleteEmail(ctx, accountID, emailID); err != nil {
					if restoreErr := h.deps.Metadata.Save(ctx, originalMeta); restoreErr != nil {
						slog.Error("JMAP: failed to restore email metadata", "account", accountID, "id", emailID, "err", restoreErr)
					}
					return fail(base.NewSetError("serverFail", "Failed to delete email"))
				}
				if err := h.deps.Changes.Record(ctx, accountID, base.StateEmail, emailID, base.ChangeDestroy); err != nil {
					return fail(base.NewSetError("serverFail", "Failed to record email change"))
				}
			}
			if err := h.deps.Storage.DeleteMailbox(ctx, accountID, id); err != nil {
				setErr := base.SetError{}
				switch {
				case errors.Is(err, storage.ErrNotFound):
					setErr = base.NewSetError("notFound", "Mailbox not found")
				case errors.Is(err, storage.ErrMailboxNotEmpty):
					setErr = base.NewSetError("mailboxHasChild", "Mailbox is not empty")
				default:
					setErr = base.NewSetError("serverFail", err.Error())
				}
				return &setErr
			}
			return nil
		},
	}
}

func (h *Handler) MailboxChanges(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error) {
	return base.HandleChanges(ctx, accountID, args, base.ChangesSpec{
		Allowed: base.ArgumentSet("accountId", "sinceState", "maxChanges"),
		Sync: func(ctx context.Context) error {
			_, err := h.syncMailboxState(ctx, accountID, h.getMailboxes(ctx, accountID))
			return err
		},
		Changes: func(ctx context.Context, sinceState string, maxChanges *int) (map[string]any, error) {
			result, err := h.deps.Changes.ChangesFrom(ctx, accountID, base.StateMailbox, sinceState, maxChanges)
			if err != nil {
				return nil, err
			}
			// RFC 8621 section 2.2: the shared tracker fingerprints whole
			// objects, so this server cannot tell whether a change affected
			// only the count properties. It MUST then report null.
			result["updatedProperties"] = nil
			return result, nil
		},
	})
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

// supportedMailboxFilterFields lists every mailbox condition this server
// implements. Compound conditions and anything else are rejected before
// decoding so they cannot silently turn into an empty, match-all filter.
var supportedMailboxFilterFields = map[string]bool{
	"parentId":     true,
	"name":         true,
	"role":         true,
	"hasAnyRole":   true,
	"isSubscribed": true,
}

func parseMailboxFilter(args json.RawMessage) (*mailboxFilter, error) {
	if err := base.ValidateConditionObject(args, "filter", supportedMailboxFilterFields); err != nil {
		return nil, err
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(args, &envelope); err != nil {
		return nil, base.Errorf("invalidArguments", "arguments must be an object")
	}
	raw, ok := envelope["filter"]
	if !ok || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, nil
	}
	var filter mailboxFilter
	if err := json.Unmarshal(raw, &filter); err != nil {
		return nil, base.Errorf("invalidArguments", "filter must be a condition object")
	}
	return &filter, nil
}

// queryMailboxIDs refreshes the object state and returns the full filtered and
// sorted result set, before pagination.
func (h *Handler) queryMailboxIDs(ctx context.Context, accountID string, args json.RawMessage, filter *mailboxFilter, ascending bool) ([]string, error) {
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
	// RFC 8621 section 2.3 requires both sortOrder and name sorts. Storage
	// already returns the default ascending order; an explicit comparator
	// overrides it. The id is always the final tie-breaker so the ordering is
	// stable even when two mailboxes share a sort key.
	var query struct {
		Sort []struct {
			Property string `json:"property"`
		} `json:"sort"`
	}
	if err := json.Unmarshal(args, &query); err != nil {
		return nil, base.Errorf("invalidArguments", "query arguments must be an object")
	}
	if len(query.Sort) > 0 {
		property := query.Sort[0].Property
		sort.SliceStable(mailboxes, func(i, j int) bool {
			order := 0
			switch property {
			case "sortOrder":
				order = cmp.Compare(mailboxes[i].SortOrder, mailboxes[j].SortOrder)
				if order == 0 {
					order = strings.Compare(strings.ToLower(mailboxes[i].Name), strings.ToLower(mailboxes[j].Name))
				}
			case "name":
				order = strings.Compare(strings.ToLower(mailboxes[i].Name), strings.ToLower(mailboxes[j].Name))
			}
			if order == 0 {
				return mailboxes[i].ID < mailboxes[j].ID
			}
			if ascending {
				return order < 0
			}
			return order > 0
		})
	}

	ids := make([]string, len(mailboxes))
	for i, mb := range mailboxes {
		ids[i] = mb.ID
	}
	return ids, nil
}

func (h *Handler) MailboxQuery(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error) {
	return base.HandleQuery(ctx, accountID, args, h.querySpec())
}

func (h *Handler) MailboxQueryChanges(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error) {
	return base.HandleQueryChanges(ctx, accountID, args, h.querySpec())
}

// querySpec is shared by Mailbox/query and Mailbox/queryChanges so filter,
// ordering and query state are derived identically in both methods.
func (h *Handler) querySpec() base.QuerySpec {
	return base.QuerySpec{
		// RFC 8621 section 2.3 requires these two sort properties.
		SortProperties: map[string]bool{"sortOrder": true, "name": true},
		Allowed: base.ArgumentSet("accountId", "filter", "sort", "position", "anchor", "anchorOffset",
			"limit", "calculateTotal"),
		ChangesAllowed: base.ArgumentSet("accountId", "filter", "sinceQueryState", "sort", "maxChanges",
			"upToId", "calculateTotal"),
		ParseFilter: func(args json.RawMessage) (any, error) { return parseMailboxFilter(args) },
		AllIDs: func(ctx context.Context, accountID string, args json.RawMessage, filter any, ascending bool) ([]string, error) {
			typed, _ := filter.(*mailboxFilter)
			return h.queryMailboxIDs(ctx, accountID, args, typed, ascending)
		},
		Tracker: h.deps.Changes,
		Kind:    base.StateMailboxQuery,
	}
}
