package email

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/kncept/Emissary/mailer/auth"
	"github.com/kncept/Emissary/mailer/components/metadata"
	"github.com/kncept/Emissary/mailer/components/storage"
	mailemail "github.com/kncept/Emissary/mailer/email"
	"github.com/kncept/Emissary/mailer/jmap/internal/base"
)

// releaseBlobRefs drops the references held by a destroyed Email. A blob may
// back several messages, so only this Email's registrations are released; the
// bytes remain downloadable until the last reference is gone and the
// unreferenced-blob lifetime expires (RFC 8620 section 6).
func (h *Handler) releaseBlobRefs(ctx context.Context, accountID string, raw []byte) {
	for _, blobID := range storage.EmailBlobIDs(raw) {
		if _, err := h.deps.Storage.AddBlobRefs(ctx, accountID, blobID, -1); err != nil &&
			!errors.Is(err, storage.ErrNotFound) {
			slog.Warn("JMAP: failed to release email blob reference", "account", accountID, "blob", blobID, "err", err)
		}
	}
}

func (h *Handler) EmailGet(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error) {
	var req struct {
		FetchTextBodyValues bool `json:"fetchTextBodyValues"`
		FetchHTMLBodyValues bool `json:"fetchHTMLBodyValues"`
		FetchAllBodyValues  bool `json:"fetchAllBodyValues"`
		MaxBodyValueBytes   int  `json:"maxBodyValueBytes"`
	}
	allowed := base.ArgumentSet(
		"accountId", "ids", "properties", "bodyProperties", "fetchTextBodyValues",
		"fetchHTMLBodyValues", "fetchAllBodyValues", "maxBodyValueBytes")
	if err := base.RejectUnknownArguments(args, allowed); err != nil {
		return nil, err
	}
	if err := validateHeaderPropertyForms(args, "properties", "bodyProperties"); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(args, &req); err != nil {
		return nil, err
	}
	if req.MaxBodyValueBytes < 0 {
		return nil, base.Errorf("invalidArguments", "maxBodyValueBytes must not be negative")
	}
	// RFC 8621 section 4.1 replaces the RFC 8620 "all properties" default
	// with a fixed metadata/body list; section 4.2 does the same for
	// bodyProperties. The kernel applies the properties default; these two
	// selections are needed by Render for body values and nested body parts.
	bodyProperties, err := base.ParseSelectionWithDefault(args, "bodyProperties", EmailBodyPart{}, "header:", base.DefaultProperties("EmailBodyPart"))
	if err != nil {
		return nil, err
	}
	bodyProperties = bodyProperties.WithRecursion("subParts")
	fetchText := req.FetchTextBodyValues || req.FetchAllBodyValues
	fetchHTML := req.FetchHTMLBodyValues || req.FetchAllBodyValues

	// State is derived from the same records clients see, so a server-side
	// change or per-request property selection does not affect it.
	listed, err := h.deps.Storage.ListEmails(ctx, accountID, "")
	if err != nil {
		return nil, err
	}
	state, err := h.syncEmailStateFromListed(ctx, accountID, listed)
	if err != nil {
		return nil, err
	}
	_, threadIDs, err := base.GroupThreads(ctx, h.deps, accountID, listed)
	if err != nil {
		return nil, err
	}

	return base.HandleGet(ctx, accountID, args, base.GetSpec[*mailemail.ListedEmail]{
		Allowed:       allowed,
		Model:         JMAPEmail{},
		Defaults:      base.DefaultProperties("Email"),
		DynamicPrefix: "header:",
		Load:          func(context.Context) ([]*mailemail.ListedEmail, error) { return listed, nil },
		LoadIDs: func(ctx context.Context, ids []string) ([]*mailemail.ListedEmail, []string, error) {
			found := make([]*mailemail.ListedEmail, 0, len(ids))
			var notFound []string
			for _, id := range ids {
				email, err := h.deps.Storage.GetEmail(ctx, accountID, id)
				if err != nil || email == nil || email.Ref == nil || !strings.EqualFold(email.Ref.AccountID, accountID) {
					notFound = append(notFound, id)
					continue
				}
				found = append(found, email)
			}
			return found, notFound, nil
		},
		ID:    func(email *mailemail.ListedEmail) string { return email.Ref.ID },
		State: func(context.Context) (string, error) { return state, nil },
		Render: func(ctx context.Context, email *mailemail.ListedEmail, properties *base.Selection, _ json.RawMessage) (json.RawMessage, error) {
			// Hydration failure must never silently omit a requested email.
			jmapEmail, err := h.toJMAPEmailWithThreadID(ctx, email, threadIDs[email.Ref.ID])
			if err != nil {
				return nil, err
			}
			jmapEmail.setBodyValues(fetchText, fetchHTML, req.FetchAllBodyValues, req.MaxBodyValueBytes)
			return jmapEmail.filtered(properties, bodyProperties)
		},
	})
}

func (h *Handler) EmailSet(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error) {
	return base.HandleSet(ctx, accountID, args, h.setSpec(accountID))
}

// setSpec returns the type-specific callbacks for Email/set. A fresh spec is
// built per request so no per-request state leaks between invocations.
func (h *Handler) setSpec(accountID string) base.SetSpec {
	// Destroy cannot return a method error (only a SetError), so a rare change
	// tracker failure is captured here and surfaced by Finalize.
	var destroyErr error
	return base.SetSpec{
		Allowed: base.ArgumentSet("accountId", "ifInState", "create", "update", "destroy"),
		State: func(ctx context.Context) (string, error) {
			return h.deps.Changes.State(ctx, accountID, base.StateEmail)
		},
		Finalize: func(ctx context.Context) error {
			if destroyErr != nil {
				return destroyErr
			}
			_, err := h.syncEmailState(ctx, accountID)
			return err
		},
		Destroy: func(ctx context.Context, id string) *base.SetError {
			fail := func(setErr base.SetError) *base.SetError { return &setErr }
			// Verify the message belongs to the requested account so a
			// crafted id cannot delete another user's mail. Metadata is
			// removed first; if storage deletion then fails, the original
			// metadata is restored so neither store is half-updated.
			listed, err := h.deps.Storage.GetEmail(ctx, accountID, id)
			if err != nil || listed == nil || listed.Ref == nil || !strings.EqualFold(listed.Ref.AccountID, accountID) {
				return fail(base.NewSetError("notFound", "Email not found"))
			}
			mbID := listed.Ref.MailboxID
			if mbID == "" {
				mbID = "inbox"
			}
			mb, mbErr := h.deps.Storage.GetMailbox(ctx, accountID, mbID)
			if mbErr == nil {
				if rights := base.RightsForRole(roleOf(mb)); !rights.MayRemoveItems {
					return fail(base.NewSetError("forbidden", "May not remove items from this mailbox"))
				}
			}
			originalMeta := base.EmailMetadata(ctx, h.deps, accountID, id)
			// Capture the raw bytes before deleting the message. Blob ids are
			// digests of those bytes, so the same ids registered at creation
			// can be released once the last reference to this Email is gone.
			raw, rawErr := h.deps.Storage.GetEmailRaw(ctx, accountID, id)
			if err := h.deps.Metadata.Delete(ctx, accountID, id); err != nil {
				slog.Error("JMAP: failed to delete email metadata", "account", accountID, "id", id, "err", err)
				return fail(base.NewSetError("serverFail", "Failed to delete email metadata"))
			}
			if err := h.deps.Storage.DeleteEmail(ctx, accountID, id); err != nil {
				if restoreErr := h.deps.Metadata.Save(ctx, originalMeta); restoreErr != nil {
					slog.Error("JMAP: failed to restore email metadata", "account", accountID, "id", id, "err", restoreErr)
				}
				if errors.Is(err, storage.ErrNotFound) {
					return fail(base.NewSetError("notFound", "Email not found"))
				}
				slog.Error("JMAP: failed to delete email", "account", accountID, "id", id, "err", err)
				return fail(base.NewSetError("serverFail", "Failed to delete email"))
			}
			if rawErr == nil {
				h.releaseBlobRefs(ctx, accountID, raw)
			}
			if err := h.deps.Changes.Record(ctx, accountID, base.StateEmail, id, base.ChangeDestroy); err != nil {
				destroyErr = err
			}
			return nil
		},
		Create: func(ctx context.Context, createID string, raw json.RawMessage) (json.RawMessage, *base.SetError, error) {
			fail := func(setErr base.SetError) (json.RawMessage, *base.SetError, error) {
				return nil, &setErr, nil
			}
			var createMap map[string]interface{}
			if err := json.Unmarshal(raw, &createMap); err != nil {
				return fail(base.InvalidProperties("Invalid create object", "invalidProperties"))
			}
			if properties := validateEmailCreate(raw); len(properties) > 0 {
				return fail(base.InvalidProperties("Conflicting or unsupported create properties", properties...))
			}
			subject, _ := createMap["subject"].(string)
			replyTo, err := jmapAddressList(createMap, "replyTo")
			if err != nil {
				return nil, nil, base.Errorf("invalidArguments", "%s", err.Error())
			}
			to, err := jmapAddressList(createMap, "to")
			if err != nil {
				return nil, nil, base.Errorf("invalidArguments", "%s", err.Error())
			}
			cc, err := jmapAddressList(createMap, "cc")
			if err != nil {
				return nil, nil, base.Errorf("invalidArguments", "%s", err.Error())
			}
			bcc, err := jmapAddressList(createMap, "bcc")
			if err != nil {
				return nil, nil, base.Errorf("invalidArguments", "%s", err.Error())
			}

			// Locked From: submissions/creates send as the caller's own
			// account, or as the target account when a user manager
			// masquerades.
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
					return fail(base.NewSetError("invalidProperties", "Only one mailbox is supported per email"))
				}
				if len(targets) == 1 {
					targetMailbox = targets[0]
				}
			}
			mb, mbErr := h.deps.Storage.GetMailbox(ctx, accountID, targetMailbox)
			if mbErr != nil {
				return fail(base.NewSetError("invalidProperties", "Target mailbox not found"))
			}
			if rights := base.RightsForRole(roleOf(mb)); !rights.MayAddItems {
				return fail(base.NewSetError("forbidden", "May not add items to this mailbox"))
			}

			now := time.Now().UTC()
			inReplyToList := []string{}
			if strings.TrimSpace(inReplyTo) != "" {
				inReplyToList = []string{strings.TrimSpace(inReplyTo)}
			}
			// A client may supply a Message-ID (RFC 8621 section 4.6); the
			// server generates one only when it is absent.
			messageID := ""
			if ids, ok := createMap["messageId"].([]interface{}); ok && len(ids) > 0 {
				if supplied, ok := ids[0].(string); ok {
					messageID = strings.TrimSpace(supplied)
				}
			}
			if normalizeMessageID(messageID) == "" {
				messageID = mailemail.GenerateMessageID(messageIDDomain(lockedFrom))
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
				MessageID:  messageID,
				InReplyTo:  inReplyToList,
				References: references,
			}
			// Render before storage so the same bytes back blobId, size and
			// the MIME tree returned by a later Email/get.
			rawBytes := mailemail.RawFromUserEmail(newEmail)
			blobID := storage.BlobID(rawBytes)

			// This server forbids two emails with the same explicit Message-ID
			// in one account (RFC 8621 section 4.8). Generated IDs are unique,
			// so only client-supplied ids can trigger the rule.
			if existingID := h.duplicateEmailID(ctx, accountID, newEmail.MessageID, nil); existingID != "" {
				return fail(base.AlreadyExists("An email with this Message-ID already exists", existingID))
			}

			// A session draft is stored separately (durable across lambda
			// invocations) and is only valid in drafts mailbox context.
			if claims := auth.ClaimsFromContext(ctx); claims != nil && claims.SID != "" {
				newID, err := h.deps.Storage.PutDraft(ctx, claims.SID, createID, newEmail)
				if err != nil {
					return fail(storeSetError(err, "Failed to create email"))
				}
				if _, err := storage.RegisterEmailBlobs(ctx, h.deps.Storage, accountID, rawBytes); err != nil {
					if cleanupErr := h.deps.Storage.DeleteDraft(ctx, claims.SID, newID); cleanupErr != nil {
						slog.Error("JMAP: failed to roll back draft after blob registration error", "id", newID, "err", cleanupErr)
					}
					return fail(storeSetError(err, "Failed to register draft blob"))
				}
				threadID := base.ThreadID(newID, newEmail)
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
					if cleanupErr := h.deps.Storage.DeleteDraft(ctx, claims.SID, newID); cleanupErr != nil {
						slog.Error("JMAP: failed to roll back draft after metadata error", "id", newID, "err", cleanupErr)
					}
					return fail(storeSetError(err, "Failed to save draft metadata"))
				}
				if err := h.deps.Changes.Record(ctx, accountID, base.StateEmail, newID, base.ChangeCreate); err != nil {
					return nil, nil, err
				}
				result, err := json.Marshal(map[string]interface{}{
					"id":         newID,
					"threadId":   threadID,
					"blobId":     blobID,
					"receivedAt": meta.ReceivedAt,
					"mailboxIds": map[string]bool{"drafts": true},
				})
				return result, nil, err
			}

			newID, err := h.deps.Storage.AddEmail(ctx, accountID, targetMailbox, newEmail, rawBytes)
			if err != nil {
				return fail(storeSetError(err, "Failed to create email"))
			}
			threadID := base.ThreadID(newID, newEmail)
			// A created Email must expose its bytes through blobId. If blob
			// registration fails, roll the email back and report the storage
			// failure rather than leaving a message whose blob cannot resolve.
			if _, err := storage.RegisterEmailBlobs(ctx, h.deps.Storage, accountID, rawBytes); err != nil {
				if cleanupErr := h.deps.Storage.DeleteEmail(ctx, accountID, newID); cleanupErr != nil {
					slog.Error("JMAP: failed to roll back email after blob registration error", "id", newID, "err", cleanupErr)
				}
				return fail(storeSetError(err, "Failed to register created email blob"))
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
				if cleanupErr := h.deps.Storage.DeleteEmail(ctx, accountID, newID); cleanupErr != nil {
					slog.Error("JMAP: failed to roll back created email after metadata error", "id", newID, "err", cleanupErr)
				}
				return fail(storeSetError(err, "Failed to save created email metadata"))
			}
			if err := h.deps.Changes.Record(ctx, accountID, base.StateEmail, newID, base.ChangeCreate); err != nil {
				return nil, nil, err
			}
			result, err := json.Marshal(map[string]interface{}{
				"id":         newID,
				"threadId":   threadID,
				"blobId":     blobID,
				"receivedAt": meta.ReceivedAt,
				"mailboxIds": map[string]bool{targetMailbox: true},
			})
			return result, nil, err
		},
		Update: func(ctx context.Context, id string, raw json.RawMessage) (json.RawMessage, *base.SetError, error) {
			fail := func(setErr base.SetError) (json.RawMessage, *base.SetError, error) {
				return nil, &setErr, nil
			}
			var updateMap map[string]interface{}
			if err := json.Unmarshal(raw, &updateMap); err != nil {
				return fail(base.NewSetError("invalidProperties", "Invalid update patch"))
			}

			// Resolve current location.
			listed, err := h.deps.Storage.GetEmail(ctx, accountID, id)
			if err != nil || listed == nil || listed.Ref == nil || !strings.EqualFold(listed.Ref.AccountID, accountID) {
				// Draft updates are limited to metadata; storage location fixed.
				if claims := auth.ClaimsFromContext(ctx); claims != nil && claims.SID != "" {
					if _, derr := h.deps.Storage.GetDraft(ctx, claims.SID, id); derr == nil {
						meta := base.EmailMetadata(ctx, h.deps, accountID, id)
						patched, ok, errType := ApplyKeywordPatch(meta, updateMap)
						if !ok {
							return fail(base.NewSetError(errType, "Invalid keyword update"))
						}
						if err := h.deps.Metadata.Save(ctx, patched); err != nil {
							return fail(base.NewSetError("serverFail", "Failed to save email metadata"))
						}
						if err := h.deps.Changes.Record(ctx, accountID, base.StateEmail, id, base.ChangeUpdate); err != nil {
							return nil, nil, err
						}
						return json.RawMessage("{}"), nil, nil
					}
				}
				return fail(base.NewSetError("notFound", "Email not found"))
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
					return fail(base.NewSetError("invalidProperties", "Invalid mailboxIds"))
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
					return fail(base.NewSetError("invalidProperties", "Exactly one mailbox is required"))
				}
				targetMailbox = targets[0]
				mailboxChanged = !strings.EqualFold(targetMailbox, currentMailbox)
			}
			for prop, v := range updateMap {
				if !strings.HasPrefix(prop, "mailboxIds/") {
					continue
				}
				mbID := storage.NormalizeMailboxID(strings.TrimPrefix(prop, "mailboxIds/"))
				b, ok := v.(bool)
				if !ok {
					return fail(base.NewSetError("invalidProperties", "Invalid mailboxIds patch"))
				}
				if b {
					targetMailbox = mbID
					mailboxChanged = !strings.EqualFold(targetMailbox, currentMailbox)
				} else if strings.EqualFold(mbID, targetMailbox) {
					// removing current/target without adding another is invalid here
					return fail(base.NewSetError("invalidProperties", "Email must remain in a mailbox"))
				}
			}

			// Validate the move and the keyword patch before mutating anything.
			if mailboxChanged {
				srcMb, srcErr := h.deps.Storage.GetMailbox(ctx, accountID, currentMailbox)
				dstMb, dstErr := h.deps.Storage.GetMailbox(ctx, accountID, targetMailbox)
				if dstErr != nil {
					return fail(base.NewSetError("invalidProperties", "Target mailbox not found"))
				}
				if srcErr == nil {
					if rights := base.RightsForRole(roleOf(srcMb)); !rights.MayRemoveItems {
						return fail(base.NewSetError("forbidden", "May not remove items from source mailbox"))
					}
				}
				if rights := base.RightsForRole(roleOf(dstMb)); !rights.MayAddItems {
					return fail(base.NewSetError("forbidden", "May not add items to target mailbox"))
				}
			}

			patched, ok, errType := ApplyKeywordPatch(base.EmailMetadata(ctx, h.deps, accountID, id), updateMap)
			if !ok {
				return fail(base.NewSetError(errType, "Invalid keyword update"))
			}

			// Apply the validated move and metadata write. A failed metadata
			// write is rolled back by moving the message back.
			if mailboxChanged {
				if _, err := h.deps.Storage.MoveEmail(ctx, accountID, id, targetMailbox); err != nil {
					if errors.Is(err, storage.ErrNotFound) {
						return fail(base.NewSetError("notFound", "Email not found"))
					}
					slog.Error("JMAP: failed to move email", "account", accountID, "id", id, "from", currentMailbox, "to", targetMailbox, "err", err)
					return fail(base.NewSetError("serverFail", "Failed to move email"))
				}
			}
			if err := h.deps.Metadata.Save(ctx, patched); err != nil {
				if mailboxChanged {
					if _, rollbackErr := h.deps.Storage.MoveEmail(ctx, accountID, id, currentMailbox); rollbackErr != nil {
						slog.Error("JMAP: failed to roll back email move after metadata error", "id", id, "err", rollbackErr)
					}
				}
				if errors.Is(err, metadata.ErrVersionConflict) {
					return fail(base.NewSetError("stateMismatch", "Email metadata changed concurrently"))
				}
				slog.Error("JMAP: failed to save email metadata", "account", accountID, "id", id, "err", err)
				return fail(storeSetError(err, "Failed to save email metadata"))
			}
			if err := h.deps.Changes.Record(ctx, accountID, base.StateEmail, id, base.ChangeUpdate); err != nil {
				return nil, nil, err
			}
			return json.RawMessage("{}"), nil, nil
		},
	}
}

func roleOf(mb storage.Mailbox) string {
	if mb.Role == nil {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(*mb.Role))
}

func (h *Handler) EmailChanges(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error) {
	return base.HandleChanges(ctx, accountID, args, base.ChangesSpec{
		Allowed: base.ArgumentSet("accountId", "sinceState", "maxChanges"),
		Sync: func(ctx context.Context) error {
			_, err := h.syncEmailState(ctx, accountID)
			return err
		},
		Changes: func(ctx context.Context, sinceState string, maxChanges *int) (map[string]any, error) {
			return h.deps.Changes.ChangesFrom(ctx, accountID, base.StateEmail, sinceState, maxChanges)
		},
	})
}

// supportedEmailSortProperties mirrors the Session's emailQuerySortOptions.
var supportedEmailSortProperties = map[string]bool{"receivedAt": true}

func (h *Handler) EmailQuery(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error) {
	return base.HandleQuery(ctx, accountID, args, h.querySpec())
}

// queryEmailIDs returns the full ordered result set for a query, before
// pagination. Both Email/query and Email/queryChanges use it so a queryState
// token always describes the same result set.
func (h *Handler) queryEmailIDs(ctx context.Context, accountID string, filter *EmailFilter, ascending,
	collapseThreads bool) ([]string, error) {
	mailboxID := ""
	if filter != nil && filter.InMailbox != nil && !FilterNeedsThreadContext(filter) {
		mailboxID = *filter.InMailbox
	}
	emails, err := h.deps.Storage.ListEmails(ctx, accountID, mailboxID)
	if err != nil {
		return nil, err
	}
	contextEmails := emails
	if FilterNeedsThreadContext(filter) || collapseThreads {
		// Thread membership and collapseThreads are account-wide, so a mailbox
		// prefix would produce incomplete groups.
		contextEmails, err = h.deps.Storage.ListEmails(ctx, accountID, "")
		if err != nil {
			return nil, err
		}
	}
	filterContext, err := NewFilterContext(ctx, h.deps, accountID, contextEmails)
	if err != nil {
		return nil, err
	}

	filtered := make([]*mailemail.ListedEmail, 0, len(emails))
	for _, e := range emails {
		if filterContext.Matches(ctx, e, filter) {
			filtered = append(filtered, e)
		}
	}

	received := make(map[string]time.Time, len(filtered))
	for _, e := range filtered {
		received[e.Ref.ID] = base.ReceivedAtFromMetadata(
			base.EmailMetadata(ctx, h.deps, accountID, e.Ref.ID), e.Email.Date)
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

	if collapseThreads {
		seen := make(map[string]bool, len(filtered))
		collapsed := filtered[:0]
		for _, e := range filtered {
			threadID := filterContext.threadIDs[e.Ref.ID]
			if seen[threadID] {
				continue
			}
			seen[threadID] = true
			collapsed = append(collapsed, e)
		}
		filtered = collapsed
	}

	ids := make([]string, len(filtered))
	for i, e := range filtered {
		ids[i] = e.Ref.ID
	}
	return ids, nil
}

func (h *Handler) EmailQueryChanges(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error) {
	return base.HandleQueryChanges(ctx, accountID, args, h.querySpec())
}

// querySpec is shared by Email/query and Email/queryChanges so filter,
// ordering and query state are derived identically in both methods.
func (h *Handler) querySpec() base.QuerySpec {
	return base.QuerySpec{
		Allowed: base.ArgumentSet("accountId", "filter", "sort", "position", "anchor", "anchorOffset",
			"limit", "calculateTotal", "collapseThreads"),
		ChangesAllowed: base.ArgumentSet("accountId", "filter", "sinceQueryState", "sort", "maxChanges",
			"upToId", "calculateTotal", "collapseThreads"),
		SortProperties: supportedEmailSortProperties,
		ParseFilter:    func(args json.RawMessage) (any, error) { return parseEmailFilter(args) },
		Extra: func(args json.RawMessage) error {
			var req struct {
				CollapseThreads *bool `json:"collapseThreads"`
			}
			if err := json.Unmarshal(args, &req); err != nil {
				return base.Errorf("invalidArguments", "query arguments must be an object")
			}
			return nil
		},
		ImmutableQuery: func(filter any, sort []interface{}) bool {
			typed, _ := filter.(*EmailFilter)
			if len(sort) > 1 {
				return false
			}
			if len(sort) == 1 {
				comparator, _ := sort[0].(map[string]interface{})
				if comparator["property"] != "receivedAt" {
					return false
				}
			}
			return ImmutableEmailFilter(typed)
		},
		AllIDs: func(ctx context.Context, accountID string, args json.RawMessage, filter any, ascending bool) ([]string, error) {
			var req struct {
				CollapseThreads *bool `json:"collapseThreads"`
			}
			if err := json.Unmarshal(args, &req); err != nil {
				return nil, base.Errorf("invalidArguments", "query arguments must be an object")
			}
			typed, _ := filter.(*EmailFilter)
			return h.queryEmailIDs(ctx, accountID, typed, ascending, req.CollapseThreads != nil && *req.CollapseThreads)
		},
		Tracker: h.deps.Changes,
		Kind:    base.StateEmailQuery,
	}
}
