package mailbox

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/kncept/Emissary/mailer/components/metadata"
	"github.com/kncept/Emissary/mailer/components/storage"
	mailemail "github.com/kncept/Emissary/mailer/email"
	"github.com/kncept/Emissary/mailer/jmap/internal/base"
)

// checkMailboxNameAvailable enforces RFC 8621 section 2: two sibling
// Mailboxes MUST NOT have the same name. Names are compared case-insensitively
// because storage normalises mailbox ids from names.
func (h *Handler) checkMailboxNameAvailable(ctx context.Context, accountID, name, parentID string) *base.SetError {
	mailboxes, err := h.deps.Storage.ListMailboxes(ctx, accountID)
	if err != nil {
		return nil
	}
	for _, mb := range mailboxes {
		existingParent := ""
		if mb.ParentID != nil {
			existingParent = storage.NormalizeMailboxID(*mb.ParentID)
		}
		if strings.EqualFold(existingParent, parentID) && strings.EqualFold(strings.TrimSpace(mb.Name), strings.TrimSpace(name)) {
			setErr := base.AlreadyExists("A mailbox with this name already exists under this parent", mb.ID)
			return &setErr
		}
	}
	return nil
}

type mailboxListCacheKey struct{ accountID string }

// getMailboxes returns the hydrated mailbox set for one JMAP request. All
// method calls in the same batch share this snapshot, so Mailbox/query and
// Mailbox/get do not each reload every mailbox from storage.
func (h *Handler) getMailboxes(ctx context.Context, accountID string) []Mailbox {
	mailboxes, _ := base.Cached(ctx, mailboxListCacheKey{accountID: accountID}, func() ([]Mailbox, error) {
		return h.loadMailboxes(ctx, accountID), nil
	})
	return mailboxes
}

func (h *Handler) loadMailboxes(ctx context.Context, accountID string) []Mailbox {
	stored, err := h.deps.Storage.ListMailboxes(ctx, accountID)
	if err != nil {
		// Fall back to defaults so /get remains usable on transient errors.
		stored = storage.DefaultMailboxes()
	}

	// One account-wide listing supplies every mailbox count. Listing each
	// folder four times (total/unread emails, total/unread threads) made a
	// single Mailbox/query + Mailbox/get batch issue dozens of S3 listings.
	emails, err := h.deps.Storage.ListEmails(ctx, accountID, "")
	if err != nil {
		emails = nil
	}
	byMailbox := make(map[string][]*mailemail.ListedEmail)
	for _, e := range emails {
		if e == nil || e.Ref == nil {
			continue
		}
		mailboxID := storage.NormalizeMailboxID(e.Ref.MailboxID)
		if mailboxID == "" {
			continue
		}
		byMailbox[mailboxID] = append(byMailbox[mailboxID], e)
	}

	out := make([]Mailbox, 0, len(stored))
	for _, mb := range stored {
		listed := byMailbox[storage.NormalizeMailboxID(mb.ID)]
		out = append(out, h.toJMAPMailbox(ctx, accountID, mb, listed))
	}
	return out
}

func (h *Handler) toJMAPMailbox(ctx context.Context, accountID string, mb storage.Mailbox, emails []*mailemail.ListedEmail) Mailbox {
	role := ""
	if mb.Role != nil {
		role = strings.ToLower(strings.TrimSpace(*mb.Role))
	}
	return Mailbox{
		ID:            mb.ID,
		Name:          mb.Name,
		ParentID:      mb.ParentID,
		Role:          mb.Role,
		SortOrder:     mb.SortOrder,
		TotalEmails:   len(emails),
		UnreadEmails:  h.countUnreadEmails(ctx, accountID, emails),
		TotalThreads:  h.countThreads(ctx, accountID, emails),
		UnreadThreads: h.countUnreadThreads(ctx, accountID, emails),
		MyRights:      base.RightsForRole(role),
		IsSubscribed:  mb.IsSubscribed,
	}
}

// CountUnreadInMailbox counts messages without $seen, excluding drafts.
func (h *Handler) CountUnreadInMailbox(ctx context.Context, accountID, mailboxID string) int {
	emails, err := h.deps.Storage.ListEmails(ctx, accountID, mailboxID)
	if err != nil {
		return 0
	}
	return h.countUnreadEmails(ctx, accountID, emails)
}

func (h *Handler) countUnreadEmails(ctx context.Context, accountID string, emails []*mailemail.ListedEmail) int {
	count := 0
	for _, e := range emails {
		if e == nil || e.Ref == nil {
			continue
		}
		meta := base.EmailMetadata(ctx, h.deps, accountID, e.Ref.ID)
		// JMAP unread means no $seen. Drafts are excluded from unread counts.
		if !meta.HasKeyword(metadata.KeywordSeen) && !meta.HasKeyword(metadata.KeywordDraft) {
			count++
		}
	}
	return count
}

func (h *Handler) countThreads(ctx context.Context, accountID string, emails []*mailemail.ListedEmail) int {
	_, threadIDs, err := base.GroupThreads(ctx, h.deps, accountID, emails)
	if err != nil {
		return 0
	}
	return len(threadIDs)
}

// countUnreadThreads counts distinct threads with at least one unread,
// non-draft message, matching the Mailbox/unreadThreads definition.
func (h *Handler) countUnreadThreads(ctx context.Context, accountID string, emails []*mailemail.ListedEmail) int {
	unread := make([]*mailemail.ListedEmail, 0, len(emails))
	for _, e := range emails {
		if e == nil || e.Ref == nil || e.Email == nil {
			continue
		}
		meta := base.EmailMetadata(ctx, h.deps, accountID, e.Ref.ID)
		if meta.HasKeyword(metadata.KeywordSeen) || meta.HasKeyword(metadata.KeywordDraft) {
			continue
		}
		unread = append(unread, e)
	}
	_, threadIDs, err := base.GroupThreads(ctx, h.deps, accountID, unread)
	if err != nil {
		return 0
	}
	return len(threadIDs)
}

// SyncState refreshes the Mailbox state from the configured mailbox store.
func (h *Handler) SyncState(ctx context.Context, accountID string) (string, error) {
	return h.syncMailboxState(ctx, accountID, h.getMailboxes(ctx, accountID))
}

// syncMailboxState builds a fingerprint snapshot from the current mailboxes.
func (h *Handler) syncMailboxState(ctx context.Context, accountID string, mailboxes []Mailbox) (string, error) {
	snapshot := make(map[string]string, len(mailboxes))
	for _, item := range mailboxes {
		fingerprint, err := json.Marshal(item)
		if err != nil {
			continue
		}
		snapshot[item.ID] = string(fingerprint)
	}
	if err := h.deps.Changes.Sync(ctx, accountID, map[base.StateKind]map[string]string{
		base.StateMailbox: snapshot,
	}); err != nil {
		return "", err
	}
	return h.deps.Changes.State(ctx, accountID, base.StateMailbox)
}
