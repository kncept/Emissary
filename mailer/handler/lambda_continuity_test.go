package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/kncept/Emissary/mailer/components/metadata"
	"github.com/kncept/Emissary/mailer/components/sender"
	"github.com/kncept/Emissary/mailer/components/session"
	"github.com/kncept/Emissary/mailer/components/storage"
	"github.com/kncept/Emissary/mailer/jmap/pushsubscription"
)

// TestJMAPCoreUseCase_LambdaContinuity is the Phase 1 use-case test. It drives
// real JMAP batches through two independent http.Handlers built over the same
// file-backed store, with a fresh handler simulating a Lambda cold start. The
// test asserts that created ids, the Email state token and a verified webhook
// subscription all survive the restart.
func TestJMAPCoreUseCase_LambdaContinuity(t *testing.T) {
	store, err := storage.NewFileStorage(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// Sessions and metadata are shared too, so the token and mailbox index
	// minted by the first instance remain visible at the "cold-started" one.
	sessions := session.NewMemorySessionStore()
	metadataStore := metadata.NewMemoryMetadata()

	newApp := func() http.Handler {
		h, err := newTestHandler(t, handlerTestConfig{
			AuthUser:     "admin",
			AuthPass:     "secret",
			JWTSecret:    "secret",
			JWTSecretTTL: time.Hour,
			MetadataType: "memory",
			Metadata:     metadataStore,
			Storage:      store,
			Session:      sessions,
			UserManager: &testUserManager{
				adminUsername: "admin",
				adminPassword: "secret",
				domain:        "example.com",
			},
			UserDomain:  "example.com",
			Sender:      sender.NewInMemoryRecording(),
			MaxFileSize: 10 << 20,
		})
		if err != nil {
			t.Fatalf("New() error = %v", err)
		}
		return h
	}

	account := "bob@example.com"
	using := []string{"urn:ietf:params:jmap:mail"}
	first := newApp()
	_, token, _ := login(t, first, account, "anything")

	// Capture the Email state before creating a message.
	before := jmapRequest(t, first, token, using, []JmapMethodCall{
		{"Email/get", map[string]interface{}{"accountId": account, "ids": []string{}}, "before"},
	})
	oldState := firstResult(t, before)["state"].(string)

	// One batch creates an email with a client-created id and opts in to the
	// createdIds response, mirroring a real JMAP client.
	created := jmapRequest(t, first, token, using, []JmapMethodCall{
		{"Email/set", map[string]interface{}{
			"accountId": account,
			"create": map[string]interface{}{
				"create1": map[string]interface{}{
					"mailboxIds": map[string]bool{"drafts": true},
					"keywords":   map[string]bool{"$draft": true},
					"from":       []map[string]string{{"email": account}},
					"to":         []map[string]string{{"email": "dave@example.com"}},
					"subject":    "lambda continuity",
					"textBody":   []map[string]interface{}{{"partId": "text1", "type": "text/plain", "size": 4}},
					"bodyValues": map[string]interface{}{"text1": map[string]interface{}{"value": "body"}},
				},
			},
		}, "create"},
	}, map[string]string{})
	createdIDs, ok := created["createdIds"].(map[string]interface{})
	if !ok || createdIDs["create1"] == "" {
		t.Fatalf("createdIds = %v, want create1", created["createdIds"])
	}
	emailID := createdIDs["create1"].(string)

	// A verified webhook subscription lives in the same durable store. The
	// subject-scoped credential is what a fresh hub derives from the account
	// on refresh, so it needs no in-memory hint from this instance.
	credential := pushsubscription.CredentialFromSubject(account)
	if err := store.SavePushSubscriptions(context.Background(), credential, []storage.PushSubscription{{
		ID: "webhook-1", URL: "https://push.example.com/state", Verified: true,
		Types: []string{"Email"},
	}}); err != nil {
		t.Fatal(err)
	}

	// Cold start: a brand-new handler over the same store and session table.
	second := newApp()

	// 1. Created ids survive: a fresh instance that opts in to createdIds
	// receives the mapping recorded by the first one, even though the map was
	// never sent on this request.
	resolved := jmapRequest(t, second, token, using, []JmapMethodCall{}, map[string]string{})
	resolvedIDs, ok := resolved["createdIds"].(map[string]interface{})
	if !ok || resolvedIDs["create1"] != emailID {
		t.Fatalf("createdIds after cold start = %v, want create1=%q (full response %v)", resolved["createdIds"], emailID, resolved)
	}

	// 2. The Email state token survives: /changes from the new instance reports
	// the message created by the previous one.
	changes := jmapRequest(t, second, token, using, []JmapMethodCall{
		{"Email/changes", map[string]interface{}{"accountId": account, "sinceState": oldState}, "changes"},
	})
	change := firstResult(t, changes)
	createdList, _ := change["created"].([]interface{})
	found := false
	for _, item := range createdList {
		id, _ := item.(string)
		if id == emailID {
			found = true
		}
	}
	if !found {
		t.Errorf("Email/changes created = %v, want %q", change["created"], emailID)
	}
	if change["newState"] == oldState || change["newState"] == "" {
		t.Errorf("Email/changes newState = %v, want an advanced token", change["newState"])
	}

	// 3. The webhook subscription survives: the cold-started instance reads it
	// from the durable store (delivery across instances is covered by the push
	// package's restart tests).
	subs, err := store.LoadPushSubscriptions(context.Background(), credential)
	if err != nil || len(subs) != 1 || subs[0].ID != "webhook-1" || !subs[0].Verified {
		t.Fatalf("subscriptions after restart = %+v, %v", subs, err)
	}
}

// firstResult returns the arguments object of the first method response.
func firstResult(t *testing.T, resp map[string]interface{}) map[string]interface{} {
	t.Helper()
	responses, _ := resp["methodResponses"].([]interface{})
	if len(responses) == 0 {
		t.Fatalf("no methodResponses in %v", resp)
	}
	call, _ := responses[0].([]interface{})
	if len(call) < 2 {
		t.Fatalf("malformed method response: %v", call)
	}
	result, ok := call[1].(map[string]interface{})
	if !ok {
		raw, _ := json.Marshal(call[1])
		t.Fatalf("method response arguments are not an object: %s", raw)
	}
	return result
}
