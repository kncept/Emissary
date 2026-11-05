package main

import (
	"embed"
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/sessions"
	"github.com/kncept/Emissary/mailer/metadata"
	"github.com/kncept/Emissary/mailer/storage"
)

//go:embed all:frontend/dist
var frontendFS embed.FS

var (
	store       *sessions.CookieStore
	authUser    string
	authPass    string
	s3Bucket    string
	s3Prefix    string
	s3Region    string
	metadataType string
	metadataPath string
	metadataTable string
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
	To      string `json:"to" binding:"required"`
	Subject string `json:"subject" binding:"required"`
	Body    string `json:"body" binding:"required"`
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

	// Initialize session store
	secretKey := os.Getenv("SESSION_SECRET")
	if secretKey == "" {
		secretKey = "default-secret-change-in-production"
	}
	store = sessions.NewCookieStore([]byte(secretKey))
	store.Options = &sessions.Options{
		Path:     "/",
		MaxAge:   86400 * 7,
		HttpOnly: true,
		Secure:   false, // Set to true in production with HTTPS
		SameSite: http.SameSiteLaxMode,
	}

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

	// Setup Gin
	gin.SetMode(gin.ReleaseMode)
	r := gin.Default()

	// API routes
	api := r.Group("/api")
	api.Use(authMiddleware())
	{
		api.GET("/emails", func(c *gin.Context) {
			handleListEmails(c, s3Storage, metaStore)
		})
		api.GET("/emails/:id", func(c *gin.Context) {
			handleGetEmail(c, s3Storage, metaStore)
		})
		api.POST("/emails/:id/read", func(c *gin.Context) {
			handleMarkRead(c, metaStore)
		})
		api.POST("/emails/:id/flag", func(c *gin.Context) {
			handleMarkFlagged(c, metaStore)
		})
		api.POST("/emails/send", func(c *gin.Context) {
			handleSendEmail(c)
		})
		api.POST("/auth/logout", handleLogout)
	}

	// Auth routes (no middleware)
	r.POST("/api/auth/login", handleLogin)
	r.GET("/api/auth/check", handleAuthCheck)

	// Serve frontend
	r.NoRoute(func(c *gin.Context) {
		path := c.Request.URL.Path
		if strings.HasPrefix(path, "/api") {
			c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
			return
		}
		serveFrontend(c)
	})

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	log.Printf("Server starting on port %s", port)
	if err := r.Run(":" + port); err != nil {
		log.Fatal(err)
	}
}

func authMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		session, _ := store.Get(c.Request, "session")
		if auth, ok := session.Values["authenticated"].(bool); !ok || !auth {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
			c.Abort()
			return
		}
		c.Next()
	}
}

func handleLogin(c *gin.Context) {
	var req struct {
		Username string `json:"username" binding:"required"`
		Password string `json:"password" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}

	if req.Username == authUser && req.Password == authPass {
		session, _ := store.Get(c.Request, "session")
		session.Values["authenticated"] = true
		session.Values["username"] = req.Username
		if err := session.Save(c.Request, c.Writer); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save session"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"success": true})
		return
	}

	c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid credentials"})
}

func handleLogout(c *gin.Context) {
	session, _ := store.Get(c.Request, "session")
	session.Values["authenticated"] = false
	session.Save(c.Request, c.Writer)
	c.JSON(http.StatusOK, gin.H{"success": true})
}

func handleAuthCheck(c *gin.Context) {
	session, _ := store.Get(c.Request, "session")
	if auth, ok := session.Values["authenticated"].(bool); ok && auth {
		c.JSON(http.StatusOK, gin.H{"authenticated": true, "username": session.Values["username"]})
		return
	}
	c.JSON(http.StatusOK, gin.H{"authenticated": false})
}

func handleListEmails(c *gin.Context, s3Storage *storage.S3Storage, metaStore metadata.MetadataStore) {
	ctx := c.Request.Context()
	emails, err := s3Storage.ListEmails(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list emails"})
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
	c.JSON(http.StatusOK, response)
}

func handleGetEmail(c *gin.Context, s3Storage *storage.S3Storage, metaStore metadata.MetadataStore) {
	ctx := c.Request.Context()
	id := c.Param("id")

	// Find the email by ID
	emails, err := s3Storage.ListEmails(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list emails"})
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
		c.JSON(http.StatusNotFound, gin.H{"error": "email not found"})
		return
	}

	meta, _ := metaStore.Get(ctx, id)
	c.JSON(http.StatusOK, EmailResponse{
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

func handleMarkRead(c *gin.Context, metaStore metadata.MetadataStore) {
	ctx := c.Request.Context()
	id := c.Param("id")

	var req struct {
		Read bool `json:"read"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}

	if err := metaStore.SetRead(ctx, id, req.Read); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update read status"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true})
}

func handleMarkFlagged(c *gin.Context, metaStore metadata.MetadataStore) {
	ctx := c.Request.Context()
	id := c.Param("id")

	var req struct {
		Flagged bool `json:"flagged"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}

	if err := metaStore.SetFlagged(ctx, id, req.Flagged); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update flag status"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true})
}

func handleSendEmail(c *gin.Context) {
	var req SendEmailRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}

	// For now, we'll just log the email (in production, use AWS SES)
	log.Printf("Sending email to: %s, Subject: %s", req.To, req.Subject)
	
	// TODO: Implement actual SES sending
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "Email queued for sending"})
}

func serveFrontend(c *gin.Context) {
	path := c.Request.URL.Path
	if path == "/" || path == "" {
		path = "/index.html"
	}

	file, err := frontendFS.Open("frontend/dist" + path)
	if err != nil {
		// Try index.html for SPA routing
		file, err = frontendFS.Open("frontend/dist/index.html")
		if err != nil {
			c.String(http.StatusNotFound, "Frontend not built. Run 'npm run build' in frontend directory.")
			return
		}
	}
	defer file.Close()

	stat, _ := file.Stat()
	contentType := getContentType(path)
	c.DataFromReader(http.StatusOK, stat.Size(), contentType, file, nil)
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