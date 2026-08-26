package authz

import (
	"context"
	"os"
	"reflect"
	"testing"
	"time"

	"agentchunzhi/internal/auth"
	"agentchunzhi/internal/store"
)

func TestPermissionScopesIntegration(t *testing.T) {
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

	var orgID, adminID, editorID, outsiderID, agentID, modelID, secondaryModelID, workspaceID, appID, endpointID string
	if err := db.Pool.QueryRow(ctx, `INSERT INTO organization.organizations (name) VALUES ('permission-regression') RETURNING id::text`).Scan(&orgID); err != nil {
		t.Fatalf("create organization: %v", err)
	}
	defer cleanupPermissionFixture(context.Background(), db, orgID)

	for _, user := range []struct {
		id          *string
		userType    string
		login       string
		displayName string
		memberRole  *string
	}{
		{&adminID, "member", "scope-admin", "Scope Admin", stringPointer("admin")},
		{&editorID, "member", "scope-editor", "Scope Editor", stringPointer("editor")},
		{&outsiderID, "member", "scope-outsider", "Scope Outsider", stringPointer("editor")},
		{&agentID, "agent", "", "Scope Agent", stringPointer("editor")},
	} {
		if err := db.Pool.QueryRow(ctx, `
			INSERT INTO identity.users (organization_id, user_type, login_name, display_name, member_role)
			VALUES ($1::uuid, $2, NULLIF($3, ''), $4, $5) RETURNING id::text
		`, orgID, user.userType, user.login, user.displayName, user.memberRole).Scan(user.id); err != nil {
			t.Fatalf("create %s: %v", user.displayName, err)
		}
	}
	if err := db.Pool.QueryRow(ctx, `
		INSERT INTO model.resource_models (organization_id, model_key, name, status, created_by)
		VALUES ($1::uuid, 'scope-model', 'Scope Model', 'active', $2::uuid) RETURNING id::text
	`, orgID, adminID).Scan(&modelID); err != nil {
		t.Fatalf("create resource model: %v", err)
	}
	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin model endpoint fixture: %v", err)
	}
	defer tx.Rollback(ctx)
	if err := tx.QueryRow(ctx, `
		INSERT INTO integration.model_endpoints
			(organization_id, name, status, created_by)
		VALUES ($1::uuid, 'Scope Endpoint', 'active', $2::uuid)
		RETURNING id::text
	`, orgID, adminID).Scan(&endpointID); err != nil {
		t.Fatalf("create model endpoint: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO integration.model_endpoint_revisions
			(model_endpoint_id, revision, provider_type, base_url, model_name,
			 credential_mode, secret_ref, config_checksum, created_by)
		VALUES ($1::uuid, 1, 'openai_compatible', 'https://model.example/v1', 'scope-model',
		        'secret_ref', 'SCOPE_MODEL_API_KEY', 'scope-fixture-v1', $2::uuid)
	`, endpointID, adminID); err != nil {
		t.Fatalf("create model endpoint revision: %v", err)
	}
	if err := tx.QueryRow(ctx, `
		INSERT INTO integration.agent_applications
			(organization_id, bound_agent_user_id, model_endpoint_id, runtime_mode, name, status)
		VALUES ($1::uuid, $2::uuid, $3::uuid, 'rag', 'Scope App', 'active')
		RETURNING id::text
	`, orgID, agentID, endpointID).Scan(&appID); err != nil {
		t.Fatalf("create agent application: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit model endpoint fixture: %v", err)
	}
	if err := db.Pool.QueryRow(ctx, `
		INSERT INTO content.workspaces
			(organization_id, name, default_agent_application_id, default_resource_model_id, created_by)
		VALUES ($1::uuid, 'Scope Workspace', $2::uuid, $3::uuid, $4::uuid) RETURNING id::text
	`, orgID, appID, modelID, adminID).Scan(&workspaceID); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	if err := db.Pool.QueryRow(ctx, `
		INSERT INTO model.resource_models (organization_id, workspace_id, model_key, name, status, created_by)
		VALUES ($1::uuid, $2::uuid, 'scope-secondary-model', 'Scope Secondary Model', 'active', $3::uuid)
		RETURNING id::text
	`, orgID, workspaceID, adminID).Scan(&secondaryModelID); err != nil {
		t.Fatalf("create secondary resource model: %v", err)
	}
	grants := []struct {
		name string
		sql  string
		args []any
	}{
		{"workspace membership", `INSERT INTO content.workspace_members (organization_id, workspace_id, user_id, role) VALUES ($1::uuid, $2::uuid, $3::uuid, 'editor')`, []any{orgID, workspaceID, editorID}},
		{"workspace application", `INSERT INTO content.workspace_agent_applications (organization_id, workspace_id, agent_application_id, enabled, created_by) VALUES ($1::uuid, $2::uuid, $3::uuid, true, $4::uuid)`, []any{orgID, workspaceID, appID, adminID}},
		{"agent model policy", `INSERT INTO content.agent_access_policies (organization_id, workspace_id, agent_user_id, resource_model_id, actions, created_by) VALUES ($1::uuid, NULL, $2::uuid, $3::uuid, ARRAY['read', 'create'], $4::uuid)`, []any{orgID, agentID, modelID, adminID}},
	}
	for _, grant := range grants {
		if _, err := db.Pool.Exec(ctx, grant.sql, grant.args...); err != nil {
			t.Fatalf("create %s: %v", grant.name, err)
		}
	}

	resolver := ScopeResolver{Store: db}
	admin := auth.Principal{OrganizationID: orgID, UserID: adminID, UserType: "member"}
	editor := auth.Principal{OrganizationID: orgID, UserID: editorID, UserType: "member"}
	outsider := auth.Principal{OrganizationID: orgID, UserID: outsiderID, UserType: "member"}
	agent := auth.Principal{OrganizationID: orgID, UserID: agentID, UserType: "agent"}

	checks := []struct {
		name    string
		resolve func() ([]string, error)
		want    []string
	}{
		{"admin system resources", func() ([]string, error) { return resolver.AllowedSystemResourceIDs(ctx, admin, "agent.manage") }, []string{"system:agent-users", "system:agent-applications"}},
		{"editor system resources", func() ([]string, error) { return resolver.AllowedSystemResourceIDs(ctx, editor, "agent.manage") }, []string{}},
		{"admin model", func() ([]string, error) { return resolver.AllowedModelIDs(ctx, admin, "asset.publish") }, []string{modelID, secondaryModelID}},
		{"workspace editor model", func() ([]string, error) { return resolver.AllowedModelIDs(ctx, editor, "asset.edit") }, []string{modelID, secondaryModelID}},
		{"outsider model", func() ([]string, error) { return resolver.AllowedModelIDs(ctx, outsider, "asset.read") }, []string{}},
		{"agent allowed action", func() ([]string, error) { return resolver.AllowedModelIDs(ctx, agent, "asset.read") }, []string{modelID}},
		{"agent denied action", func() ([]string, error) { return resolver.AllowedModelIDs(ctx, agent, "asset.publish") }, []string{}},
		{"workspace editor application", func() ([]string, error) { return resolver.AllowedAgentApplicationIDs(ctx, editor, "agent.use") }, []string{appID}},
		{"outsider application", func() ([]string, error) { return resolver.AllowedAgentApplicationIDs(ctx, outsider, "agent.use") }, []string{}},
	}
	for _, check := range checks {
		t.Run(check.name, func(t *testing.T) {
			got, err := check.resolve()
			assertScope(t, got, err, check.want)
		})
	}
}

func assertScope(t *testing.T, got []string, err error, want []string) {
	t.Helper()
	if err != nil {
		t.Fatalf("resolve scope: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("scope = %#v, want %#v", got, want)
	}
}

func cleanupPermissionFixture(ctx context.Context, db *store.Store, orgID string) {
	for _, statement := range []string{
		"DELETE FROM content.agent_access_policies WHERE organization_id = $1::uuid",
		"DELETE FROM content.workspace_agent_applications WHERE organization_id = $1::uuid",
		"DELETE FROM content.workspace_members WHERE organization_id = $1::uuid",
		"DELETE FROM content.workspaces WHERE organization_id = $1::uuid",
		"DELETE FROM integration.agent_applications WHERE organization_id = $1::uuid",
		"DELETE FROM integration.model_endpoints WHERE organization_id = $1::uuid",
		"DELETE FROM model.resource_models WHERE organization_id = $1::uuid",
		"DELETE FROM identity.users WHERE organization_id = $1::uuid",
		"DELETE FROM organization.organizations WHERE id = $1::uuid",
	} {
		_, _ = db.Pool.Exec(ctx, statement, orgID)
	}
}

func stringPointer(value string) *string { return &value }
