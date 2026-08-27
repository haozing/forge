package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"agentchunzhi/internal/authz"
	"agentchunzhi/internal/eventing"
	"agentchunzhi/internal/retrieval"

	"github.com/jackc/pgx/v5"
)

func requireRetrievalScope(w http.ResponseWriter, r *http.Request, deps Dependencies, workspaceID, action string) (authz.Scope, bool) {
	principal, ok := requireMemberSession(w, r, deps)
	if !ok {
		return authz.Scope{}, false
	}
	if deps.WorkspacePolicy == nil {
		writeError(w, http.StatusForbidden, "workspace_access_denied")
		return authz.Scope{}, false
	}
	scope, err := deps.WorkspacePolicy.Require(r.Context(), principal, workspaceID, "", action)
	if errors.Is(err, authz.ErrWorkspaceNotFound) {
		writeError(w, http.StatusNotFound, "workspace_not_found")
		return authz.Scope{}, false
	}
	if errors.Is(err, authz.ErrWorkspaceForbidden) {
		writeError(w, http.StatusForbidden, "workspace_access_denied")
		return authz.Scope{}, false
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "authorization_scope_failed")
		return authz.Scope{}, false
	}
	return scope, true
}

func retrievalIndexStatus(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		workspaceID := r.PathValue("workspaceId")
		if _, ok := requireRetrievalScope(w, r, deps, workspaceID, "asset.read"); !ok {
			return
		}
		modelID := strings.TrimSpace(r.URL.Query().Get("resource_model_id"))
		rows, err := deps.Store.Pool.Query(r.Context(), `
			SELECT pr.resource_model_id::text, pr.asset_version_id::text, pr.status,
			       COALESCE(pr.error_code, ''), pr.expected_chunk_count,
			       pr.ready_chunk_count, pr.expected_embedding_count,
			       pr.ready_embedding_count, pr.updated_at::text
			FROM retrieval.projection_runs pr
			JOIN asset.asset_versions av ON av.id = pr.asset_version_id
			JOIN asset.assets a ON a.id = av.asset_id
			WHERE pr.organization_id = (SELECT organization_id FROM content.workspaces WHERE id = $1::uuid)
			  AND a.workspace_id = $1::uuid
			  AND ($2 = '' OR pr.resource_model_id = $2::uuid)
			ORDER BY pr.updated_at DESC, pr.id DESC LIMIT 200`, workspaceID, modelID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "index_status_failed")
			return
		}
		defer rows.Close()
		items := make([]map[string]any, 0)
		for rows.Next() {
			var model, version, status, code, updated string
			var expectedChunks, readyChunks, expectedEmbeddings, readyEmbeddings int
			if err := rows.Scan(&model, &version, &status, &code, &expectedChunks, &readyChunks, &expectedEmbeddings, &readyEmbeddings, &updated); err != nil {
				writeError(w, http.StatusInternalServerError, "index_status_failed")
				return
			}
			items = append(items, map[string]any{"resource_model_id": model, "asset_version_id": version, "status": status, "error_code": code, "expected_chunk_count": expectedChunks, "ready_chunk_count": readyChunks, "expected_embedding_count": expectedEmbeddings, "ready_embedding_count": readyEmbeddings, "updated_at": updated})
		}
		if err := rows.Err(); err != nil {
			writeError(w, http.StatusInternalServerError, "index_status_failed")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": items, "has_more": false})
	}
}

type retrievalRebuildRequest struct {
	AssetIDs []string `json:"asset_ids"`
}

func rebuildRetrievalIndex(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, ok := requireMemberSession(w, r, deps)
		if !ok {
			return
		}
		if deps.WorkspacePolicy == nil {
			writeError(w, http.StatusForbidden, "workspace_access_denied")
			return
		}
		workspaceID := r.PathValue("workspaceId")
		if _, err := deps.WorkspacePolicy.Require(r.Context(), principal, workspaceID, "", "asset.write"); err != nil {
			writeError(w, http.StatusForbidden, "workspace_access_denied")
			return
		}
		var input retrievalRebuildRequest
		if r.Body != nil && r.ContentLength != 0 {
			if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64*1024)).Decode(&input); err != nil {
				writeError(w, http.StatusUnprocessableEntity, "validation_failed")
				return
			}
		}
		rows, err := deps.Store.Pool.Query(r.Context(), `
			SELECT id::text, current_published_version_id::text
			FROM asset.assets
			WHERE workspace_id = $1::uuid AND organization_id = $2::uuid
			  AND publication_status = 'published' AND current_published_version_id IS NOT NULL
			  AND ($3::text[] IS NULL OR id = ANY($3::uuid[]))`, workspaceID, principal.OrganizationID, nullableIDs(input.AssetIDs))
		if err != nil {
			writeError(w, http.StatusInternalServerError, "index_rebuild_failed")
			return
		}
		versionIDs := make([]string, 0, 32)
		for rows.Next() {
			var assetID, versionID string
			if err := rows.Scan(&assetID, &versionID); err != nil {
				rows.Close()
				writeError(w, http.StatusInternalServerError, "index_rebuild_failed")
				return
			}
			versionIDs = append(versionIDs, versionID)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			writeError(w, http.StatusInternalServerError, "index_rebuild_failed")
			return
		}
		rows.Close()
		if len(versionIDs) == 0 {
			writeJSON(w, http.StatusAccepted, map[string]any{"queued": 0})
			return
		}
		tx, txErr := deps.Store.Pool.Begin(r.Context())
		if txErr != nil {
			writeError(w, http.StatusInternalServerError, "index_rebuild_failed")
			return
		}
		for _, versionID := range versionIDs {
			if _, txErr = deps.AssetService.Events.AppendTx(r.Context(), tx, eventing.Event{OrganizationID: principal.OrganizationID, EventType: "asset.retrieval_projection_requested", AggregateType: "asset_version", AggregateID: versionID, AggregateVersion: 1, PayloadVersion: 1, Payload: map[string]string{"asset_version_id": versionID, "operation": retrieval.ProjectionRebuild}}); txErr != nil {
				_ = tx.Rollback(r.Context())
				writeError(w, http.StatusInternalServerError, "index_rebuild_failed")
				return
			}
		}
		if txErr = tx.Commit(r.Context()); txErr != nil {
			_ = tx.Rollback(r.Context())
			writeError(w, http.StatusInternalServerError, "index_rebuild_failed")
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]any{"queued": len(versionIDs)})
	}
}

func retryAssetIndex(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, ok := requireMemberSession(w, r, deps)
		if !ok {
			return
		}
		assetID := r.PathValue("assetId")
		var workspaceID, versionID string
		if err := deps.Store.Pool.QueryRow(r.Context(), `SELECT workspace_id::text, current_published_version_id::text FROM asset.assets WHERE id = $1::uuid AND organization_id = $2::uuid AND publication_status = 'published'`, assetID, principal.OrganizationID).Scan(&workspaceID, &versionID); errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "asset_not_found")
			return
		} else if err != nil {
			writeError(w, http.StatusInternalServerError, "index_retry_failed")
			return
		}
		if _, ok := requireRetrievalScope(w, r, deps, workspaceID, "asset.write"); !ok {
			return
		}
		tx, txErr := deps.Store.Pool.Begin(r.Context())
		if txErr != nil {
			writeError(w, http.StatusInternalServerError, "index_retry_failed")
			return
		}
		_, txErr = deps.AssetService.Events.AppendTx(r.Context(), tx, eventing.Event{OrganizationID: principal.OrganizationID, EventType: "asset.retrieval_projection_requested", AggregateType: "asset_version", AggregateID: versionID, AggregateVersion: 1, PayloadVersion: 1, Payload: map[string]string{"asset_version_id": versionID, "operation": retrieval.ProjectionRebuild}})
		if txErr == nil {
			txErr = tx.Commit(r.Context())
		} else {
			_ = tx.Rollback(r.Context())
		}
		if txErr != nil {
			writeError(w, http.StatusInternalServerError, "index_retry_failed")
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]any{"asset_id": assetID, "asset_version_id": versionID, "status": "queued"})
	}
}

func queryAuditLogs(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, ok := requireMemberSession(w, r, deps)
		if !ok {
			return
		}
		workspaceID := r.PathValue("workspaceId")
		if _, ok := requireRetrievalScope(w, r, deps, workspaceID, "audit.read"); !ok {
			return
		}
		rows, err := deps.Store.Pool.Query(r.Context(), `
			SELECT id::text, endpoint, query_hash, result_count, outcome, latency_ms, created_at::text
			FROM retrieval.query_logs
			WHERE organization_id = $1::uuid AND actor_user_id = $2::uuid
			ORDER BY created_at DESC, id DESC LIMIT 100`, principal.OrganizationID, principal.UserID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "query_audit_failed")
			return
		}
		defer rows.Close()
		items := make([]map[string]any, 0)
		for rows.Next() {
			var id, endpoint, hash, outcome, created string
			var count, latency int
			if err := rows.Scan(&id, &endpoint, &hash, &count, &outcome, &latency, &created); err != nil {
				writeError(w, http.StatusInternalServerError, "query_audit_failed")
				return
			}
			items = append(items, map[string]any{"id": id, "endpoint": endpoint, "query_hash": hash, "result_count": count, "outcome": outcome, "latency_ms": latency, "created_at": created})
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": items, "has_more": false})
	}
}

func nullableIDs(ids []string) []string {
	if len(ids) == 0 {
		return nil
	}
	return ids
}
