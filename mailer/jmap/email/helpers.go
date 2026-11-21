package email

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/kncept/Emissary/mailer/auth"
	"github.com/kncept/Emissary/mailer/components/metadata"
	mailemail "github.com/kncept/Emissary/mailer/email"
	"github.com/kncept/Emissary/mailer/jmap/internal/base"
	"github.com/kncept/Emissary/mailer/jmap/internal/model"
)

func (m *JMAPEmail) setBodyValues(e *mailemail.UserEmail, fetchText, fetchHTML bool, maxBytes int) {
	values := map[string]EmailBodyValue{}
	if fetchText && e.Text != "" {
		values["text1"] = truncateBodyValue(e.Text, maxBytes, "text/plain")
	}
	if fetchHTML && e.HTML != "" {
		values["html1"] = truncateBodyValue(e.HTML, maxBytes, "text/html")
	}
	if len(values) > 0 {
		m.BodyValues = values
	}
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
// properties to the Email object itself. State calculation and body-value
// fetching happen before this so they are independent of the selection.
func (m *JMAPEmail) filtered(properties, bodyProperties *base.Selection) (json.RawMessage, error) {
	raw, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, err
	}
	for _, key := range []string{"bodyStructure", "textBody", "htmlBody", "attachments"} {
		value, ok := obj[key]
		if !ok {
			continue
		}
		filtered, err := bodyProperties.FilterRaw(value)
		if err != nil {
			return nil, err
		}
		obj[key] = filtered
	}
	selected, err := json.Marshal(obj)
	if err != nil {
		return nil, err
	}
	return properties.FilterRaw(selected)
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

func (h *Handler) toJMAPEmail(ctx context.Context, listed *mailemail.ListedEmail) (*JMAPEmail, error) {
	e := listed.Email
	id := listed.Ref.ID

	meta := base.EmailMetadata(ctx, h.deps, listed.Ref.AccountID, id)
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
	mailboxIDs := map[string]bool{mailboxID: true}

	from := []model.EmailAddress{{Name: e.From.Name, Email: e.From.Address}}
	to := make([]model.EmailAddress, len(e.To))
	for i, addr := range e.To {
		to[i] = model.EmailAddress{Name: addr.Name, Email: addr.Address}
	}
	cc := make([]model.EmailAddress, len(e.Cc))
	for i, addr := range e.Cc {
		cc[i] = model.EmailAddress{Name: addr.Name, Email: addr.Address}
	}
	bcc := make([]model.EmailAddress, len(e.Bcc))
	for i, addr := range e.Bcc {
		bcc[i] = model.EmailAddress{Name: addr.Name, Email: addr.Address}
	}
	replyTo := make([]model.EmailAddress, len(e.ReplyTo))
	for i, addr := range e.ReplyTo {
		replyTo[i] = model.EmailAddress{Name: addr.Name, Email: addr.Address}
	}

	preview := e.Text
	if len(preview) > 200 {
		preview = preview[:200]
	}
	preview = strings.ReplaceAll(preview, "\n", " ")
	preview = strings.TrimSpace(preview)

	textPartID := "text1"
	htmlPartID := "html1"
	textBody := []EmailBodyPart{}
	htmlBody := []EmailBodyPart{}
	if e.Text != "" {
		textBody = append(textBody, EmailBodyPart{PartID: &textPartID, Type: "text/plain", Size: len(e.Text)})
	}
	if e.HTML != "" {
		htmlBody = append(htmlBody, EmailBodyPart{PartID: &htmlPartID, Type: "text/html", Size: len(e.HTML)})
	}

	receivedAt := base.ReceivedAtFromMetadata(meta, e.Date).UTC().Format(time.RFC3339)
	sentAt := e.Date.UTC().Format(time.RFC3339)

	return &JMAPEmail{
		ID:            id,
		BlobID:        id,
		ThreadID:      id,
		MailboxIDs:    mailboxIDs,
		Keywords:      keywords,
		Size:          int(e.Size),
		ReceivedAt:    receivedAt,
		MessageID:     messageIDs(e.MessageID),
		InReplyTo:     e.InReplyTo,
		References:    e.References,
		Sender:        from,
		From:          from,
		To:            to,
		Cc:            cc,
		Bcc:           bcc,
		ReplyTo:       replyTo,
		Subject:       e.Subject,
		SentAt:        &sentAt,
		HasAttachment: false,
		Preview:       preview,
		TextBody:      textBody,
		HTMLBody:      htmlBody,
		Attachments:   []EmailBodyPart{},
		Headers:       []EmailHeader{},
	}, nil
}

func (h *Handler) matchesFilter(listed *mailemail.ListedEmail, filter *EmailFilter) bool {
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

	if filter.MinSize != nil && int(e.Size) < *filter.MinSize {
		return false
	}
	if filter.MaxSize != nil && int(e.Size) > *filter.MaxSize {
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
			// Ignore properties we do not manage (for example mailboxIds handled elsewhere)
			// rather than failing the whole update.
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
