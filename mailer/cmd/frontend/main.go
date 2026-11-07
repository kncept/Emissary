package main

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-lambda-go/lambda"
	"github.com/awslabs/aws-lambda-go-api-proxy/httpadapter"
	"github.com/kncept/Emissary/mailer/auth"
	"github.com/kncept/Emissary/mailer/awsutil"
	"github.com/kncept/Emissary/mailer/gui"
	"github.com/kncept/Emissary/mailer/metadata"
	"github.com/kncept/Emissary/mailer/storage"
)

const maxRequestBody = 1 << 20 // 1 MiB

var (
	authenticator  *auth.Authenticator
	authUser       string
	authPass       string
	s3Bucket       string
	s3Prefix       string
	s3Region       string
	metadataType   string
	metadataPath   string
	metadataTable  string
	metadataRegion string
)

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

type SendEmailRequest struct {
	To      string `json:"to"`
	Subject string `json:"subject"`
	Body    string `json:"body"`
}

func main() {
	// Load configuration from environment
	authUser = os.Getenv("AUTH_USERNAME")
	authPass = os.Getenv("AUTH_PASSWORD")
	s3Bucket = os.Getenv("S3_BUCKET")
	s3Prefix = os.Getenv("S3_PREFIX")
	s3Region = os.Getenv("S3_REGION")
	metadataType = os.Getenv("METADATA_TYPE")
	metadataPath = os.Getenv("METADATA_PATH")
	metadataTable = os.Getenv("METADATA_TABLE")
	metadataRegion = os.Getenv("METADATA_REGION")

	// Defaults
	if s3Region == "" {
		s3Region = "us-east-1"
	}
	if metadataRegion == "" {
		metadataRegion = "us-east-1"
	}
	if metadataType == "" {
		metadataType = "memory"
	}

	// Validate required config
	if authUser == "" || authPass == "" {
		log.Fatal("AUTH_USERNAME and AUTH_PASSWORD must be set")
	}
	if s3Bucket == "" {
		log.Fatal("S3_BUCKET must be set")
	}

	// Initialize the JWT authenticator. Authentication is stateless: the
	// client stores the issued token and sends it back as a Bearer token.
	// No HTTP cookies are used.
	jwtSecret := os.Getenv("JWT_SECRET")
	if jwtSecret == "" {
		jwtSecret = os.Getenv("SESSION_SECRET") // backward compatibility
	}
	if jwtSecret == "" {
		secret := make([]byte, 32)
		if _, err := rand.Read(secret); err != nil {
			log.Fatalf("generate JWT secret: %v", err)
		}
		jwtSecret = base64.RawURLEncoding.EncodeToString(secret)
		log.Println("WARNING: JWT_SECRET not set; using an ephemeral secret. Tokens are invalidated on restart.")
	}

	jwtTTL := 24 * time.Hour
	if ttl := os.Getenv("JWT_TTL"); ttl != "" {
		parsed, err := time.ParseDuration(ttl)
		if err != nil {
			log.Fatalf("invalid JWT_TTL %q: %v", ttl, err)
		}
		jwtTTL = parsed
	}

	authenticator = auth.New(authUser, authPass, jwtSecret, jwtTTL)

	// Initialize storage
	s3Storage, err := storage.NewS3Storage(s3Bucket, s3Prefix, s3Region)
	if err != nil {
		log.Fatalf("Failed to initialize S3 storage: %v", err)
	}

	// Initialize metadata store
	var metaStore metadata.MetadataStore
	switch metadataType {
	case "file":
		metaStore, err = metadata.NewFileMetadata(metadataPath)
	case "ddb":
		metaStore, err = metadata.NewDdbMetadata(metadataTable, metadataRegion)
	default:
		metaStore = metadata.NewMemoryMetadata()
	}
	if err != nil {
		log.Fatalf("Failed to initialize metadata store: %v", err)
	}

	handler := newHandler(s3Storage, metaStore)

	// The application http.Handler is fully built at this point. In Lambda,
	// hand it to the runtime instead of starting a listener of our own; the
	// adapter translates API Gateway events into requests.
	if awsutil.IsAwsLambda() {
		log.Printf("Lambda listener loaded")
		lambda.Start(httpadapter.New(handler).ProxyWithContext)
		return
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	log.Printf("Server starting on port %s", port)
	if err := http.ListenAndServe(":"+port, handler); err != nil {
		log.Fatal(err)
	}
}

// newHandler wires the API routes, the auth middleware and the embedded
// frontend into a single http.Handler.
func newHandler(s3Storage *storage.S3Storage, metaStore metadata.MetadataStore) http.Handler {
	mux := http.NewServeMux()

	// API routes (authenticated)
	mux.Handle("GET /api/emails", requireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handleListEmails(w, r, s3Storage, metaStore)
	})))
	mux.Handle("GET /api/emails/{id}", requireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handleGetEmail(w, r, s3Storage, metaStore)
	})))
	mux.Handle("POST /api/emails/{id}/read", requireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handleMarkRead(w, r, metaStore)
	})))
	mux.Handle("POST /api/emails/{id}/flag", requireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handleMarkFlagged(w, r, metaStore)
	})))
	mux.Handle("POST /api/emails/send", requireAuth(http.HandlerFunc(handleSendEmail)))
	mux.Handle("POST /api/auth/logout", requireAuth(http.HandlerFunc(handleLogout)))

	// Auth routes (no middleware)
	mux.HandleFunc("POST /api/auth/login", handleLogin)
	mux.HandleFunc("GET /api/auth/check", handleAuthCheck)

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
func bearerSubject(r *http.Request) (string, bool) {
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

func requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := bearerSubject(r); !ok {
			w.Header().Set("WWW-Authenticate", `Bearer realm="emissary"`)
			writeError(w, http.StatusUnauthorized, "authentication required")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func handleLogin(w http.ResponseWriter, r *http.Request) {
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

func handleAuthCheck(w http.ResponseWriter, r *http.Request) {
	if subject, ok := bearerSubject(r); ok {
		writeJSON(w, http.StatusOK, map[string]any{"authenticated": true, "username": subject})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"authenticated": false})
}

func handleListEmails(w http.ResponseWriter, r *http.Request, s3Storage *storage.S3Storage, metaStore metadata.MetadataStore) {
	ctx := r.Context()
	emails, err := s3Storage.ListEmails(ctx)
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
		response = append(response, EmailResponse{
			ID:        email.ID,
			From:      email.From,
			To:        email.To,
			Subject:   email.Subject,
			Body:      email.Body,
			Timestamp: email.Timestamp,
			Read:      meta.Read,
			Flagged:   meta.Flagged,
		})
	}

	// Sort by timestamp descending (newest first)
	// For simplicity, we'll just return as-is
	writeJSON(w, http.StatusOK, response)
}

func handleGetEmail(w http.ResponseWriter, r *http.Request, s3Storage *storage.S3Storage, metaStore metadata.MetadataStore) {
	ctx := r.Context()
	id := r.PathValue("id")

	// Find the email by ID
	emails, err := s3Storage.ListEmails(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list emails")
		return
	}

	var foundEmail *storage.Email
	for i := range emails {
		if emails[i].ID == id {
			foundEmail = &emails[i]
			break
		}
	}

	if foundEmail == nil {
		writeError(w, http.StatusNotFound, "email not found")
		return
	}

	meta, _ := metaStore.Get(ctx, id)
	writeJSON(w, http.StatusOK, EmailResponse{
		ID:        foundEmail.ID,
		From:      foundEmail.From,
		To:        foundEmail.To,
		Subject:   foundEmail.Subject,
		Body:      foundEmail.Body,
		Timestamp: foundEmail.Timestamp,
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
