package jmap

import (
	"context"
	"encoding/json"
	"fmt"
)

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
	Using       []string     `json:"using"`
	MethodCalls []Invocation `json:"methodCalls"`
	CreatedIDs  map[string]string `json:"createdIds,omitempty"`
}

// Response represents a JMAP API response (batch of method responses).
type Response struct {
	MethodResponses []Invocation `json:"methodResponses"`
	CreatedIDs      map[string]string `json:"createdIds,omitempty"`
	SessionState    string            `json:"sessionState"`
}

// DispatchRequest processes a batch of method calls and returns the responses.
func (d *Dispatcher) DispatchRequest(ctx context.Context, accountID string, req *Request) (*Response, error) {
	responses := make([]Invocation, 0, len(req.MethodCalls))

	for _, call := range req.MethodCalls {
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

	var result json.RawMessage
	var err error

	switch methodName {
	// Core methods
	case "Core/echo":
		result, err = d.handler.CoreEcho(ctx, accountID, args[""])

	// Mailbox methods
	case "Mailbox/get":
		result, err = d.handler.MailboxGet(ctx, accountID, args[""])
	case "Mailbox/set":
		result, err = d.handler.MailboxSet(ctx, accountID, args[""])
	case "Mailbox/changes":
		result, err = d.handler.MailboxChanges(ctx, accountID, args[""])
	case "Mailbox/query":
		result, err = d.handler.MailboxQuery(ctx, accountID, args[""])
	case "Mailbox/queryChanges":
		result, err = d.handler.MailboxQueryChanges(ctx, accountID, args[""])

	// Thread methods
	case "Thread/get":
		result, err = d.handler.ThreadGet(ctx, accountID, args[""])
	case "Thread/changes":
		result, err = d.handler.ThreadChanges(ctx, accountID, args[""])

	// Email methods
	case "Email/get":
		result, err = d.handler.EmailGet(ctx, accountID, args[""])
	case "Email/set":
		result, err = d.handler.EmailSet(ctx, accountID, args[""])
	case "Email/changes":
		result, err = d.handler.EmailChanges(ctx, accountID, args[""])
	case "Email/query":
		result, err = d.handler.EmailQuery(ctx, accountID, args[""])
	case "Email/queryChanges":
		result, err = d.handler.EmailQueryChanges(ctx, accountID, args[""])
	case "Email/copy":
		result, err = d.handler.EmailCopy(ctx, accountID, args[""])
	case "Email/import":
		result, err = d.handler.EmailImport(ctx, accountID, args[""])
	case "Email/parse":
		result, err = d.handler.EmailParse(ctx, accountID, args[""])

	// SearchSnippet methods
	case "SearchSnippet/get":
		result, err = d.handler.SearchSnippetGet(ctx, accountID, args[""])

	// Identity methods
	case "Identity/get":
		result, err = d.handler.IdentityGet(ctx, accountID, args[""])
	case "Identity/set":
		result, err = d.handler.IdentitySet(ctx, accountID, args[""])
	case "Identity/changes":
		result, err = d.handler.IdentityChanges(ctx, accountID, args[""])

	// EmailSubmission methods
	case "EmailSubmission/get":
		result, err = d.handler.EmailSubmissionGet(ctx, accountID, args[""])
	case "EmailSubmission/set":
		result, err = d.handler.EmailSubmissionSet(ctx, accountID, args[""])
	case "EmailSubmission/changes":
		result, err = d.handler.EmailSubmissionChanges(ctx, accountID, args[""])
	case "EmailSubmission/query":
		result, err = d.handler.EmailSubmissionQuery(ctx, accountID, args[""])
	case "EmailSubmission/queryChanges":
		result, err = d.handler.EmailSubmissionQueryChanges(ctx, accountID, args[""])

	// VacationResponse methods
	case "VacationResponse/get":
		result, err = d.handler.VacationResponseGet(ctx, accountID, args[""])
	case "VacationResponse/set":
		result, err = d.handler.VacationResponseSet(ctx, accountID, args[""])
	case "VacationResponse/changes":
		result, err = d.handler.VacationResponseChanges(ctx, accountID, args[""])

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
	// The response is [methodName, result, clientId]
	return Invocation{
		Name:      methodName,
		Arguments: map[string]json.RawMessage{"result": result},
		ClientID:  clientID,
	}, nil
}

func (d *Dispatcher) errorResponse(call Invocation, err error) Invocation {
	return d.errorResponseFromError(call.Name, call.ClientID, err)
}

func (d *Dispatcher) errorResponseFromError(methodName, clientID string, err error) Invocation {
	// JMAP error response format: [methodName, {type: "...", description: "..."}, clientId]
	errorArgs := map[string]json.RawMessage{
		"type":        json.RawMessage(`"unknownMethod"`),
		"description": json.RawMessage(`"` + err.Error() + `"`),
	}
	return Invocation{
		Name:      methodName,
		Arguments: errorArgs,
		ClientID:  clientID,
	}
}