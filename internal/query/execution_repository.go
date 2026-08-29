package query

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"agentchunzhi/internal/store"
)

// Stage latency keys (doc §10.9/§13.3). The map written to
// query_executions.stage_latency_ms only ever carries these fixed keys.
const (
	StageScope       = "scope"
	StageValidate    = "validate"
	StageLexical     = "lexical_recall"
	StageEmbedQuery  = "embed_query"
	StageSemantic    = "semantic"
	StageFuse        = "fuse"
	StageRerank      = "rerank"
	StageSession     = "session"
	StageFinalAuth   = "final_auth"
)

// stageLatency accumulates fixed-key stage durations.
type stageLatency map[string]float64

func (s stageLatency) observe(stage string, started time.Time) {
	if _, exists := s[stage]; !exists {
		s[stage] = time.Since(started).Seconds() * 1000
	}
}

func (s stageLatency) toJSON() string {
	if len(s) == 0 {
		return "{}"
	}
	payload, err := json.Marshal(s)
	if err != nil {
		return "{}"
	}
	return string(payload)
}

// executionCounts carries the per-request counters persisted at completion.
type executionCounts struct {
	ResourceModelCount int
	LexicalCandidates  int
	SemanticCandidates int
	FusedCandidates    int
	ResultCount        int
}

// BeginQueryExecution inserts the started row in a short transaction and
// binds the scope workspaces. Failure aborts the query with 503
// query_audit_unavailable before any recall happens (doc §10.9).
func BeginQueryExecution(ctx context.Context, store *store.Store, organizationID, subjectKind, subjectID, channel, requestID, requestHash, requestedMode string, workspaceIDs []string) (string, error) {
	if store == nil || store.Pool == nil {
		return "", ErrQueryAuditUnavailable
	}
	tx, err := store.Pool.Begin(ctx)
	if err != nil {
		return "", fmt.Errorf("%w: begin audit: %v", ErrQueryAuditUnavailable, err)
	}
	defer tx.Rollback(ctx)
	var executionID string
	if err := tx.QueryRow(ctx, `
		INSERT INTO retrieval.query_executions
			(organization_id, subject_kind, subject_id, channel, request_id, request_hash, requested_mode)
		VALUES ($1::uuid, $2, $3::uuid, $4, $5, $6, $7)
		RETURNING id::text
	`, organizationID, subjectKind, subjectID, channel, requestID, requestHash, requestedMode).Scan(&executionID); err != nil {
		return "", fmt.Errorf("%w: insert audit: %v", ErrQueryAuditUnavailable, err)
	}
	for _, workspaceID := range workspaceIDs {
		if _, err := tx.Exec(ctx, `
			INSERT INTO retrieval.query_execution_workspaces (execution_id, organization_id, workspace_id)
			VALUES ($1::uuid, $2::uuid, $3::uuid)
			ON CONFLICT DO NOTHING
		`, executionID, organizationID, workspaceID); err != nil {
			return "", fmt.Errorf("%w: insert audit workspace: %v", ErrQueryAuditUnavailable, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return "", fmt.Errorf("%w: commit audit: %v", ErrQueryAuditUnavailable, err)
	}
	return executionID, nil
}

// BindExecutionWorkspaces records the compiled scope workspaces of an
// execution in a short transaction. It runs after the scope compiles so the
// audit begin can precede the compile (doc §10.1).
func BindExecutionWorkspaces(ctx context.Context, store *store.Store, executionID, organizationID string, workspaceIDs []string) error {
	if store == nil || store.Pool == nil || len(workspaceIDs) == 0 {
		return nil
	}
	_, err := store.Pool.Exec(ctx, `
		INSERT INTO retrieval.query_execution_workspaces (execution_id, organization_id, workspace_id)
		SELECT $1::uuid, $2::uuid, value::uuid
		FROM unnest($3::text[]) AS value
		ON CONFLICT DO NOTHING
	`, executionID, organizationID, workspaceIDs)
	if err != nil {
		return fmt.Errorf("%w: bind audit workspaces: %v", ErrQueryAuditUnavailable, err)
	}
	return nil
}

// CompleteQueryExecution finalizes the audit row in a short transaction. The
// client response is only returned when this update succeeded (doc §10.9).
func CompleteQueryExecution(ctx context.Context, store *store.Store, executionID string, succeeded bool, errorCode string, executedMode, rankingMethod string, degraded bool, reasons []string, counts executionCounts, profileID string, generation int64, embeddingIdentity, rerankerIdentity string, latency stageLatency, sessionID string) error {
	if store == nil || store.Pool == nil {
		return ErrQueryAuditUnavailable
	}
	status := "failed"
	if succeeded {
		status = "succeeded"
	}
	if _, err := store.Pool.Exec(ctx, `
		UPDATE retrieval.query_executions SET
			status = $2, executed_mode = NULLIF($3,''), ranking_method = NULLIF($4,''),
			degraded = $5, degradation_reasons = $6,
			resource_model_count = $7, lexical_candidate_count = $8,
			semantic_candidate_count = $9, fused_candidate_count = $10,
			result_count = $11,
			projection_profile_id = NULLIF($12,'')::uuid, generation = NULLIF($13, 0),
			embedding_model_identity = NULLIF($14,''), reranker_model_identity = NULLIF($15,''),
			stage_latency_ms = $16::jsonb, error_code = NULLIF($17,''),
			search_session_id = NULLIF($18,'')::uuid, completed_at = now()
		WHERE id = $1::uuid AND status = 'started'
	`, executionID, status, executedMode, rankingMethod, degraded, reasons,
		counts.ResourceModelCount, counts.LexicalCandidates, counts.SemanticCandidates,
		counts.FusedCandidates, counts.ResultCount,
		profileID, generation, embeddingIdentity, rerankerIdentity,
		latency.toJSON(), errorCode, sessionID); err != nil {
		return fmt.Errorf("%w: complete audit: %v", ErrQueryAuditUnavailable, err)
	}
	return nil
}
