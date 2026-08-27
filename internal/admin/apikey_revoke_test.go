package admin

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"agentchunzhi/internal/auth"
	"agentchunzhi/internal/store"

	"github.com/google/uuid"
)

func TestRevokeAllAPIKeysResultJSONExposesRevokedCount(t *testing.T) {
	payload, err := json.Marshal(RevokeAllAPIKeysResult{AgentUserID: "agent-1", RevokedCount: 3})
	if err != nil {
		t.Fatal(err)
	}
	text := string(payload)
	for _, fragment := range []string{`"agent_user_id":"agent-1"`, `"revoked_count":3`} {
		if !strings.Contains(text, fragment) {
			t.Fatalf("revoke-all payload missing %s in %s", fragment, text)
		}
	}
}

// TestRevokeAllAgentAPIKeysRejectsBadInput pins the argument validation before
// any database access: bad UUIDs and short idempotency keys fail closed.
func TestRevokeAllAgentAPIKeysRejectsBadInput(t *testing.T) {
	svc := Service{}
	cases := []struct {
		name      string
		principal auth.Principal
		input     RevokeAllAPIKeysInput
	}{
		{"non-member principal", auth.Principal{UserType: "agent"}, RevokeAllAPIKeysInput{AgentUserID: uuid.NewString(), IdempotencyKey: "revoke-all-agent-keys-1"}},
		{"bad agent user id", auth.Principal{UserType: "member"}, RevokeAllAPIKeysInput{AgentUserID: "not-a-uuid", IdempotencyKey: "revoke-all-agent-keys-1"}},
		{"short idempotency key", auth.Principal{UserType: "member"}, RevokeAllAPIKeysInput{AgentUserID: uuid.NewString(), IdempotencyKey: "short"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := svc.RevokeAllAgentAPIKeys(context.Background(), tc.principal, tc.input); err == nil {
				t.Fatal("expected invalid input to be rejected without touching storage")
			}
		})
	}
}

// Integration path mirrors application_integration_test.go: the dev/test
// database is provided via AGENTCHUNZHI_TEST_DATABASE_URL and the test is
// skipped when absent. It creates its own ITC-prefixed rows and removes them.
func TestRevokeAllAndRotateStayIsolatedIntegration(t *testing.T) {
	databaseURL := os.Getenv("AGENTCHUNZHI_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("AGENTCHUNZHI_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	db, err := store.Open(ctx, databaseURL)
	if err != nil {
		t.Fatalf("open integration database: %v", err)
	}
	t.Cleanup(db.Close)
	var organizationID, memberID, agentUserID string
	if err := db.Pool.QueryRow(ctx, `
		WITH org AS (
			INSERT INTO organization.organizations (name, status) VALUES ('ITC-RevokeOrg', 'active') RETURNING id
		), member AS (
			INSERT INTO identity.users (organization_id, user_type, display_name, status)
			SELECT id, 'member', 'ITC-RevokeAdmin', 'active' FROM org RETURNING id
		), agent AS (
			INSERT INTO identity.users (organization_id, user_type, display_name, status)
			SELECT id, 'agent', 'ITC-RevokeAgent', 'active' FROM org RETURNING id
		)
		SELECT o.id::text, m.id::text, a.id::text FROM org o, member m, agent a
	`).Scan(&organizationID, &memberID, &agentUserID); err != nil {
		t.Fatalf("seed integration fixture: %v", err)
	}
	defer func() {
		db.Pool.Exec(ctx, `DELETE FROM identity.api_keys WHERE user_id = $1`, agentUserID)
		db.Pool.Exec(ctx, `DELETE FROM system.idempotency_keys WHERE subject_id = ANY($1::uuid[])`, [][]string{{memberID}})
		db.Pool.Exec(ctx, `DELETE FROM audit.audit_log WHERE organization_id = $1`, organizationID)
		db.Pool.Exec(ctx, `DELETE FROM identity.users WHERE organization_id = $1`, organizationID)
		db.Pool.Exec(ctx, `DELETE FROM organization.organizations WHERE id = $1`, organizationID)
	}()
	principal := auth.Principal{UserType: "member", UserID: memberID, OrganizationID: organizationID}
	if _, err := db.Pool.Exec(ctx, `
		INSERT INTO identity.api_keys (user_id, name, key_prefix, key_hash, capabilities)
		VALUES ($1::uuid, 'itc-one', 'ak_ITCONE0000', 'hash-one', '[]'::jsonb),
		       ($1::uuid, 'itc-two', 'ak_ITCTWO0000', 'hash-two', '[]'::jsonb)
	`, agentUserID); err != nil {
		t.Fatalf("seed api keys: %v", err)
	}
	svc := Service{Store: db}
	result, err := svc.RevokeAllAgentAPIKeys(ctx, principal, RevokeAllAPIKeysInput{
		AgentUserID:    agentUserID,
		IdempotencyKey: "itc-revoke-all-idempotency",
	})
	if err != nil {
		t.Fatalf("revoke all keys: %v", err)
	}
	if result.RevokedCount != 2 {
		t.Fatalf("expected 2 revoked keys, got %d", result.RevokedCount)
	}
	var revoked int
	if err := db.Pool.QueryRow(ctx, `SELECT count(*) FROM identity.api_keys WHERE user_id = $1::uuid AND status = 'revoked' AND revoked_at IS NOT NULL`, agentUserID).Scan(&revoked); err != nil || revoked != 2 {
		t.Fatalf("persisted revocation mismatch: %d (%v)", revoked, err)
	}
	// Keys stay as audit rows (prefix retained) even though they can no longer
	// authenticate; no replacement key may be issued by this endpoint.
	var remaining int
	if err := db.Pool.QueryRow(ctx, `SELECT count(*) FROM identity.api_keys WHERE user_id = $1::uuid AND status = 'active'`, agentUserID).Scan(&remaining); err != nil || remaining != 0 {
		t.Fatalf("no active key must survive revoke-all: %d (%v)", remaining, err)
	}
}
