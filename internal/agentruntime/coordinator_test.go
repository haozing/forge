package agentruntime

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"agentchunzhi/internal/automation"
	"agentchunzhi/internal/store"
	"agentchunzhi/internal/workflows"

	"github.com/google/uuid"
)

func TestCoordinatorPersistenceIntegration(t *testing.T) {
	databaseURL := os.Getenv("AGENTCHUNZHI_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("AGENTCHUNZHI_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	db, err := store.Open(ctx, databaseURL)
	if err != nil {
		t.Fatalf("open integration database: %v", err)
	}
	t.Cleanup(db.Close)

	var organizationID, principalID, workspaceID string
	if err := db.Pool.QueryRow(ctx, `
		SELECT o.id::text, u.id::text, w.id::text
		FROM organization.organizations o
		JOIN identity.users u ON u.organization_id = o.id
		JOIN content.workspaces w ON w.organization_id = o.id
		WHERE o.status = 'active' AND u.user_type = 'member' AND u.status = 'active'
		ORDER BY o.created_at, u.created_at, w.created_at
		LIMIT 1
	`).Scan(&organizationID, &principalID, &workspaceID); err != nil {
		t.Fatalf("load integration scope: %v", err)
	}

	marker := "eino-coordinator-" + uuid.NewString()
	endpointID := uuid.NewString()
	applicationID := uuid.NewString()
	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin coordinator seed: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO integration.model_endpoints
			(id, organization_id, name, current_revision, status, last_verified_at, created_by)
		VALUES ($1::uuid, $2::uuid, $3, 1, 'active', now(), $4::uuid)
	`, endpointID, organizationID, marker, principalID); err != nil {
		tx.Rollback(ctx)
		t.Fatalf("seed model endpoint: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO integration.model_endpoint_revisions
			(model_endpoint_id, revision, provider_type, base_url, model_name, credential_mode,
			 credential_ciphertext, credential_key_id, options, capabilities, config_checksum, created_by)
		VALUES ($1::uuid, 1, 'openai_compatible', 'https://models.example.com/v1', 'test-model', 'encrypted',
			 $2, 'integration-test', '{}'::jsonb, $3::jsonb, $4, $5::uuid)
	`, endpointID, []byte("encrypted-test-credential"),
		mustJSON(map[string]bool{"generate": true, "streaming": true, "tool_calling": true}), uuid.NewString(), principalID); err != nil {
		tx.Rollback(ctx)
		t.Fatalf("seed model endpoint revision: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO integration.agent_applications
			(id, organization_id, bound_agent_user_id, name, status, capabilities, model_endpoint_id, runtime_mode)
		VALUES ($1::uuid, $2::uuid, $3::uuid, $4, 'active', $5::jsonb, $6::uuid, 'workflow')
	`, applicationID, organizationID, principalID, marker, mustJSON([]string{"query.read"}), endpointID); err != nil {
		tx.Rollback(ctx)
		t.Fatalf("seed agent application: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit coordinator seed: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(context.Background(), `DELETE FROM automation.runs WHERE organization_id = $1::uuid AND idempotency_key LIKE $2`, organizationID, marker+"%")
		_, _ = db.Pool.Exec(context.Background(), `DELETE FROM integration.agent_applications WHERE id = $1::uuid`, applicationID)
		_, _ = db.Pool.Exec(context.Background(), `DELETE FROM integration.model_endpoints WHERE id = $1::uuid`, endpointID)
	})

	coordinator := Coordinator{Store: db}
	request := RunRequest{
		OrganizationID: organizationID, WorkspaceID: workspaceID, PrincipalID: principalID,
		AgentUserID: principalID, AgentApplicationID: applicationID, ModelEndpointID: endpointID,
		ModelRevision: 1, RuntimeMode: "workflow", WorkflowKey: "asset_prepare", WorkflowCodeVer: 1,
		Source: "manual", Input: map[string]any{"asset_id": marker}, ExecutionOptions: map[string]any{"max_seconds": 30},
		PolicyRevision: 1, IdempotencyKey: marker + "-create-0001",
	}
	run, err := coordinator.Create(ctx, request)
	if err != nil {
		t.Fatalf("create persistent run: %v", err)
	}
	replayed, err := coordinator.Create(ctx, request)
	if err != nil || replayed.ID != run.ID {
		t.Fatalf("idempotent create mismatch: replay=%+v err=%v original=%+v", replayed, err, run)
	}

	interaction, err := coordinator.WaitForInteraction(ctx, organizationID, run.ID, "approval", "Approve candidate", map[string]any{"asset_id": marker}, marker+"-approval-0001")
	if err != nil {
		t.Fatalf("wait for interaction: %v", err)
	}
	waiting, err := coordinator.Get(ctx, organizationID, run.ID)
	if err != nil || waiting.Status != "waiting_approval" || waiting.WaitingInteractionID != interaction.ID {
		t.Fatalf("run did not enter approval wait: run=%+v err=%v", waiting, err)
	}
	if _, err := coordinator.ResolveInteraction(ctx, organizationID, uuid.NewString(), interaction.ID, principalID, "approved", map[string]any{"approved": true}); !errors.Is(err, ErrInteractionState) {
		t.Fatalf("interaction must not be consumed through another run ID, got %v", err)
	}
	if _, err := coordinator.ResolveInteraction(ctx, organizationID, run.ID, interaction.ID, principalID, "approved", map[string]any{"approved": true}); err != nil {
		t.Fatalf("resolve interaction: %v", err)
	}

	claimed, err := coordinator.Claim(ctx, "integration-coordinator", time.Minute)
	if err != nil {
		t.Fatalf("claim resumed run: %v", err)
	}
	if claimed.Run.ID != run.ID || claimed.Attempt.ID == "" || claimed.Run.Status != "running" {
		t.Fatalf("unexpected claimed run: %+v", claimed)
	}
	if _, err := coordinator.Renew(ctx, claimed.Attempt.ID, "integration-coordinator", time.Minute); err != nil {
		t.Fatalf("renew attempt: %v", err)
	}
	finished, err := coordinator.Finish(ctx, claimed.Attempt.ID, "integration-coordinator", true, "", "")
	if err != nil || finished.Status != "succeeded" {
		t.Fatalf("finish run: run=%+v err=%v", finished, err)
	}

	cancelRequest := request
	cancelRequest.IdempotencyKey = marker + "-cancel-0001"
	cancelledRun, err := coordinator.Create(ctx, cancelRequest)
	if err != nil {
		t.Fatalf("create cancellation run: %v", err)
	}
	if err := coordinator.RequestCancel(ctx, organizationID, cancelledRun.ID, "user requested"); err != nil {
		t.Fatalf("request cancellation: %v", err)
	}
	if _, err := coordinator.Claim(ctx, "integration-coordinator", time.Minute); !errors.Is(err, automation.ErrNoPendingRun) {
		t.Fatalf("expected canceled run to be skipped by claim, got %v", err)
	}
	finalCancelled, err := coordinator.Get(ctx, organizationID, cancelledRun.ID)
	if err != nil || finalCancelled.Status != "canceled" {
		t.Fatalf("canceled run did not reach terminal state: run=%+v err=%v", finalCancelled, err)
	}

	workflowRequest := request
	workflowRequest.IdempotencyKey = marker + "-workflow-0001"
	workflowRequest.Input = map[string]any{"asset_ids": []string{"asset-1"}, "title": "candidate"}
	workflowRun, err := coordinator.Create(ctx, workflowRequest)
	if err != nil {
		t.Fatalf("create workflow execution run: %v", err)
	}
	workflowClaim, err := coordinator.Claim(ctx, "integration-workflow", time.Minute)
	if err != nil || workflowClaim.Run.ID != workflowRun.ID {
		t.Fatalf("claim workflow run: claimed=%+v err=%v", workflowClaim, err)
	}
	workflowRegistry, err := workflows.DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	executor := workflows.Executor{Registry: workflowRegistry}
	if _, err := executor.Execute(ctx, workflowRequest.WorkflowKey, workflowClaim.Run.ID, workflowRequest.Input); err != nil {
		t.Fatalf("execute persisted fixed workflow: %v", err)
	}
	if _, err := coordinator.Finish(ctx, workflowClaim.Attempt.ID, "integration-workflow", true, "", ""); err != nil {
		t.Fatalf("finish fixed workflow run: %v", err)
	}
}
