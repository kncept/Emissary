package handler

import (
	"encoding/json"
	"fmt"
	"testing"
)

// TestJMAPFullCapabilityUseCase sends one HTTP JMAP batch that opts into all
// four advertised capabilities and invokes every one of the 30 advertised
// methods. The Phase 4 exit criterion is that none of them is answered with
// unknownMethod: every advertised method must be backed by a real handler.
func TestJMAPFullCapabilityUseCase(t *testing.T) {
	h, _, _ := newHandlerForTest(t, "secret")
	_, token, _ := login(t, h, "bob@example.com", "anything")
	const accountID = "bob@example.com"
	using := []string{
		"urn:ietf:params:jmap:core",
		"urn:ietf:params:jmap:mail",
		"urn:ietf:params:jmap:submission",
		"urn:ietf:params:jmap:vacationresponse",
	}

	// Minimal but type-correct arguments: methods may return a validation
	// error (for example cannotCalculateChanges for a synthetic state), but
	// they must all be recognised and dispatched.
	calls := []JmapMethodCall{
		{"Core/echo", map[string]any{}, "m01"},
		{"Blob/copy", map[string]any{"fromAccountId": accountID, "accountId": accountID, "blobIds": []string{}}, "m02"},
		{"Mailbox/get", map[string]any{"accountId": accountID}, "m03"},
		{"Mailbox/changes", map[string]any{"accountId": accountID, "sinceState": "0"}, "m04"},
		{"Mailbox/query", map[string]any{"accountId": accountID}, "m05"},
		{"Mailbox/queryChanges", map[string]any{"accountId": accountID, "sinceQueryState": "0", "filter": map[string]any{}, "sort": []any{}}, "m06"},
		{"Mailbox/set", map[string]any{"accountId": accountID}, "m07"},
		{"Thread/get", map[string]any{"accountId": accountID}, "m08"},
		{"Thread/changes", map[string]any{"accountId": accountID, "sinceState": "0"}, "m09"},
		{"Email/get", map[string]any{"accountId": accountID, "ids": []string{}}, "m10"},
		{"Email/changes", map[string]any{"accountId": accountID, "sinceState": "0"}, "m11"},
		{"Email/query", map[string]any{"accountId": accountID}, "m12"},
		{"Email/queryChanges", map[string]any{"accountId": accountID, "sinceQueryState": "0", "filter": map[string]any{}, "sort": []any{}}, "m13"},
		{"Email/set", map[string]any{"accountId": accountID}, "m14"},
		{"Email/copy", map[string]any{"fromAccountId": accountID, "accountId": accountID, "create": map[string]any{}}, "m15"},
		{"Email/import", map[string]any{"accountId": accountID, "emails": map[string]any{}}, "m16"},
		{"Email/parse", map[string]any{"accountId": accountID, "blobIds": []string{}}, "m17"},
		{"SearchSnippet/get", map[string]any{"accountId": accountID, "emailIds": []string{}}, "m18"},
		{"Identity/get", map[string]any{"accountId": accountID}, "m19"},
		{"Identity/changes", map[string]any{"accountId": accountID, "sinceState": "0"}, "m20"},
		{"Identity/set", map[string]any{"accountId": accountID}, "m21"},
		{"EmailSubmission/get", map[string]any{"accountId": accountID, "ids": []string{}}, "m22"},
		{"EmailSubmission/changes", map[string]any{"accountId": accountID, "sinceState": "0"}, "m23"},
		{"EmailSubmission/query", map[string]any{"accountId": accountID}, "m24"},
		{"EmailSubmission/queryChanges", map[string]any{"accountId": accountID, "sinceQueryState": "0", "filter": map[string]any{}, "sort": []any{}}, "m25"},
		{"EmailSubmission/set", map[string]any{"accountId": accountID}, "m26"},
		{"VacationResponse/get", map[string]any{"accountId": accountID}, "m27"},
		{"VacationResponse/set", map[string]any{"accountId": accountID}, "m28"},
		{"PushSubscription/get", map[string]any{}, "m29"},
		{"PushSubscription/set", map[string]any{}, "m30"},
	}
	if len(calls) != 30 {
		t.Fatalf("test covers %d methods, want all 30", len(calls))
	}

	// The Session advertises maxCallsInRequest: 16, so the full sweep is sent
	// as two batches. Every batch still opts into all four capabilities.
	const maxCalls = 15
	for start := 0; start < len(calls); start += maxCalls {
		end := start + maxCalls
		if end > len(calls) {
			end = len(calls)
		}
		batch := calls[start:end]
		resp := jmapRequest(t, h, token, using, batch)
		responses, ok := resp["methodResponses"].([]any)
		if !ok {
			t.Fatalf("methodResponses missing: %v", resp)
		}
		if len(responses) != len(batch) {
			t.Fatalf("methodResponses = %d, want %d", len(responses), len(batch))
		}
		for i, raw := range responses {
			call, ok := raw.([]any)
			if !ok || len(call) < 2 {
				t.Errorf("methodResponses[%d] = %v, want [name args]", i, raw)
				continue
			}
			name, _ := call[0].(string)
			args, _ := call[1].(map[string]any)
			if name == "error" {
				var typ string
				if rawType, ok := args["type"]; ok {
					typ, _ = rawType.(string)
				}
				if typ == "unknownMethod" {
					t.Errorf("method %q returned unknownMethod", batch[i][0])
				}
				continue
			}
			wantName, _ := batch[i][0].(string)
			if name != wantName {
				t.Errorf("methodResponses[%d] = %q, want %q", i, name, wantName)
			}
		}
		if _, ok := resp["error"]; ok {
			t.Fatalf("unexpected top-level error: %s", mustMarshal(t, resp))
		}
	}
}

func mustMarshal(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return fmt.Sprintf("%s", b)
}
