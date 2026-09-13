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
		ActionPublicationApprove, ActionPublicationReject, ActionAuditRead,
		ActionSiteDesign, ActionSiteLifecycle} {
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
		ActionTagManage, ActionModelManage, ActionSiteLifecycle, ActionAgentApplicationManage} {
		if MemberAllowed(WorkspaceRoleEditor, action) {
			t.Fatalf("editor must be denied %s", action)
		}
	}
	if !MemberAllowed(WorkspaceRoleEditor, ActionAssetWrite) || !MemberAllowed(WorkspaceRoleEditor, ActionPublicationSubmit) {
		t.Fatal("editor must write assets and submit publication requests")
	}
	// G: editor 拆得 site.design（外观/页面/发布 release），拿不到 site.lifecycle。
	if !MemberAllowed(WorkspaceRoleEditor, ActionSiteDesign) {
		t.Fatal("editor must hold site.design")
	}
	if MemberAllowed(WorkspaceRoleEditor, ActionSiteLifecycle) {
		t.Fatal("editor must be denied site.lifecycle")
	}
}

func TestEditorCanCancelOnlyThroughOwnScopeConstant(t *testing.T) {
	// The static grant carries publication.cancel; narrowing to own requests
	// is a service-level rule asserted in review service tests.
	if !MemberAllowed(WorkspaceRoleEditor, ActionPublicationCancel) {
		t.Fatal("editor must hold the cancel action; service narrows to own requests")
	}
}

func TestHumanOnlyActions(t *testing.T) {
	// 统一方案 C/I/J：human_only 动作对 agent 永久关闭 —— 与角色、覆写、
	// 能力清单无关。
	humanOnly := []string{
		ActionAssetPublish,             // J: 发布必须人做
		ActionPublicationApprove,       // 审核决策必须是人
		ActionPublicationReject,        //
		ActionPublicationBatch,         //
		ActionSiteLifecycle,            // 站点生命周期
		ActionAgentApplicationManage,   //
		ActionAuditRead,                //
		ActionOrganizationManage,       //
		ActionOrganizationMemberManage, //
		ActionWorkspaceManage,          //
		ActionTagManage,                //
	}
	for _, action := range humanOnly {
		if !HumanOnlyAction(action) {
			t.Fatalf("%s must be marked human_only", action)
		}
		if AgentActionAllowed(action) {
			t.Fatalf("agent must never be allowed %s", action)
		}
	}
	// 起草与确认链必须对 agent 开放（J：agent 只能起草与确认）。
	for _, action := range []string{ActionAssetRead, ActionAssetWrite, ActionAssetConfirm, ActionPublicationSubmit, ActionQueryExecute} {
		if HumanOnlyAction(action) {
			t.Fatalf("%s must remain agent-grantable", action)
		}
	}
}

func TestEffectiveMemberActionsAgentOverride(t *testing.T) {
	// 覆写可以给 editor 加 site.design 之外的授权，但 human_only 永远过滤。
	effective := EffectiveMemberActions(WorkspaceRoleEditor,
		[]string{ActionSiteLifecycle}, nil, "agent")
	for _, action := range effective {
		if action == ActionSiteLifecycle {
			t.Fatal("agent must never receive a human_only action via override")
		}
	}
	human := EffectiveMemberActions(WorkspaceRoleViewer,
		[]string{ActionSiteDesign}, []string{ActionQueryExecute}, "member")
	found := false
	for _, action := range human {
		if action == ActionSiteDesign {
			found = true
		}
		if action == ActionQueryExecute {
			t.Fatal("revoked action must be removed for human members")
		}
	}
	if !found {
		t.Fatal("granted override must appear for human members")
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
