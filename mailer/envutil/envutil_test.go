package envutil

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestGetCachedEnvResolvesSecretARN(t *testing.T) {
	const arn = "arn:aws:secretsmanager:ap-southeast-2:123456789012:secret:jwt-AbCdEf"
	t.Setenv("TEST_SECRET", arn)
	ClearCache()
	defer ClearCache()

	old := resolveSecret
	resolveSecret = func(_ context.Context, id string) (string, error) {
		if id != arn {
			t.Errorf("ResolveSecret id = %q, want %q", id, arn)
		}
		return "resolved-value", nil
	}
	defer func() { resolveSecret = old }()

	got, err := GetCachedEnv(context.Background(), "TEST_SECRET")
	if err != nil {
		t.Fatalf("GetCachedEnv() error = %v", err)
	}
	if got != "resolved-value" {
		t.Errorf("GetCachedEnv() = %q, want resolved value", got)
	}
}

func TestGetCachedEnvPropagatesResolutionFailure(t *testing.T) {
	t.Setenv("TEST_SECRET", "arn:aws:secretsmanager:us-east-1:123456789012:secret:broken-a1b2c3")
	ClearCache()
	defer ClearCache()

	want := errors.New("access denied")
	old := resolveSecret
	resolveSecret = func(context.Context, string) (string, error) { return "", want }
	defer func() { resolveSecret = old }()

	got, err := GetCachedEnv(context.Background(), "TEST_SECRET")
	if !errors.Is(err, want) {
		t.Fatalf("GetCachedEnv() error = %v, want %v", err, want)
	}
	if got != "" {
		t.Errorf("GetCachedEnv() = %q, want empty on failure (never the ARN)", got)
	}
}

func TestGetCachedEnvLeavesOtherARNsAlone(t *testing.T) {
	const roleARN = "arn:aws:iam::123456789012:role/emissary"
	t.Setenv("AWS_ROLE_ARN", roleARN)
	ClearCache()
	defer ClearCache()

	old := resolveSecret
	resolveSecret = func(_ context.Context, id string) (string, error) {
		t.Errorf("unexpected secret resolution for %q", id)
		return "", errors.New("should not be called")
	}
	defer func() { resolveSecret = old }()

	got, err := GetCachedEnv(context.Background(), "AWS_ROLE_ARN")
	if err != nil {
		t.Fatalf("GetCachedEnv() error = %v", err)
	}
	if got != roleARN {
		t.Errorf("GetCachedEnv() = %q, want %q", got, roleARN)
	}
}

func TestGetCachedEnvMissingAndOverride(t *testing.T) {
	t.Setenv("TEST_MISSING", "")
	ClearCache()
	defer ClearCache()

	if got, err := GetCachedEnv(context.Background(), "TEST_MISSING"); err != nil || got != "" {
		t.Fatalf("GetCachedEnv(missing) = %q, %v", got, err)
	}
	SetOverride("TEST_OVERRIDE", "  spaced  ")
	got, err := GetCachedEnv(context.Background(), "TEST_OVERRIDE")
	if err != nil {
		t.Fatalf("GetCachedEnv(override) error = %v", err)
	}
	if strings.TrimSpace(got) != "spaced" {
		t.Errorf("GetCachedEnv(override) = %q", got)
	}
}
