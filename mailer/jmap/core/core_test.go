package core

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
)

func TestCoreEcho(t *testing.T) {
	h := New()
	for _, tc := range []struct {
		name string
		args json.RawMessage
	}{
		{"empty object", json.RawMessage(`{}`)},
		{"nested", json.RawMessage(`{"a":1,"b":[true,null,{"c":"d"}]}`)},
		{"null", json.RawMessage(`null`)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := h.CoreEcho(context.Background(), "ignored@example.com", tc.args)
			if err != nil {
				t.Fatalf("CoreEcho() error = %v", err)
			}
			if !reflect.DeepEqual([]byte(got), []byte(tc.args)) {
				t.Errorf("CoreEcho() = %s, want %s", got, tc.args)
			}
		})
	}
}

func TestMethods(t *testing.T) {
	if got, want := Methods(), []string{MethodEcho}; !reflect.DeepEqual(got, want) {
		t.Errorf("Methods() = %v, want %v", got, want)
	}
}
