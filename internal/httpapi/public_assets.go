package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	agentquery "agentchunzhi/internal/query"

	"github.com/jackc/pgx/v5"
)

// PublicAsset is deliberately a read-only projection. It never exposes
// workspace membership, source credentials, audit metadata, or draft fields.
type PublicAsset struct {
	ID                   string         `json:"id"`
	WorkspaceID          string         `json:"workspace_id"`
	ResourceModelID      string         `json:"resource_model_id"`
	ContentKind          string         `json:"content_kind"`
	ResourceModelVersion string         `json:"resource_model_version_id"`
	Title                *string        `json:"title"`
	Markdown             *string        `json:"markdown,omitempty"`
	Fields               map[string]any `json:"fields"`
	Tags                 []string       `json:"tags"`
	Visibility           string         `json:"visibility"`
	PublicationStatus    string         `json:"publication_status"`
	UpdatedAt            string         `json:"updated_at"`
}

func publicAssets(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		if deps.Store == nil || deps.Store.Pool == nil {
			writeError(w, http.StatusServiceUnavailable, "database_unavailable")
			return
		}
		workspaceID := strings.TrimSpace(r.PathValue("workspaceId"))
		if !agentquery.ValidUUID(workspaceID) {
			writeError(w, http.StatusNotFound, "workspace_not_found")
			return
		}
		limit := 50
		if raw := r.URL.Query().Get("limit"); raw != "" {
			parsed, err := strconv.Atoi(raw)
			if err != nil || parsed < 1 || parsed > 100 {
				writeError(w, http.StatusUnprocessableEntity, "validation_failed")
				return
			}
			limit = parsed
		}
		query := strings.TrimSpace(r.URL.Query().Get("q"))
		rows, err := deps.Store.Pool.Query(r.Context(), `
			SELECT a.id::text, a.workspace_id::text, a.resource_model_id::text,
			       rm.content_kind, v.resource_model_version_id::text, v.title,
			       v.markdown, v.fields, v.tags, a.visibility,
			       a.publication_status, a.updated_at::text
			FROM asset.assets a
			JOIN asset.asset_versions v ON v.id = a.current_published_version_id
			JOIN model.resource_models rm ON rm.id = a.resource_model_id
			WHERE a.workspace_id = $1::uuid AND a.visibility = 'public'
			  AND a.publication_status = 'published' AND a.deleted_at IS NULL
			  AND ($2 = '' OR v.title ILIKE '%' || $2 || '%' OR v.markdown ILIKE '%' || $2 || '%')
			ORDER BY a.updated_at DESC, a.id DESC
			LIMIT $3`, workspaceID, query, limit)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "public_asset_list_failed")
			return
		}
		defer rows.Close()
		items := make([]PublicAsset, 0, limit)
		for rows.Next() {
			item, scanErr := scanPublicAsset(rows)
			if scanErr != nil {
				writeError(w, http.StatusInternalServerError, "public_asset_list_failed")
				return
			}
			items = append(items, item)
		}
		if err := rows.Err(); err != nil {
			writeError(w, http.StatusInternalServerError, "public_asset_list_failed")
			return
		}
		w.Header().Set("Cache-Control", "public, max-age=30")
		writeJSON(w, http.StatusOK, map[string]any{"items": items, "has_more": len(items) == limit})
	}
}

func publicAsset(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		if deps.Store == nil || deps.Store.Pool == nil {
			writeError(w, http.StatusServiceUnavailable, "database_unavailable")
			return
		}
		assetID := strings.TrimSpace(r.PathValue("assetId"))
		if !agentquery.ValidUUID(assetID) {
			writeError(w, http.StatusNotFound, "asset_not_found")
			return
		}
		row := deps.Store.Pool.QueryRow(r.Context(), `
			SELECT a.id::text, a.workspace_id::text, a.resource_model_id::text,
			       rm.content_kind, v.resource_model_version_id::text, v.title,
			       v.markdown, v.fields, v.tags, a.visibility,
			       a.publication_status, a.updated_at::text
			FROM asset.assets a
			JOIN asset.asset_versions v ON v.id = a.current_published_version_id
			JOIN model.resource_models rm ON rm.id = a.resource_model_id
			WHERE a.id = $1::uuid AND a.visibility = 'public'
			  AND a.publication_status = 'published' AND a.deleted_at IS NULL`, assetID)
		item, err := scanPublicAsset(row)
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "asset_not_found")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "public_asset_load_failed")
			return
		}
		w.Header().Set("Cache-Control", "public, max-age=30")
		writeJSON(w, http.StatusOK, item)
	}
}

func scanPublicAsset(row interface{ Scan(...any) error }) (PublicAsset, error) {
	var item PublicAsset
	var fields, tags []byte
	if err := row.Scan(&item.ID, &item.WorkspaceID, &item.ResourceModelID,
		&item.ContentKind, &item.ResourceModelVersion, &item.Title, &item.Markdown,
		&fields, &tags, &item.Visibility, &item.PublicationStatus, &item.UpdatedAt); err != nil {
		return PublicAsset{}, err
	}
	item.Fields = decodePublicMap(fields)
	item.Tags = decodePublicStrings(tags)
	return item, nil
}

func decodePublicMap(raw []byte) map[string]any {
	result := map[string]any{}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &result)
	}
	return result
}

func decodePublicStrings(raw []byte) []string {
	result := []string{}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &result)
	}
	return result
}
