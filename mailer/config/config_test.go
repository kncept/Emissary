package config

import (
	"os"
	"testing"
	"time"
)

func TestLoad_Defaults(t *testing.T) {
	os.Setenv("EMISSARY_USERNAME", "testuser")
	os.Setenv("EMISSARY_PASSWORD", "testpass")
	defer func() {
		os.Unsetenv("EMISSARY_USERNAME")
		os.Unsetenv("EMISSARY_PASSWORD")
		os.Unsetenv("EMISSARY_JWT_SECRET")
		os.Unsetenv("EMISSARY_STORAGE")
		os.Unsetenv("EMISSARY_SENDER")
		os.Unsetenv("EMISSARY_METADATA")
		os.Unsetenv("EMISSARY_JWT_TTL")
		os.Unsetenv("EMISSARY_PORT")
	}()

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.Username != "testuser" {
		t.Errorf("Username = %q, want %q", cfg.Username, "testuser")
	}
	if cfg.Password != "testpass" {
		t.Errorf("Password = %q, want %q", cfg.Password, "testpass")
	}
	if cfg.JWTSecret == "" {
		t.Error("JWTSecret should be generated")
	}
	if !cfg.JWTGenerated {
		t.Error("JWTGenerated should be true when secret is auto-generated")
	}
	if cfg.JWTExpiry != 24*time.Hour {
		t.Errorf("JWTExpiry = %v, want %v", cfg.JWTExpiry, 24*time.Hour)
	}
	if cfg.Storage != "memory" {
		t.Errorf("Storage = %q, want %q", cfg.Storage, "memory")
	}
	if cfg.Sender != "log" {
		t.Errorf("Sender = %q, want %q", cfg.Sender, "log")
	}
	if cfg.Metadata != "memory" {
		t.Errorf("Metadata = %q, want %q", cfg.Metadata, "memory")
	}
	if cfg.ListenAddr != ":8080" {
		t.Errorf("ListenAddr = %q, want %q", cfg.ListenAddr, ":8080")
	}
}

func TestLoad_CustomValues(t *testing.T) {
	os.Setenv("EMISSARY_USERNAME", "custom")
	os.Setenv("EMISSARY_PASSWORD", "secret")
	os.Setenv("EMISSARY_JWT_SECRET", "mysecret")
	os.Setenv("EMISSARY_STORAGE", "s3")
	os.Setenv("EMISSARY_SENDER", "aws-ses")
	os.Setenv("EMISSARY_METADATA", "file")
	os.Setenv("EMISSARY_JWT_TTL", "12h")
	os.Setenv("EMISSARY_PORT", "9090")
	os.Setenv("EMISSARY_S3_BUCKET", "my-bucket")
	os.Setenv("EMISSARY_SES_FROM", "sender@example.com")
	os.Setenv("EMISSARY_METADATA_FILE", "/tmp/meta.json")
	defer cleanupEnv()

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.Username != "custom" {
		t.Errorf("Username = %q", cfg.Username)
	}
	if cfg.JWTSecret != "mysecret" {
		t.Errorf("JWTSecret = %q", cfg.JWTSecret)
	}
	if cfg.JWTGenerated {
		t.Error("JWTGenerated should be false when secret is provided")
	}
	if cfg.JWTExpiry != 12*time.Hour {
		t.Errorf("JWTExpiry = %v, want %v", cfg.JWTExpiry, 12*time.Hour)
	}
	if cfg.Storage != "s3" {
		t.Errorf("Storage = %q", cfg.Storage)
	}
	if cfg.Sender != "aws-ses" {
		t.Errorf("Sender = %q", cfg.Sender)
	}
	if cfg.Metadata != "file" {
		t.Errorf("Metadata = %q", cfg.Metadata)
	}
	if cfg.ListenAddr != ":9090" {
		t.Errorf("ListenAddr = %q", cfg.ListenAddr)
	}
	if cfg.S3Bucket != "my-bucket" {
		t.Errorf("S3Bucket = %q", cfg.S3Bucket)
	}
	if cfg.SesFrom != "sender@example.com" {
		t.Errorf("SesFrom = %q", cfg.SesFrom)
	}
	if cfg.MetadataFile != "/tmp/meta.json" {
		t.Errorf("MetadataFile = %q", cfg.MetadataFile)
	}
}

func TestLoad_MissingUsername(t *testing.T) {
	os.Unsetenv("EMISSARY_USERNAME")
	os.Setenv("EMISSARY_PASSWORD", "pass")
	defer cleanupEnv()

	_, err := Load()
	if err == nil {
		t.Error("Load() should error when username is missing")
	}
}

func TestLoad_MissingPassword(t *testing.T) {
	os.Setenv("EMISSARY_USERNAME", "user")
	os.Unsetenv("EMISSARY_PASSWORD")
	defer cleanupEnv()

	_, err := Load()
	if err == nil {
		t.Error("Load() should error when password is missing")
	}
}

func TestLoad_InvalidJWT_TTL(t *testing.T) {
	os.Setenv("EMISSARY_USERNAME", "user")
	os.Setenv("EMISSARY_PASSWORD", "pass")
	os.Setenv("EMISSARY_JWT_TTL", "invalid")
	defer cleanupEnv()

	_, err := Load()
	if err == nil {
		t.Error("Load() should error on invalid JWT TTL")
	}
}

func TestLoad_UnknownStorage(t *testing.T) {
	os.Setenv("EMISSARY_USERNAME", "user")
	os.Setenv("EMISSARY_PASSWORD", "pass")
	os.Setenv("EMISSARY_STORAGE", "unknown")
	defer cleanupEnv()

	_, err := Load()
	if err == nil {
		t.Error("Load() should error on unknown storage")
	}
}

func TestLoad_UnknownSender(t *testing.T) {
	os.Setenv("EMISSARY_USERNAME", "user")
	os.Setenv("EMISSARY_PASSWORD", "pass")
	os.Setenv("EMISSARY_SENDER", "unknown")
	defer cleanupEnv()

	_, err := Load()
	if err == nil {
		t.Error("Load() should error on unknown sender")
	}
}

func TestLoad_UnknownMetadata(t *testing.T) {
	os.Setenv("EMISSARY_USERNAME", "user")
	os.Setenv("EMISSARY_PASSWORD", "pass")
	os.Setenv("EMISSARY_METADATA", "unknown")
	defer cleanupEnv()

	_, err := Load()
	if err == nil {
		t.Error("Load() should error on unknown metadata")
	}
}

func TestLoad_S3StorageRequiresBucket(t *testing.T) {
	os.Setenv("EMISSARY_USERNAME", "user")
	os.Setenv("EMISSARY_PASSWORD", "pass")
	os.Setenv("EMISSARY_STORAGE", "s3")
	defer cleanupEnv()

	_, err := Load()
	if err == nil {
		t.Error("Load() should error when s3 storage is selected without bucket")
	}
}

func TestLoad_DDBMetadataRequiresTable(t *testing.T) {
	os.Setenv("EMISSARY_USERNAME", "user")
	os.Setenv("EMISSARY_PASSWORD", "pass")
	os.Setenv("EMISSARY_METADATA", "ddb")
	defer cleanupEnv()

	_, err := Load()
	if err == nil {
		t.Error("Load() should error when ddb metadata is selected without table")
	}
}

func TestLoad_DynamoDBAlias(t *testing.T) {
	os.Setenv("EMISSARY_USERNAME", "user")
	os.Setenv("EMISSARY_PASSWORD", "pass")
	os.Setenv("EMISSARY_METADATA", "dynamodb")
	defer cleanupEnv()

	_, err := Load()
	if err == nil {
		t.Error("Load() should error when dynamodb metadata is selected without table")
	}
}

func TestLoad_FileMetadataDefaultPath(t *testing.T) {
	os.Setenv("EMISSARY_USERNAME", "user")
	os.Setenv("EMISSARY_PASSWORD", "pass")
	os.Setenv("EMISSARY_METADATA", "file")
	defer cleanupEnv()

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.MetadataFile != "emissary-metadata.json" {
		t.Errorf("MetadataFile = %q, want %q", cfg.MetadataFile, "emissary-metadata.json")
	}
}

func TestLoad_ListenAddrWithPort(t *testing.T) {
	os.Setenv("EMISSARY_USERNAME", "user")
	os.Setenv("EMISSARY_PASSWORD", "pass")
	os.Setenv("EMISSARY_PORT", "3000")
	defer cleanupEnv()

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.ListenAddr != ":3000" {
		t.Errorf("ListenAddr = %q, want %q", cfg.ListenAddr, ":3000")
	}
}

func TestLoad_ListenAddrWithFullAddress(t *testing.T) {
	os.Setenv("EMISSARY_USERNAME", "user")
	os.Setenv("EMISSARY_PASSWORD", "pass")
	os.Setenv("EMISSARY_LISTEN_ADDR", "127.0.0.1:4000")
	defer cleanupEnv()

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.ListenAddr != "127.0.0.1:4000" {
		t.Errorf("ListenAddr = %q, want %q", cfg.ListenAddr, "127.0.0.1:4000")
	}
}

func cleanupEnv() {
	os.Unsetenv("EMISSARY_USERNAME")
	os.Unsetenv("EMISSARY_PASSWORD")
	os.Unsetenv("EMISSARY_JWT_SECRET")
	os.Unsetenv("EMISSARY_STORAGE")
	os.Unsetenv("EMISSARY_SENDER")
	os.Unsetenv("EMISSARY_METADATA")
	os.Unsetenv("EMISSARY_JWT_TTL")
	os.Unsetenv("EMISSARY_PORT")
	os.Unsetenv("EMISSARY_S3_BUCKET")
	os.Unsetenv("EMISSARY_SES_FROM")
	os.Unsetenv("EMISSARY_METADATA_FILE")
	os.Unsetenv("EMISSARY_LISTEN_ADDR")
	os.Unsetenv("EMISSARY_S3_PREFIX")
	os.Unsetenv("EMISSARY_S3_ENDPOINT")
	os.Unsetenv("EMISSARY_DDB_TABLE")
	os.Unsetenv("AWS_REGION")
}