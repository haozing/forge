package retrieval

import (
	"context"
	"errors"
	"fmt"
	"time"

	"agentchunzhi/internal/store"
)

// CleanupOptions tunes the bounded §8.10 cleanup sweeps.
type CleanupOptions struct {
	// SessionTTL mirrors RETRIEVAL_SESSION_TTL; the grace for stale/retired
	// projection data is session TTL + 5 minutes.
	SessionTTL time.Duration
	// QueryExecutionRetention defaults to 180 days.
	QueryExecutionRetention time.Duration
	// RebuildRetention defaults to 90 days.
	RebuildRetention time.Duration
	// BatchSize bounds every single DELETE/UPDATE statement.
	BatchSize int
}

// CleanupExpired performs one bounded pass of the cleanup order:
// session items -> sessions -> stale/retired run data (grace) -> runs of
// soft-deleted assets -> interrupted executions -> audit retention ->
// rebuild retention.
func CleanupExpired(ctx context.Context, st *store.Store, opts CleanupOptions) error {
	if st == nil || st.Pool == nil {
		return errors.New("database store is not initialized")
	}
	if opts.SessionTTL <= 0 {
		opts.SessionTTL = 10 * time.Minute
	}
	if opts.QueryExecutionRetention <= 0 {
		opts.QueryExecutionRetention = 180 * 24 * time.Hour
	}
	if opts.RebuildRetention <= 0 {
		opts.RebuildRetention = 90 * 24 * time.Hour
	}
	if opts.BatchSize <= 0 || opts.BatchSize > 1000 {
		opts.BatchSize = 200
	}
	grace := opts.SessionTTL + 5*time.Minute

	// 1. Session items of expired sessions (bounded by session id page).
	if _, err := st.Pool.Exec(ctx, `
		DELETE FROM retrieval.search_session_items i
		USING (
		    SELECT id FROM retrieval.search_sessions
		    WHERE expires_at < now()
		    ORDER BY expires_at
		    LIMIT $1
		) expired
		WHERE i.session_id = expired.id
	`, opts.BatchSize); err != nil {
		return fmt.Errorf("cleanup expired retrieval session items: %w", err)
	}

	// 2. Expired sessions (items cascade, but step 1 already bounded them).
	if _, err := st.Pool.Exec(ctx, `
		DELETE FROM retrieval.search_sessions
		WHERE id IN (
		    SELECT id FROM retrieval.search_sessions
		    WHERE expires_at < now()
		    ORDER BY expires_at
		    LIMIT $1
		)
	`, opts.BatchSize); err != nil {
		return fmt.Errorf("cleanup expired retrieval sessions: %w", err)
	}

	// 3. Stale run data past the grace window: deleting the run cascades
	// chunks and embeddings; the chunks trigger permits deletes of stale
	// runs. Ready runs are never touched.
	if _, err := st.Pool.Exec(ctx, `
		DELETE FROM retrieval.projection_runs
		WHERE id IN (
		    SELECT run.id FROM retrieval.projection_runs run
		    JOIN retrieval.projection_profiles p
		        ON p.organization_id = run.organization_id AND p.id = run.projection_profile_id
		    WHERE (
		            (run.status = 'stale' AND run.stale_at < now() - $2::interval)
		            OR (p.status = 'retired' AND p.retired_at < now() - $2::interval
		                AND run.status IN ('ready','degraded','stale'))
		          )
		    ORDER BY run.updated_at
		    LIMIT $1
		)
	`, opts.BatchSize, grace.String()); err != nil {
		return fmt.Errorf("cleanup grace-expired retrieval runs: %w", err)
	}

	// 4. Projection data of soft-deleted assets: runs (chunks and embeddings
	// cascade from them) leave with the asset so queries stop matching it.
	if _, err := st.Pool.Exec(ctx, `
		DELETE FROM retrieval.projection_runs
		WHERE id IN (
		    SELECT run.id FROM retrieval.projection_runs run
		    JOIN asset.assets a
		        ON a.organization_id = run.organization_id AND a.id = run.asset_id
		    WHERE a.deleted_at IS NOT NULL
		    ORDER BY run.updated_at
		    LIMIT $1
		)
	`, opts.BatchSize); err != nil {
		return fmt.Errorf("cleanup projection runs of deleted assets: %w", err)
	}

	// 5. Long-running query executions become interrupted.
	if _, err := st.Pool.Exec(ctx, `
		UPDATE retrieval.query_executions
		SET status = 'interrupted', completed_at = now()
		WHERE id IN (
		    SELECT id FROM retrieval.query_executions
		    WHERE status = 'started' AND started_at < now() - interval '1 hour'
		    ORDER BY started_at
		    LIMIT $1
		)
	`, opts.BatchSize); err != nil {
		return fmt.Errorf("interrupt stale retrieval query executions: %w", err)
	}

	// 6. Query execution audit retention.
	if _, err := st.Pool.Exec(ctx, `
		DELETE FROM retrieval.query_executions
		WHERE id IN (
		    SELECT id FROM retrieval.query_executions
		    WHERE started_at < now() - $2::interval
		    ORDER BY started_at
		    LIMIT $1
		)
	`, opts.BatchSize, opts.QueryExecutionRetention.String()); err != nil {
		return fmt.Errorf("cleanup expired retrieval query executions: %w", err)
	}

	// 7. Rebuild batch summary retention.
	if _, err := st.Pool.Exec(ctx, `
		DELETE FROM retrieval.projection_rebuilds
		WHERE id IN (
		    SELECT id FROM retrieval.projection_rebuilds
		    WHERE completed_at IS NOT NULL AND completed_at < now() - $2::interval
		    ORDER BY completed_at
		    LIMIT $1
		)
	`, opts.BatchSize, opts.RebuildRetention.String()); err != nil {
		return fmt.Errorf("cleanup expired retrieval rebuilds: %w", err)
	}
	return nil
}
