package handler

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestEventSourceRouteAuthenticatesAndStreams exercises the handwired route
// through the real handler, including the query-token transport used by
// browser EventSource clients.
func TestEventSourceRouteAuthenticatesAndStreams(t *testing.T) {
	h, _, _ := newHandlerForTest(t, "secret")
	_, token, _ := login(t, h, "admin", "secret")

	w := request(t, h, http.MethodGet, "/eventsource?types=Email&closeafter=state&ping=0", "", "")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous status = %d, want 401", w.Code)
	}

	w = request(t, h, http.MethodGet, "/eventsource?types=Email&closeafter=maybe&ping=0&token="+token, "", "")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("invalid closeafter status = %d, want 400 (body %s)", w.Code, w.Body.String())
	}

	// An empty Last-Event-ID snapshot means every current type state may have
	// changed, so closeafter=state returns one event immediately.
	emptyID := base64.RawURLEncoding.EncodeToString([]byte("{}"))
	req := httptest.NewRequest(http.MethodGet,
		"/eventsource?types=Email&closeafter=state&ping=0&token="+token, nil)
	req.Header.Set("Last-Event-ID", emptyID)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("eventsource status = %d, body = %s", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Content-Type"); got != "text/event-stream" {
		t.Errorf("Content-Type = %q, want text/event-stream", got)
	}
	body := w.Body.String()
	if !strings.Contains(body, "event: state\n") || !strings.Contains(body, `"Email"`) {
		t.Fatalf("missing StateChange event: %s", body)
	}
	if strings.Contains(body, `"Mailbox"`) {
		t.Fatalf("event leaked unsubscribed type: %s", body)
	}
}

// TestEventSourceRESTProxyNeverStreamsRFC8620_7_3 enforces RESOLVED-1 at the
// HTTP boundary: the API Gateway REST Lambda only serves closeafter=state and
// does not expose the long-lived streaming path at all.
func TestEventSourceRESTProxyNeverStreamsRFC8620_7_3(t *testing.T) {
	h, err := newTestHandler(t, handlerTestConfig{
		AuthUser:      "admin",
		AuthPass:      "secret",
		JWTSecret:     "secret",
		JWTSecretTTL:  time.Hour,
		JMAPRESTProxy: true,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	_, token, _ := login(t, h, "admin", "secret")

	w := request(t, h, http.MethodGet,
		"/eventsource?types=Email&closeafter=no&ping=0&token="+token, "", "")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("closeafter=no status = %d, want 400 (body %s)", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "eventSourceUrl") {
		t.Errorf("error should point clients at the streaming origin: %s", w.Body.String())
	}

	// An omitted closeafter is the REST endpoint's declared default of
	// "state", so it returns one event immediately.
	emptyID := base64.RawURLEncoding.EncodeToString([]byte("{}"))
	req := httptest.NewRequest(http.MethodGet, "/eventsource?types=Email&ping=0&token="+token, nil)
	req.Header.Set("Last-Event-ID", emptyID)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "event: state\n") {
		t.Fatalf("omitted closeafter status = %d, body = %s", w.Code, w.Body.String())
	}

	w = request(t, h, http.MethodGet,
		"/eventsource/stream?types=Email&closeafter=no&ping=0&token="+token, "", "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("stream path on REST proxy status = %d, want 404", w.Code)
	}
}

// TestEventSourceStreamPathServesRFC8620_7_3 checks that the dedicated path
// advertises the RFC media type and accepts the RFC default closeafter=no.
func TestEventSourceStreamPathServesRFC8620_7_3(t *testing.T) {
	h, _, _ := newHandlerForTest(t, "secret")
	_, token, _ := login(t, h, "bob@example.com", "anything")

	emptyID := base64.RawURLEncoding.EncodeToString([]byte("{}"))
	req := httptest.NewRequest(http.MethodGet,
		"/eventsource/stream?types=Email&closeafter=state&ping=0&token="+token, nil)
	req.Header.Set("Last-Event-ID", emptyID)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("stream status = %d, body = %s", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Content-Type"); got != "text/event-stream" {
		t.Errorf("Content-Type = %q, want text/event-stream", got)
	}
	if !strings.Contains(w.Body.String(), "event: state\n") {
		t.Fatalf("missing catch-up state event: %s", w.Body.String())
	}
}
