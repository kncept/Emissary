package handler

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
