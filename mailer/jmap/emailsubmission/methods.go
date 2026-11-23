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
	"github.com/kncept/Emissary/mailer/components/sender"
	"github.com/kncept/Emissary/mailer/components/storage"
	mailemail "github.com/kncept/Emissary/mailer/email"
	"github.com/kncept/Emissary/mailer/identity"
	jmapidentity "github.com/kncept/Emissary/mailer/jmap/identity"
	"github.com/kncept/Emissary/mailer/jmap/internal/base"
)

// submissionPatchProperties is the full EmailSubmission object. RFC 8620
// section 5.3 permits a PatchObject to contain any property of the object;
// server-set properties may only be repeated with their current value.
var submissionPatchProperties = base.ArgumentSet(
	"id", "identityId", "emailId", "threadId", "envelope", "sendAt",
	"undoStatus", "deliveryStatus", "dsnBlobIds", "mdnBlobIds")

// submissionImmutableProperties marks every property except undoStatus, the
// only property RFC 8621 section 7.5 allows a client to change.
var submissionImmutableProperties = func() map[string]bool {
	immutable := make(map[string]bool, len(submissionPatchProperties))
	for name := range submissionPatchProperties {
		immutable[name] = name != "undoStatus"
	}
	return immutable
}()

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
	slog.Info("JMAP: submitting email", "account", accountID)
	// RFC 8621 section 7.5 keys onSuccessUpdateEmail by EmailSubmission id
	// and onSuccessDestroyEmail by the same ids.
	var rawArgs struct {
		IfInState             *string                           `json:"ifInState"`
		Create                map[string]json.RawMessage        `json:"create"`
		Update                map[string]json.RawMessage        `json:"update"`
		Destroy               []string                          `json:"destroy"`
		OnSuccessUpdateEmail  map[string]map[string]interface{} `json:"onSuccessUpdateEmail"`
		OnSuccessDestroyEmail []string                          `json:"onSuccessDestroyEmail"`
	}
	if err := base.RejectUnknownArguments(args, base.ArgumentSet(
		"accountId", "ifInState", "create", "update", "destroy",
		"onSuccessUpdateEmail", "onSuccessDestroyEmail")); err != nil {
		return nil, nil, err
	}
	if err := json.Unmarshal(args, &rawArgs); err != nil {
		return nil, nil, base.Errorf("invalidArguments", "set arguments must be an object")
	}

	// Strict RFC 8620 section 5.3 property validation happens before any
	// state read or send. Unknown and server-set create properties are
	// per-record invalidProperties SetErrors, never silently ignored.
	invalidCreates := base.ValidateCreateProperties(rawArgs.Create,
		base.ArgumentSet("identityId", "emailId", "envelope", "sendAt"),
		base.ArgumentSet("id", "submissionId", "undoStatus", "threadId",
			"deliveryStatus", "dsnBlobIds", "mdnBlobIds"))
	// Patch values and pointers are validated in applySubmissionPatch against
	// the current object. This accepts legal full-object patches (including
	// unchanged server-set properties) while still returning invalidPatch for
	// malformed pointers.

	created := make(map[string]interface{})
	notCreated := make(map[string]interface{})
	updated := make(map[string]interface{})
	notUpdated := make(map[string]interface{})
	destroyed := []string{}
	notDestroyed := make(map[string]interface{})

	creates := make(map[string]submissionCreate)
	for creationID, raw := range rawArgs.Create {
		if names, bad := invalidCreates[creationID]; bad {
			notCreated[creationID] = base.InvalidProperties("Invalid create properties", names...)
			continue
		}
		var create submissionCreate
		if err := json.Unmarshal(raw, &create); err != nil {
			notCreated[creationID] = base.InvalidProperties("Invalid create property value")
			continue
		}
		missing := make([]string, 0, 2)
		if strings.TrimSpace(create.IdentityID) == "" {
			missing = append(missing, "identityId")
		}
		if strings.TrimSpace(create.EmailID) == "" {
			missing = append(missing, "emailId")
		}
		if len(missing) > 0 {
			notCreated[creationID] = base.InvalidProperties("identityId and emailId are required", missing...)
			continue
		}
		var fields map[string]json.RawMessage
		_ = json.Unmarshal(raw, &fields)
		if envelopeRaw, ok := fields["envelope"]; ok && string(envelopeRaw) != "null" {
			if properties := validateEnvelopeObject(envelopeRaw); len(properties) > 0 {
				notCreated[creationID] = base.InvalidProperties("Invalid envelope", properties...)
				continue
			}
		}
		// The Session advertises maxDelayedSend: 0, so sendAt is explicitly
		// unsupported. Keep this a documented invalidArguments method error.
		if create.SendAt != nil {
			return nil, nil, base.RejectArgument("sendAt",
				fmt.Sprintf("delayed submission is not supported (creation %q)", creationID))
		}
		creates[creationID] = create
	}

	updates := rawArgs.Update

	oldState, err := h.deps.Changes.State(ctx, accountID, base.StateSubmission)
	if err != nil {
		return nil, nil, err
	}
	if err := base.CheckIfInState(rawArgs.IfInState, oldState); err != nil {
		return nil, nil, err
	}
	// Keys may be real submission ids or "#creationId" for submissions
	// created by this invocation (RFC 8621 section 7.5).
	successful := make(map[string]string) // submission id -> email id
	draftsToDelete := []string{}

	// For each submission to create
	sessionID := ""
	if claims := auth.ClaimsFromContext(ctx); claims != nil {
		sessionID = claims.SID
	}
	for createID, submission := range creates {
		// Resolve the Identity from the same durable collection Identity/get
		// serves. A missing or unknown id is never silently attributed to
		// "primary": each account may now own several verified identities.
		fromIdentity, lookupErr := jmapidentity.Lookup(ctx, h.deps, accountID, submission.IdentityID)
		if lookupErr != nil {
			// RFC 8621 section 7.5 explicitly requires invalidProperties
			// (not notFound) when the Identity id cannot be found.
			if errors.Is(lookupErr, jmapidentity.ErrIdentityNotFound) {
				notCreated[createID] = base.InvalidProperties("Identity not found", "identityId")
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
			// RFC 8621 section 7.5 explicitly requires invalidProperties
			// (not notFound) when the Email id cannot be found.
			notCreated[createID] = base.InvalidProperties("Email not found", "emailId")
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
				switch {
				case errors.Is(err, identity.ErrNotAllowed):
					slog.Warn("JMAP: rejected From outside verified identities", "from", from, "err", err)
					notCreated[createID] = base.NewSetError("forbiddenFrom", "From address is not a verified email identity")
				case errors.Is(err, identity.ErrSendForbidden):
					slog.Warn("JMAP: sending is disabled for this account", "account", accountID, "err", err)
					notCreated[createID] = base.NewSetError("forbiddenToSend", err.Error())
				default:
					slog.Error("JMAP: could not verify From identity", "from", from, "err", err)
					notCreated[createID] = base.NewSetError("serverFail", "Could not verify From address")
				}
				continue
			}
		}

		// The SMTP envelope MAIL FROM is locked to the Identity email. A
		// client may omit it (the server generates it) or repeat the verified
		// address, but may not nominate a different reverse path.
		if submission.Envelope != nil {
			mailFrom := strings.TrimSpace(submission.Envelope.MailFrom.Email)
			if mailFrom != "" {
				parsedMailFrom, err := mail.ParseAddress(mailFrom)
				if err != nil || !strings.EqualFold(parsedMailFrom.Address, from) {
					notCreated[createID] = base.NewSetError("forbiddenMailFrom",
						"Envelope mailFrom must match the Identity email")
					continue
				}
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

		// Collect and validate the header recipients. An invalid address is an
		// invalidEmail error listing every offending header (RFC 8621 7.5).
		var toAddresses, ccAddresses, bccAddresses []mailemail.Address
		badHeaderProperties := make(map[string]bool)
		for _, group := range []struct {
			name   string
			source []mailemail.Address
			dest   *[]mailemail.Address
		}{
			{"to", emailMsg.To, &toAddresses},
			{"cc", emailMsg.Cc, &ccAddresses},
			{"bcc", emailMsg.Bcc, &bccAddresses},
		} {
			for _, addr := range group.source {
				if _, err := mail.ParseAddress(addr.Address); err != nil {
					badHeaderProperties[group.name] = true
					continue
				}
				*group.dest = append(*group.dest, addr)
			}
		}
		if len(badHeaderProperties) > 0 {
			properties := make([]string, 0, len(badHeaderProperties))
			for name := range badHeaderProperties {
				properties = append(properties, name)
			}
			sort.Strings(properties)
			notCreated[createID] = base.InvalidEmail("Invalid Email header address", properties...)
			continue
		}

		// The generated envelope is the deduplicated To/Cc/Bcc set. A supplied
		// envelope is authoritative: it must cover every header recipient and
		// may add SMTP-only recipients. Its parameters are preserved on the
		// durable Envelope and passed to the sender.
		headers := dedupeAddresses(toAddresses, ccAddresses, bccAddresses)
		allRecipients := make([]envelopeRecipient, 0, len(headers))
		if submission.Envelope == nil {
			for _, addr := range headers {
				allRecipients = append(allRecipients, envelopeRecipient{Address: addr})
			}
		} else {
			if len(submission.Envelope.RcptTo) == 0 {
				notCreated[createID] = base.NewSetError("noRecipients", "Envelope has no rcptTo addresses")
				continue
			}
			byAddress := make(map[string]envelopeRecipient, len(submission.Envelope.RcptTo))
			invalid := make([]string, 0)
			for _, rcpt := range submission.Envelope.RcptTo {
				parsed, err := mail.ParseAddress(rcpt.Email)
				if err != nil {
					invalid = append(invalid, rcpt.Email)
					continue
				}
				byAddress[addressKey(parsed.Address)] = envelopeRecipient{
					Address:    mailemail.Address{Address: parsed.Address},
					Parameters: copyParameters(rcpt.Parameters),
				}
				allRecipients = append(allRecipients, envelopeRecipient{
					Address:    mailemail.Address{Address: parsed.Address},
					Parameters: copyParameters(rcpt.Parameters),
				})
			}
			if len(invalid) > 0 {
				notCreated[createID] = base.InvalidRecipients("Invalid recipient address in envelope", invalid)
				continue
			}
			missing := false
			for _, addr := range headers {
				if _, ok := byAddress[addressKey(addr.Address)]; !ok {
					notCreated[createID] = base.NewSetError("forbidden", "Recipient not in envelope: "+addr.Address)
					missing = true
					break
				}
			}
			if missing {
				continue
			}
		}

		if len(allRecipients) == 0 {
			notCreated[createID] = base.NewSetError("noRecipients", "No recipients")
			continue
		}
		if uint64(len(allRecipients)) > h.maxRecipients {
			notCreated[createID] = base.TooManyRecipients("Too many envelope recipients", h.maxRecipients)
			continue
		}

		// Create the message to send. Thread headers and the stored Message-ID
		// are carried through so replies stay linked; the explicit envelope is
		// send-time metadata and never alters the RFC 5322 headers.
		mailFrom := submissionAddress{Email: from}
		if submission.Envelope != nil {
			mailFrom = submissionAddress{
				Email:      submission.Envelope.MailFrom.Email,
				Parameters: copyParameters(submission.Envelope.MailFrom.Parameters),
			}
		}
		message := &mailemail.UserEmail{
			From:               mailemail.Address{Address: from},
			ReplyTo:            replyTo,
			To:                 toAddresses,
			Cc:                 ccAddresses,
			Bcc:                bccAddresses,
			Subject:            emailMsg.Subject,
			Date:               time.Now().UTC(),
			Text:               emailMsg.Text,
			HTML:               emailMsg.HTML,
			MessageID:          emailMsg.MessageID,
			InReplyTo:          emailMsg.InReplyTo,
			References:         emailMsg.References,
			SubmissionEnvelope: senderEnvelope(mailFrom, allRecipients),
		}

		// Enforce the same octet limit the provider sees before any durable
		// record exists. tooLarge must carry maxSize (RFC 8621 7.5).
		if size := uint64(sender.WireSize(message)); size > h.maxMessageSize {
			notCreated[createID] = base.TooLarge(
				fmt.Sprintf("Message size %d exceeds the send limit", size), h.maxMessageSize)
			continue
		}

		// A stable id makes a repeated request idempotent. The record is
		// written before Send() so a crash mid-send leaves a retryable record
		// rather than a successful send with no record at all.
		subID := submissionID(accountID, createID, submission.EmailID)
		envelope := storage.SubmissionEnvelope{
			MailFrom: storageAddress(mailFrom),
			RcptTo:   make([]storage.SubmissionAddress, 0, len(allRecipients)),
		}
		delivery := make(map[string]storage.SubmissionDelivery, len(allRecipients))
		for _, recipient := range allRecipients {
			envelope.RcptTo = append(envelope.RcptTo, storageAddress(submissionAddress{
				Email:      recipient.Address.Address,
				Parameters: recipient.Parameters,
			}))
			delivery[recipient.Address.Address] = relayedDelivery()
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

		storedRecord := record
		var prior *storage.SubmissionRecord
		if err := h.mutateSubmissions(ctx, accountID, func(records map[string]storage.SubmissionRecord) error {
			storedRecord = record
			prior = nil
			if existing, exists := records[subID]; exists {
				copy := existing
				storedRecord = copy
				prior = &copy
			} else {
				records[subID] = record
			}
			return nil
		}); err != nil {
			slog.Error("JMAP: failed to persist submission", "account", accountID, "err", err)
			notCreated[createID] = base.NewSetError("serverFail", "Failed to record submission: "+err.Error())
			continue
		}
		if prior != nil && prior.Relayed {
			// Repeated creation id with the same Email: the original request
			// already sent this message, so report success without a duplicate.
			created[createID] = submissionCreateResponse(*prior, submission.Envelope != nil)
			successful[submission.EmailID] = submission.EmailID
			successful["#"+createID] = submission.EmailID
			successful[subID] = submission.EmailID
			continue
		}

		if sendErr := h.deps.Sender.Send(ctx, message); sendErr != nil {
			sendErr = sender.ClassifySendError(sendErr)
			slog.Error("JMAP: failed to send email", "account", accountID, "create_id", createID, "err", sendErr)
			notCreated[createID] = setErrorFromSendError(sendErr, h.maxMessageSize)
			_ = h.mutateSubmissions(ctx, accountID, func(records map[string]storage.SubmissionRecord) error {
				delete(records, subID)
				return nil
			})
			continue
		}
		storedRecord.Relayed = true
		if err := h.mutateSubmissions(ctx, accountID, func(records map[string]storage.SubmissionRecord) error {
			stored := storedRecord
			if existing, exists := records[subID]; exists {
				stored = existing
				stored.Relayed = true
			}
			records[subID] = stored
			storedRecord = stored
			return nil
		}); err != nil {
			slog.Error("JMAP: failed to mark submission as relayed", "account", accountID, "id", subID, "err", err)
			notCreated[createID] = base.NewSetError("serverFail", "Message was sent but could not be recorded: "+err.Error())
			continue
		}
		slog.Info("JMAP: email submitted", "account", accountID, "email_id", submission.EmailID)

		// Every successful submission leaves a copy in the account's Sent
		// mailbox. This is a distinct Email id so the implicit Email/set below
		// may update or destroy the source draft without removing the sent copy.
		if sentID, err := h.fileInSent(ctx, accountID, message); err != nil {
			// The message has already left the server. Failing the method here
			// would encourage the client to send it a second time.
			slog.Warn("JMAP: could not file sent email", "account", accountID,
				"email_id", submission.EmailID, "err", err)
		} else {
			slog.Info("JMAP: filed sent email", "account", accountID, "sent_id", sentID)
		}

		// A session scratch draft has served its purpose once the message is
		// sent. Stored drafts are left alone: onSuccessUpdateEmail and
		// onSuccessDestroyEmail decide what happens to them, and deleting their
		// metadata here would discard keywords the client did not ask to clear.
		// Deletion is deferred until after the implicit Email/set below so
		// those patches can still address the draft.
		if fromDraft && sessionID != "" {
			draftsToDelete = append(draftsToDelete, submission.EmailID)
		}
		successful[submission.EmailID] = submission.EmailID
		successful["#"+createID] = submission.EmailID
		successful[subID] = submission.EmailID
		if err := h.deps.Changes.Record(ctx, accountID, base.StateSubmission, subID, base.ChangeCreate); err != nil {
			slog.Warn("JMAP: could not record submission change", "account", accountID, "id", subID, "err", err)
		}

		created[createID] = submissionCreateResponse(storedRecord, submission.Envelope != nil)
	}

	// Update and Destroy act on the durable collection. A CAS retry re-runs
	// the callback, so results are accumulated in a fresh outcome each time
	// and only copied to the response after the write succeeds.
	if len(updates) > 0 || len(rawArgs.Destroy) > 0 {
		type setOutcome struct {
			updated      map[string]any
			notUpdated   map[string]any
			destroyed    []string
			notDestroyed map[string]any
		}
		var outcome setOutcome
		if err := h.mutateSubmissions(ctx, accountID, func(records map[string]storage.SubmissionRecord) error {
			outcome = setOutcome{
				updated:      make(map[string]any),
				notUpdated:   make(map[string]any),
				notDestroyed: make(map[string]any),
			}
			for id, patch := range updates {
				record, ok := records[id]
				if !ok || !strings.EqualFold(record.AccountID, accountID) {
					outcome.notUpdated[id] = base.NewSetError("notFound", "Submission not found")
					continue
				}
				updatedRecord, setErr, err := applySubmissionPatch(record, patch)
				if err != nil {
					return err
				}
				if setErr != nil {
					outcome.notUpdated[id] = *setErr
					continue
				}
				records[id] = updatedRecord
				// Properties changed by the PatchObject are explicitly
				// requested, so no server-set property changed implicitly.
				outcome.updated[id] = map[string]any{}
			}
			for _, id := range rawArgs.Destroy {
				record, ok := records[id]
				if !ok || !strings.EqualFold(record.AccountID, accountID) {
					outcome.notDestroyed[id] = base.NewSetError("notFound", "Submission not found")
					continue
				}
				delete(records, id)
				outcome.destroyed = append(outcome.destroyed, id)
			}
			return nil
		}); err != nil {
			return nil, nil, err
		}
		for id, value := range outcome.updated {
			updated[id] = value
			if err := h.deps.Changes.Record(ctx, accountID, base.StateSubmission, id, base.ChangeUpdate); err != nil {
				slog.Warn("JMAP: could not record submission change", "account", accountID, "id", id, "err", err)
			}
		}
		for id, value := range outcome.notUpdated {
			notUpdated[id] = value
		}
		for id, value := range outcome.notDestroyed {
			notDestroyed[id] = value
		}
		for _, id := range outcome.destroyed {
			destroyed = append(destroyed, id)
			if err := h.deps.Changes.Record(ctx, accountID, base.StateSubmission, id, base.ChangeDestroy); err != nil {
				slog.Warn("JMAP: could not record submission change", "account", accountID, "id", id, "err", err)
			}
		}
		sort.Strings(destroyed)
	}

	// RFC 8621 section 7.5 requires one implicit Email/set call once all
	// submissions have been processed. It is a normal Email/set, so its
	// response is returned separately by the dispatcher with the same method
	// call id, rather than being merged into this response.
	implicit := map[string]interface{}{"accountId": accountID}
	if len(rawArgs.OnSuccessUpdateEmail) > 0 {
		updates := make(map[string]interface{}, len(rawArgs.OnSuccessUpdateEmail))
		for submissionID, patch := range rawArgs.OnSuccessUpdateEmail {
			emailID, ok := successful[submissionID]
			if !ok {
				slog.Warn("JMAP: onSuccessUpdateEmail references an unknown submission",
					"id", submissionID, "not_created", notCreated)
				continue
			}
			updates[emailID] = patch
		}
		implicit["update"] = updates
	}
	if len(rawArgs.OnSuccessDestroyEmail) > 0 {
		destroys := make([]string, 0, len(rawArgs.OnSuccessDestroyEmail))
		for _, submissionID := range rawArgs.OnSuccessDestroyEmail {
			emailID, ok := successful[submissionID]
			if !ok {
				slog.Warn("JMAP: onSuccessDestroyEmail references an unknown submission",
					"id", submissionID, "not_created", notCreated)
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
			// The message has already been relayed. Reporting a method error
			// would violate RFC 8620 section 3.6.2, so the implicit Email/set
			// failure is logged and the submission result is still returned.
			slog.Warn("JMAP: implicit Email/set failed after submission", "account", accountID, "err", err)
			implicitResult = nil
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
