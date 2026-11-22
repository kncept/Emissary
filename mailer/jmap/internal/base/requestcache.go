package base

import (
	"context"
	"sync"
)

// WithRequestCache attaches an empty per-request memo to ctx. A JMAP request is
// a batch of method calls, and several calls in one batch often need the same
// storage-backed snapshot. The cache lives only for the duration of the batch,
// so a following request always sees fresh data.
func WithRequestCache(ctx context.Context) context.Context {
	return context.WithValue(ctx, requestCacheKey{}, &requestCache{values: make(map[any]any)})
}

type requestCacheKey struct{}

type requestCache struct {
	mu     sync.Mutex
	values map[any]any
}

type cachedValue[T any] struct {
	value T
	err   error
}

// Cached returns the value produced by load for key on the first call within
// this request. Without WithRequestCache it simply calls load, which keeps the
// helper safe for callers outside the JMAP dispatcher (for example push timers).
func Cached[T any](ctx context.Context, key any, load func() (T, error)) (T, error) {
	cache, _ := ctx.Value(requestCacheKey{}).(*requestCache)
	if cache == nil {
		return load()
	}

	cache.mu.Lock()
	raw, found := cache.values[key]
	cache.mu.Unlock()
	if found {
		if hit, ok := raw.(cachedValue[T]); ok {
			return hit.value, hit.err
		}
	}

	value, err := load()
	cache.mu.Lock()
	cache.values[key] = cachedValue[T]{value: value, err: err}
	cache.mu.Unlock()
	return value, err
}
