package workspace

import (
	"context"
	"os"
	"testing"

	"agentchunzhi/internal/auth"
	"agentchunzhi/internal/store"
)

// TestMembershipQueriesSQLIntegration runs the member list and detail SQL
// against a real database. The statements must only reference columns that
// exist in the migration baseline (identity.users has email, no login_name);
// without a database the test skips.
func TestMembershipQueriesSQLIntegration(t *testing.T) {
	databaseURL := os.Getenv("AGENTCHUNZHI_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("AGENTCHUNZHI_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	db, err := store.Open(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	var organizationID, memberID, workspaceID string
	if err := db.Pool.QueryRow(ctx, `
		SELECT w.organization_id::text, wm.user_id::text, w.id::text
		FROM content.workspaces w
		JOIN content.workspace_members wm ON wm.organization_id = w.organization_id AND wm.workspace_id = w.id
		JOIN identity.users u ON u.id = wm.user_id AND u.user_type = 'member' AND u.status = 'active'
		WHERE w.status = 'active'
		ORDER BY w.created_at, wm.created_at, w.id
		LIMIT 1
	`).Scan(&organizationID, &memberID, &workspaceID); err != nil {
		t.Fatalf("load membership integration scope: %v", err)
	}
	principal := auth.Principal{OrganizationID: organizationID, UserID: memberID, UserType: auth.UserTypeMember}
	service := Service{Store: db}

	members, err := service.ListMembers(ctx, principal, workspaceID)
	if err != nil {
		t.Fatalf("list workspace members: %v", err)
	}
	if len(members) == 0 {
		t.Fatal("workspace member list must not be empty")
	}
	for _, item := range members {
		if item.ID == "" || item.DisplayName == "" || item.Role == "" {
			t.Fatalf("member row missing required fields: %+v", item)
		}
	}

	detail, err := service.MemberDetail(ctx, principal, workspaceID, memberID)
	if err != nil {
		t.Fatalf("get workspace member detail: %v", err)
	}
	if detail.ID != memberID {
		t.Fatalf("member detail id = %q, want %q", detail.ID, memberID)
	}

	summary, err := service.Member(ctx, principal, workspaceID)
	if err != nil {
		t.Fatalf("get workspace member summary: %v", err)
	}
	if summary.ID != memberID {
		t.Fatalf("member summary id = %q, want %q", summary.ID, memberID)
	}
}
