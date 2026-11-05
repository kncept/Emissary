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

	email, err := Parse("test-id", strings.NewReader(raw))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}

	if email.ID != "test-id" {
		t.Errorf("ID = %q, want %q", email.ID, "test-id")
	}
	if email.From.Address != "sender@example.com" {
		t.Errorf("From = %q, want %q", email.From.Address, "sender@example.com")
	}
	if len(email.To) != 1 || email.To[0].Address != "recipient@example.com" {
		t.Errorf("To = %v, want [recipient@example.com]", email.To)
	}
	if email.Subject != "Test Subject" {
		t.Errorf("Subject = %q, want %q", email.Subject, "Test Subject")
	}
	if !email.Date.Equal(time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)) {
		t.Errorf("Date = %v, want 2024-01-01 12:00:00 UTC", email.Date)
	}
	if email.Text != "Hello World" {
		t.Errorf("Text = %q, want %q", email.Text, "Hello World")
	}
	if email.HTML != "" {
		t.Errorf("HTML = %q, want empty", email.HTML)
	}
}

func TestParse_WithDisplayNames(t *testing.T) {
	raw := `From: "Sender Name" <sender@example.com>
To: "Recipient Name" <recipient@example.com>
Subject: Test
Date: Mon, 1 Jan 2024 12:00:00 +0000

Body
`

	email, err := Parse("test-id", strings.NewReader(raw))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}

	if email.From.Name != "Sender Name" {
		t.Errorf("From.Name = %q, want %q", email.From.Name, "Sender Name")
	}
	if len(email.To) != 1 || email.To[0].Name != "Recipient Name" {
		t.Errorf("To[0].Name = %q, want %q", email.To[0].Name, "Recipient Name")
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

	email, err := Parse("test-id", strings.NewReader(raw))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}

	if len(email.To) != 2 {
		t.Errorf("To count = %d, want 2", len(email.To))
	}
	if len(email.Cc) != 1 || email.Cc[0].Address != "c@example.com" {
		t.Errorf("Cc = %v, want [c@example.com]", email.Cc)
	}
	if len(email.Bcc) != 1 || email.Bcc[0].Address != "d@example.com" {
		t.Errorf("Bcc = %v, want [d@example.com]", email.Bcc)
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

	email, err := Parse("test-id", strings.NewReader(raw))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}

	if !strings.Contains(email.Text, "Plain text body") {
		t.Errorf("Text missing plain part: %q", email.Text)
	}
	if !strings.Contains(email.HTML, "HTML body") {
		t.Errorf("HTML missing html part: %q", email.HTML)
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

	email, err := Parse("test-id", strings.NewReader(raw))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}

	if email.Text != "Hello World!" {
		t.Errorf("Text = %q, want %q", email.Text, "Hello World!")
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

	email, err := Parse("test-id", strings.NewReader(raw))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}

	if email.Text != "Hello World!" {
		t.Errorf("Text = %q, want %q", email.Text, "Hello World!")
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

	email, err := Parse("test-id", strings.NewReader(raw))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}

	if email.Subject != "café" {
		t.Errorf("Subject = %q, want %q", email.Subject, "café")
	}
	if email.Text != "café" {
		t.Errorf("Text = %q, want %q", email.Text, "café")
	}
}

func TestParse_MissingHeaders(t *testing.T) {
	raw := `From: sender@example.com

Body only
`

	email, err := Parse("test-id", strings.NewReader(raw))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}

	if email.Subject != "" {
		t.Errorf("Subject = %q, want empty", email.Subject)
	}
	// Date should be zero time when missing (parseDate returns zero time on failure)
	if !email.Date.IsZero() {
		t.Errorf("Date = %v, want zero time", email.Date)
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

	email, err := Parse("test-id", strings.NewReader(raw))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}

	if email.Text != "Plain" {
		t.Errorf("Text = %q, want %q", email.Text, "Plain")
	}
	if email.HTML != "<html>HTML</html>" {
		t.Errorf("HTML = %q, want %q", email.HTML, "<html>HTML</html>")
	}
}