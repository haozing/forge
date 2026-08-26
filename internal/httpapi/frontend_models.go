package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"

	"agentchunzhi/internal/resourcemodel"
)

func writeResourceModelError(w http.ResponseWriter, err error, fallback string) {
	var schemaErr *resourcemodel.SchemaValidationError
	switch {
	case errors.Is(err, resourcemodel.ErrInvalidInput):
		writeError(w, http.StatusUnprocessableEntity, "validation_failed")
	case errors.As(err, &schemaErr):
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"code": "model_schema_invalid", "message": "resource model schema is invalid", "request_id": requestID(), "details": schemaErr})
	case errors.Is(err, resourcemodel.ErrForbidden):
		writeError(w, http.StatusForbidden, "workspace_access_denied")
	case errors.Is(err, resourcemodel.ErrNotFound):
		writeError(w, http.StatusNotFound, "resource_model_not_found")
	case errors.Is(err, resourcemodel.ErrConflict):
		writeError(w, http.StatusConflict, "resource_model_conflict")
	default:
		writeError(w, http.StatusInternalServerError, fallback)
	}
}

type createResourceModelRequest struct {
	ModelKey       string                       `json:"model_key"`
	Name           string                       `json:"name"`
	Description    string                       `json:"description"`
	ContentKind    string                       `json:"content_kind"`
	InitialVersion resourcemodel.InitialVersion `json:"initial_version"`
}

func listResourceModels(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, ok := requireMemberSession(w, r, deps)
		if !ok {
			return
		}
		items, err := deps.ResourceModelService.List(r.Context(), principal, r.PathValue("workspaceId"))
		if err != nil {
			writeResourceModelError(w, err, "resource_model_list_failed")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": items, "has_more": false})
	}
}

func createResourceModel(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, ok := requireMemberSession(w, r, deps)
		if !ok {
			return
		}
		if _, ok := requiredIdempotencyKey(r); !ok {
			writeError(w, http.StatusUnprocessableEntity, "idempotency_key_required")
			return
		}
		var input createResourceModelRequest
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256*1024))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil {
			writeError(w, http.StatusUnprocessableEntity, "validation_failed")
			return
		}
		result, err := deps.ResourceModelService.Create(r.Context(), principal, r.PathValue("workspaceId"), resourcemodel.CreateInput{
			ModelKey: input.ModelKey, Name: input.Name, Description: input.Description, ContentKind: input.ContentKind, InitialVersion: input.InitialVersion,
		})
		if err != nil {
			writeResourceModelError(w, err, "resource_model_create_failed")
			return
		}
		if result.CurrentVersion != nil {
			writeETag(w, result.CurrentVersion.SchemaChecksum)
		}
		writeJSON(w, http.StatusCreated, result)
	}
}

func resourceModelsCollection(deps Dependencies) http.HandlerFunc {
	list := listResourceModels(deps)
	create := createResourceModel(deps)
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			list(w, r)
			return
		}
		create(w, r)
	}
}

type patchResourceModelRequest struct {
	Name        *string `json:"name"`
	Description *string `json:"description"`
	Status      *string `json:"status"`
}

func getResourceModel(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, ok := requireMemberSession(w, r, deps)
		if !ok {
			return
		}
		result, err := deps.ResourceModelService.Get(r.Context(), principal, r.PathValue("resourceModelId"))
		if err != nil {
			writeResourceModelError(w, err, "resource_model_load_failed")
			return
		}
		if result.CurrentVersion != nil {
			writeETag(w, result.CurrentVersion.SchemaChecksum)
		}
		writeJSON(w, http.StatusOK, result)
	}
}

func patchResourceModel(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, ok := requireMemberSession(w, r, deps)
		if !ok {
			return
		}
		if _, ok := requiredIdempotencyKey(r); !ok {
			writeError(w, http.StatusUnprocessableEntity, "idempotency_key_required")
			return
		}
		var input patchResourceModelRequest
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32*1024))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil {
			writeError(w, http.StatusUnprocessableEntity, "validation_failed")
			return
		}
		result, err := deps.ResourceModelService.Patch(r.Context(), principal, r.PathValue("resourceModelId"), resourcemodel.PatchInput{Name: input.Name, Description: input.Description, Status: input.Status})
		if err != nil {
			writeResourceModelError(w, err, "resource_model_update_failed")
			return
		}
		if result.CurrentVersion != nil {
			writeETag(w, result.CurrentVersion.SchemaChecksum)
		}
		writeJSON(w, http.StatusOK, result)
	}
}

func resourceModelResource(deps Dependencies) http.HandlerFunc {
	get := getResourceModel(deps)
	patch := patchResourceModel(deps)
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			get(w, r)
			return
		}
		patch(w, r)
	}
}

func listResourceModelVersions(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, ok := requireMemberSession(w, r, deps)
		if !ok {
			return
		}
		items, err := deps.ResourceModelService.Versions(r.Context(), principal, r.PathValue("resourceModelId"))
		if err != nil {
			writeResourceModelError(w, err, "resource_model_versions_failed")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": items, "has_more": false})
	}
}

func createResourceModelVersion(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, ok := requireMemberSession(w, r, deps)
		if !ok {
			return
		}
		if _, ok := requiredIdempotencyKey(r); !ok {
			writeError(w, http.StatusUnprocessableEntity, "idempotency_key_required")
			return
		}
		var input resourcemodel.VersionInput
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256*1024))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil {
			writeError(w, http.StatusUnprocessableEntity, "validation_failed")
			return
		}
		result, err := deps.ResourceModelService.CreateVersion(r.Context(), principal, r.PathValue("resourceModelId"), input)
		if err != nil {
			writeResourceModelError(w, err, "resource_model_version_create_failed")
			return
		}
		writeETag(w, result.SchemaChecksum)
		writeJSON(w, http.StatusCreated, result)
	}
}

func resourceModelVersionsCollection(deps Dependencies) http.HandlerFunc {
	list := listResourceModelVersions(deps)
	create := createResourceModelVersion(deps)
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			list(w, r)
			return
		}
		create(w, r)
	}
}

func getResourceModelVersion(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, ok := requireMemberSession(w, r, deps)
		if !ok {
			return
		}
		result, err := deps.ResourceModelService.GetVersion(r.Context(), principal, r.PathValue("versionId"))
		if err != nil {
			writeResourceModelError(w, err, "resource_model_version_load_failed")
			return
		}
		writeETag(w, result.SchemaChecksum)
		writeJSON(w, http.StatusOK, result)
	}
}

func patchResourceModelVersion(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, ok := requireMemberSession(w, r, deps)
		if !ok {
			return
		}
		if _, ok := requiredIdempotencyKey(r); !ok {
			writeError(w, http.StatusUnprocessableEntity, "idempotency_key_required")
			return
		}
		current, err := deps.ResourceModelService.GetVersion(r.Context(), principal, r.PathValue("versionId"))
		if err != nil {
			writeResourceModelError(w, err, "resource_model_version_load_failed")
			return
		}
		if !ifMatchMatches(r, current.SchemaChecksum) {
			writeError(w, http.StatusPreconditionRequired, "if_match_required")
			return
		}
		var input resourcemodel.VersionPatchInput
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256*1024))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil {
			writeError(w, http.StatusUnprocessableEntity, "validation_failed")
			return
		}
		result, err := deps.ResourceModelService.PatchVersion(r.Context(), principal, r.PathValue("versionId"), r.Header.Get("If-Match"), input)
		if err != nil {
			writeResourceModelError(w, err, "resource_model_version_update_failed")
			return
		}
		writeETag(w, result.SchemaChecksum)
		writeJSON(w, http.StatusOK, result)
	}
}

func resourceModelVersionResource(deps Dependencies) http.HandlerFunc {
	get := getResourceModelVersion(deps)
	patch := patchResourceModelVersion(deps)
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			get(w, r)
			return
		}
		patch(w, r)
	}
}

type migrationRequest struct {
	SourceVersionID string         `json:"source_version_id"`
	TargetVersionID string         `json:"target_version_id"`
	AssetScope      map[string]any `json:"asset_scope"`
	Mapping         map[string]any `json:"mapping"`
	Defaults        map[string]any `json:"defaults"`
}

func toMigrationInput(input migrationRequest) resourcemodel.MigrationInput {
	return resourcemodel.MigrationInput{SourceVersionID: input.SourceVersionID, TargetVersionID: input.TargetVersionID, AssetScope: input.AssetScope, Mapping: input.Mapping, Defaults: input.Defaults}
}

func previewResourceModelMigration(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, ok := requireMemberSession(w, r, deps)
		if !ok {
			return
		}
		var input migrationRequest
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 128*1024))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil {
			writeError(w, http.StatusUnprocessableEntity, "validation_failed")
			return
		}
		result, err := deps.ResourceModelService.PreviewMigration(r.Context(), principal, r.PathValue("resourceModelId"), toMigrationInput(input))
		if err != nil {
			writeResourceModelError(w, err, "resource_model_migration_preview_failed")
			return
		}
		writeJSON(w, http.StatusOK, result)
	}
}

func startResourceModelMigration(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, ok := requireMemberSession(w, r, deps)
		if !ok {
			return
		}
		key, ok := requiredIdempotencyKey(r)
		if !ok {
			writeError(w, http.StatusUnprocessableEntity, "idempotency_key_required")
			return
		}
		var input migrationRequest
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 128*1024))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil {
			writeError(w, http.StatusUnprocessableEntity, "validation_failed")
			return
		}
		result, err := deps.ResourceModelService.StartMigration(r.Context(), principal, r.PathValue("resourceModelId"), key, toMigrationInput(input))
		if err != nil {
			writeResourceModelError(w, err, "resource_model_migration_start_failed")
			return
		}
		writeJSON(w, http.StatusAccepted, result)
	}
}

func getResourceModelMigration(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, ok := requireMemberSession(w, r, deps)
		if !ok {
			return
		}
		result, err := deps.ResourceModelService.GetMigration(r.Context(), principal, r.PathValue("migrationId"))
		if err != nil {
			writeResourceModelError(w, err, "resource_model_migration_load_failed")
			return
		}
		writeJSON(w, http.StatusOK, result)
	}
}

func cancelResourceModelMigration(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, ok := requireMemberSession(w, r, deps)
		if !ok {
			return
		}
		if _, ok := requiredIdempotencyKey(r); !ok {
			writeError(w, http.StatusUnprocessableEntity, "idempotency_key_required")
			return
		}
		result, err := deps.ResourceModelService.CancelMigration(r.Context(), principal, r.PathValue("migrationId"))
		if err != nil {
			writeResourceModelError(w, err, "resource_model_migration_cancel_failed")
			return
		}
		writeJSON(w, http.StatusOK, result)
	}
}

func validateResourceModelVersion(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, ok := requireMemberSession(w, r, deps)
		if !ok {
			return
		}
		if _, ok := requiredIdempotencyKey(r); !ok {
			writeError(w, http.StatusUnprocessableEntity, "idempotency_key_required")
			return
		}
		result, err := deps.ResourceModelService.ValidateVersion(r.Context(), principal, r.PathValue("versionId"))
		if err != nil {
			writeResourceModelError(w, err, "resource_model_version_validate_failed")
			return
		}
		writeETag(w, result.SchemaChecksum)
		writeJSON(w, http.StatusOK, result)
	}
}

func publishResourceModelVersion(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, ok := requireMemberSession(w, r, deps)
		if !ok {
			return
		}
		if _, ok := requiredIdempotencyKey(r); !ok {
			writeError(w, http.StatusUnprocessableEntity, "idempotency_key_required")
			return
		}
		result, err := deps.ResourceModelService.PublishVersion(r.Context(), principal, r.PathValue("versionId"))
		if err != nil {
			writeResourceModelError(w, err, "resource_model_version_publish_failed")
			return
		}
		writeETag(w, result.SchemaChecksum)
		writeJSON(w, http.StatusOK, result)
	}
}

func retireResourceModelVersion(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, ok := requireMemberSession(w, r, deps)
		if !ok {
			return
		}
		if _, ok := requiredIdempotencyKey(r); !ok {
			writeError(w, http.StatusUnprocessableEntity, "idempotency_key_required")
			return
		}
		result, err := deps.ResourceModelService.RetireVersion(r.Context(), principal, r.PathValue("versionId"))
		if err != nil {
			writeResourceModelError(w, err, "resource_model_version_retire_failed")
			return
		}
		writeETag(w, result.SchemaChecksum)
		writeJSON(w, http.StatusOK, result)
	}
}
