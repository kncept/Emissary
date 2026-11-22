package handler

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/kncept/Emissary/mailer/components/storage"
	"github.com/kncept/Emissary/mailer/email"
)

const (
	frontendMailCap       = "urn:ietf:params:jmap:mail"
	frontendSubmissionCap = "urn:ietf:params:jmap:submission"
	frontendVacationCap   = "urn:ietf:params:jmap:vacationresponse"
)

// frontendInvocations returns every method invocation with the requested name.
// The send flow legitimately contains two Email/set responses: the explicit
// create and the implicit update produced by onSuccessUpdateEmail.
func frontendInvocations(t *testing.T, resp map[string]interface{}, name string) []map[string]interface{} {
	t.Helper()
	var found []map[string]interface{}
	responses, _ := resp["methodResponses"].([]interface{})
	for _, raw := range responses {
		call, ok := raw.([]interface{})
		if !ok || len(call) < 2 || call[0] != name {
			continue
		}
		result, _ := call[1].(map[string]interface{})
		found = append(found, result)
	}
	return found
}

func frontendRequireInvocation(t *testing.T, resp map[string]interface{}, name string) map[string]interface{} {
	t.Helper()
	found := frontendInvocations(t, resp, name)
	if len(found) == 0 {
		t.Fatalf("no %q response in %v", name, resp)
	}
	return found[0]
}

func frontendNoSetErrors(t *testing.T, result map[string]interface{}) {
	t.Helper()
	for _, field := range []string{"notCreated", "notUpdated", "notDestroyed"} {
		if problems, _ := result[field].(map[string]interface{}); len(problems) != 0 {
			t.Fatalf("%s = %v, want empty", field, problems)
		}
	}
}

func frontendMap(t *testing.T, value interface{}, field string) map[string]interface{} {
	t.Helper()
	result, ok := value.(map[string]interface{})
	if !ok {
		t.Fatalf("%s = %v, want an object", field, value)
	}
	return result
}

// TestJMAPFrontendUseCase_EndToEndRFC8621_1_3 drives the complete narrow-client
// workflow over HTTP: login, Session discovery, role mailbox discovery, mailbox
// listing, marking a message seen, draft creation + submission with creation
// references, the implicit Email/set, VacationResponse toggling, and raw MIME
// retrieval through blobId + downloadUrl (RESOLVED-4).
func TestJMAPFrontendUseCase_EndToEndRFC8621_1_3(t *testing.T) {
	h, store, capture := newHandlerForTest(t, "secret")
	ctx := context.Background()
	const raw = "From: Sender <sender@example.com>\r\n" +
		"To: bob@example.com\r\n" +
		"Subject: Frontend use case\r\n" +
		"Date: Mon, 02 Jan 2006 15:04:05 -0700\r\n" +
		"Message-ID: <frontend-use-case@example.com>\r\n" +
		"\r\n" +
		"Hello from the end-to-end fixture."
	emailID, err := store.AddEmail(ctx, "bob@example.com", "inbox", &email.UserEmail{
		From:      email.Address{Address: "sender@example.com", Name: "Sender"},
		To:        []email.Address{{Address: "bob@example.com"}},
		Subject:   "Frontend use case",
		Date:      time.Date(2006, 1, 2, 22, 4, 5, 0, time.UTC),
		MessageID: "<frontend-use-case@example.com>",
		Text:      "Hello from the end-to-end fixture.",
	}, []byte(raw))
	if err != nil {
		t.Fatalf("AddEmail() error = %v", err)
	}
	// Delivery performs this registration; fixtures must do the same so the
	// Email.blobId exposed by Email/get resolves through downloadUrl.
	if _, err := storage.RegisterEmailBlobs(ctx, store, "bob@example.com", []byte(raw)); err != nil {
		t.Fatalf("RegisterEmailBlobs() error = %v", err)
	}

	// Login -> Session.
	status, token, loginBody := login(t, h, "bob@example.com", "anything")
	if status != http.StatusOK {
		t.Fatalf("login status = %d, body = %v", status, loginBody)
	}
	session := fetchJMAPSession(t, h, token)
	accountID := session.PrimaryAccounts[frontendMailCap]
	if accountID == "" || accountID == "bob@example.com" {
		t.Fatalf("Session primaryAccounts[%q] = %q, want an opaque account id", frontendMailCap, accountID)
	}
	if got := session.PrimaryAccounts[frontendSubmissionCap]; got != accountID {
		t.Fatalf("Session primaryAccounts[%q] = %q, want %q", frontendSubmissionCap, got, accountID)
	}
	if got := session.PrimaryAccounts[frontendVacationCap]; got != accountID {
		t.Fatalf("Session primaryAccounts[%q] = %q, want %q", frontendVacationCap, got, accountID)
	}

	// Discover Inbox and Drafts through Mailbox/query + Mailbox/get rather
	// than assuming literal mailbox ids.
	discoverMailbox := func(role string) string {
		t.Helper()
		resp := jmapRequest(t, h, token, []string{frontendMailCap}, []JmapMethodCall{
			{"Mailbox/query", map[string]interface{}{
				"accountId": accountID,
				"filter":    map[string]interface{}{"role": role},
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
		result := frontendRequireInvocation(t, resp, "Mailbox/get")
		list, _ := result["list"].([]interface{})
		for _, rawMailbox := range list {
			mailbox, _ := rawMailbox.(map[string]interface{})
			if mailbox["role"] != role {
				continue
			}
			id, _ := mailbox["id"].(string)
			if id == "" {
				t.Fatalf("Mailbox/get returned %q mailbox without an id: %v", role, mailbox)
			}
			return id
		}
		t.Fatalf("Mailbox/get did not return a %q mailbox: %v", role, result)
		return ""
	}
	inboxID := discoverMailbox("inbox")
	draftsID := discoverMailbox("drafts")
	if inboxID == draftsID {
		t.Fatalf("inbox and drafts mailbox ids are both %q", inboxID)
	}

	// Email/query + Email/get via result reference, as the narrow client does.
	resp := jmapRequest(t, h, token, []string{frontendMailCap}, []JmapMethodCall{
		{"Email/query", map[string]interface{}{
			"accountId":       accountID,
			"filter":          map[string]interface{}{"inMailbox": inboxID},
			"sort":            []map[string]interface{}{{"property": "receivedAt", "isAscending": false}},
			"collapseThreads": false,
		}, "query1"},
		{"Email/get", map[string]interface{}{
			"accountId": accountID,
			"#ids": map[string]interface{}{
				"resultOf": "query1",
				"name":     "Email/query",
				"path":     "/ids/*",
			},
			"properties": []string{"id", "blobId", "keywords", "subject"},
		}, "get1"},
	})
	queryResult := frontendRequireInvocation(t, resp, "Email/query")
	queryIDs, _ := queryResult["ids"].([]interface{})
	foundFixture := false
	for _, rawID := range queryIDs {
		if rawID == emailID {
			foundFixture = true
			break
		}
	}
	if !foundFixture {
		t.Fatalf("Email/query ids = %v, want %q", queryIDs, emailID)
	}
	getResult := frontendRequireInvocation(t, resp, "Email/get")
	var blobID string
	list, _ := getResult["list"].([]interface{})
	for _, rawEmail := range list {
		item, _ := rawEmail.(map[string]interface{})
		if item["id"] != emailID {
			continue
		}
		blobID, _ = item["blobId"].(string)
		keywords := frontendMap(t, item["keywords"], "keywords")
		if keywords["$seen"] == true {
			t.Fatalf("fixture is already marked $seen: %v", keywords)
		}
	}
	if blobID == "" {
		t.Fatalf("Email/get did not return blobId for %q: %v", emailID, getResult)
	}

	// Mark $seen and verify the change through another HTTP JMAP call.
	resp = jmapRequest(t, h, token, []string{frontendMailCap}, []JmapMethodCall{
		{"Email/set", map[string]interface{}{
			"accountId": accountID,
			"update": map[string]interface{}{
				emailID: map[string]interface{}{"keywords/$seen": true},
			},
		}, "set1"},
	})
	setResult := frontendRequireInvocation(t, resp, "Email/set")
	frontendNoSetErrors(t, setResult)
	updated := frontendMap(t, setResult["updated"], "updated")
	if _, ok := updated[emailID]; !ok {
		t.Fatalf("Email/set updated = %v, want entry for %q", updated, emailID)
	}
	resp = jmapRequest(t, h, token, []string{frontendMailCap}, []JmapMethodCall{
		{"Email/get", map[string]interface{}{
			"accountId":  accountID,
			"ids":        []string{emailID},
			"properties": []string{"id", "keywords"},
		}, "get2"},
	})
	getResult = frontendRequireInvocation(t, resp, "Email/get")
	list, _ = getResult["list"].([]interface{})
	if len(list) != 1 {
		t.Fatalf("Email/get list = %v, want one email", list)
	}
	item := frontendMap(t, list[0], "Email/get list[0]")
	keywords := frontendMap(t, item["keywords"], "keywords")
	if keywords["$seen"] != true {
		t.Fatalf("keywords = %v, want $seen true", keywords)
	}

	// Discover the sending Identity with the submission capability.
	resp = jmapRequest(t, h, token, []string{frontendSubmissionCap}, []JmapMethodCall{
		{"Identity/get", map[string]interface{}{"accountId": accountID}, "identityGet"},
	})
	identityResult := frontendRequireInvocation(t, resp, "Identity/get")
	identities, _ := identityResult["list"].([]interface{})
	if len(identities) == 0 {
		t.Fatalf("Identity/get list = %v, want at least one identity", identities)
	}
	var identityID string
	for _, rawIdentity := range identities {
		identity, _ := rawIdentity.(map[string]interface{})
		id, _ := identity["id"].(string)
		if identity["mayDelete"] == false {
			identityID = id
			break
		}
	}
	if identityID == "" {
		identityID, _ = frontendMap(t, identities[0], "identity")["id"].(string)
	}
	if identityID == "" {
		t.Fatalf("Identity/get did not return a usable id: %v", identities)
	}

	// Compose a draft and submit it in one batch using creation references.
	// No From is supplied: the server derives it from the Identity.
	resp = jmapRequest(t, h, token, []string{frontendMailCap, frontendSubmissionCap}, []JmapMethodCall{
		{"Email/set", map[string]interface{}{
			"accountId": accountID,
			"create": map[string]interface{}{
				"create1": map[string]interface{}{
					"mailboxIds": map[string]bool{draftsID: true},
					"keywords":   map[string]bool{"$draft": true},
					"to": []map[string]interface{}{
						{"email": "carol@example.com", "name": ""},
					},
					"subject":  "Frontend use case draft",
					"textBody": []map[string]interface{}{{"partId": "text1", "type": "text/plain"}},
					"bodyValues": map[string]interface{}{
						"text1": map[string]interface{}{
							"value":             "Sent through the narrow JMAP client flow.",
							"isEncodingProblem": false,
							"isTruncated":       false,
						},
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
	emailSetResults := frontendInvocations(t, resp, "Email/set")
	if len(emailSetResults) == 0 {
		t.Fatalf("no Email/set response in %v", resp)
	}
	frontendNoSetErrors(t, emailSetResults[0])
	submissionResult := frontendRequireInvocation(t, resp, "EmailSubmission/set")
	frontendNoSetErrors(t, submissionResult)
	createdIDs := frontendMap(t, resp["createdIds"], "createdIds")
	draftID, _ := createdIDs["create1"].(string)
	if draftID == "" || strings.HasPrefix(draftID, "#") {
		t.Fatalf("createdIds = %v, want create1 mapped to a real id", createdIDs)
	}
	if len(capture.Sent) != 1 {
		t.Fatalf("sent %d messages, want 1", len(capture.Sent))
	}
	if got := capture.Sent[0].From.Address; got != "bob@example.com" {
		t.Errorf("sent From = %q, want bob@example.com", got)
	}
	if len(capture.Sent[0].To) != 1 || capture.Sent[0].To[0].Address != "carol@example.com" {
		t.Errorf("sent To = %v, want carol@example.com", capture.Sent[0].To)
	}

	// RFC 8621 section 7.5: onSuccessUpdateEmail emits an implicit Email/set
	// with the same method call id as the submission.
	if len(emailSetResults) != 2 {
		t.Fatalf("Email/set responses = %d, want 2 (create + implicit update)", len(emailSetResults))
	}
	implicitUpdated := frontendMap(t, emailSetResults[1]["updated"], "implicit Email/set updated")
	if _, ok := implicitUpdated[draftID]; !ok {
		t.Fatalf("implicit Email/set updated = %v, want entry for %q", implicitUpdated, draftID)
	}

	// Toggle VacationResponse via the singleton id.
	resp = jmapRequest(t, h, token, []string{frontendVacationCap}, []JmapMethodCall{
		{"VacationResponse/get", map[string]interface{}{
			"accountId": accountID,
			"ids":       []string{"singleton"},
		}, "getVacation"},
	})
	vacationList, _ := frontendRequireInvocation(t, resp, "VacationResponse/get")["list"].([]interface{})
	if len(vacationList) != 1 {
		t.Fatalf("VacationResponse/get list = %v, want the singleton", vacationList)
	}
	before := frontendMap(t, vacationList[0], "VacationResponse")
	if before["isEnabled"] == true {
		t.Fatalf("VacationResponse is already enabled: %v", before)
	}
	resp = jmapRequest(t, h, token, []string{frontendVacationCap}, []JmapMethodCall{
		{"VacationResponse/set", map[string]interface{}{
			"accountId": accountID,
			"update": map[string]interface{}{
				"singleton": map[string]interface{}{
					"isEnabled": true,
					"subject":   "Away",
					"textBody":  "Back soon",
				},
			},
		}, "setVacation"},
	})
	vacationSet := frontendRequireInvocation(t, resp, "VacationResponse/set")
	frontendNoSetErrors(t, vacationSet)
	vacationUpdated := frontendMap(t, vacationSet["updated"], "VacationResponse/set updated")
	if _, ok := vacationUpdated["singleton"]; !ok {
		t.Fatalf("VacationResponse/set updated = %v, want singleton", vacationUpdated)
	}
	resp = jmapRequest(t, h, token, []string{frontendVacationCap}, []JmapMethodCall{
		{"VacationResponse/get", map[string]interface{}{
			"accountId": accountID,
			"ids":       []string{"singleton"},
		}, "getVacation"},
	})
	vacationList, _ = frontendRequireInvocation(t, resp, "VacationResponse/get")["list"].([]interface{})
	if len(vacationList) != 1 {
		t.Fatalf("VacationResponse/get list = %v, want the singleton", vacationList)
	}
	after := frontendMap(t, vacationList[0], "VacationResponse")
	if after["isEnabled"] != true || after["subject"] != "Away" {
		t.Fatalf("VacationResponse after toggle = %v, want isEnabled/subject set", after)
	}

	// Raw MIME: Email/get supplied the blobId; Session.downloadUrl supplies
	// the retrieval template. There is no /api/email/raw endpoint.
	downloadURL := session.DownloadURL
	for name, value := range map[string]string{
		"accountId": accountID,
		"blobId":    blobID,
		"name":      "message.eml",
		"type":      "message%2Frfc822",
	} {
		downloadURL = strings.ReplaceAll(downloadURL, "{"+name+"}", value)
	}
	if strings.Contains(downloadURL, "{") {
		t.Fatalf("unexpanded variable in downloadUrl: %q", downloadURL)
	}
	downloaded := request(t, h, http.MethodGet, downloadURL, "", token)
	if downloaded.Code != http.StatusOK {
		t.Fatalf("GET %s status = %d, want 200; body %s", downloadURL, downloaded.Code, downloaded.Body.String())
	}
	if downloaded.Body.String() != raw {
		t.Errorf("downloaded bytes = %q, want %q", downloaded.Body.String(), raw)
	}
	if got := downloaded.Header().Get("Content-Type"); got != "message/rfc822" {
		t.Errorf("download Content-Type = %q, want message/rfc822", got)
	}
}
