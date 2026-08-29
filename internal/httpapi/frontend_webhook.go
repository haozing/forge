package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	assetservice "agentchunzhi/internal/asset"
)

// webhookCreateAssetRequest is the envelope of POST /api/open/v1/hooks/assets.
// Source must be "webhook" (or empty); external_ref is an optional caller-side
// idempotency marker: replaying it returns the first result with
// replayed=true. workspace_id/resource_model_id are optional and resolved by
// TransferService.ResolveWebhookTarget (default model fallback).
type webhookCreateAssetRequest struct {
	Source          string         `json:"source"`
	ExternalRef     string         `json:"external_ref"`
	WorkspaceID     string         `json:"workspace_id"`
	ResourceModelID string         `json:"resource_model_id"`
	Title           *string        `json:"title"`
	Markdown        *string        `json:"markdown"`
	Fields          map[string]any `json:"fields"`
}

// webhookAssetResult wraps the created asset with channel metadata.
type webhookAssetResult struct {
	Asset           assetservice.AssetResult `json:"asset"`
	ResourceModelID string                   `json:"resource_model_id"`
	WorkspaceID     string                   `json:"workspace_id"`
	ExternalRef     string                   `json:"external_ref,omitempty"`
	ReceivedAt      time.Time                `json:"received_at"`
	Replayed        bool                     `json:"replayed"`
}

func writeWebhookAssetError(w http.ResponseWriter, err error, fallback string) {
	switch {
	case errors.Is(err, assetservice.ErrWebhookWorkspaceUnresolved):
		writeError(w, http.StatusUnprocessableEntity, "webhook_workspace_unresolved")
	case errors.Is(err, assetservice.ErrWebhookDefaultModelMissing):
		writeError(w, http.StatusUnprocessableEntity, "default_resource_model_missing")
	case errors.Is(err, assetservice.ErrForbidden):
		writeError(w, http.StatusForbidden, "permission_denied")
	case errors.Is(err, assetservice.ErrNotFound):
		writeError(w, http.StatusNotFound, "resource_model_not_found")
	case errors.Is(err, assetservice.ErrConflict), errors.Is(err, assetservice.ErrIdempotencyConflict):
		writeError(w, http.StatusConflict, "webhook_conflict")
	case errors.Is(err, assetservice.ErrInvalidInput):
		writeError(w, http.StatusUnprocessableEntity, "invalid_asset_request")
	default:
		writeError(w, http.StatusInternalServerError, fallback)
	}
}

func webhookCreateAsset(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, err := deps.Authenticator.Authenticate(r.Context(), r)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		if !requireAgentCapability(w, principal, "asset.create") {
			return
		}
		var input webhookCreateAssetRequest
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil {
			writeError(w, http.StatusUnprocessableEntity, "validation_failed")
			return
		}
		source := strings.TrimSpace(input.Source)
		if source != "" && source != "webhook" {
			writeError(w, http.StatusUnprocessableEntity, "invalid_webhook_source")
			return
		}
		externalRef := strings.TrimSpace(input.ExternalRef)
		if externalRef != "" && (len(externalRef) < 16 || len(externalRef) > 200) {
			writeError(w, http.StatusUnprocessableEntity, "invalid_external_ref")
			return
		}
		target, err := deps.TransferService.ResolveWebhookTarget(r.Context(), principal, input.WorkspaceID, input.ResourceModelID)
		if err != nil {
			writeWebhookAssetError(w, err, "webhook_target_resolution_failed")
			return
		}
		receivedAt := time.Now().UTC()
		result, replayed, err := deps.AssetService.CreateFromWebhook(r.Context(), principal, assetservice.WebhookAssetInput{
			WorkspaceID:     target.WorkspaceID,
			ResourceModelID: target.ResourceModelID,
			ExternalRef:     externalRef,
			Title:           input.Title,
			Markdown:        input.Markdown,
			Fields:          input.Fields,
			ReceivedAt:      receivedAt,
		})
		if err != nil {
			writeWebhookAssetError(w, err, "webhook_asset_create_failed")
			return
		}
		status := http.StatusCreated
		if replayed {
			status = http.StatusOK
		}
		writeJSON(w, status, webhookAssetResult{
			Asset:           result,
			ResourceModelID: target.ResourceModelID,
			WorkspaceID:     target.WorkspaceID,
			ExternalRef:     externalRef,
			ReceivedAt:      receivedAt,
			Replayed:        replayed,
		})
	}
}
