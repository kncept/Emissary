package handler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ses"
	supportapi "github.com/kncept/Emissary/api/support/generated"
	"github.com/kncept/Emissary/mailer/receiver"
)

// SESIdentityLister is the SES operation used to list the receiving domains.
// A narrow interface keeps the support API testable without AWS credentials.
type SESIdentityLister interface {
	ListIdentities(ctx context.Context, params *ses.ListIdentitiesInput, optFns ...func(*ses.Options)) (*ses.ListIdentitiesOutput, error)
}

// identityLister lazily creates the SES client so installations that never
// open the receiver editor do not need SES credentials or permissions.
func (h *supportHandler) identityLister(ctx context.Context) (SESIdentityLister, error) {
	h.sesMu.Lock()
	defer h.sesMu.Unlock()
	if h.sesIdentities != nil {
		return h.sesIdentities, nil
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("load AWS config: %w", err)
	}
	client := ses.NewFromConfig(cfg)
	h.sesIdentities = client
	return client, nil
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
		slog.Info("Support: persisting receiver rules", "entries", len(entries), "store", h.cfg.ReceiverConfigType)
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
