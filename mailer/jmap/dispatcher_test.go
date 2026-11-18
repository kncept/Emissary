package jmap

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/kncept/Emissary/mailer/auth"
	"github.com/kncept/Emissary/mailer/storage"
)

func TestResolveCreationIDs(t *testing.T) {
	createdIDs := map[string]string{
		"create1": "real-id-1",
		"create2": "real-id-2",
	}

	input := map[string]interface{}{
		"plain":   "unchanged",
		"value":   "#create1",
		"unknown": "#missing",
		"empty":   "#",
		"nested": []interface{}{
			"#create2",
			map[string]interface{}{
				"deep": "#create1",
			},
		},
		"onSuccessUpdateEmail": map[string]interface{}{
			"#create1": map[string]interface{}{"keywords/$draft": nil},
			"#missing": map[string]interface{}{"keywords/$draft": nil},
		},
	}

	resolved := resolveCreationIDs(input, createdIDs).(map[string]interface{})

	if got := resolved["value"]; got != "real-id-1" {
		t.Errorf("value = %v, want real-id-1", got)
	}
	if got := resolved["plain"]; got != "unchanged" {
		t.Errorf("plain = %v, want unchanged", got)
	}
	if got := resolved["unknown"]; got != "#missing" {
		t.Errorf("unknown reference = %v, want #missing left unchanged", got)
	}
	if got := resolved["empty"]; got != "#" {
		t.Errorf("empty reference = %v, want # left unchanged", got)
	}

	nested := resolved["nested"].([]interface{})
	if got := nested[0]; got != "real-id-2" {
		t.Errorf("nested[0] = %v, want real-id-2", got)
	}
	deep := nested[1].(map[string]interface{})
	if got := deep["deep"]; got != "real-id-1" {
		t.Errorf("nested.deep = %v, want real-id-1", got)
	}

	updates := resolved["onSuccessUpdateEmail"].(map[string]interface{})
	if _, ok := updates["real-id-1"]; !ok {
		t.Errorf("resolved map key missing: %v", updates)
	}
	if _, ok := updates["#missing"]; !ok {
		t.Errorf("unresolved map key should be preserved: %v", updates)
	}
}

// stubMethodHandler records the arguments it receives from the dispatcher and
// returns a response containing created ids. Embedding MethodHandler supplies
// the methods this test does not exercise.
type stubMethodHandler struct {
	MethodHandler
	emailSetArgs      map[string]json.RawMessage
	submissionSetArgs map[string]json.RawMessage
}

func (s *stubMethodHandler) EmailSet(_ context.Context, _ string, args json.RawMessage) (json.RawMessage, error) {
	if err := json.Unmarshal(args, &s.emailSetArgs); err != nil {
		return nil, err
	}
	return json.RawMessage(`{"accountId":"alice@example.com","created":{"create1":{"id":"real-email-id"}},"notCreated":{}}`), nil
}

func (s *stubMethodHandler) EmailSubmissionSet(_ context.Context, _ string, args json.RawMessage) (json.RawMessage, error) {
	if err := json.Unmarshal(args, &s.submissionSetArgs); err != nil {
		return nil, err
	}
	return json.RawMessage(`{"accountId":"alice@example.com","created":{"submission1":{"id":"real-email-id"}},"notCreated":{}}`), nil
}

func TestDispatchRequestResolvesCreationReferences(t *testing.T) {
	handler := &stubMethodHandler{}
	dispatcher := NewDispatcher(handler, nil)

	req := &Request{
		// A seeded reference (e.g. from a resumed request) must resolve too.
		CreatedIDs: map[string]string{"prior": "real-prior-id"},
		MethodCalls: []Invocation{
			{
				Name:     "Email/set",
				ClientID: "c1",
				Arguments: map[string]json.RawMessage{
					"accountId": json.RawMessage(`"alice@example.com"`),
					"create":    json.RawMessage(`{"create1":{"from":[{"email":"alice@example.com"}],"to":[]}}`),
				},
			},
			{
				Name:     "EmailSubmission/set",
				ClientID: "c2",
				Arguments: map[string]json.RawMessage{
					"accountId": json.RawMessage(`"alice@example.com"`),
					"create":    json.RawMessage(`{"submission1":{"emailId":"#create1","priorId":"#prior"}}`),
					"onSuccessUpdateEmail": json.RawMessage(
						`{"#create1":{"keywords/$draft":null},"#submission1":{"keywords/$draft":null}}`),
				},
			},
		},
	}

	claims := &auth.Claims{Subject: "alice@example.com"}
	resp, err := dispatcher.DispatchRequest(context.Background(), "alice@example.com", claims, req)
	if err != nil {
		t.Fatalf("DispatchRequest() error = %v", err)
	}

	// The first call has no references to resolve.
	if got := string(handler.emailSetArgs["create"]); !strings.Contains(got, `"email":"alice@example.com"`) {
		t.Errorf("Email/set create args = %s", got)
	}

	// The second call must see the real id created by the first call and the
	// seeded id from the request.
	submission := string(handler.submissionSetArgs["create"])
	if !strings.Contains(submission, `"emailId":"real-email-id"`) {
		t.Errorf("emailId was not resolved: %s", submission)
	}
	if !strings.Contains(submission, `"priorId":"real-prior-id"`) {
		t.Errorf("seeded reference was not resolved: %s", submission)
	}
	updates := string(handler.submissionSetArgs["onSuccessUpdateEmail"])
	if !strings.Contains(updates, `"real-email-id"`) {
		t.Errorf("onSuccessUpdateEmail key was not resolved: %s", updates)
	}

	// Both the seeded and newly-created references are returned to the client.
	want := map[string]string{
		"prior":       "real-prior-id",
		"create1":     "real-email-id",
		"submission1": "real-email-id",
	}
	if len(resp.CreatedIDs) != len(want) {
		t.Fatalf("createdIds = %v, want %v", resp.CreatedIDs, want)
	}
	for id, expected := range want {
		if got := resp.CreatedIDs[id]; got != expected {
			t.Errorf("createdIds[%q] = %q, want %q", id, got, expected)
		}
	}
}

func TestDispatchRequestPersistsCreatedIDsPerSession(t *testing.T) {
	store := storage.NewMemoryStorage()
	handler := &stubMethodHandler{}
	dispatcher := NewDispatcher(handler, store)
	claims := &auth.Claims{Subject: "alice@example.com", SID: "session-1"}
	ctx := context.Background()

	first := &Request{
		MethodCalls: []Invocation{{
			Name:     "Email/set",
			ClientID: "c1",
			Arguments: map[string]json.RawMessage{
				"accountId": json.RawMessage(`"alice@example.com"`),
				"create":    json.RawMessage(`{"create1":{"from":[{"email":"alice@example.com"}],"to":[]}}`),
			},
		}},
	}
	if _, err := dispatcher.DispatchRequest(ctx, "alice@example.com", claims, first); err != nil {
		t.Fatalf("first DispatchRequest() error = %v", err)
	}

	stored, err := store.LoadCreatedIDs(ctx, claims.SID)
	if err != nil {
		t.Fatalf("LoadCreatedIDs() error = %v", err)
	}
	if got := stored["create1"]; got != "real-email-id" {
		t.Fatalf("persisted create1 = %q, want real-email-id (map %v)", got, stored)
	}

	// A fresh request with no createdIds must still resolve the earlier
	// reference after a simulated Lambda restart.
	handler = &stubMethodHandler{}
	dispatcher = NewDispatcher(handler, store)
	second := &Request{
		MethodCalls: []Invocation{{
			Name:     "EmailSubmission/set",
			ClientID: "c2",
			Arguments: map[string]json.RawMessage{
				"accountId": json.RawMessage(`"alice@example.com"`),
				"create":    json.RawMessage(`{"submission1":{"emailId":"#create1"}}`),
			},
		}},
	}
	if _, err := dispatcher.DispatchRequest(ctx, "alice@example.com", claims, second); err != nil {
		t.Fatalf("second DispatchRequest() error = %v", err)
	}
	submission := string(handler.submissionSetArgs["create"])
	if !strings.Contains(submission, `"emailId":"real-email-id"`) {
		t.Errorf("persisted reference was not resolved: %s", submission)
	}
}
