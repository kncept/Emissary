// Package api holds the normative OpenAPI documents — auth.yaml and
// jmap.yaml — and the code generated from them. The tests in this
// package enforce the path-ownership rule that keeps the two documents
// free of overlap:
//
//   - every operation in auth.yaml lives under the reserved prefix
//     /api/auth/;
//   - no path in jmap.yaml falls under /api/auth/;
//   - the documents never share a (method, path) operation.
//
// See "Specs and path ownership" in docs/API.md.
package api

import (
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// authPrefix is the reserved URL prefix owned exclusively by auth.yaml.
const authPrefix = "/api/auth/"

// openapiPaths is the minimal projection of an OpenAPI document: the
// entries declared for each path (the HTTP methods, plus any shared
// path items such as parameters).
type openapiPaths struct {
	Paths map[string]map[string]yaml.Node `yaml:"paths"`
}

// loadSpec reads and parses the paths of an OpenAPI document.
func loadSpec(t *testing.T, name string) openapiPaths {
	t.Helper()
	data, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	var spec openapiPaths
	if err := yaml.Unmarshal(data, &spec); err != nil {
		t.Fatalf("parse %s: %v", name, err)
	}
	if len(spec.Paths) == 0 {
		t.Fatalf("%s: no paths found", name)
	}
	return spec
}

// TestAuthSpecUsesReservedPrefix verifies that every operation in
// auth.yaml lives under the reserved /api/auth/ prefix.
func TestAuthSpecUsesReservedPrefix(t *testing.T) {
	for path := range loadSpec(t, "auth.yaml").Paths {
		if !strings.HasPrefix(path, authPrefix) {
			t.Errorf("auth.yaml: %q does not use the reserved %s prefix", path, authPrefix)
		}
	}
}

// TestJmapSpecStaysOutOfAuthPrefix verifies that jmap.yaml defines no
// path under the reserved /api/auth/ prefix.
func TestJmapSpecStaysOutOfAuthPrefix(t *testing.T) {
	for path := range loadSpec(t, "jmap.yaml").Paths {
		if strings.HasPrefix(path, authPrefix) {
			t.Errorf("jmap.yaml: %q must not fall under the reserved %s prefix", path, authPrefix)
		}
	}
}

// TestSpecsDoNotOverlap verifies that the two documents never define
// the same (method, path) operation.
func TestSpecsDoNotOverlap(t *testing.T) {
	authPaths := loadSpec(t, "auth.yaml").Paths
	jmapPaths := loadSpec(t, "jmap.yaml").Paths
	for path, methods := range authPaths {
		for method := range methods {
			if _, shared := jmapPaths[path][strings.ToLower(method)]; shared {
				t.Errorf("%s %s is defined in both auth.yaml and jmap.yaml", strings.ToUpper(method), path)
			}
		}
	}
}
