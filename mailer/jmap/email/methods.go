package email

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sort"
	"strings"
	"time"

	mailemail "github.com/kncept/Emissary/mailer/email"
	"github.com/kncept/Emissary/mailer/jmap/internal/base"
	"github.com/kncept/Emissary/mailer/metadata"
	"github.com/kncept/Emissary/mailer/storage"
)

func (h *Handler) EmailGet(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error) {
	var req struct {
		AccountID           string   `json:"accountId"`
		IDs                 []string `json:"ids,omitempty"`
		Properties          []string `json:"properties,omitempty"`
		FetchTextBodyValues bool     `json:"fetchTextBodyValues"`
		FetchHTMLBodyValues bool     `json:"fetchHTMLBodyValues"`
	}
	if err := json.Unmarshal(args, &req); err != nil {
		return nil, err
	}

	// Email/get must only expose messages in the requested account. When a
	// user with EmissaryUserManagement masquerades, accountID is the target
	// account supplied in the call.
	emails, err := h.deps.Storage.ListEmails(ctx, accountID, "")
	if err != nil {
		return nil, err
	}

	// Build email map
	emailMap := make(map[string]*mailemail.ListedEmail)
	for _, e := range emails {
		emailMap[e.Ref.ID] = e
	}

	var list []JMAPEmail
	var notFound []string

	if len(req.IDs) > 0 {
		for _, id := range req.IDs {
			if e, ok := emailMap[id]; ok {
				jmapEmail, err := h.toJMAPEmail(ctx, e)
				if err != nil {
					return nil, err
				}
				jmapEmail.setBodyValues(e.Email, req.FetchTextBodyValues, req.FetchHTMLBodyValues)
				list = append(list, *jmapEmail)
			} else {
				notFound = append(notFound, id)
			}
		}
	} else {
		for _, e := range emails {
			jmapEmail, err := h.toJMAPEmail(ctx, e)
			if err != nil {
				return nil, err
			}
			jmapEmail.setBodyValues(e.Email, req.FetchTextBodyValues, req.FetchHTMLBodyValues)
			list = append(list, *jmapEmail)
		}
	}

	resp := map[string]interface{}{
		"accountId": accountID,
		"state":     h.syncEmailStateFromListed(ctx, accountID, emails),
		"list":      list,
		"notFound":  notFound,
	}
	return json.Marshal(resp)
}

func (h *Handler) EmailSet(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error) {
	var req struct {
		AccountID string                 `json:"accountId"`
		Create    map[string]interface{} `json:"create,omitempty"`
		Update    map[string]interface{} `json:"update,omitempty"`
		Destroy   []string               `json:"destroy,omitempty"`
	}
	if err := json.Unmarshal(args, &req); err != nil {
		return nil, err
	}

	// Sync first so external changes are already reflected in oldState.
	oldState := h.syncEmailState(ctx, accountID)

	created := make(map[string]interface{})
	updated := make(map[string]interface{})
	destroyed := []string{}
	notCreated := make(map[string]interface{})
	notUpdated := make(map[string]interface{})
	notDestroyed := make(map[string]interface{})

	// Handle destroy. Verify the message belongs to the requested account so
	// a crafted id cannot delete another user's mail.
	for _, id := range req.Destroy {
		listed, err := h.deps.Storage.GetEmail(ctx, id)
		if err != nil || listed.Ref == nil || !strings.EqualFold(listed.Ref.AccountID, accountID) {
			notDestroyed[id] = map[string]string{"type": "notFound", "description": "Email not found"}
			continue
		}
		// Delete the metadata record before the message. Metadata Delete is
		// idempotent, so if the storage delete fails a client retry can safely
		// repeat it; deleting the message first could leave metadata behind
		// that a retry can no longer reach. If metadata deletion fails, keep
		// the message so the whole destroy can be retried.
		if err := h.deps.Metadata.Delete(ctx, accountID, id); err != nil {
			slog.Error("JMAP: failed to delete email metadata", "account", accountID, "id", id, "err", err)
			notDestroyed[id] = map[string]string{"type": "serverFail", "description": "Failed to delete metadata: " + err.Error()}
			continue
		}
		if err := h.deps.Storage.DeleteEmail(ctx, id); err != nil {
			if errors.Is(err, storage.ErrNotFound) {
				notDestroyed[id] = map[string]string{"type": "notFound", "description": "Email not found"}
			} else {
				notDestroyed[id] = map[string]string{"type": "serverFail", "description": err.Error()}
			}
			continue
		}
		destroyed = append(destroyed, id)
	}

	// Handle create
	for createID, createData := range req.Create {
		createMap, ok := createData.(map[string]interface{})
		if !ok {
			notCreated[createID] = map[string]string{"type": "invalidProperties", "description": "Invalid create data"}
			continue
		}

		// Extract email data. The From address is locked to the authenticated
		// user (or the masquerade target account), so a client-supplied
		// "from" header is ignored.
		from, _ := base.LockedFrom(ctx, accountID)

		to, err := jmapAddressList(createMap, "to")
		if err != nil {
			return nil, err
		}
		cc, err := jmapAddressList(createMap, "cc")
		if err != nil {
			return nil, err
		}
		bcc, err := jmapAddressList(createMap, "bcc")
		if err != nil {
			return nil, err
		}

		var replyTo []mailemail.Address
		if replyToArr, ok := createMap["replyTo"].([]interface{}); ok {
			for _, r := range replyToArr {
				if rm, ok := r.(map[string]interface{}); ok {
					if address, ok := rm["email"].(string); ok {
						replyTo = append(replyTo, mailemail.Address{Address: address})
					}
				}
			}
		}

		subject := ""
		if s, ok := createMap["subject"].(string); ok {
			subject = s
		}

		bodyValues := map[string]string{}
		if bvMap, ok := createMap["bodyValues"].(map[string]interface{}); ok {
			for partID, v := range bvMap {
				if bv, ok := v.(map[string]interface{}); ok {
					if val, ok := bv["value"].(string); ok {
						bodyValues[partID] = val
					}
				}
			}
		}

		var text string
		var html string
		var textBodySize int
		var htmlBodySize int
		if tbArr, ok := createMap["textBody"].([]interface{}); ok && len(tbArr) > 0 {
			if tb, ok := tbArr[0].(map[string]interface{}); ok {
				if size, ok := tb["size"].(float64); ok {
					textBodySize = int(size)
				}
				if partID, ok := tb["partId"].(string); ok {
					text = bodyValues[partID]
				}
			}
		}
		if hbArr, ok := createMap["htmlBody"].([]interface{}); ok && len(hbArr) > 0 {
			if hb, ok := hbArr[0].(map[string]interface{}); ok {
				if size, ok := hb["size"].(float64); ok {
					htmlBodySize = int(size)
				}
				if partID, ok := hb["partId"].(string); ok {
					html = bodyValues[partID]
				}
			}
		}
		// Clients that only send a single body value (without a matching part
		// id) still populate the text part.
		if text == "" && html == "" {
			for _, value := range bodyValues {
				text = value
				break
			}
		}

		keywords := map[string]bool{}
		if kwMap, ok := createMap["keywords"].(map[string]interface{}); ok {
			for k, v := range kwMap {
				if b, ok := v.(bool); ok && b {
					keywords[k] = true
				}
			}
		}

		var inReplyTo []string
		if irArr, ok := createMap["inReplyTo"].([]interface{}); ok {
			for _, v := range irArr {
				if s, ok := v.(string); ok {
					inReplyTo = append(inReplyTo, s)
				}
			}
		}

		var references []string
		if refArr, ok := createMap["references"].([]interface{}); ok {
			for _, v := range refArr {
				if s, ok := v.(string); ok {
					references = append(references, s)
				}
			}
		}

		// Created mail is stored in the account's inbox (AddEmail assigns the
		// mailbox itself). When a session id is available, use durable
		// per-session draft storage so the draft survives across Lambda
		// invocations (S3Storage cannot add to the delivered-mail tree).
		now := time.Now().UTC()
		newEmail := &mailemail.UserEmail{
			From:       mailemail.Address{Address: from},
			ReplyTo:    replyTo,
			To:         to,
			Cc:         cc,
			Bcc:        bcc,
			Subject:    subject,
			Date:       now,
			Text:       text,
			HTML:       html,
			Size:       int64(textBodySize + htmlBodySize),
			MessageID:  mailemail.GenerateMessageID(messageIDDomain(from)),
			InReplyTo:  inReplyTo,
			References: references,
		}

		var newID string
		if claims := base.ClaimsFromContext(ctx); claims != nil && claims.SID != "" {
			var err error
			newID, err = h.deps.Storage.PutDraft(ctx, claims.SID, createID, newEmail)
			if err != nil {
				notCreated[createID] = map[string]string{"type": "serverFail", "description": "Failed to save draft: " + err.Error()}
				continue
			}
		} else {
			var err error
			// Messages created through JMAP have no original wire bytes; the
			// store renders the .eml from the parsed fields in that case.
			newID, err = h.deps.Storage.AddEmail(ctx, accountID, newEmail, nil)
			if err != nil {
				notCreated[createID] = map[string]string{"type": "serverFail", "description": "Failed to save email: " + err.Error()}
				continue
			}
		}

		// Initialize metadata. Created emails carry their JMAP keywords, and
		// the constructor stamps a server-owned receivedAt. Deliveries get a
		// record from the receiver with no keywords. Any client-supplied
		// receivedAt property is intentionally ignored.
		if err := h.deps.Metadata.Save(ctx, metadata.NewEmailMetadataFromMap(accountID, newID, keywords)); err != nil {
			notCreated[createID] = map[string]string{"type": "serverFail", "description": "Failed to save metadata: " + err.Error()}
			continue
		}

		created[createID] = map[string]interface{}{
			"id": newID,
		}
	}

	// Handle keyword updates. JMAP /set.update is a patch: "keywords/<name>"
	// with true adds a keyword and null removes it (false is not a valid
	// keyword value). A whole "keywords" object replaces the keyword set.
	for id, patchData := range req.Update {
		patch, ok := patchData.(map[string]interface{})
		if !ok {
			notUpdated[id] = map[string]string{"type": "invalidProperties", "description": "Invalid update patch"}
			continue
		}
		// A draft id is session-relative, so it is not returned by GetEmail.
		if !h.emailInAccount(ctx, accountID, id) {
			notUpdated[id] = map[string]string{"type": "notFound", "description": "Email not found"}
			continue
		}
		updatedMetadata, ok, errorType := ApplyKeywordPatch(base.EmailMetadata(ctx, h.deps, accountID, id), patch)
		if !ok {
			notUpdated[id] = map[string]string{"type": errorType, "description": "Invalid or unsupported keyword patch"}
			continue
		}
		if err := h.deps.Metadata.Save(ctx, updatedMetadata); err != nil {
			notUpdated[id] = map[string]string{"type": "serverFail", "description": "Failed to save metadata: " + err.Error()}
			continue
		}
		updated[id] = nil
	}

	resp := map[string]interface{}{
		"accountId":    accountID,
		"oldState":     oldState,
		"newState":     h.syncEmailState(ctx, accountID),
		"created":      created,
		"updated":      updated,
		"destroyed":    destroyed,
		"notCreated":   notCreated,
		"notUpdated":   notUpdated,
		"notDestroyed": notDestroyed,
	}
	return json.Marshal(resp)
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

	h.syncEmailState(ctx, accountID)
	result, err := h.deps.Changes.ChangesFrom(accountID, base.StateEmail, req.SinceState, req.MaxChanges)
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

	// Read only from the requested account's mailbox. The accountId is the
	// authenticated user's id, or the target account when a user with
	// EmissaryUserManagement is masquerading. An inMailbox filter is pushed into
	// storage so only that JMAP mailbox is listed.
	mailboxID := ""
	if req.Filter != nil && req.Filter.InMailbox != nil {
		mailboxID = *req.Filter.InMailbox
	}
	emails, err := h.deps.Storage.ListEmails(ctx, accountID, mailboxID)
	if err != nil {
		return nil, err
	}

	// Apply filter
	filtered := make([]*mailemail.ListedEmail, 0)
	for _, e := range emails {
		if h.matchesFilter(e, req.Filter) {
			filtered = append(filtered, e)
		}
	}

	// JMAP defaults to receivedAt descending; honour an explicit receivedAt
	// sort direction from the client. Sorting uses the server-recorded receive
	// time (falling back to the message Date for legacy records) so it matches
	// the receivedAt value returned by Email/get.
	ascending := false
	for _, s := range req.Sort {
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

	start, end := base.PageBounds(len(filtered), req.Position, req.Limit)
	result := filtered[start:end]
	ids := make([]string, len(result))
	for i, e := range result {
		ids[i] = e.Ref.ID
	}

	total := len(filtered)
	if req.CalculateTotal != nil && !*req.CalculateTotal {
		total = -1 // Indicate total not calculated
	}

	resp := map[string]interface{}{
		"accountId":  accountID,
		"queryState": h.syncEmailState(ctx, accountID),
		"ids":        ids,
		"total":      total,
	}
	return json.Marshal(resp)
}

func (h *Handler) EmailQueryChanges(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error) {
	var req struct {
		AccountID       string        `json:"accountId"`
		SinceQueryState string        `json:"sinceQueryState"`
		Filter          interface{}   `json:"filter,omitempty"`
		Sort            []interface{} `json:"sort,omitempty"`
		MaxChanges      *int          `json:"maxChanges,omitempty"`
		UpToID          *string       `json:"upToId,omitempty"`
		CalculateTotal  *bool         `json:"calculateTotal,omitempty"`
		CollapseThreads *bool         `json:"collapseThreads,omitempty"`
	}
	if err := json.Unmarshal(args, &req); err != nil {
		return nil, err
	}

	h.syncEmailState(ctx, accountID)
	result, err := h.deps.Changes.ChangesFrom(accountID, base.StateEmailQuery, req.SinceQueryState, req.MaxChanges)
	if err != nil {
		return nil, err
	}
	return json.Marshal(base.QueryChangesResponse(result))
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
