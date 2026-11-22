package email

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/kncept/Emissary/mailer/auth"
	"github.com/kncept/Emissary/mailer/components/metadata"
	"github.com/kncept/Emissary/mailer/components/storage"
	mailemail "github.com/kncept/Emissary/mailer/email"
	"github.com/kncept/Emissary/mailer/jmap/internal/base"
	"github.com/kncept/Emissary/mailer/jmap/internal/model"
)

// setBodyValues fetches exactly the requested body value selectors. Part ids
// are the real MIME part ids from the wire view, and fetchAll covers any text
// part regardless of its media subtype (RFC 8621 bodyProperties fetchAll).
func (m *JMAPEmail) setBodyValues(fetchText, fetchHTML, fetchAll bool, maxBytes int) {
	if m.wire == nil {
		m.BodyValues = map[string]EmailBodyValue{}
		return
	}
	m.BodyValues = m.wire.bodyValues(fetchText, fetchHTML, fetchAll, maxBytes)
}

// truncateBodyValue enforces maxBodyValueBytes without splitting a UTF-8
// codepoint. For HTML it also avoids ending inside a tag where possible, as
// recommended by RFC 8621 section 4.2.
func truncateBodyValue(value string, maxBytes int, contentType string) EmailBodyValue {
	result := EmailBodyValue{Value: value}
	if maxBytes <= 0 || len(value) <= maxBytes {
		return result
	}
	cut := value[:maxBytes]
	for len(cut) > 0 && !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	if contentType == "text/html" && strings.HasPrefix(strings.ToLower(strings.TrimSpace(cut)), "<") {
		// Back up to the end of the last complete tag rather than slicing it.
		if open := strings.LastIndex(cut, "<"); open >= 0 && open > strings.LastIndex(cut, ">") {
			cut = cut[:open]
		}
	}
	result.Value = cut
	result.IsTruncated = true
	return result
}

// filtered applies bodyProperties to every returned EmailBodyPart and then
// properties to the Email object itself. Dynamic header properties are
// injected from the parsed wire view before selection, so only requested
// header:{name} fields can appear in the response.
func (m *JMAPEmail) filtered(properties, bodyProperties *base.Selection) (json.RawMessage, error) {
	raw, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, err
	}
	if err := injectHeaderProperties(obj, properties); err != nil {
		return nil, err
	}
	for _, key := range []string{"bodyStructure", "textBody", "htmlBody", "attachments"} {
		value, ok := obj[key]
		if !ok {
			continue
		}
		value, err = injectBodyHeaders(value, bodyProperties)
		if err != nil {
			return nil, err
		}
		obj[key], err = filterBodyPart(value, bodyProperties)
		if err != nil {
			return nil, err
		}
	}
	selected, err := json.Marshal(obj)
	if err != nil {
		return nil, err
	}
	return properties.FilterRaw(selected)
}

// injectBodyHeaders adds dynamic header properties to every node of a MIME
// tree (a single part or an array of parts) before bodyProperties filtering.
func injectBodyHeaders(raw json.RawMessage, sel *base.Selection) (json.RawMessage, error) {
	if sel == nil || len(sel.DynamicNames()) == 0 {
		return raw, nil
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return raw, nil
	}
	switch trimmed[0] {
	case '[':
		var items []json.RawMessage
		if err := json.Unmarshal(trimmed, &items); err != nil {
			return nil, err
		}
		for i, item := range items {
			filtered, err := injectBodyHeaders(item, sel)
			if err != nil {
				return nil, err
			}
			items[i] = filtered
		}
		return json.Marshal(items)
	case '{':
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(trimmed, &obj); err != nil {
			return nil, err
		}
		if err := injectHeaderProperties(obj, sel); err != nil {
			return nil, err
		}
		if subParts, ok := obj["subParts"]; ok {
			filtered, err := injectBodyHeaders(subParts, sel)
			if err != nil {
				return nil, err
			}
			obj["subParts"] = filtered
		}
		return json.Marshal(obj)
	default:
		return raw, nil
	}
}

// filterBodyPart selects bodyProperties recursively. The "headers" array is
// an array of EmailHeader objects, not EmailBodyParts, so it is passed through
// unmodified when requested instead of being filtered as a body part.
func filterBodyPart(raw json.RawMessage, sel *base.Selection) (json.RawMessage, error) {
	if sel == nil {
		return raw, nil
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return raw, nil
	}
	switch trimmed[0] {
	case '[':
		var items []json.RawMessage
		if err := json.Unmarshal(trimmed, &items); err != nil {
			return nil, err
		}
		for i, item := range items {
			filtered, err := filterBodyPart(item, sel)
			if err != nil {
				return nil, err
			}
			items[i] = filtered
		}
		return json.Marshal(items)
	case '{':
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(trimmed, &obj); err != nil {
			return nil, err
		}
		if subParts, ok := obj["subParts"]; ok {
			filtered, err := filterBodyPart(subParts, sel)
			if err != nil {
				return nil, err
			}
			obj["subParts"] = filtered
		}
		if !sel.Has("headers") {
			delete(obj, "headers")
		}
		return json.Marshal(sel.Apply(obj))
	default:
		return raw, nil
	}
}

// jmapAddressList converts a JMAP address property (to, cc, bcc) into internal
// addresses. Client-supplied values are untrusted, so malformed entries are
// reported as invalidArguments instead of panicking on a type assertion.
func jmapAddressList(createMap map[string]interface{}, property string) ([]mailemail.Address, error) {
	entries, ok := createMap[property].([]interface{})
	if !ok {
		return nil, nil
	}
	addresses := make([]mailemail.Address, 0, len(entries))
	for _, entry := range entries {
		object, ok := entry.(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("%s entries must be objects", property)
		}
		address, ok := object["email"].(string)
		if !ok {
			return nil, fmt.Errorf("%s entry has a non-string email", property)
		}
		addresses = append(addresses, mailemail.Address{Address: address})
	}
	return addresses, nil
}

// messageIDs renders a single Message-ID value as the JMAP messageId list.
func messageIDs(messageID string) []string {
	if messageID = strings.TrimSpace(messageID); messageID != "" {
		return []string{messageID}
	}
	return []string{}
}

// messageIDDomain extracts the domain part of an address for use as the
// Message-ID domain; an empty result makes GenerateMessageID fall back to a
// local domain.
func messageIDDomain(address string) string {
	if _, domain, ok := strings.Cut(address, "@"); ok {
		return strings.ToLower(strings.TrimSpace(domain))
	}
	return ""
}

// SyncState refreshes the Email state from storage so external changes (such
// as inbound delivery) are visible to push notifications and /changes.
func (h *Handler) SyncState(ctx context.Context, accountID string) (string, error) {
	return h.syncEmailState(ctx, accountID)
}

// syncEmailState refreshes the Email snapshots from storage.
func (h *Handler) syncEmailState(ctx context.Context, accountID string) (string, error) {
	listed, err := h.deps.Storage.ListEmails(ctx, accountID, "")
	if err != nil {
		return h.deps.Changes.State(ctx, accountID, base.StateEmail)
	}
	return h.syncEmailStateFromListed(ctx, accountID, listed)
}

// syncEmailStateFromListed avoids a second storage listing when the caller has
// already loaded the account's full mailbox (Email/get does this).
func (h *Handler) syncEmailStateFromListed(ctx context.Context, accountID string, listed []*mailemail.ListedEmail) (string, error) {
	snapshot := make(map[string]string, len(listed))
	for _, item := range listed {
		if item == nil || item.Ref == nil || item.Email == nil {
			continue
		}
		snapshot[item.Ref.ID] = emailFingerprint(item, base.EmailMetadata(ctx, h.deps, accountID, item.Ref.ID))
	}
	if err := h.deps.Changes.Sync(ctx, accountID, map[base.StateKind]map[string]string{
		base.StateEmail: snapshot,
	}); err != nil {
		return "", err
	}
	return h.deps.Changes.State(ctx, accountID, base.StateEmail)
}

// emailFingerprint incorporates every property Emissary can change on an
// Email: its mailbox, server-owned metadata and the rendered content fields.
func emailFingerprint(listed *mailemail.ListedEmail, meta metadata.EmailMetadata) string {
	keywords := make([]string, 0, len(meta.Keywords))
	for keyword, enabled := range meta.Keywords {
		if enabled {
			keywords = append(keywords, keyword)
		}
	}
	sort.Strings(keywords)
	mailboxID := listed.Ref.MailboxID
	if mailboxID == "" {
		mailboxID = "inbox"
	}
	return fmt.Sprintf("%s|%s|%s|%d|%s|%s", mailboxID, meta.ReceivedAt,
		strings.Join(keywords, ","), listed.Email.Size, listed.Email.Subject, listed.Email.MessageID)
}

func toJMAPAddresses(addresses ...mailemail.Address) []model.EmailAddress {
	out := make([]model.EmailAddress, 0, len(addresses))
	for _, address := range addresses {
		out = append(out, model.EmailAddress{
			Name:  mailemail.DecodeHeader(address.Name),
			Email: address.Address,
		})
	}
	return out
}

// toJMAPEmail builds the structural properties and then hydrates every
// wire-derived property from one parse of the original message bytes.
func (h *Handler) toJMAPEmail(ctx context.Context, listed *mailemail.ListedEmail) (*JMAPEmail, error) {
	if listed == nil {
		return nil, fmt.Errorf("missing email")
	}
	if listed.Email == nil {
		listed.Email = &mailemail.UserEmail{}
	}
	if listed.Ref == nil {
		listed.Ref = &mailemail.EmailRef{}
	}
	e := listed.Email
	id := listed.Ref.ID
	accountID := listed.Ref.AccountID
	meta := base.EmailMetadata(ctx, h.deps, accountID, id)

	keywords := make(map[string]bool, len(meta.Keywords))
	for keyword, enabled := range meta.Keywords {
		if enabled {
			keywords[keyword] = true
		}
	}
	mailboxID := listed.Ref.MailboxID
	if mailboxID == "" {
		mailboxID = "inbox"
	}
	sentAt := e.Date.UTC().Format(time.RFC3339)
	preview := strings.TrimSpace(strings.ReplaceAll(e.Text, "\n", " "))
	if len(preview) > 256 {
		preview = preview[:256]
	}

	m := &JMAPEmail{
		ID:         id,
		ThreadID:   base.ThreadID(id, e),
		MailboxIDs: map[string]bool{mailboxID: true},
		Keywords:   keywords,
		Size:       int(e.Size),
		ReceivedAt: base.ReceivedAtFromMetadata(meta, e.Date).UTC().Format(time.RFC3339),
		MessageID:  messageIDs(e.MessageID),
		InReplyTo:  nonEmpty(e.InReplyTo...),
		References: nonEmpty(e.References...),
		Sender:     []model.EmailAddress{},
		From:       toJMAPAddresses(e.From),
		To:         toJMAPAddresses(e.To...),
		Cc:         toJMAPAddresses(e.Cc...),
		Bcc:        toJMAPAddresses(e.Bcc...),
		ReplyTo:    toJMAPAddresses(e.ReplyTo...),
		Subject:    mailemail.DecodeHeader(e.Subject),
		SentAt:     &sentAt,
		Preview:    preview,
	}
	m.hydrate(ctx, h.deps, accountID, id, e)
	return m, nil
}

func nonEmpty(values ...string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			out = append(out, value)
		}
	}
	return out
}

// hydrate fills the properties derived from the retained wire format. A store
// without raw bytes falls back to the same renderer used for new messages, so
// blobId, size and the MIME tree always describe the bytes the server keeps.
func (m *JMAPEmail) hydrate(ctx context.Context, deps *base.Deps, accountID, id string, fallback *mailemail.UserEmail) {
	var raw []byte
	if deps != nil && deps.Storage != nil {
		fetched, err := deps.Storage.GetEmailRaw(ctx, accountID, id)
		if err == nil {
			raw = fetched
		}
	}
	if len(raw) == 0 {
		raw = mailemail.RawFromUserEmail(fallback)
	}
	view := newWireView(id, raw)
	m.wire = view
	m.BlobID = storage.BlobID(raw)
	m.Size = len(raw)
	if view.root != nil {
		m.BodyStructure = &view.root.jmap
	}
	m.TextBody = view.textBody
	m.HTMLBody = view.htmlBody
	m.Attachments = view.attached
	m.Headers = view.headers
	m.HasAttachment = len(view.attached) > 0
	if sender := addressHeader(view.headers, "Sender"); len(sender) > 0 {
		m.Sender = sender
	} else if len(m.Sender) == 0 {
		// RFC 8621 section 4.1.1: when Sender is absent, From is the sender.
		m.Sender = append([]model.EmailAddress(nil), m.From...)
	}
}

func (h *Handler) matchesFilter(ctx context.Context, listed *mailemail.ListedEmail, filter *EmailFilter) bool {
	if filter == nil {
		return true
	}
	e := listed.Email
	mailboxID := listed.Ref.MailboxID

	if filter.InMailbox != nil && mailboxID != "" && !strings.EqualFold(mailboxID, *filter.InMailbox) {
		return false
	}
	if mailboxID != "" {
		for _, mb := range filter.InMailboxOtherThan {
			if strings.EqualFold(mb, mailboxID) {
				return false
			}
		}
	}

	if filter.Before != nil {
		before, err := time.Parse(time.RFC3339, *filter.Before)
		if err == nil && !e.Date.Before(before) {
			return false
		}
	}
	if filter.After != nil {
		after, err := time.Parse(time.RFC3339, *filter.After)
		if err == nil && !e.Date.After(after) {
			return false
		}
	}

	// JMAP size is the length of the stored RFC 5322 message, not the
	// convenience length on the parsed UserEmail.
	emailSize := e.Size
	has := false
	if filter.MinSize != nil || filter.MaxSize != nil || filter.HasAttachment != nil {
		if raw, err := h.deps.Storage.GetEmailRaw(ctx, listed.Ref.AccountID, listed.Ref.ID); err == nil {
			emailSize = int64(len(raw))
			has = len(newWireView(listed.Ref.ID, raw).attached) > 0
		}
	}
	if filter.MinSize != nil && emailSize < int64(*filter.MinSize) {
		return false
	}
	if filter.MaxSize != nil && emailSize > int64(*filter.MaxSize) {
		return false
	}
	if filter.HasAttachment != nil && has != *filter.HasAttachment {
		return false
	}

	if filter.Text != nil {
		text := strings.ToLower(*filter.Text)
		found := strings.Contains(strings.ToLower(e.Subject), text) ||
			strings.Contains(strings.ToLower(e.From.Address), text) ||
			strings.Contains(strings.ToLower(e.From.Name), text) ||
			strings.Contains(strings.ToLower(e.Text), text)
		if !found {
			for _, addr := range e.To {
				if strings.Contains(strings.ToLower(addr.Address), text) || strings.Contains(strings.ToLower(addr.Name), text) {
					found = true
					break
				}
			}
		}
		if !found {
			return false
		}
	}
	if filter.From != nil {
		text := strings.ToLower(*filter.From)
		if !strings.Contains(strings.ToLower(e.From.Address), text) && !strings.Contains(strings.ToLower(e.From.Name), text) {
			return false
		}
	}
	if filter.To != nil {
		text := strings.ToLower(*filter.To)
		found := false
		for _, addr := range e.To {
			if strings.Contains(strings.ToLower(addr.Address), text) || strings.Contains(strings.ToLower(addr.Name), text) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	if filter.Subject != nil {
		if !strings.Contains(strings.ToLower(e.Subject), strings.ToLower(*filter.Subject)) {
			return false
		}
	}
	if filter.Body != nil {
		if !strings.Contains(strings.ToLower(e.Text), strings.ToLower(*filter.Body)) {
			return false
		}
	}
	return true
}

// emailInAccount verifies that id belongs to accountID. Drafts use ids that
// are relative to the JMAP session, so they are looked up separately.
func (h *Handler) emailInAccount(ctx context.Context, accountID, id string) bool {
	listed, err := h.deps.Storage.GetEmail(ctx, accountID, id)
	if err == nil && listed != nil && listed.Ref != nil && strings.EqualFold(listed.Ref.AccountID, accountID) {
		return true
	}
	if claims := auth.ClaimsFromContext(ctx); claims != nil && claims.SID != "" {
		draft, err := h.deps.Storage.GetDraft(ctx, claims.SID, id)
		return err == nil && draft != nil
	}
	return false
}

// extractCreateBodies extracts text/html bodies from a JMAP Email/set create
// object. It supports both legacy "text"/"html" fields and the standard
// bodyValues + textBody/htmlBody structure.
func extractCreateBodies(createMap map[string]interface{}) (text, html string) {
	if s, ok := createMap["text"].(string); ok {
		text = s
	}
	if s, ok := createMap["html"].(string); ok {
		html = s
	}
	bodyValues, _ := createMap["bodyValues"].(map[string]interface{})
	if len(bodyValues) == 0 {
		return text, html
	}
	getPart := func(parts []interface{}, wantType string) string {
		for _, p := range parts {
			pm, ok := p.(map[string]interface{})
			if !ok {
				continue
			}
			if pt, _ := pm["type"].(string); pt != "" && !strings.EqualFold(pt, wantType) {
				continue
			}
			pid, _ := pm["partId"].(string)
			if pid == "" {
				continue
			}
			v, ok := bodyValues[pid].(map[string]interface{})
			if !ok {
				continue
			}
			if val, ok := v["value"].(string); ok {
				return val
			}
		}
		return ""
	}
	if tb, ok := createMap["textBody"].([]interface{}); ok && text == "" {
		text = getPart(tb, "text/plain")
	}
	if hb, ok := createMap["htmlBody"].([]interface{}); ok && html == "" {
		html = getPart(hb, "text/html")
	}
	// Fallbacks for common part ids or single-value cases.
	if text == "" {
		if v, ok := bodyValues["text1"].(map[string]interface{}); ok {
			if val, ok := v["value"].(string); ok {
				text = val
			}
		}
	}
	if html == "" {
		if v, ok := bodyValues["html1"].(map[string]interface{}); ok {
			if val, ok := v["value"].(string); ok {
				html = val
			}
		}
	}
	return text, html
}

// ApplyKeywordPatch applies an Email/set update patch to a metadata record.
// Per RFC 8621, "keywords/<name>": true sets a keyword and null removes it;
// false is not a valid patch value. A full "keywords" object replaces the set.
func ApplyKeywordPatch(current metadata.EmailMetadata, patch map[string]interface{}) (metadata.EmailMetadata, bool, string) {
	updated := current.Clone()

	if value, ok := patch["keywords"]; ok {
		keywords, ok := value.(map[string]interface{})
		if !ok {
			return updated, false, "invalidProperties"
		}
		replacement := make(map[string]bool)
		for name, enabled := range keywords {
			set, ok := enabled.(bool)
			if !ok {
				return updated, false, "invalidProperties"
			}
			normalized, valid := metadata.NormalizeKeyword(name)
			if !valid {
				return updated, false, "invalidProperties"
			}
			if set {
				replacement[normalized] = true
			}
		}
		updated.Keywords = replacement
	}

	for prop, value := range patch {
		if prop == "keywords" {
			continue
		}
		if !strings.HasPrefix(prop, "keywords/") {
			// mailboxIds and its patches are applied by the caller. Every
			// other patch property is unknown and must fail the whole update
			// rather than being silently ignored (RFC 8620 section 3.9).
			if prop != "mailboxIds" && !strings.HasPrefix(prop, "mailboxIds/") {
				return updated, false, "invalidProperties"
			}
			continue
		}
		normalized, valid := metadata.NormalizeKeyword(strings.TrimPrefix(prop, "keywords/"))
		if !valid {
			return updated, false, "invalidProperties"
		}
		switch v := value.(type) {
		case nil:
			delete(updated.Keywords, normalized)
		case bool:
			if !v {
				return updated, false, "invalidProperties"
			}
			updated.Keywords[normalized] = true
		default:
			return updated, false, "invalidProperties"
		}
	}

	return updated, true, ""
}
