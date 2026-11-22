package base

import (
	"encoding/json"
	"reflect"
	"testing"
)

// TestSetErrorVocabulary asserts that every declared SetError type is known
// and that each constructor produces the RFC-mandated JSON fields.
func TestSetErrorVocabulary(t *testing.T) {
	types := []string{
		"forbidden", "notFound", "invalidProperties", "invalidPatch",
		"stateMismatch", "alreadyExists", "overQuota", "tooLarge",
		"mailboxHasChild", "mailboxHasEmail", "invalidEmail", "tooManyRecipients",
		"forbiddenMailFrom", "forbiddenFrom", "forbiddenToSend", "cannotUnsend",
		"rateLimit", "willDestroy", "singleton", "blobNotFound", "serverFail",
		"invalidRecipients", "noRecipients", "invalidArguments",
	}
	for _, typ := range types {
		t.Run("declared/"+typ, func(t *testing.T) {
			if !IsSetErrorType(typ) {
				t.Errorf("IsSetErrorType(%q) = false, want true", typ)
			}
		})
	}
	if IsSetErrorType("notAnError") {
		t.Error("IsSetErrorType(\"notAnError\") = true, want false")
	}

	cases := []struct {
		name string
		err  SetError
		want map[string]any
	}{
		{
			name: "forbidden", err: NewSetError("forbidden", "no access"),
			want: map[string]any{"type": "forbidden", "description": "no access"},
		},
		{
			name: "notFound", err: NewSetError("notFound", "missing"),
			want: map[string]any{"type": "notFound", "description": "missing"},
		},
		{
			name: "invalidProperties", err: InvalidProperties("bad fields", "bogus", "id"),
			want: map[string]any{"type": "invalidProperties", "description": "bad fields", "properties": []any{"bogus", "id"}},
		},
		{
			name: "invalidPatch", err: NewSetError("invalidPatch", "bad pointer"),
			want: map[string]any{"type": "invalidPatch", "description": "bad pointer"},
		},
		{
			name: "stateMismatch", err: NewSetError("stateMismatch", "stale state"),
			want: map[string]any{"type": "stateMismatch", "description": "stale state"},
		},
		{
			name: "alreadyExists", err: AlreadyExists("duplicate", "existing-1"),
			want: map[string]any{"type": "alreadyExists", "description": "duplicate", "existingId": "existing-1"},
		},
		{
			name: "overQuota", err: NewSetError("overQuota", "quota reached"),
			want: map[string]any{"type": "overQuota", "description": "quota reached"},
		},
		{
			name: "tooLarge", err: TooLarge("message too big", 1024),
			want: map[string]any{"type": "tooLarge", "description": "message too big", "maxSize": float64(1024)},
		},
		{
			name: "mailboxHasChild", err: NewSetError("mailboxHasChild", "has children"),
			want: map[string]any{"type": "mailboxHasChild", "description": "has children"},
		},
		{
			name: "mailboxHasEmail", err: NewSetError("mailboxHasEmail", "has mail"),
			want: map[string]any{"type": "mailboxHasEmail", "description": "has mail"},
		},
		{
			name: "forbiddenFrom", err: NewSetError("forbiddenFrom", "bad sender"),
			want: map[string]any{"type": "forbiddenFrom", "description": "bad sender"},
		},
		{
			name: "invalidEmail", err: InvalidEmail("bad Email properties", "to", "subject"),
			want: map[string]any{
				"type": "invalidEmail", "description": "bad Email properties",
				"properties": []any{"to", "subject"},
			},
		},
		{
			name: "tooManyRecipients", err: TooManyRecipients("too many", 50),
			want: map[string]any{"type": "tooManyRecipients", "description": "too many", "maxRecipients": float64(50)},
		},
		{
			name: "invalidRecipients", err: InvalidRecipients("bad rcptTo", []string{"nope", "also-nope"}),
			want: map[string]any{
				"type": "invalidRecipients", "description": "bad rcptTo",
				"invalidRecipients": []any{"nope", "also-nope"},
			},
		},
		{
			name: "cannotUnsend", err: NewSetError("cannotUnsend", "already relayed"),
			want: map[string]any{"type": "cannotUnsend", "description": "already relayed"},
		},
		{
			name: "blobNotFound", err: BlobNotFound("missing blobs", []string{"b1"}),
			want: map[string]any{"type": "blobNotFound", "description": "missing blobs", "notFound": []any{"b1"}},
		},
		{
			name: "rateLimit", err: NewSetError("rateLimit", "slow down"),
			want: map[string]any{"type": "rateLimit", "description": "slow down"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := json.Marshal(tc.err)
			if err != nil {
				t.Fatalf("Marshal() error = %v", err)
			}
			var got map[string]any
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Fatalf("Unmarshal() error = %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("SetError JSON = %v, want %v", got, tc.want)
			}
			if !IsSetErrorType(tc.err.Type) {
				t.Errorf("emitted type %q is not in the vocabulary", tc.err.Type)
			}
		})
	}
}
