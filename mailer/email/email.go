package email

import (
	"encoding/base64"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"net/textproto"
	"strings"
	"time"
)

const maxPartSize = 10 << 20 // 10 MiB per MIME part

// Address is a mail address with an optional display name.
type Address struct {
	Name    string `json:"name,omitempty"`
	Address string `json:"address"`
}

// Email is the representation shared by the storage, sender, metadata and HTTP
// layers. Incoming messages are parsed from their raw MIME form; outgoing
// messages are built from the compose form in the web client.
type Email struct {
	ID      string    `json:"id"`
	From    Address   `json:"from"`
	To      []Address `json:"to"`
	Cc      []Address `json:"cc,omitempty"`
	Bcc     []Address `json:"bcc,omitempty"`
	Subject string    `json:"subject"`
	Date    time.Time `json:"date"`
	Text    string    `json:"text"`
	HTML    string    `json:"html,omitempty"`
	Read    bool      `json:"read"`
	Size    int64     `json:"size,omitempty"`
}

// EncodeID turns a storage key into an opaque, URL-safe identifier.
func EncodeID(key string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(key))
}

// DecodeID reverses EncodeID.
func DecodeID(id string) (string, error) {
	decoded, err := base64.RawURLEncoding.DecodeString(id)
	if err != nil {
		return "", err
	}
	return string(decoded), nil
}

// Parse reads a raw RFC 5322 message and returns the parts the web client can
// display. Attachments and other non-text parts are intentionally ignored.
func Parse(id string, r io.Reader) (*Email, error) {
	msg, err := mail.ReadMessage(r)
	if err != nil {
		return nil, err
	}

	text, html := parsePart(textproto.MIMEHeader(msg.Header), msg.Body, 0)

	return &Email{
		ID:      id,
		From:    parseAddress(msg.Header.Get("From")),
		To:      parseAddressList(msg.Header.Get("To")),
		Cc:      parseAddressList(msg.Header.Get("Cc")),
		Bcc:     parseAddressList(msg.Header.Get("Bcc")),
		Subject: decodeHeader(msg.Header.Get("Subject")),
		Date:    parseDate(msg.Header.Get("Date")),
		Text:    strings.TrimSpace(text),
		HTML:    strings.TrimSpace(html),
	}, nil
}

func parsePart(header textproto.MIMEHeader, body io.Reader, depth int) (text, html string) {
	if depth > 10 {
		return "", ""
	}

	mediaType, params, err := mime.ParseMediaType(header.Get("Content-Type"))
	if err != nil || mediaType == "" {
		mediaType = "text/plain"
	}

	reader, err := decodeTransferEncoding(header.Get("Content-Transfer-Encoding"), body)
	if err != nil {
		return "", ""
	}

	if strings.HasPrefix(mediaType, "multipart/") {
		boundary := params["boundary"]
		if boundary == "" {
			return "", ""
		}

		mr := multipart.NewReader(reader, boundary)
		var textParts, htmlParts []string
		for {
			part, err := mr.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				break
			}
			partText, partHTML := parsePart(part.Header, part, depth+1)
			part.Close()
			if partText != "" {
				textParts = append(textParts, partText)
			}
			if partHTML != "" {
				htmlParts = append(htmlParts, partHTML)
			}
		}
		return strings.Join(textParts, "\n\n"), strings.Join(htmlParts, "\n")
	}

	if mediaType != "text/plain" && mediaType != "text/html" {
		return "", ""
	}

	data, err := io.ReadAll(io.LimitReader(reader, maxPartSize))
	if err != nil {
		return "", ""
	}
	content := decodeCharset(data, params["charset"])

	if mediaType == "text/html" {
		return "", content
	}
	return content, ""
}

func decodeTransferEncoding(encoding string, body io.Reader) (io.Reader, error) {
	switch strings.ToLower(strings.TrimSpace(encoding)) {
	case "base64":
		return base64.NewDecoder(base64.StdEncoding, body), nil
	case "quoted-printable":
		return quotedprintable.NewReader(body), nil
	default:
		return body, nil
	}
}

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

func decodeCharset(data []byte, charset string) string {
	switch strings.ToLower(strings.TrimSpace(charset)) {
	case "iso-8859-1", "latin1", "latin-1":
		return latin1String(data)
	default:
		return string(data)
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

func parseAddress(value string) Address {
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

func parseAddressList(value string) []Address {
	value = decodeHeader(value)
	if value == "" {
		return nil
	}
	parsed, err := mail.ParseAddressList(value)
	if err != nil {
		return []Address{{Address: strings.TrimSpace(value)}}
	}
	addresses := make([]Address, 0, len(parsed))
	for _, address := range parsed {
		addresses = append(addresses, Address{Name: address.Name, Address: address.Address})
	}
	return addresses
}
