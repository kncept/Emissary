package email

import (
	"strings"
	"testing"
)

func TestAddressFormatters(t *testing.T) {
	addresses := []Address{
		{Name: "Alice", Address: "alice@example.com"},
		{Address: "  "},
		{Address: "bob@example.net"},
	}
	if got, want := FormatAddressList(addresses), `"Alice" <alice@example.com>, bob@example.net`; got != want {
		t.Errorf("FormatAddressList() = %q, want %q", got, want)
	}
	if got := BareAddresses(addresses); len(got) != 2 || got[0] != "alice@example.com" || got[1] != "bob@example.net" {
		t.Errorf("BareAddresses() = %q, want bare addresses only", got)
	}
}

func TestRenderHeadersPoliciesAndSanitization(t *testing.T) {
	message := &UserEmail{
		From:    Address{Name: "Sender", Address: "sender@example.com"},
		To:      []Address{{Address: "to@example.com"}},
		Subject: "injected\r\nBcc: hidden@example.com",
		Bcc:     []Address{{Address: "hidden@example.com"}},
		Text:    "body",
	}

	stored := RenderHeaders(message, HeaderPolicy{})
	if strings.Contains(stored, "Date:") || strings.Contains(stored, "Message-ID:") {
		t.Errorf("stored headers added Date/Message-ID: %q", stored)
	}
	if !strings.Contains(stored, "Subject: injected  Bcc: hidden@example.com\r\n") {
		t.Errorf("stored headers did not sanitize CRLF: %q", stored)
	}

	wire := RenderHeaders(message, HeaderPolicy{
		EmptyToFallback:          "undisclosed-recipients:;",
		GenerateDate:             true,
		GeneratedMessageIDDomain: "example.com",
	})
	if !strings.Contains(wire, "Date: ") {
		t.Errorf("wire headers missing generated Date: %q", wire)
	}
	if !strings.Contains(wire, "Message-ID: <") || !strings.Contains(wire, "@example.com>\r\n") {
		t.Errorf("wire headers missing generated Message-ID: %q", wire)
	}
	if strings.Contains(wire, "\r\nBcc:") {
		t.Errorf("Bcc written into headers: %q", wire)
	}

	noRecipients := RenderHeaders(&UserEmail{From: Address{Address: "sender@example.com"}}, HeaderPolicy{EmptyToFallback: "undisclosed-recipients:;"})
	if !strings.Contains(noRecipients, "To: undisclosed-recipients:;\r\n") {
		t.Errorf("empty To fallback missing: %q", noRecipients)
	}
}
