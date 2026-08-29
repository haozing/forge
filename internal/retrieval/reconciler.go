package retrieval

import (
	"context"
	"errors"
	"fmt"
	"time"

	"agentchunzhi/internal/store"
)

// maxBuildAttempts caps how often one run may be (re)built before the
// reconciler declares it a poison build and fails it for good.
const maxBuildAttempts = 8

// ReconcileProjection repairs the projection pipeline without ever touching
// ready chunk content: expired build leases return to queued, embedding
// counters recalibrate from actual rows, lost embed jobs are re-inserted and
// finalizeable runs get their finalize nudge (doc §9.5).
func ReconcileProjection(ctx context.Context, st *store.Store, queue QueueInserter, lease time.Duration) error {
	if st == nil || st.Pool == nil {
		return errors.New("database store is not initialized")
	}
	if lease <= 0 {
		lease = 15 * time.Minute
	}

	// 1. Requeue building runs whose lease expired. A stalled build burns one
	// attempt like a failed one so poison builds cannot loop forever.
	if _, err := st.Pool.Exec(ctx, `
		UPDATE retrieval.projection_runs
		SET status = 'queued', started_at = NULL, updated_at = now(),
		    build_attempts = build_attempts + 1
		WHERE status = 'building'
		  AND started_at < now() - $1::interval
	`, lease.String()); err != nil {
		return fmt.Errorf("requeue expired retrieval builds: %w", err)
	}

	// 2. Recalibrate embedding counters from the actual rows.
	if _, err := st.Pool.Exec(ctx, `
		UPDATE retrieval.projection_runs run
		SET ready_embedding_count = sub.actual
		FROM (
		    SELECT run_id, count(*) AS actual
		    FROM retrieval.chunk_embeddings
		    GROUP BY run_id
		) sub
		WHERE run.id = sub.run_id
		  AND run.ready_embedding_count <> sub.actual
		  AND run.status IN ('lexical_ready', 'embedding')
	`); err != nil {
		return fmt.Errorf("recalibrate retrieval embedding counters: %w", err)
	}

	// 3. Re-enqueue embed batches whose River jobs disappeared while the run
	// still expects embeddings.
	rows, err := st.Pool.Query(ctx, `
		SELECT run.id::text, run.expected_embedding_count, run.ready_embedding_count
		FROM retrieval.projection_runs run
		JOIN retrieval.projection_profiles p
			ON p.organization_id = run.organization_id AND p.id = run.projection_profile_id
		WHERE run.status IN ('lexical_ready', 'embedding')
		  AND run.semantic_status = 'pending'
		  AND p.status IN ('active', 'warming')
		  AND run.expected_embedding_count > run.ready_embedding_count
		LIMIT 200
	`)
	if err != nil {
		return fmt.Errorf("list retrieval runs needing embed jobs: %w", err)
	}
	var runs []runCounters
	for rows.Next() {
		var item runCounters
		if err := rows.Scan(&item.RunID, &item.Expected, &item.Ready); err != nil {
			rows.Close()
			return err
		}
		runs = append(runs, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()

	for _, item := range runs {
		missing, err := missingEmbedBatches(ctx, st, item)
		if err != nil {
			return err
		}
		if len(missing) == 0 {
			continue
		}
		if queue == nil {
			continue
		}
		for _, batch := range missing {
			if _, err := queue.Insert(ctx, EmbedChunkBatchArgs{
				RunID: item.RunID, FirstOrdinal: batch.first, LastOrdinal: batch.last,
			}, nil); err != nil {
				return fmt.Errorf("re-enqueue retrieval embed batch: %w", err)
			}
		}
	}

	// 4. Nudge finalize for runs that look complete but never finalized, and
	// re-enqueue builds for queued runs without live jobs.
	if queue != nil {
		finalizeRows, err := st.Pool.Query(ctx, `
			SELECT run.id::text
			FROM retrieval.projection_runs run
			WHERE (
			       (run.status = 'lexical_ready' AND run.expected_embedding_count = 0)
			       OR (run.status = 'embedding' AND run.ready_embedding_count >= run.expected_embedding_count)
			       OR (run.status = 'lexical_ready' AND run.semantic_status = 'failed')
			      )
			LIMIT 200
		`)
		if err != nil {
			return fmt.Errorf("list retrieval runs to finalize: %w", err)
		}
		var runIDs []string
		for finalizeRows.Next() {
			var runID string
			if err := finalizeRows.Scan(&runID); err != nil {
				finalizeRows.Close()
				return err
			}
			runIDs = append(runIDs, runID)
		}
		if err := finalizeRows.Err(); err != nil {
			finalizeRows.Close()
			return err
		}
		finalizeRows.Close()
		for _, runID := range runIDs {
			if _, err := queue.Insert(ctx, FinalizeProjectionRunArgs{RunID: runID}, nil); err != nil {
				return fmt.Errorf("re-enqueue retrieval finalize: %w", err)
			}
		}

		buildRows, err := st.Pool.Query(ctx, `
			SELECT run.id::text, run.build_attempts
			FROM retrieval.projection_runs run
			WHERE run.status = 'queued'
			LIMIT 200
		`)
		if err != nil {
			return fmt.Errorf("list queued retrieval runs: %w", err)
		}
		type queuedRun struct {
			ID       string
			Attempts int
		}
		var queuedRuns []queuedRun
		for buildRows.Next() {
			var item queuedRun
			if err := buildRows.Scan(&item.ID, &item.Attempts); err != nil {
				buildRows.Close()
				return err
			}
			queuedRuns = append(queuedRuns, item)
		}
		if err := buildRows.Err(); err != nil {
			buildRows.Close()
			return err
		}
		buildRows.Close()
		for _, item := range queuedRuns {
			live, err := hasLiveRiverJob(ctx, st, JobKindBuildProjectionRun, item.ID)
			if err != nil {
				return err
			}
			if live {
				continue
			}
			if item.Attempts >= maxBuildAttempts {
				// Poison build: every retry burned an attempt and none
				// completed the run — fail it instead of requeueing forever.
				if _, err := st.Pool.Exec(ctx, `
					UPDATE retrieval.projection_runs
					SET status = 'failed', failure_code = 'build_attempts_exhausted',
					    failure_stage = 'build', updated_at = now()
					WHERE id = $1::uuid AND status = 'queued'
				`, item.ID); err != nil {
					return fmt.Errorf("fail exhausted retrieval build: %w", err)
				}
				continue
			}
			if _, err := queue.Insert(ctx, BuildProjectionRunArgs{RunID: item.ID}, nil); err != nil {
				return fmt.Errorf("re-enqueue retrieval build: %w", err)
			}
		}
	}
	return nil
}

type ordinalBatch struct {
	first int
	last  int
}

type runCounters struct {
	RunID    string
	Expected int
	Ready    int
}

// missingEmbedBatches computes the ordinal windows with no embedding row and
// no live River job. Batches are the 32-chunk grid used by the build worker.
func missingEmbedBatches(ctx context.Context, st *store.Store, item runCounters) ([]ordinalBatch, error) {
	rows, err := st.Pool.Query(ctx, `
		SELECT ordinal FROM retrieval.chunks c
		WHERE c.projection_run_id = $1::uuid
		  AND c.ordinal >= $2
		  AND NOT EXISTS (
		      SELECT 1 FROM retrieval.chunk_embeddings e WHERE e.chunk_id = c.id
		  )
		ORDER BY ordinal
	`, item.RunID, 0)
	if err != nil {
		return nil, fmt.Errorf("list missing retrieval ordinals: %w", err)
	}
	defer rows.Close()
	var missing []int
	for rows.Next() {
		var ordinal int
		if err := rows.Scan(&ordinal); err != nil {
			return nil, err
		}
		missing = append(missing, ordinal)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	batches := make([]ordinalBatch, 0, 4)
	for index := 0; index < len(missing); index += EmbedBatchSize {
		last := index + EmbedBatchSize - 1
		if last >= len(missing) {
			last = len(missing) - 1
		}
		batches = append(batches, ordinalBatch{first: missing[index], last: missing[last]})
	}
	if len(batches) == 0 {
		return nil, nil
	}
	// Filter batches that already have live jobs.
	live := make([]ordinalBatch, 0, len(batches))
	for _, batch := range batches {
		found, err := hasLiveEmbedJob(ctx, st, item.RunID, batch.first)
		if err != nil {
			return nil, err
		}
		if !found {
			live = append(live, batch)
		}
	}
	return live, nil
}

// hasLiveRiverJob checks the River job table for an enqueueable duplicate.
func hasLiveRiverJob(ctx context.Context, st *store.Store, kind, runID string) (bool, error) {
	var count int
	pattern := fmt.Sprintf(`{"run_id":%q`, runID)
	if kind == JobKindEmbedChunkBatch {
		// Batch jobs carry first_ordinal/last_ordinal; the run_id prefix is
		// enough for the liveness check.
		pattern = fmt.Sprintf(`{"run_id":%q`, runID)
	}
	err := st.Pool.QueryRow(ctx, `
		SELECT count(*)
		FROM river_job
		WHERE kind = $1
		  AND state IN ('available','scheduled','running','retryable','pending')
		  AND args::text LIKE $2
	`, kind, pattern+"%").Scan(&count)
	return count > 0, err
}

func hasLiveEmbedJob(ctx context.Context, st *store.Store, runID string, firstOrdinal int) (bool, error) {
	var count int
	err := st.Pool.QueryRow(ctx, `
		SELECT count(*)
		FROM river_job
		WHERE kind = $1
		  AND state IN ('available','scheduled','running','retryable','pending')
		  AND args->>'run_id' = $2
		  AND COALESCE((args->>'first_ordinal')::int, -1) = $3
	`, JobKindEmbedChunkBatch, runID, firstOrdinal).Scan(&count)
	return count > 0, err
}
