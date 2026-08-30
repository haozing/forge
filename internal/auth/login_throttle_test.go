package auth

// login_throttle_test.go — unit coverage for the phase 5 public-site visitor
// limiter. The SQL bucket upsert itself needs a real database and is covered
// by the phase 5 real-database checklist; these tests pin the wiring: bucket
// type, address-prefix keying, the 120/min policy and result passthrough.

import (
	"context"
	"errors"
	"testing"
	"time"
)

// recordingCounter captures one bucket request so tests can assert how the
// public-site limiter keys and budgets requests without a database.
type recordingCounter struct {
	bucket string
	value  string
	policy RatePolicy
	allow  bool
	retry  time.Duration
	err    error
}

func (c *recordingCounter) CheckAndIncrement(_ context.Context, bucketType, value string, policy RatePolicy) (bool, time.Duration, error) {
	c.bucket, c.value, c.policy = bucketType, value, policy
	return c.allow, c.retry, c.err
}

func TestPublicSiteIPThrottleKeysAndBudget(t *testing.T) {
	rec := &recordingCounter{allow: true}
	throttle := &PublicSiteIPThrottle{Counter: rec}

	allowed, retry, err := throttle.Allow(context.Background(), "203.0.113.7:51234")
	if err != nil || !allowed || retry != 0 {
		t.Fatalf("first request must pass: allowed=%v retry=%v err=%v", allowed, retry, err)
	}
	if rec.bucket != BucketPublicSiteIP {
		t.Fatalf("bucket type = %q, want %q", rec.bucket, BucketPublicSiteIP)
	}
	if rec.bucket != "public_site_ip" {
		t.Fatalf("bucket type %q must match the security.auth_rate_limits CHECK value", rec.bucket)
	}
	// IPv4 addresses share one /24 bucket, matching the login IP buckets.
	if rec.value != "203.0.113.0" {
		t.Fatalf("bucket key = %q, want the IPv4 /24 prefix", rec.value)
	}
	if rec.policy.Max != 120 || rec.policy.Window != time.Minute {
		t.Fatalf("policy = %#v, want the 120/min public-face budget", rec.policy)
	}

	// IPv6 addresses mask to a /56 prefix: the low 8 bits of the fourth group
	// are dropped, the high 8 bits survive (0x02ab -> 0x0200).
	if _, _, err := throttle.Allow(context.Background(), "[2001:db8:1:2ab:3:4:5:6]:443"); err != nil {
		t.Fatalf("ipv6 request: %v", err)
	}
	if rec.value != "2001:db8:1:200::" {
		t.Fatalf("ipv6 bucket key = %q, want the /56 prefix", rec.value)
	}
}

func TestPublicSiteIPThrottlePassesRefusalThrough(t *testing.T) {
	rec := &recordingCounter{allow: false, retry: 42 * time.Second}
	throttle := &PublicSiteIPThrottle{Counter: rec}

	allowed, retry, err := throttle.Allow(context.Background(), "198.51.100.9:8080")
	if allowed || err != nil {
		t.Fatalf("refusal must pass through: allowed=%v err=%v", allowed, err)
	}
	if retry != 42*time.Second {
		t.Fatalf("retry-after = %v, want the counter's value", retry)
	}

	rec.err = errors.New("bucket down")
	if _, _, err := throttle.Allow(context.Background(), "198.51.100.9:8080"); err == nil {
		t.Fatal("counter errors must propagate to the caller")
	}
}

// TestLoginThrottleImplementsBucketCounter guards the delegation used by
// NewPublicSiteIPThrottle.
func TestLoginThrottleImplementsBucketCounter(t *testing.T) {
	var _ bucketCounter = &LoginThrottle{}
}
