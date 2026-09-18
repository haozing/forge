package httpapi

// modeling_plans.go — 建模计划 HTTP 面（内容治理 F4）：人把 analyze 工具
// 产出的计划 JSON 保存为行、列表/详情、分诊（批准/驳回/行级编辑）与执行
// 收口（applied/failed）。服务层自带工作区判权（asset.read），handler 只
// 做会话与路由参数校验。

import (
	"encoding/json"
	"errors"
	"net/http"

	"agentchunzhi/internal/authz"
	"agentchunzhi/internal/modeling"
)

// modelingError maps service errors to HTTP statuses (handler-local to keep
// the modeling package free of transport concerns).
func modelingError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, modeling.ErrInvalidInput):
		writeError(w, http.StatusUnprocessableEntity, "invalid_input")
	case errors.Is(err, modeling.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found")
	case errors.Is(err, modeling.ErrStatusInvalid):
		writeError(w, http.StatusUnprocessableEntity, "status_transition_not_allowed")
	case errors.Is(err, modeling.ErrForbidden):
		writeError(w, http.StatusForbidden, "action_not_allowed")
	default:
		writeError(w, http.StatusInternalServerError, "internal_error")
	}
}

func requireModelingService(w http.ResponseWriter, deps Dependencies) bool {
	if deps.Store == nil || deps.Store.Pool == nil {
		writeError(w, http.StatusServiceUnavailable, "modeling_unavailable")
		return false
	}
	return true
}

// ModelingPlansCollection serves GET/POST
// /api/workspaces/{workspaceId}/modeling-plans.
func ModelingPlansCollection(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		principal, ok := sessionPrincipal(w, r, deps)
		if !ok {
			return
		}
		if !requireModelingService(w, deps) {
			return
		}
		workspaceID := r.PathValue("workspaceId")
		if !requirePathUUID(w, workspaceID) {
			return
		}
		switch r.Method {
		case http.MethodGet:
			items, err := deps.ModelingPlans.List(r.Context(), principal, workspaceID,
				atoiDefault(r.URL.Query().Get("limit"), 50))
			if err != nil {
				modelingError(w, err)
				return
			}
			writeData(w, r, http.StatusOK, map[string]any{"items": items})
		case http.MethodPost:
			if !requireWorkspaceAction(w, r, deps, principal, workspaceID, authz.ActionAssetRead) {
				return
			}
			if _, ok := requireIdempotencyKey(w, r); !ok {
				return
			}
			var input struct {
				Intent        string          `json:"intent"`
				TargetModelID string          `json:"target_model_id"`
				Sources       json.RawMessage `json:"sources"`
				Plan          json.RawMessage `json:"plan"`
			}
			if !decodeBody(w, r, &input, 1024*1024) {
				return
			}
			item, err := deps.ModelingPlans.Save(r.Context(), principal, workspaceID, modeling.SaveInput{
				Intent:        input.Intent,
				TargetModelID: input.TargetModelID,
				Sources:       input.Sources,
				Plan:          input.Plan,
			})
			if err != nil {
				modelingError(w, err)
				return
			}
			writeData(w, r, http.StatusCreated, item)
		default:
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
		}
	}
}

// ModelingPlanResource serves GET/PATCH
// /api/workspaces/{workspaceId}/modeling-plans/{planId}.
func ModelingPlanResource(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		principal, ok := sessionPrincipal(w, r, deps)
		if !ok {
			return
		}
		if !requireModelingService(w, deps) {
			return
		}
		workspaceID := r.PathValue("workspaceId")
		planID := r.PathValue("planId")
		if !requirePathUUID(w, workspaceID, planID) {
			return
		}
		switch r.Method {
		case http.MethodGet:
			item, err := deps.ModelingPlans.Get(r.Context(), principal, workspaceID, planID)
			if err != nil {
				modelingError(w, err)
				return
			}
			writeData(w, r, http.StatusOK, item)
		case http.MethodPatch:
			var input struct {
				Status *string         `json:"status"`
				Plan   json.RawMessage `json:"plan"`
			}
			if !decodeBody(w, r, &input, 1024*1024) {
				return
			}
			item, err := deps.ModelingPlans.Patch(r.Context(), principal, workspaceID, planID, modeling.PatchInput{
				Status: input.Status,
				Plan:   input.Plan,
			})
			if err != nil {
				modelingError(w, err)
				return
			}
			writeData(w, r, http.StatusOK, item)
		default:
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
		}
	}
}
