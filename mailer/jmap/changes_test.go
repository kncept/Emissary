package jmap

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/kncept/Emissary/mailer/email"
	"github.com/kncept/Emissary/mailer/metadata"
	"github.com/kncept/Emissary/mailer/sender"
	"github.com/kncept/Emissary/mailer/storage"
	"github.com/kncept/Emissary/mailer/usermanager"
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

	if err := store.DeleteEmail(ctx, "new-1"); err != nil {
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
