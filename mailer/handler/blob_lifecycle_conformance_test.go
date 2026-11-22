package handler

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/kncept/Emissary/mailer/components/storage"
	"github.com/kncept/Emissary/mailer/email"
)

// TestBlobSurvivesLastEmailDestroyRFC8620_6 implements the plan's binary
// acceptance case: destroying the last Email that references a blob does not
// remove the bytes immediately. They stay downloadable in the same call and
// for the advertised one-hour minimum, and are collected only after expiry.
func TestBlobSurvivesLastEmailDestroyRFC8620_6(t *testing.T) {
	ctx := context.Background()
	accountID := "bob@example.com"
	store := storage.NewMemoryStorage()
	start := time.Now().UTC()
	now := start
	lifecycle := &storage.BlobLifecycle{
		Store: store,
		Policy: storage.BlobPolicy{
			QuotaBytes:  1 << 20,
			MinLifetime: storage.DefaultBlobMinLifetime,
			Now:         func() time.Time { return now },
		},
	}
	h, err := newTestHandler(t, handlerTestConfig{
		AuthUser:      accountID,
		AuthPass:      "anything",
		JWTSecret:     "secret",
		JWTSecretTTL:  time.Hour,
		Storage:       store,
		BlobLifecycle: lifecycle,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	_, token, _ := login(t, h, accountID, "anything")

	raw := []byte("From: sender@example.com\r\nTo: " + accountID +
		"\r\nSubject: blob lifetime\r\nMessage-ID: <blob-lifetime@example.com>\r\n\r\nbody")
	id, err := store.AddEmail(ctx, accountID, "inbox", &email.UserEmail{
		From:      email.Address{Address: "sender@example.com"},
		To:        []email.Address{{Address: accountID}},
		Subject:   "blob lifetime",
		MessageID: "<blob-lifetime@example.com>",
		Text:      "body",
	}, raw)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := storage.RegisterEmailBlobs(ctx, store, accountID, raw); err != nil {
		t.Fatalf("RegisterEmailBlobs() error = %v", err)
	}
	blobID := storage.BlobID(raw)
	target := "/download/" + accountID + "/" + blobID + "/message.eml?type=message%2Frfc822"

	download := func(phase string, want int) {
		t.Helper()
		w := request(t, h, http.MethodGet, target, "", token)
		if w.Code != want {
			t.Fatalf("%s download status = %d, want %d (body %s)", phase, w.Code, want, w.Body.String())
		}
	}
	download("while referenced", http.StatusOK)

	resp := jmapRequest(t, h, token, []string{"urn:ietf:params:jmap:mail"}, []JmapMethodCall{
		{"Email/set", map[string]any{"accountId": accountID, "destroy": []string{id}}, "set1"},
	})
	setResult := jmapMethodResult(t, resp, "Email/set")
	if _, ok := setResult["destroyed"].([]any); !ok || len(setResult["destroyed"].([]any)) != 1 {
		t.Fatalf("Email/destroy = %v, want one destroyed id", setResult)
	}

	// The last reference is gone, but the blob must still be fetchable now
	// and after a simulated delay shorter than the one-hour minimum.
	download("immediately after destroy", http.StatusOK)
	now = start.Add(30 * time.Minute)
	if err := lifecycle.Collect(ctx, accountID); err != nil {
		t.Fatalf("Collect() before expiry error = %v", err)
	}
	download("before one-hour lifetime", http.StatusOK)

	now = start.Add(2 * time.Hour)
	if err := lifecycle.Collect(ctx, accountID); err != nil {
		t.Fatalf("Collect() after expiry error = %v", err)
	}
	download("after expiry", http.StatusNotFound)
}

// TestBlobSharedByTwoEmailsStaysPinnedRFC8620_6 proves reference counts are
// per Email, not per digest: destroying one of two identical messages must not
// unpin their shared blob.
func TestBlobSharedByTwoEmailsStaysPinnedRFC8620_6(t *testing.T) {
	ctx := context.Background()
	accountID := "bob@example.com"
	store := storage.NewMemoryStorage()
	h, err := newTestHandler(t, handlerTestConfig{
		AuthUser:     accountID,
		AuthPass:     "anything",
		JWTSecret:    "secret",
		JWTSecretTTL: time.Hour,
		Storage:      store,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	_, token, _ := login(t, h, accountID, "anything")

	raw := []byte("From: sender@example.com\r\nTo: " + accountID +
		"\r\nSubject: shared\r\nMessage-ID: <shared@example.com>\r\n\r\nbody")
	first, err := store.AddEmail(ctx, accountID, "inbox", &email.UserEmail{
		From: email.Address{Address: "sender@example.com"},
		To:   []email.Address{{Address: accountID}}, Subject: "shared",
		MessageID: "<shared-1@example.com>", Text: "body",
	}, raw)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.AddEmail(ctx, accountID, "inbox", &email.UserEmail{
		From: email.Address{Address: "sender@example.com"},
		To:   []email.Address{{Address: accountID}}, Subject: "shared",
		MessageID: "<shared-2@example.com>", Text: "body",
	}, raw)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err := storage.RegisterEmailBlobs(ctx, store, accountID, raw); err != nil {
			t.Fatalf("RegisterEmailBlobs() #%d error = %v", i, err)
		}
	}

	blobID := storage.BlobID(raw)
	meta, err := store.GetBlobMeta(ctx, accountID, blobID)
	if err != nil || meta.Refs != 2 {
		t.Fatalf("blob refs = %d, %v; want 2", meta.Refs, err)
	}
	destroyEmail := func(id string) {
		t.Helper()
		resp := jmapRequest(t, h, token, []string{"urn:ietf:params:jmap:mail"}, []JmapMethodCall{
			{"Email/set", map[string]any{"accountId": accountID, "destroy": []string{id}}, "destroy"},
		})
		if got := jmapMethodResult(t, resp, "Email/set")["destroyed"]; got == nil {
			t.Fatalf("Email/destroy(%s) = %v", id, got)
		}
	}

	destroyEmail(first)
	meta, err = store.GetBlobMeta(ctx, accountID, blobID)
	if err != nil || meta.Refs != 1 {
		t.Fatalf("blob refs after one destroy = %d, %v; want 1", meta.Refs, err)
	}
	target := "/download/" + accountID + "/" + blobID + "/message.eml?type=message%2Frfc822"
	if w := request(t, h, http.MethodGet, target, "", token); w.Code != http.StatusOK {
		t.Fatalf("download after first destroy status = %d, want 200", w.Code)
	}

	destroyEmail(second)
	meta, err = store.GetBlobMeta(ctx, accountID, blobID)
	if err != nil || meta.Refs != 0 {
		t.Fatalf("blob refs after both destroys = %d, %v; want 0", meta.Refs, err)
	}
}
