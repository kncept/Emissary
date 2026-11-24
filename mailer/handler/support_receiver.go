package handler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"github.com/aws/aws-sdk-go-v2/service/ses"
	supportapi "github.com/kncept/Emissary/api/support/generated"
	"github.com/kncept/Emissary/mailer/identity"
	"github.com/kncept/Emissary/mailer/receiver"
)

// identityLister returns the caller-supplied lister or the shared lazy SES
// provider. Creating the provider does not load AWS config until it is used.
func (h *supportHandler) identityLister(context.Context) (SESIdentityLister, error) {
	h.sesMu.Lock()
	defer h.sesMu.Unlock()
	if h.sesIdentities == nil {
		h.sesIdentities = identity.NewLazyLister()
	}
	return h.sesIdentities, nil
}

// AutocompleteReceiverUsers searches users for the receiver rules editor. It
// deliberately mirrors the masquerade picker's autocomplete: a three-character
// minimum and the configured user manager's case-insensitive substring search,
// capped at ten suggestions. System Managers can use it without also holding
// the user-management claim.
func (h *supportHandler) AutocompleteReceiverUsers(ctx context.Context, params supportapi.AutocompleteReceiverUsersParams) (supportapi.AutocompleteReceiverUsersRes, error) {
	if !isSystemManager(ctx) {
		return &supportapi.AutocompleteReceiverUsersForbidden{Error: forbiddenError().Error}, nil
	}
	q := strings.TrimSpace(params.Q)
	if len(q) < 3 {
		return &supportapi.AutocompleteReceiverUsersBadRequest{Error: "q must be at least 3 characters"}, nil
	}
	if h.users == nil {
		slog.Error("Support: user search requested without a user manager")
		return &supportapi.AutocompleteReceiverUsersInternalServerError{Error: "user manager is not configured"}, nil
	}

	matches, err := h.users.Autocomplete(ctx, q)
	if err != nil {
		slog.Error("Support: failed to search users for receiver rules", "err", err)
		return &supportapi.AutocompleteReceiverUsersInternalServerError{Error: "failed to search users"}, nil
	}
	if matches == nil {
		matches = []string{}
	}
	if len(matches) > 10 {
		matches = matches[:10]
	}
	return &supportapi.ReceiverUserSuggestions{Users: matches}, nil
}

// ListReceiverIdentities returns the verified SES identities with a flag for
// each identity that has its own receiver rules entry.
func (h *supportHandler) ListReceiverIdentities(ctx context.Context) (supportapi.ListReceiverIdentitiesRes, error) {
	if !isSystemManager(ctx) {
		return &supportapi.ListReceiverIdentitiesForbidden{Error: forbiddenError().Error}, nil
	}

	lister, err := h.identityLister(ctx)
	if err != nil {
		slog.Error("Support: failed to configure SES client", "err", err)
		return &supportapi.ListReceiverIdentitiesBadGateway{Error: "failed to list SES identities"}, nil
	}

	identities := make([]string, 0)
	paginator := ses.NewListIdentitiesPaginator(lister, &ses.ListIdentitiesInput{})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			slog.Error("Support: failed to list SES identities", "err", err)
			return &supportapi.ListReceiverIdentitiesBadGateway{Error: "failed to list SES identities"}, nil
		}
		identities = append(identities, page.Identities...)
	}
	slog.Debug("Support: listed SES identities", "count", len(identities))
	sort.Strings(identities)

	config := h.receiverConfig()
	response := &supportapi.ReceiverIdentitiesResponse{
		Identities: make([]supportapi.ReceiverIdentity, 0, len(identities)),
	}
	for _, identity := range identities {
		response.Identities = append(response.Identities, supportapi.ReceiverIdentity{
			Identity: identity,
			HasRules: config.Has(identity),
		})
	}
	return response, nil
}

// loadReceiverConfig reads the authoritative rules from the config store.
// Without a store (tests and programmatic callers) the in-memory snapshot is
// used instead.
func (h *supportHandler) loadReceiverConfig(ctx context.Context) (receiver.MailIdentityConfig, error) {
	if h.configStore == nil {
		return h.receiverConfig().Clone(), nil
	}
	config, err := receiver.LoadMailIdentityConfig(ctx, h.configStore)
	if err != nil {
		return nil, err
	}
	h.publishReceiverConfig(config)
	return config, nil
}

// ListReceiverRules reads the configured rules from the store on every call,
// which is also how the GUI's Reload action discards unsaved edits.
func (h *supportHandler) ListReceiverRules(ctx context.Context) (supportapi.ListReceiverRulesRes, error) {
	if !isSystemManager(ctx) {
		return &supportapi.ListReceiverRulesForbidden{Error: forbiddenError().Error}, nil
	}

	config, err := h.loadReceiverConfig(ctx)
	if err != nil {
		slog.Error("Support: failed to load receiver rules", "err", err)
		return &supportapi.ListReceiverRulesInternalServerError{Error: "failed to load receiver rules"}, nil
	}
	slog.Debug("Support: listed receiver rules", "identities", len(config))
	return receiverRulesResponse(config), nil
}

// SaveReceiverRules adds or replaces the rules entries supplied by the GUI.
// Entries not in the request (notably the "_" fallback) are preserved.
func (h *supportHandler) SaveReceiverRules(ctx context.Context, req *supportapi.ReceiverRulesRequest) (supportapi.SaveReceiverRulesRes, error) {
	if !isSystemManager(ctx) {
		return &supportapi.SaveReceiverRulesForbidden{Error: forbiddenError().Error}, nil
	}
	if len(req.Rules) == 0 {
		return &supportapi.SaveReceiverRulesBadRequest{Error: "no rules supplied"}, nil
	}

	type rulesEntry struct {
		domain string
		rules  receiver.HandlerRules
	}
	entries := make([]rulesEntry, 0, len(req.Rules))
	for _, domainRules := range req.Rules {
		domain := strings.ToLower(strings.TrimSpace(domainRules.Domain))
		if err := receiver.ValidateConfigKey(domain); err != nil {
			return &supportapi.SaveReceiverRulesBadRequest{Error: "invalid receiving domain"}, nil
		}
		rules := make([]receiver.HandlerRule, 0, len(domainRules.Rules))
		for _, rule := range domainRules.Rules {
			rules = append(rules, receiver.HandlerRule(rule))
		}
		// The identity repeats its own key so the stored entry stays usable
		// when it is rendered as a flat list again.
		entry := receiver.HandlerRules{MailIdentity: domain, Rules: rules}
		if err := entry.Validate(); err != nil {
			return &supportapi.SaveReceiverRulesBadRequest{Error: "invalid handler rules"}, nil
		}
		// A DeliverTo target is only useful when it names a user the runtime
		// can resolve. Reject unknown targets at save time so the GUI cannot
		// leave a rule in place that would silently fall through on delivery.
		for _, rule := range rules {
			target, ok := rule.DeliverToTarget()
			if !ok {
				continue
			}
			if h.users == nil {
				slog.Error("Support: cannot validate DeliverTo rule without a user manager", "target", target)
				return &supportapi.SaveReceiverRulesInternalServerError{Error: "user manager is not configured"}, nil
			}
			user, err := h.users.Lookup(ctx, target)
			if err != nil || user == nil {
				slog.Info("Support: rejected DeliverTo rule for unknown user", "target", target, "err", err)
				return &supportapi.SaveReceiverRulesBadRequest{
					Error: fmt.Sprintf("delivery user %q is not a known user", target),
				}, nil
			}
		}
		entries = append(entries, rulesEntry{domain: domain, rules: entry})
	}

	// Start from the authoritative store, not the published snapshot, so a
	// save never drops rules written by another request or another process.
	current, err := h.loadReceiverConfig(ctx)
	if err != nil {
		slog.Error("Support: failed to load receiver rules for save", "err", err)
		return &supportapi.SaveReceiverRulesInternalServerError{Error: "failed to load receiver rules"}, nil
	}
	updated := current.Clone()
	if updated == nil {
		updated = receiver.BaseMailIdentityConfig()
	}
	for _, entry := range entries {
		updated[entry.domain] = entry.rules
	}
	if err := updated.Validate(); err != nil {
		return &supportapi.SaveReceiverRulesBadRequest{Error: "invalid receiver rules"}, nil
	}

	if h.configStore != nil {
		slog.Info("Support: persisting receiver rules", "entries", len(entries), "store", h.cfg.Receiver.Type)
		for _, entry := range entries {
			if err := h.configStore.Put(ctx, entry.domain, entry.rules); err != nil {
				slog.Error("Support: failed to save receiver rules", "identity", entry.domain, "err", err)
				return &supportapi.SaveReceiverRulesInternalServerError{Error: "failed to save receiver rules"}, nil
			}
		}
		saved, err := receiver.LoadMailIdentityConfig(ctx, h.configStore)
		if err != nil {
			slog.Error("Support: failed to reload receiver rules after save", "err", err)
			return &supportapi.SaveReceiverRulesInternalServerError{Error: "failed to save receiver rules"}, nil
		}
		updated = saved
	} else {
		slog.Warn("Support: receiver rules held in memory only; no config store is active")
	}

	h.publishReceiverConfig(updated)
	slog.Info("Support: receiver rules saved", "identities", len(updated))
	return receiverRulesResponse(updated), nil
}

// DeleteReceiverRules removes one receiving identity's rules entry. The "_"
// fallback is rejected because every unconfigured recipient relies on it; the
// config store enforces the same rule as a second line of defence.
func (h *supportHandler) DeleteReceiverRules(ctx context.Context, params supportapi.DeleteReceiverRulesParams) (supportapi.DeleteReceiverRulesRes, error) {
	if !isSystemManager(ctx) {
		return &supportapi.DeleteReceiverRulesForbidden{Error: forbiddenError().Error}, nil
	}

	identity := strings.ToLower(strings.TrimSpace(params.Identity))
	if identity == receiver.FallbackIdentity {
		return &supportapi.DeleteReceiverRulesBadRequest{Error: "the fallback receiving identity cannot be removed"}, nil
	}
	if err := receiver.ValidateConfigKey(identity); err != nil {
		return &supportapi.DeleteReceiverRulesBadRequest{Error: "invalid receiving identity"}, nil
	}

	config, err := h.loadReceiverConfig(ctx)
	if err != nil {
		slog.Error("Support: failed to load receiver rules for delete", "err", err)
		return &supportapi.DeleteReceiverRulesInternalServerError{Error: "failed to load receiver rules"}, nil
	}
	if !config.Has(identity) {
		return &supportapi.DeleteReceiverRulesNotFound{Error: "receiver rules not found"}, nil
	}

	updated := config.Clone()
	delete(updated, identity)
	if err := updated.Validate(); err != nil {
		return &supportapi.DeleteReceiverRulesBadRequest{Error: "invalid receiver rules"}, nil
	}

	if h.configStore != nil {
		if err := h.configStore.Delete(ctx, identity); err != nil {
			if errors.Is(err, receiver.ErrFallbackIdentity) {
				return &supportapi.DeleteReceiverRulesBadRequest{Error: "the fallback receiving identity cannot be removed"}, nil
			}
			slog.Error("Support: failed to delete receiver rules", "identity", identity, "err", err)
			return &supportapi.DeleteReceiverRulesInternalServerError{Error: "failed to delete receiver rules"}, nil
		}
		saved, err := receiver.LoadMailIdentityConfig(ctx, h.configStore)
		if err != nil {
			slog.Error("Support: failed to reload receiver rules after delete", "err", err)
			return &supportapi.DeleteReceiverRulesInternalServerError{Error: "failed to delete receiver rules"}, nil
		}
		updated = saved
	}

	h.publishReceiverConfig(updated)
	slog.Info("Support: receiver rules deleted", "identity", identity)
	return receiverRulesResponse(updated), nil
}

// receiverRulesResponse presents each receiving identity with its one ordered
// rules list. The fallback is listed first.
func receiverRulesResponse(config receiver.MailIdentityConfig) *supportapi.ReceiverRulesResponse {
	domains := make([]string, 0, len(config))
	for domain := range config {
		domains = append(domains, domain)
	}
	sort.Slice(domains, func(i, j int) bool {
		if domains[i] == receiver.FallbackIdentity {
			return true
		}
		if domains[j] == receiver.FallbackIdentity {
			return false
		}
		return domains[i] < domains[j]
	})

	response := &supportapi.ReceiverRulesResponse{
		Rules: make([]supportapi.ReceiverDomainRules, 0, len(domains)),
	}
	for _, domain := range domains {
		rules := make([]supportapi.HandlerRule, 0, len(config[domain].Rules))
		for _, rule := range config[domain].Rules {
			rules = append(rules, supportapi.HandlerRule(rule))
		}
		response.Rules = append(response.Rules, supportapi.ReceiverDomainRules{
			Domain: domain,
			Rules:  rules,
		})
	}
	return response
}
