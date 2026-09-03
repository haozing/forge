package resourcemodel

// agent_draft_integration_test.go — exercises the agent model-draft channel
// against a real database (AGENTCHUNZHI_TEST_DATABASE_URL; skipped when
// unset). Fixtures are scoped to marker rows and cleaned up on exit.

import (
	"context"
	"errors"
	"os"
	"testing"

	"agentchunzhi/internal/auth"
	"agentchunzhi/internal/store"
)

func agentDraftFixture(t *testing.T, db *store.Store, marker string) (auth.Principal, string, func()) {
	t.Helper()
	ctx := context.Background()
	var organizationID, workspaceID, modelID, memberID string
	if err := db.Pool.QueryRow(ctx, `
		SELECT o.id::text, w.id::text, m.id::text, u.id::text
		FROM organization.organizations o
		JOIN content.workspaces w ON w.organization_id = o.id AND w.status = 'active'
		JOIN model.resource_models m ON m.organization_id = o.id AND m.workspace_id IS NULL
		JOIN identity.users u ON u.organization_id = o.id AND u.user_type = 'member' AND u.status = 'active'
		WHERE o.status = 'active'
		ORDER BY o.created_at, w.created_at, m.created_at, u.created_at LIMIT 1
	`).Scan(&organizationID, &workspaceID, &modelID, &memberID); err != nil {
		t.Fatalf("load agent draft fixture scope: %v", err)
	}
	var agentID string
	if err := db.Pool.QueryRow(ctx, `
		INSERT INTO identity.users (organization_id, user_type, display_name)
		VALUES ($1::uuid, 'agent', $2)
		RETURNING id::text
	`, organizationID, marker).Scan(&agentID); err != nil {
		t.Fatalf("seed agent user: %v", err)
	}
	if _, err := db.Pool.Exec(ctx, `
		INSERT INTO content.agent_access_policies
			(organization_id, workspace_id, agent_user_id, resource_model_id, actions, created_by)
		VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, '{model.manage}', $5::uuid)
	`, organizationID, workspaceID, agentID, modelID, memberID); err != nil {
		t.Fatalf("seed agent access policy: %v", err)
	}
	principal := auth.Principal{OrganizationID: organizationID, UserID: agentID, UserType: "agent"}
	cleanup := func() {
		_, _ = db.Pool.Exec(ctx, `DELETE FROM content.agent_access_policies WHERE agent_user_id = $1::uuid`, agentID)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM identity.users WHERE id = $1::uuid`, agentID)
	}
	return principal, workspaceID, cleanup
}

func validDraftInput() CreateInput {
	return CreateInput{
		ModelKey: "agent_draft_it", Name: "集成测试模型", ContentKind: "record",
		InitialVersion: InitialVersion{
			FieldSchema: map[string]any{"additional_properties": false, "fields": []any{
				map[string]any{"key": "stage", "type": "enum", "options": []any{map[string]any{"value": "idea", "label": "Idea"}}, "default": "idea"},
			}},
			FormSchema: map[string]any{"sections": []any{}},
			ListSchema: map[string]any{"columns": []any{"title"}, "filters": []any{}},
			Policy: map[string]any{
				"visibility": map[string]any{"default": "workspace", "allowed": []any{"workspace"}},
				"channels":   map[string]any{"workspace": map[string]any{"enabled": true}},
				"retrieval":  map[string]any{"structured": map[string]any{"enabled": true}},
				"publishing": map[string]any{"mode": "direct", "required_fields": []any{}},
			},
		},
	}
}

func TestAgentDraftChannelIntegration(t *testing.T) {
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
	service := AgentDraftService{Store: db}

	t.Run("ungranted agent is forbidden", func(t *testing.T) {
		stranger := auth.Principal{OrganizationID: "00000000-0000-4000-8000-000000000001", UserID: "00000000-0000-4000-8000-000000000002", UserType: "agent"}
		if _, err := service.AgentCreateDraft(ctx, stranger, "00000000-0000-4000-8000-000000000003", validDraftInput()); !errors.Is(err, ErrForbidden) {
			t.Fatalf("expected ErrForbidden, got %v", err)
		}
	})

	principal, workspaceID, cleanup := agentDraftFixture(t, db, "agent-draft-it")
	t.Cleanup(cleanup)

	t.Run("member principal is forbidden", func(t *testing.T) {
		member := auth.Principal{OrganizationID: principal.OrganizationID, UserID: principal.UserID, UserType: "member"}
		if _, err := service.AgentCreateDraft(ctx, member, workspaceID, validDraftInput()); !errors.Is(err, ErrForbidden) {
			t.Fatalf("expected ErrForbidden for member principal, got %v", err)
		}
	})

	input := validDraftInput()
	model, err := service.AgentCreateDraft(ctx, principal, workspaceID, input)
	if err != nil {
		t.Fatalf("agent create draft: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(ctx, `DELETE FROM model.resource_model_versions WHERE resource_model_id = $1::uuid`, model.ID)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM model.resource_models WHERE id = $1::uuid`, model.ID)
	})
	if model.Status != "draft" || model.CurrentVersion == nil || model.CurrentVersion.Status != "draft" {
		t.Fatalf("draft channel must only create draft rows: %+v", model)
	}
	var persisted string
	if err := db.Pool.QueryRow(ctx, `SELECT status FROM model.resource_models WHERE id = $1::uuid`, model.ID).Scan(&persisted); err != nil || persisted != "draft" {
		t.Fatalf("model row must persist as draft, got %q err=%v", persisted, err)
	}

	t.Run("duplicate key conflicts", func(t *testing.T) {
		if _, err := service.AgentCreateDraft(ctx, principal, workspaceID, input); !errors.Is(err, ErrConflict) {
			t.Fatalf("expected ErrConflict on duplicate model_key, got %v", err)
		}
	})

	patch := VersionPatchInput{FieldSchema: &[]map[string]any{{
		"additional_properties": false,
		"fields": []any{
			map[string]any{"key": "stage", "type": "enum", "options": []any{map[string]any{"value": "idea", "label": "Idea"}}, "default": "idea"},
			map[string]any{"key": "owner", "type": "string", "default": ""},
		},
	}}[0]}
	t.Run("etag mismatch rejected", func(t *testing.T) {
		if _, err := service.AgentPatchDraftVersion(ctx, principal, workspaceID, model.CurrentVersion.ID, "deadbeef", patch); !errors.Is(err, ErrConflict) {
			t.Fatalf("expected etag conflict, got %v", err)
		}
	})
	version, err := service.AgentPatchDraftVersion(ctx, principal, workspaceID, model.CurrentVersion.ID, model.CurrentVersion.SchemaChecksum, patch)
	if err != nil {
		t.Fatalf("agent patch draft: %v", err)
	}
	if version.SchemaChecksum == model.CurrentVersion.SchemaChecksum {
		t.Fatal("checksum must change after a schema patch")
	}
	var fields int
	if err := db.Pool.QueryRow(ctx, `SELECT jsonb_array_length(field_schema->'fields') FROM model.resource_model_versions WHERE id = $1::uuid`, version.ID).Scan(&fields); err != nil || fields != 2 {
		t.Fatalf("patched schema must persist 2 fields, got %d err=%v", fields, err)
	}
}
