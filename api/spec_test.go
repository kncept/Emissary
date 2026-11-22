// Package api holds the normative OpenAPI documents — auth.yaml, jmap.yaml,
// support.yaml and users.yaml — and the code generated from them. The tests
// in this package enforce the path-ownership rule that keeps the documents
// free of overlap:
//
//   - every operation in auth.yaml lives under the reserved prefix
//     /api/auth/;
//   - every operation in support.yaml lives under the reserved prefix
//     /api/support/;
//   - every operation in users.yaml lives under the reserved prefix
//     /api/user/;
//   - no path in jmap.yaml falls under /api/auth/, /api/support/ or
//     /api/user/;
//   - the documents never share a (method, path) operation.
//
// See "Specs and path ownership" in docs/API.md.
package api

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/kncept/Emissary/mailer/handler"
	"github.com/kncept/Emissary/mailer/jmap"
	"gopkg.in/yaml.v3"
)

// authPrefix is the reserved URL prefix owned exclusively by auth.yaml.
const authPrefix = "/api/auth/"

// supportPrefix is the reserved URL prefix owned exclusively by support.yaml.
const supportPrefix = "/api/support/"

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

// TestSupportSpecUsesReservedPrefix verifies that every operation in
// support.yaml lives under the reserved /api/support/ prefix.
func TestSupportSpecUsesReservedPrefix(t *testing.T) {
	for path := range loadSpec(t, "support.yaml").Paths {
		if !strings.HasPrefix(path, supportPrefix) {
			t.Errorf("support.yaml: %q does not use the reserved %s prefix", path, supportPrefix)
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

// TestJmapSpecStaysOutOfReservedPrefixes verifies that jmap.yaml defines
// no path under the reserved auth, support or users prefixes.
func TestJmapSpecStaysOutOfReservedPrefixes(t *testing.T) {
	for path := range loadSpec(t, "jmap.yaml").Paths {
		if strings.HasPrefix(path, authPrefix) {
			t.Errorf("jmap.yaml: %q must not fall under the reserved %s prefix", path, authPrefix)
		}
		if strings.HasPrefix(path, supportPrefix) {
			t.Errorf("jmap.yaml: %q must not fall under the reserved %s prefix", path, supportPrefix)
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
	supportPaths := loadSpec(t, "support.yaml").Paths
	usersPaths := loadSpec(t, "users.yaml").Paths

	documents := []struct {
		name  string
		paths map[string]map[string]yaml.Node
	}{
		{name: "auth.yaml", paths: authPaths},
		{name: "jmap.yaml", paths: jmapPaths},
		{name: "support.yaml", paths: supportPaths},
		{name: "users.yaml", paths: usersPaths},
	}

	for i, left := range documents {
		for _, right := range documents[i+1:] {
			for path, methods := range left.paths {
				for method := range methods {
					if _, shared := right.paths[path][strings.ToLower(method)]; shared {
						t.Errorf("%s %s is defined in both %s and %s", strings.ToUpper(method), path, left.name, right.name)
					}
				}
			}
		}
	}
}

// ---------------------------------------------------------------------------
// JMAP spec <-> runtime conformance (TASK-6.1).
//
// api/jmap.yaml is the single source of truth. These tests read it directly
// and compare its machine-readable manifest (x-jmap-capabilities and
// x-jmap-methods), its error media types, its method argument schemas and its
// Session schema against the running Go implementation. A Go workspace at the
// repository root lets this module's tests import the runtime packages; see
// go.work.
// ---------------------------------------------------------------------------

// jmapMethodManifest is one entry of the normative x-jmap-methods list.
type jmapMethodManifest struct {
	Name       string `json:"name"`
	Capability string `json:"capability"`
	Request    string `json:"request"`
	Response   string `json:"response"`
}

// jmapSpecDocument is the JSON-normalised form of api/jmap.yaml.
type jmapSpecDocument struct {
	Raw          map[string]any
	Methods      []jmapMethodManifest
	Capabilities []string
}

// loadJMAPSpecDocument parses api/jmap.yaml into a JSON-compatible tree. The
// YAML -> JSON round trip normalises mapping keys and numeric types so the
// schema helpers below can treat every value as an encoding/json value.
func loadJMAPSpecDocument(t *testing.T) jmapSpecDocument {
	t.Helper()
	data, err := os.ReadFile("jmap.yaml")
	if err != nil {
		t.Fatalf("read jmap.yaml: %v", err)
	}
	var raw any
	if err := yaml.Unmarshal(data, &raw); err != nil {
		t.Fatalf("parse jmap.yaml: %v", err)
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		t.Fatalf("normalise jmap.yaml: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(encoded, &doc); err != nil {
		t.Fatalf("decode normalised jmap.yaml: %v", err)
	}

	out := jmapSpecDocument{Raw: doc}
	caps, ok := doc["x-jmap-capabilities"].([]any)
	if !ok {
		t.Fatalf("x-jmap-capabilities is %T, want a list", doc["x-jmap-capabilities"])
	}
	for _, value := range caps {
		name, ok := value.(string)
		if !ok {
			t.Fatalf("x-jmap-capabilities entry is %T, want a string", value)
		}
		out.Capabilities = append(out.Capabilities, name)
	}
	methods, ok := doc["x-jmap-methods"].([]any)
	if !ok {
		t.Fatalf("x-jmap-methods is %T, want a list", doc["x-jmap-methods"])
	}
	for _, value := range methods {
		entry, ok := value.(map[string]any)
		if !ok {
			t.Fatalf("x-jmap-methods entry is %T, want an object", value)
		}
		out.Methods = append(out.Methods, jmapMethodManifest{
			Name:       jmapSpecString(t, entry, "name"),
			Capability: jmapSpecString(t, entry, "capability"),
			Request:    jmapSpecString(t, entry, "request"),
			Response:   jmapSpecString(t, entry, "response"),
		})
	}
	return out
}

func jmapSpecString(t *testing.T, obj map[string]any, key string) string {
	t.Helper()
	value, ok := obj[key].(string)
	if !ok {
		t.Fatalf("field %q is %T, want a string", key, obj[key])
	}
	return value
}

func jmapSchemas(t *testing.T, doc map[string]any) map[string]any {
	t.Helper()
	components, ok := doc["components"].(map[string]any)
	if !ok {
		t.Fatalf("components is %T, want an object", doc["components"])
	}
	schemas, ok := components["schemas"].(map[string]any)
	if !ok {
		t.Fatalf("components.schemas is %T, want an object", components["schemas"])
	}
	return schemas
}

// compareJMAPManifest is pure so the acceptance criterion — a deliberately
// mutated capability or method name is detected — can be exercised with an
// in-memory fixture as well as with the real spec.
func compareJMAPManifest(methods []jmapMethodManifest, capabilities []string) []string {
	var problems []string
	runtimeMethods := make(map[string]bool)
	for _, name := range jmap.AllMethods() {
		runtimeMethods[name] = true
	}
	specMethods := make(map[string]bool, len(methods))
	declaredCapabilities := make(map[string]bool, len(capabilities))
	for _, capability := range capabilities {
		declaredCapabilities[capability] = true
	}

	for _, method := range methods {
		specMethods[method.Name] = true
		if !runtimeMethods[method.Name] {
			problems = append(problems, fmt.Sprintf("x-jmap-methods lists %q, which jmap.AllMethods() does not implement", method.Name))
		}
		runtimeCapability, known := jmap.MethodCapability(method.Name)
		switch {
		case !known:
			problems = append(problems, fmt.Sprintf("method %q has no runtime MethodCapability", method.Name))
		case runtimeCapability != method.Capability:
			problems = append(problems, fmt.Sprintf("method %q capability %q != runtime %q", method.Name, method.Capability, runtimeCapability))
		}
		if !declaredCapabilities[method.Capability] {
			problems = append(problems, fmt.Sprintf("method %q requires capability %q, absent from x-jmap-capabilities", method.Name, method.Capability))
		}
	}
	for _, name := range jmap.AllMethods() {
		if !specMethods[name] {
			problems = append(problems, fmt.Sprintf("runtime method %q is missing from x-jmap-methods", name))
		}
	}

	runtimeCapabilities := make(map[string]bool)
	for _, capability := range jmap.AllCapabilities() {
		runtimeCapabilities[capability] = true
		if !declaredCapabilities[capability] {
			problems = append(problems, fmt.Sprintf("runtime capability %q is missing from x-jmap-capabilities", capability))
		}
	}
	for _, capability := range capabilities {
		if !runtimeCapabilities[capability] {
			problems = append(problems, fmt.Sprintf("x-jmap-capabilities declares %q, which the Session does not advertise", capability))
		}
	}
	sort.Strings(problems)
	return problems
}

func TestJMAPSpecManifestMatchesRuntime(t *testing.T) {
	spec := loadJMAPSpecDocument(t)
	if problems := compareJMAPManifest(spec.Methods, spec.Capabilities); len(problems) > 0 {
		t.Fatalf("jmap.yaml manifest drifted from the runtime:\n  %s", strings.Join(problems, "\n  "))
	}
}

func TestJMAPSpecManifestMutationIsDetected(t *testing.T) {
	spec := loadJMAPSpecDocument(t)
	if len(spec.Methods) == 0 || len(spec.Capabilities) == 0 {
		t.Fatal("jmap.yaml manifest is empty")
	}

	mutated := append([]jmapMethodManifest(nil), spec.Methods...)
	mutated[0].Name = "Core/echo:mutated"
	if problems := compareJMAPManifest(mutated, spec.Capabilities); len(problems) == 0 {
		t.Error("mutating a method name was not detected")
	}

	mutated = append([]jmapMethodManifest(nil), spec.Methods...)
	mutated[0].Capability = "urn:ietf:params:jmap:mutated"
	if problems := compareJMAPManifest(mutated, spec.Capabilities); len(problems) == 0 {
		t.Error("mutating a method capability was not detected")
	}

	capabilities := append([]string(nil), spec.Capabilities...)
	capabilities[0] = "urn:ietf:params:jmap:mutated"
	if problems := compareJMAPManifest(spec.Methods, capabilities); len(problems) == 0 {
		t.Error("mutating an advertised capability was not detected")
	}
}

// jmapHTTPMethods are the only path-item keys that describe HTTP operations.
func jmapHTTPMethods() map[string]bool {
	return map[string]bool{"get": true, "post": true, "put": true, "patch": true, "delete": true}
}

func jmapMapKeys(values map[string]any) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// TestJMAPSpecErrorResponsesUseDeclaredMediaTypes checks that every error
// response declares exactly the media type clients are told to expect:
// application/problem+json for JSON APIs and text/plain for EventSource
// streams (an EventSource cannot read a problem document from an error body).
func TestJMAPSpecErrorResponsesUseDeclaredMediaTypes(t *testing.T) {
	doc := loadJMAPSpecDocument(t)
	paths, ok := doc.Raw["paths"].(map[string]any)
	if !ok {
		t.Fatalf("paths is %T, want an object", doc.Raw["paths"])
	}
	verbs := jmapHTTPMethods()
	checked := 0
	for path, rawPath := range paths {
		operations, ok := rawPath.(map[string]any)
		if !ok {
			continue
		}
		stream := strings.HasPrefix(path, "/eventsource")
		for verb, rawOperation := range operations {
			if !verbs[verb] {
				continue
			}
			operation, ok := rawOperation.(map[string]any)
			if !ok {
				t.Errorf("%s %s operation is %T, want an object", verb, path, rawOperation)
				continue
			}
			responses, ok := operation["responses"].(map[string]any)
			if !ok {
				t.Errorf("%s %s has no responses object", verb, path)
				continue
			}
			for code, rawResponse := range responses {
				if len(code) == 0 || (code[0] != '4' && code[0] != '5') {
					continue
				}
				response, ok := rawResponse.(map[string]any)
				if !ok {
					t.Errorf("%s %s response %s is %T, want an object", verb, path, code, rawResponse)
					continue
				}
				content, ok := response["content"].(map[string]any)
				if !ok || len(content) == 0 {
					t.Errorf("%s %s response %s declares no media type", verb, path, code)
					continue
				}
				want := "application/problem+json"
				if stream {
					want = "text/plain"
				}
				if _, ok := content[want]; !ok {
					t.Errorf("%s %s response %s declares %v, want %q", verb, path, code, jmapMapKeys(content), want)
				}
				checked++
			}
		}
	}
	if checked == 0 {
		t.Fatal("no error responses found in jmap.yaml")
	}
}

// resolveJMAPSchema follows local $ref pointers into components.schemas. A
// nullable sibling is preserved because siblings of $ref are otherwise
// ignored by JSON Schema.
func resolveJMAPSchema(schemas map[string]any, node any) (map[string]any, error) {
	// Callers pass either a schema object or a bare local $ref string.
	if ref, ok := node.(string); ok {
		node = map[string]any{"$ref": ref}
	}
	current, ok := node.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("schema is %T, want an object", node)
	}
	nullable, _ := current["nullable"].(bool)
	seen := make(map[string]bool)
	for {
		ref, hasRef := current["$ref"].(string)
		if !hasRef {
			break
		}
		if !strings.HasPrefix(ref, "#/components/schemas/") {
			return nil, fmt.Errorf("unsupported $ref %q", ref)
		}
		name := strings.TrimPrefix(ref, "#/components/schemas/")
		if seen[name] {
			return nil, fmt.Errorf("cyclic $ref %q", ref)
		}
		seen[name] = true
		target, exists := schemas[name]
		if !exists {
			return nil, fmt.Errorf("schema %q does not exist", name)
		}
		targetObject, ok := target.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("schema %q is %T, want an object", name, target)
		}
		current = targetObject
	}
	resolved := make(map[string]any, len(current)+1)
	for key, value := range current {
		resolved[key] = value
	}
	if nullable {
		resolved["nullable"] = true
	}
	return resolved, nil
}

func resolveJMAPRef(t *testing.T, schemas map[string]any, ref string) map[string]any {
	t.Helper()
	resolved, err := resolveJMAPSchema(schemas, map[string]any{"$ref": ref})
	if err != nil {
		t.Fatalf("resolve %s: %v", ref, err)
	}
	return resolved
}

// representativeJMAPValue builds one concrete value that satisfies a schema.
// It is used to prove that every method schema in x-jmap-methods can accept a
// real invocation rather than merely existing as a name.
func representativeJMAPValue(schemas map[string]any, node any, depth int) (any, error) {
	if depth > 8 {
		return nil, nil
	}
	schema, err := resolveJMAPSchema(schemas, node)
	if err != nil {
		return nil, err
	}
	if enums, ok := schema["enum"].([]any); ok && len(enums) > 0 {
		return enums[0], nil
	}
	for _, keyword := range []string{"anyOf", "oneOf"} {
		if variants, ok := schema[keyword].([]any); ok && len(variants) > 0 {
			return representativeJMAPValue(schemas, variants[0], depth+1)
		}
	}
	if variants, ok := schema["allOf"].([]any); ok && len(variants) > 0 {
		merged := make(map[string]any)
		for _, variant := range variants {
			value, err := representativeJMAPValue(schemas, variant, depth+1)
			if err != nil {
				return nil, err
			}
			if object, ok := value.(map[string]any); ok {
				for key, entry := range object {
					merged[key] = entry
				}
			}
		}
		return merged, nil
	}
	switch schema["type"] {
	case "object":
		object := make(map[string]any)
		required, _ := schema["required"].([]any)
		properties, _ := schema["properties"].(map[string]any)
		for _, rawName := range required {
			name, ok := rawName.(string)
			if !ok {
				return nil, fmt.Errorf("required entry %v is not a string", rawName)
			}
			propertySchema, declared := properties[name]
			if !declared {
				return nil, fmt.Errorf("required property %q has no schema", name)
			}
			value, err := representativeJMAPValue(schemas, propertySchema, depth+1)
			if err != nil {
				return nil, fmt.Errorf("required property %q: %w", name, err)
			}
			object[name] = value
		}
		return object, nil
	case "array":
		items, declared := schema["items"]
		if !declared {
			return []any{}, nil
		}
		value, err := representativeJMAPValue(schemas, items, depth+1)
		if err != nil {
			return nil, err
		}
		return []any{value}, nil
	case "string":
		return "example", nil
	case "integer", "number":
		return float64(1), nil
	case "boolean":
		return true, nil
	case "null":
		return nil, nil
	default:
		return nil, nil
	}
}

func jmapTypeMatches(expected any, value any) bool {
	types, ok := expected.([]any)
	if !ok {
		types = []any{expected}
	}
	for _, rawName := range types {
		name, _ := rawName.(string)
		switch name {
		case "object":
			if _, ok := value.(map[string]any); ok {
				return true
			}
		case "array":
			if _, ok := value.([]any); ok {
				return true
			}
		case "string":
			if _, ok := value.(string); ok {
				return true
			}
		case "integer":
			if number, ok := value.(float64); ok && number == math.Trunc(number) {
				return true
			}
		case "number":
			if _, ok := value.(float64); ok {
				return true
			}
		case "boolean":
			if _, ok := value.(bool); ok {
				return true
			}
		case "null":
			if value == nil {
				return true
			}
		}
	}
	return false
}

// validateJMAPValue implements the JSON Schema subset used by the JMAP method
// schemas: $ref, nullable, type, enum, required, properties,
// additionalProperties, items, and the composition keywords.
func validateJMAPValue(schemas map[string]any, node any, value any, path string) error {
	schema, err := resolveJMAPSchema(schemas, node)
	if err != nil {
		return err
	}
	if value == nil {
		if nullable, _ := schema["nullable"].(bool); nullable || schema["type"] == "null" {
			return nil
		}
		return fmt.Errorf("%s: null is not allowed", path)
	}
	if variants, ok := schema["anyOf"].([]any); ok {
		for _, variant := range variants {
			if validateJMAPValue(schemas, variant, value, path) == nil {
				return nil
			}
		}
		return fmt.Errorf("%s: value does not match anyOf", path)
	}
	if variants, ok := schema["oneOf"].([]any); ok {
		matches := 0
		for _, variant := range variants {
			if validateJMAPValue(schemas, variant, value, path) == nil {
				matches++
			}
		}
		if matches != 1 {
			return fmt.Errorf("%s: value matches %d oneOf branches, want 1", path, matches)
		}
		return nil
	}
	if variants, ok := schema["allOf"].([]any); ok {
		for _, variant := range variants {
			if err := validateJMAPValue(schemas, variant, value, path); err != nil {
				return err
			}
		}
	}
	if expected, declared := schema["type"]; declared && !jmapTypeMatches(expected, value) {
		return fmt.Errorf("%s: value %T does not match type %v", path, value, expected)
	}
	if enums, ok := schema["enum"].([]any); ok {
		found := false
		for _, candidate := range enums {
			if reflect.DeepEqual(candidate, value) {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("%s: value %v is not in enum %v", path, value, enums)
		}
	}

	switch typed := value.(type) {
	case map[string]any:
		properties, _ := schema["properties"].(map[string]any)
		if required, ok := schema["required"].([]any); ok {
			for _, rawName := range required {
				name, _ := rawName.(string)
				if _, present := typed[name]; !present {
					return fmt.Errorf("%s: missing required property %q", path, name)
				}
			}
		}
		for name, propertyValue := range typed {
			propertySchema, declared := properties[name]
			if !declared {
				additional, present := schema["additionalProperties"]
				if !present {
					continue
				}
				if allowed, isBool := additional.(bool); isBool {
					if !allowed {
						return fmt.Errorf("%s: unexpected property %q", path, name)
					}
					continue
				}
				if _, isSchema := additional.(map[string]any); isSchema {
					if err := validateJMAPValue(schemas, additional, propertyValue, path+"."+name); err != nil {
						return err
					}
				}
				continue
			}
			if err := validateJMAPValue(schemas, propertySchema, propertyValue, path+"."+name); err != nil {
				return err
			}
		}
	case []any:
		items, declared := schema["items"]
		if !declared {
			return nil
		}
		for index, element := range typed {
			if err := validateJMAPValue(schemas, items, element, fmt.Sprintf("%s[%d]", path, index)); err != nil {
				return err
			}
		}
	}
	return nil
}

// TestJMAPSpecMethodSchemasAcceptRepresentativeInvocation proves that every
// request/response schema referenced by x-jmap-methods exists and that a
// concrete, generated invocation satisfies the request schema.
func TestJMAPSpecMethodSchemasAcceptRepresentativeInvocation(t *testing.T) {
	spec := loadJMAPSpecDocument(t)
	schemas := jmapSchemas(t, spec.Raw)
	if len(schemas) == 0 {
		t.Fatal("components.schemas is empty")
	}
	for _, method := range spec.Methods {
		t.Run(method.Name, func(t *testing.T) {
			request := resolveJMAPRef(t, schemas, method.Request)
			if request["type"] != "object" {
				t.Fatalf("request schema %s has type %v, want object", method.Request, request["type"])
			}
			resolveJMAPRef(t, schemas, method.Response)

			arguments, err := representativeJMAPValue(schemas, method.Request, 0)
			if err != nil {
				t.Fatalf("build representative arguments: %v", err)
			}
			encoded, err := json.Marshal(arguments)
			if err != nil {
				t.Fatalf("marshal representative arguments: %v", err)
			}
			if err := validateJMAPValue(schemas, method.Request, arguments, method.Name); err != nil {
				t.Fatalf("representative arguments %s do not satisfy %s: %v", encoded, method.Request, err)
			}
		})
	}
}

func TestJMAPSpecSessionRequiredMatchesRuntime(t *testing.T) {
	spec := loadJMAPSpecDocument(t)
	schemas := jmapSchemas(t, spec.Raw)
	session, ok := schemas["Session"].(map[string]any)
	if !ok {
		t.Fatalf("Session schema is %T, want an object", schemas["Session"])
	}
	required, ok := session["required"].([]any)
	if !ok {
		t.Fatalf("Session.required is %T, want a list", session["required"])
	}
	specKeys := make([]string, 0, len(required))
	for _, rawKey := range required {
		key, ok := rawKey.(string)
		if !ok {
			t.Fatalf("Session.required entry is %T, want a string", rawKey)
		}
		specKeys = append(specKeys, key)
	}
	sort.Strings(specKeys)
	if runtimeKeys := handler.JMAPSessionKeys(); !reflect.DeepEqual(specKeys, runtimeKeys) {
		t.Errorf("Session.required = %v, runtime Session keys = %v", specKeys, runtimeKeys)
	}
}
