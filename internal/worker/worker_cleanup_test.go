package worker

// worker_cleanup_test.go — retention sweep regressions. The integration test
// runs only when AGENTCHUNZHI_TEST_DATABASE_URL points at a migrated
// database (same gate as internal/agentruntime/checkpoint/postgres_test.go).

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"testing"

	"agentchunzhi/internal/store"

	"github.com/google/uuid"
)

func TestSweepExpiredRowsRequiresStore(t *testing.T) {
	if _, err := SweepExpiredRows(context.Background(), nil); err == nil {
		t.Fatal("nil store must fail")
	}
	if _, err := SweepExpiredRows(context.Background(), &store.Store{}); err == nil {
		t.Fatal("store without pool must fail")
	}
	worker := &ExpiredRowsWorker{}
	if err := worker.Work(context.Background(), nil); err == nil {
		t.Fatal("worker without store must fail")
	}
}

func TestSweepExpiredRowsIntegration(t *testing.T) {
	databaseURL := os.Getenv("AGENTCHUNZHI_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("AGENTCHUNZHI_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	database, err := store.Open(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(database.Close)
	var organizationID, userID string
	if err := database.Pool.QueryRow(ctx, `
		SELECT u.organization_id::text, u.id::text
		FROM identity.users u
		JOIN organization.organizations o ON o.id = u.organization_id
		WHERE u.user_type = 'member' AND u.status = 'active' AND o.status = 'active'
		ORDER BY u.created_at
		LIMIT 1
	`).Scan(&organizationID, &userID); err != nil {
		t.Fatalf("load retention sweep scope: %v", err)
	}

	expiredKey := "sweep-expired-" + uuid.NewString()
	liveKey := "sweep-live-" + uuid.NewString()
	if _, err := database.Pool.Exec(ctx, `
		INSERT INTO system.idempotency_keys (organization_id, subject_id, operation, idempotency_key, request_hash, expires_at)
		VALUES
		  ($1::uuid, $2::uuid, 'sweep.itest', $3, 'expired-hash', now() - interval '1 hour'),
		  ($1::uuid, $2::uuid, 'sweep.itest', $4, 'live-hash', now() + interval '12 hours')
	`, organizationID, userID, expiredKey, liveKey); err != nil {
		t.Fatalf("seed idempotency keys: %v", err)
	}

	deadToken := "dead-" + uuid.NewString()
	liveToken := "live-" + uuid.NewString()
	deadHash := sha256hex(deadToken)
	liveHash := sha256hex(liveToken)
	if _, err := database.Pool.Exec(ctx, `
		INSERT INTO identity.sessions (user_id, token_hash, idle_expires_at, absolute_expires_at)
		VALUES
		  ($1::uuid, $2, now() - interval '8 days', now() - interval '8 days'),
		  ($1::uuid, $3, now() + interval '1 hour', now() + interval '1 day')
	`, userID, deadHash, liveHash); err != nil {
		t.Fatalf("seed sessions: %v", err)
	}

	staleBucket := randomBucketKey()
	blockedBucket := randomBucketKey()
	if _, err := database.Pool.Exec(ctx, `
		INSERT INTO security.auth_rate_limits (bucket_type, key_hash, window_started_at, attempt_count, blocked_until)
		VALUES
		  ('login_ip', $1, now() - interval '2 days', 3, NULL),
		  ('login_ip', $2, now() - interval '2 days', 5, now() + interval '1 hour')
	`, staleBucket, blockedBucket); err != nil {
		t.Fatalf("seed rate limit buckets: %v", err)
	}

	t.Cleanup(func() {
		_, _ = database.Pool.Exec(ctx, `DELETE FROM system.idempotency_keys WHERE idempotency_key IN ($1::text,$2::text)`, expiredKey, liveKey)
		_, _ = database.Pool.Exec(ctx, `DELETE FROM identity.sessions WHERE token_hash IN ($1::text,$2::text)`, deadHash, liveHash)
		_, _ = database.Pool.Exec(ctx, `DELETE FROM security.auth_rate_limits WHERE key_hash IN ($1::text,$2::text)`, staleBucket, blockedBucket)
	})

	swept, err := SweepExpiredRows(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	if swept[0] < 1 || swept[1] < 1 || swept[2] < 1 {
		t.Fatalf("sweep deleted nothing: idempotency_keys=%d sessions=%d auth_rate_limits=%d", swept[0], swept[1], swept[2])
	}

	var remaining int
	if err := database.Pool.QueryRow(ctx, `SELECT count(*) FROM system.idempotency_keys WHERE idempotency_key=$1`, expiredKey).Scan(&remaining); err != nil || remaining != 0 {
		t.Fatalf("expired idempotency key survived: count=%d err=%v", remaining, err)
	}
	if err := database.Pool.QueryRow(ctx, `SELECT count(*) FROM system.idempotency_keys WHERE idempotency_key=$1`, liveKey).Scan(&remaining); err != nil || remaining != 1 {
		t.Fatalf("unexpired idempotency key must survive: count=%d err=%v", remaining, err)
	}
	if err := database.Pool.QueryRow(ctx, `SELECT count(*) FROM identity.sessions WHERE token_hash=$1`, deadHash).Scan(&remaining); err != nil || remaining != 0 {
		t.Fatalf("long-dead session survived: count=%d err=%v", remaining, err)
	}
	if err := database.Pool.QueryRow(ctx, `SELECT count(*) FROM identity.sessions WHERE token_hash=$1`, liveHash).Scan(&remaining); err != nil || remaining != 1 {
		t.Fatalf("live session must survive: count=%d err=%v", remaining, err)
	}
	if err := database.Pool.QueryRow(ctx, `SELECT count(*) FROM security.auth_rate_limits WHERE key_hash=$1`, staleBucket).Scan(&remaining); err != nil || remaining != 0 {
		t.Fatalf("stale rate limit bucket survived: count=%d err=%v", remaining, err)
	}
	if err := database.Pool.QueryRow(ctx, `SELECT count(*) FROM security.auth_rate_limits WHERE key_hash=$1`, blockedBucket).Scan(&remaining); err != nil || remaining != 1 {
		t.Fatalf("bucket with an active block must survive: count=%d err=%v", remaining, err)
	}
}

func sha256hex(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func randomBucketKey() string {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "bucket-" + uuid.NewString()
	}
	return hex.EncodeToString(raw)
}
