package receiver

import (
	"errors"
	"fmt"
	"strings"
)

// FallbackIdentity is the config key holding the base fallback rules. Every
// recipient without its own entry is routed by this entry.
const FallbackIdentity = "_"

type HandlerRule string

// delivers to a user IF THAT USER EXISTS
const DeliverToUser HandlerRule = "DeliverToUser"

// delivers to a special catchall mailbox, accessible by system administrators
const DeliverToMailIdentity HandlerRule = "DeliverToMailIdentity"

// sends a standard bounce
const Bounce HandlerRule = "Bounce"

// ErrIdentityConfigNotFound is returned when a store has no configuration for
// an identity. LoadMailIdentityConfig treats it as "seed the base fallback".
var ErrIdentityConfigNotFound = errors.New("mail identity config not found")

// HandlerRules is one ordered group of actions to try for a mail identity. The
// first rule that applies wins; DeliverToUser falls through when no such user
// exists.
type HandlerRules struct {
	MailIdentity string        `json:"mailIdentity" dynamodbav:"mailIdentity"`
	Rules        []HandlerRule `json:"rules" dynamodbav:"rules"`
}

// Validate checks that the rule list is non-empty and contains only known
// rules.
func (h HandlerRules) Validate() error {
	if len(h.Rules) == 0 {
		return errors.New("at least one handler rule is required")
	}
	for _, rule := range h.Rules {
		switch rule {
		case DeliverToUser, DeliverToMailIdentity, Bounce:
		default:
			return fmt.Errorf("unknown handler rule %q", rule)
		}
	}
	return nil
}

// deliveryIdentity resolves the mailbox used by DeliverToMailIdentity. A
// concrete MailIdentity wins; the wildcard (or an empty value) means the
// mailbox is named after the recipient's own identity, which administrators
// can open via masquerade even when no user account exists.
func (h HandlerRules) deliveryIdentity(recipient string) string {
	identity := normalizeIdentity(h.MailIdentity)
	if identity == "" || identity == FallbackIdentity || identity == "*" {
		return normalizeIdentity(recipient)
	}
	return identity
}

// MailIdentityConfig is the complete set of inbound routing rules, keyed by
// mail identity. The FallbackIdentity ("_") entry holds the base rules used
// for every identity without its own configuration.
type MailIdentityConfig map[string][]HandlerRules

// BaseMailIdentityConfig returns the seed written when a config store is
// empty: deliver to existing users, bounce everyone else.
func BaseMailIdentityConfig() MailIdentityConfig {
	return MailIdentityConfig{
		FallbackIdentity: []HandlerRules{{
			MailIdentity: FallbackIdentity,
			Rules:        []HandlerRule{DeliverToUser, Bounce},
		}},
	}
}

// Get returns the rule groups for identity. Matching is case-insensitive, and
// identities without their own entry use the FallbackIdentity ("_") entry. It
// returns an error when neither exists so a misconfigured store cannot make
// the receiver drop mail silently.
func (c MailIdentityConfig) Get(identity string) ([]HandlerRules, error) {
	if rules, ok := c.lookup(identity); ok {
		return cloneRules(rules), nil
	}
	if rules, ok := c.lookup(FallbackIdentity); ok {
		return cloneRules(rules), nil
	}
	return nil, fmt.Errorf("%w: no rules for %q and no %q fallback", ErrIdentityConfigNotFound, identity, FallbackIdentity)
}

func (c MailIdentityConfig) lookup(identity string) ([]HandlerRules, bool) {
	key := normalizeIdentity(identity)
	if key == "" {
		return nil, false
	}
	if rules, ok := c[key]; ok {
		return rules, true
	}
	// Config files are normalised when loaded, but tolerate hand-written keys.
	for stored, rules := range c {
		if normalizeIdentity(stored) == key {
			return rules, true
		}
	}
	return nil, false
}

// Validate checks that the fallback exists and that every rule group is
// usable. It is called after loading a store, and by NewReceiver.
func (c MailIdentityConfig) Validate() error {
	var errs []error
	if _, ok := c[FallbackIdentity]; !ok {
		errs = append(errs, fmt.Errorf("missing %q fallback configuration", FallbackIdentity))
	}
	for identity, groups := range c {
		if strings.TrimSpace(identity) == "" {
			errs = append(errs, errors.New("mail identity key cannot be empty"))
			continue
		}
		if len(groups) == 0 {
			errs = append(errs, fmt.Errorf("identity %q has no rule groups", identity))
			continue
		}
		for i, group := range groups {
			if err := group.Validate(); err != nil {
				errs = append(errs, fmt.Errorf("identity %q rule group %d: %w", identity, i, err))
			}
		}
	}
	return errors.Join(errs...)
}

func normalizeIdentity(identity string) string {
	return strings.ToLower(strings.TrimSpace(identity))
}

func cloneRules(rules []HandlerRules) []HandlerRules {
	if rules == nil {
		return nil
	}
	clone := make([]HandlerRules, len(rules))
	for i, group := range rules {
		group.Rules = append([]HandlerRule(nil), group.Rules...)
		clone[i] = group
	}
	return clone
}

func validateConfigKey(identity string) error {
	key := strings.TrimSpace(identity)
	if key == "" || key == "." || key == ".." || strings.ContainsAny(key, "/\\") {
		return fmt.Errorf("invalid mail identity config key %q", identity)
	}
	return nil
}
