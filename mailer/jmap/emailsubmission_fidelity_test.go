package jmap

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/kncept/Emissary/mailer/auth"
	"github.com/kncept/Emissary/mailer/components/metadata"
	"github.com/kncept/Emissary/mailer/components/sender"
	"github.com/kncept/Emissary/mailer/components/storage"
	"github.com/kncept/Emissary/mailer/components/usermanager"
	"github.com/kncept/Emissary/mailer/email"
	"github.com/kncept/Emissary/mailer/identity"
)

type errorSender struct{ err error }

func (s errorSender) Name() string                                 { return "errorSender" }
func (s errorSender) Send(context.Context, *email.UserEmail) error { return s.err }
func (s errorSender) SendBounce(context.Context, string, string, string, string, *email.UserEmail) error {
	return nil
}

func newFidelityHandler(t *testing.T, accountID string, mailSender sender.Sender, validators ...FromValidator) *Handler {
	t.Helper()
	store := storage.NewMemoryStorage()
	addTestEmail(store, "draft1", accountID, "drafts", &email.UserEmail{
		From:    email.Address{Address: accountID},
		To:      []email.Address{{Address: "bob@example.com"}},
		Subject: "submission test",
		Text:    "body",
	})
	return NewHandler(store, metadata.NewMemoryMetadata(),
		usermanager.NewNone(usermanager.Config{AdminUsername: "admin", AdminPassword: "password"}),
		mailSender, validators...)
}

func submissionSetError(t *testing.T, handler *Handler, accountID, createID, createJSON string) map[string]any {
	t.Helper()
	ctx := auth.ContextWithClaims(context.Background(), &auth.Claims{Subject: accountID})
	args := json.RawMessage(fmt.Sprintf(`{"accountId":%q,"create":{%q:%s}}`, accountID, createID, createJSON))
	result, err := handler.EmailSubmissionSet(ctx, accountID, args)
	if err != nil {
		t.Fatalf("EmailSubmissionSet() method error = %v", err)
	}
	var response struct {
		Created    map[string]json.RawMessage `json:"created"`
		NotCreated map[string]json.RawMessage `json:"notCreated"`
	}
	if err := json.Unmarshal(result, &response); err != nil {
		t.Fatalf("unmarshal set response %s: %v", result, err)
	}
	if len(response.Created) != 0 {
		t.Fatalf("unexpected created entries: %s", result)
	}
	raw, ok := response.NotCreated[createID]
	if !ok {
		t.Fatalf("no SetError for %q: %s", createID, result)
	}
	var setError map[string]any
	if err := json.Unmarshal(raw, &setError); err != nil {
		t.Fatalf("unmarshal SetError %s: %v", raw, err)
	}
	return setError
}

func assertSetError(t *testing.T, got map[string]any, wantType string, fields map[string]any) {
	t.Helper()
	if got["type"] != wantType {
		t.Fatalf("SetError type = %v, want %q (%v)", got["type"], wantType, got)
	}
	for name, want := range fields {
		if fmt.Sprint(got[name]) != fmt.Sprint(want) {
			t.Errorf("SetError %s = %v, want %v (%v)", name, got[name], want, got)
		}
	}
}

func submitDraftWithCreate(t *testing.T, handler *Handler, accountID, createID, createJSON string) json.RawMessage {
	t.Helper()
	ctx := auth.ContextWithClaims(context.Background(), &auth.Claims{Subject: accountID})
	args := json.RawMessage(fmt.Sprintf(`{"accountId":%q,"create":{%q:%s}}`, accountID, createID, createJSON))
	result, err := handler.EmailSubmissionSet(ctx, accountID, args)
	if err != nil {
		t.Fatalf("EmailSubmissionSet(create) error = %v", err)
	}
	var response struct {
		Created map[string]json.RawMessage `json:"created"`
	}
	if err := json.Unmarshal(result, &response); err != nil {
		t.Fatalf("unmarshal create response %s: %v", result, err)
	}
	if len(response.Created) == 0 {
		t.Fatalf("no created submission: %s", result)
	}
	return result
}

func createdSubmissionID(t *testing.T, result json.RawMessage, createID string) string {
	t.Helper()
	var response struct {
		Created map[string]struct {
			ID string `json:"id"`
		} `json:"created"`
	}
	if err := json.Unmarshal(result, &response); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	id := response.Created[createID].ID
	if id == "" {
		t.Fatalf("created submission has no id: %s", result)
	}
	return id
}

func TestEmailSubmission_SetErrors_RFC8621_7_5(t *testing.T) {
	const accountID = "alice@example.com"
	validEmail := `{"identityId":"primary","emailId":"draft1"}`

	tests := []struct {
		name       string
		emailJSON  string
		envelope   string
		maxSize    uint64
		senderErr  error
		validators []FromValidator
		wantType   string
		fields     map[string]any
	}{
		{
			name:      "invalidEmail lists the bad header",
			emailJSON: `{"identityId":"primary","emailId":"invalid-email"}`,
			wantType:  "invalidEmail",
			fields:    map[string]any{"properties": "[to]"},
		},
		{
			name:      "noRecipients",
			emailJSON: `{"identityId":"primary","emailId":"no-recipients"}`,
			wantType:  "noRecipients",
		},
		{
			name:      "tooManyRecipients carries maxRecipients",
			emailJSON: `{"identityId":"primary","emailId":"many-recipients"}`,
			wantType:  "tooManyRecipients",
			fields:    map[string]any{"maxRecipients": "50"},
		},
		{
			name:      "invalidRecipients lists the bad address",
			emailJSON: validEmail,
			envelope:  `"envelope":{"mailFrom":{"email":"alice@example.com"},"rcptTo":[{"email":"bob@example.com"},{"email":"not an address"}]}`,
			wantType:  "invalidRecipients",
			fields:    map[string]any{"invalidRecipients": "[not an address]"},
		},
		{
			name:      "forbiddenMailFrom",
			emailJSON: validEmail,
			envelope:  `"envelope":{"mailFrom":{"email":"mallory@example.com"},"rcptTo":[{"email":"bob@example.com"}]}`,
			wantType:  "forbiddenMailFrom",
		},
		{
			name:      "tooLarge carries maxSize",
			emailJSON: validEmail,
			maxSize:   1,
			wantType:  "tooLarge",
			fields:    map[string]any{"maxSize": "1"},
		},
		{
			name:      "rateLimit",
			emailJSON: validEmail,
			senderErr: fmt.Errorf("SES throttled: %w", sender.ErrRateLimited),
			wantType:  "rateLimit",
		},
		{
			name:       "forbiddenFrom",
			emailJSON:  validEmail,
			validators: []FromValidator{stubFromValidator{fmt.Errorf("not verified: %w", identity.ErrNotAllowed)}},
			wantType:   "forbiddenFrom",
		},
		{
			name:       "forbiddenToSend",
			emailJSON:  validEmail,
			validators: []FromValidator{stubFromValidator{fmt.Errorf("paused: %w", identity.ErrSendForbidden)}},
			wantType:   "forbiddenToSend",
		},
		{
			name:      "invalidProperties for a missing email id",
			emailJSON: `{"identityId":"primary"}`,
			wantType:  "invalidProperties",
			fields:    map[string]any{"properties": "[emailId]"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := storage.NewMemoryStorage()
			message := &email.UserEmail{
				From:    email.Address{Address: accountID},
				To:      []email.Address{{Address: "bob@example.com"}},
				Subject: "submission test",
				Text:    "body",
			}
			emailID := "draft1"
			switch tc.name {
			case "invalidEmail lists the bad header":
				message.To = []email.Address{{Address: "not an address"}}
				emailID = "invalid-email"
			case "noRecipients":
				message.To = nil
				emailID = "no-recipients"
			case "tooManyRecipients carries maxRecipients":
				message.To = nil
				for i := 0; i < 51; i++ {
					message.To = append(message.To, email.Address{Address: fmt.Sprintf("user%d@example.com", i)})
				}
				emailID = "many-recipients"
			}
			addTestEmail(store, emailID, accountID, "drafts", message)

			mailSender := sender.NewInMemoryRecording()
			var chosenSender sender.Sender = mailSender
			if tc.senderErr != nil {
				chosenSender = errorSender{err: tc.senderErr}
			}
			handler := NewHandler(store, metadata.NewMemoryMetadata(),
				usermanager.NewNone(usermanager.Config{AdminUsername: "admin", AdminPassword: "password"}),
				chosenSender, tc.validators...)
			if tc.maxSize > 0 {
				handler.SetMaxSubmissionSize(tc.maxSize)
			}

			createJSON := tc.emailJSON
			if tc.envelope != "" {
				createJSON = strings.TrimSuffix(createJSON, "}") + "," + tc.envelope + "}"
			} else if emailID != "draft1" {
				createJSON = fmt.Sprintf(`{"identityId":"primary","emailId":%q}`, emailID)
			}
			setError := submissionSetError(t, handler, accountID, "c1", createJSON)
			assertSetError(t, setError, tc.wantType, tc.fields)
			if tc.senderErr == nil && len(mailSender.Sent) != 0 {
				t.Fatalf("message was sent despite %s: %+v", tc.wantType, mailSender.Sent)
			}
			records, _, err := handler.deps.Submissions.Load(context.Background(), accountID)
			if err != nil {
				t.Fatalf("load submissions: %v", err)
			}
			if len(records) != 0 {
				t.Fatalf("failed submission left a durable record: %+v", records)
			}
		})
	}
}

func TestEmailSubmission_CreatedProperties_RFC8621_7_5(t *testing.T) {
	const accountID = "alice@example.com"
	handler := newFidelityHandler(t, accountID, sender.NewInMemoryRecording())
	result := submitDraftWithCreate(t, handler, accountID, "c1", `{"identityId":"primary","emailId":"draft1"}`)

	var response struct {
		Created map[string]map[string]any `json:"created"`
	}
	if err := json.Unmarshal(result, &response); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	created := response.Created["c1"]
	for _, clientProperty := range []string{"identityId", "emailId"} {
		if _, ok := created[clientProperty]; ok {
			t.Errorf("created entry contains client-supplied %q: %v", clientProperty, created)
		}
	}
	for _, serverProperty := range []string{"id", "threadId", "sendAt", "undoStatus", "deliveryStatus", "envelope"} {
		if _, ok := created[serverProperty]; !ok {
			t.Errorf("created entry is missing server-set %q: %v", serverProperty, created)
		}
	}
}

func TestEmailSubmission_RetryCreationID_RFC8620_5_3(t *testing.T) {
	const accountID = "alice@example.com"
	capture := sender.NewInMemoryRecording()
	handler := newFidelityHandler(t, accountID, capture)
	create := `{"identityId":"primary","emailId":"draft1"}`

	first := submitDraftWithCreate(t, handler, accountID, "c1", create)
	second := submitDraftWithCreate(t, handler, accountID, "c1", create)
	if !strings.Contains(string(second), `"created":{"c1"`) {
		t.Fatalf("retry response = %s, want an idempotent created entry", second)
	}
	if createdSubmissionID(t, first, "c1") != createdSubmissionID(t, second, "c1") {
		t.Fatalf("retry created a second id: %s then %s", first, second)
	}
	if len(capture.Sent) != 1 {
		t.Fatalf("sent %d messages, want one (retries must not resend)", len(capture.Sent))
	}
}

func TestEmailSubmission_RetryAfterInterruptedSend_RFC8620_5_3(t *testing.T) {
	const accountID = "alice@example.com"
	ctx := auth.ContextWithClaims(context.Background(), &auth.Claims{Subject: accountID})
	capture := sender.NewInMemoryRecording()
	handler := newFidelityHandler(t, accountID, capture)
	subID := createdSubmissionID(t, submitDraftWithCreate(t, handler, accountID, "c1",
		`{"identityId":"primary","emailId":"draft1"}`), "c1")

	// Simulate a crash between the durable record write and the relay.
	records, revision, err := handler.deps.Submissions.Load(ctx, accountID)
	if err != nil {
		t.Fatalf("load submissions: %v", err)
	}
	record := records[subID]
	record.Relayed = false
	records[subID] = record
	if _, err := handler.deps.Submissions.Save(ctx, accountID, records, revision); err != nil {
		t.Fatalf("save interrupted submission: %v", err)
	}

	submitDraftWithCreate(t, handler, accountID, "c1", `{"identityId":"primary","emailId":"draft1"}`)
	if len(capture.Sent) != 2 {
		t.Fatalf("sent %d messages, want a resend after the interrupted attempt", len(capture.Sent))
	}
	records, _, err = handler.deps.Submissions.Load(ctx, accountID)
	if err != nil {
		t.Fatalf("reload submissions: %v", err)
	}
	if !records[subID].Relayed {
		t.Fatalf("retried submission was not marked relayed: %+v", records[subID])
	}
}

func TestEmailSubmission_FullObjectPatch_RFC8620_5_3(t *testing.T) {
	const accountID = "alice@example.com"
	handler := newFidelityHandler(t, accountID, sender.NewInMemoryRecording())
	ctx := auth.ContextWithClaims(context.Background(), &auth.Claims{Subject: accountID})
	subID := createdSubmissionID(t, submitDraftWithCreate(t, handler, accountID, "c1",
		`{"identityId":"primary","emailId":"draft1"}`), "c1")

	getRaw, err := handler.EmailSubmissionGet(ctx, accountID, json.RawMessage(fmt.Sprintf(
		`{"accountId":%q,"ids":[%q]}`, accountID, subID)))
	if err != nil {
		t.Fatalf("EmailSubmissionGet() error = %v", err)
	}
	var listed struct {
		List []json.RawMessage `json:"list"`
	}
	if err := json.Unmarshal(getRaw, &listed); err != nil || len(listed.List) != 1 {
		t.Fatalf("submission get = %s (%v)", getRaw, err)
	}

	for name, patch := range map[string]string{
		"whole object": string(listed.List[0]),
		"empty patch":  `{}`,
	} {
		t.Run(name, func(t *testing.T) {
			args := json.RawMessage(fmt.Sprintf(`{"accountId":%q,"update":{%q:%s}}`, accountID, subID, patch))
			result, err := handler.EmailSubmissionSet(ctx, accountID, args)
			if err != nil {
				t.Fatalf("EmailSubmissionSet(update) error = %v", err)
			}
			var response struct {
				Updated   map[string]json.RawMessage `json:"updated"`
				NotUpdate map[string]json.RawMessage `json:"notUpdated"`
			}
			if err := json.Unmarshal(result, &response); err != nil {
				t.Fatalf("unmarshal update response %s: %v", result, err)
			}
			if _, ok := response.Updated[subID]; !ok || len(response.NotUpdate) != 0 {
				t.Fatalf("update response = %s, want %q updated with no errors", result, subID)
			}
		})
	}
}

func TestEmailSubmission_CannotUnsend_RFC8621_7_5(t *testing.T) {
	const accountID = "alice@example.com"
	handler := newFidelityHandler(t, accountID, sender.NewInMemoryRecording())
	ctx := auth.ContextWithClaims(context.Background(), &auth.Claims{Subject: accountID})
	subID := createdSubmissionID(t, submitDraftWithCreate(t, handler, accountID, "c1",
		`{"identityId":"primary","emailId":"draft1"}`), "c1")

	args := json.RawMessage(fmt.Sprintf(
		`{"accountId":%q,"update":{%q:{"undoStatus":"canceled"}}}`, accountID, subID))
	result, err := handler.EmailSubmissionSet(ctx, accountID, args)
	if err != nil {
		t.Fatalf("EmailSubmissionSet(update) error = %v", err)
	}
	var response struct {
		NotUpdated map[string]struct {
			Type string `json:"type"`
		} `json:"notUpdated"`
	}
	if err := json.Unmarshal(result, &response); err != nil {
		t.Fatalf("unmarshal update response: %v", err)
	}
	if response.NotUpdated[subID].Type != "cannotUnsend" {
		t.Fatalf("cancel result = %s, want cannotUnsend", result)
	}
}

func TestEmailSubmission_InvalidPatch_RFC8620_5_3(t *testing.T) {
	const accountID = "alice@example.com"
	handler := newFidelityHandler(t, accountID, sender.NewInMemoryRecording())
	ctx := auth.ContextWithClaims(context.Background(), &auth.Claims{Subject: accountID})
	subID := createdSubmissionID(t, submitDraftWithCreate(t, handler, accountID, "c1",
		`{"identityId":"primary","emailId":"draft1"}`), "c1")

	args := json.RawMessage(fmt.Sprintf(
		`{"accountId":%q,"update":{%q:{"undoStatus/name":"canceled"}}}`, accountID, subID))
	_, err := handler.EmailSubmissionSet(ctx, accountID, args)
	if err == nil || !strings.HasPrefix(err.Error(), "invalidPatch") {
		t.Fatalf("invalid patch error = %v, want invalidPatch", err)
	}
}

func TestEmailSubmission_ExplicitEnvelope_RFC8621_7(t *testing.T) {
	const accountID = "alice@example.com"
	capture := sender.NewInMemoryRecording()
	handler := newFidelityHandler(t, accountID, capture)
	ctx := auth.ContextWithClaims(context.Background(), &auth.Claims{Subject: accountID})
	create := `{"identityId":"primary","emailId":"draft1","envelope":{
		"mailFrom":{"email":"alice@example.com","parameters":{"NOTIFY":"SUCCESS"}},
		"rcptTo":[
			{"email":"bob@example.com","parameters":null},
			{"email":"smtp-only@example.com","parameters":{"ORCPT":"rfc822;smtp-only@example.com"}}
		]}}`
	subID := createdSubmissionID(t, submitDraftWithCreate(t, handler, accountID, "c1", create), "c1")

	if len(capture.Sent) != 1 {
		t.Fatalf("sent %d messages, want one", len(capture.Sent))
	}
	sent := capture.Sent[0]
	if len(sent.To) != 1 || sent.To[0].Address != "bob@example.com" {
		t.Fatalf("headers were changed by the envelope: To = %+v", sent.To)
	}
	if sent.SubmissionEnvelope == nil || len(sent.SubmissionEnvelope.RcptTo) != 2 {
		t.Fatalf("sender envelope = %+v, want both rcptTo addresses", sent.SubmissionEnvelope)
	}
	if got := sent.SubmissionEnvelope.RcptTo[1].Email; got != "smtp-only@example.com" {
		t.Errorf("smtp-only recipient = %q", got)
	}
	if got := sent.SubmissionEnvelope.RcptTo[1].Parameters["ORCPT"]; got == nil || *got != "rfc822;smtp-only@example.com" {
		t.Errorf("ORCPT parameter = %v", got)
	}

	getRaw, err := handler.EmailSubmissionGet(ctx, accountID, json.RawMessage(fmt.Sprintf(
		`{"accountId":%q,"ids":[%q]}`, accountID, subID)))
	if err != nil {
		t.Fatalf("EmailSubmissionGet() error = %v", err)
	}
	var response struct {
		List []struct {
			Envelope struct {
				MailFrom struct {
					Parameters map[string]*string `json:"parameters"`
				} `json:"mailFrom"`
				RcptTo []struct {
					Email      string             `json:"email"`
					Parameters map[string]*string `json:"parameters"`
				} `json:"rcptTo"`
			} `json:"envelope"`
			DeliveryStatus map[string]struct {
				SMTPReply string `json:"smtpReply"`
				Delivered string `json:"delivered"`
			} `json:"deliveryStatus"`
		} `json:"list"`
	}
	if err := json.Unmarshal(getRaw, &response); err != nil || len(response.List) != 1 {
		t.Fatalf("submission get = %s (%v)", getRaw, err)
	}
	stored := response.List[0]
	if got := stored.Envelope.MailFrom.Parameters["NOTIFY"]; got == nil || *got != "SUCCESS" {
		t.Errorf("stored MAIL FROM parameter = %v", got)
	}
	if len(stored.Envelope.RcptTo) != 2 || stored.Envelope.RcptTo[1].Email != "smtp-only@example.com" {
		t.Fatalf("stored envelope = %+v", stored.Envelope)
	}
	if got := stored.Envelope.RcptTo[1].Parameters["ORCPT"]; got == nil || *got != "rfc822;smtp-only@example.com" {
		t.Errorf("stored ORCPT = %v", got)
	}
	syntheticReply := regexp.MustCompile(`^[0-9]{3} [0-9]\.[0-9]\.[0-9] .+`)
	for address, status := range stored.DeliveryStatus {
		if !syntheticReply.MatchString(status.SMTPReply) {
			t.Errorf("smtpReply for %s = %q, want RFC 8621 synthetic form", address, status.SMTPReply)
		}
		if status.Delivered != "queued" && status.Delivered != "yes" && status.Delivered != "no" && status.Delivered != "unknown" {
			t.Errorf("delivered for %s = %q", address, status.Delivered)
		}
	}
	if _, ok := stored.DeliveryStatus["smtp-only@example.com"]; !ok {
		t.Fatalf("smtp-only recipient missing from deliveryStatus: %+v", stored.DeliveryStatus)
	}
}

func TestEmailSubmission_IdentityAuthorization_RFC8621_7_5(t *testing.T) {
	const accountID = "alice@example.com"
	store := storage.NewMemoryStorage()
	capture := sender.NewInMemoryRecording()
	h := newMultiIdentityHandler(store, capture, accountID, "alias@example.com")
	ctx := auth.ContextWithClaims(context.Background(), &auth.Claims{Subject: accountID})
	addTestEmail(store, "draft1", accountID, "drafts", &email.UserEmail{
		From:    email.Address{Address: accountID},
		To:      []email.Address{{Address: "bob@example.com"}},
		Subject: "identity test",
		Text:    "body",
	})

	createdRaw, err := h.IdentitySet(ctx, accountID, json.RawMessage(fmt.Sprintf(
		`{"accountId":%q,"create":{"alias":{"email":"alias@example.com","name":"Alias"}}}`, accountID)))
	if err != nil {
		t.Fatalf("IdentitySet(create) error = %v", err)
	}
	var identities struct {
		Created map[string]struct {
			ID string `json:"id"`
		} `json:"created"`
	}
	if err := json.Unmarshal(createdRaw, &identities); err != nil {
		t.Fatalf("unmarshal identities: %v", err)
	}
	aliasID := identities.Created["alias"].ID
	if aliasID == "" {
		t.Fatalf("no alias id in %s", createdRaw)
	}

	submitDraftWithCreate(t, h, accountID, "alias-submit", fmt.Sprintf(
		`{"identityId":%q,"emailId":"draft1"}`, aliasID))
	if len(capture.Sent) != 1 || capture.Sent[0].From.Address != "alias@example.com" {
		t.Fatalf("sent with the wrong From: %+v", capture.Sent)
	}

	setError := submissionSetError(t, h, accountID, "unknown",
		`{"identityId":"missing-identity","emailId":"draft1"}`)
	assertSetError(t, setError, "invalidProperties", map[string]any{"properties": "[identityId]"})
}

func TestEmailSubmission_ImplicitEmailSet_RFC8621_7_5(t *testing.T) {
	const accountID = "alice@example.com"
	handler, _ := newSubmissionHandler(t, accountID)
	dispatcher := NewDispatcher(handler, nil)
	request := &Request{
		Using: allUsing,
		MethodCalls: []Invocation{{
			Name:     MethodEmailSubmissionSet,
			ClientID: "sub1",
			Arguments: map[string]json.RawMessage{
				"accountId": json.RawMessage(fmt.Sprintf(`%q`, accountID)),
				"create": json.RawMessage(
					`{"c1":{"identityId":"primary","emailId":"draft1"}}`),
				"onSuccessUpdateEmail": json.RawMessage(
					`{"#c1":{"keywords/$sent":true}}`),
			},
		}},
	}
	response, err := dispatcher.DispatchRequest(context.Background(), accountID,
		&auth.Claims{Subject: accountID}, request)
	if err != nil {
		t.Fatalf("DispatchRequest() error = %v", err)
	}
	if len(response.MethodResponses) != 2 {
		t.Fatalf("methodResponses = %+v, want submission and implicit Email/set", response.MethodResponses)
	}
	implicit := response.MethodResponses[1]
	if implicit.Name != MethodEmailSet || implicit.ClientID != "sub1" {
		t.Fatalf("implicit response = %s %q, want %s with call id sub1", implicit.Name, implicit.ClientID, MethodEmailSet)
	}
	// The #c1 creation reference is resolved to the Email id of the submitted
	// draft before the implicit Email/set is issued. Its response therefore
	// reports draft1 as updated, not the literal "#c1" or submission id.
	var updated map[string]json.RawMessage
	if err := json.Unmarshal(implicit.Arguments["updated"], &updated); err != nil {
		t.Fatalf("unmarshal implicit Email/set updated: %v", err)
	}
	if _, ok := updated["draft1"]; !ok {
		t.Fatalf("implicit Email/set updated = %v, want the #c1 reference mapped to draft1", updated)
	}
}
