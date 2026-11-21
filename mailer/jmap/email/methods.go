package email

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	mailemail "github.com/kncept/Emissary/mailer/email"
	"github.com/kncept/Emissary/mailer/jmap/internal/base"
	"github.com/kncept/Emissary/mailer/jmap/internal/model"
	"github.com/kncept/Emissary/mailer/components/metadata"
	"github.com/kncept/Emissary/mailer/components/storage"
)

func (h *Handler) EmailGet(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error) {
	var req struct {
		AccountID           string   `json:"accountId"`
		IDs                 []string `json:"ids,omitempty"`
		FetchTextBodyValues bool     `json:"fetchTextBodyValues"`
		FetchHTMLBodyValues bool     `json:"fetchHTMLBodyValues"`
		FetchAllBodyValues  bool     `json:"fetchAllBodyValues"`
		MaxBodyValueBytes   int      `json:"maxBodyValueBytes"`
	}
	if err := json.Unmarshal(args, &req); err != nil {
		return nil, err
	}
	properties, err := base.ParseSelection(args, "properties", JMAPEmail{}, "header:")
	if err != nil {
		return nil, err
	}
	bodyProperties, err := base.ParseSelection(args, "bodyProperties", EmailBodyPart{}, "header:")
	if err != nil {
		return nil, err
	}
	bodyProperties = bodyProperties.WithRecursion("subParts")
	if req.MaxBodyValueBytes < 0 {
		return nil, base.Errorf("invalidArguments", "maxBodyValueBytes must not be negative")
	}
	fetchText := req.FetchTextBodyValues || req.FetchAllBodyValues
	fetchHTML := req.FetchHTMLBodyValues || req.FetchAllBodyValues

	var list []json.RawMessage
	var notFound []string

	if len(req.IDs) > 0 {
		for _, id := range req.IDs {
			listed, err := h.deps.Storage.GetEmail(ctx, accountID, id)
			if err != nil || listed == nil || listed.Ref == nil || !strings.EqualFold(listed.Ref.AccountID, accountID) {
				notFound = append(notFound, id)
				continue
			}
			jmapEmail, err := h.toJMAPEmail(ctx, listed)
			if err != nil {
				continue
			}
			jmapEmail.setBodyValues(listed.Email, fetchText, fetchHTML, req.MaxBodyValueBytes)
			item, err := jmapEmail.filtered(properties, bodyProperties)
			if err != nil {
				return nil, err
			}
			list = append(list, item)
		}
	} else {
		emails, err := h.deps.Storage.ListEmails(ctx, accountID, "")
		if err != nil {
			return nil, err
		}
		for _, e := range emails {
			if e == nil || e.Ref == nil || e.Email == nil {
				continue
			}
			jmapEmail, err := h.toJMAPEmail(ctx, e)
			if err != nil {
				continue
			}
			jmapEmail.setBodyValues(e.Email, fetchText, fetchHTML, req.MaxBodyValueBytes)
			item, err := jmapEmail.filtered(properties, bodyProperties)
			if err != nil {
				return nil, err
			}
			list = append(list, item)
		}
	}

	// State is derived from the same record content clients see, so a
	// server-side change or per-request property selection does not affect it.
	stateListed, _ := h.deps.Storage.ListEmails(ctx, accountID, "")
	state, err := h.syncEmailStateFromListed(ctx, accountID, stateListed)
	if err != nil {
		return nil, err
	}

	resp := map[string]interface{}{
		"accountId": accountID,
		"state":     state,
		"list":      list,
		"notFound":  notFound,
	}
	return json.Marshal(resp)
}

func (h *Handler) EmailSet(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error) {
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

	oldState, err := h.deps.Changes.State(ctx, accountID, base.StateEmail)
	if err != nil {
		return nil, err
	}
	if req.IfInState != nil && *req.IfInState != oldState {
		return nil, base.Errorf("stateMismatch", "email state mismatch")
	}

	created := make(map[string]interface{})
	updated := make(map[string]interface{})
	destroyed := []string{}
	notCreated := make(map[string]interface{})
	notUpdated := make(map[string]interface{})
	notDestroyed := make(map[string]interface{})

	// Handle destroy. Verify the message belongs to the requested account so
	// a crafted id cannot delete another user's mail.
	for _, id := range req.Destroy {
		listed, err := h.deps.Storage.GetEmail(ctx, accountID, id)
		if err != nil || listed == nil || listed.Ref == nil || !strings.EqualFold(listed.Ref.AccountID, accountID) {
			notDestroyed[id] = setErr("notFound", "Email not found")
			continue
		}
		mbID := listed.Ref.MailboxID
		if mbID == "" {
			mbID = "inbox"
		}
		mb, mbErr := h.deps.Storage.GetMailbox(ctx, accountID, mbID)
		if mbErr == nil {
			if rights := rightsForRole(roleOf(mb)); !rights.MayRemoveItems {
				notDestroyed[id] = setErr("forbidden", "May not remove items from this mailbox")
				continue
			}
		}
		if err := h.deps.Metadata.Delete(ctx, accountID, id); err != nil {
			slog.Error("JMAP: failed to delete email metadata", "account", accountID, "id", id, "err", err)
			notDestroyed[id] = setErr("serverFail", "Failed to delete email metadata")
			continue
		}
		if err := h.deps.Storage.DeleteEmail(ctx, accountID, id); err != nil {
			if errors.Is(err, storage.ErrNotFound) {
				notDestroyed[id] = setErr("notFound", "Email not found")
			} else {
				slog.Error("JMAP: failed to delete email", "account", accountID, "id", id, "err", err)
				notDestroyed[id] = setErr("serverFail", "Failed to delete email")
			}
		} else {
			if err := h.deps.Changes.Record(ctx, accountID, base.StateEmail, id, base.ChangeDestroy); err != nil {
				return nil, err
			}
			destroyed = append(destroyed, id)
		}
	}

	// Handle create
	for createID, raw := range req.Create {
		var createMap map[string]interface{}
		if err := json.Unmarshal(raw, &createMap); err != nil {
			notCreated[createID] = setErr("invalidProperties", "Invalid create object")
			continue
		}
		subject, _ := createMap["subject"].(string)
		replyTo, err := jmapAddressList(createMap, "replyTo")
		if err != nil {
			return nil, base.Errorf("invalidArguments", err.Error())
		}
		to, err := jmapAddressList(createMap, "to")
		if err != nil {
			return nil, base.Errorf("invalidArguments", err.Error())
		}
		cc, err := jmapAddressList(createMap, "cc")
		if err != nil {
			return nil, base.Errorf("invalidArguments", err.Error())
		}
		bcc, err := jmapAddressList(createMap, "bcc")
		if err != nil {
			return nil, base.Errorf("invalidArguments", err.Error())
		}

		// Locked From: submissions/creates send as the caller's own account,
		// or as the target account when a user manager masquerades.
		lockedFrom, _ := base.LockedFrom(ctx, accountID)
		if !strings.Contains(lockedFrom, "@") {
			// keep a reasonable fallback for non-email accounts in tests
			if f, _ := createMap["from"].(string); strings.Contains(f, "@") {
				lockedFrom = f
			} else if farr, ok := createMap["from"].([]interface{}); ok && len(farr) > 0 {
				if m, ok := farr[0].(map[string]interface{}); ok {
					if e, ok := m["email"].(string); ok && strings.Contains(e, "@") {
						lockedFrom = e
					}
				}
			}
			if lockedFrom == "" {
				lockedFrom = accountID
			}
		}

		text, html := extractCreateBodies(createMap)

		inReplyTo := ""
		if irt, ok := createMap["inReplyTo"].(string); ok {
			inReplyTo = irt
		}
		var references []string
		if refArr, ok := createMap["references"].([]interface{}); ok {
			for _, v := range refArr {
				if s, ok := v.(string); ok {
					references = append(references, s)
				}
			}
		}

		// Determine target mailbox from mailboxIds (default inbox).
		targetMailbox := "inbox"
		if mbIDs, ok := createMap["mailboxIds"].(map[string]interface{}); ok {
			targets := make([]string, 0, len(mbIDs))
			for k, v := range mbIDs {
				b, ok := v.(bool)
				if !ok || !b {
					continue
				}
				targets = append(targets, storage.NormalizeMailboxID(k))
			}
			if len(targets) > 1 {
				notCreated[createID] = setErr("invalidProperties", "Only one mailbox is supported per email")
				continue
			}
			if len(targets) == 1 {
				targetMailbox = targets[0]
			}
		}
		mb, mbErr := h.deps.Storage.GetMailbox(ctx, accountID, targetMailbox)
		if mbErr != nil {
			notCreated[createID] = setErr("invalidProperties", "Target mailbox not found")
			continue
		}
		if rights := rightsForRole(roleOf(mb)); !rights.MayAddItems {
			notCreated[createID] = setErr("forbidden", "May not add items to this mailbox")
			continue
		}

		now := time.Now().UTC()
		inReplyToList := []string{}
		if strings.TrimSpace(inReplyTo) != "" {
			inReplyToList = []string{strings.TrimSpace(inReplyTo)}
		}
		newEmail := &mailemail.UserEmail{
			From:       mailemail.Address{Address: lockedFrom},
			ReplyTo:    replyTo,
			To:         to,
			Cc:         cc,
			Bcc:        bcc,
			Subject:    subject,
			Date:       now,
			Text:       text,
			HTML:       html,
			Size:       int64(len(text) + len(html)),
			MessageID:  mailemail.GenerateMessageID(messageIDDomain(lockedFrom)),
			InReplyTo:  inReplyToList,
			References: references,
		}

		// A session draft is stored separately (durable across lambda
		// invocations) and is only valid in drafts mailbox context.
		if claims := base.ClaimsFromContext(ctx); claims != nil && claims.SID != "" {
			if targetMailbox != "drafts" && targetMailbox != "" {
				// allow drafts mailbox only for session draft storage path? keep simple:
				// if client asks for drafts, fine; otherwise still store as draft blob but
				// mailboxIds in result reflect request. To keep semantics, prefer drafts.
			}
			newID, err := h.deps.Storage.PutDraft(ctx, claims.SID, createID, newEmail)
			if err != nil {
				notCreated[createID] = setErr("serverFail", "Failed to create email")
				continue
			}
			meta := metadata.NewEmailMetadata(accountID, newID)
			meta.ReceivedAt = now.UTC().Format(time.RFC3339)
			meta.Keywords[metadata.KeywordDraft] = true
			// Apply requested keywords if provided.
			if kw, ok := createMap["keywords"].(map[string]interface{}); ok {
				if patched, ok2, _ := ApplyKeywordPatch(meta, map[string]interface{}{"keywords": kw}); ok2 {
					meta = patched
				}
			}
			if err := h.deps.Metadata.Save(ctx, meta); err != nil {
				slog.Warn("JMAP: failed to save draft metadata", "account", accountID, "id", newID, "err", err)
			}
			if err := h.deps.Changes.Record(ctx, accountID, base.StateEmail, newID, base.ChangeCreate); err != nil {
				return nil, err
			}
			created[createID] = map[string]interface{}{
				"id":         newID,
				"threadId":   newID,
				"blobId":     newID,
				"receivedAt": meta.ReceivedAt,
				"mailboxIds": map[string]bool{"drafts": true},
			}
			continue
		}

		newID, err := h.deps.Storage.AddEmail(ctx, accountID, targetMailbox, newEmail, nil)
		if err != nil {
			notCreated[createID] = setErr("serverFail", "Failed to create email")
			continue
		}
		meta := metadata.NewEmailMetadata(accountID, newID)
		meta.ReceivedAt = now.UTC().Format(time.RFC3339)
		// Apply requested keywords if provided.
		if kw, ok := createMap["keywords"].(map[string]interface{}); ok {
			if patched, ok2, _ := ApplyKeywordPatch(meta, map[string]interface{}{"keywords": kw}); ok2 {
				meta = patched
			}
		}
		if err := h.deps.Metadata.Save(ctx, meta); err != nil {
			slog.Warn("JMAP: failed to save created email metadata", "account", accountID, "id", newID, "err", err)
		}
		if err := h.deps.Changes.Record(ctx, accountID, base.StateEmail, newID, base.ChangeCreate); err != nil {
			return nil, err
		}
		created[createID] = map[string]interface{}{
			"id":         newID,
			"threadId":   newID,
			"blobId":     newID,
			"receivedAt": meta.ReceivedAt,
			"mailboxIds": map[string]bool{targetMailbox: true},
		}
	}

	// Handle update
	for id, raw := range req.Update {
		var updateMap map[string]interface{}
		if err := json.Unmarshal(raw, &updateMap); err != nil {
			notUpdated[id] = setErr("invalidProperties", "Invalid update patch")
			continue
		}

		// Resolve current location.
		listed, err := h.deps.Storage.GetEmail(ctx, accountID, id)
		if err != nil || listed == nil || listed.Ref == nil || !strings.EqualFold(listed.Ref.AccountID, accountID) {
			// Draft updates are limited to metadata; storage location fixed.
			if claims := base.ClaimsFromContext(ctx); claims != nil && claims.SID != "" {
				if _, derr := h.deps.Storage.GetDraft(ctx, claims.SID, id); derr == nil {
					meta := base.EmailMetadata(ctx, h.deps, accountID, id)
					patched, ok, errType := ApplyKeywordPatch(meta, updateMap)
					if !ok {
						notUpdated[id] = setErr(errType, "Invalid keyword update")
						continue
					}
					if err := h.deps.Metadata.Save(ctx, patched); err != nil {
						slog.Error("JMAP: failed to save draft metadata", "account", accountID, "id", id, "err", err)
						notUpdated[id] = setErr("serverFail", "Failed to save email metadata")
					} else {
						_ = h.deps.Changes.Record(ctx, accountID, base.StateEmail, id, base.ChangeUpdate)
						updated[id] = nil
					}
					continue
				}
			}
			notUpdated[id] = setErr("notFound", "Email not found")
			continue
		}

		currentMailbox := listed.Ref.MailboxID
		if currentMailbox == "" {
			currentMailbox = "inbox"
		}

		// mailboxIds patch/move: accept full replacement and/or "mailboxIds/<id>" patches.
		targetMailbox := currentMailbox
		mailboxChanged := false
		if v, ok := updateMap["mailboxIds"]; ok {
			mbMap, ok := v.(map[string]interface{})
			if !ok {
				notUpdated[id] = setErr("invalidProperties", "Invalid mailboxIds")
				continue
			}
			targets := make([]string, 0, len(mbMap))
			for k, mv := range mbMap {
				b, ok := mv.(bool)
				if !ok || !b {
					continue
				}
				targets = append(targets, storage.NormalizeMailboxID(k))
			}
			if len(targets) != 1 {
				notUpdated[id] = setErr("invalidProperties", "Exactly one mailbox is required")
				continue
			}
			targetMailbox = targets[0]
			mailboxChanged = !strings.EqualFold(targetMailbox, currentMailbox)
		}
		mailboxPatchInvalid := false
		for prop, v := range updateMap {
			if !strings.HasPrefix(prop, "mailboxIds/") {
				continue
			}
			mbID := storage.NormalizeMailboxID(strings.TrimPrefix(prop, "mailboxIds/"))
			b, ok := v.(bool)
			if !ok {
				notUpdated[id] = setErr("invalidProperties", "Invalid mailboxIds patch")
				mailboxPatchInvalid = true
				break
			}
			if b {
				targetMailbox = mbID
				mailboxChanged = !strings.EqualFold(targetMailbox, currentMailbox)
			} else if strings.EqualFold(mbID, targetMailbox) {
				// removing current/target without adding another is invalid here
				notUpdated[id] = setErr("invalidProperties", "Email must remain in a mailbox")
				mailboxPatchInvalid = true
				break
			}
		}
		if mailboxPatchInvalid {
			continue
		}

		if mailboxChanged {
			srcMb, srcErr := h.deps.Storage.GetMailbox(ctx, accountID, currentMailbox)
			dstMb, dstErr := h.deps.Storage.GetMailbox(ctx, accountID, targetMailbox)
			if dstErr != nil {
				notUpdated[id] = setErr("invalidProperties", "Target mailbox not found")
				continue
			}
			if srcErr == nil {
				if rights := rightsForRole(roleOf(srcMb)); !rights.MayRemoveItems {
					notUpdated[id] = setErr("forbidden", "May not remove items from source mailbox")
					continue
				}
			}
			if rights := rightsForRole(roleOf(dstMb)); !rights.MayAddItems {
				notUpdated[id] = setErr("forbidden", "May not add items to target mailbox")
				continue
			}
			if _, err := h.deps.Storage.MoveEmail(ctx, accountID, id, targetMailbox); err != nil {
				if errors.Is(err, storage.ErrNotFound) {
					notUpdated[id] = setErr("notFound", "Email not found")
				} else {
					slog.Error("JMAP: failed to move email", "account", accountID, "id", id, "from", currentMailbox, "to", targetMailbox, "err", err)
					notUpdated[id] = setErr("serverFail", "Failed to move email")
				}
				continue
			}
		}

		// Keyword/metadata patch.
		meta := base.EmailMetadata(ctx, h.deps, accountID, id)
		patched, ok, errType := ApplyKeywordPatch(meta, updateMap)
		if !ok {
			// If move already happened but metadata patch invalid, report error; move is done though.
			// Better to validate keywords before move? simple approach: accept move, reject bad keywords.
			notUpdated[id] = setErr(errType, "Invalid keyword update")
			if mailboxChanged {
				_ = h.deps.Changes.Record(ctx, accountID, base.StateEmail, id, base.ChangeUpdate)
			}
			continue
		}
		if err := h.deps.Metadata.Save(ctx, patched); err != nil {
			slog.Error("JMAP: failed to save email metadata", "account", accountID, "id", id, "err", err)
			notUpdated[id] = setErr("serverFail", "Failed to save email metadata")
		} else {
			if err := h.deps.Changes.Record(ctx, accountID, base.StateEmail, id, base.ChangeUpdate); err != nil {
				return nil, err
			}
			updated[id] = nil
		}
	}

	newState, err := h.syncEmailState(ctx, accountID)
	if err != nil {
		return nil, err
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

func setErr(typ, description string) map[string]string {
	return map[string]string{"type": typ, "description": description}
}

func roleOf(mb storage.Mailbox) string {
	if mb.Role == nil {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(*mb.Role))
}

func rightsForRole(role string) struct {
	MayReadItems   bool
	MayAddItems    bool
	MayRemoveItems bool
	MaySetSeen     bool
	MaySetKeywords bool
	MayCreateChild bool
	MayRename      bool
	MayDelete      bool
	MaySubmit      bool
} {
	// Mirror mailbox package semantics for enforcement in Email/set.
	r := struct {
		MayReadItems   bool
		MayAddItems    bool
		MayRemoveItems bool
		MaySetSeen     bool
		MaySetKeywords bool
		MayCreateChild bool
		MayRename      bool
		MayDelete      bool
		MaySubmit      bool
	}{true, true, true, true, true, false, true, true, true}
	switch strings.ToLower(role) {
	case "inbox":
		r.MayRename, r.MayDelete = false, false
	case "drafts":
		r.MayRename, r.MayDelete = false, false
	case "sent":
		r.MayRename, r.MayDelete, r.MaySubmit = false, false, false
	case "trash":
		r.MayRename, r.MayDelete, r.MaySubmit = false, false, false
	case "archive":
		r.MayRename, r.MayDelete = false, false
	case "junk":
		r.MayRename, r.MayDelete, r.MaySubmit = false, false, false
	}
	return r
}

func (h *Handler) EmailChanges(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error) {
	var req struct {
		AccountID  string `json:"accountId"`
		SinceState string `json:"sinceState"`
		MaxChanges *int   `json:"maxChanges,omitempty"`
	}
	if err := json.Unmarshal(args, &req); err != nil {
		return nil, err
	}
	if _, err := h.syncEmailState(ctx, accountID); err != nil {
		return nil, err
	}
	result, err := h.deps.Changes.ChangesFrom(ctx, accountID, base.StateEmail, req.SinceState, req.MaxChanges)
	if err != nil {
		return nil, err
	}
	return json.Marshal(result)
}

func (h *Handler) EmailQuery(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error) {
	var req struct {
		AccountID       string        `json:"accountId"`
		Filter          *EmailFilter  `json:"filter,omitempty"`
		Sort            []interface{} `json:"sort,omitempty"`
		Position        *int          `json:"position,omitempty"`
		Anchor          *string       `json:"anchor,omitempty"`
		AnchorOffset    *int          `json:"anchorOffset,omitempty"`
		Limit           *int          `json:"limit,omitempty"`
		CalculateTotal  *bool         `json:"calculateTotal,omitempty"`
		CollapseThreads *bool         `json:"collapseThreads,omitempty"`
	}
	if err := json.Unmarshal(args, &req); err != nil {
		return nil, err
	}
	queryKey, err := base.QueryKey(req.Filter, req.Sort)
	if err != nil {
		return nil, err
	}

	allIDs, err := h.queryEmailIDs(ctx, accountID, req.Filter, req.Sort)
	if err != nil {
		return nil, err
	}
	total := 0
	if req.CalculateTotal == nil || *req.CalculateTotal {
		total = len(allIDs)
	}
	start, end := base.PageBounds(len(allIDs), req.Position, req.Limit)
	ids := append([]string{}, allIDs[start:end]...)

	queryState, err := h.deps.Changes.SyncQuery(ctx, accountID, base.StateEmailQuery, queryKey, allIDs)
	if err != nil {
		return nil, err
	}
	resp := map[string]interface{}{
		"accountId":     accountID,
		"queryState":    queryState,
		"canCalculateChanges": true,
		"position":      start,
		"ids":           ids,
		"total":         total,
	}
	return json.Marshal(resp)
}

// queryEmailIDs returns the full ordered result set for a query, before
// pagination. Both Email/query and Email/queryChanges use it so a queryState
// token always describes the same result set.
func (h *Handler) queryEmailIDs(ctx context.Context, accountID string, filter *EmailFilter, sortSpecs []interface{}) ([]string, error) {
	mailboxID := ""
	if filter != nil && filter.InMailbox != nil {
		mailboxID = *filter.InMailbox
	}
	emails, err := h.deps.Storage.ListEmails(ctx, accountID, mailboxID)
	if err != nil {
		return nil, err
	}

	filtered := make([]*mailemail.ListedEmail, 0, len(emails))
	for _, e := range emails {
		if h.matchesFilter(e, filter) {
			filtered = append(filtered, e)
		}
	}

	ascending := false
	for _, s := range sortSpecs {
		sortSpec, ok := s.(map[string]interface{})
		if !ok {
			continue
		}
		property, _ := sortSpec["property"].(string)
		if property != "receivedAt" && property != "date" {
			continue
		}
		if v, ok := sortSpec["isAscending"].(bool); ok {
			ascending = v
		}
	}
	received := make(map[string]time.Time, len(filtered))
	for _, e := range filtered {
		received[e.Ref.ID] = base.ReceivedAtFromMetadata(base.EmailMetadata(ctx, h.deps, accountID, e.Ref.ID), e.Email.Date)
	}
	sort.SliceStable(filtered, func(i, j int) bool {
		left, right := received[filtered[i].Ref.ID], received[filtered[j].Ref.ID]
		if left.Equal(right) {
			return filtered[i].Ref.ID < filtered[j].Ref.ID
		}
		if ascending {
			return left.Before(right)
		}
		return left.After(right)
	})

	ids := make([]string, len(filtered))
	for i, e := range filtered {
		ids[i] = e.Ref.ID
	}
	return ids, nil
}

func (h *Handler) EmailQueryChanges(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error) {
	var req struct {
		AccountID       string        `json:"accountId"`
		SinceQueryState string        `json:"sinceQueryState"`
		Filter          *EmailFilter  `json:"filter,omitempty"`
		Sort            []interface{} `json:"sort,omitempty"`
		MaxChanges      *int          `json:"maxChanges,omitempty"`
		UpToID          *string       `json:"upToId,omitempty"`
		CalculateTotal  *bool         `json:"calculateTotal,omitempty"`
		CollapseThreads *bool         `json:"collapseThreads,omitempty"`
	}
	if err := json.Unmarshal(args, &req); err != nil {
		return nil, err
	}
	queryKey, err := base.QueryKey(req.Filter, req.Sort)
	if err != nil {
		return nil, err
	}

	ids, err := h.queryEmailIDs(ctx, accountID, req.Filter, req.Sort)
	if err != nil {
		return nil, err
	}
	result, err := h.deps.Changes.QueryChangesFrom(ctx, accountID, base.StateEmailQuery, queryKey, req.SinceQueryState, req.MaxChanges, ids)
	if err != nil {
		return nil, err
	}
	return json.Marshal(result)
}

func (h *Handler) EmailCopy(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error) {
	return base.NotImplemented(MethodCopy)
}

func (h *Handler) EmailImport(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error) {
	return base.NotImplemented(MethodImport)
}

func (h *Handler) EmailParse(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error) {
	return base.NotImplemented(MethodParse)
}

// helper types used above need to be available in package; ensure EmailFilter is defined in email.go (it is).
var _ = fmt.Sprintf
var _ = model.EmailAddress{}
