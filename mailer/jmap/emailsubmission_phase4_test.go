package jmap

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/kncept/Emissary/mailer/auth"
	"github.com/kncept/Emissary/mailer/components/metadata"
	"github.com/kncept/Emissary/mailer/components/sender"
	"github.com/kncept/Emissary/mailer/components/storage"
	"github.com/kncept/Emissary/mailer/components/usermanager"
	"github.com/kncept/Emissary/mailer/email"
)

func newSubmissionHandler(t *testing.T, accountID string) (*Handler, *storage.MemoryStorage) {
	t.Helper()
	store := storage.NewMemoryStorage()
	addTestEmail(store, "draft1", accountID, "drafts", &email.UserEmail{
		From:    email.Address{Address: accountID},
		To:      []email.Address{{Address: "bob@example.com"}},
		Subject: "submission test",
		Text:    "body",
	})
	handler := NewHandler(store, metadata.NewMemoryMetadata(),
		usermanager.NewNone(usermanager.Config{AdminUsername: "admin", AdminPassword: "password"}),
		sender.NewInMemoryRecording())
	return handler, store
}

func submitDraft(t *testing.T, handler *Handler, accountID, creationID string) string {
	t.Helper()
	ctx := auth.ContextWithClaims(context.Background(), &auth.Claims{Subject: accountID})
	args := json.RawMessage(fmt.Sprintf(
		`{"accountId":%q,"create":{%q:{"identityId":"primary","emailId":"draft1"}}}`,
		accountID, creationID))
	result, err := handler.EmailSubmissionSet(ctx, accountID, args)
	if err != nil {
		t.Fatalf("EmailSubmissionSet() error = %v", err)
	}
	var resp struct {
		Created map[string]struct {
			ID       string `json:"id"`
			EmailID  string `json:"emailId"`
			Envelope struct {
				MailFrom struct{ Email string }   `json:"mailFrom"`
				RcptTo   []struct{ Email string } `json:"rcptTo"`
			} `json:"envelope"`
		} `json:"created"`
	}
	if err := json.Unmarshal(result, &resp); err != nil {
		t.Fatalf("unmarshal set response: %v", err)
	}
	created, ok := resp.Created[creationID]
	if !ok || created.ID == "" {
		t.Fatalf("created = %+v, want a submission for %q", resp.Created, creationID)
	}
	if created.EmailID != "draft1" {
		t.Errorf("emailId = %q, want draft1", created.EmailID)
	}
	if created.Envelope.MailFrom.Email != accountID {
		t.Errorf("mailFrom = %q, want %q", created.Envelope.MailFrom.Email, accountID)
	}
	if len(created.Envelope.RcptTo) != 1 || created.Envelope.RcptTo[0].Email != "bob@example.com" {
		t.Errorf("rcptTo = %+v, want bob@example.com", created.Envelope.RcptTo)
	}
	return created.ID
}

// TestEmailSubmissionPhase4GetQueryChanges drives every submission read method
// against a real durable record, verifying they return JMAP objects rather than
// unknownMethod or a stub error.
func TestEmailSubmissionPhase4GetQueryChanges(t *testing.T) {
	const accountID = "alice@example.com"
	handler, _ := newSubmissionHandler(t, accountID)
	ctx := auth.ContextWithClaims(context.Background(), &auth.Claims{Subject: accountID})
	subID := submitDraft(t, handler, accountID, "c1")

	getResult, err := handler.EmailSubmissionGet(ctx, accountID,
		json.RawMessage(fmt.Sprintf(`{"ids":[%q]}`, subID)))
	if err != nil {
		t.Fatalf("EmailSubmissionGet() error = %v", err)
	}
	var getResp struct {
		State string `json:"state"`
		List  []struct {
			ID         string `json:"id"`
			IdentityID string `json:"identityId"`
			SendAt     string `json:"sendAt"`
			UndoStatus string `json:"undoStatus"`
		} `json:"list"`
		NotFound []string `json:"notFound"`
	}
	if err := json.Unmarshal(getResult, &getResp); err != nil {
		t.Fatalf("unmarshal get response: %v", err)
	}
	if getResp.State == "" || len(getResp.List) != 1 || len(getResp.NotFound) != 0 {
		t.Fatalf("get response = %+v, want one record and no notFound", getResp)
	}
	got := getResp.List[0]
	if got.ID != subID || got.IdentityID != "primary" || got.SendAt == "" || got.UndoStatus != "final" {
		t.Errorf("submission = %+v, want a complete record", got)
	}

	changesResult, err := handler.EmailSubmissionChanges(ctx, accountID,
		json.RawMessage(`{"sinceState":"submission-0"}`))
	if err != nil {
		t.Fatalf("EmailSubmissionChanges() error = %v", err)
	}
	var changesResp struct {
		Created []string `json:"created"`
	}
	if err := json.Unmarshal(changesResult, &changesResp); err != nil {
		t.Fatalf("unmarshal changes response: %v", err)
	}
	found := false
	for _, id := range changesResp.Created {
		if id == subID {
			found = true
		}
	}
	if !found {
		t.Errorf("changes.created = %v, want an entry for %q", changesResp.Created, subID)
	}

	queryResult, err := handler.EmailSubmissionQuery(ctx, accountID,
		json.RawMessage(`{"calculateTotal":true}`))
	if err != nil {
		t.Fatalf("EmailSubmissionQuery() error = %v", err)
	}
	var queryResp struct {
		QueryState string   `json:"queryState"`
		IDs        []string `json:"ids"`
		Total      int      `json:"total"`
	}
	if err := json.Unmarshal(queryResult, &queryResp); err != nil {
		t.Fatalf("unmarshal query response: %v", err)
	}
	if queryResp.QueryState == "" || len(queryResp.IDs) != 1 || queryResp.IDs[0] != subID || queryResp.Total != 1 {
		t.Fatalf("query response = %+v, want one matching submission", queryResp)
	}

	queryChangesResult, err := handler.EmailSubmissionQueryChanges(ctx, accountID,
		json.RawMessage(fmt.Sprintf(`{"sinceQueryState":%q}`, queryResp.QueryState)))
	if err != nil {
		t.Fatalf("EmailSubmissionQueryChanges() error = %v", err)
	}
	var queryChangesResp struct {
		Removed []string `json:"removed"`
		Added   []struct {
			ID string `json:"id"`
		} `json:"added"`
	}
	if err := json.Unmarshal(queryChangesResult, &queryChangesResp); err != nil {
		t.Fatalf("unmarshal queryChanges response: %v", err)
	}
	if len(queryChangesResp.Removed) != 0 || len(queryChangesResp.Added) != 0 {
		t.Errorf("queryChanges = %+v, want no differences", queryChangesResp)
	}
}

// TestEmailSubmissionPhase4ColdStart drives the Phase 4a use case: submit via
// EmailSubmission/set, then read it back with get and queryChanges through a
// fresh handler built over only the durable storage (a simulated restart).
func TestEmailSubmissionPhase4ColdStart(t *testing.T) {
	const accountID = "alice@example.com"
	handler, store := newSubmissionHandler(t, accountID)
	ctx := auth.ContextWithClaims(context.Background(), &auth.Claims{Subject: accountID})

	before, err := handler.EmailSubmissionQuery(ctx, accountID, json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("EmailSubmissionQuery(before) error = %v", err)
	}
	var beforeResp struct {
		QueryState string `json:"queryState"`
	}
	if err := json.Unmarshal(before, &beforeResp); err != nil {
		t.Fatalf("unmarshal query(before): %v", err)
	}

	subID := submitDraft(t, handler, accountID, "cold1")

	// A new handler has none of the first handler's in-memory state; the
	// submission record and tracker state must come from storage.
	fresh := NewHandler(store, metadata.NewMemoryMetadata(),
		usermanager.NewNone(usermanager.Config{AdminUsername: "admin", AdminPassword: "password"}),
		sender.NewInMemoryRecording())

	got, err := fresh.EmailSubmissionGet(ctx, accountID,
		json.RawMessage(fmt.Sprintf(`{"ids":[%q]}`, subID)))
	if err != nil {
		t.Fatalf("EmailSubmissionGet(cold) error = %v", err)
	}
	var getResp struct {
		List []struct {
			ID string `json:"id"`
		} `json:"list"`
		NotFound []string `json:"notFound"`
	}
	if err := json.Unmarshal(got, &getResp); err != nil {
		t.Fatalf("unmarshal get(cold): %v", err)
	}
	if len(getResp.List) != 1 || getResp.List[0].ID != subID || len(getResp.NotFound) != 0 {
		t.Fatalf("get after restart = %s, want the persisted submission", got)
	}

	changes, err := fresh.EmailSubmissionQueryChanges(ctx, accountID,
		json.RawMessage(fmt.Sprintf(`{"sinceQueryState":%q}`, beforeResp.QueryState)))
	if err != nil {
		t.Fatalf("EmailSubmissionQueryChanges(cold) error = %v", err)
	}
	var changesResp struct {
		Added []struct {
			ID string `json:"id"`
		} `json:"added"`
	}
	if err := json.Unmarshal(changes, &changesResp); err != nil {
		t.Fatalf("unmarshal queryChanges(cold): %v", err)
	}
	found := false
	for _, item := range changesResp.Added {
		if item.ID == subID {
			found = true
		}
	}
	if !found {
		t.Errorf("queryChanges after restart = %s, want %q in added", changes, subID)
	}
}

// TestEmailSubmissionPhase4UpdateDestroy verifies that update and destroy act
// on the durable record and advance the type state.
func TestEmailSubmissionPhase4UpdateDestroy(t *testing.T) {
	const accountID = "alice@example.com"
	handler, _ := newSubmissionHandler(t, accountID)
	ctx := auth.ContextWithClaims(context.Background(), &auth.Claims{Subject: accountID})
	subID := submitDraft(t, handler, accountID, "c1")

	updateArgs := json.RawMessage(fmt.Sprintf(
		`{"accountId":%q,"update":{%q:{"undoStatus":"canceled"}}}`,
		accountID, subID))
	updateResult, err := handler.EmailSubmissionSet(ctx, accountID, updateArgs)
	if err != nil {
		t.Fatalf("EmailSubmissionSet(update) error = %v", err)
	}
	var updateResp struct {
		Updated map[string]struct {
			UndoStatus string `json:"undoStatus"`
		} `json:"updated"`
		NotUpdated map[string]interface{} `json:"notUpdated"`
	}
	if err := json.Unmarshal(updateResult, &updateResp); err != nil {
		t.Fatalf("unmarshal update response: %v", err)
	}
	updated, ok := updateResp.Updated[subID]
	if !ok || updated.UndoStatus != "canceled" || len(updateResp.NotUpdated) != 0 {
		t.Fatalf("update response = %+v, want %q updated to canceled", updateResp, subID)
	}

	getResult, err := handler.EmailSubmissionGet(ctx, accountID,
		json.RawMessage(fmt.Sprintf(`{"ids":[%q]}`, subID)))
	if err != nil {
		t.Fatalf("EmailSubmissionGet() error = %v", err)
	}
	var getResp struct {
		List []struct {
			UndoStatus string `json:"undoStatus"`
		} `json:"list"`
	}
	if err := json.Unmarshal(getResult, &getResp); err != nil {
		t.Fatalf("unmarshal get response: %v", err)
	}
	if len(getResp.List) != 1 || getResp.List[0].UndoStatus != "canceled" {
		t.Fatalf("after update = %+v, want undoStatus canceled", getResp.List)
	}

	destroyArgs := json.RawMessage(fmt.Sprintf(
		`{"accountId":%q,"destroy":[%q]}`, accountID, subID))
	destroyResult, err := handler.EmailSubmissionSet(ctx, accountID, destroyArgs)
	if err != nil {
		t.Fatalf("EmailSubmissionSet(destroy) error = %v", err)
	}
	var destroyResp struct {
		Destroyed []string `json:"destroyed"`
	}
	if err := json.Unmarshal(destroyResult, &destroyResp); err != nil {
		t.Fatalf("unmarshal destroy response: %v", err)
	}
	if len(destroyResp.Destroyed) != 1 || destroyResp.Destroyed[0] != subID {
		t.Fatalf("destroy response = %+v, want %q destroyed", destroyResp, subID)
	}

	getResult, err = handler.EmailSubmissionGet(ctx, accountID,
		json.RawMessage(fmt.Sprintf(`{"ids":[%q]}`, subID)))
	if err != nil {
		t.Fatalf("EmailSubmissionGet() after destroy error = %v", err)
	}
	var afterResp struct {
		NotFound []string `json:"notFound"`
	}
	if err := json.Unmarshal(getResult, &afterResp); err != nil {
		t.Fatalf("unmarshal after-destroy response: %v", err)
	}
	if len(afterResp.NotFound) != 1 || afterResp.NotFound[0] != subID {
		t.Fatalf("after destroy = %+v, want %q in notFound", afterResp, subID)
	}
}
