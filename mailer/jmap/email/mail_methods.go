package email

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/kncept/Emissary/mailer/components/metadata"
	"github.com/kncept/Emissary/mailer/components/storage"
	mailemail "github.com/kncept/Emissary/mailer/email"
	"github.com/kncept/Emissary/mailer/jmap/internal/base"
)

// rawImport is the shared shape of the create arguments for Email/import and
// Email/copy. RFC 8621 restricts an email to one mailbox in this profile; the
// mailboxIds map is validated explicitly rather than silently truncated.
type rawImport struct {
	BlobID     string          `json:"blobId"`
	ID         string          `json:"id"`
	MailboxIDs map[string]bool `json:"mailboxIds"`
	Keywords   map[string]bool `json:"keywords"`
	ReceivedAt *string         `json:"receivedAt"`
}

// targetMailbox resolves the single true mailboxId. A missing or empty map
// defaults to inbox; more than one target is rejected in line with the
// advertised maxMailboxesPerEmail limit.
func targetMailbox(mailboxIDs map[string]bool) (string, error) {
	targets := make([]string, 0, len(mailboxIDs))
	for id, keep := range mailboxIDs {
		if keep {
			targets = append(targets, storage.NormalizeMailboxID(id))
		}
	}
	if len(targets) > 1 {
		return "", fmt.Errorf("only one mailbox is supported per email")
	}
	if len(targets) == 0 {
		return "inbox", nil
	}
	return targets[0], nil
}

type mailboxError struct {
	typ         string
	description string
}

// checkMailboxRights verifies the destination exists and accepts new items.
func (h *Handler) checkMailboxRights(ctx context.Context, accountID, mailboxID string) *mailboxError {
	mb, err := h.deps.Storage.GetMailbox(ctx, accountID, mailboxID)
	if err != nil {
		return &mailboxError{typ: "invalidProperties", description: "Target mailbox not found"}
	}
	if rights := base.RightsForRole(roleOf(mb)); !rights.MayAddItems {
		return &mailboxError{typ: "forbidden", description: "May not add items to this mailbox"}
	}
	return nil
}

// parseKeywords validates each requested keyword exactly as Email/set does.
func parseKeywords(wanted map[string]bool) (map[string]bool, error) {
	out := make(map[string]bool, len(wanted))
	for keyword, keep := range wanted {
		normalized, ok := metadata.NormalizeKeyword(keyword)
		if !ok {
			return nil, fmt.Errorf("invalid keyword %q", keyword)
		}
		out[normalized] = keep
	}
	return out, nil
}

// receivedAtFor resolves the server-set receivedAt, honouring a client value
// only when it is a valid RFC 3339 timestamp.
func receivedAtFor(value *string) (string, error) {
	if value == nil || strings.TrimSpace(*value) == "" {
		return time.Now().UTC().Format(time.RFC3339), nil
	}
	parsed, err := time.Parse(time.RFC3339, *value)
	if err != nil {
		return "", fmt.Errorf("invalid receivedAt")
	}
	return parsed.UTC().Format(time.RFC3339), nil
}

// storeSetError translates a storage failure into the RFC SetError that
// describes it. Blob quota and message size are distinguishable from generic
// storage failures, even where a backend reports size as an untyped error.
func storeSetError(err error, fallback string) base.SetError {
	if err == nil {
		return base.NewSetError("serverFail", fallback)
	}
	if errors.Is(err, storage.ErrBlobQuota) {
		return base.NewSetError("overQuota", "Blob storage quota exceeded")
	}
	message := strings.ToLower(err.Error())
	if strings.Contains(message, "exceeds maximum size") || strings.Contains(message, "too large") ||
		strings.Contains(message, "size limit") {
		return base.TooLarge("Message exceeds the server size limit", uint64(mailemail.MaxPartSize()))
	}
	return base.NewSetError("serverFail", fallback)
}

// duplicateEmailID implements the server's documented duplicate rule: one
// account may not hold two emails with the same explicit Message-ID. The
// Message-ID is compared case-insensitively with RFC 5322 angle brackets and
// folding whitespace removed. An empty Message-ID is never a duplicate key.
func (h *Handler) duplicateEmailID(ctx context.Context, accountID, messageID string, _ []byte) string {
	target := normalizeMessageID(messageID)
	if target == "" {
		return ""
	}
	listed, err := h.deps.Storage.ListEmails(ctx, accountID, "")
	if err != nil {
		return ""
	}
	for _, email := range listed {
		if email == nil || email.Ref == nil || email.Email == nil {
			continue
		}
		if normalizeMessageID(email.Email.MessageID) == target {
			return email.Ref.ID
		}
	}
	return ""
}

func normalizeMessageID(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.ReplaceAll(value, "\r", "")
	value = strings.ReplaceAll(value, "\n", "")
	value = strings.ReplaceAll(value, " ", "")
	value = strings.Trim(value, "<>")
	if at := strings.IndexByte(value, '@'); at <= 0 || at == len(value)-1 {
		return ""
	}
	return value
}

// storeRawEmail persists parsed bytes under a new id and returns the JMAP
// Email object describing the stored copy. Both import and copy use it so
// state tracking, blob registration and metadata are identical.
func (h *Handler) storeRawEmail(ctx context.Context, accountID, mailboxID string, raw []byte, parsed *mailemail.UserEmail, keywords map[string]bool, receivedAt string) (*JMAPEmail, error) {
	newID, err := h.deps.Storage.AddEmail(ctx, accountID, mailboxID, parsed, raw)
	if err != nil {
		return nil, err
	}
	if _, err := storage.RegisterEmailBlobs(ctx, h.deps.Storage, accountID, raw); err != nil {
		if cleanupErr := h.deps.Storage.DeleteEmail(ctx, accountID, newID); cleanupErr != nil {
			slog.Error("JMAP: failed to roll back email after blob registration error", "id", newID, "err", cleanupErr)
		}
		return nil, err
	}
	meta := metadata.NewEmailMetadata(accountID, newID)
	meta.ReceivedAt = receivedAt
	for keyword, keep := range keywords {
		if keep {
			meta.Keywords[keyword] = true
		}
	}
	if err := h.deps.Metadata.Save(ctx, meta); err != nil {
		if cleanupErr := h.deps.Storage.DeleteEmail(ctx, accountID, newID); cleanupErr != nil {
			slog.Error("JMAP: failed to roll back imported email after metadata error", "id", newID, "err", cleanupErr)
		}
		return nil, err
	}
	if err := h.deps.Changes.Record(ctx, accountID, base.StateEmail, newID, base.ChangeCreate); err != nil {
		return nil, err
	}
	listed := &mailemail.ListedEmail{
		Ref:   &mailemail.EmailRef{ID: newID, AccountID: accountID, MailboxID: mailboxID},
		Email: parsed,
	}
	return h.toJMAPEmailFromRaw(ctx, listed, raw)
}

// EmailParse parses blobs into Email objects without storing them. RFC 8621
// section 6.5 permits an empty result for unparsable data and reports missing
// blobs in notFound.
func (h *Handler) EmailParse(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error) {
	var req struct {
		BlobIDs             []string `json:"blobIds"`
		FetchTextBodyValues bool     `json:"fetchTextBodyValues"`
		FetchHTMLBodyValues bool     `json:"fetchHTMLBodyValues"`
		FetchAllBodyValues  bool     `json:"fetchAllBodyValues"`
		MaxBodyValueBytes   int      `json:"maxBodyValueBytes"`
	}
	if err := base.RejectUnknownArguments(args, base.ArgumentSet(
		"accountId", "blobIds", "properties", "bodyProperties", "fetchTextBodyValues",
		"fetchHTMLBodyValues", "fetchAllBodyValues", "maxBodyValueBytes")); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(args, &req); err != nil {
		return nil, err
	}
	var argNames map[string]json.RawMessage
	if err := json.Unmarshal(args, &argNames); err != nil {
		return nil, err
	}
	if raw, ok := argNames["blobIds"]; !ok || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, base.Errorf("invalidArguments", "blobIds is required")
	}
	if err := validateHeaderPropertyForms(args, "properties", "bodyProperties"); err != nil {
		return nil, err
	}
	if req.MaxBodyValueBytes < 0 {
		return nil, base.Errorf("invalidArguments", "maxBodyValueBytes must not be negative")
	}
	// RFC 8621 section 4.9 defines a distinct default property set for parsed
	// blobs (no id/mailboxIds/keywords/receivedAt), and reuses the Email/get
	// bodyProperties default.
	properties, err := base.ParseSelectionWithDefault(args, "properties", JMAPEmail{}, "header:", base.DefaultProperties("Email/parse"))
	if err != nil {
		return nil, err
	}
	bodyProperties, err := base.ParseSelectionWithDefault(args, "bodyProperties", EmailBodyPart{}, "header:", base.DefaultProperties("EmailBodyPart"))
	if err != nil {
		return nil, err
	}
	bodyProperties = bodyProperties.WithRecursion("subParts")
	fetchText := req.FetchTextBodyValues || req.FetchAllBodyValues
	fetchHTML := req.FetchHTMLBodyValues || req.FetchAllBodyValues

	// RFC 8621 section 4.9 uses null (not an empty object/array) for every
	// empty result map and list.
	var parsed map[string]json.RawMessage
	var notParsable, notFound []string
	for _, blobID := range req.BlobIDs {
		_, raw, err := h.deps.Storage.GetBlob(ctx, accountID, blobID)
		if errors.Is(err, storage.ErrNotFound) {
			notFound = append(notFound, blobID)
			continue
		}
		if err != nil {
			return nil, err
		}
		message, err := mailemail.Parse("", bytes.NewReader(raw))
		if err != nil {
			notParsable = append(notParsable, blobID)
			continue
		}
		listed := &mailemail.ListedEmail{
			Ref:   &mailemail.EmailRef{ID: blobID, AccountID: accountID},
			Email: message,
		}
		jmapEmail, err := h.toJMAPEmailFromRaw(ctx, listed, raw)
		if err != nil {
			notParsable = append(notParsable, blobID)
			continue
		}
		jmapEmail.setBodyValues(fetchText, fetchHTML, req.FetchAllBodyValues, req.MaxBodyValueBytes)
		item, err := jmapEmail.filtered(properties, bodyProperties)
		if err != nil {
			return nil, err
		}
		// id, mailboxIds, keywords and receivedAt describe stored emails and
		// MUST be null when requested from Email/parse (RFC 8621 section 4.9).
		// blobId is derived from the parsed bytes and remains available. The
		// shared property filter force-includes id, so replace or remove these
		// properties after selection.
		var selected map[string]json.RawMessage
		if err := json.Unmarshal(item, &selected); err != nil {
			return nil, err
		}
		for _, property := range []string{"id", "mailboxIds", "keywords", "receivedAt"} {
			if properties.Has(property) {
				selected[property] = json.RawMessage("null")
			} else {
				delete(selected, property)
			}
		}
		item, err = json.Marshal(selected)
		if err != nil {
			return nil, err
		}
		if parsed == nil {
			parsed = make(map[string]json.RawMessage)
		}
		parsed[blobID] = item
	}

	return json.Marshal(map[string]any{
		"accountId":   accountID,
		"parsed":      parsed,
		"notParsable": notParsable,
		"notFound":    notFound,
	})
}

// EmailImport stores raw RFC 5322 blobs as new emails in the requested
// mailboxes, preserving the original bytes so later Email/get responses use
// the same blobId and MIME tree.
func (h *Handler) EmailImport(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error) {
	var req struct {
		IfInState *string              `json:"ifInState,omitempty"`
		Emails    map[string]rawImport `json:"emails"`
	}
	if err := base.RejectUnknownArguments(args, base.ArgumentSet(
		"accountId", "ifInState", "emails")); err != nil {
		return nil, err
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

	created := make(map[string]*JMAPEmail)
	notCreated := make(map[string]base.SetError)
	for createID, imp := range req.Emails {
		mailboxID, err := targetMailbox(imp.MailboxIDs)
		if err != nil {
			notCreated[createID] = base.NewSetError("invalidProperties", err.Error())
			continue
		}
		if mailboxErr := h.checkMailboxRights(ctx, accountID, mailboxID); mailboxErr != nil {
			notCreated[createID] = base.NewSetError(mailboxErr.typ, mailboxErr.description)
			continue
		}
		keywords, err := parseKeywords(imp.Keywords)
		if err != nil {
			notCreated[createID] = base.NewSetError("invalidProperties", err.Error())
			continue
		}
		receivedAt, err := receivedAtFor(imp.ReceivedAt)
		if err != nil {
			notCreated[createID] = base.NewSetError("invalidArguments", err.Error())
			continue
		}
		_, raw, err := h.deps.Storage.GetBlob(ctx, accountID, imp.BlobID)
		if err != nil {
			if errors.Is(err, storage.ErrNotFound) {
				notCreated[createID] = base.NewSetError("notFound", "Blob not found")
			} else {
				notCreated[createID] = storeSetError(err, "Failed to read blob")
			}
			continue
		}
		message, err := mailemail.Parse("", bytes.NewReader(raw))
		if err != nil {
			notCreated[createID] = base.NewSetError("invalidProperties", "Blob is not a valid email")
			continue
		}
		if existingID := h.duplicateEmailID(ctx, accountID, message.MessageID, raw); existingID != "" {
			notCreated[createID] = base.AlreadyExists("An email with this Message-ID already exists", existingID)
			continue
		}
		jmapEmail, err := h.storeRawEmail(ctx, accountID, mailboxID, raw, message, keywords, receivedAt)
		if err != nil {
			notCreated[createID] = storeSetError(err, "Failed to import email")
			continue
		}
		created[createID] = jmapEmail
	}
	newState, err := h.deps.Changes.State(ctx, accountID, base.StateEmail)
	if err != nil {
		return nil, err
	}
	return base.CopyImportResult{
		AccountID:  accountID,
		OldState:   oldState,
		NewState:   newState,
		Created:    created,
		NotCreated: notCreated,
	}.Marshal()
}

// EmailCopy copies emails between mailboxes of the authenticated account. The
// profile has no sharing model, so fromAccountId must match accountID; other
// values are reported as forbidden instead of allowing arbitrary cross-account
// reads. When onSuccessDestroyOriginal is set the source email is removed
// after the copy is durable.
func (h *Handler) EmailCopy(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error) {
	var req struct {
		FromAccountID            string               `json:"fromAccountId"`
		IfFromInState            *string              `json:"ifFromInState,omitempty"`
		AccountID                string               `json:"accountId"`
		IfInState                *string              `json:"ifInState,omitempty"`
		Create                   map[string]rawImport `json:"create"`
		OnSuccessDestroyOriginal bool                 `json:"onSuccessDestroyOriginal"`
		DestroyFromIfInState     *string              `json:"destroyFromIfInState,omitempty"`
	}
	if err := base.RejectUnknownArguments(args, base.ArgumentSet(
		"fromAccountId", "ifFromInState", "accountId", "ifInState", "create",
		"onSuccessDestroyOriginal", "destroyFromIfInState")); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(args, &req); err != nil {
		return nil, err
	}
	sourceAccount := req.FromAccountID
	if sourceAccount == "" {
		sourceAccount = accountID
	}

	// RFC 8621 section 5.5: ifFromInState guards the source account before
	// any copies, while destroyFromIfInState guards the originals before they
	// are removed. Neither argument may be silently ignored.
	destroyStateMismatch := false
	if req.IfFromInState != nil || req.DestroyFromIfInState != nil {
		fromState, err := h.deps.Changes.State(ctx, sourceAccount, base.StateEmail)
		if err != nil {
			return nil, err
		}
		if req.IfFromInState != nil && *req.IfFromInState != fromState {
			return nil, base.Errorf("stateMismatch", "source email state mismatch")
		}
		if req.DestroyFromIfInState != nil && *req.DestroyFromIfInState != fromState {
			destroyStateMismatch = true
		}
	}

	oldState, err := h.deps.Changes.State(ctx, accountID, base.StateEmail)
	if err != nil {
		return nil, err
	}
	if req.IfInState != nil && *req.IfInState != oldState {
		return nil, base.Errorf("stateMismatch", "email state mismatch")
	}

	created := make(map[string]*JMAPEmail)
	notCreated := make(map[string]base.SetError)
	notDestroyed := make(map[string]base.SetError)
	forbiddenSource := !strings.EqualFold(sourceAccount, accountID)

	for createID, copyReq := range req.Create {
		if forbiddenSource {
			notCreated[createID] = base.NewSetError("forbidden", "copying from another account is not supported")
			continue
		}
		listed, err := h.deps.Storage.GetEmail(ctx, sourceAccount, copyReq.ID)
		if err != nil || listed == nil || listed.Ref == nil || !strings.EqualFold(listed.Ref.AccountID, sourceAccount) {
			notCreated[createID] = base.NewSetError("notFound", "Original email not found")
			continue
		}
		mailboxID, err := targetMailbox(copyReq.MailboxIDs)
		if err != nil {
			notCreated[createID] = base.NewSetError("invalidProperties", err.Error())
			continue
		}
		if mailboxErr := h.checkMailboxRights(ctx, accountID, mailboxID); mailboxErr != nil {
			notCreated[createID] = base.NewSetError(mailboxErr.typ, mailboxErr.description)
			continue
		}
		keywords, err := parseKeywords(copyReq.Keywords)
		if err != nil {
			notCreated[createID] = base.NewSetError("invalidProperties", err.Error())
			continue
		}
		receivedAt, err := receivedAtFor(copyReq.ReceivedAt)
		if err != nil {
			notCreated[createID] = base.NewSetError("invalidArguments", err.Error())
			continue
		}
		raw, err := h.deps.Storage.GetEmailRaw(ctx, sourceAccount, copyReq.ID)
		if err != nil {
			notCreated[createID] = storeSetError(err, "Failed to read original email")
			continue
		}
		message := listed.Email
		if message == nil {
			message, err = mailemail.Parse("", bytes.NewReader(raw))
			if err != nil {
				notCreated[createID] = base.NewSetError("invalidProperties", "Original email is not parsable")
				continue
			}
		}
		if existingID := h.duplicateEmailID(ctx, accountID, message.MessageID, raw); existingID != "" {
			notCreated[createID] = base.AlreadyExists("An email with this Message-ID already exists", existingID)
			continue
		}
		jmapEmail, err := h.storeRawEmail(ctx, accountID, mailboxID, raw, message, keywords, receivedAt)
		if err != nil {
			notCreated[createID] = storeSetError(err, "Failed to copy email")
			continue
		}
		created[createID] = jmapEmail

		if req.OnSuccessDestroyOriginal {
			if destroyStateMismatch {
				notDestroyed[copyReq.ID] = base.NewSetError("stateMismatch", "source email state mismatch")
				continue
			}
			if err := h.deps.Metadata.Delete(ctx, sourceAccount, copyReq.ID); err != nil {
				notDestroyed[copyReq.ID] = base.NewSetError("serverFail", "Failed to delete original metadata")
				continue
			}
			if err := h.deps.Storage.DeleteEmail(ctx, sourceAccount, copyReq.ID); err != nil {
				notDestroyed[copyReq.ID] = base.NewSetError("serverFail", "Failed to delete original email")
				continue
			}
			if err := h.deps.Changes.Record(ctx, sourceAccount, base.StateEmail, copyReq.ID, base.ChangeDestroy); err != nil {
				return nil, err
			}
		}
	}

	newState, err := h.deps.Changes.State(ctx, accountID, base.StateEmail)
	if err != nil {
		return nil, err
	}
	result := base.CopyImportResult{
		AccountID:     accountID,
		FromAccountID: sourceAccount,
		OldState:      oldState,
		NewState:      newState,
		Created:       created,
		NotCreated:    notCreated,
	}
	if req.OnSuccessDestroyOriginal {
		result.NotDestroyed = notDestroyed
	}
	return result.Marshal()
}
