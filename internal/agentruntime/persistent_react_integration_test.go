package agentruntime

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	runtimetools "agentchunzhi/internal/agentruntime/tools"
	"agentchunzhi/internal/modelendpoint"
	"agentchunzhi/internal/store"

	"github.com/cloudwego/eino/components/model"
	"github.com/google/uuid"
)

type persistentTestResolver struct {
	organizationID string
	endpointID     string
	model          model.ToolCallingChatModel
}

func (r persistentTestResolver) Resolve(context.Context, string) (ResolvedModel, error) {
	return r.resolved(), nil
}

func (r persistentTestResolver) ResolveEndpoint(context.Context, string, int64) (ResolvedModel, error) {
	return r.resolved(), nil
}

func (r persistentTestResolver) resolved() ResolvedModel {
	return ResolvedModel{
		EndpointID: r.endpointID,
		Revision:   1,
		Model:      r.model,
		Config: modelendpoint.RuntimeConfig{
			OrganizationID: r.organizationID,
			ProviderType:   modelendpoint.ProviderOpenAICompatible,
			ModelName:      "persistent-react-test",
			Options:        modelendpoint.Options{EnableToolCalling: true},
			Capabilities:   modelendpoint.Capabilities{ToolCalling: true},
		},
	}
}

type persistentTestToolFactory struct {
	backend        *publishTestTool
	mu             *sync.Mutex
	authorizations *int
}

func (f persistentTestToolFactory) Build(context.Context, ReActToolScope, map[string]any) (*runtimetools.Registry, runtimetools.Policy, error) {
	registry := runtimetools.NewRegistry()
	if err := registry.Register(runtimetools.Definition{
		Name: "publish_asset", Risk: runtimetools.HighWrite,
		Capabilities: []string{"asset.publish"}, Tool: f.backend,
	}); err != nil {
		return nil, runtimetools.Policy{}, err
	}
	policy := runtimetools.Policy{
		AllowedCapabilities: map[string]bool{"asset.publish": true}, AllowHighWrite: true,
		MaxCalls: 12, Authorize: func(context.Context, string, runtimetools.Risk, map[string]any) error {
			f.mu.Lock()
			*f.authorizations++
			f.mu.Unlock()
			return nil
		},
	}
	return registry, policy, nil
}

func TestPersistentReActCrossProcessApprovalIntegration(t *testing.T) {
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
		ORDER BY o.created_at, u.created_at, w.created_at LIMIT 1
	`).Scan(&organizationID, &principalID, &workspaceID); err != nil {
		t.Fatalf("load integration scope: %v", err)
	}

	marker := "eino-react-" + uuid.NewString()
	endpointID, applicationID := uuid.NewString(), uuid.NewString()
	seedTx, err := db.Pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin ReAct seed: %v", err)
	}
	if _, err := seedTx.Exec(ctx, `
		INSERT INTO integration.model_endpoints
			(id, organization_id, name, current_revision, status, last_verified_at, created_by)
		VALUES ($1::uuid, $2::uuid, $3, 1, 'active', now(), $4::uuid)
	`, endpointID, organizationID, marker, principalID); err != nil {
		seedTx.Rollback(ctx)
		t.Fatalf("seed ReAct endpoint: %v", err)
	}
	if _, err := seedTx.Exec(ctx, `
		INSERT INTO integration.model_endpoint_revisions
			(model_endpoint_id, revision, provider_type, base_url, model_name, credential_mode,
			 credential_ciphertext, credential_key_id, options, capabilities, config_checksum, created_by)
		VALUES ($1::uuid, 1, 'openai_compatible', 'https://models.example.com/v1', 'persistent-react-test',
			'encrypted', $2, 'integration-test', $3::jsonb, $4::jsonb, $5, $6::uuid)
	`, endpointID, []byte("not-used-by-test-resolver"),
		mustJSON(map[string]any{"enable_tool_calling": true}),
		mustJSON(map[string]any{"generate": true, "streaming": true, "tool_calling": true}),
		uuid.NewString(), principalID); err != nil {
		seedTx.Rollback(ctx)
		t.Fatalf("seed ReAct endpoint revision: %v", err)
	}
	if _, err := seedTx.Exec(ctx, `
		INSERT INTO integration.agent_applications
			(id, organization_id, bound_agent_user_id, name, status, capabilities, model_endpoint_id,
			 runtime_mode, instruction, tool_policy)
		VALUES ($1::uuid, $2::uuid, $3::uuid, $4, 'active', $5::jsonb, $6::uuid,
			'react', 'Use the publish tool when requested.', $7::jsonb)
	`, applicationID, organizationID, principalID, marker,
		mustJSON([]string{"asset.publish"}), endpointID,
		mustJSON(map[string]any{"allowed_tools": []string{"publish_asset"}})); err != nil {
		seedTx.Rollback(ctx)
		t.Fatalf("seed ReAct application: %v", err)
	}
	if err := seedTx.Commit(ctx); err != nil {
		t.Fatalf("commit ReAct seed: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(context.Background(), `DELETE FROM automation.runs WHERE organization_id = $1::uuid AND idempotency_key LIKE $2`, organizationID, marker+"%")
		_, _ = db.Pool.Exec(context.Background(), `DELETE FROM integration.agent_applications WHERE id = $1::uuid`, applicationID)
		_, _ = db.Pool.Exec(context.Background(), `DELETE FROM integration.model_endpoints WHERE id = $1::uuid`, endpointID)
	})

	coordinator := Coordinator{Store: db}
	run, err := coordinator.Create(ctx, RunRequest{
		OrganizationID: organizationID, WorkspaceID: workspaceID, PrincipalID: principalID,
		AgentUserID: principalID, AgentApplicationID: applicationID, ModelEndpointID: endpointID,
		ModelRevision: 1, RuntimeMode: "react", Source: "chat",
		Input: map[string]any{"query": "publish asset-1"}, ExecutionOptions: map[string]any{"streaming": true},
		IdempotencyKey: marker + "-create-0001",
	})
	if err != nil {
		t.Fatalf("create persistent ReAct run: %v", err)
	}
	claimed, err := coordinator.Claim(ctx, "react-worker-first", time.Minute)
	if err != nil || claimed.Run.ID != run.ID {
		t.Fatalf("claim initial ReAct run: claimed=%+v err=%v", claimed, err)
	}

	cipher, err := modelendpoint.NewCredentialCipher(base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef")))
	if err != nil {
		t.Fatal(err)
	}
	backend := &publishTestTool{}
	authorizations := 0
	authorizationMu := &sync.Mutex{}
	newService := func(chatModel model.ToolCallingChatModel) PersistentReActService {
		return PersistentReActService{
			Store: db, Cipher: cipher,
			Models:      persistentTestResolver{organizationID: organizationID, endpointID: endpointID, model: chatModel},
			ToolFactory: persistentTestToolFactory{backend: backend, mu: authorizationMu, authorizations: &authorizations},
			Coordinator: Coordinator{Store: db},
		}
	}
	waiting, err := newService(&deterministicReActModel{}).Process(ctx, claimed)
	if err != nil || !waiting || backend.calls != 0 {
		t.Fatalf("initial ReAct process must wait before side effect: waiting=%v calls=%d err=%v", waiting, backend.calls, err)
	}
	var interactionID, interruptID, interactionType string
	if err := db.Pool.QueryRow(ctx, `
		SELECT id::text, interrupt_id, interaction_type FROM automation.interactions
		WHERE run_id = $1::uuid AND status = 'pending'
	`, run.ID).Scan(&interactionID, &interruptID, &interactionType); err != nil {
		t.Fatalf("load ReAct interaction: %v", err)
	}
	if interactionType != "approval" || interruptID == "" {
		t.Fatalf("unexpected ReAct interaction type=%s interrupt=%q", interactionType, interruptID)
	}
	if _, err := coordinator.ResolveInteraction(ctx, organizationID, uuid.NewString(), interactionID, principalID, "approved", map[string]any{"reason": "wrong run"}); !errors.Is(err, ErrInteractionState) {
		t.Fatalf("cross-run interaction resume must fail, got %v", err)
	}
	if _, err := coordinator.ResolveInteraction(ctx, organizationID, run.ID, interactionID, principalID, "approved", map[string]any{"reason": "reviewed"}); err != nil {
		t.Fatalf("approve ReAct interaction: %v", err)
	}
	resumedClaim, err := coordinator.Claim(ctx, "react-worker-second", time.Minute)
	if err != nil || resumedClaim.Run.ID != run.ID {
		t.Fatalf("claim resumed ReAct run: claimed=%+v err=%v", resumedClaim, err)
	}
	resumedWaiting, err := newService(&deterministicReActModel{}).Process(ctx, resumedClaim)
	if err != nil || resumedWaiting {
		t.Fatalf("resume persistent ReAct run: waiting=%v err=%v", resumedWaiting, err)
	}
	if backend.calls != 1 || authorizations != 2 {
		t.Fatalf("resume must execute once and reauthorize: calls=%d authorizations=%d", backend.calls, authorizations)
	}
	finished, err := coordinator.Finish(ctx, resumedClaim.Attempt.ID, "react-worker-second", true, "", "")
	if err != nil || finished.Status != "succeeded" {
		t.Fatalf("finish persistent ReAct run: run=%+v err=%v", finished, err)
	}
	var output []byte
	var toolStatus string
	var checkpointCount int
	if err := db.Pool.QueryRow(ctx, `SELECT output_snapshot FROM automation.runs WHERE id = $1::uuid`, run.ID).Scan(&output); err != nil {
		t.Fatal(err)
	}
	if err := db.Pool.QueryRow(ctx, `SELECT status FROM integration.agent_tool_calls WHERE run_id = $1::uuid AND tool_call_id = 'call-publish-1'`, run.ID).Scan(&toolStatus); err != nil {
		t.Fatal(err)
	}
	if err := db.Pool.QueryRow(ctx, `SELECT count(*) FROM automation.checkpoints WHERE run_id = $1::uuid`, run.ID).Scan(&checkpointCount); err != nil {
		t.Fatal(err)
	}
	var snapshot map[string]any
	if json.Unmarshal(output, &snapshot) != nil || snapshot["answer"] != "published after approval" || toolStatus != "succeeded" || checkpointCount < 1 {
		t.Fatalf("invalid persisted ReAct outcome: output=%s tool=%s checkpoints=%d", output, toolStatus, checkpointCount)
	}
}
