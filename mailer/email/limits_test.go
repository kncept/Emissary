package email

import (
	"strings"
	"testing"
)

func withMaxPartSize(t *testing.T, limit int64) {
	t.Helper()
	original := MaxPartSize()
	SetMaxPartSize(limit)
	t.Cleanup(func() { SetMaxPartSize(original) })
}

func TestParseWireFormatRejectsOversizedMessage(t *testing.T) {
	withMaxPartSize(t, 64)

	raw := "From: a@example.com\r\nTo: b@example.com\r\nSubject: " + strings.Repeat("x", 80) + "\r\n\r\nbody"
	wfe := ParseWireFormat("id", strings.NewReader(raw))
	if !wfe.TooLarge {
		t.Fatal("TooLarge = false for a message over the cap")
	}
	if len(wfe.Errs) == 0 || wfe.Errs[0].Section != "message" {
		t.Fatalf("expected a message-size parse error, got %+v", wfe.Errs)
	}
}

func TestParseReturnsErrorForOversizedMessage(t *testing.T) {
	withMaxPartSize(t, 32)

	raw := "From: a@example.com\r\nTo: b@example.com\r\n\r\nbody"
	if _, err := Parse("id", strings.NewReader(raw)); err == nil {
		t.Fatal("Parse() succeeded for an oversized message")
	}
}

func TestReadLimited(t *testing.T) {
	withMaxPartSize(t, 8)

	if data, err := ReadLimited(strings.NewReader("12345678")); err != nil || string(data) != "12345678" {
		t.Fatalf("ReadLimited() = %q, %v; want all 8 bytes", data, err)
	}
	if _, err := ReadLimited(strings.NewReader("123456789")); err == nil {
		t.Fatal("ReadLimited() succeeded for 9 bytes with an 8-byte cap")
	}
}

func TestSetMaxPartSizeIgnoresInvalidValues(t *testing.T) {
	withMaxPartSize(t, 128)

	SetMaxPartSize(0)
	if got := MaxPartSize(); got != 128 {
		t.Errorf("MaxPartSize() = %d after SetMaxPartSize(0), want 128", got)
	}
	SetMaxPartSize(1 << 50)
	if got := MaxPartSize(); got != 128 {
		t.Errorf("MaxPartSize() = %d after an absurd limit, want 128", got)
	}
}
