package handler

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	supportapi "github.com/kncept/Emissary/api/support/generated"
	usersapi "github.com/kncept/Emissary/api/users/generated"
	"github.com/kncept/Emissary/mailer/auth"
	"github.com/kncept/Emissary/mailer/components/storage"
	"github.com/kncept/Emissary/mailer/components/usermanager"
	"github.com/kncept/Emissary/mailer/config"
	configvendor "github.com/kncept/Emissary/mailer/config-vendor"
	"github.com/kncept/Emissary/mailer/email"
	"github.com/kncept/Emissary/mailer/gui"
	"github.com/kncept/Emissary/mailer/identity"
	"github.com/kncept/Emissary/mailer/jmap"
	"github.com/kncept/Emissary/mailer/jmap/push"
	"github.com/kncept/Emissary/mailer/jmap/pushsubscription"
	"github.com/kncept/Emissary/mailer/receiver"
)

const (
	// maxRequestBody is the tighter cap used by the authentication endpoints,
	// which only ever carry a username/password or a refresh token.
	maxRequestBody = 1 << 20

	// defaultJWTSecretTTL is used when neither the config nor the options
	// specify a token lifetime.
	defaultJWTSecretTTL = 24 * time.Hour

	// jmapSessionState is stable because the session object itself is static:
	// every mutable value is reported per account by Foo/get's state token.
	jmapSessionState = "session-0"

	// RFC 8620 section 2 requires every core limit to be advertised. These
	// are the limits actually enforced by the upload and API endpoints; the
	// values match the RFC's suggested minimums.
	jmapMaxConcurrentUpload   = 4
	jmapMaxConcurrentRequests = 4
	jmapMaxCallsInRequest     = 16
	jmapMaxObjectsInGet       = 500
	jmapMaxObjectsInSet       = 500

	// RFC 8621 account capability values for the fixed, flat mailbox
	// hierarchy currently exposed by Mailbox/get.
	jmapMaxMailboxesPerEmail = 1
	jmapMaxMailboxDepth      = 1
	jmapMaxSizeMailboxName   = 255

	// RFC 8620 section 2 requires at least the case-insensitive ASCII
	// collation used by the server's string comparisons.
	jmapCollationASCIICasemap = "i;ascii-casemap"

	// Resource URL templates. These match the routes registered in
	// newHandler and include the variables required by RFC 8620 section 2.
	// The type variable is placed in the query because media types contain
	// slashes, which the RFC recommends avoiding in a path segment.
	jmapAPIPath             = "/api/jmap/"
	jmapDownloadURLTemplate = "/download/{accountId}/{blobId}/{name}?type={type}"
	jmapUploadURLTemplate   = "/upload/{accountId}"
	// eventSourceUrl is a level-1 URI template; clients substitute all three
	// variables (RFC 8620 section 7.3).
	jmapEventSourceURLTemplate = "/eventsource?types={types}&closeafter={closeafter}&ping={ping}"

	// jmapUnreferencedBlobQuotaLimit names the separate quota applied to
	// blobs that no JMAP object references yet (RFC 8620 section 6).
	jmapUnreferencedBlobQuotaLimit = "unreferencedBlobQuota"
)

// SESIdentityLister is the shared SES identity source from the identity
// package. The alias keeps existing handler call sites and tests readable.
type SESIdentityLister = identity.Lister

// Options holds the handler-local settings that are not component seams.
// Empty scalar values fall back to the corresponding config.Config values so
// production wiring only needs to set genuine overrides (for example IsLambda
// or a test's SESIdentityLister).
type Options struct {
	JWTSecret            string
	JWTSecretTTL         time.Duration
	LoginRateLimit       int
	LoginRateLimitWindow time.Duration
	MaxFileSize          int64
	IsLambda             bool
	// SESIdentityLister is an optional override for tests. Nil means create
	// an SES client lazily on first use.
	SESIdentityLister SESIdentityLister
}

func resolvedJWTSecret(cfg *config.Config, opts Options) string {
	if opts.JWTSecret != "" {
		return opts.JWTSecret
	}
	return cfg.Server.JWTSecret
}

func resolvedJWTTTL(cfg *config.Config, opts Options) time.Duration {
	if opts.JWTSecretTTL > 0 {
		return opts.JWTSecretTTL
	}
	if cfg.Server.JWTExpiry > 0 {
		return cfg.Server.JWTExpiry
	}
	return defaultJWTSecretTTL
}

func resolvedMaxFileSize(cfg *config.Config, opts Options) int64 {
	if opts.MaxFileSize > 0 {
		return opts.MaxFileSize
	}
	if cfg.MaxFileSize > 0 {
		return cfg.MaxFileSize
	}
	return config.DefaultMaxFileSize
}

// New wires the HTTP handler from the one component bundle built by
// configvendor.FromConfig. Configuration and component selection live in
// config.Load and the vendor; this function only resolves handler-local
// defaults and constructs the authenticator and routes.
func New(ctx context.Context, cfg *config.Config, b *configvendor.Bundle, opts Options) (http.Handler, error) {
	if cfg == nil {
		return nil, errors.New("nil config")
	}
	if b == nil {
		return nil, errors.New("nil component bundle")
	}
	if b.Storage == nil || b.Metadata == nil || b.Session == nil || b.Sender == nil || b.UserManager == nil || b.ReceiverConfigStore == nil {
		return nil, errors.New("component bundle is incomplete")
	}

	jwtSecret := resolvedJWTSecret(cfg, opts)
	if jwtSecret == "" {
		secret := make([]byte, 32)
		if _, err := rand.Read(secret); err != nil {
			return nil, err
		}
		jwtSecret = base64.RawURLEncoding.EncodeToString(secret)
		slog.Warn("JWT_SECRET not set; using an ephemeral secret. Tokens are invalidated on restart")
	}
	jwtTTL := resolvedJWTTTL(cfg, opts)

	// The caller (main) normally sets the process-wide MIME limit before
	// building components. Apply it here only when that handoff did not
	// happen, so a deliberate caller override is not silently replaced.
	maxFileSize := resolvedMaxFileSize(cfg, opts)
	if email.MaxPartSize() == email.DefaultMaxPartSize {
		email.SetMaxPartSize(maxFileSize)
	}

	// FromConfig loads the rules from the store. A caller may inject a config
	// snapshot directly (tests and programmatic use); otherwise load it from
	// the bundled store. Work on a copy so the caller's bundle is unchanged.
	components := *b
	if components.ReceiverConfig == nil {
		identityConfig, err := receiver.LoadMailIdentityConfig(ctx, components.ReceiverConfigStore)
		if err != nil {
			return nil, err
		}
		components.ReceiverConfig = identityConfig
	}
	slog.Debug("Receiver rules loaded", "identities", len(components.ReceiverConfig))

	authenticator := auth.NewWithUserManager(
		components.UserManager, components.Session,
		jwtSecret, jwtTTL,
		cfg.Server.JWTAudience, cfg.Server.JWTIssuer, cfg.Server.Username,
	)
	return newHandler(ctx, authenticator, &components, cfg, opts), nil
}

func newHandler(ctx context.Context, authenticator *auth.Authenticator, b *configvendor.Bundle, cfg *config.Config, opts Options) http.Handler {
	mux := http.NewServeMux()
	maxFileSize := resolvedMaxFileSize(cfg, opts)
	loginLimiter := newLoginLimiter(opts.LoginRateLimit, opts.LoginRateLimitWindow)

	emailStore := b.Storage
	mailSender := b.Sender
	userManager := b.UserManager

	// Binary uploads have their own RFC 8620 quota and aging rules layered
	// over the account-scoped immutable blob store. The configured upload
	// size is also the maximum attachment total for one message, so it is a
	// valid (and advertised) unreferenced-blob quota.
	blobLifecycle := storage.NewBlobLifecycle(emailStore, maxFileSize)
	uploadSlots := make(chan struct{}, jmapMaxConcurrentUpload)
	requestSlots := make(chan struct{}, jmapMaxConcurrentRequests)

	// One lister is shared by the From validator and the receiver rules editor.
	// Remember whether the caller injected one: an injected lister enables the
	// validator even for a local sender, while the lazy default must not change
	// local/test behaviour by making SES the identity source.
	hasListerOverride := opts.SESIdentityLister != nil
	if !hasListerOverride {
		opts.SESIdentityLister = identity.NewLazyLister()
	}
	var fromValidator jmap.FromValidator
	if cfg.Sender.IsSES() || (mailSender != nil && mailSender.Name() == "AwsSesSender") || hasListerOverride {
		fromValidator = identity.NewValidator(opts.SESIdentityLister)
	}
	jmapHandler := jmap.NewHandler(emailStore, b.Metadata, userManager, mailSender, fromValidator)
	jmapHandler.SetSessionChecker(func(ctx context.Context, sid string) bool {
		_, err := b.Session.Get(ctx, sid)
		return err == nil
	})
	pushHub := push.NewHub(jmapHandler, jmapHandler, emailStore)
	pushHub.SetSessionChecker(func(ctx context.Context, sid string) bool {
		_, err := b.Session.Get(ctx, sid)
		return err == nil
	})
	pushHub.Start(ctx)
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
	mux.Handle("GET /eventsource", requireEventSourceAuth(authenticator, http.HandlerFunc(pushHub.ServeHTTP)))
	mux.Handle("POST /api/jmap/", requireAuth(authenticator, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handleJMAPAPIRequest(w, r, dispatcher, maxFileSize, pushHub, requestSlots)
	})))
	mux.Handle("POST /upload/{accountId}", requireAuth(authenticator, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handleJMAPUpload(w, r, blobLifecycle, userManager, maxFileSize, uploadSlots)
		flushPushState(r, pushHub)
	})))
	mux.Handle("GET /download/{accountId}/{blobId}/{name}", requireAuth(authenticator, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handleJMAPDownload(w, r, emailStore, userManager)
	})))

	mux.Handle("GET /api/email/raw", requireAuth(authenticator, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handleRawEmail(w, r, emailStore)
	})))

	mux.Handle("GET /api/user/autocomplete", requireAuth(authenticator, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claims := auth.ClaimsFromContext(r.Context())
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

	supportHandler := newSupportHandler(ctx, cfg, b, opts)
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

	mux.Handle("POST /api/auth/logout", requireAuth(authenticator, handleLogout(authenticator, emailStore, pushHub)))

	notFound := func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusNotFound, "not found")
	}
	mux.HandleFunc("/api", notFound)
	mux.HandleFunc("/api/", notFound)
	mux.HandleFunc("/", serveFrontend)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		slog.Debug("HTTP request", "method", r.Method, "path", safeRequestTarget(r))
		defer func() {
			if recovered := recover(); recovered != nil {
				slog.Error("Panic serving request", "method", r.Method, "path", safeRequestTarget(r), "panic", recovered)
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

func requireAuth(authenticator *auth.Authenticator, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claims, err := auth.ClaimsFromBearerHeader(authenticator, r.Header.Get("Authorization"))
		if err != nil {
			logRequestWarn(r, "validate bearer token", err)
			w.Header().Set("WWW-Authenticate", `Bearer realm="emissary"`)
			writeError(w, http.StatusUnauthorized, "authentication required")
			return
		}
		ctx := auth.ContextWithClaims(r.Context(), claims)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// requireEventSourceAuth accepts the same bearer token as requireAuth plus a
// `token` query parameter, because the browser EventSource API cannot set an
// Authorization header. The query token is equivalent to a bearer token and
// must be treated as a secret by clients and intermediaries.
func requireEventSourceAuth(authenticator *auth.Authenticator, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var claims *auth.Claims
		var err error
		if header := r.Header.Get("Authorization"); header != "" {
			claims, err = auth.ClaimsFromBearerHeader(authenticator, header)
		} else if token := r.URL.Query().Get("token"); token != "" {
			claims, err = auth.ClaimsFromBearerToken(authenticator, token)
		} else {
			err = auth.ErrInvalidToken
		}
		if err != nil {
			logRequestWarn(r, "validate event source token", err)
			w.Header().Set("WWW-Authenticate", `Bearer realm="emissary"`)
			writeError(w, http.StatusUnauthorized, "authentication required")
			return
		}
		ctx := auth.ContextWithClaims(r.Context(), claims)
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
	claims, err := auth.ClaimsFromBearerToken(h.authenticator, t.Token)
	if err != nil {
		return nil, err
	}
	return auth.ContextWithClaims(ctx, claims), nil
}

// EmissaryAuthManager and EmissaryUser live in the usermanager package. The
// aliases keep the handler's exported names stable for callers and tests.
type EmissaryAuthManager = usermanager.EmissaryAuthManager

type EmissaryUser = usermanager.EmissaryUser

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

// safeRequestTarget returns a loggable request path. The EventSource `token`
// query parameter is a bearer token (the browser EventSource API cannot set an
// Authorization header), so it is redacted before any log line is emitted.
// Without a token the path is returned unchanged, as before.
func safeRequestTarget(r *http.Request) string {
	if r.URL == nil {
		return ""
	}
	query := r.URL.Query()
	if !query.Has("token") {
		return r.URL.Path
	}
	query.Set("token", "[REDACTED]")
	return r.URL.Path + "?" + query.Encode()
}

// logRequestError records a request that failed because of a server-side
// problem. These are included at the default ERROR level.
func logRequestError(r *http.Request, operation string, err error) {
	slog.Error("Request failed", "method", r.Method, "path", safeRequestTarget(r), "operation", operation, "err", err)
}

// logRequestWarn records a request that failed for a client-controlled reason
// (bad input, invalid credentials, aborted response). These are useful when
// diagnosing issues but stay out of the quiet default configuration.
func logRequestWarn(r *http.Request, operation string, err error) {
	slog.Warn("Request rejected", "method", r.Method, "path", safeRequestTarget(r), "operation", operation, "err", err)
}

var frontendFS = func() http.FileSystem {
	sub, err := fs.Sub(gui.Files, "frontend/dist")
	if err != nil {
		panic(err)
	}
	return http.FS(sub)
}()

var frontendFileServer = http.FileServer(frontendFS)

func serveFrontend(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/")
	if name == "" {
		name = "index.html"
	}
	// FileServer redirects requests for /index.html to "./". Keep the previous
	// behaviour by asking it for the directory root instead.
	serve := r
	if name == "index.html" {
		serve = r.Clone(r.Context())
		serve.URL.Path = "/"
		serve.URL.RawPath = ""
	}
	if frontendFileExists(name) {
		frontendFileServer.ServeHTTP(w, serve)
		return
	}

	// Unknown routes are client-side routes, not missing assets. Serve the SPA
	// entry point so the frontend router can render them.
	if !frontendFileExists("index.html") {
		logRequestError(r, "open embedded frontend index", errors.New("frontend/dist/index.html not embedded"))
		http.Error(w, "Frontend not built. Run 'npm run build' in frontend directory.", http.StatusNotFound)
		return
	}
	fallback := r.Clone(r.Context())
	fallback.URL.Path = "/"
	fallback.URL.RawPath = ""
	frontendFileServer.ServeHTTP(w, fallback)
}

func frontendFileExists(name string) bool {
	if name == "" || strings.Contains(name, "..") {
		return false
	}
	file, err := frontendFS.Open(name)
	if err != nil {
		return false
	}
	defer file.Close()
	stat, err := file.Stat()
	return err == nil && !stat.IsDir()
}

func handleJMAPSession(w http.ResponseWriter, r *http.Request, maxFileSize int64) {
	claims := auth.ClaimsFromContext(r.Context())
	username := claims.Subject

	// RFC 8620 section 2 requires servers to advertise the limits they
	// enforce. Advertising the configured value keeps clients from sending
	// batches we would have to reject with a limit error.
	coreCapabilities := map[string]any{
		"maxSizeUpload":         maxFileSize,
		"maxConcurrentUpload":   jmapMaxConcurrentUpload,
		"maxSizeRequest":        maxFileSize,
		"maxConcurrentRequests": jmapMaxConcurrentRequests,
		"maxCallsInRequest":     jmapMaxCallsInRequest,
		"maxObjectsInGet":       jmapMaxObjectsInGet,
		"maxObjectsInSet":       jmapMaxObjectsInSet,
		"collationAlgorithms":   []string{jmapCollationASCIICasemap},
	}
	// The Mail and Submission capability values in the Session object are
	// empty objects; their limits are reported per account (RFC 8621 1.3).
	mailAccountCapabilities := map[string]any{
		"maxMailboxesPerEmail":       jmapMaxMailboxesPerEmail,
		"maxMailboxDepth":            jmapMaxMailboxDepth,
		"maxSizeMailboxName":         jmapMaxSizeMailboxName,
		"maxSizeAttachmentsPerEmail": maxFileSize,
		"emailQuerySortOptions":      []string{"receivedAt"},
		"mayCreateTopLevelMailbox":   true,
	}
	submissionAccountCapabilities := map[string]any{
		"maxDelayedSend":       0,
		"submissionExtensions": map[string]any{},
	}

	// The VacationResponse capability value is an empty object in both the
	// Session "capabilities" and the account's "accountCapabilities" (RFC 8621
	// section 1.3.3). Its methods are now implemented, so it is advertised.
	vacationCapabilities := map[string]any{}
	session := map[string]any{
		"capabilities": map[string]any{
			jmap.CapabilityCore:             coreCapabilities,
			jmap.CapabilityMail:             map[string]any{},
			jmap.CapabilitySubmission:       map[string]any{},
			jmap.CapabilityVacationResponse: vacationCapabilities,
		},
		"accounts": map[string]any{
			username: map[string]any{
				"name":       username,
				"isPersonal": true,
				"isReadOnly": false,
				"accountCapabilities": map[string]any{
					jmap.CapabilityCore:             coreCapabilities,
					jmap.CapabilityMail:             mailAccountCapabilities,
					jmap.CapabilitySubmission:       submissionAccountCapabilities,
					jmap.CapabilityVacationResponse: vacationCapabilities,
				},
			},
		},
		// RFC 8620: core is a server-wide capability and SHOULD NOT appear
		// in primaryAccounts.
		"primaryAccounts": map[string]any{
			jmap.CapabilityMail:             username,
			jmap.CapabilitySubmission:       username,
			jmap.CapabilityVacationResponse: username,
		},
		"username":       username,
		"apiUrl":         jmapAPIPath,
		"downloadUrl":    jmapDownloadURLTemplate,
		"uploadUrl":      jmapUploadURLTemplate,
		"eventSourceUrl": jmapEventSourceURLTemplate,
		"state":          jmapSessionState,
	}

	// The Session object reflects per-account state and capabilities; browsers
	// and proxies must never serve a cached copy in place of a fresh one.
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(session)
}

func handleJMAPAPIRequest(w http.ResponseWriter, r *http.Request, dispatcher *jmap.Dispatcher, maxFileSize int64, pushHub *push.Hub, slots chan struct{}) {
	// Enforce the advertised maxConcurrentRequests limit before reading the
	// body, mirroring the upload semaphore. Rejecting immediately also keeps a
	// saturated Lambda from buffering more request bodies.
	select {
	case slots <- struct{}{}:
		defer func() { <-slots }()
	default:
		writeJMAPProblem(w, http.StatusTooManyRequests, "urn:ietf:params:jmap:error:limit",
			"Request exceeds server limit",
			fmt.Sprintf("Too many concurrent JMAP requests (limit %d)", jmapMaxConcurrentRequests),
			"maxConcurrentRequests")
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			writeJMAPLimitError(w, "maxSizeRequest", maxFileSize)
			return
		}
		writeJMAPRequestError(w, &jmap.RequestError{
			Type:   "about:blank",
			Title:  "Request body could not be read",
			Status: http.StatusBadRequest,
			Detail: "The request body could not be read.",
		})
		return
	}
	if int64(len(body)) > maxFileSize {
		writeJMAPLimitError(w, "maxSizeRequest", maxFileSize)
		return
	}

	req, requestErr := jmap.DecodeRequest(r.Header.Get("Content-Type"), body, jmap.RequestLimits{
		MaxCallsInRequest: jmapMaxCallsInRequest,
		MaxObjectsInGet:   jmapMaxObjectsInGet,
		MaxObjectsInSet:   jmapMaxObjectsInSet,
	})
	if requestErr != nil {
		writeJMAPRequestError(w, requestErr)
		return
	}

	claims := auth.ClaimsFromContext(r.Context())
	if claims == nil {
		writeJMAPProblem(w, http.StatusUnauthorized, "about:blank", "Unauthorized",
			"Authentication required", "")
		return
	}

	// Use the authenticated user's accountId as the default for dispatch
	// The dispatcher will validate accountId per method call using the claims
	accountID := claims.Subject

	resp, err := dispatcher.DispatchRequest(r.Context(), accountID, claims, req)
	if err != nil {
		slog.Error("JMAP dispatch failed", "err", err)
		writeJMAPProblem(w, http.StatusInternalServerError, "about:blank", "Internal Server Error",
			"The request could not be processed.", "")
		return
	}

	resp.SessionState = jmapSessionState

	// Publish before the response is written so a client that immediately opens
	// an EventSource sees the new state in its snapshot as well.
	flushPushState(r, pushHub)

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(resp)
}

// flushPushState records the credential/account pair for webhook delivery and
// refreshes the type states after a write. It is deliberately non-fatal: JMAP
// writes are already durable, and push is best effort.
func flushPushState(r *http.Request, hub *push.Hub) {
	if hub == nil {
		return
	}
	claims := auth.ClaimsFromContext(r.Context())
	if claims == nil || claims.Subject == "" {
		return
	}
	hub.Remember(r.Context(), claims.Subject)
	if err := hub.Refresh(r.Context(), claims.Subject); err != nil {
		logRequestWarn(r, "refresh JMAP push state", err)
	}
}

// writeJMAPRequestError renders an RFC 8620 section 3.6.1 request-level error
// using RFC 7807 problem details.
func writeJMAPRequestError(w http.ResponseWriter, requestErr *jmap.RequestError) {
	writeJMAPProblem(w, requestErr.Status, requestErr.Type, requestErr.Title, requestErr.Detail, requestErr.Limit)
}

// writeJMAPProblem writes an RFC 7807 problem details body with the required
// problem+json content type.
func writeJMAPProblem(w http.ResponseWriter, status int, typ, title, detail, limit string) {
	problem := map[string]any{
		"type":   typ,
		"title":  title,
		"status": status,
		"detail": detail,
	}
	if limit != "" {
		problem["limit"] = limit
	}
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(problem)
}

// writeJMAPLimitError reports a request that exceeds the advertised
// maxSizeRequest limit. Unlike other limit violations, an oversized body uses
// HTTP 413 because the server refuses to read it at all.
func writeJMAPLimitError(w http.ResponseWriter, limit string, maxBytes int64) {
	writeJMAPProblem(w, http.StatusRequestEntityTooLarge, "urn:ietf:params:jmap:error:limit",
		"Request exceeds server limit",
		fmt.Sprintf("Request exceeds the %s limit of %d bytes", limit, maxBytes), limit)
}

// handleJMAPUpload implements RFC 8620 section 6.1. The raw request body is
// the blob; its Content-Type becomes the blob's type and is echoed in the
// response. The account is path-scoped to the authenticated user unless the
// caller has user-management privileges.
func handleJMAPUpload(w http.ResponseWriter, r *http.Request, lifecycle *storage.BlobLifecycle, userManager usermanager.UserManager, maxFileSize int64, slots chan struct{}) {
	claims := auth.ClaimsFromContext(r.Context())
	accountID := strings.TrimSpace(r.PathValue("accountId"))
	if status, detail := jmapBinaryAccountError(r.Context(), accountID, claims, userManager); status != 0 {
		writeJMAPProblem(w, status, "about:blank", http.StatusText(status), detail, "")
		return
	}

	select {
	case slots <- struct{}{}:
		defer func() { <-slots }()
	default:
		writeJMAPProblem(w, http.StatusTooManyRequests, "urn:ietf:params:jmap:error:limit",
			"Request exceeds server limit",
			fmt.Sprintf("Too many concurrent uploads (limit %d)", jmapMaxConcurrentUpload),
			"maxConcurrentUpload")
		return
	}

	// Expired unreferenced blobs are pruned opportunistically here, never in
	// the middle of a JMAP method call: a method may create and destroy
	// references to the same blob and must not see it disappear.
	if err := lifecycle.Collect(r.Context(), accountID); err != nil {
		logRequestWarn(r, "collect expired blobs", err)
	}

	contentType := strings.TrimSpace(r.Header.Get("Content-Type"))
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	data, err := io.ReadAll(r.Body)
	if err != nil {
		writeJMAPUploadError(w, r, err, maxFileSize)
		return
	}
	// The global MaxBytesReader normally rejects this while reading; keep an
	// explicit check for callers that supply a body directly (tests, proxies).
	if int64(len(data)) > maxFileSize {
		writeJMAPLimitError(w, "maxSizeUpload", maxFileSize)
		return
	}

	meta, err := lifecycle.Upload(r.Context(), accountID, claims.Subject, contentType, data)
	if err != nil {
		writeJMAPUploadError(w, r, err, maxFileSize)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"accountId": meta.AccountID,
		"blobId":    meta.ID,
		"type":      meta.Type,
		"size":      meta.Size,
	})
}

// handleJMAPDownload implements RFC 8620 section 6.2. The response media type
// comes from the client-supplied type parameter (or the uploaded type when
// omitted); name is returned as the Content-Disposition filename.
func handleJMAPDownload(w http.ResponseWriter, r *http.Request, store storage.BlobStore, userManager usermanager.UserManager) {
	claims := auth.ClaimsFromContext(r.Context())
	accountID := strings.TrimSpace(r.PathValue("accountId"))
	if status, detail := jmapBinaryAccountError(r.Context(), accountID, claims, userManager); status != 0 {
		writeJMAPProblem(w, status, "about:blank", http.StatusText(status), detail, "")
		return
	}
	blobID := strings.TrimSpace(r.PathValue("blobId"))
	name := strings.TrimSpace(r.PathValue("name"))
	if blobID == "" || name == "" {
		writeJMAPProblem(w, http.StatusBadRequest, "about:blank", "Bad Request",
			"blobId and name are required", "")
		return
	}

	meta, reader, err := store.OpenBlob(r.Context(), accountID, blobID)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			writeJMAPProblem(w, http.StatusNotFound, "about:blank", "Not Found",
				"Blob not found", "")
			return
		}
		logRequestError(r, "open blob", err)
		writeJMAPProblem(w, http.StatusInternalServerError, "about:blank", "Internal Server Error",
			"The blob could not be read.", "")
		return
	}
	defer reader.Close()

	// RFC 8620: an unreferenced blob is only accessible to the user who
	// uploaded it, even when the account itself is shared.
	if !meta.EmailBacked && !meta.IsPinned() && meta.Uploader() != strings.ToLower(claims.Subject) {
		writeJMAPProblem(w, http.StatusForbidden, "about:blank", "Forbidden",
			"insufficient permissions to access this blob", "")
		return
	}

	contentType := jmapDownloadContentType(r.URL.Query().Get("type"), meta.Type)
	filename := jmapDownloadFilename(name)
	headers := w.Header()
	headers.Set("Content-Type", contentType)
	headers.Set("X-Content-Type-Options", "nosniff")
	headers.Set("Content-Length", strconv.FormatInt(meta.Size, 10))
	headers.Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": filename}))
	// Blob bytes are immutable, so the response is safe to cache for a long
	// time. Keep it private because the URL carries no authentication.
	headers.Set("Cache-Control", "private, immutable, max-age=31536000")
	w.WriteHeader(http.StatusOK)
	if _, err := io.Copy(w, reader); err != nil {
		logRequestWarn(r, "write blob", err)
	}
}

// jmapBinaryAccountError validates the path-scoped account for both binary
// endpoints. It returns an HTTP status and detail, or (0, "") when allowed.
func jmapBinaryAccountError(ctx context.Context, accountID string, claims *auth.Claims, userManager usermanager.UserManager) (int, string) {
	if claims == nil {
		return http.StatusUnauthorized, "authentication required"
	}
	if accountID == "" {
		return http.StatusBadRequest, "accountId is required"
	}
	if !claims.EmissaryUserManagement && !strings.EqualFold(accountID, claims.Subject) {
		return http.StatusForbidden, "insufficient permissions"
	}
	// A privileged caller may masquerade, but only for an existing account.
	if !strings.EqualFold(accountID, claims.Subject) && userManager != nil {
		user, err := userManager.Lookup(ctx, accountID)
		if err != nil || user == nil {
			return http.StatusNotFound, "account not found"
		}
	}
	return 0, ""
}

func writeJMAPUploadError(w http.ResponseWriter, r *http.Request, err error, maxFileSize int64) {
	var maxBytesErr *http.MaxBytesError
	switch {
	case errors.As(err, &maxBytesErr):
		writeJMAPLimitError(w, "maxSizeUpload", maxFileSize)
	case errors.Is(err, storage.ErrBlobQuota):
		writeJMAPProblem(w, http.StatusRequestEntityTooLarge, "urn:ietf:params:jmap:error:limit",
			"Request exceeds server limit",
			fmt.Sprintf("Upload would exceed the %s quota of %d bytes", jmapUnreferencedBlobQuotaLimit, maxFileSize),
			jmapUnreferencedBlobQuotaLimit)
	default:
		logRequestError(r, "upload blob", err)
		writeJMAPProblem(w, http.StatusInternalServerError, "about:blank", "Internal Server Error",
			"The blob could not be stored.", "")
	}
}

// jmapDownloadContentType rejects header-injection characters and falls back
// to the blob's own type (then octet-stream) for missing or malformed values.
func jmapDownloadContentType(requested, fallback string) string {
	for _, value := range []string{strings.TrimSpace(requested), strings.TrimSpace(fallback)} {
		if value == "" {
			continue
		}
		if strings.ContainsAny(value, "\r\n") {
			break
		}
		for _, r := range value {
			if r < 0x20 || r == 0x7f {
				return "application/octet-stream"
			}
		}
		return value
	}
	return "application/octet-stream"
}

// jmapDownloadFilename keeps a client-controlled filename from injecting
// headers while preserving the name the client asked for.
func jmapDownloadFilename(name string) string {
	name = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, strings.TrimSpace(name))
	if name == "" {
		return "blob"
	}
	return name
}

// handleRawEmail serves the original RFC 5322 message for a single email. The
// id is passed as a query parameter for compatibility with legacy delivered
// object keys; current opaque ids are also safe as URL path segments.
// accountId may be provided; when omitted it is derived from a legacy delivered
// key or defaults to the authenticated user for opaque ids.
func handleRawEmail(w http.ResponseWriter, r *http.Request, store storage.Storage) {
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	if id == "" {
		writeError(w, http.StatusBadRequest, "id is required")
		return
	}

	claims := auth.ClaimsFromContext(r.Context())
	if claims == nil {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}

	accountID := strings.TrimSpace(r.URL.Query().Get("accountId"))
	if accountID == "" {
		if derived, _, ok := email.MailboxFromID(id); ok && derived != "" {
			accountID = derived
		} else {
			accountID = claims.Subject
		}
	}
	if accountID == "" {
		writeError(w, http.StatusBadRequest, "accountId is required")
		return
	}
	if !claims.EmissaryUserManagement && !strings.EqualFold(accountID, claims.Subject) {
		writeError(w, http.StatusForbidden, "insufficient permissions")
		return
	}

	raw, err := store.GetEmailRaw(r.Context(), accountID, id)
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

func handleLogout(authenticator *auth.Authenticator, store storage.PushSubscriptionStore, pushHub *push.Hub) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims := auth.ClaimsFromContext(r.Context())
		if claims == nil || claims.SID == "" {
			writeError(w, http.StatusUnauthorized, "authentication required")
			return
		}
		if err := authenticator.Logout(r.Context(), claims.SID); err != nil {
			logRequestError(r, "revoke session", err)
			writeError(w, http.StatusInternalServerError, "failed to log out")
			return
		}
		// RFC 8620 section 7.2 requires subscriptions to be destroyed when
		// the credentials that created them are revoked. Log an error rather
		// than failing the logout: the session is already gone, so the
		// subscriptions are unreachable even if cleanup cannot run.
		if err := pushsubscription.DestroyForCredential(r.Context(), store); err != nil {
			logRequestWarn(r, "destroy push subscriptions on logout", err)
		}
		// Access tokens are stateless and remain valid until expiry, so a live
		// stream must be closed explicitly when its session is revoked.
		if pushHub != nil {
			pushHub.CloseCredential(r.Context())
		}
		writeJSON(w, http.StatusOK, map[string]bool{"success": true})
	}
}
