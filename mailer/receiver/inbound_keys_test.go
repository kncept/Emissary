package receiver

import "testing"

func TestIsInboundEmailKey(t *testing.T) {
	tests := []struct {
		key  string
		want bool
	}{
		{key: "01000192a3b4c5d6", want: true},
		{key: "message.eml", want: true},
		{key: "receiver/kncept.com.json", want: false},
		{key: "receiver/_.json", want: false},
		{key: "some/nested/key", want: false},
		{key: "/leading-slash", want: false},
		{key: "trailing-slash/", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			if got := IsInboundEmailKey(tt.key); got != tt.want {
				t.Errorf("IsInboundEmailKey(%q) = %v, want %v", tt.key, got, tt.want)
			}
		})
	}
}
