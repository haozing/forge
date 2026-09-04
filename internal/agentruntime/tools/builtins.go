package tools

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

const (
	maxToolArgumentsBytes = 32 * 1024
	maxToolResultBytes    = 64 * 1024
)

type JSONHandler func(context.Context, map[string]any) (any, error)

// BuiltinHandlers are bound to one run's server-side organization, principal,
// agent user and workspace. Those identities are deliberately absent from tool
// arguments so model output cannot change the authorization scope.
type BuiltinHandlers struct {
	SearchKnowledge      JSONHandler
	QueryAssets          JSONHandler
	GetAsset             JSONHandler
	GetSchema            JSONHandler
	GetRelatedAssets     JSONHandler
	GetAttachmentText    JSONHandler
	GetTaskStatus        JSONHandler
	CreateInternalAsset  JSONHandler
	UpdateInternalAsset  JSONHandler
	CreateRelation       JSONHandler
	SubmitProcessingTask JSONHandler
	PublishAsset         JSONHandler
	ArchiveAsset         JSONHandler
	DeleteAsset          JSONHandler
	ExportAssets         JSONHandler
	SiteStyleSuggest     JSONHandler
	SiteStylePresetSave  JSONHandler
	SuggestResourceModel JSONHandler
	CreateResourceModel  JSONHandler
	UpdateModelDraft     JSONHandler
	// G3/G6 content-quality tools (公开站点投递补齐方案).
	SuggestDisplayPath JSONHandler
	PublishChecklist   JSONHandler
	ListStaleAssets    JSONHandler
	SuggestCoverAlt    JSONHandler
	// G8 content patterns (apply stays a member command — the note-tree
	// write path authorizes through workspace membership).
	SaveContentPattern JSONHandler
	ListContentPatterns JSONHandler
}

type builtinSpec struct {
	name         string
	description  string
	risk         Risk
	capabilities []string
	handler      JSONHandler
	parameters   map[string]*schema.ParameterInfo
}

func RegisterBuiltins(registry *Registry, handlers BuiltinHandlers) error {
	if registry == nil {
		return errors.New("tool registry is nil")
	}
	for _, spec := range builtinSpecs(handlers) {
		if spec.handler == nil {
			continue
		}
		implementation := &builtinTool{info: &schema.ToolInfo{
			Name: spec.name, Desc: spec.description,
			ParamsOneOf: schema.NewParamsOneOfByParams(spec.parameters),
		}, parameters: spec.parameters, handler: spec.handler}
		if err := registry.Register(Definition{
			Name: spec.name, Risk: spec.risk, Capabilities: spec.capabilities, Tool: implementation,
		}); err != nil {
			return err
		}
	}
	return nil
}

// KnownCapabilities returns the closed capability vocabulary across all
// builtin tools. Admin surfaces validate tool_policy payloads against it so
// unknown capability strings are rejected at write time.
func KnownCapabilities() []string {
	seen := make(map[string]bool)
	out := make([]string, 0)
	for _, spec := range builtinSpecs(BuiltinHandlers{}) {
		for _, capability := range spec.capabilities {
			if !seen[capability] {
				seen[capability] = true
				out = append(out, capability)
			}
		}
	}
	sort.Strings(out)
	return out
}

func builtinSpecs(handlers BuiltinHandlers) []builtinSpec {
	id := func(description string) *schema.ParameterInfo {
		return &schema.ParameterInfo{Type: schema.String, Desc: description, Required: true}
	}
	return []builtinSpec{
		{name: "search_knowledge", description: "Search authorized published knowledge", risk: ReadOnly, capabilities: []string{"query.read"}, handler: handlers.SearchKnowledge, parameters: map[string]*schema.ParameterInfo{"query": id("Search query"), "limit": {Type: schema.Integer, Desc: "Maximum 50 results"}}},
		{name: "query_assets", description: "Query authorized assets with server-validated filters", risk: ReadOnly, capabilities: []string{"asset.read"}, handler: handlers.QueryAssets, parameters: map[string]*schema.ParameterInfo{"query": {Type: schema.String}, "limit": {Type: schema.Integer}}},
		{name: "get_asset", description: "Read one authorized asset", risk: ReadOnly, capabilities: []string{"asset.read"}, handler: handlers.GetAsset, parameters: map[string]*schema.ParameterInfo{"asset_id": id("Asset ID")}},
		{name: "get_schema", description: "Read an authorized resource model schema", risk: ReadOnly, capabilities: []string{"schema.read"}, handler: handlers.GetSchema, parameters: map[string]*schema.ParameterInfo{"resource_model_id": id("Resource model ID")}},
		{name: "get_related_assets", description: "Read authorized relations for an asset", risk: ReadOnly, capabilities: []string{"asset.read"}, handler: handlers.GetRelatedAssets, parameters: map[string]*schema.ParameterInfo{"asset_id": id("Asset ID"), "limit": {Type: schema.Integer}}},
		{name: "get_attachment_text", description: "Read policy-approved extracted attachment text", risk: ReadOnly, capabilities: []string{"attachment.read"}, handler: handlers.GetAttachmentText, parameters: map[string]*schema.ParameterInfo{"attachment_id": id("Attachment ID")}},
		{name: "get_task_status", description: "Read an authorized task status", risk: ReadOnly, capabilities: []string{"task.read"}, handler: handlers.GetTaskStatus, parameters: map[string]*schema.ParameterInfo{"task_id": id("Task ID")}},
		{name: "create_internal_asset", description: "Create an internal draft asset", risk: LowWrite, capabilities: []string{"asset.write"}, handler: handlers.CreateInternalAsset, parameters: map[string]*schema.ParameterInfo{"resource_model_id": id("Resource model ID"), "fields": {Type: schema.Object}, "tag_ids": {Type: schema.Array, Desc: "Existing workspace tag IDs to attach", ElemInfo: &schema.ParameterInfo{Type: schema.String}}}},
		{name: "update_internal_asset", description: "Update an internal draft asset", risk: LowWrite, capabilities: []string{"asset.write"}, handler: handlers.UpdateInternalAsset, parameters: map[string]*schema.ParameterInfo{"asset_id": id("Asset ID"), "expected_version_id": id("Current working version ID"), "title": {Type: schema.String}, "markdown": {Type: schema.String}, "fields": {Type: schema.Object}, "tag_ids": {Type: schema.Array, Desc: "Replacement tag ID selection (omitted keeps tags, empty clears)", ElemInfo: &schema.ParameterInfo{Type: schema.String}}}},
		{name: "create_relation", description: "Propose a relation between authorized internal assets; it parks on the source draft and materializes at commit", risk: LowWrite, capabilities: []string{"asset.write"}, handler: handlers.CreateRelation, parameters: map[string]*schema.ParameterInfo{"source_asset_id": id("Source asset ID"), "target_asset_id": id("Target asset ID"), "relation_type": id("Relation type")}},
		{name: "submit_processing_task", description: "Submit an idempotent processing task", risk: LowWrite, capabilities: []string{"task.write"}, handler: handlers.SubmitProcessingTask, parameters: map[string]*schema.ParameterInfo{"asset_id": id("Asset ID"), "operation": id("Registered operation")}},
		{name: "publish_asset", description: "Publish an approved internal asset; pass scheduled_at (RFC3339) to publish at a future moment", risk: HighWrite, capabilities: []string{"asset.publish"}, handler: handlers.PublishAsset, parameters: map[string]*schema.ParameterInfo{"asset_id": id("Asset ID"), "version_id": id("Asset version ID"), "scheduled_at": {Type: "string", Desc: "Optional RFC3339 timestamp: publish at this future moment instead of now"}}},
		{name: "archive_asset", description: "Archive a published asset", risk: HighWrite, capabilities: []string{"asset.archive"}, handler: handlers.ArchiveAsset, parameters: map[string]*schema.ParameterInfo{"asset_id": id("Asset ID")}},
		{name: "delete_asset", description: "Delete an authorized asset", risk: HighWrite, capabilities: []string{"asset.delete"}, handler: handlers.DeleteAsset, parameters: map[string]*schema.ParameterInfo{"asset_id": id("Asset ID")}},
		{name: "export_assets", description: "Create an export of authorized assets", risk: HighWrite, capabilities: []string{"asset.export"}, handler: handlers.ExportAssets, parameters: map[string]*schema.ParameterInfo{"format": id("Registered export format")}},
		// Named snake_case: OpenAI-compatible providers reject dots in
		// function names (pattern ^[a-zA-Z0-9_-]+$), the design doc's
		// dotted spelling cannot travel to the model.
		{name: "site_style_preset_save", description: "Save a site's current style (parameters + custom CSS) as a named org preset (low write; applying stays human)", risk: LowWrite, capabilities: []string{"site.style"}, handler: handlers.SiteStylePresetSave, parameters: map[string]*schema.ParameterInfo{"name": id("Preset name (2-32 chars)"), "site_id": {Type: schema.String, Desc: "Site ID or slug (required when the workspace has multiple active sites)"}}},
		{name: "site_style_suggest", description: "Suggest 2-3 validated style patches for one public site from a natural-language instruction (read-only; publishing stays human)", risk: ReadOnly, capabilities: []string{"site.style"}, handler: handlers.SiteStyleSuggest, parameters: map[string]*schema.ParameterInfo{"instruction": id("Natural-language style instruction"), "site_id": {Type: schema.String, Desc: "Site ID (required when the workspace has multiple active sites)"}}},
		{name: "suggest_resource_model", description: "Infer a resource model draft (key/name/kind/field_schema) from an intent and up to 10 sample records; returns the validated draft plus schema issues (pure computation, nothing is stored)", risk: ReadOnly, capabilities: []string{"schema.read"}, handler: handlers.SuggestResourceModel, parameters: map[string]*schema.ParameterInfo{"intent": id("What the model should capture"), "samples": {Type: schema.Array, Desc: "1-10 raw sample records (text, <=4KB each)", ElemInfo: &schema.ParameterInfo{Type: schema.String}}, "extend_model_id": {Type: schema.String, Desc: "Optional existing model ID whose schema the draft should extend"}}},
		{name: "create_resource_model", description: "Create a DRAFT resource model + draft version 1 from a suggested schema (requires the model.manage capability; validating and publishing stay human)", risk: HighWrite, capabilities: []string{"model.manage"}, handler: handlers.CreateResourceModel, parameters: map[string]*schema.ParameterInfo{"model_key": id("snake_case English key"), "name": id("Display name (Chinese)"), "content_kind": id("record, document, faq or note"), "description": {Type: schema.String}, "field_schema": {Type: schema.Object, Desc: "{\"additional_properties\":false,\"fields\":[...]} per the type dictionary"}}},
		{name: "update_resource_model_draft", description: "Patch a DRAFT resource model version (checksum If-Match; requires the model.manage capability; publishing stays human)", risk: HighWrite, capabilities: []string{"model.manage"}, handler: handlers.UpdateModelDraft, parameters: map[string]*schema.ParameterInfo{"version_id": id("Draft version ID"), "expected_schema_checksum": id("Current schema_checksum (If-Match)"), "field_schema": {Type: schema.Object}, "form_schema": {Type: schema.Object}, "list_schema": {Type: schema.Object}, "policy": {Type: schema.Object}}},
		// G3/G6 content-quality tools (公开站点投递补齐与Agent技能扩展实施方案):
		// read-only trio ships in the default capability set; cover alt is a
		// low write onto the draft layer that freezes at commit.
		{name: "suggest_display_path", description: "Suggest up to 3 valid URL slugs (display_path) for an article title; pure computation, application goes through the site binding flow", risk: ReadOnly, capabilities: []string{"asset.read"}, handler: handlers.SuggestDisplayPath, parameters: map[string]*schema.ParameterInfo{"asset_id": {Type: schema.String, Desc: "Optional asset ID (title is read from it)"}, "title": {Type: schema.String, Desc: "Optional explicit title"}}},
		{name: "publish_checklist", description: "Run a pre-publish quality report (title/summary/cover/cover alt/tags/binding/body/open intents; optional external link liveness). Advisory only — warnings never block publishing", risk: ReadOnly, capabilities: []string{"asset.read"}, handler: handlers.PublishChecklist, parameters: map[string]*schema.ParameterInfo{"asset_id": id("Asset ID"), "check_links": {Type: schema.Boolean, Desc: "Probe up to 10 external links with HEAD (SSRF-guarded)"}}},
		{name: "list_stale_assets", description: "List published assets not updated for N days (default 180), oldest first, capped at 20", risk: ReadOnly, capabilities: []string{"asset.read"}, handler: handlers.ListStaleAssets, parameters: map[string]*schema.ParameterInfo{"days": {Type: schema.Integer, Desc: "Staleness threshold in days (1-3650)"}}},
		{name: "suggest_cover_alt", description: "Set the cover image's alt text on the draft (dictated text wins; otherwise drafted from the article's title and summary). Freezes into the version at commit", risk: LowWrite, capabilities: []string{"asset.write"}, handler: handlers.SuggestCoverAlt, parameters: map[string]*schema.ParameterInfo{"asset_id": id("Asset ID"), "alt_text": {Type: schema.String, Desc: "Optional dictated alt text (<=500 chars); omit to generate"}}},
		{name: "save_content_pattern", description: "Save a reusable content skeleton: snapshot an asset's note block tree (from_asset_id) or store explicit blocks, under a 2-32 char org-level name (upsert by name)", risk: LowWrite, capabilities: []string{"content.patterns"}, handler: handlers.SaveContentPattern, parameters: map[string]*schema.ParameterInfo{"name": id("Pattern name (2-32 chars)"), "description": {Type: schema.String}, "from_asset_id": {Type: schema.String, Desc: "Asset whose note tree to snapshot"}, "blocks": {Type: schema.Array, Desc: "Explicit [{kind,content}] blocks (<=200)", ElemInfo: &schema.ParameterInfo{Type: schema.Object}}}},
		{name: "list_content_patterns", description: "List the organization's saved content skeletons (name, description, block count, source asset)", risk: ReadOnly, capabilities: []string{"content.patterns"}, handler: handlers.ListContentPatterns, parameters: map[string]*schema.ParameterInfo{"limit": {Type: schema.Integer}}},
	}
}

type builtinTool struct {
	info       *schema.ToolInfo
	parameters map[string]*schema.ParameterInfo
	handler    JSONHandler
}

func (t *builtinTool) Info(context.Context) (*schema.ToolInfo, error) {
	if t == nil || t.info == nil {
		return nil, errors.New("tool is not initialized")
	}
	return t.info, nil
}

func (t *builtinTool) InvokableRun(ctx context.Context, argumentsInJSON string, _ ...tool.Option) (string, error) {
	if t == nil || t.handler == nil {
		return structuredToolError("tool_unavailable"), nil
	}
	if len(argumentsInJSON) == 0 || len(argumentsInJSON) > maxToolArgumentsBytes || strings.ContainsRune(argumentsInJSON, '\x00') {
		return structuredToolError("invalid_tool_arguments"), nil
	}
	arguments := map[string]any{}
	decoder := json.NewDecoder(strings.NewReader(argumentsInJSON))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&arguments); err != nil {
		return structuredToolError("invalid_tool_arguments"), nil
	}
	if !validBuiltinArguments(arguments, t.parameters) {
		return structuredToolError("invalid_tool_arguments"), nil
	}
	result, err := t.handler(ctx, arguments)
	if err != nil {
		// The detail rides along so the model can self-heal (design doc
		// §8.3: correction hints go back to the caller). Internal failure
		// markers are replaced wholesale — a raw SQLSTATE or driver error
		// never reaches the model (audit B-10).
		body, _ := json.Marshal(map[string]any{"ok": false, "code": "tool_failed", "detail": toolErrorDetail(err)})
		return string(body), nil
	}
	body, err := json.Marshal(map[string]any{"ok": true, "data": result})
	if err != nil || len(body) > maxToolResultBytes {
		return structuredToolError("tool_result_too_large"), nil
	}
	return string(body), nil
}

func validBuiltinArguments(arguments map[string]any, parameters map[string]*schema.ParameterInfo) bool {
	for key, value := range arguments {
		parameter, ok := parameters[key]
		if !ok || parameter == nil || !matchesParameterType(value, parameter.Type) {
			return false
		}
	}
	for key, parameter := range parameters {
		if parameter != nil && parameter.Required {
			if _, ok := arguments[key]; !ok {
				return false
			}
		}
	}
	return true
}

func matchesParameterType(value any, parameterType schema.DataType) bool {
	switch parameterType {
	case schema.String:
		_, ok := value.(string)
		return ok
	case schema.Integer:
		number, ok := value.(float64)
		return ok && number == float64(int64(number))
	case schema.Number:
		_, ok := value.(float64)
		return ok
	case schema.Boolean:
		_, ok := value.(bool)
		return ok
	case schema.Object:
		_, ok := value.(map[string]any)
		return ok
	case schema.Array:
		_, ok := value.([]any)
		return ok
	default:
		return false
	}
}

func structuredToolError(code string) string {
	body, _ := json.Marshal(map[string]any{"ok": false, "code": code})
	return string(body)
}

var _ tool.InvokableTool = (*builtinTool)(nil)

// toolErrorDetail renders a self-healing hint from a handler error. Domain
// messages pass through (bounded); anything smelling of infrastructure is
// collapsed to a generic marker so schema/driver details stay server-side.
func toolErrorDetail(err error) string {
	detail := err.Error()
	for _, marker := range []string{"SQLSTATE", "sql:", "pgx:", "dial tcp", "connection refused", "net/http", "unexpected EOF"} {
		if strings.Contains(detail, marker) {
			return "internal error (see server logs)"
		}
	}
	if len(detail) > 400 {
		detail = detail[:400]
	}
	return detail
}
