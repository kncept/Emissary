package email

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"strings"
	"time"
)

// ParseError represents a parsing error with context about what section was being parsed.
type ParseError struct {
	Section string // The section being parsed (e.g., "headers", "body", "multipart", "address")
	Message string // Description of the error
	Offset  int64  // Approximate byte offset in the source where the error occurred
}

func (e ParseError) Error() string {
	return fmt.Sprintf("parse error in %s: %s", e.Section, e.Message)
}

// WireFormatEmail represents a fully parsed RFC 5322/822 email message.
// It preserves the complete wire format structure including all headers,
// MIME parts, and their original encoding.
//
// RFC 5322 compliance:
// - Headers are parsed according to RFC 5322 section 3.6
// - Address lists follow RFC 5322 section 3.4
// - Date parsing follows RFC 5322 section 3.3
// - MIME structure follows RFC 2045, 2046, 2047
// - Content-Transfer-Encoding: base64, quoted-printable, 7bit, 8bit, binary
// - Charset decoding for common charsets (UTF-8, ISO-8859-1, US-ASCII)
//
// The parser is lenient: it attempts to recover from errors and continues
// parsing. All encountered errors are recorded in the Errs field.
//
// For bidirectional use, call ToWireFormat() to serialize back to RFC 5322 format.
type WireFormatEmail struct {
	// Headers contains all headers in their original order, including duplicates.
	// Header names are canonicalized (first letter of each word capitalized).
	Headers []Header

	// Parsed header fields for convenient access (derived from Headers).
	// These may be empty if parsing failed for that specific header.
	From        *Address
	To          []Address
	Cc          []Address
	Bcc         []Address
	Subject     string
	Date        time.Time
	MessageID   string
	InReplyTo   []string
	References  []string
	ContentType string
	MIMEVersion string

	// BodyStructure represents the parsed MIME tree.
	BodyStructure *BodyPart

	// RawBody contains the unparsed body bytes (after Content-Transfer-Encoding decoding).
	// This is populated when BodyStructure is nil (non-MIME or parse failure).
	RawBody []byte

	// Errs contains all parse errors encountered during parsing.
	// The parser continues on error (best-effort), so this may be non-empty
	// even when the email is partially usable.
	Errs []ParseError
}

// Header represents a single email header field.
type Header struct {
	Name  string // Canonicalized header name (e.g., "Content-Type")
	Value string // Raw header value (with line folding removed)
}

// BodyPart represents a node in the MIME body structure tree.
type BodyPart struct {
	// Header fields for this part
	Headers []Header

	// Parsed Content-Type
	MediaType string
	Params    map[string]string

	// Content-Transfer-Encoding (raw value)
	TransferEncoding string

	// Charset from Content-Type params
	Charset string

	// For leaf parts: decoded body content
	Body []byte

	// For multipart parts: child parts
	Parts []*BodyPart

	// Boundary for multipart (if applicable)
	Boundary string

	// Content-Disposition info
	Disposition string
	Filename    string
}

// parseWireFormatBytes parses a raw RFC 5322 email message into a WireFormatEmail.
func parseWireFormatBytes(id string, data []byte) *WireFormatEmail {
	wfe := &WireFormatEmail{}

	// Split headers from body
	headerData, bodyData, err := splitHeadersAndBody(data)
	if err != nil {
		wfe.Errs = append(wfe.Errs, ParseError{Section: "headers/body split", Message: err.Error()})
		// Continue with empty headers, treat all as body
		headerData = nil
		bodyData = data
	}

	// Parse headers
	if len(headerData) > 0 {
		wfe.Headers, err = parseHeaders(headerData)
		if err != nil {
			wfe.Errs = append(wfe.Errs, ParseError{Section: "headers", Message: err.Error()})
		}
		// Extract common header fields
		wfe.extractCommonHeaders()
	}

	// Parse body according to Content-Type
	if len(bodyData) > 0 {
		wfe.parseBody(bodyData)
	}

	return wfe
}

// splitHeadersAndBody separates the header section from the body.
// Returns (headers, body, error). Headers include the trailing CRLF.
// Per RFC 5322, headers are separated from body by a blank line (CRLF CRLF).
func splitHeadersAndBody(data []byte) ([]byte, []byte, error) {
	// Find the first occurrence of \r\n\r\n or \n\n
	crlfCrlf := []byte("\r\n\r\n")
	lfLf := []byte("\n\n")

	idx := bytes.Index(data, crlfCrlf)
	if idx >= 0 {
		return data[:idx+2], data[idx+4:], nil // Include first CRLF in headers
	}

	idx = bytes.Index(data, lfLf)
	if idx >= 0 {
		return data[:idx+1], data[idx+2:], nil // Include first LF in headers
	}

	// No blank line found - treat all as headers, empty body
	return data, nil, errors.New("no blank line separating headers from body")
}

// parseHeaders parses the raw header block into a slice of Header structs.
// Handles line folding (RFC 5322 section 2.2.3).
func parseHeaders(data []byte) ([]Header, error) {
	var headers []Header
	scanner := bufio.NewScanner(bytes.NewReader(data))

	var currentName, currentValue strings.Builder
	inHeader := false

	for scanner.Scan() {
		line := scanner.Bytes()

		// Check for line folding: line starts with space or tab
		if len(line) > 0 && (line[0] == ' ' || line[0] == '\t') {
			if inHeader {
				// Folded line: append with a single space
				currentValue.WriteByte(' ')
				currentValue.Write(bytes.TrimLeft(line, " \t"))
			}
			continue
		}

		// Save previous header if any
		if inHeader {
			headers = append(headers, Header{
				Name:  canonicalizeHeaderName(currentName.String()),
				Value: currentValue.String(),
			})
		}

		// Parse new header: "Name: value"
		colonIdx := bytes.IndexByte(line, ':')
		if colonIdx < 0 {
			// Invalid header line - skip but continue
			inHeader = false
			continue
		}

		currentName.Reset()
		currentValue.Reset()
		currentName.Write(line[:colonIdx])
		currentValue.Write(bytes.TrimSpace(line[colonIdx+1:]))
		inHeader = true
	}

	// Save last header
	if inHeader {
		headers = append(headers, Header{
			Name:  canonicalizeHeaderName(currentName.String()),
			Value: currentValue.String(),
		})
	}

	if err := scanner.Err(); err != nil {
		return headers, err
	}

	return headers, nil
}

// canonicalizeHeaderName converts header name to canonical form
// (first letter of each word capitalized, rest lowercase).
func canonicalizeHeaderName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	words := strings.Split(name, "-")
	for i, w := range words {
		if len(w) == 0 {
			continue
		}
		words[i] = strings.ToUpper(w[:1]) + strings.ToLower(w[1:])
	}
	return strings.Join(words, "-")
}

// extractCommonHeaders populates the convenient parsed header fields.
func (wfe *WireFormatEmail) extractCommonHeaders() {
	for _, h := range wfe.Headers {
		switch strings.ToLower(h.Name) {
		case "from":
			if wfe.From == nil {
				addr := parseSingleAddress(h.Value)
				wfe.From = &addr
			}
		case "to":
			wfe.To = append(wfe.To, parseAddressList(h.Value)...)
		case "cc":
			wfe.Cc = append(wfe.Cc, parseAddressList(h.Value)...)
		case "bcc":
			wfe.Bcc = append(wfe.Bcc, parseAddressList(h.Value)...)
		case "subject":
			if wfe.Subject == "" {
				wfe.Subject = decodeHeader(h.Value)
			}
		case "date":
			if wfe.Date.IsZero() {
				wfe.Date = parseDate(h.Value)
			}
		case "message-id":
			if wfe.MessageID == "" {
				wfe.MessageID = strings.TrimSpace(h.Value)
			}
		case "in-reply-to":
			wfe.InReplyTo = append(wfe.InReplyTo, parseMessageIDList(h.Value)...)
		case "references":
			wfe.References = append(wfe.References, parseMessageIDList(h.Value)...)
		case "content-type":
			if wfe.ContentType == "" {
				wfe.ContentType = h.Value
			}
		case "mime-version":
			if wfe.MIMEVersion == "" {
				wfe.MIMEVersion = h.Value
			}
		}
	}
}

// parseBody parses the message body according to the Content-Type header.
func (wfe *WireFormatEmail) parseBody(bodyData []byte) {
	if wfe.ContentType == "" {
		// No Content-Type: treat as text/plain
		wfe.BodyStructure = &BodyPart{
			Headers:          []Header{{Name: "Content-Type", Value: "text/plain"}},
			MediaType:        "text/plain",
			Params:           map[string]string{"charset": "us-ascii"},
			TransferEncoding: "7bit",
			Charset:          "us-ascii",
			Body:             bodyData,
		}
		return
	}

	mediaType, params, err := mime.ParseMediaType(wfe.ContentType)
	if err != nil {
		wfe.Errs = append(wfe.Errs, ParseError{
			Section: "content-type",
			Message: "failed to parse Content-Type: " + err.Error(),
		})
		mediaType = "text/plain"
		params = map[string]string{"charset": "us-ascii"}
	}

	wfe.BodyStructure = &BodyPart{
		Headers:       wfe.getHeadersForType("Content-Type"),
		MediaType:     mediaType,
		Params:        params,
		Charset:       strings.ToLower(strings.TrimSpace(params["charset"])),
		Parts:         []*BodyPart{}, // Initialize Parts slice for multipart
	}

	// Get transfer encoding from headers
	for _, h := range wfe.Headers {
		if strings.EqualFold(h.Name, "Content-Transfer-Encoding") {
			wfe.BodyStructure.TransferEncoding = strings.TrimSpace(h.Value)
			break
		}
	}
	if wfe.BodyStructure.TransferEncoding == "" {
		wfe.BodyStructure.TransferEncoding = "7bit"
	}

	// Decode transfer encoding
	decodedBody, err := wfe.decodeTransferEncoding(wfe.BodyStructure.TransferEncoding, bodyData)
	if err != nil {
		wfe.Errs = append(wfe.Errs, ParseError{
			Section: "transfer-encoding",
			Message: "failed to decode " + wfe.BodyStructure.TransferEncoding + ": " + err.Error(),
		})
		decodedBody = bodyData // Use raw on error
	}

	if strings.HasPrefix(mediaType, "multipart/") {
		wfe.parseMultipart(decodedBody, mediaType, params)
	} else {
		wfe.BodyStructure.Body = decodedBody
	}
}

// getHeadersForType returns all headers matching the given name (case-insensitive).
func (wfe *WireFormatEmail) getHeadersForType(name string) []Header {
	var result []Header
	for _, h := range wfe.Headers {
		if strings.EqualFold(h.Name, name) {
			result = append(result, h)
		}
	}
	return result
}

// parseMultipart parses a multipart body.
func (wfe *WireFormatEmail) parseMultipart(body []byte, mediaType string, params map[string]string) {
	boundary := params["boundary"]
	if boundary == "" {
		wfe.Errs = append(wfe.Errs, ParseError{
			Section: "multipart",
			Message: "missing boundary parameter",
		})
		wfe.BodyStructure.Body = body
		return
	}

	mr := multipart.NewReader(bytes.NewReader(body), boundary)
	wfe.BodyStructure.Parts = []*BodyPart{}
	wfe.BodyStructure.Boundary = boundary

	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			wfe.Errs = append(wfe.Errs, ParseError{
				Section: "multipart",
				Message: "failed to read part: " + err.Error(),
			})
			break
		}

		partData, err := io.ReadAll(part)
		if err != nil {
			wfe.Errs = append(wfe.Errs, ParseError{
				Section: "multipart",
				Message: "failed to read part body: " + err.Error(),
			})
			part.Close()
			continue
		}
		part.Close()

		// Parse part headers
		partHeaders := make([]Header, 0, len(part.Header))
		for k, v := range part.Header {
			for _, vv := range v {
				partHeaders = append(partHeaders, Header{
					Name:  canonicalizeHeaderName(k),
					Value: vv,
				})
			}
		}

		// Parse part Content-Type
		partContentType := ""
		for _, h := range partHeaders {
			if strings.EqualFold(h.Name, "Content-Type") {
				partContentType = h.Value
				break
			}
		}

		partMediaType := "text/plain"
		partParams := map[string]string{"charset": "us-ascii"}
		if partContentType != "" {
			if mt, p, err := mime.ParseMediaType(partContentType); err == nil {
				partMediaType = mt
				partParams = p
			}
		}

		// Get part transfer encoding
		partTransferEncoding := "7bit"
		for _, h := range partHeaders {
			if strings.EqualFold(h.Name, "Content-Transfer-Encoding") {
				partTransferEncoding = strings.TrimSpace(h.Value)
				break
			}
		}

		// Decode part transfer encoding
		partDecoded, err := wfe.decodeTransferEncoding(partTransferEncoding, partData)
		if err != nil {
			wfe.Errs = append(wfe.Errs, ParseError{
				Section: "multipart part transfer-encoding",
				Message: "failed to decode " + partTransferEncoding + ": " + err.Error(),
			})
			partDecoded = partData
		}

		// Content-Disposition
		disposition := ""
		filename := ""
		for _, h := range partHeaders {
			if strings.EqualFold(h.Name, "Content-Disposition") {
				dispType, dispParams, _ := mime.ParseMediaType(h.Value)
				disposition = dispType
				filename = dispParams["filename"]
				break
			}
		}

		partStruct := &BodyPart{
			Headers:           partHeaders,
			MediaType:         partMediaType,
			Params:            partParams,
			TransferEncoding:  partTransferEncoding,
			Charset:           strings.ToLower(strings.TrimSpace(partParams["charset"])),
			Disposition:       disposition,
			Filename:          filename,
		}

		if strings.HasPrefix(partMediaType, "multipart/") {
			// Recursively parse nested multipart
			subParams := partParams
			subBoundary := subParams["boundary"]
			if subBoundary != "" {
				// Create a sub-parser for nested multipart
				subWFE := &WireFormatEmail{
					ContentType: partContentType,
					Headers:     partHeaders,
				}
				// Initialize BodyStructure for the sub-parser
				subWFE.BodyStructure = &BodyPart{
					Headers:       partHeaders,
					MediaType:     partMediaType,
					Params:        subParams,
					Charset:       strings.ToLower(strings.TrimSpace(subParams["charset"])),
					Parts:         []*BodyPart{},
					Boundary:      subBoundary,
				}
				subWFE.parseMultipart(partDecoded, partMediaType, subParams)
				if subWFE.BodyStructure != nil {
					partStruct.Parts = subWFE.BodyStructure.Parts
					partStruct.Boundary = subBoundary
				} else {
					partStruct.Body = partDecoded
				}
			} else {
				partStruct.Body = partDecoded
			}
		} else {
			partStruct.Body = partDecoded
		}

		wfe.BodyStructure.Parts = append(wfe.BodyStructure.Parts, partStruct)
	}
}

// decodeTransferEncoding decodes the given transfer encoding.
func (wfe *WireFormatEmail) decodeTransferEncoding(encoding string, data []byte) ([]byte, error) {
	switch strings.ToLower(strings.TrimSpace(encoding)) {
	case "base64":
		decoded := make([]byte, base64.StdEncoding.DecodedLen(len(data)))
		n, err := base64.StdEncoding.Decode(decoded, data)
		return decoded[:n], err
	case "quoted-printable":
		return io.ReadAll(quotedprintable.NewReader(bytes.NewReader(data)))
	case "7bit", "8bit", "binary", "":
		return data, nil
	default:
		return data, fmt.Errorf("unknown transfer encoding: %s", encoding)
	}
}

// parseSingleAddress parses a single email address.
func parseSingleAddress(value string) Address {
	value = decodeHeader(value)
	if value == "" {
		return Address{}
	}
	parsed, err := mail.ParseAddress(value)
	if err != nil {
		return Address{Address: strings.TrimSpace(value)}
	}
	return Address{Name: parsed.Name, Address: parsed.Address}
}

// parseMessageIDList parses a list of message IDs (for In-Reply-To and References).
func parseMessageIDList(value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	// Message IDs are space-separated or comma-separated, enclosed in <>
	var result []string
	for _, token := range strings.FieldsFunc(value, func(r rune) bool {
		return r == ' ' || r == '\t' || r == ',' || r == '\n' || r == '\r'
	}) {
		token = strings.TrimSpace(token)
		if token != "" {
			result = append(result, token)
		}
	}
	return result
}

// decodeHeader decodes MIME encoded-words (RFC 2047).
func decodeHeader(value string) string {
	if value == "" {
		return ""
	}
	decoder := &mime.WordDecoder{CharsetReader: charsetReader}
	decoded, err := decoder.DecodeHeader(value)
	if err != nil {
		return value
	}
	return decoded
}

// charsetReader provides charset conversion for MIME encoded-words.
func charsetReader(charset string, input io.Reader) (io.Reader, error) {
	switch strings.ToLower(strings.TrimSpace(charset)) {
	case "iso-8859-1", "latin1", "latin-1":
		data, err := io.ReadAll(input)
		if err != nil {
			return nil, err
		}
		return strings.NewReader(latin1String(data)), nil
	default:
		return input, nil
	}
}

func latin1String(data []byte) string {
	var builder strings.Builder
	builder.Grow(len(data))
	for _, b := range data {
		builder.WriteRune(rune(b))
	}
	return builder.String()
}

// parseDate parses an RFC 5322 date string.
func parseDate(value string) time.Time {
	if value == "" {
		return time.Time{}
	}
	if parsed, err := mail.ParseDate(value); err == nil {
		return parsed
	}
	for _, layout := range []string{time.RFC1123Z, time.RFC1123, time.RFC822Z, time.RFC822, time.RFC3339} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed
		}
	}
	return time.Time{}
}

// ToUserEmail converts the wire format email to a UserEmail (simplified view
// for the web client and storage layer). This is a single-call conversion.
//
// The conversion extracts the text and HTML bodies from the MIME structure,
// preferring text/plain and text/html parts. Non-text parts are ignored.
func (wfe *WireFormatEmail) ToUserEmail(id string) *UserEmail {
	text, html := wfe.extractTextAndHTML()

	return &UserEmail{
		ID:      id,
		From:    addressPtrToValue(wfe.From),
		To:      wfe.To,
		Cc:      wfe.Cc,
		Bcc:     wfe.Bcc,
		Subject: wfe.Subject,
		Date:    wfe.Date,
		Text:    strings.TrimSpace(text),
		HTML:    strings.TrimSpace(html),
	}
}

func addressPtrToValue(addr *Address) Address {
	if addr == nil {
		return Address{}
	}
	return *addr
}

// extractTextAndHTML walks the MIME tree and extracts text/plain and text/html bodies.
func (wfe *WireFormatEmail) extractTextAndHTML() (text, html string) {
	if wfe.BodyStructure == nil {
		// No MIME structure: treat raw body as text
		if wfe.RawBody != nil {
			return decodeCharset(wfe.RawBody, "us-ascii"), ""
		}
		return "", ""
	}

	var textParts, htmlParts []string
	wfe.walkBodyStructure(wfe.BodyStructure, &textParts, &htmlParts)

	return strings.Join(textParts, "\n\n"), strings.Join(htmlParts, "\n")
}

func (wfe *WireFormatEmail) walkBodyStructure(part *BodyPart, textParts, htmlParts *[]string) {
	if part == nil {
		return
	}

	if strings.HasPrefix(part.MediaType, "multipart/") {
		for _, subPart := range part.Parts {
			wfe.walkBodyStructure(subPart, textParts, htmlParts)
		}
		return
	}

	// Decode charset
	content := decodeCharset(part.Body, part.Charset)

	switch part.MediaType {
	case "text/plain":
		*textParts = append(*textParts, content)
	case "text/html":
		*htmlParts = append(*htmlParts, content)
	}
}

// decodeCharset converts the given bytes from the specified charset to UTF-8.
func decodeCharset(data []byte, charset string) string {
	switch strings.ToLower(strings.TrimSpace(charset)) {
	case "iso-8859-1", "latin1", "latin-1":
		return latin1String(data)
	default:
		return string(data)
	}
}

// ToWireFormat serializes the WireFormatEmail back to RFC 5322 format.
// This is a best-effort serialization; some information may be lost.
// For a perfect round-trip, use the original raw bytes.
func (wfe *WireFormatEmail) ToWireFormat() ([]byte, error) {
	var buf bytes.Buffer

	// Write headers
	for _, h := range wfe.Headers {
		buf.WriteString(h.Name)
		buf.WriteString(": ")
		buf.WriteString(h.Value)
		buf.WriteString("\r\n")
	}

	// Ensure blank line between headers and body
	buf.WriteString("\r\n")

	// Write body
	if wfe.BodyStructure != nil {
		bodyBytes, err := wfe.serializeBodyStructure(wfe.BodyStructure)
		if err != nil {
			return nil, err
		}
		buf.Write(bodyBytes)
	} else if wfe.RawBody != nil {
		buf.Write(wfe.RawBody)
	}

	return buf.Bytes(), nil
}

// serializeBodyStructure serializes a BodyPart tree to bytes.
func (wfe *WireFormatEmail) serializeBodyStructure(part *BodyPart) ([]byte, error) {
	if part == nil {
		return nil, nil
	}

	if strings.HasPrefix(part.MediaType, "multipart/") && len(part.Parts) > 0 {
		return wfe.serializeMultipart(part)
	}

	// Leaf part: encode body with transfer encoding
	body := part.Body
	if part.TransferEncoding != "" && part.TransferEncoding != "7bit" && part.TransferEncoding != "8bit" && part.TransferEncoding != "binary" {
		var err error
		body, err = wfe.encodeTransferEncoding(part.TransferEncoding, body)
		if err != nil {
			return nil, err
		}
	}

	return body, nil
}

func (wfe *WireFormatEmail) serializeMultipart(part *BodyPart) ([]byte, error) {
	if part.Boundary == "" {
		// Generate a boundary if missing
		part.Boundary = fmt.Sprintf("boundary_%d", time.Now().UnixNano())
	}

	var buf bytes.Buffer
	for _, subPart := range part.Parts {
		buf.WriteString("--")
		buf.WriteString(part.Boundary)
		buf.WriteString("\r\n")

		// Write sub-part headers
		for _, h := range subPart.Headers {
			buf.WriteString(h.Name)
			buf.WriteString(": ")
			buf.WriteString(h.Value)
			buf.WriteString("\r\n")
		}
		buf.WriteString("\r\n")

		// Write sub-part body
		subBody, err := wfe.serializeBodyStructure(subPart)
		if err != nil {
			return nil, err
		}
		buf.Write(subBody)
		buf.WriteString("\r\n")
	}

	// Closing boundary
	buf.WriteString("--")
	buf.WriteString(part.Boundary)
	buf.WriteString("--\r\n")

	return buf.Bytes(), nil
}

// encodeTransferEncoding encodes the given data with the specified transfer encoding.
func (wfe *WireFormatEmail) encodeTransferEncoding(encoding string, data []byte) ([]byte, error) {
	switch strings.ToLower(strings.TrimSpace(encoding)) {
	case "base64":
		encoded := make([]byte, base64.StdEncoding.EncodedLen(len(data)))
		base64.StdEncoding.Encode(encoded, data)
		return encoded, nil
	case "quoted-printable":
		var buf bytes.Buffer
		writer := quotedprintable.NewWriter(&buf)
		if _, err := writer.Write(data); err != nil {
			return nil, err
		}
		if err := writer.Close(); err != nil {
			return nil, err
		}
		return buf.Bytes(), nil
	case "7bit", "8bit", "binary", "":
		return data, nil
	default:
		return data, fmt.Errorf("unknown transfer encoding: %s", encoding)
	}
}

// HasErrors returns true if any parse errors were recorded.
func (wfe *WireFormatEmail) HasErrors() bool {
	return len(wfe.Errs) > 0
}

// ErrorSummary returns a human-readable summary of all parse errors.
func (wfe *WireFormatEmail) ErrorSummary() string {
	if len(wfe.Errs) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("%d parse error(s):\n", len(wfe.Errs)))
	for i, err := range wfe.Errs {
		sb.WriteString(fmt.Sprintf("  %d. [%s] %s\n", i+1, err.Section, err.Message))
	}
	return sb.String()
}