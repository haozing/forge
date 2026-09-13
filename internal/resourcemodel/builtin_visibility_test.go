package resourcemodel

// builtin_visibility_test.go — builtin models are organization-level
// (workspace_id NULL). Treating that NULL as "not found" made every
// single-model/version read of the four seeded models fail with 404/500 on
// a real deployment; these tests pin the org-level contract at the SQL
// level of the service queries.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func readService(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(".", "service.go"))
	if err != nil {
		t.Fatalf("read service.go: %v", err)
	}
	return string(raw)
}

// fnBody extracts the source of one method from the service file.
func fnBody(src, signature string) string {
	idx := strings.Index(src, signature)
	if idx < 0 {
		return ""
	}
	body := src[idx:]
	if end := strings.Index(body, "\n}\n"); end > 0 {
		return body[:end]
	}
	return body
}

// Get must COALESCE workspace_id so a NULL (builtin) row scans into the
// non-pointer string field instead of failing.
func TestGetCoalescesNullWorkspace(t *testing.T) {
	fn := fnBody(readService(t), "func (s Service) Get(")
	if !strings.Contains(fn, "COALESCE(rm.workspace_id::text, '')") {
		t.Error("Get must COALESCE the nullable workspace_id or builtin models fail to scan")
	}
	if !strings.Contains(fn, "NULLIF($3, '')::uuid") {
		t.Error("Get must guard the workspace_id uuid cast against the empty string")
	}
}

// Get and GetVersion must let an organization member read a builtin model
// whose workspace_id is empty, instead of collapsing it into ErrNotFound.
func TestOrgLevelModelsAreReadableWithoutWorkspace(t *testing.T) {
	src := readService(t)

	if !strings.Contains(fnBody(src, "func (s Service) Get("), "organizationScoped") {
		t.Error("Get must branch on the organization-level (empty workspace) case")
	}
	if !strings.Contains(fnBody(src, "func (s Service) GetVersion("), `if workspaceID != ""`) {
		t.Error("GetVersion must only run the workspace policy when a workspace exists")
	}
}

// Every model-scoped management method must route through requireModelAction:
// calling the workspace-scoped s.require directly on model.WorkspaceID turns
// builtin (NULL workspace) models into ErrWorkspaceNotFound for everyone
// including organization admins — the 2026-09-10 audit caught exactly that on
// Versions/CreateVersion/Validate/Publish/Retire.
func TestVersionLifecycleRoutesThroughOrgAwareAuth(t *testing.T) {
	src := readService(t)
	for _, signature := range []string{
		"func (s Service) Patch(",
		"func (s Service) Versions(",
		"func (s Service) CreateVersion(",
		"func (s Service) PatchVersion(",
		"func (s Service) ValidateVersion(",
		"func (s Service) PublishVersion(",
		"func (s Service) RetireVersion(",
	} {
		body := fnBody(src, signature)
		if body == "" {
			t.Errorf("%s not found in service.go", signature)
			continue
		}
		if !strings.Contains(body, "requireModelAction") {
			t.Errorf("%s must authorize through requireModelAction (builtin models have no workspace)", signature)
		}
		if strings.Contains(body, "s.require(ctx, principal, model.WorkspaceID") {
			t.Errorf("%s still calls the workspace-scoped require directly", signature)
		}
	}
}

// C4: 白名单下沉后，公开视图写入口 SetModelPublicView 也必须走
// requireModelAction（builtin 模型无 workspace，与版本生命周期同规则）。
func TestModelPublicViewRoutesThroughOrgAwareAuth(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(".", "publicview.go"))
	if err != nil {
		t.Fatalf("read publicview.go: %v", err)
	}
	src := string(raw)
	for _, signature := range []string{
		"func (s Service) SetModelPublicView(",
		"func (s Service) GetModelPublicView(",
	} {
		body := fnBody(src, signature)
		if body == "" {
			t.Errorf("%s not found in publicview.go", signature)
			continue
		}
		if !strings.Contains(body, "requireModelAction") {
			t.Errorf("%s must authorize through requireModelAction (builtin models have no workspace)", signature)
		}
	}
}

