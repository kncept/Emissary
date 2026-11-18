package handler

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	supportapi "github.com/kncept/Emissary/api/support/generated"
	usersapi "github.com/kncept/Emissary/api/users/generated"
	"github.com/kncept/Emissary/mailer/auth"
	"github.com/kncept/Emissary/mailer/gui"
	"github.com/kncept/Emissary/mailer/jmap"
	"github.com/kncept/Emissary/mailer/metadata"
	"github.com/kncept/Emissary/mailer/sender"
	"github.com/kncept/Emissary/mailer/session"
	"github.com/kncept/Emissary/mailer/storage"
	"github.com/kncept/Emissary/mailer/usermanager"
)

const maxRequestBody = 1 << 20

type HandlerConfig struct {
	AuthUser         string
	AuthPass         string
	ListenAddr       string
	JWTSecret        string
	JWTSecretTTL     time.Duration
	S3Bucket         string
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
	SenderFrom       string
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
		log.Println("WARNING: JWT_SECRET not set; using an ephemeral secret. Tokens are invalidated on restart.")
	}

	jwtTTL := cfg.JWTSecretTTL
	if jwtTTL == 0 {
		jwtTTL = 24 * time.Hour
	}

	users := cfg.UserManager
	var err error
	if users == nil {
		users, err = usermanager.New(usermanager.Config{
			Type:             cfg.UserManagerType,
			AdminUsername:    cfg.AuthUser,
			AdminPassword:    cfg.AuthPass,
			Domain:           cfg.UserDomain,
			DdbTable:         resolveDDBTable(cfg.UserTable, cfg.DDBTablePrefix, "users"),
			Region:           cfg.UserRegion,
			S3Bucket:         cfg.S3Bucket,
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

	var sessionStore session.SessionStore
	switch cfg.SessionType {
	case "ddb", "dynamodb":
		sessionStore, err = session.NewDdbSessionStore(resolveDDBTable(cfg.SessionTable, cfg.DDBTablePrefix, "sessions"), cfg.SessionRegion)
		if err != nil {
			return nil, err
		}
	default:
		sessionStore = session.NewMemorySessionStore()
	}

	authenticator := auth.NewWithUserManager(users, sessionStore, jwtSecret, jwtTTL, cfg.JWTAudience, cfg.JWTIssuer, cfg.AuthUser)

	var emailStore storage.Storage
	if cfg.Storage != nil {
		emailStore = cfg.Storage
	} else {
		switch strings.ToLower(strings.TrimSpace(cfg.StorageType)) {
		case "memory":
			emailStore = storage.NewMemoryStorage()
		default:
			emailStore, err = storage.NewS3Storage(cfg.S3Bucket)
			if err != nil {
				return nil, err
			}
		}
	}

	var metaStore metadata.MetadataStore
	if cfg.Metadata != nil {
		metaStore = cfg.Metadata
	} else {
		switch cfg.MetadataType {
		case "file":
			metaStore, err = metadata.NewFileMetadata(cfg.MetadataPath)
		case "ddb", "dynamodb":
			metaStore, err = metadata.NewDdbMetadata(resolveDDBTable(cfg.MetadataTable, cfg.DDBTablePrefix, "metadata"), cfg.MetadataRegion)
		default:
			metaStore = metadata.NewMemoryMetadata()
		}
	}
	if err != nil {
		return nil, err
	}

	mailSender := cfg.Sender
	if mailSender == nil {
		switch strings.ToLower(strings.TrimSpace(cfg.SenderType)) {
		case "aws-ses", "ses":
			mailSender, err = sender.NewAwsSesSender(context.Background(), cfg.SenderFrom)
			if err != nil {
				return nil, err
			}
		default:
			mailSender = sender.NewLogSender(cfg.SenderFrom)
		}
	}

	return newHandler(authenticator, emailStore, metaStore, sessionStore, mailSender, users, cfg), nil
}

func newHandler(authenticator *auth.Authenticator, emailStore storage.Storage, metaStore metadata.MetadataStore, sessionStore session.SessionStore, mailSender sender.Sender, userManager usermanager.UserManager, cfg HandlerConfig) http.Handler {
	mux := http.NewServeMux()

	// Create JMAP handler with sender for EmailSubmission
	jmapHandler := jmap.NewHandler(emailStore, metaStore, userManager, mailSender)
	dispatcher := jmap.NewDispatcher(jmapHandler, emailStore)

	mux.HandleFunc("POST /api/auth/login", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Username string `json:"username"`
			Password string `json:"password"`
		}
		if err := decodeJSON(w, r, &req); err != nil {
			logRequestError(r, "decode login request", err)
			writeError(w, http.StatusBadRequest, "invalid request")
			return
		}
		if strings.TrimSpace(req.Username) == "" {
			writeError(w, http.StatusBadRequest, "invalid request")
			return
		}

		result, err := authenticator.Login(r.Context(), req.Username, req.Password)
		if err != nil {
			logRequestError(r, "authenticate "+strconv.Quote(req.Username), err)
			if errors.Is(err, usermanager.ErrUserDisabled) {
				writeError(w, http.StatusForbidden, "user is disabled")
				return
			}
			writeError(w, http.StatusUnauthorized, "invalid credentials")
			return
		}

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
			logRequestError(r, "decode refresh request", err)
			writeError(w, http.StatusBadRequest, "invalid request")
			return
		}
		if req.RefreshToken == "" {
			writeError(w, http.StatusBadRequest, "refreshToken is required")
			return
		}

		result, err := authenticator.Refresh(r.Context(), req.RefreshToken)
		if err != nil {
			logRequestError(r, "refresh token", err)
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
		handleJMAPSession(w, r)
	})))
	mux.Handle("POST /api/jmap/", requireAuth(authenticator, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handleJMAPAPIRequest(w, r, dispatcher)
	})))
	mux.Handle("POST /upload/", requireAuth(authenticator, http.HandlerFunc(handleJMAPUpload)))
	mux.Handle("GET /download/", requireAuth(authenticator, http.HandlerFunc(handleJMAPDownload)))
	mux.Handle("GET /eventsource/", requireAuth(authenticator, http.HandlerFunc(handleJMAPPush)))

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

	supportHandler := newSupportHandler(cfg, emailStore, metaStore, sessionStore, mailSender, userManager)
	supportServer, err := supportapi.NewServer(supportHandler, supportSecurityHandler{authenticator}, supportapi.WithErrorHandler(supportErrorHandler))
	if err != nil {
		log.Printf("failed to create support server: %v", err)
	} else {
		// Register per-method prefixes so the System Manager routes coexist
		// with the method-specific JMAP catch-all (POST /api/jmap/).
		mux.Handle("GET /api/support/", supportServer)
		mux.Handle("POST /api/support/", supportServer)
	}

	if mgr, ok := userManager.(EmissaryAuthManager); ok {
		usersHandler := NewUsersHandler(mgr, authenticator)
		usersServer, err := usersapi.NewServer(usersHandler, usersSecurityHandler{authenticator})
		if err != nil {
			log.Printf("failed to create users server: %v", err)
		} else {
			mux.Handle("/api/user/", usersServer)
		}
	}

	mux.Handle("POST /api/auth/logout", requireAuth(authenticator, http.HandlerFunc(handleLogout)))

	notFound := func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusNotFound, "not found")
	}
	mux.HandleFunc("/api", notFound)
	mux.HandleFunc("/api/", notFound)
	mux.HandleFunc("/", serveFrontend)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if recovered := recover(); recovered != nil {
				log.Printf("panic serving %s %s: %v", r.Method, r.URL.Path, recovered)
				writeError(w, http.StatusInternalServerError, "internal server error")
			}
		}()
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
		logRequestError(r, "validate bearer token", err)
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

func (h usersSecurityHandler) HandleBearerAuth(ctx context.Context, operationName usersapi.OperationName, t usersapi.BearerAuth) (context.Context, error) {
	claims, err := h.authenticator.ValidateClaims(t.Token)
	if err != nil {
		return nil, err
	}

	if !claims.EmissaryUserManagement {
		return nil, &authErr{message: "insufficient permissions"}
	}

	ctx = context.WithValue(ctx, claimsContextKey{}, claims)
	return ctx, nil
}

// EmissaryAuthManager and EmissaryUser live in the usermanager package. The
// aliases keep the handler's exported names stable for callers and tests.
type EmissaryAuthManager = usermanager.EmissaryAuthManager

type EmissaryUser = usermanager.EmissaryUser

type authErr struct {
	message string
}

func (e *authErr) Error() string {
	return e.message
}

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
		log.Printf("write JSON response: %v", err)
	}
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func logRequestError(r *http.Request, operation string, err error) {
	log.Printf("%s %q: %s: %v", r.Method, r.URL.Path, operation, err)
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
			logRequestError(r, "write embedded frontend file "+strconv.Quote(path), err)
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

func handleJMAPSession(w http.ResponseWriter, r *http.Request) {
	claims := currentClaims(r)
	username := claims.Subject

	session := map[string]any{
		"capabilities": map[string]any{
			"urn:ietf:params:jmap:core":             map[string]any{},
			"urn:ietf:params:jmap:mail":             map[string]any{},
			"urn:ietf:params:jmap:submission":       map[string]any{},
			"urn:ietf:params:jmap:vacationresponse": map[string]any{},
		},
		"accounts": map[string]any{
			username: map[string]any{
				"name":       username,
				"isPersonal": true,
				"isReadOnly": false,
				"accountCapabilities": map[string]any{
					"urn:ietf:params:jmap:core":             map[string]any{},
					"urn:ietf:params:jmap:mail":             map[string]any{},
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
		"state":          "state123",
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(session)
}

func handleJMAPAPIRequest(w http.ResponseWriter, r *http.Request, dispatcher *jmap.Dispatcher) {
	var req jmap.Request
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
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

	resp.SessionState = "state123"

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(resp)
}

func handleJMAPUpload(w http.ResponseWriter, r *http.Request) {
	writeJMAPError(w, "unimplemented", "Binary upload not implemented")
}

func handleJMAPDownload(w http.ResponseWriter, r *http.Request) {
	writeJMAPError(w, "unimplemented", "Binary download not implemented")
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

func handleLogout(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]bool{"success": true})
}
