package awsutil

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
)

var (
	secretsClientMu sync.Mutex
	secretsClient   *secretsmanager.Client
)

// loadDefaultConfig is a variable so tests can simulate a transient AWS
// configuration failure without reaching the network.
var loadDefaultConfig = config.LoadDefaultConfig

// secretsManagerClient returns a secrets manager client, creating it on first
// successful use. A failed load is deliberately not cached: a cold start with
// an unavailable config endpoint must not poison every later resolution.
func secretsManagerClient(ctx context.Context) (*secretsmanager.Client, error) {
	secretsClientMu.Lock()
	defer secretsClientMu.Unlock()
	if secretsClient != nil {
		return secretsClient, nil
	}
	cfg, err := loadDefaultConfig(ctx)
	if err != nil {
		return nil, err
	}
	secretsClient = secretsmanager.NewFromConfig(cfg)
	return secretsClient, nil
}

// IsAwsLambda returns true if running in an AWS Lambda environment.
func IsAwsLambda() bool {
	return os.Getenv("AWS_LAMBDA_FUNCTION_NAME") != ""
}

// IsSecretsManagerARN reports whether value names a Secrets Manager secret.
// Other ARNs (IAM roles, S3 buckets, ...) are ordinary configuration values and
// must never be passed to the Secrets Manager API.
func IsSecretsManagerARN(value string) bool {
	return strings.HasPrefix(value, "arn:") && strings.Contains(value, ":secretsmanager:")
}

// ResolveSecret resolves a secret value from AWS Secrets Manager.
// The secretID can be either a full ARN or a secret name.
func ResolveSecret(ctx context.Context, secretID string) (string, error) {
	client, err := secretsManagerClient(ctx)
	if err != nil {
		return "", fmt.Errorf("configure secrets manager: %w", err)
	}

	input := &secretsmanager.GetSecretValueInput{
		SecretId: aws.String(secretID),
	}
	result, err := client.GetSecretValue(ctx, input)
	if err != nil {
		return "", fmt.Errorf("get secret %q: %w", secretID, err)
	}
	if result.SecretString == nil {
		return "", fmt.Errorf("secret %q has no SecretString", secretID)
	}
	return *result.SecretString, nil
}

// credentialProbeTimeout bounds the IAM-role probe so a machine with no
// instance metadata service cannot stall configuration loading.
const credentialProbeTimeout = 2 * time.Second

// HasCredentials reports whether the AWS SDK default credential chain can
// supply credentials. Unlike a check of AWS_* environment variables this also
// recognises IAM roles: EC2 instance profiles, ECS task roles, web identity
// (IRSA) and the Lambda execution role.
func HasCredentials(ctx context.Context) bool {
	probeCtx, cancel := context.WithTimeout(ctx, credentialProbeTimeout)
	defer cancel()
	cfg, err := loadDefaultConfig(probeCtx)
	if err != nil {
		return false
	}
	_, err = cfg.Credentials.Retrieve(probeCtx)
	return err == nil
}

// ExtractSecretNameFromARN extracts the secret name from a Secrets Manager ARN.
// Returns the original string if it's not a valid ARN.
func ExtractSecretNameFromARN(arn string) string {
	if !strings.HasPrefix(arn, "arn:aws:secretsmanager:") {
		return arn
	}
	// ARN format: arn:aws:secretsmanager:region:account:secret:name-xxxxxx
	parts := strings.Split(arn, ":")
	if len(parts) < 7 {
		return arn
	}
	// The secret name is the last part, possibly with a suffix like -xxxxxx
	secretPart := parts[len(parts)-1]
	if strings.HasPrefix(secretPart, "secret/") {
		return strings.TrimPrefix(secretPart, "secret/")
	}
	return secretPart
}
