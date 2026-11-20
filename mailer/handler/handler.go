package handler

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	supportapi "github.com/kncept/Emissary/api/support/generated"
	usersapi "github.com/kncept/Emissary/api/users/generated"
	"github.com/kncept/Emissary/mailer/auth"
	configvendor "github.com/kncept/Emissary/mailer/config-vendor"
	"github.com/kncept/Emissary/mailer/email"
	"github.com/kncept/Emissary/mailer/gui"
	"github.com/kncept/Emissary/mailer/identity"
	"github.com/kncept/Emissary/mailer/jmap"
	"github.com/kncept/Emissary/mailer/metadata"
	"github.com/kncept/Emissary/mailer/receiver"
	"github.com/kncept/Emissary/mailer/sender"
	"github.com/kncept/Emissary/mailer/session"
	"github.com/kncept/Emissary/mailer/storage"
	"github.com/kncept/Emissary/mailer/usermanager"
)

const (
	// maxRequestBody is the tighter cap used by the authentication endpoints,
	// which only ever carry a username/password or a refresh token.
	maxRequestBody = 1 << 20

	// defaultMaxFileSize mirrors config.DefaultMaxFileSize for callers that
	// build a HandlerConfig directly (tests and programmatic users).
	defaultMaxFileSize = 10 << 20

	// jmapSessionState is stable because the session object itself is static:
	// every mutable value is reported per account by Foo/get's state token.
	jmapSessionState = "session-0"
)

type HandlerConfig struct {
	AuthUser         string
	AuthPass         string
	ListenAddr       string
	JWTSecret        string
	JWTSecretTTL     time.Duration
	S3Bucket         string
	FilesystemDir    string
	StorageType      string
	AWSRegion        string
	MetadataType     string
	MetadataPath     string
	MetadataTable    string
	MetadataRegion   string
	SessionType      string
	SessionTable     string
	SessionRegion    string
	DDBTablePrefix   string
	Storage          storage.Storage
	Metadata         metadata.MetadataStore
	Session          session.SessionStore
	JWTAudience      string
	JWTIssuer        string
	UserManager      usermanager.UserManager
	UserManagerType  string
	UserDomain       string
	UserTable        string
	UserRegion       string
	LDAPURL          string
	LDAPBindDN       string
	LDAPBindPassword string
	LDAPBaseDN       string
	LDAPUserFilter   string
	LDAPCacheTTL     string
	Sender           sender.Sender
	SenderType       string
	SenderMTAPort    string

	ReceiverConfigType    string // "s3", "filesystem", "memory" or "ddb"
	ReceiverBucket        string // S3_BUCKET for the "s3" receiver config backend
	ReceiverFilesystemDir string // FILESYSTEM_DIR root for the "filesystem" receiver config backend
	ReceiverTable         string // DynamoDB table name for the "ddb" backend
	ReceiverRegion        string // AWS region for the "ddb" backend
	ReceiverConfig        receiver.MailIdentityConfig
	// ReceiverConfigStore is the active store behind ReceiverConfig. It is
	// retained so the support API can add rules at runtime; a supplied
	// ReceiverConfig (tests or programmatic callers) leaves it nil.
	ReceiverConfigStore receiver.ConfigStore
	// SESIdentityLister supplies the identities shown by the receiver rules
	// editor. Nil means create an SES client lazily on first use.
	SESIdentityLister SESIdentityLister

	IsLambda bool

	// LoginRateLimit caps failed login attempts per window for both the
	// client IP and the account. Zero selects the default (10 per minute).
	LoginRateLimit       int
	LoginRateLimitWindow time.Duration

	// MaxFileSize caps JMAP request bodies and MIME messages in bytes. Zero
	// selects the 10 MiB default. It is advertised as maxSizeRequest and
	// maxSizeUpload in the JMAP session object.
	MaxFileSize int64
}

func (cfg HandlerConfig) maxFileSize() int64 {
	if cfg.MaxFileSize > 0 {
		return cfg.MaxFileSize
	}
	return defaultMaxFileSize
}

func applyDDBTablePrefix(prefix, name string) string {
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		prefix = "emissary"
	}
	if strings.HasSuffix(prefix, "-") || strings.HasSuffix(prefix, "_") {
		return prefix + name
	}
	return prefix + "-" + name
}

func resolveDDBTable(explicit, prefix, logical string) string {
	if strings.TrimSpace(explicit) != "" {
		return strings.TrimSpace(explicit)
	}
	return applyDDBTablePrefix(prefix, logical)
}

// filesystemRoot mirrors config.FILESYSTEM_DIR's default for callers that
// build a HandlerConfig directly (tests and programmatic users).
func filesystemRoot(dir string) string {
	if strings.TrimSpace(dir) != "" {
		return dir
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".emissary")
	}
	return ".emissary"
}

// receiverBucket returns the receiver-specific S3 target, falling back to the
// shared mail bucket for programmatic callers that do not set it.
func receiverBucket(cfg HandlerConfig) string {
	if strings.TrimSpace(cfg.ReceiverBucket) != "" {
		return cfg.ReceiverBucket
	}
	return cfg.S3Bucket
}

// receiverFilesystemRoot returns the receiver-specific filesystem root,
// falling back to the shared root for programmatic callers.
func receiverFilesystemRoot(cfg HandlerConfig) string {
	if strings.TrimSpace(cfg.ReceiverFilesystemDir) != "" {
		return cfg.ReceiverFilesystemDir
	}
	return filesystemRoot(cfg.FilesystemDir)
}

// senderUsesSES reports whether outbound mail goes through SES, in which case
// From addresses must be checked against the account's verified identities.
func senderUsesSES(cfg HandlerConfig, mailSender sender.Sender) bool {
	switch strings.ToLower(strings.TrimSpace(cfg.SenderType)) {
	case "aws-ses", "ses":
		return true
	}
	return mailSender != nil && mailSender.Name() == "AwsSesSender"
}

func New(cfg HandlerConfig) (http.Handler, error) {
	var jwtSecret string
	if cfg.JWTSecret != "" {
		jwtSecret = cfg.JWTSecret
	} else {
		secret := make([]byte, 32)
		if _, err := rand.Read(secret); err != nil {
			return nil, err
		}
		jwtSecret = base64.RawURLEncoding.EncodeToString(secret)
		slog.Warn("JWT_SECRET not set; using an ephemeral secret. Tokens are invalidated on restart")
	}

	jwtTTL := cfg.JWTSecretTTL
	if jwtTTL == 0 {
		jwtTTL = 24 * time.Hour
	}

	// Missing components are vended from the matching selector in cfg. Tests
	// and programmatic callers can still supply a component directly.
	var err error
	users := cfg.UserManager
	if users == nil {
		users, err = configvendor.UserManager(configvendor.UserManagerOptions{
			Type:             cfg.UserManagerType,
			AdminUsername:    cfg.AuthUser,
			AdminPassword:    cfg.AuthPass,
			Domain:           cfg.UserDomain,
			DdbTable:         resolveDDBTable(cfg.UserTable, cfg.DDBTablePrefix, "users"),
			Region:           cfg.UserRegion,
			S3Bucket:         cfg.S3Bucket,
			FilesystemDir:    filepath.Join(filesystemRoot(cfg.FilesystemDir), "users"),
			LDAPURL:          cfg.LDAPURL,
			LDAPBindDN:       cfg.LDAPBindDN,
			LDAPBindPassword: cfg.LDAPBindPassword,
			LDAPBaseDN:       cfg.LDAPBaseDN,
			LDAPUserFilter:   cfg.LDAPUserFilter,
			LDAPCacheTTL:     cfg.LDAPCacheTTL,
		})
		if err != nil {
			return nil, err
		}
	}

	sessionStore := cfg.Session
	if sessionStore == nil {
		sessionStore, err = configvendor.Session(configvendor.SessionOptions{
			Type:      cfg.SessionType,
			TableName: resolveDDBTable(cfg.SessionTable, cfg.DDBTablePrefix, "sessions"),
			Region:    cfg.SessionRegion,
		})
		if err != nil {
			return nil, err
		}
	}

	authenticator := auth.NewWithUserManager(users, sessionStore, jwtSecret, jwtTTL, cfg.JWTAudience, cfg.JWTIssuer, cfg.AuthUser)

	emailStore := cfg.Storage
	if emailStore == nil {
		emailStore, err = configvendor.Storage(configvendor.StorageOptions{
			Type:          cfg.StorageType,
			S3Bucket:      cfg.S3Bucket,
			FilesystemDir: filepath.Join(filesystemRoot(cfg.FilesystemDir), "mail"),
		})
		if err != nil {
			return nil, err
		}
	}

	metaStore := cfg.Metadata
	if metaStore == nil {
		metaStore, err = configvendor.Metadata(configvendor.MetadataOptions{
			Type:      cfg.MetadataType,
			FilePath:  cfg.MetadataPath,
			TableName: resolveDDBTable(cfg.MetadataTable, cfg.DDBTablePrefix, "metadata"),
			Region:    cfg.MetadataRegion,
		})
		if err != nil {
			return nil, err
		}
	}

	mailSender := cfg.Sender
	if mailSender == nil {
		mailSender, err = configvendor.Sender(context.Background(), configvendor.SenderOptions{
			Type:    cfg.SenderType,
			MTAPort: cfg.SenderMTAPort,
		})
		if err != nil {
			return nil, err
		}
	}

	// Inbound routing rules are loaded once at startup from the configured
	// backend. A supplied config (tests or programmatic callers) wins.
	identityConfig := cfg.ReceiverConfig
	if identityConfig == nil {
		configStore := cfg.ReceiverConfigStore
		if configStore == nil {
			configStore, err = configvendor.ReceiverConfigStore(configvendor.ReceiverConfigStoreOptions{
				Type:          cfg.ReceiverConfigType,
				FilesystemDir: receiverFilesystemRoot(cfg),
				Bucket:        receiverBucket(cfg),
				TableName:     resolveDDBTable(cfg.ReceiverTable, cfg.DDBTablePrefix, "receiver"),
				Region:        cfg.ReceiverRegion,
			})
			if err != nil {
				return nil, err
			}
		}
		identityConfig, err = receiver.LoadMailIdentityConfig(context.Background(), configStore)
		if err != nil {
			return nil, err
		}
		cfg.ReceiverConfigStore = configStore
	}
	slog.Debug("Receiver rules loaded", "identities", len(identityConfig))
	cfg.ReceiverConfig = identityConfig

	return newHandler(authenticator, emailStore, metaStore, sessionStore, mailSender, users, cfg), nil
}

func newHandler(authenticator *auth.Authenticator, emailStore storage.Storage, metaStore metadata.MetadataStore, sessionStore session.SessionStore, mailSender sender.Sender, userManager usermanager.UserManager, cfg HandlerConfig) http.Handler {
	mux := http.NewServeMux()
	maxFileSize := cfg.maxFileSize()
	loginLimiter := newLoginLimiter(cfg.LoginRateLimit, cfg.LoginRateLimitWindow)

	// Outbound From addresses must belong to a verified email identity. SES is
	// the current identity source; local/test callers that do not use SES can
	// omit the validator and keep the existing behaviour.
	var fromValidator jmap.FromValidator
	if senderUsesSES(cfg, mailSender) || cfg.SESIdentityLister != nil {
		lister := cfg.SESIdentityLister
		if lister == nil {
			lister = &lazySESIdentityLister{}
		}
		fromValidator = identity.NewValidator(lister)
	}
	jmapHandler := jmap.NewHandler(emailStore, metaStore, userManager, mailSender, fromValidator)
	dispatcher := jmap.NewDispatcher(jmapHandler, emailStore)

	mux.HandleFunc("POST /api/auth/login", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Username string `json:"username"`
			Password string `json:"password"`
		}
		if err := decodeJSON(w, r, &req); err != nil {
			logRequestWarn(r, "decode login request", err)
			writeError(w, http.StatusBadRequest, "invalid request")
			return
		}
		if strings.TrimSpace(req.Username) == "" {
			writeError(w, http.StatusBadRequest, "invalid request")
			return
		}

		ipKey, userKey := loginIPKey(r), loginUserKey(req.Username)
		for _, key := range []string{ipKey, userKey} {
			if ok, retry := loginLimiter.Allow(key); !ok {
				seconds := int((retry + time.Second - 1) / time.Second)
				if seconds < 1 {
					seconds = 1
				}
				w.Header().Set("Retry-After", strconv.Itoa(seconds))
				writeError(w, http.StatusTooManyRequests, "too many login attempts")
				return
			}
		}

		result, err := authenticator.Login(r.Context(), req.Username, req.Password)
		if err != nil {
			loginLimiter.Record(ipKey)
			loginLimiter.Record(userKey)
			logRequestWarn(r, "authenticate "+strconv.Quote(req.Username), err)
			if errors.Is(err, usermanager.ErrUserDisabled) {
				writeError(w, http.StatusForbidden, "user is disabled")
				return
			}
			writeError(w, http.StatusUnauthorized, "invalid credentials")
			return
		}
		// Don't let earlier failures against the same account penalise its
		// owner after a successful login.
		loginLimiter.Reset(userKey)

		writeJSON(w, http.StatusOK, map[string]any{
			"accessToken":  result.AccessToken,
			"refreshToken": result.RefreshToken,
			"tokenType":    result.TokenType,
			"expiresAt":    result.ExpiresAt,
		})
	})

	mux.HandleFunc("POST /api/auth/refresh", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			RefreshToken string `json:"refreshToken"`
		}
		if err := decodeJSON(w, r, &req); err != nil {
			logRequestWarn(r, "decode refresh request", err)
			writeError(w, http.StatusBadRequest, "invalid request")
			return
		}
		if req.RefreshToken == "" {
			writeError(w, http.StatusBadRequest, "refreshToken is required")
			return
		}

		result, err := authenticator.Refresh(r.Context(), req.RefreshToken)
		if err != nil {
			logRequestWarn(r, "refresh token", err)
			writeError(w, http.StatusUnauthorized, "invalid or expired refresh token")
			return
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"accessToken":  result.AccessToken,
			"refreshToken": result.RefreshToken,
			"tokenType":    result.TokenType,
			"expiresAt":    result.ExpiresAt,
		})
	})

	mux.Handle("GET /.well-known/jmap", requireAuth(authenticator, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handleJMAPSession(w, r, maxFileSize)
	})))
	mux.Handle("POST /api/jmap/", requireAuth(authenticator, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handleJMAPAPIRequest(w, r, dispatcher, maxFileSize)
	})))
	mux.Handle("POST /upload/", requireAuth(authenticator, http.HandlerFunc(handleJMAPUpload)))
	mux.Handle("GET /download/", requireAuth(authenticator, http.HandlerFunc(handleJMAPDownload)))
	mux.Handle("GET /eventsource/", requireAuth(authenticator, http.HandlerFunc(handleJMAPPush)))

	mux.Handle("GET /api/email/raw", requireAuth(authenticator, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handleRawEmail(w, r, emailStore)
	})))

	mux.Handle("GET /api/user/autocomplete", requireAuth(authenticator, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claims := currentClaims(r)
		if claims == nil || !claims.EmissaryUserManagement {
			writeError(w, http.StatusForbidden, "insufficient permissions")
			return
		}

		q := strings.TrimSpace(r.URL.Query().Get("q"))
		if len(q) < 3 {
			writeError(w, http.StatusBadRequest, "q must be at least 3 characters")
			return
		}

		matches, err := userManager.Autocomplete(r.Context(), q)
		if err != nil {
			logRequestError(r, "autocomplete users", err)
			writeError(w, http.StatusInternalServerError, "failed to search users")
			return
		}
		if matches == nil {
			matches = []string{}
		}
		if len(matches) > 10 {
			matches = matches[:10]
		}

		writeJSON(w, http.StatusOK, map[string]any{"users": matches})
	})))

	supportHandler := newSupportHandler(context.Background(), cfg, emailStore, metaStore, sessionStore, mailSender, userManager)
	supportServer, err := supportapi.NewServer(supportHandler, supportSecurityHandler{authenticator}, supportapi.WithErrorHandler(supportErrorHandler))
	if err != nil {
		slog.Error("Failed to create support server", "err", err)
	} else {
		// Register per-method prefixes so the System Manager routes coexist
		// with the method-specific JMAP catch-all (POST /api/jmap/).
		mux.Handle("GET /api/support/", supportServer)
		mux.Handle("POST /api/support/", supportServer)
		mux.Handle("PUT /api/support/", supportServer)
		mux.Handle("DELETE /api/support/", supportServer)
	}

	if mgr, ok := userManager.(EmissaryAuthManager); ok {
		usersHandler := NewUsersHandler(mgr, authenticator)
		usersServer, err := usersapi.NewServer(usersHandler, usersSecurityHandler{authenticator})
		if err != nil {
			slog.Error("Failed to create users server", "err", err)
		} else {
			mux.Handle("/api/user/", usersServer)
		}
	} else {
		// Self-service password change needs a manager that owns the user's
		// password verifier. Give a clear answer instead of the generic 404
		// that the rest of the user API returns for unsupported backends.
		mux.Handle("POST /api/user/change-password", requireAuth(authenticator, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			writeError(w, http.StatusForbidden, "password changes are not supported by the configured user manager")
		})))
		// The capability check must work for every backend so the UI can hide
		// the password form before the user tries to use it.
		mux.Handle("GET /api/user/password-change-supported", requireAuth(authenticator, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, http.StatusOK, map[string]bool{"supported": false})
		})))
	}

	mux.Handle("POST /api/auth/logout", requireAuth(authenticator, handleLogout(authenticator)))

	notFound := func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusNotFound, "not found")
	}
	mux.HandleFunc("/api", notFound)
	mux.HandleFunc("/api/", notFound)
	mux.HandleFunc("/", serveFrontend)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		slog.Debug("HTTP request", "method", r.Method, "path", r.URL.Path)
		defer func() {
			if recovered := recover(); recovered != nil {
				slog.Error("Panic serving request", "method", r.Method, "path", r.URL.Path, "panic", recovered)
				writeError(w, http.StatusInternalServerError, "internal server error")
			}
		}()
		// Support data changes at runtime. Prevent browsers or proxies from
		// serving a cached rules response after OK has saved new rules.
		if strings.HasPrefix(r.URL.Path, "/api/support/") {
			w.Header().Set("Cache-Control", "no-store")
		}
		// One cap for every request body, including the generated OpenAPI
		// decoders that read r.Body directly. Handlers that need a tighter
		// limit (the auth endpoints) may wrap it again with MaxBytesReader.
		r.Body = http.MaxBytesReader(w, r.Body, maxFileSize)
		mux.ServeHTTP(w, r)
	})
}

type claimsContextKey struct{}

func bearerClaims(authenticator *auth.Authenticator, r *http.Request) (*auth.Claims, bool) {
	const prefix = "Bearer "
	header := r.Header.Get("Authorization")
	if !strings.HasPrefix(header, prefix) {
		return nil, false
	}
	token := strings.TrimSpace(strings.TrimPrefix(header, prefix))
	claims, err := authenticator.ValidateClaims(token)
	if err != nil {
		logRequestWarn(r, "validate bearer token", err)
		return nil, false
	}
	return claims, true
}

func requireAuth(authenticator *auth.Authenticator, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claims, ok := bearerClaims(authenticator, r)
		if !ok {
			w.Header().Set("WWW-Authenticate", `Bearer realm="emissary"`)
			writeError(w, http.StatusUnauthorized, "authentication required")
			return
		}
		ctx := context.WithValue(r.Context(), claimsContextKey{}, claims)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

type usersSecurityHandler struct {
	authenticator *auth.Authenticator
}

// HandleBearerAuth validates the bearer token and makes its claims available
// to the operation handlers. Authorization is enforced per operation in
// usersHandler: management operations require EmissaryUserManagement, while
// ChangePassword and ChangeOwnPassword allow a user to change their own
// password. Checking the claim here would make those paths unreachable.
func (h usersSecurityHandler) HandleBearerAuth(ctx context.Context, _ usersapi.OperationName, t usersapi.BearerAuth) (context.Context, error) {
	claims, err := h.authenticator.ValidateClaims(t.Token)
	if err != nil {
		return nil, err
	}
	return context.WithValue(ctx, claimsContextKey{}, claims), nil
}

// EmissaryAuthManager and EmissaryUser live in the usermanager package. The
// aliases keep the handler's exported names stable for callers and tests.
type EmissaryAuthManager = usermanager.EmissaryAuthManager

type EmissaryUser = usermanager.EmissaryUser

func currentClaims(r *http.Request) *auth.Claims {
	claims, _ := r.Context().Value(claimsContextKey{}).(*auth.Claims)
	return claims
}

func decodeJSON(w http.ResponseWriter, r *http.Request, destination any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBody)
	return json.NewDecoder(r.Body).Decode(destination)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		slog.Error("Write JSON response", "err", err)
	}
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

// logRequestError records a request that failed because of a server-side
// problem. These are included at the default ERROR level.
func logRequestError(r *http.Request, operation string, err error) {
	slog.Error("Request failed", "method", r.Method, "path", r.URL.Path, "operation", operation, "err", err)
}

// logRequestWarn records a request that failed for a client-controlled reason
// (bad input, invalid credentials, aborted response). These are useful when
// diagnosing issues but stay out of the quiet default configuration.
func logRequestWarn(r *http.Request, operation string, err error) {
	slog.Warn("Request rejected", "method", r.Method, "path", r.URL.Path, "operation", operation, "err", err)
}

func serveFrontend(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	if path == "/" || path == "" {
		path = "/index.html"
	}

	file, err := gui.Files.Open("frontend/dist" + path)
	if err != nil {
		path = "/index.html"
		file, err = gui.Files.Open("frontend/dist/index.html")
		if err != nil {
			logRequestError(r, "open embedded frontend index", err)
			http.Error(w, "Frontend not built. Run 'npm run build' in frontend directory.", http.StatusNotFound)
			return
		}
	}
	defer file.Close()

	stat, err := file.Stat()
	if err != nil {
		logRequestError(r, "stat embedded frontend file "+strconv.Quote(path), err)
		http.Error(w, "file not found", http.StatusNotFound)
		return
	}
	if stat.IsDir() {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", getContentType(path))
	w.Header().Set("Content-Length", strconv.FormatInt(stat.Size(), 10))
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		if _, err := io.Copy(w, file); err != nil {
			logRequestWarn(r, "write embedded frontend file "+strconv.Quote(path), err)
		}
	}
}

func getContentType(path string) string {
	if strings.HasSuffix(path, ".html") {
		return "text/html; charset=utf-8"
	}
	if strings.HasSuffix(path, ".js") {
		return "application/javascript; charset=utf-8"
	}
	if strings.HasSuffix(path, ".css") {
		return "text/css; charset=utf-8"
	}
	if strings.HasSuffix(path, ".json") {
		return "application/json; charset=utf-8"
	}
	if strings.HasSuffix(path, ".svg") {
		return "image/svg+xml"
	}
	if strings.HasSuffix(path, ".png") {
		return "image/png"
	}
	if strings.HasSuffix(path, ".jpg") || strings.HasSuffix(path, ".jpeg") {
		return "image/jpeg"
	}
	if strings.HasSuffix(path, ".ico") {
		return "image/x-icon"
	}
	return "application/octet-stream"
}

func handleJMAPSession(w http.ResponseWriter, r *http.Request, maxFileSize int64) {
	claims := currentClaims(r)
	username := claims.Subject

	// RFC 8620 section 2 requires servers to advertise the limits they
	// enforce. Advertising the configured value keeps clients from sending
	// batches we would have to reject with a limit error.
	coreCapabilities := map[string]any{
		"maxSizeRequest": maxFileSize,
		"maxSizeUpload":  maxFileSize,
	}
	mailCapabilities := map[string]any{
		"maxSizeAttachmentsPerEmail": maxFileSize,
	}

	session := map[string]any{
		"capabilities": map[string]any{
			"urn:ietf:params:jmap:core":             coreCapabilities,
			"urn:ietf:params:jmap:mail":             mailCapabilities,
			"urn:ietf:params:jmap:submission":       map[string]any{},
			"urn:ietf:params:jmap:vacationresponse": map[string]any{},
		},
		"accounts": map[string]any{
			username: map[string]any{
				"name":       username,
				"isPersonal": true,
				"isReadOnly": false,
				"accountCapabilities": map[string]any{
					"urn:ietf:params:jmap:core":             coreCapabilities,
					"urn:ietf:params:jmap:mail":             mailCapabilities,
					"urn:ietf:params:jmap:submission":       map[string]any{},
					"urn:ietf:params:jmap:vacationresponse": map[string]any{},
				},
			},
		},
		"primaryAccounts": map[string]any{
			"urn:ietf:params:jmap:core":             username,
			"urn:ietf:params:jmap:mail":             username,
			"urn:ietf:params:jmap:submission":       username,
			"urn:ietf:params:jmap:vacationresponse": username,
		},
		"username":       username,
		"apiUrl":         "/api/jmap/",
		"downloadUrl":    "/download/{accountId}/{blobId}/{name}",
		"uploadUrl":      "/upload/{accountId}/",
		"eventSourceUrl": "/eventsource/",
		"state":          jmapSessionState,
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(session)
}

func handleJMAPAPIRequest(w http.ResponseWriter, r *http.Request, dispatcher *jmap.Dispatcher, maxFileSize int64) {
	var req jmap.Request
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			// RFC 8620 section 3.6.1: capability limit violations are an HTTP
			// error with a problem details body naming the limit.
			writeJMAPLimitError(w, "maxSizeRequest", maxFileSize)
			return
		}
		writeJMAPError(w, "invalidRequest", "Invalid JSON request")
		return
	}

	claims := currentClaims(r)
	if claims == nil {
		writeJMAPError(w, "unauthorized", "Authentication required")
		return
	}

	// Use the authenticated user's accountId as the default for dispatch
	// The dispatcher will validate accountId per method call using the claims
	accountID := claims.Subject

	resp, err := dispatcher.DispatchRequest(r.Context(), accountID, claims, &req)
	if err != nil {
		writeJMAPError(w, "serverError", err.Error())
		return
	}

	resp.SessionState = jmapSessionState

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(resp)
}

// writeJMAPLimitError reports a request that exceeds an advertised capability
// limit using the problem details format required by RFC 8620 section 3.6.1.
func writeJMAPLimitError(w http.ResponseWriter, limit string, maxBytes int64) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(http.StatusRequestEntityTooLarge)
	json.NewEncoder(w).Encode(map[string]any{
		"type":   "urn:ietf:params:jmap:error:limit",
		"title":  "Request exceeds server limit",
		"status": http.StatusRequestEntityTooLarge,
		"detail": fmt.Sprintf("Request exceeds the %s limit of %d bytes", limit, maxBytes),
		"limit":  limit,
	})
}

func handleJMAPUpload(w http.ResponseWriter, r *http.Request) {
	writeJMAPError(w, "unimplemented", "Binary upload not implemented")
}

func handleJMAPDownload(w http.ResponseWriter, r *http.Request) {
	writeJMAPError(w, "unimplemented", "Binary download not implemented")
}

// handleRawEmail serves the original RFC 5322 message for a single email. The
// id is passed as a query parameter because delivered ids contain slashes and
// would otherwise be ambiguous in a URL path.
func handleRawEmail(w http.ResponseWriter, r *http.Request, store storage.Storage) {
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	if id == "" {
		writeError(w, http.StatusBadRequest, "id is required")
		return
	}

	accountID, err := rawEmailAccount(r.Context(), store, id)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			writeError(w, http.StatusNotFound, "email not found")
			return
		}
		logRequestError(r, "resolve raw email account", err)
		writeError(w, http.StatusInternalServerError, "failed to load email")
		return
	}
	if accountID == "" {
		writeError(w, http.StatusBadRequest, "invalid email id")
		return
	}

	claims := currentClaims(r)
	if claims == nil || (!claims.EmissaryUserManagement && !strings.EqualFold(accountID, claims.Subject)) {
		writeError(w, http.StatusForbidden, "insufficient permissions")
		return
	}

	raw, err := store.GetEmailRaw(r.Context(), id)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			writeError(w, http.StatusNotFound, "email not found")
			return
		}
		logRequestError(r, "load raw email", err)
		writeError(w, http.StatusInternalServerError, "failed to load raw email")
		return
	}

	w.Header().Set("Content-Type", "message/rfc822")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Length", strconv.Itoa(len(raw)))
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(raw); err != nil {
		logRequestWarn(r, "write raw email", err)
	}
}

// rawEmailAccount determines which account owns an email id. Delivered ids
// encode the account in the object key, so the common case needs no storage
// lookup. The fallback supports stores with opaque ids, such as the in-memory
// development store.
func rawEmailAccount(ctx context.Context, store storage.Storage, id string) (string, error) {
	if accountID, _, ok := email.MailboxFromID(id); ok && accountID != "" {
		return accountID, nil
	}
	listed, err := store.GetEmail(ctx, id)
	if err != nil {
		return "", err
	}
	if listed.Ref == nil {
		return "", storage.ErrNotFound
	}
	return listed.Ref.AccountID, nil
}

func handleJMAPPush(w http.ResponseWriter, r *http.Request) {
	writeJMAPError(w, "unimplemented", "Push not implemented")
}

func writeJMAPError(w http.ResponseWriter, typ, detail string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]any{
		"@type":  "error",
		"type":   typ,
		"detail": detail,
	})
}

func handleLogout(authenticator *auth.Authenticator) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims := currentClaims(r)
		if claims == nil || claims.SID == "" {
			writeError(w, http.StatusUnauthorized, "authentication required")
			return
		}
		if err := authenticator.Logout(r.Context(), claims.SID); err != nil {
			logRequestError(r, "revoke session", err)
			writeError(w, http.StatusInternalServerError, "failed to log out")
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"success": true})
	}
}
