package site

// bound_versions_integration_test.go — the SQL gate that the pure-unit tests
// cannot provide: boundVersionRows joins six tables and projects the model
// version's field_schema. A wrong column or a wrong join silently 500s every
// section page in production while the unit suite stays green (that is exactly
// how `pv.field_schema` — a column that only exists on
// model.resource_model_versions — shipped once). Skips unless
// AGENTCHUNZHI_TEST_DATABASE_URL is configured; everything it creates is
// ITC-prefixed and removed afterwards.

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	agentquery "agentchunzhi/internal/query"
	"agentchunzhi/internal/store"
)

func TestBoundVersionRowsAgainstLiveSchema(t *testing.T) {
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

	slug := fmt.Sprintf("itc-bounds-%d", time.Now().UnixNano())
	var organizationID, userID, workspaceID string
	if err := db.Pool.QueryRow(ctx, `
		INSERT INTO organization.organizations (name, slug, status)
		VALUES ('ITC-BoundsOrg', 'itc-bounds-' || md5(random()::text), 'active')
		RETURNING id::text
	`).Scan(&organizationID); err != nil {
		t.Fatalf("seed organization: %v", err)
	}
	// Registered right after the org exists so a partial seed failure cleans
	// up too (the org id keys every child delete below).
	t.Cleanup(func() {
		db.Pool.Exec(ctx, `DELETE FROM site.site_content_bindings WHERE organization_id = $1`, organizationID)
		db.Pool.Exec(ctx, `DELETE FROM site.public_sites WHERE organization_id = $1`, organizationID)
		db.Pool.Exec(ctx, `UPDATE asset.assets SET current_working_version_id = NULL, current_published_version_id = NULL, draft_id = NULL, publication_status = 'draft', published_at = NULL WHERE organization_id = $1`, organizationID)
		db.Pool.Exec(ctx, `DELETE FROM asset.asset_drafts WHERE organization_id = $1`, organizationID)
		db.Pool.Exec(ctx, `DELETE FROM asset.asset_versions WHERE organization_id = $1`, organizationID)
		db.Pool.Exec(ctx, `DELETE FROM asset.assets WHERE organization_id = $1`, organizationID)
		db.Pool.Exec(ctx, `UPDATE model.resource_models SET current_version_id = NULL WHERE organization_id = $1`, organizationID)
		db.Pool.Exec(ctx, `DELETE FROM model.resource_model_versions WHERE organization_id = $1`, organizationID)
		db.Pool.Exec(ctx, `DELETE FROM model.resource_models WHERE organization_id = $1`, organizationID)
		db.Pool.Exec(ctx, `DELETE FROM content.workspaces WHERE organization_id = $1`, organizationID)
		db.Pool.Exec(ctx, `DELETE FROM identity.users WHERE organization_id = $1`, organizationID)
		db.Pool.Exec(ctx, `DELETE FROM organization.organizations WHERE id = $1`, organizationID)
	})
	if err := db.Pool.QueryRow(ctx, `
		INSERT INTO identity.users (organization_id, user_type, email, password_hash, display_name, status)
		VALUES ($1::uuid, 'member', 'itc-bounds-' || gen_random_uuid()::text || '@itc.invalid', 'x', 'ITC-BoundsAdmin', 'active')
		RETURNING id::text
	`, organizationID).Scan(&userID); err != nil {
		t.Fatalf("seed member: %v", err)
	}
	if err := db.Pool.QueryRow(ctx, `
		INSERT INTO content.workspaces (organization_id, slug, name, created_by)
		VALUES ($1::uuid, 'itc-bounds-ws', 'ITC Bounds WS', $2::uuid)
		RETURNING id::text
	`, organizationID, userID).Scan(&workspaceID); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}

	var modelID, modelVersionID string
	if err := db.Pool.QueryRow(ctx, `
		INSERT INTO model.resource_models (organization_id, workspace_id, model_key, name, status, created_by)
		VALUES ($1::uuid, $2::uuid, 'itc_bounds_shots', 'ITC Bounds Shots', 'active', $3::uuid)
		RETURNING id::text
	`, organizationID, workspaceID, userID).Scan(&modelID); err != nil {
		t.Fatalf("seed model: %v", err)
	}
	if err := db.Pool.QueryRow(ctx, `
		INSERT INTO model.resource_model_versions (
			organization_id, resource_model_id, version_no, status, field_schema, policy, created_by)
		VALUES ($1::uuid, $2::uuid, 1, 'published',
		        '{"fields":[{"key":"shot_size","type":"string"},{"key":"lens_mm","type":"integer"}]}'::jsonb,
		        '{"channels":{"public_site":{"enabled":true}}}'::jsonb, $3::uuid)
		RETURNING id::text
	`, organizationID, modelID, userID).Scan(&modelVersionID); err != nil {
		t.Fatalf("seed model version: %v", err)
	}
	if _, err := db.Pool.Exec(ctx, `
		UPDATE model.resource_models SET current_version_id = $2::uuid WHERE id = $1::uuid
	`, modelID, modelVersionID); err != nil {
		t.Fatalf("set current model version: %v", err)
	}

	// The assets trigger demands a working version and a shared draft at
	// commit time (deferred), so asset + version + draft materialize in one
	// transaction — the same shape asset.CreateVersionTx uses.
	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin fixture tx: %v", err)
	}
	defer tx.Rollback(ctx)
	var assetID, assetVersionID, draftID string
	if err := tx.QueryRow(ctx, `
		INSERT INTO asset.assets (organization_id, workspace_id, resource_model_id, visibility, created_by)
		VALUES ($1::uuid, $2::uuid, $3::uuid, 'public', $4::uuid)
		RETURNING id::text
	`, organizationID, workspaceID, modelID, userID).Scan(&assetID); err != nil {
		t.Fatalf("seed asset: %v", err)
	}
	if err := tx.QueryRow(ctx, `
		INSERT INTO asset.asset_versions (
			organization_id, workspace_id, asset_id, resource_model_id, resource_model_version_id,
			version_no, origin, confirmation_status, confirmed_by, confirmed_at,
			title, summary, markdown, fields, content_checksum, created_by)
		VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, $5::uuid,
		        1, 'human', 'human_confirmed', $6::uuid, now(),
		        'ITC Shot', 'summary', '# body', '{"shot_size":"wide","lens_mm":35}'::jsonb, 'itc-checksum', $6::uuid)
		RETURNING id::text
	`, organizationID, workspaceID, assetID, modelID, modelVersionID, userID).Scan(&assetVersionID); err != nil {
		t.Fatalf("seed asset version: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE asset.assets SET current_working_version_id = $2::uuid WHERE id = $1::uuid
	`, assetID, assetVersionID); err != nil {
		t.Fatalf("set working pointer: %v", err)
	}
	if err := tx.QueryRow(ctx, `
		INSERT INTO asset.asset_drafts (
			organization_id, workspace_id, asset_id, base_version_id, title, updated_by)
		VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, 'ITC Shot', $5::uuid)
		RETURNING id::text
	`, organizationID, workspaceID, assetID, assetVersionID, userID).Scan(&draftID); err != nil {
		t.Fatalf("seed draft: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE asset.assets SET draft_id = $2::uuid WHERE id = $1::uuid
	`, assetID, draftID); err != nil {
		t.Fatalf("set draft pointer: %v", err)
	}
	// Publish: the assets checks require publication_status='published' and
	// published_at to move together with the published pointer.
	if _, err := tx.Exec(ctx, `
		UPDATE asset.assets
		SET current_published_version_id = $2::uuid, published_at = now(), publication_status = 'published'
		WHERE id = $1::uuid
	`, assetID, assetVersionID); err != nil {
		t.Fatalf("publish asset: %v", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE asset.asset_versions SET sealed_at = now() WHERE id = $1::uuid`, assetVersionID); err != nil {
		t.Fatalf("seal version: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit fixture: %v", err)
	}

	var siteID string
	if err := db.Pool.QueryRow(ctx, `
		INSERT INTO site.public_sites (organization_id, workspace_id, slug, name, created_by, model_views)
		VALUES ($1::uuid, $2::uuid, $3, 'ITC Bounds Site', $4::uuid,
		        jsonb_build_object($5::text, '{"card_fields":["shot_size"],"detail_fields":["shot_size","lens_mm"]}'::jsonb))
		RETURNING id::text
	`, organizationID, workspaceID, slug, userID, modelID).Scan(&siteID); err != nil {
		t.Fatalf("seed site: %v", err)
	}
	if _, err := db.Pool.Exec(ctx, `
		INSERT INTO site.site_content_bindings (
			organization_id, workspace_id, site_id, asset_id, display_path, content_type,
			section_slug, sort_order, on_homepage, created_by)
		VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, 'itc-shots/one', 'article',
		        'itc-shots', 0, true, $5::uuid)
	`, organizationID, workspaceID, siteID, assetID, userID); err != nil {
		t.Fatalf("seed binding: %v", err)
	}

	reader := &PublicReader{Store: db}
	item := Site{
		ID:                  siteID,
		OrganizationID:      organizationID,
		WorkspaceID:         workspaceID,
		Slug:                slug,
		DefaultContentScope: "public",
		ModelViews: map[string]ModelView{
			modelID: {CardFields: []string{"shot_size"}, DetailFields: []string{"shot_size", "lens_mm"}},
		},
	}

	// The join must reach model.resource_model_versions.field_schema; a
	// reference to a non-existent column fails here as it did in production.
	rows, err := reader.boundVersionRows(ctx, item, "itc-shots", "", false, 10)
	if err != nil {
		t.Fatalf("boundVersionRows (no model filter): %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("want 1 row, got %d", len(rows))
	}
	if rows[0].VersionID != assetVersionID {
		t.Fatalf("version id mismatch: %q != %q", rows[0].VersionID, assetVersionID)
	}
	if len(rows[0].FieldSchema) == 0 || string(rows[0].FieldSchema) == "{}" {
		t.Fatalf("field_schema not joined from the model version: %s", rows[0].FieldSchema)
	}
	if rows[0].ModelID != modelID {
		t.Fatalf("model id mismatch: %q != %q", rows[0].ModelID, modelID)
	}

	// A model_key filter narrows to the owning model, and a non-matching key
	// yields zero rows (never a leak of other models' content).
	filtered, err := reader.boundVersionRows(ctx, item, "itc-shots", "itc_bounds_shots", false, 10)
	if err != nil {
		t.Fatalf("boundVersionRows (model filter): %v", err)
	}
	if len(filtered) != 1 {
		t.Fatalf("model filter dropped the row: got %d", len(filtered))
	}
	empty, err := reader.boundVersionRows(ctx, item, "itc-shots", "itc_bounds_other", false, 10)
	if err != nil {
		t.Fatalf("boundVersionRows (other model): %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("wrong model_key must match nothing, got %d", len(empty))
	}

	// The card projection must surface the whitelisted field only.
	visitor := agentquery.VisitorIdentity{
		UserType:       "member",
		OrganizationID: organizationID,
		UserID:         userID,
	}
	posts, err := reader.projectBoundRows(ctx, item, visitor, rows)
	if err != nil {
		t.Fatalf("projectBoundRows: %v", err)
	}
	if len(posts) != 1 {
		t.Fatalf("want 1 projected post, got %d", len(posts))
	}
	if len(posts[0].Fields) != 1 || posts[0].Fields[0].Key != "shot_size" {
		t.Fatalf("card whitelist projection wrong: %+v", posts[0].Fields)
	}
}
