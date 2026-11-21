package mailbox

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/kncept/Emissary/mailer/components/metadata"
	"github.com/kncept/Emissary/mailer/components/storage"
	"github.com/kncept/Emissary/mailer/jmap/internal/base"
)

func (h *Handler) getMailboxes(ctx context.Context, accountID string) []Mailbox {
	stored, err := h.deps.Storage.ListMailboxes(ctx, accountID)
	if err != nil {
		// Fall back to defaults so /get remains usable on transient errors.
		stored = storage.DefaultMailboxes()
	}
	out := make([]Mailbox, 0, len(stored))
	for _, mb := range stored {
		out = append(out, h.toJMAPMailbox(ctx, accountID, mb))
	}
	return out
}

func (h *Handler) toJMAPMailbox(ctx context.Context, accountID string, mb storage.Mailbox) Mailbox {
	count := h.countEmailsInMailbox(ctx, accountID, mb.ID)
	unread := h.CountUnreadInMailbox(ctx, accountID, mb.ID)
	threads := h.countThreadsInMailbox(ctx, accountID, mb.ID)
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
		TotalEmails:   count,
		UnreadEmails:  unread,
		TotalThreads:  threads,
		UnreadThreads: 0, // not tracked separately
		MyRights:      rightsForRole(role),
		IsSubscribed:  mb.IsSubscribed,
	}
}

func rightsForRole(role string) MailboxRights {
	// Defaults: readable, can hold messages and be used for organization.
	r := MailboxRights{
		MayReadItems:   true,
		MayAddItems:    true,
		MayRemoveItems: true,
		MaySetSeen:     true,
		MaySetKeywords: true,
		MayCreateChild: false,
		MayRename:      true,
		MayDelete:      true,
		MaySubmit:      true,
	}
	switch role {
	case "inbox":
		r.MayRename = false
		r.MayDelete = false
		r.MaySubmit = true
	case "drafts":
		r.MayRename = false
		r.MayDelete = false
		r.MaySubmit = true
	case "sent":
		r.MayRename = false
		r.MayDelete = false
		r.MayAddItems = true
		r.MayRemoveItems = true
		r.MaySubmit = false
	case "trash":
		r.MayRename = false
		r.MayDelete = false
		r.MaySubmit = false
	case "archive":
		r.MayRename = false
		r.MayDelete = false
		r.MaySubmit = true
	case "junk":
		r.MayRename = false
		r.MayDelete = false
		r.MaySubmit = false
	}
	return r
}

func (h *Handler) countEmailsInMailbox(ctx context.Context, accountID, mailboxID string) int {
	emails, err := h.deps.Storage.ListEmails(ctx, accountID, mailboxID)
	if err != nil {
		return 0
	}
	return len(emails)
}

// CountUnreadInMailbox counts messages without $seen, excluding drafts.
func (h *Handler) CountUnreadInMailbox(ctx context.Context, accountID, mailboxID string) int {
	emails, err := h.deps.Storage.ListEmails(ctx, accountID, mailboxID)
	if err != nil {
		return 0
	}
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

func (h *Handler) countThreadsInMailbox(ctx context.Context, accountID, mailboxID string) int {
	// For now, each email is its own thread
	return h.countEmailsInMailbox(ctx, accountID, mailboxID)
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
