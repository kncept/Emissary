package email

import (
	"context"
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/kncept/Emissary/mailer/components/metadata"
	"github.com/kncept/Emissary/mailer/components/storage"
	mailemail "github.com/kncept/Emissary/mailer/email"
	"github.com/kncept/Emissary/mailer/jmap/internal/base"
)

func newFilterFixture(t *testing.T) (*Handler, *storage.MemoryStorage, context.Context) {
	t.Helper()
	const accountID = "alice@example.com"
	store := storage.NewMemoryStorage()
	metaStore := metadata.NewMemoryMetadata()
	ctx := context.Background()

	messages := []struct {
		id        string
		subject   string
		from      string
		body      string
		messageID string
		inReplyTo []string
		received  string
		keywords  []string
	}{
		{"e1", "Project update", "alice@example.com", "launch", "<m1@example.com>", nil, "2026-01-01T09:00:00Z", []string{metadata.KeywordFlagged}},
		{"e2", "Re: Project update", "bob@example.com", "budget", "<m2@example.com>", []string{"<m1@example.com>"}, "2026-01-01T10:00:00Z", []string{metadata.KeywordSeen}},
		{"e3", "Re: Re: Project update", "carol@example.com", "launch", "<m3@example.com>", []string{"<m2@example.com>"}, "2026-01-01T11:00:00Z", []string{metadata.KeywordFlagged}},
		{"e4", "Quarterly report", "dave@example.com", "budget", "<m4@example.com>", nil, "2026-01-01T12:00:00Z", nil},
	}
	for _, message := range messages {
		received, err := time.Parse(time.RFC3339, message.received)
		if err != nil {
			t.Fatal(err)
		}
		store.Add(&mailemail.ListedEmail{
			Ref: &mailemail.EmailRef{ID: message.id, AccountID: accountID, MailboxID: "inbox"},
			Email: &mailemail.UserEmail{
				From:       mailemail.Address{Address: message.from},
				Subject:    message.subject,
				Text:       message.body,
				MessageID:  message.messageID,
				InReplyTo:  message.inReplyTo,
				References: append(append([]string(nil), message.inReplyTo...), message.messageID),
				Date:       received,
			},
		})
		meta := metadata.NewEmailMetadata(accountID, message.id, message.keywords...)
		meta.ReceivedAt = metadata.FormatReceivedAt(received)
		if err := metaStore.Save(ctx, meta); err != nil {
			t.Fatalf("Save(%s) error = %v", message.id, err)
		}
	}
	deps := &base.Deps{Storage: store, Metadata: metaStore, Changes: base.NewTracker(store)}
	return New(deps), store, ctx
}

func filterQueryIDs(t *testing.T, h *Handler, ctx context.Context, args string) []string {
	t.Helper()
	raw, err := h.EmailQuery(ctx, "alice@example.com", json.RawMessage(args))
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

func assertIDs(t *testing.T, got, want []string) {
	t.Helper()
	wantCopy := append([]string(nil), want...)
	if wantCopy == nil {
		wantCopy = []string{}
	}
	sort.Strings(wantCopy)
	if !reflect.DeepEqual(got, wantCopy) {
		t.Errorf("ids = %v, want %v", got, wantCopy)
	}
}

func TestFullEmailFilterConditions(t *testing.T) {
	h, _, ctx := newFilterFixture(t)

	cases := []struct {
		name string
		args string
		want []string
	}{
		{"AND composes text and keyword",
			`{"filter":{"operator":"AND","conditions":[{"text":"budget"},{"hasKeyword":"$seen"}]}}`,
			[]string{"e2"}},
		{"OR composes subject and from",
			`{"filter":{"operator":"OR","conditions":[{"subject":"Quarterly"},{"from":"bob"}]}}`,
			[]string{"e2", "e4"}},
		{"NOT excludes matching thread",
			`{"filter":{"operator":"NOT","conditions":[{"subject":"Project"}]}}`,
			[]string{"e4"}},
		{"nested operator",
			`{"filter":{"operator":"AND","conditions":[{"operator":"OR","conditions":[{"text":"launch"},{"text":"budget"}]},{"notKeyword":"$seen"}]}}`,
			[]string{"e1", "e3", "e4"}},
		{"header value matches decoded subject",
			`{"filter":{"header":["Subject","project"]}}`,
			[]string{"e1", "e2", "e3"}},
		{"header presence only",
			`{"filter":{"header":["X-Team"]}}`,
			[]string{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertIDs(t, filterQueryIDs(t, h, ctx, tc.args), tc.want)
		})
	}
}

func TestThreadIDFilterAndCollapseThreads(t *testing.T) {
	h, _, ctx := newFilterFixture(t)

	raw, err := h.EmailGet(ctx, "alice@example.com",
		json.RawMessage(`{"ids":["e1","e2","e3"],"properties":["id","threadId"]}`))
	if err != nil {
		t.Fatalf("EmailGet() error = %v", err)
	}
	var getResponse struct {
		List []struct {
			ID       string `json:"id"`
			ThreadID string `json:"threadId"`
		} `json:"list"`
	}
	if err := json.Unmarshal(raw, &getResponse); err != nil {
		t.Fatalf("unmarshal EmailGet response: %v", err)
	}
	if len(getResponse.List) != 3 {
		t.Fatalf("list = %+v, want three emails", getResponse.List)
	}
	threadID := getResponse.List[0].ThreadID
	filter, err := json.Marshal(map[string]any{"filter": map[string]any{"threadId": threadID}})
	if err != nil {
		t.Fatal(err)
	}
	assertIDs(t, filterQueryIDs(t, h, ctx, string(filter)), []string{"e1", "e2", "e3"})

	var collapsed struct {
		IDs   []string `json:"ids"`
		Total *int     `json:"total"`
	}
	raw, err = h.EmailQuery(ctx, "alice@example.com",
		json.RawMessage(`{"collapseThreads":true,"calculateTotal":true}`))
	if err != nil {
		t.Fatalf("EmailQuery(collapseThreads) error = %v", err)
	}
	if err := json.Unmarshal(raw, &collapsed); err != nil {
		t.Fatalf("unmarshal collapsed response: %v (raw %s)", err, raw)
	}
	if len(collapsed.IDs) != 2 {
		t.Fatalf("collapsed ids = %v, want one per thread (2)", collapsed.IDs)
	}
	if collapsed.Total == nil || *collapsed.Total != 2 {
		t.Errorf("collapsed total = %v, want 2", collapsed.Total)
	}
	seen := make(map[string]bool)
	for _, id := range collapsed.IDs {
		if threadID != "" && (id == "e1" || id == "e2" || id == "e3") {
			if seen[threadID] {
				t.Errorf("multiple emails from one thread survived collapse: %v", collapsed.IDs)
			}
			seen[threadID] = true
		}
	}
}

func TestUpToIDLimitsQueryChangesAddition(t *testing.T) {
	h, store, ctx := newFilterFixture(t)
	// text is derived from immutable message content, so RFC 8620 section 5.6
	// permits the server to honour upToId for this query.
	filter := `{"filter":{"text":"launch"}}`

	raw, err := h.EmailQuery(ctx, "alice@example.com", json.RawMessage(filter))
	if err != nil {
		t.Fatalf("EmailQuery() error = %v", err)
	}
	var initial struct {
		QueryState string   `json:"queryState"`
		IDs        []string `json:"ids"`
	}
	if err := json.Unmarshal(raw, &initial); err != nil {
		t.Fatalf("unmarshal initial query: %v", err)
	}
	if !reflect.DeepEqual(initial.IDs, []string{"e3", "e1"}) {
		t.Fatalf("initial ids = %v, want [e3 e1]", initial.IDs)
	}

	// e5 sorts before e3, e6 after it. Both become matches.
	addFilterEmail(t, h, store, ctx, "e5", "Launch follow-up", "2026-01-01T12:30:00Z")
	addFilterEmail(t, h, store, ctx, "e6", "Later launch note", "2026-01-01T07:00:00Z")

	// Sync the new result set so the original token still has a snapshot.
	if _, err := h.EmailQuery(ctx, "alice@example.com", json.RawMessage(filter)); err != nil {
		t.Fatalf("EmailQuery() after additions error = %v", err)
	}

	changesArgs := `{"filter":{"text":"launch"},"sinceQueryState":"` + initial.QueryState + `","upToId":"e3"}`
	raw, err = h.EmailQueryChanges(ctx, "alice@example.com", json.RawMessage(changesArgs))
	if err != nil {
		t.Fatalf("EmailQueryChanges(upToId) error = %v", err)
	}
	var changes struct {
		Added []struct {
			Index int    `json:"index"`
			ID    string `json:"id"`
		} `json:"added"`
	}
	if err := json.Unmarshal(raw, &changes); err != nil {
		t.Fatalf("unmarshal queryChanges: %v (raw %s)", err, raw)
	}
	added := make([]string, 0, len(changes.Added))
	for _, entry := range changes.Added {
		added = append(added, entry.ID)
	}
	if !reflect.DeepEqual(added, []string{"e5"}) {
		t.Errorf("added with upToId = %v, want [e5] (raw %s)", added, raw)
	}

	// Without upToId the complete diff contains both new matches.
	raw, err = h.EmailQueryChanges(ctx, "alice@example.com", json.RawMessage(
		`{"filter":{"text":"launch"},"sinceQueryState":"`+initial.QueryState+`"}`))
	if err != nil {
		t.Fatalf("EmailQueryChanges() error = %v", err)
	}
	var full struct {
		Added []struct {
			ID string `json:"id"`
		} `json:"added"`
	}
	if err := json.Unmarshal(raw, &full); err != nil {
		t.Fatalf("unmarshal full queryChanges: %v", err)
	}
	fullIDs := make([]string, 0, len(full.Added))
	for _, entry := range full.Added {
		fullIDs = append(fullIDs, entry.ID)
	}
	sort.Strings(fullIDs)
	if !reflect.DeepEqual(fullIDs, []string{"e5", "e6"}) {
		t.Errorf("added without upToId = %v, want [e5 e6] (raw %s)", fullIDs, raw)
	}
}

func addFilterEmail(t *testing.T, h *Handler, store *storage.MemoryStorage, ctx context.Context, id, subject, received string) {
	t.Helper()
	at, err := time.Parse(time.RFC3339, received)
	if err != nil {
		t.Fatal(err)
	}
	store.Add(&mailemail.ListedEmail{
		Ref: &mailemail.EmailRef{ID: id, AccountID: "alice@example.com", MailboxID: "inbox"},
		Email: &mailemail.UserEmail{
			Subject: subject,
			Text:    strings.ToLower(subject),
			Date:    at,
		},
	})
	meta := metadata.NewEmailMetadata("alice@example.com", id)
	meta.ReceivedAt = metadata.FormatReceivedAt(at)
	if err := h.deps.Metadata.Save(ctx, meta); err != nil {
		t.Fatal(err)
	}
	if err := h.deps.Changes.Record(ctx, "alice@example.com", base.StateEmail, id, base.ChangeCreate); err != nil {
		t.Fatal(err)
	}
}
