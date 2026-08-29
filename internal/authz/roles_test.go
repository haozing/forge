package authz

import (
	"testing"

	"agentchunzhi/internal/auth"
)

// Every action must have an explicit grant decision for every role: the
// matrix is closed and unknown roles deny everything.
func TestMemberRoleMatrixIsClosed(t *testing.T) {
	for _, role := range AllWorkspaceRoles {
		for _, action := range AllActions {
			_ = MemberAllowed(role, action) // must not panic on catalog actions
		}
		for _, legacy := range []string{ActionContainerManage, ActionConversationUse, ActionAutomationRead, ActionAttachmentRead, ActionStatsRead} {
			_ = MemberAllowed(role, legacy)
		}
	}
}

func TestUnknownRoleIsDeniedEverywhere(t *testing.T) {
	for _, role := range []string{"owner", "member", "", "superuser", "ADMIN", "editor "} {
		if ValidWorkspaceRole(role) {
			t.Fatalf("role %q must not be a valid workspace role", role)
		}
		if got := MemberRoleActions(role); len(got) != 0 {
			t.Fatalf("unknown role %q must grant nothing, got %v", role, got)
		}
		for _, action := range AllActions {
			if MemberAllowed(role, action) {
				t.Fatalf("unknown role %q must be denied %s", role, action)
			}
		}
	}
}

func TestViewerIsStrictlyReadOnly(t *testing.T) {
	if !MemberAllowed(WorkspaceRoleViewer, ActionWorkspaceRead) {
		t.Fatal("viewer must read workspace")
	}
	if !MemberAllowed(WorkspaceRoleViewer, ActionAssetRead) {
		t.Fatal("viewer must read assets")
	}
	for _, action := range []string{ActionAssetWrite, ActionAssetPublish, ActionAssetArchive, ActionAssetConfirm,
		ActionTagManage, ActionModelManage, ActionWorkspaceManage, ActionPublicationSubmit,
		ActionPublicationApprove, ActionPublicationReject, ActionAuditRead, ActionSiteManage} {
		if MemberAllowed(WorkspaceRoleViewer, action) {
			t.Fatalf("viewer must be denied %s", action)
		}
	}
}

func TestReviewerOnlyGetsPublicationDecisionActions(t *testing.T) {
	if !MemberAllowed(WorkspaceRoleReviewer, ActionPublicationApprove) ||
		!MemberAllowed(WorkspaceRoleReviewer, ActionPublicationReject) ||
		!MemberAllowed(WorkspaceRoleReviewer, ActionPublicationBatch) ||
		!MemberAllowed(WorkspaceRoleReviewer, ActionPublicationRead) ||
		!MemberAllowed(WorkspaceRoleReviewer, ActionPublicationComment) {
		t.Fatal("reviewer must hold the publication review actions")
	}
	for _, action := range []string{ActionAssetWrite, ActionAssetPublish, ActionAssetArchive, ActionAssetConfirm, ActionPublicationSubmit, ActionTagManage, ActionModelManage} {
		if MemberAllowed(WorkspaceRoleReviewer, action) {
			t.Fatalf("reviewer must be denied %s", action)
		}
	}
}

func TestEditorCannotApproveOrManage(t *testing.T) {
	for _, action := range []string{ActionPublicationApprove, ActionPublicationReject, ActionPublicationBatch,
		ActionTagManage, ActionModelManage, ActionSiteManage, ActionAgentApplicationManage} {
		if MemberAllowed(WorkspaceRoleEditor, action) {
			t.Fatalf("editor must be denied %s", action)
		}
	}
	if !MemberAllowed(WorkspaceRoleEditor, ActionAssetWrite) || !MemberAllowed(WorkspaceRoleEditor, ActionPublicationSubmit) {
		t.Fatal("editor must write assets and submit publication requests")
	}
}

func TestEditorCanCancelOnlyThroughOwnScopeConstant(t *testing.T) {
	// The static grant carries publication.cancel; narrowing to own requests
	// is a service-level rule asserted in review service tests.
	if !MemberAllowed(WorkspaceRoleEditor, ActionPublicationCancel) {
		t.Fatal("editor must hold the cancel action; service narrows to own requests")
	}
}

func TestAgentActionGate(t *testing.T) {
	for _, action := range []string{ActionAssetRead, ActionAssetWrite, ActionAssetPublish, ActionPublicationSubmit, ActionQueryExecute, ActionProcessingRun} {
		if !AgentActionAllowed(action) {
			t.Fatalf("agent policy must allow %s", action)
		}
	}
	for _, action := range AllActions {
		switch action {
		case ActionAssetRead, ActionAssetWrite, ActionAssetPublish, ActionPublicationSubmit, ActionQueryExecute, ActionProcessingRun:
		default:
			if AgentActionAllowed(action) {
				t.Fatalf("agent policy must never grant %s", action)
			}
		}
	}
}

func TestOrganizationRoleValues(t *testing.T) {
	if !ValidOrganizationRole(OrganizationRoleAdmin) || !ValidOrganizationRole(OrganizationRoleMember) {
		t.Fatal("organization roles must be admin/member only")
	}
	if ValidOrganizationRole(WorkspaceRoleViewer) || ValidOrganizationRole("owner") {
		t.Fatal("workspace-only or legacy roles must not be accepted as organization roles")
	}
}

func TestPrincipalTypeConstantsMatchAuth(t *testing.T) {
	if auth.UserTypeMember != "member" || auth.UserTypeAgent != "agent" {
		t.Fatal("principal subject kinds drifted from the database CHECK")
	}
}
