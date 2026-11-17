package handler

import (
	"encoding/json"
	"net/http"
	"testing"
)

func decodeJSONBody(t *testing.T, body []byte, destination any) {
	t.Helper()
	if err := json.Unmarshal(body, destination); err != nil {
		t.Fatalf("decode response: %v (body %s)", err, body)
	}
}

func TestSupportEndpointsRequireAdmin(t *testing.T) {
	h, _, _ := newHandlerForTest(t, "secret")

	status, adminToken, _ := login(t, h, "admin", "secret")
	if status != http.StatusOK {
		t.Fatalf("admin login status = %d, want %d", status, http.StatusOK)
	}
	status, userToken, _ := login(t, h, "bob@example.com", "anything")
	if status != http.StatusOK {
		t.Fatalf("user login status = %d, want %d", status, http.StatusOK)
	}

	if w := request(t, h, http.MethodGet, "/api/support/configurations", "", adminToken); w.Code != http.StatusOK {
		t.Errorf("admin configurations status = %d, want %d (body %s)", w.Code, http.StatusOK, w.Body.String())
	}

	requests := []struct {
		method string
		target string
		body   string
	}{
		{http.MethodGet, "/api/support/configurations", ""},
		{http.MethodGet, "/api/support/configurations/storage", ""},
		{http.MethodGet, "/api/support/bucket/root", ""},
		{http.MethodPost, "/api/support/process-email", `{"id":"staging"}`},
	}
	for _, test := range requests {
		w := request(t, h, test.method, test.target, test.body, userToken)
		if w.Code != http.StatusForbidden {
			t.Errorf("non-admin %s %s status = %d, want %d (body %s)", test.method, test.target, w.Code, http.StatusForbidden, w.Body.String())
		}
	}
}

func TestSupportRejectsMissingToken(t *testing.T) {
	h, _, _ := newHandlerForTest(t, "secret")

	w := request(t, h, http.MethodGet, "/api/support/configurations", "", "")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusUnauthorized)
	}
	var response map[string]string
	decodeJSONBody(t, w.Body.Bytes(), &response)
	if response["error"] != "authentication required" {
		t.Errorf("error = %q, want authentication required", response["error"])
	}
	if header := w.Header().Get("WWW-Authenticate"); header == "" {
		t.Error("WWW-Authenticate header is missing")
	}
}

func TestSupportListsConfigurations(t *testing.T) {
	h, _, _ := newHandlerForTest(t, "secret")
	_, token, _ := login(t, h, "admin", "secret")

	w := request(t, h, http.MethodGet, "/api/support/configurations", "", token)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (body %s)", w.Code, http.StatusOK, w.Body.String())
	}

	var response struct {
		Configurations []struct {
			Name           string `json:"name"`
			Implementation string `json:"implementation"`
			Description    string `json:"description"`
		} `json:"configurations"`
	}
	decodeJSONBody(t, w.Body.Bytes(), &response)

	wanted := map[string]bool{
		"server":       false,
		"storage":      false,
		"sender":       false,
		"metadata":     false,
		"session":      false,
		"user-manager": false,
		"aws":          false,
	}
	for _, configuration := range response.Configurations {
		if _, ok := wanted[configuration.Name]; !ok {
			t.Errorf("unexpected configuration %q", configuration.Name)
			continue
		}
		wanted[configuration.Name] = true
		if configuration.Implementation == "" {
			t.Errorf("configuration %q has no implementation", configuration.Name)
		}
		if configuration.Description == "" {
			t.Errorf("configuration %q has no description", configuration.Name)
		}
	}
	for name, found := range wanted {
		if !found {
			t.Errorf("configuration %q is missing", name)
		}
	}
}

func TestSupportGetsConfigurationDetailsAndRedactsSecrets(t *testing.T) {
	h, _, _ := newHandlerForTest(t, "secret")
	_, token, _ := login(t, h, "admin", "secret")

	w := request(t, h, http.MethodGet, "/api/support/configurations/server", "", token)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (body %s)", w.Code, http.StatusOK, w.Body.String())
	}

	var detail struct {
		Name           string            `json:"name"`
		Implementation string            `json:"implementation"`
		Settings       map[string]string `json:"settings"`
	}
	decodeJSONBody(t, w.Body.Bytes(), &detail)
	if detail.Name != "server" {
		t.Errorf("name = %q, want server", detail.Name)
	}
	if detail.Implementation != "http" {
		t.Errorf("implementation = %q, want http", detail.Implementation)
	}
	if detail.Settings["adminPasswordConfigured"] != "true" {
		t.Errorf("adminPasswordConfigured = %q, want true", detail.Settings["adminPasswordConfigured"])
	}
	if detail.Settings["jwtSecretConfigured"] != "true" {
		t.Errorf("jwtSecretConfigured = %q, want true", detail.Settings["jwtSecretConfigured"])
	}
	if detail.Settings["commitHash"] == "" {
		t.Errorf("commitHash is missing from server settings")
	}
	if detail.Settings["buildTime"] == "" {
		t.Errorf("buildTime is missing from server settings")
	}
	for _, secretKey := range []string{"password", "adminPassword", "jwtSecret", "ldapBindPassword"} {
		if _, ok := detail.Settings[secretKey]; ok {
			t.Errorf("settings leaked secret key %q: %v", secretKey, detail.Settings)
		}
	}

	// Section names are case-insensitive and underscore variants are accepted.
	w = request(t, h, http.MethodGet, "/api/support/configurations/USER_MANAGER", "", token)
	if w.Code != http.StatusOK {
		t.Fatalf("user-manager status = %d, want %d (body %s)", w.Code, http.StatusOK, w.Body.String())
	}
	decodeJSONBody(t, w.Body.Bytes(), &detail)
	if detail.Name != "user-manager" {
		t.Errorf("name = %q, want user-manager", detail.Name)
	}

	w = request(t, h, http.MethodGet, "/api/support/configurations/unknown", "", token)
	if w.Code != http.StatusNotFound {
		t.Errorf("unknown section status = %d, want %d", w.Code, http.StatusNotFound)
	}
}

func TestSupportBucketOperationsRequireS3Storage(t *testing.T) {
	h, _, _ := newHandlerForTest(t, "secret")
	_, token, _ := login(t, h, "admin", "secret")

	w := request(t, h, http.MethodGet, "/api/support/bucket/root", "", token)
	if w.Code != http.StatusNotImplemented {
		t.Errorf("bucket/root status = %d, want %d (body %s)", w.Code, http.StatusNotImplemented, w.Body.String())
	}

	w = request(t, h, http.MethodPost, "/api/support/process-email", `{"id":"staging"}`, token)
	if w.Code != http.StatusNotImplemented {
		t.Errorf("process-email status = %d, want %d (body %s)", w.Code, http.StatusNotImplemented, w.Body.String())
	}

	// An empty id is rejected before the storage check.
	w = request(t, h, http.MethodPost, "/api/support/process-email", `{"id":"   "}`, token)
	if w.Code != http.StatusBadRequest {
		t.Errorf("empty process-email id status = %d, want %d (body %s)", w.Code, http.StatusBadRequest, w.Body.String())
	}
}
