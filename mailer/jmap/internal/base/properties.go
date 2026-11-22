package base

import (
	"bytes"
	"encoding/json"
	"reflect"
	"sort"
	"strings"
)

// Selection is a validated `properties` (or `bodyProperties`) argument for a
// JMAP /get method. A nil Selection means the argument was omitted or null:
// every property is returned and no filtering is performed.
type Selection struct {
	names         map[string]bool
	dynamicPrefix string
	recurseKey    string
}

// DefaultProperties returns the RFC-defined default `properties` array for a
// JMAP data type. A nil result means the type has no special default and the
// RFC 8620 section 5.1 default of "all properties" applies. RFC 8621 overrides
// this for Email (section 4.1), Email/parse (section 4.9) and EmailBodyPart
// (the bodyProperties argument of sections 4.2 and 4.9).
func DefaultProperties(typeName string) []string {
	switch typeName {
	case "Email":
		return []string{
			"id", "blobId", "threadId", "mailboxIds", "keywords", "size",
			"receivedAt", "messageId", "inReplyTo", "references", "sender",
			"from", "to", "cc", "bcc", "replyTo", "subject", "sentAt",
			"hasAttachment", "preview", "bodyValues", "textBody", "htmlBody",
			"attachments",
		}
	case "Email/parse":
		return []string{
			"messageId", "inReplyTo", "references", "sender", "from", "to",
			"cc", "bcc", "replyTo", "subject", "sentAt", "hasAttachment",
			"preview", "bodyValues", "textBody", "htmlBody", "attachments",
		}
	case "EmailBodyPart":
		return []string{
			"partId", "blobId", "size", "name", "type", "charset",
			"disposition", "cid", "language", "location",
		}
	default:
		return nil
	}
}

// ParseSelection reads the named argument from a /get call and validates every
// requested name against the JSON property names of model. A dynamicPrefix
// (for example "header:") additionally permits names with that prefix, which
// RFC 8621 defines as dynamic properties. An absent or null argument yields a
// nil Selection; an unknown name yields an invalidArguments method error.
func ParseSelection(args json.RawMessage, key string, model any, dynamicPrefix string) (*Selection, error) {
	return parseSelection(args, key, model, dynamicPrefix, nil)
}

// ParseSelectionWithDefault is ParseSelection but applies defaultNames when
// the argument is omitted or null. Passing nil keeps the RFC 8620 section 5.1
// behaviour of returning every property. The default is validated against the
// model exactly like an explicit list, so it cannot request unknown names.
func ParseSelectionWithDefault(args json.RawMessage, key string, model any, dynamicPrefix string, defaultNames []string) (*Selection, error) {
	return parseSelection(args, key, model, dynamicPrefix, defaultNames)
}

func parseSelection(args json.RawMessage, key string, model any, dynamicPrefix string, defaultNames []string) (*Selection, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(args, &raw); err != nil {
		return nil, err
	}
	value, ok := raw[key]
	if !ok || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
		if defaultNames == nil {
			return nil, nil
		}
		names := append([]string(nil), defaultNames...)
		return newSelection(names, model, key, dynamicPrefix)
	}

	var names []string
	if err := json.Unmarshal(value, &names); err != nil {
		return nil, Errorf("invalidArguments", "%s must be an array of strings or null", key)
	}
	return newSelection(names, model, key, dynamicPrefix)
}

// newSelection validates a list of property names and builds a Selection.
func newSelection(names []string, model any, key, dynamicPrefix string) (*Selection, error) {
	allowed := propertyNames(model)
	selected := make(map[string]bool, len(names))
	for _, name := range names {
		if allowed[name] {
			selected[name] = true
			continue
		}
		if dynamicPrefix != "" && validDynamicName(name, dynamicPrefix) {
			selected[name] = true
			continue
		}
		return nil, Errorf("invalidArguments", "unknown property %q in %s", name, key)
	}
	return &Selection{names: selected, dynamicPrefix: dynamicPrefix}, nil
}

// DynamicNames returns the requested dynamic property names (for example
// "header:Subject") in a stable order. Callers use it to inject those values
// before filtering, so only requested names can appear in a response.
func (s *Selection) DynamicNames() []string {
	if s == nil || s.dynamicPrefix == "" {
		return nil
	}
	names := make([]string, 0, len(s.names))
	for name := range s.names {
		if strings.HasPrefix(name, s.dynamicPrefix) {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

// Has reports whether name was explicitly selected. A nil Selection selects
// every property.
func (s *Selection) Has(name string) bool {
	return s == nil || s.names[name]
}

// WithRecursion configures a Selection to recurse into the named child key
// when filtering nested objects (EmailBodyPart.subParts, for example). The
// child is only recursed into when it was itself requested.
func (s *Selection) WithRecursion(key string) *Selection {
	if s == nil {
		return nil
	}
	copy := *s
	copy.recurseKey = key
	return &copy
}

// Filter marshals value and retains only the selected properties. The id
// property is always kept when the object has one, as required by RFC 8620
// section 5.1. Arrays are filtered item by item; non-object values pass
// through unchanged.
func (s *Selection) Filter(value any) (json.RawMessage, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return s.FilterRaw(raw)
}

// FilterRaw is Filter for a value that is already JSON.
func (s *Selection) FilterRaw(raw json.RawMessage) (json.RawMessage, error) {
	if s == nil {
		return raw, nil
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return raw, nil
	}
	switch trimmed[0] {
	case '[':
		var items []json.RawMessage
		if err := json.Unmarshal(trimmed, &items); err != nil {
			return nil, err
		}
		out := make([]json.RawMessage, len(items))
		for i, item := range items {
			filtered, err := s.FilterRaw(item)
			if err != nil {
				return nil, err
			}
			out[i] = filtered
		}
		return json.Marshal(out)
	case '{':
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(trimmed, &obj); err != nil {
			return nil, err
		}
		if s.recurseKey != "" {
			if child, ok := obj[s.recurseKey]; ok {
				filtered, err := s.FilterRaw(child)
				if err != nil {
					return nil, err
				}
				obj[s.recurseKey] = filtered
			}
		}
		return json.Marshal(s.Apply(obj))
	default:
		return raw, nil
	}
}

// Apply selects properties from an already-decoded JSON object.
func (s *Selection) Apply(obj map[string]json.RawMessage) map[string]json.RawMessage {
	if s == nil {
		return obj
	}
	out := make(map[string]json.RawMessage, len(s.names)+1)
	if id, ok := obj["id"]; ok {
		out["id"] = id
	}
	for name := range s.names {
		if value, ok := obj[name]; ok {
			out[name] = value
		}
	}
	for _, name := range s.DynamicNames() {
		if value, ok := obj[name]; ok {
			out[name] = value
		}
	}
	return out
}

// ArgumentNames returns the JSON argument names declared by a struct. It is
// the companion to RejectUnknownArguments so a method can derive its allowed
// argument set from the request struct it already decodes into.
func ArgumentNames(model any) map[string]bool {
	return propertyNames(model)
}

// propertyNames returns the JSON property names declared by a model struct.
func propertyNames(model any) map[string]bool {
	t := reflect.TypeOf(model)
	for t != nil && t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	names := make(map[string]bool)
	if t == nil || t.Kind() != reflect.Struct {
		return names
	}
	for i := 0; i < t.NumField(); i++ {
		tag := t.Field(i).Tag.Get("json")
		name := strings.Split(tag, ",")[0]
		if name == "" || name == "-" {
			continue
		}
		names[name] = true
	}
	return names
}

// validDynamicName accepts RFC 8621 dynamic properties such as
// "header:Subject". Header field names and suffixes are printable ASCII.
func validDynamicName(name, prefix string) bool {
	rest, ok := strings.CutPrefix(name, prefix)
	if !ok || rest == "" {
		return false
	}
	for _, r := range rest {
		if r < 33 || r > 126 {
			return false
		}
	}
	return true
}
