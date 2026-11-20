package envutil

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/kncept/Emissary/mailer/awsutil"
)

var CommitHash string = "unknown"
var BuildTime string = "unknown"
var Version string = "dev"

// StartupMessage returns the startup log message for a binary:
// "<name> <version> (<commit hash>) built: <build time>".
func StartupMessage(name string) string {
	return fmt.Sprintf("%s %s (%s) built: %s", name, Version, CommitHash, formatBuildTime(BuildTime))
}

// formatBuildTime renders the ldflag-provided timestamp as DD/MM/YYYY HH:MM:SS.
// Values that are already in that format, or that cannot be parsed, are passed
// through unchanged.
func formatBuildTime(buildTime string) string {
	const displayLayout = "02/01/2006 15:04:05"
	for _, layout := range []string{time.RFC3339, displayLayout} {
		if t, err := time.Parse(layout, buildTime); err == nil {
			return t.Format(displayLayout)
		}
	}
	return buildTime
}

// cache holds the environment variable cache.
// It is populated lazily and shared across calls.
var (
	cache     map[string]string
	cacheOnce sync.Once
	cacheMu   sync.RWMutex
)

// initCache initializes the cache with all current environment variables.
func initCache() {
	cacheOnce.Do(func() {
		cache = make(map[string]string)
		for _, e := range os.Environ() {
			parts := strings.SplitN(e, "=", 2)
			if len(parts) == 2 {
				cache[parts[0]] = parts[1]
			}
		}
	})
}

// resolveSecret is a variable so tests can exercise resolution failures
// without contacting AWS.
var resolveSecret = awsutil.ResolveSecret

// GetCachedEnv retrieves an environment variable value, using a local cache.
// A Secrets Manager ARN is resolved before the value is returned. A resolution
// failure is propagated: returning the ARN as if it were a credential would
// turn a recoverable startup error into a silently broken deployment.
//
// The context is passed to the secret resolution call for timeout/cancellation
// support.
func GetCachedEnv(ctx context.Context, key string) (string, error) {
	initCache()

	// Fast path: check cache first (read lock)
	cacheMu.RLock()
	val, ok := cache[key]
	cacheMu.RUnlock()

	if !ok {
		// Not in cache, check os.Getenv (handles updates after init)
		val, ok = os.LookupEnv(key)
		if ok {
			cacheMu.Lock()
			cache[key] = val
			cacheMu.Unlock()
		}
	}
	if !ok {
		return "", nil
	}

	return resolveIfARN(ctx, val)
}

// resolveIfARN resolves Secrets Manager ARNs. Other ARNs are returned verbatim:
// config values such as AWS_ROLE_ARN are not secrets.
func resolveIfARN(ctx context.Context, val string) (string, error) {
	val = strings.TrimSpace(val)
	if !awsutil.IsSecretsManagerARN(val) {
		return val, nil
	}
	resolved, err := resolveSecret(ctx, val)
	if err != nil {
		return "", fmt.Errorf("resolve %q: %w", val, err)
	}
	return resolved, nil
}

// SetOverride sets an override value for a key in the cache.
// This is useful for testing or for providing computed values.
func SetOverride(key, value string) {
	initCache()
	cacheMu.Lock()
	defer cacheMu.Unlock()
	cache[key] = value
}

// ClearCache clears the environment cache (primarily for testing).
func ClearCache() {
	cacheMu.Lock()
	defer cacheMu.Unlock()
	cache = nil
	cacheOnce = sync.Once{}
}
