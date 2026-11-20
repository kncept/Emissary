package storage

import (
	"bytes"
	"context"
	"testing"

	"github.com/kncept/Emissary/mailer/email"
)

// rawMIMEWithAttachment has a custom header, a quoted-printable text part and
// a base64 attachment. Rendering a UserEmail cannot reproduce any of them, so
// stores must keep the original bytes when AddEmail receives them.
const rawMIMEWithAttachment = "From: sender@example.com\r\n" +
	"To: alice@example.com\r\n" +
	"Subject: with attachment\r\n" +
	"X-Custom-Header: keep-me\r\n" +
	"MIME-Version: 1.0\r\n" +
	"Content-Type: multipart/mixed; boundary=\"BOUND\"\r\n" +
	"\r\n" +
	"--BOUND\r\n" +
	"Content-Type: text/plain; charset=utf-8\r\n" +
	"Content-Transfer-Encoding: quoted-printable\r\n" +
	"\r\n" +
	"plain body\r\n" +
	"\r\n" +
	"--BOUND\r\n" +
	"Content-Type: application/octet-stream; name=\"report.bin\"\r\n" +
	"Content-Transfer-Encoding: base64\r\n" +
	"Content-Disposition: attachment; filename=\"report.bin\"\r\n" +
	"\r\n" +
	"AAECAwQFBgcICQ==\r\n" +
	"\r\n" +
	"--BOUND--\r\n"

func assertRawPreserved(t *testing.T, s Storage, id string) {
	t.Helper()
	got, err := s.GetEmailRaw(context.Background(), id)
	if err != nil {
		t.Fatalf("GetEmailRaw() error = %v", err)
	}
	if want := []byte(rawMIMEWithAttachment); !bytes.Equal(got, want) {
		t.Errorf("GetEmailRaw() = %q, want original bytes %q", got, want)
	}
}

func TestS3StorageAddEmailStoresOriginalBytesVerbatim(t *testing.T) {
	fake := newFakeS3(map[string]string{})
	store := &S3Storage{client: fake, bucket: "mail-bucket"}
	message := &email.UserEmail{
		From:    email.Address{Address: "sender@example.com"},
		To:      []email.Address{{Address: "alice@example.com"}},
		Subject: "with attachment",
		Text:    "plain body",
	}

	id, err := store.AddEmail(context.Background(), "alice@example.com", message, []byte(rawMIMEWithAttachment))
	if err != nil {
		t.Fatalf("AddEmail() error = %v", err)
	}
	assertRawPreserved(t, store, id)
}

func TestFileStorageAddEmailStoresOriginalBytesVerbatim(t *testing.T) {
	store, _ := newTestFileStorage(t)
	message := &email.UserEmail{
		From:    email.Address{Address: "sender@example.com"},
		To:      []email.Address{{Address: "alice@example.com"}},
		Subject: "with attachment",
		Text:    "plain body",
	}

	id, err := store.AddEmail(context.Background(), "alice@example.com", message, []byte(rawMIMEWithAttachment))
	if err != nil {
		t.Fatalf("AddEmail() error = %v", err)
	}
	assertRawPreserved(t, store, id)
}

func TestMemoryStorageAddEmailStoresOriginalBytesVerbatim(t *testing.T) {
	store := NewMemoryStorage()
	message := &email.UserEmail{Subject: "with attachment", Text: "plain body"}

	id, err := store.AddEmail(context.Background(), "alice@example.com", message, []byte(rawMIMEWithAttachment))
	if err != nil {
		t.Fatalf("AddEmail() error = %v", err)
	}
	assertRawPreserved(t, store, id)
}
