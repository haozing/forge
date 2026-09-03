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
	ActionSiteManage                = "site.manage"
	ActionAgentApplicationUse       = "agent_application.use"
	ActionAgentApplicationManage    = "agent_application.manage"
	ActionAuditRead                 = "audit.read"
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
	ActionSiteRead, ActionSiteManage,
	ActionAgentApplicationUse, ActionAgentApplicationManage,
	ActionAuditRead,
}

// agentAllowedActions is the controlled subset an AgentAccessPolicy may grant
// through the authz Require path. Agents never approve PublicationRequests
// and never manage models, tags, sites or workspaces here: the one model
// write exception — draft-only model creation — deliberately bypasses this
// list via resourcemodel.AgentDraftService with its own workspace-scoped
// model.manage grant check (产品文档-v2 §8.3, 2026-09-03).
var agentAllowedActions = map[string]bool{
	ActionAssetRead:         true,
	ActionAssetWrite:        true,
	ActionAssetPublish:      true,
	ActionPublicationSubmit: true,
	ActionQueryExecute:      true,
	ActionProcessingRun:     true,
}

// AgentActionAllowed reports whether an AgentAccessPolicy may carry the action.
func AgentActionAllowed(action string) bool {
	return agentAllowedActions[action]
}
