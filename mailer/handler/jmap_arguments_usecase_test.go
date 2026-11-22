package handler

import (
	"testing"
)

type jmapMethodResponse struct {
	name string
	args map[string]interface{}
}

func firstMethodResponse(t *testing.T, resp map[string]interface{}) jmapMethodResponse {
	t.Helper()
	responses, ok := resp["methodResponses"].([]interface{})
	if !ok || len(responses) == 0 {
		t.Fatalf("methodResponses = %v, want at least one response", resp["methodResponses"])
	}
	call, ok := responses[0].([]interface{})
	if !ok || len(call) < 2 {
		t.Fatalf("method response = %v, want a JMAP tuple", responses[0])
	}
	name, _ := call[0].(string)
	args, _ := call[1].(map[string]interface{})
	return jmapMethodResponse{name: name, args: args}
}

func TestJMAPArgumentsUseCase_UnsupportedRejected(t *testing.T) {
	h, _, _ := newHandlerForTest(t, "secret")
	_, token, _ := login(t, h, "bob@example.com", "anything")
	const accountID = "bob@example.com"

	mailUsing := []string{"urn:ietf:params:jmap:mail"}
	submissionUsing := []string{"urn:ietf:params:jmap:mail", "urn:ietf:params:jmap:submission"}

	for _, tc := range []struct {
		name    string
		using   []string
		method  string
		args    map[string]interface{}
		wantErr string
	}{
		{
			name:   "unsupported sort",
			using:  mailUsing,
			method: "Email/query",
			args: map[string]interface{}{
				"accountId": accountID,
				"sort":      []interface{}{map[string]interface{}{"property": "subject"}},
			},
			wantErr: "unsupportedSort",
		},
		{
			name:   "unsupported filter",
			using:  mailUsing,
			method: "Email/query",
			args: map[string]interface{}{
				"accountId": accountID,
				"filter":    map[string]interface{}{"operator": "or", "conditions": []interface{}{}},
			},
			wantErr: "unsupportedFilter",
		},
		{
			name:   "sendAt rejected",
			using:  submissionUsing,
			method: "EmailSubmission/set",
			args: map[string]interface{}{
				"accountId": accountID,
				"create": map[string]interface{}{
					"s1": map[string]interface{}{
						"identityId": "primary",
						"emailId":    "e1",
						"sendAt":     "2030-01-01T00:00:00Z",
					},
				},
			},
			wantErr: "invalidArguments",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := jmapRequest(t, h, token, tc.using, []JmapMethodCall{{tc.method, tc.args, "c1"}})
			call := firstMethodResponse(t, response)
			if call.name != "error" {
				t.Fatalf("response name = %q, want error (args %v)", call.name, call.args)
			}
			if got := call.args["type"]; got != tc.wantErr {
				t.Errorf("error type = %v, want %s", got, tc.wantErr)
			}
		})
	}

	// RFC 8620 section 5.4: total is only present when requested.
	t.Run("total omitted unless requested", func(t *testing.T) {
		for _, tc := range []struct {
			calculateTotal interface{}
			wantPresent    bool
		}{
			{calculateTotal: nil, wantPresent: false},
			{calculateTotal: false, wantPresent: false},
			{calculateTotal: true, wantPresent: true},
		} {
			args := map[string]interface{}{"accountId": accountID}
			if tc.calculateTotal != nil {
				args["calculateTotal"] = tc.calculateTotal
			}
			response := jmapRequest(t, h, token, mailUsing, []JmapMethodCall{{"Email/query", args, "q1"}})
			call := firstMethodResponse(t, response)
			if call.name != "Email/query" {
				t.Fatalf("response name = %q, want Email/query (args %v)", call.name, call.args)
			}
			_, present := call.args["total"]
			if present != tc.wantPresent {
				t.Errorf("calculateTotal=%v: total present = %v, want %v", tc.calculateTotal, present, tc.wantPresent)
			}
		}
	})
}
