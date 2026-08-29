package asset

// preparation.go — persists the candidate produced by the fixed asset_prepare
// graph. The graph returns data; this service re-checks permissions, validates
// the candidate and materializes it as a new sealed version (origin
// ai_generated, parent = source version) through CreateVersionTx inside one
// transaction, then emits the version fact and the audit entry. There is no
// processing claim: candidate versions are legitimate snapshots, not states.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"agentchunzhi/internal/auth"
	"agentchunzhi/internal/authz"
	"agentchunzhi/internal/eventing"
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
		return PrepareResult{}, fmt.Errorf("execute asset_prepare graph: %w", err)
	}
	fields := cloneMap(output.Candidate)
	delete(fields, "asset_ids")
	if err := ValidateFields(metadata.FieldSchema, fields); err != nil {
		return PrepareResult{}, fmt.Errorf("validate asset preparation fields: %w", err)
	}
	if err := ValidateContent(metadata.Title, metadata.Markdown, &fields); err != nil {
		return PrepareResult{}, fmt.Errorf("validate asset preparation content: %w", err)
	}
	// Re-check the permission after the model call so a policy revoked while
	// the graph ran cannot land a candidate.
	if err := s.checkPermission(ctx, principal, metadata.ResourceModelID); err != nil {
		return PrepareResult{}, err
	}
	result, err := s.persistCandidate(ctx, req, metadata, fields)
	if err != nil {
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
	Sealed          bool
}

func (s AssetPreparationService) loadSource(ctx context.Context, req PrepareRequest) (preparationMetadata, error) {
	var metadata preparationMetadata
	var fields []byte
	err := s.Store.Pool.QueryRow(ctx, `
		SELECT av.organization_id::text, a.workspace_id::text, av.asset_id::text,
		       av.resource_model_id::text, av.resource_model_version_id::text,
		       av.version_no, av.title, av.markdown, av.fields, mv.field_schema,
		       (av.sealed_at IS NOT NULL)
		FROM asset.asset_versions av
		JOIN asset.assets a ON a.organization_id = av.organization_id AND a.id = av.asset_id
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
		&metadata.Markdown, &fields, &metadata.FieldSchema, &metadata.Sealed)
	if errors.Is(err, pgx.ErrNoRows) {
		return preparationMetadata{}, ErrNotFound
	}
	if err != nil {
		return preparationMetadata{}, fmt.Errorf("load asset preparation source: %w", err)
	}
	if !metadata.Sealed {
		return preparationMetadata{}, fmt.Errorf("%w: preparation source version is not sealed", ErrConflict)
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
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return PrepareResult{}, err
	}
	defer tx.Rollback(ctx)
	row, err := LoadLifecycleTx(ctx, tx, metadata.OrganizationID, metadata.AssetID)
	if err != nil {
		return PrepareResult{}, err
	}
	if row.PublicationStatus == PublicationArchived {
		return PrepareResult{}, ErrAssetArchived
	}
	// The candidate inherits tag/attachment provenance from its source
	// snapshot; confirmation never carries over.
	tagIDs, err := loadVersionTagIDs(ctx, tx, req.AssetVersionID)
	if err != nil {
		return PrepareResult{}, err
	}
	attachmentIDs, err := loadVersionAttachmentIDs(ctx, tx, req.AssetVersionID)
	if err != nil {
		return PrepareResult{}, err
	}
	candidateID, versionNo, err := CreateVersionTx(ctx, tx, VersionMaterial{
		OrganizationID:         metadata.OrganizationID,
		WorkspaceID:            metadata.WorkspaceID,
		AssetID:                metadata.AssetID,
		ResourceModelID:        metadata.ResourceModelID,
		ResourceModelVersionID: metadata.ModelVersionID,
		ParentVersionID:        req.AssetVersionID,
		Origin:                 OriginAIGenerated,
		ConfirmationStatus:     ConfirmationUnconfirmed,
		Title:                  pointerValue(metadata.Title),
		Markdown:               pointerValue(metadata.Markdown),
		Fields:                 fields,
		TagIDs:                 tagIDs,
		AttachmentIDs:          attachmentIDs,
		CreatedBy:              req.AgentUserID,
	})
	if err != nil {
		return PrepareResult{}, err
	}
	next := row
	next.CurrentWorkingVersionID = candidateID
	next.Revision++
	actor := auth.Principal{OrganizationID: metadata.OrganizationID, UserID: req.AgentUserID, UserType: "agent"}
	if err := AppendAssetEventTx(ctx, tx, &s.Events, next, actor, eventing.EventAssetVersionCreated, eventing.PayloadVersionV1, eventing.AssetVersionCreatedPayload{
		AssetID:     metadata.AssetID,
		VersionID:   candidateID,
		VersionNo:   versionNo,
		WorkspaceID: metadata.WorkspaceID,
	}); err != nil {
		return PrepareResult{}, err
	}
	RecordAssetAuditTx(ctx, tx, metadata.OrganizationID, metadata.WorkspaceID, actor, "asset.version.prepared", metadata.AssetID, map[string]any{
		"workspace_id":      metadata.WorkspaceID,
		"source_version_id": req.AssetVersionID,
		"version_id":        candidateID,
	})
	if err := tx.Commit(ctx); err != nil {
		return PrepareResult{}, err
	}
	return PrepareResult{CandidateVersionID: candidateID}, nil
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

func pointerValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
