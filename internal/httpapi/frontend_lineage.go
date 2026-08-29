package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
)

func assetLineage(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, ok := requireMemberSession(w, r, deps)
		if !ok {
			return
		}
		assetID := strings.TrimSpace(r.PathValue("assetId"))
		asset, err := deps.MemberAssetService.Get(r.Context(), principal, assetID)
		if err != nil {
			writeMemberAssetError(w, err, "asset_lineage_failed")
			return
		}
		versions, err := loadAssetLineageVersions(r, deps, principal.OrganizationID, asset.ID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "asset_lineage_failed")
			return
		}
		rawInputs, err := loadRawInputs(r, deps, principal.OrganizationID, asset.ID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "asset_lineage_failed")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"asset": asset, "versions": versions, "raw_inputs": rawInputs})
	}
}

func assetVersionProcessing(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, ok := requireMemberSession(w, r, deps)
		if !ok {
			return
		}
		versionID := strings.TrimSpace(r.PathValue("versionId"))
		var assetID string
		if err := deps.Store.Pool.QueryRow(r.Context(), `SELECT asset_id::text FROM asset.asset_versions WHERE id = $1::uuid AND organization_id = $2::uuid`, versionID, principal.OrganizationID).Scan(&assetID); errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "asset_version_not_found")
			return
		} else if err != nil {
			writeError(w, http.StatusInternalServerError, "processing_load_failed")
			return
		}
		if _, err := deps.MemberAssetService.Get(r.Context(), principal, assetID); err != nil {
			writeMemberAssetError(w, err, "processing_load_failed")
			return
		}
		jobs, err := loadProcessingJobs(r, deps, principal.OrganizationID, assetID, versionID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "processing_load_failed")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"asset_version_id": versionID, "processing_jobs": jobs})
	}
}

func loadAssetLineageVersions(r *http.Request, deps Dependencies, organizationID, assetID string) ([]map[string]any, error) {
	rows, err := deps.Store.Pool.Query(r.Context(), `
		SELECT id::text, version_no, origin, confirmation_status, title, markdown,
		       source_raw_input_id::text, parent_version_id::text, content_checksum, created_at::text
		FROM asset.asset_versions
		WHERE organization_id = $1::uuid AND asset_id = $2::uuid
		ORDER BY version_no`, organizationID, assetID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]map[string]any, 0)
	for rows.Next() {
		var id, status, quality, checksum, created string
		var versionNo int
		var title, markdown, rawID, parentID *string
		if err := rows.Scan(&id, &versionNo, &status, &quality, &title, &markdown, &rawID, &parentID, &checksum, &created); err != nil {
			return nil, err
		}
		items = append(items, map[string]any{"id": id, "version_no": versionNo, "origin": status, "confirmation_status": quality, "title": title, "markdown": markdown, "source_raw_input_id": rawID, "parent_version_id": parentID, "content_checksum": checksum, "created_at": created})
	}
	return items, rows.Err()
}

func loadRawInputs(r *http.Request, deps Dependencies, organizationID, assetID string) ([]map[string]any, error) {
	rows, err := deps.Store.Pool.Query(r.Context(), `
		SELECT DISTINCT ri.id::text, ri.source_type, COALESCE(ri.content_type, ''), ri.payload, ri.content_checksum, ri.created_at::text
		FROM asset.raw_inputs ri
		JOIN asset.asset_versions av ON av.source_raw_input_id = ri.id
		WHERE ri.organization_id = $1::uuid AND av.asset_id = $2::uuid
                ORDER BY ri.created_at::text`, organizationID, assetID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]map[string]any, 0)
	for rows.Next() {
		var id, sourceType, contentType, checksum, created string
		var payload []byte
		if err := rows.Scan(&id, &sourceType, &contentType, &payload, &checksum, &created); err != nil {
			return nil, err
		}
		var decoded any
		_ = json.Unmarshal(payload, &decoded)
		items = append(items, map[string]any{"id": id, "source_type": sourceType, "content_type": contentType, "payload": decoded, "content_checksum": checksum, "created_at": created})
	}
	return items, rows.Err()
}

func loadProcessingJobs(r *http.Request, deps Dependencies, organizationID, assetID, versionID string) ([]map[string]any, error) {
	rows, err := deps.Store.Pool.Query(r.Context(), `
		SELECT id::text, workspace_id::text, job_type, source_type, source_id::text,
		       status, input_snapshot, output_snapshot, error_code, created_at::text,
		       started_at::text, completed_at::text
		FROM content.processing_jobs
		WHERE organization_id = $1::uuid AND source_id IN ($2::uuid, $3::uuid)
		ORDER BY created_at DESC`, organizationID, assetID, versionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]map[string]any, 0)
	for rows.Next() {
		var id, workspaceID, jobType, sourceType, sourceID, status, created string
		var errorCode *string
		var input, output []byte
		var started, completed *string
		if err := rows.Scan(&id, &workspaceID, &jobType, &sourceType, &sourceID, &status, &input, &output, &errorCode, &created, &started, &completed); err != nil {
			return nil, err
		}
		var inputValue, outputValue any
		_ = json.Unmarshal(input, &inputValue)
		_ = json.Unmarshal(output, &outputValue)
		items = append(items, map[string]any{"id": id, "workspace_id": workspaceID, "job_type": jobType, "source_type": sourceType, "source_id": sourceID, "status": status, "input_snapshot": inputValue, "output_snapshot": outputValue, "error_code": errorCode, "created_at": created, "started_at": started, "completed_at": completed})
	}
	return items, rows.Err()
}
