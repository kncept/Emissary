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
)

func TestIdentityPhase4SetGetChanges(t *testing.T) {
	const accountID = "alice@example.com"
	store := storage.NewMemoryStorage()
	handler := NewHandler(store, metadata.NewMemoryMetadata(),
		usermanager.NewNone(usermanager.Config{AdminUsername: "admin", AdminPassword: "password"}),
		sender.NewInMemoryRecording())
	ctx := context.Background()

	// Defaults come from the user record with no stored overrides.
	getResult, err := handler.IdentityGet(ctx, accountID, json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("IdentityGet() error = %v", err)
	}
	var getResp struct {
		List []struct {
			ID      string `json:"id"`
			Name    string `json:"name"`
			Email   string `json:"email"`
			ReplyTo []struct {
				Email string `json:"email"`
			} `json:"replyTo"`
		} `json:"list"`
	}
	if err := json.Unmarshal(getResult, &getResp); err != nil {
		t.Fatalf("unmarshal get response: %v", err)
	}
	if len(getResp.List) != 1 || getResp.List[0].ID != "primary" || getResp.List[0].Name != accountID || getResp.List[0].Email != accountID {
		t.Fatalf("default identity = %+v", getResp.List)
	}

	// Update the singleton, including an explicit null that clears replyTo.
	setArgs := json.RawMessage(fmt.Sprintf(`{
		"accountId":%q,
		"update":{"primary":{
			"name":"Alice Example",
			"replyTo":[{"name":"","email":"alice@example.com"}],
			"textSignature":"--\nAlice"
		}}
	}`, accountID))
	setResult, err := handler.IdentitySet(ctx, accountID, setArgs)
	if err != nil {
		t.Fatalf("IdentitySet() error = %v", err)
	}
	var setResp struct {
		Updated map[string]struct {
			Name          string                   `json:"name"`
			ReplyTo       []struct{ Email string } `json:"replyTo"`
			TextSignature string                   `json:"textSignature"`
		} `json:"updated"`
		NotUpdated map[string]interface{} `json:"notUpdated"`
	}
	if err := json.Unmarshal(setResult, &setResp); err != nil {
		t.Fatalf("unmarshal set response: %v", err)
	}
	updated, ok := setResp.Updated["primary"]
	if !ok || updated.Name != "Alice Example" || updated.TextSignature != "--\nAlice" || len(updated.ReplyTo) != 1 || len(setResp.NotUpdated) != 0 {
		t.Fatalf("set response = %+v, want updated singleton", setResp)
	}

	getResult, err = handler.IdentityGet(ctx, accountID, json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("IdentityGet() after set error = %v", err)
	}
	if err := json.Unmarshal(getResult, &getResp); err != nil {
		t.Fatalf("unmarshal after-set response: %v", err)
	}
	if len(getResp.List) != 1 || getResp.List[0].Name != "Alice Example" || len(getResp.List[0].ReplyTo) != 1 {
		t.Fatalf("after set = %+v, want persisted overrides", getResp.List)
	}

	changesResult, err := handler.IdentityChanges(ctx, accountID, json.RawMessage(`{"sinceState":"identity-0"}`))
	if err != nil {
		t.Fatalf("IdentityChanges() error = %v", err)
	}
	var changesResp struct {
		Updated []string `json:"updated"`
	}
	if err := json.Unmarshal(changesResult, &changesResp); err != nil {
		t.Fatalf("unmarshal changes response: %v", err)
	}
	found := false
	for _, id := range changesResp.Updated {
		if id == "primary" {
			found = true
		}
	}
	if !found {
		t.Errorf("changes.updated = %v, want primary", changesResp.Updated)
	}

	// An explicit null clears the replyTo list and the signature.
	clearArgs := json.RawMessage(fmt.Sprintf(
		`{"accountId":%q,"update":{"primary":{"replyTo":null,"textSignature":null}}}`,
		accountID))
	if _, err := handler.IdentitySet(ctx, accountID, clearArgs); err != nil {
		t.Fatalf("IdentitySet(clear) error = %v", err)
	}
	getResult, err = handler.IdentityGet(ctx, accountID, json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("IdentityGet() after clear error = %v", err)
	}
	var clearResp struct {
		List []struct {
			ReplyTo       []struct{ Email string } `json:"replyTo"`
			TextSignature string                   `json:"textSignature"`
		} `json:"list"`
	}
	if err := json.Unmarshal(getResult, &clearResp); err != nil {
		t.Fatalf("unmarshal clear response: %v", err)
	}
	if len(clearResp.List) != 1 || len(clearResp.List[0].ReplyTo) != 0 || clearResp.List[0].TextSignature != "" {
		t.Fatalf("after clear = %+v, want replyTo cleared", clearResp.List)
	}

	// The default identity cannot be destroyed, but new identities can be
	// created (RFC 8621 section 6.3).
	rejectArgs := json.RawMessage(fmt.Sprintf(
		`{"accountId":%q,"create":{"other":{"email":"other@example.com"}},"destroy":["primary"]}`,
		accountID))
	rejectResult, err := handler.IdentitySet(ctx, accountID, rejectArgs)
	if err != nil {
		t.Fatalf("IdentitySet(mixed) error = %v", err)
	}
	var rejectResp struct {
		Created      map[string]struct{ ID string }   `json:"created"`
		NotDestroyed map[string]struct{ Type string } `json:"notDestroyed"`
	}
	if err := json.Unmarshal(rejectResult, &rejectResp); err != nil {
		t.Fatalf("unmarshal mixed response: %v", err)
	}
	if rejectResp.Created["other"].ID == "" || rejectResp.NotDestroyed["primary"].Type != "forbidden" {
		t.Fatalf("mixed response = %+v", rejectResp)
	}
}

func TestIdentityPhase4NullDefaultsAndForbiddenEmail(t *testing.T) {
	const accountID = "alice@example.com"
	store := storage.NewMemoryStorage()
	handler := NewHandler(store, metadata.NewMemoryMetadata(),
		usermanager.NewNone(usermanager.Config{AdminUsername: "admin", AdminPassword: "password"}),
		sender.NewInMemoryRecording())
	ctx := context.Background()

	// replyTo and bcc default to JSON null, not an empty array.
	getResult, err := handler.IdentityGet(ctx, accountID, json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("IdentityGet() error = %v", err)
	}
	var rawResp struct {
		List []map[string]json.RawMessage `json:"list"`
	}
	if err := json.Unmarshal(getResult, &rawResp); err != nil {
		t.Fatalf("unmarshal get response: %v", err)
	}
	if len(rawResp.List) != 1 {
		t.Fatalf("list = %v", rawResp.List)
	}
	for _, name := range []string{"replyTo", "bcc"} {
		if got := strings.TrimSpace(string(rawResp.List[0][name])); got != "null" {
			t.Errorf("default %s = %s, want null", name, got)
		}
	}

	// A mismatched email is reported with its own forbiddenFrom SetError.
	var setResp struct {
		Updated    map[string]json.RawMessage `json:"updated"`
		NotUpdated map[string]struct {
			Type string `json:"type"`
		} `json:"notUpdated"`
	}
	setResult, err := handler.IdentitySet(ctx, accountID, json.RawMessage(fmt.Sprintf(
		`{"accountId":%q,"update":{"primary":{"email":"mallory@example.net","name":"Changed"}}}`,
		accountID)))
	if err != nil {
		t.Fatalf("IdentitySet(mismatched email) error = %v", err)
	}
	if err := json.Unmarshal(setResult, &setResp); err != nil {
		t.Fatalf("unmarshal set response: %v", err)
	}
	if setResp.NotUpdated["primary"].Type != "forbiddenFrom" || len(setResp.Updated) != 0 {
		t.Errorf("mismatched email response = %s, want forbiddenFrom and no update", setResult)
	}

	// A matching email is a no-op and the rest of the patch still applies.
	setResult, err = handler.IdentitySet(ctx, accountID, json.RawMessage(fmt.Sprintf(
		`{"accountId":%q,"update":{"primary":{"email":%q,"name":"Legit"}}}`,
		accountID, accountID)))
	if err != nil {
		t.Fatalf("IdentitySet(matching email) error = %v", err)
	}
	setResp = struct {
		Updated    map[string]json.RawMessage `json:"updated"`
		NotUpdated map[string]struct {
			Type string `json:"type"`
		} `json:"notUpdated"`
	}{}
	if err := json.Unmarshal(setResult, &setResp); err != nil {
		t.Fatalf("unmarshal set response: %v", err)
	}
	if len(setResp.NotUpdated) != 0 || len(setResp.Updated) != 1 {
		t.Errorf("matching email response = %s, want successful update", setResult)
	}
}
