package email

import (
	"reflect"
	"strings"
	"testing"
)

func TestEnvelopeRecipients(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want []string
	}{
		{
			name: "single bracketed recipient",
			raw: "Received: from mail.example.com by inbound-smtp.us-east-1.amazonaws.com " +
				"with SMTP id abc123 for <Alice@Example.com>; Fri, 08 Mar 2024 15:30:00 +0000\r\n" +
				"To: someone@example.net\r\n\r\nbody\r\n",
			want: []string{"alice@example.com"},
		},
		{
			name: "multiple recipients in one header",
			raw: "Received: from mail.example.com by inbound-smtp.us-east-1.amazonaws.com " +
				"with SMTP id abc123 for <alice@example.com> for <BOB@example.com> " +
				"for <alice@example.com>; Fri, 08 Mar 2024 15:30:00 +0000\r\n\r\nbody\r\n",
			want: []string{"alice@example.com", "bob@example.com"},
		},
		{
			name: "unbracketed recipient",
			raw:  "Received: by inbound-smtp.example.com for alice@example.com; Fri, 08 Mar 2024 15:30:00 +0000\r\n\r\nbody\r\n",
			want: []string{"alice@example.com"},
		},
		{
			name: "uses first Received header with a for clause",
			raw: "Received: by inbound-smtp.example.com for <alias@example.com>; Fri, 08 Mar 2024 15:30:00 +0000\r\n" +
				"Received: from list.other.example by relay.other.example for <list@other.example>; Thu, 07 Mar 2024 10:00:00 +0000\r\n" +
				"\r\nbody\r\n",
			want: []string{"alias@example.com"},
		},
		{
			name: "skips Received headers without a for clause",
			raw: "Received: from mail.example.com by inbound-smtp.example.com with SMTP id abc123; Fri, 08 Mar 2024 15:30:00 +0000\r\n" +
				"Received: by inbound-smtp.example.com for <alice@example.com>; Fri, 08 Mar 2024 15:29:00 +0000\r\n\r\nbody\r\n",
			want: []string{"alice@example.com"},
		},
		{
			name: "no Received header",
			raw:  "From: sender@example.com\r\nTo: alice@example.com\r\n\r\nbody\r\n",
			want: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := EnvelopeRecipients(ParseWireFormat("", strings.NewReader(tt.raw)))
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("EnvelopeRecipients() = %v, want %v", got, tt.want)
			}
		})
	}

	if got := EnvelopeRecipients(nil); got != nil {
		t.Errorf("EnvelopeRecipients(nil) = %v, want nil", got)
	}
}
