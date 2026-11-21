package jmap

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode"
	"unicode/utf8"
)

// RequestLimits mirrors the core capability values advertised by the Session
// object. They are applied to the Request object before any method is called.
type RequestLimits struct {
	MaxCallsInRequest int
	MaxObjectsInGet   int
	MaxObjectsInSet   int
}

// RequestError is an RFC 8620 section 3.6.1 request-level error. These errors
// reject the whole request and are rendered as RFC 7807 problem details.
type RequestError struct {
	Type   string
	Title  string
	Status int
	Detail string
	Limit  string
}

func (e *RequestError) Error() string { return e.Detail }

// Problem returns the RFC 7807 problem details object for the error.
func (e *RequestError) Problem() map[string]any {
	problem := map[string]any{
		"type":   e.Type,
		"title":  e.Title,
		"status": e.Status,
		"detail": e.Detail,
	}
	if e.Limit != "" {
		problem["limit"] = e.Limit
	}
	return problem
}

const (
	errorNotJSON           = "urn:ietf:params:jmap:error:notJSON"
	errorNotRequest        = "urn:ietf:params:jmap:error:notRequest"
	errorUnknownCapability = "urn:ietf:params:jmap:error:unknownCapability"
	errorLimit             = "urn:ietf:params:jmap:error:limit"
)

func notJSONError(detail string) *RequestError {
	return &RequestError{
		Type:   errorNotJSON,
		Title:  "Request is not JSON",
		Status: 400,
		Detail: detail,
	}
}

func notRequestError(detail string) *RequestError {
	return &RequestError{
		Type:   errorNotRequest,
		Title:  "Invalid JMAP Request object",
		Status: 400,
		Detail: detail,
	}
}

func unknownCapabilityError(capability string) *RequestError {
	return &RequestError{
		Type:   errorUnknownCapability,
		Title:  "Unknown capability",
		Status: 400,
		Detail: fmt.Sprintf("The Request object used capability %q, which is not supported by this server.", capability),
	}
}

func limitError(limit string, status int, detail string) *RequestError {
	return &RequestError{
		Type:   errorLimit,
		Title:  "Request exceeds server limit",
		Status: status,
		Detail: detail,
		Limit:  limit,
	}
}

var supportedCapabilities = map[string]struct{}{
	CapabilityCore:             {},
	CapabilityMail:             {},
	CapabilitySubmission:       {},
	CapabilityVacationResponse: {},
}

// IsSupportedCapability reports whether the capability is advertised by the
// Session object and may therefore appear in a Request's `using` array.
func IsSupportedCapability(capability string) bool {
	_, ok := supportedCapabilities[capability]
	return ok
}

// DecodeRequest validates and decodes a JMAP Request object. `contentType`
// must be application/json; `body` must be a single I-JSON Request object.
// Invalid input is returned as a request-level error rather than a decoding
// error so the caller can render the prescribed problem details response.
//
// Method names are validated syntactically. An unknown but well-formed method
// name is deliberately left to the dispatcher, which returns a method-level
// `unknownMethod` error as required by RFC 8620 section 3.6.2.
func DecodeRequest(contentType string, body []byte, limits RequestLimits) (*Request, *RequestError) {
	mediaType, _, err := parseJSONContentType(contentType)
	if err != nil || mediaType != "application/json" {
		return nil, notJSONError("The request Content-Type must be application/json.")
	}

	if err := validateIJSON(body); err != nil {
		return nil, notJSONError("The request body is not valid I-JSON: " + err.Error())
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil || fields == nil {
		return nil, notRequestError("The request body must be a JSON object.")
	}

	rawUsing, ok := fields["using"]
	if !ok {
		return nil, notRequestError("The Request object is missing the required `using` property.")
	}
	var using []string
	if err := decodeJSONArray(rawUsing, &using); err != nil {
		return nil, notRequestError("The `using` property must be an array of strings.")
	}

	rawCalls, ok := fields["methodCalls"]
	if !ok {
		return nil, notRequestError("The Request object is missing the required `methodCalls` property.")
	}
	var callTuples []json.RawMessage
	if err := decodeJSONArray(rawCalls, &callTuples); err != nil {
		return nil, notRequestError("The `methodCalls` property must be an array of Invocation tuples.")
	}

	var createdIDs map[string]string
	if rawCreatedIDs, ok := fields["createdIds"]; ok {
		if isJSONNull(rawCreatedIDs) {
			return nil, notRequestError("The `createdIds` property must be an object when present.")
		}
		if err := json.Unmarshal(rawCreatedIDs, &createdIDs); err != nil || createdIDs == nil {
			return nil, notRequestError("The `createdIds` property must map string creation ids to string ids.")
		}
	}

	req := &Request{
		Using:      using,
		CreatedIDs: createdIDs,
	}

	seenClientIDs := make(map[string]struct{}, len(callTuples))
	for i, rawCall := range callTuples {
		call, err := decodeInvocation(rawCall)
		if err != nil {
			return nil, notRequestError(fmt.Sprintf("methodCalls[%d]: %v", i, err))
		}
		if _, duplicate := seenClientIDs[call.ClientID]; duplicate {
			return nil, notRequestError(fmt.Sprintf("methodCalls[%d]: methodCallId %q is not unique", i, call.ClientID))
		}
		seenClientIDs[call.ClientID] = struct{}{}
		req.MethodCalls = append(req.MethodCalls, *call)
	}

	for _, capability := range using {
		if !IsSupportedCapability(capability) {
			return nil, unknownCapabilityError(capability)
		}
	}

	if limits.MaxCallsInRequest > 0 && len(req.MethodCalls) > limits.MaxCallsInRequest {
		return nil, limitError("maxCallsInRequest", 400, fmt.Sprintf(
			"The request contains %d method calls, exceeding the limit of %d.",
			len(req.MethodCalls), limits.MaxCallsInRequest))
	}

	for _, call := range req.MethodCalls {
		if count, ok := getObjectCount(call); ok && limits.MaxObjectsInGet > 0 && count > limits.MaxObjectsInGet {
			return nil, limitError("maxObjectsInGet", 400, fmt.Sprintf(
				"Method %s requested %d objects, exceeding the limit of %d.",
				call.Name, count, limits.MaxObjectsInGet))
		}
		if count, ok := setObjectCount(call); ok && limits.MaxObjectsInSet > 0 && count > limits.MaxObjectsInSet {
			return nil, limitError("maxObjectsInSet", 400, fmt.Sprintf(
				"Method %s contains %d object changes, exceeding the limit of %d.",
				call.Name, count, limits.MaxObjectsInSet))
		}
	}

	return req, nil
}

// parseJSONContentType accepts application/json with optional parameters such
// as `charset=utf-8`. It intentionally rejects `application/*+json` because
// RFC 8620 section 3.1 requires application/json exactly.
func parseJSONContentType(value string) (string, map[string]string, error) {
	if strings.TrimSpace(value) == "" {
		return "", nil, errors.New("missing Content-Type")
	}
	parts := strings.Split(value, ";")
	mediaType := strings.ToLower(strings.TrimSpace(parts[0]))
	params := make(map[string]string, len(parts)-1)
	for _, part := range parts[1:] {
		key, val, found := strings.Cut(part, "=")
		if !found {
			return "", nil, errors.New("invalid Content-Type parameter")
		}
		params[strings.ToLower(strings.TrimSpace(key))] = strings.Trim(strings.TrimSpace(val), `"`)
	}
	return mediaType, params, nil
}

// validateIJSON checks the constraints I-JSON adds to JSON that matter for a
// request envelope: valid UTF-8, one top-level value, no trailing data, and no
// duplicate object member names.
func validateIJSON(data []byte) error {
	if !utf8.Valid(data) {
		return errors.New("body is not valid UTF-8")
	}

	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := decodeJSONValue(dec); err != nil {
		return err
	}
	if tok, err := dec.Token(); err != io.EOF {
		if err != nil {
			return err
		}
		return fmt.Errorf("unexpected trailing token %v", tok)
	}
	return nil
}

func decodeJSONValue(dec *json.Decoder) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	delim, ok := tok.(json.Delim)
	if !ok {
		return nil
	}

	switch delim {
	case '{':
		seen := make(map[string]struct{})
		for dec.More() {
			keyToken, err := dec.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("object member name is not a string")
			}
			if _, duplicate := seen[key]; duplicate {
				return fmt.Errorf("duplicate object member %q", key)
			}
			seen[key] = struct{}{}
			if err := decodeJSONValue(dec); err != nil {
				return err
			}
		}
		return expectClosingDelim(dec, '}')
	case '[':
		for dec.More() {
			if err := decodeJSONValue(dec); err != nil {
				return err
			}
		}
		return expectClosingDelim(dec, ']')
	default:
		return fmt.Errorf("unexpected delimiter %q", delim)
	}
}

func expectClosingDelim(dec *json.Decoder, want rune) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	if tok != json.Delim(want) {
		return fmt.Errorf("expected %q, got %v", want, tok)
	}
	return nil
}

func decodeJSONArray(raw json.RawMessage, destination any) error {
	if isJSONNull(raw) {
		return errors.New("null is not an array")
	}
	return json.Unmarshal(raw, destination)
}

func decodeInvocation(raw json.RawMessage) (*Invocation, error) {
	var tuple []json.RawMessage
	if err := decodeJSONArray(raw, &tuple); err != nil {
		return nil, errors.New("must be a three-element array")
	}
	if len(tuple) != 3 {
		return nil, fmt.Errorf("must be a three-element array, got %d elements", len(tuple))
	}

	name, err := decodeJSONString(tuple[0])
	if err != nil || !validMethodName(name) {
		return nil, fmt.Errorf("method name must be a non-empty `Type/method` string")
	}

	if isJSONNull(tuple[1]) {
		return nil, errors.New("arguments must be an object")
	}
	var arguments map[string]json.RawMessage
	if err := json.Unmarshal(tuple[1], &arguments); err != nil || arguments == nil {
		return nil, errors.New("arguments must be an object")
	}

	clientID, err := decodeJSONString(tuple[2])
	if err != nil {
		return nil, errors.New("methodCallId must be a string")
	}

	return &Invocation{Name: name, Arguments: arguments, ClientID: clientID}, nil
}

func decodeJSONString(raw json.RawMessage) (string, error) {
	if isJSONNull(raw) {
		return "", errors.New("null is not a string")
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", err
	}
	return value, nil
}

func isJSONNull(raw json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

func validMethodName(name string) bool {
	parts := strings.Split(name, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return false
	}
	for _, r := range name {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func getObjectCount(call Invocation) (int, bool) {
	if !strings.HasSuffix(call.Name, "/get") {
		return 0, false
	}
	rawIDs, ok := call.Arguments["ids"]
	if !ok || isJSONNull(rawIDs) {
		return 0, false
	}
	var ids []json.RawMessage
	if err := json.Unmarshal(rawIDs, &ids); err != nil {
		// A malformed `ids` argument is handled by the method as
		// `invalidArguments`; envelope validation only enforces the limit.
		return 0, false
	}
	return len(ids), true
}

func setObjectCount(call Invocation) (int, bool) {
	if !strings.HasSuffix(call.Name, "/set") {
		return 0, false
	}

	total := 0
	for _, key := range []string{"create", "update"} {
		raw, ok := call.Arguments[key]
		if !ok || isJSONNull(raw) {
			continue
		}
		var objects map[string]json.RawMessage
		if err := json.Unmarshal(raw, &objects); err != nil {
			return 0, false
		}
		total += len(objects)
	}
	if raw, ok := call.Arguments["destroy"]; ok && !isJSONNull(raw) {
		var ids []json.RawMessage
		if err := json.Unmarshal(raw, &ids); err != nil {
			return 0, false
		}
		total += len(ids)
	}
	return total, true
}
