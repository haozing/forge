package content

// pattern_integration_test.go — DB-gated coverage of the pattern service
// (G8): create (explicit blocks + asset snapshot), list, delete and the
// member apply command (single transaction, batched block insert, tree
// revision + draft epoch advance). Runs only against a migrated database.

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"agentchunzhi/internal/auth"
	"agentchunzhi/internal/store"
)

func patternTestStore(t *testing.T) *store.Store {
	t.Helper()
	databaseURL := os.Getenv("AGENTCHUNZHI_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("AGENTCHUNZHI_TEST_DATABASE_URL is not set")
	}
	database, err := store.Open(context.Background(), databaseURL)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { database.Pool.Close() })
	return database
}

// patternFixture builds the note chain a pattern needs: workspace, member,
// conversation bound to a note asset with a two-block live tree.
func patternFixture(t *testing.T, database *store.Store) (principal auth.Principal, conversationID, noteAssetID string) {
	t.Helper()
	ctx := context.Background()
	suffix := randomSuffix()
	var organizationID, workspaceID, userID string
	if err := database.Pool.QueryRow(ctx, `
		SELECT o.id::text, o.id::text FROM organization.organizations o LIMIT 1
	`).Scan(&organizationID, &workspaceID); err != nil {
		t.Fatalf("organization: %v", err)
	}
	_ = workspaceID
	var wsID string
	if err := database.Pool.QueryRow(ctx, `
		INSERT INTO content.workspaces (organization_id, slug, name, created_by)
		VALUES ($1::uuid, 'pattern-it-' || $2, 'pattern-it-' || $2, (SELECT id FROM identity.users WHERE organization_id = $1::uuid ORDER BY created_at LIMIT 1))
		RETURNING id::text
	`, organizationID, suffix).Scan(&wsID); err != nil {
		t.Fatalf("workspace: %v", err)
	}
	if err := database.Pool.QueryRow(ctx, `
		INSERT INTO identity.users (email, display_name, password_hash, status, organization_role, organization_id, user_type)
		VALUES ('pattern-' || $2 || '@itd.example', 'pattern', 'x', 'active', 'member', $1::uuid, 'member')
		RETURNING id::text
	`, organizationID, suffix).Scan(&userID); err != nil {
		t.Fatalf("user: %v", err)
	}
	if _, err := database.Pool.Exec(ctx, `
		INSERT INTO content.workspace_members (organization_id, workspace_id, user_id, role, granted_by)
		VALUES ($1::uuid, $2::uuid, $3::uuid, 'admin', $3::uuid)
	`, organizationID, wsID, userID); err != nil {
		t.Fatalf("member: %v", err)
	}
	var modelID string
	if err := database.Pool.QueryRow(ctx, `
		SELECT id::text FROM model.resource_models
		WHERE organization_id = $1::uuid AND model_key = 'builtin_note' LIMIT 1
	`, organizationID).Scan(&modelID); err != nil {
		t.Fatalf("note model: %v", err)
	}
	// The assets trigger demands a working version at commit time (deferred),
	// so asset + first version + draft materialize in one transaction — the
	// same shape asset.CreateVersionTx uses.
	fixtureTx, err := database.Pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer fixtureTx.Rollback(ctx)
	var assetID, draftID, versionID string
	if err := fixtureTx.QueryRow(ctx, `
		INSERT INTO asset.assets (organization_id, workspace_id, resource_model_id, visibility, created_by)
		VALUES ($1::uuid, $2::uuid, $3::uuid, 'workspace', $4::uuid)
		RETURNING id::text
	`, organizationID, wsID, modelID, userID).Scan(&assetID); err != nil {
		t.Fatalf("asset: %v", err)
	}
	if err := fixtureTx.QueryRow(ctx, `
		INSERT INTO asset.asset_versions (organization_id, workspace_id, asset_id, resource_model_id, title, summary, markdown, origin, confirmation_status, resource_model_version_id, version_no, content_checksum, created_by)
		VALUES ($1::uuid, $5::uuid, $2::uuid, $3::uuid, 'pattern source', '', '', 'human', 'unconfirmed',
		        (SELECT current_version_id FROM model.resource_models WHERE id = $3::uuid), 1, 'pattern-checksum', $4::uuid)
		RETURNING id::text
	`, organizationID, assetID, modelID, userID, wsID).Scan(&versionID); err != nil {
		t.Fatalf("version: %v", err)
	}
	if _, err := fixtureTx.Exec(ctx, `
		UPDATE asset.assets SET current_working_version_id = $2::uuid
		WHERE organization_id = $1::uuid AND id = $3::uuid
	`, organizationID, versionID, assetID); err != nil {
		t.Fatalf("working pointer: %v", err)
	}
	if err := fixtureTx.QueryRow(ctx, `
		INSERT INTO asset.asset_drafts (organization_id, workspace_id, asset_id, base_version_id, title, updated_by)
		VALUES ($1::uuid, $4::uuid, $2::uuid, $5::uuid, 'pattern source', $3::uuid)
		RETURNING id::text
	`, organizationID, assetID, userID, wsID, versionID).Scan(&draftID); err != nil {
		t.Fatalf("draft: %v", err)
	}
	if _, err := fixtureTx.Exec(ctx, `
		UPDATE asset.assets SET draft_id = $2::uuid
		WHERE organization_id = $1::uuid AND id = $3::uuid
	`, organizationID, draftID, assetID); err != nil {
		t.Fatalf("draft pointer: %v", err)
	}
	if _, err := fixtureTx.Exec(ctx, `
		UPDATE asset.asset_versions SET sealed_at = now() WHERE id = $1::uuid
	`, versionID); err != nil {
		t.Fatalf("seal version: %v", err)
	}
	if err := fixtureTx.Commit(ctx); err != nil {
		t.Fatalf("commit fixture: %v", err)
	}
	// Conversations carry a NOT NULL agent_application_id; reuse any existing
	// application of the organization (the dev database always has one from
	// the react suites) instead of growing a model-endpoint fixture chain.
	var containerID string
	var applicationID string
	if err := database.Pool.QueryRow(ctx, `
		SELECT aa.id::text FROM integration.agent_applications aa
		WHERE aa.organization_id = $1::uuid LIMIT 1
	`, organizationID).Scan(&applicationID); err != nil {
		t.Skipf("no agent application fixture available: %v", err)
	}
	if err := database.Pool.QueryRow(ctx, `
		INSERT INTO content.conversations (organization_id, workspace_id, initiator_user_id, agent_application_id, bound_agent_user_id, title)
		VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, $3::uuid, 'pattern')
		RETURNING id::text
	`, organizationID, wsID, userID, applicationID).Scan(&conversationID); err != nil {
		t.Fatalf("conversation: %v", err)
	}
	if _, err := database.Pool.Exec(ctx, `
		INSERT INTO content.note_bindings (organization_id, conversation_id, note_asset_id)
		VALUES ($1::uuid, $2::uuid, $3::uuid)
	`, organizationID, conversationID, assetID); err != nil {
		t.Fatalf("note binding: %v", err)
	}
	if err := database.Pool.QueryRow(ctx, `
		INSERT INTO content.containers (organization_id, kind, asset_id, created_by)
		VALUES ($1::uuid, 'note', $2::uuid, $3::uuid)
		RETURNING id::text
	`, organizationID, assetID, userID).Scan(&containerID); err != nil {
		t.Fatalf("container: %v", err)
	}
	for i, body := range []string{"# 源标题", "源正文一段"} {
		var blockID, revisionID string
		if err := database.Pool.QueryRow(ctx, `
			INSERT INTO content.blocks (organization_id, block_type, created_by)
			VALUES ($1::uuid, $2, $3::uuid) RETURNING id::text
		`, organizationID, blockKindFor(i), userID).Scan(&blockID); err != nil {
			t.Fatalf("block: %v", err)
		}
		if err := database.Pool.QueryRow(ctx, `
			INSERT INTO content.block_revisions (organization_id, block_id, revision_no, content, content_format, created_by, content_checksum)
			VALUES ($1::uuid, $2::uuid, 1, $3, 'markdown', $4::uuid, $5) RETURNING id::text
		`, organizationID, blockID, body, userID, hashBytes(body)).Scan(&revisionID); err != nil {
			t.Fatalf("revision: %v", err)
		}
		if _, err := database.Pool.Exec(ctx, `
			INSERT INTO content.block_placements (organization_id, container_id, block_revision_id, position)
			VALUES ($1::uuid, $2::uuid, $3::uuid, $4)
		`, organizationID, containerID, revisionID, float64(i)); err != nil {
			t.Fatalf("placement: %v", err)
		}
	}
	return auth.Principal{OrganizationID: organizationID, UserID: userID, UserType: "member"}, conversationID, assetID
}

func blockKindFor(i int) string {
	if i == 0 {
		return "heading"
	}
	return "paragraph"
}

func randomSuffix() string {
	return fmt.Sprintf("it%d", time.Now().UnixNano()%1e9)
}

func TestPatternLifecycleIntegration(t *testing.T) {
	database := patternTestStore(t)
	service := Service{Store: database}
	principal, conversationID, noteAssetID := patternFixture(t, database)

	created, err := service.CreatePattern(context.Background(), principal, "IT 样板", "集成测试",
		[]PatternBlock{{Kind: "heading", Content: "## 固定开头"}, {Kind: "paragraph", Content: "说明段落"}}, "")
	if err != nil {
		t.Fatalf("CreatePattern: %v", err)
	}
	if len(created.Blocks) != 2 || created.ID == "" {
		t.Fatalf("unexpected created pattern: %+v", created)
	}
	// Upsert by name keeps one row.
	if _, err := service.CreatePattern(context.Background(), principal, "IT 样板", "改描述",
		[]PatternBlock{{Kind: "paragraph", Content: "v2"}}, ""); err != nil {
		t.Fatalf("CreatePattern upsert: %v", err)
	}
	items, err := service.ListPatterns(context.Background(), principal, 20)
	if err != nil {
		t.Fatalf("ListPatterns: %v", err)
	}
	// The shared dev database carries patterns from other suites and from
	// earlier runs of this test; only this run's own upsert is asserted.
	found := false
	for _, item := range items {
		if item.Name == "IT 样板" && len(item.Blocks) == 1 {
			found = true
		}
	}
	if !found {
		t.Fatalf("IT 样板 with 1 block not listed: %+v", items)
	}
	// Snapshot from the note asset captures its live blocks.
	snap, err := service.CreatePattern(context.Background(), principal, "IT 快照", "",
		nil, noteAssetID)
	if err != nil {
		t.Fatalf("CreatePattern snapshot: %v", err)
	}
	if len(snap.Blocks) != 2 || snap.SourceAssetID != noteAssetID {
		t.Fatalf("snapshot should carry 2 live blocks: %+v", snap.Blocks)
	}

	var beforeRevision int64
	if err := database.Pool.QueryRow(context.Background(), `
		SELECT cc.revision FROM content.containers cc
		JOIN content.note_bindings nb ON nb.organization_id = cc.organization_id AND nb.note_asset_id = cc.asset_id
		WHERE nb.conversation_id = $1::uuid
	`, conversationID).Scan(&beforeRevision); err != nil {
		t.Fatalf("container revision: %v", err)
	}
	// The upsert replaced the pattern with a single-paragraph skeleton.
	applied, err := service.ApplyPattern(context.Background(), principal, "apply-"+created.ID, conversationID, "IT 样板")
	if err != nil {
		t.Fatalf("ApplyPattern: %v", err)
	}
	if applied != 1 {
		t.Fatalf("applied %d blocks, want 1 (post-upsert skeleton)", applied)
	}
	// Idempotent replay answers the same count without touching the tree.
	replayed, err := service.ApplyPattern(context.Background(), principal, "apply-"+created.ID, conversationID, "IT 样板")
	if err != nil || replayed != 1 {
		t.Fatalf("replay: %v count=%d", err, replayed)
	}
	var afterRevision int64
	var blockCount int
	if err := database.Pool.QueryRow(context.Background(), `
		SELECT cc.revision, (SELECT count(*) FROM content.block_placements bp
			WHERE bp.container_id = cc.id)
		FROM content.containers cc
		JOIN content.note_bindings nb ON nb.organization_id = cc.organization_id AND nb.note_asset_id = cc.asset_id
		WHERE nb.conversation_id = $1::uuid
	`, conversationID).Scan(&afterRevision, &blockCount); err != nil {
		t.Fatalf("after: %v", err)
	}
	if blockCount != 3 {
		t.Fatalf("tree should hold 3 blocks (2 live + 1 applied), got %d", blockCount)
	}
	if afterRevision != beforeRevision+1 {
		t.Fatalf("one apply must advance the tree revision once: %d -> %d", beforeRevision, afterRevision)
	}
	if err := service.DeletePattern(context.Background(), principal, "IT 样板"); err != nil {
		t.Fatalf("DeletePattern: %v", err)
	}
	if err := service.DeletePattern(context.Background(), principal, "IT 样板"); err == nil {
		t.Fatal("double delete must answer ErrNotFound")
	}
}
