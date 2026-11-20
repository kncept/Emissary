package jmap

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/kncept/Emissary/mailer/email"
	"github.com/kncept/Emissary/mailer/metadata"
	"github.com/kncept/Emissary/mailer/sender"
	"github.com/kncept/Emissary/mailer/storage"
	"github.com/kncept/Emissary/mailer/usermanager"
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
	if item.BodyValues["text1"].Value != "plain body" || item.BodyValues["html1"].Value != "<p>html body</p>" {
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
	if values["text1"].Value != "plain body" || values["html1"].Value != "<p>html body</p>" {
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
	text := values["text1"]
	if !text.IsTruncated || text.Value != "éé" || !utf8.ValidString(text.Value) {
		t.Errorf("truncated text = %q, truncated=%v; want valid UTF-8 %q", text.Value, text.IsTruncated, "éé")
	}
	html := values["html1"]
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
	html = resp.List[0].BodyValues["html1"]
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
	if untruncated := resp.List[0].BodyValues["text1"]; untruncated.IsTruncated || untruncated.Value != strings.Repeat("é", 10) {
		t.Errorf("untruncated text = %q, truncated=%v", untruncated.Value, untruncated.IsTruncated)
	}
}
