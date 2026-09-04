package retrieval

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"agentchunzhi/internal/store"
	"agentchunzhi/internal/vectorvalue"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
)

// River job kinds (doc §9.2). These are queue commands, never domain facts.
const (
	JobKindBuildProjectionRun    = "retrieval_build_projection_run"
	JobKindEmbedChunkBatch       = "retrieval_embed_chunk_batch"
	JobKindFinalizeProjectionRun = "retrieval_finalize_projection_run"
	JobKindBackfillProfile       = "retrieval_backfill_profile"
	JobKindReconcile             = "retrieval_reconcile"
	JobKindCleanup               = "retrieval_cleanup"

	// BuildQueue/EmbedQueue keep projection work off the event queues.
	BuildQueue = "retrieval_projection"
	EmbedQueue = "retrieval_embedding"
	MaintQueue = "retrieval_maintenance"

	EmbedBatchSize = EmbeddingBatchLimit

	// buildLease bounds a building run before the reconciler requeues it.
	buildLease = 15 * time.Minute
)

// HandlerManifest lists the job kinds a retrieval worker must register; the
// heartbeat stores it for cross-process readiness checks (doc §13.4).
func HandlerManifest() []string {
	return []string{
		JobKindBuildProjectionRun,
		JobKindEmbedChunkBatch,
		JobKindFinalizeProjectionRun,
		JobKindBackfillProfile,
		JobKindReconcile,
		JobKindCleanup,
	}
}

// ---------------------------------------------------------------------------
// Job args
// ---------------------------------------------------------------------------

// BuildProjectionRunArgs claims one run and persists its chunks.
type BuildProjectionRunArgs struct {
	RunID string `json:"run_id" river:"unique"`
}

func (BuildProjectionRunArgs) Kind() string { return JobKindBuildProjectionRun }

func (BuildProjectionRunArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{
		MaxAttempts: 8,
		Queue:       BuildQueue,
		UniqueOpts:  river.UniqueOpts{ByArgs: true},
	}
}

// EmbedChunkBatchArgs embeds one [FirstOrdinal, LastOrdinal] chunk window.
type EmbedChunkBatchArgs struct {
	RunID        string `json:"run_id"`
	FirstOrdinal int    `json:"first_ordinal"`
	LastOrdinal  int    `json:"last_ordinal"`
}

func (EmbedChunkBatchArgs) Kind() string { return JobKindEmbedChunkBatch }

func (EmbedChunkBatchArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{
		MaxAttempts: 8,
		Queue:       EmbedQueue,
		UniqueOpts:  river.UniqueOpts{ByArgs: true},
	}
}

// FinalizeProjectionRunArgs applies the §9.5 decision tree to one run.
type FinalizeProjectionRunArgs struct {
	RunID string `json:"run_id" river:"unique"`
}

func (FinalizeProjectionRunArgs) Kind() string { return JobKindFinalizeProjectionRun }

func (FinalizeProjectionRunArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{
		// Pending outcomes retry in-process; embeddings may take minutes on
		// large assets, so allow enough attempts before giving up.
		MaxAttempts: 24,
		Queue:       BuildQueue,
		UniqueOpts:  river.UniqueOpts{ByArgs: true},
	}
}

// BackfillProfileArgs processes one page of a rebuild batch.
type BackfillProfileArgs struct {
	RebuildID string `json:"rebuild_id"`
	Page      int    `json:"page"`
}

func (BackfillProfileArgs) Kind() string { return JobKindBackfillProfile }

func (BackfillProfileArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{
		MaxAttempts: 5,
		Queue:       BuildQueue,
		UniqueOpts:  river.UniqueOpts{ByArgs: true},
	}
}

// ReconcileArgs is the periodic projection repair sweep.
type ReconcileArgs struct{}

func (ReconcileArgs) Kind() string { return JobKindReconcile }

func (ReconcileArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{MaxAttempts: 3, Queue: MaintQueue}
}

// CleanupArgs is the periodic bounded cleanup sweep (doc §8.10).
type CleanupArgs struct{}

func (CleanupArgs) Kind() string { return JobKindCleanup }

func (CleanupArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{MaxAttempts: 3, Queue: MaintQueue}
}

// Engine bundles the dependencies shared by all retrieval workers.
type Engine struct {
	Store *store.Store
	// Queue inserts follow-up jobs from worker transactions; the River
	// context client takes precedence inside Work when present.
	Queue QueueInserter
	// Embeddings is nil when the runtime has no semantic manifest; the
	// build worker then records semantic unavailable instead of faking data.
	Embeddings EmbeddingProvider
	Tokenizer  Tokenizer
}

// QueueInserter is the narrow River surface workers need.
type QueueInserter interface {
	Insert(ctx context.Context, args river.JobArgs, opts *river.InsertOpts) (*rivertype.JobInsertResult, error)
}

// clientFromContext resolves the transaction-bound River client inside jobs.
func clientFromContext(ctx context.Context) (QueueInserter, error) {
	client := river.ClientFromContext[pgx.Tx](ctx)
	if client == nil {
		return nil, errors.New("River client is unavailable in job context")
	}
	return client, nil
}

// ---------------------------------------------------------------------------
// BuildProjectionRun (§9.3)
// ---------------------------------------------------------------------------

// BuildProjectionRunWorker canonicalizes and chunks in memory, then writes
// all chunks in one short transaction.
type BuildProjectionRunWorker struct {
	river.WorkerDefaults[BuildProjectionRunArgs]
	Engine Engine
}

func (w *BuildProjectionRunWorker) Work(ctx context.Context, job *river.Job[BuildProjectionRunArgs]) error {
	queue := w.Engine.Queue
	if client, err := clientFromContext(ctx); err == nil {
		queue = client
	}
	return RunBuildProjection(ctx, w.Engine, queue, job.Args.RunID)
}

// RunBuildProjection executes the build pipeline for one run. queue receives
// the follow-up embed/finalize jobs; tests may pass an insert-only client.
func RunBuildProjection(ctx context.Context, engine Engine, queue QueueInserter, runID string) error {
	if engine.Store == nil || engine.Store.Pool == nil {
		return errors.New("database store is not initialized")
	}
	repo := RunRepository{Store: engine.Store}
	run, err := repo.ClaimRun(ctx, runID)
	if errors.Is(err, ErrRunNotFound) {
		// Already claimed or terminal; nothing to do.
		return nil
	}
	if err != nil {
		return err
	}
	buildErr := func() error {
		build, err := repo.LoadBuildContext(ctx, run.ID)
		if errors.Is(err, ErrRunNotFound) {
			return nil
		}
		if err != nil {
			return err
		}

		// The published pointer and bound policy decide before work is spent.
		current, err := checkPublishedEligibility(ctx, engine.Store, run.OrganizationID, run.AssetID, run.AssetVersionID)
		if err != nil {
			return err
		}
		if !current {
			return repo.MarkRunStale(ctx, run.ID)
		}

		// Canonicalize + chunk fully in memory, no transaction held.
		segments := Canonicalize(CanonicalizeInput{
			Title: build.Title, Summary: build.Summary, Markdown: build.Markdown,
			Fields: build.Fields, FieldSchema: build.FieldSchema, Tags: build.Tags,
			Attachments: build.Attachments,
		})
		checksum := CanonicalChecksum(segments)
		chunks := ChunkSegments(segments, engineTokenizer(engine))

		// Terminal semantic gaps (manifest unavailable) degrade the run without
		// touching the lexical projection.
		semanticFailure := ""
		if build.SemanticEnabled && engine.Embeddings == nil {
			semanticFailure = FailureCodeSemanticMissing
		}

		if err := writeChunks(ctx, engine, run.ID, build, chunks, checksum, semanticFailure); err != nil {
			return err
		}
		return enqueueEmbedAndFinalize(ctx, queue, run, build, len(chunks))
	}()
	if buildErr != nil {
		// Every failed build burns one attempt; once build_attempts reaches
		// the reconciler cap the run is failed for good instead of looping.
		if noteErr := repo.NoteBuildAttempt(ctx, run.ID); noteErr != nil {
			return noteErr
		}
		return buildErr
	}
	return nil
}

// checkPublishedEligibility verifies the current published pointer and the
// bound model policy in one short transaction.
func checkPublishedEligibility(ctx context.Context, st *store.Store, organizationID, assetID, assetVersionID string) (bool, error) {
	tx, err := st.Pool.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("begin pointer check: %w", err)
	}
	defer tx.Rollback(ctx)
	current, err := IsCurrentPublishedTx(ctx, tx, organizationID, assetID, assetVersionID)
	if err != nil || !current {
		return current, err
	}
	return BuildEligibilityTx(ctx, tx, organizationID, assetVersionID)
}

func engineTokenizer(engine Engine) Tokenizer {
	if engine.Tokenizer != nil {
		return engine.Tokenizer
	}
	return NewWordTokenizer()
}

func writeChunks(ctx context.Context, engine Engine, runID string, build BuildContext, chunks []Chunk, checksum, semanticFailure string) error {
	repo := RunRepository{Store: engine.Store}
	tx, err := engine.Store.Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin chunk write: %w", err)
	}
	defer tx.Rollback(ctx)
	if _, err := repo.AdoptChecksumTx(ctx, tx, build.Run, checksum); err != nil {
		return err
	}
	if err := repo.WriteChunksTx(ctx, tx, runID, build, chunks, semanticFailure); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func enqueueEmbedAndFinalize(ctx context.Context, client QueueInserter, run Run, build BuildContext, chunkCount int) error {
	if client == nil {
		return nil
	}
	if build.SemanticEnabled && semanticPending(build) && chunkCount > 0 {
		for first := 0; first < chunkCount; first += EmbedBatchSize {
			last := first + EmbedBatchSize - 1
			if last >= chunkCount {
				last = chunkCount - 1
			}
			if _, err := client.Insert(ctx, EmbedChunkBatchArgs{
				RunID: run.ID, FirstOrdinal: first, LastOrdinal: last,
			}, nil); err != nil {
				return fmt.Errorf("enqueue retrieval embed batch: %w", err)
			}
		}
	}
	if _, err := client.Insert(ctx, FinalizeProjectionRunArgs{RunID: run.ID}, nil); err != nil {
		return fmt.Errorf("enqueue retrieval finalize: %w", err)
	}
	return nil
}

func semanticPending(build BuildContext) bool {
	return build.SemanticEnabled && build.Run.SemanticStatus != SemanticStatusFailed
}

// ---------------------------------------------------------------------------
// EmbedChunkBatch (§9.4)
// ---------------------------------------------------------------------------

// EmbedChunkBatchWorker calls the provider outside any transaction and
// persists the batch with counter recalibration.
type EmbedChunkBatchWorker struct {
	river.WorkerDefaults[EmbedChunkBatchArgs]
	Engine Engine
}

func (w *EmbedChunkBatchWorker) Work(ctx context.Context, job *river.Job[EmbedChunkBatchArgs]) error {
	queue := w.Engine.Queue
	if client, err := clientFromContext(ctx); err == nil {
		queue = client
	}
	return RunEmbedChunkBatch(ctx, w.Engine, queue, job.Args)
}

// RunEmbedChunkBatch embeds one ordinal window and persists the batch.
func RunEmbedChunkBatch(ctx context.Context, engine Engine, queue QueueInserter, args EmbedChunkBatchArgs) error {
	if engine.Store == nil || engine.Store.Pool == nil {
		return errors.New("database store is not initialized")
	}
	if engine.Embeddings == nil {
		// Semantic manifest missing: mark terminal so finalize degrades.
		return markSemanticFailed(ctx, engine, queue, args.RunID, FailureCodeSemanticMissing)
	}
	repo := RunRepository{Store: engine.Store}
	run, err := loadRunRow(ctx, engine.Store, args.RunID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if run.Status != RunStatusLexicalSafe && run.Status != RunStatusEmbedding {
		return nil
	}
	if run.SemanticStatus == SemanticStatusDisabled || run.SemanticStatus == SemanticStatusFailed {
		return nil
	}

	// Short transaction: read the chunk texts, release the connection.
	var chunkIDs, texts []string
	err = func() error {
		tx, err := engine.Store.Pool.Begin(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback(ctx)
		chunkIDs, texts, err = repo.LoadEmbedBatchTx(ctx, tx, run.ID, args.FirstOrdinal, args.LastOrdinal)
		if err != nil {
			return err
		}
		return tx.Commit(ctx)
	}()
	if err != nil {
		return err
	}
	if len(texts) == 0 {
		return nil
	}

	// Provider HTTP call, strictly outside any transaction. The input is the
	// stored chunk text; label prefixes were accounted for by the budget.
	vectors, embedErr := engine.Embeddings.EmbedDocuments(ctx, texts)
	if embedErr != nil {
		if IsTerminalProviderError(embedErr) {
			return markSemanticFailed(ctx, engine, queue, run.ID, FailureCodeEmbeddingFailed)
		}
		// Retryable: the queue retries this batch.
		return embedErr
	}
	if err := ValidateEmbeddingResponse(vectors, len(chunkIDs), engine.Embeddings.Manifest().Dimensions); err != nil {
		return markSemanticFailed(ctx, engine, queue, run.ID, FailureCodeEmbeddingFailed)
	}

	tx, err := engine.Store.Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin embedding write: %w", err)
	}
	defer tx.Rollback(ctx)
	if err := repo.InsertEmbeddingsTx(ctx, tx, run, chunkIDs, vectors, vectorLiteral); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit embedding write: %w", err)
	}

	if queue != nil {
		if _, err := queue.Insert(ctx, FinalizeProjectionRunArgs{RunID: run.ID}, nil); err != nil {
			return fmt.Errorf("enqueue retrieval finalize: %w", err)
		}
	}
	return nil
}

func loadRunRow(ctx context.Context, st *store.Store, runID string) (Run, error) {
	row := st.Pool.QueryRow(ctx, `
		SELECT id::text, organization_id::text, workspace_id::text, asset_id::text,
		       asset_version_id::text, resource_model_id::text, resource_model_version_id::text,
		       projection_profile_id::text, canonical_checksum, status, semantic_status,
		       expected_chunk_count, ready_chunk_count, expected_embedding_count,
		       ready_embedding_count, COALESCE(failure_code,''), COALESCE(failure_stage,''),
		       (SELECT generation FROM retrieval.projection_profiles p WHERE p.id = projection_profile_id)
		FROM retrieval.projection_runs
		WHERE id = $1::uuid
	`, runID)
	return scanRun(row)
}

// markSemanticFailed records a terminal semantic failure and nudges finalize.
func markSemanticFailed(ctx context.Context, engine Engine, queue QueueInserter, runID, code string) error {
	tx, err := engine.Store.Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin semantic failure write: %w", err)
	}
	defer tx.Rollback(ctx)
	repo := RunRepository{Store: engine.Store}
	if err := repo.MarkSemanticFailedTx(ctx, tx, runID, code); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit semantic failure write: %w", err)
	}
	if queue != nil {
		if _, err := queue.Insert(ctx, FinalizeProjectionRunArgs{RunID: runID}, nil); err != nil {
			return fmt.Errorf("enqueue retrieval finalize: %w", err)
		}
	}
	return nil
}

// vectorLiteral encodes a float32 vector into the pgvector text format.
func vectorLiteral(vector []float32) (string, error) {
	values := make([]float64, len(vector))
	for i, value := range vector {
		if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
			return "", fmt.Errorf("embedding value %d is not finite", i)
		}
		values[i] = float64(value)
	}
	return vectorvalue.Literal(values)
}

// ---------------------------------------------------------------------------
// FinalizeProjectionRun (§9.5)
// ---------------------------------------------------------------------------

// FinalizeProjectionRunWorker applies the decision tree and head switch.
type FinalizeProjectionRunWorker struct {
	river.WorkerDefaults[FinalizeProjectionRunArgs]
	Engine Engine
}

func (w *FinalizeProjectionRunWorker) Work(ctx context.Context, job *river.Job[FinalizeProjectionRunArgs]) error {
	return RunFinalizeProjection(ctx, w.Engine, job.Args.RunID)
}

// RunFinalizeProjection finalizes one run.
func RunFinalizeProjection(ctx context.Context, engine Engine, runID string) error {
	if engine.Store == nil || engine.Store.Pool == nil {
		return errors.New("database store is not initialized")
	}
	repo := RunRepository{Store: engine.Store}
	tx, err := engine.Store.Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin retrieval finalize: %w", err)
	}
	defer tx.Rollback(ctx)
	outcome, err := repo.FinalizeRunTx(ctx, tx, runID)
	if err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit retrieval finalize: %w", err)
	}
	if outcome == FinalizeFailed {
		return fmt.Errorf("retrieval run %s finalized as failed", runID)
	}
	if outcome == FinalizePending {
		// Embeddings or the build are still outstanding. The job must retry:
		// ByArgs uniqueness would silently swallow any new finalize enqueue
		// while this completed row is retained, deadlocking the run.
		return fmt.Errorf("retrieval run %s finalize pending; retrying", runID)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Backfill / Reconcile / Cleanup drivers
// ---------------------------------------------------------------------------

// BackfillProfileWorker processes one page of a rebuild batch.
type BackfillProfileWorker struct {
	river.WorkerDefaults[BackfillProfileArgs]
	Engine Engine
}

// BackfillPageSize bounds one job execution.
const BackfillPageSize = 200

func (w *BackfillProfileWorker) Work(ctx context.Context, job *river.Job[BackfillProfileArgs]) error {
	if w.Engine.Store == nil || w.Engine.Store.Pool == nil {
		return errors.New("database store is not initialized")
	}
	queue := w.Engine.Queue
	if client, err := clientFromContext(ctx); err == nil {
		queue = client
	}
	return ProcessBackfillPage(ctx, w.Engine.Store, queue, job.Args)
}

// ReconcileWorker runs the periodic projection repair.
type ReconcileWorker struct {
	river.WorkerDefaults[ReconcileArgs]
	Engine Engine
}

func (w *ReconcileWorker) Work(ctx context.Context, _ *river.Job[ReconcileArgs]) error {
	if w.Engine.Store == nil || w.Engine.Store.Pool == nil {
		return errors.New("database store is not initialized")
	}
	queue := w.Engine.Queue
	if client, err := clientFromContext(ctx); err == nil {
		queue = client
	}
	return ReconcileProjection(ctx, w.Engine.Store, queue, buildLease)
}

// CleanupWorker runs the periodic bounded cleanup.
type CleanupWorker struct {
	river.WorkerDefaults[CleanupArgs]
	Engine Engine
}

func (w *CleanupWorker) Work(ctx context.Context, _ *river.Job[CleanupArgs]) error {
	if w.Engine.Store == nil || w.Engine.Store.Pool == nil {
		return errors.New("database store is not initialized")
	}
	return CleanupExpired(ctx, w.Engine.Store, CleanupOptions{})
}
