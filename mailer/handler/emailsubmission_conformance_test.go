package handler

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/kncept/Emissary/mailer/auth"
)

// These tests exercise EmailSubmission through the real HTTP JMAP endpoint.
// They complement the package-level fidelity tests by proving the envelope,
// result-reference and error shapes survive dispatch and auth unchanged.

func submissionAccountID(t *testing.T) string {
	t.Helper()
	return auth.AccountIDForEmail("bob@example.com")
}

func jmapCreatedSubmissionID(t *testing.T, result map[string]interface{}, creationID string) string {
	t.Helper()
	created, _ := result["created"].(map[string]interface{})
	entry, ok := created[creationID].(map[string]interface{})
	if !ok {
		t.Fatalf("created[%q] missing from %v", creationID, result)
	}
	id, _ := entry["id"].(string)
	if id == "" {
		t.Fatalf("created[%q] has no id: %v", creationID, entry)
	}
	return id
}

func jmapSubmissionSetResult(t *testing.T, h http.Handler, token, accountID string, args map[string]interface{}) (map[string]interface{}, map[string]interface{}) {
	t.Helper()
	resp := jmapRequest(t, h, token,
		[]string{"urn:ietf:params:jmap:mail", "urn:ietf:params:jmap:submission"},
		[]JmapMethodCall{{"EmailSubmission/set", args, "sub1"}})
	responses, _ := resp["methodResponses"].([]interface{})
	if len(responses) != 1 {
		t.Fatalf("methodResponses = %v, want one response", responses)
	}
	call, _ := responses[0].([]interface{})
	name, _ := call[0].(string)
	result, _ := call[1].(map[string]interface{})
	return result, map[string]interface{}{"name": name}
}

// TestEmailSubmissionSetErrorsRFC8621_7_5 checks the RFC 8621 section 7.5
// SetErrors over HTTP. Each case creates a fresh handler so failed attempts
// cannot reuse another case's submission id.
func TestEmailSubmissionSetErrorsRFC8621_7_5(t *testing.T) {
	tests := []struct {
		name       string
		create     map[string]interface{}
		wantType   string
		wantFields map[string]any
	}{
		{
			name: "missing email id is invalidProperties",
			create: map[string]interface{}{
				"identityId": "primary",
				"emailId":    "",
			},
			wantType: "invalidProperties",
			wantFields: map[string]any{
				"properties": []any{"emailId"},
			},
		},
		{
			name: "mailFrom outside the identity is forbidden",
			create: map[string]interface{}{
				"identityId": "primary",
				"envelope": map[string]interface{}{
					"mailFrom": map[string]string{"email": "mallory@example.com"},
					"rcptTo":   []map[string]string{{"email": "dave@example.com"}},
				},
			},
			wantType: "forbiddenMailFrom",
		},
		{
			name: "malformed envelope address is invalidRecipients",
			create: map[string]interface{}{
				"identityId": "primary",
				"envelope": map[string]interface{}{
					"mailFrom": map[string]string{"email": ""},
					"rcptTo":   []map[string]string{{"email": "not an address"}},
				},
			},
			wantType: "invalidRecipients",
			wantFields: map[string]any{
				"invalidRecipients": []any{"not an address"},
			},
		},
		{
			name: "too many recipients carries maxRecipients",
			create: map[string]interface{}{
				"identityId": "primary",
				"envelope": func() map[string]interface{} {
					recipients := []map[string]string{{"email": "dave@example.com"}}
					for i := 0; i < 50; i++ {
						recipients = append(recipients, map[string]string{
							"email": fmt.Sprintf("extra%d@example.com", i),
						})
					}
					return map[string]interface{}{
						"mailFrom": map[string]string{"email": "bob@example.com"},
						"rcptTo":   recipients,
					}
				}(),
			},
			wantType: "tooManyRecipients",
			wantFields: map[string]any{
				"maxRecipients": float64(50),
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h, _, capture := newHandlerForTest(t, "secret")
			accountID := submissionAccountID(t)
			_, token, _ := login(t, h, "bob@example.com", "anything")
			emailID := createDraftForSend(t, h, token, accountID, "bob@example.com", "dave@example.com")

			create := map[string]interface{}{"emailId": emailID}
			for name, value := range tc.create {
				create[name] = value
			}
			result, _ := jmapSubmissionSetResult(t, h, token, accountID, map[string]interface{}{
				"accountId": accountID,
				"create":    map[string]interface{}{"c1": create},
			})
			if result["created"] != nil {
				t.Fatalf("unexpected created entries: %v", result)
			}
			notCreated, _ := result["notCreated"].(map[string]interface{})
			entry, ok := notCreated["c1"].(map[string]interface{})
			if !ok {
				t.Fatalf("no SetError for c1: %v", result)
			}
			if entry["type"] != tc.wantType {
				t.Fatalf("SetError type = %v, want %q: %v", entry["type"], tc.wantType, entry)
			}
			for field, want := range tc.wantFields {
				if fmt.Sprint(entry[field]) != fmt.Sprint(want) {
					t.Errorf("SetError %s = %v, want %v", field, entry[field], want)
				}
			}
			if len(capture.Sent) != 0 {
				t.Fatalf("message was sent despite %s: %+v", tc.wantType, capture.Sent)
			}
			// RFC 8620 section 5.3: empty /set maps are null, destroyed is [].
			for _, field := range []string{"created", "updated", "notUpdated", "notDestroyed"} {
				if result[field] != nil {
					t.Errorf("%s = %v, want null", field, result[field])
				}
			}
			if notCreated == nil {
				t.Error("notCreated = null, want the SetError entry")
			}
			if destroyed, ok := result["destroyed"].([]interface{}); !ok || len(destroyed) != 0 {
				t.Errorf("destroyed = %v, want an empty array", result["destroyed"])
			}
		})
	}
}

// TestEmailSubmissionSendAtRejectedRFC8621_7_5 proves the advertised
// maxDelayedSend=0 profile reports sendAt as a method-level invalidArguments
// error with a description, rather than silently ignoring it.
func TestEmailSubmissionSendAtRejectedRFC8621_7_5(t *testing.T) {
	h, _, capture := newHandlerForTest(t, "secret")
	accountID := submissionAccountID(t)
	_, token, _ := login(t, h, "bob@example.com", "anything")
	emailID := createDraftForSend(t, h, token, accountID, "bob@example.com", "dave@example.com")

	resp := jmapRequest(t, h, token,
		[]string{"urn:ietf:params:jmap:mail", "urn:ietf:params:jmap:submission"},
		[]JmapMethodCall{{"EmailSubmission/set", map[string]interface{}{
			"accountId": accountID,
			"create": map[string]interface{}{"c1": map[string]interface{}{
				"identityId": "primary",
				"emailId":    emailID,
				"sendAt":     "2030-01-01T00:00:00Z",
			}},
		}, "sub1"}})
	result := jmapMethodResult(t, resp, "error")
	if jmapErrorType(t, result) != "invalidArguments" {
		t.Fatalf("error = %v, want invalidArguments", result)
	}
	description, _ := result["description"].(string)
	if !strings.Contains(description, "delayed submission") {
		t.Errorf("description = %q, want delayed submission detail", description)
	}
	if len(capture.Sent) != 0 {
		t.Fatalf("sendAt rejection still sent: %+v", capture.Sent)
	}
}

// TestEmailSubmissionEnvelopeRFC8621_7 verifies that an explicit envelope is
// authoritative: parameters survive into storage, SMTP-only rcptTo addresses
// are handed to the sender, and headers remain unchanged. An empty mailFrom is
// accepted as the null reverse path for this profile.
func TestEmailSubmissionEnvelopeRFC8621_7(t *testing.T) {
	h, _, capture := newHandlerForTest(t, "secret")
	accountID := submissionAccountID(t)
	_, token, _ := login(t, h, "bob@example.com", "anything")
	emailID := createDraftForSend(t, h, token, accountID, "bob@example.com", "dave@example.com")

	result, _ := jmapSubmissionSetResult(t, h, token, accountID, map[string]interface{}{
		"accountId": accountID,
		"create": map[string]interface{}{"c1": map[string]interface{}{
			"identityId": "primary",
			"emailId":    emailID,
			"envelope": map[string]interface{}{
				"mailFrom": map[string]interface{}{
					"email":      "",
					"parameters": map[string]string{"NOTIFY": "SUCCESS"},
				},
				"rcptTo": []map[string]interface{}{
					{"email": "dave@example.com", "parameters": nil},
					{"email": "smtp-only@example.com", "parameters": map[string]string{
						"ORCPT": "rfc822;smtp-only@example.com",
					}},
				},
			},
		}},
	})
	subID := jmapCreatedSubmissionID(t, result, "c1")

	if len(capture.Sent) != 1 {
		t.Fatalf("sent %d messages, want one", len(capture.Sent))
	}
	sent := capture.Sent[0]
	if len(sent.To) != 1 || sent.To[0].Address != "dave@example.com" {
		t.Errorf("To header = %+v, want only dave@example.com", sent.To)
	}
	envelope := sent.SubmissionEnvelope
	if envelope == nil || len(envelope.RcptTo) != 2 {
		t.Fatalf("sender envelope = %+v, want two recipients", envelope)
	}
	if envelope.RcptTo[1].Email != "smtp-only@example.com" {
		t.Errorf("second recipient = %q", envelope.RcptTo[1].Email)
	}
	if value := envelope.RcptTo[1].Parameters["ORCPT"]; value == nil || *value != "rfc822;smtp-only@example.com" {
		t.Errorf("ORCPT parameter = %v", value)
	}
	if value := envelope.MailFrom.Parameters["NOTIFY"]; value == nil || *value != "SUCCESS" {
		t.Errorf("MAIL FROM parameter = %v", value)
	}

	getResp := jmapRequest(t, h, token, []string{"urn:ietf:params:jmap:submission"},
		[]JmapMethodCall{{"EmailSubmission/get", map[string]interface{}{
			"accountId": accountID,
			"ids":       []string{subID},
		}, "get1"}})
	getResult := jmapMethodResult(t, getResp, "EmailSubmission/get")
	list, _ := getResult["list"].([]interface{})
	if len(list) != 1 {
		t.Fatalf("EmailSubmission/get list = %v", getResult)
	}
	stored, _ := list[0].(map[string]interface{})
	storedEnvelope, _ := stored["envelope"].(map[string]interface{})
	mailFrom, _ := storedEnvelope["mailFrom"].(map[string]interface{})
	if mailFrom["email"] != "" {
		t.Errorf("stored mailFrom = %v, want the null reverse path", mailFrom["email"])
	}
	parameters, _ := mailFrom["parameters"].(map[string]interface{})
	if parameters["NOTIFY"] != "SUCCESS" {
		t.Errorf("stored MAIL FROM parameters = %v", parameters)
	}
	rcptTo, _ := storedEnvelope["rcptTo"].([]interface{})
	if len(rcptTo) != 2 {
		t.Fatalf("stored rcptTo = %v, want two recipients", rcptTo)
	}
	delivery, _ := stored["deliveryStatus"].(map[string]interface{})
	if _, ok := delivery["smtp-only@example.com"]; !ok {
		t.Errorf("deliveryStatus = %v, want an entry for the SMTP-only recipient", delivery)
	}
}

// TestEmailSubmissionPatchRFC8620_5_3 checks full-object patches, impossible
// undos and invalid patch pointers through the HTTP endpoint.
func TestEmailSubmissionPatchRFC8620_5_3(t *testing.T) {
	h, _, _ := newHandlerForTest(t, "secret")
	accountID := submissionAccountID(t)
	_, token, _ := login(t, h, "bob@example.com", "anything")
	emailID := createDraftForSend(t, h, token, accountID, "bob@example.com", "dave@example.com")

	createResult, _ := jmapSubmissionSetResult(t, h, token, accountID, map[string]interface{}{
		"accountId": accountID,
		"create": map[string]interface{}{"c1": map[string]interface{}{
			"identityId": "primary",
			"emailId":    emailID,
		}},
	})
	subID := jmapCreatedSubmissionID(t, createResult, "c1")

	getResp := jmapRequest(t, h, token, []string{"urn:ietf:params:jmap:submission"},
		[]JmapMethodCall{{"EmailSubmission/get", map[string]interface{}{
			"accountId": accountID,
			"ids":       []string{subID},
		}, "get1"}})
	list, _ := jmapMethodResult(t, getResp, "EmailSubmission/get")["list"].([]interface{})
	if len(list) != 1 {
		t.Fatalf("EmailSubmission/get list = %v", list)
	}
	fullObject, _ := list[0].(map[string]interface{})

	result, _ := jmapSubmissionSetResult(t, h, token, accountID, map[string]interface{}{
		"accountId": accountID,
		"update":    map[string]interface{}{subID: fullObject},
	})
	if result["updated"] == nil || result["notUpdated"] != nil {
		t.Fatalf("full-object patch result = %v, want a successful no-op update", result)
	}

	result, _ = jmapSubmissionSetResult(t, h, token, accountID, map[string]interface{}{
		"accountId": accountID,
		"update":    map[string]interface{}{subID: map[string]interface{}{"undoStatus": "canceled"}},
	})
	notUpdated, _ := result["notUpdated"].(map[string]interface{})
	cancel, _ := notUpdated[subID].(map[string]interface{})
	if cancel["type"] != "cannotUnsend" {
		t.Fatalf("cancel result = %v, want cannotUnsend", result)
	}

	resp := jmapRequest(t, h, token, []string{"urn:ietf:params:jmap:submission"},
		[]JmapMethodCall{{"EmailSubmission/set", map[string]interface{}{
			"accountId": accountID,
			"update": map[string]interface{}{subID: map[string]interface{}{
				"undoStatus/name": "canceled",
			}},
		}, "sub2"}})
	if got := jmapErrorType(t, jmapMethodResult(t, resp, "error")); got != "invalidPatch" {
		t.Errorf("invalid patch error type = %q, want invalidPatch", got)
	}
}

// TestEmailSubmissionImplicitEmailSetRFC8621_7_5 proves the second invocation
// is emitted with the caller's method id and the #creationId resolves to the
// submitted Email id.
func TestEmailSubmissionImplicitEmailSetRFC8621_7_5(t *testing.T) {
	h, _, capture := newHandlerForTest(t, "secret")
	accountID := submissionAccountID(t)
	_, token, _ := login(t, h, "bob@example.com", "anything")
	emailID := createDraftForSend(t, h, token, accountID, "bob@example.com", "dave@example.com")

	resp := jmapRequest(t, h, token,
		[]string{"urn:ietf:params:jmap:mail", "urn:ietf:params:jmap:submission"},
		[]JmapMethodCall{{"EmailSubmission/set", map[string]interface{}{
			"accountId": accountID,
			"create": map[string]interface{}{"c1": map[string]interface{}{
				"identityId": "primary",
				"emailId":    emailID,
			}},
			"onSuccessUpdateEmail": map[string]interface{}{
				"#c1": map[string]interface{}{"keywords/$sent": true},
			},
		}, "sub1"}})
	responses, _ := resp["methodResponses"].([]interface{})
	if len(responses) != 2 {
		t.Fatalf("methodResponses = %v, want submission and implicit Email/set", responses)
	}
	implicit, _ := responses[1].([]interface{})
	if implicit[0] != "Email/set" || implicit[2] != "sub1" {
		t.Fatalf("implicit response = %v, want Email/set with call id sub1", implicit)
	}
	args, _ := implicit[1].(map[string]interface{})
	updated, _ := args["updated"].(map[string]interface{})
	if _, ok := updated[emailID]; !ok {
		t.Errorf("implicit updated = %v, want draft %q", updated, emailID)
	}
	if len(capture.Sent) != 1 {
		t.Fatalf("sent %d messages, want one", len(capture.Sent))
	}
}
