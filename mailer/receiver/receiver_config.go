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

// discards the message without delivering it or notifying the sender
const Drop HandlerRule = "Drop"

// ErrIdentityConfigNotFound is returned when a store has no configuration for
// an identity. LoadMailIdentityConfig treats it as "seed the base fallback".
var ErrIdentityConfigNotFound = errors.New("mail identity config not found")

// ErrFallbackIdentity is returned when a management operation tries to delete
// the base fallback rules. Every recipient without its own entry relies on
// them, so the fallback can never be removed.
var ErrFallbackIdentity = errors.New("cannot delete the fallback receiving identity")

// HandlerRules is the ordered rules for one mail identity. It is a struct
// rather than a bare []HandlerRule only so that it can hold its own key: the
// MailIdentity field repeats the map key, which lets the management API turn
// the config into a list without losing which identity each entry belongs to.
// The first applicable rule wins; DeliverToUser falls through when no such
// user exists.
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
		case DeliverToUser, DeliverToMailIdentity, Bounce, Drop:
		default:
			return fmt.Errorf("unknown handler rule %q", rule)
		}
	}
	return nil
}

// deliveryIdentity resolves the mailbox used by DeliverToMailIdentity. The
// identity's own key normally names the catchall mailbox; the wildcard (or an
// empty value, including the "_" fallback) means the mailbox is named after
// the recipient, which administrators can open via masquerade even when no
// user account exists.
func (h HandlerRules) deliveryIdentity(recipient string) string {
	identity := normalizeIdentity(h.MailIdentity)
	if identity == "" || identity == FallbackIdentity || identity == "*" {
		return normalizeIdentity(recipient)
	}
	return identity
}

// MailIdentityConfig is the complete set of inbound routing rules, keyed by
// mail identity. Each identity has exactly one ordered HandlerRules list. The
// FallbackIdentity ("_") entry holds the base rules used for every identity
// without its own configuration.
type MailIdentityConfig map[string]HandlerRules

// BaseMailIdentityConfig returns the seed written when a config store is
// empty: deliver to existing users, bounce everyone else.
func BaseMailIdentityConfig() MailIdentityConfig {
	return MailIdentityConfig{
		FallbackIdentity: {
			MailIdentity: FallbackIdentity,
			Rules:        []HandlerRule{DeliverToUser, Bounce},
		},
	}
}

// MailIdentityForRecipient returns the mail identity whose rules apply to
// recipient: the hostname after the last "@", normalised for case and space.
// A recipient without a hostname uses the fallback identity. Callers must use
// this value with GetByIdentity, so every rule lookup is keyed by mail identity.
func MailIdentityForRecipient(recipient string) string {
	at := strings.LastIndex(recipient, "@")
	if at < 0 || at == len(recipient)-1 {
		return FallbackIdentity
	}
	return normalizeIdentity(recipient[at+1:])
}

// GetByIdentity returns the rules for identity. Matching is case-insensitive,
// and identities without their own entry use the FallbackIdentity ("_") entry.
// It returns an error when neither exists so a misconfigured store cannot make
// the receiver drop mail silently.
func (c MailIdentityConfig) GetByIdentity(identity string) (HandlerRules, error) {
	if rules, ok := c.lookup(identity); ok {
		return cloneHandlerRules(rules), nil
	}
	if rules, ok := c.lookup(FallbackIdentity); ok {
		return cloneHandlerRules(rules), nil
	}
	return HandlerRules{}, fmt.Errorf("%w: no rules for %q and no %q fallback", ErrIdentityConfigNotFound, identity, FallbackIdentity)
}

// Has reports whether identity has its own rules entry. Unlike Get it does
// not consult the fallback entry, so callers can tell explicitly configured
// identities from those relying on "_".
func (c MailIdentityConfig) Has(identity string) bool {
	_, ok := c.lookup(identity)
	return ok
}

// Clone returns a deep copy of the configuration. It is used by management
// operations which build a replacement config before publishing it.
func (c MailIdentityConfig) Clone() MailIdentityConfig {
	if c == nil {
		return nil
	}
	clone := make(MailIdentityConfig, len(c))
	for identity, rules := range c {
		clone[identity] = cloneHandlerRules(rules)
	}
	return clone
}

// ValidateConfigKey checks that identity is safe to use as a config store
// key. The stores call this on Put; management APIs call it before writing.
func ValidateConfigKey(identity string) error {
	return validateConfigKey(identity)
}

func (c MailIdentityConfig) lookup(identity string) (HandlerRules, bool) {
	key := normalizeIdentity(identity)
	if key == "" {
		return HandlerRules{}, false
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
	return HandlerRules{}, false
}

// Validate checks that the fallback exists and that every identity has a
// usable rule list. It is called after loading a store, and by NewReceiver.
func (c MailIdentityConfig) Validate() error {
	var errs []error
	if _, ok := c[FallbackIdentity]; !ok {
		errs = append(errs, fmt.Errorf("missing %q fallback configuration", FallbackIdentity))
	}
	for identity, rules := range c {
		if strings.TrimSpace(identity) == "" {
			errs = append(errs, errors.New("mail identity key cannot be empty"))
			continue
		}
		if err := rules.Validate(); err != nil {
			errs = append(errs, fmt.Errorf("identity %q: %w", identity, err))
		}
	}
	return errors.Join(errs...)
}

func normalizeIdentity(identity string) string {
	return strings.ToLower(strings.TrimSpace(identity))
}

func cloneHandlerRules(rules HandlerRules) HandlerRules {
	rules.Rules = append([]HandlerRule(nil), rules.Rules...)
	return rules
}

func validateConfigKey(identity string) error {
	key := strings.TrimSpace(identity)
	if key == "" || key == "." || key == ".." || strings.ContainsAny(key, "/\\") {
		return fmt.Errorf("invalid mail identity config key %q", identity)
	}
	return nil
}

// validateDeletableConfigKey checks an identity before a store deletes its
// rules entry. The fallback is deliberately excluded: without it the receiver
// would have no rules for recipients that do not have their own entry.
func validateDeletableConfigKey(identity string) error {
	if err := validateConfigKey(identity); err != nil {
		return err
	}
	if normalizeIdentity(identity) == FallbackIdentity {
		return ErrFallbackIdentity
	}
	return nil
}
