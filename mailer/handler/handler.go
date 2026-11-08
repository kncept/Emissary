package handler

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/kncept/Emissary/mailer/auth"
	"github.com/kncept/Emissary/mailer/gui"
	"github.com/kncept/Emissary/mailer/metadata"
	"github.com/kncept/Emissary/mailer/storage"
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
	To      string `json:"to"`
	Subject string `json:"subject"`
	Body    string `json:"body"`
}

// HandlerConfig holds configuration for creating the handler.
type HandlerConfig struct {
	AuthUser      string
	AuthPass      string
	JWTSecret     string
	JWTSecretTTL  time.Duration
	S3Bucket      string
	S3Prefix      string
	S3Region      string
	MetadataType  string
	MetadataPath  string
	MetadataTable string
	MetadataRegion string
	Storage       storage.Storage // Optional: for testing with custom storage
	// JWT audience and issuer for token validation
	JWTAudience string
	JWTIssuer   string
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

	authenticator := auth.New(cfg.AuthUser, cfg.AuthPass, jwtSecret, jwtTTL, cfg.JWTAudience, cfg.JWTIssuer)

	// Initialize storage
	var emailStore storage.Storage
	var err error
	if cfg.Storage != nil {
		emailStore = cfg.Storage
	} else {
		emailStore, err = storage.NewS3Storage(cfg.S3Bucket, cfg.S3Prefix, cfg.S3Region)
		if err != nil {
			return nil, err
		}
	}

	// Initialize metadata store
	var metaStore metadata.MetadataStore
	switch cfg.MetadataType {
	case "file":
		metaStore, err = metadata.NewFileMetadata(cfg.MetadataPath)
	case "ddb":
		metaStore, err = metadata.NewDdbMetadata(cfg.MetadataTable, cfg.MetadataRegion)
	default:
		metaStore = metadata.NewMemoryMetadata()
	}
	if err != nil {
		return nil, err
	}

	return newHandler(authenticator, emailStore, metaStore), nil
}

// newHandler wires the API routes, the auth middleware and the embedded
// frontend into a single http.Handler.
func newHandler(authenticator *auth.Authenticator, emailStore storage.Storage, metaStore metadata.MetadataStore) http.Handler {
	mux := http.NewServeMux()

	// API routes (authenticated)
	mux.Handle("GET /api/emails", requireAuth(authenticator, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handleListEmails(w, r, emailStore, metaStore)
	})))
	mux.Handle("GET /api/emails/{id}", requireAuth(authenticator, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handleGetEmail(w, r, emailStore, metaStore)
	})))
	mux.Handle("POST /api/emails/{id}/read", requireAuth(authenticator, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handleMarkRead(w, r, metaStore)
	})))
	mux.Handle("POST /api/emails/{id}/flag", requireAuth(authenticator, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handleMarkFlagged(w, r, metaStore)
	})))
	mux.Handle("POST /api/emails/send", requireAuth(authenticator, http.HandlerFunc(handleSendEmail)))
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

// bearerSubject extracts the "Authorization: Bearer <token>" header and, when
// the token is present and valid, returns the authenticated subject.
func bearerSubject(authenticator *auth.Authenticator, r *http.Request) (string, bool) {
	const prefix = "Bearer "
	header := r.Header.Get("Authorization")
	if !strings.HasPrefix(header, prefix) {
		return "", false
	}
	token := strings.TrimSpace(strings.TrimPrefix(header, prefix))
	subject, err := authenticator.Validate(token)
	if err != nil {
		return "", false
	}
	return subject, true
}

func requireAuth(authenticator *auth.Authenticator, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := bearerSubject(authenticator, r); !ok {
			w.Header().Set("WWW-Authenticate", `Bearer realm="emissary"`)
			writeError(w, http.StatusUnauthorized, "authentication required")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func handleLogin(w http.ResponseWriter, r *http.Request, authenticator *auth.Authenticator) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := decodeJSON(w, r, &req); err != nil || req.Username == "" || req.Password == "" {
		writeError(w, http.StatusBadRequest, "invalid request")
		return
	}

	if !authenticator.Authenticate(req.Username, req.Password) {
		writeError(w, http.StatusUnauthorized, "invalid credentials")
		return
	}

	token, expiresAt, err := authenticator.Issue(req.Username)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to issue token")
		return
	}

	// The client stores this token and presents it as a Bearer token on
	// subsequent requests. No refresh token is issued yet.
	writeJSON(w, http.StatusOK, map[string]any{
		"token":     token,
		"tokenType": "Bearer",
		"expiresAt": expiresAt,
	})
}

// JWTs are stateless, so there is nothing to invalidate server-side; the
// client discards the token. The endpoint is kept as a stable hook in case
// token revocation is added later.
func handleLogout(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]bool{"success": true})
}

func handleAuthCheck(w http.ResponseWriter, r *http.Request, authenticator *auth.Authenticator) {
	if subject, ok := bearerSubject(authenticator, r); ok {
		writeJSON(w, http.StatusOK, map[string]any{"authenticated": true, "username": subject})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"authenticated": false})
}

func handleListEmails(w http.ResponseWriter, r *http.Request, emailStore storage.Storage, metaStore metadata.MetadataStore) {
	ctx := r.Context()
	emails, err := emailStore.ListAllEmails(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list emails")
		return
	}

	metaList, _ := metaStore.List(ctx)
	metaMap := make(map[string]metadata.EmailMetadata)
	for _, m := range metaList {
		metaMap[m.ID] = m
	}

	var response []EmailResponse
	for _, email := range emails {
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

	// Find the email by ID
	email, err := emailStore.GetEmail(ctx, id)
	if err != nil {
		if err == storage.ErrNotFound {
			writeError(w, http.StatusNotFound, "email not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to get email")
		return
	}

	meta, _ := metaStore.Get(ctx, id)
	fromAddr := ""
	if email.From.Address != "" {
		fromAddr = email.From.Address
	}
	toAddr := ""
	if len(email.To) > 0 {
		toAddr = email.To[0].Address
	}
	writeJSON(w, http.StatusOK, EmailResponse{
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

func handleMarkRead(w http.ResponseWriter, r *http.Request, metaStore metadata.MetadataStore) {
	ctx := r.Context()
	id := r.PathValue("id")

	var req struct {
		Read bool `json:"read"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request")
		return
	}

	if err := metaStore.SetRead(ctx, id, req.Read); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update read status")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"success": true})
}

func handleMarkFlagged(w http.ResponseWriter, r *http.Request, metaStore metadata.MetadataStore) {
	ctx := r.Context()
	id := r.PathValue("id")

	var req struct {
		Flagged bool `json:"flagged"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request")
		return
	}

	if err := metaStore.SetFlagged(ctx, id, req.Flagged); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update flag status")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"success": true})
}

func handleSendEmail(w http.ResponseWriter, r *http.Request) {
	var req SendEmailRequest
	if err := decodeJSON(w, r, &req); err != nil || req.To == "" || req.Subject == "" || req.Body == "" {
		writeError(w, http.StatusBadRequest, "invalid request")
		return
	}

	// For now, we'll just log the email (in production, use AWS SES)
	log.Printf("Sending email to: %s, Subject: %s", req.To, req.Subject)

	// TODO: Implement actual SES sending
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": "Email queued for sending"})
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
			http.Error(w, "Frontend not built. Run 'npm run build' in frontend directory.", http.StatusNotFound)
			return
		}
	}
	defer file.Close()

	stat, err := file.Stat()
	if err != nil {
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
		_, _ = io.Copy(w, file)
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