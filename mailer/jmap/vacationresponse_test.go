package jmap

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kncept/Emissary/mailer/components/metadata"
	"github.com/kncept/Emissary/mailer/components/sender"
	"github.com/kncept/Emissary/mailer/components/storage"
	"github.com/kncept/Emissary/mailer/components/usermanager"
)

func newVacationTestHandler(t *testing.T) (*Handler, *usermanager.FileManager) {
	t.Helper()
	ctx := context.Background()
	users, err := usermanager.NewFile(usermanager.Config{
		AdminUsername: "admin",
		AdminPassword: "admin-pass",
		FilesystemDir: filepath.Join(t.TempDir(), "users"),
	})
	if err != nil {
		t.Fatalf("NewFile() error = %v", err)
	}
	if err := users.Create(ctx, &usermanager.EmissaryUser{
		User:    usermanager.User{Email: "alice@example.com"},
		Enabled: true,
	}); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	h := NewHandler(storage.NewMemoryStorage(), metadata.NewMemoryMetadata(), users, sender.NewInMemoryRecording())
	return h, users
}

func decodeMap(t *testing.T, raw json.RawMessage) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode response %s: %v", raw, err)
	}
	return out
}

func TestVacationResponseGetSetPersistsToUserJSON(t *testing.T) {
	ctx := context.Background()
	h, users := newVacationTestHandler(t)

	// A fresh account has the default, disabled singleton.
	raw, err := h.VacationResponseGet(ctx, "alice@example.com", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("VacationResponseGet() error = %v", err)
	}
	resp := decodeMap(t, raw)
	list := resp["list"].([]any)
	if len(list) != 1 {
		t.Fatalf("list length = %d, want 1: %s", len(list), raw)
	}
	obj := list[0].(map[string]any)
	if obj["id"] != "singleton" || obj["isEnabled"] != false {
		t.Fatalf("default singleton = %v, want disabled singleton", obj)
	}
	for _, key := range []string{"fromDate", "toDate", "subject", "textBody", "htmlBody"} {
		if value, ok := obj[key]; !ok || value != nil {
			t.Errorf("default %s = %v (present=%v), want null", key, value, ok)
		}
	}
	initialState := resp["state"].(string)

	// Update a subset of properties, including dates and bodies.
	raw, err = h.VacationResponseSet(ctx, "alice@example.com", json.RawMessage(`{
		"update": {"singleton": {
			"isEnabled": true,
			"fromDate": "2026-01-01T00:00:00Z",
			"toDate": "2026-02-01T00:00:00Z",
			"subject": "Away",
			"textBody": "Back soon"
		}}
	}`))
	if err != nil {
		t.Fatalf("VacationResponseSet() error = %v", err)
	}
	setResp := decodeMap(t, raw)
	if setResp["newState"] == setResp["oldState"] {
		t.Errorf("state did not advance: %s", raw)
	}
	updated := setResp["updated"].(map[string]any)
	if _, ok := updated["singleton"]; !ok {
		t.Errorf("singleton not reported as updated: %s", raw)
	}

	// The settings must survive in the user's user.json record.
	stored, err := users.Get(ctx, "alice@example.com")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if !stored.VacationResponse.IsEnabled || stored.VacationResponse.Subject == nil || *stored.VacationResponse.Subject != "Away" {
		t.Fatalf("stored vacation settings = %+v", stored.VacationResponse)
	}

	// A subsequent get returns the persisted values and only requested fields.
	raw, err = h.VacationResponseGet(ctx, "alice@example.com", json.RawMessage(`{
		"properties": ["id", "subject", "textBody"]
	}`))
	if err != nil {
		t.Fatalf("VacationResponseGet() error = %v", err)
	}
	resp = decodeMap(t, raw)
	obj = resp["list"].([]any)[0].(map[string]any)
	if len(obj) != 3 {
		t.Errorf("filtered object has %d fields, want 3: %v", len(obj), obj)
	}
	if obj["id"] != "singleton" || obj["subject"] != "Away" || obj["textBody"] != "Back soon" {
		t.Errorf("filtered singleton = %v", obj)
	}
	if got := resp["state"]; got == initialState {
		t.Error("state did not change after update")
	}

	// A no-op update reports success without advancing state.
	raw, err = h.VacationResponseSet(ctx, "alice@example.com", json.RawMessage(`{
		"update": {"singleton": {"subject": "Away"}}
	}`))
	if err != nil {
		t.Fatalf("no-op VacationResponseSet() error = %v", err)
	}
	noOp := decodeMap(t, raw)
	if noOp["newState"] != noOp["oldState"] {
		t.Errorf("no-op update advanced state: %s", raw)
	}
}

// TestVacationResponseSetExplicitNullClears checks that a PatchObject value of
// null clears an optional field. Absent properties must leave the stored value
// untouched; JSON unmarshalling alone cannot tell the two apart.
func TestVacationResponseSetExplicitNullClears(t *testing.T) {
	ctx := context.Background()
	h, users := newVacationTestHandler(t)

	set := func(args string) {
		t.Helper()
		if _, err := h.VacationResponseSet(ctx, "alice@example.com", json.RawMessage(args)); err != nil {
			t.Fatalf("VacationResponseSet() error = %v", err)
		}
	}
	get := func() map[string]any {
		t.Helper()
		raw, err := h.VacationResponseGet(ctx, "alice@example.com", json.RawMessage(`{}`))
		if err != nil {
			t.Fatalf("VacationResponseGet() error = %v", err)
		}
		return decodeMap(t, raw)["list"].([]any)[0].(map[string]any)
	}

	set(`{"update":{"singleton":{
		"isEnabled":true,
		"fromDate":"2026-01-01T00:00:00Z",
		"toDate":"2026-02-01T00:00:00Z",
		"subject":"Away",
		"textBody":"Back soon",
		"htmlBody":"<p>Back soon</p>"
	}}}`)
	obj := get()
	for _, key := range []string{"fromDate", "toDate", "subject", "textBody", "htmlBody"} {
		if obj[key] == nil {
			t.Errorf("%s = nil after set, want a value", key)
		}
	}

	// An absent subject must keep its value; every other field is cleared by
	// an explicit null.
	set(`{"update":{"singleton":{
		"fromDate":null,
		"toDate":null,
		"textBody":null,
		"htmlBody":null
	}}}`)
	// isEnabled is not nullable and its whole patch is rejected atomically.
	raw, err := h.VacationResponseSet(ctx, "alice@example.com", json.RawMessage(`{"update":{"singleton":{"isEnabled":null}}}`))
	if err != nil {
		t.Fatalf("VacationResponseSet(isEnabled:null) error = %v", err)
	}
	if notUpdated := decodeMap(t, raw)["notUpdated"].(map[string]any); notUpdated["singleton"] == nil {
		t.Errorf("isEnabled:null was not rejected: %s", raw)
	}

	obj = get()
	for _, key := range []string{"fromDate", "toDate", "textBody", "htmlBody"} {
		if value, ok := obj[key]; !ok || value != nil {
			t.Errorf("%s = %v (present=%v), want null after explicit null", key, value, ok)
		}
	}
	if obj["subject"] != "Away" {
		t.Errorf("subject = %v after absent property, want unchanged Away", obj["subject"])
	}
	if obj["isEnabled"] != true {
		t.Errorf("isEnabled = %v, want unchanged true", obj["isEnabled"])
	}

	stored, err := users.Get(ctx, "alice@example.com")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	v := stored.VacationResponse
	if v.FromDate != nil || v.ToDate != nil || v.TextBody != nil || v.HTMLBody != nil {
		t.Errorf("stored vacation settings not cleared: %+v", v)
	}
	if v.Subject == nil || *v.Subject != "Away" || !v.IsEnabled {
		t.Errorf("stored settings lost unchanged fields: %+v", v)
	}
}

func TestVacationResponseGetIDsAndNotFound(t *testing.T) {
	ctx := context.Background()
	h, _ := newVacationTestHandler(t)

	raw, err := h.VacationResponseGet(ctx, "alice@example.com", json.RawMessage(`{"ids":["singleton","other"]}`))
	if err != nil {
		t.Fatalf("VacationResponseGet() error = %v", err)
	}
	resp := decodeMap(t, raw)
	if got := len(resp["list"].([]any)); got != 1 {
		t.Errorf("list length = %d, want 1", got)
	}
	notFound := resp["notFound"].([]any)
	if len(notFound) != 1 || notFound[0] != "other" {
		t.Errorf("notFound = %v, want [other]", notFound)
	}
}

func TestVacationResponseSetErrors(t *testing.T) {
	ctx := context.Background()
	h, _ := newVacationTestHandler(t)

	raw, err := h.VacationResponseGet(ctx, "alice@example.com", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("VacationResponseGet() error = %v", err)
	}
	state := decodeMap(t, raw)["state"].(string)

	// ifInState mismatch is a method-level error.
	_, err = h.VacationResponseSet(ctx, "alice@example.com", json.RawMessage(`{"ifInState":"stale","update":{}}`))
	if err == nil || !strings.Contains(err.Error(), "stateMismatch") {
		t.Fatalf("ifInState mismatch error = %v, want stateMismatch", err)
	}

	// Create and destroy of the singleton are rejected; unknown properties too.
	raw, err = h.VacationResponseSet(ctx, "alice@example.com", json.RawMessage(`{
		"create": {"singleton": {}},
		"destroy": ["singleton"],
		"update": {"singleton": {"notAProperty": 1}}
	}`))
	if err != nil {
		t.Fatalf("VacationResponseSet() error = %v", err)
	}
	resp := decodeMap(t, raw)
	notCreated := resp["notCreated"].(map[string]any)
	if err, ok := notCreated["singleton"].(map[string]any); !ok || err["type"] != "singleton" {
		t.Errorf("notCreated = %v, want singleton", notCreated)
	}
	notUpdated := resp["notUpdated"].(map[string]any)
	if err, ok := notUpdated["singleton"].(map[string]any); !ok || err["type"] != "invalidProperties" {
		t.Errorf("notUpdated = %v, want invalidProperties", notUpdated)
	}
	notDestroyed := resp["notDestroyed"].(map[string]any)
	if _, ok := notDestroyed["singleton"]; !ok {
		t.Errorf("notDestroyed = %v, want singleton", notDestroyed)
	}
	if resp["newState"] != state || resp["oldState"] != state {
		t.Errorf("failed set changed state: %s", raw)
	}
}

func TestVacationResponseChangesIsNotADispatchedMethod(t *testing.T) {
	for _, method := range AllMethods() {
		if method == "VacationResponse/changes" {
			t.Fatal("VacationResponse/changes is still registered")
		}
	}
}
