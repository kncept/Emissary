package jmap

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kncept/Emissary/mailer/email"
	"github.com/kncept/Emissary/mailer/components/metadata"
	"github.com/kncept/Emissary/mailer/components/sender"
	"github.com/kncept/Emissary/mailer/components/storage"
	"github.com/kncept/Emissary/mailer/components/usermanager"
)

const changesAccount = "user@example.com"

func newChangesTestHandler() (*Handler, *storage.MemoryStorage, *metadata.MemoryMetadata) {
	store := storage.NewMemoryStorage()
	meta := metadata.NewMemoryMetadata()
	userMgr := usermanager.NewNone(usermanager.Config{AdminUsername: "admin", AdminPassword: "password"})
	return NewHandler(store, meta, userMgr, sender.NewLogSender()), store, meta
}

func emailStateOf(t *testing.T, h *Handler, ctx context.Context) string {
	t.Helper()
	raw, err := h.EmailGet(ctx, changesAccount, json.RawMessage(`{"accountId":"user@example.com"}`))
	if err != nil {
		t.Fatalf("EmailGet() error = %v", err)
	}
	var response struct {
		State string `json:"state"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		t.Fatalf("decode Email/get response: %v", err)
	}
	if response.State == "" {
		t.Fatal("Email/get returned an empty state token")
	}
	return response.State
}

func emailChangesOf(t *testing.T, h *Handler, ctx context.Context, since string, maxChanges int) map[string]interface{} {
	t.Helper()
	sinceJSON, err := json.Marshal(since)
	if err != nil {
		t.Fatal(err)
	}
	args := `{"accountId":"user@example.com","sinceState":` + string(sinceJSON)
	if maxChanges > 0 {
		args += `,"maxChanges":` + strconv.Itoa(maxChanges)
	}
	args += `}`
	raw, err := h.EmailChanges(ctx, changesAccount, json.RawMessage(args))
	if err != nil {
		t.Fatalf("EmailChanges() error = %v", err)
	}
	var response map[string]interface{}
	if err := json.Unmarshal(raw, &response); err != nil {
		t.Fatalf("decode Email/changes response: %v", err)
	}
	return response
}

func emailQueryOf(t *testing.T, h *Handler, ctx context.Context, filter string) map[string]interface{} {
	t.Helper()
	args := `{"accountId":"user@example.com"`
	if filter != "" {
		args += `,"filter":` + filter
	}
	args += `}`
	raw, err := h.EmailQuery(ctx, changesAccount, json.RawMessage(args))
	if err != nil {
		t.Fatalf("EmailQuery() error = %v", err)
	}
	var response map[string]interface{}
	if err := json.Unmarshal(raw, &response); err != nil {
		t.Fatalf("decode Email/query response: %v", err)
	}
	return response
}

func emailQueryChangesOf(t *testing.T, h *Handler, ctx context.Context, since, filter string) map[string]interface{} {
	t.Helper()
	sinceJSON, err := json.Marshal(since)
	if err != nil {
		t.Fatal(err)
	}
	args := `{"accountId":"user@example.com","sinceQueryState":` + string(sinceJSON)
	if filter != "" {
		args += `,"filter":` + filter
	}
	args += `}`
	raw, err := h.EmailQueryChanges(ctx, changesAccount, json.RawMessage(args))
	if err != nil {
		t.Fatalf("EmailQueryChanges() error = %v", err)
	}
	var response map[string]interface{}
	if err := json.Unmarshal(raw, &response); err != nil {
		t.Fatalf("decode Email/queryChanges response: %v", err)
	}
	return response
}

func stringIDs(t *testing.T, value interface{}) []string {
	t.Helper()
	items, ok := value.([]interface{})
	if !ok {
		t.Fatalf("ids are %T, want []interface{}", value)
	}
	ids := make([]string, 0, len(items))
	for _, item := range items {
		id, ok := item.(string)
		if !ok {
			t.Fatalf("id is %T, want string", item)
		}
		ids = append(ids, id)
	}
	return ids
}

func TestEmailStateIsStableWhenDataDoesNotChange(t *testing.T) {
	ctx := context.Background()
	h, _, _ := newChangesTestHandler()

	first := emailStateOf(t, h, ctx)
	second := emailStateOf(t, h, ctx)
	if first != second {
		t.Errorf("state changed without data changing: %q -> %q", first, second)
	}
	if !strings.HasPrefix(first, string(stateEmail)+"-") {
		t.Errorf("state %q does not carry the Email type prefix", first)
	}
}

func TestEmailChangesReportsExternalCreatesAndDestroys(t *testing.T) {
	ctx := context.Background()
	h, store, _ := newChangesTestHandler()

	since := emailStateOf(t, h, ctx)
	addTestEmail(store, "new-1", changesAccount, "inbox", &email.UserEmail{Subject: "new"})

	response := emailChangesOf(t, h, ctx, since, 0)
	created := stringIDs(t, response["created"])
	if len(created) != 1 || created[0] != "new-1" {
		t.Fatalf("created = %v, want [new-1]", created)
	}
	if response["oldState"] != since {
		t.Errorf("oldState = %v, want %v", response["oldState"], since)
	}
	newState, _ := response["newState"].(string)
	if newState == "" || newState == since {
		t.Fatalf("newState = %v, want a new token", response["newState"])
	}

	// A second call from the new state has nothing left to report.
	empty := emailChangesOf(t, h, ctx, newState, 0)
	if len(stringIDs(t, empty["created"])) != 0 {
		t.Fatalf("created = %v after consuming the delta, want empty", empty["created"])
	}

	if err := store.DeleteEmail(ctx, changesAccount, "new-1"); err != nil {
		t.Fatalf("DeleteEmail() error = %v", err)
	}
	destroyed := emailChangesOf(t, h, ctx, newState, 0)
	ids := stringIDs(t, destroyed["destroyed"])
	if len(ids) != 1 || ids[0] != "new-1" {
		t.Fatalf("destroyed = %v, want [new-1]", ids)
	}
}

func TestEmailChangesReportsKeywordUpdates(t *testing.T) {
	ctx := context.Background()
	h, store, meta := newChangesTestHandler()
	addTestEmail(store, "msg-1", changesAccount, "inbox", &email.UserEmail{Subject: "subject"})

	since := emailStateOf(t, h, ctx)
	if err := meta.Save(ctx, metadata.NewEmailMetadata(changesAccount, "msg-1", metadata.KeywordSeen)); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	response := emailChangesOf(t, h, ctx, since, 0)
	updated := stringIDs(t, response["updated"])
	if len(updated) != 1 || updated[0] != "msg-1" {
		t.Fatalf("updated = %v, want [msg-1]", updated)
	}
}

func TestEmailChangesMaxChangesReturnsIntermediateState(t *testing.T) {
	ctx := context.Background()
	h, store, _ := newChangesTestHandler()
	since := emailStateOf(t, h, ctx)
	for _, id := range []string{"one", "two", "three"} {
		addTestEmail(store, id, changesAccount, "inbox", &email.UserEmail{Subject: id})
	}

	first := emailChangesOf(t, h, ctx, since, 1)
	if first["hasMoreChanges"] != true {
		t.Fatalf("hasMoreChanges = %v, want true", first["hasMoreChanges"])
	}
	if got := stringIDs(t, first["created"]); len(got) != 1 {
		t.Fatalf("created = %v, want one id", got)
	}
	intermediate, _ := first["newState"].(string)
	if intermediate == "" {
		t.Fatal("missing intermediate newState")
	}

	rest := emailChangesOf(t, h, ctx, intermediate, 0)
	if rest["hasMoreChanges"] != false {
		t.Fatalf("hasMoreChanges = %v, want false", rest["hasMoreChanges"])
	}
	if got := stringIDs(t, rest["created"]); len(got) != 2 {
		t.Fatalf("created = %v, want two remaining ids", got)
	}
}

func TestEmailChangesMaxChangesUsesCoalescedCount(t *testing.T) {
	ctx := context.Background()
	h, store, _ := newChangesTestHandler()
	since := emailStateOf(t, h, ctx)

	addTestEmail(store, "one", changesAccount, "inbox", &email.UserEmail{Subject: "one"})
	emailStateOf(t, h, ctx)
	addTestEmail(store, "two", changesAccount, "inbox", &email.UserEmail{Subject: "two"})
	emailStateOf(t, h, ctx)
	if err := store.DeleteEmail(ctx, changesAccount, "one"); err != nil {
		t.Fatalf("DeleteEmail() error = %v", err)
	}

	// "one" was created inside the window and then destroyed, so only "two"
	// remains. maxChanges=1 must not report hasMoreChanges.
	response := emailChangesOf(t, h, ctx, since, 1)
	if response["hasMoreChanges"] != false {
		t.Fatalf("hasMoreChanges = %v, want false", response["hasMoreChanges"])
	}
	if created := stringIDs(t, response["created"]); len(created) != 1 || created[0] != "two" {
		t.Fatalf("created = %v, want [two]", created)
	}
	if destroyed := stringIDs(t, response["destroyed"]); len(destroyed) != 0 {
		t.Fatalf("destroyed = %v, want empty", destroyed)
	}
}

func TestEmailQueryStateTracksResultSetsPerFilter(t *testing.T) {
	ctx := context.Background()
	h, store, _ := newChangesTestHandler()
	addTestEmail(store, "inbox-1", changesAccount, "inbox", &email.UserEmail{
		Subject: "inbox 1",
		Date:    time.Date(2026, time.November, 1, 12, 0, 0, 0, time.UTC),
	})
	addTestEmail(store, "inbox-2", changesAccount, "inbox", &email.UserEmail{
		Subject: "inbox 2",
		Date:    time.Date(2026, time.November, 2, 12, 0, 0, 0, time.UTC),
	})
	addTestEmail(store, "sent-1", changesAccount, "sent", &email.UserEmail{
		Subject: "sent 1",
		Date:    time.Date(2026, time.November, 3, 12, 0, 0, 0, time.UTC),
	})

	inboxFilter := `{"inMailbox":"inbox"}`
	sentFilter := `{"inMailbox":"sent"}`
	inbox := emailQueryOf(t, h, ctx, inboxFilter)
	sent := emailQueryOf(t, h, ctx, sentFilter)
	inboxState, _ := inbox["queryState"].(string)
	sentState, _ := sent["queryState"].(string)
	if inboxState == "" || sentState == "" || inboxState == sentState {
		t.Fatalf("query states = %q / %q, want two distinct tokens", inboxState, sentState)
	}
	if got := emailQueryOf(t, h, ctx, inboxFilter)["queryState"]; got != inboxState {
		t.Fatalf("queryState changed without data changing: %q -> %v", inboxState, got)
	}

	addTestEmail(store, "inbox-3", changesAccount, "inbox", &email.UserEmail{
		Subject: "inbox 3",
		Date:    time.Date(2026, time.November, 4, 12, 0, 0, 0, time.UTC),
	})
	nextInbox := emailQueryOf(t, h, ctx, inboxFilter)
	nextInboxState, _ := nextInbox["queryState"].(string)
	if nextInboxState == inboxState {
		t.Fatalf("queryState did not change when the result set changed: %q", nextInboxState)
	}
	if got := emailQueryOf(t, h, ctx, sentFilter)["queryState"]; got != sentState {
		t.Fatalf("unrelated query state changed: %q -> %v", sentState, got)
	}

	changes := emailQueryChangesOf(t, h, ctx, inboxState, inboxFilter)
	if changes["canCalculateChanges"] != true {
		t.Fatalf("canCalculateChanges = %v, want true", changes["canCalculateChanges"])
	}
	if removed := stringIDs(t, changes["removed"]); len(removed) != 0 {
		t.Fatalf("removed = %v, want empty", removed)
	}
	added, ok := changes["added"].([]interface{})
	if !ok || len(added) != 1 {
		t.Fatalf("added = %v, want one item", changes["added"])
	}
	item, ok := added[0].(map[string]interface{})
	if !ok || item["id"] != "inbox-3" {
		t.Fatalf("added item = %v, want inbox-3", added[0])
	}

	mismatch := emailQueryChangesOf(t, h, ctx, inboxState, sentFilter)
	if mismatch["canCalculateChanges"] != false {
		t.Fatalf("canCalculateChanges = %v for another query, want false", mismatch["canCalculateChanges"])
	}

	followUp := emailQueryChangesOf(t, h, ctx, nextInboxState, inboxFilter)
	if followUp["canCalculateChanges"] != true {
		t.Fatalf("canCalculateChanges = %v, want true", followUp["canCalculateChanges"])
	}
	if len(stringIDs(t, followUp["removed"])) != 0 || len(followUp["added"].([]interface{})) != 0 {
		t.Fatalf("follow-up changes = %v / %v, want empty", followUp["added"], followUp["removed"])
	}
}

func TestEmailChangesRejectsInvalidStateAndMaxChanges(t *testing.T) {
	ctx := context.Background()
	h, _, _ := newChangesTestHandler()

	if _, err := h.EmailChanges(ctx, changesAccount, json.RawMessage(`{"accountId":"user@example.com","sinceState":"garbage"}`)); err == nil || !strings.HasPrefix(err.Error(), "cannotCalculateChanges:") {
		t.Fatalf("EmailChanges() error = %v, want cannotCalculateChanges", err)
	}
	if _, err := h.EmailChanges(ctx, changesAccount, json.RawMessage(`{"accountId":"user@example.com","sinceState":"email-0","maxChanges":0}`)); err == nil || !strings.HasPrefix(err.Error(), "invalidArguments:") {
		t.Fatalf("EmailChanges() error = %v, want invalidArguments", err)
	}
}
