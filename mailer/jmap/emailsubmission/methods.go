package emailsubmission

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/mail"
	"strings"
	"time"

	mailemail "github.com/kncept/Emissary/mailer/email"
	"github.com/kncept/Emissary/mailer/identity"
	"github.com/kncept/Emissary/mailer/jmap/internal/base"
	"github.com/kncept/Emissary/mailer/storage"
)

func (h *Handler) EmailSubmissionGet(_ context.Context, _ string, _ json.RawMessage) (json.RawMessage, error) {
	return base.NotImplemented(MethodGet)
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
	var req struct {
		AccountID string `json:"accountId"`
		Create    map[string]struct {
			IdentityID string `json:"identityId"`
			EmailID    string `json:"emailId"`
			Envelope   *struct {
				MailFrom struct {
					Email string `json:"email"`
				} `json:"mailFrom"`
				RcptTo []struct {
					Email string `json:"email"`
				} `json:"rcptTo"`
			} `json:"envelope,omitempty"`
			SendAt *string `json:"sendAt,omitempty"`
		} `json:"create,omitempty"`
		// RFC 8621 section 7.5 keys onSuccessUpdateEmail by EmailSubmission
		// id and onSuccessDestroyEmail by the same ids. Emissary uses the
		// submission's email id as its submission id.
		OnSuccessUpdateEmail  map[string]map[string]interface{} `json:"onSuccessUpdateEmail,omitempty"`
		OnSuccessDestroyEmail []string                          `json:"onSuccessDestroyEmail,omitempty"`
	}
	if err := json.Unmarshal(args, &req); err != nil {
		return nil, nil, err
	}

	oldState := h.deps.Changes.State(accountID, base.StateSubmission)
	created := make(map[string]interface{})
	notCreated := make(map[string]interface{})
	notUpdated := make(map[string]interface{})
	notDestroyed := make(map[string]interface{})
	// Keys may be real submission ids or "#creationId" for submissions
	// created by this invocation (RFC 8621 section 7.5).
	successful := make(map[string]string) // submission id -> email id
	draftsToDelete := []string{}

	// For each submission to create
	sessionID := ""
	if claims := base.ClaimsFromContext(ctx); claims != nil {
		sessionID = claims.SID
	}
	for createID, submission := range req.Create {
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
				listed, err = h.deps.Storage.GetEmail(ctx, submission.EmailID)
				if listed != nil {
					emailMsg, ref = listed.Email, listed.Ref
				}
			}
		} else {
			var listed *mailemail.ListedEmail
			listed, err = h.deps.Storage.GetEmail(ctx, submission.EmailID)
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

		// Send the email
		if err := h.deps.Sender.Send(ctx, message); err != nil {
			slog.Error("JMAP: failed to send email", "account", accountID, "create_id", createID, "err", err)
			notCreated[createID] = map[string]string{"type": "serverFail", "description": "Failed to send email: " + err.Error()}
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
		h.deps.Changes.Record(accountID, base.StateSubmission, submission.EmailID, base.ChangeCreate)

		// Success - create the submission object
		created[createID] = map[string]interface{}{
			"id":         submission.EmailID, // Use the email ID as submission ID
			"identityId": submission.IdentityID,
			"emailId":    submission.EmailID,
			"envelope": map[string]interface{}{
				"mailFrom": map[string]string{"email": from},
				"rcptTo":   toAddresses,
			},
		}
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

	resp := map[string]interface{}{
		"accountId":    accountID,
		"oldState":     oldState,
		"newState":     h.deps.Changes.State(accountID, base.StateSubmission),
		"created":      created,
		"notCreated":   notCreated,
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
		AccountID  string `json:"accountId"`
		SinceState string `json:"sinceState"`
		MaxChanges *int   `json:"maxChanges,omitempty"`
	}
	if err := json.Unmarshal(args, &req); err != nil {
		return nil, err
	}
	result, err := h.deps.Changes.ChangesFrom(accountID, base.StateSubmission, req.SinceState, req.MaxChanges)
	if err != nil {
		return nil, err
	}
	return json.Marshal(result)
}

func (h *Handler) EmailSubmissionQuery(_ context.Context, _ string, _ json.RawMessage) (json.RawMessage, error) {
	return base.NotImplemented(MethodQuery)
}

func (h *Handler) EmailSubmissionQueryChanges(_ context.Context, _ string, _ json.RawMessage) (json.RawMessage, error) {
	return base.NotImplemented(MethodQueryChanges)
}
