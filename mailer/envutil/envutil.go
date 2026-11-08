package envutil

import (
	"context"
	"os"
	"strings"
	"sync"

	"github.com/kncept/Emissary/mailer/awsutil"
)

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

// GetCachedEnv retrieves an environment variable value, using a local cache.
// If the value starts with an AWS ARN (arn:aws:...), it resolves the secret
// using awsutil.ResolveSecret.
//
// The context is passed to the secret resolution call for timeout/cancellation
// support.
func GetCachedEnv(ctx context.Context, key string) string {
	initCache()

	// Fast path: check cache first (read lock)
	cacheMu.RLock()
	val, ok := cache[key]
	cacheMu.RUnlock()

	if ok {
		return resolveIfARN(ctx, val)
	}

	// Not in cache, check os.Getenv (handles updates after init)
	val, ok = os.LookupEnv(key)
	if ok {
		// Update cache for next time (write lock)
		cacheMu.Lock()
		cache[key] = val
		cacheMu.Unlock()
		return resolveIfARN(ctx, val)
	}

	return ""
}

// resolveIfARN checks if the value is an AWS ARN and resolves it if so.
func resolveIfARN(ctx context.Context, val string) string {
	val = strings.TrimSpace(val)
	if strings.HasPrefix(val, "arn:aws:") {
		resolved, err := awsutil.ResolveSecret(ctx, val)
		if err != nil {
			// Log error but return original value to avoid breaking existing behavior
			// In production, you might want to handle this differently
			return val
		}
		return resolved
	}
	return val
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