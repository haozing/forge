package agentruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"


	runtimetools "agentchunzhi/internal/agentruntime/tools"
	"agentchunzhi/internal/agenttask"
	assetservice "agentchunzhi/internal/asset"
	"agentchunzhi/internal/auth"
	"agentchunzhi/internal/authz"
	"agentchunzhi/internal/content"
	"agentchunzhi/internal/eventing"
	agentquery "agentchunzhi/internal/query"
	"agentchunzhi/internal/delivery"
	"agentchunzhi/internal/site"
	"agentchunzhi/internal/resourcemodel"
	"agentchunzhi/internal/review"
	"agentchunzhi/internal/store"

	"github.com/cloudwego/eino/compose"
	"github.com/jackc/pgx/v5"
)

type DomainToolFactory struct {
	Store  *store.Store
	Events eventing.EventStore
	Query  agentquery.Service
	// Models resolves the run's pinned structured-output endpoint for the
	// suggest_display_path tool (nil disables the tool).
	Models *ModelRegistry
	// Sites carries the public-site domain surface for the §7.2 theme design
	// tools (nil disables the whole set). Delivery backs render_html.
	Sites    *site.Service
	Delivery *delivery.Service
	// Reviews carries the scheduled-publish registration for the
	// publish_asset tool's scheduled_at parameter (nil disables deferral).
	Reviews *review.Service
	// Contents carries the content-pattern domain surface for the G8 tools
	// (nil disables the pattern tools).
	Contents *content.Service
}

func (f DomainToolFactory) Build(ctx context.Context, scope ReActToolScope, rawPolicy map[string]any) (*runtimetools.Registry, runtimetools.Policy, error) {
	if f.Store == nil || f.Store.Pool == nil || strings.TrimSpace(scope.AgentUserID) == "" {
		return nil, runtimetools.Policy{}, errors.New("domain tool factory is not initialized")
	}
	principal := auth.Principal{OrganizationID: scope.OrganizationID, UserID: scope.AgentUserID, UserType: "agent"}
	// 能力清单（key/应用）是判权的附加授予源（authz A/B）：runs 通道的
	// principal 不经过 api key 解析，这里从应用行补载 Capabilities，
	// 否则站点域等按 Require 能力门判权的域会全部被拒。
	var appCaps []string
	if err := f.Store.Pool.QueryRow(ctx, `
		SELECT capabilities FROM integration.agent_applications
		WHERE id = $1::uuid AND organization_id = $2::uuid
	`, scope.AgentApplicationID, scope.OrganizationID).Scan(&appCaps); err == nil {
		principal.Capabilities = appCaps
	}
	scopeResolver := authz.ScopeResolver{Store: f.Store}
	// 决策 D（防提权）：工具动作必须同时落在发起人与 agent 两套权限的交集内
	// —— viewer 发起的会话里，editor agent 也只按 viewer 干活。任一侧无
	// 成员行时下限取 viewer（最保守）。
	floorRole := f.effectiveFloorRole(ctx, scope)
	allowed := func(ctx context.Context, action string) ([]string, error) {
		if authz.ValidAction(action) && !authz.MemberAllowed(floorRole, action) {
			return nil, fmt.Errorf("action %s 超出发起人在该工作区的权限，已被拒绝", action)
		}
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
		SuggestResourceModel: func(ctx context.Context, arguments map[string]any) (any, error) {
			return f.suggestResourceModel(ctx, scope, stringValue(arguments["intent"]), stringListValue(arguments["samples"]), stringValue(arguments["extend_model_id"]))
		},
		CreateResourceModel: func(ctx context.Context, arguments map[string]any) (any, error) {
			fieldSchema, _ := arguments["field_schema"].(map[string]any)
			if fieldSchema == nil {
				return nil, errors.New("field_schema is required")
			}
			form, list, policy := defaultModelSchemas(fieldSchema)
			if value, ok := arguments["form_schema"].(map[string]any); ok {
				form = value
			}
			if value, ok := arguments["list_schema"].(map[string]any); ok {
				list = value
			}
			if value, ok := arguments["policy"].(map[string]any); ok {
				policy = value
			}
			model, err := (resourcemodel.AgentDraftService{Store: f.Store}).AgentCreateDraft(ctx, principal, scope.WorkspaceID, resourcemodel.CreateInput{
				ModelKey: stringValue(arguments["model_key"]), Name: stringValue(arguments["name"]),
				Description: stringValue(arguments["description"]), ContentKind: stringValue(arguments["content_kind"]),
				InitialVersion: resourcemodel.InitialVersion{FieldSchema: fieldSchema, FormSchema: form, ListSchema: list, Policy: policy},
			})
			if err != nil {
				return nil, modelDraftToolError(err)
			}
			return model, nil
		},
		UpdateModelDraft: func(ctx context.Context, arguments map[string]any) (any, error) {
			input := resourcemodel.VersionPatchInput{}
			if value, ok := arguments["field_schema"].(map[string]any); ok {
				input.FieldSchema = &value
			}
			if value, ok := arguments["form_schema"].(map[string]any); ok {
				input.FormSchema = &value
			}
			if value, ok := arguments["list_schema"].(map[string]any); ok {
				input.ListSchema = &value
			}
			if value, ok := arguments["policy"].(map[string]any); ok {
				input.Policy = &value
			}
			version, err := (resourcemodel.AgentDraftService{Store: f.Store}).AgentPatchDraftVersion(ctx, principal, scope.WorkspaceID,
				stringValue(arguments["version_id"]), stringValue(arguments["expected_schema_checksum"]), input)
			if err != nil {
				return nil, modelDraftToolError(err)
			}
			return version, nil
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
				TagIDs:          stringListValue(arguments["tag_ids"]),
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
			if items, ok := arguments["tag_ids"].([]any); ok {
				ids := value2stringList(items)
				input.TagIDs = &ids
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
			// A future scheduled_at defers the publish (G4): the intent lands
			// as a scheduled publication request the worker executes at the
			// due moment — same approval flow as the immediate path.
			if raw := stringValue(arguments["scheduled_at"]); raw != "" {
				moment, err := time.Parse(time.RFC3339, strings.TrimSpace(raw))
				if err != nil {
					return nil, errors.New("scheduled_at must be an RFC3339 timestamp")
				}
				if f.Reviews == nil {
					return nil, errors.New("scheduled publishing is unavailable")
				}
				return f.Reviews.ScheduleDirect(ctx, principal, stringValue(arguments["asset_id"]), moment)
			}
			models, err := allowed(ctx, "asset.publish")
			if err != nil {
				return nil, err
			}
			return assets.Publish(ctx, principal, models, stringValue(arguments["asset_id"]), stringValue(arguments["version_id"]), "")
		},
		ArchiveAsset: func(ctx context.Context, arguments map[string]any) (any, error) {
			models, err := allowed(ctx, "asset.archive")
			if err != nil {
				return nil, err
			}
			return assets.Archive(ctx, principal, models, stringValue(arguments["asset_id"]))
		},
		// G3/G6 content-quality tools: the read-only trio ships whenever the
		// structured endpoint registry exists; cover alt additionally needs
		// the asset.write capability gate.
		PublishChecklist: func(ctx context.Context, arguments map[string]any) (any, error) {
			if _, err := allowed(ctx, "asset.read"); err != nil {
				return nil, err
			}
			return f.publishChecklist(ctx, scope, stringValue(arguments["asset_id"]), boolValue(arguments["check_links"]))
		},
		ListStaleAssets: func(ctx context.Context, arguments map[string]any) (any, error) {
			if _, err := allowed(ctx, "asset.read"); err != nil {
				return nil, err
			}
			days, _ := arguments["days"].(float64)
			return f.listStaleAssets(ctx, scope, int64(days))
		},
		SuggestCoverAlt: func(ctx context.Context, arguments map[string]any) (any, error) {
			if _, err := allowed(ctx, "asset.write"); err != nil {
				return nil, err
			}
			return f.suggestCoverAlt(ctx, scope, principal.UserID, stringValue(arguments["asset_id"]), stringValue(arguments["alt_text"]))
		},
		SuggestNoteImage: func(ctx context.Context, arguments map[string]any) (any, error) {
			if _, err := allowed(ctx, "attachment.read"); err != nil {
				return nil, err
			}
			return f.suggestNoteImage(ctx, scope, stringValue(arguments["attachment_id"]), stringValue(arguments["alt"]), stringValue(arguments["caption"]))
		},
	}
	if f.Contents != nil {
		handlers.SaveContentPattern = func(ctx context.Context, arguments map[string]any) (any, error) {
			blocks := []content.PatternBlock{}
			if raw, ok := arguments["blocks"].([]any); ok && len(raw) > 0 {
				encoded, err := json.Marshal(raw)
				if err != nil {
					return nil, errors.New("blocks must be a list of {kind, content} objects")
				}
				if err := json.Unmarshal(encoded, &blocks); err != nil {
					return nil, errors.New("blocks must be a list of {kind, content} objects")
				}
			}
			return f.Contents.CreatePattern(ctx, principal, stringValue(arguments["name"]), stringValue(arguments["description"]), blocks, stringValue(arguments["from_asset_id"]))
		}
		handlers.ListContentPatterns = func(ctx context.Context, arguments map[string]any) (any, error) {
			limit, _ := arguments["limit"].(float64)
			return f.Contents.ListPatterns(ctx, principal, int(limit))
		}
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
		handlers.SuggestDisplayPath = func(ctx context.Context, arguments map[string]any) (any, error) {
			return f.suggestDisplayPath(ctx, scope, stringValue(arguments["title"]), stringValue(arguments["asset_id"]))
		}
	}
	// §7.2 主题设计工具集（Sites 未接线时整体跳过）。
	f.applySiteThemeTools(&handlers, scope, principal, allowed)
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

// agentVisibilityBand resolves the agent's data_scope policy row into the
// asset visibility band its read tools may touch. The tool SQL used to read
// published relations/attachment text without any visibility narrowing, so a
// workspace-visible asset leaked into a public-scope agent's context; missing
// policy rows fail closed to public-only.
// effectiveFloorRole resolves the weaker of the initiator's and the agent's
// workspace roles (决策 D). Missing memberships degrade to viewer — the most
// conservative floor — so an unseeded principal can never widen the run.
func (f DomainToolFactory) effectiveFloorRole(ctx context.Context, scope ReActToolScope) string {
	rank := map[string]int{
		authz.WorkspaceRoleViewer:   0,
		authz.WorkspaceRoleReviewer: 1,
		authz.WorkspaceRoleEditor:   2,
		authz.WorkspaceRoleAdmin:    3,
	}
	role := func(userType, userID string) string {
		if f.Store == nil || f.Store.Pool == nil {
			return authz.WorkspaceRoleViewer
		}
		var role string
		err := f.Store.Pool.QueryRow(ctx, `
			SELECT wm.role FROM content.workspace_members wm
			JOIN content.workspaces w ON w.organization_id = wm.organization_id AND w.id = wm.workspace_id
			WHERE wm.organization_id = $1::uuid AND wm.workspace_id = $2::uuid
			  AND wm.user_id = $3::uuid AND wm.principal_type = $4 AND w.status = 'active'
		`, scope.OrganizationID, scope.WorkspaceID, userID, userType).Scan(&role)
		if err != nil || !authz.ValidWorkspaceRole(role) {
			return authz.WorkspaceRoleViewer
		}
		return role
	}
	agentRole := role("agent", scope.AgentUserID)
	// principal_type 的词表是 human|agent（0031 CHECK）；发起人是 human。
	// 曾经误写 'member' 导致永远查不到发起人成员行、floor 恒降级 viewer，
	// 全部写能力工具（建模/资产/站点设计）被拒。
	initiatorRole := role("human", scope.PrincipalID)
	if rank[agentRole] <= rank[initiatorRole] {
		return agentRole
	}
	return initiatorRole
}

func (f DomainToolFactory) agentVisibilityBand(ctx context.Context, scope ReActToolScope) []string {
	var dataScope string
	_ = f.Store.Pool.QueryRow(ctx, "SELECT ap.data_scope FROM content.agent_access_policies ap WHERE ap.agent_user_id = $1::uuid AND (ap.workspace_id = $2::uuid OR ap.workspace_id IS NULL) ORDER BY ap.workspace_id NULLS LAST LIMIT 1", scope.AgentUserID, scope.WorkspaceID).Scan(&dataScope)
	switch dataScope {
	case "workspace":
		return []string{"public", "organization", "workspace"}
	case "organization":
		return []string{"public", "organization"}
	default:
		return []string{"public"}
	}
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
		  AND target.visibility = ANY($5::text[])
		ORDER BY rel.created_at DESC LIMIT $4
	`, scope.OrganizationID, assetID, allowed, limit, f.agentVisibilityBand(ctx, scope))
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
		  AND a.visibility = ANY($4::text[])
	`, scope.OrganizationID, attachmentID, allowed, f.agentVisibilityBand(ctx, scope)).Scan(&text, &checksum, &language)
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
	// AI-proposed relations park on the source asset's draft (source='ai'):
	// sealed versions reject direct asset_relations inserts (sealed guard),
	// and the draft commit transaction is the only materialization path.
	// The revision bump keeps the draft dirty so the parked edge cannot be
	// short-circuited by a clean-draft commit.
	commandTag, err := f.Store.Pool.Exec(ctx, `
		WITH parked AS (
			INSERT INTO asset.asset_draft_relations
				(organization_id, workspace_id, asset_draft_id, asset_id, target_asset_id,
				 relation_type, source, confidence, citation, added_by)
			SELECT source.organization_id, source.workspace_id, d.id, source.id, target.id,
			       $4, 'ai', 0.8, '{}'::jsonb, $5::uuid
			FROM asset.assets source
			JOIN asset.asset_drafts d
			  ON d.organization_id = source.organization_id AND d.asset_id = source.id
			JOIN asset.assets target
			  ON target.organization_id = source.organization_id
			 AND target.workspace_id = source.workspace_id
			 AND target.resource_model_id::text = ANY($3::text[])
			WHERE source.organization_id = $1::uuid AND source.id = $2::uuid
			  AND source.workspace_id = $6::uuid
			  AND source.resource_model_id::text = ANY($3::text[])
			  AND source.publication_status <> 'archived'
			ON CONFLICT (asset_draft_id, target_asset_id, relation_type) DO NOTHING
			RETURNING asset_draft_id
		)
		UPDATE asset.asset_drafts d
		SET revision = d.revision + 1
		WHERE d.id IN (SELECT asset_draft_id FROM parked)
	`, scope.OrganizationID, stringValue(arguments["source_asset_id"]), allowed,
		relationType, principal.UserID, scope.WorkspaceID)
	if err != nil {
		return nil, err
	}
	if commandTag.RowsAffected() == 0 {
		return nil, errors.New("relation assets were not found, authorized, or already parked")
	}
	return map[string]any{
		"status":        "parked_on_draft",
		"relation_type": relationType,
		"note":          "the relation materializes when the source asset's draft is committed",
	}, nil
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

// modelDraftToolError renders model-draft failures so the react model can
// self-heal: schema issues travel as JSON, everything else stays a plain
// message.
func modelDraftToolError(err error) error {
	var schemaErr *resourcemodel.SchemaValidationError
	if errors.As(err, &schemaErr) {
		issues, _ := json.Marshal(schemaErr.Issues)
		return fmt.Errorf("model_schema_invalid: %s", issues)
	}
	return err
}

// stringListValue coerces a tool argument to a string slice; a non-array
// argument yields nil so callers keep their "omitted" semantics.
func stringListValue(value any) []string {
	items, _ := value.([]any)
	return value2stringList(items)
}

func value2stringList(items []any) []string {
	result := make([]string, 0, len(items))
	for _, item := range items {
		if text, ok := item.(string); ok {
			result = append(result, text)
		}
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

func clipText(value string, max int) string {
	if len(value) > max {
		return value[:max] + "…"
	}
	return value
}

var _ ReActToolFactory = DomainToolFactory{}
