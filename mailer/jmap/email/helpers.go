package email

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	mailemail "github.com/kncept/Emissary/mailer/email"
	"github.com/kncept/Emissary/mailer/jmap/internal/base"
	"github.com/kncept/Emissary/mailer/jmap/internal/model"
	"github.com/kncept/Emissary/mailer/metadata"
)

func (m *JMAPEmail) setBodyValues(e *mailemail.UserEmail, fetchText, fetchHTML bool) {
	values := map[string]EmailBodyValue{}
	if fetchText && e.Text != "" {
		values["text1"] = EmailBodyValue{Value: e.Text}
	}
	if fetchHTML && e.HTML != "" {
		values["html1"] = EmailBodyValue{Value: e.HTML}
	}
	if len(values) > 0 {
		m.BodyValues = values
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
			return nil, fmt.Errorf("invalidArguments: %s entries must be objects", property)
		}
		address, ok := object["email"].(string)
		if !ok {
			return nil, fmt.Errorf("invalidArguments: %s entry has a non-string email", property)
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

// syncEmailState refreshes the Email snapshots from storage.
func (h *Handler) syncEmailState(ctx context.Context, accountID string) string {
	listed, err := h.deps.Storage.ListEmails(ctx, accountID, "")
	if err != nil {
		// Keep the last known state; a storage failure must not invent a
		// change or erase the delta history.
		return h.deps.Changes.State(accountID, base.StateEmail)
	}
	return h.syncEmailStateFromListed(ctx, accountID, listed)
}

// syncEmailStateFromListed avoids a second storage listing when the caller has
// already loaded the account's full mailbox (Email/get does this).
func (h *Handler) syncEmailStateFromListed(ctx context.Context, accountID string, listed []*mailemail.ListedEmail) string {
	snapshot := make(map[string]string, len(listed))
	for _, item := range listed {
		if item == nil || item.Ref == nil || item.Email == nil {
			continue
		}
		snapshot[item.Ref.ID] = emailFingerprint(item, base.EmailMetadata(ctx, h.deps, accountID, item.Ref.ID))
	}
	h.deps.Changes.Sync(accountID, map[base.StateKind]map[string]string{
		base.StateEmail:      snapshot,
		base.StateEmailQuery: snapshot,
	})
	return h.deps.Changes.State(accountID, base.StateEmail)
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

	// Stitch JMAP keywords from metadata into the returned object. A missing
	// metadata record is normal (older messages, receiver without metadata)
	// and simply yields an empty keyword set; it must not fail the request.
	meta := base.EmailMetadata(ctx, h.deps, listed.Ref.AccountID, id)
	keywords := make(map[string]bool, len(meta.Keywords))
	for keyword, enabled := range meta.Keywords {
		if enabled {
			keywords[keyword] = true
		}
	}

	// Determine mailbox IDs from the mailbox the message was delivered to.
	// Locally created messages are stored in the inbox by AddEmail.
	mailboxID := listed.Ref.MailboxID
	if mailboxID == "" {
		mailboxID = "inbox"
	}
	mailboxIDs := map[string]bool{
		mailboxID: true,
	}

	// Convert addresses
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

	// Build preview from text body
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

	// Format dates. receivedAt is server-managed metadata, not the Date
	// header (which is the message's sent time). Legacy records without a
	// receivedAt value fall back to the Date header.
	receivedAt := base.ReceivedAtFromMetadata(meta, e.Date).UTC().Format(time.RFC3339)
	sentAt := e.Date.UTC().Format(time.RFC3339)

	return &JMAPEmail{
		ID:            id,
		BlobID:        id, // Use ID as blobId for now
		ThreadID:      id, // Each email is its own thread for now
		MailboxIDs:    mailboxIDs,
		Keywords:      keywords,
		Size:          int(e.Size),
		ReceivedAt:    receivedAt,
		MessageID:     messageIDs(e.MessageID),
		InReplyTo:     e.InReplyTo,
		References:    e.References,
		Sender:        from, // Use From as Sender for simplicity
		From:          from,
		To:            to,
		Cc:            cc,
		Bcc:           bcc,
		ReplyTo:       replyTo,
		Subject:       e.Subject,
		SentAt:        &sentAt,
		HasAttachment: false, // Would need to parse from wire format
		Preview:       preview,
		BodyStructure: nil,
		BodyValues:    nil,
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

	// InMailbox filter. Storage resolves this into a mailbox prefix; when the
	// mailbox is known on the message, verify it as well so a stale or
	// hand-crafted id cannot be used to read from another mailbox.
	if filter.InMailbox != nil && mailboxID != "" && !strings.EqualFold(mailboxID, *filter.InMailbox) {
		return false
	}

	// InMailboxOtherThan filter
	if mailboxID != "" {
		for _, mb := range filter.InMailboxOtherThan {
			if strings.EqualFold(mb, mailboxID) {
				return false
			}
		}
	}

	// Date filters
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

	// Size filters
	if filter.MinSize != nil && int(e.Size) < *filter.MinSize {
		return false
	}
	if filter.MaxSize != nil && int(e.Size) > *filter.MaxSize {
		return false
	}

	// Text search (simple substring match on subject, from, to, text)
	if filter.Text != nil {
		text := strings.ToLower(*filter.Text)
		found := false
		if strings.Contains(strings.ToLower(e.Subject), text) {
			found = true
		}
		// Check From (single address)
		if strings.Contains(strings.ToLower(e.From.Address), text) || strings.Contains(strings.ToLower(e.From.Name), text) {
			found = true
		}
		for _, addr := range e.To {
			if strings.Contains(strings.ToLower(addr.Address), text) || strings.Contains(strings.ToLower(addr.Name), text) {
				found = true
				break
			}
		}
		if strings.Contains(strings.ToLower(e.Text), text) {
			found = true
		}
		if !found {
			return false
		}
	}

	// From filter
	if filter.From != nil {
		text := strings.ToLower(*filter.From)
		found := false
		if strings.Contains(strings.ToLower(e.From.Address), text) || strings.Contains(strings.ToLower(e.From.Name), text) {
			found = true
		}
		if !found {
			return false
		}
	}

	// To filter
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

	// Subject filter
	if filter.Subject != nil {
		if !strings.Contains(strings.ToLower(e.Subject), strings.ToLower(*filter.Subject)) {
			return false
		}
	}

	// Body filter
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
	listed, err := h.deps.Storage.GetEmail(ctx, id)
	if err == nil && listed != nil && listed.Ref != nil && strings.EqualFold(listed.Ref.AccountID, accountID) {
		return true
	}
	if claims := base.ClaimsFromContext(ctx); claims != nil && claims.SID != "" {
		draft, err := h.deps.Storage.GetDraft(ctx, claims.SID, id)
		return err == nil && draft != nil
	}
	return false
}

// ApplyKeywordPatch applies an Email/set update patch to a metadata record.
// Per RFC 8621, "keywords/<name>": true sets a keyword and null removes it;
// false is not a valid patch value. A full "keywords" object replaces the set.
// receivedAt is server-owned: it is preserved by Clone and never read from the
// patch, so clients cannot assign, edit or clear it. The returned string is a
// JMAP error type when ok is false.
func ApplyKeywordPatch(current metadata.EmailMetadata, patch map[string]interface{}) (metadata.EmailMetadata, bool, string) {
	updated := current.Clone()

	// Apply a whole-object replacement first, then any individual keyword
	// patches, so the result does not depend on Go map iteration order.
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
			// Ignore properties we do not manage (for example mailboxIds)
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
