package httpapi

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Simple in-process token bucket rate limiting for the two highest-abuse
// surfaces: session login (brute force) and the open API (fair use). Buckets
// are per client key; capacity resets via continuous refill. Configuration is
// optional — see RATE_LIMIT_* in .env.example.

type rateBucket struct {
	tokens float64
	last   time.Time
}

const (
	defaultLoginRatePerMinute     = 10
	defaultLoginBackstopPerMinute = 90
	defaultOpenRatePerMinute      = 60

	bucketIdleTTL = 30 * time.Minute
	sweepInterval = 5 * time.Minute
	maxBuckets    = 65536 // hard cap; beyond it the sweep runs and fresh unknown keys are refused
)

type rateLimiter struct {
	mu        sync.Mutex
	buckets   map[string]*rateBucket
	now       func() time.Time
	lastSweep time.Time
}

func newRateLimiter() *rateLimiter {
	return &rateLimiter{buckets: map[string]*rateBucket{}, now: time.Now}
}

// allow consumes one token, refilling first. It never blocks: callers turn a
// false return into 429 immediately.
func (l *rateLimiter) allow(key string, limitPerMinute int) bool {
	if limitPerMinute <= 0 {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()

	if now.Sub(l.lastSweep) >= sweepInterval || len(l.buckets) >= maxBuckets {
		l.sweepLocked(now)
	}
	refillPerSec := float64(limitPerMinute) / 60.0
	bucket, ok := l.buckets[key]
	if !ok {
		if len(l.buckets) >= maxBuckets {
			return false
		}
		bucket = &rateBucket{tokens: float64(limitPerMinute), last: now}
		l.buckets[key] = bucket
	}
	bucket.tokens = math.Min(float64(limitPerMinute), bucket.tokens+now.Sub(bucket.last).Seconds()*refillPerSec)
	bucket.last = now
	if bucket.tokens < 1 {
		return false
	}
	bucket.tokens -= 1
	return true
}

// sweepLocked drops buckets idle for longer than bucketIdleTTL. It is O(n) and
// runs at most once per sweepInterval (or when the map hits maxBuckets),
// bounding memory even under unique attacker-chosen keys.
func (l *rateLimiter) sweepLocked(now time.Time) {
	l.lastSweep = now
	for key, bucket := range l.buckets {
		if now.Sub(bucket.last) > bucketIdleTTL {
			delete(l.buckets, key)
		}
	}
}

var sharedRateLimiter = newRateLimiter()

func envRateLimit(name string, fallback int) int {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}

// trustForwardedFor reports whether operators vouch for the proxy chain that
// sets X-Forwarded-For. Off by default so an anonymous caller cannot rotate a
// spoofed header to sidestep per-address buckets.
func trustForwardedFor() bool {
	return strings.EqualFold(strings.TrimSpace(os.Getenv("RATE_LIMIT_TRUST_XFF")), "true")
}

const trackedDigestBytes = 32

// stableClientKey hashes caller-controlled identifiers before they become map
// keys, bounding retained bytes and keeping raw credentials out of memory.
func stableClientKey(identifier string) string {
	sum := sha256.Sum256([]byte(identifier))
	return hex.EncodeToString(sum[:trackedDigestBytes])
}

// rateLimitMiddleware applies selective token-bucket limits on top of an
// existing handler chain. Health/public endpoints are intentionally exempt.
func rateLimitMiddleware(next http.Handler) http.Handler {
	loginLimit := envRateLimit("RATE_LIMIT_LOGIN_PER_MIN", defaultLoginRatePerMinute)
	loginBackstopLimit := envRateLimit("RATE_LIMIT_LOGIN_IP_PER_MIN", defaultLoginBackstopPerMinute)
	openLimit := envRateLimit("RATE_LIMIT_OPEN_PER_MIN", defaultOpenRatePerMinute)
	trustXFF := trustForwardedFor()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/sessions":
			addr := socketAddr(r, trustXFF)
			// Coarse per-address backstop caps total credential traffic; the
			// fine per-(address, login_name) bucket stops single-account brute
			// force without penalizing legitimate multi-user clients.
			if !sharedRateLimiter.allow("login-ip:"+addr, loginBackstopLimit) {
				w.Header().Set("Retry-After", "30")
				writeError(w, http.StatusTooManyRequests, "rate_limited")
				return
			}
			loginName := peekLoginName(r)
			if !sharedRateLimiter.allow(stableClientKey("login:"+addr+":"+loginName), loginLimit) {
				w.Header().Set("Retry-After", "30")
				writeError(w, http.StatusTooManyRequests, "rate_limited")
				return
			}
		case strings.HasPrefix(r.URL.Path, "/api/open/"):
			key := "open:" + socketAddr(r, trustXFF)
			if bearer := bearerToken(r); bearer != "" {
				// Bucket by credential digest so one noisy agent key cannot
				// exhaust the budget shared with other callers behind the same
				// address; hashing keeps secrets out of memory.
				key = stableClientKey(bearer)
			}
			if !sharedRateLimiter.allow(key, openLimit) {
				w.Header().Set("Retry-After", "10")
				writeError(w, http.StatusTooManyRequests, "rate_limited")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func socketAddr(r *http.Request, trustXFF bool) string {
	if trustXFF {
		if forwarded := strings.TrimSpace(r.Header.Get("X-Forwarded-For")); forwarded != "" {
			parts := strings.Split(forwarded, ",")
			first := strings.TrimSpace(parts[0])
			if first != "" && len(first) < 256 {
				return first
			}
		}
	}
	if addr := strings.TrimSpace(r.RemoteAddr); addr != "" {
		if idx := strings.LastIndex(addr, ":"); idx > 0 {
			return addr[:idx]
		}
		return addr
	}
	return "unknown"
}

func bearerToken(r *http.Request) string {
	header := strings.TrimSpace(r.Header.Get("Authorization"))
	if len(header) > 8 && strings.EqualFold(header[:7], "Bearer ") {
		value := strings.TrimSpace(header[7:])
		if value != "" && len(value) <= 256 {
			return value
		}
	}
	return ""
}

// peekLoginName extracts login_name from a small JSON login body for bucket
// keying. The body is read bounded and restored so handlers see it unchanged;
// malformed bodies simply yield an empty name.
func peekLoginName(r *http.Request) string {
	if r.Body == nil || r.Body == http.NoBody {
		return ""
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, 4096))
	if err != nil {
		r.Body.Close()
		r.Body = http.NoBody
		return ""
	}
	r.Body.Close()
	r.Body = io.NopCloser(bytes.NewReader(data))
	var payload struct {
		LoginName string `json:"login_name"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(payload.LoginName))
}
