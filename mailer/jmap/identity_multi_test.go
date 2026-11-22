package jmap

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/kncept/Emissary/mailer/components/metadata"
	"github.com/kncept/Emissary/mailer/components/sender"
	"github.com/kncept/Emissary/mailer/components/storage"
	"github.com/kncept/Emissary/mailer/components/usermanager"
	mailemail "github.com/kncept/Emissary/mailer/email"
	appidentity "github.com/kncept/Emissary/mailer/identity"
)

type allowFromValidator struct{ allowed map[string]bool }

func (v allowFromValidator) Validate(_ context.Context, from string) error {
	if v.allowed == nil || v.allowed[strings.ToLower(strings.TrimSpace(from))] {
		return nil
	}
	return fmt.Errorf("%w: %q", appidentity.ErrNotAllowed, from)
}

func newMultiIdentityHandler(store storage.Storage, capture *sender.InMemoryRecording, allowed ...string) *Handler {
	permitted := map[string]bool{}
	for _, address := range allowed {
		permitted[strings.ToLower(address)] = true
	}
	return NewHandler(store, metadata.NewMemoryMetadata(),
		usermanager.NewNone(usermanager.Config{AdminUsername: "admin", AdminPassword: "password"}),
		capture, allowFromValidator{allowed: permitted})
}

func TestIdentityMultiLifecycleRFC8621_6_3(t *testing.T) {
	const accountID = "alice@example.com"
	store := storage.NewMemoryStorage()
	capture := sender.NewInMemoryRecording()
	h := newMultiIdentityHandler(store, capture, accountID, "alias@example.com", "second@example.com")
	ctx := context.Background()

	// Two identities can coexist with the default one, and duplicate addresses
	// are rejected with the existing id (RFC 8620 section 5.3).
	createdRaw, err := h.IdentitySet(ctx, accountID, json.RawMessage(fmt.Sprintf(
		`{"accountId":%q,"create":{
			"c1":{"email":"Alias@Example.com","name":"Alias"},
			"c2":{"email":"second@example.com","name":"Second"},
			"dup":{"email":"alias@example.com"}
		}}`, accountID)))
	if err != nil {
		t.Fatalf("IdentitySet(create) error = %v", err)
	}
	var created struct {
		OldState string `json:"oldState"`
		NewState string `json:"newState"`
		Created  map[string]struct {
			ID        string `json:"id"`
			Email     string `json:"email"`
			MayDelete bool   `json:"mayDelete"`
		} `json:"created"`
		NotCreated map[string]struct {
			Type       string `json:"type"`
			ExistingID string `json:"existingId"`
		} `json:"notCreated"`
	}
	if err := json.Unmarshal(createdRaw, &created); err != nil {
		t.Fatalf("unmarshal create response: %v", err)
	}
	if len(created.Created) != 2 {
		t.Fatalf("created = %+v, want two identities", created.Created)
	}
	if _, ok := created.NotCreated["c1"]; ok {
		t.Errorf("first create returned alreadyExists for c1: %+v", created.NotCreated)
	}
	if _, ok := created.NotCreated["c2"]; ok {
		t.Errorf("first create returned alreadyExists for c2: %+v", created.NotCreated)
	}
	if created.NewState == "" || created.NewState == created.OldState {
		t.Errorf("create states = old %q, new %q, want a new state", created.OldState, created.NewState)
	}
	if dup := created.NotCreated["dup"]; dup.Type != "alreadyExists" || dup.ExistingID != created.Created["c1"].ID {
		t.Errorf("duplicate create = %+v, want alreadyExists with existingId %q", dup, created.Created["c1"].ID)
	}
	if !created.Created["c1"].MayDelete || strings.EqualFold(created.Created["c1"].Email, "alias@example.com") == false {
		t.Errorf("created identity = %+v", created.Created["c1"])
	}

	getRaw, err := h.IdentityGet(ctx, accountID, json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("IdentityGet() error = %v", err)
	}
	var list struct {
		List []struct {
			ID    string `json:"id"`
			Email string `json:"email"`
		} `json:"list"`
	}
	if err := json.Unmarshal(getRaw, &list); err != nil {
		t.Fatalf("unmarshal get response: %v", err)
	}
	if len(list.List) != 3 {
		t.Fatalf("identities = %+v, want the default plus two new ones", list.List)
	}

	// Update one identity and destroy the other in a single /set.
	secondID := created.Created["c2"].ID
	updatedRaw, err := h.IdentitySet(ctx, accountID, json.RawMessage(fmt.Sprintf(
		`{"accountId":%q,"update":{%q:{"name":"Renamed","email":"second@example.com"}},
		  "destroy":[%q]}`,
		accountID, secondID, created.Created["c1"].ID)))
	if err != nil {
		t.Fatalf("IdentitySet(update/destroy) error = %v", err)
	}
	var changed struct {
		OldState  string                           `json:"oldState"`
		NewState  string                           `json:"newState"`
		Updated   map[string]struct{ Name string } `json:"updated"`
		Destroyed []string                         `json:"destroyed"`
	}
	if err := json.Unmarshal(updatedRaw, &changed); err != nil {
		t.Fatalf("unmarshal update response: %v", err)
	}
	if changed.Updated[secondID].Name != "Renamed" ||
		len(changed.Destroyed) != 1 || changed.Destroyed[0] != created.Created["c1"].ID {
		t.Fatalf("update/destroy response = %+v", changed)
	}
	if changed.OldState != created.NewState || changed.NewState == "" || changed.NewState == changed.OldState {
		t.Errorf("update/destroy states = old %q, new %q, want %q then a new state",
			changed.OldState, changed.NewState, created.NewState)
	}

	// The default identity remains protected and immutable email still yields
	// forbiddenFrom on update.
	immutableRaw, err := h.IdentitySet(ctx, accountID, json.RawMessage(fmt.Sprintf(
		`{"accountId":%q,"update":{"primary":{"email":"mallory@example.net"}},"destroy":["primary"]}`,
		accountID)))
	if err != nil {
		t.Fatalf("IdentitySet(immutable) error = %v", err)
	}
	var immutable struct {
		NotUpdated   map[string]struct{ Type string } `json:"notUpdated"`
		NotDestroyed map[string]struct{ Type string } `json:"notDestroyed"`
	}
	if err := json.Unmarshal(immutableRaw, &immutable); err != nil {
		t.Fatalf("unmarshal immutable response: %v", err)
	}
	if immutable.NotUpdated["primary"].Type != "forbiddenFrom" || immutable.NotDestroyed["primary"].Type != "forbidden" {
		t.Errorf("immutable response = %+v", immutable)
	}

	// Cold start over the same durable store sees the collection.
	restarted := newMultiIdentityHandler(store, capture, accountID, "alias@example.com", "second@example.com")
	restartRaw, err := restarted.IdentityGet(ctx, accountID, json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("IdentityGet after restart error = %v", err)
	}
	var restartedList struct {
		List []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"list"`
	}
	if err := json.Unmarshal(restartRaw, &restartedList); err != nil {
		t.Fatalf("unmarshal restart response: %v", err)
	}
	if len(restartedList.List) != 2 {
		t.Fatalf("identities after restart = %+v, want two", restartedList.List)
	}
	for _, identity := range restartedList.List {
		if identity.ID == secondID && identity.Name != "Renamed" {
			t.Errorf("updated identity after restart = %+v", identity)
		}
	}
}

func TestIdentityLegacyMigrationRFC8621_6_1(t *testing.T) {
	const accountID = "alice@example.com"
	store := storage.NewMemoryStorage()
	ctx := context.Background()
	_, err := storage.NewIdentityStore(store).Save(ctx, accountID, storage.IdentityRecord{
		Name:          "Legacy Name",
		TextSignature: "legacy signature",
	}, "")
	if err != nil {
		t.Fatalf("seed legacy identity: %v", err)
	}
	h := newMultiIdentityHandler(store, sender.NewInMemoryRecording(), accountID)

	raw, err := h.IdentityGet(ctx, accountID, json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("IdentityGet() error = %v", err)
	}
	var resp struct {
		List []struct {
			ID            string `json:"id"`
			Name          string `json:"name"`
			Email         string `json:"email"`
			TextSignature string `json:"textSignature"`
		} `json:"list"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal get response: %v", err)
	}
	if len(resp.List) != 1 || resp.List[0].ID != "primary" ||
		resp.List[0].Name != "Legacy Name" || resp.List[0].Email != accountID ||
		resp.List[0].TextSignature != "legacy signature" {
		t.Fatalf("migrated legacy identity = %+v", resp.List)
	}

	// A write materialises the collection, which then survives a cold start.
	if _, err := h.IdentitySet(ctx, accountID, json.RawMessage(fmt.Sprintf(
		`{"accountId":%q,"update":{"primary":{"name":"Migrated"}}}`, accountID))); err != nil {
		t.Fatalf("IdentitySet(migrated) error = %v", err)
	}
	restarted := newMultiIdentityHandler(store, sender.NewInMemoryRecording(), accountID)
	raw, err = restarted.IdentityGet(ctx, accountID, json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("IdentityGet after restart error = %v", err)
	}
	resp = struct {
		List []struct {
			ID            string `json:"id"`
			Name          string `json:"name"`
			Email         string `json:"email"`
			TextSignature string `json:"textSignature"`
		} `json:"list"`
	}{}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal restart response: %v", err)
	}
	if len(resp.List) != 1 || resp.List[0].Name != "Migrated" || resp.List[0].TextSignature != "legacy signature" {
		t.Fatalf("identity after restart = %+v", resp.List)
	}
}

func TestEmailSubmissionUsesNonPrimaryIdentityRFC8621_7_5(t *testing.T) {
	const accountID = "alice@example.com"
	const alias = "alias@example.com"
	store := storage.NewMemoryStorage()
	capture := sender.NewInMemoryRecording()
	h := newMultiIdentityHandler(store, capture, accountID, alias)
	ctx := context.Background()

	addTestEmail(store, "draft1", accountID, "inbox", &mailemail.UserEmail{
		From:    mailemail.Address{Address: accountID},
		To:      []mailemail.Address{{Address: "dave@example.com"}},
		Subject: "subject",
		Text:    "body",
	})

	createdRaw, err := h.IdentitySet(ctx, accountID, json.RawMessage(fmt.Sprintf(
		`{"accountId":%q,"create":{"alias":{"email":%q,"name":"Alias"}}}`, accountID, alias)))
	if err != nil {
		t.Fatalf("IdentitySet(create) error = %v", err)
	}
	var identityResponse struct {
		Created map[string]struct{ ID string } `json:"created"`
	}
	if err := json.Unmarshal(createdRaw, &identityResponse); err != nil {
		t.Fatalf("unmarshal identity response: %v", err)
	}
	identityID := identityResponse.Created["alias"].ID
	if identityID == "" {
		t.Fatalf("created identity = %+v", identityResponse.Created)
	}

	submissionRaw, err := h.EmailSubmissionSet(ctx, accountID, json.RawMessage(fmt.Sprintf(
		`{"accountId":%q,"create":{"s1":{"identityId":%q,"emailId":"draft1"}}}`, accountID, identityID)))
	if err != nil {
		t.Fatalf("EmailSubmissionSet() error = %v", err)
	}
	var submission struct {
		Created    map[string]json.RawMessage `json:"created"`
		NotCreated map[string]struct {
			Type string `json:"type"`
		} `json:"notCreated"`
	}
	if err := json.Unmarshal(submissionRaw, &submission); err != nil {
		t.Fatalf("unmarshal submission response: %v", err)
	}
	if len(submission.Created) != 1 || len(submission.NotCreated) != 0 || len(capture.Sent) != 1 {
		t.Fatalf("submission = %s, sent = %d", submissionRaw, len(capture.Sent))
	}
	if !strings.EqualFold(capture.Sent[0].From.Address, alias) {
		t.Errorf("sent From = %q, want %q", capture.Sent[0].From.Address, alias)
	}

	// An unknown id is an explicit invalidProperties SetError (RFC 8621 7.5),
	// not a silent fallback.
	unknownRaw, err := h.EmailSubmissionSet(ctx, accountID, json.RawMessage(fmt.Sprintf(
		`{"accountId":%q,"create":{"s2":{"identityId":"missing","emailId":"draft1"}}}`, accountID)))
	if err != nil {
		t.Fatalf("EmailSubmissionSet(unknown identity) error = %v", err)
	}
	var unknown struct {
		NotCreated map[string]struct{ Type string } `json:"notCreated"`
	}
	if err := json.Unmarshal(unknownRaw, &unknown); err != nil {
		t.Fatalf("unmarshal unknown response: %v", err)
	}
	if unknown.NotCreated["s2"].Type != "invalidProperties" || len(capture.Sent) != 1 {
		t.Errorf("unknown identity response = %s, sent = %d", unknownRaw, len(capture.Sent))
	}
}
