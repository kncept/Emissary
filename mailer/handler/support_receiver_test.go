package handler

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/ses"
	supportapi "github.com/kncept/Emissary/api/support/generated"
	"github.com/kncept/Emissary/mailer/auth"
	"github.com/kncept/Emissary/mailer/receiver"
	"github.com/kncept/Emissary/mailer/storage"
)

type fakeSESIdentityLister struct {
	pages []*ses.ListIdentitiesOutput
	err   error
}

func (f *fakeSESIdentityLister) ListIdentities(_ context.Context, _ *ses.ListIdentitiesInput, _ ...func(*ses.Options)) (*ses.ListIdentitiesOutput, error) {
	if f.err != nil {
		return nil, f.err
	}
	if len(f.pages) == 0 {
		return &ses.ListIdentitiesOutput{}, nil
	}
	page := f.pages[0]
	f.pages = f.pages[1:]
	return page, nil
}

func newReceiverSupportTestHandler(t *testing.T, lister SESIdentityLister, store receiver.ConfigStore) (http.Handler, receiver.ConfigStore) {
	t.Helper()
	if store == nil {
		store = receiver.NewMemoryConfigStore()
	}
	config, err := receiver.LoadMailIdentityConfig(context.Background(), store)
	if err != nil {
		t.Fatalf("load receiver config: %v", err)
	}
	h, err := New(HandlerConfig{
		AuthUser:     "admin",
		AuthPass:     "secret",
		JWTSecret:    "secret",
		JWTSecretTTL: time.Hour,
		MetadataType: "memory",
		Storage:      storage.NewMemoryStorage(),
		UserManager: &testUserManager{
			adminUsername: "admin",
			adminPassword: "secret",
			domain:        "example.com",
		},
		UserDomain:          "example.com",
		Sender:              &captureSender{},
		ReceiverConfig:      config,
		ReceiverConfigStore: store,

		SESIdentityLister: lister,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return h, store
}

func TestNewSupportHandlerLoadsCurrentRulesFromStore(t *testing.T) {
	ctx := context.Background()
	store := receiver.NewMemoryConfigStore()
	stored := receiver.HandlerRules{
		MailIdentity: "stored.example.com",
		Rules:        []receiver.HandlerRule{receiver.DeliverToMailIdentity, receiver.Bounce},
	}
	if err := store.Put(ctx, "stored.example.com", stored); err != nil {
		t.Fatalf("seed rules: %v", err)
	}

	h := newSupportHandler(ctx, HandlerConfig{
		ReceiverConfig:      receiver.BaseMailIdentityConfig(),
		ReceiverConfigStore: store,
	}, nil, nil, nil, nil, nil)

	config := h.receiverConfig()
	if !config.Has("stored.example.com") {
		t.Fatalf("created cache = %+v, want stored identity", config)
	}
	if !reflect.DeepEqual(config["stored.example.com"].Rules, stored.Rules) {
		t.Errorf("stored rules = %v, want %v", config["stored.example.com"].Rules, stored.Rules)
	}
	if !config.Has(receiver.FallbackIdentity) {
		t.Error("created cache is missing the fallback rules")
	}

	empty := newSupportHandler(ctx, HandlerConfig{
		ReceiverConfigStore: receiver.NewMemoryConfigStore(),
	}, nil, nil, nil, nil, nil)
	fallback := empty.receiverConfig()
	if len(fallback) != 1 || !reflect.DeepEqual(fallback[receiver.FallbackIdentity].Rules, []receiver.HandlerRule{receiver.DeliverToUser, receiver.Bounce}) {
		t.Errorf("empty store cache = %+v, want only the base fallback", fallback)
	}
}

func TestSupportReceiverSaveUpdatesCacheAndStore(t *testing.T) {
	ctx := context.WithValue(context.Background(), claimsContextKey{}, &auth.Claims{SystemManager: true})
	store := receiver.NewMemoryConfigStore()
	other := receiver.HandlerRules{
		MailIdentity: "other.example.com",
		Rules:        []receiver.HandlerRule{receiver.DeliverToUser, receiver.Bounce},
	}
	if err := store.Put(ctx, "other.example.com", other); err != nil {
		t.Fatalf("seed rules: %v", err)
	}

	h := newSupportHandler(ctx, HandlerConfig{ReceiverConfigStore: store}, nil, nil, nil, nil, nil)
	response, err := h.SaveReceiverRules(ctx, &supportapi.ReceiverRulesRequest{
		Rules: []supportapi.ReceiverDomainRules{{
			Domain: "save.example.com",
			Rules: []supportapi.HandlerRule{
				supportapi.HandlerRuleDeliverToMailIdentity,
				supportapi.HandlerRuleBounce,
			},
		}},
	})
	if err != nil {
		t.Fatalf("SaveReceiverRules() error = %v", err)
	}
	if _, ok := response.(*supportapi.ReceiverRulesResponse); !ok {
		t.Fatalf("SaveReceiverRules() response = %T", response)
	}

	stored, err := store.Get(ctx, "save.example.com")
	if err != nil {
		t.Fatalf("saved rules were not persisted: %v", err)
	}
	want := []receiver.HandlerRule{receiver.DeliverToMailIdentity, receiver.Bounce}
	if !reflect.DeepEqual(stored.Rules, want) {
		t.Errorf("stored rules = %v, want %v", stored.Rules, want)
	}
	if _, err := store.Get(ctx, "other.example.com"); err != nil {
		t.Errorf("save dropped untouched store entry: %v", err)
	}

	config := h.receiverConfig()
	if !config.Has("save.example.com") || !reflect.DeepEqual(config["save.example.com"].Rules, want) {
		t.Errorf("published cache = %+v, want saved rules", config)
	}
	if !config.Has("other.example.com") {
		t.Errorf("published cache = %+v, want untouched store entry", config)
	}
}

func TestSupportReceiverCreatesStoreFromConfiguredType(t *testing.T) {
	ctx := context.WithValue(context.Background(), claimsContextKey{}, &auth.Claims{SystemManager: true})
	dir := t.TempDir()
	h := newSupportHandler(ctx, HandlerConfig{
		ReceiverConfigType: "filesystem",
		FilesystemDir:      dir,
	}, nil, nil, nil, nil, nil)
	if h.configStore == nil {
		t.Fatal("configured receiver type did not create a config store")
	}

	receiverDir := filepath.Join(dir, "receiver")
	if _, err := os.Stat(receiverDir); !os.IsNotExist(err) {
		t.Fatalf("creating the handler wrote to %s (err=%v)", receiverDir, err)
	}

	if _, err := h.SaveReceiverRules(ctx, &supportapi.ReceiverRulesRequest{
		Rules: []supportapi.ReceiverDomainRules{{
			Domain: "saved.example.com",
			Rules:  []supportapi.HandlerRule{supportapi.HandlerRuleBounce},
		}},
	}); err != nil {
		t.Fatalf("SaveReceiverRules() error = %v", err)
	}

	path := filepath.Join(dir, "receiver", "saved.example.com.json")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("save did not write %s: %v", path, err)
	}
	if !h.receiverConfig().Has("saved.example.com") {
		t.Error("save did not update the published receiver cache")
	}
}

func TestSupportReceiverIdentitiesMarksRuleCoverage(t *testing.T) {
	lister := &fakeSESIdentityLister{pages: []*ses.ListIdentitiesOutput{
		{Identities: []string{"example.com", "alias.example.com"}, NextToken: strPtr("next")},
		{Identities: []string{"news.example.com"}},
	}}
	store := receiver.NewMemoryConfigStore()
	if err := store.Put(context.Background(), "example.com", receiver.HandlerRules{
		MailIdentity: "example.com",
		Rules:        []receiver.HandlerRule{receiver.DeliverToUser},
	}); err != nil {
		t.Fatalf("seed rules: %v", err)
	}
	h, _ := newReceiverSupportTestHandler(t, lister, store)
	_, token, _ := login(t, h, "admin", "secret")

	w := request(t, h, http.MethodGet, "/api/support/receiver/identities", "", token)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (body %s)", w.Code, http.StatusOK, w.Body.String())
	}
	var response struct {
		Identities []struct {
			Identity string `json:"identity"`
			HasRules bool   `json:"hasRules"`
		} `json:"identities"`
	}
	decodeJSONBody(t, w.Body.Bytes(), &response)
	want := []struct {
		identity string
		hasRules bool
	}{
		{"alias.example.com", false},
		{"example.com", true},
		{"news.example.com", false},
	}
	if len(response.Identities) != len(want) {
		t.Fatalf("identities = %+v, want %d entries", response.Identities, len(want))
	}
	for i, expected := range want {
		if response.Identities[i].Identity != expected.identity || response.Identities[i].HasRules != expected.hasRules {
			t.Errorf("identity[%d] = %+v, want %+v", i, response.Identities[i], expected)
		}
	}
}

func TestSupportReceiverRulesRoundTrip(t *testing.T) {
	lister := &fakeSESIdentityLister{}
	h, store := newReceiverSupportTestHandler(t, lister, nil)
	_, token, _ := login(t, h, "admin", "secret")

	w := request(t, h, http.MethodGet, "/api/support/receiver/rules", "", token)
	if w.Code != http.StatusOK {
		t.Fatalf("GET status = %d, want %d (body %s)", w.Code, http.StatusOK, w.Body.String())
	}
	var response struct {
		Rules []struct {
			Domain string   `json:"domain"`
			Rules  []string `json:"rules"`
		} `json:"rules"`
	}
	decodeJSONBody(t, w.Body.Bytes(), &response)
	if len(response.Rules) != 1 || response.Rules[0].Domain != "_" {
		t.Fatalf("initial rules = %+v, want only the fallback", response.Rules)
	}
	if got := response.Rules[0].Rules; !reflect.DeepEqual(got, []string{"DeliverToUser", "Bounce"}) {
		t.Errorf("fallback rules = %v", got)
	}

	body := `{"rules":[{"domain":"news.example.com","rules":["DeliverToMailIdentity","Bounce"]}]}`
	w = request(t, h, http.MethodPut, "/api/support/receiver/rules", body, token)
	if w.Code != http.StatusOK {
		t.Fatalf("PUT status = %d, want %d (body %s)", w.Code, http.StatusOK, w.Body.String())
	}
	decodeJSONBody(t, w.Body.Bytes(), &response)
	if len(response.Rules) != 2 {
		t.Fatalf("saved rules = %+v, want fallback plus new domain", response.Rules)
	}
	if response.Rules[0].Domain != "_" || response.Rules[1].Domain != "news.example.com" {
		t.Errorf("saved domains = %q, %q", response.Rules[0].Domain, response.Rules[1].Domain)
	}
	if got := response.Rules[1].Rules; !reflect.DeepEqual(got, []string{"DeliverToMailIdentity", "Bounce"}) {
		t.Errorf("saved rules = %v", got)
	}

	// The rules are persisted in the config store, not just in the response.
	stored, err := store.Get(context.Background(), "news.example.com")
	if err != nil {
		t.Fatalf("stored rules: %v", err)
	}
	if stored.MailIdentity != "news.example.com" || !reflect.DeepEqual(stored.Rules, []receiver.HandlerRule{receiver.DeliverToMailIdentity, receiver.Bounce}) {
		t.Errorf("stored rules = %+v", stored)
	}

	// The support configuration view sees the new entry too.
	w = request(t, h, http.MethodGet, "/api/support/configurations/receiver", "", token)
	if w.Code != http.StatusOK {
		t.Fatalf("configuration status = %d", w.Code)
	}
	var detail struct {
		Settings map[string]string `json:"settings"`
	}
	decodeJSONBody(t, w.Body.Bytes(), &detail)
	if got := detail.Settings["rules.news.example.com"]; got != `["DeliverToMailIdentity","Bounce"]` {
		t.Errorf("rules.news.example.com = %q", got)
	}

	// Starting again from the same store is the GUI's Reload behaviour.
	reloaded, _ := newReceiverSupportTestHandler(t, lister, store)
	_, reloadedToken, _ := login(t, reloaded, "admin", "secret")
	w = request(t, reloaded, http.MethodGet, "/api/support/receiver/rules", "", reloadedToken)
	decodeJSONBody(t, w.Body.Bytes(), &response)
	if len(response.Rules) != 2 || response.Rules[1].Domain != "news.example.com" {
		t.Errorf("reloaded rules = %+v", response.Rules)
	}
}

func TestSupportReceiverRulesAcceptsDrop(t *testing.T) {
	lister := &fakeSESIdentityLister{}
	h, store := newReceiverSupportTestHandler(t, lister, nil)
	_, token, _ := login(t, h, "admin", "secret")

	body := `{"rules":[{"domain":"blocked.example.com","rules":["Drop"]}]}`
	w := request(t, h, http.MethodPut, "/api/support/receiver/rules", body, token)
	if w.Code != http.StatusOK {
		t.Fatalf("PUT status = %d, want %d (body %s)", w.Code, http.StatusOK, w.Body.String())
	}

	stored, err := store.Get(context.Background(), "blocked.example.com")
	if err != nil {
		t.Fatalf("stored rules: %v", err)
	}
	if !reflect.DeepEqual(stored.Rules, []receiver.HandlerRule{receiver.Drop}) {
		t.Errorf("stored rules = %+v, want [Drop]", stored.Rules)
	}
}

func TestSupportReceiverRulesDelete(t *testing.T) {
	lister := &fakeSESIdentityLister{pages: []*ses.ListIdentitiesOutput{
		{Identities: []string{"example.com", "news.example.com"}},
	}}
	store := receiver.NewMemoryConfigStore()
	entry := receiver.HandlerRules{
		MailIdentity: "example.com",
		Rules:        []receiver.HandlerRule{receiver.DeliverToMailIdentity, receiver.Bounce},
	}
	if err := store.Put(context.Background(), "example.com", entry); err != nil {
		t.Fatalf("seed rules: %v", err)
	}
	h, _ := newReceiverSupportTestHandler(t, lister, store)
	_, token, _ := login(t, h, "admin", "secret")
	_, userToken, _ := login(t, h, "bob@example.com", "anything")

	// A non-manager cannot remove a rules entry.
	w := request(t, h, http.MethodDelete, "/api/support/receiver/rules/example.com", "", userToken)
	if w.Code != http.StatusForbidden {
		t.Errorf("non-manager DELETE status = %d, want %d", w.Code, http.StatusForbidden)
	}

	// The fallback can never be removed, on the API either.
	w = request(t, h, http.MethodDelete, "/api/support/receiver/rules/_", "", token)
	if w.Code != http.StatusBadRequest {
		t.Errorf("fallback DELETE status = %d, want %d (body %s)", w.Code, http.StatusBadRequest, w.Body.String())
	}
	if _, err := store.Get(context.Background(), "_"); !errors.Is(err, receiver.ErrIdentityConfigNotFound) {
		t.Errorf("fallback should not be written on boot; store.Get(_) error = %v", err)
	}

	// Removing an identity that has no entry reports 404.
	w = request(t, h, http.MethodDelete, "/api/support/receiver/rules/missing.example.com", "", token)
	if w.Code != http.StatusNotFound {
		t.Errorf("missing DELETE status = %d, want %d", w.Code, http.StatusNotFound)
	}

	// A successful delete returns the remaining rules and flips the identity
	// back to "no rules" in the identities list.
	w = request(t, h, http.MethodDelete, "/api/support/receiver/rules/example.com", "", token)
	if w.Code != http.StatusOK {
		t.Fatalf("DELETE status = %d, want %d (body %s)", w.Code, http.StatusOK, w.Body.String())
	}
	var response struct {
		Rules []struct {
			Domain string `json:"domain"`
		} `json:"rules"`
	}
	decodeJSONBody(t, w.Body.Bytes(), &response)
	if len(response.Rules) != 1 || response.Rules[0].Domain != "_" {
		t.Errorf("rules after delete = %+v, want only the fallback", response.Rules)
	}
	if _, err := store.Get(context.Background(), "example.com"); err == nil {
		t.Error("rules entry was not removed from the config store")
	}

	w = request(t, h, http.MethodGet, "/api/support/receiver/identities", "", token)
	var identities struct {
		Identities []struct {
			Identity string `json:"identity"`
			HasRules bool   `json:"hasRules"`
		} `json:"identities"`
	}
	decodeJSONBody(t, w.Body.Bytes(), &identities)
	for _, identity := range identities.Identities {
		if identity.Identity == "example.com" && identity.HasRules {
			t.Error("deleted identity still reports hasRules = true")
		}
	}

	// A fresh handler (the GUI's Reload) sees the persisted deletion too.
	reloaded, _ := newReceiverSupportTestHandler(t, lister, store)
	_, reloadedToken, _ := login(t, reloaded, "admin", "secret")
	w = request(t, reloaded, http.MethodGet, "/api/support/receiver/rules", "", reloadedToken)
	decodeJSONBody(t, w.Body.Bytes(), &response)
	if len(response.Rules) != 1 || response.Rules[0].Domain != "_" {
		t.Errorf("reloaded rules = %+v, want only the fallback", response.Rules)
	}
}

func TestSupportReceiverRulesValidationAndAuthorization(t *testing.T) {
	lister := &fakeSESIdentityLister{}
	h, _ := newReceiverSupportTestHandler(t, lister, nil)
	_, token, _ := login(t, h, "admin", "secret")
	_, userToken, _ := login(t, h, "bob@example.com", "anything")

	invalid := []string{
		`{"rules":[]}`,
		`{"rules":[{"domain":"bad/name","rules":["Bounce"]}]}`,
		`{"rules":[{"domain":"example.com","rules":[]}]}`,
		`{"rules":[{"domain":"example.com","rules":["Unknown"]}]}`,
	}
	for _, body := range invalid {
		w := request(t, h, http.MethodPut, "/api/support/receiver/rules", body, token)
		if w.Code != http.StatusBadRequest {
			t.Errorf("PUT %s status = %d, want %d", body, w.Code, http.StatusBadRequest)
		}
	}

	valid := `{"rules":[{"domain":"example.com","rules":["Bounce"]}]}`
	if w := request(t, h, http.MethodPut, "/api/support/receiver/rules", valid, userToken); w.Code != http.StatusForbidden {
		t.Errorf("non-manager PUT status = %d, want %d", w.Code, http.StatusForbidden)
	}
	if w := request(t, h, http.MethodGet, "/api/support/receiver/rules", "", userToken); w.Code != http.StatusForbidden {
		t.Errorf("non-manager GET status = %d, want %d", w.Code, http.StatusForbidden)
	}
	if w := request(t, h, http.MethodGet, "/api/support/receiver/identities", "", userToken); w.Code != http.StatusForbidden {
		t.Errorf("non-manager identities status = %d, want %d", w.Code, http.StatusForbidden)
	}
}

func strPtr(value string) *string { return &value }
