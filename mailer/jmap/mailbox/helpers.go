package mailbox

import (
	"context"
	"encoding/json"

	"github.com/kncept/Emissary/mailer/jmap/internal/base"
	"github.com/kncept/Emissary/mailer/metadata"
)

// getMailboxes returns the standard JMAP mailboxes. Counts are scoped to the
// requested account so a masquerading user sees the target user's mailbox
// state.
func (h *Handler) getMailboxes(ctx context.Context, accountID string) []Mailbox {
	count := func(mailboxID string) int { return h.countEmailsInMailbox(ctx, accountID, mailboxID) }
	unread := func(mailboxID string) int { return h.CountUnreadInMailbox(ctx, accountID, mailboxID) }
	threads := func(mailboxID string) int { return h.countThreadsInMailbox(ctx, accountID, mailboxID) }

	return []Mailbox{
		{
			ID:            "inbox",
			Name:          "Inbox",
			ParentID:      nil,
			Role:          stringPtr("inbox"),
			SortOrder:     0,
			TotalEmails:   count("inbox"),
			UnreadEmails:  unread("inbox"),
			TotalThreads:  threads("inbox"),
			UnreadThreads: 0,
			MyRights: MailboxRights{
				MayReadItems:   true,
				MayAddItems:    true,
				MayRemoveItems: true,
				MaySetSeen:     true,
				MaySetKeywords: true,
				MayCreateChild: true,
				MayRename:      false,
				MayDelete:      false,
				MaySubmit:      true,
			},
			IsSubscribed: true,
		},
		{
			ID:            "sent",
			Name:          "Sent",
			ParentID:      nil,
			Role:          stringPtr("sent"),
			SortOrder:     1,
			TotalEmails:   count("sent"),
			UnreadEmails:  0,
			TotalThreads:  threads("sent"),
			UnreadThreads: 0,
			MyRights: MailboxRights{
				MayReadItems:   true,
				MayAddItems:    true,
				MayRemoveItems: true,
				MaySetSeen:     true,
				MaySetKeywords: true,
				MayCreateChild: true,
				MayRename:      false,
				MayDelete:      false,
				MaySubmit:      true,
			},
			IsSubscribed: true,
		},
		{
			ID:            "drafts",
			Name:          "Drafts",
			ParentID:      nil,
			Role:          stringPtr("drafts"),
			SortOrder:     2,
			TotalEmails:   count("drafts"),
			UnreadEmails:  0,
			TotalThreads:  threads("drafts"),
			UnreadThreads: 0,
			MyRights: MailboxRights{
				MayReadItems:   true,
				MayAddItems:    true,
				MayRemoveItems: true,
				MaySetSeen:     true,
				MaySetKeywords: true,
				MayCreateChild: true,
				MayRename:      false,
				MayDelete:      false,
				MaySubmit:      true,
			},
			IsSubscribed: true,
		},
		{
			ID:            "trash",
			Name:          "Trash",
			ParentID:      nil,
			Role:          stringPtr("trash"),
			SortOrder:     3,
			TotalEmails:   count("trash"),
			UnreadEmails:  0,
			TotalThreads:  threads("trash"),
			UnreadThreads: 0,
			MyRights: MailboxRights{
				MayReadItems:   true,
				MayAddItems:    true,
				MayRemoveItems: true,
				MaySetSeen:     true,
				MaySetKeywords: true,
				MayCreateChild: true,
				MayRename:      false,
				MayDelete:      true,
				MaySubmit:      false,
			},
			IsSubscribed: true,
		},
		{
			ID:            "archive",
			Name:          "Archive",
			ParentID:      nil,
			Role:          stringPtr("archive"),
			SortOrder:     4,
			TotalEmails:   count("archive"),
			UnreadEmails:  unread("archive"),
			TotalThreads:  threads("archive"),
			UnreadThreads: 0,
			MyRights: MailboxRights{
				MayReadItems:   true,
				MayAddItems:    true,
				MayRemoveItems: true,
				MaySetSeen:     true,
				MaySetKeywords: true,
				MayCreateChild: true,
				MayRename:      false,
				MayDelete:      false,
				MaySubmit:      true,
			},
			IsSubscribed: true,
		},
		{
			ID:            "junk",
			Name:          "Junk",
			ParentID:      nil,
			Role:          stringPtr("junk"),
			SortOrder:     5,
			TotalEmails:   count("junk"),
			UnreadEmails:  unread("junk"),
			TotalThreads:  threads("junk"),
			UnreadThreads: 0,
			MyRights: MailboxRights{
				MayReadItems:   true,
				MayAddItems:    true,
				MayRemoveItems: true,
				MaySetSeen:     true,
				MaySetKeywords: true,
				MayCreateChild: true,
				MayRename:      false,
				MayDelete:      false,
				MaySubmit:      false,
			},
			IsSubscribed: true,
		},
	}
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

func stringPtr(s string) *string { return &s }
