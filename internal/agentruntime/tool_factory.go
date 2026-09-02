package agentruntime

import (
	"context"
	"errors"
	"fmt"
	"strings"

	runtimetools "agentchunzhi/internal/agentruntime/tools"
	"agentchunzhi/internal/agenttask"
	assetservice "agentchunzhi/internal/asset"
	"agentchunzhi/internal/auth"
	"agentchunzhi/internal/authz"
	"agentchunzhi/internal/eventing"
	agentquery "agentchunzhi/internal/query"
	"agentchunzhi/internal/store"

	"github.com/cloudwego/eino/compose"
	"github.com/jackc/pgx/v5"
)

type DomainToolFactory struct {
	Store  *store.Store
	Events eventing.EventStore
	Query  agentquery.Service
	// Models resolves the run's pinned structured-output endpoint for the
	// site.style.suggest tool (nil disables the tool).
	Models *ModelRegistry
}

func (f DomainToolFactory) Build(ctx context.Context, scope ReActToolScope, rawPolicy map[string]any) (*runtimetools.Registry, runtimetools.Policy, error) {
	if f.Store == nil || f.Store.Pool == nil || strings.TrimSpace(scope.AgentUserID) == "" {
		return nil, runtimetools.Policy{}, errors.New("domain tool factory is not initialized")
	}
	principal := auth.Principal{OrganizationID: scope.OrganizationID, UserID: scope.AgentUserID, UserType: "agent"}
	scopeResolver := authz.ScopeResolver{Store: f.Store}
	allowed := func(ctx context.Context, action string) ([]string, error) {
		return scopeResolver.AllowedModelIDs(ctx, principal, action)
	}
	queryService := f.Query
	if queryService.Store == nil {
		queryService.Store = f.Store
	}
	assets := assetservice.Service{Store: f.Store, Events: &f.Events}
	tasks := agenttask.Service{Store: f.Store}
	idempotencyKey := func(name string, ctx context.Context) string {
		callID := compose.GetToolCallID(ctx)
		if strings.TrimSpace(callID) == "" {
			callID = "unknown"
		}
		return "react:" + scope.RunID + ":" + name + ":" + callID
	}
	handlers := runtimetools.BuiltinHandlers{
		SearchKnowledge: func(ctx context.Context, arguments map[string]any) (any, error) {
			models, err := allowed(ctx, "asset.read")
			if err != nil {
				return nil, err
			}
			return queryService.Query(ctx, principal, agentquery.QueryRequest{
				Mode: "hybrid", Query: stringValue(arguments["query"]), TopK: boundedInt(arguments["limit"], 10, 1, 50),
			}, models)
		},
		QueryAssets: func(ctx context.Context, arguments map[string]any) (any, error) {
			models, err := allowed(ctx, "asset.read")
			if err != nil {
				return nil, err
			}
			mode := "structured"
			if stringValue(arguments["query"]) != "" {
				mode = "hybrid"
			}
			return queryService.Query(ctx, principal, agentquery.QueryRequest{
				Mode: mode, Query: stringValue(arguments["query"]), TopK: boundedInt(arguments["limit"], 10, 1, 50),
			}, models)
		},
		GetAsset: func(ctx context.Context, arguments map[string]any) (any, error) {
			models, err := allowed(ctx, "asset.read")
			if err != nil {
				return nil, err
			}
			return queryService.Reference(ctx, principal, stringValue(arguments["asset_id"]), models)
		},
		GetSchema: func(ctx context.Context, arguments map[string]any) (any, error) {
			models, err := allowed(ctx, "asset.read")
			if err != nil {
				return nil, err
			}
			return f.getSchema(ctx, scope, stringValue(arguments["resource_model_id"]), models)
		},
		GetRelatedAssets: func(ctx context.Context, arguments map[string]any) (any, error) {
			models, err := allowed(ctx, "asset.read")
			if err != nil {
				return nil, err
			}
			return f.getRelatedAssets(ctx, scope, stringValue(arguments["asset_id"]), boundedInt(arguments["limit"], 10, 1, 50), models)
		},
		GetTaskStatus: func(ctx context.Context, arguments map[string]any) (any, error) {
			return tasks.Get(ctx, principal, stringValue(arguments["task_id"]))
		},
		CreateInternalAsset: func(ctx context.Context, arguments map[string]any) (any, error) {
			models, err := allowed(ctx, "asset.create")
			if err != nil {
				return nil, err
			}
			fields, _ := arguments["fields"].(map[string]any)
			return assets.Create(ctx, principal, models, idempotencyKey("create", ctx), assetservice.CreateInput{
				// Run scope pins the target workspace: builtin models are
				// organization-level (NULL workspace) and would otherwise be
				// rejected as invalid input.
				ResourceModelID: stringValue(arguments["resource_model_id"]),
				WorkspaceID:     scope.WorkspaceID,
				Fields:          fields,
			})
		},
		UpdateInternalAsset: func(ctx context.Context, arguments map[string]any) (any, error) {
			models, err := allowed(ctx, "asset.edit")
			if err != nil {
				return nil, err
			}
			input := assetservice.UpdateInput{}
			if value, ok := arguments["title"].(string); ok {
				input.Title = &value
			}
			if value, ok := arguments["markdown"].(string); ok {
				input.Markdown = &value
			}
			if value, ok := arguments["fields"].(map[string]any); ok {
				input.Fields = &value
			}
			return assets.Update(ctx, principal, models, idempotencyKey("update", ctx),
				stringValue(arguments["asset_id"]), stringValue(arguments["expected_version_id"]), input)
		},
		CreateRelation: func(ctx context.Context, arguments map[string]any) (any, error) {
			models, err := allowed(ctx, "asset.edit")
			if err != nil {
				return nil, err
			}
			return f.createRelation(ctx, scope, principal, models, arguments)
		},
		SubmitProcessingTask: func(ctx context.Context, arguments map[string]any) (any, error) {
			readable, err := allowed(ctx, "asset.read")
			if err != nil {
				return nil, err
			}
			editable, err := allowed(ctx, "asset.edit")
			if err != nil {
				return nil, err
			}
			return tasks.Create(ctx, principal, agenttask.CreateInput{
				AgentApplicationID: scope.AgentApplicationID, Operation: stringValue(arguments["operation"]),
				InputAssetIDs: []string{stringValue(arguments["asset_id"])}, IdempotencyKey: idempotencyKey("task", ctx),
			}, readable, editable)
		},
		PublishAsset: func(ctx context.Context, arguments map[string]any) (any, error) {
			models, err := allowed(ctx, "asset.publish")
			if err != nil {
				return nil, err
			}
			return assets.Publish(ctx, principal, models, stringValue(arguments["asset_id"]), stringValue(arguments["version_id"]))
		},
		ArchiveAsset: func(ctx context.Context, arguments map[string]any) (any, error) {
			models, err := allowed(ctx, "asset.archive")
			if err != nil {
				return nil, err
			}
			return assets.Archive(ctx, principal, models, stringValue(arguments["asset_id"]))
		},
	}
	if boolValue(rawPolicy["allow_attachment_text"]) {
		handlers.GetAttachmentText = func(ctx context.Context, arguments map[string]any) (any, error) {
			models, err := allowed(ctx, "asset.read")
			if err != nil {
				return nil, err
			}
			return f.getAttachmentText(ctx, scope, stringValue(arguments["attachment_id"]), models)
		}
	}
	if f.Models != nil {
		handlers.SiteStyleSuggest = func(ctx context.Context, arguments map[string]any) (any, error) {
			// Read-only tool (design doc §8.3): the site row is scoped to
			// the run's organization and workspace; no site.manage involved.
			identifier := strings.TrimSpace(stringValue(arguments["site_id"]))
			if identifier == "" {
				// Default to the workspace's single active site; ambiguous
				// workspaces must address the site explicitly (the error
				// lists the candidates so the model can retry, §8.3).
				rows, err := f.Store.Pool.Query(ctx, `
					SELECT slug FROM site.public_sites
					WHERE organization_id = $1::uuid AND workspace_id = $2::uuid AND status = 'active'
					ORDER BY created_at
				`, scope.OrganizationID, scope.WorkspaceID)
				if err != nil {
					return nil, err
				}
				var slugs []string
				for rows.Next() {
					var slug string
					if err := rows.Scan(&slug); err != nil {
						rows.Close()
						return nil, err
					}
					slugs = append(slugs, slug)
				}
				rows.Close()
				if len(slugs) == 1 {
					identifier = slugs[0]
				} else {
					return nil, fmt.Errorf("site_id is required (uuid or slug); workspace sites: %s", strings.Join(slugs, ", "))
				}
			}
			// Accept a uuid or a slug; resolve to the row id in-scope.
			var siteID string
			query := `SELECT id::text FROM site.public_sites
				WHERE organization_id = $1::uuid AND workspace_id = $2::uuid AND status = 'active' AND (`
			args := []any{scope.OrganizationID, scope.WorkspaceID}
			if len(identifier) == 36 {
				query += `id = $3::uuid OR slug = $3`
			} else {
				query += `slug = $3`
			}
			args = append(args, identifier)
			query += `)`
			if err := f.Store.Pool.QueryRow(ctx, query, args...).Scan(&siteID); err != nil {
				return nil, errors.New("site was not found in the run workspace")
			}
			return f.suggestStylePatches(ctx, scope, scope.OrganizationID, scope.WorkspaceID, siteID, stringValue(arguments["instruction"]))
		}
	}
	registry := runtimetools.NewRegistry()
	if err := runtimetools.RegisterBuiltins(registry, handlers); err != nil {
		return nil, runtimetools.Policy{}, err
	}
	policy := parseToolPolicy(rawPolicy)
	policy.Authorize = func(ctx context.Context, _ string, _ runtimetools.Risk, _ map[string]any) error {
		var active bool
		err := f.Store.Pool.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM integration.agent_applications
				WHERE id = $1::uuid AND organization_id = $2::uuid AND bound_agent_user_id = $3::uuid
				  AND runtime_mode = 'react' AND status = 'active'
			)
		`, scope.AgentApplicationID, scope.OrganizationID, scope.AgentUserID).Scan(&active)
		if err != nil {
			return err
		}
		if !active {
			return errors.New("agent application authorization was revoked")
		}
		return nil
	}
	return registry, policy, nil
}

func parseToolPolicy(raw map[string]any) runtimetools.Policy {
	policy := runtimetools.Policy{
		AllowedNames: namesMap(raw["allowed_tools"]),
		AllowedCapabilities: map[string]bool{
			"query.read": true, "asset.read": true, "schema.read": true, "attachment.read": true, "task.read": true,
		},
		AllowLowWrite: boolValue(raw["allow_low_write"]), AllowHighWrite: boolValue(raw["allow_high_write"]),
		MaxCalls:      boundedInt(raw["max_tool_calls"], 12, 1, 12),
		ApprovalRisks: map[runtimetools.Risk]bool{runtimetools.HighWrite: true},
	}
	if capabilities := namesMap(raw["allowed_capabilities"]); len(capabilities) > 0 {
		policy.AllowedCapabilities = capabilities
	}
	if boolValue(raw["approve_low_write"]) {
		policy.ApprovalRisks[runtimetools.LowWrite] = true
	}
	return policy
}

func (f DomainToolFactory) getSchema(ctx context.Context, scope ReActToolScope, modelID string, allowed []string) (map[string]any, error) {
	if !contains(allowed, modelID) {
		return nil, errors.New("resource model is not authorized")
	}
	var name, kind, versionID, checksum string
	var schema map[string]any
	err := f.Store.Pool.QueryRow(ctx, `
		SELECT rm.name, rm.content_kind, mv.id::text, mv.field_schema, mv.schema_checksum
		FROM model.resource_models rm JOIN model.resource_model_versions mv ON mv.id = rm.current_version_id
		WHERE rm.organization_id = $1::uuid AND rm.id = $2::uuid AND rm.workspace_id = $3::uuid
		  AND rm.status = 'active' AND mv.status = 'published'
	`, scope.OrganizationID, modelID, scope.WorkspaceID).Scan(&name, &kind, &versionID, &schema, &checksum)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errors.New("resource model was not found")
	}
	return map[string]any{"resource_model_id": modelID, "name": name, "content_kind": kind, "version_id": versionID, "field_schema": schema, "schema_checksum": checksum}, err
}

func (f DomainToolFactory) getRelatedAssets(ctx context.Context, scope ReActToolScope, assetID string, limit int, allowed []string) ([]map[string]any, error) {
	rows, err := f.Store.Pool.Query(ctx, `
		SELECT target.id::text, target.current_published_version_id::text, rel.relation_type
		FROM asset.asset_relations rel
		JOIN asset.asset_versions source_version ON source_version.id = rel.source_asset_version_id
		JOIN asset.assets source ON source.id = source_version.asset_id
		JOIN asset.asset_versions target_version ON target_version.id = rel.target_asset_version_id
		JOIN asset.assets target ON target.id = target_version.asset_id AND target.current_published_version_id = target_version.id
		WHERE rel.organization_id = $1::uuid AND source.id = $2::uuid
		  AND source.resource_model_id::text = ANY($3::text[])
		  AND target.resource_model_id::text = ANY($3::text[])
		ORDER BY rel.created_at DESC LIMIT $4
	`, scope.OrganizationID, assetID, allowed, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []map[string]any{}
	for rows.Next() {
		var relatedID, versionID, relationType string
		if err := rows.Scan(&relatedID, &versionID, &relationType); err != nil {
			return nil, err
		}
		result = append(result, map[string]any{"asset_id": relatedID, "asset_version_id": versionID, "relation_type": relationType})
	}
	return result, rows.Err()
}

func (f DomainToolFactory) getAttachmentText(ctx context.Context, scope ReActToolScope, attachmentID string, allowed []string) (map[string]any, error) {
	var text, checksum, language string
	err := f.Store.Pool.QueryRow(ctx, `
		SELECT LEFT(atx.text_content, 32000), atx.checksum, COALESCE(atx.language, '')
		FROM asset.attachments att
		JOIN asset.attachment_texts atx ON atx.attachment_id = att.id
		JOIN asset.asset_version_attachments lva ON lva.organization_id = att.organization_id AND lva.attachment_id = att.id
		JOIN asset.asset_versions av ON av.organization_id = lva.organization_id AND av.id = lva.asset_version_id
		JOIN asset.assets a ON a.organization_id = av.organization_id AND a.id = av.asset_id AND a.current_published_version_id = av.id
		WHERE att.organization_id = $1::uuid AND att.id = $2::uuid AND att.deleted_at IS NULL
		  AND att.status = 'clean' AND att.extraction_status = 'succeeded'
		  AND a.resource_model_id::text = ANY($3::text[])
	`, scope.OrganizationID, attachmentID, allowed).Scan(&text, &checksum, &language)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errors.New("attachment text was not found")
	}
	return map[string]any{"attachment_id": attachmentID, "text": text, "checksum": checksum, "language": language}, err
}

func (f DomainToolFactory) createRelation(ctx context.Context, scope ReActToolScope, principal auth.Principal, allowed []string, arguments map[string]any) (map[string]any, error) {
	relationType := stringValue(arguments["relation_type"])
	if !map[string]bool{"related_to": true, "references": true, "derived_from": true, "cites": true, "continues_from": true}[relationType] {
		return nil, errors.New("unsupported relation type")
	}
	var relationID string
	err := f.Store.Pool.QueryRow(ctx, `
		INSERT INTO asset.asset_relations
			(organization_id, source_asset_version_id, target_asset_version_id, relation_type, created_by)
		SELECT $1::uuid, source.current_working_version_id, target.current_working_version_id, $4, $5::uuid
		FROM asset.assets source, asset.assets target
		WHERE source.id = $2::uuid AND target.id = $3::uuid
		  AND source.organization_id = $1::uuid AND target.organization_id = $1::uuid
		  AND source.workspace_id = $6::uuid AND target.workspace_id = $6::uuid
		  AND source.resource_model_id::text = ANY($7::text[])
		  AND target.resource_model_id::text = ANY($7::text[])
		ON CONFLICT (source_asset_version_id, target_asset_version_id, relation_type)
		DO UPDATE SET relation_type = EXCLUDED.relation_type
		RETURNING id::text
	`, scope.OrganizationID, stringValue(arguments["source_asset_id"]), stringValue(arguments["target_asset_id"]),
		relationType, principal.UserID, scope.WorkspaceID, allowed).Scan(&relationID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errors.New("relation assets were not found or authorized")
	}
	return map[string]any{"relation_id": relationID, "relation_type": relationType}, err
}

func boundedInt(value any, fallback, minimum, maximum int) int {
	number, ok := value.(float64)
	if !ok {
		return fallback
	}
	result := int(number)
	if result < minimum || result > maximum {
		return fallback
	}
	return result
}

func boolValue(value any) bool {
	result, _ := value.(bool)
	return result
}

func namesMap(value any) map[string]bool {
	items, _ := value.([]any)
	result := make(map[string]bool, len(items))
	for _, item := range items {
		if name, ok := item.(string); ok && strings.TrimSpace(name) != "" {
			result[strings.TrimSpace(name)] = true
		}
	}
	return result
}

func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

var _ ReActToolFactory = DomainToolFactory{}
