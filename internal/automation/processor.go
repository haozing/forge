package automation

import (
	"context"
	"errors"
	"fmt"
	"strings"

	assetservice "agentchunzhi/internal/asset"
	"agentchunzhi/internal/auth"
	"agentchunzhi/internal/eventing"
	"agentchunzhi/internal/store"
	"agentchunzhi/internal/workflows"

	"github.com/jackc/pgx/v5"
)

var ErrRunWaiting = errors.New("run is waiting for interaction")

type PersistentRunProcessor interface {
	Process(context.Context, ClaimedRun) (bool, error)
}

// OperationProcessor executes the bounded set of operations exposed by the
// automation API. It deliberately receives a claimed run, so lease ownership
// and retry decisions remain in Service.FinishAttempt.
type OperationProcessor struct {
	Store       *store.Store
	Events      eventing.EventStore
	Workflows   workflows.Executor
	Preparation *assetservice.AssetPreparationService
	ReAct       PersistentRunProcessor
}

func (p OperationProcessor) Process(ctx context.Context, claimed ClaimedRun) error {
	if p.Store == nil || p.Store.Pool == nil {
		return errors.New("database store is not initialized")
	}
	var runtimeMode string
	if err := p.Store.Pool.QueryRow(ctx, `SELECT COALESCE(runtime_mode, '') FROM automation.runs WHERE id = $1::uuid`, claimed.Run.ID).Scan(&runtimeMode); err != nil {
		return fmt.Errorf("load run runtime mode: %w", err)
	}
	if runtimeMode == "react" {
		if p.ReAct == nil {
			return errors.New("persistent ReAct processor is not configured")
		}
		waiting, err := p.ReAct.Process(ctx, claimed)
		if err != nil {
			return err
		}
		if waiting {
			return ErrRunWaiting
		}
		return nil
	}
	if p.Workflows.RegistryReady() {
		var workflowKey string
		var inputSnapshot []byte
		if err := p.Store.Pool.QueryRow(ctx, `
			SELECT COALESCE(runtime_mode, ''), COALESCE(workflow_key, ''), input_snapshot
			FROM automation.runs WHERE id = $1::uuid
		`, claimed.Run.ID).Scan(&runtimeMode, &workflowKey, &inputSnapshot); err != nil {
			return fmt.Errorf("load workflow runtime metadata: %w", err)
		}
		if runtimeMode == "workflow" {
			if strings.TrimSpace(workflowKey) == "" {
				return errors.New("workflow run has no workflow key")
			}
			payload := workflows.DecodePayload(inputSnapshot)
			if workflowKey == "asset_prepare" {
				if p.Preparation == nil {
					return errors.New("asset preparation service is not configured")
				}
				var organizationID, workspaceID, agentUserID, applicationID, endpointID string
				var endpointRevision int64
				if err := p.Store.Pool.QueryRow(ctx, `
						SELECT organization_id::text, workspace_id::text, COALESCE(agent_user_id::text, ''),
						       COALESCE(agent_application_id::text, ''), COALESCE(model_endpoint_id::text, ''),
						       COALESCE(model_endpoint_revision, 0)
						FROM automation.runs WHERE id = $1::uuid
					`, claimed.Run.ID).Scan(&organizationID, &workspaceID, &agentUserID, &applicationID, &endpointID, &endpointRevision); err != nil {
					return fmt.Errorf("load asset preparation run scope: %w", err)
				}
				if agentUserID == "" || applicationID == "" || endpointID == "" || endpointRevision <= 0 {
					return errors.New("asset_prepare run requires fixed AgentApplication and ModelEndpoint revision")
				}
				versionIDs, err := p.assetPreparationVersions(ctx, claimed.Run.ID, payload)
				if err != nil {
					return err
				}
				// Phase 4: prepare produces a suggestion set per asset, not a
				// candidate version. The snapshot therefore carries the
				// processing-result ids and per-kind suggestion counts; the
				// legacy candidate_version_ids key stays as an empty array so
				// JSON consumers keyed on it do not see a nil shape (the agent
				// task sync reads the singular candidate_version_id, whose
				// absence now clears the column on success — correct, since no
				// candidate version exists in the suggestion flow).
				processingResultIDs := make([]string, 0, len(versionIDs))
				counts := make(map[string]int)
				inputTokens, outputTokens := 0, 0
				for _, assetVersionID := range versionIDs {
					result, prepareErr := p.Preparation.Prepare(ctx, assetservice.PrepareRequest{
						OrganizationID: organizationID, WorkspaceID: workspaceID, AgentUserID: agentUserID,
						AgentApplicationID: applicationID, ModelEndpointID: endpointID, ModelRevision: endpointRevision,
						RunID: claimed.Run.ID, AssetVersionID: assetVersionID,
					})
					if prepareErr != nil {
						return prepareErr
					}
					if result.ProcessingResultID != "" {
						processingResultIDs = append(processingResultIDs, result.ProcessingResultID)
					}
					for kind, count := range result.Counts {
						counts[kind] += count
					}
					inputTokens += result.InputTokens
					outputTokens += result.OutputTokens
				}
				output := map[string]any{
					"processing_result_ids": processingResultIDs,
					"counts":                counts,
					"candidate_version_ids": []string{},
				}
				if _, err := p.Store.Pool.Exec(ctx, `
					UPDATE automation.runs SET output_snapshot = $2::jsonb,
						input_tokens = input_tokens + $3, output_tokens = output_tokens + $4
					WHERE id = $1::uuid AND status = 'running'
				`, claimed.Run.ID, mustJSON(output), inputTokens, outputTokens); err != nil {
					return fmt.Errorf("persist asset preparation output: %w", err)
				}
				return p.progress(ctx, claimed.Run.ID, 100)
			}
			return fmt.Errorf("unsupported workflow key %q", workflowKey)
		}
	}
	return fmt.Errorf("unsupported automation operation %q", claimed.Run.Operation)
}

func (p OperationProcessor) runPrincipal(ctx context.Context, runID string) (auth.Principal, error) {
	var organizationID, userID string
	err := p.Store.Pool.QueryRow(ctx, `
		SELECT r.organization_id::text, COALESCE(r.principal_id, j.created_by)::text
		FROM automation.runs r
		LEFT JOIN automation.jobs j ON j.id = r.automation_job_id
		WHERE r.id = $1::uuid
	`, runID).Scan(&organizationID, &userID)
	if err != nil {
		return auth.Principal{}, fmt.Errorf("load automation principal: %w", err)
	}
	return auth.Principal{OrganizationID: organizationID, UserID: userID, UserType: "member"}, nil
}

func (p OperationProcessor) assetPreparationVersions(ctx context.Context, runID string, payload map[string]any) ([]string, error) {
	if versionID, ok := payload["asset_version_id"].(string); ok && strings.TrimSpace(versionID) != "" {
		return []string{strings.TrimSpace(versionID)}, nil
	}
	assetIDs := scopedAssetIDs(payload)
	if len(assetIDs) == 0 {
		return nil, errors.New("asset_prepare requires asset_version_id or asset_ids")
	}
	versionIDs := make([]string, 0, len(assetIDs))
	for _, assetID := range assetIDs {
		var versionID string
		err := p.Store.Pool.QueryRow(ctx, `
			SELECT a.current_working_version_id::text
			FROM asset.assets a
			WHERE a.id = $1::uuid
			  AND a.organization_id = (SELECT organization_id FROM automation.runs WHERE id = $2::uuid)
			  AND a.current_working_version_id IS NOT NULL
			  AND a.deleted_at IS NULL
		`, assetID, runID).Scan(&versionID)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("asset %s is not editable", assetID)
		}
		if err != nil {
			return nil, err
		}
		versionIDs = append(versionIDs, versionID)
	}
	return versionIDs, nil
}

func (p OperationProcessor) progress(ctx context.Context, runID string, progress float64) error {
	_, err := p.Store.Pool.Exec(ctx, `
		WITH updated AS (
			UPDATE automation.runs SET progress = $2
			WHERE id = $1::uuid AND status = 'running'
			RETURNING organization_id, id
		)
		INSERT INTO automation.run_events (organization_id, run_id, event_type, payload)
		SELECT organization_id, id, 'run.progress', jsonb_build_object('progress', $2)
		FROM updated
	`, runID, progress)
	return err
}

func scopedAssetIDs(scope map[string]any) []string {
	return scopedIDs(scope, "asset_ids")
}

func scopedIDs(scope map[string]any, key string) []string {
	values, _ := scope[key].([]any)
	ids := make([]string, 0, len(values))
	for _, value := range values {
		if id, ok := value.(string); ok && strings.TrimSpace(id) != "" {
			ids = append(ids, strings.TrimSpace(id))
		}
	}
	if typed, ok := scope[key].([]string); ok {
		ids = append(ids, typed...)
	}
	return ids
}
