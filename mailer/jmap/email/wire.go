package email

import (
	"bytes"
	"encoding/json"
	"net/mail"
	"strconv"
	"strings"
	"time"

	"github.com/kncept/Emissary/mailer/components/storage"
	mailemail "github.com/kncept/Emissary/mailer/email"
	"github.com/kncept/Emissary/mailer/jmap/internal/base"
	"github.com/kncept/Emissary/mailer/jmap/internal/model"
)

// wireView is the single parsed RFC 5322 view of one message. Email/get builds
// it once and derives blobId, size, headers, the MIME tree and body values
// from it, so those properties can never disagree with each other.
type wireView struct {
	root     *wirePart
	parts    map[string]*wirePart
	textBody []EmailBodyPart
	htmlBody []EmailBodyPart
	attached []EmailBodyPart
	headers  []EmailHeader
}

type wirePart struct {
	jmap     EmailBodyPart
	part     *mailemail.BodyPart
	children []*wirePart
}

// newWireView parses raw into a JMAP-facing MIME tree. Part ids follow RFC 8621
// section 4.1.4: root is 1, children are 1.1, 1.2, and so on; multipart nodes
// have no partId.
func newWireView(id string, raw []byte) *wireView {
	parsed := mailemail.ParseWireFormat(id, bytes.NewReader(raw))
	view := &wireView{
		parts:   make(map[string]*wirePart),
		headers: jmapHeaders(parsed.Headers),
	}
	view.root = view.buildPart(parsed.BodyStructure, "", 1)
	view.collect(view.root)
	return view
}

func (v *wireView) buildPart(part *mailemail.BodyPart, parentID string, index int) *wirePart {
	if part == nil {
		return nil
	}
	mediaType := strings.ToLower(strings.TrimSpace(part.MediaType))
	multipart := strings.HasPrefix(mediaType, "multipart/")
	path := strconv.Itoa(index)
	if parentID != "" {
		path = parentID + "." + path
	}

	wp := &wirePart{part: part, jmap: EmailBodyPart{
		Type:    part.MediaType,
		Size:    partSize(part),
		Headers: jmapHeaders(part.Headers),
	}}
	if multipart {
		for i, child := range part.Parts {
			sub := v.buildPart(child, path, i+1)
			if sub != nil {
				wp.children = append(wp.children, sub)
				wp.jmap.SubParts = append(wp.jmap.SubParts, sub.jmap)
			}
		}
		return wp
	}

	wp.jmap.PartID = &path
	partBlobID := storage.BlobID(part.Body)
	wp.jmap.BlobID = &partBlobID
	v.parts[path] = wp
	if charset := strings.TrimSpace(part.Charset); charset != "" {
		wp.jmap.Charset = &charset
	}
	if name := strings.TrimSpace(part.Filename); name != "" {
		wp.jmap.Name = &name
	} else if name = strings.TrimSpace(part.Params["name"]); name != "" {
		wp.jmap.Name = &name
	}
	if disposition := strings.TrimSpace(part.Disposition); disposition != "" {
		wp.jmap.Disposition = &disposition
	}
	cid := strings.TrimSpace(part.Params["cid"])
	if cid == "" {
		// Content-ID is a standalone MIME header, not a Content-Type
		// parameter. RFC 8621 section 4.1.4 exposes it without the angle
		// brackets used on the wire.
		cid = strings.Trim(strings.TrimSpace(headerValue(part.Headers, "Content-ID")), "<>")
	}
	if cid != "" {
		wp.jmap.CID = &cid
	}
	if languages := splitHeaderValues(headerValue(part.Headers, "Content-Language")); len(languages) > 0 {
		wp.jmap.Language = languages
	}
	if location := strings.TrimSpace(headerValue(part.Headers, "Content-Location")); location != "" {
		wp.jmap.Location = &location
	}
	return wp
}

func (v *wireView) collect(wp *wirePart) {
	if wp == nil {
		return
	}
	if wp.jmap.PartID != nil {
		switch strings.ToLower(strings.TrimSpace(wp.jmap.Type)) {
		case "text/plain":
			v.textBody = append(v.textBody, wp.jmap)
		case "text/html":
			v.htmlBody = append(v.htmlBody, wp.jmap)
		}
		attachment := wp.jmap.Disposition != nil && strings.EqualFold(strings.TrimSpace(*wp.jmap.Disposition), "attachment")
		if attachment || wp.jmap.Name != nil {
			v.attached = append(v.attached, wp.jmap)
		}
	}
	for _, child := range wp.children {
		v.collect(child)
	}
}

func partSize(part *mailemail.BodyPart) int {
	if part == nil {
		return 0
	}
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(part.MediaType)), "multipart/") {
		total := 0
		for _, child := range part.Parts {
			total += partSize(child)
		}
		return total
	}
	return len(part.Body)
}

func jmapHeaders(headers []mailemail.Header) []EmailHeader {
	out := make([]EmailHeader, 0, len(headers))
	for _, h := range headers {
		out = append(out, EmailHeader{Name: h.Name, Value: h.Value})
	}
	return out
}

func headerValue(headers []mailemail.Header, name string) string {
	for _, h := range headers {
		if strings.EqualFold(h.Name, name) {
			return strings.TrimSpace(h.Value)
		}
	}
	return ""
}

// addressHeader reads an address-bearing header from the parsed wire format.
// It is used for Sender because that header has no first-class field on the
// internal UserEmail model.
func addressHeader(headers []EmailHeader, name string) []model.EmailAddress {
	for _, h := range headers {
		if !strings.EqualFold(h.Name, name) {
			continue
		}
		parsed, err := mail.ParseAddressList(mailemail.DecodeHeader(h.Value))
		if err != nil {
			continue
		}
		out := make([]model.EmailAddress, 0, len(parsed))
		for _, address := range parsed {
			out = append(out, model.EmailAddress{Name: address.Name, Email: address.Address})
		}
		return out
	}
	return nil
}

func splitHeaderValues(value string) []string {
	if value == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

// bodyValues returns decoded values keyed by real part id. Only text parts
// are fetched: binary attachments have no UTF-8 JMAP string representation.
func (v *wireView) bodyValues(fetchText, fetchHTML, fetchAll bool, maxBytes int) map[string]EmailBodyValue {
	values := make(map[string]EmailBodyValue)
	if v == nil {
		return values
	}
	for id, wp := range v.parts {
		mediaType := strings.ToLower(strings.TrimSpace(wp.jmap.Type))
		if !strings.HasPrefix(mediaType, "text/") {
			continue
		}
		fetch := fetchAll || (fetchText && mediaType == "text/plain") || (fetchHTML && mediaType == "text/html")
		if !fetch {
			continue
		}
		value, lossless := mailemail.DecodeCharsetWithStatus(wp.part.Body, wp.part.Charset)
		result := truncateBodyValue(value, maxBytes, mediaType)
		result.IsEncodingProblem = !lossless
		values[id] = result
	}
	return values
}

// injectHeaderProperties adds the requested header:{name} properties to a
// JSON object (Email or EmailBodyPart) before property selection. Headers are
// matched case-insensitively and the unselected "headers" array is removed
// later by the selection filter.
func injectHeaderProperties(obj map[string]json.RawMessage, sel *base.Selection) error {
	if sel == nil {
		return nil
	}
	names := sel.DynamicNames()
	if len(names) == 0 {
		return nil
	}
	var headers []EmailHeader
	if raw, ok := obj["headers"]; ok {
		if err := json.Unmarshal(raw, &headers); err != nil {
			return err
		}
	}
	for _, name := range names {
		value, err := json.Marshal(headerSelection(headers, name))
		if err != nil {
			return err
		}
		obj[name] = value
	}
	return nil
}

// headerSelection implements RFC 8621 section 4.1.3 for the header property
// forms Emissary supports: raw text, asText, asAddresses, asMessageIds,
// asDate, asURLs and the :all suffix.
func headerSelection(headers []EmailHeader, request string) any {
	name, form, all := parseHeaderRequest(request)
	matches := make([]string, 0, len(headers))
	for _, h := range headers {
		if strings.EqualFold(h.Name, name) {
			matches = append(matches, h.Value)
		}
	}
	if all {
		values := make([]any, 0, len(matches))
		for _, value := range matches {
			values = append(values, parseHeaderForm(value, form))
		}
		return values
	}
	if len(matches) == 0 {
		return nil
	}
	return parseHeaderForm(matches[len(matches)-1], form)
}

func jmapAddresses(value string) []model.EmailAddress {
	parsed, err := mail.ParseAddressList(value)
	if err != nil {
		return []model.EmailAddress{}
	}
	out := make([]model.EmailAddress, 0, len(parsed))
	for _, address := range parsed {
		out = append(out, model.EmailAddress{Name: address.Name, Email: address.Address})
	}
	return out
}

// jmapGroupedAddresses implements RFC 8621 section 4.1.2's
// asGroupedAddresses form. A group is a display name followed by a colon and
// a comma-separated address list terminated by a semicolon; addresses outside
// any named group are returned in a single unnamed group.
func jmapGroupedAddresses(value string) []model.EmailAddressGroup {
	groups := make([]model.EmailAddressGroup, 0, 2)
	var loose []string
	chunks := strings.Split(value, ";")
	for _, chunk := range chunks {
		chunk = strings.TrimSpace(chunk)
		if chunk == "" {
			continue
		}
		colon := strings.IndexByte(chunk, ':')
		at := strings.IndexByte(chunk, '@')
		if colon > 0 && (at < 0 || colon < at) {
			name := strings.Trim(strings.TrimSpace(chunk[:colon]), "\"")
			groups = append(groups, model.EmailAddressGroup{
				Name:      name,
				Addresses: jmapAddresses(chunk[colon+1:]),
			})
			continue
		}
		loose = append(loose, chunk)
	}
	if len(loose) > 0 {
		groups = append(groups, model.EmailAddressGroup{
			Name:      "",
			Addresses: jmapAddresses(strings.Join(loose, ",")),
		})
	}
	return groups
}

func parseHeaderRequest(request string) (name, form string, all bool) {
	rest := strings.TrimPrefix(request, "header:")
	all = strings.HasSuffix(rest, ":all")
	rest = strings.TrimSuffix(rest, ":all")
	if index := strings.LastIndex(rest, ":as"); index >= 0 {
		form = rest[index+len(":as"):]
		rest = rest[:index]
	}
	return rest, form, all
}

func parseHeaderForm(value, form string) any {
	switch strings.ToLower(form) {
	case "", "raw":
		return value
	case "text":
		return mailemail.DecodeHeader(value)
	case "addresses":
		return jmapAddresses(value)
	case "groupedaddresses":
		return jmapGroupedAddresses(value)
	case "messageids":
		out := make([]string, 0)
		for _, token := range strings.FieldsFunc(value, func(r rune) bool {
			return r == ' ' || r == '\t' || r == ',' || r == '\r' || r == '\n'
		}) {
			out = append(out, token)
		}
		return out
	case "date":
		parsed, err := mail.ParseDate(value)
		if err != nil {
			return nil
		}
		return parsed.UTC().Format(time.RFC3339)
	case "urls":
		return strings.Fields(value)
	default:
		return nil
	}
}
