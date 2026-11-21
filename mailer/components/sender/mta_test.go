package sender

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"testing"

	"github.com/kncept/Emissary/mailer/email"
	"github.com/mjl-/mox/dns"
)

type smtpSession struct {
	commands []string
	data     string
}

// startSMTPServer returns the client end of a pipe and a channel that receives
// the commands and message body captured by a minimal SMTP server.
func startSMTPServer(t *testing.T) (net.Conn, <-chan smtpSession) {
	t.Helper()
	clientConn, serverConn := net.Pipe()
	done := make(chan smtpSession, 1)
	go func() {
		defer serverConn.Close()
		session := smtpSession{}
		reader := bufio.NewReader(serverConn)
		fmt.Fprint(serverConn, "220 test.example ESMTP\r\n")
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				done <- session
				return
			}
			line = strings.TrimRight(line, "\r\n")
			session.commands = append(session.commands, line)
			switch upper := strings.ToUpper(line); {
			case strings.HasPrefix(upper, "EHLO "), strings.HasPrefix(upper, "HELO "):
				fmt.Fprint(serverConn, "250-test.example\r\n250 8BITMIME\r\n")
			case strings.HasPrefix(upper, "MAIL FROM:"), strings.HasPrefix(upper, "RCPT TO:"):
				fmt.Fprint(serverConn, "250 OK\r\n")
			case upper == "DATA":
				fmt.Fprint(serverConn, "354 go ahead\r\n")
				var data strings.Builder
				for {
					dataLine, err := reader.ReadString('\n')
					if err != nil {
						done <- session
						return
					}
					if dataLine == ".\r\n" || dataLine == ".\n" {
						break
					}
					data.WriteString(dataLine)
				}
				session.data = data.String()
				fmt.Fprint(serverConn, "250 queued\r\n")
			case upper == "QUIT":
				fmt.Fprint(serverConn, "221 bye\r\n")
				done <- session
				return
			default:
				fmt.Fprint(serverConn, "500 unknown command\r\n")
			}
		}
	}()
	return clientConn, done
}

func testMtaSender() *MtaSender {
	s := NewMtaSender("2525")
	// Keep the DMARC pre-flight check hermetic: no DNS in unit tests.
	s.resolver = dns.MockResolver{}
	return s
}

func TestMtaSender_NameAndPort(t *testing.T) {
	s := testMtaSender()
	if s.Name() != "MtaSender" {
		t.Errorf("Name() = %q, want MtaSender", s.Name())
	}
	if s.port != "2525" {
		t.Errorf("port = %q, want 2525", s.port)
	}
	if NewMtaSender("").port != "25" {
		t.Error("empty port should default to 25")
	}
}

func TestMtaSender_HelloFromMessageFrom(t *testing.T) {
	// The message's From determines the envelope sender and the EHLO identity.
	s := testMtaSender()
	s.lookupMX = func(_ context.Context, _ string) ([]*net.MX, error) { return nil, nil }

	var done <-chan smtpSession
	s.dial = func(_ context.Context, _, _ string) (net.Conn, error) {
		conn, sessionDone := startSMTPServer(t)
		done = sessionDone
		return conn, nil
	}

	err := s.Send(context.Background(), &email.UserEmail{
		From: email.Address{Address: "user@mail.example.net"},
		To:   []email.Address{{Address: "to@example.net"}},
	})
	if err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	session := <-done
	if session.commands[0] != "EHLO mail.example.net" {
		t.Errorf("EHLO = %q, want EHLO mail.example.net", session.commands[0])
	}
	if session.commands[1] != "MAIL FROM:<user@mail.example.net> BODY=8BITMIME" {
		t.Errorf("envelope sender = %q", session.commands[1])
	}
}

func TestMtaSender_SendGroupsRecipientsByDomain(t *testing.T) {
	s := testMtaSender()
	s.lookupMX = func(_ context.Context, domain string) ([]*net.MX, error) {
		switch domain {
		case "example.net":
			return []*net.MX{{Host: "mx2.example.net.", Pref: 20}, {Host: "mx1.example.net.", Pref: 10}}, nil
		case "example.org":
			return []*net.MX{{Host: "mx.example.org.", Pref: 10}}, nil
		default:
			return nil, nil
		}
	}

	var dialed []string
	sessions := make(map[string]<-chan smtpSession)
	s.dial = func(_ context.Context, _, address string) (net.Conn, error) {
		dialed = append(dialed, address)
		conn, done := startSMTPServer(t)
		sessions[address] = done
		return conn, nil
	}

	err := s.Send(context.Background(), &email.UserEmail{
		From:    email.Address{Address: "sender@example.com"},
		To:      []email.Address{{Address: "one@example.net"}, {Address: "two@example.net"}},
		Cc:      []email.Address{{Address: "three@example.org"}},
		Bcc:     []email.Address{{Address: "hidden@example.net"}},
		Subject: "Hello",
		Text:    "Body\r\nline two",
	})
	if err != nil {
		t.Fatalf("Send() error = %v", err)
	}

	if len(dialed) != 2 {
		t.Fatalf("dialed %v, want two hosts", dialed)
	}
	if dialed[0] != "mx1.example.net:2525" || dialed[1] != "mx.example.org:2525" {
		t.Errorf("dial order = %v, want MX preference order", dialed)
	}

	netSession := <-sessions["mx1.example.net:2525"]
	wantCommands := []string{
		"EHLO example.com",
		"MAIL FROM:<sender@example.com> BODY=8BITMIME",
		"RCPT TO:<one@example.net>",
		"RCPT TO:<two@example.net>",
		"RCPT TO:<hidden@example.net>",
		"DATA",
		"QUIT",
	}
	if got := strings.Join(netSession.commands, "|"); got != strings.Join(wantCommands, "|") {
		t.Errorf("SMTP commands = %v, want %v", netSession.commands, wantCommands)
	}

	if !strings.HasPrefix(netSession.data, "From: sender@example.com\r\n") {
		t.Errorf("missing From header: %q", netSession.data)
	}
	if !strings.Contains(netSession.data, "To: one@example.net, two@example.net\r\n") {
		t.Errorf("missing To header: %q", netSession.data)
	}
	if !strings.Contains(netSession.data, "Cc: three@example.org\r\n") {
		t.Errorf("missing Cc header: %q", netSession.data)
	}
	if strings.Contains(netSession.data, "Bcc:") || strings.Contains(netSession.data, "hidden@example.net") {
		t.Errorf("Bcc leaked into message: %q", netSession.data)
	}
	if !strings.Contains(netSession.data, "Content-Transfer-Encoding: base64\r\n") {
		t.Errorf("missing base64 encoding header: %q", netSession.data)
	}
	encoded := base64.StdEncoding.EncodeToString([]byte("Body\nline two"))
	if !strings.Contains(netSession.data, encoded) {
		t.Errorf("body not found (base64 %q): %q", encoded, netSession.data)
	}

	orgSession := <-sessions["mx.example.org:2525"]
	if !strings.Contains(strings.Join(orgSession.commands, "|"), "RCPT TO:<three@example.org>") {
		t.Errorf("org session commands = %v", orgSession.commands)
	}
}

// TestMtaSender_ThreadHeadersAndMessageID checks that In-Reply-To and
// References reach the wire format, that a stored Message-ID is reused, and
// that a missing one is generated.
func TestMtaSender_ThreadHeadersAndMessageID(t *testing.T) {
	s := testMtaSender()
	s.lookupMX = func(_ context.Context, _ string) ([]*net.MX, error) { return nil, nil }

	var done <-chan smtpSession
	s.dial = func(_ context.Context, _, _ string) (net.Conn, error) {
		conn, sessionDone := startSMTPServer(t)
		done = sessionDone
		return conn, nil
	}

	err := s.Send(context.Background(), &email.UserEmail{
		From:       email.Address{Address: "user@example.com"},
		To:         []email.Address{{Address: "to@example.net"}},
		Subject:    "RE: Hello",
		Text:       "reply body",
		MessageID:  "<reply-1@example.com>",
		InReplyTo:  []string{"<original-1@example.com>"},
		References: []string{"<first@example.com>", "<original-1@example.com>"},
	})
	if err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	session := <-done
	if !strings.Contains(session.data, "Message-ID: <reply-1@example.com>\r\n") {
		t.Errorf("stored Message-ID was not reused: %q", session.data)
	}
	if !strings.Contains(session.data, "In-Reply-To: <original-1@example.com>\r\n") {
		t.Errorf("missing In-Reply-To header: %q", session.data)
	}
	if !strings.Contains(session.data, "References: <first@example.com> <original-1@example.com>\r\n") {
		t.Errorf("missing References header: %q", session.data)
	}
}

func TestBuildWireMessage_GeneratesMessageIDWhenAbsent(t *testing.T) {
	message := &email.UserEmail{
		From:    email.Address{Address: "user@example.com"},
		To:      []email.Address{{Address: "to@example.net"}},
		Subject: "Hello",
		Text:    "body",
	}
	wire := string(BuildWireMessage(message, "user@example.com", "example.com"))
	if !strings.Contains(wire, "Message-ID: <") {
		t.Fatalf("missing Message-ID header: %q", wire)
	}
	id := wire[strings.Index(wire, "Message-ID: <")+len("Message-ID: <"):]
	id = id[:strings.Index(id, ">")]
	if !strings.HasSuffix(id, "@example.com") {
		t.Errorf("generated Message-ID = %q, want domain example.com", id)
	}
	if strings.Contains(wire, "In-Reply-To:") || strings.Contains(wire, "References:") {
		t.Errorf("thread headers written without values: %q", wire)
	}
}

func TestMtaSender_MXFailoverAndLastResort(t *testing.T) {
	s := testMtaSender()
	s.lookupMX = func(_ context.Context, domain string) ([]*net.MX, error) {
		return []*net.MX{{Host: "slow.example.net.", Pref: 20}, {Host: "fast.example.net.", Pref: 10}}, nil
	}

	var dialed []string
	s.dial = func(_ context.Context, _, address string) (net.Conn, error) {
		dialed = append(dialed, address)
		if address == "fast.example.net:2525" {
			return nil, errors.New("connection refused")
		}
		conn, _ := startSMTPServer(t)
		return conn, nil
	}

	err := s.Send(context.Background(), &email.UserEmail{
		From: email.Address{Address: "sender@example.com"},
		To:   []email.Address{{Address: "to@example.net"}},
		Text: "body",
	})
	if err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	want := []string{"fast.example.net:2525", "slow.example.net:2525"}
	if strings.Join(dialed, "|") != strings.Join(want, "|") {
		t.Errorf("dial order = %v, want %v", dialed, want)
	}
}

func TestMtaSender_LastResortDomainAfterMXFailures(t *testing.T) {
	s := testMtaSender()
	s.lookupMX = func(_ context.Context, _ string) ([]*net.MX, error) {
		return []*net.MX{{Host: "mx.example.net.", Pref: 10}}, nil
	}

	var dialed []string
	s.dial = func(_ context.Context, _, address string) (net.Conn, error) {
		dialed = append(dialed, address)
		if address == "mx.example.net:2525" {
			return nil, errors.New("connection refused")
		}
		conn, _ := startSMTPServer(t)
		return conn, nil
	}

	if err := s.Send(context.Background(), &email.UserEmail{
		From: email.Address{Address: "sender@example.com"},
		To:   []email.Address{{Address: "to@example.net"}},
	}); err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	want := []string{"mx.example.net:2525", "example.net:2525"}
	if strings.Join(dialed, "|") != strings.Join(want, "|") {
		t.Errorf("dial order = %v, want %v", dialed, want)
	}
}

func TestMtaSender_NoMXUsesDomain(t *testing.T) {
	s := testMtaSender()
	s.lookupMX = func(_ context.Context, _ string) ([]*net.MX, error) { return nil, nil }

	var dialed []string
	s.dial = func(_ context.Context, _, address string) (net.Conn, error) {
		dialed = append(dialed, address)
		conn, _ := startSMTPServer(t)
		return conn, nil
	}

	if err := s.Send(context.Background(), &email.UserEmail{
		From: email.Address{Address: "sender@example.com"},
		To:   []email.Address{{Address: "to@example.net"}},
	}); err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	if len(dialed) != 1 || dialed[0] != "example.net:2525" {
		t.Errorf("dialed = %v, want example.net:2525", dialed)
	}
}

func TestMtaSender_AllHostsFail(t *testing.T) {
	s := testMtaSender()
	s.lookupMX = func(_ context.Context, _ string) ([]*net.MX, error) {
		return []*net.MX{{Host: "mx1.example.net.", Pref: 10}, {Host: "mx2.example.net.", Pref: 20}}, nil
	}
	s.dial = func(_ context.Context, _, _ string) (net.Conn, error) {
		return nil, errors.New("connection refused")
	}

	err := s.Send(context.Background(), &email.UserEmail{
		From: email.Address{Address: "sender@example.com"},
		To:   []email.Address{{Address: "to@example.net"}},
	})
	if err == nil {
		t.Fatal("Send() should fail when all MX hosts and the domain fail")
	}
	for _, want := range []string{"mx1.example.net", "mx2.example.net", "example.net", "connection refused"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

func TestMtaSender_SendValidation(t *testing.T) {
	s := testMtaSender()
	if err := s.Send(context.Background(), &email.UserEmail{To: []email.Address{{Address: "to@example.net"}}}); err == nil {
		t.Error("Send() with no From should fail")
	}

	if err := s.Send(context.Background(), &email.UserEmail{From: email.Address{Address: "sender@example.com"}}); err == nil {
		t.Error("Send() with no recipients should fail")
	}
	if err := s.Send(context.Background(), &email.UserEmail{
		From: email.Address{Address: "sender@example.com"},
		To:   []email.Address{{Address: "not-an-address"}},
	}); err == nil {
		t.Error("Send() with an invalid recipient should fail")
	}
}

func TestMtaSender_SendBounceUsesNullEnvelopeSender(t *testing.T) {
	s := testMtaSender()
	s.lookupMX = func(_ context.Context, _ string) ([]*net.MX, error) { return nil, nil }

	var done <-chan smtpSession
	s.dial = func(_ context.Context, _, _ string) (net.Conn, error) {
		conn, sessionDone := startSMTPServer(t)
		done = sessionDone
		return conn, nil
	}

	err := s.SendBounce(context.Background(), "message-id-123", "mailer-daemon@example.com", "bad@example.net", "UserUnknown", &email.UserEmail{Subject: "Original"})
	if err != nil {
		t.Fatalf("SendBounce() error = %v", err)
	}
	session := <-done
	commands := strings.Join(session.commands, "|")
	if !strings.Contains(commands, "MAIL FROM:<>") {
		t.Errorf("bounce envelope sender = %v, want MAIL FROM:<>", session.commands)
	}
	if !strings.Contains(commands, "RCPT TO:<bad@example.net>") {
		t.Errorf("bounce recipient = %v", session.commands)
	}
	if !strings.Contains(session.data, "Subject: Mail delivery failed: Original\r\n") {
		t.Errorf("bounce subject = %q", session.data)
	}
	parts := strings.SplitN(session.data, "\r\n\r\n", 2)
	if len(parts) != 2 {
		t.Fatalf("bounce message has no body: %q", session.data)
	}
	body, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(parts[1], "\r\n", ""))
	if err != nil {
		t.Fatalf("decode bounce body: %v", err)
	}
	if !strings.Contains(string(body), "message-id-123") {
		t.Errorf("bounce body missing original message ID: %q", body)
	}
}

func captureLog(t *testing.T, fn func()) string {
	t.Helper()
	var buf bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(previous)
	fn()
	return buf.String()
}

func TestMtaSender_DMARCCheck(t *testing.T) {
	tests := []struct {
		name         string
		from         string
		envelopeFrom string
		txt          map[string][]string
		wantWarning  bool
	}{
		{
			name:         "no DMARC record",
			from:         "user@example.com",
			envelopeFrom: "mailer@other.example",
			txt:          map[string][]string{},
		},
		{
			name:         "aligned SPF domain",
			from:         "user@example.com",
			envelopeFrom: "mailer@mail.example.com",
			txt: map[string][]string{
				"_dmarc.example.com.": {"v=DMARC1; p=reject"},
				"mail.example.com.":   {"v=spf1 -all"},
			},
		},
		{
			name:         "spf subdomain alias",
			from:         "user@example.com",
			envelopeFrom: "mailer@example.com",
			txt: map[string][]string{
				"_dmarc.example.com.": {"v=DMARC1; p=quarantine"},
				"spf.example.com.":    {"v=spf1 -all"},
			},
		},
		{
			name:         "misaligned SPF domain",
			from:         "user@example.com",
			envelopeFrom: "mailer@other.example",
			txt: map[string][]string{
				"_dmarc.example.com.": {"v=DMARC1; p=reject"},
				"other.example.":      {"v=spf1 -all"},
			},
			wantWarning: true,
		},
		{
			name:         "strict alignment rejects subdomain",
			from:         "user@example.com",
			envelopeFrom: "mailer@mail.example.com",
			txt: map[string][]string{
				"_dmarc.example.com.": {"v=DMARC1; p=reject; aspf=s"},
				"mail.example.com.":   {"v=spf1 -all"},
			},
			wantWarning: true,
		},
		{
			name:         "no SPF record",
			from:         "user@example.com",
			envelopeFrom: "mailer@example.com",
			txt: map[string][]string{
				"_dmarc.example.com.": {"v=DMARC1; p=reject"},
			},
			wantWarning: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := testMtaSender()
			s.resolver = dns.MockResolver{TXT: tt.txt}
			message := &email.UserEmail{From: email.Address{Address: tt.from}}
			output := captureLog(t, func() {
				s.checkDMARC(context.Background(), tt.envelopeFrom, message.From.Address)
			})
			warned := strings.Contains(output, "DMARC warning")
			if warned != tt.wantWarning {
				t.Errorf("DMARC warning = %t, want %t (log: %q)", warned, tt.wantWarning, output)
			}
		})
	}
}

func TestMtaSender_DMARCCheckDoesNotBlockSend(t *testing.T) {
	s := testMtaSender()
	s.resolver = dns.MockResolver{TXT: map[string][]string{
		"_dmarc.example.com.": {"v=DMARC1; p=reject"},
		"other.example.":      {"v=spf1 -all"},
	}}
	s.lookupMX = func(_ context.Context, _ string) ([]*net.MX, error) { return nil, nil }
	s.dial = func(_ context.Context, _, _ string) (net.Conn, error) {
		conn, _ := startSMTPServer(t)
		return conn, nil
	}

	output := captureLog(t, func() {
		err := s.Send(context.Background(), &email.UserEmail{From: email.Address{Address: "user@example.com"}, To: []email.Address{{Address: "to@example.net"}}})
		if err != nil {
			t.Errorf("Send() error = %v; a DMARC warning must not block delivery", err)
		}
	})
	if !strings.Contains(output, "DMARC warning") {
		t.Errorf("expected DMARC warning for misaligned From/envelope domains, got %q", output)
	}
}
