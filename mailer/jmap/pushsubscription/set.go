package pushsubscription

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/kncept/Emissary/mailer/components/storage"
	"github.com/kncept/Emissary/mailer/jmap/internal/base"
)

const (
	// RFC 8620 section 8.6 requires limits on the number of push subscriptions
	// and their creation rate so a user cannot turn the verification request
	// into a denial-of-service vector.
	maxPushSubscriptionsPerCredential = 20
	maxPushCreatesPerMinute           = 10
	pushCreateRateWindow              = time.Minute
	maxVerificationRedirects          = 3
)

var setArgumentNames = map[string]bool{
	"create":  true,
	"update":  true,
	"destroy": true,
}

// PushSubscriptionSet creates, updates and destroys subscriptions scoped to
// the current credentials. It is account-less and has no state arguments or
// response values (RFC 8620 section 7.2.2).
func (h *Handler) PushSubscriptionSet(ctx context.Context, _ string, args json.RawMessage) (json.RawMessage, error) {
	unlock := h.lockCredential(CredentialID(ctx))
	defer unlock()

	if err := validateArguments(args, setArgumentNames); err != nil {
		return nil, err
	}
	var req struct {
		Create  map[string]json.RawMessage `json:"create,omitempty"`
		Update  map[string]json.RawMessage `json:"update,omitempty"`
		Destroy []string                   `json:"destroy,omitempty"`
	}
	if err := json.Unmarshal(args, &req); err != nil {
		return nil, base.Errorf("invalidArguments", "invalid PushSubscription/set arguments")
	}

	credentialID := CredentialID(ctx)
	subs, err := h.load(ctx, credentialID)
	if err != nil {
		return nil, base.Errorf("serverFail", "could not load push subscriptions: %v", err)
	}
	indexByID := make(map[string]int, len(subs))
	for i := range subs {
		indexByID[subs[i].ID] = i
	}
	destroyedFlags := make([]bool, len(subs))

	created := make(map[string]any)
	updated := make(map[string]any)
	destroyed := []string{}
	notCreated := make(map[string]setError)
	notUpdated := make(map[string]setError)
	notDestroyed := make(map[string]setError)

	type verification struct {
		id, target, code string
	}
	verifications := []verification{}
	mutated := false

	for createID, raw := range req.Create {
		fields, err := h.decodeCreate(ctx, raw)
		if err != nil {
			notCreated[createID] = setErrorOf("invalidProperties", err.Error())
			continue
		}
		if err := h.allowCreate(ctx, credentialID, len(subs)); err != nil {
			notCreated[createID] = setErrorOf("rateLimit", err.Error())
			continue
		}
		id, err := randomToken(16)
		if err != nil {
			notCreated[createID] = setErrorOf("serverFail", "could not create subscription id")
			continue
		}
		code, err := randomToken(32)
		if err != nil {
			notCreated[createID] = setErrorOf("serverFail", "could not create verification code")
			continue
		}
		sub := storage.PushSubscription{
			ID:               id,
			CredentialID:     credentialID,
			DeviceClientID:   fields.deviceClientID,
			URL:              fields.url,
			Keys:             fields.keys,
			VerificationCode: code,
			Expires:          fields.expires,
			Types:            fields.types,
		}
		indexByID[id] = len(subs)
		subs = append(subs, sub)
		destroyedFlags = append(destroyedFlags, false)
		created[createID] = toView(sub)
		verifications = append(verifications, verification{id, fields.url, code})
		mutated = true
	}

	for id, raw := range req.Update {
		index, ok := indexByID[id]
		if !ok || destroyedFlags[index] {
			notUpdated[id] = setErrorOf("notFound", "PushSubscription not found")
			continue
		}
		fields, err := decodeUpdate(raw, subs[index])
		if err != nil {
			notUpdated[id] = setErrorOf("invalidProperties", err.Error())
			continue
		}
		if fields.verified {
			subs[index].Verified = true
		}
		if fields.clearExpires {
			subs[index].Expires = ""
		} else if fields.expires != "" {
			subs[index].Expires = fields.expires
		}
		updated[id] = toView(subs[index])
		mutated = true
	}

	destroyedIDs := make(map[string]bool, len(req.Destroy))
	for _, id := range req.Destroy {
		if destroyedIDs[id] {
			continue
		}
		destroyedIDs[id] = true
		index, ok := indexByID[id]
		if !ok || destroyedFlags[index] {
			notDestroyed[id] = setErrorOf("notFound", "PushSubscription not found")
			continue
		}
		erase(&subs[index])
		destroyedFlags[index] = true
		destroyed = append(destroyed, id)
		mutated = true
	}

	if mutated {
		kept := make([]storage.PushSubscription, 0, len(subs))
		for i, sub := range subs {
			if !destroyedFlags[i] {
				kept = append(kept, sub)
			}
		}
		if err := h.deps.Storage.SavePushSubscriptions(ctx, credentialID, kept); err != nil {
			return nil, base.Errorf("serverFail", "could not save push subscriptions: %v", err)
		}
	}

	// The verification POST must happen as soon as the subscription is stored.
	// A failure is logged rather than reported as a set error: the record is
	// already created, and RFC 8620 gives no set error for a failed push. No
	// other request will be made to this URL until the code is verified.
	for _, item := range verifications {
		if err := h.sendVerification(ctx, item.id, item.target, item.code); err != nil {
			slog.Warn("JMAP: push verification failed", "subscription", item.id, "err", err)
		}
	}

	return base.SetResult{
		AccountLess:  true,
		Created:      created,
		Updated:      updated,
		Destroyed:    destroyed,
		NotCreated:   notCreated,
		NotUpdated:   notUpdated,
		NotDestroyed: notDestroyed,
	}.Marshal()
}

type createFields struct {
	deviceClientID string
	url            string
	keys           *storage.PushKeys
	expires        string
	types          []string
}

var createPropertyNames = map[string]bool{
	"deviceClientId":   true,
	"url":              true,
	"keys":             true,
	"expires":          true,
	"types":            true,
	"verificationCode": true,
}

func (h *Handler) decodeCreate(ctx context.Context, raw json.RawMessage) (createFields, error) {
	var fields createFields
	var values map[string]json.RawMessage
	if err := json.Unmarshal(raw, &values); err != nil {
		return fields, fmt.Errorf("PushSubscription value must be an object")
	}
	for name, value := range values {
		if !createPropertyNames[name] {
			return fields, fmt.Errorf("unknown or immutable property %q", name)
		}
		if name == "verificationCode" && !isJSONNull(value) {
			return fields, fmt.Errorf("verificationCode must be null or omitted when created")
		}
	}

	if value, ok := values["deviceClientId"]; ok {
		if err := json.Unmarshal(value, &fields.deviceClientID); err != nil || strings.TrimSpace(fields.deviceClientID) == "" {
			return fields, fmt.Errorf("deviceClientId is required")
		}
	} else {
		return fields, fmt.Errorf("deviceClientId is required")
	}
	if value, ok := values["url"]; ok {
		if err := json.Unmarshal(value, &fields.url); err != nil {
			return fields, fmt.Errorf("url must be a string")
		}
		if err := h.validatePushURL(ctx, fields.url); err != nil {
			return fields, err
		}
	} else {
		return fields, fmt.Errorf("url is required")
	}
	if value, ok := values["keys"]; ok && !isJSONNull(value) {
		keys, err := decodeKeys(value)
		if err != nil {
			return fields, err
		}
		fields.keys = keys
	}
	if value, ok := values["expires"]; ok {
		expires, err := decodeExpiry(value, false)
		if err != nil {
			return fields, err
		}
		fields.expires = expires
	}
	if value, ok := values["types"]; ok && !isJSONNull(value) {
		if err := json.Unmarshal(value, &fields.types); err != nil {
			return fields, fmt.Errorf("types must be an array of strings or null")
		}
	}
	return fields, nil
}

type updateFields struct {
	verified     bool
	expires      string
	clearExpires bool
}

var updatePropertyNames = map[string]bool{
	"verificationCode": true,
	"expires":          true,
}

func decodeUpdate(raw json.RawMessage, current storage.PushSubscription) (updateFields, error) {
	var fields updateFields
	var values map[string]json.RawMessage
	if err := json.Unmarshal(raw, &values); err != nil {
		return fields, fmt.Errorf("PushSubscription value must be an object")
	}
	for name := range values {
		if !updatePropertyNames[name] {
			return fields, fmt.Errorf("property %q cannot be updated", name)
		}
	}
	if value, ok := values["verificationCode"]; ok {
		var code string
		if err := json.Unmarshal(value, &code); err != nil || code == "" || code != current.VerificationCode {
			return fields, fmt.Errorf("verificationCode is invalid")
		}
		fields.verified = true
	}
	if value, ok := values["expires"]; ok {
		expires, err := decodeExpiry(value, true)
		if err != nil {
			return fields, err
		}
		fields.expires = expires
		fields.clearExpires = isJSONNull(value)
	}
	return fields, nil
}

func isJSONNull(raw json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

// validatePushURL enforces the structural URL rules from RFC 8620 section 7.2
// and the SSRF protection required by section 8.6. Every address the host
// resolves to must be publicly routable; refusing private, loopback,
// link-local and similar addresses keeps the verification POST off internal
// networks and cloud metadata endpoints.
func (h *Handler) validatePushURL(ctx context.Context, value string) error {
	return validatePushURL(ctx, value, h.allowPrivateURLs)
}

// ValidateURL applies the production URL validation outside the set handler.
// Webhook delivery revalidates both the stored URL and redirects with it.
func ValidateURL(ctx context.Context, value string) error {
	return validatePushURL(ctx, value, false)
}

func validatePushURL(ctx context.Context, value string, allowPrivate bool) error {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return fmt.Errorf("url must be an absolute https:// URL")
	}
	if parsed.User != nil {
		return fmt.Errorf("url must not contain credentials")
	}
	if allowPrivate {
		return nil
	}

	host := parsed.Hostname()
	if host == "" {
		return fmt.Errorf("url must have a host")
	}
	var addresses []net.IP
	if literal := net.ParseIP(host); literal != nil {
		addresses = []net.IP{literal}
	} else {
		lookups, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil {
			return fmt.Errorf("url host could not be resolved: %v", err)
		}
		for _, lookup := range lookups {
			addresses = append(addresses, lookup.IP)
		}
	}
	if len(addresses) == 0 {
		return fmt.Errorf("url host has no addresses")
	}
	for _, address := range addresses {
		if !isPublicPushIP(address) {
			return fmt.Errorf("url host must resolve to a public address")
		}
	}
	return nil
}

func isPublicPushIP(ip net.IP) bool {
	return !ip.IsLoopback() &&
		!ip.IsPrivate() &&
		!ip.IsLinkLocalUnicast() &&
		!ip.IsLinkLocalMulticast() &&
		!ip.IsMulticast() &&
		!ip.IsUnspecified()
}

// pushRateKey is a synthetic JMAP-state key holding the recent creation
// timestamps for one credential. Sharing it through durable storage means the
// RFC 8620 section 8.6 rate limit applies across Lambda instances, not just
// within one process.
func pushRateKey(credentialID string) string { return "push:ratelimit:" + credentialID }

// allowCreate enforces the per-credential subscription count and creation
// rate limits. Callers mark a create as failed with a rateLimit SetError when
// this returns an error. The count comes from the durable subscription store;
// the sliding window is updated in the same revision-aware state family used
// by the change tracker and created ids.
func (h *Handler) allowCreate(ctx context.Context, credentialID string, activeCount int) error {
	if activeCount >= maxPushSubscriptionsPerCredential {
		return fmt.Errorf("push subscription limit reached")
	}
	now := time.Now()
	cutoff := now.Add(-pushCreateRateWindow).UnixNano()
	limited := false
	err := base.UpdateState(ctx, h.deps.Storage, pushRateKey(credentialID), func(data []byte) ([]byte, bool, error) {
		var stamps []int64
		if len(data) != 0 {
			if err := json.Unmarshal(data, &stamps); err != nil {
				return nil, false, err
			}
		}
		kept := stamps[:0]
		for _, stamp := range stamps {
			if stamp > cutoff {
				kept = append(kept, stamp)
			}
		}
		if len(kept) >= maxPushCreatesPerMinute {
			limited = true
			return nil, false, nil
		}
		kept = append(kept, now.UnixNano())
		next, err := json.Marshal(kept)
		return next, true, err
	})
	if err != nil {
		return fmt.Errorf("record push creation: %w", err)
	}
	if limited {
		return fmt.Errorf("push subscription creation rate limit reached")
	}
	return nil
}

func decodeKeys(raw json.RawMessage) (*storage.PushKeys, error) {
	var keys storage.PushKeys
	if err := json.Unmarshal(raw, &keys); err != nil {
		return nil, fmt.Errorf("keys must contain p256dh and auth")
	}
	for name, value := range map[string]string{"p256dh": keys.P256DH, "auth": keys.Auth} {
		if value == "" {
			return nil, fmt.Errorf("keys.%s is required", name)
		}
		if _, err := decodeBase64URL(value); err != nil {
			return nil, fmt.Errorf("keys.%s must be URL-safe base64", name)
		}
	}
	return &keys, nil
}

func decodeBase64URL(value string) ([]byte, error) {
	if decoded, err := base64.RawURLEncoding.DecodeString(value); err == nil && len(decoded) > 0 {
		return decoded, nil
	}
	if decoded, err := base64.URLEncoding.DecodeString(value); err == nil && len(decoded) > 0 {
		return decoded, nil
	}
	return nil, fmt.Errorf("invalid URL-safe base64")
}

func decodeExpiry(raw json.RawMessage, allowNull bool) (string, error) {
	if isJSONNull(raw) {
		if allowNull {
			return "", nil
		}
		return "", fmt.Errorf("expires must be a UTCDate")
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", fmt.Errorf("expires must be a UTCDate")
	}
	expires, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return "", fmt.Errorf("expires must be a UTCDate")
	}
	if !expires.After(time.Now()) {
		return "", fmt.Errorf("expires must be in the future")
	}
	return expires.UTC().Format(time.RFC3339), nil
}

func (h *Handler) sendVerification(ctx context.Context, id, target, code string) error {
	body, err := json.Marshal(map[string]string{
		"@type":              "PushVerification",
		"pushSubscriptionId": id,
		"verificationCode":   code,
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("TTL", verificationTTL)

	// Use a shallow copy of the configured client so redirect validation does
	// not affect any other outbound request. Each redirect target is checked
	// with the same SSRF rules as the original URL, preventing an apparently
	// external URL from bouncing to an internal service.
	baseClient := h.deps.HTTPClient
	if baseClient == nil {
		baseClient = http.DefaultClient
	}
	client := *baseClient
	client.CheckRedirect = func(redirect *http.Request, via []*http.Request) error {
		if len(via) >= maxVerificationRedirects {
			return fmt.Errorf("too many push verification redirects")
		}
		return h.validatePushURL(redirect.Context(), redirect.URL.String())
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("verification endpoint returned %s", resp.Status)
	}
	return nil
}
