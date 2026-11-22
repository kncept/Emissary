package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kncept/Emissary/mailer/auth"
	"github.com/kncept/Emissary/mailer/jmap"
)

// TestJMAPSessionState_RFC8620_2 protects the requirement that the Session
// state change whenever any advertised Session property changes, while staying
// identical for identical configuration across independent handlers (cold
// starts).
func TestJMAPSessionState_RFC8620_2(t *testing.T) {
	const (
		publicURL = "https://jmap.example.com"
		eventURL  = "https://stream.example.com"
	)
	claims := &auth.Claims{
		Subject:   "alice@example.com",
		AccountID: auth.AccountIDForEmail("alice@example.com"),
	}
	baseState := jmapSessionStateFor(claims, 10<<20, publicURL, eventURL)
	if baseState == "" || baseState == "session-0" {
		t.Fatalf("Session state = %q, want an opaque fingerprint", baseState)
	}

	// Identical configuration and identity always produce the same token.
	again := jmapSessionStateFor(
		&auth.Claims{Subject: claims.Subject, AccountID: claims.AccountID},
		10<<20, publicURL, eventURL,
	)
	if again != baseState {
		t.Fatalf("identical configuration produced state %q, want %q", again, baseState)
	}

	for _, tc := range []struct {
		name        string
		claims      *auth.Claims
		maxFileSize int64
		public      string
		event       string
	}{
		{"limit", claims, 5 << 20, publicURL, eventURL},
		{"api and download URL", claims, 10 << 20, "https://other.example.com", eventURL},
		{"event source URL", claims, 10 << 20, publicURL, "https://push.example.com"},
		{"username", &auth.Claims{Subject: "bob@example.com", AccountID: auth.AccountIDForEmail("bob@example.com")}, 10 << 20, publicURL, eventURL},
		{"account", &auth.Claims{Subject: claims.Subject, AccountID: auth.AccountIDForEmail("other@example.com")}, 10 << 20, publicURL, eventURL},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := jmapSessionStateFor(tc.claims, tc.maxFileSize, tc.public, tc.event)
			if got == baseState {
				t.Errorf("changing %s did not change the Session state (%q)", tc.name, got)
			}
		})
	}

	// Capability and account-capability maps live in code rather than config,
	// so exercise them through the shared fingerprint helper directly.
	session := buildJMAPSessionMap(claims, 10<<20, publicURL, eventURL)
	before, err := jmap.SessionState(session)
	if err != nil {
		t.Fatal(err)
	}
	session["capabilities"].(map[string]any)[jmap.CapabilityMail] = map[string]any{"extra": true}
	afterCapability, err := jmap.SessionState(session)
	if err != nil {
		t.Fatal(err)
	}
	if afterCapability == before {
		t.Error("changing a capability did not change the Session state")
	}
}

// TestJMAPSessionStateMatchesEnvelope_RFC8620_2 checks that the envelope's
// sessionState is the same token advertised by the Session resource; clients
// compare the two to detect stale data.
func TestJMAPSessionStateMatchesEnvelope_RFC8620_2(t *testing.T) {
	h, _, _ := newHandlerForTest(t, "secret")
	_, token, _ := login(t, h, "admin", "secret")

	req := httptest.NewRequest(http.MethodGet, "/.well-known/jmap", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	var session struct {
		State string `json:"state"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &session); err != nil {
		t.Fatalf("decode Session: %v (body %s)", err, w.Body.String())
	}

	resp := jmapRequest(t, h, token, []string{jmap.CapabilityCore}, []JmapMethodCall{
		{"Core/echo", map[string]any{}, "echo"},
	})
	var envelope struct {
		SessionState string `json:"sessionState"`
	}
	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.SessionState != session.State {
		t.Errorf("envelope sessionState = %q, Session state = %q", envelope.SessionState, session.State)
	}
}
