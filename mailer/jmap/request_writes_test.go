package jmap

import "testing"

func TestRequestHasWriteMethod(t *testing.T) {
	cases := []struct {
		name string
		req  *Request
		want bool
	}{
		{"nil request", nil, false},
		{"read only batch", &Request{MethodCalls: []Invocation{
			{Name: "Mailbox/query"}, {Name: "Mailbox/get"},
		}}, false},
		{"empty batch", &Request{}, false},
		{"set method", &Request{MethodCalls: []Invocation{{Name: "Email/set"}}}, true},
		{"set after reads", &Request{MethodCalls: []Invocation{
			{Name: "Email/query"}, {Name: "Mailbox/set"},
		}}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.req.HasWriteMethod(); got != tc.want {
				t.Errorf("HasWriteMethod() = %v, want %v", got, tc.want)
			}
		})
	}
}
