package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/kncept/Emissary/mailer/auth"
	"github.com/kncept/Emissary/mailer/components/sender"
	"github.com/kncept/Emissary/mailer/components/storage"
)

// newJMAPLimitHandler builds a real handler with a deliberately small body cap
// so the size limit can be exercised without allocating megabytes.
func newJMAPLimitHandler(t *testing.T, maxFileSize int64) http.Handler {
	t.Helper()
	return newJMAPLimitHandlerWithStorage(t, maxFileSize, storage.NewMemoryStorage())
}

func newJMAPLimitHandlerWithStorage(t *testing.T, maxFileSize int64, store storage.Storage) http.Handler {
	t.Helper()
	h, err := newTestHandler(t, handlerTestConfig{
		AuthUser:     "admin",
		AuthPass:     "secret",
		JWTSecret:    "secret",
		JWTSecretTTL: time.Hour,
		MetadataType: "memory",
		Storage:      store,
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

func TestJMAPSessionIsNotCacheable(t *testing.T) {
	h := newJMAPLimitHandler(t, 4096)
	token := jmapToken(t, h)
	w := request(t, h, http.MethodGet, "/.well-known/jmap", "", token)
	if w.Code != http.StatusOK {
		t.Fatalf("session status = %d, want 200", w.Code)
	}
	const want = "no-cache, no-store, must-revalidate"
	if got := w.Header().Get("Cache-Control"); got != want {
		t.Errorf("Session Cache-Control = %q, want %q", got, want)
	}
}

func TestSafeRequestTargetRedactsToken(t *testing.T) {
	const secret = "eyJhbGciOi-secret-value"
	req := httptest.NewRequest(http.MethodGet,
		"/eventsource?token="+secret+"&types=Email&closeafter=state", nil)
	got := safeRequestTarget(req)
	if strings.Contains(got, secret) {
		t.Errorf("safeRequestTarget leaked the token: %q", got)
	}
	if !strings.HasPrefix(got, "/eventsource?") ||
		!strings.Contains(got, "types=Email") ||
		!strings.Contains(got, "closeafter=state") ||
		!strings.Contains(got, "token=%5BREDACTED%5D") {
		t.Errorf("safeRequestTarget = %q, want path with other params and redacted token", got)
	}

	plain := httptest.NewRequest(http.MethodGet, "/eventsource?types=Email", nil)
	if got := safeRequestTarget(plain); got != "/eventsource" {
		t.Errorf("safeRequestTarget without token = %q, want path only", got)
	}
}

func TestJMAPSessionAdvertisesConfiguredLimits(t *testing.T) {
	const limit = 4096
	h := newJMAPLimitHandler(t, limit)
	token := jmapToken(t, h)
	session := fetchJMAPSession(t, h, token)
	accountID := auth.AccountIDForEmail("admin")

	// No stubbed or unsupported capability may be advertised to clients.
	w := request(t, h, http.MethodGet, "/.well-known/jmap", "", token)
	var rawSession struct {
		Capabilities map[string]json.RawMessage `json:"capabilities"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &rawSession); err != nil {
		t.Fatalf("decode raw session: %v (body %s)", err, w.Body.String())
	}
	wantCapabilities := []string{
		"urn:ietf:params:jmap:core",
		"urn:ietf:params:jmap:mail",
		"urn:ietf:params:jmap:submission",
		"urn:ietf:params:jmap:vacationresponse",
	}
	if len(rawSession.Capabilities) != len(wantCapabilities) {
		t.Errorf("capabilities = %v, want exactly %v", rawSession.Capabilities, wantCapabilities)
	}
	for _, capability := range wantCapabilities {
		if _, ok := rawSession.Capabilities[capability]; !ok {
			t.Errorf("capabilities is missing %q", capability)
		}
	}

	for name, core := range map[string]map[string]any{
		"capabilities":        session.Capabilities.Core,
		"accountCapabilities": session.Accounts[accountID].AccountCapabilities.Core,
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

	mail := session.Accounts[accountID].AccountCapabilities.Mail
	wantMail := map[string]any{
		"maxMailboxesPerEmail":       float64(1),
		"maxMailboxDepth":            float64(1),
		"maxSizeMailboxName":         float64(255),
		"maxSizeAttachmentsPerEmail": float64(limit),
		"mayCreateTopLevelMailbox":   true,
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

	submission := session.Accounts[accountID].AccountCapabilities.Submission
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

	// VacationResponse methods are implemented, so the capability is an empty
	// object in both Session and accountCapabilities (RFC 8621 section 1.3.3).
	if session.Capabilities.Vacation == nil {
		t.Error("capabilities is missing vacationresponse")
	} else if len(session.Capabilities.Vacation) != 0 {
		t.Errorf("capabilities.vacationresponse = %v, want empty object", session.Capabilities.Vacation)
	}
	accountVacation := session.Accounts[accountID].AccountCapabilities.Vacation
	if accountVacation == nil {
		t.Error("accountCapabilities is missing vacationresponse")
	} else if len(accountVacation) != 0 {
		t.Errorf("accountCapabilities.vacationresponse = %v, want empty object", accountVacation)
	}

	// RFC 8620: core is server-wide and must not be a primary account.
	if _, ok := session.PrimaryAccounts["urn:ietf:params:jmap:core"]; ok {
		t.Errorf("primaryAccounts must not include core: %v", session.PrimaryAccounts)
	}
	for _, capability := range []string{"urn:ietf:params:jmap:mail", "urn:ietf:params:jmap:submission", "urn:ietf:params:jmap:vacationresponse"} {
		if session.PrimaryAccounts[capability] != accountID {
			t.Errorf("primaryAccounts[%q] = %q, want %q", capability, session.PrimaryAccounts[capability], accountID)
		}
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
		"maxConcurrentUpload":   float64(jmapMaxConcurrentUpload),
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
	if !ok || len(algorithms) != 1 || algorithms[0] != jmapCollationASCIICasemap {
		t.Errorf("%s[collationAlgorithms] = %v, want [%s]", name, core["collationAlgorithms"], jmapCollationASCIICasemap)
	}
	if _, ok := core["maxSizeAttachmentsPerEmail"]; ok {
		t.Errorf("%s must not include maxSizeAttachmentsPerEmail; it belongs in mail accountCapabilities", name)
	}
	if len(core) != len(want)+1 {
		t.Errorf("%s has unexpected keys: %v", name, core)
	}
}

// TestJMAPSessionURLsAreAbsolute protects RFC 8620 section 2: every Session
// URL must be an absolute HTTPS URL, and the EventSource URL may use a
// dedicated streaming origin without changing the API origin.
func TestJMAPSessionURLsAreAbsolute(t *testing.T) {
	h, err := newTestHandler(t, handlerTestConfig{
		AuthUser:           "admin",
		AuthPass:           "secret",
		JWTSecret:          "secret",
		JWTSecretTTL:       time.Hour,
		UserDomain:         "example.com",
		JMAPPublicURL:      "https://jmap.example.net/",
		JMAPEventSourceURL: "https://stream.example.net/",
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	token := jmapToken(t, h)
	session := fetchJMAPSession(t, h, token)

	for name, raw := range map[string]string{
		"apiUrl":         session.APIURL,
		"downloadUrl":    session.DownloadURL,
		"uploadUrl":      session.UploadURL,
		"eventSourceUrl": session.EventSourceURL,
	} {
		parsed, err := url.Parse(raw)
		if err != nil {
			t.Errorf("%s = %q is not a valid URL: %v", name, raw, err)
			continue
		}
		if parsed.Scheme != "https" || parsed.Host == "" {
			t.Errorf("%s = %q, want an absolute https URL", name, raw)
		}
	}

	if !strings.HasPrefix(session.APIURL, "https://jmap.example.net/api/jmap/") {
		t.Errorf("apiUrl = %q, want the configured public origin", session.APIURL)
	}
	if !strings.HasPrefix(session.EventSourceURL, "https://stream.example.net/eventsource") {
		t.Errorf("eventSourceUrl = %q, want the dedicated streaming origin", session.EventSourceURL)
	}
	if strings.Contains(session.EventSourceURL, "jmap.example.net") {
		t.Errorf("eventSourceUrl = %q must not use the REST API origin", session.EventSourceURL)
	}
}

func TestJMAPSessionURLTemplatesMatchRoutes(t *testing.T) {
	const limit = 4096
	h := newJMAPLimitHandler(t, limit)
	token := jmapToken(t, h)
	session := fetchJMAPSession(t, h, token)

	wantTemplates := map[string]string{
		"apiUrl":         testJMAPPublicURL + "/api/jmap/",
		"downloadUrl":    testJMAPPublicURL + "/download/{accountId}/{blobId}/{name}?type={type}",
		"uploadUrl":      testJMAPPublicURL + "/upload/{accountId}",
		"eventSourceUrl": testJMAPPublicURL + "/eventsource?types={types}&closeafter={closeafter}&ping={ping}",
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

	// RFC 8620 section 7.3 requires the EventSource template to expose all
	// three variables; assert them explicitly even though the full string is
	// compared above.
	for _, variable := range []string{"types", "closeafter", "ping"} {
		if !strings.Contains(session.EventSourceURL, "{"+variable+"}") {
			t.Errorf("eventSourceUrl is missing {%s}: %q", variable, session.EventSourceURL)
		}
	}

	// Expanding the advertised templates must upload bytes and download the
	// same bytes back. Blob ids use the same opaque, URL-safe format as email
	// ids, so they need no escaping in a path segment.
	uploadURL := strings.ReplaceAll(session.UploadURL, "{accountId}", "admin")
	if strings.Contains(uploadURL, "{") {
		t.Fatalf("unexpanded variable in uploadUrl: %q", uploadURL)
	}
	uploaded := request(t, h, http.MethodPost, uploadURL, "template round trip", token)
	if uploaded.Code != http.StatusOK {
		t.Fatalf("POST %s status = %d, want 200 (body %s)", uploadURL, uploaded.Code, uploaded.Body.String())
	}
	var result struct {
		BlobID string `json:"blobId"`
	}
	if err := json.Unmarshal(uploaded.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode upload response: %v (body %s)", err, uploaded.Body.String())
	}
	if result.BlobID == "" {
		t.Fatalf("upload response has no blobId: %s", uploaded.Body.String())
	}

	downloadURL := session.DownloadURL
	for name, value := range map[string]string{
		"accountId": "admin",
		"blobId":    result.BlobID,
		"name":      "message.eml",
		"type":      "message%2Frfc822",
	} {
		downloadURL = strings.ReplaceAll(downloadURL, "{"+name+"}", value)
	}
	if strings.Contains(downloadURL, "{") {
		t.Fatalf("unexpanded variable in downloadUrl: %q", downloadURL)
	}
	downloaded := request(t, h, http.MethodGet, downloadURL, "", token)
	if downloaded.Code != http.StatusOK {
		t.Fatalf("GET %s status = %d, want 200 (body %s)", downloadURL, downloaded.Code, downloaded.Body.String())
	}
	if downloaded.Body.String() != "template round trip" {
		t.Errorf("downloaded bytes = %q, want %q", downloaded.Body.String(), "template round trip")
	}
	if got := downloaded.Header().Get("Content-Type"); got != "message/rfc822" {
		t.Errorf("download Content-Type = %q, want message/rfc822", got)
	}
}

func TestJMAPRejectsOversizedRequestBody(t *testing.T) {
	const limit = 128
	h := newJMAPLimitHandler(t, limit)
	token := jmapToken(t, h)

	within := `{"using":["urn:ietf:params:jmap:core"],"methodCalls":[]}`
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

func jmapRequestWithContentType(t *testing.T, h http.Handler, token, contentType, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/jmap/", strings.NewReader(body))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

func jmapJSONArray(prefix string, count int) string {
	items := make([]string, count)
	for i := range items {
		items[i] = fmt.Sprintf("%q", fmt.Sprintf("%s%d", prefix, i))
	}
	return "[" + strings.Join(items, ",") + "]"
}

func jmapJSONObject(prefix string, count int) string {
	items := make([]string, count)
	for i := range items {
		items[i] = fmt.Sprintf("%q:{}", fmt.Sprintf("%s%d", prefix, i))
	}
	return "{" + strings.Join(items, ",") + "}"
}

func assertJMAPProblem(t *testing.T, w *httptest.ResponseRecorder, wantStatus int, wantType, wantLimit string) {
	t.Helper()
	if w.Code != wantStatus {
		t.Fatalf("status = %d, want %d (body %s)", w.Code, wantStatus, w.Body.String())
	}
	if got := w.Header().Get("Content-Type"); got != "application/problem+json" {
		t.Errorf("Content-Type = %q, want application/problem+json", got)
	}

	var problem map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &problem); err != nil {
		t.Fatalf("decode problem details: %v (body %s)", err, w.Body.String())
	}
	if _, legacy := problem["@type"]; legacy {
		t.Errorf("problem details must not contain legacy @type: %v", problem)
	}
	if problem["type"] != wantType {
		t.Errorf("type = %v, want %s", problem["type"], wantType)
	}
	if problem["status"] != float64(wantStatus) {
		t.Errorf("status body = %v, want %d", problem["status"], wantStatus)
	}
	if detail, ok := problem["detail"].(string); !ok || detail == "" {
		t.Errorf("detail = %v, want a non-empty string", problem["detail"])
	}
	if wantLimit == "" {
		if _, present := problem["limit"]; present {
			t.Errorf("limit should not be present: %v", problem)
		}
	} else if problem["limit"] != wantLimit {
		t.Errorf("limit = %v, want %s", problem["limit"], wantLimit)
	}
}

func TestJMAPRequestLevelErrors(t *testing.T) {
	h := newJMAPLimitHandler(t, 1<<20)
	token := jmapToken(t, h)

	validBody := `{"using":["urn:ietf:params:jmap:core"],"methodCalls":[]}`
	manyCalls := make([]string, jmapMaxCallsInRequest+1)
	for i := range manyCalls {
		manyCalls[i] = fmt.Sprintf(`["Core/echo",{},"c%d"]`, i)
	}

	cases := []struct {
		name        string
		contentType string
		body        string
		wantStatus  int
		wantType    string
		wantLimit   string
	}{
		{
			name: "wrong content type", contentType: "text/plain", body: validBody,
			wantStatus: 400, wantType: "urn:ietf:params:jmap:error:notJSON",
		},
		{
			name: "missing content type", contentType: "", body: validBody,
			wantStatus: 400, wantType: "urn:ietf:params:jmap:error:notJSON",
		},
		{
			name: "content type with charset", contentType: "application/json; charset=utf-8", body: validBody,
			wantStatus: 200,
		},
		{
			name: "malformed json", contentType: "application/json", body: `{"using":`,
			wantStatus: 400, wantType: "urn:ietf:params:jmap:error:notJSON",
		},
		{
			name: "trailing json", contentType: "application/json", body: validBody + validBody,
			wantStatus: 400, wantType: "urn:ietf:params:jmap:error:notJSON",
		},
		{
			name: "duplicate member", contentType: "application/json",
			body:       `{"using":[],"using":[],"methodCalls":[]}`,
			wantStatus: 400, wantType: "urn:ietf:params:jmap:error:notJSON",
		},
		{
			name: "top level array", contentType: "application/json", body: `[]`,
			wantStatus: 400, wantType: "urn:ietf:params:jmap:error:notRequest",
		},
		{
			name: "missing using", contentType: "application/json", body: `{"methodCalls":[]}`,
			wantStatus: 400, wantType: "urn:ietf:params:jmap:error:notRequest",
		},
		{
			name: "missing methodCalls", contentType: "application/json", body: `{"using":[]}`,
			wantStatus: 400, wantType: "urn:ietf:params:jmap:error:notRequest",
		},
		{
			name: "using not array", contentType: "application/json",
			body:       `{"using":"urn:ietf:params:jmap:core","methodCalls":[]}`,
			wantStatus: 400, wantType: "urn:ietf:params:jmap:error:notRequest",
		},
		{
			name: "using contains non-string", contentType: "application/json",
			body:       `{"using":[1],"methodCalls":[]}`,
			wantStatus: 400, wantType: "urn:ietf:params:jmap:error:notRequest",
		},
		{
			name: "invocation arity", contentType: "application/json",
			body:       `{"using":[],"methodCalls":[["Core/echo",{}]]}`,
			wantStatus: 400, wantType: "urn:ietf:params:jmap:error:notRequest",
		},
		{
			name: "arguments not object", contentType: "application/json",
			body:       `{"using":[],"methodCalls":[["Core/echo",[],"c1"]]}`,
			wantStatus: 400, wantType: "urn:ietf:params:jmap:error:notRequest",
		},
		{
			name: "arguments null", contentType: "application/json",
			body:       `{"using":[],"methodCalls":[["Core/echo",null,"c1"]]}`,
			wantStatus: 400, wantType: "urn:ietf:params:jmap:error:notRequest",
		},
		{
			name: "method name not string", contentType: "application/json",
			body:       `{"using":[],"methodCalls":[[1,{},"c1"]]}`,
			wantStatus: 400, wantType: "urn:ietf:params:jmap:error:notRequest",
		},
		{
			// RFC 8620 section 3.2 defines the name as a String. "Echo" is a
			// syntactically valid but unknown method, so it is a method-level
			// unknownMethod error with HTTP 200, not a notRequest error.
			name: "syntactically valid unknown method name", contentType: "application/json",
			body:       `{"using":["urn:ietf:params:jmap:core"],"methodCalls":[["Echo",{},"c1"]]}`,
			wantStatus: 200,
		},
		{
			name: "methodCallId not string", contentType: "application/json",
			body:       `{"using":[],"methodCalls":[["Core/echo",{},1]]}`,
			wantStatus: 400, wantType: "urn:ietf:params:jmap:error:notRequest",
		},
		{
			name: "duplicate methodCallId", contentType: "application/json",
			body:       `{"using":[],"methodCalls":[["Core/echo",{},"c1"],["Core/echo",{},"c1"]]}`,
			wantStatus: 400, wantType: "urn:ietf:params:jmap:error:notRequest",
		},
		{
			name: "createdIds null", contentType: "application/json",
			body:       `{"using":[],"methodCalls":[],"createdIds":null}`,
			wantStatus: 400, wantType: "urn:ietf:params:jmap:error:notRequest",
		},
		{
			name: "createdIds wrong value type", contentType: "application/json",
			body:       `{"using":[],"methodCalls":[],"createdIds":{"c1":1}}`,
			wantStatus: 400, wantType: "urn:ietf:params:jmap:error:notRequest",
		},
		{
			name: "createdIds invalid creation id", contentType: "application/json",
			body:       `{"using":[],"methodCalls":[],"createdIds":{"not/id":"id1"}}`,
			wantStatus: 400, wantType: "urn:ietf:params:jmap:error:notRequest",
		},
		{
			name: "createdIds invalid assigned id", contentType: "application/json",
			body:       `{"using":[],"methodCalls":[],"createdIds":{"c1":"not/id"}}`,
			wantStatus: 400, wantType: "urn:ietf:params:jmap:error:notRequest",
		},
		{
			name: "createdIds empty id", contentType: "application/json",
			body:       `{"using":[],"methodCalls":[],"createdIds":{"":"id1"}}`,
			wantStatus: 400, wantType: "urn:ietf:params:jmap:error:notRequest",
		},
		{
			name: "createdIds overlong id", contentType: "application/json",
			body:       fmt.Sprintf(`{"using":[],"methodCalls":[],"createdIds":{%q:"id1"}}`, strings.Repeat("a", 256)),
			wantStatus: 400, wantType: "urn:ietf:params:jmap:error:notRequest",
		},
		{
			name: "createdIds valid URL-safe ids", contentType: "application/json",
			body:       `{"using":["urn:ietf:params:jmap:core"],"methodCalls":[],"createdIds":{"create_1":"-valid-id"}}`,
			wantStatus: 200,
		},
		{
			name: "unknown capability", contentType: "application/json",
			body:       `{"using":["https://example.com/not-supported"],"methodCalls":[]}`,
			wantStatus: 400, wantType: "urn:ietf:params:jmap:error:unknownCapability",
		},
		{
			name: "too many calls", contentType: "application/json",
			body:       `{"using":["urn:ietf:params:jmap:core"],"methodCalls":[` + strings.Join(manyCalls, ",") + `]}`,
			wantStatus: 400, wantType: "urn:ietf:params:jmap:error:limit", wantLimit: "maxCallsInRequest",
		},
		{
			name: "too many get ids", contentType: "application/json",
			body: fmt.Sprintf(`{"using":["urn:ietf:params:jmap:mail"],"methodCalls":[[%q,%s,"g1"]]}`,
				"Email/get", fmt.Sprintf(`{"accountId":"admin","ids":%s}`, jmapJSONArray("e", jmapMaxObjectsInGet+1))),
			wantStatus: 400, wantType: "urn:ietf:params:jmap:error:limit", wantLimit: "maxObjectsInGet",
		},
		{
			name: "too many set objects", contentType: "application/json",
			body: fmt.Sprintf(`{"using":["urn:ietf:params:jmap:mail"],"methodCalls":[[%q,%s,"s1"]]}`,
				"Email/set", fmt.Sprintf(`{"accountId":"admin","create":%s}`,
					jmapJSONObject("c", jmapMaxObjectsInSet+1))),
			wantStatus: 400, wantType: "urn:ietf:params:jmap:error:limit", wantLimit: "maxObjectsInSet",
		},
		{
			name: "combined set objects exceed limit", contentType: "application/json",
			body: fmt.Sprintf(`{"using":["urn:ietf:params:jmap:mail"],"methodCalls":[[%q,%s,"s1"]]}`,
				"Email/set", fmt.Sprintf(`{"accountId":"admin","create":%s,"update":%s,"destroy":%s}`,
					jmapJSONObject("c", jmapMaxObjectsInSet/2),
					jmapJSONObject("u", jmapMaxObjectsInSet/2),
					jmapJSONArray("d", 1))),
			wantStatus: 400, wantType: "urn:ietf:params:jmap:error:limit", wantLimit: "maxObjectsInSet",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := jmapRequestWithContentType(t, h, token, tc.contentType, tc.body)
			if tc.wantStatus == http.StatusOK {
				if w.Code != http.StatusOK {
					t.Fatalf("status = %d, want 200 (body %s)", w.Code, w.Body.String())
				}
				return
			}
			assertJMAPProblem(t, w, tc.wantStatus, tc.wantType, tc.wantLimit)
		})
	}
}

// TestJMAPObjectLimitAfterResultReference verifies that a result reference
// expanding past maxObjectsInGet is rejected as a request-level limit error
// (HTTP 400, type=limit), matching the literal-id case. Core/echo is used to
// produce the oversized id list without needing that many stored emails.
func TestJMAPObjectLimitAfterResultReference(t *testing.T) {
	h := newJMAPLimitHandler(t, 1<<20)
	token := jmapToken(t, h)

	ids := jmapJSONArray("e", jmapMaxObjectsInGet+1)
	body := fmt.Sprintf(`{
		"using":["urn:ietf:params:jmap:core","urn:ietf:params:jmap:mail"],
		"methodCalls":[
			["Core/echo",{"ids":%s},"echo1"],
			["Email/get",{"accountId":"admin","#ids":{"resultOf":"echo1","name":"Core/echo","path":"/ids/*"}},"get1"]
		]
	}`, ids)

	w := jmapRequestWithContentType(t, h, token, "application/json", body)
	assertJMAPProblem(t, w, http.StatusBadRequest, "urn:ietf:params:jmap:error:limit", "maxObjectsInGet")
}

func TestJMAPUnknownMethodRemainsMethodLevelError(t *testing.T) {
	h := newJMAPLimitHandler(t, 1<<20)
	token := jmapToken(t, h)
	body := `{"using":["urn:ietf:params:jmap:core"],"methodCalls":[["Foo/bar",{},"c1"]]}`
	w := jmapRequestWithContentType(t, h, token, "application/json", body)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", w.Code, w.Body.String())
	}

	var response struct {
		MethodResponses [][]json.RawMessage `json:"methodResponses"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v (body %s)", err, w.Body.String())
	}
	if len(response.MethodResponses) != 1 {
		t.Fatalf("methodResponses = %d, want 1", len(response.MethodResponses))
	}
	var name string
	if err := json.Unmarshal(response.MethodResponses[0][0], &name); err != nil || name != "error" {
		t.Errorf("response name = %q, want error", name)
	}
	var args struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(response.MethodResponses[0][1], &args); err != nil {
		t.Fatalf("decode error args: %v", err)
	}
	if args.Type != "unknownMethod" {
		t.Errorf("error type = %q, want unknownMethod", args.Type)
	}
}

func TestJMAPGetPropertySelectionEndToEnd(t *testing.T) {
	h := newJMAPLimitHandler(t, 1<<20)
	token := jmapToken(t, h)

	const selected = `{"using":["urn:ietf:params:jmap:mail"],"methodCalls":[` +
		`["Mailbox/get",{"accountId":"admin","ids":["inbox"],"properties":["name"]},"g1"]]}`
	w := jmapRequestWithContentType(t, h, token, "application/json", selected)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", w.Code, w.Body.String())
	}

	var response struct {
		MethodResponses [][]json.RawMessage `json:"methodResponses"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v (body %s)", err, w.Body.String())
	}
	if len(response.MethodResponses) != 1 {
		t.Fatalf("methodResponses = %d, want 1", len(response.MethodResponses))
	}
	var result struct {
		List []map[string]json.RawMessage `json:"list"`
	}
	if err := json.Unmarshal(response.MethodResponses[0][1], &result); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if len(result.List) != 1 {
		t.Fatalf("list = %d items, want 1", len(result.List))
	}
	for key := range result.List[0] {
		if key != "id" && key != "name" {
			t.Errorf("unexpected property %q in selected mailbox", key)
		}
	}
	if result.List[0]["id"] == nil || result.List[0]["name"] == nil {
		t.Errorf("selected mailbox = %v, want id and name", result.List[0])
	}

	const rejected = `{"using":["urn:ietf:params:jmap:mail"],"methodCalls":[` +
		`["Mailbox/get",{"accountId":"admin","ids":["inbox"],"properties":["notAProperty"]},"g1"]]}`
	w = jmapRequestWithContentType(t, h, token, "application/json", rejected)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", w.Code, w.Body.String())
	}
	response = struct {
		MethodResponses [][]json.RawMessage `json:"methodResponses"`
	}{}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v (body %s)", err, w.Body.String())
	}
	var name string
	if err := json.Unmarshal(response.MethodResponses[0][0], &name); err != nil || name != "error" {
		t.Fatalf("response name = %q, want error", name)
	}
	var args struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(response.MethodResponses[0][1], &args); err != nil {
		t.Fatalf("decode error args: %v", err)
	}
	if args.Type != "invalidArguments" {
		t.Errorf("error type = %q, want invalidArguments", args.Type)
	}
}

// blockingStorage holds a request after its handler has acquired a concurrency
// slot. Only the configured seam blocks; all other storage calls pass through.
type blockingStorage struct {
	storage.Storage
	createdIDsEntered chan struct{}
	uploadEntered     chan struct{}
	release           chan struct{}
}

func (s *blockingStorage) block(entered chan struct{}) {
	if entered == nil {
		return
	}
	select {
	case entered <- struct{}{}:
	default:
	}
	select {
	case <-s.release:
	case <-time.After(5 * time.Second):
	}
}

func (s *blockingStorage) LoadCreatedIDs(ctx context.Context, sessionID string) (map[string]string, string, error) {
	s.block(s.createdIDsEntered)
	return s.Storage.LoadCreatedIDs(ctx, sessionID)
}

func (s *blockingStorage) UploadBlob(ctx context.Context, accountID, uploaderID, contentType string, data []byte, now time.Time) (storage.Blob, error) {
	s.block(s.uploadEntered)
	return s.Storage.UploadBlob(ctx, accountID, uploaderID, contentType, data, now)
}

func waitForBlocked(t *testing.T, entered <-chan struct{}, count int) {
	t.Helper()
	for i := 0; i < count; i++ {
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			t.Fatalf("only %d of %d requests reached the blocking seam", i, count)
		}
	}
}

func TestJMAPMaxConcurrentRequestsLimit(t *testing.T) {
	store := &blockingStorage{
		Storage:           storage.NewMemoryStorage(),
		createdIDsEntered: make(chan struct{}, jmapMaxConcurrentRequests),
		release:           make(chan struct{}),
	}
	h := newJMAPLimitHandlerWithStorage(t, 4096, store)
	token := jmapToken(t, h)
	const body = `{"using":["urn:ietf:params:jmap:core"],"methodCalls":[["Core/echo",{},"c1"]]}`

	results := make(chan *httptest.ResponseRecorder, jmapMaxConcurrentRequests)
	for i := 0; i < jmapMaxConcurrentRequests; i++ {
		go func() { results <- request(t, h, http.MethodPost, "/api/jmap/", body, token) }()
	}
	waitForBlocked(t, store.createdIDsEntered, jmapMaxConcurrentRequests)

	// Every slot is held: the excess request must fail at the HTTP level with
	// the exact advertised limit, not as a method-level error.
	w := jmapRequestWithContentType(t, h, token, "application/json", body)
	assertJMAPProblem(t, w, http.StatusTooManyRequests, "urn:ietf:params:jmap:error:limit", "maxConcurrentRequests")

	close(store.release)
	for i := 0; i < jmapMaxConcurrentRequests; i++ {
		select {
		case held := <-results:
			if held.Code != http.StatusOK {
				t.Errorf("held request status = %d, want 200 (body %s)", held.Code, held.Body.String())
			}
		case <-time.After(5 * time.Second):
			t.Fatal("held JMAP request did not finish after release")
		}
	}
}

func TestJMAPMaxConcurrentUploadLimit(t *testing.T) {
	store := &blockingStorage{
		Storage:       storage.NewMemoryStorage(),
		uploadEntered: make(chan struct{}, jmapMaxConcurrentUpload),
		release:       make(chan struct{}),
	}
	h := newJMAPLimitHandlerWithStorage(t, 4096, store)
	token := jmapToken(t, h)

	results := make(chan *httptest.ResponseRecorder, jmapMaxConcurrentUpload)
	for i := 0; i < jmapMaxConcurrentUpload; i++ {
		go func() {
			results <- binaryRequest(t, h, http.MethodPost, "/upload/admin",
				"payload", "application/octet-stream", token)
		}()
	}
	waitForBlocked(t, store.uploadEntered, jmapMaxConcurrentUpload)

	w := binaryRequest(t, h, http.MethodPost, "/upload/admin", "payload", "application/octet-stream", token)
	assertJMAPProblem(t, w, http.StatusTooManyRequests, "urn:ietf:params:jmap:error:limit", "maxConcurrentUpload")

	close(store.release)
	for i := 0; i < jmapMaxConcurrentUpload; i++ {
		select {
		case held := <-results:
			if held.Code != http.StatusOK {
				t.Errorf("held upload status = %d, want 200 (body %s)", held.Code, held.Body.String())
			}
		case <-time.After(5 * time.Second):
			t.Fatal("held upload did not finish after release")
		}
	}
}
