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
		errs = append(errs, errors.New("username is not configured (set EMISSARY_USERNAME)"))
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
		errs = append(errs, errors.New("storage=s3 requires a bucket (set EMISSARY_S3_BUCKET)"))
	}
	return errors.Join(errs...)
}

// SenderConfig holds outbound email sender configuration.
type SenderConfig struct {
	Type   string // "aws-ses" or "log"
	From   string // SES from address
	Region string // AWS region for SES
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

// Validate checks that the metadata configuration is complete and consistent.
func (c *MetadataConfig) Validate() error {
	var errs []error
	switch c.Type {
	case "memory", "file", "ddb", "dynamodb":
	default:
		errs = append(errs, fmt.Errorf("unknown metadata type %q (want memory, file or ddb)", c.Type))
	}
	if (c.Type == "ddb" || c.Type == "dynamodb") && c.DdbTable == "" {
		errs = append(errs, errors.New("metadata=ddb requires a table (set EMISSARY_DDB_TABLE)"))
	}
	return errors.Join(errs...)
}

// UserManagerConfig selects the authentication backend for ordinary users.
// The AUTH_USERNAME/AUTH_PASSWORD administrator is configured separately and
// is available with every backend.
type UserManagerConfig struct {
	Type   string // "none", "any", "ddb" or "ldap"
	Domain string // receiving domain for the "any" backend
	Region string // AWS region for the "ddb" backend

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
	case "", "none", "any", "ddb", "dynamodb", "ldap":
	default:
		errs = append(errs, fmt.Errorf("unknown user manager type %q (want none, any, ddb or ldap)", c.Type))
	}
	if c.Type == "any" && c.Domain == "" {
		errs = append(errs, errors.New("user manager any requires a domain (set EMISSARY_DOMAIN)"))
	}
	if (c.Type == "ddb" || c.Type == "dynamodb") && c.DdbTable == "" {
		errs = append(errs, errors.New("user manager ddb requires a table (set EMISSARY_USER_TABLE)"))
	}
	if c.Type == "ldap" && c.LDAPURL == "" {
		errs = append(errs, errors.New("user manager ldap requires a server URL (set EMISSARY_LDAP_URL)"))
	}
	return errors.Join(errs...)
}

// Config is the complete runtime configuration for cmd/frontend.
type Config struct {
	Server      ServerConfig
	Storage     StorageConfig
	Sender      SenderConfig
	Metadata    MetadataConfig
	UserManager UserManagerConfig

	// AWSRegion is the default AWS region used when a specific service
	// region is not configured.
	AWSRegion string
}

// Validate checks the entire configuration for consistency. It returns a
// composite error containing all validation failures from the sub-configs.
func (c *Config) Validate() error {
	return errors.Join(
		c.Server.Validate(),
		c.Storage.Validate(),
		c.Sender.Validate(),
		c.Metadata.Validate(),
		c.UserManager.Validate(),
	)
}

// Load reads configuration from the environment. The account credentials are
// required; every other value has a local-development friendly default.
// Pass a context for secret resolution (e.g., from AWS Secrets Manager).
func Load(ctx context.Context) (*Config, error) {
	awsRegion := firstEnv(ctx, "AWS_REGION", "AWS_DEFAULT_REGION")

	cfg := &Config{
		Server: ServerConfig{
			ListenAddr:  listenAddr(ctx),
			Username:    firstEnv(ctx, "EMISSARY_USERNAME", "AUTH_USERNAME", "USERNAME"),
			Password:    firstEnv(ctx, "EMISSARY_PASSWORD", "AUTH_PASSWORD", "PASSWORD"),
			JWTSecret:   firstEnv(ctx, "EMISSARY_JWT_SECRET", "JWT_SECRET"),
			JWTAudience: firstEnv(ctx, "EMISSARY_JWT_AUDIENCE", "JWT_AUDIENCE"),
			JWTIssuer:   firstEnv(ctx, "EMISSARY_JWT_ISSUER", "JWT_ISSUER"),
		},
		Storage: StorageConfig{
			Type:     strings.ToLower(strings.TrimSpace(firstEnv(ctx, "EMISSARY_STORAGE"))),
			S3Bucket: firstEnv(ctx, "EMISSARY_S3_BUCKET", "S3_BUCKET"),
		},
		Sender: SenderConfig{
			Type:   strings.ToLower(strings.TrimSpace(firstEnv(ctx, "EMISSARY_SENDER"))),
			From:   firstEnv(ctx, "EMISSARY_SES_FROM", "SES_FROM", "SES_FROM_ADDRESS"),
			Region: firstEnv(ctx, "EMISSARY_SES_REGION", "SES_REGION"),
		},
		Metadata: MetadataConfig{
			Type:     strings.ToLower(strings.TrimSpace(firstEnv(ctx, "EMISSARY_METADATA"))),
			DdbTable: firstEnv(ctx, "EMISSARY_DDB_TABLE", "DDB_TABLE"),
			FilePath: firstEnv(ctx, "EMISSARY_METADATA_FILE", "METADATA_FILE"),
			Region:   firstEnv(ctx, "EMISSARY_METADATA_REGION", "METADATA_REGION"),
		},
		UserManager: UserManagerConfig{
			Type:             strings.ToLower(strings.TrimSpace(firstEnv(ctx, "EMISSARY_USER_MANAGER", "USER_MANAGER"))),
			Domain:           strings.ToLower(strings.TrimSpace(firstEnv(ctx, "EMISSARY_DOMAIN", "EMAIL_DOMAIN"))),
			DdbTable:         firstEnv(ctx, "EMISSARY_USER_TABLE", "USER_TABLE"),
			Region:           firstEnv(ctx, "EMISSARY_USER_REGION", "USER_REGION"),
			LDAPURL:          firstEnv(ctx, "EMISSARY_LDAP_URL", "LDAP_URL"),
			LDAPBindDN:       firstEnv(ctx, "EMISSARY_LDAP_BIND_DN", "LDAP_BIND_DN"),
			LDAPBindPassword: firstEnv(ctx, "EMISSARY_LDAP_BIND_PASSWORD", "LDAP_BIND_PASSWORD"),
			LDAPBaseDN:       firstEnv(ctx, "EMISSARY_LDAP_BASE_DN", "LDAP_BASE_DN"),
			LDAPUserFilter:   firstEnv(ctx, "EMISSARY_LDAP_USER_FILTER", "LDAP_USER_FILTER"),
			LDAPCacheTTL:     firstEnv(ctx, "EMISSARY_LDAP_CACHE_TTL", "LDAP_CACHE_TTL"),
		},
		AWSRegion: awsRegion,
	}

	ttl, err := time.ParseDuration(firstEnvDefault(ctx, "24h", "EMISSARY_JWT_TTL", "JWT_TTL"))
	if err != nil {
		return nil, fmt.Errorf("invalid EMISSARY_JWT_TTL: %w", err)
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
	if cfg.Sender.Region == "" {
		cfg.Sender.Region = cfg.AWSRegion
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

	// User manager defaults
	if cfg.UserManager.Type == "" {
		cfg.UserManager.Type = "none"
	}
	if cfg.UserManager.Region == "" {
		cfg.UserManager.Region = cfg.AWSRegion
	}

	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	return cfg, nil
}

func listenAddr(ctx context.Context) string {
	if addr := firstEnv(ctx, "EMISSARY_LISTEN_ADDR", "LISTEN_ADDR"); addr != "" {
		return addr
	}
	port := firstEnvDefault(ctx, "8080", "EMISSARY_PORT", "PORT")
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
