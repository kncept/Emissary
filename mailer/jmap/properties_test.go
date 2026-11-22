package jmap

import (
	"bytes"
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/kncept/Emissary/mailer/components/metadata"
	"github.com/kncept/Emissary/mailer/components/sender"
	"github.com/kncept/Emissary/mailer/components/storage"
	"github.com/kncept/Emissary/mailer/components/usermanager"
	"github.com/kncept/Emissary/mailer/email"
)

func newPropertyTestHandler(t *testing.T) (*Handler, context.Context) {
	t.Helper()
	ctx := context.Background()
	store := storage.NewMemoryStorage()
	addTestEmail(store, "mail1", "test@example.com", "inbox", &email.UserEmail{
		From:    email.Address{Address: "sender@example.com"},
		To:      []email.Address{{Address: "test@example.com"}},
		Subject: "Property selection",
		Date:    time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC),
		Text:    "plain body",
		HTML:    "<p>html body</p>",
		Size:    100,
	})
	handler := NewHandler(store, metadata.NewMemoryMetadata(),
		usermanager.NewNone(usermanager.Config{AdminUsername: "admin", AdminPassword: "password"}),
		sender.NewLogSender())
	return handler, ctx
}

func getListKeys(t *testing.T, result json.RawMessage) ([]string, map[string]json.RawMessage) {
	t.Helper()
	var resp struct {
		List []map[string]json.RawMessage `json:"list"`
	}
	if err := json.Unmarshal(result, &resp); err != nil {
		t.Fatalf("unmarshal get response: %v (body %s)", err, result)
	}
	if len(resp.List) != 1 {
		t.Fatalf("list length = %d, want 1", len(resp.List))
	}
	keys := make([]string, 0, len(resp.List[0]))
	for key := range resp.List[0] {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys, resp.List[0]
}

func TestGetMethodsSelectOnlyRequestedProperties(t *testing.T) {
	handler, ctx := newPropertyTestHandler(t)

	cases := []struct {
		name string
		call func(json.RawMessage) (json.RawMessage, error)
		args string
		want []string
	}{
		{
			name: "Email/get without id in request",
			call: func(args json.RawMessage) (json.RawMessage, error) {
				return handler.EmailGet(ctx, "test@example.com", args)
			},
			args: `{"accountId":"test@example.com","properties":["subject"]}`,
			want: []string{"id", "subject"},
		},
		{
			name: "Email/get with empty selection",
			call: func(args json.RawMessage) (json.RawMessage, error) {
				return handler.EmailGet(ctx, "test@example.com", args)
			},
			args: `{"accountId":"test@example.com","properties":[]}`,
			want: []string{"id"},
		},
		{
			name: "Mailbox/get",
			call: func(args json.RawMessage) (json.RawMessage, error) {
				return handler.MailboxGet(ctx, "test@example.com", args)
			},
			args: `{"accountId":"test@example.com","ids":["inbox"],"properties":["name","totalEmails"]}`,
			want: []string{"id", "name", "totalEmails"},
		},
		{
			name: "Thread/get",
			call: func(args json.RawMessage) (json.RawMessage, error) {
				return handler.ThreadGet(ctx, "test@example.com", args)
			},
			args: `{"accountId":"test@example.com","properties":["id"]}`,
			want: []string{"id"},
		},
		{
			name: "Identity/get",
			call: func(args json.RawMessage) (json.RawMessage, error) {
				return handler.IdentityGet(ctx, "test@example.com", args)
			},
			args: `{"accountId":"test@example.com","properties":["email"]}`,
			want: []string{"email", "id"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := tc.call(json.RawMessage(tc.args))
			if err != nil {
				t.Fatalf("get call failed: %v", err)
			}
			keys, _ := getListKeys(t, result)
			if !slices.Equal(keys, tc.want) {
				t.Errorf("properties = %v, want %v", keys, tc.want)
			}
		})
	}
}

func TestGetMethodsRejectUnknownProperties(t *testing.T) {
	handler, ctx := newPropertyTestHandler(t)

	cases := []struct {
		name string
		call func(json.RawMessage) error
		args string
	}{
		{
			name: "Email/get properties",
			call: func(args json.RawMessage) error {
				_, err := handler.EmailGet(ctx, "test@example.com", args)
				return err
			},
			args: `{"accountId":"test@example.com","properties":["madeUp"]}`,
		},
		{
			name: "Email/get bodyProperties",
			call: func(args json.RawMessage) error {
				_, err := handler.EmailGet(ctx, "test@example.com", args)
				return err
			},
			args: `{"accountId":"test@example.com","bodyProperties":["madeUp"]}`,
		},
		{
			name: "Email/get properties wrong type",
			call: func(args json.RawMessage) error {
				_, err := handler.EmailGet(ctx, "test@example.com", args)
				return err
			},
			args: `{"accountId":"test@example.com","properties":"subject"}`,
		},
		{
			name: "Mailbox/get",
			call: func(args json.RawMessage) error {
				_, err := handler.MailboxGet(ctx, "test@example.com", args)
				return err
			},
			args: `{"accountId":"test@example.com","properties":["madeUp"]}`,
		},
		{
			name: "Thread/get",
			call: func(args json.RawMessage) error {
				_, err := handler.ThreadGet(ctx, "test@example.com", args)
				return err
			},
			args: `{"accountId":"test@example.com","properties":["madeUp"]}`,
		},
		{
			name: "Identity/get",
			call: func(args json.RawMessage) error {
				_, err := handler.IdentityGet(ctx, "test@example.com", args)
				return err
			},
			args: `{"accountId":"test@example.com","properties":["madeUp"]}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.call(json.RawMessage(tc.args))
			if err == nil {
				t.Fatal("expected invalidArguments error, got nil")
			}
			if !strings.HasPrefix(err.Error(), "invalidArguments:") {
				t.Errorf("error = %q, want invalidArguments prefix", err)
			}
		})
	}
}

func TestEmailGetBodyPropertiesFilterBodyParts(t *testing.T) {
	handler, ctx := newPropertyTestHandler(t)
	result, err := handler.EmailGet(ctx, "test@example.com", json.RawMessage(
		`{"accountId":"test@example.com","properties":["id","textBody","htmlBody","bodyValues"],`+
			`"bodyProperties":["partId","type"],"fetchTextBodyValues":true,"fetchHTMLBodyValues":true}`))
	if err != nil {
		t.Fatalf("EmailGet() error = %v", err)
	}

	var resp struct {
		List []struct {
			ID         string                       `json:"id"`
			TextBody   []map[string]json.RawMessage `json:"textBody"`
			HTMLBody   []map[string]json.RawMessage `json:"htmlBody"`
			BodyValues map[string]struct {
				Value string `json:"value"`
			} `json:"bodyValues"`
		} `json:"list"`
	}
	if err := json.Unmarshal(result, &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if len(resp.List) != 1 {
		t.Fatalf("list length = %d, want 1", len(resp.List))
	}
	item := resp.List[0]
	if len(item.TextBody) != 1 || len(item.HTMLBody) != 1 {
		t.Fatalf("textBody/htmlBody = %d/%d parts, want 1/1", len(item.TextBody), len(item.HTMLBody))
	}
	for _, part := range append(item.TextBody, item.HTMLBody...) {
		if len(part) != 2 || part["partId"] == nil || part["type"] == nil {
			t.Errorf("body part = %v, want only partId and type", part)
		}
	}
	if item.BodyValues["1.1"].Value != "plain body" || item.BodyValues["1.2"].Value != "<p>html body</p>" {
		t.Errorf("bodyValues = %+v, want both text and html values", item.BodyValues)
	}
}

func TestEmailGetFetchAllBodyValues(t *testing.T) {
	handler, ctx := newPropertyTestHandler(t)
	result, err := handler.EmailGet(ctx, "test@example.com", json.RawMessage(
		`{"accountId":"test@example.com","properties":["id","bodyValues"],"fetchAllBodyValues":true}`))
	if err != nil {
		t.Fatalf("EmailGet() error = %v", err)
	}

	var resp struct {
		List []struct {
			BodyValues map[string]struct {
				Value string `json:"value"`
			} `json:"bodyValues"`
		} `json:"list"`
	}
	if err := json.Unmarshal(result, &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	values := resp.List[0].BodyValues
	if values["1.1"].Value != "plain body" || values["1.2"].Value != "<p>html body</p>" {
		t.Errorf("bodyValues = %+v, want text and html parts", values)
	}
}

func TestEmailGetMaxBodyValueBytes(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemoryStorage()
	addTestEmail(store, "mail1", "test@example.com", "inbox", &email.UserEmail{
		From: email.Address{Address: "sender@example.com"},
		Date: time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC),
		Text: strings.Repeat("é", 10), // two bytes per rune
		HTML: `<p>hello</p><a href="https://example.com">link</a>`,
	})
	handler := NewHandler(store, metadata.NewMemoryMetadata(),
		usermanager.NewNone(usermanager.Config{AdminUsername: "admin", AdminPassword: "password"}),
		sender.NewLogSender())

	result, err := handler.EmailGet(ctx, "test@example.com", json.RawMessage(
		`{"accountId":"test@example.com","properties":["id","bodyValues"],`+
			`"fetchTextBodyValues":true,"fetchHTMLBodyValues":true,"maxBodyValueBytes":5}`))
	if err != nil {
		t.Fatalf("EmailGet() error = %v", err)
	}

	var resp struct {
		List []struct {
			BodyValues map[string]struct {
				Value       string `json:"value"`
				IsTruncated bool   `json:"isTruncated"`
			} `json:"bodyValues"`
		} `json:"list"`
	}
	if err := json.Unmarshal(result, &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	values := resp.List[0].BodyValues
	text := values["1.1"]
	if !text.IsTruncated || text.Value != "éé" || !utf8.ValidString(text.Value) {
		t.Errorf("truncated text = %q, truncated=%v; want valid UTF-8 %q", text.Value, text.IsTruncated, "éé")
	}
	html := values["1.2"]
	if !html.IsTruncated || html.Value != "<p>he" {
		t.Errorf("truncated html = %q, truncated=%v; want %q", html.Value, html.IsTruncated, "<p>he")
	}

	// A 20-byte limit lands inside <a href="...">; the value must stop at the
	// end of the preceding complete tag.
	result, err = handler.EmailGet(ctx, "test@example.com", json.RawMessage(
		`{"accountId":"test@example.com","properties":["id","bodyValues"],`+
			`"fetchHTMLBodyValues":true,"maxBodyValueBytes":20}`))
	if err != nil {
		t.Fatalf("EmailGet() with html limit error = %v", err)
	}
	if err := json.Unmarshal(result, &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	html = resp.List[0].BodyValues["1.2"]
	if !html.IsTruncated || html.Value != "<p>hello</p>" {
		t.Errorf("html at tag boundary = %q, truncated=%v; want %q", html.Value, html.IsTruncated, "<p>hello</p>")
	}

	result, err = handler.EmailGet(ctx, "test@example.com", json.RawMessage(
		`{"accountId":"test@example.com","properties":["id","bodyValues"],"fetchTextBodyValues":true}`))
	if err != nil {
		t.Fatalf("EmailGet() without limit error = %v", err)
	}
	if err := json.Unmarshal(result, &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if untruncated := resp.List[0].BodyValues["1.1"]; untruncated.IsTruncated || untruncated.Value != strings.Repeat("é", 10) {
		t.Errorf("untruncated text = %q, truncated=%v", untruncated.Value, untruncated.IsTruncated)
	}
}

// multipartWireFixture is a delivered message with a real MIME tree, an HTML
// alternative, a base64 attachment and a custom header. It is stored verbatim
// so JMAP properties are checked against the original wire bytes.
func multipartWireFixture() []byte {
	lines := []string{
		"From: Sender Name <sender@example.com>",
		"Sender: Delegate <delegate@example.com>",
		"To: recipient@example.com",
		"Subject: MIME fixture",
		"Date: Mon, 02 Jan 2006 15:04:05 -0700",
		"Message-ID: <mime-fixture@example.com>",
		"X-Custom-Header: custom-value",
		"MIME-Version: 1.0",
		`Content-Type: multipart/mixed; boundary="outer"`,
		"",
		"--outer",
		`Content-Type: multipart/alternative; boundary="inner"`,
		"",
		"--inner",
		"Content-Type: text/plain; charset=utf-8",
		"",
		"plain fixture",
		"--inner",
		"Content-Type: text/html; charset=utf-8",
		"",
		"<p>html fixture</p>",
		"--inner--",
		"--outer",
		`Content-Type: application/octet-stream; name="report.txt"`,
		`Content-Disposition: attachment; filename="report.txt"`,
		"Content-Transfer-Encoding: base64",
		"",
		"cmVwb3J0IGJvZHk=",
		"--outer--",
		"",
	}
	return []byte(strings.Join(lines, "\r\n"))
}

func TestEmailGetRealMIMEProperties(t *testing.T) {
	ctx := context.Background()
	raw := multipartWireFixture()
	store := storage.NewMemoryStorage()
	id, err := store.AddEmail(ctx, "test@example.com", "inbox", &email.UserEmail{
		From:      email.Address{Address: "sender@example.com"},
		To:        []email.Address{{Address: "test@example.com"}},
		Subject:   "MIME fixture",
		Date:      time.Date(2006, 1, 2, 22, 4, 5, 0, time.UTC),
		MessageID: "<mime-fixture@example.com>",
		Text:      "plain fixture",
		HTML:      "<p>html fixture</p>",
	}, raw)
	if err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(store, metadata.NewMemoryMetadata(),
		usermanager.NewNone(usermanager.Config{AdminUsername: "admin", AdminPassword: "password"}),
		sender.NewLogSender())

	result, err := handler.EmailGet(ctx, "test@example.com", json.RawMessage(
		`{"ids":["`+id+`"],"properties":["id","blobId","threadId","size","sender","hasAttachment",`+
			`"bodyStructure","textBody","htmlBody","attachments","headers","bodyValues","header:X-Custom-Header"],`+
			`"fetchTextBodyValues":true,"fetchHTMLBodyValues":true}`))
	if err != nil {
		t.Fatalf("EmailGet() error = %v", err)
	}
	var resp struct {
		List []struct {
			ID       string `json:"id"`
			BlobID   string `json:"blobId"`
			ThreadID string `json:"threadId"`
			Size     int    `json:"size"`
			Sender   []struct {
				Email string `json:"email"`
			} `json:"sender"`
			HasAttachment bool `json:"hasAttachment"`
			BodyStructure struct {
				Type     string  `json:"type"`
				PartID   *string `json:"partId"`
				SubParts []struct {
					Type     string  `json:"type"`
					PartID   *string `json:"partId"`
					SubParts []struct {
						Type   string  `json:"type"`
						PartID *string `json:"partId"`
					} `json:"subParts"`
				} `json:"subParts"`
			} `json:"bodyStructure"`
			TextBody []struct {
				PartID string `json:"partId"`
			} `json:"textBody"`
			HTMLBody []struct {
				PartID string `json:"partId"`
			} `json:"htmlBody"`
			Attachments []struct {
				PartID      string `json:"partId"`
				Name        string `json:"name"`
				Disposition string `json:"disposition"`
				BlobID      string `json:"blobId"`
				Size        int    `json:"size"`
			} `json:"attachments"`
			Headers []struct {
				Name  string `json:"name"`
				Value string `json:"value"`
			} `json:"headers"`
			BodyValues map[string]struct {
				Value string `json:"value"`
			} `json:"bodyValues"`
			CustomHeader string `json:"header:X-Custom-Header"`
		} `json:"list"`
	}
	if err := json.Unmarshal(result, &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if len(resp.List) != 1 {
		t.Fatalf("list length = %d, want 1", len(resp.List))
	}
	item := resp.List[0]
	if item.BlobID != storage.BlobID(raw) || item.Size != len(raw) {
		t.Errorf("blobId/size = %q/%d, want %q/%d", item.BlobID, item.Size, storage.BlobID(raw), len(raw))
	}
	if item.ThreadID == "" || item.ThreadID == item.ID {
		t.Errorf("threadId = %q, want stable id distinct from email id %q", item.ThreadID, item.ID)
	}
	if len(item.Sender) != 1 || item.Sender[0].Email != "delegate@example.com" {
		t.Errorf("sender = %+v, want Sender header address", item.Sender)
	}
	if item.BodyStructure.Type != "multipart/mixed" || item.BodyStructure.PartID != nil {
		t.Errorf("bodyStructure = %+v, want multipart root with no partId", item.BodyStructure)
	}
	if len(item.BodyStructure.SubParts) != 2 || item.BodyStructure.SubParts[0].Type != "multipart/alternative" {
		t.Fatalf("bodyStructure subParts = %+v", item.BodyStructure.SubParts)
	}
	if len(item.TextBody) != 1 || item.TextBody[0].PartID != "1.1.1" {
		t.Errorf("textBody = %+v, want part 1.1.1", item.TextBody)
	}
	if len(item.HTMLBody) != 1 || item.HTMLBody[0].PartID != "1.1.2" {
		t.Errorf("htmlBody = %+v, want part 1.1.2", item.HTMLBody)
	}
	if !item.HasAttachment || len(item.Attachments) != 1 {
		t.Fatalf("hasAttachment/attachments = %v/%+v, want true/one part", item.HasAttachment, item.Attachments)
	}
	attachment := item.Attachments[0]
	attachmentBytes := []byte("report body")
	if attachment.PartID != "1.2" || attachment.Name != "report.txt" || attachment.Disposition != "attachment" ||
		attachment.BlobID != storage.BlobID(attachmentBytes) || attachment.Size != len(attachmentBytes) {
		t.Errorf("attachment = %+v, want decoded report.txt at part 1.2", attachment)
	}
	if item.BodyValues["1.1.1"].Value != "plain fixture" || item.BodyValues["1.1.2"].Value != "<p>html fixture</p>" {
		t.Errorf("bodyValues = %+v, want values keyed by real part ids", item.BodyValues)
	}
	if item.CustomHeader != "custom-value" {
		t.Errorf("header:X-Custom-Header = %q, want custom-value", item.CustomHeader)
	}
	foundSubject, foundCustom := false, false
	for _, header := range item.Headers {
		if header.Name == "Subject" && header.Value == "MIME fixture" {
			foundSubject = true
		}
		if header.Name == "X-Custom-Header" && header.Value == "custom-value" {
			foundCustom = true
		}
	}
	if !foundSubject || !foundCustom {
		t.Errorf("headers = %+v, want parsed Subject and X-Custom-Header", item.Headers)
	}
	if !bytes.Contains(raw, []byte("X-Custom-Header")) {
		t.Fatal("fixture lost its custom header")
	}
}

// TestRFC8621PropertyTables asserts that every property in the RFC 8621
// section 1.1 tables for Email and Mailbox appears when properties is null.
func TestRFC8621PropertyTables(t *testing.T) {
	handler, ctx := newPropertyTestHandler(t)
	emailProperties := []string{
		"id", "blobId", "threadId", "mailboxIds", "keywords", "size", "receivedAt",
		"messageId", "inReplyTo", "references", "sender", "from", "to", "cc", "bcc",
		"replyTo", "subject", "sentAt", "hasAttachment", "preview", "bodyStructure",
		"bodyValues", "textBody", "htmlBody", "attachments", "headers",
	}
	emailResult, err := handler.EmailGet(ctx, "test@example.com", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("EmailGet() error = %v", err)
	}
	_, emailObj := getListKeys(t, emailResult)
	for _, name := range emailProperties {
		if _, ok := emailObj[name]; !ok {
			t.Errorf("Email/get omitted RFC 8621 property %q", name)
		}
	}

	mailboxProperties := []string{
		"id", "name", "parentId", "role", "sortOrder", "totalEmails", "unreadEmails",
		"totalThreads", "unreadThreads", "myRights", "isSubscribed",
	}
	mailboxResult, err := handler.MailboxGet(ctx, "test@example.com", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("MailboxGet() error = %v", err)
	}
	var mailboxResp struct {
		List []map[string]json.RawMessage `json:"list"`
	}
	if err := json.Unmarshal(mailboxResult, &mailboxResp); err != nil {
		t.Fatalf("unmarshal Mailbox/get response: %v", err)
	}
	if len(mailboxResp.List) == 0 {
		t.Fatal("Mailbox/get returned no mailboxes")
	}
	mailboxObj := mailboxResp.List[0]
	for _, name := range mailboxProperties {
		if _, ok := mailboxObj[name]; !ok {
			t.Errorf("Mailbox/get omitted RFC 8621 property %q", name)
		}
	}
	for _, right := range []string{
		"mayReadItems", "mayAddItems", "mayRemoveItems", "maySetSeen", "maySetKeywords",
		"mayCreateChild", "mayRename", "mayDelete", "maySubmit",
	} {
		var rights map[string]json.RawMessage
		if err := json.Unmarshal(mailboxObj["myRights"], &rights); err != nil {
			t.Fatalf("unmarshal myRights: %v", err)
		}
		if _, ok := rights[right]; !ok {
			t.Errorf("Mailbox/myRights omitted %q", right)
		}
	}
}

func TestEmailSetAtomicity(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemoryStorage()
	id, err := store.AddEmail(ctx, "alice@example.com", "inbox", &email.UserEmail{
		From:    email.Address{Address: "alice@example.com"},
		Subject: "Atomicity",
		Text:    "body",
		Date:    time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	meta := metadata.NewMemoryMetadata()
	record := metadata.NewEmailMetadata("alice@example.com", id)
	if err := meta.Save(ctx, record); err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(store, meta,
		usermanager.NewNone(usermanager.Config{AdminUsername: "admin", AdminPassword: "password"}),
		sender.NewLogSender())

	// A valid mailbox move paired with an invalid keyword must leave the email
	// exactly as it was: no move and no keyword change.
	result, err := handler.EmailSet(ctx, "alice@example.com", json.RawMessage(
		`{"update":{"`+id+`":{"mailboxIds":{"archive":true},"keywords/bad key":true}}}`))
	if err != nil {
		t.Fatalf("EmailSet() error = %v", err)
	}
	var resp struct {
		Updated    map[string]json.RawMessage `json:"updated"`
		NotUpdated map[string]struct {
			Type string `json:"type"`
		} `json:"notUpdated"`
	}
	if err := json.Unmarshal(result, &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if _, ok := resp.Updated[id]; ok {
		t.Error("invalid update was reported as updated")
	}
	if resp.NotUpdated[id].Type != "invalidProperties" {
		t.Errorf("notUpdated type = %q, want invalidProperties", resp.NotUpdated[id].Type)
	}
	listed, err := store.GetEmail(ctx, "alice@example.com", id)
	if err != nil {
		t.Fatal(err)
	}
	if listed.Ref.MailboxID != "inbox" {
		t.Errorf("mailbox after failed update = %q, want inbox", listed.Ref.MailboxID)
	}
	storedMeta, err := meta.Get(ctx, "alice@example.com", id)
	if err != nil {
		t.Fatal(err)
	}
	if len(storedMeta.Keywords) != 0 {
		t.Errorf("keywords after failed update = %+v, want empty", storedMeta.Keywords)
	}
}
