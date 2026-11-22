package jmap

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/kncept/Emissary/mailer/auth"
	"github.com/kncept/Emissary/mailer/components/storage"
	"github.com/kncept/Emissary/mailer/components/usermanager"
)

// createdIDSaveRetries bounds optimistic-concurrency retries when persisting
// the per-session creation-id map. A conflict means another Lambda invocation
// changed the same session, so the map is reloaded and merged before retrying.
const createdIDSaveRetries = 8

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
	userMgr      usermanager.UserManager
}

// accountCapabilityChecker is implemented by handlers that can report which
// capabilities an account supports. It lets the dispatcher return
// accountNotSupportedByMethod instead of dispatching a method the account
// cannot serve.
type accountCapabilityChecker interface {
	AccountSupportsCapability(ctx context.Context, accountID, capability string) bool
}

// NewDispatcher creates a new method dispatcher. sessionState persists the
// creation-id map per session so references survive across Lambda invocations;
// it may be nil when no durable session state is configured. userMgr is used
// only to validate masqueraded account ids and is optional.
func NewDispatcher(handler MethodHandler, sessionState storage.SessionStateStore, userMgr ...usermanager.UserManager) *Dispatcher {
	d := &Dispatcher{handler: handler, sessionState: sessionState}
	if len(userMgr) > 0 {
		d.userMgr = userMgr[0]
	}
	return d
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
	ctx = auth.ContextWithClaims(ctx, claims)
	slog.Debug("JMAP request", "calls", len(req.MethodCalls), "account", defaultAccountID)

	// RFC 8620 section 3.3: a method may only be used when its capability was
	// opted into by the Request's `using` array. Treat a missing capability as
	// an unknown method rather than executing code the client did not opt into.
	usingSet := make(map[string]bool, len(req.Using))
	for _, capability := range req.Using {
		usingSet[capability] = true
	}

	// Seed the creation-id map from durable per-session state and from the
	// request. The session copy lets a client continue after a Lambda cold
	// start; the request copy follows RFC 8620's createdIds argument.
	sessionID := ""
	if claims != nil {
		sessionID = claims.SID
	}
	createdIDs := make(map[string]string, len(req.CreatedIDs))
	revision := ""
	if sessionID != "" && d.sessionState != nil {
		stored, storedRevision, err := d.sessionState.LoadCreatedIDs(ctx, sessionID)
		if err != nil {
			slog.Error("JMAP: failed to load session created ids", "err", err)
			return nil, fmt.Errorf("load created ids: %w", err)
		}
		revision = storedRevision
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

		requiredCapability, knownMethod := MethodCapability(call.Name)
		if knownMethod && !usingSet[requiredCapability] {
			resp := d.errorResponse(call, fmt.Errorf("unknownMethod: %s", call.Name))
			appendMethodResponse(&responses, responsesByID, createdIDs, resp)
			continue
		}

		// Extract the public, opaque accountId from method arguments, falling
		// back to the authenticated default. Convert it to the internal
		// storage account only after authorisation; method handlers continue
		// to see the email-shaped storage key.
		publicAccountID := defaultAccountID
		if call.Arguments != nil {
			if accountIDRaw, ok := call.Arguments["accountId"]; ok {
				var extracted string
				if err := json.Unmarshal(accountIDRaw, &extracted); err != nil || extracted == "" {
					resp := d.errorResponse(call, fmt.Errorf("invalidArguments: accountId must be a non-empty string"))
					appendMethodResponse(&responses, responsesByID, createdIDs, resp)
					continue
				}
				publicAccountID = extracted
			}
		}
		accountID, err := auth.ResolveAccountID(ctx, claims, publicAccountID, d.userMgr)
		if err != nil {
			slog.Warn("JMAP: account resolution failed", "method", call.Name, "account", publicAccountID, "err", err)
			resp := d.errorResponse(call, fmt.Errorf("%s", jmapAccountError(err)))
			appendMethodResponse(&responses, responsesByID, createdIDs, resp)
			continue
		}
		setStringArgument(call.Arguments, "accountId", accountID)

		// Blob/copy's fromAccountId is method-specific, so the dispatcher must
		// authorise and translate it as well.
		if call.Arguments != nil {
			if raw, ok := call.Arguments["fromAccountId"]; ok {
				var fromAccountID string
				if err := json.Unmarshal(raw, &fromAccountID); err != nil || fromAccountID == "" {
					resp := d.errorResponse(call, fmt.Errorf("invalidArguments: fromAccountId must be a non-empty string"))
					appendMethodResponse(&responses, responsesByID, createdIDs, resp)
					continue
				}
				internalFrom, err := auth.ResolveAccountID(ctx, claims, fromAccountID, d.userMgr)
				if err != nil {
					resp := d.errorResponse(call, fmt.Errorf("%s", jmapAccountError(err)))
					appendMethodResponse(&responses, responsesByID, createdIDs, resp)
					continue
				}
				setStringArgument(call.Arguments, "fromAccountId", internalFrom)
			}
		}
		if knownMethod {
			if checker, ok := d.handler.(accountCapabilityChecker); ok && !checker.AccountSupportsCapability(ctx, accountID, requiredCapability) {
				resp := d.errorResponse(call, fmt.Errorf("accountNotSupportedByMethod: account %s does not support %s", accountID, call.Name))
				appendMethodResponse(&responses, responsesByID, createdIDs, resp)
				continue
			}
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

	if err := d.persistCreatedIDs(ctx, sessionID, createdIDs, initial, revision); err != nil {
		slog.Error("JMAP: failed to save session created ids", "err", err)
		return nil, fmt.Errorf("save created ids: %w", err)
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
	externalizeAccountIDs(response)
	return response, nil
}

// persistCreatedIDs writes the creation-id map changed by this batch using the
// revision read at its start. On a conflict another invocation has written a
// newer map, so it is reloaded and merged before retrying. Ids produced by this
// batch win for keys they define; all other ids from the newer map are kept.
// Read-only batches do not write at all.
func (d *Dispatcher) persistCreatedIDs(ctx context.Context, sessionID string, ids, initial map[string]string, revision string) error {
	if sessionID == "" || d.sessionState == nil || sameStringMap(ids, initial) {
		return nil
	}
	var lastErr error
	for attempt := 0; attempt < createdIDSaveRetries; attempt++ {
		if attempt > 0 {
			stored, currentRevision, err := d.sessionState.LoadCreatedIDs(ctx, sessionID)
			if err != nil {
				return fmt.Errorf("reload created ids: %w", err)
			}
			revision = currentRevision
			merged := make(map[string]string, len(stored)+len(ids))
			for creationID, id := range stored {
				merged[creationID] = id
			}
			for creationID, id := range ids {
				merged[creationID] = id
			}
			if sameStringMap(merged, stored) {
				return nil // The concurrent invocation already stored our ids.
			}
			ids = merged
		}
		_, err := d.sessionState.SaveCreatedIDs(ctx, sessionID, ids, revision)
		if err == nil {
			return nil
		}
		if !errors.Is(err, storage.ErrSessionStateConflict) {
			return err
		}
		lastErr = err
	}
	return fmt.Errorf("persist created ids for session %q after %d retries: %w", sessionID, createdIDSaveRetries, lastErr)
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
		err = fmt.Errorf("unknownMethod: %s", methodName)
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

// jmapAccountError converts a resolved-account failure into the RFC 8620
// method error type clients expect.
func jmapAccountError(err error) string {
	switch {
	case errors.Is(err, auth.ErrAccountForbidden), errors.Is(err, auth.ErrAccountAuthenticationRequired):
		return "forbidden: " + err.Error()
	case errors.Is(err, auth.ErrAccountNotFound):
		return "accountNotFound: " + err.Error()
	default:
		return "serverFail: " + err.Error()
	}
}

// setStringArgument replaces a JSON string argument in place. It is used to
// swap public opaque account ids for the internal storage account before a
// method handler runs.
func setStringArgument(args map[string]json.RawMessage, key, value string) {
	if args == nil {
		return
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return
	}
	args[key] = encoded
}

// externalizeAccountIDs rewrites internal (email-shaped) account ids in method
// responses back to the opaque identifiers advertised by the Session.
func externalizeAccountIDs(resp *Response) {
	if resp == nil {
		return
	}
	for i := range resp.MethodResponses {
		args := resp.MethodResponses[i].Arguments
		for _, key := range []string{"accountId", "fromAccountId"} {
			raw, ok := args[key]
			if !ok {
				continue
			}
			var value string
			if err := json.Unmarshal(raw, &value); err != nil || !strings.Contains(value, "@") {
				continue
			}
			setStringArgument(args, key, auth.AccountIDForEmail(value))
		}
	}
}

// jmapErrorTypes is the set of method-level error types the dispatcher emits.
// RFC 8620 section 3.6.2 requires clients to treat unknown error types as
// serverFail; we use the same fallback here so an untyped internal error is
// never mislabelled as unknownMethod.
var jmapErrorTypes = map[string]bool{
	"accountNotFound":             true,
	"accountNotSupportedByMethod": true,
	"accountReadOnly":             true,
	"anchorNotFound":              true,
	"cannotCalculateChanges":      true,
	"forbidden":                   true,
	"fromAccountNotFound":         true,
	"invalidArguments":            true,
	"invalidProperties":           true,
	"invalidResultReference":      true,
	"notCreated":                  true,
	"notFound":                    true,
	"rateLimit":                   true,
	"requestTooLarge":             true,
	"serverFail":                  true,
	"serverPartialFail":           true,
	"serverUnavailable":           true,
	"stateMismatch":               true,
	"tooLarge":                    true,
	"unsupportedFilter":           true,
	"unsupportedSort":             true,
	"unknownMethod":               true,
}

func jmapErrorType(err error) string {
	prefix, _, found := strings.Cut(err.Error(), ": ")
	if found && jmapErrorTypes[prefix] {
		return prefix
	}
	return "serverFail"
}

func (d *Dispatcher) errorResponseFromError(methodName, clientID string, err error) Invocation {
	// JMAP error response format: ["error", {type: "...", description: "..."}, clientId]
	description, _ := json.Marshal(err.Error())

	// Method errors carry their JMAP type as the prefix before ": ". Keeping
	// the mapping table here means every subpackage can use base.Errorf
	// without adding another special case to the dispatcher.
	errorType := jmapErrorType(err)

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
