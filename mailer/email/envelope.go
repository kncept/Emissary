package email

import (
	"regexp"
	"strings"
)

// envelopeForPattern matches the "for <address>" clause an MTA adds to a
// Received header for each SMTP envelope recipient (RCPT TO). The angle
// brackets are optional because not every MTA includes them.
var envelopeForPattern = regexp.MustCompile(`(?i)\bfor\s+<?([^\s;>,]+@[^\s;>,]+)>?`)

// EnvelopeRecipients returns the SMTP envelope recipients recorded in the
// message's topmost Received header. SES adds a Received header with a "for"
// clause for every RCPT TO when it writes the raw message to S3, so this is the
// authoritative recipient list for inbound routing. The To/Cc/Bcc headers are
// supplied by the sender and may omit Bcc recipients or name aliases that
// differ from the address the message was actually accepted for.
//
// The first Received header with a usable "for" clause wins; headers added by
// earlier hops are ignored. Addresses are trimmed, lower-cased and deduplicated.
func EnvelopeRecipients(wfe *WireFormatEmail) []string {
	if wfe == nil {
		return nil
	}
	for _, h := range wfe.Headers {
		if !strings.EqualFold(h.Name, "Received") {
			continue
		}
		if recipients := envelopeRecipientsFromReceived(h.Value); len(recipients) > 0 {
			return recipients
		}
	}
	return nil
}

func envelopeRecipientsFromReceived(value string) []string {
	seen := make(map[string]bool)
	var recipients []string
	for _, match := range envelopeForPattern.FindAllStringSubmatch(value, -1) {
		address := strings.ToLower(strings.TrimSpace(match[1]))
		if address == "" || seen[address] {
			continue
		}
		seen[address] = true
		recipients = append(recipients, address)
	}
	return recipients
}
