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
	jmapidentity "github.com/kncept/Emissary/mailer/jmap/identity"
	"github.com/kncept/Emissary/mailer/jmap/internal/base"
)

func (h *Handler) EmailSubmissionGet(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error) {
	records, _, err := h.deps.Submissions.Load(ctx, accountID)
	if err != nil {
		return nil, err
	}
	// The store returns a map; sort by id so the "all records" case is stable.
	ordered := make([]storage.SubmissionRecord, 0, len(records))
	for _, record := range records {
		if strings.EqualFold(record.AccountID, accountID) {
			ordered = append(ordered, record)
		}
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].ID < ordered[j].ID })

	state, err := h.deps.Changes.State(ctx, accountID, base.StateSubmission)
	if err != nil {
		return nil, err
	}
	return base.HandleGet(ctx, accountID, args, base.GetSpec[storage.SubmissionRecord]{
		Allowed: base.ArgumentSet("accountId", "ids", "properties"),
		Model:   jmapSubmission{},
		Load:    func(context.Context) ([]storage.SubmissionRecord, error) { return ordered, nil },
		ID:      func(record storage.SubmissionRecord) string { return record.ID },
		State:   func(context.Context) (string, error) { return state, nil },
		Render: func(_ context.Context, record storage.SubmissionRecord, selection *base.Selection, _ json.RawMessage) (json.RawMessage, error) {
			return selection.Filter(toJMAPSubmission(record))
		},
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
	if err := base.RejectUnknownArguments(args, base.ArgumentSet(
		"accountId", "ifInState", "create", "update", "destroy",
		"onSuccessUpdateEmail", "onSuccessDestroyEmail")); err != nil {
		return nil, nil, err
	}
	// Keep the raw create/update objects so RFC 8620 property and patch
	// validation can inspect the exact keys the client supplied.
	var rawArgs struct {
		Create map[string]json.RawMessage `json:"create"`
		Update map[string]json.RawMessage `json:"update"`
	}
	if err := json.Unmarshal(args, &rawArgs); err != nil {
		return nil, nil, err
	}
	invalidCreates := base.ValidateCreateProperties(rawArgs.Create,
		base.ArgumentSet("identityId", "emailId", "envelope", "sendAt"),
		base.ArgumentSet("id", "submissionId", "undoStatus"))
	for id, raw := range rawArgs.Update {
		if _, err := base.ApplyPatch(json.RawMessage(`{}`), raw); err != nil {
			return nil, nil, base.Errorf("invalidPatch", "update %q: %s", id, err.Error())
		}
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
		if names, bad := invalidCreates[createID]; bad {
			notCreated[createID] = base.InvalidProperties(base.SetErrorNames(names), names...)
			continue
		}
		// Resolve the Identity from the same durable collection Identity/get
		// serves. A missing or unknown id is never silently attributed to
		// "primary": each account may now own several verified identities.
		fromIdentity, lookupErr := jmapidentity.Lookup(ctx, h.deps, accountID, submission.IdentityID)
		if lookupErr != nil {
			if errors.Is(lookupErr, jmapidentity.ErrIdentityNotFound) {
				notCreated[createID] = base.NewSetError("notFound", "Identity not found")
			} else {
				notCreated[createID] = base.NewSetError("serverFail", "Could not resolve Identity")
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
			notCreated[createID] = base.NewSetError("notFound", "Email not found")
			continue
		}

		// The From address comes from the selected Identity, never from the
		// client-supplied envelope or draft header. Because identities are
		// loaded for the target account, a masquerading caller is still limited
		// to addresses owned by that account.
		_, masquerading := base.LockedFrom(ctx, accountID)
		from := fromIdentity.Email
		if !strings.Contains(from, "@") {
			// An account without an email address (for example the built-in
			// "admin" account) cannot send mail.
			notCreated[createID] = base.NewSetError("invalidProperties", "From address must be a valid email address")
			continue
		}
		parsed, parseErr := mail.ParseAddress(from)
		if parseErr != nil {
			notCreated[createID] = base.NewSetError("invalidProperties", "Invalid from address")
			continue
		}
		from = parsed.Address

		// The identity's From still has to belong to a verified email
		// identity. SES identities are the source of truth; rejecting here
		// keeps a misconfigured account from quietly sending as an unverified
		// address (or, with the log sender, disappearing entirely).
		if h.deps.FromValidator != nil {
			if err := h.deps.FromValidator.Validate(ctx, from); err != nil {
				if errors.Is(err, identity.ErrNotAllowed) {
					slog.Warn("JMAP: rejected From outside verified identities", "from", from, "err", err)
					notCreated[createID] = base.NewSetError("forbidden", "From address is not a verified email identity")
				} else {
					slog.Error("JMAP: could not verify From identity", "from", from, "err", err)
					notCreated[createID] = base.NewSetError("serverFail", "Could not verify From address")
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
				notCreated[createID] = base.NewSetError("invalidProperties", "Reply-To must match From when masquerading")
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
					notCreated[createID] = base.NewSetError("invalidRecipients", "Invalid recipient address in envelope")
					envelopeInvalid = true
					continue
				}
				envelopeRecipients[parsed.Address] = true
			}
			// Check that all To, Cc, Bcc recipients are in the envelope
			for _, addr := range emailMsg.To {
				if !envelopeRecipients[addr.Address] {
					notCreated[createID] = base.NewSetError("forbidden", "Recipient not in envelope: "+addr.Address)
					envelopeInvalid = true
					continue
				}
			}
			for _, addr := range emailMsg.Cc {
				if !envelopeRecipients[addr.Address] {
					notCreated[createID] = base.NewSetError("forbidden", "Recipient not in envelope: "+addr.Address)
					envelopeInvalid = true
					continue
				}
			}
			for _, addr := range emailMsg.Bcc {
				if !envelopeRecipients[addr.Address] {
					notCreated[createID] = base.NewSetError("forbidden", "Recipient not in envelope: "+addr.Address)
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
			notCreated[createID] = base.NewSetError("noRecipients", "No recipients")
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
			notCreated[createID] = base.NewSetError("serverFail", "Failed to record submission: "+err.Error())
			continue
		}

		// Send the email
		if sendErr := h.deps.Sender.Send(ctx, message); sendErr != nil {
			slog.Error("JMAP: failed to send email", "account", accountID, "create_id", createID, "err", sendErr)
			notCreated[createID] = base.NewSetError("serverFail", "Failed to send email: "+sendErr.Error())
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
					notUpdated[id] = base.NewSetError("notFound", "Submission not found")
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
					notDestroyed[id] = base.NewSetError("notFound", "Submission not found")
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
	result, err := base.SetResult{
		AccountID:    accountID,
		OldState:     &oldState,
		NewState:     &newState,
		Created:      created,
		Updated:      updated,
		Destroyed:    destroyed,
		NotCreated:   notCreated,
		NotUpdated:   notUpdated,
		NotDestroyed: notDestroyed,
	}.Marshal()
	if err != nil {
		return nil, nil, err
	}
	return result, implicitResult, nil
}

func (h *Handler) EmailSubmissionChanges(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error) {
	return base.HandleChanges(ctx, accountID, args, base.ChangesSpec{
		Allowed: base.ArgumentSet("accountId", "sinceState", "maxChanges"),
		Changes: func(ctx context.Context, sinceState string, maxChanges *int) (map[string]any, error) {
			return h.deps.Changes.ChangesFrom(ctx, accountID, base.StateSubmission, sinceState, maxChanges)
		},
	})
}

func (h *Handler) EmailSubmissionQuery(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error) {
	return base.HandleQuery(ctx, accountID, args, h.querySpec())
}

func (h *Handler) EmailSubmissionQueryChanges(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error) {
	return base.HandleQueryChanges(ctx, accountID, args, h.querySpec())
}

// querySpec is shared by EmailSubmission/query and /queryChanges.
func (h *Handler) querySpec() base.QuerySpec {
	return base.QuerySpec{
		Allowed: base.ArgumentSet("accountId", "filter", "sort", "position", "anchor", "anchorOffset",
			"limit", "calculateTotal"),
		ChangesAllowed: base.ArgumentSet("accountId", "filter", "sinceQueryState", "sort", "maxChanges",
			"upToId", "calculateTotal"),
		SortProperties: submissionSortFields,
		ParseFilter:    func(args json.RawMessage) (any, error) { return parseSubmissionFilter(args) },
		AllIDs: func(ctx context.Context, accountID string, args json.RawMessage, filter any, ascending bool) ([]string, error) {
			var raw struct {
				Sort []interface{} `json:"sort"`
			}
			if err := json.Unmarshal(args, &raw); err != nil {
				return nil, base.Errorf("invalidArguments", "query arguments must be an object")
			}
			property, _, err := submissionSortProperty(raw.Sort)
			if err != nil {
				return nil, err
			}
			records, _, err := h.deps.Submissions.Load(ctx, accountID)
			if err != nil {
				return nil, err
			}
			typed, _ := filter.(*submissionFilter)
			return sortedSubmissionIDs(records, typed, property, ascending), nil
		},
		Tracker: h.deps.Changes,
		Kind:    base.StateSubmissionQuery,
	}
}
