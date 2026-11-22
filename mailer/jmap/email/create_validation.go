package email

import (
	"bytes"
	"encoding/json"
	"sort"
	"strings"

	"github.com/kncept/Emissary/mailer/jmap/internal/base"
)

// emailCreateProperties is the set of properties a client may supply when
// creating a draft (RFC 8621 section 4.6). Server-derived properties and the
// low-level `headers` property are rejected by emailCreateImmutable below.
var emailCreateProperties = base.ArgumentSet(
	"mailboxIds", "keywords", "receivedAt", "messageId", "inReplyTo", "references",
	"sender", "from", "to", "cc", "bcc", "replyTo", "subject", "sentAt",
	"bodyStructure", "bodyValues", "textBody", "htmlBody", "attachments",
)

var emailCreateImmutable = base.ArgumentSet(
	"id", "blobId", "threadId", "size", "hasAttachment", "preview", "headers",
)

// namedHeaderProperties maps a shorthand Email property to the RFC 5322
// header field it represents. Supplying both forms in one create is ambiguous
// and must be rejected (RFC 8621 section 4.6).
var namedHeaderProperties = map[string]string{
	"sender":     "sender",
	"from":       "from",
	"replyTo":    "reply-to",
	"to":         "to",
	"cc":         "cc",
	"bcc":        "bcc",
	"subject":    "subject",
	"sentAt":     "date",
	"messageId":  "message-id",
	"inReplyTo":  "in-reply-to",
	"references": "references",
}

// parsedFormHeaders lists the RFC 5322/RFC 2369 header fields with restricted
// parsed forms. Unknown (non-standard) headers may use any form.
var parsedFormHeaders = map[string]map[string]bool{
	"subject":           {"text": true},
	"comments":          {"text": true},
	"keywords":          {"text": true},
	"list-id":           {"text": true},
	"from":              {"addresses": true, "groupedaddresses": true},
	"sender":            {"addresses": true, "groupedaddresses": true},
	"reply-to":          {"addresses": true, "groupedaddresses": true},
	"to":                {"addresses": true, "groupedaddresses": true},
	"cc":                {"addresses": true, "groupedaddresses": true},
	"bcc":               {"addresses": true, "groupedaddresses": true},
	"resent-from":       {"addresses": true, "groupedaddresses": true},
	"resent-sender":     {"addresses": true, "groupedaddresses": true},
	"resent-reply-to":   {"addresses": true, "groupedaddresses": true},
	"resent-to":         {"addresses": true, "groupedaddresses": true},
	"resent-cc":         {"addresses": true, "groupedaddresses": true},
	"resent-bcc":        {"addresses": true, "groupedaddresses": true},
	"message-id":        {"messageids": true},
	"in-reply-to":       {"messageids": true},
	"references":        {"messageids": true},
	"resent-message-id": {"messageids": true},
	"date":              {"date": true},
	"resent-date":       {"date": true},
	"list-help":         {"urls": true},
	"list-unsubscribe":  {"urls": true},
	"list-subscribe":    {"urls": true},
	"list-post":         {"urls": true},
	"list-owner":        {"urls": true},
	"list-archive":      {"urls": true},
}

var headerForms = map[string]bool{
	"raw": true, "text": true, "addresses": true, "groupedaddresses": true,
	"messageids": true, "date": true, "urls": true,
}

// dynamicHeaderName validates an RFC 8621 `header:{name}` property and returns
// its lower-cased header name and parsed form (empty for the raw form). It
// enforces the restrictions on parsed forms that apply both when fetching and
// when creating an Email.
func dynamicHeaderName(property string) (header, form string, ok bool) {
	if !strings.HasPrefix(property, "header:") {
		return "", "", false
	}
	parts := strings.Split(strings.TrimPrefix(property, "header:"), ":")
	header = strings.ToLower(strings.TrimSpace(parts[0]))
	if header == "" || len(parts) > 3 || len(parts) == 3 && parts[2] != "all" {
		return "", "", false
	}
	if len(parts) > 1 {
		form = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(parts[1], "as")))
		if !headerForms[form] {
			return "", "", false
		}
		if forms, known := parsedFormHeaders[header]; known && !forms[form] {
			return "", "", false
		}
	}
	return header, form, true
}

// validateHeaderPropertyForms rejects forbidden parsed forms in `properties`
// and `bodyProperties` arguments. RFC 8621 section 4.9 requires a method error
// rather than silently returning an empty or wrongly typed header value.
func validateHeaderPropertyForms(args json.RawMessage, keys ...string) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(args, &raw); err != nil {
		return err
	}
	for _, key := range keys {
		value, present := raw[key]
		if !present || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			continue
		}
		var names []string
		if err := json.Unmarshal(value, &names); err != nil {
			continue // ParseSelection reports the type error with the right key.
		}
		for _, name := range names {
			if !strings.HasPrefix(name, "header:") {
				continue
			}
			if _, _, ok := dynamicHeaderName(name); !ok {
				return base.Errorf("invalidArguments", "forbidden header form %q in %s", name, key)
			}
		}
	}
	return nil
}

// validateEmailCreate returns every property that makes an Email/create
// object ambiguous or invalid under RFC 8621 section 4.6. The caller rejects
// them with a single invalidProperties SetError listing all offenders.
func validateEmailCreate(raw json.RawMessage) []string {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil
	}
	bad := make(map[string]bool)

	allowed := make(map[string]bool, len(emailCreateProperties)+len(fields))
	for name := range emailCreateProperties {
		allowed[name] = true
	}
	for name := range fields {
		if strings.HasPrefix(name, "header:") {
			allowed[name] = true
		}
	}
	for _, properties := range base.ValidateCreateProperties(
		map[string]json.RawMessage{"create": raw}, allowed, emailCreateImmutable) {
		for _, property := range properties {
			bad[property] = true
		}
	}

	topLevelHeaders := make(map[string]string, len(namedHeaderProperties))
	for property, header := range namedHeaderProperties {
		if _, present := fields[property]; present {
			topLevelHeaders[header] = property
		}
	}

	for property := range fields {
		if !strings.HasPrefix(property, "header:") {
			continue
		}
		header, _, ok := dynamicHeaderName(property)
		if !ok || strings.HasPrefix(header, "content-") {
			bad[property] = true
		}
		if previous, duplicate := topLevelHeaders[header]; duplicate {
			bad[previous] = true
			bad[property] = true
			continue
		}
		topLevelHeaders[header] = property
	}

	if _, present := fields["bodyStructure"]; present {
		for _, conflicting := range []string{"textBody", "htmlBody", "attachments"} {
			if _, supplied := fields[conflicting]; supplied {
				bad["bodyStructure"] = true
				bad[conflicting] = true
			}
		}
	}

	for _, key := range []string{"textBody", "htmlBody", "attachments"} {
		rawParts, present := fields[key]
		if !present {
			continue
		}
		var parts []json.RawMessage
		if err := json.Unmarshal(rawParts, &parts); err != nil {
			bad[key] = true
			continue
		}
		expected := ""
		if key == "textBody" {
			expected = "text/plain"
		}
		if key == "htmlBody" {
			expected = "text/html"
		}
		if expected != "" && (len(parts) != 1 || bodyPartType(parts[0]) != expected) {
			bad[key] = true
		}
		for _, part := range parts {
			validateBodyPart(part, key, topLevelHeaders, bad)
		}
	}
	if rawPart, present := fields["bodyStructure"]; present {
		validateBodyPart(rawPart, "bodyStructure", topLevelHeaders, bad)
	}

	bodyValuePartIDs := make(map[string]bool)
	if rawValues, present := fields["bodyValues"]; present {
		var values map[string]json.RawMessage
		if err := json.Unmarshal(rawValues, &values); err != nil {
			bad["bodyValues"] = true
		}
		for partID, rawValue := range values {
			bodyValuePartIDs[partID] = true
			var value struct {
				IsEncodingProblem bool `json:"isEncodingProblem"`
				IsTruncated       bool `json:"isTruncated"`
			}
			if err := json.Unmarshal(rawValue, &value); err != nil || value.IsEncodingProblem || value.IsTruncated {
				bad["bodyValues"] = true
			}
		}
	}

	// A partId must resolve to an entry in bodyValues. This check runs after
	// the bodyValues map has been decoded.
	for key, rawPart := range map[string]json.RawMessage{
		"bodyStructure": fields["bodyStructure"], "textBody": fields["textBody"],
		"htmlBody": fields["htmlBody"], "attachments": fields["attachments"],
	} {
		if len(bytes.TrimSpace(rawPart)) == 0 {
			continue
		}
		var partMap map[string]json.RawMessage
		if err := json.Unmarshal(rawPart, &partMap); err == nil {
			checkPartIDReferences(partMap, key, bodyValuePartIDs, bad)
		} else {
			var parts []json.RawMessage
			if err := json.Unmarshal(rawPart, &parts); err == nil {
				for _, part := range parts {
					var child map[string]json.RawMessage
					if json.Unmarshal(part, &child) == nil {
						checkPartIDReferences(child, key, bodyValuePartIDs, bad)
					}
				}
			}
		}
	}

	out := make([]string, 0, len(bad))
	for property := range bad {
		out = append(out, property)
	}
	sort.Strings(out)
	return out
}

func bodyPartType(raw json.RawMessage) string {
	var part struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(raw, &part) != nil {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(part.Type))
}

func validateBodyPart(raw json.RawMessage, property string,
	topLevelHeaders map[string]string, bad map[string]bool) {
	var part map[string]json.RawMessage
	if err := json.Unmarshal(raw, &part); err != nil {
		bad[property] = true
		return
	}
	if _, hasPartID := part["partId"]; hasPartID {
		if _, hasBlobID := part["blobId"]; hasBlobID {
			bad[property] = true
		}
		if _, present := part["charset"]; present {
			bad[property] = true
		}
		if _, present := part["size"]; present {
			bad[property] = true
		}
	}
	if _, present := part["headers"]; present {
		// RFC 8621 section 4.6 forbids the low-level headers property on
		// EmailBodyPart as well as on the top-level Email.
		bad[property] = true
		var headers []struct {
			Name string `json:"name"`
		}
		if json.Unmarshal(part["headers"], &headers) == nil {
			for _, header := range headers {
				markDuplicatePartHeader(header.Name, property, topLevelHeaders, bad)
			}
		}
	}
	for name := range part {
		if !strings.HasPrefix(name, "header:") {
			continue
		}
		header := strings.ToLower(strings.TrimSpace(strings.Split(strings.TrimPrefix(name, "header:"), ":")[0]))
		if strings.HasPrefix(header, "content-transfer-encoding") {
			bad[property] = true
		}
		markDuplicatePartHeader(header, property, topLevelHeaders, bad)
	}
	for _, childKey := range []string{"subParts"} {
		if rawChildren, ok := part[childKey]; ok {
			var children []json.RawMessage
			if json.Unmarshal(rawChildren, &children) == nil {
				for _, child := range children {
					validateBodyPart(child, property, topLevelHeaders, bad)
				}
			}
		}
	}
}

func markDuplicatePartHeader(header, property string, topLevelHeaders map[string]string, bad map[string]bool) {
	name := strings.ToLower(strings.TrimSpace(header))
	topLevelProperty, duplicate := topLevelHeaders[name]
	if !duplicate {
		return
	}
	bad[topLevelProperty] = true
	bad[property] = true
}

func checkPartIDReferences(part map[string]json.RawMessage, property string, bodyValuePartIDs map[string]bool, bad map[string]bool) {
	var partID string
	if raw, ok := part["partId"]; ok && json.Unmarshal(raw, &partID) == nil && partID != "" {
		if !bodyValuePartIDs[partID] {
			bad[property] = true
		}
	}
}
