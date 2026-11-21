package receiver

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/kncept/Emissary/mailer/components/usermanager"
	"github.com/kncept/Emissary/mailer/email"
)

// defaultVacationSubject is used when the account has not configured a
// subject. RFC 3834 section 3.1.5 asks that automatic responses be identifiable
// as such; the "Auto: " prefix is the conventional marker.
const defaultVacationSubject = "Auto: Automatic reply"

// defaultVacationText is the RFC 8621 section 8 fallback body used when both
// textBody and htmlBody are null. It deliberately reveals nothing about the
// recipient's schedule beyond "may not read messages promptly".
const defaultVacationText = "This is an automatic reply.\n\n" +
	"Your message has been received, but it may not be read promptly.\n"

// maybeSendVacationReply sends an RFC 3834-compliant automatic reply for one
// delivered message. Failures are logged rather than returned: delivery has
// already succeeded, and retrying the whole message would deliver it twice.
func (r *Receiver) maybeSendVacationReply(ctx context.Context, recipient string, parsed *email.UserEmail, wire *email.WireFormatEmail) {
	if parsed == nil {
		return
	}
	store := r.cfg.VacationStore
	vacation, err := store.GetVacationResponse(ctx, recipient)
	if err != nil {
		slog.Warn("Receiver: could not read vacation response", "recipient", recipient, "err", err)
		return
	}
	now := time.Now().UTC()
	if !vacation.ActiveAt(now) {
		return
	}

	destination := vacationReplyDestination(parsed, wire)
	if destination == "" {
		return
	}
	if !vacationReplyAllowed(parsed, wire, recipient, destination) {
		slog.Debug("Receiver: suppressing vacation reply", "recipient", recipient, "from", parsed.From.Address)
		return
	}
	if vacation.RecentlyResponded(destination, now, usermanager.VacationResponseInterval) {
		slog.Debug("Receiver: vacation reply already sent to sender", "recipient", recipient, "sender", destination)
		return
	}

	// Record before sending so a re-entrant delivery cannot emit a second
	// reply while this one is in flight. If sending fails, the sender simply
	// does not receive a reply until the interval has elapsed.
	vacation.MarkResponded(destination, now)
	if err := store.SaveVacationResponse(ctx, recipient, vacation); err != nil {
		slog.Warn("Receiver: could not save vacation reply state", "recipient", recipient, "err", err)
		return
	}

	reply := buildVacationReply(vacation, recipient, destination, parsed, wire)
	if err := r.cfg.Sender.Send(ctx, reply); err != nil {
		slog.Warn("Receiver: failed to send vacation reply", "recipient", recipient, "to", destination, "err", err)
		return
	}
	slog.Info("Receiver: sent vacation reply", "recipient", recipient, "to", destination)
}

// vacationReplyAllowed implements the RFC 3834 section 2 suppression rules.
func vacationReplyAllowed(parsed *email.UserEmail, wire *email.WireFormatEmail, recipient, destination string) bool {
	if parsed == nil || wire == nil {
		return false
	}

	// Never respond to an automatic message. RFC 3834 section 2: any
	// Auto-Submitted value other than "no" suppresses a reply.
	if value := strings.ToLower(strings.TrimSpace(wire.HeaderValue("Auto-Submitted"))); value != "" && value != "no" {
		return false
	}

	// Mailing-list and bulk traffic is not personal correspondence.
	if value := strings.ToLower(wire.HeaderValue("Precedence")); value != "" {
		for _, marker := range []string{"bulk", "list", "auto", "junk"} {
			if strings.Contains(value, marker) {
				return false
			}
		}
	}
	for _, name := range []string{
		"List-Id", "List-Unsubscribe", "List-Help", "List-Post", "List-Owner",
		"List-Subscribe", "List-Archive",
	} {
		if wire.HasHeader(name) {
			return false
		}
	}

	returnPath := strings.TrimSpace(wire.HeaderValue("Return-Path"))
	if strings.Contains(returnPath, "<>") || strings.EqualFold(returnPath, "<>") {
		return false
	}

	// addresses commonly used by automated systems and daemons.
	if isAutomatedAddress(parsed.From.Address) {
		return false
	}
	if sender := wire.HeaderValue("Sender"); sender != "" && isAutomatedAddress(sender) {
		return false
	}
	if returnPath != "" && isAutomatedAddress(headerAddress(returnPath)) {
		return false
	}
	if isAutomatedAddress(destination) {
		return false
	}

	// A personal vacation reply must not be sent to the vacationing account.
	if usermanager.NormalizeVacationSender(destination) == usermanager.NormalizeVacationSender(recipient) {
		return false
	}

	// RFC 3834 section 2: only reply when the recipient was explicitly named
	// in a recipient header. If the message carries no recipient headers at
	// all, fall back to the envelope recipient (Bcc-style delivery).
	headerRecipients := wireRecipientAddresses(wire)
	if len(headerRecipients) > 0 {
		target := usermanager.NormalizeVacationSender(recipient)
		for _, address := range headerRecipients {
			if usermanager.NormalizeVacationSender(address) == target {
				return true
			}
		}
		return false
	}
	return true
}

// vacationReplyDestination picks the auto-reply destination per RFC 3834
// section 4: Return-Path first, then Sender, then From. An empty or null
// Return-Path suppresses the reply entirely.
func vacationReplyDestination(parsed *email.UserEmail, wire *email.WireFormatEmail) string {
	if wire != nil {
		if value := headerAddress(wire.HeaderValue("Return-Path")); value != "" {
			if strings.Contains(value, "<>") {
				return ""
			}
			return value
		}
		if sender := headerAddress(wire.HeaderValue("Sender")); sender != "" {
			return sender
		}
	}
	if parsed != nil {
		return strings.TrimSpace(parsed.From.Address)
	}
	return ""
}

// headerAddress extracts the bare address from a header value such as
// "<sender@example.com>" or "\"Name\" <sender@example.com>".
func headerAddress(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || strings.Contains(value, "<>") {
		return ""
	}
	if start := strings.IndexByte(value, '<'); start >= 0 {
		if end := strings.IndexByte(value[start:], '>'); end > 1 {
			return strings.TrimSpace(value[start+1 : start+end])
		}
		return ""
	}
	return value
}

func wireRecipientAddresses(wire *email.WireFormatEmail) []string {
	var addresses []string
	for _, list := range [][]email.Address{wire.To, wire.Cc, wire.Bcc} {
		for _, address := range list {
			if address.Address != "" {
				addresses = append(addresses, address.Address)
			}
		}
	}
	for _, name := range []string{"Resent-To", "Resent-Cc", "Resent-Bcc"} {
		if value := headerAddress(wire.HeaderValue(name)); value != "" {
			addresses = append(addresses, value)
		}
	}
	return addresses
}

func isAutomatedAddress(address string) bool {
	local := strings.ToLower(strings.SplitN(strings.TrimSpace(address), "@", 2)[0])
	if local == "" {
		return false
	}
	switch local {
	case "mailer-daemon", "postmaster", "noreply", "no-reply", "donotreply", "do-not-reply", "bounce", "bounces":
		return true
	}
	return strings.HasPrefix(local, "owner-") ||
		strings.HasSuffix(local, "-request") ||
		strings.HasSuffix(local, "-bounce") ||
		strings.HasPrefix(local, "bounce-")
}

func buildVacationReply(vacation usermanager.VacationResponse, recipient, destination string, original *email.UserEmail, wire *email.WireFormatEmail) *email.UserEmail {
	subject := defaultVacationSubject
	if vacation.Subject != nil && strings.TrimSpace(*vacation.Subject) != "" {
		subject = strings.TrimSpace(*vacation.Subject)
	} else if original.Subject != "" {
		subject = "Auto: " + original.Subject
	}

	reply := &email.UserEmail{
		From:          email.Address{Address: recipient},
		To:            []email.Address{{Address: destination}},
		Subject:       subject,
		Date:          time.Now().UTC(),
		AutoSubmitted: "auto-replied",
	}
	if vacation.TextBody != nil && *vacation.TextBody != "" {
		reply.Text = *vacation.TextBody
	} else if vacation.HTMLBody == nil || *vacation.HTMLBody == "" {
		reply.Text = defaultVacationText
	}
	if vacation.HTMLBody != nil && *vacation.HTMLBody != "" {
		reply.HTML = *vacation.HTMLBody
	}

	if wire != nil && strings.TrimSpace(wire.MessageID) != "" {
		reply.InReplyTo = []string{wire.MessageID}
		reply.References = []string{wire.MessageID}
	}
	return reply
}
