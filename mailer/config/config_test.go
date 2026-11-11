package config

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/kncept/Emissary/mailer/envutil"
)

func TestLoad_Defaults(t *testing.T) {
	os.Setenv("USERNAME", "testuser")
	os.Setenv("PASSWORD", "testpass")
	defer func() {
		os.Unsetenv("USERNAME")
		os.Unsetenv("PASSWORD")
		os.Unsetenv("JWT_SECRET")
		os.Unsetenv("STORAGE")
		os.Unsetenv("SENDER")
		os.Unsetenv("METADATA")
		os.Unsetenv("JWT_TTL")
		os.Unsetenv("PORT")
		os.Unsetenv("METADATA_REGION")
		os.Unsetenv("AWS_REGION")
		envutil.ClearCache()
	}()

	cfg, err := Load(context.Background())
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
	os.Setenv("USERNAME", "custom")
	os.Setenv("PASSWORD", "secret")
	os.Setenv("JWT_SECRET", "mysecret")
	os.Setenv("STORAGE", "s3")
	os.Setenv("SENDER", "aws-ses")
	os.Setenv("METADATA", "file")
	os.Setenv("JWT_TTL", "12h")
	os.Setenv("PORT", "9090")
	os.Setenv("S3_BUCKET", "my-bucket")
	os.Setenv("SES_FROM", "sender@example.com")
	os.Setenv("METADATA_FILE", "/tmp/meta.json")
	os.Setenv("METADATA_REGION", "ap-southeast-1")
	os.Setenv("AWS_REGION", "us-east-1")
	defer cleanupEnv()

	cfg, err := Load(context.Background())
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
	if cfg.Sender.From != "sender@example.com" {
		t.Errorf("Sender.From = %q", cfg.Sender.From)
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
	os.Unsetenv("USERNAME")
	os.Setenv("PASSWORD", "pass")
	defer cleanupEnv()

	_, err := Load(context.Background())
	if err == nil {
		t.Error("Load() should error when username is missing")
	}
}

func TestLoad_BlankPasswordAllowed(t *testing.T) {
	os.Setenv("USERNAME", "user")
	os.Unsetenv("PASSWORD")
	defer cleanupEnv()

	cfg, err := Load(context.Background())
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Server.Password != "" {
		t.Errorf("Server.Password = %q, want empty", cfg.Server.Password)
	}
}

func TestLoad_InvalidJWT_TTL(t *testing.T) {
	os.Setenv("USERNAME", "user")
	os.Setenv("PASSWORD", "pass")
	os.Setenv("JWT_TTL", "invalid")
	defer cleanupEnv()

	_, err := Load(context.Background())
	if err == nil {
		t.Error("Load() should error on invalid JWT TTL")
	}
}

func TestLoad_UnknownStorage(t *testing.T) {
	os.Setenv("USERNAME", "user")
	os.Setenv("PASSWORD", "pass")
	os.Setenv("STORAGE", "unknown")
	defer cleanupEnv()

	_, err := Load(context.Background())
	if err == nil {
		t.Error("Load() should error on unknown storage")
	}
}

func TestLoad_UnknownSender(t *testing.T) {
	os.Setenv("USERNAME", "user")
	os.Setenv("PASSWORD", "pass")
	os.Setenv("SENDER", "unknown")
	defer cleanupEnv()

	_, err := Load(context.Background())
	if err == nil {
		t.Error("Load() should error on unknown sender")
	}
}

func TestLoad_UnknownMetadata(t *testing.T) {
	os.Setenv("USERNAME", "user")
	os.Setenv("PASSWORD", "pass")
	os.Setenv("METADATA", "unknown")
	defer cleanupEnv()

	_, err := Load(context.Background())
	if err == nil {
		t.Error("Load() should error on unknown metadata")
	}
}

func TestLoad_S3StorageRequiresBucket(t *testing.T) {
	os.Setenv("USERNAME", "user")
	os.Setenv("PASSWORD", "pass")
	os.Setenv("STORAGE", "s3")
	defer cleanupEnv()

	_, err := Load(context.Background())
	if err == nil {
		t.Error("Load() should error when s3 storage is selected without bucket")
	}
}

func TestLoad_DDBMetadataRequiresTable(t *testing.T) {
	os.Setenv("USERNAME", "user")
	os.Setenv("PASSWORD", "pass")
	os.Setenv("METADATA", "ddb")
	defer cleanupEnv()

	_, err := Load(context.Background())
	if err == nil {
		t.Error("Load() should error when ddb metadata is selected without table")
	}
}

func TestLoad_DynamoDBAlias(t *testing.T) {
	os.Setenv("USERNAME", "user")
	os.Setenv("PASSWORD", "pass")
	os.Setenv("METADATA", "dynamodb")
	defer cleanupEnv()

	_, err := Load(context.Background())
	if err == nil {
		t.Error("Load() should error when dynamodb metadata is selected without table")
	}
}

func TestLoad_FileMetadataDefaultPath(t *testing.T) {
	os.Setenv("USERNAME", "user")
	os.Setenv("PASSWORD", "pass")
	os.Setenv("METADATA", "file")
	defer cleanupEnv()

	cfg, err := Load(context.Background())
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.Metadata.FilePath != "emissary-metadata.json" {
		t.Errorf("Metadata.FilePath = %q, want %q", cfg.Metadata.FilePath, "emissary-metadata.json")
	}
}

func TestLoad_ListenAddrWithPort(t *testing.T) {
	os.Setenv("USERNAME", "user")
	os.Setenv("PASSWORD", "pass")
	os.Setenv("PORT", "3000")
	defer cleanupEnv()

	cfg, err := Load(context.Background())
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.Server.ListenAddr != ":3000" {
		t.Errorf("Server.ListenAddr = %q, want %q", cfg.Server.ListenAddr, ":3000")
	}
}

func TestLoad_ListenAddrWithFullAddress(t *testing.T) {
	os.Setenv("USERNAME", "user")
	os.Setenv("PASSWORD", "pass")
	os.Setenv("LISTEN_ADDR", "127.0.0.1:4000")
	defer cleanupEnv()

	cfg, err := Load(context.Background())
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.Server.ListenAddr != "127.0.0.1:4000" {
		t.Errorf("Server.ListenAddr = %q, want %q", cfg.Server.ListenAddr, "127.0.0.1:4000")
	}
}

func TestLoad_MetadataRegionDefaultsToAWSRegion(t *testing.T) {
	os.Setenv("USERNAME", "user")
	os.Setenv("PASSWORD", "pass")
	os.Setenv("METADATA", "ddb")
	os.Setenv("DDB_TABLE", "my-table")
	os.Setenv("AWS_REGION", "ap-northeast-1")
	defer cleanupEnv()

	cfg, err := Load(context.Background())
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.Metadata.Region != "ap-northeast-1" {
		t.Errorf("Metadata.Region = %q, want %q", cfg.Metadata.Region, "ap-northeast-1")
	}
}

func TestServerConfig_Validate(t *testing.T) {
	tests := []struct {
		name    string
		cfg     ServerConfig
		wantErr bool
	}{
		{"valid", ServerConfig{Username: "u", Password: "p"}, false},
		{"missing username", ServerConfig{Password: "p"}, true},
		{"blank password", ServerConfig{Username: "u"}, false},
		{"missing both", ServerConfig{}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cfg.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestServerConfig_Validate_MultipleErrors(t *testing.T) {
	cfg := ServerConfig{}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("Validate() should return error for missing username")
	}
	if !strings.Contains(err.Error(), "username") {
		t.Errorf("error should mention username, got: %v", err)
	}
	if strings.Contains(err.Error(), "password") {
		t.Errorf("blank passwords should be allowed, got: %v", err)
	}
}

func TestStorageConfig_Validate(t *testing.T) {
	tests := []struct {
		name    string
		cfg     StorageConfig
		wantErr bool
	}{
		{"memory", StorageConfig{Type: "memory"}, false},
		{"s3 with bucket", StorageConfig{Type: "s3", S3Bucket: "b"}, false},
		{"s3 without bucket", StorageConfig{Type: "s3"}, true},
		{"unknown type", StorageConfig{Type: "foo"}, true},
		{"s3 unknown bucket", StorageConfig{Type: "s3", S3Bucket: "b"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cfg.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestSenderConfig_Validate(t *testing.T) {
	tests := []struct {
		name    string
		cfg     SenderConfig
		wantErr bool
	}{
		{"log", SenderConfig{Type: "log"}, false},
		{"aws-ses", SenderConfig{Type: "aws-ses"}, false},
		{"ses alias", SenderConfig{Type: "ses"}, false},
		{"unknown", SenderConfig{Type: "smtp"}, true},
		{"empty", SenderConfig{}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cfg.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestMetadataConfig_Validate(t *testing.T) {
	tests := []struct {
		name    string
		cfg     MetadataConfig
		wantErr bool
	}{
		{"memory", MetadataConfig{Type: "memory"}, false},
		{"file", MetadataConfig{Type: "file"}, false},
		{"ddb with table", MetadataConfig{Type: "ddb", DdbTable: "t"}, false},
		{"ddb without table", MetadataConfig{Type: "ddb"}, true},
		{"dynamodb with table", MetadataConfig{Type: "dynamodb", DdbTable: "t"}, false},
		{"dynamodb without table", MetadataConfig{Type: "dynamodb"}, true},
		{"unknown", MetadataConfig{Type: "redis"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cfg.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestConfig_Validate_CompositeError(t *testing.T) {
	cfg := &Config{
		Server:   ServerConfig{}, // missing username and password
		Storage:  StorageConfig{Type: "bad"},
		Sender:   SenderConfig{Type: "bad"},
		Metadata: MetadataConfig{Type: "ddb"}, // missing table
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("Validate() should return composite error")
	}

	// Verify all sub-config errors are present
	msg := err.Error()
	if !strings.Contains(msg, "username") {
		t.Errorf("composite error should mention username, got: %v", msg)
	}
	if !strings.Contains(msg, "storage type") {
		t.Errorf("composite error should mention storage type, got: %v", msg)
	}
	if !strings.Contains(msg, "sender type") {
		t.Errorf("composite error should mention sender type, got: %v", msg)
	}
	if !strings.Contains(msg, "metadata=ddb") {
		t.Errorf("composite error should mention ddb table requirement, got: %v", msg)
	}
}

func TestConfig_Validate_AllValid(t *testing.T) {
	cfg := &Config{
		Server:   ServerConfig{Username: "u", Password: "p"},
		Storage:  StorageConfig{Type: "memory"},
		Sender:   SenderConfig{Type: "log"},
		Metadata: MetadataConfig{Type: "memory"},
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("Validate() error = %v, want nil", err)
	}
}

func cleanupEnv() {
	os.Unsetenv("USERNAME")
	os.Unsetenv("PASSWORD")
	os.Unsetenv("JWT_SECRET")
	os.Unsetenv("STORAGE")
	os.Unsetenv("SENDER")
	os.Unsetenv("METADATA")
	os.Unsetenv("JWT_TTL")
	os.Unsetenv("PORT")
	os.Unsetenv("S3_BUCKET")
	os.Unsetenv("SES_FROM")
	os.Unsetenv("METADATA_FILE")
	os.Unsetenv("LISTEN_ADDR")
	os.Unsetenv("DDB_TABLE")
	os.Unsetenv("METADATA_REGION")
	os.Unsetenv("USER_MANAGER")
	os.Unsetenv("DOMAIN")
	os.Unsetenv("USER_TABLE")
	os.Unsetenv("USER_REGION")
	os.Unsetenv("AWS_REGION")
	envutil.ClearCache()
}