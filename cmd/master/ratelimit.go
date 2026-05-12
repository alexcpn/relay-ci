package main

import (
	"net/http"
	"strconv"
	"sync"
	"time"
)

// rateLimiter is a per-key token-bucket rate limiter.
// Configured via WEBHOOK_RATE_LIMIT (requests per second, default 10)
// and WEBHOOK_RATE_BURST (burst size, default 20).
type rateLimiter struct {
	mu      sync.Mutex
	buckets map[string]*bucket
	rate    float64 // tokens per second
	burst   float64
}

type bucket struct {
	tokens    float64
	lastRefil time.Time
}

func newRateLimiter(ratePerSec, burst float64) *rateLimiter {
	rl := &rateLimiter{
		buckets: make(map[string]*bucket),
		rate:    ratePerSec,
		burst:   burst,
	}
	// Prune stale buckets every 5 minutes.
	go func() {
		for range time.Tick(5 * time.Minute) {
			rl.prune()
		}
	}()
	return rl
}

func newRateLimiterFromEnv() *rateLimiter {
	rate := parseFloat(getEnvOrDefault("WEBHOOK_RATE_LIMIT", "10"))
	burst := parseFloat(getEnvOrDefault("WEBHOOK_RATE_BURST", "20"))
	return newRateLimiter(rate, burst)
}

// Allow returns true if the key is within the rate limit.
func (rl *rateLimiter) Allow(key string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	b, ok := rl.buckets[key]
	if !ok {
		b = &bucket{tokens: rl.burst, lastRefil: now}
		rl.buckets[key] = b
	}

	// Refill tokens since last check.
	elapsed := now.Sub(b.lastRefil).Seconds()
	b.tokens += elapsed * rl.rate
	if b.tokens > rl.burst {
		b.tokens = rl.burst
	}
	b.lastRefil = now

	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

func (rl *rateLimiter) prune() {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	cutoff := time.Now().Add(-10 * time.Minute)
	for k, b := range rl.buckets {
		if b.lastRefil.Before(cutoff) {
			delete(rl.buckets, k)
		}
	}
}

// Middleware wraps an http.Handler with per-IP rate limiting.
func (rl *rateLimiter) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := clientIP(r)
		if !rl.Allow(key) {
			http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// clientIP extracts the real client IP, respecting X-Forwarded-For from
// trusted proxies. For simplicity we use the first entry.
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		// Take the first (client) IP from the chain.
		if idx := len(xff); idx > 0 {
			for i, c := range xff {
				if c == ',' {
					return xff[:i]
				}
			}
			return xff
		}
	}
	// Strip port from RemoteAddr.
	addr := r.RemoteAddr
	for i := len(addr) - 1; i >= 0; i-- {
		if addr[i] == ':' {
			return addr[:i]
		}
	}
	return addr
}

func parseFloat(s string) float64 {
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || v <= 0 {
		return 10
	}
	return v
}

func getEnvOrDefault(key, def string) string {
	return envOrDefault(key, def)
}
