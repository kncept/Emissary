package storage

import "context"

// PushKeys holds the RFC 8291 client keys used to encrypt pushed data.
type PushKeys struct {
	P256DH string `json:"p256dh"`
	Auth   string `json:"auth"`
}

// PushSubscription is the durable, private form of a JMAP PushSubscription.
// URL, Keys and VerificationCode must never be returned by a JMAP method;
// those values are stored here only so the server can send push messages.
type PushSubscription struct {
	ID               string    `json:"id"`
	CredentialID     string    `json:"credentialId"`
	DeviceClientID   string    `json:"deviceClientId"`
	URL              string    `json:"url"`
	Keys             *PushKeys `json:"keys,omitempty"`
	VerificationCode string    `json:"verificationCode,omitempty"`
	Verified         bool      `json:"verified"`
	Expires          string    `json:"expires,omitempty"`
	Types            []string  `json:"types,omitempty"`
}

// PushSubscriptionStore persists subscriptions per authentication credential.
// Subscriptions are account-less in JMAP but must only be visible to the
// credentials that created them.
type PushSubscriptionStore interface {
	LoadPushSubscriptions(ctx context.Context, credentialID string) ([]PushSubscription, error)
	SavePushSubscriptions(ctx context.Context, credentialID string, subscriptions []PushSubscription) error
	DeletePushSubscriptions(ctx context.Context, credentialID string) error
}

func clonePushSubscriptions(source []PushSubscription) []PushSubscription {
	out := make([]PushSubscription, 0, len(source))
	for _, sub := range source {
		copy := sub
		if sub.Keys != nil {
			keys := *sub.Keys
			copy.Keys = &keys
		}
		if sub.Types != nil {
			copy.Types = append([]string(nil), sub.Types...)
		}
		out = append(out, copy)
	}
	return out
}
