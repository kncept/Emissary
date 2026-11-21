package jmap

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/kncept/Emissary/mailer/auth"
	"github.com/kncept/Emissary/mailer/components/storage"
)

// UnmarshalJSON implements custom JSON unmarshaling for Invocation.
// JMAP uses a 3-element array format: [methodName, arguments, clientId]
func (inv *Invocation) UnmarshalJSON(data []byte) error {
	var raw []json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if len(raw) != 3 {
		return fmt.Errorf("expected 3-element array for Invocation, got %d elements", len(raw))
	}

	// Unmarshal method name
	if err := json.Unmarshal(raw[0], &inv.Name); err != nil {
		return err
	}

	// Unmarshal arguments as map
	if err := json.Unmarshal(raw[1], &inv.Arguments); err != nil {
		return err
	}

	// Unmarshal client ID
	if err := json.Unmarshal(raw[2], &inv.ClientID); err != nil {
		return err
	}

	return nil
}

// MarshalJSON implements custom JSON marshaling for Invocation.
func (inv Invocation) MarshalJSON() ([]byte, error) {
	return json.Marshal([]interface{}{inv.Name, inv.Arguments, inv.ClientID})
}

// Dispatcher handles routing JMAP method calls to the appropriate handler.
type Dispatcher struct {
	handler      MethodHandler
	sessionState storage.SessionStateStore
}

// NewDispatcher creates a new method dispatcher. sessionState persists the
// creation-id map per session so references survive across Lambda invocations;
// it may be nil when no durable session state is configured.
func NewDispatcher(handler MethodHandler, sessionState storage.SessionStateStore) *Dispatcher {
	return &Dispatcher{handler: handler, sessionState: sessionState}
}

// MethodCall represents a single JMAP method call from the batch request.
type MethodCall struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
	ClientID  string          `json:"clientId"`
}

// MethodResponse represents a single JMAP method response in the batch response.
type MethodResponse struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
	ClientID  string          `json:"clientId"`
}

// Invocation represents a JMAP method call/response tuple: [methodName, arguments, clientId]
type Invocation struct {
	Name      string
	Arguments map[string]json.RawMessage
	ClientID  string
}

// Request represents a JMAP API request (batch of method calls).
type Request struct {
	Using       []string          `json:"using"`
	MethodCalls []Invocation      `json:"methodCalls"`
	CreatedIDs  map[string]string `json:"createdIds,omitempty"`
}

// Response represents a JMAP API response (batch of method responses).
type Response struct {
	MethodResponses []Invocation      `json:"methodResponses"`
	CreatedIDs      map[string]string `json:"createdIds,omitempty"`
	SessionState    string            `json:"sessionState"`

	// includeCreatedIDs records whether the request supplied createdIds. An
	// empty map must still be emitted as {} (RFC 8620 section 3.4), which
	// omitempty alone would drop.
	includeCreatedIDs bool
}

// MarshalJSON emits createdIds when the client opted in, even if the map is
// empty. Responses built directly with a non-empty map keep the old behavior.
func (r Response) MarshalJSON() ([]byte, error) {
	fields := make(map[string]json.RawMessage, 3)
	methodResponses, err := json.Marshal(r.MethodResponses)
	if err != nil {
		return nil, err
	}
	fields["methodResponses"] = methodResponses
	sessionState, err := json.Marshal(r.SessionState)
	if err != nil {
		return nil, err
	}
	fields["sessionState"] = sessionState
	if r.includeCreatedIDs || len(r.CreatedIDs) > 0 {
		createdIDs := r.CreatedIDs
		if createdIDs == nil {
			createdIDs = map[string]string{}
		}
		encoded, err := json.Marshal(createdIDs)
		if err != nil {
			return nil, err
		}
		fields["createdIds"] = encoded
	}
	return json.Marshal(fields)
}

// resolveCreationIDs recursively replaces creation references ("#creationId")
// in a decoded JSON value with the server-assigned id from createdIDs. Both
// string values and object keys are handled; the latter is required by
// arguments such as EmailSubmission/set's onSuccessUpdateEmail, which is
// keyed by Email id. Unresolved references are left unchanged so the method
// handler reports the appropriate notFound error.
func resolveCreationIDs(value interface{}, createdIDs map[string]string) interface{} {
	switch v := value.(type) {
	case string:
		if creationID, ok := creationReference(v); ok {
			if id, found := createdIDs[creationID]; found {
				return id
			}
		}
		return v
	case []interface{}:
		for i, item := range v {
			v[i] = resolveCreationIDs(item, createdIDs)
		}
		return v
	case map[string]interface{}:
		resolved := make(map[string]interface{}, len(v))
		for key, item := range v {
			if creationID, ok := creationReference(key); ok {
				if id, found := createdIDs[creationID]; found {
					key = id
				}
			}
			resolved[key] = resolveCreationIDs(item, createdIDs)
		}
		return resolved
	default:
		return value
	}
}

// creationReference reports whether s is a JMAP creation reference and, if so,
// returns the creation id without the leading '#'.
func creationReference(s string) (string, bool) {
	if len(s) > 1 && s[0] == '#' {
		return s[1:], true
	}
	return "", false
}

// resolveCallCreationIDs applies creation-id resolution to a method call's
// arguments. Using UseNumber preserves JSON number representations exactly.
func resolveCallCreationIDs(args map[string]json.RawMessage, createdIDs map[string]string) (map[string]json.RawMessage, error) {
	if len(createdIDs) == 0 {
		return args, nil
	}

	raw, err := json.Marshal(args)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var generic interface{}
	if err := decoder.Decode(&generic); err != nil {
		return nil, err
	}

	resolved, err := json.Marshal(resolveCreationIDs(generic, createdIDs))
	if err != nil {
		return nil, err
	}
	var result map[string]json.RawMessage
	if err := json.Unmarshal(resolved, &result); err != nil {
		return nil, err
	}
	return result, nil
}

// recordCreatedIDs records the server-assigned ids returned in a method
// response's "created" map so later calls in the same batch can reference
// them. This covers all /set methods (including EmailSubmission/set) as well
// as /copy and /import.
func recordCreatedIDs(response Invocation, createdIDs map[string]string) {
	if response.Name == "error" {
		return
	}
	createdRaw, ok := response.Arguments["created"]
	if !ok {
		return
	}
	var created map[string]struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(createdRaw, &created); err != nil {
		return
	}
	for creationID, object := range created {
		if object.ID != "" {
			createdIDs[creationID] = object.ID
		}
	}
}

func appendMethodResponse(responses *[]Invocation, byID map[string]Invocation, createdIDs map[string]string, resp Invocation) {
	appendMethodResponses(responses, byID, createdIDs, []Invocation{resp})
}

// appendMethodResponses records the responses of a processed call in the
// batch. The first response for a method call id is the one a later result
// reference resolves against (RFC 8620 section 3.7); implicit responses share
// the id but must not replace it.
func appendMethodResponses(responses *[]Invocation, byID map[string]Invocation, createdIDs map[string]string, resps []Invocation) {
	for _, resp := range resps {
		if _, seen := byID[resp.ClientID]; !seen {
			byID[resp.ClientID] = resp
		}
		recordCreatedIDs(resp, createdIDs)
		*responses = append(*responses, resp)
	}
}

// DispatchRequest processes a batch of method calls and returns the responses.
func (d *Dispatcher) DispatchRequest(ctx context.Context, defaultAccountID string, claims *auth.Claims, req *Request) (*Response, error) {
	// Method handlers need the caller's identity and privileges to authorise
	// operations such as EmailSubmission/set, where the From address must
	// belong to the caller (or the caller must be allowed to masquerade).
	ctx = ContextWithClaims(ctx, claims)
	slog.Debug("JMAP request", "calls", len(req.MethodCalls), "account", defaultAccountID)

	// Seed the creation-id map from durable per-session state and from the
	// request. The session copy lets a client continue after a Lambda cold
	// start; the request copy follows RFC 8620's createdIds argument.
	sessionID := ""
	if claims != nil {
		sessionID = claims.SID
	}
	createdIDs := make(map[string]string, len(req.CreatedIDs))
	if sessionID != "" && d.sessionState != nil {
		stored, err := d.sessionState.LoadCreatedIDs(ctx, sessionID)
		if err != nil {
			slog.Error("JMAP: failed to load session created ids", "err", err)
			return nil, fmt.Errorf("load created ids: %w", err)
		}
		for creationID, id := range stored {
			createdIDs[creationID] = id
		}
	}
	for creationID, id := range req.CreatedIDs {
		createdIDs[creationID] = id
	}
	initial := make(map[string]string, len(createdIDs))
	for creationID, id := range createdIDs {
		initial[creationID] = id
	}

	responses := make([]Invocation, 0, len(req.MethodCalls))
	// RFC 8620 result references may only point at responses from earlier
	// method calls. Keep the first response for each method call id; a method
	// may emit more than one response, all sharing the same id.
	responsesByID := make(map[string]Invocation)

	for _, call := range req.MethodCalls {
		// Resolve RFC 8620 result references ("#ids") before dispatch. This
		// must happen before creation-id resolution so that a "#create1" value
		// inside a referenced result is still resolved to the real id.
		resolvedArgs, err := resolveResultReferences(call.Arguments, responsesByID)
		if err != nil {
			slog.Warn("JMAP: invalid result reference", "method", call.Name, "err", err)
			appendMethodResponse(&responses, responsesByID, createdIDs, d.errorResponse(call, err))
			continue
		}
		call.Arguments = resolvedArgs

		// Resolve JMAP creation references (e.g. "#create1") in the call's
		// arguments using the ids created by earlier calls in this batch (or
		// supplied by the client in createdIds).
		resolvedArgs, err = resolveCallCreationIDs(call.Arguments, createdIDs)
		if err != nil {
			slog.Warn("JMAP: invalid creation id reference", "method", call.Name, "err", err)
			appendMethodResponse(&responses, responsesByID, createdIDs, d.errorResponse(call, fmt.Errorf("invalidArguments: %w", err)))
			continue
		}
		call.Arguments = resolvedArgs

		// Extract accountId from method arguments, fallback to default
		accountID := defaultAccountID
		if call.Arguments != nil {
			if accountIDRaw, ok := call.Arguments["accountId"]; ok {
				var extracted string
				if err := json.Unmarshal(accountIDRaw, &extracted); err == nil && extracted != "" {
					accountID = extracted
				}
			}
		}

		// Validate accountId: users without User Manager permission can only
		// access their own account.
		if claims != nil && !claims.EmissaryUserManagement && accountID != claims.Subject {
			slog.Warn("JMAP: forbidden account access", "method", call.Name, "account", accountID)
			resp := d.errorResponse(call, fmt.Errorf("forbidden: insufficient permissions to access account %s", accountID))
			appendMethodResponse(&responses, responsesByID, createdIDs, resp)
			continue
		}

		slog.Debug("JMAP method call", "method", call.Name, "client_id", call.ClientID, "account", accountID)
		resps, err := d.dispatchCall(ctx, accountID, call)
		if err != nil {
			slog.Error("JMAP method failed", "method", call.Name, "account", accountID, "err", err)
			// Return an error response for this method call
			resps = []Invocation{d.errorResponse(call, err)}
		}
		appendMethodResponses(&responses, responsesByID, createdIDs, resps)
	}

	// Persist only when the map actually changed, so read-only JMAP requests
	// do not write to the session store.
	if sessionID != "" && d.sessionState != nil && !sameStringMap(createdIDs, initial) {
		if err := d.sessionState.SaveCreatedIDs(ctx, sessionID, createdIDs); err != nil {
			slog.Error("JMAP: failed to save session created ids", "err", err)
			return nil, fmt.Errorf("save created ids: %w", err)
		}
	}

	response := &Response{
		MethodResponses: responses,
		SessionState:    "", // Will be set by caller
	}
	// RFC 8620 section 3.4: createdIds is only returned when the client sent
	// it. Resolution above still uses the session and newly created ids, so
	// #create1 references work in requests that omit createdIds.
	if req.CreatedIDs != nil {
		response.CreatedIDs = createdIDs
		response.includeCreatedIDs = true
	}
	return response, nil
}

func sameStringMap(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for key, value := range a {
		if b[key] != value {
			return false
		}
	}
	return true
}

func (d *Dispatcher) dispatchCall(ctx context.Context, accountID string, call Invocation) ([]Invocation, error) {
	methodName := call.Name
	args := call.Arguments
	clientID := call.ClientID

	// Marshal the entire args map to JSON for the handler methods
	argsJSON, err := json.Marshal(args)
	if err != nil {
		return []Invocation{d.errorResponseFromError(methodName, clientID, err)}, nil
	}

	var result json.RawMessage
	var extra []Invocation

	switch methodName {
	// Core methods
	case MethodCoreEcho:
		result, err = d.handler.CoreEcho(ctx, accountID, argsJSON)

	// Mailbox methods
	case MethodMailboxGet:
		result, err = d.handler.MailboxGet(ctx, accountID, argsJSON)
	case MethodMailboxSet:
		result, err = d.handler.MailboxSet(ctx, accountID, argsJSON)
	case MethodMailboxChanges:
		result, err = d.handler.MailboxChanges(ctx, accountID, argsJSON)
	case MethodMailboxQuery:
		result, err = d.handler.MailboxQuery(ctx, accountID, argsJSON)
	case MethodMailboxQueryChanges:
		result, err = d.handler.MailboxQueryChanges(ctx, accountID, argsJSON)

	// Thread methods
	case MethodThreadGet:
		result, err = d.handler.ThreadGet(ctx, accountID, argsJSON)
	case MethodThreadChanges:
		result, err = d.handler.ThreadChanges(ctx, accountID, argsJSON)

	// Email methods
	case MethodEmailGet:
		result, err = d.handler.EmailGet(ctx, accountID, argsJSON)
	case MethodEmailSet:
		result, err = d.handler.EmailSet(ctx, accountID, argsJSON)
	case MethodEmailChanges:
		result, err = d.handler.EmailChanges(ctx, accountID, argsJSON)
	case MethodEmailQuery:
		result, err = d.handler.EmailQuery(ctx, accountID, argsJSON)
	case MethodEmailQueryChanges:
		result, err = d.handler.EmailQueryChanges(ctx, accountID, argsJSON)
	case MethodEmailCopy:
		result, err = d.handler.EmailCopy(ctx, accountID, argsJSON)
	case MethodEmailImport:
		result, err = d.handler.EmailImport(ctx, accountID, argsJSON)
	case MethodEmailParse:
		result, err = d.handler.EmailParse(ctx, accountID, argsJSON)

	// SearchSnippet methods
	case MethodSearchSnippetGet:
		result, err = d.handler.SearchSnippetGet(ctx, accountID, argsJSON)

	// Identity methods
	case MethodIdentityGet:
		result, err = d.handler.IdentityGet(ctx, accountID, argsJSON)
	case MethodIdentitySet:
		result, err = d.handler.IdentitySet(ctx, accountID, argsJSON)
	case MethodIdentityChanges:
		result, err = d.handler.IdentityChanges(ctx, accountID, argsJSON)

	// EmailSubmission methods
	case MethodEmailSubmissionGet:
		result, err = d.handler.EmailSubmissionGet(ctx, accountID, argsJSON)
	case MethodEmailSubmissionSet:
		if multi, ok := d.handler.(ImplicitSetHandler); ok {
			var implicit json.RawMessage
			result, implicit, err = multi.EmailSubmissionSetWithImplicit(ctx, accountID, argsJSON)
			if err == nil && len(implicit) > 0 {
				extra = append(extra, d.resultInvocation(MethodEmailSet, clientID, implicit))
			}
		} else {
			result, err = d.handler.EmailSubmissionSet(ctx, accountID, argsJSON)
		}
	case MethodEmailSubmissionChanges:
		result, err = d.handler.EmailSubmissionChanges(ctx, accountID, argsJSON)
	case MethodEmailSubmissionQuery:
		result, err = d.handler.EmailSubmissionQuery(ctx, accountID, argsJSON)
	case MethodEmailSubmissionQueryChanges:
		result, err = d.handler.EmailSubmissionQueryChanges(ctx, accountID, argsJSON)

	// VacationResponse methods
	case MethodVacationResponseGet:
		result, err = d.handler.VacationResponseGet(ctx, accountID, argsJSON)
	case MethodVacationResponseSet:
		result, err = d.handler.VacationResponseSet(ctx, accountID, argsJSON)
	case MethodVacationResponseChanges:
		result, err = d.handler.VacationResponseChanges(ctx, accountID, argsJSON)

	// PushSubscription methods are account-less in RFC 8620 section 7.2,
	// but still receive their full arguments object like every other method.
	case MethodPushSubscriptionGet:
		result, err = d.handler.PushSubscriptionGet(ctx, accountID, argsJSON)
	case MethodPushSubscriptionSet:
		result, err = d.handler.PushSubscriptionSet(ctx, accountID, argsJSON)

	// Blob methods
	case MethodBlobCopy:
		result, err = d.handler.BlobCopy(ctx, accountID, argsJSON)

	default:
		err = fmt.Errorf("unknown method: %s", methodName)
	}

	if err != nil {
		return []Invocation{d.errorResponseFromError(methodName, clientID, err)}, nil
	}
	return append([]Invocation{d.resultInvocation(methodName, clientID, result)}, extra...), nil
}

// resultInvocation constructs the response invocation. The response is
// [methodName, arguments, clientId], where arguments is the result object
// returned by the method handler.
func (d *Dispatcher) resultInvocation(methodName, clientID string, result json.RawMessage) Invocation {
	var resultArgs map[string]json.RawMessage
	if err := json.Unmarshal(result, &resultArgs); err != nil {
		return d.errorResponseFromError(methodName, clientID, err)
	}
	return Invocation{
		Name:      methodName,
		Arguments: resultArgs,
		ClientID:  clientID,
	}
}

func (d *Dispatcher) errorResponse(call Invocation, err error) Invocation {
	return d.errorResponseFromError(call.Name, call.ClientID, err)
}

func (d *Dispatcher) errorResponseFromError(methodName, clientID string, err error) Invocation {
	// JMAP error response format: ["error", {type: "...", description: "..."}, clientId]
	description, _ := json.Marshal(err.Error())

	// Determine error type from error message
	errorType := "unknownMethod"
	errMsg := err.Error()
	if strings.HasPrefix(errMsg, "forbidden:") {
		errorType = "forbidden"
	} else if strings.HasPrefix(errMsg, "notFound:") {
		errorType = "notFound"
	} else if strings.HasPrefix(errMsg, "invalidArguments:") {
		errorType = "invalidArguments"
	} else if strings.HasPrefix(errMsg, "cannotCalculateChanges:") {
		errorType = "cannotCalculateChanges"
	} else if strings.HasPrefix(errMsg, "invalidResultReference:") {
		errorType = "invalidResultReference"
	}

	errorArgs := map[string]json.RawMessage{
		"type":        json.RawMessage(`"` + errorType + `"`),
		"description": json.RawMessage(description),
	}
	return Invocation{
		Name:      "error",
		Arguments: errorArgs,
		ClientID:  clientID,
	}
}
