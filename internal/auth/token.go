// Package auth provides bearer-token HTTP middleware.
package auth

import (
	"crypto/subtle"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	maxFailures   = 10
	failureWindow = time.Minute
	maxTrackedIPs = 10000
)

// failureLimiter throttles repeated wrong-token attempts per client IP so the
// token cannot be brute-forced at wire speed.
type failureLimiter struct {
	mu    sync.Mutex
	fails map[string]failureRecord
}

type failureRecord struct {
	count       int
	windowStart time.Time
}

func (l *failureLimiter) blocked(ip string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	rec, ok := l.fails[ip]
	if !ok || now.Sub(rec.windowStart) >= failureWindow {
		return false
	}
	return rec.count >= maxFailures
}

func (l *failureLimiter) recordFailure(ip string, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.fails) >= maxTrackedIPs {
		// Crude but bounded: a flood of distinct IPs resets everyone's
		// window rather than growing without limit.
		l.fails = map[string]failureRecord{}
	}
	rec, ok := l.fails[ip]
	if !ok || now.Sub(rec.windowStart) >= failureWindow {
		rec = failureRecord{windowStart: now}
	}
	rec.count++
	l.fails[ip] = rec
}

func (l *failureLimiter) reset(ip string) {
	l.mu.Lock()
	delete(l.fails, ip)
	l.mu.Unlock()
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// Bearer returns middleware that requires Authorization: Bearer <expected>.
// Comparison is constant-time, and repeated wrong-token attempts from one IP
// are rejected with 429 for a cooldown window. An empty expected token panics
// at construction time — a missing token is a startup error, not a runtime 401.
func Bearer(expected string) func(http.Handler) http.Handler {
	if expected == "" {
		panic("auth.Bearer: empty token")
	}
	expectedBytes := []byte(expected)
	limiter := &failureLimiter{fails: map[string]failureRecord{}}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := r.Header.Get("Authorization")
			const prefix = "Bearer "
			if !strings.HasPrefix(h, prefix) {
				w.Header().Set("WWW-Authenticate", `Bearer realm="funnel-manager"`)
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			ip := clientIP(r)
			if limiter.blocked(ip, time.Now()) {
				http.Error(w, "too many failed attempts", http.StatusTooManyRequests)
				return
			}
			got := []byte(strings.TrimPrefix(h, prefix))
			if subtle.ConstantTimeCompare(got, expectedBytes) != 1 {
				limiter.recordFailure(ip, time.Now())
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			limiter.reset(ip)
			next.ServeHTTP(w, r)
		})
	}
}
