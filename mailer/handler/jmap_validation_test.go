package handler

import (
	"strings"
	"testing"
)

// jmapMethodResult finds the first method response with the given name and
// returns its arguments object. JMAP error responses use the name "error".
func jmapMethodResult(t *testing.T, resp map[string]interface{}, name string) map[string]interface{} {
	t.Helper()
	responses, _ := resp["methodResponses"].([]interface{})
	for _, raw := range responses {
		call, ok := raw.([]interface{})
		if !ok || len(call) < 2 || call[0] != name {
			continue
		}
		result, _ := call[1].(map[string]interface{})
		return result
	}
	t.Fatalf("no %q response in %v", name, resp)
	return nil
}

// jmapErrorType extracts the type field of a JMAP method-level error response.
func jmapErrorType(t *testing.T, result map[string]interface{}) string {
	t.Helper()
	if result == nil {
		t.Fatal("nil error result")
	}
	typ, _ := result["type"].(string)
	return typ
}

// TestJMAPUnknownArgumentRejected verifies RFC 8620 section 5 strict argument
// validation: an unrecognised argument produces invalidArguments rather than
// being silently ignored.
func TestJMAPUnknownArgumentRejected(t *testing.T) {
	h := newJMAPLimitHandler(t, 1<<20)
	token := jmapToken(t, h)
	resp := jmapRequest(t, h, token, []string{"urn:ietf:params:jmap:core", "urn:ietf:params:jmap:mail"},
		[]JmapMethodCall{{"Email/get", map[string]any{"accountId": "admin", "ids": []string{}, "bogus": 1}, "c1"}})
	if got := jmapErrorType(t, jmapMethodResult(t, resp, "error")); got != "invalidArguments" {
		t.Errorf("error type = %q, want invalidArguments", got)
	}
}

// TestJMAPUnknownCreatePropertyRejected verifies that an unknown create
// property is reported as an invalidProperties SetError listing the key.
func TestJMAPUnknownCreatePropertyRejected(t *testing.T) {
	h := newJMAPLimitHandler(t, 1<<20)
	token := jmapToken(t, h)
	resp := jmapRequest(t, h, token, []string{"urn:ietf:params:jmap:core", "urn:ietf:params:jmap:mail"},
		[]JmapMethodCall{{"Mailbox/set", map[string]any{
			"accountId": "admin",
			"create":    map[string]any{"c1": map[string]any{"name": "New", "bogus": 1}},
		}, "c1"}})
	result := jmapMethodResult(t, resp, "Mailbox/set")
	notCreated, _ := result["notCreated"].(map[string]interface{})
	entry, ok := notCreated["c1"].(map[string]interface{})
	if !ok {
		t.Fatalf("notCreated[c1] = %v, want a SetError", notCreated["c1"])
	}
	if got, _ := entry["type"].(string); got != "invalidProperties" {
		t.Errorf("SetError type = %q, want invalidProperties", got)
	}
	if desc, _ := entry["description"].(string); !strings.Contains(desc, "bogus") {
		t.Errorf("SetError description = %q, want it to list bogus", desc)
	}
}

// TestJMAPMalformedPatchRejected verifies RFC 8620 section 5.3 patch
// validation: a pointer with an invalid escape produces invalidPatch.
func TestJMAPMalformedPatchRejected(t *testing.T) {
	h := newJMAPLimitHandler(t, 1<<20)
	token := jmapToken(t, h)
	// Seed a mailbox to patch so the handler reaches patch validation.
	resp := jmapRequest(t, h, token, []string{"urn:ietf:params:jmap:core", "urn:ietf:params:jmap:mail"},
		[]JmapMethodCall{{"Mailbox/set", map[string]any{
			"accountId": "admin",
			"create":    map[string]any{"c1": map[string]any{"name": "PatchTarget"}},
		}, "c1"}})
	created := jmapMethodResult(t, resp, "Mailbox/set")
	createdMap, _ := created["created"].(map[string]interface{})
	target, ok := createdMap["c1"].(map[string]interface{})
	if !ok {
		t.Fatalf("mailbox create failed: %v", created)
	}
	id, _ := target["id"].(string)

	resp = jmapRequest(t, h, token, []string{"urn:ietf:params:jmap:core", "urn:ietf:params:jmap:mail"},
		[]JmapMethodCall{{"Mailbox/set", map[string]any{
			"accountId": "admin",
			"update":    map[string]any{id: map[string]any{"na~2me": "Changed"}},
		}, "c1"}})
	result := jmapMethodResult(t, resp, "Mailbox/set")
	notUpdated, _ := result["notUpdated"].(map[string]interface{})
	entry, ok := notUpdated[id].(map[string]interface{})
	if !ok {
		t.Fatalf("notUpdated[%s] = %v, want a SetError", id, notUpdated[id])
	}
	if got, _ := entry["type"].(string); got != "invalidPatch" {
		t.Errorf("SetError type = %q, want invalidPatch", got)
	}
}

// TestJMAPAccountlessMethodsReceiveNoAccountID verifies RFC 8620 section 7:
// PushSubscription methods are account-less, so the dispatcher must neither
// inject accountId nor require an account for them. Core/echo must return its
// arguments unchanged for the same reason.
func TestJMAPAccountlessMethodsReceiveNoAccountID(t *testing.T) {
	h := newJMAPLimitHandler(t, 1<<20)
	token := jmapToken(t, h)
	resp := jmapRequest(t, h, token, []string{"urn:ietf:params:jmap:core"},
		[]JmapMethodCall{
			{"PushSubscription/get", map[string]any{"ids": []string{}}, "c1"},
			{"Core/echo", map[string]any{"hello": "world"}, "c2"},
		})

	get := jmapMethodResult(t, resp, "PushSubscription/get")
	if _, ok := get["accountId"]; ok {
		t.Errorf("PushSubscription/get must not return accountId: %v", get)
	}
	if _, ok := get["error"]; ok {
		t.Fatalf("PushSubscription/get returned an error: %v", get)
	}

	echo := jmapMethodResult(t, resp, "Core/echo")
	if _, ok := echo["accountId"]; ok {
		t.Errorf("Core/echo must not contain an injected accountId: %v", echo)
	}
	if echo["hello"] != "world" {
		t.Errorf("Core/echo = %v, want the original arguments", echo)
	}
}

// TestJMAPUnknownMethodRemainsUnknownMethod verifies that relaxing the method
// name syntax check did not change the unknownMethod behaviour for a well
// formed but unrecognised method name.
func TestJMAPUnknownMethodRemainsUnknownMethod(t *testing.T) {
	h := newJMAPLimitHandler(t, 1<<20)
	token := jmapToken(t, h)
	resp := jmapRequest(t, h, token, []string{"urn:ietf:params:jmap:core"},
		[]JmapMethodCall{{"Echo", map[string]any{}, "c1"}})
	if got := jmapErrorType(t, jmapMethodResult(t, resp, "error")); got != "unknownMethod" {
		t.Errorf("error type = %q, want unknownMethod", got)
	}
}
