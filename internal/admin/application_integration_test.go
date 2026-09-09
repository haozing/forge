package admin

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"

	"agentchunzhi/internal/auth"
	"agentchunzhi/internal/modelendpoint"
	"agentchunzhi/internal/store"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestAgentApplicationModelEndpointBindingIntegration(t *testing.T) {
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

	var organizationID, memberID string
	if err := db.Pool.QueryRow(ctx, `
		SELECT o.id::text, u.id::text
		FROM organization.organizations o
		JOIN identity.users u ON u.organization_id = o.id
		WHERE o.status = 'active' AND u.user_type = 'member' AND u.status = 'active'
		ORDER BY o.created_at, u.created_at
		LIMIT 1
	`).Scan(&organizationID, &memberID); err != nil {
		t.Fatalf("load integration principal: %v", err)
	}
	principal := auth.Principal{OrganizationID: organizationID, UserID: memberID, UserType: "member"}
	service := Service{Store: db}
	marker := "eino-binding-" + uuid.NewString()
	var endpointRAG, endpointReAct string
	var createdApplications, createdUsers []string
	t.Cleanup(func() {
		for _, applicationID := range createdApplications {
			cleanupApplicationIntegrationRow(t, db, `DELETE FROM audit.audit_log WHERE agent_application_id = $1::uuid`, applicationID)
		}
		cleanupApplicationIntegrationRow(t, db, `DELETE FROM system.idempotency_keys WHERE organization_id = $1::uuid AND idempotency_key LIKE $2`, organizationID, marker+"%")
		for _, applicationID := range createdApplications {
			cleanupApplicationIntegrationRow(t, db, `DELETE FROM integration.agent_applications WHERE id = $1::uuid`, applicationID)
		}
		for _, userID := range createdUsers {
			cleanupApplicationIntegrationRow(t, db, `DELETE FROM identity.api_keys WHERE user_id = $1::uuid`, userID)
			cleanupApplicationIntegrationRow(t, db, `DELETE FROM identity.users WHERE id = $1::uuid`, userID)
		}
		for _, endpointID := range []string{endpointRAG, endpointReAct} {
			if endpointID != "" {
				cleanupApplicationIntegrationRow(t, db, `DELETE FROM integration.model_endpoints WHERE id = $1::uuid`, endpointID)
			}
		}
	})
	endpointRAG = seedApplicationEndpoint(t, ctx, db, organizationID, memberID, marker+"-rag", modelendpoint.Capabilities{Generate: true, Streaming: true})
	endpointReAct = seedApplicationEndpoint(t, ctx, db, organizationID, memberID, marker+"-react", modelendpoint.Capabilities{Generate: true, Streaming: true, ToolCalling: true})

	register := func(name, endpointID, runtimeMode string) RegisterAgentResult {
		result, err := service.RegisterAgent(ctx, principal, RegisterAgentInput{
			DisplayName:     name + " user",
			ApiKeyName:      name + " key",
			ApplicationName: name,
			ModelEndpointID: endpointID,
			RuntimeMode:     runtimeMode,
			Capabilities:    []string{"query.read", "reference.read"},
			IdempotencyKey:  marker + "-register-" + name,
		})
		if err != nil {
			t.Fatalf("register %s: %v", name, err)
		}
		createdApplications = append(createdApplications, result.AgentApplicationID)
		createdUsers = append(createdUsers, result.AgentUserID)
		return result
	}

	ragApplication := register("rag", endpointRAG, "rag")
	reactApplication := register("react", endpointReAct, "react")
	ragSummary, err := service.GetAgentApplication(ctx, principal, ragApplication.AgentApplicationID)
	if err != nil {
		t.Fatalf("load RAG application: %v", err)
	}
	reactSummary, err := service.GetAgentApplication(ctx, principal, reactApplication.AgentApplicationID)
	if err != nil {
		t.Fatalf("load ReAct application: %v", err)
	}
	if ragSummary.ModelEndpointID != endpointRAG || reactSummary.ModelEndpointID != endpointReAct {
		t.Fatalf("applications did not retain isolated endpoint bindings: rag=%s react=%s", ragSummary.ModelEndpointID, reactSummary.ModelEndpointID)
	}

	_, err = service.RegisterAgent(ctx, principal, RegisterAgentInput{
		DisplayName: "invalid react user", ApiKeyName: "invalid react key", ApplicationName: "invalid react",
		ModelEndpointID: endpointRAG, RuntimeMode: "react", Capabilities: []string{"query.read"},
		IdempotencyKey: marker + "-invalid-react",
	})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("expected ReAct binding without tool calling to fail, got %v", err)
	}

	newEndpointID, newRuntimeMode := endpointReAct, "react"
	if _, err := service.UpdateAgentApplication(ctx, principal, UpdateAgentApplicationInput{
		ApplicationID: ragApplication.AgentApplicationID, ModelEndpointID: &newEndpointID,
		RuntimeMode: &newRuntimeMode, IdempotencyKey: marker + "-update-rag",
	}); err != nil {
		t.Fatalf("update application binding: %v", err)
	}
	ragSummary, err = service.GetAgentApplication(ctx, principal, ragApplication.AgentApplicationID)
	if err != nil || ragSummary.ModelEndpointID != endpointReAct || ragSummary.RuntimeMode != "react" {
		t.Fatalf("updated binding was not persisted: summary=%+v err=%v", ragSummary, err)
	}

	if _, err := service.SetAgentApplicationStatus(ctx, principal, SetApplicationStatusInput{
		ApplicationID: ragApplication.AgentApplicationID, Status: "disabled", IdempotencyKey: marker + "-disable-rag",
	}); err != nil {
		t.Fatalf("disable application: %v", err)
	}
	if _, err := db.Pool.Exec(ctx, `UPDATE integration.model_endpoints SET status = 'disabled' WHERE id = $1::uuid`, endpointReAct); err != nil {
		t.Fatalf("disable model endpoint: %v", err)
	}
	_, err = service.SetAgentApplicationStatus(ctx, principal, SetApplicationStatusInput{
		ApplicationID: ragApplication.AgentApplicationID, Status: "active", IdempotencyKey: marker + "-enable-rag",
	})
	if !errors.Is(err, ErrApplicationStatusInvalidInput) {
		t.Fatalf("expected application enable to reject unavailable endpoint, got %v", err)
	}

	foreignOrganizationID := uuid.NewString()
	if _, err := db.Pool.Exec(ctx, `INSERT INTO organization.organizations (id, name, slug) VALUES ($1::uuid, $2, $3)`, foreignOrganizationID, marker+"-foreign", marker+"-foreign"); err != nil {
		t.Fatalf("create foreign organization: %v", err)
	}
	var foreignEndpointID string
	t.Cleanup(func() {
		if foreignEndpointID != "" {
			cleanupApplicationIntegrationRow(t, db, `DELETE FROM integration.model_endpoints WHERE id = $1::uuid`, foreignEndpointID)
		}
		cleanupApplicationIntegrationRow(t, db, `DELETE FROM organization.organizations WHERE id = $1::uuid`, foreignOrganizationID)
	})
	foreignEndpointID = seedApplicationEndpoint(t, ctx, db, foreignOrganizationID, memberID, marker+"-foreign-endpoint", modelendpoint.Capabilities{Generate: true})
	_, err = db.Pool.Exec(ctx, `UPDATE integration.agent_applications SET model_endpoint_id = $2::uuid WHERE id = $1::uuid`, ragApplication.AgentApplicationID, foreignEndpointID)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23503" {
		t.Fatalf("expected cross-organization endpoint foreign key violation, got %v", err)
	}
}

func TestAgentApplicationKnowledgeScopeIntegration(t *testing.T) {
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

	var organizationID, memberID string
	if err := db.Pool.QueryRow(ctx, `
		SELECT o.id::text, u.id::text
		FROM organization.organizations o
		JOIN identity.users u ON u.organization_id = o.id
		WHERE o.status = 'active' AND u.user_type = 'member' AND u.status = 'active'
		ORDER BY o.created_at, u.created_at
		LIMIT 1
	`).Scan(&organizationID, &memberID); err != nil {
		t.Fatalf("load integration principal: %v", err)
	}
	type knowledgeBase struct{ id, defaultModel string }
	var knowledgeBases []knowledgeBase
	rows, err := db.Pool.Query(ctx, `
		SELECT w.id::text, w.default_resource_model_id::text
		FROM content.workspaces w
		WHERE w.organization_id = $1::uuid AND w.default_resource_model_id IS NOT NULL
		ORDER BY w.created_at
		LIMIT 2
	`, organizationID)
	if err != nil {
		t.Fatalf("list knowledge bases: %v", err)
	}
	for rows.Next() {
		var kb knowledgeBase
		if err := rows.Scan(&kb.id, &kb.defaultModel); err != nil {
			t.Fatalf("scan knowledge base: %v", err)
		}
		knowledgeBases = append(knowledgeBases, kb)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate knowledge bases: %v", err)
	}
	if len(knowledgeBases) == 0 {
		t.Skip("integration organization has no workspace with a default resource model")
	}

	principal := auth.Principal{OrganizationID: organizationID, UserID: memberID, UserType: "member"}
	service := Service{Store: db}
	marker := "kb-scope-" + uuid.NewString()
	var createdApplication, createdUser string
	t.Cleanup(func() {
		cleanupApplicationIntegrationRow(t, db, `DELETE FROM content.workspace_agent_applications WHERE organization_id = $1::uuid AND agent_application_id = $2::uuid`, organizationID, createdApplication)
		cleanupApplicationIntegrationRow(t, db, `DELETE FROM content.agent_access_policies WHERE organization_id = $1::uuid AND agent_user_id = $2::uuid`, organizationID, createdUser)
		cleanupApplicationIntegrationRow(t, db, `DELETE FROM system.idempotency_keys WHERE organization_id = $1::uuid AND idempotency_key LIKE $2`, organizationID, marker+"%")
		if createdApplication != "" {
			cleanupApplicationIntegrationRow(t, db, `DELETE FROM integration.agent_applications WHERE id = $1::uuid`, createdApplication)
		}
		if createdUser != "" {
			cleanupApplicationIntegrationRow(t, db, `DELETE FROM identity.api_keys WHERE user_id = $1::uuid`, createdUser)
			cleanupApplicationIntegrationRow(t, db, `DELETE FROM identity.users WHERE id = $1::uuid`, createdUser)
		}
	})

	endpointID := seedApplicationEndpoint(t, ctx, db, organizationID, memberID, marker+"-endpoint", modelendpoint.Capabilities{Generate: true, Streaming: true})
	registered, err := service.RegisterAgent(ctx, principal, RegisterAgentInput{
		DisplayName: "kb scope user", ApiKeyName: "kb scope key", ApplicationName: "kb scope app",
		ModelEndpointID: endpointID, RuntimeMode: "rag", Capabilities: []string{"query.read"},
		IdempotencyKey: marker + "-register",
	})
	if err != nil {
		t.Fatalf("register application: %v", err)
	}
	createdApplication, createdUser = registered.AgentApplicationID, registered.AgentUserID

	// Admin-service registration carries no knowledge grant: scopes start empty.
	summary, err := service.GetAgentApplication(ctx, principal, createdApplication)
	if err != nil {
		t.Fatalf("load application: %v", err)
	}
	if len(summary.KnowledgeScopes) != 0 {
		t.Fatalf("expected empty knowledge scopes before first move, got %+v", summary.KnowledgeScopes)
	}

	moveTo := knowledgeBases[0].id
	if _, err := service.UpdateAgentApplication(ctx, principal, UpdateAgentApplicationInput{
		ApplicationID: createdApplication, KnowledgeBaseWorkspaceID: &moveTo, IdempotencyKey: marker + "-move-1",
	}); err != nil {
		t.Fatalf("move application to first knowledge base: %v", err)
	}
	summary, err = service.GetAgentApplication(ctx, principal, createdApplication)
	if err != nil {
		t.Fatalf("reload application: %v", err)
	}
	if len(summary.KnowledgeScopes) != 1 {
		t.Fatalf("expected exactly one knowledge scope, got %+v", summary.KnowledgeScopes)
	}
	scope := summary.KnowledgeScopes[0]
	if scope.WorkspaceID != moveTo || scope.ResourceModelID != knowledgeBases[0].defaultModel || scope.WorkspaceName == "" || scope.DataScope == "" {
		t.Fatalf("knowledge scope does not match target workspace: %+v (want ws=%s model=%s)", scope, moveTo, knowledgeBases[0].defaultModel)
	}
	var enabledCount int
	if err := db.Pool.QueryRow(ctx, `
		SELECT count(*) FROM content.workspace_agent_applications
		WHERE organization_id = $1::uuid AND agent_application_id = $2::uuid AND enabled = true AND workspace_id = $3::uuid
	`, organizationID, createdApplication, moveTo).Scan(&enabledCount); err != nil || enabledCount != 1 {
		t.Fatalf("target knowledge base enablement missing: count=%d err=%v", enabledCount, err)
	}

	if len(knowledgeBases) > 1 {
		second := knowledgeBases[1].id
		if _, err := service.UpdateAgentApplication(ctx, principal, UpdateAgentApplicationInput{
			ApplicationID: createdApplication, KnowledgeBaseWorkspaceID: &second, IdempotencyKey: marker + "-move-2",
		}); err != nil {
			t.Fatalf("move application to second knowledge base: %v", err)
		}
		summary, err = service.GetAgentApplication(ctx, principal, createdApplication)
		if err != nil {
			t.Fatalf("reload application after second move: %v", err)
		}
		if len(summary.KnowledgeScopes) != 1 || summary.KnowledgeScopes[0].WorkspaceID != second {
			t.Fatalf("second move did not replace the scope: %+v", summary.KnowledgeScopes)
		}
		if err := db.Pool.QueryRow(ctx, `
			SELECT count(*) FROM content.workspace_agent_applications
			WHERE organization_id = $1::uuid AND agent_application_id = $2::uuid AND enabled = true
		`, organizationID, createdApplication).Scan(&enabledCount); err != nil || enabledCount != 1 {
			t.Fatalf("expected exactly one enabled knowledge base after move: count=%d err=%v", enabledCount, err)
		}
	}

	bogus := uuid.NewString()
	if _, err := service.UpdateAgentApplication(ctx, principal, UpdateAgentApplicationInput{
		ApplicationID: createdApplication, KnowledgeBaseWorkspaceID: &bogus, IdempotencyKey: marker + "-move-bogus",
	}); !errors.Is(err, ErrApplicationUpdateInvalidInput) {
		t.Fatalf("expected unknown knowledge base to fail validation, got %v", err)
	}
}

func cleanupApplicationIntegrationRow(t *testing.T, db *store.Store, sql string, arguments ...any) {
	t.Helper()
	if _, err := db.Pool.Exec(context.Background(), sql, arguments...); err != nil {
		t.Errorf("clean integration test data: %v", err)
	}
}

func seedApplicationEndpoint(t *testing.T, ctx context.Context, db *store.Store, organizationID, memberID, name string, capabilities modelendpoint.Capabilities) string {
	t.Helper()
	endpointID := uuid.NewString()
	capabilitiesJSON, err := json.Marshal(capabilities)
	if err != nil {
		t.Fatalf("encode endpoint capabilities: %v", err)
	}
	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin endpoint seed: %v", err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `
		INSERT INTO integration.model_endpoints
			(id, organization_id, name, current_revision, status, last_verified_at, created_by)
		VALUES ($1::uuid, $2::uuid, $3, 1, 'active', now(), $4::uuid)
	`, endpointID, organizationID, name, memberID); err != nil {
		t.Fatalf("insert model endpoint: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO integration.model_endpoint_revisions
			(model_endpoint_id, revision, provider_type, base_url, model_name, credential_mode,
			 credential_ciphertext, credential_key_id, options, capabilities, config_checksum, created_by)
		VALUES ($1::uuid, 1, 'openai_compatible', 'https://models.example.com/v1', 'test-model', 'encrypted',
			 $2, 'integration-test', '{}'::jsonb, $3::jsonb, $4, $5::uuid)
	`, endpointID, []byte("integration-test-ciphertext"), string(capabilitiesJSON), uuid.NewString(), memberID); err != nil {
		t.Fatalf("insert model endpoint revision: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit endpoint seed: %v", err)
	}
	return endpointID
}
