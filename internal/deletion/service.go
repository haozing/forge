package deletion

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"agentchunzhi/internal/auth"
	"agentchunzhi/internal/store"

	"github.com/jackc/pgx/v5"
)

var (
	ErrInvalidInput = errors.New("invalid deletion job input")
	ErrNotFound     = errors.New("deletion job not found")
	ErrConflict     = errors.New("deletion job idempotency conflict")
	ErrNoPendingJob = errors.New("no pending deletion job")
)

type Job struct {
	ID           string     `json:"id"`
	WorkspaceID  string     `json:"workspace_id"`
	ResourceType string     `json:"resource_type"`
	ResourceID   string     `json:"resource_id"`
	Status       string     `json:"status"`
	ErrorCode    string     `json:"error_code,omitempty"`
	ErrorSummary string     `json:"error_summary,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
	StartedAt    *time.Time `json:"started_at,omitempty"`
	CompletedAt  *time.Time `json:"completed_at,omitempty"`
}

type Service struct {
	Store *store.Store
}

func (s Service) Enqueue(ctx context.Context, principal auth.Principal, workspaceID, resourceType, resourceID, idempotencyKey string) (Job, error) {
	workspaceID = strings.TrimSpace(workspaceID)
	resourceType = strings.TrimSpace(resourceType)
	resourceID = strings.TrimSpace(resourceID)
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	if principal.UserType != "member" || !validID(principal.OrganizationID) || !validID(principal.UserID) ||
		!validID(workspaceID) || !validID(resourceID) || (resourceType != "asset" && resourceType != "workspace") ||
		len(idempotencyKey) < 16 || len(idempotencyKey) > 200 {
		return Job{}, ErrInvalidInput
	}
	if resourceType == "workspace" && resourceID != workspaceID {
		return Job{}, ErrInvalidInput
	}
	if s.Store == nil || s.Store.Pool == nil {
		return Job{}, errors.New("database store is not initialized")
	}
	hash := requestHash(workspaceID, resourceType, resourceID)
	var job Job
	err := s.Store.Pool.QueryRow(ctx, `
		INSERT INTO content.deletion_jobs
			(organization_id, workspace_id, resource_type, resource_id, requested_by, idempotency_key, request_hash)
		VALUES ($1::uuid, $2::uuid, $3, $4::uuid, $5::uuid, $6, $7)
		ON CONFLICT (organization_id, requested_by, resource_type, idempotency_key) DO NOTHING
		RETURNING id::text, workspace_id::text, resource_type, resource_id::text, status,
		          COALESCE(error_code, ''), COALESCE(error_summary, ''), created_at, started_at, completed_at
	`, principal.OrganizationID, workspaceID, resourceType, resourceID, principal.UserID, idempotencyKey, hash).Scan(jobDestinations(&job)...)
	if err == nil {
		return job, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Job{}, fmt.Errorf("enqueue deletion job: %w", err)
	}
	var storedHash string
	err = s.Store.Pool.QueryRow(ctx, `
		SELECT id::text, workspace_id::text, resource_type, resource_id::text, status,
		       COALESCE(error_code, ''), COALESCE(error_summary, ''), created_at, started_at, completed_at, request_hash
		FROM content.deletion_jobs
		WHERE organization_id = $1::uuid AND requested_by = $2::uuid
		  AND resource_type = $3 AND idempotency_key = $4
	`, principal.OrganizationID, principal.UserID, resourceType, idempotencyKey).Scan(append(jobDestinations(&job), &storedHash)...)
	if errors.Is(err, pgx.ErrNoRows) {
		return Job{}, ErrConflict
	}
	if err != nil {
		return Job{}, fmt.Errorf("load idempotent deletion job: %w", err)
	}
	if storedHash != hash {
		return Job{}, ErrConflict
	}
	return job, nil
}

func (s Service) Get(ctx context.Context, principal auth.Principal, jobID string) (Job, error) {
	if principal.UserType != "member" || !validID(principal.OrganizationID) || !validID(jobID) {
		return Job{}, ErrNotFound
	}
	if s.Store == nil || s.Store.Pool == nil {
		return Job{}, errors.New("database store is not initialized")
	}
	var job Job
	err := s.Store.Pool.QueryRow(ctx, `
		SELECT id::text, workspace_id::text, resource_type, resource_id::text, status,
		       COALESCE(error_code, ''), COALESCE(error_summary, ''), created_at, started_at, completed_at
		FROM content.deletion_jobs
		WHERE organization_id = $1::uuid AND id = $2::uuid
		  AND (requested_by = $3::uuid OR EXISTS (
		      SELECT 1 FROM content.workspace_members wm
		      WHERE wm.organization_id = content.deletion_jobs.organization_id
		        AND wm.workspace_id = content.deletion_jobs.workspace_id
		        AND wm.user_id = $3::uuid AND wm.role = 'admin'
		  ))
	`, principal.OrganizationID, jobID, principal.UserID).Scan(jobDestinations(&job)...)
	if errors.Is(err, pgx.ErrNoRows) {
		return Job{}, ErrNotFound
	}
	if err != nil {
		return Job{}, fmt.Errorf("load deletion job: %w", err)
	}
	return job, nil
}

func jobDestinations(job *Job) []any {
	return []any{&job.ID, &job.WorkspaceID, &job.ResourceType, &job.ResourceID, &job.Status,
		&job.ErrorCode, &job.ErrorSummary, &job.CreatedAt, &job.StartedAt, &job.CompletedAt}
}

func requestHash(workspaceID, resourceType, resourceID string) string {
	sum := sha256.Sum256([]byte(workspaceID + "\x00" + resourceType + "\x00" + resourceID))
	return hex.EncodeToString(sum[:])
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
		if !((char >= '0' && char <= '9') || (char >= 'a' && char <= 'f') || (char >= 'A' && char <= 'F')) {
			return false
		}
	}
	return true
}
