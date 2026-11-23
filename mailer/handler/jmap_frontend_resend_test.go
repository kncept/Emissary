package handler

import (
	"net/http"
	"testing"
)

// TestJMAPFrontendResendReusesCreationIDs checks that a second send in the same
// session, which reuses the frontend's fixed creation ids, still gets an
// implicit Email/set that updates the newly created draft. The session's
// creation-id map must not resolve "#submission1" to the first submission.
func TestJMAPFrontendResendReusesCreationIDs(t *testing.T) {
	h, _, capture := newHandlerForTest(t, "secret")
	status, token, _ := login(t, h, "bob@example.com", "anything")
	if status != http.StatusOK {
		t.Fatalf("login status = %d", status)
	}
	session := fetchJMAPSession(t, h, token)
	accountID := session.PrimaryAccounts[frontendMailCap]

	// Discover the drafts mailbox as the frontend does.
	resp := jmapRequest(t, h, token, []string{frontendMailCap}, []JmapMethodCall{
		{"Mailbox/query", map[string]interface{}{
			"accountId": accountID,
			"filter":    map[string]interface{}{"role": "drafts"},
		}, "mailboxQuery"},
		{"Mailbox/get", map[string]interface{}{
			"accountId": accountID,
			"#ids": map[string]interface{}{
				"resultOf": "mailboxQuery",
				"name":     "Mailbox/query",
				"path":     "/ids/*",
			},
			"properties": []string{"id", "name", "role"},
		}, "mailboxGet"},
	})
	mailboxes, _ := frontendRequireInvocation(t, resp, "Mailbox/get")["list"].([]interface{})
	draftsID := ""
	for _, raw := range mailboxes {
		mailbox, _ := raw.(map[string]interface{})
		if mailbox["role"] == "drafts" {
			draftsID, _ = mailbox["id"].(string)
		}
	}
	if draftsID == "" {
		t.Fatal("no drafts mailbox discovered")
	}

	resp = jmapRequest(t, h, token, []string{frontendSubmissionCap}, []JmapMethodCall{
		{"Identity/get", map[string]interface{}{"accountId": accountID}, "identityGet"},
	})
	identities, _ := frontendRequireInvocation(t, resp, "Identity/get")["list"].([]interface{})
	if len(identities) == 0 {
		t.Fatal("no sending identity discovered")
	}
	identityID, _ := frontendMap(t, identities[0], "identity")["id"].(string)

	send := func(subject string) (string, []map[string]interface{}) {
		t.Helper()
		resp := jmapRequest(t, h, token, []string{frontendMailCap, frontendSubmissionCap}, []JmapMethodCall{
			{"Email/set", map[string]interface{}{
				"accountId": accountID,
				"create": map[string]interface{}{
					"create1": map[string]interface{}{
						"mailboxIds": map[string]bool{draftsID: true},
						"keywords":   map[string]bool{"$draft": true},
						"to":         []map[string]interface{}{{"email": "carol@example.com", "name": ""}},
						"subject":    subject,
						"textBody":   []map[string]interface{}{{"partId": "text1", "type": "text/plain"}},
						"bodyValues": map[string]interface{}{
							"text1": map[string]interface{}{"value": "Hello", "isEncodingProblem": false, "isTruncated": false},
						},
					},
				},
			}, "emailCreate"},
			{"EmailSubmission/set", map[string]interface{}{
				"accountId": accountID,
				"create": map[string]interface{}{
					"submission1": map[string]interface{}{
						"identityId": identityID,
						"emailId":    "#create1",
						"envelope": map[string]interface{}{
							"mailFrom": map[string]interface{}{"email": ""},
							"rcptTo":   []map[string]interface{}{{"email": "carol@example.com"}},
						},
					},
				},
				"onSuccessUpdateEmail": map[string]interface{}{
					"#submission1": map[string]interface{}{"keywords/$draft": nil},
				},
			}, "submissionCreate"},
		}, map[string]string{})

		frontendNoSetErrors(t, frontendRequireInvocation(t, resp, "EmailSubmission/set"))
		createdIDs := frontendMap(t, resp["createdIds"], "createdIds")
		draftID, _ := createdIDs["create1"].(string)
		if draftID == "" {
			t.Fatalf("createdIds = %v, want create1 mapped to a real id", createdIDs)
		}
		return draftID, frontendInvocations(t, resp, "Email/set")
	}

	firstDraft, firstEmailSets := send("First")
	if len(firstEmailSets) != 2 {
		t.Fatalf("first send Email/set responses = %d, want 2", len(firstEmailSets))
	}
	if _, ok := frontendMap(t, firstEmailSets[1]["updated"], "implicit Email/set updated")[firstDraft]; !ok {
		t.Fatalf("first implicit Email/set updated = %v, want %q", firstEmailSets[1]["updated"], firstDraft)
	}

	secondDraft, secondEmailSets := send("Second")
	if secondDraft == firstDraft {
		t.Fatalf("second send reused draft id %q", secondDraft)
	}
	if len(secondEmailSets) != 2 {
		t.Fatalf("second send Email/set responses = %d, want 2", len(secondEmailSets))
	}
	updated := frontendMap(t, secondEmailSets[1]["updated"], "second implicit Email/set updated")
	if _, ok := updated[secondDraft]; !ok {
		t.Fatalf("second implicit Email/set updated = %v, want %q", updated, secondDraft)
	}
	if len(capture.Sent) != 2 {
		t.Fatalf("sent %d messages, want 2", len(capture.Sent))
	}
}
