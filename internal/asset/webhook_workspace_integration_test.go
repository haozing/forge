package asset

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"agentchunzhi/internal/auth"
	"agentchunzhi/internal/authz"
	"agentchunzhi/internal/eventing"
	"agentchunzhi/internal/store"

	"github.com/google/uuid"
)

// TestWebhookExportWorkspaceResolutionIntegration covers the webhook and export
// channels against organization-level models (builtin_* ships with NULL
// workspace): webhook target resolution and creation must accept an explicit
// active workspace instead of failing the NULL scan, exports must authorize on
// the requested workspace, and workspace-bound models keep their pinning
// semantics.
func TestWebhookExportWorkspaceResolutionIntegration(t *testing.T) {
	databaseURL := os.Getenv("ASSET_INTEGRATION_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("ASSET_INTEGRATION_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	db, err := store.Open(ctx, databaseURL)
	if err != nil {
		t.Fatalf("open integration database: %v", err)
	}
	defer db.Close()

	orgSlug := "asset-webhook-ws-" + strings.ToLower(uuid.NewString()[:8])
	var orgID, memberID, agentID, wsA, wsB string
	if err := db.Pool.QueryRow(ctx, `INSERT INTO organization.organizations (slug, name) VALUES ($1, 'Webhook WS Regression') RETURNING id::text`, orgSlug).Scan(&orgID); err != nil {
		t.Fatalf("create organization: %v", err)
	}
	defer cleanupWebhookWorkspaceFixture(t, context.Background(), db, orgID)

	if err := db.Pool.QueryRow(ctx, `
		INSERT INTO identity.users (organization_id, user_type, email, display_name, organization_role, password_hash)
		VALUES ($1::uuid, 'member', $2, 'Webhook WS Member', 'admin', 'integration-fixture-not-loginable')
		RETURNING id::text
	`, orgID, "asset-webhook-ws-"+strings.ToLower(uuid.NewString()[:8])+"@example.invalid").Scan(&memberID); err != nil {
		t.Fatalf("create member: %v", err)
	}
	if err := db.Pool.QueryRow(ctx, `
		INSERT INTO identity.users (organization_id, user_type, display_name)
		VALUES ($1::uuid, 'agent', 'Webhook WS Agent')
		RETURNING id::text
	`, orgID).Scan(&agentID); err != nil {
		t.Fatalf("create agent user: %v", err)
	}
	for _, ws := range []*struct {
		slug string
		id   *string
	}{{"webhook-ws-a", &wsA}, {"webhook-ws-b", &wsB}} {
		if err := db.Pool.QueryRow(ctx, `
			INSERT INTO content.workspaces (organization_id, slug, name, created_by)
			VALUES ($1::uuid, $2, $3, $4::uuid) RETURNING id::text
		`, orgID, ws.slug, "Webhook "+ws.slug, memberID).Scan(ws.id); err != nil {
			t.Fatalf("create workspace %s: %v", ws.slug, err)
		}
	}
	for _, wsID := range []string{wsA, wsB} {
		if _, err := db.Pool.Exec(ctx, `
			INSERT INTO content.workspace_members (organization_id, workspace_id, user_id, role, granted_by)
			VALUES ($1::uuid, $2::uuid, $3::uuid, 'admin', $4::uuid)
		`, orgID, wsID, memberID, memberID); err != nil {
			t.Fatalf("add workspace member: %v", err)
		}
	}

	createModel := func(key string, workspace *string) string {
		t.Helper()
		var modelID string
		if err := db.Pool.QueryRow(ctx, `
			INSERT INTO model.resource_models (organization_id, workspace_id, model_key, name, status, created_by)
			VALUES ($1::uuid, $2::uuid, $3, $3, 'active', $4::uuid) RETURNING id::text
		`, orgID, workspace, key, memberID).Scan(&modelID); err != nil {
			t.Fatalf("create model %s: %v", key, err)
		}
		if _, err := db.Pool.Exec(ctx, `
			INSERT INTO model.resource_model_versions
				(organization_id, resource_model_id, version_no, status, field_schema, published_at, created_by)
			VALUES ($1::uuid, $2::uuid, 1, 'published', '{"fields":[]}', now(), $3::uuid)
		`, orgID, modelID, memberID); err != nil {
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
	orgModelID := createModel("webhook-ws-org", nil)
	boundModelID := createModel("webhook-ws-bound", &wsA)
	for _, modelID := range []string{orgModelID, boundModelID} {
		if _, err := db.Pool.Exec(ctx, `
			INSERT INTO content.agent_access_policies
				(organization_id, workspace_id, agent_user_id, resource_model_id, actions, created_by)
			VALUES ($1::uuid, NULL, $2::uuid, $3::uuid, '{create}', $4::uuid)
		`, orgID, agentID, modelID, memberID); err != nil {
			t.Fatalf("grant agent policy: %v", err)
		}
	}

	events, err := eventing.NewEventStore(db.Pool)
	if err != nil {
		t.Fatalf("event store: %v", err)
	}
	transfer := TransferService{Store: db, Policy: authz.WorkspacePolicyService{Store: db}}
	svc := Service{Store: db, Events: &events}
	agent := auth.Principal{OrganizationID: orgID, UserID: agentID, UserType: auth.UserTypeAgent}
	member := auth.Principal{OrganizationID: orgID, UserID: memberID, UserType: auth.UserTypeMember}
	// Syntactically valid but owns no workspace row, exercising the EXISTS
	// check rather than uuid parsing.
	unknownWorkspace := "00000000-0000-4000-8000-0000000000bd"

	t.Run("resolve target", func(t *testing.T) {
		cases := []struct {
			name          string
			modelID       string
			workspaceID   string
			wantErr       error
			wantWorkspace string
		}{
			{"org-level model with explicit workspace", orgModelID, wsA, nil, wsA},
			{"org-level model with other workspace", orgModelID, wsB, nil, wsB},
			{"org-level model without workspace", orgModelID, "", ErrInvalidInput, ""},
			{"org-level model with unknown workspace", orgModelID, unknownWorkspace, ErrInvalidInput, ""},
			{"workspace-bound model pins its workspace", boundModelID, "", nil, wsA},
			{"workspace-bound model rejects foreign workspace", boundModelID, wsB, ErrInvalidInput, ""},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				target, err := transfer.ResolveWebhookTarget(ctx, agent, tc.workspaceID, tc.modelID)
				if tc.wantErr != nil {
					if !errors.Is(err, tc.wantErr) {
						t.Fatalf("expected %v, got %v", tc.wantErr, err)
					}
					return
				}
				if err != nil {
					t.Fatalf("resolve: %v", err)
				}
				if target.WorkspaceID != tc.wantWorkspace || target.ResourceModelID != tc.modelID {
					t.Fatalf("target = (%s, %s), want (%s, %s)", target.WorkspaceID, target.ResourceModelID, tc.wantWorkspace, tc.modelID)
				}
			})
		}
	})

	t.Run("create from webhook", func(t *testing.T) {
		title := "webhook 组织级模型落位"
		result, replay, err := svc.CreateFromWebhook(ctx, agent, WebhookAssetInput{
			WorkspaceID:     wsA,
			ResourceModelID: orgModelID,
			ExternalRef:     "itd-webhook-ws-" + strings.ToLower(uuid.NewString()[:8]),
			Title:           &title,
			ReceivedAt:      time.Now(),
		})
		if err != nil {
			t.Fatalf("create from webhook with org-level model: %v", err)
		}
		if replay {
			t.Fatal("first push must not replay")
		}
		if result.WorkspaceID != wsA {
			t.Fatalf("result workspace = %q, want %q", result.WorkspaceID, wsA)
		}
		var ws string
		if err := db.Pool.QueryRow(ctx, `SELECT workspace_id::text FROM asset.assets WHERE id = $1::uuid`, result.ID).Scan(&ws); err != nil {
			t.Fatalf("load created asset: %v", err)
		}
		if ws != wsA {
			t.Fatalf("asset workspace = %s, want %s", ws, wsA)
		}

		if _, _, err := svc.CreateFromWebhook(ctx, agent, WebhookAssetInput{
			WorkspaceID:     unknownWorkspace,
			ResourceModelID: orgModelID,
			ExternalRef:     "itd-webhook-ws-" + strings.ToLower(uuid.NewString()[:8]),
			Title:           &title,
		}); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("unknown workspace: expected ErrInvalidInput, got %v", err)
		}
		if _, _, err := svc.CreateFromWebhook(ctx, agent, WebhookAssetInput{
			WorkspaceID:     wsB,
			ResourceModelID: boundModelID,
			ExternalRef:     "itd-webhook-ws-" + strings.ToLower(uuid.NewString()[:8]),
			Title:           &title,
		}); !errors.Is(err, ErrForbidden) {
			t.Fatalf("bound model foreign workspace: expected ErrForbidden, got %v", err)
		}
	})

	t.Run("start export", func(t *testing.T) {
		job, err := transfer.StartExport(ctx, member, wsA, "itd-export-ws-"+strings.ToLower(uuid.NewString()[:8])+"-1", ExportInput{ResourceModelID: orgModelID})
		if err != nil {
			t.Fatalf("export with org-level model: %v", err)
		}
		if job.WorkspaceID != wsA {
			t.Fatalf("job workspace = %q, want %q", job.WorkspaceID, wsA)
		}
		if _, err := transfer.StartExport(ctx, member, wsB, "itd-export-ws-"+strings.ToLower(uuid.NewString()[:8])+"-2", ExportInput{ResourceModelID: boundModelID}); !errors.Is(err, ErrForbidden) {
			t.Fatalf("bound model foreign workspace: expected ErrForbidden, got %v", err)
		}
	})
}

func cleanupWebhookWorkspaceFixture(t *testing.T, ctx context.Context, db *store.Store, orgID string) {
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
		"DELETE FROM asset.export_jobs WHERE organization_id = $1::uuid",
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
		"DELETE FROM content.agent_access_policies WHERE organization_id = $1::uuid",
		"DELETE FROM model.resource_models WHERE organization_id = $1::uuid",
		"DELETE FROM content.workspace_members WHERE organization_id = $1::uuid",
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
