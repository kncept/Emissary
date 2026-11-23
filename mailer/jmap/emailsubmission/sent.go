package emailsubmission

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/kncept/Emissary/mailer/components/metadata"
	"github.com/kncept/Emissary/mailer/components/storage"
	mailemail "github.com/kncept/Emissary/mailer/email"
	"github.com/kncept/Emissary/mailer/jmap/internal/base"
)

// keywordSent is the keyword clients use to mark a submitted message. It is
// not one of RFC 8621's system keywords, but it is part of this server's
// established Email/set vocabulary for sent mail.
const keywordSent = "$sent"

// sentMailboxID resolves the account's sent mailbox by role rather than by
// assuming the default id, since role mailboxes may have custom ids.
func (h *Handler) sentMailboxID(ctx context.Context, accountID string) (string, error) {
	mailboxes, err := h.deps.Storage.ListMailboxes(ctx, accountID)
	if err != nil {
		return "", err
	}
	for _, mailbox := range mailboxes {
		if mailbox.Role != nil && strings.EqualFold(strings.TrimSpace(*mailbox.Role), "sent") {
			return mailbox.ID, nil
		}
	}
	// Fall back to the conventional id for stores that predate roles.
	if _, err := h.deps.Storage.GetMailbox(ctx, accountID, "sent"); err == nil {
		return "sent", nil
	}
	return "", fmt.Errorf("account %q has no sent mailbox", accountID)
}

// fileInSent stores a copy of a successfully relayed message in the account's
// Sent mailbox. A new stable id is used rather than moving the submitted Email
// so that client-driven cleanup (onSuccessUpdateEmail/onSuccessDestroyEmail)
// cannot remove the record of what was actually sent.
func (h *Handler) fileInSent(ctx context.Context, accountID string, message *mailemail.UserEmail) (string, error) {
	mailboxID, err := h.sentMailboxID(ctx, accountID)
	if err != nil {
		return "", err
	}

	sent := *message
	sent.Attachments = append([]mailemail.Attachment(nil), message.Attachments...)
	// The SMTP envelope is send-time metadata and is not part of the stored
	// message. Sent copies are read by definition.
	sent.SubmissionEnvelope = nil
	sent.Read = true
	raw := mailemail.RawFromUserEmail(&sent)

	id, err := h.deps.Storage.AddEmail(ctx, accountID, mailboxID, &sent, raw)
	if err != nil {
		return "", fmt.Errorf("store sent email: %w", err)
	}
	rollback := func() {
		if err := h.deps.Storage.DeleteEmail(ctx, accountID, id); err != nil {
			slog.Warn("JMAP: failed to roll back sent email", "account", accountID, "id", id, "err", err)
		}
	}
	if _, err := storage.RegisterEmailBlobs(ctx, h.deps.Storage, accountID, raw); err != nil {
		rollback()
		return "", fmt.Errorf("register sent email blobs: %w", err)
	}
	meta := metadata.NewEmailMetadata(accountID, id, metadata.KeywordSeen, keywordSent)
	if !sent.Date.IsZero() {
		meta.ReceivedAt = metadata.FormatReceivedAt(sent.Date)
	}
	if err := h.deps.Metadata.Save(ctx, meta); err != nil {
		rollback()
		return "", fmt.Errorf("save sent email metadata: %w", err)
	}
	if err := h.deps.Changes.Record(ctx, accountID, base.StateEmail, id, base.ChangeCreate); err != nil {
		return "", err
	}
	return id, nil
}
