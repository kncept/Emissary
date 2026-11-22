package base

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

func TestRejectUnknownArguments(t *testing.T) {
	allowed := map[string]bool{"accountId": true, "ids": true, "properties": true}
	cases := []struct {
		name, args, wantPrefix string
	}{
		{"known only", `{"accountId":"a","ids":null}`, ""},
		{"empty", `{}`, ""},
		{"unknown", `{"accountId":"a","bogus":1}`, "invalidArguments:"},
		{"several unknown", `{"z":1,"a":2}`, "invalidArguments:"},
		{"not an object", `[]`, "invalidArguments:"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := RejectUnknownArguments(json.RawMessage(tc.args), allowed)
			if tc.wantPrefix == "" {
				if err != nil {
					t.Fatalf("RejectUnknownArguments() = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.HasPrefix(err.Error(), tc.wantPrefix) {
				t.Fatalf("RejectUnknownArguments() = %v, want prefix %q", err, tc.wantPrefix)
			}
		})
	}
}

func TestValidateCreateProperties(t *testing.T) {
	allowed := map[string]bool{"name": true, "parentId": true, "sortOrder": true}
	immutable := map[string]bool{"id": true}
	create := map[string]json.RawMessage{
		"c1": json.RawMessage(`{"name":"Inbox","parentId":null}`),
		"c2": json.RawMessage(`{"name":"Bad","id":"x","bogus":1}`),
	}
	got := ValidateCreateProperties(create, allowed, immutable)
	if _, ok := got["c1"]; ok {
		t.Errorf("c1 = %v, want valid", got["c1"])
	}
	if want := []string{"bogus", "id"}; !slices.Equal(got["c2"], want) {
		t.Errorf("c2 = %v, want %v", got["c2"], want)
	}
}

func TestApplyPatch(t *testing.T) {
	current := json.RawMessage(`{"name":"Inbox","sortOrder":10,"keywords":{"$seen":true,"$flagged":true},"nested":{"a":1}}`)
	cases := []struct {
		name, patch, want string
	}{
		{"replace", `{"name":"Sent"}`, `{"keywords":{"$flagged":true,"$seen":true},"name":"Sent","nested":{"a":1},"sortOrder":10}`},
		{"remove", `{"sortOrder":null}`, `{"keywords":{"$flagged":true,"$seen":true},"name":"Inbox","nested":{"a":1}}`},
		{"nested patch", `{"keywords/$seen":null}`, `{"keywords":{"$flagged":true},"name":"Inbox","nested":{"a":1},"sortOrder":10}`},
		{"add", `{"nested":{"a":1,"b":2}}`, `{"keywords":{"$flagged":true,"$seen":true},"name":"Inbox","nested":{"a":1,"b":2},"sortOrder":10}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ApplyPatch(current, json.RawMessage(tc.patch))
			if err != nil {
				t.Fatalf("ApplyPatch() error = %v", err)
			}
			if string(got) != tc.want {
				t.Errorf("ApplyPatch() = %s, want %s", got, tc.want)
			}
		})
	}
}

// TestValidatePatchRFC8620_5_3 covers the shared PatchObject classification
// used by every /set type with immutable properties.
func TestValidatePatchRFC8620_5_3(t *testing.T) {
	current := json.RawMessage(`{"id":"singleton","isEnabled":true,"subject":"Away","fromDate":null}`)
	allowed := ArgumentSet("id", "isEnabled", "subject", "fromDate")
	immutable := ArgumentSet("id")

	t.Run("unchanged server set properties", func(t *testing.T) {
		got, err := ValidatePatch(current, json.RawMessage(`{"id":"singleton","subject":"Back"}`), allowed, immutable)
		if err != nil {
			t.Fatalf("ValidatePatch() error = %v", err)
		}
		if len(got.Immutable) != 0 || len(got.Unknown) != 0 {
			t.Fatalf("unchanged id was rejected: %+v", got)
		}
		if want := "Back"; string(got.Changed["subject"]) != `"`+want+`"` {
			t.Errorf("changed subject = %s, want %q", got.Changed["subject"], want)
		}
	})

	t.Run("null clears a nullable property", func(t *testing.T) {
		got, err := ValidatePatch(current, json.RawMessage(`{"subject":null}`), allowed, immutable)
		if err != nil {
			t.Fatalf("ValidatePatch() error = %v", err)
		}
		if value, ok := got.Changed["subject"]; !ok || string(value) != "null" {
			t.Errorf("cleared subject = %v (present=%v), want null", value, ok)
		}
		if _, ok := got.Changed["fromDate"]; ok {
			t.Errorf("already-null fromDate reported as changed: %v", got.Changed)
		}
	})

	t.Run("immutable and unknown properties", func(t *testing.T) {
		got, err := ValidatePatch(current, json.RawMessage(`{"id":"other","bogus":null}`), allowed, immutable)
		if err != nil {
			t.Fatalf("ValidatePatch() error = %v", err)
		}
		if !slices.Equal(got.Immutable, []string{"id"}) {
			t.Errorf("immutable = %v, want [id]", got.Immutable)
		}
		if !slices.Equal(got.Unknown, []string{"bogus"}) {
			t.Errorf("unknown = %v, want [bogus]", got.Unknown)
		}
	})

	t.Run("structural errors remain invalidPatch", func(t *testing.T) {
		_, err := ValidatePatch(current, json.RawMessage(`{"subject/name":"x"}`), allowed, immutable)
		if err == nil || !strings.HasPrefix(err.Error(), "invalidPatch:") {
			t.Fatalf("ValidatePatch() = %v, want invalidPatch", err)
		}
	})

	t.Run("repeating an unchanged value is not a change", func(t *testing.T) {
		got, err := ValidatePatch(
			json.RawMessage(`{"id":"singleton","subject":"Away"}`),
			json.RawMessage(`{"subject":"Away"}`),
			ArgumentSet("id", "subject"), immutable)
		if err != nil {
			t.Fatalf("ValidatePatch() error = %v", err)
		}
		if len(got.Changed) != 0 {
			t.Errorf("changed = %v, want no changes", got.Changed)
		}
	})
}

func TestApplyPatchRejectsInvalidPatches(t *testing.T) {
	current := json.RawMessage(`{"keywords":{"$seen":true},"list":["a"]}`)
	cases := []struct {
		name, patch string
	}{
		{"missing parent", `{"missing/child":true}`},
		{"array traversal", `{"list/0":"b"}`},
		{"prefix conflict", `{"keywords":{},"keywords/$seen":true}`},
		{"bad escape", `{"keywords/~2":"x"}`},
		{"empty pointer", `{"":true}`},
		{"not an object", `[]`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ApplyPatch(current, json.RawMessage(tc.patch))
			if err == nil || !strings.HasPrefix(err.Error(), "invalidPatch:") {
				t.Fatalf("ApplyPatch() = %v, want invalidPatch", err)
			}
		})
	}
}
