package asset

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"agentchunzhi/internal/auth"
	"agentchunzhi/internal/authz"
	"agentchunzhi/internal/eventing"
	"agentchunzhi/internal/retrieval"
	"agentchunzhi/internal/store"
	"agentchunzhi/internal/workflows"

	"github.com/jackc/pgx/v5"
)

var ErrPreparationPermissionRevoked = errors.New("asset preparation permission has been revoked")

type PrepareRequest struct {
	OrganizationID     string
	WorkspaceID        string
	AgentUserID        string
	AgentApplicationID string
	ModelEndpointID    string
	ModelRevision      int64
	RunID              string
	AssetVersionID     string
}

type PrepareResult struct {
	CandidateVersionID string
	InputTokens        int
	OutputTokens       int
}

// AssetPreparationService is the only service allowed to persist a candidate
// produced by the fixed asset_prepare graph. The graph returns data; this
// service owns the claim, re-check, transaction, projection event and reset.
type AssetPreparationService struct {
	Store       *store.Store
	Events      eventing.EventStore
	Permissions authz.ScopeResolver
	Workflow    workflows.Runnable
}

func (s AssetPreparationService) Prepare(ctx context.Context, req PrepareRequest) (PrepareResult, error) {
	if s.Store == nil || s.Store.Pool == nil || s.Workflow == nil || strings.TrimSpace(req.AssetVersionID) == "" ||
		strings.TrimSpace(req.AgentUserID) == "" || strings.TrimSpace(req.AgentApplicationID) == "" ||
		strings.TrimSpace(req.ModelEndpointID) == "" || req.ModelRevision <= 0 {
		return PrepareResult{}, errors.New("asset preparation service is not initialized")
	}
	metadata, err := s.loadSource(ctx, req)
	if err != nil {
		return PrepareResult{}, err
	}
	principal := auth.Principal{OrganizationID: metadata.OrganizationID, UserID: req.AgentUserID, UserType: "agent"}
	if err := s.checkPermission(ctx, principal, metadata.ResourceModelID); err != nil {
		return PrepareResult{}, err
	}
	claimed, err := s.Store.Pool.Exec(ctx, `
		UPDATE asset.asset_versions SET processing_started_at = now()
		WHERE id = $1::uuid AND workflow_status = 'submitted'
		  AND (processing_started_at IS NULL OR processing_started_at < now() - interval '10 minutes')
	`, req.AssetVersionID)
	if err != nil {
		return PrepareResult{}, fmt.Errorf("claim asset preparation source: %w", err)
	}
	if claimed.RowsAffected() != 1 {
		return PrepareResult{}, errors.New("asset preparation source is already being processed")
	}

	values := cloneMap(metadata.Fields)
	output, err := s.Workflow.Invoke(ctx, workflows.Input{
		OrganizationID:     metadata.OrganizationID,
		WorkspaceID:        metadata.WorkspaceID,
		RunID:              req.RunID,
		AgentApplicationID: req.AgentApplicationID,
		ModelEndpointID:    req.ModelEndpointID,
		ModelRevision:      req.ModelRevision,
		AssetIDs:           []string{metadata.AssetID},
		Title:              pointerValue(metadata.Title),
		Markdown:           pointerValue(metadata.Markdown),
		FieldSchema:        append(json.RawMessage(nil), metadata.FieldSchema...),
		Values:             values,
	})
	if err != nil {
		_ = s.reset(ctx, req.AssetVersionID)
		return PrepareResult{}, fmt.Errorf("execute asset_prepare graph: %w", err)
	}
	fields := cloneMap(output.Candidate)
	delete(fields, "asset_ids")
	delete(fields, "workflow_status")
	if err := ValidateFields(metadata.FieldSchema, fields); err != nil {
		_ = s.reset(ctx, req.AssetVersionID)
		return PrepareResult{}, fmt.Errorf("validate asset preparation fields: %w", err)
	}
	if err := ValidateContent(metadata.Title, metadata.Markdown, &fields); err != nil {
		_ = s.reset(ctx, req.AssetVersionID)
		return PrepareResult{}, fmt.Errorf("validate asset preparation content: %w", err)
	}
	if err := s.checkPermission(ctx, principal, metadata.ResourceModelID); err != nil {
		_ = s.reset(ctx, req.AssetVersionID)
		return PrepareResult{}, err
	}
	result, err := s.persistCandidate(ctx, req, metadata, fields)
	if err != nil {
		_ = s.reset(ctx, req.AssetVersionID)
		return PrepareResult{}, err
	}
	result.InputTokens = output.InputTokens
	result.OutputTokens = output.OutputTokens
	return result, nil
}

type preparationMetadata struct {
	OrganizationID  string
	WorkspaceID     string
	AssetID         string
	ResourceModelID string
	ModelVersionID  string
	VersionNo       int
	Title           *string
	Markdown        *string
	Fields          map[string]any
	FieldSchema     []byte
	Status          string
}

func (s AssetPreparationService) loadSource(ctx context.Context, req PrepareRequest) (preparationMetadata, error) {
	var metadata preparationMetadata
	var fields []byte
	err := s.Store.Pool.QueryRow(ctx, `
		SELECT av.organization_id::text, a.workspace_id::text, av.asset_id::text,
		       av.resource_model_id::text, av.resource_model_version_id::text,
		       av.version_no, av.title, av.markdown, av.fields, mv.field_schema, av.workflow_status
		FROM asset.asset_versions av
		JOIN asset.assets a ON a.id = av.asset_id
		JOIN model.resource_model_versions mv ON mv.id = av.resource_model_version_id
		JOIN integration.agent_applications aa ON aa.id = $5::uuid
		  AND aa.organization_id = av.organization_id
		  AND aa.bound_agent_user_id = $2::uuid
		  AND aa.model_endpoint_id = $6::uuid
		  AND aa.status = 'active' AND aa.runtime_mode = 'workflow' AND aa.workflow_key = 'asset_prepare'
		JOIN integration.model_endpoints me ON me.id = aa.model_endpoint_id AND me.organization_id = aa.organization_id AND me.status = 'active'
		JOIN integration.model_endpoint_revisions mer ON mer.model_endpoint_id = me.id AND mer.revision = $7
		  AND mer.revoked_at IS NULL
		WHERE av.id = $1::uuid AND av.organization_id = $3::uuid AND a.workspace_id = $4::uuid
	`, req.AssetVersionID, req.AgentUserID, req.OrganizationID, req.WorkspaceID,
		req.AgentApplicationID, req.ModelEndpointID, req.ModelRevision).Scan(
		&metadata.OrganizationID, &metadata.WorkspaceID, &metadata.AssetID,
		&metadata.ResourceModelID, &metadata.ModelVersionID, &metadata.VersionNo, &metadata.Title,
		&metadata.Markdown, &fields, &metadata.FieldSchema, &metadata.Status)
	if errors.Is(err, pgx.ErrNoRows) {
		return preparationMetadata{}, ErrNotFound
	}
	if err != nil {
		return preparationMetadata{}, fmt.Errorf("load asset preparation source: %w", err)
	}
	if metadata.Status != "submitted" {
		return preparationMetadata{}, fmt.Errorf("asset preparation source is not submitted")
	}
	metadata.Fields = map[string]any{}
	if len(fields) > 0 {
		_ = json.Unmarshal(fields, &metadata.Fields)
	}
	return metadata, nil
}

func (s AssetPreparationService) checkPermission(ctx context.Context, principal auth.Principal, modelID string) error {
	if s.Permissions.Store == nil {
		return ErrPreparationPermissionRevoked
	}
	for _, action := range []string{"asset.read", "asset.edit"} {
		allowed, err := s.Permissions.AllowedModelIDs(ctx, principal, action)
		if err != nil {
			return fmt.Errorf("check asset preparation permission: %w", err)
		}
		if !containsString(allowed, modelID) {
			return ErrPreparationPermissionRevoked
		}
	}
	return nil
}

func (s AssetPreparationService) persistCandidate(ctx context.Context, req PrepareRequest, metadata preparationMetadata, fields map[string]any) (PrepareResult, error) {
	contentChecksum := hashPreparation(fields, metadata.Title, metadata.Markdown)
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return PrepareResult{}, err
	}
	defer tx.Rollback(ctx)
	var candidateID string
	if err := tx.QueryRow(ctx, `
		SELECT av.id::text FROM asset.asset_versions av
		WHERE av.id = $1::uuid AND av.organization_id = $2::uuid AND av.workflow_status = 'submitted'
		FOR UPDATE
	`, req.AssetVersionID, metadata.OrganizationID).Scan(new(string)); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return PrepareResult{}, nil
		}
		return PrepareResult{}, err
	}
	if err := tx.QueryRow(ctx, `
		INSERT INTO asset.asset_versions
			(organization_id, workspace_id, asset_id, resource_model_id, resource_model_version_id, version_no,
			 workflow_status, quality, title, markdown, fields, parent_version_id, content_checksum, created_by)
		VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, $5::uuid, $6,
			'draft', 'ai_generated', $7, $8, $9::jsonb, $10::uuid, $11, $12::uuid)
		RETURNING id::text
	`, metadata.OrganizationID, metadata.WorkspaceID, metadata.AssetID, metadata.ResourceModelID,
		metadata.ModelVersionID, metadata.VersionNo+1, metadata.Title, metadata.Markdown,
		string(mustJSON(fields)), req.AssetVersionID, contentChecksum, req.AgentUserID).Scan(&candidateID); err != nil {
		return PrepareResult{}, fmt.Errorf("persist asset preparation candidate: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE asset.assets SET current_working_version_id = $2::uuid, updated_at = now() WHERE id = $1::uuid`, metadata.AssetID, candidateID); err != nil {
		return PrepareResult{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE asset.asset_versions SET workflow_status = 'draft', processing_started_at = NULL WHERE id = $1::uuid`, req.AssetVersionID); err != nil {
		return PrepareResult{}, err
	}
	if err := retrieval.EnqueueProjectionTx(ctx, tx, s.Events, metadata.OrganizationID, candidateID, retrieval.ProjectionRebuild); err != nil {
		return PrepareResult{}, err
	}
	if _, err := s.Events.AppendTx(ctx, tx, eventing.Event{OrganizationID: metadata.OrganizationID, EventType: "asset.agent_candidate_created", AggregateType: "asset_version", AggregateID: candidateID, AggregateVersion: 1, PayloadVersion: 1, Payload: map[string]string{"source_version_id": req.AssetVersionID, "agent_user_id": req.AgentUserID, "candidate_version_id": candidateID}}); err != nil {
		return PrepareResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return PrepareResult{}, err
	}
	return PrepareResult{CandidateVersionID: candidateID}, nil
}

func (s AssetPreparationService) reset(ctx context.Context, versionID string) error {
	_, err := s.Store.Pool.Exec(ctx, `UPDATE asset.asset_versions SET processing_started_at = NULL WHERE id = $1::uuid AND workflow_status = 'submitted'`, versionID)
	return err
}

func cloneMap(input map[string]any) map[string]any {
	result := make(map[string]any, len(input))
	for key, value := range input {
		result[key] = value
	}
	return result
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func hashPreparation(fields map[string]any, title, markdown *string) string {
	payload, _ := json.Marshal(struct {
		Fields   map[string]any `json:"fields"`
		Title    *string        `json:"title"`
		Markdown *string        `json:"markdown"`
	}{fields, title, markdown})
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:])
}

func pointerValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
