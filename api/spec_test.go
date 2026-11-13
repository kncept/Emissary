// Package api holds the normative OpenAPI documents — auth.yaml, jmap.yaml
// and users.yaml — and the code generated from them. The tests in this
// package enforce the path-ownership rule that keeps the three documents
// free of overlap:
//
//   - every operation in auth.yaml lives under the reserved prefix
//     /api/auth/;
//   - every operation in users.yaml lives under the reserved prefix
//     /api/user/;
//   - no path in jmap.yaml falls under /api/auth/ or /api/user/;
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

// usersPrefix is the reserved URL prefix owned exclusively by users.yaml.
const usersPrefix = "/api/user/"

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

// TestUsersSpecUsesReservedPrefix verifies that every operation in
// users.yaml lives under the reserved /api/user/ prefix.
func TestUsersSpecUsesReservedPrefix(t *testing.T) {
	for path := range loadSpec(t, "users.yaml").Paths {
		if !strings.HasPrefix(path, usersPrefix) {
			t.Errorf("users.yaml: %q does not use the reserved %s prefix", path, usersPrefix)
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
		if strings.HasPrefix(path, usersPrefix) {
			t.Errorf("jmap.yaml: %q must not fall under the reserved %s prefix", path, usersPrefix)
		}
	}
}

// TestSpecsDoNotOverlap verifies that the documents never define
// the same (method, path) operation.
func TestSpecsDoNotOverlap(t *testing.T) {
	authPaths := loadSpec(t, "auth.yaml").Paths
	jmapPaths := loadSpec(t, "jmap.yaml").Paths
	usersPaths := loadSpec(t, "users.yaml").Paths

	for path, methods := range authPaths {
		for method := range methods {
			if _, shared := jmapPaths[path][strings.ToLower(method)]; shared {
				t.Errorf("%s %s is defined in both auth.yaml and jmap.yaml", strings.ToUpper(method), path)
			}
			if _, shared := usersPaths[path][strings.ToLower(method)]; shared {
				t.Errorf("%s %s is defined in both auth.yaml and users.yaml", strings.ToUpper(method), path)
			}
		}
	}

	for path, methods := range usersPaths {
		for method := range methods {
			if _, shared := jmapPaths[path][strings.ToLower(method)]; shared {
				t.Errorf("%s %s is defined in both users.yaml and jmap.yaml", strings.ToUpper(method), path)
			}
		}
	}
}
