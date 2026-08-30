package asset

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"agentchunzhi/internal/auth"
	"agentchunzhi/internal/eventing"
	"agentchunzhi/internal/store"

	"github.com/google/uuid"
)

// TestCreateWorkspaceResolutionIntegration covers the open-API/agent create
// workspace contract: a workspace-bound model pins the target workspace, an
// organization-level model (builtin_* ships with NULL workspace) requires the
// caller to name an active workspace of the organization.
func TestCreateWorkspaceResolutionIntegration(t *testing.T) {
	databaseURL := os.Getenv("ASSET_INTEGRATION_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("ASSET_INTEGRATION_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	db, err := store.Open(ctx, databaseURL)
	if err != nil {
		t.Fatalf("open integration database: %v", err)
	}
	defer db.Close()

	var stale string
	if err := db.Pool.QueryRow(ctx, `SELECT id::text FROM organization.organizations WHERE slug = 'asset-create-ws-regression'`).Scan(&stale); err == nil {
		cleanupCreateWorkspaceFixture(t, ctx, db, stale)
	}
	var orgID, userID, wsA, wsB string
	// Unique slug per run (mirrors the QA scripts' suffix convention): a
	// fixture killed mid-run leaks rows instead of blocking the next run.
	orgSlug := "asset-create-ws-" + strings.ToLower(uuid.NewString()[:8])
	if err := db.Pool.QueryRow(ctx, `INSERT INTO organization.organizations (slug, name) VALUES ($1, 'Asset Create WS Regression') RETURNING id::text`, orgSlug).Scan(&orgID); err != nil {
		t.Fatalf("create organization: %v", err)
	}
	defer cleanupCreateWorkspaceFixture(t, context.Background(), db, orgID)

	if err := db.Pool.QueryRow(ctx, `
		INSERT INTO identity.users (organization_id, user_type, email, display_name, organization_role, password_hash)
		VALUES ($1::uuid, 'member', $2, 'Asset Create WS', 'member', 'integration-fixture-not-loginable')
		RETURNING id::text
	`, orgID, "asset-create-ws-"+strings.ToLower(uuid.NewString()[:8])+"@example.invalid").Scan(&userID); err != nil {
		t.Fatalf("create member: %v", err)
	}
	if err := db.Pool.QueryRow(ctx, `
		INSERT INTO content.workspaces (organization_id, slug, name, created_by)
		VALUES ($1::uuid, 'create-ws-a', 'Create WS A', $2::uuid) RETURNING id::text
	`, orgID, userID).Scan(&wsA); err != nil {
		t.Fatalf("create workspace A: %v", err)
	}
	if err := db.Pool.QueryRow(ctx, `
		INSERT INTO content.workspaces (organization_id, slug, name, created_by)
		VALUES ($1::uuid, 'create-ws-b', 'Create WS B', $2::uuid) RETURNING id::text
	`, orgID, userID).Scan(&wsB); err != nil {
		t.Fatalf("create workspace B: %v", err)
	}

	createModel := func(key string, workspace *string) string {
		t.Helper()
		var modelID string
		if err := db.Pool.QueryRow(ctx, `
			INSERT INTO model.resource_models (organization_id, workspace_id, model_key, name, status, created_by)
			VALUES ($1::uuid, $2::uuid, $3, $3, 'active', $4::uuid) RETURNING id::text
		`, orgID, workspace, key, userID).Scan(&modelID); err != nil {
			t.Fatalf("create model %s: %v", key, err)
		}
		if _, err := db.Pool.Exec(ctx, `
			INSERT INTO model.resource_model_versions
				(organization_id, resource_model_id, version_no, status, field_schema, published_at, created_by)
			VALUES ($1::uuid, $2::uuid, 1, 'published', '{"fields":[]}', now(), $3::uuid)
		`, orgID, modelID, userID); err != nil {
			t.Fatalf("create version for %s: %v", key, err)
		}
		if _, err := db.Pool.Exec(ctx, `
			UPDATE model.resource_models SET current_version_id = (
				SELECT id FROM model.resource_model_versions
				WHERE resource_model_id = $1::uuid AND version_no = 1
			) WHERE id = $1::uuid
		`, modelID); err != nil {
			t.Fatalf("pin current version for %s: %v", key, err)
		}
		return modelID
	}
	orgModelID := createModel("create-ws-org", nil)
	boundModelID := createModel("create-ws-bound", &wsA)

	events, err := eventing.NewEventStore(db.Pool)
	if err != nil {
		t.Fatalf("event store: %v", err)
	}
	svc := Service{Store: db, Events: &events}
	principal := auth.Principal{OrganizationID: orgID, UserID: userID, UserType: auth.UserTypeMember}

	// Syntactically valid but owns no workspace row, exercising the EXISTS
	// check rather than uuid parsing.
	unknownWorkspace := "00000000-0000-4000-8000-0000000000ad"
	cases := []struct {
		name          string
		modelID       string
		workspaceID   string
		wantErr       error
		wantWorkspace string
	}{
		{"org-level model with explicit workspace", orgModelID, wsA, nil, wsA},
		{"org-level model without workspace", orgModelID, "", ErrInvalidInput, ""},
		{"org-level model with unknown workspace", orgModelID, unknownWorkspace, ErrInvalidInput, ""},
		{"workspace-bound model pins its workspace", boundModelID, "", nil, wsA},
		{"workspace-bound model accepts its workspace", boundModelID, wsA, nil, wsA},
		{"workspace-bound model rejects foreign workspace", boundModelID, wsB, ErrInvalidInput, ""},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			title := "创建工作区解析集成"
			result, err := svc.Create(ctx, principal, []string{orgModelID, boundModelID},
				fmt.Sprintf("it-create-ws-%02d-0000000000", i),
				CreateInput{ResourceModelID: tc.modelID, WorkspaceID: tc.workspaceID, Title: &title})
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("expected %v, got %v", tc.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("create: %v", err)
			}
			if result.WorkspaceID != tc.wantWorkspace {
				t.Fatalf("result workspace = %q, want %q", result.WorkspaceID, tc.wantWorkspace)
			}
			var ws string
			if err := db.Pool.QueryRow(ctx, `SELECT workspace_id::text FROM asset.assets WHERE id = $1::uuid`, result.ID).Scan(&ws); err != nil {
				t.Fatalf("load created asset: %v", err)
			}
			if ws != tc.wantWorkspace {
				t.Fatalf("asset workspace = %s, want %s", ws, tc.wantWorkspace)
			}
		})
	}
}

func cleanupCreateWorkspaceFixture(t *testing.T, ctx context.Context, db *store.Store, orgID string) {
	// assets → drafts → versions → assets is a FK cycle (only assets_draft_fk
	// is deferrable), so the deletes run in one transaction with constraints
	// deferred, ordered children-first around the cycle.
	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		t.Logf("cleanup: begin: %v", err)
		return
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, "SET CONSTRAINTS ALL DEFERRED"); err != nil {
		t.Logf("cleanup: set constraints deferred: %v", err)
	}
	for _, statement := range []string{
		// assets.current_working_version_id is not deferrable: drop first.
		"UPDATE asset.assets SET current_working_version_id = NULL, current_published_version_id = NULL WHERE organization_id = $1::uuid",
		"DELETE FROM audit.event_deliveries WHERE event_id IN (SELECT id FROM audit.outbox_events WHERE organization_id = $1::uuid)",
		"DELETE FROM audit.outbox_events WHERE organization_id = $1::uuid",
		"DELETE FROM audit.audit_log WHERE organization_id = $1::uuid",
		"DELETE FROM asset.asset_drafts WHERE organization_id = $1::uuid",
		"DELETE FROM asset.asset_versions WHERE organization_id = $1::uuid",
		"DELETE FROM asset.assets WHERE organization_id = $1::uuid",
		"DELETE FROM asset.raw_inputs WHERE organization_id = $1::uuid",
		"DELETE FROM system.idempotency_keys WHERE organization_id = $1::uuid",
		"UPDATE model.resource_models SET current_version_id = NULL WHERE organization_id = $1::uuid",
		"DELETE FROM model.resource_model_versions WHERE organization_id = $1::uuid",
		"DELETE FROM model.resource_models WHERE organization_id = $1::uuid",
		"DELETE FROM content.workspaces WHERE organization_id = $1::uuid",
		"DELETE FROM identity.users WHERE organization_id = $1::uuid",
		"DELETE FROM organization.organizations WHERE id = $1::uuid",
	} {
		if _, err := tx.Exec(ctx, statement, orgID); err != nil {
			head := statement
			if len(head) > 60 {
				head = head[:60]
			}
			t.Logf("cleanup: %s: %v", head, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Logf("cleanup: commit: %v", err)
	}
}
