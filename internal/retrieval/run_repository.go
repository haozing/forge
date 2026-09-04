package retrieval

import (
	"context"
	"errors"
	"fmt"

	"agentchunzhi/internal/store"

	"github.com/jackc/pgx/v5"
)

// RunRepository owns the retrieval.projection_runs / projection_heads /
// chunks / chunk_embeddings SQL. Every state transition is a short
// transaction; provider calls never happen inside one.
type RunRepository struct {
	Store *store.Store
}

// BuildContext carries the immutable inputs of one build job.
type BuildContext struct {
	Run              Run
	OrganizationID   string
	Title            string
	Summary          string
	Markdown         string
	Fields           []byte
	FieldSchema      []byte
	Tags             []TagInput
	Attachments      []AttachmentInput
	SemanticEnabled  bool
	ProfileStatus    string
	CanonicalizerVer string
	ChunkerVer       string
}

// EnsureQueuedRunTx idempotently inserts a queued run for
// (organization, asset_version, profile) with an empty canonical checksum
// and returns its id. Repeated deliveries collapse onto the same row.
func EnsureQueuedRunTx(ctx context.Context, tx pgx.Tx, organizationID, workspaceID, assetID, assetVersionID, resourceModelID, resourceModelVersionID, profileID string, semanticEnabled bool) (string, error) {
	semantic := SemanticStatusPending
	if !semanticEnabled {
		semantic = SemanticStatusDisabled
	}
	var runID string
	err := tx.QueryRow(ctx, `
		INSERT INTO retrieval.projection_runs
			(organization_id, workspace_id, asset_id, asset_version_id,
			 resource_model_id, resource_model_version_id, projection_profile_id,
			 status, semantic_status)
		VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, $5::uuid, $6::uuid, $7::uuid,
		        'queued', $8)
		ON CONFLICT (organization_id, asset_version_id, projection_profile_id, canonical_checksum)
		DO UPDATE SET updated_at = now()
		RETURNING id::text
	`, organizationID, workspaceID, assetID, assetVersionID,
		resourceModelID, resourceModelVersionID, profileID, semantic).Scan(&runID)
	if err != nil {
		return "", fmt.Errorf("ensure retrieval projection run: %w", err)
	}
	return runID, nil
}

// NoteBuildAttempt records one burned build attempt of a run (build failure or
// expired lease); the reconciler stops requeueing once the attempts reach the
// cap and fails the run instead of looping on a poison build.
func (r RunRepository) NoteBuildAttempt(ctx context.Context, runID string) error {
	_, err := r.Store.Pool.Exec(ctx, `
		UPDATE retrieval.projection_runs
		SET build_attempts = build_attempts + 1, updated_at = now()
		WHERE id = $1::uuid
	`, runID)
	if err != nil {
		return fmt.Errorf("note retrieval build attempt: %w", err)
	}
	return nil
}

// ClaimRun transitions a queued/failed run to building. The conditional
// UPDATE is atomic: only one worker can win per run.
func (r RunRepository) ClaimRun(ctx context.Context, runID string) (Run, error) {
	row := r.Store.Pool.QueryRow(ctx, `
		UPDATE retrieval.projection_runs
		SET status = 'building', started_at = now(), updated_at = now(),
		    failure_code = NULL, failure_stage = NULL
		WHERE id = $1::uuid AND status IN ('queued', 'failed')
		RETURNING id::text, organization_id::text, workspace_id::text, asset_id::text,
		          asset_version_id::text, resource_model_id::text, resource_model_version_id::text,
		          projection_profile_id::text, canonical_checksum, status, semantic_status,
		          expected_chunk_count, ready_chunk_count, expected_embedding_count,
		          ready_embedding_count, COALESCE(failure_code,''), COALESCE(failure_stage,''),
		          (SELECT generation FROM retrieval.projection_profiles p WHERE p.id = projection_profile_id)
	`, runID)
	run, err := scanRun(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Run{}, ErrRunNotFound
	}
	return run, err
}

// LoadBuildContext reads the immutable version content, the bound model
// version and the tag relations outside any transaction (doc §9.3).
func (r RunRepository) LoadBuildContext(ctx context.Context, runID string) (BuildContext, error) {
	var build BuildContext
	var semanticEnabled bool
	var profileStatus string
	var fields, fieldSchema []byte
	err := r.Store.Pool.QueryRow(ctx, `
		SELECT run.id::text, run.organization_id::text, run.workspace_id::text,
		       run.asset_id::text, run.asset_version_id::text,
		       run.resource_model_id::text, run.resource_model_version_id::text,
		       run.projection_profile_id::text, run.canonical_checksum, run.status,
		       run.semantic_status, run.expected_chunk_count, run.ready_chunk_count,
		       run.expected_embedding_count, run.ready_embedding_count,
		       COALESCE(run.failure_code,''), COALESCE(run.failure_stage,''),
		       p.generation,
		       v.title, v.summary, v.markdown, v.fields, mv.field_schema,
		       p.semantic_enabled, p.status, p.canonicalizer_version, p.chunker_version
		FROM retrieval.projection_runs run
		JOIN retrieval.projection_profiles p
			ON p.organization_id = run.organization_id AND p.id = run.projection_profile_id
		JOIN asset.asset_versions v
			ON v.organization_id = run.organization_id AND v.id = run.asset_version_id
		JOIN model.resource_model_versions mv
			ON mv.organization_id = run.organization_id AND mv.id = v.resource_model_version_id
		WHERE run.id = $1::uuid
	`, runID).Scan(
		&build.Run.ID, &build.Run.OrganizationID, &build.Run.WorkspaceID,
		&build.Run.AssetID, &build.Run.AssetVersionID,
		&build.Run.ResourceModelID, &build.Run.ResourceModelVersionID,
		&build.Run.ProjectionProfileID, &build.Run.CanonicalChecksum, &build.Run.Status,
		&build.Run.SemanticStatus, &build.Run.ExpectedChunkCount, &build.Run.ReadyChunkCount,
		&build.Run.ExpectedEmbeddingCount, &build.Run.ReadyEmbeddingCount,
		&build.Run.FailureCode, &build.Run.FailureStage,
		&build.Run.ProjectionGeneration,
		&build.Title, &build.Summary, &build.Markdown, &fields, &fieldSchema,
		&semanticEnabled, &profileStatus, &build.CanonicalizerVer, &build.ChunkerVer,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return build, ErrRunNotFound
	}
	if err != nil {
		return build, fmt.Errorf("load retrieval build context: %w", err)
	}
	build.Fields = fields
	build.FieldSchema = fieldSchema
	build.SemanticEnabled = semanticEnabled
	build.ProfileStatus = profileStatus

	tagRows, err := r.Store.Pool.Query(ctx, `
		SELECT t.id::text, t.normalized_key, t.display_name
		FROM asset.asset_version_tags avt
		JOIN asset.tags t ON t.organization_id = avt.organization_id AND t.id = avt.tag_id
		WHERE avt.organization_id = $1::uuid AND avt.asset_version_id = $2::uuid
	`, build.Run.OrganizationID, build.Run.AssetVersionID)
	if err != nil {
		return build, fmt.Errorf("load retrieval build tags: %w", err)
	}
	defer tagRows.Close()
	for tagRows.Next() {
		var tag TagInput
		if err := tagRows.Scan(&tag.ID, &tag.Key, &tag.DisplayName); err != nil {
			return build, err
		}
		build.Tags = append(build.Tags, tag)
	}
	if err := tagRows.Err(); err != nil {
		return build, err
	}

	// Attachment texts join the retrievable corpus: the vision extractor's
	// OCR + description of every clean, successfully extracted image
	// materialized on this version.
	attachmentRows, err := r.Store.Pool.Query(ctx, `
		SELECT att.id::text, att.default_alt_text, atx.text_content
		FROM asset.asset_version_attachments lva
		JOIN asset.attachments att
		  ON att.organization_id = lva.organization_id AND att.id = lva.attachment_id
		JOIN asset.attachment_texts atx ON atx.attachment_id = att.id
		WHERE lva.organization_id = $1::uuid AND lva.asset_version_id = $2::uuid
		  AND att.deleted_at IS NULL AND att.status = 'clean'
		  AND att.extraction_status = 'succeeded'
		ORDER BY att.id
	`, build.Run.OrganizationID, build.Run.AssetVersionID)
	if err != nil {
		return build, fmt.Errorf("load retrieval build attachments: %w", err)
	}
	defer attachmentRows.Close()
	for attachmentRows.Next() {
		var attachment AttachmentInput
		if err := attachmentRows.Scan(&attachment.ID, &attachment.Alt, &attachment.Text); err != nil {
			return build, err
		}
		build.Attachments = append(build.Attachments, attachment)
	}
	if err := attachmentRows.Err(); err != nil {
		return build, err
	}
	return build, nil
}

// IsCurrentPublishedTx reports whether the run's version is still the
// current published pointer of a published asset (inside a transaction).
func IsCurrentPublishedTx(ctx context.Context, tx pgx.Tx, organizationID, assetID, assetVersionID string) (bool, error) {
	var current string
	var published bool
	err := tx.QueryRow(ctx, `
		SELECT COALESCE(current_published_version_id::text,''),
		       publication_status = 'published'
		FROM asset.assets
		WHERE organization_id = $1::uuid AND id = $2::uuid
	`, organizationID, assetID).Scan(&current, &published)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("load asset published pointer: %w", err)
	}
	return published && current == assetVersionID, nil
}

// WriteChunksTx persists the whole chunk set, counters and lexical_ready_at
// in one transaction; any failure rolls the batch back entirely (doc §9.3).
func (r RunRepository) WriteChunksTx(ctx context.Context, tx pgx.Tx, runID string, build BuildContext, chunks []Chunk, semanticFailure string) error {
	totalChunks := len(chunks)
	expectedEmbeddings := 0
	semantic := SemanticStatusDisabled
	if build.SemanticEnabled {
		semantic = SemanticStatusPending
		expectedEmbeddings = totalChunks
	}
	// A retried failed run may carry partial chunks from the previous
	// attempt; the run is building/failed here so the immutability trigger
	// permits the cleanup (embeddings cascade with the chunks).
	if _, err := tx.Exec(ctx, `DELETE FROM retrieval.chunks WHERE projection_run_id = $1::uuid`, runID); err != nil {
		return fmt.Errorf("clear partial retrieval chunks: %w", err)
	}
	for _, chunk := range chunks {
		if _, err := tx.Exec(ctx, `
			INSERT INTO retrieval.chunks
				(organization_id, workspace_id, asset_id, asset_version_id,
				 resource_model_id, resource_model_version_id,
				 projection_run_id, projection_profile_id, projection_generation,
				 ordinal, source_type, source_locator, char_start, char_end, content,
				 source_checksum, chunk_checksum, canonicalizer_version, chunker_version)
			VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, $5::uuid, $6::uuid,
			        $7::uuid, $8::uuid, $9, $10, $11, $12::jsonb, $13, $14, $15,
			        $16, $17, $18, $19)
		`, build.Run.OrganizationID, build.Run.WorkspaceID, build.Run.AssetID, build.Run.AssetVersionID,
			build.Run.ResourceModelID, build.Run.ResourceModelVersionID,
			runID, build.Run.ProjectionProfileID, build.Run.ProjectionGeneration,
			chunk.Ordinal, chunk.SourceType, string(chunk.SourceLocator), chunk.CharStart, chunk.CharEnd, chunk.Text,
			chunk.SourceChecksum, chunk.ChunkChecksum, build.CanonicalizerVer, build.ChunkerVer); err != nil {
			return fmt.Errorf("write retrieval chunk %d: %w", chunk.Ordinal, err)
		}
	}
	if _, err := tx.Exec(ctx, `
		UPDATE retrieval.projection_runs
		SET expected_chunk_count = $2, ready_chunk_count = $2,
		    expected_embedding_count = $3, ready_embedding_count = 0,
		    status = 'lexical_ready', semantic_status = $4,
		    lexical_ready_at = now(), updated_at = now()
		WHERE id = $1::uuid
	`, runID, totalChunks, expectedEmbeddings, semantic); err != nil {
		return fmt.Errorf("mark retrieval run lexical_ready: %w", err)
	}
	if semanticFailure != "" {
		if _, err := tx.Exec(ctx, `
			UPDATE retrieval.projection_runs
			SET semantic_status = 'failed', failure_code = $2, failure_stage = 'semantic', updated_at = now()
			WHERE id = $1::uuid
		`, runID, semanticFailure); err != nil {
			return fmt.Errorf("record retrieval semantic failure: %w", err)
		}
	}
	return nil
}

// AdoptChecksumTx binds the computed canonical checksum to the run. When a
// sibling run already owns the checksum the current run becomes stale and
// the sibling id is returned.
func (r RunRepository) AdoptChecksumTx(ctx context.Context, tx pgx.Tx, run Run, checksum string) (string, error) {
	rows, err := tx.Query(ctx, `
		SELECT id::text, status FROM retrieval.projection_runs
		WHERE organization_id = $1::uuid AND asset_version_id = $2::uuid
		  AND projection_profile_id = $3::uuid AND canonical_checksum = $4
		  AND id <> $5::uuid
	`, run.OrganizationID, run.AssetVersionID, run.ProjectionProfileID, checksum, run.ID)
	if err != nil {
		return "", fmt.Errorf("lookup retrieval run checksum: %w", err)
	}
	var liveSibling, deadSibling string
	for rows.Next() {
		var id, status string
		if err := rows.Scan(&id, &status); err != nil {
			rows.Close()
			return "", err
		}
		if status == RunStatusStale || status == RunStatusFailed {
			deadSibling = id
		} else {
			liveSibling = id
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return "", err
	}
	rows.Close()

	if liveSibling != "" {
		// A live run already covers this canonical text: the current run is
		// superseded and the winner stays in charge of the pointer.
		// The lexical_pointer CHECK only allows 'stale' for runs that had
		// reached lexical readiness; younger runs retire as failed.
		if _, err := tx.Exec(ctx, `
			UPDATE retrieval.projection_runs
			SET status = CASE WHEN lexical_ready_at IS NULL THEN 'failed' ELSE 'stale' END,
			    -- stale_metadata CHECK: stale_at must be set iff status='stale'.
			    stale_at = CASE WHEN lexical_ready_at IS NULL THEN NULL ELSE now() END,
			    failure_code = 'superseded', updated_at = now()
			WHERE id = $1::uuid AND status NOT IN ('stale')
		`, run.ID); err != nil {
			return "", fmt.Errorf("retire superseded retrieval run: %w", err)
		}
		return liveSibling, nil
	}
	// A stale/failed sibling still occupies the checksum slot; its content is
	// dead weight, so reclaim it before adopting the checksum on this run.
	if deadSibling != "" {
		if _, err := tx.Exec(ctx, `DELETE FROM retrieval.projection_runs WHERE id = $1::uuid`, deadSibling); err != nil {
			return "", fmt.Errorf("reclaim dead retrieval run: %w", err)
		}
	}
	if _, err := tx.Exec(ctx, `
		UPDATE retrieval.projection_runs
		SET canonical_checksum = $2, updated_at = now()
		WHERE id = $1::uuid AND canonical_checksum <> $2
	`, run.ID, checksum); err != nil {
		return "", fmt.Errorf("adopt retrieval canonical checksum: %w", err)
	}
	return "", nil
}

// LoadEmbedBatchTx reads one ordinal range of chunk ids/texts for embedding.
func (r RunRepository) LoadEmbedBatchTx(ctx context.Context, tx pgx.Tx, runID string, firstOrdinal, lastOrdinal int) ([]string, []string, error) {
	rows, err := tx.Query(ctx, `
		SELECT id::text, content FROM retrieval.chunks
		WHERE projection_run_id = $1::uuid AND ordinal BETWEEN $2 AND $3
		ORDER BY ordinal
	`, runID, firstOrdinal, lastOrdinal)
	if err != nil {
		return nil, nil, fmt.Errorf("load retrieval embed batch: %w", err)
	}
	defer rows.Close()
	ids, texts := make([]string, 0, lastOrdinal-firstOrdinal+1), make([]string, 0, lastOrdinal-firstOrdinal+1)
	for rows.Next() {
		var id, text string
		if err := rows.Scan(&id, &text); err != nil {
			return nil, nil, err
		}
		ids = append(ids, id)
		texts = append(texts, text)
	}
	return ids, texts, rows.Err()
}

// InsertEmbeddingsTx stores one batch with ON CONFLICT DO NOTHING and
// recalibrates the ready counter from the actual row count (doc §9.4).
func (r RunRepository) InsertEmbeddingsTx(ctx context.Context, tx pgx.Tx, run Run, chunkIDs []string, vectors [][]float32, encode func([]float32) (string, error)) error {
	for i, chunkID := range chunkIDs {
		literal, err := encode(vectors[i])
		if err != nil {
			return fmt.Errorf("encode retrieval embedding %d: %w", i, err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO retrieval.chunk_embeddings
				(chunk_id, organization_id, workspace_id, projection_run_id,
				 projection_profile_id, projection_generation, embedding)
			VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, $5::uuid, $6, $7::vector(1024))
			ON CONFLICT (chunk_id) DO NOTHING
		`, chunkID, run.OrganizationID, run.WorkspaceID, run.ID,
			run.ProjectionProfileID, run.ProjectionGeneration, literal); err != nil {
			return fmt.Errorf("write retrieval embedding: %w", err)
		}
	}
	if _, err := tx.Exec(ctx, `
		UPDATE retrieval.projection_runs run
		SET ready_embedding_count = (
		        SELECT count(*) FROM retrieval.chunk_embeddings e WHERE e.projection_run_id = run.id
		    ),
		    semantic_status = CASE
		        WHEN (SELECT count(*) FROM retrieval.chunk_embeddings e WHERE e.projection_run_id = run.id) >= run.expected_embedding_count
		            THEN 'ready' ELSE semantic_status END,
		    status = CASE WHEN run.status = 'lexical_ready' THEN 'embedding' ELSE run.status END,
		    updated_at = now()
		WHERE run.id = $1::uuid
	`, run.ID); err != nil {
		return fmt.Errorf("calibrate retrieval embedding counter: %w", err)
	}
	return nil
}

// MarkSemanticFailedTx records a terminal embedding failure so finalize can
// degrade the run without discarding the lexical projection.
func (r RunRepository) MarkSemanticFailedTx(ctx context.Context, tx pgx.Tx, runID, failureCode string) error {
	if _, err := tx.Exec(ctx, `
		UPDATE retrieval.projection_runs
		SET semantic_status = 'failed', failure_code = $2, failure_stage = 'semantic', updated_at = now()
		WHERE id = $1::uuid AND status IN ('lexical_ready','embedding')
	`, runID, failureCode); err != nil {
		return fmt.Errorf("record retrieval semantic failure: %w", err)
	}
	return nil
}

// FinalizeOutcome reports what finalize decided.
type FinalizeOutcome string

const (
	FinalizeReady     FinalizeOutcome = "ready"
	FinalizeDegraded  FinalizeOutcome = "degraded"
	FinalizeStale     FinalizeOutcome = "stale"
	FinalizeFailed    FinalizeOutcome = "failed"
	FinalizePending   FinalizeOutcome = "pending"
	FinalizeUnchanged FinalizeOutcome = "unchanged"
)

// FinalizeRunTx implements the §9.5 decision tree and the projection head
// switch inside one short transaction. Lock order: run -> head.
func (r RunRepository) FinalizeRunTx(ctx context.Context, tx pgx.Tx, runID string) (FinalizeOutcome, error) {
	run, err := lockRunTx(ctx, tx, runID)
	if errors.Is(err, pgx.ErrNoRows) {
		return FinalizeUnchanged, ErrRunNotFound
	}
	if err != nil {
		return FinalizeUnchanged, err
	}
	if run.Terminal() {
		return FinalizeUnchanged, nil
	}
	if run.Status != RunStatusLexicalSafe && run.Status != RunStatusEmbedding {
		// queued/building runs are not finalizeable; their build job will
		// resume them.
		return FinalizePending, nil
	}

	if run.ReadyChunkCount < run.ExpectedChunkCount {
		return FinalizeFailed, r.markRunTerminalTx(ctx, tx, run.ID, RunStatusFailed, "lexical_incomplete", "lexical")
	}

	// Current published pointer wins over any completed work.
	current, err := IsCurrentPublishedTx(ctx, tx, run.OrganizationID, run.AssetID, run.AssetVersionID)
	if err != nil {
		return FinalizeUnchanged, err
	}
	if !current {
		return FinalizeStale, r.markRunStaleTx(ctx, tx, run.ID)
	}

	// A retired profile stops serving immediately.
	var profileStatus string
	var semanticEnabled bool
	if err := tx.QueryRow(ctx, `
		SELECT status, semantic_enabled FROM retrieval.projection_profiles
		WHERE organization_id = $1::uuid AND id = $2::uuid
	`, run.OrganizationID, run.ProjectionProfileID).Scan(&profileStatus, &semanticEnabled); err != nil {
		return FinalizeUnchanged, fmt.Errorf("load retrieval profile for finalize: %w", err)
	}
	if profileStatus == ProfileStatusRetired || profileStatus == ProfileStatusFailed {
		return FinalizeStale, r.markRunStaleTx(ctx, tx, run.ID)
	}

	target := RunStatusDegraded
	switch {
	case run.SemanticStatus == SemanticStatusDisabled:
		target = RunStatusReady
	case run.ReadyEmbeddingCount >= run.ExpectedEmbeddingCount && run.SemanticStatus != SemanticStatusFailed:
		target = RunStatusReady
	case run.SemanticStatus == SemanticStatusFailed:
		target = RunStatusDegraded
	default:
		// Embedding work is still outstanding (retryable).
		return FinalizePending, nil
	}

	// Head switch: re-verify the published pointer under the head lock.
	headExists, headRunID, err := lockHeadTx(ctx, tx, run.OrganizationID, run.AssetID, run.ProjectionProfileID)
	if err != nil {
		return FinalizeUnchanged, err
	}
	switch {
	case !headExists:
		// First projection: degraded may become head to serve lexical.
		if err := insertHeadTx(ctx, tx, run); err != nil {
			return FinalizeUnchanged, err
		}
	case headRunID == run.ID:
		// Already serving; refresh the pointer metadata.
		if err := updateHeadTx(ctx, tx, run); err != nil {
			return FinalizeUnchanged, err
		}
	case target == RunStatusReady:
		// Only fully ready runs replace an existing head.
		stillCurrent, err := IsCurrentPublishedTx(ctx, tx, run.OrganizationID, run.AssetID, run.AssetVersionID)
		if err != nil {
			return FinalizeUnchanged, err
		}
		if !stillCurrent {
			return FinalizeStale, r.markRunStaleTx(ctx, tx, run.ID)
		}
		if err := replaceHeadTx(ctx, tx, run, headRunID); err != nil {
			return FinalizeUnchanged, err
		}
	default:
		// Degraded never displaces an existing head.
	}

	return FinalizeOutcome(target), r.markRunTerminalTx(ctx, tx, run.ID, target, "", "")
}

// markRunTerminalTx applies a terminal state with completed_at. A run that
// finalizes ready with all embeddings persisted but a still-pending semantic
// flag (e.g. after a counter recalibration) is normalized to semantic ready
// so the state CHECK holds.
func (r RunRepository) markRunTerminalTx(ctx context.Context, tx pgx.Tx, runID, status, failureCode, failureStage string) error {
	_, err := tx.Exec(ctx, `
		UPDATE retrieval.projection_runs
		SET status = $2,
		    semantic_status = CASE
		        WHEN $2 = 'ready' AND semantic_status = 'pending'
		            AND ready_embedding_count >= expected_embedding_count
		            THEN 'ready'
		        ELSE semantic_status END,
		    completed_at = CASE WHEN $2 IN ('ready','degraded','failed') THEN now() ELSE completed_at END,
		    stale_at = CASE WHEN $2 = 'stale' THEN now() ELSE stale_at END,
		    failure_code = CASE WHEN $3 = '' THEN failure_code ELSE NULLIF($3,'') END,
		    failure_stage = CASE WHEN $4 = '' THEN failure_stage ELSE NULLIF($4,'') END,
		    updated_at = now()
		WHERE id = $1::uuid
	`, runID, status, failureCode, failureStage)
	if err != nil {
		return fmt.Errorf("mark retrieval run %s: %w", status, err)
	}
	return nil
}

// markRunStaleTx marks the run stale.
func (r RunRepository) markRunStaleTx(ctx context.Context, tx pgx.Tx, runID string) error {
	return r.markRunTerminalTx(ctx, tx, runID, RunStatusStale, "", "")
}

// MarkRunStale is the standalone stale transition used by the coordinator.
func (r RunRepository) MarkRunStale(ctx context.Context, runID string) error {
	_, err := r.Store.Pool.Exec(ctx, `
		UPDATE retrieval.projection_runs
		SET status = 'stale', stale_at = now(), updated_at = now()
		WHERE id = $1::uuid AND status NOT IN ('stale')
	`, runID)
	if err != nil {
		return fmt.Errorf("mark retrieval run stale: %w", err)
	}
	return nil
}

// MarkAssetRunsStaleTx marks every non-stale run of one asset version stale
// and removes the asset's heads (archive path).
func (r RunRepository) MarkAssetRunsStaleTx(ctx context.Context, tx pgx.Tx, organizationID, assetID, assetVersionID string) error {
	if _, err := tx.Exec(ctx, `
		DELETE FROM retrieval.projection_heads
		WHERE organization_id = $1::uuid AND asset_id = $2::uuid
	`, organizationID, assetID); err != nil {
		return fmt.Errorf("delete retrieval heads: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE retrieval.projection_runs
		SET status = 'stale', stale_at = now(), updated_at = now()
		WHERE organization_id = $1::uuid AND asset_version_id = $2::uuid AND status NOT IN ('stale')
	`, organizationID, assetVersionID); err != nil {
		return fmt.Errorf("stale retrieval runs: %w", err)
	}
	return nil
}

func lockRunTx(ctx context.Context, tx pgx.Tx, runID string) (Run, error) {
	row := tx.QueryRow(ctx, `
		SELECT id::text, organization_id::text, workspace_id::text, asset_id::text,
		       asset_version_id::text, resource_model_id::text, resource_model_version_id::text,
		       projection_profile_id::text, canonical_checksum, status, semantic_status,
		       expected_chunk_count, ready_chunk_count, expected_embedding_count,
		       ready_embedding_count, COALESCE(failure_code,''), COALESCE(failure_stage,''),
		       (SELECT generation FROM retrieval.projection_profiles p WHERE p.id = projection_profile_id)
		FROM retrieval.projection_runs
		WHERE id = $1::uuid
		FOR UPDATE
	`, runID)
	return scanRun(row)
}

func lockHeadTx(ctx context.Context, tx pgx.Tx, organizationID, assetID, profileID string) (bool, string, error) {
	var headRunID string
	err := tx.QueryRow(ctx, `
		SELECT active_run_id::text FROM retrieval.projection_heads
		WHERE organization_id = $1::uuid AND asset_id = $2::uuid AND projection_profile_id = $3::uuid
		FOR UPDATE
	`, organizationID, assetID, profileID).Scan(&headRunID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, "", nil
	}
	if err != nil {
		return false, "", fmt.Errorf("lock retrieval head: %w", err)
	}
	return true, headRunID, nil
}

func insertHeadTx(ctx context.Context, tx pgx.Tx, run Run) error {
	if _, err := tx.Exec(ctx, `
		INSERT INTO retrieval.projection_heads
			(organization_id, asset_id, projection_profile_id, asset_version_id,
			 active_run_id, generation, revision)
		VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, $5::uuid, $6, 1)
		ON CONFLICT (organization_id, asset_id, projection_profile_id) DO NOTHING
	`, run.OrganizationID, run.AssetID, run.ProjectionProfileID, run.AssetVersionID,
		run.ID, run.ProjectionGeneration); err != nil {
		return fmt.Errorf("insert retrieval head: %w", err)
	}
	return nil
}

func updateHeadTx(ctx context.Context, tx pgx.Tx, run Run) error {
	if _, err := tx.Exec(ctx, `
		UPDATE retrieval.projection_heads
		SET asset_version_id = $4::uuid, generation = $5, revision = revision + 1, updated_at = now()
		WHERE organization_id = $1::uuid AND asset_id = $2::uuid AND projection_profile_id = $3::uuid
	`, run.OrganizationID, run.AssetID, run.ProjectionProfileID, run.AssetVersionID,
		run.ProjectionGeneration); err != nil {
		return fmt.Errorf("update retrieval head: %w", err)
	}
	return nil
}

// replaceHeadTx switches the head to the new run and marks the previous run
// stale in the same transaction (doc §7.5).
func replaceHeadTx(ctx context.Context, tx pgx.Tx, run Run, previousRunID string) error {
	if _, err := tx.Exec(ctx, `
		UPDATE retrieval.projection_heads
		SET asset_version_id = $4::uuid, active_run_id = $5::uuid, generation = $6,
		    revision = revision + 1, updated_at = now()
		WHERE organization_id = $1::uuid AND asset_id = $2::uuid AND projection_profile_id = $3::uuid
	`, run.OrganizationID, run.AssetID, run.ProjectionProfileID, run.AssetVersionID,
		run.ID, run.ProjectionGeneration); err != nil {
		return fmt.Errorf("replace retrieval head: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE retrieval.projection_runs
		SET status = 'stale', stale_at = now(), updated_at = now()
		WHERE id = $1::uuid AND status IN ('ready','degraded')
	`, previousRunID); err != nil {
		return fmt.Errorf("stale replaced retrieval run: %w", err)
	}
	return nil
}

func scanRun(row pgx.Row) (Run, error) {
	var run Run
	err := row.Scan(&run.ID, &run.OrganizationID, &run.WorkspaceID, &run.AssetID,
		&run.AssetVersionID, &run.ResourceModelID, &run.ResourceModelVersionID,
		&run.ProjectionProfileID, &run.CanonicalChecksum, &run.Status, &run.SemanticStatus,
		&run.ExpectedChunkCount, &run.ReadyChunkCount, &run.ExpectedEmbeddingCount,
		&run.ReadyEmbeddingCount, &run.FailureCode, &run.FailureStage,
		&run.ProjectionGeneration)
	if err != nil {
		return Run{}, err
	}
	return run, nil
}

// BuildEligibilityTx re-checks the doc §7.1 eligibility of the run's bound
// model version inside a transaction.
func BuildEligibilityTx(ctx context.Context, tx pgx.Tx, organizationID, assetVersionID string) (bool, error) {
	var eligible bool
	err := tx.QueryRow(ctx, `
		SELECT (
		    COALESCE((mv.policy #>> '{retrieval,fulltext,enabled}')::boolean, false)
		    OR COALESCE((mv.policy #>> '{retrieval,semantic,enabled}')::boolean, false)
		) AND EXISTS (
		    SELECT 1 FROM jsonb_object_keys(COALESCE(mv.policy->'channels','{}'::jsonb)) AS channel
		    WHERE COALESCE((mv.policy #>> ARRAY['channels', channel, 'enabled'])::boolean, false)
		)
		FROM asset.asset_versions v
		JOIN model.resource_model_versions mv
			ON mv.organization_id = v.organization_id AND mv.id = v.resource_model_version_id
		WHERE v.organization_id = $1::uuid AND v.id = $2::uuid
	`, organizationID, assetVersionID).Scan(&eligible)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return eligible, err
}
