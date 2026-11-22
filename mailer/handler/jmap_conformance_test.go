package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/kncept/Emissary/mailer/auth"
	"github.com/kncept/Emissary/mailer/jmap"
	"gopkg.in/yaml.v3"
)

// jmapSpec is the projection of api/jmap.yaml needed to check the normative
// manifest and HTTP surface against the runtime.
type jmapSpec struct {
	Paths         map[string]map[string]yaml.Node `yaml:"paths"`
	XCapabilities []string                        `yaml:"x-jmap-capabilities"`
	XMethods      []struct {
		Name       string `yaml:"name"`
		Capability string `yaml:"capability"`
	} `yaml:"x-jmap-methods"`
}

func loadJMAPSpec(t *testing.T) jmapSpec {
	t.Helper()
	data, err := os.ReadFile("../../api/jmap.yaml")
	if err != nil {
		t.Fatalf("read api/jmap.yaml: %v", err)
	}
	var spec jmapSpec
	if err := yaml.Unmarshal(data, &spec); err != nil {
		t.Fatalf("parse api/jmap.yaml: %v", err)
	}
	return spec
}

func sortedSet(values []string) []string {
	out := append([]string(nil), values...)
	sort.Strings(out)
	return out
}

// TestJMAPRoutesMatchOpenAPI fails when a route registered in newHandler is
// missing from api/jmap.yaml, or when the spec advertises a route the runtime
// never registers.
func TestJMAPRoutesMatchOpenAPI(t *testing.T) {
	spec := loadJMAPSpec(t)
	specRoutes := make(map[string]bool)
	for path, operations := range spec.Paths {
		for method := range operations {
			lower := strings.ToLower(method)
			switch lower {
			case "get", "post", "put", "patch", "delete":
				specRoutes[strings.ToUpper(lower)+" "+path] = true
			}
		}
	}

	runtimeRoutes := []string{
		jmapSessionRoute,
		jmapEventSourceRoute,
		jmapAPIRoute,
		jmapUploadRoute,
		jmapDownloadRoute,
	}
	for _, route := range runtimeRoutes {
		if !specRoutes[route] {
			t.Errorf("runtime registers %q but api/jmap.yaml does not define it", route)
		}
		delete(specRoutes, route)
	}
	for route := range specRoutes {
		t.Errorf("api/jmap.yaml defines %q but newHandler does not register it", route)
	}
}

// TestJMAPManifestMatchesRuntime enforces x-jmap-capabilities and the canonical
// x-jmap-methods list as the single source of truth for Session discovery and
// per-method capability gating.
func TestJMAPManifestMatchesRuntime(t *testing.T) {
	spec := loadJMAPSpec(t)

	declaredCapabilities := make(map[string]bool, len(spec.XCapabilities))
	for _, capability := range spec.XCapabilities {
		declaredCapabilities[capability] = true
	}
	runtimeCapabilities := jmap.AllCapabilities()
	if !equalStrings(sortedSet(spec.XCapabilities), sortedSet(runtimeCapabilities)) {
		t.Errorf("x-jmap-capabilities = %v, runtime AllCapabilities() = %v", spec.XCapabilities, runtimeCapabilities)
	}

	specMethods := make(map[string]string, len(spec.XMethods))
	for _, method := range spec.XMethods {
		specMethods[method.Name] = method.Capability
		if !declaredCapabilities[method.Capability] {
			t.Errorf("method %s requires capability %s, absent from x-jmap-capabilities", method.Name, method.Capability)
		}
	}

	runtimeMethods := jmap.AllMethods()
	if len(specMethods) != len(runtimeMethods) {
		t.Errorf("x-jmap-methods has %d entries, runtime AllMethods() has %d", len(specMethods), len(runtimeMethods))
	}
	for _, name := range runtimeMethods {
		specCapability, ok := specMethods[name]
		if !ok {
			t.Errorf("runtime method %s is missing from x-jmap-methods", name)
			continue
		}
		runtimeCapability, ok := jmap.MethodCapability(name)
		if !ok {
			t.Errorf("runtime method %s has no capability mapping", name)
			continue
		}
		if specCapability != runtimeCapability {
			t.Errorf("method %s: spec capability %s, runtime capability %s", name, specCapability, runtimeCapability)
		}
	}
}

// TestSessionAdvertisesManifestCapabilities verifies that every capability in
// the OpenAPI manifest is actually advertised by the live Session object.
func TestSessionAdvertisesManifestCapabilities(t *testing.T) {
	spec := loadJMAPSpec(t)
	claims := &auth.Claims{Subject: "alice@example.com"}
	ctx := auth.ContextWithClaims(context.Background(), claims)
	req := httptest.NewRequest(http.MethodGet, "/.well-known/jmap", nil).WithContext(ctx)
	w := httptest.NewRecorder()
	handleJMAPSession(w, req, 10<<20)

	var session struct {
		Capabilities map[string]json.RawMessage `json:"capabilities"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &session); err != nil {
		t.Fatalf("decode Session: %v (body %s)", err, w.Body.String())
	}
	for _, capability := range spec.XCapabilities {
		if _, ok := session.Capabilities[capability]; !ok {
			t.Errorf("Session does not advertise manifest capability %s", capability)
		}
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
