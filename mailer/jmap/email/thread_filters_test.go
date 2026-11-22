package email

import (
	"context"
	"encoding/json"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/kncept/Emissary/mailer/components/metadata"
	"github.com/kncept/Emissary/mailer/components/storage"
	mailemail "github.com/kncept/Emissary/mailer/email"
	"github.com/kncept/Emissary/mailer/jmap/internal/base"
)

const threadFilterAccount = "alice@example.com"

func seedThreadedEmails(t *testing.T, store *storage.MemoryStorage, metaStore *metadata.MemoryMetadata,
	ctx context.Context) {
	t.Helper()
	messages := []struct {
		id        string
		subject   string
		messageID string
		inReplyTo []string
		received  string
		keywords  []string
	}{
		{"e1", "Project update", "<m1@example.com>", nil, "2026-01-01T09:00:00Z", []string{metadata.KeywordFlagged}},
		{"e2", "Re: Project update", "<m2@example.com>", []string{"<m1@example.com>"}, "2026-01-01T10:00:00Z", nil},
		{"e3", "Re: Re: Project update", "<m3@example.com>", []string{"<m2@example.com>"}, "2026-01-01T11:00:00Z", []string{metadata.KeywordFlagged}},
	}
	for _, message := range messages {
		received, err := time.Parse(time.RFC3339, message.received)
		if err != nil {
			t.Fatal(err)
		}
		store.Add(&mailemail.ListedEmail{
			Ref: &mailemail.EmailRef{ID: message.id, AccountID: threadFilterAccount, MailboxID: "inbox"},
			Email: &mailemail.UserEmail{
				Subject:    message.subject,
				MessageID:  message.messageID,
				InReplyTo:  message.inReplyTo,
				References: append(append([]string(nil), message.inReplyTo...), message.messageID),
				Date:       received,
			},
		})
		meta := metadata.NewEmailMetadata(threadFilterAccount, message.id, message.keywords...)
		meta.ReceivedAt = metadata.FormatReceivedAt(received)
		if err := metaStore.Save(ctx, meta); err != nil {
			t.Fatalf("Save(%s) error = %v", message.id, err)
		}
	}
}

func threadFilterHandler(t *testing.T) (*Handler, context.Context) {
	t.Helper()
	store := storage.NewMemoryStorage()
	metaStore := metadata.NewMemoryMetadata()
	ctx := context.Background()
	seedThreadedEmails(t, store, metaStore, ctx)
	deps := &base.Deps{
		Storage:  store,
		Metadata: metaStore,
		Changes:  base.NewTracker(store),
	}
	return New(deps), ctx
}

func queryIDs(t *testing.T, h *Handler, ctx context.Context, args string) []string {
	t.Helper()
	raw, err := h.EmailQuery(ctx, threadFilterAccount, json.RawMessage(args))
	if err != nil {
		t.Fatalf("EmailQuery(%s) error = %v", args, err)
	}
	var response struct {
		IDs []string `json:"ids"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		t.Fatalf("unmarshal EmailQuery response: %v (raw %s)", err, raw)
	}
	if response.IDs == nil {
		response.IDs = []string{}
	}
	sort.Strings(response.IDs)
	return response.IDs
}

func TestThreadKeywordFiltersUseGroupedMembers(t *testing.T) {
	h, ctx := threadFilterHandler(t)
	all := []string{"e1", "e2", "e3"}
	cases := []struct {
		name string
		args string
		want []string
	}{
		{"allInThreadHaveKeyword fails when one replies lacks it",
			`{"filter":{"allInThreadHaveKeyword":"$flagged"}}`, []string{}},
		{"someInThreadHaveKeyword matches the whole thread",
			`{"filter":{"someInThreadHaveKeyword":"$flagged"}}`, all},
		{"noneInThreadHaveKeyword fails when any member has it",
			`{"filter":{"noneInThreadHaveKeyword":"$flagged"}}`, []string{}},
		{"noneInThreadHaveKeyword with absent keyword matches",
			`{"filter":{"noneInThreadHaveKeyword":"$seen"}}`, all},
		{"allInThreadHaveKeyword with absent keyword matches nobody",
			`{"filter":{"allInThreadHaveKeyword":"$seen"}}`, []string{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := queryIDs(t, h, ctx, tc.args)
			want := append([]string(nil), tc.want...)
			if want == nil {
				want = []string{}
			}
			sort.Strings(want)
			if !reflect.DeepEqual(got, want) {
				t.Errorf("ids = %v, want %v", got, want)
			}
		})
	}
}

func TestEmailGetExposesSharedThreadID(t *testing.T) {
	h, ctx := threadFilterHandler(t)
	raw, err := h.EmailGet(ctx, threadFilterAccount,
		json.RawMessage(`{"ids":["e1","e2","e3"],"properties":["id","threadId"]}`))
	if err != nil {
		t.Fatalf("EmailGet() error = %v", err)
	}
	var response struct {
		List []struct {
			ID       string `json:"id"`
			ThreadID string `json:"threadId"`
		} `json:"list"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		t.Fatalf("unmarshal EmailGet response: %v (raw %s)", err, raw)
	}
	if len(response.List) != 3 {
		t.Fatalf("list = %+v, want three emails", response.List)
	}
	threadID := response.List[0].ThreadID
	if threadID == "" || threadID == "e1" {
		t.Fatalf("threadId = %q, want a stable digest", threadID)
	}
	for _, email := range response.List[1:] {
		if email.ThreadID != threadID {
			t.Errorf("email %s threadId = %q, want shared %q", email.ID, email.ThreadID, threadID)
		}
	}
}
