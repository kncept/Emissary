package handler

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/kncept/Emissary/mailer/auth"
)

// These tests drive VacationResponse through the real HTTP JMAP endpoint so
// the RFC 8620 /set shapes and RFC 8621 section 8 properties are verified after
// authentication, dispatch and result-reference processing.

func vacationAccountID(t *testing.T) string {
	t.Helper()
	return auth.AccountIDForEmail("bob@example.com")
}

func jmapVacationGet(t *testing.T, h http.Handler, token, accountID string, extra map[string]any) map[string]any {
	t.Helper()
	args := map[string]any{"accountId": accountID}
	for name, value := range extra {
		args[name] = value
	}
	resp := jmapRequest(t, h, token,
		[]string{"urn:ietf:params:jmap:core", "urn:ietf:params:jmap:vacationresponse"},
		[]JmapMethodCall{{"VacationResponse/get", args, "get1"}})
	return jmapMethodResult(t, resp, "VacationResponse/get")
}

func jmapVacationSet(t *testing.T, h http.Handler, token, accountID string, args map[string]any) (map[string]any, map[string]any) {
	t.Helper()
	args["accountId"] = accountID
	resp := jmapRequest(t, h, token,
		[]string{"urn:ietf:params:jmap:core", "urn:ietf:params:jmap:vacationresponse"},
		[]JmapMethodCall{{"VacationResponse/set", args, "set1"}})
	responses, _ := resp["methodResponses"].([]any)
	if len(responses) != 1 {
		t.Fatalf("methodResponses = %v, want one response", responses)
	}
	call, _ := responses[0].([]any)
	result, _ := call[1].(map[string]any)
	return result, map[string]any{"name": call[0]}
}

func vacationObject(t *testing.T, result map[string]any) map[string]any {
	t.Helper()
	list, ok := result["list"].([]any)
	if !ok || len(list) != 1 {
		t.Fatalf("list = %v, want the singleton", result["list"])
	}
	object, _ := list[0].(map[string]any)
	return object
}

func vacationSetError(t *testing.T, result map[string]any, field, id string) map[string]any {
	t.Helper()
	errors, _ := result[field].(map[string]any)
	entry, ok := errors[id].(map[string]any)
	if !ok {
		t.Fatalf("%s[%q] missing from %v", field, id, result)
	}
	return entry
}

func assertVacationProperties(t *testing.T, entry map[string]any, want map[string]any) {
	t.Helper()
	for name, expected := range want {
		if got := entry[name]; fmt.Sprint(got) != fmt.Sprint(expected) {
			t.Errorf("%s = %v, want %v", name, got, expected)
		}
	}
}

// TestVacationResponseAllPropertiesRoundTripRFC8621_8 proves every property
// defined by RFC 8621 section 8 survives a /set followed by a /get, and that a
// /set updated entry contains only server-changed properties (none here).
func TestVacationResponseAllPropertiesRoundTripRFC8621_8(t *testing.T) {
	h, _, _ := newHandlerForTest(t, "secret")
	accountID := vacationAccountID(t)
	_, token, _ := login(t, h, "bob@example.com", "anything")

	initial := jmapVacationGet(t, h, token, accountID, nil)
	initialObject := vacationObject(t, initial)
	assertVacationProperties(t, initialObject, map[string]any{
		"id": "singleton", "isEnabled": false, "fromDate": nil, "toDate": nil,
		"subject": nil, "textBody": nil, "htmlBody": nil,
	})

	want := map[string]any{
		"isEnabled": true,
		"fromDate":  "2026-01-01T00:00:00Z",
		"toDate":    "2026-02-01T00:00:00Z",
		"subject":   "Away",
		"textBody":  "Back soon",
		"htmlBody":  "<p>Back soon</p>",
	}
	setArgs := map[string]any{"update": map[string]any{"singleton": want}}
	result, invocation := jmapVacationSet(t, h, token, accountID, setArgs)
	if invocation["name"] != "VacationResponse/set" {
		t.Fatalf("response name = %v", invocation)
	}
	if result["newState"] == result["oldState"] {
		t.Errorf("state did not advance: %v", result)
	}
	updated, _ := result["updated"].(map[string]any)
	if entry, ok := updated["singleton"].(map[string]any); !ok || len(entry) != 0 {
		t.Errorf("updated[singleton] = %v, want an empty object", updated["singleton"])
	}
	for _, field := range []string{"created", "notCreated", "notUpdated", "notDestroyed"} {
		if result[field] != nil {
			t.Errorf("%s = %v, want null", field, result[field])
		}
	}
	if destroyed, ok := result["destroyed"].([]any); !ok || len(destroyed) != 0 {
		t.Errorf("destroyed = %v, want an empty array", result["destroyed"])
	}

	stored := vacationObject(t, jmapVacationGet(t, h, token, accountID, nil))
	want["id"] = "singleton"
	assertVacationProperties(t, stored, want)

	filtered := vacationObject(t, jmapVacationGet(t, h, token, accountID,
		map[string]any{"properties": []string{"id", "subject", "toDate"}}))
	if len(filtered) != 3 {
		t.Errorf("filtered object = %v, want three properties", filtered)
	}
	assertVacationProperties(t, filtered, map[string]any{
		"id": "singleton", "subject": "Away", "toDate": "2026-02-01T00:00:00Z",
	})
}

// TestVacationResponsePatchRFC8620_5_3 covers full-object patches, explicit
// nulls, immutable properties and malformed patches over HTTP.
func TestVacationResponsePatchRFC8620_5_3(t *testing.T) {
	h, _, _ := newHandlerForTest(t, "secret")
	accountID := vacationAccountID(t)
	_, token, _ := login(t, h, "bob@example.com", "anything")

	_, _ = jmapVacationSet(t, h, token, accountID, map[string]any{
		"update": map[string]any{"singleton": map[string]any{
			"isEnabled": true,
			"fromDate":  "2026-01-01T00:00:00Z",
			"toDate":    "2026-02-01T00:00:00Z",
			"subject":   "Away",
			"textBody":  "Back soon",
			"htmlBody":  "<p>Back soon</p>",
		}},
	})

	// A full-object patch may repeat every server-set property unchanged.
	fullObject := vacationObject(t, jmapVacationGet(t, h, token, accountID, nil))
	result, _ := jmapVacationSet(t, h, token, accountID, map[string]any{
		"update": map[string]any{"singleton": fullObject},
	})
	if result["updated"] == nil || result["notUpdated"] != nil {
		t.Fatalf("full-object patch = %v, want a successful update", result)
	}

	// Explicit null clears; an absent property keeps its value.
	result, _ = jmapVacationSet(t, h, token, accountID, map[string]any{
		"update": map[string]any{"singleton": map[string]any{
			"fromDate": nil,
			"toDate":   nil,
			"textBody": nil,
			"htmlBody": nil,
		}},
	})
	if result["updated"] == nil || result["notUpdated"] != nil {
		t.Fatalf("null-clearing patch = %v, want a successful update", result)
	}
	stored := vacationObject(t, jmapVacationGet(t, h, token, accountID, nil))
	assertVacationProperties(t, stored, map[string]any{
		"isEnabled": true, "fromDate": nil, "toDate": nil,
		"subject": "Away", "textBody": nil, "htmlBody": nil,
	})

	cases := []struct {
		name       string
		patch      map[string]any
		wantType   string
		wantFields map[string]any
	}{
		{
			name:       "immutable id",
			patch:      map[string]any{"id": "other"},
			wantType:   "invalidProperties",
			wantFields: map[string]any{"properties": []any{"id"}},
		},
		{
			name:       "unknown property",
			patch:      map[string]any{"bogus": true},
			wantType:   "invalidProperties",
			wantFields: map[string]any{"properties": []any{"bogus"}},
		},
		{
			name:       "non nullable isEnabled",
			patch:      map[string]any{"isEnabled": nil},
			wantType:   "invalidProperties",
			wantFields: map[string]any{"properties": []any{"isEnabled"}},
		},
		{
			name:       "invalid date",
			patch:      map[string]any{"fromDate": "yesterday"},
			wantType:   "invalidProperties",
			wantFields: map[string]any{"properties": []any{"fromDate"}},
		},
		{
			name:     "invalid string type",
			patch:    map[string]any{"subject": 42},
			wantType: "invalidProperties",
			wantFields: map[string]any{
				"properties": []any{"subject"},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result, _ := jmapVacationSet(t, h, token, accountID, map[string]any{
				"update": map[string]any{"singleton": tc.patch},
			})
			entry := vacationSetError(t, result, "notUpdated", "singleton")
			if entry["type"] != tc.wantType {
				t.Fatalf("SetError = %v, want %q", entry, tc.wantType)
			}
			for field, want := range tc.wantFields {
				if fmt.Sprint(entry[field]) != fmt.Sprint(want) {
					t.Errorf("%s = %v, want %v", field, entry[field], want)
				}
			}
			if result["updated"] != nil {
				t.Errorf("updated = %v, want null", result["updated"])
			}
		})
	}

	// RFC 8620 section 5.3 makes invalidPatch a per-record SetError.
	result, _ = jmapVacationSet(t, h, token, accountID, map[string]any{
		"update": map[string]any{"singleton": map[string]any{"subject/name": "x"}},
	})
	if got := vacationSetError(t, result, "notUpdated", "singleton")["type"]; got != "invalidPatch" {
		t.Errorf("invalid patch SetError = %v, want invalidPatch", got)
	}
}

// TestVacationResponseStateOnlyAdvancesOnChangeRFC8620_5_3 proves ifInState
// and state are driven by the stored value, not by the number of /set calls.
func TestVacationResponseStateOnlyAdvancesOnChangeRFC8620_5_3(t *testing.T) {
	h, _, _ := newHandlerForTest(t, "secret")
	accountID := vacationAccountID(t)
	_, token, _ := login(t, h, "bob@example.com", "anything")

	state := jmapVacationGet(t, h, token, accountID, nil)["state"].(string)
	result, _ := jmapVacationSet(t, h, token, accountID, map[string]any{
		"ifInState": state,
		"update":    map[string]any{"singleton": map[string]any{"isEnabled": false}},
	})
	if result["oldState"] != state || result["newState"] != state {
		t.Errorf("no-op state = %v, want %q unchanged", result, state)
	}

	result, _ = jmapVacationSet(t, h, token, accountID, map[string]any{
		"ifInState": state,
		"update":    map[string]any{"singleton": map[string]any{"isEnabled": true, "subject": "Away"}},
	})
	changed := result["newState"].(string)
	if changed == state {
		t.Errorf("real change did not advance state: %v", result)
	}

	// The new state is accepted; the superseded one is not.
	result, _ = jmapVacationSet(t, h, token, accountID, map[string]any{
		"ifInState": changed,
		"update":    map[string]any{"singleton": map[string]any{"subject": "Back"}},
	})
	if result["updated"] == nil {
		t.Fatalf("current state rejected: %v", result)
	}
	resp := jmapRequest(t, h, token,
		[]string{"urn:ietf:params:jmap:core", "urn:ietf:params:jmap:vacationresponse"},
		[]JmapMethodCall{{"VacationResponse/set", map[string]any{
			"accountId": accountID,
			"ifInState": state,
			"update":    map[string]any{"singleton": map[string]any{"subject": "Stale"}},
		}, "set3"}})
	if got := jmapErrorType(t, jmapMethodResult(t, resp, "error")); got != "stateMismatch" {
		t.Errorf("stale state error = %q, want stateMismatch", got)
	}
}

// TestVacationResponseSingletonErrorsRFC8621_8 checks the singleton contract
// from RFC 8621 section 8 with the RFC 8620 section 5.3 SetErrors.
func TestVacationResponseSingletonErrorsRFC8621_8(t *testing.T) {
	h, _, _ := newHandlerForTest(t, "secret")
	accountID := vacationAccountID(t)
	_, token, _ := login(t, h, "bob@example.com", "anything")

	result, _ := jmapVacationSet(t, h, token, accountID, map[string]any{
		"create": map[string]any{
			"c1": map[string]any{"subject": "Hello"},
			"c2": map[string]any{"id": "singleton"},
		},
		"destroy": []string{"singleton", "unknown"},
		"update":  map[string]any{"unknown": map[string]any{"subject": "Hello"}},
	})
	createError := vacationSetError(t, result, "notCreated", "c1")
	if createError["type"] != "singleton" {
		t.Errorf("create SetError = %v, want singleton", createError)
	}
	createError = vacationSetError(t, result, "notCreated", "c2")
	if createError["type"] != "invalidProperties" {
		t.Errorf("create with id = %v, want invalidProperties", createError)
	}
	if got := fmt.Sprint(createError["properties"]); got != "[id]" {
		t.Errorf("create properties = %s, want [id]", got)
	}
	if got := vacationSetError(t, result, "notDestroyed", "singleton")["type"]; got != "singleton" {
		t.Errorf("destroy singleton = %v, want singleton", got)
	}
	if got := vacationSetError(t, result, "notDestroyed", "unknown")["type"]; got != "notFound" {
		t.Errorf("destroy unknown = %v, want notFound", got)
	}
	if got := vacationSetError(t, result, "notUpdated", "unknown")["type"]; got != "notFound" {
		t.Errorf("update unknown = %v, want notFound", got)
	}

	getResult := jmapVacationGet(t, h, token, accountID,
		map[string]any{"ids": []string{"singleton", "unknown"}})
	if got := len(getResult["list"].([]any)); got != 1 {
		t.Errorf("list length = %d, want 1", got)
	}
	if got := fmt.Sprint(getResult["notFound"]); got != "[unknown]" {
		t.Errorf("notFound = %s, want [unknown]", got)
	}
}
