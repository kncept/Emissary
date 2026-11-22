package jmap

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/kncept/Emissary/mailer/components/metadata"
	"github.com/kncept/Emissary/mailer/components/sender"
	"github.com/kncept/Emissary/mailer/components/storage"
	"github.com/kncept/Emissary/mailer/components/usermanager"
	mailemail "github.com/kncept/Emissary/mailer/email"
)

func newArgumentHandler(t *testing.T) *Handler {
	t.Helper()
	return NewHandler(storage.NewMemoryStorage(), metadata.NewMemoryMetadata(),
		usermanager.NewNone(usermanager.Config{AdminUsername: "admin", AdminPassword: "password"}),
		sender.NewLogSender())
}

// TestJMAPArgumentsRejected asserts the exact RFC 8620 error type for every
// argument that this server deliberately refuses to honour (section 3.9).
func TestJMAPArgumentsRejected(t *testing.T) {
	const accountID = "alice@example.com"
	h := newArgumentHandler(t)
	ctx := context.Background()

	tests := []struct {
		name string
		call func() error
		want string
	}{
		{
			name: "email sort on unsupported property",
			call: func() error {
				_, err := h.EmailQuery(ctx, accountID, json.RawMessage(`{"sort":[{"property":"subject"}]}`))
				return err
			},
			want: "unsupportedSort",
		},
		{
			name: "email sort with unsupported collation",
			call: func() error {
				_, err := h.EmailQuery(ctx, accountID, json.RawMessage(`{"sort":[{"property":"receivedAt","collation":"en;locale"}]}`))
				return err
			},
			want: "unsupportedSort",
		},
		{
			name: "email multi-key sort",
			call: func() error {
				_, err := h.EmailQuery(ctx, accountID, json.RawMessage(`{"sort":[{"property":"receivedAt"},{"property":"subject"}]}`))
				return err
			},
			want: "unsupportedSort",
		},
		{
			name: "email lowercase operator",
			call: func() error {
				_, err := h.EmailQuery(ctx, accountID, json.RawMessage(`{"filter":{"operator":"or","conditions":[{},{}]}}`))
				return err
			},
			want: "invalidArguments",
		},
		{
			name: "email header must be a string array",
			call: func() error {
				_, err := h.EmailQuery(ctx, accountID, json.RawMessage(`{"filter":{"header":[{"name":"X","value":"y"}]}}`))
				return err
			},
			want: "invalidArguments",
		},
		{
			name: "email invalid before date",
			call: func() error {
				_, err := h.EmailQuery(ctx, accountID, json.RawMessage(`{"filter":{"before":"yesterday"}}`))
				return err
			},
			want: "invalidArguments",
		},
		{
			name: "email anchor not found",
			call: func() error {
				_, err := h.EmailQuery(ctx, accountID, json.RawMessage(`{"anchor":"missing-id"}`))
				return err
			},
			want: "anchorNotFound",
		},
		{
			name: "mailbox sort on unsupported property",
			call: func() error {
				_, err := h.MailboxQuery(ctx, accountID, json.RawMessage(`{"sort":[{"property":"subject"}]}`))
				return err
			},
			want: "unsupportedSort",
		},
		{
			name: "mailbox compound filter",
			call: func() error {
				_, err := h.MailboxQuery(ctx, accountID, json.RawMessage(`{"filter":{"operator":"and","conditions":[]}}`))
				return err
			},
			want: "unsupportedFilter",
		},
		{
			name: "mailbox anchor not found",
			call: func() error {
				_, err := h.MailboxQuery(ctx, accountID, json.RawMessage(`{"anchor":"missing-id"}`))
				return err
			},
			want: "anchorNotFound",
		},
		{
			name: "submission sendAt",
			call: func() error {
				_, _, err := h.EmailSubmissionSetWithImplicit(ctx, accountID, json.RawMessage(
					`{"create":{"s1":{"identityId":"primary","emailId":"e1","sendAt":"2030-01-01T00:00:00Z"}}}`))
				return err
			},
			want: "invalidArguments",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.call()
			if err == nil {
				t.Fatalf("expected %s error, got nil", tc.want)
			}
			if got := jmapErrorType(err); got != tc.want {
				t.Errorf("error type = %q, want %q (error: %v)", got, tc.want, err)
			}
		})
	}
}

func TestJMAPQueryAnchorAndTotal(t *testing.T) {
	const accountID = "alice@example.com"
	store := storage.NewMemoryStorage()
	for _, id := range []string{"e1", "e2", "e3"} {
		addTestEmail(store, id, accountID, "inbox", &mailemail.UserEmail{Subject: id})
	}
	h := NewHandler(store, metadata.NewMemoryMetadata(),
		usermanager.NewNone(usermanager.Config{AdminUsername: "admin", AdminPassword: "password"}),
		sender.NewLogSender())
	ctx := context.Background()

	raw, err := h.EmailQuery(ctx, accountID, json.RawMessage(
		`{"anchor":"e1","anchorOffset":1,"limit":1}`))
	if err != nil {
		t.Fatalf("EmailQuery(anchor) error = %v", err)
	}
	var anchored struct {
		IDs []string `json:"ids"`
	}
	if err := json.Unmarshal(raw, &anchored); err != nil {
		t.Fatalf("unmarshal anchored response: %v", err)
	}
	if len(anchored.IDs) != 1 || anchored.IDs[0] != "e2" {
		t.Errorf("anchor ids = %v, want [e2]", anchored.IDs)
	}

	for _, tc := range []struct {
		args      string
		wantTotal bool
	}{
		{args: `{}`, wantTotal: false},
		{args: `{"calculateTotal":false}`, wantTotal: false},
		{args: `{"calculateTotal":true}`, wantTotal: true},
	} {
		raw, err := h.EmailQuery(ctx, accountID, json.RawMessage(tc.args))
		if err != nil {
			t.Fatalf("EmailQuery(%s) error = %v", tc.args, err)
		}
		var response map[string]json.RawMessage
		if err := json.Unmarshal(raw, &response); err != nil {
			t.Fatalf("unmarshal response: %v", err)
		}
		_, present := response["total"]
		if present != tc.wantTotal {
			t.Errorf("EmailQuery(%s) total present = %v, want %v", tc.args, present, tc.wantTotal)
		}
	}
}

func TestJMAPMaxSizeIsStrict(t *testing.T) {
	const accountID = "alice@example.com"
	store := storage.NewMemoryStorage()
	addTestEmail(store, "e1", accountID, "inbox", &mailemail.UserEmail{Subject: "sized", Size: 100})
	h := NewHandler(store, metadata.NewMemoryMetadata(),
		usermanager.NewNone(usermanager.Config{AdminUsername: "admin", AdminPassword: "password"}),
		sender.NewLogSender())
	ctx := context.Background()
	rawEmail, err := store.GetEmailRaw(ctx, accountID, "e1")
	if err != nil {
		t.Fatalf("GetEmailRaw() error = %v", err)
	}
	size := len(rawEmail)

	for _, tc := range []struct {
		maxSize int
		want    int
	}{
		{maxSize: size, want: 0},
		{maxSize: size + 1, want: 1},
	} {
		queryRaw, err := h.EmailQuery(ctx, accountID, json.RawMessage(
			`{"filter":{"maxSize":`+jsonNumber(tc.maxSize)+`}}`))
		if err != nil {
			t.Fatalf("EmailQuery(maxSize=%d) error = %v", tc.maxSize, err)
		}
		var response struct {
			IDs []string `json:"ids"`
		}
		if err := json.Unmarshal(queryRaw, &response); err != nil {
			t.Fatalf("unmarshal response: %v", err)
		}
		if len(response.IDs) != tc.want {
			t.Errorf("maxSize %d matched %d emails, want %d", tc.maxSize, len(response.IDs), tc.want)
		}
	}
}

func jsonNumber(value int) string {
	data, _ := json.Marshal(value)
	return string(data)
}

// TestQueryChangesUpToIDReturnsValidDiff verifies RFC 8620 section 5.6:
// upToId is accepted and, because the server's sorts and filters may use
// mutable properties, ignored in favour of the complete (valid) diff.
func TestQueryChangesUpToIDReturnsValidDiff(t *testing.T) {
	const accountID = "alice@example.com"
	h := newArgumentHandler(t)
	ctx := context.Background()

	raw, err := h.MailboxQuery(ctx, accountID, json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("MailboxQuery() error = %v", err)
	}
	var query struct {
		QueryState string `json:"queryState"`
	}
	if err := json.Unmarshal(raw, &query); err != nil {
		t.Fatalf("unmarshal query response: %v", err)
	}
	if query.QueryState == "" {
		t.Fatal("MailboxQuery() returned no queryState")
	}

	raw, err = h.MailboxQueryChanges(ctx, accountID,
		json.RawMessage(fmt.Sprintf(`{"sinceQueryState":%q,"upToId":"inbox"}`, query.QueryState)))
	if err != nil {
		t.Fatalf("MailboxQueryChanges(upToId) error = %v", err)
	}
	var diff map[string]json.RawMessage
	if err := json.Unmarshal(raw, &diff); err != nil {
		t.Fatalf("unmarshal queryChanges response: %v", err)
	}
	for _, field := range []string{"oldQueryState", "newQueryState", "added", "removed"} {
		if _, ok := diff[field]; !ok {
			t.Errorf("queryChanges response is missing %q: %s", field, raw)
		}
	}
}
