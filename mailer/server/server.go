package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"net/mail"
	"path"
	"strings"
	"time"

	"github.com/kncept/Emissary/mailer/auth"
	"github.com/kncept/Emissary/mailer/email"
	"github.com/kncept/Emissary/mailer/metadata"
	"github.com/kncept/Emissary/mailer/sender"
	"github.com/kncept/Emissary/mailer/storage"
)

const maxRequestBody = 1 << 20 // 1 MiB

// Config contains the dependencies served by the HTTP API.
type Config struct {
	Auth     *auth.Authenticator
	Storage  storage.Storage
	Sender   sender.Sender
	Metadata metadata.Metadata
	GUI      fs.FS
	Logger   *log.Logger
}

type handler struct {
	auth     *auth.Authenticator
	storage  storage.Storage
	sender   sender.Sender
	metadata metadata.Metadata
	gui      fs.FS
	logger   *log.Logger
}

// New builds the complete HTTP handler: the JSON API under /api and the
// embedded React application everywhere else.
func New(cfg Config) http.Handler {
	logger := cfg.Logger
	if logger == nil {
		logger = log.Default()
	}

	h := &handler{
		auth:     cfg.Auth,
		storage:  cfg.Storage,
		sender:   cfg.Sender,
		metadata: cfg.Metadata,
		gui:      cfg.GUI,
		logger:   logger,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", h.health)
	mux.HandleFunc("POST /api/login", h.login)
	mux.Handle("GET /api/emails", h.requireAuth(http.HandlerFunc(h.listEmails)))
	mux.Handle("GET /api/emails/{id}", h.requireAuth(http.HandlerFunc(h.getEmail)))
	mux.Handle("POST /api/emails/{id}/read", h.requireAuth(http.HandlerFunc(h.markRead)))
	mux.Handle("POST /api/emails/{id}/unread", h.requireAuth(http.HandlerFunc(h.markUnread)))
	mux.Handle("POST /api/send", h.requireAuth(http.HandlerFunc(h.sendEmail)))
	mux.Handle("/", h.staticHandler())

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "same-origin")
		defer func() {
			if recovered := recover(); recovered != nil {
				h.logger.Printf("panic serving %s %s: %v", r.Method, r.URL.Path, recovered)
				writeError(w, http.StatusInternalServerError, "internal server error")
			}
		}()
		mux.ServeHTTP(w, r)
	})
}

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type loginResponse struct {
	Token     string    `json:"token"`
	TokenType string    `json:"tokenType"`
	ExpiresAt time.Time `json:"expiresAt"`
}

func (h *handler) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{
		"status":   "ok",
		"storage":  h.storage.Name(),
		"sender":   h.sender.Name(),
		"metadata": h.metadata.Name(),
	})
}

func (h *handler) login(w http.ResponseWriter, r *http.Request) {
	var request loginRequest
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if !h.auth.Authenticate(request.Username, request.Password) {
		writeError(w, http.StatusUnauthorized, auth.ErrInvalidCredentials.Error())
		return
	}

	token, expiresAt, err := h.auth.Issue(request.Username)
	if err != nil {
		h.logger.Printf("issue token: %v", err)
		writeError(w, http.StatusInternalServerError, "could not issue token")
		return
	}

	writeJSON(w, http.StatusOK, loginResponse{
		Token:     token,
		TokenType: "Bearer",
		ExpiresAt: expiresAt,
	})
}

func (h *handler) listEmails(w http.ResponseWriter, r *http.Request) {
	messages, err := h.storage.ListAllEmails(r.Context())
	if err != nil {
		h.logger.Printf("list emails: %v", err)
		writeError(w, http.StatusBadGateway, "could not list emails")
		return
	}

	for _, message := range messages {
		read, err := h.metadata.IsRead(r.Context(), message.ID)
		if err != nil {
			h.logger.Printf("read metadata for %q: %v", message.ID, err)
			continue
		}
		message.Read = read
	}

	writeJSON(w, http.StatusOK, map[string]any{"emails": messages})
}

func (h *handler) getEmail(w http.ResponseWriter, r *http.Request) {
	message, err := h.storage.GetEmail(r.Context(), r.PathValue("id"))
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			writeError(w, http.StatusNotFound, "email not found")
			return
		}
		h.logger.Printf("get email: %v", err)
		writeError(w, http.StatusBadGateway, "could not load email")
		return
	}

	if err := h.metadata.MarkRead(r.Context(), message.ID); err != nil {
		h.logger.Printf("mark email read: %v", err)
	} else {
		message.Read = true
	}
	writeJSON(w, http.StatusOK, message)
}

func (h *handler) markRead(w http.ResponseWriter, r *http.Request) {
	if err := h.metadata.MarkRead(r.Context(), r.PathValue("id")); err != nil {
		h.logger.Printf("mark email read: %v", err)
		writeError(w, http.StatusBadGateway, "could not update email")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (h *handler) markUnread(w http.ResponseWriter, r *http.Request) {
	if err := h.metadata.MarkUnread(r.Context(), r.PathValue("id")); err != nil {
		h.logger.Printf("mark email unread: %v", err)
		writeError(w, http.StatusBadGateway, "could not update email")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

type sendRequest struct {
	To      stringList `json:"to"`
	Cc      stringList `json:"cc"`
	Bcc     stringList `json:"bcc"`
	Subject string     `json:"subject"`
	Text    string     `json:"text"`
	HTML    string     `json:"html"`
}

func (h *handler) sendEmail(w http.ResponseWriter, r *http.Request) {
	var request sendRequest
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	to, err := parseAddresses(request.To)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	cc, err := parseAddresses(request.Cc)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	bcc, err := parseAddresses(request.Bcc)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if len(to) == 0 {
		writeError(w, http.StatusBadRequest, "at least one recipient is required")
		return
	}

	message := &email.UserEmail{
		From:    email.Address{Address: h.sender.From()},
		To:      to,
		Cc:      cc,
		Bcc:     bcc,
		Subject: strings.TrimSpace(request.Subject),
		Date:    time.Now().UTC(),
		Text:    request.Text,
		HTML:    request.HTML,
	}

	if err := h.sender.Send(r.Context(), message); err != nil {
		h.logger.Printf("send email: %v", err)
		writeError(w, http.StatusBadGateway, "could not send email")
		return
	}

	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (h *handler) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		const prefix = "Bearer "
		header := r.Header.Get("Authorization")
		if !strings.HasPrefix(header, prefix) {
			unauthorized(w)
			return
		}
		token := strings.TrimSpace(strings.TrimPrefix(header, prefix))
		if _, err := h.auth.Validate(token); err != nil {
			unauthorized(w)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (h *handler) staticHandler() http.Handler {
	fileServer := http.FileServer(http.FS(h.gui))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api" || strings.HasPrefix(r.URL.Path, "/api/") {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		if h.gui == nil {
			http.Error(w, "frontend not built", http.StatusServiceUnavailable)
			return
		}

		name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		if name == "" {
			name = "index.html"
		}
		if strings.HasSuffix(name, ".go") || strings.HasPrefix(name, "assets/") && !fileExists(h.gui, name) {
			http.NotFound(w, r)
			return
		}

		if info, err := fs.Stat(h.gui, name); err == nil && !info.IsDir() {
			if strings.HasPrefix(r.URL.Path, "/assets/") {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			}
			fileServer.ServeHTTP(w, r)
			return
		}

		// Client-side routes all resolve to the single-page application.
		index, err := fs.ReadFile(h.gui, "index.html")
		if err != nil {
			http.Error(w, "frontend not built", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)
		if r.Method != http.MethodHead {
			_, _ = w.Write(index)
		}
	})
}

func fileExists(fsys fs.FS, name string) bool {
	info, err := fs.Stat(fsys, name)
	return err == nil && !info.IsDir()
}

type stringList []string

func (s *stringList) UnmarshalJSON(data []byte) error {
	if len(data) == 0 || string(data) == "null" {
		*s = nil
		return nil
	}
	var list []string
	if err := json.Unmarshal(data, &list); err == nil {
		*s = list
		return nil
	}
	var single string
	if err := json.Unmarshal(data, &single); err == nil {
		*s = []string{single}
		return nil
	}
	return errors.New("expected a string or an array of strings")
}

func parseAddresses(values []string) ([]email.Address, error) {
	addresses := make([]email.Address, 0, len(values))
	for _, value := range values {
		for _, part := range strings.Split(value, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			parsed, err := mail.ParseAddress(part)
			if err != nil {
				return nil, fmt.Errorf("invalid email address %q", part)
			}
			addresses = append(addresses, email.Address{Name: parsed.Name, Address: parsed.Address})
		}
	}
	return addresses, nil
}

func unauthorized(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", `Bearer realm="emissary"`)
	writeError(w, http.StatusUnauthorized, "authentication required")
}

func decodeJSON(w http.ResponseWriter, r *http.Request, destination any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBody)
	decoder := json.NewDecoder(r.Body)
	return decoder.Decode(destination)
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
