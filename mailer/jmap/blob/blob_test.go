package blob

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/kncept/Emissary/mailer/auth"
	"github.com/kncept/Emissary/mailer/components/storage"
	"github.com/kncept/Emissary/mailer/components/usermanager"
	mailemail "github.com/kncept/Emissary/mailer/email"
	"github.com/kncept/Emissary/mailer/jmap/internal/base"
)

const (
	blobAlice = "alice@example.com"
	blobBob   = "bob@example.com"
)

type testUserManager struct{ known map[string]bool }

func (m *testUserManager) Name() string { return "test" }
func (m *testUserManager) Authenticate(_ context.Context, username, _ string) (*usermanager.User, error) {
	return m.Lookup(context.Background(), username)
}
func (m *testUserManager) Lookup(_ context.Context, username string) (*usermanager.User, error) {
	if !m.known[username] {
		return nil, usermanager.ErrInvalidCredentials
	}
	return &usermanager.User{Email: username}, nil
}
func (m *testUserManager) Autocomplete(_ context.Context, _ string) ([]string, error) {
	return nil, nil
}

func newBlobTest(t *testing.T) (*Handler, *base.Deps, *storage.MemoryStorage, context.Context) {
	t.Helper()
	store := storage.NewMemoryStorage()
	deps := &base.Deps{
		Storage: store,
		UserMgr: &testUserManager{known: map[string]bool{blobAlice: true, blobBob: true}},
		Changes: base.NewTracker(store),
	}
	claims := &auth.Claims{Subject: blobAlice, SID: "session-1", EmissaryUserManagement: true}
	return New(deps), deps, store, auth.ContextWithClaims(context.Background(), claims)
}

type copyResult struct {
	FromAccountID string              `json:"fromAccountId"`
	AccountID     string              `json:"accountId"`
	Copied        map[string]string   `json:"copied"`
	NotCopied     map[string]setError `json:"notCopied"`
}

func callBlobCopy(t *testing.T, h *Handler, ctx context.Context, args string) copyResult {
	t.Helper()
	raw, err := h.BlobCopy(ctx, blobAlice, json.RawMessage(args))
	if err != nil {
		t.Fatalf("BlobCopy() error = %v", err)
	}
	var result copyResult
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("decode BlobCopy() response: %v (raw %s)", err, raw)
	}
	return result
}

func TestEmailBackedBlobDownload(t *testing.T) {
	_, _, store, ctx := newBlobTest(t)
	raw := []byte(strings.Join([]string{
		"From: sender@example.com",
		"To: alice@example.com",
		"Subject: Blob fixture",
		"MIME-Version: 1.0",
		`Content-Type: multipart/mixed; boundary="b"`,
		"",
		"--b",
		"Content-Type: text/plain; charset=utf-8",
		"",
		"body",
		"--b",
		`Content-Type: text/plain; name="note.txt"`,
		`Content-Disposition: attachment; filename="note.txt"`,
		"",
		"attachment bytes",
		"--b--",
		"",
	}, "\r\n"))
	id, err := store.AddEmail(ctx, blobAlice, "inbox", &mailemail.UserEmail{
		From:    mailemail.Address{Address: "sender@example.com"},
		To:      []mailemail.Address{{Address: blobAlice}},
		Subject: "Blob fixture",
		Text:    "body",
	}, raw)
	if err != nil {
		t.Fatal(err)
	}
	messageBlobID, err := storage.RegisterEmailBlobs(ctx, store, blobAlice, raw)
	if err != nil {
		t.Fatalf("RegisterEmailBlobs() error = %v", err)
	}
	if messageBlobID != storage.BlobID(raw) {
		t.Fatalf("message blob id = %q, want %q", messageBlobID, storage.BlobID(raw))
	}

	cases := []struct {
		name string
		id   string
		want string
	}{
		{"whole email", messageBlobID, string(raw)},
		{"attachment", storage.BlobID([]byte("attachment bytes")), "attachment bytes"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			meta, reader, err := store.OpenBlob(ctx, blobAlice, tc.id)
			if err != nil {
				t.Fatalf("OpenBlob() error = %v", err)
			}
			defer reader.Close()
			data, err := io.ReadAll(reader)
			if err != nil {
				t.Fatalf("read blob: %v", err)
			}
			if string(data) != tc.want {
				t.Errorf("blob bytes = %q, want %q", data, tc.want)
			}
			if meta.Size != int64(len(tc.want)) {
				t.Errorf("blob size = %d, want %d", meta.Size, len(tc.want))
			}
		})
	}
	// The message itself must remain intact after opening its blobs.
	if _, err := store.GetEmail(ctx, blobAlice, id); err != nil {
		t.Errorf("GetEmail() after blob download: %v", err)
	}
}

func TestBlobCopyCopiesBetweenAccounts(t *testing.T) {
	h, _, store, ctx := newBlobTest(t)
	data := []byte("copy me")
	meta, err := store.PutBlob(ctx, blobAlice, "text/plain", data)
	if err != nil {
		t.Fatalf("PutBlob() error = %v", err)
	}

	result := callBlobCopy(t, h, ctx, `{"fromAccountId":"alice@example.com","accountId":"bob@example.com","blobIds":["`+meta.ID+`"]}`)
	if result.FromAccountID != blobAlice || result.AccountID != blobBob {
		t.Errorf("account ids = %q/%q, want %q/%q", result.FromAccountID, result.AccountID, blobAlice, blobBob)
	}
	if result.Copied[meta.ID] != meta.ID || len(result.NotCopied) != 0 {
		t.Fatalf("copy result = %+v, want copied id %q", result, meta.ID)
	}

	_, copiedData, err := store.GetBlob(ctx, blobBob, meta.ID)
	if err != nil || string(copiedData) != string(data) {
		t.Errorf("GetBlob(dest) = %q, %v", copiedData, err)
	}
	// RFC 8620 section 6.3 has no destroy argument; the source is unchanged.
	if _, _, err := store.GetBlob(ctx, blobAlice, meta.ID); err != nil {
		t.Errorf("original blob was removed by Blob/copy: %v", err)
	}
}

func TestBlobCopyReleasesTemporaryPin(t *testing.T) {
	h, _, store, ctx := newBlobTest(t)
	meta, err := store.PutBlob(ctx, blobAlice, "text/plain", []byte("still referenced"))
	if err != nil {
		t.Fatalf("PutBlob() error = %v", err)
	}
	if _, err := store.AddBlobRefs(ctx, blobAlice, meta.ID, 1); err != nil {
		t.Fatalf("AddBlobRefs() error = %v", err)
	}

	result := callBlobCopy(t, h, ctx, `{"fromAccountId":"alice@example.com","accountId":"bob@example.com","blobIds":["`+meta.ID+`"]}`)
	if result.Copied[meta.ID] != meta.ID {
		t.Fatalf("copy result = %+v, want copied id %q", result, meta.ID)
	}
	if _, _, err := store.GetBlob(ctx, blobAlice, meta.ID); err != nil {
		t.Errorf("referenced blob was removed inside Blob/copy: %v", err)
	}
	// The method's own temporary pin must be released after the call.
	got, err := store.GetBlobMeta(ctx, blobAlice, meta.ID)
	if err != nil || got.Refs != 1 {
		t.Errorf("blob refs = %+v, %v, want the original single reference", got, err)
	}
}

func TestBlobCopyRejectsNonStandardDestroyArgument(t *testing.T) {
	h, _, _, ctx := newBlobTest(t)
	_, err := h.BlobCopy(ctx, blobAlice, json.RawMessage(`{"fromAccountId":"alice@example.com","accountId":"bob@example.com","blobIds":["x"],"onSuccessDestroyOriginal":true}`))
	if err == nil || !strings.HasPrefix(err.Error(), "invalidArguments:") {
		t.Fatalf("BlobCopy() with onSuccessDestroyOriginal error = %v, want invalidArguments", err)
	}
}

func TestBlobCopyReturnsPerRecordNotFound(t *testing.T) {
	h, _, _, ctx := newBlobTest(t)
	result := callBlobCopy(t, h, ctx, `{"fromAccountId":"alice@example.com","accountId":"bob@example.com","blobIds":["missing"]}`)
	got, ok := result.NotCopied["missing"]
	if !ok || got.Type != "notFound" {
		t.Fatalf("notCopied = %+v, want notFound for missing", result.NotCopied)
	}
	if result.Copied != nil {
		t.Errorf("copied = %+v, want null when nothing was copied", result.Copied)
	}
}

func TestBlobCopyHonorsIfFromInState(t *testing.T) {
	h, deps, store, ctx := newBlobTest(t)
	if _, err := store.PutBlob(ctx, blobAlice, "text/plain", []byte("state guarded")); err != nil {
		t.Fatalf("PutBlob() error = %v", err)
	}
	state, err := deps.Changes.State(ctx, blobAlice, base.StateBlob)
	if err != nil {
		t.Fatalf("State() error = %v", err)
	}

	_, err = h.BlobCopy(ctx, blobAlice, json.RawMessage(`{"fromAccountId":"alice@example.com","accountId":"bob@example.com","blobIds":["ignored"],"ifFromInState":"blob-99"}`))
	if err == nil || !strings.HasPrefix(err.Error(), "stateMismatch:") {
		t.Fatalf("BlobCopy() with stale state error = %v, want stateMismatch", err)
	}

	result := callBlobCopy(t, h, ctx, `{"fromAccountId":"alice@example.com","accountId":"bob@example.com","blobIds":["`+storage.BlobID([]byte("state guarded"))+`"],"ifFromInState":"`+state+`"}`)
	if len(result.Copied) != 1 {
		t.Fatalf("copy with current state = %+v, want one copied blob", result)
	}
}

func TestBlobCopyRejectsSameAccount(t *testing.T) {
	h, _, store, ctx := newBlobTest(t)
	meta, err := store.PutBlob(ctx, blobAlice, "text/plain", []byte("do not destroy"))
	if err != nil {
		t.Fatalf("PutBlob() error = %v", err)
	}

	_, err = h.BlobCopy(ctx, blobAlice, json.RawMessage(`{"fromAccountId":"alice@example.com","accountId":"alice@example.com","blobIds":["`+meta.ID+`"]}`))
	if err == nil || !strings.HasPrefix(err.Error(), "invalidArguments:") {
		t.Fatalf("same-account BlobCopy() error = %v, want invalidArguments", err)
	}
	if _, _, err := store.GetBlob(ctx, blobAlice, meta.ID); err != nil {
		t.Fatalf("source blob was destroyed on rejected copy: %v", err)
	}
}

func TestBlobCopyChecksAccounts(t *testing.T) {
	h, _, _, ctx := newBlobTest(t)

	_, err := h.BlobCopy(ctx, blobAlice, json.RawMessage(`{"fromAccountId":"ghost@example.com","accountId":"alice@example.com","blobIds":["x"]}`))
	// An unknown account fails before any per-record processing.
	if err == nil || !strings.HasPrefix(err.Error(), "fromAccountNotFound:") {
		t.Fatalf("unknown source account error = %v, want fromAccountNotFound", err)
	}

	limited := auth.ContextWithClaims(context.Background(), &auth.Claims{Subject: blobAlice, SID: "session-1"})
	_, err = h.BlobCopy(limited, blobAlice, json.RawMessage(`{"fromAccountId":"bob@example.com","accountId":"alice@example.com","blobIds":["x"]}`))
	if err == nil || !strings.HasPrefix(err.Error(), "forbidden:") {
		t.Fatalf("cross-account source error = %v, want forbidden", err)
	}
}

func TestBlobCopyEmailBackedBlobKeepsOriginal(t *testing.T) {
	h, _, store, ctx := newBlobTest(t)
	data := []byte("From: alice@example.com\r\nSubject: Email blob\r\n\r\nbody\r\n")
	emailID, err := store.AddEmail(ctx, blobAlice, "inbox", &mailemail.UserEmail{
		From:    mailemail.Address{Address: blobAlice},
		To:      []mailemail.Address{{Address: blobBob}},
		Subject: "Email blob",
		Text:    "body",
	}, data)
	if err != nil {
		t.Fatalf("AddEmail() error = %v", err)
	}

	result := callBlobCopy(t, h, ctx, `{"fromAccountId":"alice@example.com","accountId":"bob@example.com","blobIds":["`+emailID+`"]}`)
	wantID := storage.BlobID(data)
	if result.Copied[emailID] != wantID {
		t.Fatalf("copy result = %+v, want destination id %q", result, wantID)
	}
	if _, _, err := store.GetBlob(ctx, blobAlice, emailID); err != nil {
		t.Errorf("email-backed blob was destroyed: %v", err)
	}
}
