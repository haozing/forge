package automation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"agentchunzhi/internal/auth"
	"agentchunzhi/internal/authz"
	"agentchunzhi/internal/store"

	"github.com/jackc/pgx/v5"
)

var (
	ErrInvalidInput = errors.New("invalid automation input")
	ErrForbidden    = errors.New("automation access denied")
	ErrNotFound     = errors.New("automation job or run not found")
	ErrConflict     = errors.New("automation conflict")
	ErrNoPendingRun = errors.New("no pending automation run")

)

// The only internal automation operation is the P0 Agent preparation flow
// (prepare_asset); chat ReAct runs carry runtime_mode='react' instead of an
// operation and dispatch to PersistentReActService.
var supportedOperations = map[string]struct{}{
	"prepare_asset": {},
}

type Service struct {
	Store  *store.Store
	Policy authz.WorkspacePolicy
}

type Run struct {
	ID                  string         `json:"id"`
	WorkspaceID         string         `json:"workspace_id"`
	AutomationJobID     *string        `json:"job_id,omitempty"`
	Source              string         `json:"source"`
	Operation           string         `json:"operation"`
	Status              string         `json:"status"`
	Progress            float64        `json:"progress"`
	AttemptCount        int            `json:"attempt_count"`
	ErrorCode           *string        `json:"error_code,omitempty"`
	ErrorSummary        *string        `json:"error_summary,omitempty"`
	CreatedAt           time.Time      `json:"created_at"`
	StartedAt           *time.Time     `json:"started_at,omitempty"`
	CompletedAt         *time.Time     `json:"completed_at,omitempty"`
	NextAttemptAt       *time.Time     `json:"next_attempt_at,omitempty"`
	CancelRequested     bool           `json:"cancel_requested"`
	InputScope          map[string]any `json:"input_scope,omitempty"`
	OutputSnapshot      map[string]any `json:"output_snapshot,omitempty"`
	Credential          string         `json:"credential,omitempty"`
	CredentialExpiresAt *time.Time     `json:"credential_expires_at,omitempty"`
}

type Attempt struct {
	ID             string     `json:"id"`
	RunID          string     `json:"run_id"`
	AttemptNo      int        `json:"attempt_no"`
	Status         string     `json:"status"`
	ErrorCode      *string    `json:"error_code,omitempty"`
	ErrorSummary   *string    `json:"error_summary,omitempty"`
	ClaimedBy      *string    `json:"claimed_by,omitempty"`
	LeaseExpiresAt *time.Time `json:"lease_expires_at,omitempty"`
	NextRetryAt    *time.Time `json:"next_retry_at,omitempty"`
	StartedAt      time.Time  `json:"started_at"`
	CompletedAt    *time.Time `json:"completed_at,omitempty"`
}

type ClaimedRun struct {
	Run     Run     `json:"run"`
	Attempt Attempt `json:"attempt"`
}

func (s Service) require(ctx context.Context, principal auth.Principal, workspaceID, action string) error {
	if principal.UserType != "member" || s.Store == nil || s.Store.Pool == nil || s.Policy == nil {
		return ErrForbidden
	}
	_, err := s.Policy.Require(ctx, principal, workspaceID, "", action)
	if errors.Is(err, authz.ErrWorkspaceNotFound) {
		return ErrNotFound
	}
	if errors.Is(err, authz.ErrWorkspaceForbidden) {
		return ErrForbidden
	}
	return err
}

func (s Service) GetRun(ctx context.Context, principal auth.Principal, runID string) (Run, error) {
	if !validID(runID) {
		return Run{}, ErrInvalidInput
	}
	var workspaceID string
	if err := s.Store.Pool.QueryRow(ctx, `SELECT workspace_id::text FROM automation.runs WHERE organization_id = $1::uuid AND id = $2::uuid`, principal.OrganizationID, runID).Scan(&workspaceID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Run{}, ErrNotFound
		}
		return Run{}, err
	}
	if err := s.require(ctx, principal, workspaceID, "automation.read"); err != nil {
		return Run{}, err
	}
	return s.getRunByID(ctx, principal.OrganizationID, runID)
}

func (s Service) getRunByID(ctx context.Context, organizationID, runID string) (Run, error) {
	row := s.Store.Pool.QueryRow(ctx, `SELECT id::text, workspace_id::text, automation_job_id::text, source, operation, status, progress, attempt_count, error_code, error_summary, created_at, started_at, completed_at, next_attempt_at, cancel_requested, input_scope FROM automation.runs WHERE organization_id = $1::uuid AND id = $2::uuid`, organizationID, runID)
	item, err := scanRun(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Run{}, ErrNotFound
	}
	return item, err
}

func (s Service) CancelRun(ctx context.Context, principal auth.Principal, runID string) (Run, error) {
	run, err := s.GetRun(ctx, principal, runID)
	if err != nil {
		return Run{}, err
	}
	if err := s.require(ctx, principal, run.WorkspaceID, "automation.run"); err != nil {
		return Run{}, err
	}
	if run.Status != "queued" && run.Status != "running" {
		return Run{}, ErrConflict
	}
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return Run{}, err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `UPDATE automation.runs SET status = 'cancel_requested', cancel_requested = true, error_code = 'canceled', error_summary = 'cancellation requested' WHERE organization_id = $1::uuid AND id = $2::uuid AND status IN ('queued', 'running')`, principal.OrganizationID, runID); err != nil {
		return Run{}, err
	}
	if err := insertRunEvent(ctx, tx, principal.OrganizationID, runID, "run.cancel_requested", map[string]any{}); err != nil {
		return Run{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Run{}, err
	}
	return s.GetRun(ctx, principal, runID)
}


// ClaimNextRun atomically leases one queued run for a worker.
func (s Service) ClaimNextRun(ctx context.Context, workerID string, lease time.Duration) (ClaimedRun, error) {
	workerID = strings.TrimSpace(workerID)
	if workerID == "" || s.Store == nil || s.Store.Pool == nil {
		return ClaimedRun{}, ErrInvalidInput
	}
	if lease <= 0 {
		lease = 10 * time.Minute
	}
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return ClaimedRun{}, err
	}
	defer tx.Rollback(ctx)
	var canceledID, canceledOrg string
	err = tx.QueryRow(ctx, `
		SELECT id::text, organization_id::text
		FROM automation.runs r
		WHERE r.status = 'cancel_requested'
		  AND NOT EXISTS (SELECT 1 FROM automation.attempts a WHERE a.run_id = r.id AND a.status = 'started')
		ORDER BY r.created_at, r.id
		FOR UPDATE OF r SKIP LOCKED LIMIT 1
	`).Scan(&canceledID, &canceledOrg)
	if err == nil {
		if _, err := tx.Exec(ctx, `UPDATE automation.runs SET status = 'canceled', progress = 100, completed_at = now(), error_code = 'canceled', error_summary = 'run was canceled' WHERE id = $1::uuid`, canceledID); err != nil {
			return ClaimedRun{}, err
		}
		if err := syncAgentTaskForRun(ctx, tx, canceledID); err != nil {
			return ClaimedRun{}, err
		}
		if err := insertRunEvent(ctx, tx, canceledOrg, canceledID, "run.canceled", map[string]any{"progress": 100}); err != nil {
			return ClaimedRun{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return ClaimedRun{}, err
		}
		return ClaimedRun{}, ErrNoPendingRun
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return ClaimedRun{}, err
	}
	var run Run
	var maxAttempts int
	var retryBackoff []byte
	row := tx.QueryRow(ctx, `SELECT r.id::text, r.workspace_id::text, r.automation_job_id::text, r.source, r.operation, r.status, r.progress, r.attempt_count, r.error_code, r.error_summary, r.created_at, r.started_at, r.completed_at, r.next_attempt_at, r.cancel_requested, r.input_scope, COALESCE(j.max_attempts, 3), COALESCE(j.retry_backoff, '{}'::jsonb) FROM automation.runs r LEFT JOIN automation.jobs j ON j.id = r.automation_job_id WHERE r.status = 'queued' AND (r.next_attempt_at IS NULL OR r.next_attempt_at <= now()) ORDER BY r.created_at, r.id FOR UPDATE OF r SKIP LOCKED LIMIT 1`)
	if err := scanRunWithJob(row, &run, &maxAttempts, &retryBackoff); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ClaimedRun{}, ErrNoPendingRun
		}
		return ClaimedRun{}, err
	}
	var organizationID string
	if err := tx.QueryRow(ctx, `SELECT organization_id::text FROM automation.runs WHERE id = $1::uuid`, run.ID).Scan(&organizationID); err != nil {
		return ClaimedRun{}, err
	}
	attemptNo := run.AttemptCount + 1
	if maxAttempts < 1 {
		maxAttempts = 1
	}
	if attemptNo > maxAttempts {
		if _, err := tx.Exec(ctx, `UPDATE automation.runs SET status = 'failed', progress = 100, completed_at = now(), error_code = 'max_attempts_exceeded', error_summary = 'maximum attempts exceeded' WHERE id = $1::uuid`, run.ID); err != nil {
			return ClaimedRun{}, err
		}
		if err := syncAgentTaskForRun(ctx, tx, run.ID); err != nil {
			return ClaimedRun{}, err
		}
		if err := insertRunEvent(ctx, tx, organizationID, run.ID, "run.failed", map[string]any{"error_code": "max_attempts_exceeded", "error_summary": "maximum attempts exceeded"}); err != nil {
			return ClaimedRun{}, err
		}
		if err := insertRunNotification(ctx, tx, run.ID, false); err != nil {
			return ClaimedRun{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return ClaimedRun{}, err
		}
		return ClaimedRun{}, ErrNoPendingRun
	}
	var attempt Attempt
	if err := tx.QueryRow(ctx, `INSERT INTO automation.attempts (run_id, attempt_no, status, claimed_by, lease_expires_at) VALUES ($1::uuid, $2, 'started', $3, now() + $4::interval) RETURNING id::text, run_id::text, attempt_no, status, error_code, error_summary, claimed_by, lease_expires_at, next_retry_at, started_at, completed_at`, run.ID, attemptNo, workerID, intervalLiteral(lease)).Scan(&attempt.ID, &attempt.RunID, &attempt.AttemptNo, &attempt.Status, &attempt.ErrorCode, &attempt.ErrorSummary, &attempt.ClaimedBy, &attempt.LeaseExpiresAt, &attempt.NextRetryAt, &attempt.StartedAt, &attempt.CompletedAt); err != nil {
		return ClaimedRun{}, err
	}
	if err := tx.QueryRow(ctx, `UPDATE automation.runs SET status = 'running', attempt_count = $2, started_at = COALESCE(started_at, now()), next_attempt_at = NULL, error_code = NULL, error_summary = NULL WHERE id = $1::uuid RETURNING id::text, workspace_id::text, automation_job_id::text, source, operation, status, progress, attempt_count, error_code, error_summary, created_at, started_at, completed_at, next_attempt_at, cancel_requested, input_scope`, run.ID, attemptNo).Scan(&run.ID, &run.WorkspaceID, &run.AutomationJobID, &run.Source, &run.Operation, &run.Status, &run.Progress, &run.AttemptCount, &run.ErrorCode, &run.ErrorSummary, &run.CreatedAt, &run.StartedAt, &run.CompletedAt, &run.NextAttemptAt, &run.CancelRequested, &run.InputScope); err != nil {
		return ClaimedRun{}, err
	}
	if err := syncAgentTaskForRun(ctx, tx, run.ID); err != nil {
		return ClaimedRun{}, err
	}
	if err := insertRunEvent(ctx, tx, organizationID, run.ID, "run.running", map[string]any{"attempt_no": attemptNo}); err != nil {
		return ClaimedRun{}, err
	}
	_ = retryBackoff
	if err := tx.Commit(ctx); err != nil {
		return ClaimedRun{}, err
	}
	return ClaimedRun{Run: run, Attempt: attempt}, nil
}

func (s Service) RenewAttempt(ctx context.Context, attemptID, workerID string, lease time.Duration) (Attempt, error) {
	if !validID(attemptID) || strings.TrimSpace(workerID) == "" {
		return Attempt{}, ErrInvalidInput
	}
	if lease <= 0 {
		lease = 10 * time.Minute
	}
	row := s.Store.Pool.QueryRow(ctx, `UPDATE automation.attempts SET lease_expires_at = now() + $3::interval, updated_at = now() WHERE id = $1::uuid AND claimed_by = $2 AND status = 'started' RETURNING id::text, run_id::text, attempt_no, status, error_code, error_summary, claimed_by, lease_expires_at, next_retry_at, started_at, completed_at`, attemptID, strings.TrimSpace(workerID), intervalLiteral(lease))
	item, err := scanAttempt(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Attempt{}, ErrConflict
	}
	return item, err
}

func (s Service) FinishAttempt(ctx context.Context, attemptID, workerID string, success bool, errorCode, errorSummary string) (Run, error) {
	if !validID(attemptID) || strings.TrimSpace(workerID) == "" {
		return Run{}, ErrInvalidInput
	}
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return Run{}, err
	}
	defer tx.Rollback(ctx)
	var attempt Attempt
	var runID, organizationID string
	var runStatus string
	var maxAttempts int
	var retryBackoff []byte
	if err := tx.QueryRow(ctx, `SELECT a.id::text, a.run_id::text, a.attempt_no, a.status, a.error_code, a.error_summary, a.claimed_by, a.lease_expires_at, a.next_retry_at, a.started_at, a.completed_at, r.organization_id::text, r.status, COALESCE(j.max_attempts, 3), COALESCE(j.retry_backoff, '{}'::jsonb) FROM automation.attempts a JOIN automation.runs r ON r.id = a.run_id LEFT JOIN automation.jobs j ON j.id = r.automation_job_id WHERE a.id = $1::uuid FOR UPDATE OF a, r`, attemptID).Scan(&attempt.ID, &attempt.RunID, &attempt.AttemptNo, &attempt.Status, &attempt.ErrorCode, &attempt.ErrorSummary, &attempt.ClaimedBy, &attempt.LeaseExpiresAt, &attempt.NextRetryAt, &attempt.StartedAt, &attempt.CompletedAt, &organizationID, &runStatus, &maxAttempts, &retryBackoff); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Run{}, ErrNotFound
		}
		return Run{}, err
	}
	if attempt.Status != "started" || attempt.ClaimedBy == nil || *attempt.ClaimedBy != strings.TrimSpace(workerID) {
		return Run{}, ErrConflict
	}
	if attempt.LeaseExpiresAt != nil && attempt.LeaseExpiresAt.Before(time.Now().UTC()) {
		return Run{}, ErrConflict
	}
	if maxAttempts < 1 {
		maxAttempts = 1
	}
	runID = attempt.RunID
	cancellationRequested := runStatus == "cancel_requested" || runStatus == "canceled"
	if cancellationRequested {
		success = false
		errorCode = "canceled"
		errorSummary = "run was canceled"
	}
	status := "failed"
	var nextRetry *time.Time
	if success {
		status = "succeeded"
	} else if attempt.AttemptNo < maxAttempts && !cancellationRequested {
		next := time.Now().UTC().Add(backoffDuration(decodeMap(retryBackoff), attempt.AttemptNo))
		nextRetry = &next
	} else if cancellationRequested {
		status = "canceled"
	}
	if _, err := tx.Exec(ctx, `UPDATE automation.attempts SET status = $2, error_code = NULLIF($3, ''), error_summary = NULLIF($4, ''), next_retry_at = $5, completed_at = now(), lease_expires_at = NULL, updated_at = now() WHERE id = $1::uuid AND status = 'started'`, attemptID, status, errorCode, errorSummary, nextRetry); err != nil {
		return Run{}, err
	}
	if success {
		_, err = tx.Exec(ctx, `UPDATE automation.runs SET status = 'succeeded', progress = 100, completed_at = now(), error_code = NULL, error_summary = NULL, next_attempt_at = NULL WHERE id = $1::uuid`, runID)
	} else if nextRetry != nil {
		_, err = tx.Exec(ctx, `UPDATE automation.runs SET status = 'queued', error_code = NULLIF($2, ''), error_summary = NULLIF($3, ''), next_attempt_at = $4, completed_at = NULL WHERE id = $1::uuid`, runID, errorCode, errorSummary, nextRetry)
	} else if cancellationRequested {
		_, err = tx.Exec(ctx, `UPDATE automation.runs SET status = 'canceled', progress = 100, completed_at = COALESCE(completed_at, now()), error_code = NULLIF($2, ''), error_summary = NULLIF($3, '') WHERE id = $1::uuid`, runID, errorCode, errorSummary)
	} else {
		_, err = tx.Exec(ctx, `UPDATE automation.runs SET status = 'failed', progress = 100, completed_at = now(), error_code = NULLIF($2, ''), error_summary = NULLIF($3, '') WHERE id = $1::uuid`, runID, errorCode, errorSummary)
	}
	if err != nil {
		return Run{}, err
	}
	if nextRetry == nil {
		if err := syncAgentTaskForRun(ctx, tx, runID); err != nil {
			return Run{}, err
		}
	}
	eventType := "run.failed"
	eventPayload := map[string]any{"attempt_no": attempt.AttemptNo, "error_code": errorCode, "error_summary": errorSummary}
	if success {
		eventType = "run.succeeded"
		eventPayload = map[string]any{"attempt_no": attempt.AttemptNo, "progress": 100}
	} else if nextRetry != nil {
		eventType = "run.queued"
		eventPayload["next_attempt_at"] = nextRetry
	} else if cancellationRequested {
		eventType = "run.canceled"
	}
	if err := insertRunEvent(ctx, tx, organizationID, runID, eventType, eventPayload); err != nil {
		return Run{}, err
	}
	if success || (!cancellationRequested && nextRetry == nil) {
		if err := insertRunNotification(ctx, tx, runID, success); err != nil {
			return Run{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return Run{}, err
	}
	return s.getRunByID(ctx, organizationID, runID)
}

// RequeueExpiredAttempts releases leases left behind by a crashed worker.
// It is intentionally transaction-based so a run is never requeued without
// its corresponding attempt being marked terminal.
func (s Service) RequeueExpiredAttempts(ctx context.Context, limit int) (int, error) {
	if s.Store == nil || s.Store.Pool == nil {
		return 0, ErrInvalidInput
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `
		SELECT a.id::text, a.run_id::text, a.attempt_no, r.organization_id::text, r.status,
		       COALESCE(j.max_attempts, 3), COALESCE(j.retry_backoff, '{}'::jsonb)
		FROM automation.attempts a
		JOIN automation.runs r ON r.id = a.run_id
		LEFT JOIN automation.jobs j ON j.id = r.automation_job_id
		WHERE a.status = 'started' AND a.lease_expires_at IS NOT NULL AND a.lease_expires_at < now()
		ORDER BY a.lease_expires_at, a.id
		FOR UPDATE OF a, r SKIP LOCKED
		LIMIT $1`, limit)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	type expired struct {
		attemptID, runID, organizationID, runStatus string
		attemptNo, maxAttempts                      int
		retryBackoff                                []byte
	}
	items := []expired{}
	for rows.Next() {
		var item expired
		if err := rows.Scan(&item.attemptID, &item.runID, &item.attemptNo, &item.organizationID, &item.runStatus, &item.maxAttempts, &item.retryBackoff); err != nil {
			return 0, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	rows.Close()
	for _, item := range items {
		status := "failed"
		var nextRetry *time.Time
		cancellationRequested := item.runStatus == "cancel_requested" || item.runStatus == "canceled"
		if cancellationRequested {
			status = "canceled"
		} else if item.attemptNo < item.maxAttempts {
			next := time.Now().UTC().Add(backoffDuration(decodeMap(item.retryBackoff), item.attemptNo))
			nextRetry = &next
		}
		if _, err := tx.Exec(ctx, `UPDATE automation.attempts SET status = $2, error_code = 'lease_expired', error_summary = 'worker lease expired', next_retry_at = $3, completed_at = now(), lease_expires_at = NULL, updated_at = now() WHERE id = $1::uuid AND status = 'started'`, item.attemptID, status, nextRetry); err != nil {
			return 0, err
		}
		if nextRetry != nil {
			if _, err := tx.Exec(ctx, `UPDATE automation.runs SET status = 'queued', error_code = 'lease_expired', error_summary = 'worker lease expired', next_attempt_at = $2, completed_at = NULL WHERE id = $1::uuid AND status = 'running'`, item.runID, nextRetry); err != nil {
				return 0, err
			}
			if err := insertRunEvent(ctx, tx, item.organizationID, item.runID, "run.queued", map[string]any{"error_code": "lease_expired", "error_summary": "worker lease expired", "next_attempt_at": nextRetry}); err != nil {
				return 0, err
			}
		} else if cancellationRequested {
			if _, err := tx.Exec(ctx, `UPDATE automation.runs SET status = 'canceled', progress = 100, error_code = 'canceled', error_summary = 'run was canceled', completed_at = now() WHERE id = $1::uuid`, item.runID); err != nil {
				return 0, err
			}
			if err := insertRunEvent(ctx, tx, item.organizationID, item.runID, "run.canceled", map[string]any{"error_code": "canceled", "error_summary": "run was canceled"}); err != nil {
				return 0, err
			}
		} else {
			if _, err := tx.Exec(ctx, `UPDATE automation.runs SET status = 'failed', progress = 100, error_code = 'lease_expired', error_summary = 'worker lease expired', completed_at = now() WHERE id = $1::uuid AND status = 'running'`, item.runID); err != nil {
				return 0, err
			}
			if err := insertRunEvent(ctx, tx, item.organizationID, item.runID, "run.failed", map[string]any{"error_code": "lease_expired", "error_summary": "worker lease expired"}); err != nil {
				return 0, err
			}
			if err := insertRunNotification(ctx, tx, item.runID, false); err != nil {
				return 0, err
			}
		}
		if nextRetry == nil {
			if err := syncAgentTaskForRun(ctx, tx, item.runID); err != nil {
				return 0, err
			}
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return len(items), nil
}

func (s Service) ListAttempts(ctx context.Context, principal auth.Principal, runID string) ([]Attempt, error) {
	if !validID(runID) {
		return nil, ErrInvalidInput
	}
	run, err := s.GetRun(ctx, principal, runID)
	if err != nil {
		return nil, err
	}
	rows, err := s.Store.Pool.Query(ctx, `SELECT id::text, run_id::text, attempt_no, status, error_code, error_summary, claimed_by, lease_expires_at, next_retry_at, started_at, completed_at FROM automation.attempts WHERE run_id = $1::uuid ORDER BY attempt_no`, run.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []Attempt{}
	for rows.Next() {
		item, scanErr := scanAttempt(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func scanRun(row interface{ Scan(...any) error }) (Run, error) {
	var item Run
	var scope []byte
	err := row.Scan(&item.ID, &item.WorkspaceID, &item.AutomationJobID, &item.Source, &item.Operation, &item.Status, &item.Progress, &item.AttemptCount, &item.ErrorCode, &item.ErrorSummary, &item.CreatedAt, &item.StartedAt, &item.CompletedAt, &item.NextAttemptAt, &item.CancelRequested, &scope)
	item.InputScope = decodeMap(scope)
	return item, err
}
func scanRunWithJob(row interface{ Scan(...any) error }, run *Run, maxAttempts *int, retryBackoff *[]byte) error {
	var scope []byte
	err := row.Scan(&run.ID, &run.WorkspaceID, &run.AutomationJobID, &run.Source, &run.Operation, &run.Status, &run.Progress, &run.AttemptCount, &run.ErrorCode, &run.ErrorSummary, &run.CreatedAt, &run.StartedAt, &run.CompletedAt, &run.NextAttemptAt, &run.CancelRequested, &scope, maxAttempts, retryBackoff)
	run.InputScope = decodeMap(scope)
	return err
}
func scanAttempt(row interface{ Scan(...any) error }) (Attempt, error) {
	var item Attempt
	err := row.Scan(&item.ID, &item.RunID, &item.AttemptNo, &item.Status, &item.ErrorCode, &item.ErrorSummary, &item.ClaimedBy, &item.LeaseExpiresAt, &item.NextRetryAt, &item.StartedAt, &item.CompletedAt)
	return item, err
}
func decodeMap(raw []byte) map[string]any {
	result := map[string]any{}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &result)
	}
	return result
}
func mustJSON(value any) []byte { body, _ := json.Marshal(value); return body }

func insertRunEvent(ctx context.Context, tx pgx.Tx, organizationID, runID, eventType string, payload any) error {
	_, err := tx.Exec(ctx, `INSERT INTO automation.run_events (organization_id, run_id, event_type, payload) VALUES ($1::uuid, $2::uuid, $3, $4::jsonb)`, organizationID, runID, eventType, mustJSON(payload))
	return err
}

func syncAgentTaskForRun(ctx context.Context, tx pgx.Tx, runID string) error {
	_, err := tx.Exec(ctx, `
		UPDATE integration.agent_tasks t
		SET status = CASE r.status
				WHEN 'queued' THEN 'queued'
				WHEN 'running' THEN 'running'
				WHEN 'succeeded' THEN 'succeeded'
				WHEN 'failed' THEN 'failed'
				WHEN 'canceled' THEN 'cancelled'
				ELSE t.status
			END,
			candidate_version_id = CASE WHEN r.status = 'succeeded'
				THEN NULLIF(r.output_snapshot->>'candidate_version_id', '')::uuid
				ELSE t.candidate_version_id END,
			error_code = CASE WHEN r.status IN ('failed', 'canceled') THEN r.error_code ELSE NULL END,
			completed_at = CASE WHEN r.status IN ('succeeded', 'failed', 'canceled') THEN r.completed_at ELSE NULL END
		FROM automation.runs r
		WHERE r.id = $1::uuid AND r.agent_task_id = t.id
		  AND r.status IN ('queued', 'running', 'succeeded', 'failed', 'canceled')
	`, runID)
	return err
}

func insertRunNotification(ctx context.Context, tx pgx.Tx, runID string, succeeded bool) error {
	notificationType, title, body := "task_failed", "Task failed", "The task could not be completed."
	if succeeded {
		notificationType, title, body = "task_completed", "Task completed", "The task completed successfully."
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO content.notifications (
			organization_id, workspace_id, recipient_user_id, kind, payload
		)
		SELECT organization_id, workspace_id, created_by, 'system',
		       jsonb_build_object('type', $2::text, 'title', $3::text, 'body', $4::text,
		                          'object_type', 'task_run', 'object_id', id::text,
		                          'status', status, 'operation', operation)
		FROM automation.runs WHERE id = $1::uuid
	`, runID, notificationType, title, body)
	return err
}

func intervalLiteral(value time.Duration) string {
	seconds := int64(value / time.Second)
	if seconds < 1 {
		seconds = 1
	}
	return fmt.Sprintf("%d seconds", seconds)
}
func backoffDuration(config map[string]any, attemptNo int) time.Duration {
	base := numberConfig(config, "base_seconds", 10)
	max := numberConfig(config, "max_seconds", 300)
	if base < 1 {
		base = 1
	}
	if max < base {
		max = base
	}
	delay := base
	for i := 1; i < attemptNo && delay < max; i++ {
		delay *= 2
	}
	if delay > max {
		delay = max
	}
	return time.Duration(delay) * time.Second
}
func numberConfig(config map[string]any, key string, fallback int64) int64 {
	value, ok := config[key]
	if !ok {
		return fallback
	}
	switch number := value.(type) {
	case float64:
		return int64(number)
	case int:
		return int64(number)
	case int64:
		return number
	default:
		return fallback
	}
}
func validID(value string) bool {
	value = strings.TrimSpace(value)
	if len(value) != 36 {
		return false
	}
	for index, char := range value {
		if index == 8 || index == 13 || index == 18 || index == 23 {
			if char != '-' {
				return false
			}
			continue
		}
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') && (char < 'A' || char > 'F') {
			return false
		}
	}
	return true
}
