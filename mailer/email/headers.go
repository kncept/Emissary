package email

import (
	"fmt"
	"net/mail"
	"strings"
	"time"
)

// HeaderPolicy controls the few places where the header renderer for stored
// messages and the wire sender differ. Both use the same field order, address
// formatting and CRLF sanitization.
type HeaderPolicy struct {
	// EmptyToFallback replaces an empty To header. Wire senders use
	// "undisclosed-recipients:;"; storage rendering leaves To empty.
	EmptyToFallback string

	// GenerateDate omits a zero Date when false (stored messages) and stamps
	// the current UTC time when true (wire messages).
	GenerateDate bool

	// GeneratedMessageIDDomain makes RenderHeaders generate a Message-ID when
	// the message has none. An empty domain means no generated Message-ID.
	GeneratedMessageIDDomain string
}

// FormatAddress renders a single address for an RFC 5322 header, including a
// display name when present. Empty addresses render as an empty string.
func FormatAddress(address Address) string {
	value := strings.TrimSpace(address.Address)
	if value == "" {
		return ""
	}
	if strings.TrimSpace(address.Name) == "" {
		return value
	}
	return (&mail.Address{Name: address.Name, Address: value}).String()
}

// FormatAddressList renders addresses as a comma-separated header value,
// skipping empty addresses.
func FormatAddressList(addresses []Address) string {
	values := make([]string, 0, len(addresses))
	for _, address := range addresses {
		if value := FormatAddress(address); value != "" {
			values = append(values, value)
		}
	}
	return strings.Join(values, ", ")
}

// BareAddresses returns the address portion of each address, for envelope
// fields that must not include display names.
func BareAddresses(addresses []Address) []string {
	values := make([]string, 0, len(addresses))
	for _, address := range addresses {
		if value := strings.TrimSpace(address.Address); value != "" {
			values = append(values, value)
		}
	}
	return values
}

// RenderHeaders writes the common RFC 5322 header block for e. Bcc is never
// written: it belongs only in the delivery envelope. The returned string ends
// with CRLF after the last header; callers add the blank line before the body.
func RenderHeaders(e *UserEmail, policy HeaderPolicy) string {
	if e == nil {
		return ""
	}

	var b strings.Builder
	write := func(name, value string) {
		if value == "" {
			return
		}
		fmt.Fprintf(&b, "%s: %s\r\n", name, sanitizeHeaderValue(value))
	}

	write("From", FormatAddress(e.From))
	to := FormatAddressList(e.To)
	if to == "" {
		to = policy.EmptyToFallback
	}
	write("To", to)
	if cc := FormatAddressList(e.Cc); cc != "" {
		write("Cc", cc)
	}
	if replyTo := FormatAddressList(e.ReplyTo); replyTo != "" {
		write("Reply-To", replyTo)
	}
	write("Subject", e.Subject)

	date := e.Date
	switch {
	case !date.IsZero():
		write("Date", date.UTC().Format(time.RFC1123Z))
	case policy.GenerateDate:
		write("Date", time.Now().UTC().Format(time.RFC1123Z))
	}

	messageID := strings.TrimSpace(e.MessageID)
	if messageID == "" && policy.GeneratedMessageIDDomain != "" {
		messageID = GenerateMessageID(policy.GeneratedMessageIDDomain)
	}
	write("Message-ID", messageID)
	if len(e.InReplyTo) > 0 {
		write("In-Reply-To", strings.Join(e.InReplyTo, " "))
	}
	if len(e.References) > 0 {
		write("References", strings.Join(e.References, " "))
	}
	// RFC 3834 section 3.1.7: automatic responses carry Auto-Submitted so
	// receiving responders can suppress their own reply and break loops.
	write("Auto-Submitted", e.AutoSubmitted)
	return b.String()
}

func sanitizeHeaderValue(value string) string {
	value = strings.ReplaceAll(value, "\r", " ")
	return strings.ReplaceAll(value, "\n", " ")
}
