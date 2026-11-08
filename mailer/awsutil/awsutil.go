package awsutil

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
)

var (
	secretsClient *secretsmanager.Client
	initOnce      sync.Once
	initErr       error
)

func initSecretsClient(ctx context.Context) error {
	initOnce.Do(func() {
		cfg, err := config.LoadDefaultConfig(ctx)
		if err != nil {
			initErr = err
			return
		}
		secretsClient = secretsmanager.NewFromConfig(cfg)
	})
	return initErr
}

// IsAwsLambda returns true if running in an AWS Lambda environment.
func IsAwsLambda() bool {
	return os.Getenv("AWS_LAMBDA_FUNCTION_NAME") != ""
}

// ResolveSecret resolves a secret value from AWS Secrets Manager.
// The secretID can be either a full ARN or a secret name.
func ResolveSecret(ctx context.Context, secretID string) (string, error) {
	if err := initSecretsClient(ctx); err != nil {
		return "", err
	}

	// Handle both ARN and plain secret name
	// ARN format: arn:aws:secretsmanager:region:account:secret:name-xxxxxx
	input := &secretsmanager.GetSecretValueInput{
		SecretId: aws.String(secretID),
	}

	result, err := secretsClient.GetSecretValue(ctx, input)
	if err != nil {
		return "", err
	}

	if result.SecretString == nil {
		return "", fmt.Errorf("secret %s has no SecretString", secretID)
	}

	return *result.SecretString, nil
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
