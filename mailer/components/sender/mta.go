package sender

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/mail"
	"net/smtp"
	"sort"
	"strings"
	"time"

	"github.com/kncept/Emissary/mailer/email"
	"github.com/mjl-/mox/dmarc"
	moxdns "github.com/mjl-/mox/dns"
	"github.com/mjl-/mox/spf"
)

const (
	mtaDefaultPort    = "25"
	mtaDefaultTimeout = 10 * time.Second
)

// mxLookup and mtaDial are function types so tests can supply fake DNS and
// network implementations instead of touching the outside world.
type mxLookup func(ctx context.Context, domain string) ([]*net.MX, error)
type mtaDial func(ctx context.Context, network, address string) (net.Conn, error)

var _ Sender = (*MtaSender)(nil)

// MtaSender is a direct-delivery sender. It resolves the MX records for each
// recipient domain and speaks SMTP to the preferred mail exchanger on port 25.
// It needs no AWS credentials or SES configuration.
//
// Recipients are grouped by domain and one SMTP transaction is made per
// domain. MX hosts are tried in preference order; if every MX host fails, the
// domain itself is tried as a last resort (RFC 5321 section 5.1). The envelope
// sender and EHLO/HELO identity come from the From address of each message.
type MtaSender struct {
	port     string
	timeout  time.Duration
	lookupMX mxLookup
	dial     mtaDial
	resolver moxdns.Resolver
}

// NewMtaSender creates a direct-MX sender. The envelope sender and EHLO/HELO
// identity are derived from the From address of each message. An empty port
// means 25.
func NewMtaSender(port string) *MtaSender {
	if port == "" {
		port = mtaDefaultPort
	}
	resolver := net.DefaultResolver
	return &MtaSender{
		port:     port,
		timeout:  mtaDefaultTimeout,
		lookupMX: resolver.LookupMX,
		dial:     (&net.Dialer{Timeout: mtaDefaultTimeout}).DialContext,
		resolver: moxdns.StrictResolver{},
	}
}

func (s *MtaSender) Name() string {
	return "MtaSender"
}

func (s *MtaSender) Send(ctx context.Context, message *email.UserEmail) error {
	envelopeFrom := message.From.Address
	if envelopeFrom == "" {
		return errors.New("message has no From address")
	}
	slog.Debug("mta: sending message", "from", envelopeFrom, "subject", message.Subject)
	s.checkDMARC(ctx, envelopeFrom, envelopeFrom)
	return s.deliver(ctx, envelopeFrom, helloNameFromAddress(envelopeFrom), message)
}

// checkDMARC is a sender-side pre-flight check using mox's DMARC
// implementation. The DMARC record for the From domain is looked up and the
// envelope sender domain is treated as the SPF-authenticated identity: that is
// the identity a receiving MTA would evaluate against SPF. SPF IP matching and
// DKIM signing are outside this sender's control (we don't know our outbound
// public IP and don't sign messages), so this checks identifier alignment and
// the published policy only. A failure is logged as a warning; delivery is not
// blocked.
func (s *MtaSender) checkDMARC(ctx context.Context, envelopeFrom, fromAddress string) {
	fromDomain := domainOfAddress(fromAddress)
	spfDomain := domainOfAddress(envelopeFrom)
	if fromDomain == "" || spfDomain == "" {
		return
	}

	from, err := moxdns.ParseDomain(fromDomain)
	if err != nil {
		slog.Warn("mta: DMARC warning: invalid From domain", "from_domain", fromDomain, "err", err)
		return
	}
	spfIdentity, err := moxdns.ParseDomain(spfDomain)
	if err != nil {
		slog.Warn("mta: DMARC warning: invalid SPF domain", "spf_domain", spfDomain, "err", err)
		return
	}

	spfFound, spfErr := s.spfRecordExists(ctx, spfDomain)
	spfResult := spf.StatusFail
	if spfFound {
		spfResult = spf.StatusPass
	}

	// No DKIM results: this sender does not sign messages.
	_, result := dmarc.Verify(ctx, slog.Default(), s.resolver, from, nil, spfResult, &spfIdentity, false)
	if result.Record == nil {
		if result.Status == dmarc.StatusTemperror || result.Status == dmarc.StatusPermerror {
			slog.Warn("mta: DMARC warning: lookup failed", "from_domain", fromDomain, "status", result.Status, "err", result.Err)
		}
		return
	}
	if result.Status == dmarc.StatusPass {
		slog.Debug("mta: DMARC check passed", "from_domain", fromDomain, "spf_identity", spfDomain, "policy", result.Record.Policy)
		return
	}
	slog.Warn("mta: DMARC warning: From domain is not DMARC-aligned with SPF identity; receivers may reject this message",
		"from_domain", fromDomain, "spf_identity", spfDomain, "status", result.Status,
		"policy", result.Record.Policy, "spf_record_found", spfFound, "spf_lookup_err", spfErr,
		"reject_recommended", result.Reject)
}

// spfRecordExists looks for an SPF TXT record under the standard name and the
// spf.<domain> alias. Like mox's resolver, lookups use absolute DNS names.
func (s *MtaSender) spfRecordExists(ctx context.Context, domain string) (bool, error) {
	var lastErr error
	for _, name := range []string{"spf." + domain, domain} {
		txts, _, err := s.resolver.LookupTXT(ctx, name+".")
		if err != nil {
			lastErr = err
			continue
		}
		for _, txt := range txts {
			if strings.HasPrefix(strings.ToLower(strings.TrimSpace(txt)), "v=spf1") {
				return true, nil
			}
		}
	}
	return false, lastErr
}

func domainOfAddress(address string) string {
	at := strings.LastIndex(address, "@")
	if at <= 0 || at == len(address)-1 {
		return ""
	}
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(address[at+1:])), ".")
}

// SendBounce composes a bounce message and delivers it directly. SES message
// IDs do not apply here, so originalMessageID is included in the body for
// diagnostics. The envelope sender is empty (<>) to prevent bounce loops.
func (s *MtaSender) SendBounce(ctx context.Context, originalMessageID, bounceSender, recipient, bounceType string, originalMessage *email.UserEmail) error {
	subject := "Mail delivery failed"
	if originalMessage != nil && originalMessage.Subject != "" {
		subject = "Mail delivery failed: " + originalMessage.Subject
	}
	body := fmt.Sprintf("Delivery to %s failed.\nBounce type: %s\nOriginal message ID: %s\n", recipient, bounceType, originalMessageID)
	return s.deliver(ctx, "", helloNameFromAddress(bounceSender), &email.UserEmail{
		From:    email.Address{Address: bounceSender},
		To:      []email.Address{{Address: recipient}},
		Subject: subject,
		Date:    time.Now().UTC(),
		Text:    body,
	})
}

func (s *MtaSender) deliver(ctx context.Context, envelopeFrom, helloName string, message *email.UserEmail) error {
	recipients, err := groupRecipients(message)
	if err != nil {
		return err
	}
	if len(recipients) == 0 {
		return errors.New("no recipients")
	}

	// Serialize once so every MX host receives an identical message (including
	// the same Message-ID). Bcc appears only in the SMTP envelope.
	wire := BuildWireMessage(message, envelopeFrom, helloName)

	domains := make([]string, 0, len(recipients))
	for domain := range recipients {
		domains = append(domains, domain)
	}
	sort.Strings(domains)

	var errs []error
	for _, domain := range domains {
		if err := s.deliverToDomain(ctx, domain, envelopeFrom, helloName, recipients[domain], wire); err != nil {
			errs = append(errs, fmt.Errorf("deliver to %s: %w", domain, err))
		}
	}
	return errors.Join(errs...)
}

func (s *MtaSender) deliverToDomain(ctx context.Context, domain, envelopeFrom, helloName string, recipients []string, wire []byte) error {
	hosts, lookupErr := s.mxHosts(ctx, domain)
	if lookupErr != nil {
		slog.Warn("mta: MX lookup failed; trying domain as mail host", "domain", domain, "err", lookupErr)
	}
	slog.Debug("mta: delivering to domain", "domain", domain, "hosts", hosts, "recipients", len(recipients))

	var errs []error
	for _, host := range hosts {
		if err := s.deliverToHost(ctx, host, envelopeFrom, helloName, recipients, wire); err != nil {
			errs = append(errs, fmt.Errorf("mx %s: %w", host, err))
			continue
		}
		slog.Info("mta: delivered to domain", "domain", domain, "host", host, "recipients", len(recipients))
		return nil
	}
	return errors.Join(errs...)
}

// mxHosts returns mail hosts in preference order. A domain with no usable MX
// records is its own mail host, and the domain is also a last-resort fallback
// when it has MX records that all fail.
func (s *MtaSender) mxHosts(ctx context.Context, domain string) ([]string, error) {
	records, err := s.lookupMX(ctx, domain)
	if err != nil {
		return []string{domain}, err
	}

	sort.SliceStable(records, func(i, j int) bool {
		return records[i].Pref < records[j].Pref
	})

	hosts := make([]string, 0, len(records)+1)
	seen := make(map[string]bool)
	for _, record := range records {
		host := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(record.Host), "."))
		if host == "" || seen[host] {
			continue
		}
		seen[host] = true
		hosts = append(hosts, host)
	}
	if !seen[strings.ToLower(domain)] {
		hosts = append(hosts, domain)
	}
	return hosts, nil
}

func (s *MtaSender) deliverToHost(ctx context.Context, host, envelopeFrom, helloName string, recipients []string, wire []byte) error {
	addr := net.JoinHostPort(host, s.port)
	conn, err := s.dial(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("dial %s: %w", addr, err)
	}
	defer conn.Close()

	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	} else {
		_ = conn.SetDeadline(time.Now().Add(s.timeout))
	}

	client, err := smtp.NewClient(conn, host)
	if err != nil {
		return fmt.Errorf("smtp greeting: %w", err)
	}
	defer client.Close()

	if err := client.Hello(helloName); err != nil {
		return fmt.Errorf("smtp hello: %w", err)
	}
	if ok, _ := client.Extension("STARTTLS"); ok {
		if err := client.StartTLS(&tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}); err != nil {
			return fmt.Errorf("smtp starttls: %w", err)
		}
	}
	if err := client.Mail(envelopeFrom); err != nil {
		return fmt.Errorf("smtp mail from: %w", err)
	}
	for _, recipient := range recipients {
		if err := client.Rcpt(recipient); err != nil {
			return fmt.Errorf("smtp rcpt to %s: %w", recipient, err)
		}
	}

	writer, err := client.Data()
	if err != nil {
		return fmt.Errorf("smtp data: %w", err)
	}
	if _, err := writer.Write(wire); err != nil {
		_ = writer.Close()
		return fmt.Errorf("smtp write: %w", err)
	}
	if err := writer.Close(); err != nil {
		return fmt.Errorf("smtp finish: %w", err)
	}
	// The message has already been accepted at this point; a broken QUIT must
	// not turn a successful delivery into a reported failure.
	if err := client.Quit(); err != nil {
		slog.Warn("mta: smtp quit warning", "host", host, "err", err)
	}
	return nil
}

// groupRecipients returns the bare envelope recipients for each domain,
// preserving header order and removing duplicates across To, Cc and Bcc.
func groupRecipients(message *email.UserEmail) (map[string][]string, error) {
	groups := make(map[string][]string)
	seen := make(map[string]bool)
	for _, list := range [][]email.Address{message.To, message.Cc, message.Bcc} {
		for _, address := range list {
			recipient := bareAddress(address.Address)
			if recipient == "" {
				continue
			}
			key := strings.ToLower(recipient)
			if seen[key] {
				continue
			}
			at := strings.LastIndex(recipient, "@")
			if at <= 0 || at == len(recipient)-1 {
				return nil, fmt.Errorf("invalid recipient address %q", address.Address)
			}
			seen[key] = true
			domain := strings.ToLower(strings.TrimSuffix(recipient[at+1:], "."))
			groups[domain] = append(groups[domain], recipient)
		}
	}
	return groups, nil
}

func bareAddress(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if parsed, err := mail.ParseAddress(value); err == nil {
		return parsed.Address
	}
	return value
}

// BuildWireMessage renders message as an RFC 5322 wire-format byte string.
// It is shared by every wire sender so that each delivery path emits the same
// headers and body for the same message. envelopeFrom and helloName only
// determine the domain part of a generated Message-ID; a message that already
// carries a Message-ID reuses it. Bcc appears only in the delivery envelope,
// never in the headers.
func BuildWireMessage(message *email.UserEmail, envelopeFrom, helloName string) []byte {
	from := message.From

	var b strings.Builder
	fmt.Fprintf(&b, "From: %s\r\n", headerValue(formatAddress(from)))
	to := formatAddresses(message.To)
	if to == "" {
		to = "undisclosed-recipients:;"
	}
	fmt.Fprintf(&b, "To: %s\r\n", headerValue(to))
	if cc := formatAddresses(message.Cc); cc != "" {
		fmt.Fprintf(&b, "Cc: %s\r\n", headerValue(cc))
	}
	if replyTo := formatAddresses(message.ReplyTo); replyTo != "" {
		fmt.Fprintf(&b, "Reply-To: %s\r\n", headerValue(replyTo))
	}
	fmt.Fprintf(&b, "Subject: %s\r\n", headerValue(message.Subject))
	date := message.Date
	if date.IsZero() {
		date = time.Now().UTC()
	}
	fmt.Fprintf(&b, "Date: %s\r\n", date.UTC().Format(time.RFC1123Z))
	if messageID := strings.TrimSpace(message.MessageID); messageID != "" {
		fmt.Fprintf(&b, "Message-ID: %s\r\n", headerValue(messageID))
	} else {
		fmt.Fprintf(&b, "Message-ID: <%s@%s>\r\n", randomToken(16), messageDomain(envelopeFrom, helloName))
	}
	if len(message.InReplyTo) > 0 {
		fmt.Fprintf(&b, "In-Reply-To: %s\r\n", headerValue(strings.Join(message.InReplyTo, " ")))
	}
	if len(message.References) > 0 {
		fmt.Fprintf(&b, "References: %s\r\n", headerValue(strings.Join(message.References, " ")))
	}
	b.WriteString("MIME-Version: 1.0\r\n")

	text := message.Text
	html := message.HTML
	if text == "" && html == "" {
		text = " "
	}
	if text != "" && html != "" {
		boundary := "emissary_" + randomToken(16)
		fmt.Fprintf(&b, "Content-Type: multipart/alternative; boundary=%q\r\n\r\n", boundary)
		fmt.Fprintf(&b, "--%s\r\nContent-Type: text/plain; charset=UTF-8\r\nContent-Transfer-Encoding: base64\r\n\r\n", boundary)
		writeBase64(&b, text)
		fmt.Fprintf(&b, "--%s\r\nContent-Type: text/html; charset=UTF-8\r\nContent-Transfer-Encoding: base64\r\n\r\n", boundary)
		writeBase64(&b, html)
		fmt.Fprintf(&b, "--%s--\r\n", boundary)
	} else {
		contentType, body := "text/plain", text
		if html != "" {
			contentType, body = "text/html", html
		}
		fmt.Fprintf(&b, "Content-Type: %s; charset=UTF-8\r\n", contentType)
		b.WriteString("Content-Transfer-Encoding: base64\r\n\r\n")
		writeBase64(&b, body)
	}
	return []byte(b.String())
}

// messageDomain chooses the domain part for a generated Message-ID: the
// envelope sender's domain, falling back to the EHLO/HELO identity.
func messageDomain(envelopeFrom, helloName string) string {
	if _, domain, ok := strings.Cut(envelopeFrom, "@"); ok && domain != "" {
		return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(domain)), ".")
	}
	return helloName
}

func headerValue(value string) string {
	value = strings.ReplaceAll(value, "\r", " ")
	return strings.ReplaceAll(value, "\n", " ")
}

func formatAddress(address email.Address) string {
	value := strings.TrimSpace(address.Address)
	if value == "" {
		return ""
	}
	if address.Name == "" {
		return value
	}
	return (&mail.Address{Name: address.Name, Address: value}).String()
}

func formatAddresses(addresses []email.Address) string {
	values := make([]string, 0, len(addresses))
	for _, address := range addresses {
		if value := formatAddress(address); value != "" {
			values = append(values, value)
		}
	}
	return strings.Join(values, ", ")
}

func helloNameFromAddress(from string) string {
	at := strings.LastIndex(from, "@")
	if at <= 0 || at == len(from)-1 {
		return "localhost"
	}
	domain := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(from[at+1:])), ".")
	if domain == "" {
		return "localhost"
	}
	return domain
}

func writeBase64(b *strings.Builder, body string) {
	body = strings.ReplaceAll(body, "\r\n", "\n")
	body = strings.ReplaceAll(body, "\r", "\n")
	encoded := base64.StdEncoding.EncodeToString([]byte(body))
	for len(encoded) > 76 {
		b.WriteString(encoded[:76])
		b.WriteString("\r\n")
		encoded = encoded[76:]
	}
	b.WriteString(encoded)
	b.WriteString("\r\n")
}

func randomToken(bytes int) string {
	buf := make([]byte, bytes)
	if _, err := rand.Read(buf); err == nil {
		return hex.EncodeToString(buf)
	}
	return fmt.Sprintf("%d", time.Now().UnixNano())
}
