package handler

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/kncept/Emissary/mailer/sender"
	"github.com/kncept/Emissary/mailer/storage"
)

// newJMAPLimitHandler builds a real handler with a deliberately small body cap
// so the size limit can be exercised without allocating megabytes.
func newJMAPLimitHandler(t *testing.T, maxFileSize int64) http.Handler {
	t.Helper()
	h, err := New(HandlerConfig{
		AuthUser:     "admin",
		AuthPass:     "secret",
		JWTSecret:    "secret",
		JWTSecretTTL: time.Hour,
		MetadataType: "memory",
		Storage:      storage.NewMemoryStorage(),
		UserDomain:   "example.com",
		Sender:       sender.NewInMemoryRecording(),
		UserManager: &testUserManager{
			adminUsername: "admin",
			adminPassword: "secret",
			domain:        "example.com",
		},
		MaxFileSize: maxFileSize,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return h
}

func jmapToken(t *testing.T, h http.Handler) string {
	t.Helper()
	status, token, response := login(t, h, "admin", "secret")
	if status != http.StatusOK {
		t.Fatalf("login status = %d, want 200 (body %v)", status, response)
	}
	return token
}

// jmapTestSession is only the part of the Session object these tests inspect.
type jmapTestSession struct {
	State          string `json:"state"`
	APIURL         string `json:"apiUrl"`
	DownloadURL    string `json:"downloadUrl"`
	UploadURL      string `json:"uploadUrl"`
	EventSourceURL string `json:"eventSourceUrl"`
	Capabilities   struct {
		Core       map[string]any `json:"urn:ietf:params:jmap:core"`
		Mail       map[string]any `json:"urn:ietf:params:jmap:mail"`
		Submission map[string]any `json:"urn:ietf:params:jmap:submission"`
		Vacation   map[string]any `json:"urn:ietf:params:jmap:vacationresponse"`
	} `json:"capabilities"`
	Accounts map[string]struct {
		AccountCapabilities struct {
			Core       map[string]any `json:"urn:ietf:params:jmap:core"`
			Mail       map[string]any `json:"urn:ietf:params:jmap:mail"`
			Submission map[string]any `json:"urn:ietf:params:jmap:submission"`
			Vacation   map[string]any `json:"urn:ietf:params:jmap:vacationresponse"`
		} `json:"accountCapabilities"`
	} `json:"accounts"`
	PrimaryAccounts map[string]string `json:"primaryAccounts"`
}

func fetchJMAPSession(t *testing.T, h http.Handler, token string) jmapTestSession {
	t.Helper()
	w := request(t, h, http.MethodGet, "/.well-known/jmap", "", token)
	if w.Code != http.StatusOK {
		t.Fatalf("session status = %d, want 200 (body %s)", w.Code, w.Body.String())
	}
	var session jmapTestSession
	if err := json.Unmarshal(w.Body.Bytes(), &session); err != nil {
		t.Fatalf("decode session: %v (body %s)", err, w.Body.String())
	}
	return session
}

func TestJMAPSessionAdvertisesConfiguredLimits(t *testing.T) {
	const limit = 4096
	h := newJMAPLimitHandler(t, limit)
	token := jmapToken(t, h)
	session := fetchJMAPSession(t, h, token)

	for name, core := range map[string]map[string]any{
		"capabilities":        session.Capabilities.Core,
		"accountCapabilities": session.Accounts["admin"].AccountCapabilities.Core,
	} {
		assertCoreCapabilities(t, name, core, limit)
	}

	// RFC 8621: the Session-level mail and submission objects are empty;
	// their limits belong to the account capability objects.
	if len(session.Capabilities.Mail) != 0 {
		t.Errorf("capabilities.mail = %v, want empty object", session.Capabilities.Mail)
	}
	if len(session.Capabilities.Submission) != 0 {
		t.Errorf("capabilities.submission = %v, want empty object", session.Capabilities.Submission)
	}

	mail := session.Accounts["admin"].AccountCapabilities.Mail
	wantMail := map[string]any{
		"maxMailboxesPerEmail":       float64(1),
		"maxMailboxDepth":            float64(1),
		"maxSizeMailboxName":         float64(255),
		"maxSizeAttachmentsPerEmail": float64(limit),
		"mayCreateTopLevelMailbox":   false,
	}
	for key, want := range wantMail {
		if mail[key] != want {
			t.Errorf("accountCapabilities.mail[%q] = %v, want %v", key, mail[key], want)
		}
	}
	sortOptions, ok := mail["emailQuerySortOptions"].([]any)
	if !ok || len(sortOptions) != 1 || sortOptions[0] != "receivedAt" {
		t.Errorf("emailQuerySortOptions = %v, want [receivedAt]", mail["emailQuerySortOptions"])
	}
	if len(mail) != len(wantMail)+1 {
		t.Errorf("accountCapabilities.mail has unexpected keys: %v", mail)
	}

	submission := session.Accounts["admin"].AccountCapabilities.Submission
	if submission["maxDelayedSend"] != float64(0) {
		t.Errorf("maxDelayedSend = %v, want 0", submission["maxDelayedSend"])
	}
	extensions, ok := submission["submissionExtensions"].(map[string]any)
	if !ok || len(extensions) != 0 {
		t.Errorf("submissionExtensions = %v, want empty object", submission["submissionExtensions"])
	}
	if len(submission) != 2 {
		t.Errorf("accountCapabilities.submission has unexpected keys: %v", submission)
	}

	// No capability may be advertised when its methods are not implemented.
	if session.Capabilities.Vacation != nil {
		t.Errorf("capabilities includes unimplemented vacationresponse: %v", session.Capabilities.Vacation)
	}
	if session.Accounts["admin"].AccountCapabilities.Vacation != nil {
		t.Errorf("accountCapabilities includes unimplemented vacationresponse")
	}

	// RFC 8620: core is server-wide and must not be a primary account.
	if _, ok := session.PrimaryAccounts["urn:ietf:params:jmap:core"]; ok {
		t.Errorf("primaryAccounts must not include core: %v", session.PrimaryAccounts)
	}
	for _, capability := range []string{"urn:ietf:params:jmap:mail", "urn:ietf:params:jmap:submission"} {
		if session.PrimaryAccounts[capability] != "admin" {
			t.Errorf("primaryAccounts[%q] = %q, want admin", capability, session.PrimaryAccounts[capability])
		}
	}
	if _, ok := session.PrimaryAccounts["urn:ietf:params:jmap:vacationresponse"]; ok {
		t.Errorf("primaryAccounts includes unimplemented vacationresponse: %v", session.PrimaryAccounts)
	}

	if session.State == "" || session.State == "state123" {
		t.Errorf("session state = %q, want a stable non-placeholder token", session.State)
	}

	// The session state must not change between identical requests.
	second := fetchJMAPSession(t, h, token)
	if second.State != session.State {
		t.Errorf("session state changed from %q to %q without any data changing", session.State, second.State)
	}
}

func assertCoreCapabilities(t *testing.T, name string, core map[string]any, limit int64) {
	t.Helper()
	want := map[string]any{
		"maxSizeUpload":         float64(limit),
		"maxConcurrentUpload":   float64(0),
		"maxSizeRequest":        float64(limit),
		"maxConcurrentRequests": float64(4),
		"maxCallsInRequest":     float64(16),
		"maxObjectsInGet":       float64(500),
		"maxObjectsInSet":       float64(500),
	}
	for key, value := range want {
		if core[key] != value {
			t.Errorf("%s[%q] = %v, want %v", name, key, core[key], value)
		}
	}
	algorithms, ok := core["collationAlgorithms"].([]any)
	if !ok || len(algorithms) != 0 {
		t.Errorf("%s[collationAlgorithms] = %v, want empty array", name, core["collationAlgorithms"])
	}
	if len(core) != len(want)+1 {
		t.Errorf("%s has unexpected keys: %v", name, core)
	}
}

func TestJMAPSessionURLTemplatesMatchRoutes(t *testing.T) {
	const limit = 4096
	h := newJMAPLimitHandler(t, limit)
	token := jmapToken(t, h)
	session := fetchJMAPSession(t, h, token)

	wantTemplates := map[string]string{
		"apiUrl":         "/api/jmap/",
		"downloadUrl":    "/download/{accountId}/{blobId}/{name}?type={type}",
		"uploadUrl":      "/upload/{accountId}",
		"eventSourceUrl": "/eventsource?types={types}&closeafter={closeafter}&ping={ping}",
	}
	for name, want := range wantTemplates {
		var got string
		switch name {
		case "apiUrl":
			got = session.APIURL
		case "downloadUrl":
			got = session.DownloadURL
		case "uploadUrl":
			got = session.UploadURL
		case "eventSourceUrl":
			got = session.EventSourceURL
		}
		if got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}

	// Expanding each advertised template must reach a registered route. The
	// handlers are stubs today, but they must return the JMAP stub error
	// rather than the SPA catch-all or a 404.
	cases := []struct {
		method   string
		template string
		values   map[string]string
	}{
		{http.MethodPost, session.UploadURL, map[string]string{"accountId": "admin"}},
		{http.MethodGet, session.DownloadURL, map[string]string{
			"accountId": "admin",
			"blobId":    "blob",
			"name":      "message.eml",
			"type":      "message%2Frfc822",
		}},
		{http.MethodGet, session.EventSourceURL, map[string]string{
			"types":      "*",
			"closeafter": "no",
			"ping":       "0",
		}},
	}
	for _, tc := range cases {
		target := tc.template
		for name, value := range tc.values {
			target = strings.ReplaceAll(target, "{"+name+"}", value)
		}
		if strings.Contains(target, "{") {
			t.Fatalf("unexpanded variable in %s: %q", tc.template, target)
		}
		w := request(t, h, tc.method, target, "", token)
		if w.Code != http.StatusOK {
			t.Errorf("%s %s status = %d, want 200 (body %s)", tc.method, target, w.Code, w.Body.String())
		}
		if !strings.Contains(w.Body.String(), `"type":"unimplemented"`) {
			t.Errorf("%s %s body = %s, want the JMAP unimplemented error", tc.method, target, w.Body.String())
		}
	}
}

func TestJMAPRejectsOversizedRequestBody(t *testing.T) {
	const limit = 128
	h := newJMAPLimitHandler(t, limit)
	token := jmapToken(t, h)

	within := `{"using":["urn:ietf:params:jmap:core"],"calls":[],"id":"small"}`
	if len(within) > limit {
		t.Fatalf("test fixture is %d bytes, larger than the %d byte cap", len(within), limit)
	}
	w := request(t, h, http.MethodPost, "/api/jmap/", within, token)
	if w.Code != http.StatusOK {
		t.Fatalf("small request status = %d, want 200 (body %s)", w.Code, w.Body.String())
	}

	oversized := `{"using":["urn:ietf:params:jmap:core"],"calls":[],"id":"` + strings.Repeat("x", limit) + `"}`
	w = request(t, h, http.MethodPost, "/api/jmap/", oversized, token)
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized request status = %d, want 413 (body %s)", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Content-Type"); got != "application/problem+json" {
		t.Errorf("Content-Type = %q, want application/problem+json", got)
	}
	var problem struct {
		Type   string `json:"type"`
		Status int    `json:"status"`
		Limit  string `json:"limit"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &problem); err != nil {
		t.Fatalf("decode problem details: %v (body %s)", err, w.Body.String())
	}
	if problem.Type != "urn:ietf:params:jmap:error:limit" {
		t.Errorf("type = %q, want urn:ietf:params:jmap:error:limit", problem.Type)
	}
	if problem.Status != http.StatusRequestEntityTooLarge {
		t.Errorf("status = %d, want %d", problem.Status, http.StatusRequestEntityTooLarge)
	}
	if problem.Limit != "maxSizeRequest" {
		t.Errorf("limit = %q, want maxSizeRequest", problem.Limit)
	}
}
