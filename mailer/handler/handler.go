package handler

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/mail"
	"strconv"
	"strings"
	"time"

	"github.com/kncept/Emissary/mailer/auth"
	"github.com/kncept/Emissary/mailer/email"
	"github.com/kncept/Emissary/mailer/gui"
	"github.com/kncept/Emissary/mailer/metadata"
	"github.com/kncept/Emissary/mailer/sender"
	"github.com/kncept/Emissary/mailer/storage"
	"github.com/kncept/Emissary/mailer/usermanager"
)

const maxRequestBody = 1 << 20 // 1 MiB

// EmailResponse represents an email in API responses.
type EmailResponse struct {
	ID        string `json:"id"`
	From      string `json:"from"`
	To        string `json:"to"`
	Subject   string `json:"subject"`
	Body      string `json:"body"`
	Timestamp string `json:"timestamp"`
	Read      bool   `json:"read"`
	Flagged   bool   `json:"flagged"`
}

// SendEmailRequest represents a request to send an email.
type SendEmailRequest struct {
	// From may only be set by an administrator. Ordinary users always send as
	// their own address.
	From      string   `json:"from,omitempty"`
	To        []string `json:"to"`
	Cc        []string `json:"cc,omitempty"`
	Bcc       []string `json:"bcc,omitempty"`
	Subject   string   `json:"subject"`
	Body      string   `json:"body"`
	ReplyToID string   `json:"replyToId,omitempty"`
}

// HandlerConfig holds configuration for creating the handler.
type HandlerConfig struct {
	AuthUser       string
	AuthPass       string
	JWTSecret      string
	JWTSecretTTL   time.Duration
	S3Bucket       string
	StorageType    string
	MetadataType   string
	MetadataPath   string
	MetadataTable  string
	MetadataRegion string
	Storage        storage.Storage        // Optional: for testing with custom storage
	Metadata       metadata.MetadataStore // Optional: for testing with custom metadata
	// JWT audience and issuer for token validation
	JWTAudience string
	JWTIssuer   string

	// User manager configuration. UserManager may be supplied directly for
	// tests; otherwise it is built from the fields below.
	UserManager      usermanager.Manager
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

	// Outbound email sender. Sender may be supplied directly for tests;
	// otherwise it is built from the fields below.
	Sender     sender.Sender
	SenderType string
	SenderFrom string
}

// New creates a new HTTP handler with all routes configured.
func New(cfg HandlerConfig) (http.Handler, error) {
	// Initialize the JWT authenticator
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

	// Initialize the pluggable user manager. The built-in administrator is
	// configured inside the manager so that every backend has the same admin
	// semantics.
	users := cfg.UserManager
	var err error
	if users == nil {
		users, err = usermanager.New(usermanager.Config{
			Type:             cfg.UserManagerType,
			AdminUsername:    cfg.AuthUser,
			AdminPassword:    cfg.AuthPass,
			Domain:           cfg.UserDomain,
			DdbTable:         cfg.UserTable,
			Region:           cfg.UserRegion,
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

	authenticator := auth.NewWithUserManager(users, jwtSecret, jwtTTL, cfg.JWTAudience, cfg.JWTIssuer)

	// Initialize storage
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

	// Initialize metadata store
	var metaStore metadata.MetadataStore
	if cfg.Metadata != nil {
		metaStore = cfg.Metadata
	} else {
		switch cfg.MetadataType {
		case "file":
			metaStore, err = metadata.NewFileMetadata(cfg.MetadataPath)
		case "ddb", "dynamodb":
			metaStore, err = metadata.NewDdbMetadata(cfg.MetadataTable, cfg.MetadataRegion)
		default:
			metaStore = metadata.NewMemoryMetadata()
		}
	}
	if err != nil {
		return nil, err
	}

	// Initialize the outbound sender.
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

	return newHandler(authenticator, emailStore, metaStore, mailSender), nil
}

// newHandler wires the API routes, the auth middleware and the embedded
// frontend into a single http.Handler.
func newHandler(authenticator *auth.Authenticator, emailStore storage.Storage, metaStore metadata.MetadataStore, mailSender sender.Sender) http.Handler {
	mux := http.NewServeMux()

	// API routes (authenticated)
	mux.Handle("GET /api/emails", requireAuth(authenticator, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handleListEmails(w, r, emailStore, metaStore)
	})))
	mux.Handle("GET /api/emails/{id}", requireAuth(authenticator, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handleGetEmail(w, r, emailStore, metaStore)
	})))
	mux.Handle("POST /api/emails/{id}/read", requireAuth(authenticator, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handleMarkRead(w, r, emailStore, metaStore)
	})))
	mux.Handle("POST /api/emails/{id}/flag", requireAuth(authenticator, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handleMarkFlagged(w, r, emailStore, metaStore)
	})))
	mux.Handle("DELETE /api/emails/{id}", requireAuth(authenticator, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handleDeleteEmail(w, r, emailStore, metaStore)
	})))
	mux.Handle("POST /api/emails/send", requireAuth(authenticator, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handleSendEmail(w, r, emailStore, mailSender)
	})))
	mux.Handle("POST /api/auth/logout", requireAuth(authenticator, http.HandlerFunc(handleLogout)))

	// Auth routes (no middleware)
	mux.HandleFunc("POST /api/auth/login", func(w http.ResponseWriter, r *http.Request) {
		handleLogin(w, r, authenticator)
	})
	mux.HandleFunc("GET /api/auth/check", func(w http.ResponseWriter, r *http.Request) {
		handleAuthCheck(w, r, authenticator)
	})

	// Unknown API routes and everything else falls through to the frontend.
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

// bearerClaims extracts the "Authorization: Bearer <token>" header and, when
// the token is present and valid, returns its claims.
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

func currentClaims(r *http.Request) *auth.Claims {
	claims, _ := r.Context().Value(claimsContextKey{}).(*auth.Claims)
	return claims
}

func handleLogin(w http.ResponseWriter, r *http.Request, authenticator *auth.Authenticator) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	// A blank password is intentionally allowed: the "any" user manager
	// accepts any password, while the administrator is rejected separately.
	if err := decodeJSON(w, r, &req); err != nil {
		logRequestError(r, "decode login request", err)
		writeError(w, http.StatusBadRequest, "invalid request")
		return
	}
	if strings.TrimSpace(req.Username) == "" {
		writeError(w, http.StatusBadRequest, "invalid request")
		return
	}

	user, err := authenticator.AuthenticateUser(r.Context(), req.Username, req.Password)
	if err != nil {
		logRequestError(r, "authenticate "+strconv.Quote(req.Username), err)
		writeError(w, http.StatusUnauthorized, "invalid credentials")
		return
	}

	token, expiresAt, err := authenticator.IssueUser(user)
	if err != nil {
		logRequestError(r, "issue token for "+strconv.Quote(req.Username), err)
		writeError(w, http.StatusInternalServerError, "failed to issue token")
		return
	}

	// The client stores this token and presents it as a Bearer token on
	// subsequent requests. No refresh token is issued yet.
	writeJSON(w, http.StatusOK, map[string]any{
		"token":     token,
		"tokenType": "Bearer",
		"expiresAt": expiresAt,
		"username":  user.ID,
		"email":     user.Email,
		"admin":     user.Admin,
	})
}

// JWTs are stateless, so there is nothing to invalidate server-side; the
// client discards the token. The endpoint is kept as a stable hook in case
// token revocation is added later.
func handleLogout(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]bool{"success": true})
}

func handleAuthCheck(w http.ResponseWriter, r *http.Request, authenticator *auth.Authenticator) {
	if claims, ok := bearerClaims(authenticator, r); ok {
		writeJSON(w, http.StatusOK, map[string]any{
			"authenticated": true,
			"username":      claims.Subject,
			"admin":         claims.Admin,
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"authenticated": false})
}

// normalizeAddress returns a lower-case mailbox address. Invalid values are
// returned trimmed and lower-cased so filtering remains deterministic.
func normalizeAddress(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if parsed, err := mail.ParseAddress(value); err == nil {
		return strings.ToLower(strings.TrimSpace(parsed.Address))
	}
	return strings.ToLower(value)
}

// filterForClaims returns the mailbox an authenticated request is restricted
// to. An empty filter means "all emails" and is only possible for an admin.
func filterForClaims(claims *auth.Claims, requested string) string {
	if claims == nil {
		return ""
	}
	if claims.Admin {
		return normalizeAddress(requested)
	}
	return normalizeAddress(claims.Subject)
}

func addressMatches(address email.Address, user string) bool {
	return user != "" && normalizeAddress(address.Address) == user
}

// emailMatchesUser reports whether msg was sent by user or addressed to user
// (directly, by cc, or by bcc).
func emailMatchesUser(msg *email.UserEmail, user string) bool {
	if msg == nil || user == "" {
		return false
	}
	if addressMatches(msg.From, user) {
		return true
	}
	for _, list := range [][]email.Address{msg.To, msg.Cc, msg.Bcc} {
		for _, address := range list {
			if addressMatches(address, user) {
				return true
			}
		}
	}
	return false
}

// canAccessEmail applies the admin/user visibility rules to a single message.
func canAccessEmail(claims *auth.Claims, msg *email.UserEmail, requested string) bool {
	if claims == nil {
		return false
	}
	filter := filterForClaims(claims, requested)
	if claims.Admin && filter == "" {
		return true
	}
	return emailMatchesUser(msg, filter)
}

func handleListEmails(w http.ResponseWriter, r *http.Request, emailStore storage.Storage, metaStore metadata.MetadataStore) {
	ctx := r.Context()
	claims := currentClaims(r)
	requested := r.URL.Query().Get("user")

	emails, err := emailStore.ListAllEmails(ctx)
	if err != nil {
		logRequestError(r, "list emails", err)
		writeError(w, http.StatusInternalServerError, "failed to list emails")
		return
	}

	metaList, err := metaStore.List(ctx)
	if err != nil {
		// Read/flagged state is decorative: serve the emails anyway, but
		// keep the cause in the log.
		logRequestError(r, "list email metadata", err)
	}
	metaMap := make(map[string]metadata.EmailMetadata)
	for _, m := range metaList {
		metaMap[m.ID] = m
	}

	// An empty slice (not nil) so an empty inbox marshals as [] rather than
	// null; clients iterate the result directly.
	response := make([]EmailResponse, 0, len(emails))
	for _, email := range emails {
		if !canAccessEmail(claims, email, requested) {
			continue
		}
		meta := metaMap[email.ID]
		fromAddr := ""
		if email.From.Address != "" {
			fromAddr = email.From.Address
		}
		toAddr := ""
		if len(email.To) > 0 {
			toAddr = email.To[0].Address
		}
		response = append(response, EmailResponse{
			ID:        email.ID,
			From:      fromAddr,
			To:        toAddr,
			Subject:   email.Subject,
			Body:      email.Text,
			Timestamp: email.Date.Format(time.RFC3339),
			Read:      meta.Read,
			Flagged:   meta.Flagged,
		})
	}

	// Sort by timestamp descending (newest first)
	// For simplicity, we'll just return as-is
	writeJSON(w, http.StatusOK, response)
}

func handleGetEmail(w http.ResponseWriter, r *http.Request, emailStore storage.Storage, metaStore metadata.MetadataStore) {
	ctx := r.Context()
	id := r.PathValue("id")
	claims := currentClaims(r)

	// Find the email by ID
	message, err := emailStore.GetEmail(ctx, id)
	if err != nil {
		if err == storage.ErrNotFound {
			writeError(w, http.StatusNotFound, "email not found")
			return
		}
		logRequestError(r, "get email "+strconv.Quote(id), err)
		writeError(w, http.StatusInternalServerError, "failed to get email")
		return
	}

	// Do not reveal the existence of messages outside the caller's visibility.
	if !canAccessEmail(claims, message, r.URL.Query().Get("user")) {
		writeError(w, http.StatusNotFound, "email not found")
		return
	}

	meta, err := metaStore.Get(ctx, id)
	if err != nil {
		// Missing metadata should not make the message unreadable.
		logRequestError(r, "get email metadata "+strconv.Quote(id), err)
	}
	fromAddr := ""
	if message.From.Address != "" {
		fromAddr = message.From.Address
	}
	toAddr := ""
	if len(message.To) > 0 {
		toAddr = message.To[0].Address
	}
	writeJSON(w, http.StatusOK, EmailResponse{
		ID:        message.ID,
		From:      fromAddr,
		To:        toAddr,
		Subject:   message.Subject,
		Body:      message.Text,
		Timestamp: message.Date.Format(time.RFC3339),
		Read:      meta.Read,
		Flagged:   meta.Flagged,
	})
}

// requireVisibleEmail loads an email and confirms the caller may access it.
func requireVisibleEmail(w http.ResponseWriter, r *http.Request, emailStore storage.Storage) (*email.UserEmail, bool) {
	message, err := emailStore.GetEmail(r.Context(), r.PathValue("id"))
	if err != nil {
		if err == storage.ErrNotFound {
			writeError(w, http.StatusNotFound, "email not found")
			return nil, false
		}
		logRequestError(r, "get email "+strconv.Quote(r.PathValue("id")), err)
		writeError(w, http.StatusInternalServerError, "failed to get email")
		return nil, false
	}
	if !canAccessEmail(currentClaims(r), message, r.URL.Query().Get("user")) {
		writeError(w, http.StatusNotFound, "email not found")
		return nil, false
	}
	return message, true
}

func handleMarkRead(w http.ResponseWriter, r *http.Request, emailStore storage.Storage, metaStore metadata.MetadataStore) {
	ctx := r.Context()
	id := r.PathValue("id")

	message, ok := requireVisibleEmail(w, r, emailStore)
	if !ok {
		return
	}
	id = message.ID

	var req struct {
		Read bool `json:"read"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		logRequestError(r, "decode mark-read request "+strconv.Quote(id), err)
		writeError(w, http.StatusBadRequest, "invalid request")
		return
	}

	if err := metaStore.SetRead(ctx, id, req.Read); err != nil {
		logRequestError(r, "update read status "+strconv.Quote(id), err)
		writeError(w, http.StatusInternalServerError, "failed to update read status")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"success": true})
}

func handleMarkFlagged(w http.ResponseWriter, r *http.Request, emailStore storage.Storage, metaStore metadata.MetadataStore) {
	ctx := r.Context()
	id := r.PathValue("id")

	message, ok := requireVisibleEmail(w, r, emailStore)
	if !ok {
		return
	}
	id = message.ID

	var req struct {
		Flagged bool `json:"flagged"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		logRequestError(r, "decode mark-flagged request "+strconv.Quote(id), err)
		writeError(w, http.StatusBadRequest, "invalid request")
		return
	}

	if err := metaStore.SetFlagged(ctx, id, req.Flagged); err != nil {
		logRequestError(r, "update flag status "+strconv.Quote(id), err)
		writeError(w, http.StatusInternalServerError, "failed to update flag status")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"success": true})
}

// handleDeleteEmail removes a message the caller can see. Deletion follows
// the visibility rules of read/flag, but is shared: the message is a single
// object, so removing it takes effect for every recipient.
func handleDeleteEmail(w http.ResponseWriter, r *http.Request, emailStore storage.Storage, metaStore metadata.MetadataStore) {
	ctx := r.Context()

	message, ok := requireVisibleEmail(w, r, emailStore)
	if !ok {
		return
	}

	if err := emailStore.DeleteEmail(ctx, message.ID); err != nil {
		if err == storage.ErrNotFound {
			// Deleted concurrently between the visibility check and the
			// delete; report it the same as any other missing message.
			writeError(w, http.StatusNotFound, "email not found")
			return
		}
		logRequestError(r, "delete email "+strconv.Quote(message.ID), err)
		writeError(w, http.StatusInternalServerError, "failed to delete email")
		return
	}

	// Metadata is decorative: a failure to clean it up must not fail the
	// delete, but it is kept in the log.
	if err := metaStore.Delete(ctx, message.ID); err != nil {
		logRequestError(r, "delete email metadata "+strconv.Quote(message.ID), err)
	}
	writeJSON(w, http.StatusOK, map[string]bool{"success": true})
}

func handleSendEmail(w http.ResponseWriter, r *http.Request, emailStore storage.Storage, mailSender sender.Sender) {
	claims := currentClaims(r)
	var req SendEmailRequest
	if err := decodeJSON(w, r, &req); err != nil {
		logRequestError(r, "decode send-email request", err)
		writeError(w, http.StatusBadRequest, "invalid request")
		return
	}
	if len(req.To) == 0 || req.Subject == "" || req.Body == "" {
		writeError(w, http.StatusBadRequest, "invalid request")
		return
	}

	from := ""
	if claims != nil && claims.Admin {
		from = strings.TrimSpace(req.From)
		// When replying, an admin defaults to the address the original
		// message was addressed to (i.e. the user whose inbox it is in).
		if from == "" && req.ReplyToID != "" {
			original, err := emailStore.GetEmail(r.Context(), req.ReplyToID)
			if err != nil {
				logRequestError(r, "load reply target "+strconv.Quote(req.ReplyToID), err)
			} else if len(original.To) > 0 {
				from = original.To[0].Address
			}
		}
	} else if claims != nil {
		// Ordinary users can only send as themselves.
		from = claims.Subject
	}

	if from != "" {
		parsed, err := mail.ParseAddress(from)
		if err != nil {
			logRequestError(r, "parse from address "+strconv.Quote(from), err)
			writeError(w, http.StatusBadRequest, "invalid from address")
			return
		}
		from = parsed.Address
	}
	if from == "" {
		from = mailSender.From()
	}

	// Parse all To addresses
	toAddresses := make([]email.Address, 0, len(req.To))
	for _, addr := range req.To {
		parsed, err := mail.ParseAddress(strings.TrimSpace(addr))
		if err != nil {
			logRequestError(r, "parse to address "+strconv.Quote(addr), err)
			writeError(w, http.StatusBadRequest, "invalid to address")
			return
		}
		toAddresses = append(toAddresses, email.Address{Address: parsed.Address})
	}

	// Parse CC addresses
	ccAddresses := make([]email.Address, 0, len(req.Cc))
	for _, addr := range req.Cc {
		parsed, err := mail.ParseAddress(strings.TrimSpace(addr))
		if err != nil {
			logRequestError(r, "parse cc address "+strconv.Quote(addr), err)
			writeError(w, http.StatusBadRequest, "invalid cc address")
			return
		}
		ccAddresses = append(ccAddresses, email.Address{Address: parsed.Address})
	}

	// Parse BCC addresses
	bccAddresses := make([]email.Address, 0, len(req.Bcc))
	for _, addr := range req.Bcc {
		parsed, err := mail.ParseAddress(strings.TrimSpace(addr))
		if err != nil {
			logRequestError(r, "parse bcc address "+strconv.Quote(addr), err)
			writeError(w, http.StatusBadRequest, "invalid bcc address")
			return
		}
		bccAddresses = append(bccAddresses, email.Address{Address: parsed.Address})
	}

	message := &email.UserEmail{
		From:    email.Address{Address: from},
		To:      toAddresses,
		Cc:      ccAddresses,
		Bcc:     bccAddresses,
		Subject: strings.TrimSpace(req.Subject),
		Date:    time.Now().UTC(),
		Text:    req.Body,
	}
	if err := mailSender.Send(r.Context(), message); err != nil {
		logRequestError(r, "send email", err)
		writeError(w, http.StatusBadGateway, "could not send email")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": "Email sent"})
}

func serveFrontend(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	if path == "/" || path == "" {
		path = "/index.html"
	}

	file, err := gui.Files.Open("frontend/dist" + path)
	if err != nil {
		// Try index.html for SPA routing
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

// logRequestError records the underlying cause of a failed request. Clients
// only ever see the generic message passed to writeError; the real error is
// kept in the server log for debugging.
func logRequestError(r *http.Request, operation string, err error) {
	log.Printf("%s %q: %s: %v", r.Method, r.URL.Path, operation, err)
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
