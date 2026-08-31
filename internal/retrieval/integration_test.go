// Integration test for the v2 projection pipeline against a real
// PostgreSQL with pgroonga and pgvector. Set RETRIEVAL_INTEGRATION_DATABASE_URL
// to run (scripts/acceptance.cmd wires a project-locked image).
package retrieval_test

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"agentchunzhi/internal/eventing"
	"agentchunzhi/internal/retrieval"
	"agentchunzhi/internal/store"
	"agentchunzhi/internal/testkit"
)

func TestPGroongaProjectionPipelineV2(t *testing.T) {
	databaseURL := os.Getenv("RETRIEVAL_INTEGRATION_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("RETRIEVAL_INTEGRATION_DATABASE_URL is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	db, err := store.Open(ctx, databaseURL)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer db.Close()

	var organizationID, userID, workspaceID, modelID, modelVersionID, assetID, assetVersionID, draftID string
	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin setup: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := tx.QueryRow(ctx, `
		INSERT INTO organization.organizations (name) VALUES ('retrieval-v2-integration')
		RETURNING id::text
	`).Scan(&organizationID); err != nil {
		t.Fatalf("create organization: %v", err)
	}
	if err := tx.QueryRow(ctx, `
		INSERT INTO identity.users (organization_id, user_type, display_name)
		VALUES ($1::uuid, 'agent', 'retrieval-v2-agent') RETURNING id::text
	`, organizationID).Scan(&userID); err != nil {
		t.Fatalf("create user: %v", err)
	}
	if err := tx.QueryRow(ctx, `
		INSERT INTO content.workspaces (organization_id, slug, name, created_by)
		VALUES ($1::uuid, 'retrieval-v2', 'Retrieval V2', $2::uuid) RETURNING id::text
	`, organizationID, userID).Scan(&workspaceID); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	if err := tx.QueryRow(ctx, `
		INSERT INTO model.resource_models (organization_id, model_key, name, created_by)
		VALUES ($1::uuid, 'retrieval-v2-model', 'Retrieval V2 Model', $2::uuid)
		RETURNING id::text
	`, organizationID, userID).Scan(&modelID); err != nil {
		t.Fatalf("create resource model: %v", err)
	}
	if err := tx.QueryRow(ctx, `
		INSERT INTO model.resource_model_versions
			(resource_model_id, version_no, status, field_schema, policy, created_by, published_at)
		VALUES ($1::uuid, 1, 'published', $2::jsonb, $3::jsonb, $4::uuid, now())
		RETURNING id::text
	`, modelID,
		`{"additional_properties": false, "fields": [
			{"key": "topic", "label": "主题", "type": "text", "searchable": true}
		]}`,
		`{"retrieval": {"fulltext": {"enabled": true}, "semantic": {"enabled": true}},
		  "channels": {"agent": {"enabled": true}}}`,
		userID).Scan(&modelVersionID); err != nil {
		t.Fatalf("create model version: %v", err)
	}
	if err := tx.QueryRow(ctx, `
		INSERT INTO asset.assets (organization_id, workspace_id, resource_model_id, created_by)
		VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid) RETURNING id::text
	`, organizationID, workspaceID, modelID, userID).Scan(&assetID); err != nil {
		t.Fatalf("create asset: %v", err)
	}
	if err := tx.QueryRow(ctx, `
		INSERT INTO asset.asset_versions
			(organization_id, workspace_id, asset_id, resource_model_id, resource_model_version_id,
			 version_no, title, summary, markdown, fields, content_checksum, created_by, sealed_at)
		VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, $5::uuid, 1,
		        'PGroonga V2 Integration', '集成验收摘要', $6,
		        '{"topic":"全文检索语义验收"}'::jsonb, 'retrieval-v2-checksum', $7::uuid, now())
		RETURNING id::text
	`, organizationID, workspaceID, assetID, modelID, modelVersionID,
		strings.Repeat("## 全文验收\n\n这是一段可检索的全文内容。", 200), userID).Scan(&assetVersionID); err != nil {
		t.Fatalf("create asset version: %v", err)
	}
	if err := tx.QueryRow(ctx, `
		INSERT INTO asset.asset_drafts
			(organization_id, workspace_id, asset_id, base_version_id, created_at)
		VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, now()) RETURNING id::text
	`, organizationID, workspaceID, assetID, assetVersionID).Scan(&draftID); err != nil {
		t.Fatalf("create draft: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE asset.assets
		SET current_working_version_id = $2::uuid, draft_id = $3::uuid,
		    current_published_version_id = $2::uuid, publication_status = 'published', published_at = now()
		WHERE id = $1::uuid
	`, assetID, assetVersionID, draftID); err != nil {
		t.Fatalf("publish asset: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit setup: %v", err)
	}
	cleanup := func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cleanupCancel()
		statements := []string{
			`DELETE FROM retrieval.projection_heads WHERE organization_id = $1::uuid`,
			`DELETE FROM retrieval.projection_runs WHERE organization_id = $1::uuid`,
			`DELETE FROM retrieval.projection_profiles WHERE organization_id = $1::uuid`,
			`DELETE FROM retrieval.projection_rebuilds WHERE organization_id = $1::uuid`,
			`DELETE FROM asset.asset_versions WHERE organization_id = $1::uuid`,
			`DELETE FROM asset.assets WHERE organization_id = $1::uuid`,
			`DELETE FROM model.resource_model_versions WHERE organization_id = $1::uuid`,
			`DELETE FROM model.resource_models WHERE organization_id = $1::uuid`,
			`DELETE FROM content.workspaces WHERE organization_id = $1::uuid`,
			`DELETE FROM identity.users WHERE organization_id = $1::uuid`,
			`DELETE FROM organization.organizations WHERE id = $1::uuid`,
		}
		for _, statement := range statements {
			if _, err := db.Pool.Exec(cleanupCtx, statement, organizationID); err != nil {
				t.Errorf("cleanup integration data: %v", err)
				return
			}
		}
	}
	defer cleanup()

	hash := testkit.HashEmbedding{Dimensions: 1024}
	profiles := &retrieval.ProfileService{
		Store:              db,
		Manifests:          map[string]retrieval.EmbeddingManifest{"hash-embedding@test": hash.Manifest()},
		DefaultManifestKey: "hash-embedding@test",
	}
	// activated_by is a plain uuid column (no FK): a synthetic actor satisfies
	// the activation metadata CHECK in this fixture.
	profile, activated, err := profiles.EnsureProfilesForOrganization(ctx, organizationID, "00000000-0000-4000-8000-0000000000a1")
	if err != nil {
		t.Fatalf("bootstrap retrieval profile: %v", err)
	}
	if !activated || !profile.SemanticEnabled {
		t.Fatalf("expected an activated semantic profile, got %+v activated=%v", profile, activated)
	}

	// The eventing insert-only client stands in for the River queue so the
	// test drives the job pipeline deterministically.
	queue, err := eventing.NewInsertOnlyClient(db.Pool)
	if err != nil {
		t.Fatalf("create queue client: %v", err)
	}
	coordinator := retrieval.Coordinator{Store: db, Queue: queue}
	payload, _ := json.Marshal(map[string]string{
		"asset_id": assetID, "version_id": assetVersionID, "workspace_id": workspaceID,
	})
	if err := coordinator.ProcessFact(ctx, "asset.published", payload); err != nil {
		t.Fatalf("coordinate asset.published: %v", err)
	}

	var runID string
	if err := db.Pool.QueryRow(ctx, `
		SELECT id::text FROM retrieval.projection_runs
		WHERE organization_id = $1::uuid AND asset_version_id = $2::uuid
	`, organizationID, assetVersionID).Scan(&runID); err != nil {
		t.Fatalf("ensure projection run: %v", err)
	}

	engine := retrieval.Engine{Store: db, Queue: queue, Embeddings: hash, Tokenizer: retrieval.NewWordTokenizer()}
	if err := retrieval.RunBuildProjection(ctx, engine, queue, runID); err != nil {
		t.Fatalf("build projection: %v", err)
	}
	var chunkCount int
	if err := db.Pool.QueryRow(ctx, `
		SELECT count(*) FROM retrieval.chunks WHERE projection_run_id = $1::uuid
	`, runID).Scan(&chunkCount); err != nil || chunkCount == 0 {
		t.Fatalf("expected persisted chunks, got %d (err=%v)", chunkCount, err)
	}
	for first := 0; first < chunkCount; first += 32 {
		last := first + 32 - 1
		if last >= chunkCount {
			last = chunkCount - 1
		}
		if err := retrieval.RunEmbedChunkBatch(ctx, engine, queue, retrieval.EmbedChunkBatchArgs{
			RunID: runID, FirstOrdinal: first, LastOrdinal: last,
		}); err != nil {
			t.Fatalf("embed batch %d-%d: %v", first, last, err)
		}
	}
	if err := retrieval.RunFinalizeProjection(ctx, engine, runID); err != nil {
		t.Fatalf("finalize projection: %v", err)
	}

	var status, semanticStatus string
	var readyEmbeddings int
	if err := db.Pool.QueryRow(ctx, `
		SELECT status, semantic_status, ready_embedding_count
		FROM retrieval.projection_runs WHERE id = $1::uuid
	`, runID).Scan(&status, &semanticStatus, &readyEmbeddings); err != nil {
		t.Fatalf("load finalized run: %v", err)
	}
	if status != "ready" || semanticStatus != "ready" || readyEmbeddings != chunkCount {
		t.Fatalf("run not ready: status=%s semantic=%s embeddings=%d/%d", status, semanticStatus, readyEmbeddings, chunkCount)
	}
	var headRun, headVersion string
	if err := db.Pool.QueryRow(ctx, `
		SELECT active_run_id::text, asset_version_id::text FROM retrieval.projection_heads
		WHERE organization_id = $1::uuid AND asset_id = $2::uuid
	`, organizationID, assetID).Scan(&headRun, &headVersion); err != nil {
		t.Fatalf("load projection head: %v", err)
	}
	if headRun != runID || headVersion != assetVersionID {
		t.Fatalf("head points elsewhere: run=%s version=%s", headRun, headVersion)
	}

	// PGroonga index serves full-text recall over the canonical content.
	var pgroongaHits int
	if err := db.Pool.QueryRow(ctx, `
		SELECT count(*) FROM retrieval.chunks
		WHERE projection_run_id = $1::uuid AND content &@~ '全文'
	`, runID).Scan(&pgroongaHits); err != nil {
		t.Fatalf("pgroonga search: %v", err)
	}
	if pgroongaHits == 0 {
		t.Fatal("pgroonga found no hits for the canonical keyword")
	}
	// Repeated delivery is idempotent: no second run is created.
	if err := coordinator.ProcessFact(ctx, "asset.published", payload); err != nil {
		t.Fatalf("re-deliver asset.published: %v", err)
	}
	var runCount int
	if err := db.Pool.QueryRow(ctx, `
		SELECT count(*) FROM retrieval.projection_runs
		WHERE organization_id = $1::uuid AND asset_version_id = $2::uuid
	`, organizationID, assetVersionID).Scan(&runCount); err != nil {
		t.Fatal(err)
	}
	if runCount != 1 {
		t.Fatalf("duplicate delivery created %d runs, want 1", runCount)
	}

	// Archive stales the run and removes the head immediately.
	archivePayload, _ := json.Marshal(map[string]string{
		"asset_id": assetID, "previous_version_id": assetVersionID, "workspace_id": workspaceID,
	})
	if err := coordinator.ProcessFact(ctx, "asset.archived", archivePayload); err != nil {
		t.Fatalf("coordinate asset.archived: %v", err)
	}
	if err := db.Pool.QueryRow(ctx, `SELECT status FROM retrieval.projection_runs WHERE id=$1::uuid`, runID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "stale" {
		t.Fatalf("archived run status = %s, want stale", status)
	}
	var headCount int
	if err := db.Pool.QueryRow(ctx, `
		SELECT count(*) FROM retrieval.projection_heads
		WHERE organization_id = $1::uuid AND asset_id = $2::uuid
	`, organizationID, assetID).Scan(&headCount); err != nil {
		t.Fatal(err)
	}
	if headCount != 0 {
		t.Fatalf("archived asset still has %d heads", headCount)
	}

	var riverJobs int
	if err := db.Pool.QueryRow(ctx, `
		SELECT count(*) FROM river_job WHERE kind LIKE 'retrieval_%'
	`).Scan(&riverJobs); err != nil {
		t.Fatalf("count retrieval river jobs: %v", err)
	}
	if riverJobs == 0 {
		t.Fatal("expected retrieval River jobs to be enqueued by the coordinator")
	}
}
