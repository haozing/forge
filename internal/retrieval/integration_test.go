package retrieval_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"agentchunzhi/internal/auth"
	"agentchunzhi/internal/query"
	"agentchunzhi/internal/retrieval"
	"agentchunzhi/internal/store"
)

func TestRetrievalProjectionAndQuery(t *testing.T) {
	databaseURL := os.Getenv("RETRIEVAL_INTEGRATION_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("RETRIEVAL_INTEGRATION_DATABASE_URL is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db, err := store.Open(ctx, databaseURL)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer db.Close()

	var organizationID, userID, modelID, modelVersionID, assetID, assetVersionID string
	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin setup: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := tx.QueryRow(ctx, `
		INSERT INTO organization.organizations (name)
		VALUES ('retrieval-integration') RETURNING id::text
	`).Scan(&organizationID); err != nil {
		t.Fatalf("create organization: %v", err)
	}
	if err := tx.QueryRow(ctx, `
		INSERT INTO identity.users (organization_id, user_type, display_name)
		VALUES ($1::uuid, 'agent', 'retrieval-agent') RETURNING id::text
	`, organizationID).Scan(&userID); err != nil {
		t.Fatalf("create agent user: %v", err)
	}
	if err := tx.QueryRow(ctx, `
		INSERT INTO model.resource_models (organization_id, model_key, name, created_by)
		VALUES ($1::uuid, 'retrieval-integration', 'Retrieval Integration', $2::uuid)
		RETURNING id::text
	`, organizationID, userID).Scan(&modelID); err != nil {
		t.Fatalf("create resource model: %v", err)
	}
	if err := tx.QueryRow(ctx, `
		INSERT INTO model.resource_model_versions
			(resource_model_id, version_no, status, policy, created_by)
		VALUES ($1::uuid, 1, 'published',
			'{"outlets":{"agent_tool":{"enabled":true},"fulltext":{"enabled":true},"semantic":{"enabled":true}}}'::jsonb,
			$2::uuid)
		RETURNING id::text
	`, modelID, userID).Scan(&modelVersionID); err != nil {
		t.Fatalf("create model version: %v", err)
	}
	if err := tx.QueryRow(ctx, `
		INSERT INTO asset.assets
			(organization_id, resource_model_id, publication_status, created_by)
		VALUES ($1::uuid, $2::uuid, 'published', $3::uuid)
		RETURNING id::text
	`, organizationID, modelID, userID).Scan(&assetID); err != nil {
		t.Fatalf("create asset: %v", err)
	}
	if err := tx.QueryRow(ctx, `
		INSERT INTO asset.asset_versions
			(organization_id, asset_id, resource_model_id, resource_model_version_id,
			 version_no, workflow_status, quality, title, markdown, fields, content_checksum, created_by)
		VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, 1, 'draft', 'human_confirmed',
		'PGroonga Integration', $6, '{"topic":"全文验收"}'::jsonb,
		'retrieval-integration-checksum', $5::uuid)
		RETURNING id::text
	`, organizationID, assetID, modelID, modelVersionID, userID, strings.Repeat("这是一段可检索的全文内容。", 200)).Scan(&assetVersionID); err != nil {
		t.Fatalf("create asset version: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE asset.assets
		SET current_published_version_id = $2::uuid
		WHERE id = $1::uuid
	`, assetID, assetVersionID); err != nil {
		t.Fatalf("publish asset: %v", err)
	}
	if err := retrieval.UpsertProjectionTx(ctx, tx, assetVersionID); err != nil {
		t.Fatalf("build projection: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit setup: %v", err)
	}
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		cleanupStatements := []string{
			`UPDATE asset.assets SET current_working_version_id = NULL, current_published_version_id = NULL WHERE organization_id = $1::uuid`,
			`DELETE FROM retrieval.search_sessions WHERE organization_id = $1::uuid`,
			`DELETE FROM asset.asset_versions WHERE organization_id = $1::uuid`,
			`DELETE FROM asset.assets WHERE organization_id = $1::uuid`,
			`DELETE FROM model.resource_model_versions WHERE resource_model_id IN (SELECT id FROM model.resource_models WHERE organization_id = $1::uuid)`,
			`DELETE FROM model.resource_models WHERE organization_id = $1::uuid`,
			`DELETE FROM identity.users WHERE organization_id = $1::uuid`,
			`DELETE FROM organization.organizations WHERE id = $1::uuid`,
		}
		for _, statement := range cleanupStatements {
			if _, err := db.Pool.Exec(cleanupCtx, statement, organizationID); err != nil {
				t.Errorf("cleanup integration data: %v", err)
				return
			}
		}
	}()

	page, err := (query.Service{Store: db}).Query(ctx, auth.Principal{
		UserID:         userID,
		OrganizationID: organizationID,
		UserType:       "agent",
	}, query.QueryRequest{Mode: "fulltext", Query: "全文", TopK: 1}, []string{modelID})
	if err != nil {
		t.Fatalf("query pgroonga projection: %v", err)
	}
	if len(page.Items) != 1 || !page.HasMore || page.NextCursor == "" {
		t.Fatalf("expected first page with cursor, got %+v", page)
	}
	if page.Items[0].AssetVersionID != assetVersionID || page.Items[0].Source != "asset" {
		t.Fatalf("unexpected search result: %+v", page.Items[0])
	}
	if page.Items[0].Snippet == "" {
		t.Fatal("expected non-empty snippet")
	}
	next, err := (query.Service{Store: db}).Query(ctx, auth.Principal{
		UserID: userID, OrganizationID: organizationID, UserType: "agent",
	}, query.QueryRequest{Mode: "fulltext", Query: "全文", TopK: 1, Cursor: page.NextCursor}, []string{modelID})
	if err != nil {
		t.Fatalf("query cursor page: %v", err)
	}
	if len(next.Items) == 0 || next.Items[0].AssetVersionID != assetVersionID {
		t.Fatalf("unexpected cursor result: %+v", next)
	}
	hybrid, err := (query.Service{Store: db, Embeddings: retrieval.HashEmbeddingProvider{Dimensions: retrieval.DefaultEmbeddingDimensions}}).Query(ctx, auth.Principal{
		UserID: userID, OrganizationID: organizationID, UserType: "agent",
	}, query.QueryRequest{Mode: "hybrid", Query: "全文", TopK: 10}, []string{modelID})
	if err != nil {
		t.Fatalf("query hybrid projection: %v", err)
	}
	if len(hybrid.Items) == 0 || hybrid.Items[0].AssetVersionID != assetVersionID {
		t.Fatalf("unexpected hybrid result: %+v", hybrid)
	}
	if hybrid.RankingMethod != "rrf" {
		t.Fatalf("hybrid should use RRF: %+v", hybrid)
	}
}
