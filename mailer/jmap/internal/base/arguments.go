package base

import (
	"bytes"
	"encoding/json"
)

// DefaultCollation is the only collation algorithm advertised by the Session
// object (see handleJMAPSession's coreCapabilities).
const DefaultCollation = "i;ascii-casemap"

// PageBoundsForIDs resolves the RFC 8620 section 3.9 pagination arguments
// against an already ordered id list. When anchor is set, position is ignored
// and the window starts at the anchor's index plus anchorOffset. Without an
// anchor, anchorOffset is ignored. A missing anchor is an anchorNotFound error
// rather than a silently ignored argument.
func PageBoundsForIDs(ids []string, position, limit *int, anchor *string, anchorOffset *int) (int, int, error) {
	start := 0
	if anchor != nil {
		index := -1
		for i, id := range ids {
			if id == *anchor {
				index = i
				break
			}
		}
		if index < 0 {
			return 0, 0, Errorf("anchorNotFound", "anchor %q not found in query results", *anchor)
		}
		start = index
		if anchorOffset != nil {
			start += *anchorOffset
		}
	} else if position != nil {
		start = *position
		// RFC 8620 section 3.9: a negative position is an offset from the
		// end of the result set, not an invalid argument.
		if start < 0 {
			start = len(ids) + start
		}
	}
	if start < 0 {
		start = 0
	}
	if start > len(ids) {
		start = len(ids)
	}

	end := len(ids)
	if limit != nil && *limit < end-start {
		end = start + *limit
	}
	if end < start {
		end = start
	}
	return start, end, nil
}

// SortAscending validates a JMAP sort argument. This server supports at most
// one comparator over a property in allowed, with the advertised default
// collation. Everything else is rejected with unsupportedSort so no comparator
// is silently ignored. An absent/empty sort preserves the server's default
// (descending) order.
func SortAscending(sort []interface{}, allowed map[string]bool) (bool, error) {
	if len(sort) == 0 {
		return false, nil
	}
	if len(sort) > 1 {
		return false, Errorf("unsupportedSort", "multi-property sort is not supported")
	}
	spec, ok := sort[0].(map[string]interface{})
	if !ok {
		return false, Errorf("unsupportedSort", "sort comparator must be an object")
	}
	property, ok := spec["property"].(string)
	if !ok || !allowed[property] {
		return false, Errorf("unsupportedSort", "unsupported sort property %v", spec["property"])
	}
	if value, present := spec["collation"]; present && value != nil {
		collation, ok := value.(string)
		if !ok || collation != DefaultCollation {
			return false, Errorf("unsupportedSort", "unsupported collation %v", value)
		}
	}
	if value, present := spec["keyword"]; present && value != nil {
		return false, Errorf("unsupportedSort", "keyword comparators are not supported")
	}
	// RFC 8620 section 2.7: isAscending defaults to true when a comparator
	// is supplied.
	ascending := true
	if value, present := spec["isAscending"]; present {
		flag, ok := value.(bool)
		if !ok {
			return false, Errorf("invalidArguments", "isAscending must be a boolean")
		}
		ascending = flag
	}
	return ascending, nil
}

// ValidateConditionObject rejects compound filter conditions and fields that
// are not in allowed before the condition is decoded into a typed struct (an
// operator object would otherwise decode to an empty, match-everything
// condition). Values may be null, which means the condition was omitted.
func ValidateConditionObject(args json.RawMessage, key string, allowed map[string]bool) error {
	if len(args) == 0 {
		return nil
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(args, &envelope); err != nil {
		return Errorf("invalidArguments", "arguments must be an object")
	}
	raw, ok := envelope[key]
	if !ok || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return Errorf("invalidArguments", "%s must be an object", key)
	}
	for name := range fields {
		if name == "operator" || name == "conditions" {
			return Errorf("unsupportedFilter", "compound %s conditions are not supported", key)
		}
		if !allowed[name] {
			return Errorf("unsupportedFilter", "unsupported %s condition %q", key, name)
		}
	}
	return nil
}

// RejectArgument returns an invalidArguments error for an argument that is
// present but cannot be honoured. Keeping the check in one place makes it
// explicit that the argument was considered rather than silently dropped.
func RejectArgument(name, reason string) error {
	return Errorf("invalidArguments", "%s is not supported: %s", name, reason)
}
