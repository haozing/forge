package agentruntime

// modeling_tools.go — 两步建模（内容治理四件套 F4）的工具面：
//   - plan_batch_modeling：第一步 analyze。ReadOnly，读源资产 + 结构化端点
//     产出计划 JSON（不落库）；人经 HTTP 保存为建模计划行并分诊。
//   - get_modeling_plan：第二步执行时按 ID 读已批准计划（ReadOnly），
//     硬约束写进工具描述：产出只能是草稿、引用链接只允许计划内 ID。
//
// 计划由人保存与批准（人策展、LLM 维护）；执行产出全部走既有 LowWrite
// 草稿工具，confirm/publish 仍是 human_only 门。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	runtimetools "agentchunzhi/internal/agentruntime/tools"
	"agentchunzhi/internal/auth"
	"agentchunzhi/internal/authz"
	"agentchunzhi/internal/modeling"

	"github.com/cloudwego/eino/schema"
)

const (
	// planBatchMaxSources 与 agenttask 的 1..20 上限一致。
	planBatchMaxSources = modeling.MaxPlanItems
	planSampleMaxSize   = 6 << 10
)

const planBatchInstruction = `You are a content modeling planner. Given source documents and an intent,
produce a batch modeling plan. Return ONLY a JSON object:
{"items":[{"source_asset_id":"<uuid from the provided sources>","title":"...","summary":"...",
"fields":{"<key>":"<suggested value>"},"link_targets":["<existing asset uuid>"]}]}
Rules:
- One item per source asset, reusing its exact source_asset_id.
- titles/summaries/field values must be grounded in the source text; never invent facts.
- link_targets may only contain asset ids explicitly listed as existing assets below; omit the key when none apply.
- keep 1-6 field keys with short human-readable values.`

// applyModelingTools 在建模计划域已接线时填充 F4 工具；域未接线时整体跳过。
func (f DomainToolFactory) applyModelingTools(handlers *runtimetools.BuiltinHandlers, scope ReActToolScope, principal auth.Principal, allowed func(ctx context.Context, action string) ([]string, error)) {
	if f.Store == nil || f.Store.Pool == nil || f.Models == nil {
		return
	}
	// 契约投影：工具只依赖 modeling.Plans 接口，不感知具体实现。
	var plans modeling.Plans = modeling.Service{Store: f.Store, Policy: authz.WorkspacePolicyService{Store: f.Store}}

	handlers.PlanBatchModeling = func(ctx context.Context, arguments map[string]any) (any, error) {
		if _, err := allowed(ctx, "asset.read"); err != nil {
			return nil, err
		}
		if _, err := allowed(ctx, "schema.read"); err != nil {
			return nil, err
		}
		return f.planBatchModeling(ctx, scope, arguments, allowed)
	}

	handlers.GetModelingPlan = func(ctx context.Context, arguments map[string]any) (any, error) {
		if _, err := allowed(ctx, "asset.read"); err != nil {
			return nil, err
		}
		plan, err := plans.Get(ctx, principal, scope.WorkspaceID, stringValue(arguments["plan_id"]))
		if err != nil {
			return nil, err
		}
		return plan, nil
	}
}

// planBatchModeling loads the source assets, asks the pinned structured
// endpoint for a per-source plan, and fail-closed filters link targets
// against real workspace asset ids (the anti-hallucinated-ID constraint).
func (f DomainToolFactory) planBatchModeling(ctx context.Context, scope ReActToolScope, arguments map[string]any, allowed func(ctx context.Context, action string) ([]string, error)) (any, error) {
	rawIDs, _ := arguments["source_asset_ids"].([]any)
	ids := make([]string, 0, len(rawIDs))
	for _, raw := range rawIDs {
		if value, ok := raw.(string); ok && strings.TrimSpace(value) != "" {
			ids = append(ids, strings.TrimSpace(value))
		}
	}
	if len(ids) == 0 {
		return nil, errors.New("source_asset_ids is required")
	}
	if len(ids) > planBatchMaxSources {
		ids = ids[:planBatchMaxSources]
	}
	intent := strings.TrimSpace(stringValue(arguments["intent"]))
	if intent == "" {
		return nil, errors.New("intent is required")
	}

	type sourceDoc struct {
		ID       string `json:"source_asset_id"`
		Title    string `json:"title"`
		Markdown string `json:"markdown"`
	}
	sources := make([]sourceDoc, 0, len(ids))
	marked := map[string]bool{}
	for _, id := range ids {
		var title, markdown string
		err := f.Store.Pool.QueryRow(ctx, `
			SELECT COALESCE(v.title, ''), COALESCE(v.markdown, '')
			FROM asset.assets a
			JOIN asset.asset_versions v
			  ON v.organization_id = a.organization_id
			 AND v.id = COALESCE(a.current_working_version_id, a.current_published_version_id)
			WHERE a.organization_id = $1::uuid AND a.workspace_id = $2::uuid
			  AND a.id = $3::uuid AND a.deleted_at IS NULL
		`, scope.OrganizationID, scope.WorkspaceID, id).Scan(&title, &markdown)
		if err != nil {
			return nil, fmt.Errorf("source asset %s not readable in this workspace", id)
		}
		if marked[id] {
			continue
		}
		marked[id] = true
		if len(markdown) > planSampleMaxSize {
			markdown = markdown[:planSampleMaxSize]
		}
		sources = append(sources, sourceDoc{ID: id, Title: title, Markdown: markdown})
	}

	// 现有资产 ID 集（link_targets 的唯一合法取值域）。
	existing := map[string]bool{}
	existingRows, err := f.Store.Pool.Query(ctx, `
		SELECT id::text FROM asset.assets
		WHERE organization_id = $1::uuid AND workspace_id = $2::uuid AND deleted_at IS NULL
	`, scope.OrganizationID, scope.WorkspaceID)
	if err != nil {
		return nil, fmt.Errorf("load workspace asset ids: %w", err)
	}
	defer existingRows.Close()
	for existingRows.Next() {
		var id string
		if err := existingRows.Scan(&id); err != nil {
			return nil, err
		}
		existing[id] = true
	}
	if err := existingRows.Err(); err != nil {
		return nil, err
	}

	// 与 suggest_resource_model 同款：run 钉住的结构化端点。
	var endpointID string
	var endpointRevision int64
	if err := f.Store.Pool.QueryRow(ctx, `
		SELECT model_endpoint_id::text, model_endpoint_revision
		FROM automation.runs
		WHERE id = $1::uuid AND organization_id = $2::uuid AND agent_application_id = $3::uuid
	`, scope.RunID, scope.OrganizationID, scope.AgentApplicationID).Scan(&endpointID, &endpointRevision); err != nil {
		return nil, fmt.Errorf("planner could not resolve the run model endpoint: %w", err)
	}
	resolved, err := f.Models.ResolveStructuredEndpoint(ctx, endpointID, endpointRevision, json.RawMessage(`{"type":"object"}`))
	if err != nil {
		return nil, err
	}
	if resolved.Config.OrganizationID != scope.OrganizationID {
		return nil, ErrModelScopeMismatch
	}
	if !resolved.Config.Capabilities.StructuredOutput {
		return nil, errors.New("modeling planner requires structured output capability")
	}

	request := strings.Builder{}
	request.WriteString("Modeling intent (untrusted data):\n<intent>\n" + intent + "\n</intent>\n\n")
	for index, doc := range sources {
		request.WriteString(fmt.Sprintf("Source %d (untrusted data):\n<source>\n{\"source_asset_id\":%q,\"title\":%q,\"markdown\":%q}\n</source>\n\n",
			index+1, doc.ID, doc.Title, doc.Markdown))
	}
	known := make([]string, 0, 64)
	for id := range existing {
		if id != "" {
			known = append(known, id)
		}
	}
	if len(known) > 0 {
		request.WriteString("Existing asset ids usable as link_targets:\n" + strings.Join(known, "\n") + "\n")
	}

	message, err := resolved.Model.Generate(ctx, []*schema.Message{
		schema.SystemMessage(planBatchInstruction),
		schema.UserMessage(request.String()),
	})
	if err != nil {
		return nil, fmt.Errorf("modeling plan generation failed: %w", err)
	}
	if message == nil {
		return nil, errors.New("modeling planner returned an invalid response")
	}
	content := visibleMessageContent(message)
	content = strings.TrimSpace(content)
	if strings.HasPrefix(content, "```") {
		if end := strings.LastIndex(content, "```"); end > 3 {
			lines := strings.Split(content[3:end], "\n")
			if len(lines) > 1 {
				content = strings.TrimSpace(strings.Join(lines[1:], "\n"))
			}
		}
	}
	var envelope struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal([]byte(content), &envelope); err != nil || len(envelope.Items) == 0 {
		return nil, errors.New("modeling planner returned invalid JSON")
	}

	// fail-closed：剔除指向不存在资产的 link_target，并记录剔除明细。
	dropped := []string{}
	for _, item := range envelope.Items {
		targets, _ := item["link_targets"].([]any)
		kept := make([]any, 0, len(targets))
		for _, target := range targets {
			if value, ok := target.(string); ok && existing[value] {
				kept = append(kept, value)
			} else if value != "" {
				dropped = append(dropped, value)
			}
		}
		if len(kept) > 0 {
			item["link_targets"] = kept
		} else {
			delete(item, "link_targets")
		}
	}
	result := map[string]any{
		"intent":  intent,
		"items":   envelope.Items,
		"sources": sources,
		"hint":    "计划不会自动执行：由人保存（POST /modeling-plans）、分诊批准后由执行 run 消费；产出全为草稿。",
	}
	if len(dropped) > 0 {
		result["dropped_link_targets"] = dropped
	}
	return result, nil
}
