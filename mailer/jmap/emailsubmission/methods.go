package emailsubmission

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/mail"
	"sort"
	"strings"
	"time"

	"github.com/kncept/Emissary/mailer/auth"
	"github.com/kncept/Emissary/mailer/components/storage"
	mailemail "github.com/kncept/Emissary/mailer/email"
	"github.com/kncept/Emissary/mailer/identity"
	"github.com/kncept/Emissary/mailer/jmap/internal/base"
)

func (h *Handler) EmailSubmissionGet(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error) {
	var req struct {
		IDs []string `json:"ids,omitempty"`
	}
	if err := json.Unmarshal(args, &req); err != nil {
		return nil, err
	}
	properties, err := base.ParseSelection(args, "properties", jmapSubmission{}, "")
	if err != nil {
		return nil, err
	}
	records, _, err := h.deps.Submissions.Load(ctx, accountID)
	if err != nil {
		return nil, err
	}

	list := make([]json.RawMessage, 0, len(req.IDs))
	notFound := make([]string, 0)
	if len(req.IDs) > 0 {
		for _, id := range req.IDs {
			record, ok := records[id]
			if !ok || !strings.EqualFold(record.AccountID, accountID) {
				notFound = append(notFound, id)
				continue
			}
			item, err := properties.Filter(toJMAPSubmission(record))
			if err != nil {
				return nil, err
			}
			list = append(list, item)
		}
	} else {
		for _, record := range records {
			item, err := properties.Filter(toJMAPSubmission(record))
			if err != nil {
				return nil, err
			}
			list = append(list, item)
		}
	}

	state, err := h.deps.Changes.State(ctx, accountID, base.StateSubmission)
	if err != nil {
		return nil, err
	}
	return json.Marshal(map[string]interface{}{
		"accountId": accountID,
		"state":     state,
		"list":      list,
		"notFound":  notFound,
	})
}

// EmailSubmissionSet implements the single-response form of the method. The
// dispatcher uses EmailSubmissionSetWithImplicit so clients also receive the
// separate Email/set response required by RFC 8621 section 7.5.
func (h *Handler) EmailSubmissionSet(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error) {
	response, _, err := h.EmailSubmissionSetWithImplicit(ctx, accountID, args)
	return response, err
}

// EmailSubmissionSetWithImplicit returns both the EmailSubmission/set response
// and, when onSuccessUpdateEmail/onSuccessDestroyEmail were supplied, the
// implicit Email/set response. Both share the caller's method call id.
func (h *Handler) EmailSubmissionSetWithImplicit(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, json.RawMessage, error) {
	slog.Debug("JMAP: submitting email", "account", accountID)
	type submissionAddress struct {
		Email      string             `json:"email"`
		Parameters map[string]*string `json:"parameters"`
	}
	type envelopeArg struct {
		MailFrom submissionAddress   `json:"mailFrom"`
		RcptTo   []submissionAddress `json:"rcptTo"`
	}
	var req struct {
		IfInState *string `json:"ifInState,omitempty"`
		Create    map[string]struct {
			IdentityID string       `json:"identityId"`
			EmailID    string       `json:"emailId"`
			Envelope   *envelopeArg `json:"envelope,omitempty"`
			SendAt     *string      `json:"sendAt,omitempty"`
		} `json:"create,omitempty"`
		Update map[string]struct {
			SendAt     *string `json:"sendAt,omitempty"`
			UndoStatus *string `json:"undoStatus,omitempty"`
		} `json:"update,omitempty"`
		Destroy []string `json:"destroy,omitempty"`
		// RFC 8621 section 7.5 keys onSuccessUpdateEmail by EmailSubmission
		// id and onSuccessDestroyEmail by the same ids.
		OnSuccessUpdateEmail  map[string]map[string]interface{} `json:"onSuccessUpdateEmail,omitempty"`
		OnSuccessDestroyEmail []string                          `json:"onSuccessDestroyEmail,omitempty"`
	}
	if err := json.Unmarshal(args, &req); err != nil {
		return nil, nil, err
	}
	// The Session advertises maxDelayedSend: 0, so a non-null sendAt cannot
	// be honoured. Reject before any state read or side effect.
	for creationID, create := range req.Create {
		if create.SendAt != nil {
			return nil, nil, base.RejectArgument("sendAt", fmt.Sprintf("delayed submission is not supported (creation %q)", creationID))
		}
	}
	for id, update := range req.Update {
		if update.SendAt != nil {
			return nil, nil, base.RejectArgument("sendAt", fmt.Sprintf("delayed submission is not supported (update %q)", id))
		}
		if update.UndoStatus != nil && *update.UndoStatus != "canceled" {
			return nil, nil, base.Errorf("invalidArguments", "update %q may only set undoStatus to canceled", id)
		}
	}

	oldState, err := h.deps.Changes.State(ctx, accountID, base.StateSubmission)
	if err != nil {
		return nil, nil, err
	}
	if req.IfInState != nil && *req.IfInState != oldState {
		return nil, nil, base.Errorf("stateMismatch", "EmailSubmission state mismatch")
	}
	created := make(map[string]interface{})
	notCreated := make(map[string]interface{})
	updated := make(map[string]interface{})
	notUpdated := make(map[string]interface{})
	destroyed := []string{}
	notDestroyed := make(map[string]interface{})
	// Keys may be real submission ids or "#creationId" for submissions
	// created by this invocation (RFC 8621 section 7.5).
	successful := make(map[string]string) // submission id -> email id
	draftsToDelete := []string{}

	// For each submission to create
	sessionID := ""
	if claims := auth.ClaimsFromContext(ctx); claims != nil {
		sessionID = claims.SID
	}
	for createID, submission := range req.Create {
		// The profile exposes exactly one synthetic identity. A missing or
		// unknown identityId cannot be honoured, so reject it rather than
		// silently attributing the submission to "primary".
		if submission.IdentityID != "primary" {
			notCreated[createID] = map[string]any{
				"type":        "invalidProperties",
				"description": "the only valid identityId is \"primary\"",
			}
			continue
		}

		// Get the email to send. Session drafts take precedence: they live in
		// per-session durable storage rather than the delivered-mail tree.
		// Fall back to regular stored mail for submissions that reference an
		// already delivered message. Only an email in the requested account
		// may be submitted, even when masquerading as that account.
		var emailMsg *mailemail.UserEmail
		var ref *mailemail.EmailRef
		var fromDraft bool
		var err error
		if sessionID != "" {
			emailMsg, err = h.deps.Storage.GetDraft(ctx, sessionID, submission.EmailID)
			if err == nil {
				// Drafts are scoped to the session, so their ref has no
				// account to compare against the requested one.
				draftRef := mailemail.EmailRefFromID(submission.EmailID)
				ref, fromDraft = &draftRef, true
			} else if errors.Is(err, storage.ErrNotFound) {
				var listed *mailemail.ListedEmail
				listed, err = h.deps.Storage.GetEmail(ctx, accountID, submission.EmailID)
				if listed != nil {
					emailMsg, ref = listed.Email, listed.Ref
				}
			}
		} else {
			var listed *mailemail.ListedEmail
			listed, err = h.deps.Storage.GetEmail(ctx, accountID, submission.EmailID)
			if listed != nil {
				emailMsg, ref = listed.Email, listed.Ref
			}
		}
		// Delivered mail must carry an account matching the request. Drafts
		// have no account on their ref; their session scope is the check.
		if err != nil || emailMsg == nil || (!fromDraft && (ref == nil || !strings.EqualFold(ref.AccountID, accountID))) {
			notCreated[createID] = map[string]string{"type": "notFound", "description": "Email not found"}
			continue
		}

		// The From address is locked to the authenticated user: submissions
		// send as the caller's own account, or as the target account when a
		// User Manager (EmissaryUserManagement) masquerades as a different
		// accountId. A client-supplied sender (the envelope mailFrom or the
		// draft's From header) is ignored.
		from, masquerading := base.LockedFrom(ctx, accountID)
		if !strings.Contains(from, "@") {
			// An account without an email address (for example the built-in
			// "admin" account) cannot send mail.
			notCreated[createID] = map[string]string{
				"type":        "invalidProperties",
				"description": "From address must be a valid email address",
			}
			continue
		}
		parsed, parseErr := mail.ParseAddress(from)
		if parseErr != nil {
			notCreated[createID] = map[string]string{"type": "invalidProperties", "description": "Invalid from address"}
			continue
		}
		from = parsed.Address

		// A locked From still has to belong to one of the account's verified
		// email identities. SES identities are the source of truth; rejecting
		// here keeps a misconfigured account from quietly sending as an
		// unverified address (or, with the log sender, disappearing entirely).
		if h.deps.FromValidator != nil {
			if err := h.deps.FromValidator.Validate(ctx, from); err != nil {
				if errors.Is(err, identity.ErrNotAllowed) {
					slog.Warn("JMAP: rejected From outside verified identities", "from", from, "err", err)
					notCreated[createID] = map[string]string{
						"type":        "forbidden",
						"description": "From address is not a verified email identity",
					}
				} else {
					slog.Error("JMAP: could not verify From identity", "from", from, "err", err)
					notCreated[createID] = map[string]string{
						"type":        "serverError",
						"description": "Could not verify From address",
					}
				}
				continue
			}
		}

		// When sending on behalf of another account, replies must go back to
		// the same address that appears in From. Default Reply-To to From when
		// the client did not set one, and reject a conflicting one.
		replyTo := append([]mailemail.Address(nil), emailMsg.ReplyTo...)
		if masquerading {
			mismatch := false
			for _, addr := range replyTo {
				if !strings.EqualFold(addr.Address, from) {
					mismatch = true
					break
				}
			}
			if mismatch {
				notCreated[createID] = map[string]string{
					"type":        "invalidProperties",
					"description": "Reply-To must match From when masquerading",
				}
				continue
			}
			if len(replyTo) == 0 {
				replyTo = []mailemail.Address{{Address: from}}
			}
		}

		// Determine recipients from the email's headers
		var toAddresses []mailemail.Address
		var ccAddresses []mailemail.Address
		var bccAddresses []mailemail.Address
		for _, addr := range emailMsg.To {
			toAddresses = append(toAddresses, addr)
		}
		for _, addr := range emailMsg.Cc {
			ccAddresses = append(ccAddresses, addr)
		}
		for _, addr := range emailMsg.Bcc {
			bccAddresses = append(bccAddresses, addr)
		}

		// If envelope is provided, validate that all recipients in the email are covered by the envelope's RcptTo
		if submission.Envelope != nil && len(submission.Envelope.RcptTo) > 0 {
			envelopeRecipients := make(map[string]bool)
			envelopeInvalid := false
			for _, rcpt := range submission.Envelope.RcptTo {
				parsed, err := mail.ParseAddress(rcpt.Email)
				if err != nil {
					notCreated[createID] = map[string]string{"type": "invalidRecipients", "description": "Invalid recipient address in envelope"}
					envelopeInvalid = true
					continue
				}
				envelopeRecipients[parsed.Address] = true
			}
			// Check that all To, Cc, Bcc recipients are in the envelope
			for _, addr := range emailMsg.To {
				if !envelopeRecipients[addr.Address] {
					notCreated[createID] = map[string]string{"type": "forbidden", "description": "Recipient not in envelope: " + addr.Address}
					envelopeInvalid = true
					continue
				}
			}
			for _, addr := range emailMsg.Cc {
				if !envelopeRecipients[addr.Address] {
					notCreated[createID] = map[string]string{"type": "forbidden", "description": "Recipient not in envelope: " + addr.Address}
					envelopeInvalid = true
					continue
				}
			}
			for _, addr := range emailMsg.Bcc {
				if !envelopeRecipients[addr.Address] {
					notCreated[createID] = map[string]string{"type": "forbidden", "description": "Recipient not in envelope: " + addr.Address}
					envelopeInvalid = true
					continue
				}
			}
			// A `continue` in the loops above only skips that address; the
			// submission itself must be skipped so it is neither sent nor
			// reported as created.
			if envelopeInvalid {
				continue
			}
		}

		if len(toAddresses) == 0 && len(ccAddresses) == 0 && len(bccAddresses) == 0 {
			notCreated[createID] = map[string]string{"type": "noRecipients", "description": "No recipients"}
			continue
		}

		// Create the message to send. The thread headers (In-Reply-To and
		// References) and the stored Message-ID are carried into the delivered
		// message so replies and forwards stay linked to their thread.
		message := &mailemail.UserEmail{
			From:       mailemail.Address{Address: from},
			ReplyTo:    replyTo,
			To:         toAddresses,
			Cc:         ccAddresses,
			Bcc:        bccAddresses,
			Subject:    emailMsg.Subject,
			Date:       time.Now().UTC(),
			Text:       emailMsg.Text,
			HTML:       emailMsg.HTML,
			MessageID:  emailMsg.MessageID,
			InReplyTo:  emailMsg.InReplyTo,
			References: emailMsg.References,
		}

		// Assign a stable id from the client's creation id, so a retried /set
		// resolves to the same record instead of creating a second one.
		subID := submissionID(accountID, createID, submission.EmailID)
		allRecipients := append(append(append([]mailemail.Address{}, toAddresses...), ccAddresses...), bccAddresses...)
		envelope := storage.SubmissionEnvelope{
			MailFrom: storage.SubmissionAddress{Email: from},
			RcptTo:   make([]storage.SubmissionAddress, 0, len(allRecipients)),
		}
		delivery := make(map[string]storage.SubmissionDelivery, len(allRecipients))
		for _, addr := range allRecipients {
			envelope.RcptTo = append(envelope.RcptTo, storage.SubmissionAddress{Email: addr.Address})
			delivery[addr.Address] = storage.SubmissionDelivery{
				SMTPReply: "250 OK",
				Delivered: "queued",
				Displayed: "unknown",
			}
		}
		record := storage.SubmissionRecord{
			ID:             subID,
			AccountID:      accountID,
			IdentityID:     submission.IdentityID,
			EmailID:        submission.EmailID,
			ThreadID:       base.ThreadID(submission.EmailID, emailMsg),
			Envelope:       envelope,
			SendAt:         time.Now().UTC().Format(time.RFC3339),
			UndoStatus:     "final",
			DeliveryStatus: delivery,
			DSNBlobIDs:     []string{},
			MDNBlobIDs:     []string{},
		}

		// Persist before handing the message to the sender. A failed state
		// write must never leave a sent message with no record (which would
		// turn a successful send into a client error), and a failed send is
		// safe to retry because the id is deterministic.
		storedRecord := record
		if err := h.mutateSubmissions(ctx, accountID, func(records map[string]storage.SubmissionRecord) error {
			if prior, exists := records[subID]; exists {
				storedRecord = prior // idempotent retry: report what is stored
				return nil
			}
			records[subID] = record
			return nil
		}); err != nil {
			slog.Error("JMAP: failed to persist submission", "account", accountID, "err", err)
			notCreated[createID] = map[string]string{"type": "serverFail", "description": "Failed to record submission: " + err.Error()}
			continue
		}

		// Send the email
		if sendErr := h.deps.Sender.Send(ctx, message); sendErr != nil {
			slog.Error("JMAP: failed to send email", "account", accountID, "create_id", createID, "err", sendErr)
			notCreated[createID] = map[string]string{"type": "serverFail", "description": "Failed to send email: " + sendErr.Error()}
			// Reflect the failure in the durable record. Best-effort: a
			// cleanup failure must not change the error already reported.
			_ = h.mutateSubmissions(ctx, accountID, func(records map[string]storage.SubmissionRecord) error {
				stored, ok := records[subID]
				if !ok {
					return nil
				}
				for addr := range stored.DeliveryStatus {
					stored.DeliveryStatus[addr] = storage.SubmissionDelivery{
						SMTPReply: "550 failed",
						Delivered: "false",
						Displayed: "unknown",
					}
				}
				records[subID] = stored
				return nil
			})
			continue
		}
		slog.Info("JMAP: email submitted", "account", accountID, "email_id", submission.EmailID)

		// A session draft has served its purpose once the message is sent.
		// Deletion is deferred until after the implicit Email/set below so
		// onSuccessUpdateEmail/onSuccessDestroyEmail can still address it.
		if sessionID != "" {
			draftsToDelete = append(draftsToDelete, submission.EmailID)
		}
		successful[submission.EmailID] = submission.EmailID
		successful["#"+createID] = submission.EmailID
		successful[subID] = submission.EmailID
		if err := h.deps.Changes.Record(ctx, accountID, base.StateSubmission, subID, base.ChangeCreate); err != nil {
			return nil, nil, err
		}

		created[createID] = toJMAPSubmission(storedRecord)
	}

	// Update and Destroy act on the durable collection. Immediate submission
	// has no undo window (maxDelayedSend: 0), so the only accepted update is
	// undoStatus: "canceled", validated above.
	if len(req.Update) > 0 || len(req.Destroy) > 0 {
		updatedIDs := make(map[string]bool)
		destroyedIDs := make(map[string]bool)
		if err := h.mutateSubmissions(ctx, accountID, func(records map[string]storage.SubmissionRecord) error {
			for id, patch := range req.Update {
				record, ok := records[id]
				if !ok || !strings.EqualFold(record.AccountID, accountID) {
					notUpdated[id] = map[string]string{"type": "notFound", "description": "Submission not found"}
					continue
				}
				if patch.UndoStatus != nil {
					record.UndoStatus = *patch.UndoStatus
				}
				records[id] = record
				updated[id] = toJMAPSubmission(record)
				updatedIDs[id] = true
			}
			for _, id := range req.Destroy {
				record, ok := records[id]
				if !ok || !strings.EqualFold(record.AccountID, accountID) {
					notDestroyed[id] = map[string]string{"type": "notFound", "description": "Submission not found"}
					continue
				}
				delete(records, id)
				destroyedIDs[id] = true
			}
			return nil
		}); err != nil {
			return nil, nil, err
		}
		for id := range updatedIDs {
			if err := h.deps.Changes.Record(ctx, accountID, base.StateSubmission, id, base.ChangeUpdate); err != nil {
				return nil, nil, err
			}
		}
		for id := range destroyedIDs {
			destroyed = append(destroyed, id)
			if err := h.deps.Changes.Record(ctx, accountID, base.StateSubmission, id, base.ChangeDestroy); err != nil {
				return nil, nil, err
			}
		}
		sort.Strings(destroyed)
	}

	// RFC 8621 section 7.5 requires one implicit Email/set call once all
	// submissions have been processed. It is a normal Email/set, so its
	// response is returned separately by the dispatcher with the same method
	// call id, rather than being merged into this response.
	implicit := map[string]interface{}{"accountId": accountID}
	if len(req.OnSuccessUpdateEmail) > 0 {
		updates := make(map[string]interface{}, len(req.OnSuccessUpdateEmail))
		for submissionID, patch := range req.OnSuccessUpdateEmail {
			emailID, ok := successful[submissionID]
			if !ok {
				slog.Debug("JMAP: onSuccessUpdateEmail references an unknown submission", "id", submissionID)
				continue
			}
			updates[emailID] = patch
		}
		implicit["update"] = updates
	}
	if len(req.OnSuccessDestroyEmail) > 0 {
		destroys := make([]string, 0, len(req.OnSuccessDestroyEmail))
		for _, submissionID := range req.OnSuccessDestroyEmail {
			emailID, ok := successful[submissionID]
			if !ok {
				slog.Debug("JMAP: onSuccessDestroyEmail references an unknown submission", "id", submissionID)
				continue
			}
			destroys = append(destroys, emailID)
		}
		implicit["destroy"] = destroys
	}
	var implicitResult json.RawMessage
	if implicit["update"] != nil || implicit["destroy"] != nil {
		raw, err := json.Marshal(implicit)
		if err != nil {
			return nil, nil, err
		}
		implicitResult, err = h.emailSet.EmailSet(ctx, accountID, raw)
		if err != nil {
			return nil, nil, err
		}
	}

	// Draft cleanup happens last so a failed implicit Email/set cannot leave a
	// successfully sent draft behind, and a successful one can still address
	// it. Deletion failures must not turn a successful send into a client
	// error, since that would encourage a duplicate submission.
	for _, draftID := range draftsToDelete {
		if err := h.deps.Storage.DeleteDraft(ctx, sessionID, draftID); err != nil {
			slog.Warn("JMAP: failed to delete sent draft", "email_id", draftID, "err", err)
		}
		if err := h.deps.Metadata.Delete(ctx, accountID, draftID); err != nil {
			slog.Warn("JMAP: failed to delete sent draft metadata", "account", accountID, "email_id", draftID, "err", err)
		}
	}

	newState, err := h.deps.Changes.State(ctx, accountID, base.StateSubmission)
	if err != nil {
		return nil, nil, err
	}
	resp := map[string]interface{}{
		"accountId":    accountID,
		"oldState":     oldState,
		"newState":     newState,
		"created":      created,
		"notCreated":   notCreated,
		"updated":      updated,
		"destroyed":    destroyed,
		"notUpdated":   notUpdated,
		"notDestroyed": notDestroyed,
	}
	result, err := json.Marshal(resp)
	if err != nil {
		return nil, nil, err
	}
	return result, implicitResult, nil
}

func (h *Handler) EmailSubmissionChanges(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error) {
	var req struct {
		SinceState string `json:"sinceState"`
		MaxChanges *int   `json:"maxChanges,omitempty"`
	}
	if err := json.Unmarshal(args, &req); err != nil {
		return nil, err
	}
	result, err := h.deps.Changes.ChangesFrom(ctx, accountID, base.StateSubmission, req.SinceState, req.MaxChanges)
	if err != nil {
		return nil, err
	}
	return json.Marshal(result)
}

func (h *Handler) EmailSubmissionQuery(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error) {
	var req struct {
		Sort           []interface{} `json:"sort,omitempty"`
		Position       *int          `json:"position,omitempty"`
		Anchor         *string       `json:"anchor,omitempty"`
		AnchorOffset   *int          `json:"anchorOffset,omitempty"`
		Limit          *int          `json:"limit,omitempty"`
		CalculateTotal *bool         `json:"calculateTotal,omitempty"`
	}
	if err := json.Unmarshal(args, &req); err != nil {
		return nil, err
	}
	filter, err := parseSubmissionFilter(args)
	if err != nil {
		return nil, err
	}
	property, ascending, err := submissionSortProperty(req.Sort)
	if err != nil {
		return nil, err
	}
	queryKey, err := base.QueryKey(filter, req.Sort)
	if err != nil {
		return nil, err
	}

	records, _, err := h.deps.Submissions.Load(ctx, accountID)
	if err != nil {
		return nil, err
	}
	allIDs := sortedSubmissionIDs(records, filter, property, ascending)
	start, end, err := base.PageBoundsForIDs(allIDs, req.Position, req.Limit, req.Anchor, req.AnchorOffset)
	if err != nil {
		return nil, err
	}
	ids := append([]string{}, allIDs[start:end]...)

	queryState, err := h.deps.Changes.SyncQuery(ctx, accountID, base.StateSubmissionQuery, queryKey, allIDs)
	if err != nil {
		return nil, err
	}
	resp := map[string]interface{}{
		"accountId":           accountID,
		"queryState":          queryState,
		"canCalculateChanges": true,
		"position":            start,
		"ids":                 ids,
	}
	// RFC 8620 section 5.4: total MUST be omitted unless requested.
	if req.CalculateTotal != nil && *req.CalculateTotal {
		resp["total"] = len(allIDs)
	}
	return json.Marshal(resp)
}

func (h *Handler) EmailSubmissionQueryChanges(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error) {
	var req struct {
		SinceQueryState string        `json:"sinceQueryState"`
		Sort            []interface{} `json:"sort,omitempty"`
		MaxChanges      *int          `json:"maxChanges,omitempty"`
		UpToID          *string       `json:"upToId,omitempty"`
		CalculateTotal  *bool         `json:"calculateTotal,omitempty"`
	}
	if err := json.Unmarshal(args, &req); err != nil {
		return nil, err
	}
	if req.UpToID != nil {
		return nil, base.RejectArgument("upToId", "the change tracker cannot limit a query diff to an id")
	}
	filter, err := parseSubmissionFilter(args)
	if err != nil {
		return nil, err
	}
	property, ascending, err := submissionSortProperty(req.Sort)
	if err != nil {
		return nil, err
	}
	queryKey, err := base.QueryKey(filter, req.Sort)
	if err != nil {
		return nil, err
	}

	records, _, err := h.deps.Submissions.Load(ctx, accountID)
	if err != nil {
		return nil, err
	}
	ids := sortedSubmissionIDs(records, filter, property, ascending)
	result, err := h.deps.Changes.QueryChangesFrom(ctx, accountID, base.StateSubmissionQuery, queryKey, req.SinceQueryState, req.MaxChanges, ids)
	if err != nil {
		return nil, err
	}
	if req.CalculateTotal != nil && *req.CalculateTotal {
		result["total"] = len(ids)
	}
	return json.Marshal(result)
}
