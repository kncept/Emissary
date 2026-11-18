package email

import (
	"bytes"
	"encoding/base64"
	"io"
	"net/mail"
	"strings"
	"time"
)

const maxPartSize = 10 << 20 // 10 MiB per MIME part

// MailboxKeyRoot is the S3 key prefix that holds per-user mailboxes.
const MailboxKeyRoot = "users/"

// DeliveredTimestampFormat is the ISO 8601 basic ("compressed") layout used in
// delivered email object keys: YYYYMMDDTHHMMSS in UTC, e.g. 20240308T143000.
const DeliveredTimestampFormat = "20060102T150405"

// FormatDeliveredTimestamp formats t as an ISO 8601 basic UTC timestamp for use
// in a delivered email object key (e.g. 20240308T143000).
func FormatDeliveredTimestamp(t time.Time) string {
	return t.UTC().Format(DeliveredTimestampFormat)
}

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
	ID         string    `json:"id"`
	AccountID  string    `json:"accountId,omitempty"`
	MailboxID  string    `json:"mailboxId,omitempty"`
	From       Address   `json:"from"`
	ReplyTo    []Address `json:"replyTo,omitempty"`
	To         []Address `json:"to"`
	Cc         []Address `json:"cc,omitempty"`
	Bcc        []Address `json:"bcc,omitempty"`
	Subject    string    `json:"subject"`
	Date       time.Time `json:"date"`
	Text       string    `json:"text"`
	HTML       string    `json:"html,omitempty"`
	Read       bool      `json:"read"`
	Size       int64     `json:"size,omitempty"`
	InReplyTo  []string  `json:"inReplyTo,omitempty"`
	References []string  `json:"references,omitempty"`
}

// MailboxKeyPrefix returns the object-key prefix that holds the messages in a
// JMAP account's mailbox. The layout is:
//
//	users/<accountId>/<mailboxId>/<timestamp>_<ses-message-id>.eml
//
// An empty mailboxID returns the prefix for every mailbox in the account.
// Callers must supply a non-empty accountID; an empty one never produces a
// prefix that could accidentally match the whole bucket.
func MailboxKeyPrefix(accountID, mailboxID string) string {
	accountID = strings.ToLower(strings.TrimSpace(accountID))
	if accountID == "" {
		return ""
	}
	prefix := MailboxKeyRoot + accountID + "/"
	if mailboxID = strings.ToLower(strings.TrimSpace(mailboxID)); mailboxID != "" {
		prefix += mailboxID + "/"
	}
	return prefix
}

// IsDeliveredMessageKey reports whether key has the layout of a delivered
// message: a per-user mailbox subfolder and a .eml suffix.
//
//	users/<accountId>/<mailboxId>/<file>.eml
//
// System records such as users/<accountId>/user.json, directory markers and
// raw SES staging objects at the bucket root do not match. Storage listings
// use this to avoid returning non-message objects as mail.
func IsDeliveredMessageKey(key string) bool {
	if !strings.HasSuffix(strings.ToLower(key), ".eml") {
		return false
	}
	_, _, ok := MailboxFromID(key)
	return ok
}

// MailboxFromID extracts the account and mailbox from a delivered message ID.
// A delivered ID is the full object key of the .eml file (including the
// timestamp), so it also identifies which account's mailbox the message lives
// in. Keys at the bucket root (SES staging objects) have no mailbox and return
// ok == false.
func MailboxFromID(id string) (accountID, mailboxID string, ok bool) {
	id = strings.TrimPrefix(id, "/")

	// Tolerate an optional storage-level prefix before "users/" (e.g.
	// "emails/users/alice@example.com/inbox/...").
	if idx := strings.LastIndex(id, "/"+MailboxKeyRoot); idx >= 0 {
		id = id[idx+1:]
	}
	if !strings.HasPrefix(id, MailboxKeyRoot) {
		return "", "", false
	}

	parts := strings.SplitN(strings.TrimPrefix(id, MailboxKeyRoot), "/", 3)
	if len(parts) < 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return "", "", false
	}
	return strings.ToLower(parts[0]), strings.ToLower(parts[1]), true
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

// RawFromUserEmail builds a minimal RFC 5322 message from a parsed email. It
// is intended for stores that do not retain the original delivered bytes (for
// example the in-memory development store). It is a best-effort rendering, not
// a byte-exact copy of the original wire format.
func RawFromUserEmail(e *UserEmail) []byte {
	if e == nil {
		return nil
	}

	var buf bytes.Buffer
	buf.WriteString("From: ")
	buf.WriteString(addressString(e.From))
	buf.WriteString("\r\nTo: ")
	recipients := make([]string, 0, len(e.To))
	for _, addr := range e.To {
		recipients = append(recipients, addressString(addr))
	}
	buf.WriteString(strings.Join(recipients, ", "))
	buf.WriteString("\r\nSubject: ")
	buf.WriteString(e.Subject)
	if !e.Date.IsZero() {
		buf.WriteString("\r\nDate: ")
		buf.WriteString(e.Date.UTC().Format(time.RFC1123Z))
	}
	buf.WriteString("\r\n\r\n")
	buf.WriteString(e.Text)
	if !strings.HasSuffix(e.Text, "\r\n") {
		buf.WriteString("\r\n")
	}
	return buf.Bytes()
}

func addressString(a Address) string {
	if strings.TrimSpace(a.Name) == "" {
		return a.Address
	}
	return (&mail.Address{Name: a.Name, Address: a.Address}).String()
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
