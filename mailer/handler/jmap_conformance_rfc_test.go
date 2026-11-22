package handler

import (
	"net/http"
	"testing"
	"time"
)

// These protocol-level tests exercise the JMAP HTTP endpoint. They cover the
// conformance findings that were fixed while integrating the external suite:
// stable state tokens, attachment creation, blob validation, parsed-blob
// metadata and SearchSnippet's nullable notFound.

const conformanceMessage = "From: Sender <sender@example.com>\r\n" +
	"To: bob@example.com\r\n" +
	"Subject: Conformance fixture\r\n" +
	"Date: Mon, 02 Jan 2006 15:04:05 +0000\r\n" +
	"Message-ID: <conformance-fixture@example.com>\r\n" +
	"MIME-Version: 1.0\r\n" +
	"Content-Type: text/plain; charset=utf-8\r\n" +
	"\r\n" +
	"Conformance body\r\n"

func conformanceHandler(t *testing.T, filesystem bool) (http.Handler, string, string) {
	t.Helper()
	tc := handlerTestConfig{
		AuthUser:     "bob@example.com",
		AuthPass:     "anything",
		JWTSecret:    "secret",
		JWTSecretTTL: time.Hour,
		MetadataType: "memory",
		UserDomain:   "example.com",
		UserManager: &testUserManager{
			adminUsername: "bob@example.com",
			adminPassword: "anything",
			domain:        "example.com",
		},
	}
	if filesystem {
		tc.StorageType = "filesystem"
		tc.FilesystemDir = t.TempDir()
	}
	h, err := newTestHandler(t, tc)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	_, token, _ := login(t, h, "bob@example.com", "anything")
	return h, token, "bob@example.com"
}

func conformanceState(t *testing.T, h http.Handler, token, accountID string) string {
	t.Helper()
	resp := jmapRequest(t, h, token, []string{"urn:ietf:params:jmap:mail"}, []JmapMethodCall{
		{"Email/get", map[string]any{"accountId": accountID, "ids": []string{}}, "get"},
	})
	state, _ := jmapMethodResult(t, resp, "Email/get")["state"].(string)
	if state == "" {
		t.Fatalf("Email/get has no state: %v", resp)
	}
	return state
}

// TestEmailChanges_StableStateWithNonUTF8Subject_RFC8621_4_3 verifies that a
// state token remains valid across requests. Fingerprints derived from raw
// RFC 5322 headers must be safe to persist as JSON: otherwise a non-UTF-8
// subject changes its fingerprint on serialization and /changes reports a
// spurious update even though no email changed.
func TestEmailChanges_StableStateWithNonUTF8Subject_RFC8621_4_3(t *testing.T) {
	h, token, accountID := conformanceHandler(t, true)
	raw := "From: Sender <sender@example.com>\r\nTo: bob@example.com\r\n" +
		"Subject: \xb0\xd4\xc7\xd9\xc7\xa1\r\nDate: Mon, 02 Jan 2006 15:04:05 +0000\r\n" +
		"Message-ID: <non-utf8@example.com>\r\nMIME-Version: 1.0\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n\r\nbody\r\n"
	uploaded := uploadBinary(t, h, accountID, raw, "message/rfc822", token)

	importResp := jmapRequest(t, h, token, []string{"urn:ietf:params:jmap:mail"}, []JmapMethodCall{
		{"Email/import", map[string]any{
			"accountId": accountID,
			"emails": map[string]any{
				"c1": map[string]any{
					"blobId":     uploaded.BlobID,
					"mailboxIds": map[string]any{"inbox": true},
				},
			},
		}, "import"},
	})
	if notCreated, ok := jmapMethodResult(t, importResp, "Email/import")["notCreated"].(map[string]any); ok && len(notCreated) != 0 {
		t.Fatalf("Email/import failed: %v", notCreated)
	}

	state1 := conformanceState(t, h, token, accountID)
	state2 := conformanceState(t, h, token, accountID)
	if state1 != state2 {
		t.Fatalf("state changed without a data change: %q then %q", state1, state2)
	}

	changeResp := jmapRequest(t, h, token, []string{"urn:ietf:params:jmap:mail"}, []JmapMethodCall{
		{"Email/changes", map[string]any{"accountId": accountID, "sinceState": state1}, "changes"},
	})
	result := jmapMethodResult(t, changeResp, "Email/changes")
	for _, property := range []string{"created", "updated", "destroyed"} {
		ids, ok := result[property].([]any)
		if !ok || len(ids) != 0 {
			t.Errorf("%s = %v, want empty array", property, result[property])
		}
	}
	if result["newState"] != state1 {
		t.Errorf("newState = %v, want %q", result["newState"], state1)
	}
}

// TestEmailSet_CreateWithAttachment_RFC8621_4_6 checks that a blobId-bearing
// attachment is rendered into the stored message. The resulting hasAttachment,
// attachments and bodyStructure must all be derived from those bytes.
func TestEmailSet_CreateWithAttachment_RFC8621_4_6(t *testing.T) {
	h, token, accountID := conformanceHandler(t, false)
	uploaded := uploadBinary(t, h, accountID, "attachment bytes", "application/octet-stream", token)

	setResp := jmapRequest(t, h, token, []string{"urn:ietf:params:jmap:mail"}, []JmapMethodCall{
		{"Email/set", map[string]any{
			"accountId": accountID,
			"create": map[string]any{
				"c1": map[string]any{
					"mailboxIds": map[string]any{"inbox": true},
					"subject":    "Attachment create",
					"textBody":   []any{map[string]any{"type": "text/plain", "value": "with a file"}},
					"attachments": []any{map[string]any{
						"blobId": uploaded.BlobID,
						"type":   "application/octet-stream",
						"name":   "report.bin",
					}},
				},
			},
		}, "set"},
	})
	setResult := jmapMethodResult(t, setResp, "Email/set")
	created, _ := setResult["created"].(map[string]any)
	entry, ok := created["c1"].(map[string]any)
	if !ok {
		t.Fatalf("created = %v, want c1", setResult)
	}
	if entry["hasAttachment"] != true {
		t.Errorf("created hasAttachment = %v, want true", entry["hasAttachment"])
	}
	id, _ := entry["id"].(string)

	getResp := jmapRequest(t, h, token, []string{"urn:ietf:params:jmap:mail"}, []JmapMethodCall{
		{"Email/get", map[string]any{
			"accountId":  accountID,
			"ids":        []string{id},
			"properties": []string{"id", "hasAttachment", "attachments", "bodyStructure", "size"},
		}, "get"},
	})
	list, _ := jmapMethodResult(t, getResp, "Email/get")["list"].([]any)
	if len(list) != 1 {
		t.Fatalf("Email/get list = %v, want one email", list)
	}
	emailObj, _ := list[0].(map[string]any)
	if emailObj["hasAttachment"] != true {
		t.Errorf("stored hasAttachment = %v, want true", emailObj["hasAttachment"])
	}
	attachments, ok := emailObj["attachments"].([]any)
	if !ok || len(attachments) != 1 {
		t.Fatalf("attachments = %v, want one attachment", emailObj["attachments"])
	}
	attachment, _ := attachments[0].(map[string]any)
	if attachment["blobId"] != uploaded.BlobID || attachment["name"] != "report.bin" {
		t.Errorf("attachment = %v, want uploaded blob and name", attachment)
	}
	structure, _ := emailObj["bodyStructure"].(map[string]any)
	if structure["type"] != "multipart/mixed" {
		t.Errorf("bodyStructure type = %v, want multipart/mixed", structure["type"])
	}
}

// TestEmailImport_InvalidBlob_RFC8621_4_8 checks the two blob-related failure
// modes: uploaded bytes that are not an RFC 5322 message and an unknown blobId.
func TestEmailImport_InvalidBlob_RFC8621_4_8(t *testing.T) {
	h, token, accountID := conformanceHandler(t, false)
	uploaded := uploadBinary(t, h, accountID, "this is not an email", "application/octet-stream", token)

	resp := jmapRequest(t, h, token, []string{"urn:ietf:params:jmap:mail"}, []JmapMethodCall{
		{"Email/import", map[string]any{
			"accountId": accountID,
			"emails": map[string]any{
				"invalid": map[string]any{
					"blobId":     uploaded.BlobID,
					"mailboxIds": map[string]any{"inbox": true},
				},
				"missing": map[string]any{
					"blobId":     "no-such-blob",
					"mailboxIds": map[string]any{"inbox": true},
				},
			},
		}, "import"},
	})
	result := jmapMethodResult(t, resp, "Email/import")
	notCreated, _ := result["notCreated"].(map[string]any)
	invalid, _ := notCreated["invalid"].(map[string]any)
	missing, _ := notCreated["missing"].(map[string]any)
	if invalid["type"] != "invalidEmail" {
		t.Errorf("invalid blob error = %v, want invalidEmail", invalid)
	}
	if missing["type"] != "invalidProperties" {
		t.Errorf("missing blob error = %v, want invalidProperties", missing)
	}
}

// TestEmailParse_NullStoredProperties_RFC8621_4_9 verifies that properties
// describing a stored Email are null for a blob parsed in isolation.
func TestEmailParse_NullStoredProperties_RFC8621_4_9(t *testing.T) {
	h, token, accountID := conformanceHandler(t, false)
	uploaded := uploadBinary(t, h, accountID, conformanceMessage, "message/rfc822", token)

	resp := jmapRequest(t, h, token, []string{"urn:ietf:params:jmap:mail"}, []JmapMethodCall{
		{"Email/parse", map[string]any{
			"accountId": accountID,
			"blobIds":   []string{uploaded.BlobID},
			"properties": []string{
				"id", "threadId", "mailboxIds", "keywords", "receivedAt", "subject",
			},
		}, "parse"},
	})
	result := jmapMethodResult(t, resp, "Email/parse")
	parsed, _ := result["parsed"].(map[string]any)
	item, ok := parsed[uploaded.BlobID].(map[string]any)
	if !ok {
		t.Fatalf("parsed = %v, want the uploaded blob", result)
	}
	for _, property := range []string{"id", "threadId", "mailboxIds", "keywords", "receivedAt"} {
		value, present := item[property]
		if !present || value != nil {
			t.Errorf("%s = %v (present=%v), want explicit null", property, value, present)
		}
	}
	if item["subject"] != "Conformance fixture" {
		t.Errorf("subject = %v, want the message header", item["subject"])
	}

	// Non-message bytes are reported in notParsable rather than parsed.
	bad := uploadBinary(t, h, accountID, "not a message at all", "application/octet-stream", token)
	badResp := jmapRequest(t, h, token, []string{"urn:ietf:params:jmap:mail"}, []JmapMethodCall{
		{"Email/parse", map[string]any{"accountId": accountID, "blobIds": []string{bad.BlobID}}, "parse"},
	})
	badResult := jmapMethodResult(t, badResp, "Email/parse")
	notParsable, _ := badResult["notParsable"].([]any)
	if len(notParsable) != 1 || notParsable[0] != bad.BlobID {
		t.Errorf("notParsable = %v, want %q", notParsable, bad.BlobID)
	}
}

// TestSearchSnippet_NotFoundNull_RFC8621_5 checks the RFC 8621 section 5
// response shape: notFound is null when every id was found, and an array only
// when ids actually failed to resolve.
func TestSearchSnippet_NotFoundNull_RFC8621_5(t *testing.T) {
	h, token, accountID := conformanceHandler(t, false)
	uploaded := uploadBinary(t, h, accountID, conformanceMessage, "message/rfc822", token)
	importResp := jmapRequest(t, h, token, []string{"urn:ietf:params:jmap:mail"}, []JmapMethodCall{
		{"Email/import", map[string]any{
			"accountId": accountID,
			"emails": map[string]any{
				"c1": map[string]any{
					"blobId":     uploaded.BlobID,
					"mailboxIds": map[string]any{"inbox": true},
				},
			},
		}, "import"},
	})
	created, _ := jmapMethodResult(t, importResp, "Email/import")["created"].(map[string]any)
	entry, _ := created["c1"].(map[string]any)
	id, _ := entry["id"].(string)
	if id == "" {
		t.Fatalf("Email/import created = %v", created)
	}

	call := func(ids []string) map[string]any {
		resp := jmapRequest(t, h, token, []string{"urn:ietf:params:jmap:mail"}, []JmapMethodCall{
			{"SearchSnippet/get", map[string]any{
				"accountId": accountID,
				"filter":    map[string]any{},
				"emailIds":  ids,
			}, "snippet"},
		})
		return jmapMethodResult(t, resp, "SearchSnippet/get")
	}

	result := call([]string{id})
	if notFound, present := result["notFound"]; !present || notFound != nil {
		t.Errorf("notFound = %v (present=%v), want null", notFound, present)
	}
	if snippetList, ok := result["list"].([]any); !ok || len(snippetList) != 1 {
		t.Fatalf("list = %v, want one snippet", result["list"])
	}

	result = call([]string{id, "missing-id"})
	notFound, ok := result["notFound"].([]any)
	if !ok || len(notFound) != 1 || notFound[0] != "missing-id" {
		t.Errorf("notFound = %v, want [missing-id]", result["notFound"])
	}
}

// Guard against an accidental regression to an empty array when no snippet
// matches the filter: RFC 8621 section 5 still returns null for notFound.
func TestSearchSnippet_EmptyResultNullNotFound_RFC8621_5(t *testing.T) {
	h, token, accountID := conformanceHandler(t, false)
	resp := jmapRequest(t, h, token, []string{"urn:ietf:params:jmap:mail"}, []JmapMethodCall{
		{"SearchSnippet/get", map[string]any{
			"accountId": accountID,
			"filter":    map[string]any{},
			"emailIds":  []string{},
		}, "snippet"},
	})
	result := jmapMethodResult(t, resp, "SearchSnippet/get")
	if notFound, present := result["notFound"]; !present || notFound != nil {
		t.Errorf("notFound = %v (present=%v), want null", notFound, present)
	}
	if list, ok := result["list"].([]any); !ok || len(list) != 0 {
		t.Errorf("list = %v, want empty array", result["list"])
	}
}
