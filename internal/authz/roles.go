package authz

import "errors"

// Phase 0 v2 contract: workspace collaboration roles. There is no workspace
// owner and no workspace member role; Organization is the owner tier and
// identity.users.organization_role carries the organization-level identity.
const (
	WorkspaceRoleAdmin    = "admin"
	WorkspaceRoleEditor   = "editor"
	WorkspaceRoleReviewer = "reviewer"
	WorkspaceRoleViewer   = "viewer"
)

// Organization roles stored in identity.users.organization_role. Agents must
// never carry an organization role (database CHECK enforces NULL).
const (
	OrganizationRoleAdmin  = "admin"
	OrganizationRoleMember = "member"
)

var ErrUnknownWorkspaceRole = errors.New("unknown workspace role")

// AllWorkspaceRoles is the closed set of workspace roles. The schema CHECK and
// the authorization matrix must stay aligned with this slice.
var AllWorkspaceRoles = []string{
	WorkspaceRoleAdmin,
	WorkspaceRoleEditor,
	WorkspaceRoleReviewer,
	WorkspaceRoleViewer,
}

// ValidWorkspaceRole reports whether the value is one of the four v2 roles.
// Unknown values must be rejected, never downgraded to a read-only role.
func ValidWorkspaceRole(role string) bool {
	switch role {
	case WorkspaceRoleAdmin, WorkspaceRoleEditor, WorkspaceRoleReviewer, WorkspaceRoleViewer:
		return true
	default:
		return false
	}
}

// ValidOrganizationRole reports whether the value is a v2 organization role.
func ValidOrganizationRole(role string) bool {
	switch role {
	case OrganizationRoleAdmin, OrganizationRoleMember:
		return true
	default:
		return false
	}
}

// memberRoleActions is the single static grant matrix for workspace members.
// Every permission question must be answered through this table; handlers and
// services must not branch on role strings themselves.
var memberRoleActions = map[string]map[string]bool{
	WorkspaceRoleAdmin: {
		ActionWorkspaceRead:          true,
		ActionWorkspaceManage:        true,
		ActionModelRead:              true,
		ActionModelManage:            true,
		ActionTagRead:                true,
		ActionTagManage:              true,
		ActionAssetRead:              true,
		ActionAssetWrite:             true,
		ActionAssetConfirm:           true,
		ActionAssetPublish:           true,
		ActionAssetArchive:           true,
		ActionProcessingRun:          true,
		ActionQueryExecute:           true,
		ActionPublicationSubmit:      true,
		ActionPublicationRead:        true,
		ActionPublicationComment:     true,
		ActionPublicationApprove:     true,
		ActionPublicationReject:      true,
		ActionPublicationCancel:      true,
		ActionPublicationBatch:       true,
		ActionSiteRead:               true,
		ActionSiteManage:             true,
		ActionAgentApplicationUse:    true,
		ActionAgentApplicationManage: true,
		ActionAuditRead:              true,
	},
	WorkspaceRoleEditor: {
		ActionWorkspaceRead:       true,
		ActionModelRead:           true,
		ActionTagRead:             true,
		ActionAssetRead:           true,
		ActionAssetWrite:          true,
		ActionAssetConfirm:        true,
		ActionProcessingRun:       true,
		ActionQueryExecute:        true,
		ActionPublicationSubmit:   true,
		ActionPublicationRead:     true,
		ActionPublicationComment:  true,
		ActionPublicationCancel:   true, // only own requests; service narrows
		ActionAgentApplicationUse: true,
	},
	WorkspaceRoleReviewer: {
		ActionWorkspaceRead:       true,
		ActionModelRead:           true,
		ActionTagRead:             true,
		ActionAssetRead:           true,
		ActionQueryExecute:        true,
		ActionPublicationRead:     true,
		ActionPublicationComment:  true,
		ActionPublicationApprove:  true,
		ActionPublicationReject:   true,
		ActionPublicationBatch:    true,
		ActionAgentApplicationUse: true,
	},
	WorkspaceRoleViewer: {
		ActionWorkspaceRead:       true,
		ActionModelRead:           true,
		ActionTagRead:             true,
		ActionAssetRead:           true,
		ActionQueryExecute:        true,
		ActionAgentApplicationUse: true,
	},
}

// Agent action gate: see AgentActionAllowed in actions.go.

// Legacy-domain actions below are used only by routes scheduled for retirement
// in stages 4-6 (containers, conversations, automation, attachments, stats).
// They are granted through this same matrix so every role question still has a
// single source of truth; v2 domains never consult them.
const (
	ActionContainerManage = "container.manage"
	ActionConversationUse = "conversation.use"
	ActionAutomationRead  = "automation.read"
	ActionAutomationWrite = "automation.write"
	ActionAutomationRun   = "automation.run"
	ActionAttachmentRead  = "attachment.read"
	ActionAttachmentWrite = "attachment.write"
	ActionStatsRead       = "stats.read"
)

var legacyRoleActions = map[string]map[string]bool{
	WorkspaceRoleAdmin: {
		ActionContainerManage: true,
		ActionConversationUse: true,
		ActionAutomationRead:  true,
		ActionAutomationWrite: true,
		ActionAutomationRun:   true,
		ActionAttachmentRead:  true,
		ActionAttachmentWrite: true,
		ActionStatsRead:       true,
	},
	WorkspaceRoleEditor: {
		ActionContainerManage: true,
		ActionConversationUse: true,
		ActionAttachmentRead:  true,
		ActionAttachmentWrite: true,
		ActionStatsRead:       true,
	},
	WorkspaceRoleReviewer: {
		ActionConversationUse: true,
		ActionAttachmentRead:  true,
		ActionStatsRead:       true,
	},
	WorkspaceRoleViewer: {
		ActionConversationUse: true,
		ActionAttachmentRead:  true,
		ActionStatsRead:       true,
	},
}

// MemberRoleActions returns the static grant set for a workspace role.
// Unknown roles yield an empty set (deny by default).
func MemberRoleActions(role string) []string {
	grants := memberRoleActions[role]
	actions := make([]string, 0, len(grants)+len(legacyRoleActions[role]))
	for action, allowed := range grants {
		if allowed {
			actions = append(actions, action)
		}
	}
	for action, allowed := range legacyRoleActions[role] {
		if allowed {
			actions = append(actions, action)
		}
	}
	return actions
}

// MemberAllowed reports whether the workspace role grants the action.
func MemberAllowed(role, action string) bool {
	return memberRoleActions[role][action] || legacyRoleActions[role][action]
}
