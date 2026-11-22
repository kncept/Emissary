package handler

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/kncept/Emissary/mailer/components/storage"
	"github.com/kncept/Emissary/mailer/email"
)

// TestJMAPMailUseCase_MimeFidelity drives the complete HTTP path: a delivered
// multipart message is fetched with every Mail property and its attachment is
// downloaded through the JMAP blob URL. Every derived property is checked
// against the same wire bytes.
func TestJMAPMailUseCase_MimeFidelity(t *testing.T) {
	h, store, _ := newHandlerForTest(t, "secret")
	accountID := "bob@example.com"
	raw := []byte(strings.Join([]string{
		"From: Sender <sender@example.com>",
		"Sender: Delegate <delegate@example.com>",
		"To: " + accountID,
		"Subject: Fidelity fixture",
		"Date: Mon, 02 Jan 2006 15:04:05 -0700",
		"Message-ID: <fidelity@example.com>",
		"X-Fidelity: wire-value",
		"MIME-Version: 1.0",
		`Content-Type: multipart/mixed; boundary="outer"`,
		"",
		"--outer",
		`Content-Type: multipart/alternative; boundary="inner"`,
		"",
		"--inner",
		"Content-Type: text/plain; charset=utf-8",
		"",
		"plain wire body",
		"--inner",
		"Content-Type: text/html; charset=utf-8",
		"",
		"<p>html wire body</p>",
		"--inner--",
		"--outer",
		`Content-Type: application/octet-stream; name="fidelity.txt"`,
		`Content-Disposition: attachment; filename="fidelity.txt"`,
		"Content-Transfer-Encoding: base64",
		"",
		"ZmlkZWxpdHkgYXR0YWNobWVudA==",
		"--outer--",
		"",
	}, "\r\n"))
	id, err := store.AddEmail(context.Background(), accountID, "inbox", &email.UserEmail{
		From:      email.Address{Address: "sender@example.com"},
		To:        []email.Address{{Address: accountID}},
		Subject:   "Fidelity fixture",
		Date:      time.Date(2006, 1, 2, 22, 4, 5, 0, time.UTC),
		MessageID: "<fidelity@example.com>",
		Text:      "plain wire body",
		HTML:      "<p>html wire body</p>",
	}, raw)
	if err != nil {
		t.Fatal(err)
	}
	// This is the same registration the receiver performs on delivery.
	if _, err := storage.RegisterEmailBlobs(context.Background(), store, accountID, raw); err != nil {
		t.Fatalf("RegisterEmailBlobs() error = %v", err)
	}

	_, token, _ := login(t, h, accountID, "anything")
	resp := jmapRequest(t, h, token, []string{"urn:ietf:params:jmap:mail"}, []JmapMethodCall{
		{"Email/get", map[string]interface{}{
			"accountId":          accountID,
			"ids":                []string{id},
			"properties":         []string{"id", "blobId", "size", "bodyStructure", "textBody", "htmlBody", "attachments", "bodyValues", "headers"},
			"fetchAllBodyValues": true,
		}, "get1"},
	})
	var getResult map[string]interface{}
	for _, rawCall := range resp["methodResponses"].([]interface{}) {
		call := rawCall.([]interface{})
		if call[0] == "Email/get" {
			getResult = call[1].(map[string]interface{})
			break
		}
	}
	if getResult == nil {
		t.Fatal("no Email/get response")
	}
	list := getResult["list"].([]interface{})
	if len(list) != 1 {
		t.Fatalf("Email/get list length = %d, want 1", len(list))
	}
	item := list[0].(map[string]interface{})
	if item["blobId"] != storage.BlobID(raw) {
		t.Errorf("blobId = %v, want %q", item["blobId"], storage.BlobID(raw))
	}
	if item["size"].(float64) != float64(len(raw)) {
		t.Errorf("size = %v, want %d", item["size"], len(raw))
	}
	bodyStructure := item["bodyStructure"].(map[string]interface{})
	if bodyStructure["type"] != "multipart/mixed" {
		t.Errorf("bodyStructure.type = %v, want multipart/mixed", bodyStructure["type"])
	}
	attachments := item["attachments"].([]interface{})
	if len(attachments) != 1 {
		t.Fatalf("attachments = %d, want 1", len(attachments))
	}
	attachment := attachments[0].(map[string]interface{})
	blobID, _ := attachment["blobId"].(string)
	if blobID != storage.BlobID([]byte("fidelity attachment")) {
		t.Errorf("attachment blobId = %q, want %q", blobID, storage.BlobID([]byte("fidelity attachment")))
	}
	headers := item["headers"].([]interface{})
	foundHeader := false
	for _, rawHeader := range headers {
		header := rawHeader.(map[string]interface{})
		if header["name"] == "X-Fidelity" && header["value"] == "wire-value" {
			foundHeader = true
		}
	}
	if !foundHeader {
		t.Errorf("headers = %v, want X-Fidelity: wire-value", headers)
	}

	target := "/download/" + accountID + "/" + blobID + "/fidelity.txt?type=application/octet-stream"
	w := request(t, h, http.MethodGet, target, "", token)
	if w.Code != http.StatusOK {
		t.Fatalf("download status = %d, want 200; body %s", w.Code, w.Body.String())
	}
	if w.Body.String() != "fidelity attachment" {
		t.Errorf("download body = %q, want %q", w.Body.String(), "fidelity attachment")
	}
	if got := w.Header().Get("Content-Type"); got != "application/octet-stream" {
		t.Errorf("download Content-Type = %q, want application/octet-stream", got)
	}
}
