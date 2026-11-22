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

func TestSearchSnippetGetPhase4(t *testing.T) {
	const accountID = "alice@example.com"
	h, store := newMailMethodsTestHandler(t)
	ctx := context.Background()
	created, err := store.AddEmail(ctx, accountID, "inbox", &mailemail.UserEmail{
		From: mailemail.Address{Address: "sender@example.net"}, Subject: "Known",
	}, rawTestEmail("Known"))
	if err != nil {
		t.Fatalf("AddEmail() error = %v", err)
	}

	raw, err := h.SearchSnippetGet(ctx, accountID, json.RawMessage(fmt.Sprintf(
		`{"accountId":%q,"emailIds":[%q,"missing"]}`, accountID, created)))
	if err != nil {
		t.Fatalf("SearchSnippet/get error = %v", err)
	}
	resp := decodeMap(t, raw)
	list := resp["list"].([]any)
	if len(list) != 1 {
		t.Fatalf("list = %v, want one snippet", list)
	}
	snippet := list[0].(map[string]any)
	if snippet["emailId"] != created || snippet["subject"] != nil || snippet["preview"] != nil {
		t.Errorf("snippet = %v, want null subject/preview", snippet)
	}
	if _, ok := snippet["id"]; ok {
		t.Errorf("snippet must not have an id property: %v", snippet)
	}
	notFound := resp["notFound"].([]any)
	if len(notFound) != 1 || notFound[0] != "missing" {
		t.Errorf("notFound = %v, want [missing]", notFound)
	}

	// A filter must not be silently ignored.
	if _, err := h.SearchSnippetGet(ctx, accountID, json.RawMessage(`{"emailIds":[],"filter":{"subject":"x"}}`)); err == nil {
		t.Error("SearchSnippet/get with filter succeeded, want unsupportedFilter")
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
