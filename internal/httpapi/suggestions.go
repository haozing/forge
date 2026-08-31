package httpapi

// suggestions.go — the phase 4 member review surface: the unified
// suggestion queue, single and batch accept/reject decisions, agent
// processing results and member-initiated asset preparation. Handlers only
// authenticate, parse, call the suggestion review service and map errors.
// The two surfaces that cross package boundaries owned by parallel waves
// (processing-result reads and automation.run creation) keep their SQL here
// on purpose: the automation/agenttask packages are being reshaped and the
// member prepare run is exactly one bounded INSERT plus application pick.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"agentchunzhi/internal/asset"
	"agentchunzhi/internal/auth"
	"agentchunzhi/internal/authz"

	"github.com/jackc/pgx/v5"
)

// requireSuggestionService answers 500 when the review service is not wired;
// only misconfigured process bootstrapping can hit this.
func requireSuggestionService(w http.ResponseWriter, deps Dependencies) bool {
	if deps.SuggestionReviews == nil {
		writeError(w, http.StatusInternalServerError, "internal_error")
		return false
	}
	return true
}

// requireHTTPStore guards the two handlers that read/write the store
// directly: an unwired store is a deployment error, never a client error.
func requireHTTPStore(w http.ResponseWriter, deps Dependencies) bool {
	if deps.Store == nil || deps.Store.Pool == nil {
		writeError(w, http.StatusInternalServerError, "internal_error")
		return false
	}
	return true
}

// suggestionScopeTable maps a wire suggestion kind onto the table that stores
// it; field and summary share one table.
func suggestionScopeTable(kind string) string {
	switch kind {
	case asset.SuggestionKindField, asset.SuggestionKindSummary:
		return "asset.asset_field_suggestions"
	case asset.SuggestionKindTag:
		return "asset.asset_version_tag_suggestions"
	case asset.SuggestionKindRelation:
		return "asset.asset_relation_suggestions"
	default:
		return ""
	}
}

// resolveSuggestionAsset finds the asset a suggestion belongs to. The accept
// and reject routes address suggestions without an asset segment, but the
// review service scopes every decision to (workspace, asset): this lookup
// re-derives that pair from the suggestion's source version. A suggestion
// from another workspace hides as suggestion_not_found.
func resolveSuggestionAsset(ctx context.Context, deps Dependencies, principal auth.Principal, workspaceID, kind, suggestionID string) (string, error) {
	if deps.Store == nil || deps.Store.Pool == nil {
		return "", errors.New("database store is not initialized")
	}
	table := suggestionScopeTable(kind)
	if table == "" {
		return "", asset.ErrSuggestionKindInvalid
	}
	var assetID string
	err := deps.Store.Pool.QueryRow(ctx, `
		SELECT v.asset_id::text
		FROM `+table+` s
		JOIN asset.asset_versions v
		  ON v.organization_id = s.organization_id AND v.id = s.source_version_id
		WHERE s.organization_id = $1::uuid AND s.id = $2::uuid AND v.workspace_id = $3::uuid
	`, principal.OrganizationID, suggestionID, workspaceID).Scan(&assetID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", asset.ErrSuggestionNotFound
	}
	if err != nil {
		return "", err
	}
	return assetID, nil
}

// ---------- suggestion queue ----------

// AssetSuggestions serves GET /api/workspaces/{workspaceId}/assets/{assetId}/suggestions:
// the unified pending/decided queue plus the recent processing-result header.
func AssetSuggestions(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		principal, ok := sessionPrincipal(w, r, deps)
		if !ok {
			return
		}
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		if !requireSuggestionService(w, deps) {
			return
		}
		workspaceID := r.PathValue("workspaceId")
		assetID := r.PathValue("assetId")
		if !requirePathUUID(w, workspaceID, assetID) {
			return
		}
		page, err := deps.SuggestionReviews.List(r.Context(), principal, workspaceID, assetID,
			r.URL.Query().Get("status"), r.URL.Query().Get("run_id"),
			r.URL.Query().Get("cursor"), atoiDefault(r.URL.Query().Get("limit"), 50))
		if err != nil {
			ServiceError(w, err)
			return
		}
		writeData(w, r, http.StatusOK, map[string]any{
			"items":              page.Items,
			"page":               cursorPageFrom(page.HasMore, page.NextCursor),
			"processing_results": page.ProcessingResults,
		})
	}
}

// cursorPageFrom adapts a service-computed keyset slice onto the shared
// CursorPage envelope.
func cursorPageFrom(hasMore bool, next string) CursorPage {
	page := CursorPage{HasMore: hasMore}
	if next != "" {
		page.NextCursor = &next
	}
	return page
}

// ---------- single decisions ----------

// SuggestionDecisionBody carries the optional tag resolution of an accept:
// tag_id overrides the suggested key with an explicit workspace tag.
type SuggestionDecisionBody struct {
	TagID string `json:"tag_id"`
}

// SuggestionAccept serves POST /api/workspaces/{workspaceId}/suggestions/{kind}/{suggestionId}/accept.
// The kind segment is the wire vocabulary (field/summary/tag/relation); the
// service maps it onto the suggestion tables.
func SuggestionAccept(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		principal, ok := sessionPrincipal(w, r, deps)
		if !ok {
			return
		}
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		if _, ok := requireIdempotencyKey(w, r); !ok {
			return
		}
		if !requireSuggestionService(w, deps) {
			return
		}
		workspaceID := r.PathValue("workspaceId")
		kind := r.PathValue("kind")
		suggestionID := r.PathValue("suggestionId")
		if !requirePathUUID(w, workspaceID, suggestionID) {
			return
		}
		if !asset.ValidSuggestionKind(kind) {
			writeError(w, http.StatusUnprocessableEntity, "suggestion_kind_invalid")
			return
		}
		var body SuggestionDecisionBody
		if r.ContentLength != 0 {
			if !decodeBody(w, r, &body, 16*1024) {
				return
			}
		}
		assetID, err := resolveSuggestionAsset(r.Context(), deps, principal, workspaceID, kind, suggestionID)
		if err != nil {
			ServiceError(w, err)
			return
		}
		outcome, err := deps.SuggestionReviews.Accept(r.Context(), principal, workspaceID, assetID, kind, suggestionID, body.TagID)
		if err != nil {
			ServiceError(w, err)
			return
		}
		writeData(w, r, http.StatusOK, outcome)
	}
}

// SuggestionReject serves POST /api/workspaces/{workspaceId}/suggestions/{kind}/{suggestionId}/reject.
func SuggestionReject(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		principal, ok := sessionPrincipal(w, r, deps)
		if !ok {
			return
		}
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		if _, ok := requireIdempotencyKey(w, r); !ok {
			return
		}
		if !requireSuggestionService(w, deps) {
			return
		}
		workspaceID := r.PathValue("workspaceId")
		kind := r.PathValue("kind")
		suggestionID := r.PathValue("suggestionId")
		if !requirePathUUID(w, workspaceID, suggestionID) {
			return
		}
		if !asset.ValidSuggestionKind(kind) {
			writeError(w, http.StatusUnprocessableEntity, "suggestion_kind_invalid")
			return
		}
		assetID, err := resolveSuggestionAsset(r.Context(), deps, principal, workspaceID, kind, suggestionID)
		if err != nil {
			ServiceError(w, err)
			return
		}
		if err := deps.SuggestionReviews.Reject(r.Context(), principal, workspaceID, assetID, kind, suggestionID); err != nil {
			ServiceError(w, err)
			return
		}
		writeData(w, r, http.StatusOK, map[string]any{
			"suggestion_id": suggestionID,
			"kind":          kind,
			"status":        asset.SuggestionStatusRejected,
		})
	}
}

// ---------- batch accept ----------

// AcceptBatchRef is one wire entry of the explicit-ids batch form. Only the
// explicit ids variant is implemented; the whole-queue filter form is deferred to a
// follow-up batch.
type AcceptBatchRef struct {
	Kind  string `json:"kind"`
	ID    string `json:"id"`
	TagID string `json:"tag_id,omitempty"`
}

type AcceptBatchBody struct {
	IDs []AcceptBatchRef `json:"ids"`
}

// AssetSuggestionsAcceptBatch serves POST /api/workspaces/{workspaceId}/assets/{assetId}/suggestions/accept-batch:
// one transaction, one draft revision for every accepted entry.
func AssetSuggestionsAcceptBatch(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		principal, ok := sessionPrincipal(w, r, deps)
		if !ok {
			return
		}
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		if _, ok := requireIdempotencyKey(w, r); !ok {
			return
		}
		if !requireSuggestionService(w, deps) {
			return
		}
		workspaceID := r.PathValue("workspaceId")
		assetID := r.PathValue("assetId")
		if !requirePathUUID(w, workspaceID, assetID) {
			return
		}
		var body AcceptBatchBody
		if !decodeBody(w, r, &body, 256*1024) {
			return
		}
		if len(body.IDs) == 0 || len(body.IDs) > asset.MaxAcceptBatchSize {
			writeError(w, http.StatusUnprocessableEntity, "validation_failed")
			return
		}
		refs := make([]asset.AcceptRef, 0, len(body.IDs))
		for _, entry := range body.IDs {
			refs = append(refs, asset.AcceptRef{
				Kind:          entry.Kind,
				SuggestionID:  entry.ID,
				OverrideTagID: entry.TagID,
			})
		}
		outcome, err := deps.SuggestionReviews.AcceptBatch(r.Context(), principal, workspaceID, assetID, refs)
		if err != nil {
			ServiceError(w, err)
			return
		}
		writeData(w, r, http.StatusOK, outcome)
	}
}

// ---------- processing results ----------

// ProcessingResult is one agent run record over the asset: input/output
// versions, rule version, suggestion summary, field diff, confidence,
// citations and token usage.
type ProcessingResult struct {
	ID                 string          `json:"id"`
	RunID              string          `json:"run_id"`
	InputVersionID     string          `json:"input_version_id"`
	OutputVersionID    string          `json:"output_version_id,omitempty"`
	AgentUserID        string          `json:"agent_user_id"`
	AgentApplicationID string          `json:"agent_application_id"`
	RuleVersion        string          `json:"rule_version"`
	SuggestionSummary  json.RawMessage `json:"suggestion_summary,omitempty"`
	FieldDiff          json.RawMessage `json:"field_diff,omitempty"`
	OverallConfidence  *float64        `json:"overall_confidence,omitempty"`
	Citations          json.RawMessage `json:"citations,omitempty"`
	InputTokens        int             `json:"input_tokens"`
	OutputTokens       int             `json:"output_tokens"`
	CreatedAt          time.Time       `json:"created_at"`
	CompletedAt        *time.Time      `json:"completed_at,omitempty"`
}

const maxProcessingResultPageSize = 100

type processingResultCursor struct {
	CreatedAt string `json:"created_at"`
	ID        string `json:"id"`
}

func encodeProcessingResultCursor(item ProcessingResult) string {
	raw, _ := json.Marshal(processingResultCursor{
		CreatedAt: item.CreatedAt.UTC().Format(time.RFC3339Nano),
		ID:        item.ID,
	})
	return base64.RawURLEncoding.EncodeToString(raw)
}

func decodeProcessingResultCursor(value string) (processingResultCursor, error) {
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return processingResultCursor{}, asset.ErrInvalidInput
	}
	var cursor processingResultCursor
	if err := json.Unmarshal(raw, &cursor); err != nil {
		return processingResultCursor{}, asset.ErrInvalidInput
	}
	if cursor.ID == "" {
		return processingResultCursor{}, asset.ErrInvalidInput
	}
	parsed, err := time.Parse(time.RFC3339Nano, cursor.CreatedAt)
	if err != nil || parsed.IsZero() {
		return processingResultCursor{}, asset.ErrInvalidInput
	}
	return cursor, nil
}

// AssetProcessingResults serves GET /api/workspaces/{workspaceId}/assets/{assetId}/processing-results.
// Ownership follows the two-stage gate of the draft surface: resolve the
// asset's workspace with asset.read, hide cross-workspace asset ids as 404,
// then page integration.agent_processing_results by (created_at, id) desc.
func AssetProcessingResults(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		principal, ok := sessionPrincipal(w, r, deps)
		if !ok {
			return
		}
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		if !requireHTTPStore(w, deps) {
			return
		}
		workspaceID := r.PathValue("workspaceId")
		assetID := r.PathValue("assetId")
		if !requirePathUUID(w, workspaceID, assetID) {
			return
		}
		target, err := deps.MemberAssetService.Get(r.Context(), principal, assetID)
		if err != nil {
			ServiceError(w, err)
			return
		}
		if target.WorkspaceID != workspaceID {
			writeError(w, http.StatusNotFound, "resource_not_found")
			return
		}
		limit := atoiDefault(r.URL.Query().Get("limit"), 20)
		if limit > maxProcessingResultPageSize {
			limit = maxProcessingResultPageSize
		}
		var cursorTime any
		cursorID := ""
		if raw := r.URL.Query().Get("cursor"); raw != "" {
			cursor, err := decodeProcessingResultCursor(raw)
			if err != nil {
				ServiceError(w, err)
				return
			}
			parsed, _ := time.Parse(time.RFC3339Nano, cursor.CreatedAt)
			cursorTime = parsed
			cursorID = cursor.ID
		}
		rows, err := deps.Store.Pool.Query(r.Context(), `
			SELECT r.id::text, r.run_id::text, r.input_version_id::text, COALESCE(r.output_version_id::text, ''),
			       r.agent_user_id::text, r.agent_application_id::text, r.rule_version,
			       r.suggestion_summary, r.field_diff, r.overall_confidence, r.citations,
			       r.input_tokens, r.output_tokens, r.created_at, r.completed_at
			FROM integration.agent_processing_results r
			JOIN asset.assets a
			  ON a.organization_id = r.organization_id AND a.id = r.asset_id
			 AND a.workspace_id = $3::uuid AND a.deleted_at IS NULL
			WHERE r.organization_id = $1::uuid AND r.asset_id = $2::uuid
			  AND ($4::timestamptz IS NULL OR (r.created_at, r.id::text) < ($4::timestamptz, $5::text))
			ORDER BY r.created_at DESC, r.id DESC
			LIMIT $6::int
		`, principal.OrganizationID, assetID, workspaceID, cursorTime, cursorID, limit+1)
		if err != nil {
			ServiceError(w, err)
			return
		}
		defer rows.Close()
		items := []ProcessingResult{}
		for rows.Next() {
			var item ProcessingResult
			var summary, diff, citations []byte
			if err := rows.Scan(&item.ID, &item.RunID, &item.InputVersionID, &item.OutputVersionID,
				&item.AgentUserID, &item.AgentApplicationID, &item.RuleVersion,
				&summary, &diff, &item.OverallConfidence, &citations,
				&item.InputTokens, &item.OutputTokens, &item.CreatedAt, &item.CompletedAt); err != nil {
				ServiceError(w, err)
				return
			}
			if summary != nil {
				item.SuggestionSummary = json.RawMessage(summary)
			}
			if diff != nil {
				item.FieldDiff = json.RawMessage(diff)
			}
			if citations != nil {
				item.Citations = json.RawMessage(citations)
			}
			items = append(items, item)
		}
		if err := rows.Err(); err != nil {
			ServiceError(w, err)
			return
		}
		page := CursorPage{}
		if len(items) > limit {
			page = cursorPageFrom(true, encodeProcessingResultCursor(items[limit-1]))
			items = items[:limit]
		}
		writeData(w, r, http.StatusOK, map[string]any{"items": items, "page": page})
	}
}

// ---------- member-initiated preparation ----------

// PrepareBody pins the input version; omitted means the asset's current
// working version.
type PrepareBody struct {
	AssetVersionID string `json:"asset_version_id"`
}

// AssetPrepare serves POST /api/workspaces/{workspaceId}/assets/{assetId}/prepare.
// A member-initiated prepare creates the automation.run the worker's
// asset_prepare branch consumes: source=manual, the workspace-enabled
// workflow application pinned with its current endpoint revision. The run row
// carries agent_user_id/agent_application_id/model_endpoint_id/revision and
// workflow_key='asset_prepare', exactly the fixed-scope facts the processor
// requires. Idempotency: the HTTP middleware replays full responses, and the
// run-level unique index on (organization, workspace, created_by,
// idempotency_key) is the backstop for keys the middleware does not cover.
func AssetPrepare(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		principal, ok := sessionPrincipal(w, r, deps)
		if !ok {
			return
		}
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		key, ok := requireIdempotencyKey(w, r)
		if !ok {
			return
		}
		if !requireHTTPStore(w, deps) {
			return
		}
		workspaceID := r.PathValue("workspaceId")
		assetID := r.PathValue("assetId")
		if !requirePathUUID(w, workspaceID, assetID) {
			return
		}
		var body PrepareBody
		if r.ContentLength != 0 {
			if !decodeBody(w, r, &body, 16*1024) {
				return
			}
		}
		// Two-stage gate: the member service resolves the asset's workspace and
		// checks asset.read; write permission and the routed-workspace match are
		// asserted here.
		target, err := deps.MemberAssetService.Get(r.Context(), principal, assetID)
		if err != nil {
			ServiceError(w, err)
			return
		}
		if target.WorkspaceID != workspaceID {
			writeError(w, http.StatusNotFound, "resource_not_found")
			return
		}
		if !requireWorkspaceAction(w, r, deps, principal, target.WorkspaceID, authz.ActionAssetWrite) {
			return
		}
		versionID := body.AssetVersionID
		if versionID == "" {
			versionID = target.CurrentWorkingVersionID
		}
		if versionID == "" {
			writeError(w, http.StatusUnprocessableEntity, "validation_failed")
			return
		}
		if versionID == body.AssetVersionID {
			var belongs bool
			if err := deps.Store.Pool.QueryRow(r.Context(), `
				SELECT EXISTS (
					SELECT 1 FROM asset.asset_versions
					WHERE organization_id = $1::uuid AND id = $2::uuid AND asset_id = $3::uuid
				)
			`, principal.OrganizationID, versionID, assetID).Scan(&belongs); err != nil {
				ServiceError(w, err)
				return
			}
			if !belongs {
				writeError(w, http.StatusUnprocessableEntity, "validation_failed")
				return
			}
		}
		// Pick the application: the workspace-enabled asset_prepare workflow
		// app with an active endpoint and a live current revision. The binding
		// lives in content.workspace_agent_applications (agent_applications has
		// no workspace column), matching the workspace app listing and the
		// automation create-run prerequisites.
		var applicationID, agentUserID, modelEndpointID string
		var modelEndpointRevision int64
		err = deps.Store.Pool.QueryRow(r.Context(), `
			SELECT aa.id::text, aa.bound_agent_user_id::text, aa.model_endpoint_id::text, me.current_revision
			FROM content.workspace_agent_applications wa
			JOIN integration.agent_applications aa
			  ON aa.organization_id = wa.organization_id AND aa.id = wa.agent_application_id
			JOIN integration.model_endpoints me
			  ON me.id = aa.model_endpoint_id AND me.organization_id = aa.organization_id AND me.status = 'active'
			JOIN integration.model_endpoint_revisions mer
			  ON mer.model_endpoint_id = me.id AND mer.revision = me.current_revision AND mer.revoked_at IS NULL
			WHERE wa.organization_id = $1::uuid AND wa.workspace_id = $2::uuid AND wa.enabled = true
			  AND aa.status = 'active' AND aa.runtime_mode = 'workflow' AND aa.workflow_key = 'asset_prepare'
			ORDER BY aa.created_at, aa.id
			LIMIT 1
		`, principal.OrganizationID, target.WorkspaceID).Scan(&applicationID, &agentUserID, &modelEndpointID, &modelEndpointRevision)
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusConflict, "prepare_application_missing")
			return
		}
		if err != nil {
			ServiceError(w, err)
			return
		}
		// The processor's asset_prepare branch hard-requires the fixed
		// application/endpoint facts; a zero revision would strand the run.
		if modelEndpointRevision <= 0 || modelEndpointID == "" || agentUserID == "" {
			writeError(w, http.StatusConflict, "prepare_application_missing")
			return
		}
		snapshot, _ := json.Marshal(map[string]any{
			"asset_ids":        []string{assetID},
			"asset_version_id": versionID,
			"requested_by":     principal.UserID,
		})
		// The run-level idempotency key namespaces the client key by the member
		// so the unique index arbitrates replays per creator.
		runKey := principal.UserID + ":" + key
		var runID string
		err = deps.Store.Pool.QueryRow(r.Context(), `
			INSERT INTO automation.runs
				(organization_id, workspace_id, source, operation, status, input_asset_ids, input_scope,
				 idempotency_key, principal_id, agent_user_id, agent_application_id, model_endpoint_id,
				 model_endpoint_revision, runtime_mode, workflow_key, workflow_code_version,
				 input_snapshot, execution_options, created_by)
			VALUES ($1::uuid, $2::uuid, 'manual', 'prepare_asset', 'queued', ARRAY[$3::uuid], $4::jsonb,
			        $5, $6::uuid, $7::uuid, $8::uuid, $9::uuid, $10, 'workflow', 'asset_prepare', 1,
			        $4::jsonb, '{}'::jsonb, $6::uuid)
			ON CONFLICT DO NOTHING
			RETURNING id::text
		`, principal.OrganizationID, target.WorkspaceID, assetID, snapshot, runKey, principal.UserID,
			agentUserID, applicationID, modelEndpointID, modelEndpointRevision).Scan(&runID)
		if errors.Is(err, pgx.ErrNoRows) {
			// Replay: another request already created this member's run for the
			// same key. The snapshot facts of the stored row win.
			var storedVersionID string
			scanErr := deps.Store.Pool.QueryRow(r.Context(), `
				SELECT id::text, COALESCE(input_snapshot->>'asset_version_id', '')
				FROM automation.runs
				WHERE organization_id = $1::uuid AND workspace_id = $2::uuid
				  AND created_by = $3::uuid AND idempotency_key = $4
			`, principal.OrganizationID, target.WorkspaceID, principal.UserID, runKey).Scan(&runID, &storedVersionID)
			if scanErr != nil {
				ServiceError(w, scanErr)
				return
			}
			if storedVersionID != "" {
				versionID = storedVersionID
			}
		} else if err != nil {
			ServiceError(w, err)
			return
		} else {
			eventPayload, _ := json.Marshal(map[string]any{
				"runtime_mode": "workflow", "workflow_key": "asset_prepare", "requested_by": principal.UserID,
			})
			if _, err := deps.Store.Pool.Exec(r.Context(), `
				INSERT INTO automation.run_events (organization_id, run_id, event_type, payload)
				VALUES ($1::uuid, $2::uuid, 'run.queued', $3::jsonb)
			`, principal.OrganizationID, runID, eventPayload); err != nil {
				ServiceError(w, err)
				return
			}
		}
		writeData(w, r, http.StatusAccepted, map[string]any{
			"run_id":           runID,
			"asset_version_id": versionID,
		})
	}
}
