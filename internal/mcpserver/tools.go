package mcpserver

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"agentchunzhi/internal/asset"
	"agentchunzhi/internal/auth"
	agentquery "agentchunzhi/internal/query"
)

// registerTools adds every tool the principal's capabilities allow. The open
// face's capability keywords are reused verbatim; the service layer keeps
// doing organization/workspace authorization — capabilities only gate which
// tools are listed.
func registerTools(server *mcp.Server, deps Deps, principal auth.Principal) {
	// OpenAPI 检索通道要求 query.execute（与 /api/open/query 一致）；
	// query.read 仅放宽只读资产工具的可见性。
	if can(principal, "query.execute") {
		mcp.AddTool(server, &mcp.Tool{
			Name:        "search_assets",
			Description: "在资产中台检索知识资产（全文/语义/混合）。返回资产标题、摘要、可见性与相关度。写作或答问前先调用本工具检索已有知识。",
		}, func(ctx context.Context, req *mcp.CallToolRequest, args searchAssetsArgs) (*mcp.CallToolResult, any, error) {
			return searchAssets(ctx, deps, principal, args)
		})
	}
	if can(principal, "asset.read") || can(principal, "query.read") {
		mcp.AddTool(server, &mcp.Tool{
			Name:        "get_asset",
			Description: "读取单个资产的完整内容：标题、摘要、Markdown 正文、结构化字段、标签与发布状态。",
		}, func(ctx context.Context, req *mcp.CallToolRequest, args getAssetArgs) (*mcp.CallToolResult, any, error) {
			return getAsset(ctx, deps, principal, args)
		})
		mcp.AddTool(server, &mcp.Tool{
			Name:        "list_tables",
			Description: "列出工作区内可用的数据表（结构化资源模型）：模型 key、名称、字段与当前版本。",
		}, func(ctx context.Context, req *mcp.CallToolRequest, args listTablesArgs) (*mcp.CallToolResult, any, error) {
			return listTables(ctx, deps, principal, args)
		})
	}
	if can(principal, "asset.create") {
		mcp.AddTool(server, &mcp.Tool{
			Name:        "create_document",
			Description: "创建知识库文档（通用文档模型）。创建后为工作草稿，需依次调用 confirm_asset_version 与 publish_asset 完成入库。",
		}, func(ctx context.Context, req *mcp.CallToolRequest, args createDocumentArgs) (*mcp.CallToolResult, any, error) {
			return createDocument(ctx, deps, principal, args)
		})
		mcp.AddTool(server, &mcp.Tool{
			Name:        "insert_record",
			Description: "向指定数据表插入一条结构化记录（fields 的键须匹配表字段定义）。创建后同 create_document 需确认与发布。",
		}, func(ctx context.Context, req *mcp.CallToolRequest, args insertRecordArgs) (*mcp.CallToolResult, any, error) {
			return insertRecord(ctx, deps, principal, args)
		})
	}
	if can(principal, "asset.edit") {
		mcp.AddTool(server, &mcp.Tool{
			Name:        "update_document",
			Description: "更新资产草稿（标题/正文/字段）。必须携带 base_version_id 乐观锁（来自 create_document 或 get_asset 的输出），防止覆盖他人编辑。",
		}, func(ctx context.Context, req *mcp.CallToolRequest, args updateDocumentArgs) (*mcp.CallToolResult, any, error) {
			return updateDocument(ctx, deps, principal, args)
		})
	}
	if can(principal, "asset.publish") {
		mcp.AddTool(server, &mcp.Tool{
			Name:        "confirm_asset_version",
			Description: "人工确认资产的当前工作版本（入库治理链第一步；direct 发布策略要求先确认）。version_id 缺省时自动解析当前工作版本。",
		}, func(ctx context.Context, req *mcp.CallToolRequest, args confirmAssetArgs) (*mcp.CallToolResult, any, error) {
			return confirmAssetVersion(ctx, deps, principal, args)
		})
		mcp.AddTool(server, &mcp.Tool{
			Name:        "publish_asset",
			Description: "发布资产到知识库。direct 策略直接发布；review 策略自动转发布审核请求。输出会说明实际结果。",
		}, func(ctx context.Context, req *mcp.CallToolRequest, args publishAssetArgs) (*mcp.CallToolResult, any, error) {
			return publishAsset(ctx, deps, principal, args)
		})
	}
	if can(principal, "asset.archive") {
		mcp.AddTool(server, &mcp.Tool{
			Name:        "archive_asset",
			Description: "归档资产：内容下架但保留全部历史。",
		}, func(ctx context.Context, req *mcp.CallToolRequest, args archiveAssetArgs) (*mcp.CallToolResult, any, error) {
			return archiveAsset(ctx, deps, principal, args)
		})
	}
}

// ---------------------------------------------------------------------------
// 输入结构（jsonschema tag 生成工具入参 schema）
// ---------------------------------------------------------------------------

type searchAssetsArgs struct {
	Query string `json:"query" jsonschema:"检索关键词；语义与混合模式必填"`
	Mode  string `json:"mode,omitempty" jsonschema:"检索模式：fulltext（默认）、semantic、hybrid"`
	TopK  int    `json:"top_k,omitempty" jsonschema:"返回条数上限，默认 10，最大 50"`
}

type getAssetArgs struct {
	AssetID string `json:"asset_id" jsonschema:"资产 UUID"`
}

type listTablesArgs struct{}

type createDocumentArgs struct {
	WorkspaceID string `json:"workspace_id" jsonschema:"目标工作区 UUID（内置文档模型为组织级，必须显式指定工作区）"`
	Title       string `json:"title" jsonschema:"文档标题"`
	Markdown    string `json:"markdown" jsonschema:"Markdown 正文"`
	Summary     string `json:"summary,omitempty" jsonschema:"一句话摘要（可选，展示在列表）"`
}

type insertRecordArgs struct {
	WorkspaceID string         `json:"workspace_id" jsonschema:"目标工作区 UUID"`
	ModelID     string         `json:"model_id" jsonschema:"数据表（资源模型）UUID，来自 list_tables"`
	Title       string         `json:"title" jsonschema:"记录标题"`
	Fields      map[string]any `json:"fields" jsonschema:"结构化字段，键须匹配表字段定义"`
}

type updateDocumentArgs struct {
	AssetID       string `json:"asset_id" jsonschema:"资产 UUID"`
	BaseVersionID string `json:"base_version_id" jsonschema:"当前工作版本 UUID（乐观锁，来自 create_document 或 get_asset）"`
	Title         string `json:"title,omitempty" jsonschema:"新标题（可选）"`
	Markdown      string `json:"markdown,omitempty" jsonschema:"新 Markdown 正文（可选）"`
}

type confirmAssetArgs struct {
	AssetID   string `json:"asset_id" jsonschema:"资产 UUID"`
	VersionID string `json:"version_id,omitempty" jsonschema:"工作版本 UUID（可选；缺省自动解析当前工作版本）"`
}

type publishAssetArgs struct {
	AssetID       string `json:"asset_id" jsonschema:"资产 UUID"`
	BaseVersionID string `json:"base_version_id" jsonschema:"待发布的已确认版本 UUID（confirm_asset_version 输出）"`
}

type archiveAssetArgs struct {
	AssetID string `json:"asset_id" jsonschema:"资产 UUID"`
}

// ---------------------------------------------------------------------------
// 工具实现（薄适配：参数映射 + 摘要；业务全部在 service）
// ---------------------------------------------------------------------------

func searchAssets(ctx context.Context, deps Deps, principal auth.Principal, args searchAssetsArgs) (*mcp.CallToolResult, any, error) {
	mode := strings.TrimSpace(args.Mode)
	if mode == "" {
		mode = agentquery.ModeFulltext
	}
	topK := args.TopK
	if topK <= 0 {
		topK = 10
	}
	if topK > 50 {
		topK = 50
	}
	response, err := deps.QueryService.OpenAPIQuery(ctx, principal, agentquery.Request{
		Query: args.Query,
		Mode:  mode,
		TopK:  topK,
	})
	if err != nil {
		return toolError(err)
	}
	type hit struct {
		AssetID    string   `json:"asset_id"`
		Title      string   `json:"title"`
		Summary    string   `json:"summary"`
		Visibility string   `json:"visibility"`
		Score      *float64 `json:"score,omitempty"`
	}
	hits := make([]hit, 0, len(response.Items))
	var lines strings.Builder
	for _, item := range response.Items {
		hits = append(hits, hit{
			AssetID: item.AssetID, Title: item.Title,
			Summary: item.Summary, Visibility: item.Visibility, Score: item.Score,
		})
		fmt.Fprintf(&lines, "- %s（asset_id=%s）\n", item.Title, item.AssetID)
	}
	if len(hits) == 0 {
		lines.WriteString("没有匹配的资产。")
	}
	summary := fmt.Sprintf("检索到 %d 条（执行模式 %s）。\n%s", len(hits), response.ExecutedMode, lines.String())
	return textResult(summary, map[string]any{"items": hits, "executed_mode": response.ExecutedMode})
}

func getAsset(ctx context.Context, deps Deps, principal auth.Principal, args getAssetArgs) (*mcp.CallToolResult, any, error) {
	memberAsset, err := deps.MemberAssetService.Get(ctx, principal, args.AssetID)
	if err != nil {
		return toolError(err)
	}
	summary := fmt.Sprintf("「%s」状态 %s，当前工作版本 %s。", deref(memberAsset.Title), memberAsset.PublicationStatus, memberAsset.CurrentWorkingVersionID)
	return textResult(summary, memberAsset)
}

func listTables(ctx context.Context, deps Deps, principal auth.Principal, _ listTablesArgs) (*mcp.CallToolResult, any, error) {
	tables, err := listTablesForAgent(ctx, deps, principal)
	if err != nil {
		return toolError(err)
	}
	summary := fmt.Sprintf("共 %d 张表。\n%s", len(tables), renderTables(tables))
	return textResult(summary, map[string]any{"tables": tables})
}

type agentTable struct {
	ID       string `json:"id"`
	ModelKey string `json:"model_key"`
	Name     string `json:"name"`
}

func renderTables(tables []agentTable) string {
	var lines strings.Builder
	for _, t := range tables {
		fmt.Fprintf(&lines, "- %s（model_id=%s, key=%s）\n", t.Name, t.ID, t.ModelKey)
	}
	return lines.String()
}

// listTablesForAgent reads active models straight from the store:
// resourcemodel.Service.List is member-gated (workspace policy), while
// agents legitimately need the model catalog for create/insert tools.
func listTablesForAgent(ctx context.Context, deps Deps, principal auth.Principal) ([]agentTable, error) {
	rows, err := deps.AssetService.Store.Pool.Query(ctx, `
		SELECT rm.id::text, rm.model_key, rm.name
		FROM model.resource_models rm
		WHERE rm.organization_id = $1::uuid AND rm.status = 'active'
		ORDER BY rm.name, rm.id
	`, principal.OrganizationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	tables := []agentTable{}
	for rows.Next() {
		var t agentTable
		if err := rows.Scan(&t.ID, &t.ModelKey, &t.Name); err != nil {
			return nil, err
		}
		tables = append(tables, t)
	}
	return tables, rows.Err()
}

func createDocument(ctx context.Context, deps Deps, principal auth.Principal, args createDocumentArgs) (*mcp.CallToolResult, any, error) {
	result, err := createAssetCommon(ctx, deps, principal, createAssetCommonInput{
		WorkspaceID: args.WorkspaceID,
		Title:       args.Title,
		Markdown:    args.Markdown,
	})
	if err != nil {
		return nil, nil, err
	}
	summary := fmt.Sprintf("文档草稿已创建：asset_id=%s，current_working_version_id=%s（乐观锁）。\n下一步：confirm_asset_version → publish_asset。",
		result.ID, result.CurrentWorkingVersionID)
	return textResult(summary, result)
}

func insertRecord(ctx context.Context, deps Deps, principal auth.Principal, args insertRecordArgs) (*mcp.CallToolResult, any, error) {
	result, err := createAssetCommon(ctx, deps, principal, createAssetCommonInput{
		WorkspaceID: args.WorkspaceID,
		ModelID:     args.ModelID,
		Title:       args.Title,
		Fields:      args.Fields,
	})
	if err != nil {
		return nil, nil, err
	}
	summary := fmt.Sprintf("记录已创建：asset_id=%s，current_working_version_id=%s。\n下一步：confirm_asset_version → publish_asset。",
		result.ID, result.CurrentWorkingVersionID)
	return textResult(summary, result)
}

type createAssetCommonInput struct {
	WorkspaceID string
	ModelID     string
	Title       string
	Markdown    string
	Fields      map[string]any
}

func createAssetCommon(ctx context.Context, deps Deps, principal auth.Principal, input createAssetCommonInput) (asset.AssetResult, error) {
	modelID := input.ModelID
	if modelID == "" {
		modelID = builtinDocumentModelID(ctx, deps, principal)
		if modelID == "" {
			return asset.AssetResult{}, fmt.Errorf("未找到通用文档模型（builtin_document），请用 list_tables 确认可用表")
		}
	}
	allowedModels, err := deps.ScopeResolver.AllowedModelIDs(ctx, principal, "asset.create")
	if err != nil {
		return asset.AssetResult{}, err
	}
	title := strings.TrimSpace(input.Title)
	if title == "" {
		return asset.AssetResult{}, fmt.Errorf("title 不能为空")
	}
	return deps.AssetService.Create(ctx, principal, allowedModels, newIdempotencyKey(), asset.CreateInput{
		ResourceModelID: modelID,
		WorkspaceID:     input.WorkspaceID,
		Title:           &title,
		Markdown:        ptrOrNil(input.Markdown),
		Fields:          input.Fields,
	})
}

func updateDocument(ctx context.Context, deps Deps, principal auth.Principal, args updateDocumentArgs) (*mcp.CallToolResult, any, error) {
	if args.BaseVersionID == "" {
		return toolError(fmt.Errorf("base_version_id 必填（乐观锁）：请先 get_asset 或使用 create_document 的输出"))
	}
	allowedModels, err := deps.ScopeResolver.AllowedModelIDs(ctx, principal, "asset.edit")
	if err != nil {
		return nil, nil, err
	}
	var title, markdown *string
	if args.Title != "" {
		title = &args.Title
	}
	if args.Markdown != "" {
		markdown = &args.Markdown
	}
	result, err := deps.AssetService.Update(ctx, principal, allowedModels, newIdempotencyKey(), args.AssetID, args.BaseVersionID, asset.UpdateInput{
		Title:    title,
		Markdown: markdown,
	})
	if err != nil {
		return toolError(err)
	}
	summary := fmt.Sprintf("草稿已更新：asset_id=%s，新工作版本 %s。入库需重新 confirm_asset_version → publish_asset。", result.ID, result.CurrentWorkingVersionID)
	return textResult(summary, result)
}

func confirmAssetVersion(ctx context.Context, deps Deps, principal auth.Principal, args confirmAssetArgs) (*mcp.CallToolResult, any, error) {
	versionID := strings.TrimSpace(args.VersionID)
	if versionID == "" {
		var err error
		versionID, err = workingVersionID(ctx, deps, principal, args.AssetID)
		if err != nil {
			return toolError(err)
		}
	}
	version, err := deps.MemberAssetService.ConfirmVersion(ctx, principal, versionID, newIdempotencyKey())
	if err != nil {
		return toolError(err)
	}
	summary := fmt.Sprintf("版本已人工确认：version_id=%s。下一步 publish_asset（base_version_id 用它）。", version.ID)
	return textResult(summary, version)
}

// workingVersionID resolves the asset's current working version for confirm.
// Direct store read: MemberService.Get runs the workspace policy that bars
// agent principals, while confirm itself authorizes by version ownership.
func workingVersionID(ctx context.Context, deps Deps, principal auth.Principal, assetID string) (string, error) {
	var versionID string
	err := deps.AssetService.Store.Pool.QueryRow(ctx, `
		SELECT a.current_working_version_id::text
		FROM asset.assets a
		WHERE a.organization_id = $1::uuid AND a.id = $2::uuid AND a.deleted_at IS NULL
	`, principal.OrganizationID, assetID).Scan(&versionID)
	if err != nil {
		return "", fmt.Errorf("解析当前工作版本失败: %w", err)
	}
	if versionID == "" {
		return "", fmt.Errorf("资产 %s 没有当前工作版本", assetID)
	}
	return versionID, nil
}

func publishAsset(ctx context.Context, deps Deps, principal auth.Principal, args publishAssetArgs) (*mcp.CallToolResult, any, error) {
	if args.BaseVersionID == "" {
		return toolError(fmt.Errorf("base_version_id 必填：请使用 confirm_asset_version 输出的版本 UUID"))
	}
	allowedModels, err := deps.ScopeResolver.AllowedModelIDs(ctx, principal, "asset.publish")
	if err != nil {
		return nil, nil, err
	}
	result, err := deps.AssetService.Publish(ctx, principal, allowedModels, args.AssetID, args.BaseVersionID)
	if err != nil {
		return toolError(err)
	}
	summary := fmt.Sprintf("资产已发布：asset_id=%s，published_version_id=%s，状态 %s。", result.AssetID, result.PublishedVersionID, result.PublicationStatus)
	return textResult(summary, result)
}

func archiveAsset(ctx context.Context, deps Deps, principal auth.Principal, args archiveAssetArgs) (*mcp.CallToolResult, any, error) {
	allowedModels, err := deps.ScopeResolver.AllowedModelIDs(ctx, principal, "asset.archive")
	if err != nil {
		return nil, nil, err
	}
	result, err := deps.AssetService.Archive(ctx, principal, allowedModels, args.AssetID)
	if err != nil {
		return toolError(err)
	}
	summary := fmt.Sprintf("资产已归档：asset_id=%s。", result.AssetID)
	return textResult(summary, result)
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func builtinDocumentModelID(ctx context.Context, deps Deps, principal auth.Principal) string {
	tables, err := listTablesForAgent(ctx, deps, principal)
	if err != nil {
		return ""
	}
	for _, t := range tables {
		if t.ModelKey == "builtin_document" {
			return t.ID
		}
	}
	return ""
}

func newIdempotencyKey() string {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return fmt.Sprintf("mcp-%d", time.Now().UnixNano())
	}
	return "mcp-" + hex.EncodeToString(buf[:])
}

func deref(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func ptrOrNil(value string) *string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return &value
}
