package base

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// ArgumentSet builds the allowed-name set used by RejectUnknownArguments.
func ArgumentSet(names ...string) map[string]bool {
	set := make(map[string]bool, len(names))
	for _, name := range names {
		set[name] = true
	}
	return set
}

// RejectUnknownArguments rejects any argument name not present in allowed.
// RFC 8620 section 5 requires unknown arguments to produce an
// invalidArguments error rather than being silently ignored. It must be called
// after result references are resolved so "#foo" keys have become "foo".
func RejectUnknownArguments(args json.RawMessage, allowed map[string]bool) error {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(args, &obj); err != nil {
		return Errorf("invalidArguments", "arguments must be an object")
	}
	unknown := make([]string, 0)
	for name := range obj {
		if !allowed[name] {
			unknown = append(unknown, name)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return Errorf("invalidArguments", "unknown arguments: %s", strings.Join(unknown, ", "))
	}
	return nil
}

// ValidateCreateProperties checks each object in a /set create argument for
// unknown or immutable properties. It returns a map from creation id to the
// sorted property names that the caller must reject with an invalidProperties
// SetError. Objects with no offending properties are omitted from the result.
func ValidateCreateProperties(create map[string]json.RawMessage, allowed, immutable map[string]bool) map[string][]string {
	invalid := make(map[string][]string)
	for creationID, raw := range create {
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(raw, &obj); err != nil {
			// A non-object create value is rejected as invalidArguments by the
			// caller; do not duplicate that error here.
			continue
		}
		bad := make([]string, 0)
		for name := range obj {
			if !allowed[name] || immutable[name] {
				bad = append(bad, name)
			}
		}
		if len(bad) > 0 {
			sort.Strings(bad)
			invalid[creationID] = bad
		}
	}
	return invalid
}

// ApplyPatch applies an RFC 8620 section 5.3 PatchObject to a JSON object.
// Patch keys are JSON Pointers with an implicit leading "/" (for example
// "keywords/$seen" addresses "/keywords/$seen"). A null value removes the
// property, or is a no-op if the key is absent. Any RFC 8620 restriction
// violation (pointer escaping, array traversal, a missing intermediate path,
// or conflicting prefixes) is reported as an invalidPatch error.
func ApplyPatch(current json.RawMessage, patch json.RawMessage) (json.RawMessage, error) {
	var obj any
	if err := json.Unmarshal(current, &obj); err != nil {
		return nil, Errorf("invalidPatch", "current value is not valid JSON: %v", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(patch, &fields); err != nil {
		return nil, Errorf("invalidPatch", "PatchObject must be an object")
	}

	type patchOp struct {
		tokens []string
		value  json.RawMessage
	}
	ops := make([]patchOp, 0, len(fields))
	for key, value := range fields {
		tokens, err := parsePatchPointer(key)
		if err != nil {
			return nil, err
		}
		ops = append(ops, patchOp{tokens: tokens, value: value})
	}
	// RFC 8620: no patch pointer may be a prefix of another patch pointer.
	for i := range ops {
		for j := range ops {
			if i == j {
				continue
			}
			if len(ops[i].tokens) < len(ops[j].tokens) && prefixOf(ops[i].tokens, ops[j].tokens) {
				return nil, Errorf("invalidPatch", "patch pointer %q is a prefix of %q",
					patchKey(ops[i].tokens), patchKey(ops[j].tokens))
			}
		}
	}

	root, ok := obj.(map[string]any)
	if !ok {
		return nil, Errorf("invalidPatch", "current value must be an object")
	}
	// Sort so application order is deterministic even though patches are an
	// unordered set; prefix conflicts have already been excluded.
	sort.Slice(ops, func(i, j int) bool {
		return strings.Compare(patchKey(ops[i].tokens), patchKey(ops[j].tokens)) < 0
	})
	for _, op := range ops {
		if err := applyPatchOp(root, op.tokens, op.value); err != nil {
			return nil, err
		}
	}
	return json.Marshal(root)
}

func applyPatchOp(root map[string]any, tokens []string, value json.RawMessage) error {
	node := any(root)
	for _, token := range tokens[:len(tokens)-1] {
		obj, ok := node.(map[string]any)
		if !ok {
			return Errorf("invalidPatch", "patch path traverses a non-object")
		}
		child, ok := obj[token]
		if !ok {
			return Errorf("invalidPatch", "patch path %q does not exist", patchKey(tokens))
		}
		if _, isArray := child.([]any); isArray {
			return Errorf("invalidPatch", "patch path must not reference inside an array")
		}
		node = child
	}
	parent, ok := node.(map[string]any)
	if !ok {
		return Errorf("invalidPatch", "patch path traverses a non-object")
	}
	last := tokens[len(tokens)-1]
	if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
		delete(parent, last)
		return nil
	}
	var decoded any
	decoder := json.NewDecoder(bytes.NewReader(value))
	decoder.UseNumber()
	if err := decoder.Decode(&decoded); err != nil {
		return Errorf("invalidPatch", "patch value is not valid JSON: %v", err)
	}
	parent[last] = decoded
	return nil
}

// parsePatchPointer validates the JSON Pointer escapes and decodes a patch key
// into its tokens. The implicit leading slash means key "" addresses the whole
// object, which is not a valid patch target on its own.
func parsePatchPointer(key string) ([]string, error) {
	if key == "" {
		return nil, Errorf("invalidPatch", "patch pointer must name a property")
	}
	raw := "/" + key
	tokens := strings.Split(raw, "/")[1:] // drop the empty token before the leading slash
	for i, token := range tokens {
		var b strings.Builder
		for j := 0; j < len(token); j++ {
			if token[j] != '~' {
				b.WriteByte(token[j])
				continue
			}
			if j+1 >= len(token) || (token[j+1] != '0' && token[j+1] != '1') {
				return nil, Errorf("invalidPatch", "invalid escape in patch pointer %q", key)
			}
			j++
			if token[j] == '0' {
				b.WriteByte('~')
			} else {
				b.WriteByte('/')
			}
		}
		tokens[i] = b.String()
	}
	return tokens, nil
}

func prefixOf(prefix, full []string) bool {
	if len(prefix) > len(full) {
		return false
	}
	for i := range prefix {
		if prefix[i] != full[i] {
			return false
		}
	}
	return true
}

func patchKey(tokens []string) string {
	parts := make([]string, len(tokens))
	for i, token := range tokens {
		parts[i] = strings.ReplaceAll(strings.ReplaceAll(token, "~", "~0"), "/", "~1")
	}
	return strings.Join(parts, "/")
}

// JSONEqual compares two JSON values by structure rather than by byte layout,
// so key order and whitespace do not create false changes.
func JSONEqual(left, right json.RawMessage) bool {
	return canonicalJSON(left) == canonicalJSON(right)
}

func canonicalJSON(raw json.RawMessage) string {
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return string(raw)
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return string(raw)
	}
	return string(encoded)
}

// PatchValidation is the classified result of applying a PatchObject to the
// current JSON representation of an object.
type PatchValidation struct {
	Patched   json.RawMessage
	Changed   map[string]json.RawMessage
	Unknown   []string
	Immutable []string
}

// ValidatePatch applies an RFC 8620 section 5.3 PatchObject and classifies its
// effects. A removed property is treated as an explicit null so nullable
// properties can be cleared even when the current object includes null fields.
// Unknown top-level keys are reported even when a null value makes ApplyPatch
// a no-op. Immutable names are reported only when their value actually changes,
// allowing a full-object patch to repeat unchanged server-set properties.
// Nested patch pointers are never allowed: they are rejected by ApplyPatch with
// an invalidPatch error before classification.
func ValidatePatch(current, patch json.RawMessage, allowed, immutable map[string]bool) (PatchValidation, error) {
	patched, err := ApplyPatch(current, patch)
	if err != nil {
		return PatchValidation{}, err
	}
	var currentFields, patchedFields map[string]json.RawMessage
	if err := json.Unmarshal(current, &currentFields); err != nil {
		return PatchValidation{}, Errorf("invalidPatch", "current value is not an object")
	}
	if err := json.Unmarshal(patched, &patchedFields); err != nil {
		return PatchValidation{}, Errorf("invalidPatch", "patched value is not an object")
	}
	changed := make(map[string]json.RawMessage)
	for name, oldValue := range currentFields {
		newValue, ok := patchedFields[name]
		if !ok {
			newValue = json.RawMessage("null")
		}
		if !JSONEqual(oldValue, newValue) {
			changed[name] = newValue
		}
	}
	for name, newValue := range patchedFields {
		if _, existed := currentFields[name]; !existed {
			changed[name] = newValue
		}
	}
	var rawPatch map[string]json.RawMessage
	if err := json.Unmarshal(patch, &rawPatch); err != nil {
		return PatchValidation{}, Errorf("invalidPatch", "PatchObject must be an object")
	}
	result := PatchValidation{Patched: patched, Changed: changed}
	for name := range rawPatch {
		if !allowed[name] {
			result.Unknown = append(result.Unknown, name)
		}
	}
	for name := range changed {
		if immutable[name] {
			result.Immutable = append(result.Immutable, name)
		}
	}
	sort.Strings(result.Unknown)
	sort.Strings(result.Immutable)
	return result, nil
}

// SetErrorNames formats a list of property names for an invalidProperties
// SetError description.
func SetErrorNames(names []string) string {
	return fmt.Sprintf("invalid properties: %s", strings.Join(names, ", "))
}
