package handler

import (
	"context"
	"log"
	"net/http"
	"strings"

	"github.com/go-faster/errors"
	"github.com/ogen-go/ogen/ogenerrors"

	supportapi "github.com/kncept/Emissary/api/support/generated"
	"github.com/kncept/Emissary/mailer/auth"
	"github.com/kncept/Emissary/mailer/envutil"
	"github.com/kncept/Emissary/mailer/metadata"
	"github.com/kncept/Emissary/mailer/receiver"
	"github.com/kncept/Emissary/mailer/sender"
	"github.com/kncept/Emissary/mailer/session"
	"github.com/kncept/Emissary/mailer/storage"
	"github.com/kncept/Emissary/mailer/usermanager"
)

const (
	serverConfigurationName      = "server"
	storageConfigurationName     = "storage"
	senderConfigurationName      = "sender"
	metadataConfigurationName    = "metadata"
	sessionConfigurationName     = "session"
	userManagerConfigurationName = "user-manager"
	awsConfigurationName         = "aws"
)

// supportHandler implements the generated support API. All operations are
// restricted to System Managers; the generated security handler only validates
// the bearer token and stores the claims in the context.
type supportHandler struct {
	cfg      HandlerConfig
	storage  storage.Storage
	metadata metadata.MetadataStore
	session  session.SessionStore
	sender   sender.Sender
	users    usermanager.UserManager
}

func newSupportHandler(cfg HandlerConfig, emailStore storage.Storage, metaStore metadata.MetadataStore, sessionStore session.SessionStore, mailSender sender.Sender, users usermanager.UserManager) *supportHandler {
	return &supportHandler{
		cfg:      cfg,
		storage:  emailStore,
		metadata: metaStore,
		session:  sessionStore,
		sender:   mailSender,
		users:    users,
	}
}

// supportSecurityHandler validates the bearer token and makes the claims
// available to the operation handlers. System Manager enforcement happens in
// each operation so an authenticated non-manager receives 403 rather than 401.
type supportSecurityHandler struct {
	authenticator *auth.Authenticator
}

func (h supportSecurityHandler) HandleBearerAuth(ctx context.Context, _ supportapi.OperationName, t supportapi.BearerAuth) (context.Context, error) {
	claims, err := h.authenticator.ValidateClaims(t.Token)
	if err != nil {
		return nil, err
	}
	return context.WithValue(ctx, claimsContextKey{}, claims), nil
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
		log.Printf("support API %s %s: %v", r.Method, r.URL.Path, err)
		writeError(w, code, "internal server error")
	default:
		writeError(w, code, "request failed")
	}
}

func isSystemManager(ctx context.Context) bool {
	claims := currentClaimsFromContext(ctx)
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
		log.Printf("support: list bucket root: %v", err)
		return &supportapi.ListBucketRootBadGateway{Error: "failed to list bucket root"}, nil
	}

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
	if !ok {
		return &supportapi.ProcessEmailNotImplemented{Error: "email processing requires s3 storage"}, nil
	}

	exists, err := store.ObjectExists(ctx, id)
	if err != nil {
		log.Printf("support: check staging object %q: %v", id, err)
		return &supportapi.ProcessEmailBadGateway{Error: "failed to process email"}, nil
	}
	if !exists {
		return &supportapi.ProcessEmailNotFound{Error: "staging object not found"}, nil
	}

	rcvr, err := receiver.NewReceiver(receiver.ReceiverConfig{
		Bucket:      store.BucketName(),
		UserManager: h.users,
		Sender:      h.sender,
		Domain:      h.cfg.UserDomain,
		SESClient:   store.Client(),
	})
	if err != nil {
		log.Printf("support: create receiver: %v", err)
		return &supportapi.ProcessEmailBadGateway{Error: "failed to process email"}, nil
	}

	if err := rcvr.ProcessMessage(ctx, id); err != nil {
		log.Printf("support: process staging object %q: %v", id, err)
		return &supportapi.ProcessEmailBadGateway{Error: "failed to process email"}, nil
	}

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
	return []configurationSummary{
		{
			name:           serverConfigurationName,
			implementation: "http",
			description:    "HTTP server and authentication",
		},
		{
			name:           storageConfigurationName,
			implementation: storageImplementation(h.cfg.StorageType, h.storage),
			description:    "Email storage backend",
		},
		{
			name:           senderConfigurationName,
			implementation: senderImplementation(h.cfg.SenderType, h.sender),
			description:    "Outbound email sender",
		},
		{
			name:           metadataConfigurationName,
			implementation: metadataImplementation(h.cfg.MetadataType, h.metadata),
			description:    "Email metadata store",
		},
		{
			name:           sessionConfigurationName,
			implementation: sessionImplementation(h.cfg.SessionType, h.session),
			description:    "Refresh-token session store",
		},
		{
			name:           userManagerConfigurationName,
			implementation: userManagerImplementation(h.cfg.UserManagerType, h.users),
			description:    "User authentication backend",
		},
		{
			name:           awsConfigurationName,
			implementation: "aws-sdk",
			description:    "AWS SDK region and DynamoDB table naming",
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

	switch name {
	case serverConfigurationName:
		addSetting(settings, "listenAddr", h.cfg.ListenAddr)
		addSetting(settings, "adminUsername", h.cfg.AuthUser)
		settings["adminPasswordConfigured"] = boolSetting(h.cfg.AuthPass != "")
		settings["jwtSecretConfigured"] = boolSetting(h.cfg.JWTSecret != "")
		if h.cfg.JWTSecretTTL > 0 {
			addSetting(settings, "jwtTTL", h.cfg.JWTSecretTTL.String())
		}
		addSetting(settings, "jwtAudience", h.cfg.JWTAudience)
		addSetting(settings, "jwtIssuer", h.cfg.JWTIssuer)
		addSetting(settings, "commitHash", envutil.CommitHash)
		addSetting(settings, "buildTime", envutil.BuildTime)

	case storageConfigurationName:
		addSetting(settings, "type", storageImplementation(h.cfg.StorageType, h.storage))
		addSetting(settings, "bucket", h.cfg.S3Bucket)
		if s3store, ok := h.storage.(*storage.S3Storage); ok {
			addSetting(settings, "bucket", s3store.BucketName())
		}

	case senderConfigurationName:
		addSetting(settings, "type", senderImplementation(h.cfg.SenderType, h.sender))
		addSetting(settings, "from", h.cfg.SenderFrom)

	case metadataConfigurationName:
		addSetting(settings, "type", metadataImplementation(h.cfg.MetadataType, h.metadata))
		addSetting(settings, "path", h.cfg.MetadataPath)
		addSetting(settings, "table", resolveDDBTable(h.cfg.MetadataTable, h.cfg.DDBTablePrefix, "metadata"))
		addSetting(settings, "region", h.cfg.MetadataRegion)

	case sessionConfigurationName:
		addSetting(settings, "type", sessionImplementation(h.cfg.SessionType, h.session))
		addSetting(settings, "table", resolveDDBTable(h.cfg.SessionTable, h.cfg.DDBTablePrefix, "sessions"))
		addSetting(settings, "region", h.cfg.SessionRegion)

	case userManagerConfigurationName:
		addSetting(settings, "type", userManagerImplementation(h.cfg.UserManagerType, h.users))
		addSetting(settings, "domain", h.cfg.UserDomain)
		addSetting(settings, "table", resolveDDBTable(h.cfg.UserTable, h.cfg.DDBTablePrefix, "users"))
		addSetting(settings, "region", h.cfg.UserRegion)
		addSetting(settings, "ldapURL", h.cfg.LDAPURL)
		addSetting(settings, "ldapBindDN", h.cfg.LDAPBindDN)
		addSetting(settings, "ldapBaseDN", h.cfg.LDAPBaseDN)
		addSetting(settings, "ldapUserFilter", h.cfg.LDAPUserFilter)
		addSetting(settings, "ldapCacheTTL", h.cfg.LDAPCacheTTL)
		settings["ldapBindPasswordConfigured"] = boolSetting(h.cfg.LDAPBindPassword != "")

	case awsConfigurationName:
		addSetting(settings, "region", h.cfg.AWSRegion)
		addSetting(settings, "tablePrefix", h.cfg.DDBTablePrefix)
	}

	return settings
}

func canonicalConfigurationName(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	name = strings.ReplaceAll(name, "_", "-")
	switch name {
	case "user", "users", "user-manager", "usermanager":
		return userManagerConfigurationName
	case "server", "storage", "sender", "metadata", "session", "aws":
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
	}
	switch strings.ToLower(strings.TrimSpace(configured)) {
	case "":
		if store != nil {
			return strings.ToLower(store.Name())
		}
		return "s3"
	default:
		return strings.ToLower(strings.TrimSpace(configured))
	}
}

func senderImplementation(configured string, mailSender sender.Sender) string {
	switch strings.ToLower(strings.TrimSpace(configured)) {
	case "aws-ses", "ses":
		return "aws-ses"
	case "log":
		return "log"
	}
	if mailSender != nil {
		switch mailSender.Name() {
		case "AwsSesSender":
			return "aws-ses"
		case "LogSender":
			return "log"
		default:
			return strings.ToLower(mailSender.Name())
		}
	}
	return "log"
}

func metadataImplementation(configured string, store metadata.MetadataStore) string {
	if store != nil {
		return store.Name()
	}
	if configured != "" {
		return strings.ToLower(strings.TrimSpace(configured))
	}
	return "memory"
}

func sessionImplementation(configured string, store session.SessionStore) string {
	if store != nil {
		return store.Name()
	}
	if configured != "" {
		return strings.ToLower(strings.TrimSpace(configured))
	}
	return "memory"
}

func userManagerImplementation(configured string, users usermanager.UserManager) string {
	if users != nil {
		return users.Name()
	}
	if configured != "" {
		return strings.ToLower(strings.TrimSpace(configured))
	}
	return "none"
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
