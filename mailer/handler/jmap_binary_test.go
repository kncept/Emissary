package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func binaryRequest(t *testing.T, h http.Handler, method, target, body, contentType, token string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

type binaryUploadResponse struct {
	AccountID string `json:"accountId"`
	BlobID    string `json:"blobId"`
	Type      string `json:"type"`
	Size      int64  `json:"size"`
}

func uploadBinary(t *testing.T, h http.Handler, accountID, body, contentType, token string) binaryUploadResponse {
	t.Helper()
	w := binaryRequest(t, h, http.MethodPost, "/upload/"+accountID, body, contentType, token)
	if w.Code != http.StatusOK {
		t.Fatalf("upload status = %d, want 200 (body %s)", w.Code, w.Body.String())
	}
	var response binaryUploadResponse
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode upload response: %v (body %s)", err, w.Body.String())
	}
	return response
}

func TestJMAPBinaryUploadDownloadRoundTrip(t *testing.T) {
	h := newJMAPLimitHandler(t, 4096)
	token := jmapToken(t, h)
	payload := "attachment bytes"

	uploaded := uploadBinary(t, h, "admin", payload, "image/png", token)
	if uploaded.AccountID != "admin" || uploaded.Type != "image/png" || uploaded.Size != int64(len(payload)) || uploaded.BlobID == "" {
		t.Fatalf("upload response = %+v, want account/type/size and a blob id", uploaded)
	}

	download := "/download/admin/" + uploaded.BlobID + "/report.bin?type=text%2Fplain"
	w := binaryRequest(t, h, http.MethodGet, download, "", "", token)
	if w.Code != http.StatusOK {
		t.Fatalf("download status = %d, want 200 (body %s)", w.Code, w.Body.String())
	}
	if w.Body.String() != payload {
		t.Errorf("downloaded bytes = %q, want %q", w.Body.String(), payload)
	}
	if got := w.Header().Get("Content-Type"); got != "text/plain" {
		t.Errorf("Content-Type = %q, want text/plain", got)
	}
	if got := w.Header().Get("Content-Disposition"); !strings.Contains(got, "report.bin") {
		t.Errorf("Content-Disposition = %q, want the requested filename", got)
	}
	if got := w.Header().Get("Content-Length"); got != "16" {
		t.Errorf("Content-Length = %q, want 16", got)
	}
	if got := w.Header().Get("Cache-Control"); !strings.Contains(got, "immutable") {
		t.Errorf("Cache-Control = %q, want an immutable directive", got)
	}

	// Without a type parameter the stored upload type is used.
	w = binaryRequest(t, h, http.MethodGet, "/download/admin/"+uploaded.BlobID+"/report.bin", "", "", token)
	if w.Code != http.StatusOK {
		t.Fatalf("download without type status = %d, want 200", w.Code)
	}
	if got := w.Header().Get("Content-Type"); got != "image/png" {
		t.Errorf("Content-Type without type = %q, want image/png", got)
	}

	w = binaryRequest(t, h, http.MethodGet, "/download/admin/missing/report.bin", "", "", token)
	if w.Code != http.StatusNotFound {
		t.Errorf("missing blob status = %d, want 404 (body %s)", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Content-Type"); got != "application/problem+json" {
		t.Errorf("missing blob Content-Type = %q, want application/problem+json", got)
	}
}

func TestJMAPBinaryAccountScoping(t *testing.T) {
	h := newJMAPLimitHandler(t, 4096)
	adminToken := jmapToken(t, h)
	status, bobToken, _ := login(t, h, "bob@example.com", "anything")
	if status != http.StatusOK {
		t.Fatalf("bob login status = %d, want 200", status)
	}

	w := binaryRequest(t, h, http.MethodPost, "/upload/admin", "not allowed", "text/plain", bobToken)
	if w.Code != http.StatusForbidden {
		t.Errorf("bob upload into admin status = %d, want 403 (body %s)", w.Code, w.Body.String())
	}

	adminBlob := uploadBinary(t, h, "admin", "admin bytes", "text/plain", adminToken)
	w = binaryRequest(t, h, http.MethodGet, "/download/admin/"+adminBlob.BlobID+"/a.txt", "", "", bobToken)
	if w.Code != http.StatusForbidden {
		t.Errorf("bob download of admin blob status = %d, want 403 (body %s)", w.Code, w.Body.String())
	}

	bobBlob := uploadBinary(t, h, "bob@example.com", "bob bytes", "text/plain", bobToken)
	w = binaryRequest(t, h, http.MethodGet, "/download/bob@example.com/"+bobBlob.BlobID+"/b.txt", "", "", adminToken)
	if w.Code != http.StatusForbidden {
		t.Errorf("admin download of bob's unreferenced blob status = %d, want 403 (body %s)", w.Code, w.Body.String())
	}

	// A system manager may upload into another account; that uploader may
	// then download its own unreferenced blob from that account.
	masqueraded := uploadBinary(t, h, "bob@example.com", "manager bytes", "text/plain", adminToken)
	w = binaryRequest(t, h, http.MethodGet, "/download/bob@example.com/"+masqueraded.BlobID+"/b.txt", "", "", adminToken)
	if w.Code != http.StatusOK || w.Body.String() != "manager bytes" {
		t.Errorf("manager download of own blob = %d %q, want 200 manager bytes", w.Code, w.Body.String())
	}
}

func TestJMAPBinaryRejectsOversizedUpload(t *testing.T) {
	const limit = 256
	h := newJMAPLimitHandler(t, limit)
	token := jmapToken(t, h)

	w := binaryRequest(t, h, http.MethodPost, "/upload/admin", strings.Repeat("x", limit+1), "application/octet-stream", token)
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized upload status = %d, want 413 (body %s)", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Content-Type"); got != "application/problem+json" {
		t.Errorf("Content-Type = %q, want application/problem+json", got)
	}
	if !strings.Contains(w.Body.String(), `"limit":"maxSizeUpload"`) {
		t.Errorf("body = %s, want a maxSizeUpload limit error", w.Body.String())
	}
}

func TestJMAPBinaryRejectsHeaderInjectionInType(t *testing.T) {
	h := newJMAPLimitHandler(t, 4096)
	token := jmapToken(t, h)
	uploaded := uploadBinary(t, h, "admin", "safe bytes", "image/png", token)

	w := binaryRequest(t, h, http.MethodGet,
		"/download/admin/"+uploaded.BlobID+"/safe.txt?type=text%2Fplain%0DX-Evil%3A%20yes", "", "", token)
	if w.Code != http.StatusOK {
		t.Fatalf("download status = %d, want 200 (body %s)", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Content-Type"); got != "application/octet-stream" {
		t.Errorf("Content-Type = %q, want application/octet-stream for a malformed type", got)
	}
	for name, values := range w.Header() {
		if strings.EqualFold(name, "X-Evil") {
			t.Errorf("injected header %s = %v", name, values)
		}
	}
}
