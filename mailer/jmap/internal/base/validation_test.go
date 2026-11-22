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
