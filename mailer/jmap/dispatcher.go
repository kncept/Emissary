package jmap

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/kncept/Emissary/mailer/auth"
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

// claimsContextKey is the context key used to pass the authenticated caller's
// claims from the dispatcher to individual method handlers.
type claimsContextKey struct{}

// ContextWithClaims attaches the authenticated caller's claims to ctx.
func ContextWithClaims(ctx context.Context, claims *auth.Claims) context.Context {
	return context.WithValue(ctx, claimsContextKey{}, claims)
}

// ClaimsFromContext returns the authenticated caller's claims, if present.
func ClaimsFromContext(ctx context.Context) *auth.Claims {
	claims, _ := ctx.Value(claimsContextKey{}).(*auth.Claims)
	return claims
}

// Dispatcher handles routing JMAP method calls to the appropriate handler.
type Dispatcher struct {
	handler MethodHandler
}

// NewDispatcher creates a new method dispatcher.
func NewDispatcher(handler MethodHandler) *Dispatcher {
	return &Dispatcher{handler: handler}
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
}

// DispatchRequest processes a batch of method calls and returns the responses.
func (d *Dispatcher) DispatchRequest(ctx context.Context, defaultAccountID string, claims *auth.Claims, req *Request) (*Response, error) {
	// Method handlers need the caller's identity and privileges to authorise
	// operations such as EmailSubmission/set, where the From address must
	// belong to the caller (or the caller must be allowed to masquerade).
	ctx = ContextWithClaims(ctx, claims)

	responses := make([]Invocation, 0, len(req.MethodCalls))

	for _, call := range req.MethodCalls {
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
			resp := d.errorResponse(call, fmt.Errorf("forbidden: insufficient permissions to access account %s", accountID))
			responses = append(responses, resp)
			continue
		}

		resp, err := d.dispatchCall(ctx, accountID, call)
		if err != nil {
			// Return an error response for this method call
			resp = d.errorResponse(call, err)
		}
		responses = append(responses, resp)
	}

	return &Response{
		MethodResponses: responses,
		CreatedIDs:      req.CreatedIDs,
		SessionState:    "", // Will be set by caller
	}, nil
}

func (d *Dispatcher) dispatchCall(ctx context.Context, accountID string, call Invocation) (Invocation, error) {
	methodName := call.Name
	args := call.Arguments
	clientID := call.ClientID

	// Marshal the entire args map to JSON for the handler methods
	argsJSON, err := json.Marshal(args)
	if err != nil {
		return d.errorResponseFromError(methodName, clientID, err), nil
	}

	var result json.RawMessage

	switch methodName {
	// Core methods
	case "Core/echo":
		result, err = d.handler.CoreEcho(ctx, accountID, argsJSON)

	// Mailbox methods
	case "Mailbox/get":
		result, err = d.handler.MailboxGet(ctx, accountID, argsJSON)
	case "Mailbox/set":
		result, err = d.handler.MailboxSet(ctx, accountID, argsJSON)
	case "Mailbox/changes":
		result, err = d.handler.MailboxChanges(ctx, accountID, argsJSON)
	case "Mailbox/query":
		result, err = d.handler.MailboxQuery(ctx, accountID, argsJSON)
	case "Mailbox/queryChanges":
		result, err = d.handler.MailboxQueryChanges(ctx, accountID, argsJSON)

	// Thread methods
	case "Thread/get":
		result, err = d.handler.ThreadGet(ctx, accountID, argsJSON)
	case "Thread/changes":
		result, err = d.handler.ThreadChanges(ctx, accountID, argsJSON)

	// Email methods
	case "Email/get":
		result, err = d.handler.EmailGet(ctx, accountID, argsJSON)
	case "Email/set":
		result, err = d.handler.EmailSet(ctx, accountID, argsJSON)
	case "Email/changes":
		result, err = d.handler.EmailChanges(ctx, accountID, argsJSON)
	case "Email/query":
		result, err = d.handler.EmailQuery(ctx, accountID, argsJSON)
	case "Email/queryChanges":
		result, err = d.handler.EmailQueryChanges(ctx, accountID, argsJSON)
	case "Email/copy":
		result, err = d.handler.EmailCopy(ctx, accountID, argsJSON)
	case "Email/import":
		result, err = d.handler.EmailImport(ctx, accountID, argsJSON)
	case "Email/parse":
		result, err = d.handler.EmailParse(ctx, accountID, argsJSON)

	// SearchSnippet methods
	case "SearchSnippet/get":
		result, err = d.handler.SearchSnippetGet(ctx, accountID, argsJSON)

	// Identity methods
	case "Identity/get":
		result, err = d.handler.IdentityGet(ctx, accountID, argsJSON)
	case "Identity/set":
		result, err = d.handler.IdentitySet(ctx, accountID, argsJSON)
	case "Identity/changes":
		result, err = d.handler.IdentityChanges(ctx, accountID, argsJSON)

	// EmailSubmission methods
	case "EmailSubmission/get":
		result, err = d.handler.EmailSubmissionGet(ctx, accountID, argsJSON)
	case "EmailSubmission/set":
		result, err = d.handler.EmailSubmissionSet(ctx, accountID, argsJSON)
	case "EmailSubmission/changes":
		result, err = d.handler.EmailSubmissionChanges(ctx, accountID, argsJSON)
	case "EmailSubmission/query":
		result, err = d.handler.EmailSubmissionQuery(ctx, accountID, argsJSON)
	case "EmailSubmission/queryChanges":
		result, err = d.handler.EmailSubmissionQueryChanges(ctx, accountID, argsJSON)

	// VacationResponse methods
	case "VacationResponse/get":
		result, err = d.handler.VacationResponseGet(ctx, accountID, argsJSON)
	case "VacationResponse/set":
		result, err = d.handler.VacationResponseSet(ctx, accountID, argsJSON)
	case "VacationResponse/changes":
		result, err = d.handler.VacationResponseChanges(ctx, accountID, argsJSON)

	// PushSubscription methods
	case "PushSubscription/get":
		result, err = d.handler.PushSubscriptionGet(ctx, accountID, args[""])
	case "PushSubscription/set":
		result, err = d.handler.PushSubscriptionSet(ctx, accountID, args[""])
	case "PushSubscription/changes":
		result, err = d.handler.PushSubscriptionChanges(ctx, accountID, args[""])

	default:
		err = fmt.Errorf("unknown method: %s", methodName)
	}

	if err != nil {
		return d.errorResponseFromError(methodName, clientID, err), nil
	}

	// Construct the response invocation
	// The response is [methodName, arguments, clientId], where arguments is
	// the result object returned by the method handler.
	var resultArgs map[string]json.RawMessage
	if err := json.Unmarshal(result, &resultArgs); err != nil {
		return d.errorResponseFromError(methodName, clientID, err), nil
	}
	return Invocation{
		Name:      methodName,
		Arguments: resultArgs,
		ClientID:  clientID,
	}, nil
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
