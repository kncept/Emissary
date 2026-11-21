package handler

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-faster/errors"
	"github.com/ogen-go/ogen/ogenerrors"

	supportapi "github.com/kncept/Emissary/api/support/generated"
	"github.com/kncept/Emissary/mailer/auth"
	"github.com/kncept/Emissary/mailer/components/metadata"
	"github.com/kncept/Emissary/mailer/components/sender"
	"github.com/kncept/Emissary/mailer/components/session"
	"github.com/kncept/Emissary/mailer/components/storage"
	"github.com/kncept/Emissary/mailer/components/usermanager"
	"github.com/kncept/Emissary/mailer/config"
	configvendor "github.com/kncept/Emissary/mailer/config-vendor"
	"github.com/kncept/Emissary/mailer/envutil"
	"github.com/kncept/Emissary/mailer/receiver"
)

const (
	serverConfigurationName      = "server"
	storageConfigurationName     = "storage"
	senderConfigurationName      = "sender"
	receiverConfigurationName    = "receiver"
	metadataConfigurationName    = "metadata"
	sessionConfigurationName     = "session"
	userManagerConfigurationName = "user-manager"
)

// supportHandler implements the generated support API. All operations are
// restricted to System Managers; the generated security handler only validates
// the bearer token and stores the claims in the context.
type supportHandler struct {
	// cfg is the single source of truth for reported settings. Only
	// non-secret fields are read; secrets are reported as booleans.
	cfg *config.Config

	// Server settings effective for this handler, not raw config values.
	jwtSecretConfigured bool
	jwtTTL              time.Duration
	isLambda            bool

	storage  storage.Storage
	metadata metadata.MetadataStore
	session  session.SessionStore
	sender   sender.Sender
	users    usermanager.UserManager

	// stagedProcessor owns S3 reads, dedup and receiver construction for the
	// support API. It is nil when storage is not S3.
	stagedProcessor receiver.StagedMessageProcessor

	// identityConfig is an immutable snapshot shared with the staged processor.
	// Rule changes publish a new config instead of mutating the shared map, so
	// processing never races with a save.
	identityConfig *receiver.MutableMailIdentityConfigSource
	configStore    receiver.ConfigStore

	sesMu         sync.Mutex
	sesIdentities SESIdentityLister
}

func newSupportHandler(ctx context.Context, cfg *config.Config, b *configvendor.Bundle, opts Options) *supportHandler {
	if cfg == nil {
		cfg = &config.Config{}
	}
	if b == nil {
		b = &configvendor.Bundle{}
	}
	identityConfig := b.MailIdentityConfigSource
	if identityConfig == nil {
		identityConfig = receiver.NewMailIdentityConfigSource(nil)
		b.MailIdentityConfigSource = identityConfig
	}
	h := &supportHandler{
		cfg: cfg,

		jwtSecretConfigured: resolvedJWTSecret(cfg, opts) != "",
		jwtTTL:              resolvedJWTTTL(cfg, opts),
		isLambda:            opts.IsLambda,

		storage:  b.Storage,
		metadata: b.Metadata,
		session:  b.Session,
		sender:   b.Sender,
		users:    b.UserManager,

		stagedProcessor: b.StagedMessageProcessor,
		identityConfig:  identityConfig,
		configStore:     b.ReceiverConfigStore,
		sesIdentities:   opts.SESIdentityLister,
	}

	// The bundled store is authoritative. A caller may inject a snapshot for
	// tests, but an active store still wins so a fresh handler (including one
	// created on a Lambda cold start) sees saved rules.
	config := b.ReceiverConfig.Clone()
	if h.configStore != nil {
		stored, err := receiver.LoadMailIdentityConfig(ctx, h.configStore)
		if err != nil {
			slog.Error("Support: failed to load receiver rules on create", "err", err)
		} else {
			config = stored
		}
	}
	if config == nil {
		config = receiver.BaseMailIdentityConfig()
	}
	h.identityConfig.Store(config)
	return h
}

// receiverConfig returns the currently published rules snapshot.
func (h *supportHandler) receiverConfig() receiver.MailIdentityConfig {
	return h.identityConfig.MailIdentityConfig()
}

func (h *supportHandler) publishReceiverConfig(config receiver.MailIdentityConfig) {
	h.identityConfig.Store(config)
}

// supportSecurityHandler validates the bearer token and makes the claims
// available to the operation handlers. System Manager enforcement happens in
// each operation so an authenticated non-manager receives 403 rather than 401.
type supportSecurityHandler struct {
	authenticator *auth.Authenticator
}

func (h supportSecurityHandler) HandleBearerAuth(ctx context.Context, _ supportapi.OperationName, t supportapi.BearerAuth) (context.Context, error) {
	claims, err := auth.ClaimsFromBearerToken(h.authenticator, t.Token)
	if err != nil {
		return nil, err
	}
	return auth.ContextWithClaims(ctx, claims), nil
}

// supportErrorHandler keeps support responses consistent with the rest of the
// API ({"error": "..."}) instead of ogen's default error_message envelope.
func supportErrorHandler(_ context.Context, w http.ResponseWriter, r *http.Request, err error) {
	var securityErr *ogenerrors.SecurityError
	if errors.As(err, &securityErr) {
		w.Header().Set("WWW-Authenticate", `Bearer realm="emissary"`)
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}

	code := ogenerrors.ErrorCode(err)
	switch {
	case code == http.StatusBadRequest:
		writeError(w, code, "invalid request")
	case code == http.StatusUnsupportedMediaType:
		writeError(w, code, "unsupported media type")
	case code >= 500:
		slog.Error("Support API request failed", "method", r.Method, "path", r.URL.Path, "err", err)
		writeError(w, code, "internal server error")
	default:
		writeError(w, code, "request failed")
	}
}

func isSystemManager(ctx context.Context) bool {
	claims := auth.ClaimsFromContext(ctx)
	return claims != nil && claims.SystemManager
}

func forbiddenError() *supportapi.Error {
	return &supportapi.Error{Error: "insufficient permissions"}
}

func (h *supportHandler) ListConfigurations(ctx context.Context) (supportapi.ListConfigurationsRes, error) {
	if !isSystemManager(ctx) {
		return &supportapi.ListConfigurationsForbidden{Error: forbiddenError().Error}, nil
	}

	sections := h.configurationSummaries()
	slog.Debug("Support: listed configuration sections", "sections", len(sections))
	response := &supportapi.ConfigurationsResponse{
		Configurations: make([]supportapi.ConfigurationSummary, 0, len(sections)),
	}
	for _, section := range sections {
		response.Configurations = append(response.Configurations, supportapi.ConfigurationSummary{
			Name:           section.name,
			Implementation: section.implementation,
			Description:    optionalString(section.description),
		})
	}
	return response, nil
}

func (h *supportHandler) GetConfiguration(ctx context.Context, params supportapi.GetConfigurationParams) (supportapi.GetConfigurationRes, error) {
	if !isSystemManager(ctx) {
		return &supportapi.GetConfigurationForbidden{Error: forbiddenError().Error}, nil
	}

	name := canonicalConfigurationName(params.Name)
	section, ok := h.configurationDetail(name)
	if !ok {
		return &supportapi.GetConfigurationNotFound{Error: "configuration section not found"}, nil
	}
	slog.Debug("Support: read configuration section", "section", name)
	return section, nil
}

func (h *supportHandler) ListBucketRoot(ctx context.Context) (supportapi.ListBucketRootRes, error) {
	if !isSystemManager(ctx) {
		return &supportapi.ListBucketRootForbidden{Error: forbiddenError().Error}, nil
	}

	store, ok := h.storage.(*storage.S3Storage)
	if !ok {
		return &supportapi.ListBucketRootNotImplemented{Error: "bucket operations require s3 storage"}, nil
	}

	objects, prefixes, err := store.ListRoot(ctx)
	if err != nil {
		slog.Error("Support: failed to list bucket root", "bucket", store.BucketName(), "err", err)
		return &supportapi.ListBucketRootBadGateway{Error: "failed to list bucket root"}, nil
	}
	slog.Debug("Support: listed bucket root", "bucket", store.BucketName(), "objects", len(objects), "prefixes", len(prefixes))

	response := &supportapi.BucketRootResponse{
		Bucket:   store.BucketName(),
		Objects:  make([]supportapi.BucketObject, 0, len(objects)),
		Prefixes: prefixes,
	}
	for _, object := range objects {
		item := supportapi.BucketObject{
			Key:          object.Key,
			Size:         object.Size,
			LastModified: object.LastModified,
		}
		if object.ETag != "" {
			item.Etag = supportapi.NewOptString(object.ETag)
		}
		response.Objects = append(response.Objects, item)
	}
	return response, nil
}

func (h *supportHandler) ProcessEmail(ctx context.Context, req *supportapi.ProcessEmailRequest) (supportapi.ProcessEmailRes, error) {
	if !isSystemManager(ctx) {
		return &supportapi.ProcessEmailForbidden{Error: forbiddenError().Error}, nil
	}

	id := strings.TrimSpace(req.ID)
	if id == "" {
		return &supportapi.ProcessEmailBadRequest{Error: "id is required"}, nil
	}

	store, ok := h.storage.(*storage.S3Storage)
	if !ok || h.stagedProcessor == nil {
		return &supportapi.ProcessEmailNotImplemented{Error: "email processing requires s3 storage"}, nil
	}

	exists, err := store.ObjectExists(ctx, id)
	if err != nil {
		slog.Error("Support: failed to check staging object", "key", id, "err", err)
		return &supportapi.ProcessEmailBadGateway{Error: "failed to process email"}, nil
	}
	if !exists {
		return &supportapi.ProcessEmailNotFound{Error: "staging object not found"}, nil
	}

	// The injected service owns S3 reads, dedup and receiver construction. The
	// handler only supplies the configured bucket and the object key.
	if err := h.stagedProcessor.ProcessRawKey(ctx, h.cfg.Storage.S3Bucket, id); err != nil {
		slog.Error("Support: failed to process staging object", "key", id, "err", err)
		return &supportapi.ProcessEmailBadGateway{Error: "failed to process email"}, nil
	}
	slog.Info("Support: processed staging object", "key", id)

	return &supportapi.ProcessEmailResponse{
		Success: true,
		ID:      id,
		Message: supportapi.NewOptString("message processed"),
	}, nil
}

type configurationSummary struct {
	name           string
	implementation string
	description    string
}

func (h *supportHandler) configurationSummaries() []configurationSummary {
	serverImpl := "http"
	if h.isLambda {
		serverImpl = "http (lambda)"
	}
	return []configurationSummary{
		{
			name:           serverConfigurationName,
			implementation: serverImpl,
			description:    "HTTP server and authentication",
		},
		{
			name:           storageConfigurationName,
			implementation: storageImplementation(h.cfg.Storage.Type, h.storage),
			description:    "Email storage backend",
		},
		{
			name:           senderConfigurationName,
			implementation: senderImplementation(h.cfg.Sender.Type, h.sender),
			description:    "Outbound email sender",
		},
		{
			name:           receiverConfigurationName,
			implementation: receiverImplementation(h.cfg.Receiver.Type),
			description:    "Inbound recipient routing rules",
		},
		{
			name:           metadataConfigurationName,
			implementation: metadataImplementation(h.cfg.Metadata.Type, h.metadata),
			description:    "Email metadata store",
		},
		{
			name:           sessionConfigurationName,
			implementation: sessionImplementation(h.cfg.Session.Type, h.session),
			description:    "Refresh-token session store",
		},
		{
			name:           userManagerConfigurationName,
			implementation: userManagerImplementation(h.cfg.UserManager.Type, h.users),
			description:    "User authentication backend",
		},
	}
}

func (h *supportHandler) configurationDetail(name string) (*supportapi.ConfigurationDetail, bool) {
	for _, summary := range h.configurationSummaries() {
		if summary.name != name {
			continue
		}

		settings := h.configurationSettings(name)
		return &supportapi.ConfigurationDetail{
			Name:           summary.name,
			Implementation: summary.implementation,
			Description:    optionalString(summary.description),
			Settings:       supportapi.ConfigurationDetailSettings(settings),
		}, true
	}
	return nil, false
}

func (h *supportHandler) configurationSettings(name string) map[string]string {
	settings := make(map[string]string)
	cfg := h.cfg

	switch name {
	case serverConfigurationName:
		addSetting(settings, "listenAddr", cfg.Server.ListenAddr)
		addSetting(settings, "adminUsername", cfg.Server.Username)
		settings["adminPasswordConfigured"] = boolSetting(cfg.Server.Password != "")
		settings["jwtSecretConfigured"] = boolSetting(h.jwtSecretConfigured)
		if h.jwtTTL > 0 {
			addSetting(settings, "jwtTTL", h.jwtTTL.String())
		}
		addSetting(settings, "jwtAudience", cfg.Server.JWTAudience)
		addSetting(settings, "jwtIssuer", cfg.Server.JWTIssuer)
		addSetting(settings, "commitHash", envutil.CommitHash)
		addSetting(settings, "buildTime", envutil.BuildTime)
		addSetting(settings, "lambda", boolSetting(h.isLambda))

	case storageConfigurationName:
		addSetting(settings, "type", storageImplementation(cfg.Storage.Type, h.storage))
		addSetting(settings, "bucket", cfg.Storage.S3Bucket)
		if s3store, ok := h.storage.(*storage.S3Storage); ok {
			addSetting(settings, "bucket", s3store.BucketName())
		}
		if fileStore, ok := h.storage.(*storage.FileStorage); ok {
			addSetting(settings, "dir", fileStore.RootDir())
		}

	case senderConfigurationName:
		addSetting(settings, "type", senderImplementation(cfg.Sender.Type, h.sender))

	case receiverConfigurationName:
		implementation := receiverImplementation(cfg.Receiver.Type)
		addSetting(settings, "type", implementation)
		// Only report the settings relevant to the active backend. These are
		// the same resolved values config.Load and FromConfig hand to the
		// bundled store, so the reported setting matches the active store.
		switch implementation {
		case "s3":
			addSetting(settings, "bucket", cfg.Receiver.S3Bucket)
		case "filesystem":
			addSetting(settings, "dir", filepath.Join(cfg.Receiver.FilesystemDir, "receiver"))
		case "ddb":
			addSetting(settings, "table", cfg.ReceiverTableName())
			addSetting(settings, "region", cfg.Receiver.Region)
		}
		identityConfig := h.receiverConfig()
		settings["identityCount"] = strconv.Itoa(len(identityConfig))
		// The rules themselves are safe for a System Manager to read. Show
		// each identity as a JSON array of rule names so the loaded map is
		// auditable, including the "_" base fallback. Rules are evaluated in
		// order, which is the order they are stored in.
		for identity, rules := range identityConfig {
			encoded, err := json.Marshal(rules.Rules)
			if err != nil {
				slog.Warn("Support: failed to encode receiver rules", "identity", identity, "err", err)
				continue
			}
			settings["rules."+identity] = string(encoded)
		}

	case metadataConfigurationName:
		addSetting(settings, "type", metadataImplementation(cfg.Metadata.Type, h.metadata))
		addSetting(settings, "path", cfg.Metadata.FilePath)
		addSetting(settings, "table", cfg.MetadataTableName())
		addSetting(settings, "region", cfg.Metadata.Region)

	case sessionConfigurationName:
		addSetting(settings, "type", sessionImplementation(cfg.Session.Type, h.session))
		addSetting(settings, "table", cfg.SessionTableName())
		addSetting(settings, "region", cfg.Session.Region)

	case userManagerConfigurationName:
		addSetting(settings, "type", userManagerImplementation(cfg.UserManager.Type, h.users))
		addSetting(settings, "domain", cfg.UserManager.Domain)
		if fileUsers, ok := h.users.(*usermanager.FileManager); ok {
			addSetting(settings, "dir", fileUsers.RootDir())
		}
		addSetting(settings, "table", cfg.UserTableName())
		addSetting(settings, "region", cfg.UserManager.Region)
		addSetting(settings, "ldapURL", cfg.UserManager.LDAPURL)
		addSetting(settings, "ldapBindDN", cfg.UserManager.LDAPBindDN)
		addSetting(settings, "ldapBaseDN", cfg.UserManager.LDAPBaseDN)
		addSetting(settings, "ldapUserFilter", cfg.UserManager.LDAPUserFilter)
		addSetting(settings, "ldapCacheTTL", cfg.UserManager.LDAPCacheTTL)
		settings["ldapBindPasswordConfigured"] = boolSetting(cfg.UserManager.LDAPBindPassword != "")
	}

	return settings
}

func canonicalConfigurationName(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	name = strings.ReplaceAll(name, "_", "-")
	switch name {
	case "user", "users", "user-manager", "usermanager":
		return userManagerConfigurationName
	case "receiver", "receiver-config", "receiver-configuration", "routing":
		return receiverConfigurationName
	case "server", "storage", "sender", "metadata", "session":
		return name
	default:
		return name
	}
}

func storageImplementation(configured string, store storage.Storage) string {
	switch store.(type) {
	case *storage.MemoryStorage:
		return "memory"
	case *storage.S3Storage:
		return "s3"
	case *storage.FileStorage:
		return "filesystem"
	}
	switch strings.ToLower(strings.TrimSpace(configured)) {
	case "":
		if store != nil {
			return strings.ToLower(store.Name())
		}
		return "unconfigured"
	default:
		return strings.ToLower(strings.TrimSpace(configured))
	}
}

func receiverImplementation(configured string) string {
	switch strings.ToLower(strings.TrimSpace(configured)) {
	case "":
		return "unconfigured"
	case "memory":
		return "memory"
	case "file", "filesystem":
		return "filesystem"
	case "ddb", "dynamodb":
		return "ddb"
	default:
		return strings.ToLower(strings.TrimSpace(configured))
	}
}

func senderImplementation(configured string, mailSender sender.Sender) string {
	switch strings.ToLower(strings.TrimSpace(configured)) {
	case "aws-ses", "ses":
		return "aws-ses"
	case "mta":
		return "mta"
	case "log":
		return "log"
	}
	if mailSender != nil {
		switch mailSender.Name() {
		case "AwsSesSender":
			return "aws-ses"
		case "MtaSender":
			return "mta"
		case "LogSender":
			return "log"
		default:
			return strings.ToLower(mailSender.Name())
		}
	}
	return "unconfigured"
}

func metadataImplementation(configured string, store metadata.MetadataStore) string {
	if store != nil {
		return store.Name()
	}
	if configured != "" {
		return strings.ToLower(strings.TrimSpace(configured))
	}
	return "unconfigured"
}

func sessionImplementation(configured string, store session.SessionStore) string {
	if store != nil {
		return store.Name()
	}
	if configured != "" {
		return strings.ToLower(strings.TrimSpace(configured))
	}
	return "unconfigured"
}

func userManagerImplementation(configured string, users usermanager.UserManager) string {
	if users != nil {
		return users.Name()
	}
	if configured != "" {
		return strings.ToLower(strings.TrimSpace(configured))
	}
	return "unconfigured"
}

func addSetting(settings map[string]string, key, value string) {
	if value != "" {
		settings[key] = value
	}
}

func boolSetting(value bool) string {
	if value {
		return "true"
	}
	return "false"
}

func optionalString(value string) supportapi.OptString {
	if value == "" {
		return supportapi.OptString{}
	}
	return supportapi.NewOptString(value)
}
