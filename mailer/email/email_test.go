package email

import (
	"strings"
	"testing"
	"time"
)

func TestEncodeDecodeID(t *testing.T) {
	tests := []string{
		"simple-key",
		"key/with/slashes",
		"key.with.dots",
		"key=with=equals",
		"key+with+plus",
		"",
		"🎉unicode🎉",
	}

	for _, key := range tests {
		t.Run(key, func(t *testing.T) {
			encoded := EncodeID(key)
			decoded, err := DecodeID(encoded)
			if err != nil {
				t.Fatalf("DecodeID(%q) error = %v", encoded, err)
			}
			if decoded != key {
				t.Errorf("DecodeID(EncodeID(%q)) = %q, want %q", key, decoded, key)
			}
		})
	}
}

func TestDecodeID_Invalid(t *testing.T) {
	_, err := DecodeID("not-valid-base64!")
	if err == nil {
		t.Error("DecodeID should error on invalid base64")
	}
}

func TestParse_SimpleTextEmail(t *testing.T) {
	raw := `From: sender@example.com
To: recipient@example.com
Subject: Test Subject
Date: Mon, 1 Jan 2024 12:00:00 +0000

Hello World
`

	msg, err := Parse("test-id", strings.NewReader(raw))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}

	if msg.ID != "test-id" {
		t.Errorf("ID = %q, want %q", msg.ID, "test-id")
	}
	if msg.From.Address != "sender@example.com" {
		t.Errorf("From = %q, want %q", msg.From.Address, "sender@example.com")
	}
	if len(msg.To) != 1 || msg.To[0].Address != "recipient@example.com" {
		t.Errorf("To = %v, want [recipient@example.com]", msg.To)
	}
	if msg.Subject != "Test Subject" {
		t.Errorf("Subject = %q, want %q", msg.Subject, "Test Subject")
	}
	if !msg.Date.Equal(time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)) {
		t.Errorf("Date = %v, want 2024-01-01 12:00:00 UTC", msg.Date)
	}
	if msg.Text != "Hello World" {
		t.Errorf("Text = %q, want %q", msg.Text, "Hello World")
	}
	if msg.HTML != "" {
		t.Errorf("HTML = %q, want empty", msg.HTML)
	}
}

func TestParse_WithDisplayNames(t *testing.T) {
	raw := `From: "Sender Name" <sender@example.com>
To: "Recipient Name" <recipient@example.com>
Subject: Test
Date: Mon, 1 Jan 2024 12:00:00 +0000

Body
`

	msg, err := Parse("test-id", strings.NewReader(raw))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}

	if msg.From.Name != "Sender Name" {
		t.Errorf("From.Name = %q, want %q", msg.From.Name, "Sender Name")
	}
	if len(msg.To) != 1 || msg.To[0].Name != "Recipient Name" {
		t.Errorf("To[0].Name = %q, want %q", msg.To[0].Name, "Recipient Name")
	}
}

func TestParse_MultipleRecipients(t *testing.T) {
	raw := `From: sender@example.com
To: a@example.com, b@example.com
Cc: c@example.com
Bcc: d@example.com
Subject: Test
Date: Mon, 1 Jan 2024 12:00:00 +0000

Body
`

	msg, err := Parse("test-id", strings.NewReader(raw))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}

	if len(msg.To) != 2 {
		t.Errorf("To count = %d, want 2", len(msg.To))
	}
	if len(msg.Cc) != 1 || msg.Cc[0].Address != "c@example.com" {
		t.Errorf("Cc = %v, want [c@example.com]", msg.Cc)
	}
	if len(msg.Bcc) != 1 || msg.Bcc[0].Address != "d@example.com" {
		t.Errorf("Bcc = %v, want [d@example.com]", msg.Bcc)
	}
}

func TestParse_MultipartEmail(t *testing.T) {
	raw := `From: sender@example.com
To: recipient@example.com
Subject: Multipart Test
Date: Mon, 1 Jan 2024 12:00:00 +0000
MIME-Version: 1.0
Content-Type: multipart/alternative; boundary="boundary123"

--boundary123
Content-Type: text/plain; charset=utf-8

Plain text body

--boundary123
Content-Type: text/html; charset=utf-8

<html><body><p>HTML body</p></body></html>

--boundary123--
`

	msg, err := Parse("test-id", strings.NewReader(raw))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}

	if !strings.Contains(msg.Text, "Plain text body") {
		t.Errorf("Text missing plain part: %q", msg.Text)
	}
	if !strings.Contains(msg.HTML, "HTML body") {
		t.Errorf("HTML missing html part: %q", msg.HTML)
	}
}

func TestParse_Base64Encoded(t *testing.T) {
	raw := `From: sender@example.com
To: recipient@example.com
Subject: Base64 Test
Date: Mon, 1 Jan 2024 12:00:00 +0000
Content-Type: text/plain; charset=utf-8
Content-Transfer-Encoding: base64

SGVsbG8gV29ybGQh
`

	msg, err := Parse("test-id", strings.NewReader(raw))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}

	if msg.Text != "Hello World!" {
		t.Errorf("Text = %q, want %q", msg.Text, "Hello World!")
	}
}

func TestParse_QuotedPrintable(t *testing.T) {
	raw := `From: sender@example.com
To: recipient@example.com
Subject: QP Test
Date: Mon, 1 Jan 2024 12:00:00 +0000
Content-Type: text/plain; charset=utf-8
Content-Transfer-Encoding: quoted-printable

Hello=20World=21
`

	msg, err := Parse("test-id", strings.NewReader(raw))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}

	if msg.Text != "Hello World!" {
		t.Errorf("Text = %q, want %q", msg.Text, "Hello World!")
	}
}

func TestParse_ISO8859_1(t *testing.T) {
	// Latin-1 encoded "café" - subject uses MIME encoded-word, body uses raw 0xE9 byte
	raw := "From: sender@example.com\r\n" +
		"To: recipient@example.com\r\n" +
		"Subject: =?iso-8859-1?q?caf=E9?=\r\n" +
		"Date: Mon, 1 Jan 2024 12:00:00 +0000\r\n" +
		"Content-Type: text/plain; charset=iso-8859-1\r\n" +
		"\r\n" +
		"caf\xE9"

	msg, err := Parse("test-id", strings.NewReader(raw))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}

	if msg.Subject != "café" {
		t.Errorf("Subject = %q, want %q", msg.Subject, "café")
	}
	if msg.Text != "café" {
		t.Errorf("Text = %q, want %q", msg.Text, "café")
	}
}

func TestParse_MissingHeaders(t *testing.T) {
	raw := `From: sender@example.com

Body only
`

	msg, err := Parse("test-id", strings.NewReader(raw))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}

	if msg.Subject != "" {
		t.Errorf("Subject = %q, want empty", msg.Subject)
	}
	// Date should be zero time when missing (parseDate returns zero time on failure)
	if !msg.Date.IsZero() {
		t.Errorf("Date = %v, want zero time", msg.Date)
	}
}

func TestParse_NestedMultipart(t *testing.T) {
	raw := `From: sender@example.com
To: recipient@example.com
Subject: Nested
Date: Mon, 1 Jan 2024 12:00:00 +0000
MIME-Version: 1.0
Content-Type: multipart/mixed; boundary="outer"

--outer
Content-Type: multipart/alternative; boundary="inner"

--inner
Content-Type: text/plain

Plain

--inner
Content-Type: text/html

<html>HTML</html>

--inner--
--outer--
`

	msg, err := Parse("test-id", strings.NewReader(raw))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}

	if msg.Text != "Plain" {
		t.Errorf("Text = %q, want %q", msg.Text, "Plain")
	}
	if msg.HTML != "<html>HTML</html>" {
		t.Errorf("HTML = %q, want %q", msg.HTML, "<html>HTML</html>")
	}
}

// ============================================================================
// WireFormatEmail tests
// ============================================================================

func TestWireFormat_ParseSimple(t *testing.T) {
	raw := `From: sender@example.com
To: recipient@example.com
Subject: Test Subject
Date: Mon, 1 Jan 2024 12:00:00 +0000

Hello World
`

	wfe := ParseWireFormat("test-id", strings.NewReader(raw))

	if wfe.From == nil || wfe.From.Address != "sender@example.com" {
		t.Errorf("From = %v, want sender@example.com", wfe.From)
	}
	if len(wfe.To) != 1 || wfe.To[0].Address != "recipient@example.com" {
		t.Errorf("To = %v, want [recipient@example.com]", wfe.To)
	}
	if wfe.Subject != "Test Subject" {
		t.Errorf("Subject = %q, want %q", wfe.Subject, "Test Subject")
	}
	if wfe.Date.IsZero() {
		t.Error("Date should be parsed")
	}
	if wfe.BodyStructure == nil {
		t.Fatal("BodyStructure should not be nil")
	}
	if !strings.Contains(string(wfe.BodyStructure.Body), "Hello World") {
		t.Errorf("Body = %q, want to contain Hello World", string(wfe.BodyStructure.Body))
	}
	if wfe.HasErrors() {
		t.Errorf("Unexpected parse errors: %s", wfe.ErrorSummary())
	}
}

func TestWireFormat_ParseMultipart(t *testing.T) {
	raw := `From: sender@example.com
To: recipient@example.com
Subject: Multipart Test
Date: Mon, 1 Jan 2024 12:00:00 +0000
MIME-Version: 1.0
Content-Type: multipart/alternative; boundary="boundary123"

--boundary123
Content-Type: text/plain; charset=utf-8

Plain text body

--boundary123
Content-Type: text/html; charset=utf-8

<html><body><p>HTML body</p></body></html>

--boundary123--
`

	wfe := ParseWireFormat("test-id", strings.NewReader(raw))

	if wfe.BodyStructure == nil {
		t.Fatal("BodyStructure should not be nil")
	}
	if !strings.HasPrefix(wfe.BodyStructure.MediaType, "multipart/") {
		t.Errorf("MediaType = %q, want multipart/*", wfe.BodyStructure.MediaType)
	}
	if len(wfe.BodyStructure.Parts) != 2 {
		t.Errorf("Parts count = %d, want 2", len(wfe.BodyStructure.Parts))
	}

	// Check text part
	foundText := false
	foundHTML := false
	for _, part := range wfe.BodyStructure.Parts {
		if part.MediaType == "text/plain" {
			foundText = true
			if !strings.Contains(string(part.Body), "Plain text body") {
				t.Errorf("Text part body = %q, want to contain 'Plain text body'", string(part.Body))
			}
		}
		if part.MediaType == "text/html" {
			foundHTML = true
			if !strings.Contains(string(part.Body), "HTML body") {
				t.Errorf("HTML part body = %q, want to contain 'HTML body'", string(part.Body))
			}
		}
	}
	if !foundText {
		t.Error("Missing text/plain part")
	}
	if !foundHTML {
		t.Error("Missing text/html part")
	}
}

func TestWireFormat_ParseBase64(t *testing.T) {
	raw := `From: sender@example.com
To: recipient@example.com
Subject: Base64 Test
Date: Mon, 1 Jan 2024 12:00:00 +0000
Content-Type: text/plain; charset=utf-8
Content-Transfer-Encoding: base64

SGVsbG8gV29ybGQh
`

	wfe := ParseWireFormat("test-id", strings.NewReader(raw))

	if wfe.BodyStructure == nil {
		t.Fatal("BodyStructure should not be nil")
	}
	if wfe.BodyStructure.TransferEncoding != "base64" {
		t.Errorf("TransferEncoding = %q, want base64", wfe.BodyStructure.TransferEncoding)
	}
	if !strings.Contains(string(wfe.BodyStructure.Body), "Hello World!") {
		t.Errorf("Decoded body = %q, want to contain Hello World!", string(wfe.BodyStructure.Body))
	}
}

func TestWireFormat_ParseQuotedPrintable(t *testing.T) {
	raw := `From: sender@example.com
To: recipient@example.com
Subject: QP Test
Date: Mon, 1 Jan 2024 12:00:00 +0000
Content-Type: text/plain; charset=utf-8
Content-Transfer-Encoding: quoted-printable

Hello=20World=21
`

	wfe := ParseWireFormat("test-id", strings.NewReader(raw))

	if wfe.BodyStructure == nil {
		t.Fatal("BodyStructure should not be nil")
	}
	if wfe.BodyStructure.TransferEncoding != "quoted-printable" {
		t.Errorf("TransferEncoding = %q, want quoted-printable", wfe.BodyStructure.TransferEncoding)
	}
	if !strings.Contains(string(wfe.BodyStructure.Body), "Hello World!") {
		t.Errorf("Decoded body = %q, want to contain Hello World!", string(wfe.BodyStructure.Body))
	}
}

func TestWireFormat_ParseWithDisplayNames(t *testing.T) {
	raw := `From: "Sender Name" <sender@example.com>
To: "Recipient Name" <recipient@example.com>
Subject: Test
Date: Mon, 1 Jan 2024 12:00:00 +0000

Body
`

	wfe := ParseWireFormat("test-id", strings.NewReader(raw))

	if wfe.From == nil || wfe.From.Name != "Sender Name" {
		t.Errorf("From.Name = %v, want 'Sender Name'", wfe.From)
	}
	if len(wfe.To) != 1 || wfe.To[0].Name != "Recipient Name" {
		t.Errorf("To[0].Name = %v, want 'Recipient Name'", wfe.To[0])
	}
}

func TestWireFormat_ParseMultipleAddresses(t *testing.T) {
	raw := `From: sender@example.com
To: a@example.com, b@example.com
Cc: c@example.com
Bcc: d@example.com
Subject: Test
Date: Mon, 1 Jan 2024 12:00:00 +0000

Body
`

	wfe := ParseWireFormat("test-id", strings.NewReader(raw))

	if len(wfe.To) != 2 {
		t.Errorf("To count = %d, want 2", len(wfe.To))
	}
	if len(wfe.Cc) != 1 || wfe.Cc[0].Address != "c@example.com" {
		t.Errorf("Cc = %v, want [c@example.com]", wfe.Cc)
	}
	if len(wfe.Bcc) != 1 || wfe.Bcc[0].Address != "d@example.com" {
		t.Errorf("Bcc = %v, want [d@example.com]", wfe.Bcc)
	}
}

func TestWireFormat_ParseNestedMultipart(t *testing.T) {
	raw := `From: sender@example.com
To: recipient@example.com
Subject: Nested
Date: Mon, 1 Jan 2024 12:00:00 +0000
MIME-Version: 1.0
Content-Type: multipart/mixed; boundary="outer"

--outer
Content-Type: multipart/alternative; boundary="inner"

--inner
Content-Type: text/plain

Plain

--inner
Content-Type: text/html

<html>HTML</html>

--inner--
--outer--
`

	wfe := ParseWireFormat("test-id", strings.NewReader(raw))

	if wfe.BodyStructure == nil {
		t.Fatal("BodyStructure should not be nil")
	}
	if !strings.HasPrefix(wfe.BodyStructure.MediaType, "multipart/mixed") {
		t.Errorf("Outer MediaType = %q, want multipart/mixed", wfe.BodyStructure.MediaType)
	}
	if len(wfe.BodyStructure.Parts) != 1 {
		t.Errorf("Outer parts count = %d, want 1", len(wfe.BodyStructure.Parts))
	}

	inner := wfe.BodyStructure.Parts[0]
	if inner == nil {
		t.Fatal("Inner part should not be nil")
	}
	if !strings.HasPrefix(inner.MediaType, "multipart/alternative") {
		t.Errorf("Inner MediaType = %q, want multipart/alternative", inner.MediaType)
	}
	if len(inner.Parts) != 2 {
		t.Errorf("Inner parts count = %d, want 2", len(inner.Parts))
	}

	foundText := false
	foundHTML := false
	for _, part := range inner.Parts {
		if part.MediaType == "text/plain" && strings.TrimSpace(string(part.Body)) == "Plain" {
			foundText = true
		}
		if part.MediaType == "text/html" && strings.TrimSpace(string(part.Body)) == "<html>HTML</html>" {
			foundHTML = true
		}
	}
	if !foundText {
		t.Error("Missing text/plain part with body 'Plain'")
	}
	if !foundHTML {
		t.Error("Missing text/html part with body '<html>HTML</html>'")
	}
}

func TestWireFormat_ParseISO8859_1(t *testing.T) {
	raw := "From: sender@example.com\r\n" +
		"To: recipient@example.com\r\n" +
		"Subject: =?iso-8859-1?q?caf=E9?=\r\n" +
		"Date: Mon, 1 Jan 2024 12:00:00 +0000\r\n" +
		"Content-Type: text/plain; charset=iso-8859-1\r\n" +
		"\r\n" +
		"caf\xE9"

	wfe := ParseWireFormat("test-id", strings.NewReader(raw))

	if wfe.Subject != "café" {
		t.Errorf("Subject = %q, want %q", wfe.Subject, "café")
	}
	if wfe.BodyStructure == nil {
		t.Fatal("BodyStructure should not be nil")
	}
	if wfe.BodyStructure.Charset != "iso-8859-1" {
		t.Errorf("Charset = %q, want iso-8859-1", wfe.BodyStructure.Charset)
	}
	// BodyStructure.Body contains raw bytes; charset decoding happens on extraction
	text, _ := wfe.extractTextAndHTML()
	if !strings.Contains(text, "café") {
		t.Errorf("Extracted text = %q, want to contain café", text)
	}
}

func TestWireFormat_ParseMissingHeaders(t *testing.T) {
	raw := `From: sender@example.com

Body only
`

	wfe := ParseWireFormat("test-id", strings.NewReader(raw))

	if wfe.Subject != "" {
		t.Errorf("Subject = %q, want empty", wfe.Subject)
	}
	if !wfe.Date.IsZero() {
		t.Errorf("Date = %v, want zero time", wfe.Date)
	}
	if wfe.BodyStructure == nil {
		t.Fatal("BodyStructure should not be nil")
	}
	if !strings.Contains(string(wfe.BodyStructure.Body), "Body only") {
		t.Errorf("Body = %q, want to contain 'Body only'", string(wfe.BodyStructure.Body))
	}
}

func TestWireFormat_HeadersPreserved(t *testing.T) {
	raw := `From: sender@example.com
To: recipient@example.com
Subject: Test
Date: Mon, 1 Jan 2024 12:00:00 +0000
X-Custom-Header: custom-value
X-Another-Header: another-value

Body
`

	wfe := ParseWireFormat("test-id", strings.NewReader(raw))

	foundCustom := false
	foundAnother := false
	for _, h := range wfe.Headers {
		if h.Name == "X-Custom-Header" && h.Value == "custom-value" {
			foundCustom = true
		}
		if h.Name == "X-Another-Header" && h.Value == "another-value" {
			foundAnother = true
		}
	}
	if !foundCustom {
		t.Error("X-Custom-Header not preserved")
	}
	if !foundAnother {
		t.Error("X-Another-Header not preserved")
	}
}

func TestWireFormat_ParseErrorRecovery(t *testing.T) {
	// Missing boundary in multipart
	raw := `From: sender@example.com
To: recipient@example.com
Subject: Bad Multipart
Date: Mon, 1 Jan 2024 12:00:00 +0000
Content-Type: multipart/alternative

This body has no boundary
`

	wfe := ParseWireFormat("test-id", strings.NewReader(raw))

	if !wfe.HasErrors() {
		t.Error("Expected parse error for missing boundary")
	}
	// Should still have some body content
	if wfe.BodyStructure == nil {
		t.Error("BodyStructure should not be nil even with errors")
	}
}

func TestWireFormat_ParseInvalidDate(t *testing.T) {
	raw := `From: sender@example.com
To: recipient@example.com
Subject: Test
Date: not-a-date

Body
`

	wfe := ParseWireFormat("test-id", strings.NewReader(raw))

	// Date should be zero time
	if !wfe.Date.IsZero() {
		t.Errorf("Date = %v, want zero time", wfe.Date)
	}
	// Should not have errors for invalid date (we handle it gracefully)
	// but check that parsing continued
	if wfe.BodyStructure == nil {
		t.Error("BodyStructure should not be nil")
	}
}

func TestWireFormat_ToUserEmail(t *testing.T) {
	raw := `From: "Sender Name" <sender@example.com>
To: "Recipient Name" <recipient@example.com>
Cc: cc@example.com
Subject: Test Subject
Date: Mon, 1 Jan 2024 12:00:00 +0000
MIME-Version: 1.0
Content-Type: multipart/alternative; boundary="boundary123"

--boundary123
Content-Type: text/plain; charset=utf-8

Plain text body

--boundary123
Content-Type: text/html; charset=utf-8

<html><body><p>HTML body</p></body></html>

--boundary123--
`

	wfe := ParseWireFormat("test-id", strings.NewReader(raw))
	userEmail := wfe.ToUserEmail("test-id")

	if userEmail.ID != "test-id" {
		t.Errorf("ID = %q, want %q", userEmail.ID, "test-id")
	}
	if userEmail.From.Name != "Sender Name" || userEmail.From.Address != "sender@example.com" {
		t.Errorf("From = %v, want {Sender Name, sender@example.com}", userEmail.From)
	}
	if len(userEmail.To) != 1 || userEmail.To[0].Name != "Recipient Name" {
		t.Errorf("To = %v, want [Recipient Name <recipient@example.com>]", userEmail.To)
	}
	if len(userEmail.Cc) != 1 || userEmail.Cc[0].Address != "cc@example.com" {
		t.Errorf("Cc = %v, want [cc@example.com]", userEmail.Cc)
	}
	if userEmail.Subject != "Test Subject" {
		t.Errorf("Subject = %q, want %q", userEmail.Subject, "Test Subject")
	}
	if !strings.Contains(userEmail.Text, "Plain text body") {
		t.Errorf("Text = %q, want to contain 'Plain text body'", userEmail.Text)
	}
	if !strings.Contains(userEmail.HTML, "HTML body") {
		t.Errorf("HTML = %q, want to contain 'HTML body'", userEmail.HTML)
	}
}

func TestWireFormat_ParseMessageIDAndReferences(t *testing.T) {
	raw := `From: sender@example.com
To: recipient@example.com
Subject: Test
Date: Mon, 1 Jan 2024 12:00:00 +0000
Message-ID: <msg1@example.com>
In-Reply-To: <msg0@example.com>
References: <msg0@example.com> <msg-1@example.com>

Body
`

	wfe := ParseWireFormat("test-id", strings.NewReader(raw))

	if wfe.MessageID != "<msg1@example.com>" {
		t.Errorf("MessageID = %q, want <msg1@example.com>", wfe.MessageID)
	}
	if len(wfe.InReplyTo) != 1 || wfe.InReplyTo[0] != "<msg0@example.com>" {
		t.Errorf("InReplyTo = %v, want [<msg0@example.com>]", wfe.InReplyTo)
	}
	if len(wfe.References) != 2 || wfe.References[0] != "<msg0@example.com>" || wfe.References[1] != "<msg-1@example.com>" {
		t.Errorf("References = %v, want [<msg0@example.com> <msg-1@example.com>]", wfe.References)
	}
}

func TestWireFormat_ParseContentDisposition(t *testing.T) {
	raw := `From: sender@example.com
To: recipient@example.com
Subject: Attachment Test
Date: Mon, 1 Jan 2024 12:00:00 +0000
MIME-Version: 1.0
Content-Type: multipart/mixed; boundary="boundary123"

--boundary123
Content-Type: text/plain; charset=utf-8

Body text

--boundary123
Content-Type: application/pdf; name="document.pdf"
Content-Transfer-Encoding: base64
Content-Disposition: attachment; filename="document.pdf"

JVBERi0xLjQKJ...

--boundary123--
`

	wfe := ParseWireFormat("test-id", strings.NewReader(raw))

	if wfe.BodyStructure == nil || len(wfe.BodyStructure.Parts) != 2 {
		t.Fatal("Expected 2 parts")
	}

	// Check attachment part
	attachment := wfe.BodyStructure.Parts[1]
	if attachment.Disposition != "attachment" {
		t.Errorf("Disposition = %q, want attachment", attachment.Disposition)
	}
	if attachment.Filename != "document.pdf" {
		t.Errorf("Filename = %q, want document.pdf", attachment.Filename)
	}
	if attachment.MediaType != "application/pdf" {
		t.Errorf("MediaType = %q, want application/pdf", attachment.MediaType)
	}
}

func TestWireFormat_RoundTrip(t *testing.T) {
	raw := `From: sender@example.com
To: recipient@example.com
Subject: Round Trip Test
Date: Mon, 1 Jan 2024 12:00:00 +0000
Content-Type: text/plain; charset=utf-8

Hello World
`

	wfe := ParseWireFormat("test-id", strings.NewReader(raw))
	serialized, err := wfe.ToWireFormat()
	if err != nil {
		t.Fatalf("ToWireFormat error = %v", err)
	}

	// Parse the serialized output
	wfe2 := ParseWireFormat("test-id", strings.NewReader(string(serialized)))

	if wfe2.From == nil || wfe2.From.Address != "sender@example.com" {
		t.Errorf("From = %v, want sender@example.com", wfe2.From)
	}
	if wfe2.Subject != "Round Trip Test" {
		t.Errorf("Subject = %q, want %q", wfe2.Subject, "Round Trip Test")
	}
	if !strings.Contains(string(wfe2.BodyStructure.Body), "Hello World") {
		t.Errorf("Body = %q, want to contain Hello World", string(wfe2.BodyStructure.Body))
	}
}

func TestWireFormat_ErrorSummary(t *testing.T) {
	raw := `From: sender@example.com
To: recipient@example.com
Subject: Bad Multipart
Date: Mon, 1 Jan 2024 12:00:00 +0000
Content-Type: multipart/alternative

No boundary
`

	wfe := ParseWireFormat("test-id", strings.NewReader(raw))

	summary := wfe.ErrorSummary()
	if summary == "" {
		t.Error("ErrorSummary should not be empty when there are errors")
	}
	if !strings.Contains(summary, "multipart") {
		t.Errorf("ErrorSummary should mention multipart: %s", summary)
	}
}