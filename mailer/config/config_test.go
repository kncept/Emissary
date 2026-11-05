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
		os.Unsetenv("EMISSARY_S3_REGION")
		os.Unsetenv("EMISSARY_SES_REGION")
		os.Unsetenv("EMISSARY_METADATA_REGION")
		os.Unsetenv("AWS_REGION")
	}()

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.Server.Username != "testuser" {
		t.Errorf("Server.Username = %q, want %q", cfg.Server.Username, "testuser")
	}
	if cfg.Server.Password != "testpass" {
		t.Errorf("Server.Password = %q, want %q", cfg.Server.Password, "testpass")
	}
	if cfg.Server.JWTSecret == "" {
		t.Error("Server.JWTSecret should be generated")
	}
	if !cfg.Server.JWTGenerated {
		t.Error("Server.JWTGenerated should be true when secret is auto-generated")
	}
	if cfg.Server.JWTExpiry != 24*time.Hour {
		t.Errorf("Server.JWTExpiry = %v, want %v", cfg.Server.JWTExpiry, 24*time.Hour)
	}
	if cfg.Storage.Type != "memory" {
		t.Errorf("Storage.Type = %q, want %q", cfg.Storage.Type, "memory")
	}
	if cfg.Sender.Type != "log" {
		t.Errorf("Sender.Type = %q, want %q", cfg.Sender.Type, "log")
	}
	if cfg.Metadata.Type != "memory" {
		t.Errorf("Metadata.Type = %q, want %q", cfg.Metadata.Type, "memory")
	}
	if cfg.Server.ListenAddr != ":8080" {
		t.Errorf("Server.ListenAddr = %q, want %q", cfg.Server.ListenAddr, ":8080")
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
	os.Setenv("EMISSARY_S3_PREFIX", "emails/")
	os.Setenv("EMISSARY_S3_ENDPOINT", "http://localhost:9000")
	os.Setenv("EMISSARY_S3_REGION", "us-west-2")
	os.Setenv("EMISSARY_SES_FROM", "sender@example.com")
	os.Setenv("EMISSARY_SES_REGION", "eu-west-1")
	os.Setenv("EMISSARY_METADATA_FILE", "/tmp/meta.json")
	os.Setenv("EMISSARY_METADATA_REGION", "ap-southeast-1")
	os.Setenv("AWS_REGION", "us-east-1")
	defer cleanupEnv()

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.Server.Username != "custom" {
		t.Errorf("Server.Username = %q", cfg.Server.Username)
	}
	if cfg.Server.JWTSecret != "mysecret" {
		t.Errorf("Server.JWTSecret = %q", cfg.Server.JWTSecret)
	}
	if cfg.Server.JWTGenerated {
		t.Error("Server.JWTGenerated should be false when secret is provided")
	}
	if cfg.Server.JWTExpiry != 12*time.Hour {
		t.Errorf("Server.JWTExpiry = %v, want %v", cfg.Server.JWTExpiry, 12*time.Hour)
	}
	if cfg.Storage.Type != "s3" {
		t.Errorf("Storage.Type = %q", cfg.Storage.Type)
	}
	if cfg.Sender.Type != "aws-ses" {
		t.Errorf("Sender.Type = %q", cfg.Sender.Type)
	}
	if cfg.Metadata.Type != "file" {
		t.Errorf("Metadata.Type = %q", cfg.Metadata.Type)
	}
	if cfg.Server.ListenAddr != ":9090" {
		t.Errorf("Server.ListenAddr = %q", cfg.Server.ListenAddr)
	}
	if cfg.Storage.S3Bucket != "my-bucket" {
		t.Errorf("Storage.S3Bucket = %q", cfg.Storage.S3Bucket)
	}
	if cfg.Storage.S3Prefix != "emails/" {
		t.Errorf("Storage.S3Prefix = %q", cfg.Storage.S3Prefix)
	}
	if cfg.Storage.S3Endpoint != "http://localhost:9000" {
		t.Errorf("Storage.S3Endpoint = %q", cfg.Storage.S3Endpoint)
	}
	if cfg.Storage.S3Region != "us-west-2" {
		t.Errorf("Storage.S3Region = %q", cfg.Storage.S3Region)
	}
	if cfg.Sender.From != "sender@example.com" {
		t.Errorf("Sender.From = %q", cfg.Sender.From)
	}
	if cfg.Sender.Region != "eu-west-1" {
		t.Errorf("Sender.Region = %q", cfg.Sender.Region)
	}
	if cfg.Metadata.FilePath != "/tmp/meta.json" {
		t.Errorf("Metadata.FilePath = %q", cfg.Metadata.FilePath)
	}
	if cfg.Metadata.Region != "ap-southeast-1" {
		t.Errorf("Metadata.Region = %q", cfg.Metadata.Region)
	}
	if cfg.AWSRegion != "us-east-1" {
		t.Errorf("AWSRegion = %q", cfg.AWSRegion)
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

	if cfg.Metadata.FilePath != "emissary-metadata.json" {
		t.Errorf("Metadata.FilePath = %q, want %q", cfg.Metadata.FilePath, "emissary-metadata.json")
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

	if cfg.Server.ListenAddr != ":3000" {
		t.Errorf("Server.ListenAddr = %q, want %q", cfg.Server.ListenAddr, ":3000")
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

	if cfg.Server.ListenAddr != "127.0.0.1:4000" {
		t.Errorf("Server.ListenAddr = %q, want %q", cfg.Server.ListenAddr, "127.0.0.1:4000")
	}
}

func TestLoad_S3RegionDefaultsToAWSRegion(t *testing.T) {
	os.Setenv("EMISSARY_USERNAME", "user")
	os.Setenv("EMISSARY_PASSWORD", "pass")
	os.Setenv("EMISSARY_STORAGE", "s3")
	os.Setenv("EMISSARY_S3_BUCKET", "my-bucket")
	os.Setenv("AWS_REGION", "us-east-1")
	defer cleanupEnv()

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.Storage.S3Region != "us-east-1" {
		t.Errorf("Storage.S3Region = %q, want %q", cfg.Storage.S3Region, "us-east-1")
	}
}

func TestLoad_SESRegionDefaultsToAWSRegion(t *testing.T) {
	os.Setenv("EMISSARY_USERNAME", "user")
	os.Setenv("EMISSARY_PASSWORD", "pass")
	os.Setenv("EMISSARY_SENDER", "aws-ses")
	os.Setenv("EMISSARY_SES_FROM", "sender@example.com")
	os.Setenv("AWS_REGION", "eu-central-1")
	defer cleanupEnv()

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.Sender.Region != "eu-central-1" {
		t.Errorf("Sender.Region = %q, want %q", cfg.Sender.Region, "eu-central-1")
	}
}

func TestLoad_MetadataRegionDefaultsToAWSRegion(t *testing.T) {
	os.Setenv("EMISSARY_USERNAME", "user")
	os.Setenv("EMISSARY_PASSWORD", "pass")
	os.Setenv("EMISSARY_METADATA", "ddb")
	os.Setenv("EMISSARY_DDB_TABLE", "my-table")
	os.Setenv("AWS_REGION", "ap-northeast-1")
	defer cleanupEnv()

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.Metadata.Region != "ap-northeast-1" {
		t.Errorf("Metadata.Region = %q, want %q", cfg.Metadata.Region, "ap-northeast-1")
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
	os.Unsetenv("EMISSARY_S3_REGION")
	os.Unsetenv("EMISSARY_SES_REGION")
	os.Unsetenv("EMISSARY_DDB_TABLE")
	os.Unsetenv("EMISSARY_METADATA_REGION")
	os.Unsetenv("AWS_REGION")
}