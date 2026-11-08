package email

import (
	"encoding/base64"
	"io"
	"net/mail"
	"strings"
	"time"
)

const maxPartSize = 10 << 20 // 10 MiB per MIME part

// Address is a mail address with an optional display name.
type Address struct {
	Name    string `json:"name,omitempty"`
	Address string `json:"address"`
}

// UserEmail is the simplified email representation used by the storage, sender,
// metadata and HTTP layers. It contains only the fields needed for display
// and search, extracted from the full wire format.
//
// To parse a raw RFC 5322 message into a UserEmail, use ParseWireFormat()
// followed by WireFormatEmail.ToUserEmail(), or use the convenience function Parse().
type UserEmail struct {
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

// Parse reads a raw RFC 5322 message and returns a UserEmail.
// This is a convenience function that combines ParseWireFormat and ToUserEmail.
// Attachments and other non-text parts are intentionally ignored.
func Parse(id string, r io.Reader) (*UserEmail, error) {
	wfe := ParseWireFormat(id, r)
	return wfe.ToUserEmail(id), nil
}

// ParseWireFormat parses a raw RFC 5322 message into a WireFormatEmail,
// preserving the complete message structure. All parse errors are collected
// in the returned WireFormatEmail.Errs slice; the function never returns an error.
//
// This is the primary parsing function for applications that need full access
// to the wire format (all headers, MIME structure, encodings, etc.).
func ParseWireFormat(id string, r io.Reader) *WireFormatEmail {
	return parseWireFormatBytes(id, readAllOrEmpty(r))
}

func readAllOrEmpty(r io.Reader) []byte {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil
	}
	return data
}

// parseAddress parses a single address from a header value.
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

// parseAddressList parses a comma-separated list of addresses.
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
	for _, addr := range parsed {
		addresses = append(addresses, Address{Name: addr.Name, Address: addr.Address})
	}
	return addresses
}