package authz

// workspace_integration_test.go — the agent Require semantics fixed with the
// C11-family NULL rule: an organization-level AgentAccessPolicy row
// (workspace_id NULL) answers for every workspace; a workspace-specific row
// wins over the org-level fallback.

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"agentchunzhi/internal/auth"
	"agentchunzhi/internal/store"
)

func TestAgentRequireOrgLevelPolicyIntegration(t *testing.T) {
	databaseURL := os.Getenv("PERMISSION_INTEGRATION_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("PERMISSION_INTEGRATION_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	db, err := store.Open(ctx, databaseURL)
	if err != nil {
		t.Fatalf("open integration database: %v", err)
	}
	defer db.Close()

	var orgID, adminID, agentID, modelID, workspaceA, workspaceB string
	suffix := time.Now().UnixNano() % 1e9
	if err := db.Pool.QueryRow(ctx, `INSERT INTO organization.organizations (name, slug) VALUES ('agent-require-null', 'arn-' || md5(random()::text)) RETURNING id::text`).Scan(&orgID); err != nil {
		t.Fatalf("create organization: %v", err)
	}
	defer func() {
		_, _ = db.Pool.Exec(context.Background(), `DELETE FROM organization.organizations WHERE id = $1::uuid`, orgID)
	}()
	for _, user := range []struct {
		id    *string
		kind  string
		email string
	}{
		{&adminID, "member", "arn-admin@example.invalid"},
		{&agentID, "agent", "arn-agent@example.invalid"},
	} {
		if err := db.Pool.QueryRow(ctx, `
			INSERT INTO identity.users (organization_id, user_type, email, display_name, organization_role, password_hash)
			VALUES ($1::uuid, $2, $3 || '-' || $5, $3, NULLIF($4, '')::text, 'arn-fixture') RETURNING id::text
		`, orgID, user.kind, user.email, map[bool]string{true: "member", false: ""}[user.kind == "member"], fmt.Sprintf("%d", suffix)).Scan(user.id); err != nil {
			t.Fatalf("create %s: %v", user.email, err)
		}
	}
	if err := db.Pool.QueryRow(ctx, `
		INSERT INTO model.resource_models (organization_id, model_key, name, status, created_by)
		VALUES ($1::uuid, 'arn-model', 'ARN Model', 'active', $2::uuid) RETURNING id::text
	`, orgID, adminID).Scan(&modelID); err != nil {
		t.Fatalf("create resource model: %v", err)
	}
	for index, workspace := range []*string{&workspaceA, &workspaceB} {
		if err := db.Pool.QueryRow(ctx, `
			INSERT INTO content.workspaces (organization_id, slug, name, default_resource_model_id, created_by)
			VALUES ($1::uuid, 'arn-ws-' || $4, 'arn-ws', $2::uuid, $3::uuid) RETURNING id::text
		`, orgID, modelID, adminID, fmt.Sprintf("w%d-%d", index, suffix)).Scan(workspace); err != nil {
			t.Fatalf("create workspace: %v", err)
		}
	}
	service := WorkspacePolicyService{Store: db}
	agent := auth.Principal{OrganizationID: orgID, UserID: agentID, UserType: auth.UserTypeAgent}

	// 1. No policy row at all: denied.
	if _, err := service.Require(ctx, agent, workspaceA, modelID, ActionQueryExecute); err == nil {
		t.Fatal("agent without any policy row must be denied")
	}
	// 2. An org-level row (workspace NULL) answers for EVERY workspace.
	if _, err := db.Pool.Exec(ctx, `
		INSERT INTO content.agent_access_policies (organization_id, workspace_id, agent_user_id, resource_model_id, actions, created_by)
		VALUES ($1::uuid, NULL, $2::uuid, $3::uuid, ARRAY['read','query.execute']::text[], $4::uuid)
	`, orgID, agentID, modelID, adminID); err != nil {
		t.Fatalf("org-level policy row: %v", err)
	}
	for _, workspace := range []string{workspaceA, workspaceB} {
		scope, err := service.Require(ctx, agent, workspace, modelID, ActionQueryExecute)
		if err != nil {
			t.Fatalf("org-level row must answer in workspace %s: %v", workspace, err)
		}
		if scope.WorkspaceID != workspace {
			t.Fatalf("scope workspace mismatch: %s", scope.WorkspaceID)
		}
	}
	// 3. A workspace-specific row wins over the org fallback (narrowest
	// grant first): workspaceA narrows the agent down to read-only.
	if _, err := db.Pool.Exec(ctx, `
		INSERT INTO content.agent_access_policies (organization_id, workspace_id, agent_user_id, resource_model_id, actions, created_by)
		VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, ARRAY['read']::text[], $5::uuid)
	`, orgID, workspaceA, agentID, modelID, adminID); err != nil {
		t.Fatalf("workspace policy row: %v", err)
	}
	if _, err := service.Require(ctx, agent, workspaceA, modelID, ActionQueryExecute); err == nil {
		t.Fatal("workspace row must win over the org-level fallback and deny query.execute")
	}
	if _, err := service.Require(ctx, agent, workspaceB, modelID, ActionQueryExecute); err != nil {
		t.Fatalf("workspaceB still falls back to the org row: %v", err)
	}
}
