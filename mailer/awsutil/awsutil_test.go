package awsutil

import (
	"context"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
)

func TestSecretsManagerClientRetriesAfterFailedInit(t *testing.T) {
	secretsClientMu.Lock()
	oldClient := secretsClient
	secretsClient = nil
	secretsClientMu.Unlock()
	defer func() {
		secretsClientMu.Lock()
		secretsClient = oldClient
		secretsClientMu.Unlock()
	}()

	calls := 0
	oldLoad := loadDefaultConfig
	loadDefaultConfig = func(context.Context, ...func(*awsconfig.LoadOptions) error) (aws.Config, error) {
		calls++
		if calls == 1 {
			return aws.Config{}, errors.New("transient failure")
		}
		return aws.Config{Region: "us-east-1"}, nil
	}
	defer func() { loadDefaultConfig = oldLoad }()

	if _, err := secretsManagerClient(context.Background()); err == nil {
		t.Fatal("first init should fail")
	}
	if _, err := secretsManagerClient(context.Background()); err != nil {
		t.Fatalf("second init should succeed after a transient failure: %v", err)
	}
	if calls != 2 {
		t.Errorf("config loads = %d, want 2 (the error must not be cached)", calls)
	}
	// Once initialised the client is reused.
	if _, err := secretsManagerClient(context.Background()); err != nil {
		t.Fatalf("cached client should be reused: %v", err)
	}
	if calls != 2 {
		t.Errorf("config loads = %d, want 2 after caching", calls)
	}
}

func TestHasCredentials(t *testing.T) {
	tests := []struct {
		name string
		cfg  aws.Config
		want bool
	}{
		{"static credentials", aws.Config{Credentials: credentials.NewStaticCredentialsProvider("key", "secret", "")}, true},
		{"no credentials", aws.Config{Credentials: credentials.NewStaticCredentialsProvider("", "", "")}, false},
	}
	oldLoad := loadDefaultConfig
	defer func() { loadDefaultConfig = oldLoad }()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			loadDefaultConfig = func(context.Context, ...func(*awsconfig.LoadOptions) error) (aws.Config, error) {
				return tt.cfg, nil
			}
			if got := HasCredentials(context.Background()); got != tt.want {
				t.Errorf("HasCredentials() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIsSecretsManagerARN(t *testing.T) {
	tests := []struct {
		value string
		want  bool
	}{
		{"arn:aws:secretsmanager:us-east-1:123456789012:secret:jwt-a1b2c3", true},
		{"arn:aws-us-gov:secretsmanager:us-west-2:123456789012:secret:jwt-a1b2c3", true},
		{"arn:aws:iam::123456789012:role/emissary", false},
		{"plain-value", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := IsSecretsManagerARN(tt.value); got != tt.want {
			t.Errorf("IsSecretsManagerARN(%q) = %v, want %v", tt.value, got, tt.want)
		}
	}
}
