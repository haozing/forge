package modeling

// modeling.go — 建模计划服务（内容治理四件套 F4）。计划 = 批量内容建模的
// 人审分诊载体：第一步 analyze 工具纯计算产出计划 JSON（不落库），人经
// HTTP 保存为本表行并逐条取舍（approve/reject），批准后的计划由 react run
// 消费执行（产出全为草稿，发布仍走 human_only 门）。
//
// 技术架构借鉴（agentblog）三处落地：
//   - sqlc（queries.sql → internal/modeling/store）：数据访问全部生成，
//     queries.sql 即契约；
//   - RLS 试点（0045）：content.modeling_plans 启用 FORCE ROW LEVEL
//     SECURITY，本服务每个方法都在事务内 set_config('app.organization_id')
//     —— 租户隔离是结构性约束而非应用层约定；
//   - 事件接力：状态变更同事务落 outbox 事件（modeling.plan_changed），
//     下游（通知/检索/回流）按 consumer manifest 陆续挂载。

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"agentchunzhi/internal/auth"
	"agentchunzhi/internal/authz"
	"agentchunzhi/internal/eventing"
	modelingstore "agentchunzhi/internal/modeling/store"
	"agentchunzhi/internal/store"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

const (
	// StatusDraft 计划刚保存；StatusApproved 人已批准可执行；
	// StatusRejected 人驳回；StatusApplied 全部条目已产出；StatusFailed 执行中断。
	StatusDraft    = "draft"
	StatusApproved = "approved"
	StatusRejected = "rejected"
	StatusApplied  = "applied"
	StatusFailed   = "failed"
)

var (
	ErrNotFound      = errors.New("modeling plan not found")
	ErrInvalidInput  = errors.New("invalid modeling plan input")
	ErrForbidden     = errors.New("modeling plan access denied")
	ErrStatusInvalid = errors.New("modeling plan status transition not allowed")
)

// MaxPlanItems 与 agenttask 的批量上限一致（单任务 1..20 资产）。
const MaxPlanItems = 20

type Service struct {
	Store  *store.Store
	Policy authz.WorkspacePolicyService
	// Events 为空时 Patch 不发生命周期事件（测试/嵌入式构造场景）。
	Events *eventing.EventStore
}

// Plans 是建模计划域的契约接口（契约投影模式）：适配层（react 工具、
// 未来 MCP 工具）依赖接口而非具体 Service，可替换 Fake。
type Plans interface {
	Save(ctx context.Context, principal auth.Principal, workspaceID string, input SaveInput) (Plan, error)
	List(ctx context.Context, principal auth.Principal, workspaceID string, limit int) ([]Plan, error)
	Get(ctx context.Context, principal auth.Principal, workspaceID, planID string) (Plan, error)
	Patch(ctx context.Context, principal auth.Principal, workspaceID, planID string, input PatchInput) (Plan, error)
}

var _ Plans = Service{}

// Plan is one saved modeling plan row.
type Plan struct {
	ID             string          `json:"id"`
	OrganizationID string          `json:"organization_id"`
	WorkspaceID    string          `json:"workspace_id"`
	Intent         string          `json:"intent"`
	TargetModelID  string          `json:"target_model_id"`
	Sources        json.RawMessage `json:"sources"`
	Plan           json.RawMessage `json:"plan"`
	Status         string          `json:"status"`
	CreatedAt      time.Time       `json:"created_at"`
	UpdatedAt      time.Time       `json:"updated_at"`
}

type SaveInput struct {
	Intent        string
	TargetModelID string
	Sources       json.RawMessage
	Plan          json.RawMessage
}

type PatchInput struct {
	Status *string
	// Plan 整体替换（分诊台行级编辑后保存）；nil = 不动。
	Plan json.RawMessage
}

// require 让人与 agent 同域判权（asset.read 即可读写计划：计划是分诊台
// 面向操作者的，写动作本身是工作区行为，与收录清单 site.design 不同档）。
func (s Service) require(ctx context.Context, principal auth.Principal, workspaceID, action string) error {
	if principal.UserType != auth.UserTypeMember && principal.UserType != auth.UserTypeAgent {
		return ErrForbidden
	}
	if s.Store == nil || s.Store.Pool == nil || s.Policy.Store == nil {
		return ErrForbidden
	}
	if _, err := s.Policy.Require(ctx, principal, workspaceID, "", action); err != nil {
		return ErrForbidden
	}
	return nil
}

func validID(value string) bool {
	if len(value) != 36 {
		return false
	}
	for index, char := range value {
		if index == 8 || index == 13 || index == 18 || index == 23 {
			if char != '-' {
				return false
			}
			continue
		}
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') && (char < 'A' || char > 'F') {
			return false
		}
	}
	return true
}

// orgUUID 把 36 位 UUID 文本转成 pgtype.UUID；invalid 输入在上层已被
// validID 拦截，这里 panic 属于编程错误。
func orgUUID(value string) pgtype.UUID {
	parsed := uuid.MustParse(value)
	return pgtype.UUID{Bytes: parsed, Valid: true}
}

// withOrgTx 在事务内钉住 RLS 组织 GUC 后执行 fn（RLS 试点 0045）：
// content.modeling_plans 已 FORCE ROW LEVEL SECURITY，未设置 GUC 的事务
// 一行都看不见、也写不进——租户隔离由数据库结构性保证。
func (s Service) withOrgTx(ctx context.Context, organizationID string, fn func(q *modelingstore.Queries, tx pgx.Tx) error) error {
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT set_config('app.organization_id', $1, true)`, organizationID); err != nil {
		return err
	}
	if err := fn(modelingstore.New(tx), tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func validatePlanShape(plan json.RawMessage) error {
	var items []map[string]any
	if err := json.Unmarshal(plan, &items); err != nil {
		return ErrInvalidInput
	}
	if len(items) == 0 || len(items) > MaxPlanItems {
		return ErrInvalidInput
	}
	for _, item := range items {
		source, _ := item["source_asset_id"].(string)
		if !validID(source) {
			return ErrInvalidInput
		}
		if _, ok := item["enabled"].(bool); !ok {
			return ErrInvalidInput
		}
	}
	return nil
}

// Save persists one plan produced by the analyze step. Only humans reach the
// HTTP surface the handler enforces; the service re-checks workspace scope.
func (s Service) Save(ctx context.Context, principal auth.Principal, workspaceID string, input SaveInput) (Plan, error) {
	if !validID(workspaceID) {
		return Plan{}, ErrInvalidInput
	}
	if err := s.require(ctx, principal, workspaceID, authz.ActionAssetRead); err != nil {
		return Plan{}, err
	}
	intent := strings.TrimSpace(input.Intent)
	if intent == "" {
		return Plan{}, ErrInvalidInput
	}
	if err := validatePlanShape(input.Plan); err != nil {
		return Plan{}, err
	}
	if input.TargetModelID != "" && !validID(input.TargetModelID) {
		return Plan{}, ErrInvalidInput
	}
	var target pgtype.UUID
	if input.TargetModelID != "" {
		target = orgUUID(input.TargetModelID)
	}
	var item Plan
	err := s.withOrgTx(ctx, principal.OrganizationID, func(q *modelingstore.Queries, _ pgx.Tx) error {
		row, err := q.InsertPlan(ctx, modelingstore.InsertPlanParams{
			OrgID:         orgUUID(principal.OrganizationID),
			WorkspaceID:   orgUUID(workspaceID),
			Intent:        intent,
			TargetModelID: target,
			Sources:       defaultJSON(input.Sources),
			Plan:          input.Plan,
			CreatedBy:     orgUUID(principal.UserID),
		})
		if err != nil {
			return err
		}
		item = planFromRow(row.ID, row.OrganizationID, row.WorkspaceID, row.Intent,
			targetText(row.TargetModelID), row.Sources, row.Plan, row.Status, row.CreatedAt, row.UpdatedAt)
		return nil
	})
	if err != nil {
		return Plan{}, err
	}
	return item, nil
}

func defaultJSON(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 || string(raw) == "null" {
		return json.RawMessage("[]")
	}
	return raw
}

// List pages the workspace plans, newest first.
func (s Service) List(ctx context.Context, principal auth.Principal, workspaceID string, limit int) ([]Plan, error) {
	if !validID(workspaceID) {
		return nil, ErrInvalidInput
	}
	if err := s.require(ctx, principal, workspaceID, authz.ActionAssetRead); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	items := []Plan{}
	err := s.withOrgTx(ctx, principal.OrganizationID, func(q *modelingstore.Queries, _ pgx.Tx) error {
		rows, err := q.ListPlans(ctx, modelingstore.ListPlansParams{
			OrgID:       orgUUID(principal.OrganizationID),
			WorkspaceID: orgUUID(workspaceID),
			RowLimit:    int32(limit),
		})
		if err != nil {
			return err
		}
		for _, row := range rows {
			items = append(items, planFromRow(row.ID, row.OrganizationID, row.WorkspaceID, row.Intent,
				targetText(row.TargetModelID), row.Sources, row.Plan, row.Status, row.CreatedAt, row.UpdatedAt))
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return items, nil
}

// Get reads one plan inside the workspace scope.
func (s Service) Get(ctx context.Context, principal auth.Principal, workspaceID, planID string) (Plan, error) {
	if !validID(workspaceID) || !validID(planID) {
		return Plan{}, ErrInvalidInput
	}
	if err := s.require(ctx, principal, workspaceID, authz.ActionAssetRead); err != nil {
		return Plan{}, err
	}
	var item Plan
	err := s.withOrgTx(ctx, principal.OrganizationID, func(q *modelingstore.Queries, _ pgx.Tx) error {
		row, err := q.GetPlan(ctx, modelingstore.GetPlanParams{
			OrgID:       orgUUID(principal.OrganizationID),
			WorkspaceID: orgUUID(workspaceID),
			PlanID:      orgUUID(planID),
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		item = planFromRow(row.ID, row.OrganizationID, row.WorkspaceID, row.Intent,
			targetText(row.TargetModelID), row.Sources, row.Plan, row.Status, row.CreatedAt, row.UpdatedAt)
		return nil
	})
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return Plan{}, ErrNotFound
		}
		return Plan{}, err
	}
	return item, nil
}

// Patch transitions status (draft→approved|rejected, approved→applied|failed
// by the executor) and optionally replaces the plan body. Approve/reject are
// human decisions on the triage console; the service only guards the shape.
// Status transitions emit a modeling.plan_changed outbox event in the same
// transaction (事件接力 groundwork).
func (s Service) Patch(ctx context.Context, principal auth.Principal, workspaceID, planID string, input PatchInput) (Plan, error) {
	if !validID(workspaceID) || !validID(planID) {
		return Plan{}, ErrInvalidInput
	}
	if err := s.require(ctx, principal, workspaceID, authz.ActionAssetRead); err != nil {
		return Plan{}, err
	}
	if input.Plan != nil {
		if err := validatePlanShape(input.Plan); err != nil {
			return Plan{}, err
		}
	}
	var item Plan
	changed := false
	err := s.withOrgTx(ctx, principal.OrganizationID, func(q *modelingstore.Queries, tx pgx.Tx) error {
		current, err := q.LockPlan(ctx, modelingstore.LockPlanParams{
			OrgID:       orgUUID(principal.OrganizationID),
			WorkspaceID: orgUUID(workspaceID),
			PlanID:      orgUUID(planID),
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if input.Plan != nil {
			if current.Status != StatusDraft {
				return ErrStatusInvalid
			}
			if err := q.UpdatePlanBody(ctx, modelingstore.UpdatePlanBodyParams{
				OrgID:       orgUUID(principal.OrganizationID),
				WorkspaceID: orgUUID(workspaceID),
				PlanID:      orgUUID(planID),
				Plan:        input.Plan,
			}); err != nil {
				return err
			}
			changed = true
		}
		if input.Status != nil {
			status := strings.TrimSpace(*input.Status)
			allowed := map[string][]string{
				StatusDraft:    {StatusApproved, StatusRejected},
				StatusApproved: {StatusApplied, StatusFailed, StatusDraft},
				StatusFailed:   {StatusApproved, StatusRejected},
			}
			ok := false
			for _, next := range allowed[current.Status] {
				if next == status {
					ok = true
					break
				}
			}
			if !ok {
				return ErrStatusInvalid
			}
			row, err := q.UpdatePlanStatus(ctx, modelingstore.UpdatePlanStatusParams{
				OrgID:       orgUUID(principal.OrganizationID),
				WorkspaceID: orgUUID(workspaceID),
				PlanID:      orgUUID(planID),
				Status:      status,
			})
			if err != nil {
				return err
			}
			item = planFromStatus(row)
			changed = true
			if s.Events != nil {
				if _, err := s.Events.AppendTx(ctx, tx, eventing.Event{
					OrganizationID:   principal.OrganizationID,
					WorkspaceID:      workspaceID,
					EventType:        eventing.EventModelingPlanChanged,
					AggregateType:    "modeling_plan",
					AggregateID:      planID,
					AggregateVersion: 1,
					PayloadVersion:   eventing.PayloadVersionV1,
					Actor:            eventing.ActorFromPrincipal(principal),
					Payload: eventing.ModelingPlanChangedPayload{
						PlanID:      planID,
						WorkspaceID: workspaceID,
						Status:      status,
						Action:      "status_transition",
					},
				}); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		return Plan{}, err
	}
	if !changed {
		return s.Get(ctx, principal, workspaceID, planID)
	}
	return item, nil
}

// planFromRow 组装服务层 Plan（sqlc 生成的各 Row 形状一致，共用组装器）。
// targetText normalizes the sqlc narg-generated interface{} (nil or string).
func targetText(value any) string {
	text, _ := value.(string)
	return text
}

func planFromRow(id, organizationID, workspaceID, intent, targetModelID string,
	sources, plan []byte, status string, createdAt, updatedAt pgtype.Timestamptz) Plan {
	item := Plan{
		ID:             id,
		OrganizationID: organizationID,
		WorkspaceID:    workspaceID,
		Intent:         intent,
		TargetModelID:  targetModelID,
		Sources:        json.RawMessage(defaultJSON(sources)),
		Plan:           json.RawMessage(defaultJSON(plan)),
		Status:         status,
	}
	if createdAt.Valid {
		item.CreatedAt = createdAt.Time
	}
	if updatedAt.Valid {
		item.UpdatedAt = updatedAt.Time
	}
	return item
}

func planFromStatus(row modelingstore.UpdatePlanStatusRow) Plan {
	return planFromRow(row.ID, row.OrganizationID, row.WorkspaceID, row.Intent,
		targetText(row.TargetModelID), row.Sources, row.Plan, row.Status, row.CreatedAt, row.UpdatedAt)
}
