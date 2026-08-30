package auth

// login_throttle.go — PostgreSQL-backed authentication rate limiting shared
// by all instances. Buckets are HMAC'd so raw emails and IP prefixes never
// reach the table.

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"time"

	"agentchunzhi/internal/store"
)

// Bucket types fixed by the phase 1 contract; public_site_ip is the phase 5
// anonymous public-site visitor bucket (doc phase 5 B5).
const (
	BucketLoginEmail   = "login_email"
	BucketLoginIP      = "login_ip"
	BucketResetEmail   = "password_reset_email"
	BucketResetIP      = "password_reset_ip"
	BucketInvitationIP = "invitation_ip"
	BucketPublicSiteIP = "public_site_ip"
)

// Default windows from the contract.
var (
	LoginEmailLimit   = RatePolicy{Window: 15 * time.Minute, Max: 5, Block: 15 * time.Minute}
	LoginIPLimit      = RatePolicy{Window: 15 * time.Minute, Max: 30, Block: 15 * time.Minute}
	ResetEmailLimit   = RatePolicy{Window: time.Hour, Max: 3, Block: time.Hour}
	ResetIPLimit      = RatePolicy{Window: 15 * time.Minute, Max: 20, Block: 15 * time.Minute}
	InvitationIPLimit = RatePolicy{Window: 15 * time.Minute, Max: 20, Block: 15 * time.Minute}
	// PublicSiteIPLimit is the phase 5 public-face budget: 120 requests per
	// minute per client address prefix, blocked for one minute once exceeded.
	PublicSiteIPLimit = RatePolicy{Window: time.Minute, Max: 120, Block: time.Minute}
)

type RatePolicy struct {
	Window time.Duration
	Max    int
	Block  time.Duration
}

type LoginThrottle struct {
	Store   *store.Store
	HMACKey []byte
	Now     func() time.Time
}

func NewLoginThrottle(store *store.Store, hmacKey []byte) *LoginThrottle {
	return &LoginThrottle{Store: store, HMACKey: hmacKey, Now: time.Now}
}

func (t *LoginThrottle) key(bucketType, value string) string {
	if t.HMACKey == nil {
		panic("rate limit hmac key is required")
	}
	mac := hmac.New(sha256.New, t.HMACKey)
	_, _ = mac.Write([]byte(bucketType + "\x00" + value))
	return hex.EncodeToString(mac.Sum(nil))
}

// CheckAndIncrement records one failure attempt and reports whether the
// bucket is blocked. A blocked result carries the retry-after duration.
func (t *LoginThrottle) CheckAndIncrement(ctx context.Context, bucketType, value string, policy RatePolicy) (allowed bool, retryAfter time.Duration, err error) {
	if t.Store == nil || t.Store.Pool == nil {
		return false, 0, errors.New("database store is not initialized")
	}
	now := t.Now()
	keyHash := t.key(bucketType, value)
	var allowedFlag bool
	var retrySeconds float64
	err = t.Store.Pool.QueryRow(ctx, `
		WITH upsert AS (
			INSERT INTO security.auth_rate_limits
				(bucket_type, key_hash, window_started_at, attempt_count, blocked_until, updated_at)
			VALUES ($1, $2, $3, 1, NULL, $3)
			ON CONFLICT (bucket_type, key_hash) DO UPDATE
			SET window_started_at = CASE
					 WHEN security.auth_rate_limits.window_started_at < $3 - make_interval(secs => $4::double precision)
					 THEN $3 ELSE security.auth_rate_limits.window_started_at END,
			    attempt_count = CASE
				 WHEN security.auth_rate_limits.window_started_at < $3 - make_interval(secs => $4::double precision)
				 THEN 1 ELSE security.auth_rate_limits.attempt_count + 1 END,
			    blocked_until = CASE
				 WHEN security.auth_rate_limits.blocked_until IS NOT NULL AND security.auth_rate_limits.blocked_until > $3
				 THEN security.auth_rate_limits.blocked_until
				 WHEN security.auth_rate_limits.window_started_at >= $3 - make_interval(secs => $4::double precision)
				      AND security.auth_rate_limits.attempt_count + 1 > $5
				 THEN $3 + make_interval(secs => $6::double precision)
				 ELSE NULL END,
			    updated_at = $3
			RETURNING (blocked_until IS NULL OR blocked_until <= $3) AS allowed,
			          COALESCE(EXTRACT(EPOCH FROM (blocked_until - $3)), 0) AS retry
		)
		SELECT allowed, retry FROM upsert
	`, bucketType, keyHash, now, policy.Window.Seconds(), policy.Max, policy.Block.Seconds()).Scan(&allowedFlag, &retrySeconds)
	if err != nil {
		return false, 0, fmt.Errorf("rate limit bucket: %w", err)
	}
	return allowedFlag, time.Duration(retrySeconds * float64(time.Second)), nil
}

// Clear resets the failure counter after a successful login (email bucket
// only; the IP bucket is intentionally preserved).
func (t *LoginThrottle) Clear(ctx context.Context, bucketType, value string) error {
	_, err := t.Store.Pool.Exec(ctx, `
		DELETE FROM security.auth_rate_limits WHERE bucket_type = $1 AND key_hash = $2
	`, bucketType, t.key(bucketType, value))
	return err
}

// ClientIPPrefix derives the truncated client key: /24 for IPv4, /56 for
// IPv6. Forwarded headers are only honored when remoteAddr is a trusted
// proxy; the caller resolves that and passes the effective address.
func ClientIPPrefix(effectiveAddr string) string {
	host, _, err := net.SplitHostPort(effectiveAddr)
	if err != nil {
		host = effectiveAddr
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return "invalid"
	}
	if v4 := ip.To4(); v4 != nil {
		return v4.Mask(net.CIDRMask(24, 32)).String()
	}
	return ip.Mask(net.CIDRMask(56, 128)).String()
}

// bucketCounter is the shared DB-backed bucket primitive. *LoginThrottle
// implements it; the interface exists so the public-site limiter can be
// unit-tested against a fake counter without a database.
type bucketCounter interface {
	CheckAndIncrement(ctx context.Context, bucketType, value string, policy RatePolicy) (bool, time.Duration, error)
}

var _ bucketCounter = (*LoginThrottle)(nil)

// PublicSiteIPThrottle is the anonymous public-site visitor rate limiter
// (phase 5 B5): one shared budget per client address prefix across all public
// site reads, 120 requests per minute. It reuses the HMAC'd
// security.auth_rate_limits buckets (bucket_type='public_site_ip') so the
// limit is shared by every instance behind the deployment. Unlike the login
// buckets there is no success/failure distinction: every public request
// counts and nothing is cleared afterwards. P5-3 wires this onto the public
// routes; the capability and its tests live here.
type PublicSiteIPThrottle struct {
	Counter bucketCounter
}

// NewPublicSiteIPThrottle wires the limiter onto the shared DB bucket store.
func NewPublicSiteIPThrottle(store *store.Store, hmacKey []byte) *PublicSiteIPThrottle {
	return &PublicSiteIPThrottle{Counter: NewLoginThrottle(store, hmacKey)}
}

// Allow records one public read for the effective client address and reports
// whether the request may proceed. A refused result carries the retry-after
// duration. The address is truncated through ClientIPPrefix so a /24 (IPv4)
// or /56 (IPv6) shares one bucket, matching the login IP buckets.
func (t *PublicSiteIPThrottle) Allow(ctx context.Context, effectiveAddr string) (bool, time.Duration, error) {
	return t.Counter.CheckAndIncrement(ctx, BucketPublicSiteIP, ClientIPPrefix(effectiveAddr), PublicSiteIPLimit)
}
