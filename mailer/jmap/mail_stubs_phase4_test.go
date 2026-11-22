package jmap

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/kncept/Emissary/mailer/components/metadata"
	"github.com/kncept/Emissary/mailer/components/sender"
	"github.com/kncept/Emissary/mailer/components/storage"
	"github.com/kncept/Emissary/mailer/components/usermanager"
	mailemail "github.com/kncept/Emissary/mailer/email"
)

func newMailMethodsTestHandler(t *testing.T) (*Handler, storage.Storage) {
	t.Helper()
	store := storage.NewMemoryStorage()
	h := NewHandler(store, metadata.NewMemoryMetadata(),
		usermanager.NewNone(usermanager.Config{AdminUsername: "admin", AdminPassword: "password"}),
		sender.NewInMemoryRecording())
	return h, store
}

func putRawBlob(t *testing.T, store storage.Storage, accountID string, raw []byte) string {
	t.Helper()
	meta, err := store.PutBlob(context.Background(), accountID, "message/rfc822", raw)
	if err != nil {
		t.Fatalf("PutBlob() error = %v", err)
	}
	return meta.ID
}

func rawTestEmail(subject string) []byte {
	message := &mailemail.UserEmail{
		From:    mailemail.Address{Address: "sender@example.net"},
		To:      []mailemail.Address{{Address: "alice@example.com"}},
		Subject: subject,
		Date:    time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
		Text:    "Hello from a raw message.",
	}
	return mailemail.RawFromUserEmail(message)
}

func addSnippetEmail(t *testing.T, store storage.Storage, accountID, subject, text string) string {
	t.Helper()
	message := &mailemail.UserEmail{
		From:    mailemail.Address{Address: "sender@example.net"},
		To:      []mailemail.Address{{Address: accountID}},
		Subject: subject,
		Date:    time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
		Text:    text,
	}
	id, err := store.AddEmail(context.Background(), accountID, "inbox", message, mailemail.RawFromUserEmail(message))
	if err != nil {
		t.Fatalf("AddEmail() error = %v", err)
	}
	return id
}

func TestSearchSnippetGetRFC8621_5(t *testing.T) {
	const accountID = "alice@example.com"
	h, store := newMailMethodsTestHandler(t)
	ctx := context.Background()
	matching := addSnippetEmail(t, store, accountID, "The Foosball competition",
		"Last year the foosball event will be held in the Stadium with a very long tail of irrelevant words that must not overflow the RFC preview budget at all.")
	other := addSnippetEmail(t, store, accountID, "Quarterly report", "Revenue is up.")

	// No filter means there is no filter text to highlight; both properties
	// are null. Unknown ids still appear in notFound, and a snippet has no id.
	raw, err := h.SearchSnippetGet(ctx, accountID, json.RawMessage(fmt.Sprintf(
		`{"accountId":%q,"emailIds":[%q,"missing"]}`, accountID, matching)))
	if err != nil {
		t.Fatalf("SearchSnippet/get error = %v", err)
	}
	resp := decodeMap(t, raw)
	list := resp["list"].([]any)
	if len(list) != 1 {
		t.Fatalf("list = %v, want one snippet", list)
	}
	snippet := list[0].(map[string]any)
	if snippet["emailId"] != matching || snippet["subject"] != nil || snippet["preview"] != nil {
		t.Errorf("snippet = %v, want null subject/preview without a filter", snippet)
	}
	if _, ok := snippet["id"]; ok {
		t.Errorf("snippet must not have an id property: %v", snippet)
	}
	notFound := resp["notFound"].([]any)
	if len(notFound) != 1 || notFound[0] != "missing" {
		t.Errorf("notFound = %v, want [missing]", notFound)
	}

	cases := []struct {
		name            string
		emailID         string
		filter          string
		wantSubject     any
		wantNoSubject   bool
		wantPreviewMark string
		wantNoPreview   bool
	}{
		{
			name: "text matches subject and body", emailID: matching,
			filter:          `{"text":"foosball"}`,
			wantSubject:     "The <mark>Foosball</mark> competition",
			wantPreviewMark: "<mark>foosball</mark>",
		},
		{
			name: "subject-only match leaves preview null", emailID: matching,
			filter:        `{"subject":"competition"}`,
			wantSubject:   "The Foosball <mark>competition</mark>",
			wantNoPreview: true,
		},
		{
			name: "body-only match leaves subject null", emailID: matching,
			filter:          `{"body":"stadium"}`,
			wantNoSubject:   true,
			wantPreviewMark: "<mark>Stadium</mark>",
		},
		{
			name: "no text match returns nulls", emailID: matching,
			filter:        `{"text":"not-in-this-mailbox"}`,
			wantNoSubject: true, wantNoPreview: true,
		},
		{
			name: "compound filter uses shared query helpers", emailID: matching,
			filter:          `{"operator":"AND","conditions":[{"subject":"foosball"},{"body":"stadium"}]}`,
			wantSubject:     "The <mark>Foosball</mark> competition",
			wantPreviewMark: "<mark>Stadium</mark>",
		},
		{
			name: "nonmatching email returns nulls", emailID: other,
			filter:        `{"text":"foosball"}`,
			wantNoSubject: true, wantNoPreview: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			arguments := fmt.Sprintf(`{"accountId":%q,"filter":%s,"emailIds":[%q]}`,
				accountID, tc.filter, tc.emailID)
			raw, err := h.SearchSnippetGet(ctx, accountID, json.RawMessage(arguments))
			if err != nil {
				t.Fatalf("SearchSnippet/get error = %v", err)
			}
			resp := decodeMap(t, raw)
			items := resp["list"].([]any)
			if len(items) != 1 {
				t.Fatalf("list = %v, want one snippet", items)
			}
			item := items[0].(map[string]any)
			if tc.wantNoSubject {
				if item["subject"] != nil {
					t.Errorf("subject = %v, want null", item["subject"])
				}
			} else if item["subject"] != tc.wantSubject {
				t.Errorf("subject = %v, want %v", item["subject"], tc.wantSubject)
			}
			if tc.wantNoPreview {
				if item["preview"] != nil {
					t.Errorf("preview = %v, want null", item["preview"])
				}
				return
			}
			preview, ok := item["preview"].(string)
			if !ok {
				t.Fatalf("preview = %v, want a string", item["preview"])
			}
			if !strings.Contains(preview, tc.wantPreviewMark) {
				t.Errorf("preview = %q, want %q", preview, tc.wantPreviewMark)
			}
			if len(preview) > 255 {
				t.Errorf("preview is %d octets, want at most 255: %q", len(preview), preview)
			}
		})
	}

	if _, err := h.SearchSnippetGet(ctx, accountID,
		json.RawMessage(`{"emailIds":[],"filter":{"nonsense":"x"}}`)); err == nil ||
		!strings.Contains(err.Error(), "unsupportedFilter") {
		t.Errorf("SearchSnippet/get with unsupported filter error = %v, want unsupportedFilter", err)
	}
}

func TestSearchSnippetEscapesHTMLRFC8621_5(t *testing.T) {
	const accountID = "alice@example.com"
	h, store := newMailMethodsTestHandler(t)
	id := addSnippetEmail(t, store, accountID, "A & B <tag> result", "Body here")
	raw, err := h.SearchSnippetGet(context.Background(), accountID, json.RawMessage(fmt.Sprintf(
		`{"accountId":%q,"filter":{"text":"B"},"emailIds":[%q]}`, accountID, id)))
	if err != nil {
		t.Fatalf("SearchSnippet/get error = %v", err)
	}
	item := decodeMap(t, raw)["list"].([]any)[0].(map[string]any)
	const want = "A &amp; <mark>B</mark> &lt;tag&gt; result"
	if item["subject"] != want {
		t.Errorf("subject = %v, want %q", item["subject"], want)
	}
}

func TestEmailParsePhase4(t *testing.T) {
	const accountID = "alice@example.com"
	h, store := newMailMethodsTestHandler(t)
	ctx := context.Background()
	blobID := putRawBlob(t, store, accountID, rawTestEmail("Parsed subject"))

	raw, err := h.EmailParse(ctx, accountID, json.RawMessage(fmt.Sprintf(
		`{"accountId":%q,"blobIds":[%q,"unknown"],"properties":["id","subject","blobId"],"fetchTextBodyValues":true}`,
		accountID, blobID)))
	if err != nil {
		t.Fatalf("Email/parse error = %v", err)
	}
	resp := decodeMap(t, raw)
	parsed := resp["parsed"].(map[string]any)
	obj, ok := parsed[blobID].(map[string]any)
	if !ok {
		t.Fatalf("parsed = %v, want entry for %q", parsed, blobID)
	}
	if obj["subject"] != "Parsed subject" || obj["blobId"] != blobID {
		t.Errorf("parsed email = %v, want subject and blobId", obj)
	}
	if id, ok := obj["id"]; !ok || id != nil {
		t.Errorf("parsed id = %v, want null", id)
	}
	if len(obj) != 3 {
		t.Errorf("parsed properties = %v, want only id/subject/blobId", obj)
	}
	notFound := resp["notFound"].([]any)
	if len(notFound) != 1 || notFound[0] != "unknown" {
		t.Errorf("notFound = %v, want [unknown]", notFound)
	}
}

func TestEmailImportPhase4(t *testing.T) {
	const accountID = "alice@example.com"
	h, store := newMailMethodsTestHandler(t)
	ctx := context.Background()
	blobID := putRawBlob(t, store, accountID, rawTestEmail("Imported subject"))

	raw, err := h.EmailImport(ctx, accountID, json.RawMessage(fmt.Sprintf(`{
		"accountId":%q,
		"emails":{
			"c1":{"blobId":%q,"mailboxIds":{"inbox":true},"keywords":{"$important":true}}
		}
	}`, accountID, blobID)))
	if err != nil {
		t.Fatalf("Email/import error = %v", err)
	}
	resp := decodeMap(t, raw)
	created := resp["created"].(map[string]any)
	obj, ok := created["c1"].(map[string]any)
	if !ok {
		t.Fatalf("created = %v, want c1", created)
	}
	id, _ := obj["id"].(string)
	if id == "" || obj["blobId"] != blobID || obj["subject"] != "Imported subject" {
		t.Errorf("created email = %v, want stored email with original blob", obj)
	}
	if resp["newState"] == resp["oldState"] {
		t.Errorf("state did not advance after import: %s", raw)
	}

	// The imported email is retrievable with the original bytes and keywords.
	got, err := h.EmailGet(ctx, accountID, json.RawMessage(fmt.Sprintf(
		`{"ids":[%q],"properties":["id","subject","blobId","keywords"]}`, id)))
	if err != nil {
		t.Fatalf("Email/get error = %v", err)
	}
	gotObj := decodeMap(t, got)["list"].([]any)[0].(map[string]any)
	if gotObj["blobId"] != blobID || gotObj["subject"] != "Imported subject" {
		t.Errorf("stored email = %v, want imported bytes", gotObj)
	}
	keywords := gotObj["keywords"].(map[string]any)
	if keywords["$important"] != true {
		t.Errorf("keywords = %v, want $important", keywords)
	}

	// Unknown blob is reported per creation id, not as a method error.
	raw, err = h.EmailImport(ctx, accountID, json.RawMessage(fmt.Sprintf(
		`{"accountId":%q,"emails":{"c2":{"blobId":"missing","mailboxIds":{"inbox":true}}}}`, accountID)))
	if err != nil {
		t.Fatalf("Email/import(missing blob) error = %v", err)
	}
	notCreated := decodeMap(t, raw)["notCreated"].(map[string]any)
	if e, ok := notCreated["c2"].(map[string]any); !ok || e["type"] != "notFound" {
		t.Errorf("notCreated = %v, want notFound", notCreated)
	}
}

func TestEmailCopyPhase4(t *testing.T) {
	const accountID = "alice@example.com"
	h, store := newMailMethodsTestHandler(t)
	ctx := context.Background()
	sourceID, err := store.AddEmail(ctx, accountID, "inbox", &mailemail.UserEmail{
		From: mailemail.Address{Address: "sender@example.net"}, Subject: "Copy me",
	}, rawTestEmail("Copy me"))
	if err != nil {
		t.Fatalf("AddEmail() error = %v", err)
	}

	raw, err := h.EmailCopy(ctx, accountID, json.RawMessage(fmt.Sprintf(`{
		"fromAccountId":%q,"accountId":%q,
		"create":{"c1":{"id":%q,"mailboxIds":{"archive":true}}}
	}`, accountID, accountID, sourceID)))
	if err != nil {
		t.Fatalf("Email/copy error = %v", err)
	}
	resp := decodeMap(t, raw)
	created := resp["created"].(map[string]any)
	obj, ok := created["c1"].(map[string]any)
	if !ok {
		t.Fatalf("created = %v, want c1", created)
	}
	if obj["subject"] != "Copy me" {
		t.Errorf("copied email = %v, want Copy me", obj)
	}
	list, err := store.ListEmails(ctx, accountID, "archive")
	if err != nil || len(list) != 1 {
		t.Fatalf("archive emails = %v / %v, want one copy", list, err)
	}

	// Cross-account copies are rejected as forbidden rather than silently
	// reading another account's mail.
	raw, err = h.EmailCopy(ctx, accountID, json.RawMessage(fmt.Sprintf(`{
		"fromAccountId":"bob@example.com","accountId":%q,
		"create":{"c2":{"id":"any","mailboxIds":{"inbox":true}}}
	}`, accountID)))
	if err != nil {
		t.Fatalf("Email/copy(cross account) error = %v", err)
	}
	notCreated := decodeMap(t, raw)["notCreated"].(map[string]any)
	if e, ok := notCreated["c2"].(map[string]any); !ok || e["type"] != "forbidden" {
		t.Errorf("notCreated = %v, want forbidden", notCreated)
	}

	// onSuccessDestroyOriginal removes the source after a successful copy.
	raw, err = h.EmailCopy(ctx, accountID, json.RawMessage(fmt.Sprintf(`{
		"fromAccountId":%q,"accountId":%q,"onSuccessDestroyOriginal":true,
		"create":{"c3":{"id":%q,"mailboxIds":{"inbox":true}}}
	}`, accountID, accountID, sourceID)))
	if err != nil {
		t.Fatalf("Email/copy(destroy) error = %v", err)
	}
	if len(decodeMap(t, raw)["created"].(map[string]any)) != 1 {
		t.Errorf("copy with destroy did not create: %s", raw)
	}
	if _, err := store.GetEmail(ctx, accountID, sourceID); err == nil {
		t.Error("original email still present after onSuccessDestroyOriginal")
	}
}

func TestEmailCopyPhase4StateGuards(t *testing.T) {
	const accountID = "alice@example.com"
	h, store := newMailMethodsTestHandler(t)
	ctx := context.Background()
	sourceID, err := store.AddEmail(ctx, accountID, "inbox", &mailemail.UserEmail{
		From: mailemail.Address{Address: "sender@example.net"}, Subject: "Guarded",
	}, rawTestEmail("Guarded"))
	if err != nil {
		t.Fatalf("AddEmail() error = %v", err)
	}

	// A stale ifFromInState rejects the whole method before any copy.
	_, err = h.EmailCopy(ctx, accountID, json.RawMessage(fmt.Sprintf(`{
		"fromAccountId":%q,"accountId":%q,"ifFromInState":"email-stale",
		"create":{"c1":{"id":%q,"mailboxIds":{"archive":true}}}
	}`, accountID, accountID, sourceID)))
	if err == nil || !strings.Contains(err.Error(), "stateMismatch") {
		t.Fatalf("stale ifFromInState error = %v, want stateMismatch", err)
	}

	// A stale destroyFromIfInState allows the copy but reports each original
	// as notDestroyed instead of silently skipping the guard.
	raw, err := h.EmailCopy(ctx, accountID, json.RawMessage(fmt.Sprintf(`{
		"fromAccountId":%q,"accountId":%q,"onSuccessDestroyOriginal":true,
		"destroyFromIfInState":"email-stale",
		"create":{"c2":{"id":%q,"mailboxIds":{"archive":true}}}
	}`, accountID, accountID, sourceID)))
	if err != nil {
		t.Fatalf("stale destroyFromIfInState error = %v", err)
	}
	resp := decodeMap(t, raw)
	if len(resp["created"].(map[string]any)) != 1 {
		t.Errorf("created = %v, want one copy", resp["created"])
	}
	notDestroyed := resp["notDestroyed"].(map[string]any)
	if setErr, ok := notDestroyed[sourceID].(map[string]any); !ok || setErr["type"] != "stateMismatch" {
		t.Errorf("notDestroyed = %v, want stateMismatch for %q", notDestroyed, sourceID)
	}
	if _, err := store.GetEmail(ctx, accountID, sourceID); err != nil {
		t.Errorf("original was removed despite stale destroy state: %v", err)
	}
}
