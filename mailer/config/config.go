package config

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/kncept/Emissary/mailer/envutil"
)

// ServerConfig holds HTTP server and authentication configuration.
type ServerConfig struct {
	ListenAddr string

	Username     string
	Password     string
	JWTSecret    string
	JWTGenerated bool
	JWTExpiry    time.Duration
	JWTAudience  string
	JWTIssuer    string
}

// Validate checks that the server configuration is complete and consistent.
// A blank password is valid: it disables the built-in administrator login
// without preventing other user managers from working.
func (c *ServerConfig) Validate() error {
	var errs []error
	if c.Username == "" {
		errs = append(errs, errors.New("username is not configured (set USERNAME)"))
	}
	return errors.Join(errs...)
}

// StorageConfig holds email storage configuration.
type StorageConfig struct {
	Type     string // "s3" or "memory"
	S3Bucket string
}

// Validate checks that the storage configuration is complete and consistent.
func (c *StorageConfig) Validate() error {
	var errs []error
	switch c.Type {
	case "s3", "memory":
	default:
		errs = append(errs, fmt.Errorf("unknown storage type %q (want s3 or memory)", c.Type))
	}
	if c.Type == "s3" && c.S3Bucket == "" {
		errs = append(errs, errors.New("storage=s3 requires a bucket (set S3_BUCKET)"))
	}
	return errors.Join(errs...)
}

// SenderConfig holds outbound email sender configuration. The SES region is
// resolved from the AWS SDK configuration, not from the environment.
type SenderConfig struct {
	Type string // "aws-ses" or "log"
	From string // SES from address
}

// Validate checks that the sender configuration is complete and consistent.
func (c *SenderConfig) Validate() error {
	switch c.Type {
	case "aws-ses", "ses", "log":
		return nil
	}
	return fmt.Errorf("unknown sender type %q (want aws-ses or log)", c.Type)
}

// MetadataConfig holds email metadata storage configuration.
type MetadataConfig struct {
	Type     string // "memory", "file", "ddb", "dynamodb"
	DdbTable string
	FilePath string
	Region   string // AWS region for DDB
}

// SessionConfig holds session storage configuration for refresh tokens.
type SessionConfig struct {
	Type     string // "memory", "ddb", "dynamodb"
	DdbTable string
	Region   string // AWS region for DDB
}

// Validate checks that the metadata configuration is complete and consistent.
func (c *MetadataConfig) Validate() error {
	var errs []error
	switch c.Type {
	case "memory", "file", "ddb", "dynamodb":
	default:
		errs = append(errs, fmt.Errorf("unknown metadata type %q (want memory, file or ddb)", c.Type))
	}
	return errors.Join(errs...)
}

// Validate checks that the session configuration is complete and consistent.
func (c *SessionConfig) Validate() error {
	var errs []error
	switch c.Type {
	case "memory", "ddb", "dynamodb":
	default:
		errs = append(errs, fmt.Errorf("unknown session type %q (want memory, ddb or dynamodb)", c.Type))
	}
	return errors.Join(errs...)
}

// UserManagerConfig selects the authentication backend for ordinary users.
// The USERNAME/PASSWORD administrator is configured separately and
// is available with every backend.
type UserManagerConfig struct {
	Type   string // "none", "ddb", "ldap" or "s3"
	Domain string // receiving domain for mail delivery and reporting
	Region string // AWS region for the "ddb" and "s3" backends

	DdbTable string

	LDAPURL          string
	LDAPBindDN       string
	LDAPBindPassword string
	LDAPBaseDN       string
	LDAPUserFilter   string
	LDAPCacheTTL     string
}

// Validate checks that the user manager configuration is complete.
func (c *UserManagerConfig) Validate() error {
	var errs []error
	switch c.Type {
	case "", "none", "ddb", "dynamodb", "ldap", "s3":
	default:
		errs = append(errs, fmt.Errorf("unknown user manager type %q (want none, ddb, ldap or s3)", c.Type))
	}
	if c.Type == "ldap" && c.LDAPURL == "" {
		errs = append(errs, errors.New("user manager ldap requires a server URL (set LDAP_URL)"))
	}
	return errors.Join(errs...)
}

// Config is the complete runtime configuration for cmd/frontend.
type Config struct {
	Server      ServerConfig
	Storage     StorageConfig
	Sender      SenderConfig
	Metadata    MetadataConfig
	Session     SessionConfig
	UserManager UserManagerConfig

	// AWSRegion is the default AWS region used when a specific service
	// region is not configured.
	AWSRegion string

	// DDBTablePrefix is prepended to logical DynamoDB table names when
	// building table names (see TABLE_PREFIX). It defaults to "emissary".
	DDBTablePrefix string
}

// Validate checks the entire configuration for consistency. It returns a
// composite error containing all validation failures from the sub-configs.
func (c *Config) Validate() error {
	errs := []error{
		c.Server.Validate(),
		c.Storage.Validate(),
		c.Sender.Validate(),
		c.Metadata.Validate(),
		c.Session.Validate(),
		c.UserManager.Validate(),
	}
	// The s3 user manager stores users in the mail storage bucket. The
	// storage validation already enforces S3_BUCKET when STORAGE=s3 (which
	// also covers the auto-selected backend); this catches an explicit
	// USER_MANAGER=s3 with no bucket at all.
	if c.UserManager.Type == "s3" && c.Storage.S3Bucket == "" {
		errs = append(errs, errors.New("user manager s3 requires a bucket (set S3_BUCKET)"))
	}
	return errors.Join(errs...)
}

// ApplyDDBTablePrefix names tables the same way as the household project
// (TABLE_PREFIX): prefix joined with a separator and the logical table name.
// If prefix is empty, "emissary-" is used. If prefix does not already end with
// "-" or "_", "-" is inserted.
func ApplyDDBTablePrefix(prefix, name string) string {
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
	return ApplyDDBTablePrefix(prefix, logical)
}

// MetadataTableName returns the DynamoDB table name for message metadata.
func (c *Config) MetadataTableName() string {
	return resolveDDBTable(c.Metadata.DdbTable, c.DDBTablePrefix, "metadata")
}

// SessionTableName returns the DynamoDB table name for sessions.
func (c *Config) SessionTableName() string {
	return resolveDDBTable(c.Session.DdbTable, c.DDBTablePrefix, "sessions")
}

// UserTableName returns the DynamoDB table name for users.
func (c *Config) UserTableName() string {
	return resolveDDBTable(c.UserManager.DdbTable, c.DDBTablePrefix, "users")
}

// Load reads configuration from the environment. The account credentials are
// required; every other value has a local-development friendly default.
// Pass a context for secret resolution (e.g., from AWS Secrets Manager).
func Load(ctx context.Context) (*Config, error) {
	awsRegion := firstEnv(ctx, "AWS_REGION", "AWS_DEFAULT_REGION")

	cfg := &Config{
		Server: ServerConfig{
			ListenAddr:  listenAddr(ctx),
			Username:    firstEnv(ctx, "USERNAME"),
			Password:    firstEnv(ctx, "PASSWORD"),
			JWTSecret:   firstEnv(ctx, "JWT_SECRET"),
			JWTAudience: firstEnv(ctx, "JWT_AUDIENCE"),
			JWTIssuer:   firstEnv(ctx, "JWT_ISSUER"),
		},
		Storage: StorageConfig{
			Type:     strings.ToLower(strings.TrimSpace(firstEnv(ctx, "STORAGE"))),
			S3Bucket: firstEnv(ctx, "S3_BUCKET"),
		},
		Sender: SenderConfig{
			Type: strings.ToLower(strings.TrimSpace(firstEnv(ctx, "SENDER"))),
			From: firstEnv(ctx, "SES_FROM"),
		},
		Metadata: MetadataConfig{
			Type:     strings.ToLower(strings.TrimSpace(firstEnv(ctx, "METADATA"))),
			DdbTable: firstEnv(ctx, "DDB_TABLE"),
			FilePath: firstEnv(ctx, "METADATA_FILE"),
			Region:   firstEnv(ctx, "METADATA_REGION"),
		},
		Session: SessionConfig{
			Type:     strings.ToLower(strings.TrimSpace(firstEnv(ctx, "SESSION"))),
			DdbTable: firstEnv(ctx, "SESSION_TABLE"),
			Region:   firstEnv(ctx, "SESSION_REGION"),
		},
		UserManager: UserManagerConfig{
			Type:             strings.ToLower(strings.TrimSpace(firstEnv(ctx, "USER_MANAGER"))),
			Domain:           strings.ToLower(strings.TrimSpace(firstEnv(ctx, "DOMAIN"))),
			DdbTable:         firstEnv(ctx, "USER_TABLE"),
			Region:           firstEnv(ctx, "USER_REGION"),
			LDAPURL:          firstEnv(ctx, "LDAP_URL"),
			LDAPBindDN:       firstEnv(ctx, "LDAP_BIND_DN"),
			LDAPBindPassword: firstEnv(ctx, "LDAP_BIND_PASSWORD"),
			LDAPBaseDN:       firstEnv(ctx, "LDAP_BASE_DN"),
			LDAPUserFilter:   firstEnv(ctx, "LDAP_USER_FILTER"),
			LDAPCacheTTL:     firstEnv(ctx, "LDAP_CACHE_TTL"),
		},
		AWSRegion:      awsRegion,
		DDBTablePrefix: strings.TrimSpace(firstEnvDefault(ctx, "emissary", "TABLE_PREFIX", "DDB_TABLE_PREFIX")),
	}

	ttl, err := time.ParseDuration(firstEnvDefault(ctx, "24h", "JWT_TTL"))
	if err != nil {
		return nil, fmt.Errorf("invalid JWT_TTL: %w", err)
	}
	cfg.Server.JWTExpiry = ttl

	if cfg.Server.JWTSecret == "" {
		secret := make([]byte, 32)
		if _, err := rand.Read(secret); err != nil {
			return nil, fmt.Errorf("generate JWT secret: %w", err)
		}
		cfg.Server.JWTSecret = base64.RawURLEncoding.EncodeToString(secret)
		cfg.Server.JWTGenerated = true
	}

	// Storage defaults
	if cfg.Storage.Type == "" {
		if cfg.Storage.S3Bucket != "" {
			cfg.Storage.Type = "s3"
		} else {
			cfg.Storage.Type = "memory"
		}
	}

	// Sender defaults
	if cfg.Sender.Type == "" {
		if cfg.Sender.From != "" {
			cfg.Sender.Type = "aws-ses"
		} else {
			cfg.Sender.Type = "log"
		}
	}

	// Metadata defaults
	if cfg.Metadata.Type == "" {
		cfg.Metadata.Type = "memory"
	}
	if cfg.Metadata.Type == "file" && cfg.Metadata.FilePath == "" {
		cfg.Metadata.FilePath = "emissary-metadata.json"
	}
	if cfg.Metadata.Region == "" {
		cfg.Metadata.Region = cfg.AWSRegion
	}

	// User manager defaults. When no backend is selected, follow the
	// storage backend: S3 mail storage implies the S3 user manager, so a
	// bucket alone is enough to run with Emissary-mastered users.
	if cfg.UserManager.Type == "" {
		if cfg.Storage.Type == "s3" {
			cfg.UserManager.Type = "s3"
		} else {
			cfg.UserManager.Type = "none"
		}
	}
	if cfg.UserManager.Region == "" {
		cfg.UserManager.Region = cfg.AWSRegion
	}

	// Session defaults
	if cfg.Session.Type == "" {
		cfg.Session.Type = "memory"
	}
	if cfg.Session.Region == "" {
		cfg.Session.Region = cfg.AWSRegion
	}

	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	return cfg, nil
}

func listenAddr(ctx context.Context) string {
	if addr := firstEnv(ctx, "LISTEN_ADDR"); addr != "" {
		return addr
	}
	port := firstEnvDefault(ctx, "8080", "PORT")
	if strings.HasPrefix(port, ":") {
		return port
	}
	return ":" + port
}

func firstEnv(ctx context.Context, names ...string) string {
	for _, name := range names {
		if value := envutil.GetCachedEnv(ctx, name); value != "" {
			return value
		}
	}
	return ""
}

func firstEnvDefault(ctx context.Context, fallback string, names ...string) string {
	if value := firstEnv(ctx, names...); value != "" {
		return value
	}
	return fallback
}
