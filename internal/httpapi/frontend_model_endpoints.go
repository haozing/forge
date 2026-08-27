package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"agentchunzhi/internal/modelendpoint"
)

type modelEndpointRequest struct {
	Name         string                `json:"name"`
	ProviderType string                `json:"provider_type"`
	BaseURL      string                `json:"base_url"`
	ModelName    string                `json:"model_name"`
	APIKey       string                `json:"api_key,omitempty"`
	SecretRef    string                `json:"secret_ref,omitempty"`
	Options      modelendpoint.Options `json:"options"`
}

func modelEndpointCollection(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		principal, ok := requireMemberSession(w, r, deps)
		if !ok {
			return
		}
		switch r.Method {
		case http.MethodGet:
			limit := 100
			if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
				parsed, err := strconv.Atoi(raw)
				if err != nil {
					writeError(w, http.StatusUnprocessableEntity, "validation_failed")
					return
				}
				limit = parsed
			}
			items, err := deps.ModelEndpointService.List(r.Context(), principal, limit)
			if err != nil {
				writeModelEndpointError(w, err, "model_endpoint_list_failed")
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"items": items})
		case http.MethodPost:
			key, ok := requestIdempotencyKey(w, r)
			if !ok {
				writeError(w, http.StatusUnprocessableEntity, "idempotency_key_invalid")
				return
			}
			var input modelEndpointRequest
			if !decodeModelEndpointRequest(w, r, &input) {
				return
			}
			item, err := deps.ModelEndpointService.Create(r.Context(), principal, modelendpoint.CreateInput{
				Name: input.Name, ProviderType: input.ProviderType, BaseURL: input.BaseURL,
				ModelName: input.ModelName, APIKey: input.APIKey, SecretRef: input.SecretRef,
				Options: input.Options, IdempotencyKey: key,
			})
			if err != nil {
				writeModelEndpointError(w, err, "model_endpoint_create_failed")
				return
			}
			writeJSON(w, http.StatusCreated, item)
		default:
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
		}
	}
}

func modelEndpointResource(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		principal, ok := requireMemberSession(w, r, deps)
		if !ok {
			return
		}
		endpointID := r.PathValue("endpointId")
		switch r.Method {
		case http.MethodGet:
			item, err := deps.ModelEndpointService.Get(r.Context(), principal, endpointID)
			if err != nil {
				writeModelEndpointError(w, err, "model_endpoint_load_failed")
				return
			}
			writeJSON(w, http.StatusOK, item)
		case http.MethodPut:
			key, ok := requestIdempotencyKey(w, r)
			if !ok {
				writeError(w, http.StatusUnprocessableEntity, "idempotency_key_invalid")
				return
			}
			var input modelEndpointRequest
			if !decodeModelEndpointRequest(w, r, &input) {
				return
			}
			item, err := deps.ModelEndpointService.Replace(r.Context(), principal, modelendpoint.ReplaceInput{
				EndpointID: endpointID, Name: input.Name, ProviderType: input.ProviderType,
				BaseURL: input.BaseURL, ModelName: input.ModelName, APIKey: input.APIKey,
				SecretRef: input.SecretRef, Options: input.Options, IdempotencyKey: key,
			})
			if err != nil {
				writeModelEndpointError(w, err, "model_endpoint_update_failed")
				return
			}
			writeJSON(w, http.StatusOK, item)
		default:
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
		}
	}
}

func testModelEndpoint(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, ok := requireMemberSession(w, r, deps)
		if !ok {
			return
		}
		result, err := deps.ModelEndpointService.Test(r.Context(), principal, r.PathValue("endpointId"))
		if err != nil {
			writeModelEndpointError(w, err, "model_endpoint_test_failed")
			return
		}
		// Pure probe: always 200 once the probe ran; outcome lives in ok/detail
		// and endpoint.status is left untouched either way.
		writeJSON(w, http.StatusOK, result)
	}
}

func setModelEndpointStatus(deps Dependencies, status string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, ok := requireMemberSession(w, r, deps)
		if !ok {
			return
		}
		if _, ok := requestIdempotencyKey(w, r); !ok {
			writeError(w, http.StatusUnprocessableEntity, "idempotency_key_invalid")
			return
		}
		item, err := deps.ModelEndpointService.SetStatus(r.Context(), principal, r.PathValue("endpointId"), status)
		if err != nil {
			writeModelEndpointError(w, err, "model_endpoint_status_failed")
			return
		}
		writeJSON(w, http.StatusOK, item)
	}
}

func decodeModelEndpointRequest(w http.ResponseWriter, r *http.Request, output *modelEndpointRequest) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64*1024))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(output); err != nil {
		writeError(w, http.StatusUnprocessableEntity, "validation_failed")
		return false
	}
	return true
}

func writeModelEndpointError(w http.ResponseWriter, err error, fallback string) {
	switch {
	case errors.Is(err, modelendpoint.ErrInvalidInput):
		writeError(w, http.StatusUnprocessableEntity, "validation_failed")
	case errors.Is(err, modelendpoint.ErrNotFound):
		writeError(w, http.StatusNotFound, "model_endpoint_not_found")
	case errors.Is(err, modelendpoint.ErrConflict):
		writeError(w, http.StatusConflict, "model_endpoint_conflict")
	case errors.Is(err, modelendpoint.ErrUnavailable):
		writeError(w, http.StatusBadGateway, "model_endpoint_unavailable")
	default:
		writeError(w, http.StatusInternalServerError, fallback)
	}
}
