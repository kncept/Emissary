package email

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/mail"
	"strings"
	"sync/atomic"
	"time"

	"github.com/kncept/Emissary/mailer/readlimit"
)

// DefaultMaxPartSize is the default cap for a whole MIME message and for an
// individual MIME part (10 MiB). SetMaxPartSize overrides it at startup.
const DefaultMaxPartSize int64 = 10 << 20

// maxAllowedPartSize leaves room for the extra lookahead byte used to detect
// oversized readers without integer overflow.
const maxAllowedPartSize int64 = 1 << 40

var maxPartSize atomic.Int64

func init() {
	maxPartSize.Store(DefaultMaxPartSize)
}

// SetMaxPartSize sets the limit used by the MIME parser and by callers that
// read untrusted message bodies. Non-positive values are ignored so a bad
// configuration cannot remove the limit.
func SetMaxPartSize(limit int64) {
	if limit > 0 && limit < maxAllowedPartSize {
		maxPartSize.Store(limit)
	}
}

// MaxPartSize returns the configured MIME/message size limit in bytes.
func MaxPartSize() int64 {
	return maxPartSize.Load()
}

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

// EmailRef identifies a stored message: its storage id and the JMAP
// account/mailbox that own it. These are metadata about where a message
// lives, separate from the message content held by UserEmail.
type EmailRef struct {
	ID        string `json:"id"`
	AccountID string `json:"accountId,omitempty"`
	MailboxID string `json:"mailboxId,omitempty"`
}

// ListedEmail is one entry returned by a storage listing: the ref (ids) the
// message is stored under together with its content.
type ListedEmail struct {
	Ref   *EmailRef
	Email *UserEmail
}

// UserEmail is the simplified email content used by the storage, sender,
// metadata and HTTP layers. It contains only the fields needed for display
// and search, extracted from the full wire format. Where a message lives is
// described by EmailRef instead.
//
// To parse a raw RFC 5322 message into a UserEmail, use ParseWireFormat()
// followed by WireFormatEmail.ToUserEmail(), or use the convenience function Parse().
type UserEmail struct {
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
	MessageID  string    `json:"messageId,omitempty"`
	InReplyTo  []string  `json:"inReplyTo,omitempty"`
	References []string  `json:"references,omitempty"`

	// AutoSubmitted is the RFC 3834 Auto-Submitted header value. "auto-replied"
	// marks an automatic response so other responders can avoid mail loops.
	AutoSubmitted string `json:"autoSubmitted,omitempty"`
}

// GenerateMessageID returns a globally unique RFC 5322 Message-ID value of the
// form <token@domain>. An empty domain renders as "emissary.local".
func GenerateMessageID(domain string) string {
	domain = strings.ToLower(strings.TrimSpace(domain))
	if domain == "" {
		domain = "emissary.local"
	}
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("%d@%s", time.Now().UnixNano(), domain)
	}
	return fmt.Sprintf("<%x@%s>", buf, domain)
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

// DeliveredMessageToken returns the stable message token for a delivered
// object key. The token is the filename component (including the .eml suffix)
// and stays the same when a message is moved between mailbox folders.
func DeliveredMessageToken(key string) (string, bool) {
	if !IsDeliveredMessageKey(key) {
		return "", false
	}
	key = strings.TrimPrefix(key, "/")
	parts := strings.Split(key, "/")
	if len(parts) < 4 {
		return "", false
	}
	token := parts[len(parts)-1]
	if token == "" {
		return "", false
	}
	return token, true
}

// DeliveredMessageKey builds the physical object key for a delivered message
// from its stable token.
func DeliveredMessageKey(accountID, mailboxID, token string) string {
	token = strings.Trim(strings.TrimSpace(token), "/")
	if token == "" {
		return ""
	}
	return MailboxKeyPrefix(accountID, mailboxID) + token
}

// MailboxFromID extracts the account and mailbox from a delivered message
// identifier. It accepts either a full physical object key
// (users/<accountId>/<mailboxId>/<token>.eml) or the stable token form used by
// JMAP ids for delivered mail; the latter does not encode a mailbox and
// returns ok == false. Keys at the bucket root (SES staging objects) have no
// mailbox and return ok == false.
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

// EmailRefFromID builds the metadata ref for a message from its identifier.
// For a full delivered object key it derives the account/mailbox and uses the
// stable filename token as the message ID so moves between folders do not
// change the JMAP identity. Other identifiers keep only the id itself.
func EmailRefFromID(id string) EmailRef {
	accountID, mailboxID, ok := MailboxFromID(id)
	if ok {
		if token, ok := DeliveredMessageToken(id); ok {
			return EmailRef{ID: token, AccountID: accountID, MailboxID: mailboxID}
		}
	}
	return EmailRef{ID: id, AccountID: accountID, MailboxID: mailboxID}
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
// is intended for messages that have no original wire bytes to retain (for
// example messages created through JMAP). It is a best-effort rendering, not
// a byte-exact copy of the original wire format; inbound delivery passes the
// staging bytes to Storage.AddEmail instead so they can be stored verbatim.
func RawFromUserEmail(e *UserEmail) []byte {
	if e == nil {
		return nil
	}

	var buf bytes.Buffer
	buf.WriteString(RenderHeaders(e, HeaderPolicy{}))
	buf.WriteString("\r\n")
	buf.WriteString(e.Text)
	if !strings.HasSuffix(e.Text, "\r\n") {
		buf.WriteString("\r\n")
	}
	return buf.Bytes()
}

// Parse reads a raw RFC 5322 message and returns its content as a UserEmail.
// This is a convenience function that combines ParseWireFormat and ToUserEmail.
// Attachments and other non-text parts are intentionally ignored. The id
// argument is ignored: identity is not part of the wire format. Storage
// callers pass a delivered object key for compatibility, but must derive
// account/mailbox metadata with EmailRefFromID instead of expecting the parser
// to use it.
func Parse(_ string, r io.Reader) (*UserEmail, error) {
	wfe := ParseWireFormat("", r)
	if wfe.TooLarge {
		return nil, fmt.Errorf("message exceeds maximum size of %d bytes", MaxPartSize())
	}
	return wfe.ToUserEmail(), nil
}

// ParseWireFormat parses a raw RFC 5322 message into a WireFormatEmail,
// preserving the complete message structure. All parse errors are collected
// in the returned WireFormatEmail.Errs slice; the function never returns an error.
//
// The id argument is ignored: callers may pass a storage key for context, but
// the parser never assigns identity from it. Use EmailRefFromID for that.
//
// This is the primary parsing function for applications that need full access
// to the wire format (all headers, MIME structure, encodings, etc.).
func ParseWireFormat(_ string, r io.Reader) *WireFormatEmail {
	data, err := readlimit.ReadAll(r, MaxPartSize())
	wfe := parseWireFormatBytes("", data)
	if err != nil {
		wfe.TooLarge = true
		wfe.Errs = append([]ParseError{{Section: "message", Message: err.Error()}}, wfe.Errs...)
	}
	return wfe
}

// ReadLimited reads an untrusted message body up to the configured limit.
// Callers that read wire bytes before handing them to ParseWireFormat should
// use this so the cap is enforced at the earliest possible point.
func ReadLimited(r io.Reader) ([]byte, error) {
	data, err := readlimit.ReadAll(r, MaxPartSize())
	if errors.Is(err, readlimit.ErrTooLarge) {
		return nil, fmt.Errorf("message exceeds maximum size of %d bytes", MaxPartSize())
	}
	return data, err
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
