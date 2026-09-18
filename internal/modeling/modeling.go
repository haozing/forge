package modeling

// modeling.go — 建模计划服务（内容治理四件套 F4）。计划 = 批量内容建模的
// 人审分诊载体：第一步 analyze 工具纯计算产出计划 JSON（不落库），人经
// HTTP 保存为本表行并逐条取舍（approve/reject），批准后的计划由 react run
// 消费执行（产出全为草稿，发布仍走 human_only）。

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"agentchunzhi/internal/auth"
	"agentchunzhi/internal/authz"
	"agentchunzhi/internal/store"

	"github.com/jackc/pgx/v5"
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
}

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

const planColumns = `id::text, organization_id::text, workspace_id::text, intent,
	COALESCE(target_model_id::text, ''), sources, plan, status, created_at, updated_at`

func scanPlan(row interface{ Scan(...any) error }) (Plan, error) {
	var item Plan
	err := row.Scan(&item.ID, &item.OrganizationID, &item.WorkspaceID, &item.Intent,
		&item.TargetModelID, &item.Sources, &item.Plan, &item.Status,
		&item.CreatedAt, &item.UpdatedAt)
	return item, err
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

// validatePlanShape 检查计划条目数组的基本形状：对象数组、上限、每条必带
// source_asset_id 与 enabled 布尔。字段骨架等深校验留给执行链的既有校验器。
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
	var target any
	if input.TargetModelID != "" {
		target = input.TargetModelID
	}
	row := s.Store.Pool.QueryRow(ctx, `
		INSERT INTO content.modeling_plans
			(organization_id, workspace_id, intent, target_model_id, sources, plan, status, created_by)
		VALUES ($1::uuid, $2::uuid, $3, $4::uuid, $5::jsonb, $6::jsonb, 'draft', $7::uuid)
		RETURNING `+planColumns,
		principal.OrganizationID, workspaceID, intent, target,
		defaultJSON(input.Sources), input.Plan, principal.UserID)
	item, err := scanPlan(row)
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
	rows, err := s.Store.Pool.Query(ctx, `
		SELECT `+planColumns+`
		FROM content.modeling_plans
		WHERE organization_id = $1::uuid AND workspace_id = $2::uuid
		ORDER BY created_at DESC
		LIMIT $3::int
	`, principal.OrganizationID, workspaceID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []Plan{}
	for rows.Next() {
		item, err := scanPlan(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// Get reads one plan inside the workspace scope.
func (s Service) Get(ctx context.Context, principal auth.Principal, workspaceID, planID string) (Plan, error) {
	if !validID(workspaceID) || !validID(planID) {
		return Plan{}, ErrInvalidInput
	}
	if err := s.require(ctx, principal, workspaceID, authz.ActionAssetRead); err != nil {
		return Plan{}, err
	}
	item, err := scanPlan(s.Store.Pool.QueryRow(ctx, `
		SELECT `+planColumns+`
		FROM content.modeling_plans
		WHERE organization_id = $1::uuid AND workspace_id = $2::uuid AND id = $3::uuid
	`, principal.OrganizationID, workspaceID, planID))
	if errors.Is(err, pgx.ErrNoRows) {
		return Plan{}, ErrNotFound
	}
	return item, err
}

type PatchInput struct {
	Status *string
	// Plan 整体替换（分诊台行级编辑后保存）；nil = 不动。
	Plan json.RawMessage
}

// Patch transitions status (draft→approved|rejected, approved→applied|failed
// by the executor) and optionally replaces the plan body. Approve/reject are
// human decisions on the triage console; the service only guards the shape.
func (s Service) Patch(ctx context.Context, principal auth.Principal, workspaceID, planID string, input PatchInput) (Plan, error) {
	if !validID(workspaceID) || !validID(planID) {
		return Plan{}, ErrInvalidInput
	}
	if err := s.require(ctx, principal, workspaceID, authz.ActionAssetRead); err != nil {
		return Plan{}, err
	}
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return Plan{}, err
	}
	defer tx.Rollback(ctx)
	current, err := scanPlan(tx.QueryRow(ctx, `
		SELECT `+planColumns+`
		FROM content.modeling_plans
		WHERE organization_id = $1::uuid AND workspace_id = $2::uuid AND id = $3::uuid
		FOR UPDATE
	`, principal.OrganizationID, workspaceID, planID))
	if errors.Is(err, pgx.ErrNoRows) {
		return Plan{}, ErrNotFound
	}
	if err != nil {
		return Plan{}, err
	}
	sets := []string{"updated_at = now()"}
	args := []any{principal.OrganizationID, workspaceID, planID}
	arg := func(value any) string {
		args = append(args, value)
		return "$" + itoa(len(args))
	}
	if input.Plan != nil {
		if err := validatePlanShape(input.Plan); err != nil {
			return Plan{}, err
		}
		if current.Status != StatusDraft {
			return Plan{}, ErrStatusInvalid
		}
		sets = append(sets, "plan = "+arg(string(input.Plan))+"::jsonb")
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
			return Plan{}, ErrStatusInvalid
		}
		sets = append(sets, "status = "+arg(status))
	}
	item, err := scanPlan(tx.QueryRow(ctx, `
		UPDATE content.modeling_plans SET `+strings.Join(sets, ", ")+`
		WHERE organization_id = $1::uuid AND workspace_id = $2::uuid AND id = $3::uuid
		RETURNING `+planColumns, args...))
	if err != nil {
		return Plan{}, err
	}
	return item, tx.Commit(ctx)
}

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	digits := []byte{}
	for value > 0 {
		digits = append([]byte{byte('0' + value%10)}, digits...)
		value /= 10
	}
	return string(digits)
}

// RecordExecution flips a plan to applied/failed after the executor run
// finishes; item-level outcomes live inside the plan jsonb itself.
func (s Service) RecordExecution(ctx context.Context, principal auth.Principal, workspaceID, planID, status string) error {
	return firstErr(s.Patch(ctx, principal, workspaceID, planID, PatchInput{Status: &status}))
}

func firstErr(_ any, err error) error { return err }
