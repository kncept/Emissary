package config

import (
	"context"
	"os"
	"path/filepath"
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
		os.Unsetenv("RECEIVER")
		os.Unsetenv("RECEIVER_BUCKET")
		os.Unsetenv("RECEIVER_FILESYSTEM_DIR")
		os.Unsetenv("RECEIVER_TABLE")
		os.Unsetenv("RECEIVER_REGION")
		os.Unsetenv("MTA_PORT")
		os.Unsetenv("SESSION")
		os.Unsetenv("SESSION_TABLE")
		os.Unsetenv("SESSION_REGION")
		os.Unsetenv("TABLE_PREFIX")
		os.Unsetenv("DDB_TABLE_PREFIX")
		os.Unsetenv("METADATA")
		os.Unsetenv("JWT_TTL")
		os.Unsetenv("PORT")
		os.Unsetenv("METADATA_REGION")
		os.Unsetenv("AWS_REGION")
		clearAWSCredentialEnv()
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
	if cfg.Receiver.Type != "memory" {
		t.Errorf("Receiver.Type = %q, want %q", cfg.Receiver.Type, "memory")
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

func TestLoad_UserManagerAutoSelect(t *testing.T) {
	tests := []struct {
		name         string
		storage      string
		s3Bucket     string
		userManager  string
		wantStorage  string
		wantUserType string
	}{
		{
			name:         "s3 storage auto-selects s3 user manager",
			storage:      "s3",
			s3Bucket:     "my-bucket",
			wantStorage:  "s3",
			wantUserType: "s3",
		},
		{
			name:         "s3 storage with explicit backend keeps it",
			storage:      "s3",
			s3Bucket:     "my-bucket",
			userManager:  "ddb",
			wantStorage:  "s3",
			wantUserType: "ddb",
		},
		{
			name:         "s3 storage with explicit none stays none",
			storage:      "s3",
			s3Bucket:     "my-bucket",
			userManager:  "none",
			wantStorage:  "s3",
			wantUserType: "none",
		},
		{
			name:         "bucket without storage type auto-selects s3",
			storage:      "",
			s3Bucket:     "my-bucket",
			wantStorage:  "s3",
			wantUserType: "s3",
		},
		{
			name:         "memory storage auto-selects none",
			storage:      "",
			s3Bucket:     "",
			wantStorage:  "memory",
			wantUserType: "none",
		},
		{
			name:         "memory storage with explicit none stays none",
			storage:      "memory",
			s3Bucket:     "",
			userManager:  "none",
			wantStorage:  "memory",
			wantUserType: "none",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			os.Setenv("USERNAME", "user")
			os.Setenv("PASSWORD", "pass")
			os.Unsetenv("USER_MANAGER")
			os.Unsetenv("STORAGE")
			os.Unsetenv("S3_BUCKET")
			if tt.storage != "" {
				os.Setenv("STORAGE", tt.storage)
			}
			if tt.s3Bucket != "" {
				os.Setenv("S3_BUCKET", tt.s3Bucket)
			}
			if tt.userManager != "" {
				os.Setenv("USER_MANAGER", tt.userManager)
			}
			defer cleanupEnv()

			cfg, err := Load(context.Background())
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}
			if cfg.Storage.Type != tt.wantStorage {
				t.Errorf("Storage.Type = %q, want %q", cfg.Storage.Type, tt.wantStorage)
			}
			if cfg.UserManager.Type != tt.wantUserType {
				t.Errorf("UserManager.Type = %q, want %q", cfg.UserManager.Type, tt.wantUserType)
			}
		})
	}
}

func TestLoad_MissingUsernameAllowed(t *testing.T) {
	os.Unsetenv("USERNAME")
	os.Setenv("PASSWORD", "pass")
	defer cleanupEnv()

	cfg, err := Load(context.Background())
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Server.Username != "" {
		t.Errorf("Server.Username = %q, want empty", cfg.Server.Username)
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

func TestLoad_DDBMetadataDerivesTableFromPrefix(t *testing.T) {
	os.Setenv("USERNAME", "user")
	os.Setenv("PASSWORD", "pass")
	os.Setenv("METADATA", "ddb")
	defer cleanupEnv()

	cfg, err := Load(context.Background())
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got := cfg.MetadataTableName(); got != "emissary-metadata" {
		t.Errorf("MetadataTableName() = %q, want %q", got, "emissary-metadata")
	}
}

func TestLoad_DynamoDBAliasDerivesTableFromPrefix(t *testing.T) {
	os.Setenv("USERNAME", "user")
	os.Setenv("PASSWORD", "pass")
	os.Setenv("METADATA", "dynamodb")
	os.Setenv("TABLE_PREFIX", "custom")
	defer cleanupEnv()

	cfg, err := Load(context.Background())
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.DDBTablePrefix != "custom" {
		t.Errorf("DDBTablePrefix = %q, want %q", cfg.DDBTablePrefix, "custom")
	}
	if got := cfg.MetadataTableName(); got != "custom-metadata" {
		t.Errorf("MetadataTableName() = %q, want %q", got, "custom-metadata")
	}
}

func TestLoad_ReceiverDDBTableDerivesFromPrefix(t *testing.T) {
	os.Setenv("USERNAME", "user")
	os.Setenv("PASSWORD", "pass")
	os.Setenv("RECEIVER", "dynamodb")
	os.Setenv("TABLE_PREFIX", "custom")
	defer cleanupEnv()

	cfg, err := Load(context.Background())
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got := cfg.ReceiverTableName(); got != "custom-receiver" {
		t.Errorf("ReceiverTableName() = %q, want %q", got, "custom-receiver")
	}
	os.Setenv("RECEIVER_TABLE", "explicit-table")
	defer os.Unsetenv("RECEIVER_TABLE")
	cfg, err = Load(context.Background())
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got := cfg.ReceiverTableName(); got != "explicit-table" {
		t.Errorf("ReceiverTableName() with override = %q, want %q", got, "explicit-table")
	}
}

func TestLoad_FileMetadataDefaultPath(t *testing.T) {
	os.Setenv("USERNAME", "user")
	os.Setenv("PASSWORD", "pass")
	os.Setenv("METADATA", "file")
	os.Unsetenv("METADATA_FILE")
	os.Unsetenv("FILESYSTEM_DIR")
	defer cleanupEnv()

	cfg, err := Load(context.Background())
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir() error = %v", err)
	}
	want := filepath.Join(home, ".emissary", "metadata")
	if cfg.Metadata.FilePath != want {
		t.Errorf("Metadata.FilePath = %q, want %q", cfg.Metadata.FilePath, want)
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
		{"missing username", ServerConfig{Password: "p"}, false},
		{"blank password", ServerConfig{Username: "u"}, false},
		{"missing both", ServerConfig{}, false},
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

func TestServerConfig_Validate_CredentialsOptional(t *testing.T) {
	cfg := ServerConfig{}
	if err := cfg.Validate(); err != nil {
		t.Errorf("Validate() error = %v, want nil (credentials are optional)", err)
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

func TestReceiverConfig_Validate(t *testing.T) {
	tests := []struct {
		name    string
		cfg     ReceiverConfig
		wantErr bool
	}{
		{"unset", ReceiverConfig{}, false},
		{"memory", ReceiverConfig{Type: "memory"}, false},
		{"s3 with bucket", ReceiverConfig{Type: "s3", S3Bucket: "receiver-bucket"}, false},
		{"s3 without bucket", ReceiverConfig{Type: "s3"}, true},
		{"filesystem with dir", ReceiverConfig{Type: "filesystem", FilesystemDir: "/tmp/receiver"}, false},
		{"filesystem without dir", ReceiverConfig{Type: "filesystem"}, true},
		{"ddb", ReceiverConfig{Type: "ddb"}, false},
		{"unknown type", ReceiverConfig{Type: "foo"}, true},
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
		{"mta", SenderConfig{Type: "mta"}, false},
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

func TestLoad_MtaSenderConfig(t *testing.T) {
	os.Setenv("USERNAME", "testuser")
	os.Setenv("SENDER", "mta")
	os.Setenv("MTA_PORT", "1025")
	defer cleanupEnv()

	cfg, err := Load(context.Background())
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Sender.Type != "mta" {
		t.Errorf("Sender.Type = %q, want mta", cfg.Sender.Type)
	}
	if cfg.Sender.MTAPort != "1025" {
		t.Errorf("Sender.MTAPort = %q", cfg.Sender.MTAPort)
	}
}

func TestLoad_SenderAutoSelect(t *testing.T) {
	tests := []struct {
		name   string
		sender string
		env    map[string]string
		want   string
	}{
		{"no credentials", "", nil, "log"},
		{"access key pair", "", map[string]string{
			"AWS_ACCESS_KEY_ID": "key", "AWS_SECRET_ACCESS_KEY": "secret",
		}, "aws-ses"},
		{"incomplete access key pair", "", map[string]string{
			"AWS_ACCESS_KEY_ID": "key",
		}, "log"},
		{"shared profile", "", map[string]string{"AWS_PROFILE": "dev"}, "aws-ses"},
		{"web identity", "", map[string]string{
			"AWS_WEB_IDENTITY_TOKEN_FILE": "/token", "AWS_ROLE_ARN": "arn:aws:iam::123456789012:role/test",
		}, "aws-ses"},
		{"container credentials", "", map[string]string{
			"AWS_CONTAINER_CREDENTIALS_RELATIVE_URI": "/v2/credentials/test",
		}, "aws-ses"},
		{"explicit sender wins over credentials", "mta", map[string]string{
			"AWS_ACCESS_KEY_ID": "key", "AWS_SECRET_ACCESS_KEY": "secret",
		}, "mta"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearAWSCredentialEnv()
			defer cleanupEnv()

			os.Setenv("USERNAME", "testuser")
			if tt.sender != "" {
				os.Setenv("SENDER", tt.sender)
			}
			for key, value := range tt.env {
				os.Setenv(key, value)
			}

			cfg, err := Load(context.Background())
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}
			if cfg.Sender.Type != tt.want {
				t.Errorf("Sender.Type = %q, want %q", cfg.Sender.Type, tt.want)
			}
		})
	}
}

func TestLoad_ReceiverAutoSelect(t *testing.T) {
	tests := []struct {
		name     string
		receiver string
		env      map[string]string
		want     string
	}{
		{"no credentials", "", nil, "memory"},
		{"credentials without bucket", "", map[string]string{
			"AWS_ACCESS_KEY_ID": "key", "AWS_SECRET_ACCESS_KEY": "secret",
		}, "memory"},
		{"access key pair", "", map[string]string{
			"AWS_ACCESS_KEY_ID": "key", "AWS_SECRET_ACCESS_KEY": "secret", "S3_BUCKET": "bucket",
		}, "s3"},
		{"shared profile", "", map[string]string{
			"AWS_PROFILE": "dev", "S3_BUCKET": "bucket",
		}, "s3"},
		{"explicit memory wins over credentials", "memory", map[string]string{
			"AWS_ACCESS_KEY_ID": "key", "AWS_SECRET_ACCESS_KEY": "secret",
		}, "memory"},
		{"explicit filesystem", "filesystem", map[string]string{
			"FILESYSTEM_DIR": t.TempDir(),
		}, "filesystem"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearAWSCredentialEnv()
			defer cleanupEnv()

			os.Setenv("USERNAME", "testuser")
			if tt.receiver != "" {
				os.Setenv("RECEIVER", tt.receiver)
			}
			for key, value := range tt.env {
				os.Setenv(key, value)
			}

			cfg, err := Load(context.Background())
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}
			if cfg.Receiver.Type != tt.want {
				t.Errorf("Receiver.Type = %q, want %q", cfg.Receiver.Type, tt.want)
			}
		})
	}
}

func TestLoad_DedicatedReceiverTargets(t *testing.T) {
	defer cleanupEnv()
	clearAWSCredentialEnv()

	os.Setenv("USERNAME", "testuser")
	os.Setenv("STORAGE", "memory")
	os.Setenv("RECEIVER", "s3")
	os.Setenv("S3_BUCKET", "mail-bucket")
	os.Setenv("RECEIVER_BUCKET", "receiver-bucket")

	cfg, err := Load(context.Background())
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Receiver.S3Bucket != "receiver-bucket" {
		t.Errorf("Receiver.S3Bucket = %q, want %q", cfg.Receiver.S3Bucket, "receiver-bucket")
	}

	os.Unsetenv("RECEIVER_BUCKET")
	clearAWSCredentialEnv()
	os.Setenv("RECEIVER", "filesystem")
	os.Setenv("RECEIVER_FILESYSTEM_DIR", t.TempDir())

	cfg, err = Load(context.Background())
	if err != nil {
		t.Fatalf("Load(filesystem) error = %v", err)
	}
	if cfg.Receiver.FilesystemDir == "" {
		t.Error("Receiver.FilesystemDir is empty")
	}
	if cfg.Receiver.S3Bucket != "mail-bucket" {
		t.Errorf("Receiver.S3Bucket fallback = %q, want %q", cfg.Receiver.S3Bucket, "mail-bucket")
	}
}

func clearAWSCredentialEnv() {
	for _, key := range []string{
		"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_PROFILE", "AWS_DEFAULT_PROFILE",
		"AWS_WEB_IDENTITY_TOKEN_FILE", "AWS_ROLE_ARN",
		"AWS_CONTAINER_CREDENTIALS_RELATIVE_URI", "AWS_CONTAINER_CREDENTIALS_FULL_URI",
	} {
		os.Unsetenv(key)
	}
	envutil.ClearCache()
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
		{"ddb without table", MetadataConfig{Type: "ddb"}, false},
		{"dynamodb with table", MetadataConfig{Type: "dynamodb", DdbTable: "t"}, false},
		{"dynamodb without table", MetadataConfig{Type: "dynamodb"}, false},
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
		Server:      ServerConfig{}, // credentials are optional
		Storage:     StorageConfig{Type: "bad"},
		Sender:      SenderConfig{Type: "bad"},
		Metadata:    MetadataConfig{Type: "redis"},
		Session:     SessionConfig{Type: "bad"},
		UserManager: UserManagerConfig{Type: "bad"},
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("Validate() should return composite error")
	}

	// Verify all sub-config errors are present
	msg := err.Error()
	if !strings.Contains(msg, "storage type") {
		t.Errorf("composite error should mention storage type, got: %v", msg)
	}
	if !strings.Contains(msg, "sender type") {
		t.Errorf("composite error should mention sender type, got: %v", msg)
	}
	if !strings.Contains(msg, "metadata type") {
		t.Errorf("composite error should mention metadata type, got: %v", msg)
	}
	if !strings.Contains(msg, "session type") {
		t.Errorf("composite error should mention session type, got: %v", msg)
	}
	if !strings.Contains(msg, "user manager type") {
		t.Errorf("composite error should mention user manager type, got: %v", msg)
	}
}

func TestConfig_Validate_AllValid(t *testing.T) {
	cfg := &Config{
		Server:   ServerConfig{Username: "u", Password: "p"},
		Storage:  StorageConfig{Type: "memory"},
		Sender:   SenderConfig{Type: "log"},
		Metadata: MetadataConfig{Type: "memory"},
		Session:  SessionConfig{Type: "memory"},
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("Validate() error = %v, want nil", err)
	}
}

func TestUserManagerConfig_Validate(t *testing.T) {
	tests := []struct {
		name    string
		cfg     UserManagerConfig
		wantErr bool
	}{
		{"unset", UserManagerConfig{}, false},
		{"none", UserManagerConfig{Type: "none"}, false},
		{"ddb", UserManagerConfig{Type: "ddb"}, false},
		{"dynamodb", UserManagerConfig{Type: "dynamodb"}, false},
		{"s3", UserManagerConfig{Type: "s3"}, false},
		{"ldap with URL", UserManagerConfig{Type: "ldap", LDAPURL: "ldap://localhost:389"}, false},
		{"ldap without URL", UserManagerConfig{Type: "ldap"}, true},
		{"removed any backend", UserManagerConfig{Type: "any", Domain: "example.com"}, true},
		{"unknown", UserManagerConfig{Type: "smtp"}, true},
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

func cleanupEnv() {
	os.Unsetenv("USERNAME")
	os.Unsetenv("PASSWORD")
	os.Unsetenv("JWT_SECRET")
	os.Unsetenv("STORAGE")
	os.Unsetenv("SENDER")
	os.Unsetenv("RECEIVER")
	os.Unsetenv("RECEIVER_BUCKET")
	os.Unsetenv("RECEIVER_FILESYSTEM_DIR")
	os.Unsetenv("RECEIVER_TABLE")
	os.Unsetenv("RECEIVER_REGION")
	os.Unsetenv("SESSION")
	os.Unsetenv("SESSION_TABLE")
	os.Unsetenv("SESSION_REGION")
	os.Unsetenv("TABLE_PREFIX")
	os.Unsetenv("DDB_TABLE_PREFIX")
	os.Unsetenv("METADATA")
	os.Unsetenv("JWT_TTL")
	os.Unsetenv("PORT")
	os.Unsetenv("S3_BUCKET")
	os.Unsetenv("MTA_PORT")
	os.Unsetenv("METADATA_FILE")
	os.Unsetenv("FILESYSTEM_DIR")
	os.Unsetenv("LISTEN_ADDR")
	os.Unsetenv("DDB_TABLE")
	os.Unsetenv("METADATA_REGION")
	os.Unsetenv("USER_MANAGER")
	os.Unsetenv("DOMAIN")
	os.Unsetenv("USER_TABLE")
	os.Unsetenv("USER_REGION")
	os.Unsetenv("AWS_REGION")
	clearAWSCredentialEnv()
}

func TestLoad_FilesystemBackend(t *testing.T) {
	root := t.TempDir()
	os.Setenv("USERNAME", "user")
	os.Setenv("PASSWORD", "pass")
	os.Setenv("STORAGE", "filesystem")
	os.Setenv("FILESYSTEM_DIR", root)
	os.Setenv("METADATA", "file")
	os.Unsetenv("USER_MANAGER")
	os.Unsetenv("METADATA_FILE")
	defer cleanupEnv()

	cfg, err := Load(context.Background())
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Storage.Type != "filesystem" || cfg.UserManager.Type != "filesystem" {
		t.Errorf("storage=%q user manager=%q", cfg.Storage.Type, cfg.UserManager.Type)
	}
	if cfg.FilesystemDir != root {
		t.Errorf("FilesystemDir = %q, want %q", cfg.FilesystemDir, root)
	}
	if cfg.Storage.FilesystemDir != root || cfg.UserManager.FilesystemDir != root {
		t.Errorf("sub-config dirs = %q, %q", cfg.Storage.FilesystemDir, cfg.UserManager.FilesystemDir)
	}
	if got, want := cfg.FilesystemMailDir(), filepath.Join(root, "mail"); got != want {
		t.Errorf("FilesystemMailDir() = %q, want %q", got, want)
	}
	if got, want := cfg.FilesystemUsersDir(), filepath.Join(root, "users"); got != want {
		t.Errorf("FilesystemUsersDir() = %q, want %q", got, want)
	}
	if got, want := cfg.Metadata.FilePath, filepath.Join(root, "metadata"); got != want {
		t.Errorf("metadata path = %q, want %q", got, want)
	}
}

func TestLoad_FilesystemDirExpandsHome(t *testing.T) {
	os.Setenv("USERNAME", "user")
	os.Setenv("PASSWORD", "pass")
	os.Setenv("FILESYSTEM_DIR", "~/emissary-test")
	defer cleanupEnv()

	cfg, err := Load(context.Background())
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir() error = %v", err)
	}
	if want := filepath.Join(home, "emissary-test"); cfg.FilesystemDir != want {
		t.Errorf("FilesystemDir = %q, want %q", cfg.FilesystemDir, want)
	}
}

func TestValidate_FilesystemTypes(t *testing.T) {
	if err := (&StorageConfig{Type: "filesystem", FilesystemDir: "/tmp/emissary"}).Validate(); err != nil {
		t.Errorf("storage validate = %v", err)
	}
	if err := (&StorageConfig{Type: "filesystem"}).Validate(); err == nil {
		t.Error("storage validate without dir succeeded")
	}
	if err := (&UserManagerConfig{Type: "filesystem", FilesystemDir: "/tmp/emissary"}).Validate(); err != nil {
		t.Errorf("user manager validate = %v", err)
	}
	if err := (&UserManagerConfig{Type: "filesystem"}).Validate(); err == nil {
		t.Error("user manager validate without dir succeeded")
	}
}
