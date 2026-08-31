package admin

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"agentchunzhi/internal/auth"
	"agentchunzhi/internal/store"
)

// Integration path mirrors application_integration_test.go and skips when no
// AGENTCHUNZHI_TEST_DATABASE_URL is configured. Everything it creates is
// ITC-prefixed and removed afterwards.
func TestGetAgentOnboardingPackageIntegration(t *testing.T) {
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
	var organizationID, memberID, agentUserID, modelID string
	if err := db.Pool.QueryRow(ctx, `
		WITH org AS (
			INSERT INTO organization.organizations (name, status) VALUES ('ITC-OnboardOrg', 'active') RETURNING id
		), member AS (
			INSERT INTO identity.users (organization_id, user_type, display_name, status)
			SELECT id, 'member', 'ITC-OnboardAdmin', 'active' FROM org RETURNING id
		), agent AS (
			INSERT INTO identity.users (organization_id, user_type, display_name, status)
			SELECT id, 'agent', 'ITC-OnboardAgent', 'active' FROM org RETURNING id
		), model AS (
			INSERT INTO model.resource_models (organization_id, model_key, name, status, created_by)
			SELECT o.id, 'itc_onboard_docs', 'ITC Onboard Docs', 'active', m.id FROM org o, member m RETURNING id
		)
		SELECT o.id::text, m.id::text, a.id::text, r.id::text FROM org o, member m, agent a, model r
	`).Scan(&organizationID, &memberID, &agentUserID, &modelID); err != nil {
		t.Fatalf("seed integration fixture: %v", err)
	}
	defer func() {
		db.Pool.Exec(ctx, `DELETE FROM content.agent_access_policies WHERE organization_id = $1`, organizationID)
		db.Pool.Exec(ctx, `DELETE FROM integration.agent_applications WHERE organization_id = $1`, organizationID)
		db.Pool.Exec(ctx, `DELETE FROM identity.api_keys WHERE user_id = $1`, agentUserID)
		db.Pool.Exec(ctx, `DELETE FROM model.resource_models WHERE organization_id = $1`, organizationID)
		db.Pool.Exec(ctx, `DELETE FROM identity.users WHERE organization_id = $1`, organizationID)
		db.Pool.Exec(ctx, `DELETE FROM organization.organizations WHERE id = $1`, organizationID)
	}()
	if _, err := db.Pool.Exec(ctx, `
		INSERT INTO identity.api_keys (user_id, name, key_prefix, key_hash, capabilities)
		VALUES ($1::uuid, 'itc-onboard-key', 'ak_ITCONB0000', 'hash-onboard', '["query.read","reference.read"]'::jsonb)
	`, agentUserID); err != nil {
		t.Fatalf("seed api key: %v", err)
	}
	if _, err := db.Pool.Exec(ctx, `
		INSERT INTO integration.agent_applications
			(organization_id, bound_agent_user_id, name, runtime_mode, workflow_key, capabilities)
		VALUES ($1::uuid, $2::uuid, 'ITC-OnboardApp', 'workflow', 'asset_prepare', '["query.read","reference.read"]'::jsonb)
	`, organizationID, agentUserID); err != nil {
		t.Fatalf("seed application: %v", err)
	}
	if _, err := db.Pool.Exec(ctx, `
		INSERT INTO content.agent_access_policies
			(organization_id, workspace_id, agent_user_id, resource_model_id, actions, created_by)
		VALUES ($1::uuid, NULL, $2::uuid, $3::uuid, ARRAY['publish','read'], $4::uuid)
	`, organizationID, agentUserID, modelID, memberID); err != nil {
		t.Fatalf("seed access policy: %v", err)
	}

	principal := auth.Principal{UserType: "member", UserID: memberID, OrganizationID: organizationID}
	svc := Service{Store: db}
	pack, err := svc.GetAgentOnboarding(ctx, principal, agentUserID, "https://kb.example.com/")
	if err != nil {
		t.Fatalf("build onboarding package: %v", err)
	}
	if pack.BaseURL != "https://kb.example.com" || pack.AgentUserID != agentUserID || pack.ApiKeyPrefix == nil || *pack.ApiKeyPrefix != "ak_ITCONB0000" {
		t.Fatalf("identity block wrong: %#v", pack)
	}
	if pack.RuntimeMode != "workflow" || pack.WorkflowKey != "asset_prepare" {
		t.Fatalf("runtime block wrong: %s/%s", pack.RuntimeMode, pack.WorkflowKey)
	}
	if pack.OpenAPIURL != "/openapi.yaml" || pack.Auth.Type != "Bearer" || pack.Auth.Header != "Authorization" {
		t.Fatalf("docs/auth block wrong: %s %+v", pack.OpenAPIURL, pack.Auth)
	}
	if len(pack.Capabilities) != 2 || pack.Capabilities[0] != "query.read" || pack.Capabilities[1] != "reference.read" {
		t.Fatalf("capabilities wrong: %#v", pack.Capabilities)
	}
	if len(pack.ResourceModels) != 1 || pack.ResourceModels[0].ID != modelID || len(pack.ResourceModels[0].Actions) != 2 {
		t.Fatalf("resource models wrong: %#v", pack.ResourceModels)
	}
	allowed := map[string]bool{}
	for _, operation := range pack.AllowedOperations {
		allowed[operation.Operation] = operation.Allowed
	}
	if !allowed["query"] || !allowed["references"] || !allowed["automation.callback"] {
		t.Fatalf("granted operations missing: %#v", allowed)
	}
	if allowed["assets.create"] || allowed["assets.update"] || allowed["assets.publish"] || allowed["assets.archive"] || allowed["tasks"] {
		t.Fatalf("operations beyond grants must stay denied: %#v", allowed)
	}
	for _, fragment := range []string{"https://kb.example.com/api/open/query", "Authorization: Bearer"} {
		if !strings.Contains(pack.SampleCurl, fragment) {
			t.Fatalf("sample curl missing %q: %s", fragment, pack.SampleCurl)
		}
	}

	// A disabled agent must be refused distinctly, not treated as unknown.
	if _, err := db.Pool.Exec(ctx, `UPDATE identity.users SET status = 'disabled' WHERE id = $1::uuid`, agentUserID); err != nil {
		t.Fatalf("disable agent fixture: %v", err)
	}
	if _, err := svc.GetAgentOnboarding(ctx, principal, agentUserID, "https://kb.example.com"); !errors.Is(err, ErrAgentNotAllowed) {
		t.Fatalf("disabled agent must map to ErrAgentNotAllowed, got %v", err)
	}
}
