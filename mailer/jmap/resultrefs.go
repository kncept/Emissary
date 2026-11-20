package jmap

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// resultReference is the RFC 8620 section 3.7 object used as the value of an
// argument whose name starts with '#'.
type resultReference struct {
	ResultOf string `json:"resultOf"`
	Name     string `json:"name"`
	Path     string `json:"path"`
}

// invalidResultReferenceError builds an error whose prefix is mapped to the
// JMAP invalidResultReference method-level error by errorResponseFromError.
func invalidResultReferenceError(format string, args ...any) error {
	return fmt.Errorf("invalidResultReference: "+format, args...)
}

// resolveResultReferences replaces every "#name" argument with the value
// obtained from an earlier method response. responses holds the first response
// emitted for each method call id processed so far in this request; only those
// responses may be referenced (RFC 8620 section 3.7).
//
// The input map is copied rather than modified: a rejected call must not leave
// partially resolved arguments behind. Errors carry the JMAP error type prefix
// expected by errorResponseFromError.
func resolveResultReferences(args map[string]json.RawMessage, responses map[string]Invocation) (map[string]json.RawMessage, error) {
	var refKeys []string
	for key := range args {
		if strings.HasPrefix(key, "#") {
			refKeys = append(refKeys, key)
		}
	}
	if len(refKeys) == 0 {
		return args, nil
	}
	sort.Strings(refKeys)

	resolved := make(map[string]json.RawMessage, len(args))
	for key, value := range args {
		resolved[key] = value
	}

	for _, key := range refKeys {
		name := strings.TrimPrefix(key, "#")
		if name == "" {
			return nil, invalidResultReferenceError("argument %q has no name after '#'", key)
		}
		// RFC 8620: specifying an argument in both forms is invalidArguments,
		// not invalidResultReference.
		if _, ok := resolved[name]; ok {
			return nil, fmt.Errorf("invalidArguments: both %q and %q given for the same argument", name, key)
		}

		var ref resultReference
		if err := json.Unmarshal(args[key], &ref); err != nil {
			return nil, invalidResultReferenceError("argument %q: malformed result reference: %v", key, err)
		}
		if ref.ResultOf == "" || ref.Name == "" || ref.Path == "" {
			return nil, invalidResultReferenceError("argument %q: resultOf, name and path are required", key)
		}
		response, ok := responses[ref.ResultOf]
		if !ok {
			return nil, invalidResultReferenceError("argument %q: no earlier response for method call id %q", key, ref.ResultOf)
		}
		// An error response with a matching call id counts as a response, but
		// its name will not match the referenced method name.
		if response.Name != ref.Name {
			return nil, invalidResultReferenceError("argument %q: method call %q returned %q, want %q", key, ref.ResultOf, response.Name, ref.Name)
		}

		argumentsJSON, err := json.Marshal(response.Arguments)
		if err != nil {
			return nil, invalidResultReferenceError("argument %q: cannot encode response arguments: %v", key, err)
		}
		var document interface{}
		decoder := json.NewDecoder(bytes.NewReader(argumentsJSON))
		decoder.UseNumber()
		if err := decoder.Decode(&document); err != nil {
			return nil, invalidResultReferenceError("argument %q: response arguments are not valid JSON: %v", key, err)
		}
		value, ok := resolveJSONPointer(document, ref.Path)
		if !ok {
			return nil, invalidResultReferenceError("argument %q: path %q does not resolve in the response to %q", key, ref.Path, ref.ResultOf)
		}
		encoded, err := json.Marshal(value)
		if err != nil {
			return nil, invalidResultReferenceError("argument %q: cannot encode resolved value: %v", key, err)
		}

		resolved[name] = encoded
		delete(resolved, key)
	}
	return resolved, nil
}

// resolveJSONPointer applies an RFC 6901 pointer to a decoded JSON document.
// The empty pointer selects the whole document. In addition to RFC 6901, a
// token of "*" is allowed while the current value is an array: the remaining
// tokens are applied to every item and array results are flattened into the
// output (RFC 8620 section 3.7).
func resolveJSONPointer(document interface{}, pointer string) (interface{}, bool) {
	if pointer == "" {
		return document, true
	}
	if !strings.HasPrefix(pointer, "/") {
		return nil, false
	}
	rawTokens := strings.Split(pointer[1:], "/")
	tokens := make([]string, len(rawTokens))
	for i, raw := range rawTokens {
		token, ok := unescapeJSONPointerToken(raw)
		if !ok {
			return nil, false
		}
		tokens[i] = token
	}
	return applyJSONPointerTokens(document, tokens)
}

// unescapeJSONPointerToken decodes the ~0 and ~1 escapes, rejecting any other
// use of '~' as an invalid pointer.
func unescapeJSONPointerToken(token string) (string, bool) {
	if !strings.Contains(token, "~") {
		return token, true
	}
	var out strings.Builder
	for i := 0; i < len(token); i++ {
		if token[i] != '~' {
			out.WriteByte(token[i])
			continue
		}
		if i+1 >= len(token) {
			return "", false
		}
		switch token[i+1] {
		case '0':
			out.WriteByte('~')
		case '1':
			out.WriteByte('/')
		default:
			return "", false
		}
		i++
	}
	return out.String(), true
}

func applyJSONPointerTokens(value interface{}, tokens []string) (interface{}, bool) {
	if len(tokens) == 0 {
		return value, true
	}
	token, rest := tokens[0], tokens[1:]

	switch current := value.(type) {
	case []interface{}:
		if token == "*" {
			items := make([]interface{}, 0, len(current))
			for _, item := range current {
				resolved, ok := applyJSONPointerTokens(item, rest)
				if !ok {
					return nil, false
				}
				if nested, ok := resolved.([]interface{}); ok {
					items = append(items, nested...)
				} else {
					items = append(items, resolved)
				}
			}
			return items, true
		}
		// RFC 6901 array indexes are decimal digits with no leading zeros;
		// "-", "+1" and other non-numeric tokens are not valid pointers.
		for i := 0; i < len(token); i++ {
			if token[i] < '0' || token[i] > '9' {
				return nil, false
			}
		}
		if len(token) > 1 && token[0] == '0' {
			return nil, false
		}
		index, err := strconv.Atoi(token)
		if err != nil || index < 0 || index >= len(current) {
			return nil, false
		}
		return applyJSONPointerTokens(current[index], rest)
	case map[string]interface{}:
		item, ok := current[token]
		if !ok {
			return nil, false
		}
		return applyJSONPointerTokens(item, rest)
	default:
		return nil, false
	}
}
