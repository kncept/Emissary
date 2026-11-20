package jmap

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/kncept/Emissary/mailer/auth"
)

// resultRefHandler records the arguments Email/get receives and returns a
// configurable Email/query result. Embedding MethodHandler supplies the
// methods these tests do not exercise.
type resultRefHandler struct {
	MethodHandler
	emailGetArgs   map[string]json.RawMessage
	queryResponse  json.RawMessage
	emailGetCalled bool
}

func (h *resultRefHandler) EmailGet(_ context.Context, _ string, args json.RawMessage) (json.RawMessage, error) {
	h.emailGetCalled = true
	if err := json.Unmarshal(args, &h.emailGetArgs); err != nil {
		return nil, err
	}
	return json.RawMessage(`{"accountId":"alice@example.com","state":"s","list":[],"notFound":[]}`), nil
}

func (h *resultRefHandler) EmailQuery(context.Context, string, json.RawMessage) (json.RawMessage, error) {
	return h.queryResponse, nil
}

func (h *resultRefHandler) EmailSet(context.Context, string, json.RawMessage) (json.RawMessage, error) {
	return json.RawMessage(`{"accountId":"alice@example.com","created":{"create1":{"id":"real-email-id"}},"notCreated":{}}`), nil
}

func (h *resultRefHandler) EmailSubmissionSetWithImplicit(context.Context, string, json.RawMessage) (json.RawMessage, json.RawMessage, error) {
	return json.RawMessage(`{"ids":["primary-id"]}`), json.RawMessage(`{"ids":["implicit-id"]}`), nil
}

func resultRefRequest(calls ...Invocation) *Request {
	return &Request{MethodCalls: calls}
}

func emailGetWithRef(ref string) Invocation {
	return Invocation{
		Name:     "Email/get",
		ClientID: "get1",
		Arguments: map[string]json.RawMessage{
			"#ids": json.RawMessage(ref),
		},
	}
}

func errorType(t *testing.T, response Invocation) string {
	t.Helper()
	if response.Name != "error" {
		t.Fatalf("response name = %q, want error (arguments: %v)", response.Name, response.Arguments)
	}
	var typ string
	if err := json.Unmarshal(response.Arguments["type"], &typ); err != nil {
		t.Fatalf("unmarshal error type: %v", err)
	}
	return typ
}

func TestDispatchRequestResolvesResultReference(t *testing.T) {
	handler := &resultRefHandler{
		queryResponse: json.RawMessage(`{"accountId":"alice@example.com","queryState":"s","ids":["id-a","id-b"],"total":2}`),
	}
	dispatcher := NewDispatcher(handler, nil)

	resp, err := dispatcher.DispatchRequest(context.Background(), "alice@example.com",
		&auth.Claims{Subject: "alice@example.com"}, resultRefRequest(
			Invocation{
				Name:      "Email/query",
				ClientID:  "query1",
				Arguments: map[string]json.RawMessage{"accountId": json.RawMessage(`"alice@example.com"`)},
			},
			emailGetWithRef(`{"resultOf":"query1","name":"Email/query","path":"/ids/*"}`),
		))
	if err != nil {
		t.Fatalf("DispatchRequest() error = %v", err)
	}
	if len(resp.MethodResponses) != 2 {
		t.Fatalf("methodResponses = %d, want 2", len(resp.MethodResponses))
	}
	for i, response := range resp.MethodResponses {
		if response.Name == "error" {
			t.Fatalf("methodResponses[%d] = error: %v", i, response.Arguments)
		}
	}
	if !handler.emailGetCalled {
		t.Fatal("Email/get was not dispatched")
	}
	if got, want := string(handler.emailGetArgs["ids"]), `["id-a","id-b"]`; got != want {
		t.Errorf("Email/get ids = %s, want %s", got, want)
	}
	if _, ok := handler.emailGetArgs["#ids"]; ok {
		t.Errorf("Email/get still has a #ids argument: %v", handler.emailGetArgs)
	}
}

func TestDispatchRequestResolvesResultReferenceBeforeCreationID(t *testing.T) {
	// The referenced result itself contains a creation reference, which must
	// still be resolved after the result reference is replaced.
	handler := &resultRefHandler{
		queryResponse: json.RawMessage(`{"ids":["#create1","literal-id"]}`),
	}
	dispatcher := NewDispatcher(handler, nil)

	_, err := dispatcher.DispatchRequest(context.Background(), "alice@example.com",
		&auth.Claims{Subject: "alice@example.com"}, resultRefRequest(
			Invocation{
				Name:     "Email/set",
				ClientID: "create",
				Arguments: map[string]json.RawMessage{
					"create": json.RawMessage(`{"create1":{"subject":"draft"}}`),
				},
			},
			Invocation{Name: "Email/query", ClientID: "query1", Arguments: map[string]json.RawMessage{}},
			emailGetWithRef(`{"resultOf":"query1","name":"Email/query","path":"/ids/*"}`),
		))
	if err != nil {
		t.Fatalf("DispatchRequest() error = %v", err)
	}
	if got, want := string(handler.emailGetArgs["ids"]), `["real-email-id","literal-id"]`; got != want {
		t.Errorf("Email/get ids = %s, want %s", got, want)
	}
}

func TestDispatchRequestResultReferenceUsesFirstResponseForClientID(t *testing.T) {
	// EmailSubmission/set emits an implicit Email/set response with the same
	// method call id. RFC 8620 resolves against the first response only.
	handler := &resultRefHandler{}
	dispatcher := NewDispatcher(handler, nil)

	_, err := dispatcher.DispatchRequest(context.Background(), "alice@example.com",
		&auth.Claims{Subject: "alice@example.com"}, resultRefRequest(
			Invocation{Name: "EmailSubmission/set", ClientID: "sub1", Arguments: map[string]json.RawMessage{}},
			emailGetWithRef(`{"resultOf":"sub1","name":"EmailSubmission/set","path":"/ids/*"}`),
		))
	if err != nil {
		t.Fatalf("DispatchRequest() error = %v", err)
	}
	if got, want := string(handler.emailGetArgs["ids"]), `["primary-id"]`; got != want {
		t.Errorf("Email/get ids = %s, want %s (first response with the call id)", got, want)
	}
}

func TestDispatchRequestInvalidResultReferences(t *testing.T) {
	tests := []struct {
		name         string
		ref          string
		extraCalls   []Invocation
		wantResponse int
	}{
		{
			name:         "unknown method call id",
			ref:          `{"resultOf":"missing","name":"Email/query","path":"/ids/*"}`,
			wantResponse: 2,
		},
		{
			name:         "method name mismatch",
			ref:          `{"resultOf":"query1","name":"Email/get","path":"/ids/*"}`,
			wantResponse: 2,
		},
		{
			name:         "path does not resolve",
			ref:          `{"resultOf":"query1","name":"Email/query","path":"/missing"}`,
			wantResponse: 2,
		},
		{
			name:         "path is not a JSON pointer",
			ref:          `{"resultOf":"query1","name":"Email/query","path":"ids/*"}`,
			wantResponse: 2,
		},
		{
			name:         "invalid JSON pointer escape",
			ref:          `{"resultOf":"query1","name":"Email/query","path":"/ids/~2"}`,
			wantResponse: 2,
		},
		{
			name:         "array index out of range",
			ref:          `{"resultOf":"query1","name":"Email/query","path":"/ids/9"}`,
			wantResponse: 2,
		},
		{
			name:         "malformed result reference",
			ref:          `"not-an-object"`,
			wantResponse: 2,
		},
		{
			name:         "missing path",
			ref:          `{"resultOf":"query1","name":"Email/query"}`,
			wantResponse: 2,
		},
		{
			name:         "reference to a later call",
			ref:          `{"resultOf":"later","name":"Email/query","path":"/ids/*"}`,
			extraCalls:   []Invocation{{Name: "Email/query", ClientID: "later", Arguments: map[string]json.RawMessage{}}},
			wantResponse: 3,
		},
		{
			name:         "reference to the same call",
			ref:          `{"resultOf":"get1","name":"Email/get","path":"/ids/*"}`,
			wantResponse: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := &resultRefHandler{
				queryResponse: json.RawMessage(`{"ids":["id-a"]}`),
			}
			dispatcher := NewDispatcher(handler, nil)
			calls := []Invocation{
				{Name: "Email/query", ClientID: "query1", Arguments: map[string]json.RawMessage{}},
				emailGetWithRef(tt.ref),
			}
			calls = append(calls, tt.extraCalls...)
			resp, err := dispatcher.DispatchRequest(context.Background(), "alice@example.com",
				&auth.Claims{Subject: "alice@example.com"}, resultRefRequest(calls...))
			if err != nil {
				t.Fatalf("DispatchRequest() error = %v", err)
			}
			if len(resp.MethodResponses) != tt.wantResponse {
				t.Fatalf("methodResponses = %d, want %d", len(resp.MethodResponses), tt.wantResponse)
			}
			if got := errorType(t, resp.MethodResponses[1]); got != "invalidResultReference" {
				t.Errorf("error type = %q, want invalidResultReference", got)
			}
			if handler.emailGetCalled {
				t.Error("Email/get must not be dispatched when a result reference fails")
			}
		})
	}
}

func TestDispatchRequestDuplicateReferencedArgumentReturnsInvalidArguments(t *testing.T) {
	handler := &resultRefHandler{queryResponse: json.RawMessage(`{"ids":["id-a"]}`)}
	dispatcher := NewDispatcher(handler, nil)

	call := emailGetWithRef(`{"resultOf":"query1","name":"Email/query","path":"/ids/*"}`)
	call.Arguments["ids"] = json.RawMessage(`["literal-id"]`)
	resp, err := dispatcher.DispatchRequest(context.Background(), "alice@example.com",
		&auth.Claims{Subject: "alice@example.com"}, resultRefRequest(
			Invocation{Name: "Email/query", ClientID: "query1", Arguments: map[string]json.RawMessage{}},
			call,
		))
	if err != nil {
		t.Fatalf("DispatchRequest() error = %v", err)
	}
	if got := errorType(t, resp.MethodResponses[1]); got != "invalidArguments" {
		t.Errorf("error type = %q, want invalidArguments", got)
	}
	if handler.emailGetCalled {
		t.Error("Email/get must not be dispatched when both ids and #ids are given")
	}
}

func TestResolveJSONPointer(t *testing.T) {
	tests := []struct {
		name     string
		document string
		pointer  string
		want     string
	}{
		{"whole document", `{"x":1}`, "", `{"x":1}`},
		{"array wildcard", `{"ids":["a","b"]}`, "/ids/*", `["a","b"]`},
		{"wildcard with trailing token", `{"items":[{"id":1},{"id":2}]}`, "/items/*/id", `[1,2]`},
		{"nested wildcards are flattened", `{"groups":[{"members":["a","b"]},{"members":["c"]}]}`, "/groups/*/members/*", `["a","b","c"]`},
		{"array index", `{"ids":["a","b"]}`, "/ids/1", `"b"`},
		{"escaped slash and tilde", `{"a/b":{"~c":"ok"}}`, "/a~1b/~0c", `"ok"`},
		{"star is a normal object key", `{"star":{"*":"v"}}`, "/star/*", `"v"`},
		{"null value", `{"ids":null}`, "/ids", `null`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var document interface{}
			decoder := json.NewDecoder(strings.NewReader(tt.document))
			decoder.UseNumber()
			if err := decoder.Decode(&document); err != nil {
				t.Fatalf("decode document: %v", err)
			}
			got, ok := resolveJSONPointer(document, tt.pointer)
			if !ok {
				t.Fatalf("resolveJSONPointer(%q) failed", tt.pointer)
			}
			encoded, err := json.Marshal(got)
			if err != nil {
				t.Fatalf("marshal resolved value: %v", err)
			}
			if string(encoded) != tt.want {
				t.Errorf("resolved = %s, want %s", encoded, tt.want)
			}
		})
	}
}

func TestResolveJSONPointerFailures(t *testing.T) {
	document := map[string]interface{}{
		"ids": []interface{}{"a", "b"},
	}
	for _, pointer := range []string{
		"ids",              // not an absolute pointer
		"/missing",         // no such key
		"/ids/9",           // out of range
		"/ids/-1",          // negative index
		"/ids/01",          // leading zero
		"/ids/-",           // append token has no value
		"/ids/x",           // non-numeric index
		"/ids/+1",          // signed index
		"/ids/~2",          // invalid escape
		"/ids/~",           // dangling escape
		"/ids/0/missing",   // cannot descend into a string
		"/ids/*/missing",   // wildcard element has no such key
		"/missing/*/x",     // wildcard on a missing value
		"/ids/*/0/missing", // wildcard element has no such key
	} {
		if value, ok := resolveJSONPointer(document, pointer); ok {
			t.Errorf("resolveJSONPointer(%q) = %v, want failure", pointer, value)
		}
	}
}
