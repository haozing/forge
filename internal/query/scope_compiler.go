package query

import (
	"context"
	"errors"
	"fmt"

	"agentchunzhi/internal/auth"
	"agentchunzhi/internal/authz"
	"agentchunzhi/internal/store"

	"github.com/jackc/pgx/v5"
)

// ScopeCompiler turns an authenticated principal into the current
// QueryAccessScope (doc §5). All SQL lives here and in the repositories; the
// repositories never re-derive permissions from roles or endpoints.
type ScopeCompiler struct {
	Store          *store.Store
	HashSecret     string
	OrganizationID string
}

func newCompiler(store *store.Store, hashSecret string) ScopeCompiler {
	return ScopeCompiler{Store: store, HashSecret: hashSecret}
}

// seal normalizes, stamps the policy revision and fingerprints the scope.
func (c ScopeCompiler) seal(ctx context.Context, scope QueryAccessScope) (QueryAccessScope, error) {
	revision, err := c.policyRevision(ctx, scope.OrganizationID)
	if err != nil {
		return QueryAccessScope{}, err
	}
	scope.WorkspaceIDs = normalizeScopeIDs(scope.WorkspaceIDs)
	scope.ResourceModelIDs = normalizeScopeIDs(scope.ResourceModelIDs)
	scope.AllowedVisibilities = normalizeScopeIDs(scope.AllowedVisibilities)
	scope.VersionScope = VersionScopePublished
	scope.PolicyRevision = revision
	scope.ScopeFingerprint = computeScopeFingerprint(scope, c.HashSecret)
	return scope, nil
}

// ForWorkspaceMember compiles a single-workspace member scope: explicit
// membership plus query.execute, every visibility inside the workspace and the
// models available to that workspace with channels.workspace enabled
// (doc §5.2).
func (c ScopeCompiler) ForWorkspaceMember(ctx context.Context, principal auth.Principal, workspaceID string) (QueryAccessScope, error) {
	if c.Store == nil || c.Store.Pool == nil {
		return QueryAccessScope{}, errors.New("database store is not initialized")
	}
	if !ValidUUID(workspaceID) {
		return QueryAccessScope{}, ErrQueryScopeForbidden
	}
	var role string
	err := c.Store.Pool.QueryRow(ctx, `
		SELECT wm.role
		FROM content.workspace_members wm
		JOIN content.workspaces w ON w.organization_id = wm.organization_id
		  AND w.id = wm.workspace_id AND w.status = 'active'
		JOIN identity.users u ON u.id = wm.user_id AND u.user_type = 'member'
		  AND u.status = 'active'
		JOIN organization.organizations o ON o.id = u.organization_id AND o.status = 'active'
		WHERE wm.organization_id = $1::uuid AND wm.workspace_id = $2::uuid
		  AND wm.user_id = $3::uuid
	`, principal.OrganizationID, workspaceID, principal.UserID).Scan(&role)
	if errors.Is(err, pgx.ErrNoRows) {
		return QueryAccessScope{}, ErrQueryScopeForbidden
	}
	if err != nil {
		return QueryAccessScope{}, fmt.Errorf("load workspace membership: %w", err)
	}
	if !containsString(authz.MemberRoleActions(role), authz.ActionQueryExecute) {
		return QueryAccessScope{}, ErrQueryScopeForbidden
	}
	models, err := c.workspaceModels(ctx, principal.OrganizationID, workspaceID, ChannelWorkspace)
	if err != nil {
		return QueryAccessScope{}, err
	}
	scope := QueryAccessScope{
		OrganizationID:      principal.OrganizationID,
		SubjectKind:         SubjectMember,
		SubjectID:           principal.UserID,
		Channel:             ChannelWorkspace,
		WorkspaceIDs:        []string{workspaceID},
		ResourceModelIDs:    models,
		AllowedVisibilities: publishedVisibilities,
	}
	// Organization admins without a membership never reach this point: the
	// membership row above is mandatory (doc §5.2 rule 6).
	return c.seal(ctx, scope)
}

// ForOrganizationMember compiles the cross-workspace published scope: any
// active member may query the organization/public band without borrowing a
// workspace role (doc §5.3).
func (c ScopeCompiler) ForOrganizationMember(ctx context.Context, principal auth.Principal) (QueryAccessScope, error) {
	if c.Store == nil || c.Store.Pool == nil {
		return QueryAccessScope{}, errors.New("database store is not initialized")
	}
	var member bool
	err := c.Store.Pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM identity.users u
			JOIN organization.organizations o ON o.id = u.organization_id AND o.status = 'active'
			WHERE u.id = $2::uuid AND u.organization_id = $1::uuid
			  AND u.user_type = 'member' AND u.status = 'active'
		)
	`, principal.OrganizationID, principal.UserID).Scan(&member)
	if err != nil {
		return QueryAccessScope{}, fmt.Errorf("load organization membership: %w", err)
	}
	if !member {
		return QueryAccessScope{}, ErrQueryScopeForbidden
	}
	workspaces, err := c.organizationWorkspaces(ctx, principal.OrganizationID)
	if err != nil {
		return QueryAccessScope{}, err
	}
	models, err := c.organizationModels(ctx, principal.OrganizationID, ChannelWorkspace)
	if err != nil {
		return QueryAccessScope{}, err
	}
	scope := QueryAccessScope{
		OrganizationID:      principal.OrganizationID,
		SubjectKind:         SubjectMember,
		SubjectID:           principal.UserID,
		Channel:             ChannelWorkspace,
		WorkspaceIDs:        workspaces,
		ResourceModelIDs:    models,
		AllowedVisibilities: organizationVisibilities,
	}
	return c.seal(ctx, scope)
}

// ForAgent intersects the agent identity with its access policies:
// AgentAccessPolicy.workspaces ∩ resource_models ∩ actions contains
// query.execute (doc §5.4). The channel and retrieval-mode policies narrow
// further inside the planner and final authorizer.
func (c ScopeCompiler) ForAgent(ctx context.Context, principal auth.Principal, requestedModelIDs []string) (QueryAccessScope, error) {
	if c.Store == nil || c.Store.Pool == nil {
		return QueryAccessScope{}, errors.New("database store is not initialized")
	}
	if principal.UserType != auth.UserTypeAgent {
		return QueryAccessScope{}, ErrQueryScopeForbidden
	}
	rows, err := c.Store.Pool.Query(ctx, `
		SELECT DISTINCT COALESCE(ap.workspace_id::text, ''), ap.resource_model_id::text
		FROM content.agent_access_policies ap
		WHERE ap.organization_id = $1::uuid AND ap.agent_user_id = $2::uuid
		  AND 'query.execute' = ANY(ap.actions)
	`, principal.OrganizationID, principal.UserID)
	if err != nil {
		return QueryAccessScope{}, fmt.Errorf("load agent access policies: %w", err)
	}
	defer rows.Close()
	orgWide := false
	workspaces := []string{}
	modelSet := map[string]bool{}
	for rows.Next() {
		var workspaceID, modelID string
		if err := rows.Scan(&workspaceID, &modelID); err != nil {
			return QueryAccessScope{}, err
		}
		if workspaceID == "" {
			orgWide = true
		} else {
			workspaces = append(workspaces, workspaceID)
		}
		modelSet[modelID] = true
	}
	if err := rows.Err(); err != nil {
		return QueryAccessScope{}, err
	}
	if orgWide {
		workspaces, err = c.organizationWorkspaces(ctx, principal.OrganizationID)
		if err != nil {
			return QueryAccessScope{}, err
		}
	}
	models := make([]string, 0, len(modelSet))
	for modelID := range modelSet {
		models = append(models, modelID)
	}
	if len(requestedModelIDs) > 0 {
		requested := make(map[string]bool, len(requestedModelIDs))
		for _, modelID := range requestedModelIDs {
			requested[modelID] = true
		}
		narrowed := models[:0]
		for _, modelID := range models {
			if requested[modelID] {
				narrowed = append(narrowed, modelID)
			}
		}
		models = narrowed
	}
	scope := QueryAccessScope{
		OrganizationID:      principal.OrganizationID,
		SubjectKind:         SubjectAgent,
		SubjectID:           principal.UserID,
		Channel:             ChannelAgent,
		WorkspaceIDs:        workspaces,
		ResourceModelIDs:    models,
		AllowedVisibilities: organizationVisibilities,
	}
	return c.seal(ctx, scope)
}

// ForOpenAPI compiles the technical API key scope: channels.open_api enabled
// models across the organization, and the key must carry query.execute
// (doc §5.4/§11.2).
func (c ScopeCompiler) ForOpenAPI(ctx context.Context, principal auth.Principal) (QueryAccessScope, error) {
	if c.Store == nil || c.Store.Pool == nil {
		return QueryAccessScope{}, errors.New("database store is not initialized")
	}
	if principal.UserType != auth.UserTypeAgent || !principal.HasCapability(authz.ActionQueryExecute) {
		return QueryAccessScope{}, ErrQueryScopeForbidden
	}
	workspaces, err := c.organizationWorkspaces(ctx, principal.OrganizationID)
	if err != nil {
		return QueryAccessScope{}, err
	}
	models, err := c.organizationModels(ctx, principal.OrganizationID, ChannelOpenAPI)
	if err != nil {
		return QueryAccessScope{}, err
	}
	scope := QueryAccessScope{
		OrganizationID:      principal.OrganizationID,
		SubjectKind:         SubjectAgent,
		SubjectID:           principal.UserID,
		Channel:             ChannelOpenAPI,
		WorkspaceIDs:        workspaces,
		ResourceModelIDs:    models,
		AllowedVisibilities: organizationVisibilities,
	}
	return c.seal(ctx, scope)
}

// ForMemberCompat compiles the phase-3-compatible scope used by the in-process
// agent runtime until phase 4 rewires it onto ForAgent: the caller supplies an
// already-resolved model allowlist and the member reaches every workspace with
// an explicit membership.
func (c ScopeCompiler) ForMemberCompat(ctx context.Context, principal auth.Principal, allowedModelIDs []string) (QueryAccessScope, error) {
	if c.Store == nil || c.Store.Pool == nil {
		return QueryAccessScope{}, errors.New("database store is not initialized")
	}
	rows, err := c.Store.Pool.Query(ctx, `
		SELECT wm.workspace_id::text
		FROM content.workspace_members wm
		JOIN content.workspaces w ON w.organization_id = wm.organization_id
		  AND w.id = wm.workspace_id AND w.status = 'active'
		JOIN identity.users u ON u.id = wm.user_id AND u.user_type = 'member' AND u.status = 'active'
		WHERE wm.organization_id = $1::uuid AND wm.user_id = $2::uuid
	`, principal.OrganizationID, principal.UserID)
	if err != nil {
		return QueryAccessScope{}, fmt.Errorf("load member workspaces: %w", err)
	}
	defer rows.Close()
	workspaces := []string{}
	for rows.Next() {
		var workspaceID string
		if err := rows.Scan(&workspaceID); err != nil {
			return QueryAccessScope{}, err
		}
		workspaces = append(workspaces, workspaceID)
	}
	if err := rows.Err(); err != nil {
		return QueryAccessScope{}, err
	}
	scope := QueryAccessScope{
		OrganizationID:      principal.OrganizationID,
		SubjectKind:         SubjectMember,
		SubjectID:           principal.UserID,
		Channel:             ChannelWorkspace,
		WorkspaceIDs:        workspaces,
		ResourceModelIDs:    allowedModelIDs,
		AllowedVisibilities: publishedVisibilities,
	}
	return c.seal(ctx, scope)
}

// organizationWorkspaces lists every active workspace of the organization.
func (c ScopeCompiler) organizationWorkspaces(ctx context.Context, organizationID string) ([]string, error) {
	rows, err := c.Store.Pool.Query(ctx, `
		SELECT id::text FROM content.workspaces
		WHERE organization_id = $1::uuid AND status = 'active'
	`, organizationID)
	if err != nil {
		return nil, fmt.Errorf("list organization workspaces: %w", err)
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// workspaceModels lists the models usable inside one workspace (workspace
// bound or builtin) whose current published version enables the channel.
func (c ScopeCompiler) workspaceModels(ctx context.Context, organizationID, workspaceID string, channel QueryChannel) ([]string, error) {
	rows, _ := c.Store.Pool.Query(ctx, `
		SELECT rm.id::text
		FROM model.resource_models rm
		JOIN model.resource_model_versions v
		  ON v.organization_id = rm.organization_id AND v.id = rm.current_version_id
		WHERE rm.organization_id = $1::uuid AND rm.status = 'active'
		  AND (rm.workspace_id = $2::uuid OR rm.workspace_id IS NULL)
		  AND COALESCE(NULLIF(v.policy #>> ('{channels,'||$3||',enabled}')::text, '')::boolean, false)
	`, organizationID, workspaceID, string(channel))
	return collectModelIDs(rows, "list workspace models")
}

// organizationModels lists the organization-wide models whose current
// published version enables the channel.
func (c ScopeCompiler) organizationModels(ctx context.Context, organizationID string, channel QueryChannel) ([]string, error) {
	rows, _ := c.Store.Pool.Query(ctx, `
		SELECT rm.id::text
		FROM model.resource_models rm
		JOIN model.resource_model_versions v
		  ON v.organization_id = rm.organization_id AND v.id = rm.current_version_id
		WHERE rm.organization_id = $1::uuid AND rm.status = 'active'
		  AND COALESCE(NULLIF(v.policy #>> ('{channels,'||$2||',enabled}')::text, '')::boolean, false)
	`, organizationID, string(channel))
	return collectModelIDs(rows, "list organization models")
}

func collectModelIDs(rows pgx.Rows, context string) ([]string, error) {
	if rows == nil {
		return nil, errors.New("database store is not initialized")
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("%s: %w", context, err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("%s: %w", context, err)
	}
	return ids, nil
}

// policyRevision reads the authorization policy revision used for the session
// snapshot (doc §10.8).
func (c ScopeCompiler) policyRevision(ctx context.Context, organizationID string) (int64, error) {
	var revision int64
	err := c.Store.Pool.QueryRow(ctx, `
		SELECT COALESCE((SELECT revision FROM "authorization".policy_revisions
		                 WHERE organization_id = $1::uuid), 1)
	`, organizationID).Scan(&revision)
	if err != nil {
		return 0, fmt.Errorf("load policy revision: %w", err)
	}
	return revision, nil
}
