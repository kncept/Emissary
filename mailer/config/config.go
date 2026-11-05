package config

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

// Config is the complete runtime configuration for cmd/frontend.
type Config struct {
	ListenAddr string

	Username     string
	Password     string
	JWTSecret    string
	JWTGenerated bool
	JWTExpiry    time.Duration

	Storage  string
	Sender   string
	Metadata string

	AWSRegion    string
	S3Bucket     string
	S3Prefix     string
	S3Endpoint   string
	SesFrom      string
	DdbTable     string
	MetadataFile string
}

// Load reads configuration from the environment. The account credentials are
// required; every other value has a local-development friendly default.
func Load() (*Config, error) {
	cfg := &Config{
		ListenAddr: listenAddr(),

		Username:  firstEnv("EMISSARY_USERNAME", "AUTH_USERNAME", "USERNAME"),
		Password:  firstEnv("EMISSARY_PASSWORD", "AUTH_PASSWORD", "PASSWORD"),
		JWTSecret: firstEnv("EMISSARY_JWT_SECRET", "JWT_SECRET"),

		Storage:  strings.ToLower(strings.TrimSpace(os.Getenv("EMISSARY_STORAGE"))),
		Sender:   strings.ToLower(strings.TrimSpace(os.Getenv("EMISSARY_SENDER"))),
		Metadata: strings.ToLower(strings.TrimSpace(os.Getenv("EMISSARY_METADATA"))),

		AWSRegion:    firstEnv("AWS_REGION", "AWS_DEFAULT_REGION"),
		S3Bucket:     firstEnv("EMISSARY_S3_BUCKET", "S3_BUCKET"),
		S3Prefix:     firstEnv("EMISSARY_S3_PREFIX", "S3_PREFIX"),
		S3Endpoint:   firstEnv("EMISSARY_S3_ENDPOINT", "S3_ENDPOINT"),
		SesFrom:      firstEnv("EMISSARY_SES_FROM", "SES_FROM", "SES_FROM_ADDRESS"),
		DdbTable:     firstEnv("EMISSARY_DDB_TABLE", "DDB_TABLE"),
		MetadataFile: firstEnv("EMISSARY_METADATA_FILE", "METADATA_FILE"),
	}

	if cfg.Username == "" {
		return nil, errors.New("username is not configured (set EMISSARY_USERNAME)")
	}
	if cfg.Password == "" {
		return nil, errors.New("password is not configured (set EMISSARY_PASSWORD)")
	}

	ttl, err := time.ParseDuration(firstEnvDefault("24h", "EMISSARY_JWT_TTL", "JWT_TTL"))
	if err != nil {
		return nil, fmt.Errorf("invalid EMISSARY_JWT_TTL: %w", err)
	}
	cfg.JWTExpiry = ttl

	if cfg.JWTSecret == "" {
		secret := make([]byte, 32)
		if _, err := rand.Read(secret); err != nil {
			return nil, fmt.Errorf("generate JWT secret: %w", err)
		}
		cfg.JWTSecret = base64.RawURLEncoding.EncodeToString(secret)
		cfg.JWTGenerated = true
	}

	if cfg.Storage == "" {
		if cfg.S3Bucket != "" {
			cfg.Storage = "s3"
		} else {
			cfg.Storage = "memory"
		}
	}
	if cfg.Sender == "" {
		if cfg.SesFrom != "" {
			cfg.Sender = "aws-ses"
		} else {
			cfg.Sender = "log"
		}
	}
	if cfg.Metadata == "" {
		cfg.Metadata = "memory"
	}
	if cfg.Metadata == "file" && cfg.MetadataFile == "" {
		cfg.MetadataFile = "emissary-metadata.json"
	}

	switch cfg.Storage {
	case "s3", "memory":
	default:
		return nil, fmt.Errorf("unknown EMISSARY_STORAGE %q (want s3 or memory)", cfg.Storage)
	}
	switch cfg.Sender {
	case "aws-ses", "ses", "log":
	default:
		return nil, fmt.Errorf("unknown EMISSARY_SENDER %q (want aws-ses or log)", cfg.Sender)
	}
	switch cfg.Metadata {
	case "memory", "file", "ddb", "dynamodb":
	default:
		return nil, fmt.Errorf("unknown EMISSARY_METADATA %q (want memory, file or ddb)", cfg.Metadata)
	}
	if cfg.Storage == "s3" && cfg.S3Bucket == "" {
		return nil, errors.New("EMISSARY_STORAGE=s3 requires EMISSARY_S3_BUCKET")
	}
	if cfg.Metadata == "ddb" || cfg.Metadata == "dynamodb" {
		if cfg.DdbTable == "" {
			return nil, errors.New("EMISSARY_METADATA=ddb requires EMISSARY_DDB_TABLE")
		}
	}

	return cfg, nil
}

func listenAddr() string {
	if addr := firstEnv("EMISSARY_LISTEN_ADDR", "LISTEN_ADDR"); addr != "" {
		return addr
	}
	port := firstEnvDefault("8080", "EMISSARY_PORT", "PORT")
	if strings.HasPrefix(port, ":") {
		return port
	}
	return ":" + port
}

func firstEnv(names ...string) string {
	for _, name := range names {
		if value, ok := os.LookupEnv(name); ok && value != "" {
			return value
		}
	}
	return ""
}

func firstEnvDefault(fallback string, names ...string) string {
	if value := firstEnv(names...); value != "" {
		return value
	}
	return fallback
}
