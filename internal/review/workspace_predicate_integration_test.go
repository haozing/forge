package review

// workspace_predicate_integration_test.go — DB-gated coverage for the
// cross-workspace authorization (IDOR) fix: a member of two workspaces must
// not reach a publication request (or submit for an asset) that belongs to
// the other workspace by routing through the workspace they belong to. The
// dev/test database is provided via AGENTCHUNZHI_TEST_DATABASE_URL; without
// it the test skips.

import (
	"context"
	"errors"
	"os"
	"testing"

	"agentchunzhi/internal/asset"
	"agentchunzhi/internal/auth"
	"agentchunzhi/internal/authz"
	"agentchunzhi/internal/store"
)

func TestReviewAggregateIsWorkspaceScopedIntegration(t *testing.T) {
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

	// One organization, an editor member of workspaces A and B, and an asset
	// with a pending publication request that lives in workspace B only.
	// Deferrable pointer constraints allow asset + version + draft in one
	// statement.
	var organizationID, memberID, workspaceA, workspaceB, requestID, assetID string
	err = db.Pool.QueryRow(ctx, `
		WITH org AS (
			INSERT INTO organization.organizations (name, status)
			VALUES ('ITC-ReviewWsGuard-' || gen_random_uuid()::text, 'active') RETURNING id
		), member AS (
			INSERT INTO identity.users (organization_id, user_type, display_name, status)
			SELECT id, 'member', 'ITC-ReviewWsEditor', 'active' FROM org RETURNING id, organization_id
		), ws_a AS (
			INSERT INTO content.workspaces (organization_id, slug, name, created_by)
			SELECT organization_id, 'ws-a-' || gen_random_uuid()::text, 'ITC Review WS A', id FROM member RETURNING id
		), ws_b AS (
			INSERT INTO content.workspaces (organization_id, slug, name, created_by)
			SELECT organization_id, 'ws-b-' || gen_random_uuid()::text, 'ITC Review WS B', id FROM member RETURNING id
		), membership_a AS (
			INSERT INTO content.workspace_members (organization_id, workspace_id, user_id, role, granted_by)
			SELECT organization_id, id, (SELECT id FROM member), 'editor', (SELECT id FROM member) FROM ws_a
		), membership_b AS (
			INSERT INTO content.workspace_members (organization_id, workspace_id, user_id, role, granted_by)
			SELECT organization_id, id, (SELECT id FROM member), 'editor', (SELECT id FROM member) FROM ws_b
		), model AS (
			INSERT INTO model.resource_models (organization_id, workspace_id, model_key, name, created_by)
			SELECT organization_id, (SELECT id FROM ws_b), 'guard-' || gen_random_uuid()::text, 'ITC Guard Model', id
			FROM member RETURNING id, organization_id
		), model_version AS (
			INSERT INTO model.resource_model_versions
				(organization_id, resource_model_id, version_no, status, policy, created_by)
			SELECT organization_id, id, 1, 'published', '{"publishing":{"mode":"approval"}}'::jsonb,
			       (SELECT id FROM member)
			FROM model RETURNING id, resource_model_id
		), model_link AS (
			UPDATE model.resource_models m SET current_version_id = v.id
			FROM model_version v WHERE m.id = v.resource_model_id
		), asset_row AS (
			INSERT INTO asset.assets (organization_id, workspace_id, resource_model_id, created_by)
			SELECT organization_id, (SELECT id FROM ws_b), (SELECT id FROM model), id FROM member
			RETURNING id, organization_id, workspace_id, resource_model_id
		), version AS (
			INSERT INTO asset.asset_versions
				(organization_id, workspace_id, asset_id, resource_model_id, resource_model_version_id,
				 version_no, title, content_checksum, created_by, sealed_at)
			SELECT a.organization_id, a.workspace_id, a.id, a.resource_model_id, (SELECT id FROM model_version),
			       1, 'ITC guarded asset', 'itc-guard-checksum', (SELECT id FROM member), now()
			FROM asset_row a RETURNING id, asset_id
		), draft AS (
			INSERT INTO asset.asset_drafts (organization_id, workspace_id, asset_id, base_version_id, title)
			SELECT a.organization_id, a.workspace_id, a.id, v.id, 'ITC guarded asset'
			FROM asset_row a, version v RETURNING id
		), asset_link AS (
			UPDATE asset.assets a
			SET current_working_version_id = (SELECT id FROM version), draft_id = (SELECT id FROM draft)
			WHERE a.id = (SELECT asset_id FROM version)
		), request AS (
			INSERT INTO asset.publication_requests
				(organization_id, workspace_id, asset_id, asset_version_id, submitted_by)
			SELECT a.organization_id, a.workspace_id, a.id, (SELECT id FROM version), (SELECT id FROM member)
			FROM asset_row a RETURNING id
		)
		SELECT o.id::text, m.id::text, wa.id::text, wb.id::text, r.id::text, a.id::text
		FROM org o, member m, ws_a wa, ws_b wb, request r, asset_row a
	`).Scan(&organizationID, &memberID, &workspaceA, &workspaceB, &requestID, &assetID)
	if err != nil {
		t.Fatalf("seed integration fixture: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(context.Background(),
			`DELETE FROM organization.organizations WHERE id = $1::uuid`, organizationID)
	})

	principal := auth.Principal{OrganizationID: organizationID, UserID: memberID, UserType: auth.UserTypeMember}
	policy := authz.WorkspacePolicyService{Store: db}
	service := Service{
		Store:     db,
		Policy:    policy,
		Committer: asset.MemberService{Store: db, Policy: policy},
	}

	// Same-workspace reads succeed (positive control).
	if item, err := service.Get(ctx, principal, workspaceB, requestID); err != nil || item.ID != requestID {
		t.Fatalf("same-workspace Get must succeed, got item=%v err=%v", item.ID, err)
	}

	// Cross-workspace routes must hide as ErrNotFound on every aggregate
	// surface, even though the caller is a legitimate member of workspace A.
	if _, err := service.Get(ctx, principal, workspaceA, requestID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-workspace Get must return ErrNotFound, got %v", err)
	}
	if _, err := service.Cancel(ctx, principal, workspaceA, requestID, ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-workspace Cancel must return ErrNotFound, got %v", err)
	}
	if _, err := service.AddComment(ctx, principal, workspaceA, requestID, "leak?"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-workspace AddComment must return ErrNotFound, got %v", err)
	}
	if _, _, err := service.ListComments(ctx, principal, workspaceA, requestID, "", 20); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-workspace ListComments must return ErrNotFound, got %v", err)
	}
	if _, err := service.Submit(ctx, principal, workspaceA, assetID, 1, "itc-key-1", "", nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-workspace Submit must return ErrNotFound, got %v", err)
	}

	// The commit path underneath Submit enforces the same boundary directly.
	member := asset.MemberService{Store: db, Policy: policy}
	if _, err := member.CommitDraft(ctx, principal, workspaceA, assetID, ""); !errors.Is(err, asset.ErrNotFound) {
		t.Fatalf("cross-workspace CommitDraft must return ErrNotFound, got %v", err)
	}
}
