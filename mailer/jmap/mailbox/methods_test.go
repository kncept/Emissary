package mailbox

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"testing"

	"github.com/kncept/Emissary/mailer/components/metadata"
	"github.com/kncept/Emissary/mailer/components/storage"
	mailemail "github.com/kncept/Emissary/mailer/email"
	"github.com/kncept/Emissary/mailer/jmap/internal/base"
)

const mailboxTestAccount = "alice@example.com"

func newMailboxTest(t *testing.T) (*Handler, *storage.MemoryStorage, context.Context) {
	t.Helper()
	store := storage.NewMemoryStorage()
	deps := &base.Deps{
		Storage:  store,
		Metadata: metadata.NewMemoryMetadata(),
		Changes:  base.NewTracker(store),
	}
	return New(deps), store, context.Background()
}

// seedMailboxes adds custom top-level mailboxes alongside the six role
// mailboxes every account starts with.
func seedMailboxes(t *testing.T, store *storage.MemoryStorage, ctx context.Context) {
	t.Helper()
	for _, mb := range []storage.Mailbox{
		{ID: "alpha", Name: "Alpha", SortOrder: 5, IsSubscribed: true},
		{ID: "beta", Name: "Beta", SortOrder: 2, IsSubscribed: false},
		{ID: "special", Name: "Special", Role: stringPtr("archive"), SortOrder: 9, IsSubscribed: true},
	} {
		if _, err := store.CreateMailbox(ctx, mailboxTestAccount, mb); err != nil {
			t.Fatalf("CreateMailbox(%s) error = %v", mb.ID, err)
		}
	}
}

func stringPtr(value string) *string { return &value }

func callMailboxQuery(t *testing.T, h *Handler, ctx context.Context, args string) []string {
	t.Helper()
	raw, err := h.MailboxQuery(ctx, mailboxTestAccount, json.RawMessage(args))
	if err != nil {
		t.Fatalf("MailboxQuery(%s) error = %v", args, err)
	}
	var response struct {
		IDs []string `json:"ids"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		t.Fatalf("unmarshal MailboxQuery response: %v", err)
	}
	return response.IDs
}

// TestMailboxQueryFilters is the table-driven acceptance test for RFC 8621
// section 2.3 FilterCondition support.
func TestMailboxQueryFilters(t *testing.T) {
	h, store, ctx := newMailboxTest(t)
	seedMailboxes(t, store, ctx)

	cases := []struct {
		name string
		args string
		want []string
	}{
		{"all mailboxes", `{}`, []string{"inbox", "drafts", "sent", "archive", "junk", "trash", "alpha", "beta", "special"}},
		{"name contains", `{"filter":{"name":"alp"}}`, []string{"alpha"}},
		{"role exact", `{"filter":{"role":"archive"}}`, []string{"archive", "special"}},
		{"hasAnyRole false", `{"filter":{"hasAnyRole":false}}`, []string{"alpha", "beta"}},
		{"hasAnyRole true", `{"filter":{"hasAnyRole":true}}`, []string{"inbox", "drafts", "sent", "archive", "junk", "trash", "special"}},
		{"isSubscribed false", `{"filter":{"isSubscribed":false}}`, []string{"beta"}},
		{"parentId null", `{"filter":{"parentId":null}}`, []string{"inbox", "drafts", "sent", "archive", "junk", "trash", "alpha", "beta", "special"}},
		{"no match", `{"filter":{"name":"nope"}}`, []string{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := callMailboxQuery(t, h, ctx, tc.args)
			sort.Strings(got)
			want := append([]string(nil), tc.want...)
			sort.Strings(want)
			if strings.Join(got, ",") != strings.Join(want, ",") {
				t.Errorf("ids = %v, want %v", got, want)
			}
		})
	}
}

// TestMailboxQuerySorts is the table-driven acceptance test for the two sorts
// RFC 8621 section 2.3 requires.
func TestMailboxQuerySorts(t *testing.T) {
	h, store, ctx := newMailboxTest(t)
	seedMailboxes(t, store, ctx)

	cases := []struct {
		name       string
		args       string
		wantFirst  string
		wantErrTyp string
	}{
		{"sortOrder ascending", `{"sort":[{"property":"sortOrder","isAscending":true}]}`, "inbox", ""},
		{"sortOrder descending", `{"sort":[{"property":"sortOrder","isAscending":false}]}`, "special", ""},
		{"name ascending", `{"sort":[{"property":"name","isAscending":true}]}`, "alpha", ""},
		{"name descending", `{"sort":[{"property":"name","isAscending":false}]}`, "trash", ""},
		{"unsupported property", `{"sort":[{"property":"subject"}]}`, "", "unsupportedSort"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.wantErrTyp != "" {
				_, err := h.MailboxQuery(ctx, mailboxTestAccount, json.RawMessage(tc.args))
				if err == nil || !strings.HasPrefix(err.Error(), tc.wantErrTyp+":") {
					t.Fatalf("MailboxQuery() error = %v, want %s", err, tc.wantErrTyp)
				}
				return
			}
			ids := callMailboxQuery(t, h, ctx, tc.args)
			if len(ids) == 0 || ids[0] != tc.wantFirst {
				t.Errorf("first id = %v, want %s", ids, tc.wantFirst)
			}
		})
	}
}

type mailboxSetResponse struct {
	Created      map[string]json.RawMessage `json:"created"`
	Destroyed    []string                   `json:"destroyed"`
	NotCreated   map[string]base.SetError   `json:"notCreated"`
	NotDestroyed map[string]base.SetError   `json:"notDestroyed"`
}

func callMailboxSet(t *testing.T, h *Handler, ctx context.Context, args string) mailboxSetResponse {
	t.Helper()
	raw, err := h.MailboxSet(ctx, mailboxTestAccount, json.RawMessage(args))
	if err != nil {
		t.Fatalf("MailboxSet(%s) error = %v", args, err)
	}
	var response mailboxSetResponse
	if err := json.Unmarshal(raw, &response); err != nil {
		t.Fatalf("unmarshal MailboxSet response: %v (raw %s)", err, raw)
	}
	return response
}

// TestMailboxSetErrors is the table-driven acceptance test for the extra
// SetErrors and onDestroyRemoveEmails defined by RFC 8621 section 2.5.
func TestMailboxSetErrors(t *testing.T) {
	cases := []struct {
		name       string
		seed       func(t *testing.T, store *storage.MemoryStorage, ctx context.Context)
		args       string
		wantSetErr string
		wantGone   bool
	}{
		{
			name: "mailboxHasChild",
			seed: func(t *testing.T, store *storage.MemoryStorage, ctx context.Context) {
				seedMailboxes(t, store, ctx)
				if _, err := store.CreateMailbox(ctx, mailboxTestAccount, storage.Mailbox{
					ID: "child", Name: "Child", ParentID: stringPtr("alpha"),
				}); err != nil {
					t.Fatalf("CreateMailbox(child) error = %v", err)
				}
			},
			args:       `{"destroy":["alpha"]}`,
			wantSetErr: "mailboxHasChild",
		},
		{
			name: "mailboxHasEmail",
			seed: func(t *testing.T, store *storage.MemoryStorage, ctx context.Context) {
				addMailboxEmail(t, store, "alpha", "e1")
			},
			args:       `{"destroy":["alpha"]}`,
			wantSetErr: "mailboxHasEmail",
		},
		{
			name: "onDestroyRemoveEmails destroys contained email",
			seed: func(t *testing.T, store *storage.MemoryStorage, ctx context.Context) {
				addMailboxEmail(t, store, "alpha", "e1")
			},
			args:     `{"destroy":["alpha"],"onDestroyRemoveEmails":true}`,
			wantGone: true,
		},
		{
			name: "role mailbox cannot be destroyed",
			args: `{"destroy":["inbox"]}`,
			// Role mailboxes are protected with forbidden, not a mailbox
			// SetError; assert through the generic path below.
			wantSetErr: "forbidden",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, store, ctx := newMailboxTest(t)
			if tc.seed != nil {
				tc.seed(t, store, ctx)
			}
			result := callMailboxSet(t, h, ctx, tc.args)
			if tc.wantSetErr != "" {
				got := result.NotDestroyed["alpha"]
				if got.Type == "" {
					got = result.NotDestroyed["inbox"]
				}
				if got.Type != tc.wantSetErr {
					t.Fatalf("notDestroyed = %+v, want %q (full response %+v)", result.NotDestroyed, tc.wantSetErr, result)
				}
				return
			}
			if len(result.Destroyed) != 1 || result.Destroyed[0] != "alpha" {
				t.Fatalf("destroyed = %v, want [alpha]", result.Destroyed)
			}
			if tc.wantGone {
				if _, err := store.GetEmail(ctx, mailboxTestAccount, "e1"); err == nil {
					t.Error("email in destroyed mailbox still present")
				}
			}
		})
	}
}

func addMailboxEmail(t *testing.T, store *storage.MemoryStorage, mailboxID, id string) {
	t.Helper()
	store.Add(&mailemail.ListedEmail{
		Ref:   &mailemail.EmailRef{ID: id, AccountID: mailboxTestAccount, MailboxID: mailboxID},
		Email: &mailemail.UserEmail{Subject: "in " + mailboxID},
	})
}

func TestMailboxNameLengthLimit(t *testing.T) {
	h, _, ctx := newMailboxTest(t)
	longName := strings.Repeat("x", maxMailboxNameBytes+1)
	quoted, err := json.Marshal(longName)
	if err != nil {
		t.Fatal(err)
	}
	result := callMailboxSet(t, h, ctx, `{"create":{"c1":{"name":`+string(quoted)+`}}}`)
	got := result.NotCreated["c1"]
	if got.Type != "invalidProperties" {
		t.Fatalf("notCreated = %+v, want invalidProperties", result.NotCreated)
	}
	if len(got.Properties) != 1 || got.Properties[0] != "name" {
		t.Errorf("properties = %v, want [name]", got.Properties)
	}
}

func TestMailboxChangesReturnsUpdatedPropertiesField(t *testing.T) {
	h, _, ctx := newMailboxTest(t)
	// Establish the initial state with /get before asking for changes.
	initial, err := h.MailboxGet(ctx, mailboxTestAccount, json.RawMessage(`{"ids":null}`))
	if err != nil {
		t.Fatalf("MailboxGet() error = %v", err)
	}
	var getResponse struct {
		State string `json:"state"`
	}
	if err := json.Unmarshal(initial, &getResponse); err != nil {
		t.Fatalf("unmarshal MailboxGet response: %v", err)
	}
	raw, err := h.MailboxChanges(ctx, mailboxTestAccount,
		json.RawMessage(`{"sinceState":"`+getResponse.State+`"}`))
	if err != nil {
		t.Fatalf("MailboxChanges() error = %v", err)
	}
	var response map[string]json.RawMessage
	if err := json.Unmarshal(raw, &response); err != nil {
		t.Fatalf("unmarshal MailboxChanges response: %v", err)
	}
	value, present := response["updatedProperties"]
	if !present {
		t.Fatalf("response is missing updatedProperties: %s", raw)
	}
	if string(value) != "null" {
		t.Errorf("updatedProperties = %s, want null (RFC 8621 section 2.2 permits null)", value)
	}
}
