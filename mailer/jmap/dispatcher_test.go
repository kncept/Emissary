package jmap

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/kncept/Emissary/mailer/auth"
	"github.com/kncept/Emissary/mailer/components/metadata"
	"github.com/kncept/Emissary/mailer/components/sender"
	"github.com/kncept/Emissary/mailer/components/storage"
	"github.com/kncept/Emissary/mailer/components/usermanager"
)

// allUsing mirrors a Request that opts in to every advertised capability.
// Direct dispatcher tests bypass DecodeRequest, so they must supply it.
var allUsing = []string{CapabilityCore, CapabilityMail, CapabilitySubmission, CapabilityVacationResponse}

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
		Using: allUsing,
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

func TestDispatchRequestCreatedIDsOnlyWhenRequested(t *testing.T) {
	handler := &stubMethodHandler{}
	dispatcher := NewDispatcher(handler, nil)
	claims := &auth.Claims{Subject: "alice@example.com"}
	ctx := context.Background()
	call := Invocation{
		Name:     "Email/set",
		ClientID: "c1",
		Arguments: map[string]json.RawMessage{
			"accountId": json.RawMessage(`"alice@example.com"`),
			"create":    json.RawMessage(`{"create1":{"subject":"draft"}}`),
		},
	}

	resp, err := dispatcher.DispatchRequest(ctx, "alice@example.com", claims,
		&Request{Using: allUsing, MethodCalls: []Invocation{call}})
	if err != nil {
		t.Fatalf("DispatchRequest() error = %v", err)
	}
	if resp.CreatedIDs != nil {
		t.Errorf("CreatedIDs = %v, want nil when the request omitted createdIds", resp.CreatedIDs)
	}
	encoded, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}
	if strings.Contains(string(encoded), `"createdIds"`) {
		t.Errorf("response JSON contains createdIds: %s", encoded)
	}

	// An empty createdIds object still opts in to receiving the full map.
	resp, err = dispatcher.DispatchRequest(ctx, "alice@example.com", claims,
		&Request{Using: allUsing, CreatedIDs: map[string]string{}, MethodCalls: []Invocation{call}})
	if err != nil {
		t.Fatalf("DispatchRequest() error = %v", err)
	}
	if resp.CreatedIDs == nil || resp.CreatedIDs["create1"] != "real-email-id" {
		t.Errorf("CreatedIDs = %v, want create1 mapped to real-email-id", resp.CreatedIDs)
	}
}

func TestResponseMarshalIncludesEmptyCreatedIDsWhenRequested(t *testing.T) {
	withIDs, err := json.Marshal(Response{includeCreatedIDs: true})
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}
	if !strings.Contains(string(withIDs), `"createdIds":{}`) {
		t.Errorf("response JSON = %s, want an empty createdIds object", withIDs)
	}

	withoutIDs, err := json.Marshal(Response{})
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}
	if strings.Contains(string(withoutIDs), `"createdIds"`) {
		t.Errorf("response JSON = %s, want no createdIds property", withoutIDs)
	}

	direct, err := json.Marshal(Response{CreatedIDs: map[string]string{"c1": "id-1"}})
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}
	if !strings.Contains(string(direct), `"createdIds":{"c1":"id-1"}`) {
		t.Errorf("response JSON = %s, want createdIds from a directly built response", direct)
	}
}

func TestDispatchRequestResolvesCreationReferencesWithoutRequestCreatedIDs(t *testing.T) {
	handler := &stubMethodHandler{}
	dispatcher := NewDispatcher(handler, nil)

	resp, err := dispatcher.DispatchRequest(context.Background(), "alice@example.com",
		&auth.Claims{Subject: "alice@example.com"}, &Request{Using: allUsing, MethodCalls: []Invocation{
			{
				Name:     "Email/set",
				ClientID: "c1",
				Arguments: map[string]json.RawMessage{
					"accountId": json.RawMessage(`"alice@example.com"`),
					"create":    json.RawMessage(`{"create1":{"subject":"draft"}}`),
				},
			},
			{
				Name:     "EmailSubmission/set",
				ClientID: "c2",
				Arguments: map[string]json.RawMessage{
					"create": json.RawMessage(`{"sub1":{"emailId":"#create1"}}`),
				},
			},
		}})
	if err != nil {
		t.Fatalf("DispatchRequest() error = %v", err)
	}
	if got := string(handler.submissionSetArgs["create"]); !strings.Contains(got, `"emailId":"real-email-id"`) {
		t.Errorf("creation reference was not resolved without request createdIds: %s", got)
	}
	if resp.CreatedIDs != nil {
		t.Errorf("CreatedIDs = %v, want nil when the request omitted createdIds", resp.CreatedIDs)
	}
}

func TestDispatchRequestPersistsCreatedIDsPerSession(t *testing.T) {
	store := storage.NewMemoryStorage()
	handler := &stubMethodHandler{}
	dispatcher := NewDispatcher(handler, store)
	claims := &auth.Claims{Subject: "alice@example.com", SID: "session-1"}
	ctx := context.Background()

	first := &Request{
		Using: allUsing,
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

	stored, _, err := store.LoadCreatedIDs(ctx, claims.SID)
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
		Using: allUsing,
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

// blockingCreatedIDHandler lets a test hold one dispatcher after its initial
// load but before it records a creation id, forcing a revision conflict.
type blockingCreatedIDHandler struct {
	MethodHandler
	createdID string
	started   chan struct{}
	release   chan struct{}
}

func (h *blockingCreatedIDHandler) EmailSet(_ context.Context, _ string, _ json.RawMessage) (json.RawMessage, error) {
	if h.started != nil {
		close(h.started)
		select {
		case <-h.release:
		case <-time.After(5 * time.Second):
			return nil, errors.New("timed out waiting for test release")
		}
	}
	return json.RawMessage(fmt.Sprintf(`{"created":{%q:{"id":%q}},"notCreated":{}}`, h.createdID, h.createdID)), nil
}

func createdIDRequest(creationID string) *Request {
	return &Request{
		Using: allUsing,
		MethodCalls: []Invocation{{
			Name:     "Email/set",
			ClientID: "call-" + creationID,
			Arguments: map[string]json.RawMessage{
				"accountId": json.RawMessage(`"alice@example.com"`),
				"create": json.RawMessage(fmt.Sprintf(
					`{%q:{"from":[{"email":"alice@example.com"}],"to":[]}}`, creationID)),
			},
		}},
	}
}

func TestDispatchRequestConcurrentSessionsDoNotLoseCreatedIDs(t *testing.T) {
	store := storage.NewMemoryStorage()
	claims := &auth.Claims{Subject: "alice@example.com", SID: "session-1"}
	ctx := context.Background()

	started := make(chan struct{})
	release := make(chan struct{})
	first := NewDispatcher(&blockingCreatedIDHandler{
		createdID: "id-from-first", started: started, release: release,
	}, store)
	second := NewDispatcher(&blockingCreatedIDHandler{createdID: "id-from-second"}, store)

	firstDone := make(chan error, 1)
	go func() {
		_, err := first.DispatchRequest(ctx, claims.Subject, claims, createdIDRequest("created-first"))
		firstDone <- err
	}()

	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("first dispatcher did not reach Email/set")
	}
	if _, err := second.DispatchRequest(ctx, claims.Subject, claims, createdIDRequest("created-second")); err != nil {
		t.Fatalf("second DispatchRequest() error = %v", err)
	}
	close(release)
	select {
	case err := <-firstDone:
		if err != nil {
			t.Fatalf("first DispatchRequest() error = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("first dispatcher did not finish after conflict retry")
	}

	stored, _, err := store.LoadCreatedIDs(ctx, claims.SID)
	if err != nil {
		t.Fatalf("LoadCreatedIDs() error = %v", err)
	}
	if stored["id-from-first"] != "id-from-first" || stored["id-from-second"] != "id-from-second" {
		t.Errorf("stored ids = %v, want both invocations' ids", stored)
	}
}

func TestDispatchRequestMalformedRecipientReturnsInvalidArguments(t *testing.T) {
	handler := NewHandler(storage.NewMemoryStorage(), metadata.NewMemoryMetadata(),
		usermanager.NewNone(usermanager.Config{AdminUsername: "admin", AdminPassword: "password"}),
		sender.NewLogSender())
	dispatcher := NewDispatcher(handler, nil)
	req := &Request{
		Using: allUsing,
		MethodCalls: []Invocation{{
			Name:     "Email/set",
			ClientID: "c1",
			Arguments: map[string]json.RawMessage{
				"accountId": json.RawMessage(`"alice@example.com"`),
				"create":    json.RawMessage(`{"c1":{"to":[{"email":123}]}}`),
			},
		}},
	}

	resp, err := dispatcher.DispatchRequest(context.Background(), "alice@example.com",
		&auth.Claims{Subject: "alice@example.com"}, req)
	if err != nil {
		t.Fatalf("DispatchRequest() error = %v", err)
	}
	if len(resp.MethodResponses) != 1 {
		t.Fatalf("method responses = %d, want 1", len(resp.MethodResponses))
	}
	response := resp.MethodResponses[0]
	if response.Name != "error" {
		t.Fatalf("response name = %q, want error", response.Name)
	}
	var errorType string
	if err := json.Unmarshal(response.Arguments["type"], &errorType); err != nil {
		t.Fatalf("unmarshal error type: %v", err)
	}
	if errorType != "invalidArguments" {
		t.Errorf("error type = %q, want invalidArguments", errorType)
	}
}

// pushMethodHandler records arguments passed to the PushSubscription methods.
// Embedding MethodHandler supplies methods this test does not exercise.
type pushMethodHandler struct {
	MethodHandler
	getArgs map[string]json.RawMessage
	setArgs map[string]json.RawMessage
}

func (s *pushMethodHandler) PushSubscriptionGet(_ context.Context, _ string, args json.RawMessage) (json.RawMessage, error) {
	if err := json.Unmarshal(args, &s.getArgs); err != nil {
		return nil, err
	}
	return json.RawMessage(`{"list":[],"notFound":[]}`), nil
}

func (s *pushMethodHandler) PushSubscriptionSet(_ context.Context, _ string, args json.RawMessage) (json.RawMessage, error) {
	if err := json.Unmarshal(args, &s.setArgs); err != nil {
		return nil, err
	}
	return json.RawMessage(`{"created":{},"updated":{},"destroyed":[],"notCreated":{},"notUpdated":{},"notDestroyed":{}}`), nil
}

func TestJMAPErrorTypeMapping(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want string
	}{
		{errors.New("fromAccountNotFound: no such account"), "fromAccountNotFound"},
		{errors.New("stateMismatch: stale state"), "stateMismatch"},
		{errors.New("forbidden: denied"), "forbidden"},
		{errors.New("invalidProperties: unrecognised patch property"), "invalidProperties"},
		{errors.New("rateLimit: too many requests"), "rateLimit"},
		{errors.New("serverPartialFail: partial changes"), "serverPartialFail"},
		{errors.New("wrap: unknown prefix"), "serverFail"},
		{errors.New("no typed prefix"), "serverFail"},
	} {
		if got := jmapErrorType(tc.err); got != tc.want {
			t.Errorf("jmapErrorType(%q) = %q, want %q", tc.err, got, tc.want)
		}
	}
}

// TestJMAPFormerStubReturnsRealResponse guards the Phase 4 hard rule: a method
// that used to be a NotImplemented stub must now produce its own JMAP response
// shape, never a generic serverFail.
func TestJMAPFormerStubReturnsRealResponse(t *testing.T) {
	handler := NewHandler(storage.NewMemoryStorage(), metadata.NewMemoryMetadata(),
		usermanager.NewNone(usermanager.Config{AdminUsername: "admin", AdminPassword: "password"}),
		sender.NewLogSender())
	dispatcher := NewDispatcher(handler, nil)
	req := &Request{
		Using: allUsing,
		MethodCalls: []Invocation{{
			Name: "Email/copy", ClientID: "c1", Arguments: map[string]json.RawMessage{
				"fromAccountId": json.RawMessage(`"alice@example.com"`),
				"accountId":     json.RawMessage(`"alice@example.com"`),
			},
		}},
	}
	resp, err := dispatcher.DispatchRequest(context.Background(), "alice@example.com",
		&auth.Claims{Subject: "alice@example.com"}, req)
	if err != nil {
		t.Fatalf("DispatchRequest() error = %v", err)
	}
	if len(resp.MethodResponses) != 1 || resp.MethodResponses[0].Name != "Email/copy" {
		t.Fatalf("responses = %+v, want one Email/copy response", resp.MethodResponses)
	}
	if _, ok := resp.MethodResponses[0].Arguments["created"]; !ok {
		t.Errorf("response = %+v, want a real CopyResponse", resp.MethodResponses[0].Arguments)
	}
}

func TestPushSubscriptionMethodsReceiveFullArguments(t *testing.T) {
	handler := &pushMethodHandler{}
	dispatcher := NewDispatcher(handler, nil)
	req := &Request{
		Using: allUsing,
		MethodCalls: []Invocation{
			{
				Name:     "PushSubscription/get",
				ClientID: "c1",
				Arguments: map[string]json.RawMessage{
					"id": json.RawMessage(`"sub-1"`),
				},
			},
			{
				Name:     "PushSubscription/set",
				ClientID: "c2",
				Arguments: map[string]json.RawMessage{
					"create": json.RawMessage(`{"create1":{"url":"https://example.com/callback"}}`),
				},
			},
			// RFC 8620 defines no PushSubscription/changes method.
			{Name: "PushSubscription/changes", ClientID: "c3", Arguments: map[string]json.RawMessage{}},
		},
	}

	resp, err := dispatcher.DispatchRequest(context.Background(), "alice@example.com",
		&auth.Claims{Subject: "alice@example.com"}, req)
	if err != nil {
		t.Fatalf("DispatchRequest() error = %v", err)
	}
	if len(resp.MethodResponses) != 3 {
		t.Fatalf("method responses = %d, want 3", len(resp.MethodResponses))
	}
	if _, ok := handler.getArgs["id"]; !ok {
		t.Errorf("PushSubscription/get args = %v, want the full arguments object", handler.getArgs)
	}
	if _, ok := handler.setArgs["create"]; !ok {
		t.Errorf("PushSubscription/set args = %v, want the full arguments object", handler.setArgs)
	}
	if resp.MethodResponses[2].Name != "error" {
		t.Fatalf("PushSubscription/changes response = %q, want error", resp.MethodResponses[2].Name)
	}
	var errorType string
	if err := json.Unmarshal(resp.MethodResponses[2].Arguments["type"], &errorType); err != nil {
		t.Fatalf("unmarshal error type: %v", err)
	}
	if errorType != "unknownMethod" {
		t.Errorf("error type = %q, want unknownMethod", errorType)
	}
}

func TestAllMethodsHaveDeclaredCapabilities(t *testing.T) {
	advertised := make(map[string]bool, len(AllMethods()))
	for _, method := range AllMethods() {
		advertised[method] = true
		capability, ok := MethodCapability(method)
		if !ok {
			t.Errorf("method %q has no declared capability", method)
			continue
		}
		if !IsSupportedCapability(capability) {
			t.Errorf("method %q requires unadvertised capability %q", method, capability)
		}
	}
	for method := range methodCapabilities {
		if !advertised[method] {
			t.Errorf("capability map contains unimplemented method %q", method)
		}
	}
}

// TestDispatchRequestCapabilityNotInUsing covers RFC 8620 section 3.3: a
// method must not run unless the client opted in to the capability that
// declares it.
func TestDispatchRequestCapabilityNotInUsing(t *testing.T) {
	handler := &stubMethodHandler{}
	dispatcher := NewDispatcher(handler, nil)
	claims := &auth.Claims{Subject: "alice@example.com"}

	for _, tc := range []struct {
		name    string
		using   []string
		method  string
		wantErr string
	}{
		{"mail method without mail capability", []string{CapabilityCore}, MethodEmailSet, "unknownMethod"},
		{"submission method without submission capability", []string{CapabilityMail}, MethodEmailSubmissionSet, "unknownMethod"},
		{"vacation method without vacation capability", []string{CapabilityCore}, MethodVacationResponseGet, "unknownMethod"},
		{"core method without core capability", []string{CapabilityMail}, MethodCoreEcho, "unknownMethod"},
		{"method with its capability is dispatched", allUsing, MethodEmailSet, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := &Request{
				Using: tc.using,
				MethodCalls: []Invocation{{
					Name: tc.method, ClientID: "c1",
					Arguments: map[string]json.RawMessage{},
				}},
			}
			resp, err := dispatcher.DispatchRequest(context.Background(), "alice@example.com", claims, req)
			if err != nil {
				t.Fatalf("DispatchRequest() error = %v", err)
			}
			if len(resp.MethodResponses) != 1 {
				t.Fatalf("method responses = %d, want 1", len(resp.MethodResponses))
			}
			if tc.wantErr == "" {
				if resp.MethodResponses[0].Name != tc.method {
					t.Fatalf("response = %q, want %q", resp.MethodResponses[0].Name, tc.method)
				}
				return
			}
			if got := errorType(t, resp.MethodResponses[0]); got != tc.wantErr {
				t.Errorf("error type = %q, want %q", got, tc.wantErr)
			}
		})
	}
}

// accountLookupManager answers only for the accounts listed in found.
type accountLookupManager struct {
	usermanager.UserManager
	found map[string]bool
}

func (m *accountLookupManager) Lookup(_ context.Context, username string) (*usermanager.User, error) {
	if m.found[username] {
		return &usermanager.User{}, nil
	}
	return nil, errors.New("no such user")
}

func TestDispatchRequestAccountValidation(t *testing.T) {
	handler := &stubMethodHandler{}
	manager := &accountLookupManager{found: map[string]bool{"bob@example.com": true}}
	dispatcher := NewDispatcher(handler, nil, manager)
	claims := &auth.Claims{Subject: "admin@example.com", EmissaryUserManagement: true}

	for _, tc := range []struct {
		name      string
		accountID string
		wantErr   string
	}{
		{"existing masquerade target", `"bob@example.com"`, ""},
		{"missing masquerade target", `"missing@example.com"`, "accountNotFound"},
		{"malformed account id", `123`, "invalidArguments"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := &Request{
				Using: allUsing,
				MethodCalls: []Invocation{{
					Name: MethodEmailSet, ClientID: "c1",
					Arguments: map[string]json.RawMessage{"accountId": json.RawMessage(tc.accountID)},
				}},
			}
			resp, err := dispatcher.DispatchRequest(context.Background(), "admin@example.com", claims, req)
			if err != nil {
				t.Fatalf("DispatchRequest() error = %v", err)
			}
			if tc.wantErr == "" {
				if resp.MethodResponses[0].Name != MethodEmailSet {
					t.Fatalf("response = %q, want %q", resp.MethodResponses[0].Name, MethodEmailSet)
				}
				return
			}
			if got := errorType(t, resp.MethodResponses[0]); got != tc.wantErr {
				t.Errorf("error type = %q, want %q", got, tc.wantErr)
			}
		})
	}
}

// noCapabilityHandler reports that no account supports any capability, so the
// dispatcher's accountNotSupportedByMethod branch can be exercised.
type noCapabilityHandler struct {
	MethodHandler
}

func (h *noCapabilityHandler) EmailSet(context.Context, string, json.RawMessage) (json.RawMessage, error) {
	return json.RawMessage(`{"created":{},"notCreated":{}}`), nil
}

func (h *noCapabilityHandler) AccountSupportsCapability(context.Context, string, string) bool {
	return false
}

func TestDispatchRequestAccountNotSupportedByMethod(t *testing.T) {
	dispatcher := NewDispatcher(&noCapabilityHandler{}, nil)
	req := &Request{
		Using: allUsing,
		MethodCalls: []Invocation{{
			Name: MethodEmailSet, ClientID: "c1", Arguments: map[string]json.RawMessage{},
		}},
	}
	resp, err := dispatcher.DispatchRequest(context.Background(), "alice@example.com",
		&auth.Claims{Subject: "alice@example.com"}, req)
	if err != nil {
		t.Fatalf("DispatchRequest() error = %v", err)
	}
	if got := errorType(t, resp.MethodResponses[0]); got != "accountNotSupportedByMethod" {
		t.Errorf("error type = %q, want accountNotSupportedByMethod", got)
	}
}
