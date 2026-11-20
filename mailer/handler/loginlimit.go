package handler

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	defaultLoginAttemptLimit  = 10
	defaultLoginAttemptWindow = time.Minute
)

// loginLimiter is a fixed-window limiter for failed login attempts. It keeps
// two independent buckets per attempt, one for the client IP and one for the
// account, so neither spraying many accounts from one host nor hammering one
// account from many hosts is unbounded.
//
// The limiter is process-local. Deployments running several instances should
// still rate limit at the edge; this closes the unbounded path within an
// instance and keeps the login endpoint cheap to protect.
type loginLimiter struct {
	mu      sync.Mutex
	limit   int
	window  time.Duration
	now     func() time.Time
	buckets map[string]loginAttempts
	ops     int
}

type loginAttempts struct {
	start time.Time
	count int
}

func newLoginLimiter(limit int, window time.Duration) *loginLimiter {
	if limit <= 0 {
		limit = defaultLoginAttemptLimit
	}
	if window <= 0 {
		window = defaultLoginAttemptWindow
	}
	return &loginLimiter{
		limit:   limit,
		window:  window,
		now:     time.Now,
		buckets: make(map[string]loginAttempts),
	}
}

// Allow reports whether another attempt for key is within the limit. It does
// not count the attempt; call Record for a failed login.
func (l *loginLimiter) Allow(key string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	attempts := l.current(key, now)
	if attempts.count >= l.limit {
		return false, l.window - now.Sub(attempts.start)
	}
	return true, 0
}

// Record counts one failed login attempt for key.
func (l *loginLimiter) Record(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()

	attempts := l.current(key, l.now())
	attempts.count++
	l.buckets[key] = attempts
}

// Reset drops the bucket for key. A successful login clears the per-account
// bucket so earlier failures do not lock out the account owner.
func (l *loginLimiter) Reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.buckets, key)
}

// current returns the live window for key, starting a new one when the
// previous window has elapsed. The caller must hold l.mu.
func (l *loginLimiter) current(key string, now time.Time) loginAttempts {
	attempts := l.buckets[key]
	if attempts.start.IsZero() || now.Sub(attempts.start) >= l.window {
		attempts = loginAttempts{start: now}
		l.buckets[key] = attempts
	}
	// Prune periodically so a stream of unique IPs cannot grow the map
	// without bound.
	l.ops++
	if l.ops%1024 == 0 {
		for k, existing := range l.buckets {
			if now.Sub(existing.start) >= l.window {
				delete(l.buckets, k)
			}
		}
	}
	return attempts
}

// loginIPKey returns the IP bucket key. The leftmost X-Forwarded-For value is
// used when present because deployments behind a proxy see the proxy address
// as the peer. The per-account bucket is independent of this value, so a
// spoofed forwarded-for header cannot bypass the account limit.
func loginIPKey(r *http.Request) string {
	if forwarded := r.Header.Get("X-Forwarded-For"); forwarded != "" {
		if first := strings.TrimSpace(strings.SplitN(forwarded, ",", 2)[0]); first != "" {
			return "ip:" + first
		}
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return "ip:" + host
	}
	return "ip:" + r.RemoteAddr
}

func loginUserKey(username string) string {
	return "user:" + strings.ToLower(strings.TrimSpace(username))
}
