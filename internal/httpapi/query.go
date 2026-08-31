package httpapi

// query.go — the phase 3 unified query HTTP surface (doc §11): member and
// OpenAPI query routes, projection profile/rebuild operations, query execution
// audit and the readiness probe. Handlers only translate transport into the
// query contract; every permission decision lives in the service and scope
// compiler.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"agentchunzhi/internal/auth"
	"agentchunzhi/internal/authz"
	"agentchunzhi/internal/organization"
	agentquery "agentchunzhi/internal/query"
	"agentchunzhi/internal/retrieval"
	"agentchunzhi/internal/store"

	"github.com/jackc/pgx/v5"
)

// QueryService is the unified query surface consumed by the handlers. The
// concrete implementation is agentquery.Service (doc §4.1: exactly one
// UnifiedQueryService).
type QueryService interface {
	WorkspaceQuery(ctx context.Context, principal auth.Principal, workspaceID string, input agentquery.Request) (agentquery.Response, error)
	OrganizationQuery(ctx context.Context, principal auth.Principal, input agentquery.Request) (agentquery.Response, error)
	OpenAPIQuery(ctx context.Context, principal auth.Principal, input agentquery.Request) (agentquery.Response, error)
	ValidateCitationRefs(ctx context.Context, principal auth.Principal, refs []string) ([]agentquery.ValidatedReference, error)
	Reference(ctx context.Context, principal auth.Principal, assetID string, allowedModelIDs []string) (agentquery.AssetReference, error)
}

// RetrievalProfileService is the profile lifecycle surface the operations
// routes call (implemented by retrieval.ProfileService / ProfileRepository).
type RetrievalProfileService interface {
	ListProfiles(ctx context.Context, organizationID string) ([]retrieval.Profile, error)
	Create(ctx context.Context, organizationID, manifestKey, createdBy string) (retrieval.Profile, error)
	Activate(ctx context.Context, organizationID, profileID, activatedBy string) (retrieval.Profile, error)
}

// RetrievalRebuildService starts projection rebuild batches (implemented by
// retrieval.RebuildService).
type RetrievalRebuildService interface {
	StartRebuild(ctx context.Context, organizationID, scopeType, workspaceID, resourceModelID, assetID, reason, requestedBy, idempotencyKey string) (retrieval.Rebuild, error)
}

// ---------------------------------------------------------------------------
// Request/response DTOs
// ---------------------------------------------------------------------------

type QueryRequest struct {
	Query                string                 `json:"query"`
	Mode                 string                 `json:"mode"`
	ResourceModelIDs     []string               `json:"resource_model_ids"`
	Visibility           []string               `json:"visibility"`
	TagsAny              []string               `json:"tags_any"`
	TagsAll              []string               `json:"tags_all"`
	TagsNone             []string               `json:"tags_none"`
	FieldFilters         []agentquery.FieldFilter `json:"field_filters"`
	Origins              []string               `json:"origins"`
	ConfirmationStatuses []string               `json:"confirmation_statuses"`
	PublishedAfter       *string                `json:"published_after"`
	PublishedBefore      *string                `json:"published_before"`
	TopK                 int                    `json:"top_k"`
	Cursor               string                 `json:"cursor"`
}

func (v QueryRequest) toContract() (agentquery.Request, bool) {
	req := agentquery.Request{
		Query:                v.Query,
		Mode:                 v.Mode,
		ResourceModelIDs:     v.ResourceModelIDs,
		Visibility:           v.Visibility,
		TagsAny:              v.TagsAny,
		TagsAll:              v.TagsAll,
		TagsNone:             v.TagsNone,
		FieldFilters:         v.FieldFilters,
		Origins:              v.Origins,
		ConfirmationStatuses: v.ConfirmationStatuses,
		TopK:                 v.TopK,
		Cursor:               v.Cursor,
	}
	parse := func(raw *string) (*time.Time, bool) {
		if raw == nil || strings.TrimSpace(*raw) == "" {
			return nil, true
		}
		value, err := time.Parse(time.RFC3339, strings.TrimSpace(*raw))
		if err != nil {
			return nil, false
		}
		return &value, true
	}
	var ok bool = true
	req.PublishedAfter, ok = parse(v.PublishedAfter)
	if !ok {
		return req, false
	}
	req.PublishedBefore, ok = parse(v.PublishedBefore)
	if !ok {
		return req, false
	}
	return req, true
}

type profileDTO struct {
	ID                    string `json:"id"`
	Generation            int64  `json:"generation"`
	ManifestKey           string `json:"manifest_key"`
	CanonicalizerVersion  string `json:"canonicalizer_version"`
	ChunkerVersion        string `json:"chunker_version"`
	TokenizerVersion      string `json:"tokenizer_version"`
	SemanticEnabled       bool   `json:"semantic_enabled"`
	EmbeddingModel        string `json:"embedding_model,omitempty"`
	EmbeddingModelVersion string `json:"embedding_model_version,omitempty"`
	EmbeddingDimensions   int    `json:"embedding_dimensions,omitempty"`
	Status                string `json:"status"`
	Revision              int64  `json:"revision"`
	CreatedAt             string `json:"created_at"`
	ActivatedAt           string `json:"activated_at,omitempty"`
}

func toProfileDTO(profile retrieval.Profile) profileDTO {
	dto := profileDTO{
		ID:                    profile.ID,
		Generation:            profile.Generation,
		ManifestKey:           profile.ManifestKey,
		CanonicalizerVersion:  profile.CanonicalizerVersion,
		ChunkerVersion:        profile.ChunkerVersion,
		TokenizerVersion:      profile.TokenizerVersion,
		SemanticEnabled:       profile.SemanticEnabled,
		EmbeddingModel:        profile.EmbeddingModel,
		EmbeddingModelVersion: profile.EmbeddingModelVersion,
		EmbeddingDimensions:   profile.EmbeddingDimensions,
		Status:                profile.Status,
		Revision:              profile.Revision,
		CreatedAt:             profile.CreatedAt.UTC().Format(time.RFC3339),
	}
	if !profile.ActivatedAt.IsZero() {
		dto.ActivatedAt = profile.ActivatedAt.UTC().Format(time.RFC3339)
	}
	return dto
}

type rebuildDTO struct {
	ID            string `json:"id"`
	ScopeType     string `json:"scope_type"`
	ScopeID       string `json:"scope_id"`
	WorkspaceID   string `json:"workspace_id,omitempty"`
	Reason        string `json:"reason"`
	Status        string `json:"status"`
	TotalCount    int    `json:"total_count"`
	QueuedCount   int    `json:"queued_count"`
	ReadyCount    int    `json:"ready_count"`
	DegradedCount int    `json:"degraded_count"`
	FailedCount   int    `json:"failed_count"`
	RequestedAt   string `json:"requested_at"`
	CompletedAt   string `json:"completed_at,omitempty"`
}

type executionDTO struct {
	ID                  string           `json:"id"`
	SubjectKind         string           `json:"subject_kind"`
	Channel             string           `json:"channel"`
	RequestedMode       string           `json:"requested_mode"`
	ExecutedMode        string           `json:"executed_mode,omitempty"`
	RankingMethod       string           `json:"ranking_method,omitempty"`
	Status              string           `json:"status"`
	Degraded            bool             `json:"degraded"`
	DegradationReasons  []string         `json:"degradation_reasons"`
	ResourceModelCount  int              `json:"resource_model_count"`
	ResultCount         int              `json:"result_count"`
	StageLatencyMS      map[string]any   `json:"stage_latency_ms"`
	ErrorCode           string           `json:"error_code,omitempty"`
	EmbeddingIdentity   string           `json:"embedding_model_identity,omitempty"`
	RerankerIdentity    string           `json:"reranker_model_identity,omitempty"`
	StartedAt           string           `json:"started_at"`
	CompletedAt         string           `json:"completed_at,omitempty"`
}

// ---------------------------------------------------------------------------
// Query routes
// ---------------------------------------------------------------------------

// WorkspaceQuery handles POST /api/workspaces/{workspaceId}/query.
func WorkspaceQuery(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, ok := requireMemberSession(w, r, deps)
		if !ok {
			return
		}
		if !requirePathUUID(w, r.PathValue("workspaceId")) {
			return
		}
		input, ok := decodeQueryRequest(w, r)
		if !ok {
			return
		}
		response, err := deps.QueryService.WorkspaceQuery(agentquery.WithRequestID(r.Context(), RequestIDFromContext(r.Context())), principal, r.PathValue("workspaceId"), input)
		writeQueryResponse(w, r, response, err)
	}
}

// OrganizationQuery handles POST /api/organization/query.
func OrganizationQuery(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, ok := requireMemberSession(w, r, deps)
		if !ok {
			return
		}
		input, ok := decodeQueryRequest(w, r)
		if !ok {
			return
		}
		response, err := deps.QueryService.OrganizationQuery(agentquery.WithRequestID(r.Context(), RequestIDFromContext(r.Context())), principal, input)
		writeQueryResponse(w, r, response, err)
	}
}

// OpenQuery handles POST /api/open/query with the technical API key.
func OpenQuery(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, err := deps.Authenticator.Authenticate(r.Context(), r)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "authentication_required")
			return
		}
		input, ok := decodeQueryRequest(w, r)
		if !ok {
			return
		}
		response, queryErr := deps.QueryService.OpenAPIQuery(agentquery.WithRequestID(r.Context(), RequestIDFromContext(r.Context())), principal, input)
		writeQueryResponse(w, r, response, queryErr)
	}
}

// OpenReferenceValidate handles POST /api/open/references/validate.
func OpenReferenceValidate(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, err := deps.Authenticator.Authenticate(r.Context(), r)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "authentication_required")
			return
		}
		var input struct {
			CitationRefs []string `json:"citation_refs"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64*1024))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil || len(input.CitationRefs) == 0 {
			writeError(w, http.StatusUnprocessableEntity, "invalid_reference_request")
			return
		}
		validated, err := deps.QueryService.ValidateCitationRefs(r.Context(), principal, input.CitationRefs)
		if err != nil {
			writeQueryError(w, err)
			return
		}
		writeData(w, r, http.StatusOK, map[string]any{
			"validated":      validated,
			"rejected_count": len(input.CitationRefs) - len(validated),
		})
	}
}

func decodeQueryRequest(w http.ResponseWriter, r *http.Request) (agentquery.Request, bool) {
	var input QueryRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256*1024))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid_query_request")
		return agentquery.Request{}, false
	}
	contract, ok := input.toContract()
	if !ok {
		writeError(w, http.StatusUnprocessableEntity, "invalid_query_request")
		return agentquery.Request{}, false
	}
	return contract, true
}

// writeQueryResponse maps the service outcome onto the API envelope and the
// fixed error table (doc §11.5).
func writeQueryResponse(w http.ResponseWriter, r *http.Request, response agentquery.Response, err error) {
	if err != nil {
		writeQueryError(w, err)
		return
	}
	writeJSONValue(w, http.StatusOK, map[string]any{
		"data":       response,
		"request_id": RequestIDFromContext(r.Context()),
	})
}

func writeQueryError(w http.ResponseWriter, err error) {
	status, code := agentquery.HTTPStatus(err)
	writeError(w, status, code)
}

// ---------------------------------------------------------------------------
// Projection operations routes (doc §11.3)
// ---------------------------------------------------------------------------

// ListRetrievalProfiles handles GET /api/organization/retrieval/profiles.
func ListRetrievalProfiles(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, ok := requireMemberSession(w, r, deps)
		if !ok {
			return
		}
		if !requireOrganizationManage(w, r, deps, principal) {
			return
		}
		profiles, err := deps.RetrievalProfiles.ListProfiles(r.Context(), principal.OrganizationID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "retrieval_profiles_load_failed")
			return
		}
		items := make([]profileDTO, 0, len(profiles))
		for _, profile := range profiles {
			items = append(items, toProfileDTO(profile))
		}
		writeData(w, r, http.StatusOK, map[string]any{"items": items})
	}
}

// CreateRetrievalProfile handles POST /api/organization/retrieval/profiles.
func CreateRetrievalProfile(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, ok := requireMemberSession(w, r, deps)
		if !ok {
			return
		}
		if !requireOrganizationManage(w, r, deps, principal) {
			return
		}
		if _, ok := requestIdempotencyKey(w, r); !ok {
			return
		}
		var input struct {
			ManifestKey string `json:"manifest_key"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16*1024))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil || strings.TrimSpace(input.ManifestKey) == "" {
			writeError(w, http.StatusUnprocessableEntity, "invalid_retrieval_profile_request")
			return
		}
		profile, err := deps.RetrievalProfiles.Create(r.Context(), principal.OrganizationID, strings.TrimSpace(input.ManifestKey), principal.UserID)
		if errors.Is(err, retrieval.ErrUnknownManifestKey) {
			writeError(w, http.StatusUnprocessableEntity, "unknown_manifest_key")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "retrieval_profile_create_failed")
			return
		}
		writeData(w, r, http.StatusCreated, toProfileDTO(profile))
	}
}

// ActivateRetrievalProfile handles POST .../profiles/{profileId}/activate.
func ActivateRetrievalProfile(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, ok := requireMemberSession(w, r, deps)
		if !ok {
			return
		}
		if !requireOrganizationManage(w, r, deps, principal) {
			return
		}
		if _, ok := requestIdempotencyKey(w, r); !ok {
			return
		}
		profileID := r.PathValue("profileId")
		if !agentquery.ValidUUID(profileID) {
			writeError(w, http.StatusUnprocessableEntity, "invalid_retrieval_profile_id")
			return
		}
		profile, err := deps.RetrievalProfiles.Activate(r.Context(), principal.OrganizationID, profileID, principal.UserID)
		if errors.Is(err, retrieval.ErrProfileNotReady) {
			writeError(w, http.StatusConflict, "profile_not_ready")
			return
		}
		if errors.Is(err, retrieval.ErrProfileLifecycle) {
			writeError(w, http.StatusConflict, "retrieval_profile_lifecycle")
			return
		}
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "resource_not_found")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "retrieval_profile_activate_failed")
			return
		}
		writeData(w, r, http.StatusOK, toProfileDTO(profile))
	}
}

// WorkspaceRetrievalStatus handles GET /api/workspaces/{workspaceId}/retrieval/status.
// The route only exposes aggregate counters — never chunk content or tenant
// identifiers beyond the workspace the caller administers.
func WorkspaceRetrievalStatus(deps Dependencies) http.HandlerFunc {
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
		if !requirePathUUID(w, workspaceID) {
			return
		}
		if _, err := deps.WorkspacePolicy.Require(r.Context(), principal, workspaceID, "", authz.ActionWorkspaceManage); err != nil {
			writeWorkspacePolicyError(w, err)
			return
		}
		if deps.Store == nil || deps.Store.Pool == nil {
			writeError(w, http.StatusServiceUnavailable, "database_unavailable")
			return
		}
		counts := map[string]int{}
		rows, err := deps.Store.Pool.Query(r.Context(), `
			SELECT status, count(*)
			FROM retrieval.projection_runs
			WHERE organization_id = $1::uuid AND workspace_id = $2::uuid
			GROUP BY status
		`, principal.OrganizationID, workspaceID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "retrieval_status_failed")
			return
		}
		for rows.Next() {
			var status string
			var count int
			if err := rows.Scan(&status, &count); err != nil {
				rows.Close()
				writeError(w, http.StatusInternalServerError, "retrieval_status_failed")
				return
			}
			counts[status] = count
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			writeError(w, http.StatusInternalServerError, "retrieval_status_failed")
			return
		}
		var activeProfile *profileDTO
		profiles, err := deps.RetrievalProfiles.ListProfiles(r.Context(), principal.OrganizationID)
		if err == nil {
			for _, profile := range profiles {
				if profile.Status == retrieval.ProfileStatusActive {
					dto := toProfileDTO(profile)
					activeProfile = &dto
					break
				}
			}
		}
		writeData(w, r, http.StatusOK, map[string]any{
			"workspace_id":  workspaceID,
			"run_counts":    counts,
			"active_profile": activeProfile,
		})
	}
}

// WorkspaceRetrievalRebuild handles POST /api/workspaces/{workspaceId}/retrieval/rebuilds.
func WorkspaceRetrievalRebuild(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, ok := requireMemberSession(w, r, deps)
		if !ok {
			return
		}
		workspaceID := r.PathValue("workspaceId")
		if !requirePathUUID(w, workspaceID) {
			return
		}
		if _, err := deps.WorkspacePolicy.Require(r.Context(), principal, workspaceID, "", authz.ActionWorkspaceManage); err != nil {
			writeWorkspacePolicyError(w, err)
			return
		}
		if _, ok := requestIdempotencyKey(w, r); !ok {
			return
		}
		rebuild, err := deps.RetrievalRebuilds.StartRebuild(r.Context(), principal.OrganizationID,
			retrieval.ScopeWorkspace, workspaceID, "", "", retrieval.ReasonManual, principal.UserID, requestIdempotencyKeyValue(r))
		writeRebuildResponse(w, r, rebuild, err)
	}
}

// WorkspaceRebuildGet handles GET /api/workspaces/{workspaceId}/retrieval/rebuilds/{rebuildId}.
func WorkspaceRebuildGet(deps Dependencies) http.HandlerFunc {
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
		if !requirePathUUID(w, workspaceID, r.PathValue("rebuildId")) {
			return
		}
		if _, err := deps.WorkspacePolicy.Require(r.Context(), principal, workspaceID, "", authz.ActionWorkspaceManage); err != nil {
			writeWorkspacePolicyError(w, err)
			return
		}
		rebuild, err := loadRebuildRow(r.Context(), deps, principal.OrganizationID, r.PathValue("rebuildId"))
		if err != nil {
			writeQueryError(w, err)
			return
		}
		if rebuild.WorkspaceID != workspaceID {
			writeError(w, http.StatusNotFound, "resource_not_found")
			return
		}
		writeData(w, r, http.StatusOK, rebuild)
	}
}

// OrganizationRetrievalRebuilds handles POST /api/organization/retrieval/rebuilds.
func OrganizationRetrievalRebuilds(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, ok := requireMemberSession(w, r, deps)
		if !ok {
			return
		}
		if !requireOrganizationManage(w, r, deps, principal) {
			return
		}
		if _, ok := requestIdempotencyKey(w, r); !ok {
			return
		}
		rebuild, err := deps.RetrievalRebuilds.StartRebuild(r.Context(), principal.OrganizationID,
			retrieval.ScopeOrganization, "", "", "", retrieval.ReasonManual, principal.UserID, requestIdempotencyKeyValue(r))
		writeRebuildResponse(w, r, rebuild, err)
	}
}

// OrganizationRebuildGet handles GET /api/organization/retrieval/rebuilds/{rebuildId}.
// Organization admins without workspace membership see aggregate counters only.
func OrganizationRebuildGet(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, ok := requireMemberSession(w, r, deps)
		if !ok {
			return
		}
		if !requireOrganizationManage(w, r, deps, principal) {
			return
		}
		if !requirePathUUID(w, r.PathValue("rebuildId")) {
			return
		}
		rebuild, err := loadRebuildRow(r.Context(), deps, principal.OrganizationID, r.PathValue("rebuildId"))
		if err != nil {
			writeQueryError(w, err)
			return
		}
		writeData(w, r, http.StatusOK, rebuild)
	}
}

func writeRebuildResponse(w http.ResponseWriter, r *http.Request, rebuild retrieval.Rebuild, err error) {
	if err != nil {
		if errors.Is(err, retrieval.ErrNoActiveProfile) {
			writeError(w, http.StatusConflict, "retrieval_profile_unavailable")
			return
		}
		if errors.Is(err, retrieval.ErrInvalidScope) {
			writeError(w, http.StatusUnprocessableEntity, "invalid_rebuild_scope")
			return
		}
		writeError(w, http.StatusInternalServerError, "retrieval_rebuild_failed")
		return
	}
	writeData(w, r, http.StatusAccepted, toRebuildDTO(rebuild))
}

func toRebuildDTO(rebuild retrieval.Rebuild) rebuildDTO {
	return rebuildDTO{
		ID:            rebuild.ID,
		ScopeType:     rebuild.ScopeType,
		ScopeID:       rebuild.ScopeID,
		WorkspaceID:   rebuild.WorkspaceID,
		Reason:        rebuild.Reason,
		Status:        rebuild.Status,
		TotalCount:    rebuild.TotalCount,
		QueuedCount:   rebuild.QueuedCount,
		ReadyCount:    rebuild.ReadyCount,
		DegradedCount: rebuild.DegradedCount,
		FailedCount:   rebuild.FailedCount,
	}
}

// loadRebuildRow reads one rebuild batch with aggregate counters only.
func loadRebuildRow(ctx context.Context, deps Dependencies, organizationID, rebuildID string) (rebuildDTO, error) {
	var dto rebuildDTO
	var workspaceID *string
	var requestedAt, completedAt *time.Time
	err := deps.Store.Pool.QueryRow(ctx, `
		SELECT id::text, scope_type, scope_id, COALESCE(workspace_id::text,''), reason,
		       status, total_count, queued_count, ready_count, degraded_count, failed_count,
		       requested_at, completed_at
		FROM retrieval.projection_rebuilds
		WHERE organization_id = $1::uuid AND id = $2::uuid
	`, organizationID, rebuildID).Scan(&dto.ID, &dto.ScopeType, &dto.ScopeID, &workspaceID,
		&dto.Reason, &dto.Status, &dto.TotalCount, &dto.QueuedCount, &dto.ReadyCount,
		&dto.DegradedCount, &dto.FailedCount, &requestedAt, &completedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return rebuildDTO{}, agentquery.ErrResourceNotFound
	}
	if err != nil {
		return rebuildDTO{}, agentquery.ErrResourceNotFound
	}
	if workspaceID != nil {
		dto.WorkspaceID = *workspaceID
	}
	if requestedAt != nil {
		dto.RequestedAt = requestedAt.UTC().Format(time.RFC3339)
	}
	if completedAt != nil {
		dto.CompletedAt = completedAt.UTC().Format(time.RFC3339)
	}
	return dto, nil
}

// ---------------------------------------------------------------------------
// Query execution audit routes (doc §11.4)
// ---------------------------------------------------------------------------

// WorkspaceQueryExecutions handles GET /api/workspaces/{workspaceId}/query-executions.
// audit.read holders only see executions bound to their workspace; query and
// filter text are never part of the audit payload.
func WorkspaceQueryExecutions(deps Dependencies) http.HandlerFunc {
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
		if !requirePathUUID(w, workspaceID) {
			return
		}
		if _, err := deps.WorkspacePolicy.Require(r.Context(), principal, workspaceID, "", authz.ActionAuditRead); err != nil {
			writeWorkspacePolicyError(w, err)
			return
		}
		if deps.Store == nil || deps.Store.Pool == nil {
			writeError(w, http.StatusServiceUnavailable, "database_unavailable")
			return
		}
		limit := 50
		if value := strings.TrimSpace(r.URL.Query().Get("limit")); value != "" {
			parsed, parseErr := strconv.Atoi(value)
			if parseErr != nil || parsed < 1 || parsed > 200 {
				writeError(w, http.StatusUnprocessableEntity, "invalid_query_execution_limit")
				return
			}
			limit = parsed
		}
		rows, err := deps.Store.Pool.Query(r.Context(), `
			SELECT e.id::text, e.subject_kind, e.channel, e.requested_mode,
			       COALESCE(e.executed_mode,''), COALESCE(e.ranking_method,''),
			       e.status, e.degraded, e.degradation_reasons,
			       e.resource_model_count, e.result_count, e.stage_latency_ms,
			       COALESCE(e.error_code,''), e.started_at, e.completed_at
			FROM retrieval.query_executions e
			JOIN retrieval.query_execution_workspaces w
			  ON w.organization_id = e.organization_id AND w.execution_id = e.id
			WHERE e.organization_id = $1::uuid AND w.workspace_id = $2::uuid
			ORDER BY e.started_at DESC, e.id DESC
			LIMIT $3
		`, principal.OrganizationID, workspaceID, limit)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "query_executions_load_failed")
			return
		}
		defer rows.Close()
		items := []executionDTO{}
		for rows.Next() {
			item, scanErr := scanExecutionRow(rows)
			if scanErr != nil {
				writeError(w, http.StatusInternalServerError, "query_executions_load_failed")
				return
			}
			items = append(items, item)
		}
		if err := rows.Err(); err != nil {
			writeError(w, http.StatusInternalServerError, "query_executions_load_failed")
			return
		}
		writeData(w, r, http.StatusOK, map[string]any{"items": items, "has_more": false})
	}
}

// OrganizationQueryExecutions handles GET /api/organization/query-executions.
func OrganizationQueryExecutions(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, ok := requireMemberSession(w, r, deps)
		if !ok {
			return
		}
		if !requireOrganizationAuditRead(w, r, deps, principal) {
			return
		}
		if deps.Store == nil || deps.Store.Pool == nil {
			writeError(w, http.StatusServiceUnavailable, "database_unavailable")
			return
		}
		limit := 50
		if value := strings.TrimSpace(r.URL.Query().Get("limit")); value != "" {
			parsed, parseErr := strconv.Atoi(value)
			if parseErr != nil || parsed < 1 || parsed > 200 {
				writeError(w, http.StatusUnprocessableEntity, "invalid_query_execution_limit")
				return
			}
			limit = parsed
		}
		rows, err := deps.Store.Pool.Query(r.Context(), `
			SELECT id::text, subject_kind, channel, requested_mode,
			       COALESCE(executed_mode,''), COALESCE(ranking_method,''),
			       status, degraded, degradation_reasons,
			       resource_model_count, result_count, stage_latency_ms,
			       COALESCE(error_code,''), started_at, completed_at
			FROM retrieval.query_executions
			WHERE organization_id = $1::uuid
			ORDER BY started_at DESC, id DESC
			LIMIT $2
		`, principal.OrganizationID, limit)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "query_executions_load_failed")
			return
		}
		defer rows.Close()
		items := []executionDTO{}
		for rows.Next() {
			item, scanErr := scanExecutionRow(rows)
			if scanErr != nil {
				writeError(w, http.StatusInternalServerError, "query_executions_load_failed")
				return
			}
			items = append(items, item)
		}
		if err := rows.Err(); err != nil {
			writeError(w, http.StatusInternalServerError, "query_executions_load_failed")
			return
		}
		writeData(w, r, http.StatusOK, map[string]any{"items": items, "has_more": false})
	}
}

// QueryExecution handles GET /api/query-executions/{executionId}: org
// admins and audit.read holders of one bound workspace may read the detail.
func QueryExecution(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, ok := requireMemberSession(w, r, deps)
		if !ok {
			return
		}
		if !requirePathUUID(w, r.PathValue("executionId")) {
			return
		}
		if deps.Store == nil || deps.Store.Pool == nil {
			writeError(w, http.StatusServiceUnavailable, "database_unavailable")
			return
		}
		executionID := r.PathValue("executionId")
		// Organization admins read any execution of the organization; other
		// members need audit.read on one of the bound workspaces.
		if err := deps.OrganizationService.RequireOrganizationAction(r.Context(), principal, authz.ActionAuditRead); err == nil {
			item, loadErr := loadExecutionRow(r.Context(), deps, principal.OrganizationID, executionID)
			if loadErr != nil {
				writeQueryError(w, loadErr)
				return
			}
			writeData(w, r, http.StatusOK, item)
			return
		}
		var bound bool
		if err := deps.Store.Pool.QueryRow(r.Context(), `
			SELECT EXISTS (
				SELECT 1
				FROM retrieval.query_executions e
				JOIN retrieval.query_execution_workspaces w
				  ON w.organization_id = e.organization_id AND w.execution_id = e.id
				JOIN content.workspace_members wm
				  ON wm.organization_id = w.organization_id AND wm.workspace_id = w.workspace_id
				WHERE e.organization_id = $1::uuid AND e.id = $2::uuid
				  AND wm.user_id = $3::uuid
			)
		`, principal.OrganizationID, executionID, principal.UserID).Scan(&bound); err != nil {
			writeError(w, http.StatusInternalServerError, "query_execution_load_failed")
			return
		}
		if !bound {
			writeError(w, http.StatusNotFound, "resource_not_found")
			return
		}
		if _, err := deps.WorkspacePolicy.Require(r.Context(), principal, boundWorkspaceID(r, deps, principal, executionID), "", authz.ActionAuditRead); err != nil {
			writeWorkspacePolicyError(w, err)
			return
		}
		item, loadErr := loadExecutionRow(r.Context(), deps, principal.OrganizationID, executionID)
		if loadErr != nil {
			writeQueryError(w, loadErr)
			return
		}
		writeData(w, r, http.StatusOK, item)
	}
}

// boundWorkspaceID resolves one workspace of the execution the caller is a
// member of; empty when none matches.
func boundWorkspaceID(r *http.Request, deps Dependencies, principal auth.Principal, executionID string) string {
	var workspaceID string
	_ = deps.Store.Pool.QueryRow(r.Context(), `
		SELECT w.workspace_id::text
		FROM retrieval.query_execution_workspaces w
		JOIN content.workspace_members wm
		  ON wm.organization_id = w.organization_id AND wm.workspace_id = w.workspace_id
		WHERE w.organization_id = $1::uuid AND w.execution_id = $2::uuid
		  AND wm.user_id = $3::uuid
		LIMIT 1
	`, principal.OrganizationID, executionID, principal.UserID).Scan(&workspaceID)
	return workspaceID
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanExecutionRow(rows rowScanner) (executionDTO, error) {
	var item executionDTO
	var startedAt, completedAt *time.Time
	var stageLatency []byte
	var reasons []string
	if err := rows.Scan(&item.ID, &item.SubjectKind, &item.Channel, &item.RequestedMode,
		&item.ExecutedMode, &item.RankingMethod, &item.Status, &item.Degraded,
		&reasons, &item.ResourceModelCount, &item.ResultCount, &stageLatency,
		&item.ErrorCode, &startedAt, &completedAt); err != nil {
		return executionDTO{}, err
	}
	if startedAt != nil {
		item.StartedAt = startedAt.UTC().Format(time.RFC3339)
	}
	item.DegradationReasons = reasons
	if item.DegradationReasons == nil {
		item.DegradationReasons = []string{}
	}
	if len(stageLatency) > 0 {
		_ = json.Unmarshal(stageLatency, &item.StageLatencyMS)
	}
	if item.StageLatencyMS == nil {
		item.StageLatencyMS = map[string]any{}
	}
	if completedAt != nil {
		item.CompletedAt = completedAt.UTC().Format(time.RFC3339)
	}
	return item, nil
}

func loadExecutionRow(ctx context.Context, deps Dependencies, organizationID, executionID string) (executionDTO, error) {
	row := deps.Store.Pool.QueryRow(ctx, `
		SELECT id::text, subject_kind, channel, requested_mode,
		       COALESCE(executed_mode,''), COALESCE(ranking_method,''),
		       status, degraded, degradation_reasons,
		       resource_model_count, result_count, stage_latency_ms,
		       COALESCE(error_code,''), started_at, completed_at
		FROM retrieval.query_executions
		WHERE organization_id = $1::uuid AND id = $2::uuid
	`, organizationID, executionID)
	item, err := scanExecutionRow(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return executionDTO{}, agentquery.ErrResourceNotFound
	}
	if err != nil {
		return executionDTO{}, agentquery.ErrResourceNotFound
	}
	return item, nil
}

// ---------------------------------------------------------------------------
// Shared guards
// ---------------------------------------------------------------------------

// requireOrganizationManage enforces organization.manage through the
// organization service (no ad-hoc role string checks).
func requireOrganizationManage(w http.ResponseWriter, r *http.Request, deps Dependencies, principal auth.Principal) bool {
	if err := deps.OrganizationService.RequireOrganizationAction(r.Context(), principal, authz.ActionOrganizationManage); err != nil {
		if errors.Is(err, organization.ErrForbidden) {
			writeError(w, http.StatusForbidden, "query_scope_forbidden")
			return false
		}
		writeError(w, http.StatusInternalServerError, "authorization_scope_failed")
		return false
	}
	return true
}

// requireOrganizationAuditRead enforces the org-admin-only audit surface.
func requireOrganizationAuditRead(w http.ResponseWriter, r *http.Request, deps Dependencies, principal auth.Principal) bool {
	if err := deps.OrganizationService.RequireOrganizationAction(r.Context(), principal, authz.ActionAuditRead); err != nil {
		if errors.Is(err, organization.ErrForbidden) {
			writeError(w, http.StatusForbidden, "query_scope_forbidden")
			return false
		}
		writeError(w, http.StatusInternalServerError, "authorization_scope_failed")
		return false
	}
	return true
}

func writeWorkspacePolicyError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, authz.ErrWorkspaceNotFound):
		writeError(w, http.StatusNotFound, "workspace_not_found")
	case errors.Is(err, authz.ErrWorkspaceForbidden):
		writeError(w, http.StatusForbidden, "workspace_access_denied")
	default:
		writeError(w, http.StatusInternalServerError, "authorization_scope_failed")
	}
}

// requestIdempotencyKeyValue returns the raw key for service calls after
// requestIdempotencyKey validated its shape.
func requestIdempotencyKeyValue(r *http.Request) string {
	return strings.TrimSpace(r.Header.Get("Idempotency-Key"))
}

// sortStrings keeps the ready diagnostics output stable.
func sortStrings(values []string) { sort.Strings(values) }

// ---------------------------------------------------------------------------
// Readiness (doc §13.4)
// ---------------------------------------------------------------------------

// ready implements the phase 3 readiness probe: PostgreSQL, the retrieval
// extensions, both query secrets, the manifest identity of serving profiles
// and a fresh retrieval worker heartbeat with the required handler manifest.
func ready(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		failures := []string{}
		if deps.Store == nil || deps.Store.Pool == nil {
			writeReady(w, http.StatusServiceUnavailable, []string{"database_unconfigured"})
			return
		}
		if err := deps.Store.Pool.Ping(r.Context()); err != nil {
			writeReady(w, http.StatusServiceUnavailable, []string{"database_unavailable"})
			return
		}
		// Extensions (pgroonga + vector).
		var extensions int
		if err := deps.Store.Pool.QueryRow(r.Context(), `
			SELECT count(*) FROM pg_extension WHERE extname IN ('pgroonga', 'vector')
		`).Scan(&extensions); err != nil || extensions != 2 {
			failures = append(failures, "retrieval_extensions_missing")
		}
		// Query secrets: both present, at least 32 bytes and distinct.
		if len(deps.SearchCursorSecret) < 32 {
			failures = append(failures, "cursor_secret_invalid")
		}
		if len(deps.QueryHashSecret) < 32 {
			failures = append(failures, "query_hash_secret_invalid")
		}
		if len(deps.SearchCursorSecret) >= 32 && deps.SearchCursorSecret == deps.QueryHashSecret {
			failures = append(failures, "query_secrets_identical")
		}
		// Serving profiles must match the runtime manifest identity.
		servingProfiles, err := countServingProfiles(r.Context(), deps.Store)
		if err != nil {
			failures = append(failures, "retrieval_profiles_unavailable")
		}
		if servingProfiles > 0 {
			mismatched, mismatchErr := countManifestMismatches(r.Context(), deps.Store, deps.ManifestFingerprint)
			if mismatchErr != nil || mismatched > 0 {
				failures = append(failures, "manifest_identity_mismatch")
			}
			// A semantic-enabled serving profile without an initialized query
			// embedding provider is a configuration error (doc §13.1/§13.4).
			semanticServing, servingErr := countSemanticServingProfiles(r.Context(), deps.Store)
			if servingErr != nil {
				failures = append(failures, "retrieval_profiles_unavailable")
			} else if semanticServing > 0 && !deps.SemanticAvailable {
				failures = append(failures, "semantic_provider_unconfigured")
			}
			// A fresh retrieval worker heartbeat with the required handlers.
			heartbeatOK, heartbeatErr := workerHeartbeatFresh(r.Context(), deps.Store,
				deps.ManifestFingerprint,
				[]string{"retrieval_build_projection_run", "retrieval_embed_chunk_batch",
					"retrieval_finalize_projection_run", "retrieval_reconcile", "retrieval_cleanup"})
			if heartbeatErr != nil || !heartbeatOK {
				failures = append(failures, "retrieval_worker_not_ready")
			}
		}
		if len(failures) > 0 {
			sortStrings(failures)
			writeReady(w, http.StatusServiceUnavailable, failures)
			return
		}
		writeReady(w, http.StatusOK, nil)
	}
}

func writeReady(w http.ResponseWriter, status int, failures []string) {
	body := map[string]any{
		"status": "ready",
		"time":   time.Now().UTC().Format(time.RFC3339),
	}
	if len(failures) > 0 {
		body["status"] = "unavailable"
		body["failures"] = failures
	}
	writeJSONValue(w, status, body)
}

// countServingProfiles counts active/warming profiles across organizations.
func countServingProfiles(ctx context.Context, st *store.Store) (int, error) {
	var count int
	err := st.Pool.QueryRow(ctx, `
		SELECT count(*) FROM retrieval.projection_profiles WHERE status IN ('active', 'warming')
	`).Scan(&count)
	return count, err
}

// countSemanticServingProfiles counts serving profiles that require the
// semantic embedding pipeline.
func countSemanticServingProfiles(ctx context.Context, st *store.Store) (int, error) {
	var count int
	err := st.Pool.QueryRow(ctx, `
		SELECT count(*) FROM retrieval.projection_profiles
		WHERE status IN ('active', 'warming') AND semantic_enabled = true
	`).Scan(&count)
	return count, err
}

// countManifestMismatches counts serving semantic profiles whose identity does
// not hash to the runtime manifest fingerprint.
func countManifestMismatches(ctx context.Context, st *store.Store, fingerprint string) (int, error) {
	rows, err := st.Pool.Query(ctx, `
		SELECT COALESCE(embedding_provider_key,''), COALESCE(embedding_model,''),
		       COALESCE(embedding_model_version,''), COALESCE(embedding_dimensions,0),
		       tokenizer_version
		FROM retrieval.projection_profiles
		WHERE status IN ('active', 'warming') AND semantic_enabled = true
	`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	mismatched := 0
	for rows.Next() {
		var providerKey, model, modelVersion, tokenizer string
		var dimensions int
		if err := rows.Scan(&providerKey, &model, &modelVersion, &dimensions, &tokenizer); err != nil {
			return 0, err
		}
		if manifestFingerprint(providerKey, model, modelVersion, dimensions, tokenizer) != fingerprint {
			mismatched++
		}
	}
	return mismatched, rows.Err()
}

// manifestFingerprint mirrors retrieval.EmbeddingManifest.Fingerprint without
// importing the manifest type.
func manifestFingerprint(providerKey, model, modelVersion string, dimensions int, tokenizer string) string {
	joined := strings.Join([]string{
		providerKey, model, modelVersion,
		strconv.Itoa(dimensions), tokenizer, "cosine",
	}, "|")
	sum := sha256.Sum256([]byte(joined))
	return hex.EncodeToString(sum[:])
}

// workerHeartbeatFresh verifies a recent role='retrieval' heartbeat with a
// matching fingerprint and the required handler manifest.
func workerHeartbeatFresh(ctx context.Context, st *store.Store, fingerprint string, handlers []string) (bool, error) {
	var fresh bool
	err := st.Pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM system.worker_heartbeats
			WHERE role = 'retrieval'
			  AND last_seen_at > now() - interval '30 seconds'
			  AND manifest_fingerprint = $1
			  AND handler_manifest ?& $2::text[]
		)
	`, fingerprint, handlers).Scan(&fresh)
	return fresh, err
}
