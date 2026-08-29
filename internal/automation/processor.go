package automation

import (
	"context"
	"errors"
	"fmt"
	"strings"

	assetservice "agentchunzhi/internal/asset"
	"agentchunzhi/internal/auth"
	contentservice "agentchunzhi/internal/content"
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
	Transfers   *assetservice.TransferProcessor
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
				candidateIDs := make([]string, 0, len(versionIDs))
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
					if result.CandidateVersionID != "" {
						candidateIDs = append(candidateIDs, result.CandidateVersionID)
					}
					inputTokens += result.InputTokens
					outputTokens += result.OutputTokens
				}
				output := map[string]any{"candidate_version_ids": candidateIDs}
				if len(candidateIDs) == 1 {
					output["candidate_version_id"] = candidateIDs[0]
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
			if _, err := p.Workflows.Execute(ctx, workflowKey, claimed.Run.ID, payload); err != nil {
				return err
			}
			claimed.Run.InputScope = payload
			switch workflowKey {
			case "asset_publish":
				return p.publishAssets(ctx, claimed, scopedAssetIDs(payload))
			case "asset_archive":
				return p.archiveAssets(ctx, claimed, scopedAssetIDs(payload))
			case "asset_reindex":
				return p.reindexAssets(ctx, claimed, scopedAssetIDs(payload))
			case "asset_import":
				return p.processTransfer(ctx, claimed, "import")
			case "asset_transcribe":
				return p.transcribeMedia(ctx, claimed, scopedIDs(payload, "media_ids"))
			case "note_sync":
				return p.syncNotes(ctx, claimed, scopedIDs(payload, "conversation_ids"))
			default:
				return fmt.Errorf("unsupported workflow key %q", workflowKey)
			}
		}
	}
	assetIDs := scopedAssetIDs(claimed.Run.InputScope)
	switch claimed.Run.Operation {
	case "publish":
		return p.publishAssets(ctx, claimed, assetIDs)
	case "archive":
		return p.archiveAssets(ctx, claimed, assetIDs)
	case "reindex":
		return p.reindexAssets(ctx, claimed, assetIDs)
	case "import", "export":
		return p.processTransfer(ctx, claimed, claimed.Run.Operation)
	case "transcribe":
		return p.transcribeMedia(ctx, claimed, scopedIDs(claimed.Run.InputScope, "media_ids"))
	case "sync_note":
		return p.syncNotes(ctx, claimed, scopedIDs(claimed.Run.InputScope, "conversation_ids"))
	default:
		return fmt.Errorf("unsupported automation operation %q", claimed.Run.Operation)
	}
}

func (p OperationProcessor) processTransfer(ctx context.Context, claimed ClaimedRun, operation string) error {
	if p.Transfers == nil {
		return errors.New("transfer processor is not configured")
	}
	if jobID, ok := claimed.Run.InputScope[operation+"_job_id"].(string); ok && strings.TrimSpace(jobID) != "" {
		if operation == "import" {
			return p.Transfers.ProcessImportJob(ctx, jobID)
		}
		return p.Transfers.ProcessExportJob(ctx, jobID)
	}
	return fmt.Errorf("automation transfer operation requires input_scope.%s_job_id", operation)
}

func (p OperationProcessor) transcribeMedia(ctx context.Context, claimed ClaimedRun, mediaIDs []string) error {
	if len(mediaIDs) == 0 {
		return errors.New("transcribe requires input_scope.media_ids")
	}
	principal, err := p.runPrincipal(ctx, claimed.Run.ID)
	if err != nil {
		return err
	}
	service := contentservice.Service{Store: p.Store, Events: p.Events}
	for _, mediaID := range mediaIDs {
		if _, err := service.RequestTranscription(ctx, principal, "automation:"+claimed.Run.ID+":transcribe:"+mediaID, mediaID); err != nil {
			return fmt.Errorf("transcribe media %s: %w", mediaID, err)
		}
	}
	return p.progress(ctx, claimed.Run.ID, 100)
}

func (p OperationProcessor) syncNotes(ctx context.Context, claimed ClaimedRun, conversationIDs []string) error {
	if len(conversationIDs) == 0 {
		return errors.New("sync_note requires input_scope.conversation_ids")
	}
	principal, err := p.runPrincipal(ctx, claimed.Run.ID)
	if err != nil {
		return err
	}
	service := contentservice.Service{Store: p.Store, Events: p.Events}
	for _, conversationID := range conversationIDs {
		if _, err := service.SyncNote(ctx, principal, "automation:"+claimed.Run.ID+":sync-note:"+conversationID, conversationID); err != nil {
			return fmt.Errorf("sync conversation note %s: %w", conversationID, err)
		}
	}
	return p.progress(ctx, claimed.Run.ID, 100)
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

func (p OperationProcessor) publishAssets(ctx context.Context, claimed ClaimedRun, assetIDs []string) error {
	if len(assetIDs) == 0 {
		return errors.New("publish requires input_scope.asset_ids")
	}
	for _, assetID := range assetIDs {
		tx, err := p.Store.Pool.Begin(ctx)
		if err != nil {
			return err
		}
		var organizationID, versionID, createdBy, workspaceID string
		var previousPublishedID *string
		var assetRevision int64
		err = tx.QueryRow(ctx, `
				SELECT a.organization_id::text, a.current_working_version_id::text, a.current_published_version_id::text,
				       a.workspace_id::text, a.revision,
				       COALESCE(run.principal_id, j.created_by)::text
				FROM asset.assets a JOIN automation.runs run ON run.id = $2::uuid
		LEFT JOIN automation.jobs j ON j.id = run.automation_job_id
		WHERE a.id = $1::uuid AND a.organization_id = run.organization_id
			FOR UPDATE OF a
				`, assetID, claimed.Run.ID).Scan(&organizationID, &versionID, &previousPublishedID, &workspaceID, &assetRevision, &createdBy)
		if err != nil {
			tx.Rollback(ctx)
			return err
		}
		if _, err := tx.Exec(ctx, `
			UPDATE asset.assets
			SET current_published_version_id = $2::uuid, publication_status = 'published',
			    published_at = now(), updated_at = now()
			WHERE organization_id = $1::uuid AND id = $3::uuid
		`, organizationID, versionID, assetID); err != nil {
			tx.Rollback(ctx)
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO audit.audit_log (organization_id, actor_user_id, initiator_user_id, action, resource_type, resource_id, result, metadata) VALUES ($1::uuid, $2::uuid, $2::uuid, 'automation.publish', 'asset', $3::uuid, 'allowed', jsonb_build_object('run_id', $4::text, 'review_required', false))`, organizationID, createdBy, assetID, claimed.Run.ID); err != nil {
			tx.Rollback(ctx)
			return err
		}
		// Phase 3: publishing emits the asset.published fact; the retrieval
		// coordinator derives projection runs from it asynchronously.
		previousPublished := ""
		if previousPublishedID != nil {
			previousPublished = *previousPublishedID
		}
		if _, err := p.Events.AppendTx(ctx, tx, eventing.Event{
			OrganizationID:   organizationID,
			WorkspaceID:      workspaceID,
			EventType:        eventing.EventAssetPublished,
			AggregateType:    "asset",
			AggregateID:      assetID,
			AggregateVersion: assetRevision,
			PayloadVersion:   eventing.PayloadVersionV1,
			Actor:            map[string]any{"type": "system"},
			Payload: eventing.AssetPublishedPayload{
				AssetID:           assetID,
				VersionID:         versionID,
				PreviousVersionID: previousPublished,
				WorkspaceID:       workspaceID,
			},
		}); err != nil {
			tx.Rollback(ctx)
			return err
		}
		if err := tx.Commit(ctx); err != nil {
			return err
		}
	}
	return p.progress(ctx, claimed.Run.ID, 100)
}

func (p OperationProcessor) archiveAssets(ctx context.Context, claimed ClaimedRun, assetIDs []string) error {
	if len(assetIDs) == 0 {
		return errors.New("archive requires input_scope.asset_ids")
	}
	tx, err := p.Store.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	orgID := ""
	if err := tx.QueryRow(ctx, `SELECT organization_id::text FROM automation.runs WHERE id = $1::uuid`, claimed.Run.ID).Scan(&orgID); err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `SELECT id::text, current_published_version_id::text, revision FROM asset.assets WHERE id = ANY($1::uuid[]) AND organization_id = $2::uuid AND current_published_version_id IS NOT NULL FOR UPDATE`, assetIDs, orgID)
	if err != nil {
		return err
	}
	defer rows.Close()
	type archivedAsset struct {
		AssetID   string
		VersionID string
		Revision  int64
	}
	var archived []archivedAsset
	for rows.Next() {
		var item archivedAsset
		if err := rows.Scan(&item.AssetID, &item.VersionID, &item.Revision); err != nil {
			return err
		}
		archived = append(archived, item)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE asset.assets SET publication_status = 'archived', current_published_version_id = NULL, updated_at = now() WHERE id = ANY($1::uuid[]) AND organization_id = $2::uuid`, assetIDs, orgID); err != nil {
		return err
	}
	// Phase 3: archiving emits the asset.archived fact; the retrieval
	// coordinator stales the previous published runs and drops the heads.
	for _, item := range archived {
		if _, err := p.Events.AppendTx(ctx, tx, eventing.Event{
			OrganizationID:   orgID,
			EventType:        eventing.EventAssetArchived,
			AggregateType:    "asset",
			AggregateID:      item.AssetID,
			AggregateVersion: item.Revision,
			PayloadVersion:   eventing.PayloadVersionV1,
			Actor:            map[string]any{"type": "system"},
			Payload: eventing.AssetArchivedPayload{
				AssetID:           item.AssetID,
				PreviousVersionID: item.VersionID,
			},
		}); err != nil {
			return err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	return p.progress(ctx, claimed.Run.ID, 100)
}

func (p OperationProcessor) reindexAssets(ctx context.Context, claimed ClaimedRun, assetIDs []string) error {
	if len(assetIDs) == 0 {
		return errors.New("reindex requires input_scope.asset_ids")
	}
	tx, err := p.Store.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	// Phase 3: only the current published pointer may enter the retrieval
	// index, so reindex re-emits the asset.published fact for published
	// assets; the coordinator's run ensure is idempotent. Working (draft)
	// versions are never projected.
	rows, err := tx.Query(ctx, `
		SELECT a.organization_id::text, a.workspace_id::text, a.id::text,
		       a.current_published_version_id::text, a.revision
		FROM asset.assets a
		WHERE a.id = ANY($1::uuid[])
		  AND a.organization_id = (SELECT organization_id FROM automation.runs WHERE id = $2::uuid)
		  AND a.publication_status = 'published'
		  AND a.current_published_version_id IS NOT NULL
	`, assetIDs, claimed.Run.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	type reindexAsset struct {
		OrganizationID string
		WorkspaceID    string
		AssetID        string
		VersionID      string
		Revision       int64
	}
	var targets []reindexAsset
	for rows.Next() {
		var item reindexAsset
		if err := rows.Scan(&item.OrganizationID, &item.WorkspaceID, &item.AssetID,
			&item.VersionID, &item.Revision); err != nil {
			return err
		}
		targets = append(targets, item)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, item := range targets {
		if _, err := p.Events.AppendTx(ctx, tx, eventing.Event{
			OrganizationID:   item.OrganizationID,
			WorkspaceID:      item.WorkspaceID,
			EventType:        eventing.EventAssetPublished,
			AggregateType:    "asset",
			AggregateID:      item.AssetID,
			AggregateVersion: item.Revision,
			PayloadVersion:   eventing.PayloadVersionV1,
			Actor:            map[string]any{"type": "system"},
			Payload: eventing.AssetPublishedPayload{
				AssetID:     item.AssetID,
				VersionID:   item.VersionID,
				WorkspaceID: item.WorkspaceID,
			},
		}); err != nil {
			return err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	return p.progress(ctx, claimed.Run.ID, 100)
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
