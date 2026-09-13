package authz

// Phase 0 contract: every permission decision uses one of these action
// constants through WorkspacePolicy / OrganizationPolicy. Handlers, services
// and repositories must not compare role strings or invent ad-hoc action names.
const (
	ActionOrganizationRead          = "organization.read"
	ActionOrganizationManage        = "organization.manage"
	ActionOrganizationMemberRead    = "organization.member.read"
	ActionOrganizationMemberManage  = "organization.member.manage"
	ActionOrganizationInvitationMng = "organization.invitation.manage"
	ActionWorkspaceRead             = "workspace.read"
	ActionWorkspaceManage           = "workspace.manage"
	ActionWorkspaceCreate           = "workspace.create"
	ActionWorkspaceArchive          = "workspace.archive"
	ActionWorkspaceRestore          = "workspace.restore"
	ActionModelRead                 = "model.read"
	ActionModelManage               = "model.manage"
	ActionTagRead                   = "tag.read"
	ActionTagManage                 = "tag.manage"
	ActionAssetRead                 = "asset.read"
	ActionAssetWrite                = "asset.write"
	ActionAssetConfirm              = "asset.confirm"
	ActionAssetPublish              = "asset.publish"
	ActionAssetArchive              = "asset.archive"
	ActionProcessingRun             = "processing.run"
	ActionQueryExecute              = "query.execute"
	ActionPublicationSubmit         = "publication.submit"
	ActionPublicationRead           = "publication.read"
	ActionPublicationComment        = "publication.comment"
	ActionPublicationApprove        = "publication.approve"
	ActionPublicationReject         = "publication.reject"
	ActionPublicationCancel         = "publication.cancel"
	ActionPublicationBatch          = "publication.batch"
	ActionSiteRead                  = "site.read"
	// 成员与 Agent 权限统一方案 G：site.manage 拆分。
	// site.design = 站点外观/页面/导航/发布 release（editor 可授）；
	// site.lifecycle = 建站/停用/域名/删除（admin 专属）。
	ActionSiteDesign             = "site.design"
	ActionSiteLifecycle          = "site.lifecycle"
	ActionAgentApplicationUse    = "agent_application.use"
	ActionAgentApplicationManage = "agent_application.manage"
	ActionAuditRead              = "audit.read"
)

// AllActions is the closed catalog of actions. Policy implementations must
// deny unknown actions; the catalog exists so tests can enumerate the matrix.
var AllActions = []string{
	ActionOrganizationRead, ActionOrganizationManage,
	ActionOrganizationMemberRead, ActionOrganizationMemberManage,
	ActionOrganizationInvitationMng,
	ActionWorkspaceRead, ActionWorkspaceManage, ActionWorkspaceCreate,
	ActionWorkspaceArchive, ActionWorkspaceRestore,
	ActionModelRead, ActionModelManage,
	ActionTagRead, ActionTagManage,
	ActionAssetRead, ActionAssetWrite, ActionAssetConfirm,
	ActionAssetPublish, ActionAssetArchive,
	ActionProcessingRun,
	ActionQueryExecute,
	ActionPublicationSubmit, ActionPublicationRead, ActionPublicationComment,
	ActionPublicationApprove, ActionPublicationReject, ActionPublicationCancel,
	ActionPublicationBatch,
	ActionSiteRead, ActionSiteDesign, ActionSiteLifecycle,
	ActionAgentApplicationUse, ActionAgentApplicationManage,
	ActionAuditRead,
}

// humanOnlyActions marks actions an agent principal can never hold — through
// a role preset, a member-level override or a capability grant alike
// (成员与 Agent 权限统一方案 C/I/J)。This replaces the old closed
// agentAllowedActions allow-list: agents now inherit the same matrix as
// humans and the exceptions are explicit.
//
// Rationale per group:
//   - organization/workspace/tag/agent-application management and audit:
//     人事权与治理权（成员管理、配置、审计）。
//   - asset.publish (J): agent 只能起草与确认，发布必须人做 —— D16
//     「AI 起草 · 人工确认」叙事成立的前提。
//   - publication.approve/reject/batch: 审核必须是人的判断。
//   - site.lifecycle: 建站/停用/域名/删除是站点生命周期权。
var humanOnlyActions = map[string]bool{
	ActionOrganizationManage:        true,
	ActionOrganizationMemberManage:  true,
	ActionOrganizationInvitationMng: true,
	ActionWorkspaceManage:           true,
	ActionWorkspaceCreate:           true,
	ActionWorkspaceArchive:          true,
	ActionWorkspaceRestore:          true,
	ActionTagManage:                 true,
	ActionAssetPublish:              true,
	ActionPublicationApprove:        true,
	ActionPublicationReject:         true,
	ActionPublicationBatch:          true,
	ActionSiteLifecycle:             true,
	ActionAgentApplicationManage:    true,
	ActionAuditRead:                 true,
}

// HumanOnlyAction reports whether the action is reserved for human members.
// Agent principals are denied these regardless of role, overrides,
// capabilities or access policies.
func HumanOnlyAction(action string) bool {
	return humanOnlyActions[action]
}

// AgentActionAllowed reports whether an action is grantable to an agent
// principal. Kept as the single judgment entry: it is the negation of the
// human_only set (the old closed allow-list is gone — new non-human-only
// actions are agent-grantable by default).
func AgentActionAllowed(action string) bool {
	return !HumanOnlyAction(action)
}
