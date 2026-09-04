package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	adminservice "agentchunzhi/internal/admin"
	"agentchunzhi/internal/agentapp"
	"agentchunzhi/internal/agentruntime"
	agenttask "agentchunzhi/internal/agenttask"
	assetservice "agentchunzhi/internal/asset"
	"agentchunzhi/internal/attachment"
	"agentchunzhi/internal/auth"
	"agentchunzhi/internal/authz"
	"agentchunzhi/internal/automation"
	contentservice "agentchunzhi/internal/content"
	"agentchunzhi/internal/conversation"
	"agentchunzhi/internal/delivery"
	"agentchunzhi/internal/identity"
	"agentchunzhi/internal/modelendpoint"
	"agentchunzhi/internal/organization"
	agentquery "agentchunzhi/internal/query"
	"agentchunzhi/internal/resourcemodel"
	"agentchunzhi/internal/review"
	"agentchunzhi/internal/site"
	"agentchunzhi/internal/store"
	"agentchunzhi/internal/tag"
	"agentchunzhi/internal/workspace"
)

type healthResponse struct {
	Status string `json:"status"`
	Time   string `json:"time"`
}

type Dependencies struct {
	Store                *store.Store
	Authenticator        auth.APIKeyAuthenticator
	SessionService       auth.SessionService
	ScopeResolver        authz.ScopeResolver
	WorkspacePolicy      authz.WorkspacePolicy
	QueryService         QueryService
	AttachmentService    attachment.Service
	AssetService         assetservice.Service
	AgentAppService      agentapp.Service
	AgentRuntime         agentruntime.ChatRuntime
	AgentTaskService     agenttask.Service
	ModelEndpointService modelendpoint.Service
	AdminService         adminservice.Service
	WorkspaceService     workspace.Service
	ResourceModelService resourcemodel.Service
	MemberAssetService   assetservice.MemberService
	TransferService      assetservice.TransferService
	ReviewService        review.Service
	ConversationService  conversation.Service
	AutomationService    automation.Service
	// Phase 1 member identity and organization governance services.
	OrganizationService organization.Service
	InvitationService   *organization.InvitationService
	IdentityService     *identity.Service
	LoginThrottle       *auth.LoginThrottle
	// Phase 2 tag domain services.
	TagService   tag.Service
	FacetService tag.FacetService
	// Phase 5 public-site management service (site CRUD, bindings, preview).
	Sites *site.Service
	// Phase 5 public-site read face (anonymous/optional-member visitors):
	// home/posts/detail/sections/tags/search with D4 ETags and the shared
	// public_site_ip budget (B5).
	PublicSites *site.PublicReader
	// SSR delivery face: the /sites/{slug}/... HTML pages, the page cache
	// and the real-render preview (公开站点SSR投递与样式参数空间设计方案).
	Delivery *delivery.Service
	// Phase 4 member suggestion review surface (queue, accept/reject, batch).
	SuggestionReviews *assetservice.SuggestionReviewService
	// Phase 3 retrieval operations services (projection profiles and rebuilds).
	RetrievalProfiles RetrievalProfileService
	RetrievalRebuilds RetrievalRebuildService
	// TrustedProxyCIDRs vouches the proxies allowed to set X-Forwarded-For
	// for rate-limit keys; AllowedOrigins drives the CSRF Origin policy.
	TrustedProxyCIDRs []string
	AllowedOrigins    []string
	// Phase 3 retrieval readiness: SemanticAvailable mirrors the provider
	// registry result and ManifestFingerprint is the embedding manifest
	// fingerprint compared against worker heartbeats by /readyz.
	SemanticAvailable   bool
	ManifestFingerprint string
	// Phase 3 readiness inputs: the two query secrets must be present and
	// distinct for the API to serve queries.
	SearchCursorSecret string
	QueryHashSecret    string
}

func NewHandler() http.Handler {
	return NewHandlerWithDeps(Dependencies{})
}

func NewHandlerWithDeps(deps Dependencies) http.Handler {
	var handler http.Handler = httpIdempotency(deps, newRouter(deps))
	handler = rateLimitMiddleware(handler)
	if len(deps.AllowedOrigins) > 0 {
		handler = withOriginPolicy(deps.AllowedOrigins, handler)
	}
	return withJSONDefaults(withRequestID(handler))
}

type createAssetRequest struct {
	ResourceModelID string         `json:"resource_model_id"`
	WorkspaceID     string         `json:"workspace_id"`
	Title           *string        `json:"title"`
	Markdown        *string        `json:"markdown"`
	Fields          map[string]any `json:"fields"`
	Source          map[string]any `json:"source"`
}

type updateAssetRequest struct {
	Title    *string         `json:"title"`
	Markdown *string         `json:"markdown"`
	Fields   *map[string]any `json:"fields"`
}

type registerAgentRequest struct {
	DisplayName     string   `json:"display_name"`
	ApiKeyName      string   `json:"api_key_name"`
	ApplicationName string   `json:"application_name"`
	ModelEndpointID string   `json:"model_endpoint_id"`
	RuntimeMode     string   `json:"runtime_mode"`
	WorkflowKey     string   `json:"workflow_key,omitempty"`
	Capabilities    []string `json:"capabilities"`
	AnswerPosture   string   `json:"answer_posture"`
	ExpiresAt       *string  `json:"expires_at"`
}

type replaceAgentModelPolicyRequest struct {
	ResourceModelID string   `json:"resource_model_id"`
	Actions         []string `json:"actions"`
	DataScope       string   `json:"data_scope"`
}

type rotateAgentAPIKeyRequest struct {
	Name      string  `json:"name"`
	ExpiresAt *string `json:"expires_at"`
}

type updateAgentApplicationStatusRequest struct {
	Status string `json:"status"`
}

type createAgentTaskRequest struct {
	AgentApplicationID string   `json:"agent_application_id"`
	Operation          string   `json:"operation"`
	InputAssetIDs      []string `json:"input_asset_ids"`
}

type validateAgentReferenceRequest struct {
	AssetID        string `json:"asset_id"`
	AssetVersionID string `json:"asset_version_id"`
}

type validateAgentReferencesRequest struct {
	References []validateAgentReferenceRequest `json:"references"`
}

type validateAgentReferencesResponse struct {
	References    []agentquery.AssetReference `json:"references"`
	RejectedCount int                         `json:"rejected_count"`
}

type agentChatRequest struct {
	Query          string `json:"query"`
	ConversationID string `json:"conversation_id"`
	ResponseMode   string `json:"response_mode"`
}

type agentChatResponse struct {
	Answer                 string                      `json:"answer"`
	Grounded               bool                        `json:"grounded"`
	ConversationID         string                      `json:"conversation_id,omitempty"`
	MessageID              string                      `json:"message_id,omitempty"`
	References             []agentquery.AssetReference `json:"references"`
	RejectedReferenceCount int                         `json:"rejected_reference_count"`
}

type createConversationRequest struct {
	AgentApplicationID string `json:"agent_application_id"`
	ContainerID        string `json:"container_id"`
	Title              string `json:"title"`
	Source             string `json:"source"`
	Visibility         string `json:"visibility"`
}

type appendMessageRequest struct {
	Role                   string `json:"role"`
	Content                string `json:"content"`
	ContentFormat          string `json:"content_format"`
	ProviderConversationID string `json:"provider_conversation_id"`
	ProviderMessageID      string `json:"provider_message_id"`
	Status                 string `json:"status"`
	ReplyToBlockID         string `json:"reply_to_block_id"`
}

type createDerivationRequest struct {
	SourceBlockRevisionIDs []string `json:"source_block_revision_ids"`
	ContextPolicy          string   `json:"context_policy"`
	Title                  string   `json:"title"`
}

type finalizeDerivationRequest struct {
	Disposition                  string `json:"disposition"`
	TargetAssetID                string `json:"target_asset_id"`
	ExpectedSourceAssetVersionID string `json:"expected_source_asset_version_id"`
	ExpectedTargetAssetVersionID string `json:"expected_target_asset_version_id"`
	MergeMode                    string `json:"merge_mode"`
	AutoArchive                  *bool  `json:"auto_archive"`
}

type registerMediaRequest struct {
	AttachmentID string `json:"attachment_id"`
	MediaKind    string `json:"media_kind"`
	Language     string `json:"language"`
	DurationMS   *int64 `json:"duration_ms"`
}

func createConversation(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, err := deps.SessionService.Authenticate(r.Context(), r)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		if principal.UserType != "member" {
			writeError(w, http.StatusForbidden, "member_required")
			return
		}
		idempotencyKey, ok := requestIdempotencyKey(w, r)
		if !ok {
			writeError(w, http.StatusUnprocessableEntity, "idempotency_key_invalid")
			return
		}
		var input createConversationRequest
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32*1024))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil {
			writeError(w, http.StatusUnprocessableEntity, "invalid_conversation_request")
			return
		}
		result, err := deps.ConversationService.CreateConversation(r.Context(), principal, idempotencyKey, contentservice.CreateConversationInput{
			WorkspaceID: r.PathValue("workspaceId"), AgentApplicationID: input.AgentApplicationID,
			ContainerID: input.ContainerID,
			Title:       input.Title, Source: input.Source, Visibility: input.Visibility,
		})
		if errors.Is(err, contentservice.ErrInvalidInput) {
			writeError(w, http.StatusUnprocessableEntity, "invalid_conversation_request")
			return
		}
		if errors.Is(err, contentservice.ErrNotFound) {
			writeError(w, http.StatusNotFound, "workspace_not_found")
			return
		}
		if errors.Is(err, contentservice.ErrForbidden) {
			writeError(w, http.StatusForbidden, "agent_application_not_allowed")
			return
		}
		if errors.Is(err, contentservice.ErrConflict) {
			writeError(w, http.StatusConflict, "conversation_conflict")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "conversation_create_failed")
			return
		}
		writeData(w, r, http.StatusCreated, result)
	}
}

func getConversation(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, err := deps.SessionService.Authenticate(r.Context(), r)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		if principal.UserType != "member" {
			writeError(w, http.StatusForbidden, "member_required")
			return
		}
		result, err := deps.ConversationService.GetConversation(r.Context(), principal, r.PathValue("conversationId"))
		if errors.Is(err, contentservice.ErrInvalidInput) {
			writeError(w, http.StatusUnprocessableEntity, "invalid_conversation_id")
			return
		}
		if errors.Is(err, contentservice.ErrNotFound) {
			writeError(w, http.StatusNotFound, "conversation_not_found")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "conversation_load_failed")
			return
		}
		writeData(w, r, http.StatusOK, result)
	}
}

func appendMessage(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			principal, err := deps.SessionService.Authenticate(r.Context(), r)
			if err != nil {
				writeError(w, http.StatusUnauthorized, "unauthorized")
				return
			}
			if principal.UserType != "member" {
				writeError(w, http.StatusForbidden, "member_required")
				return
			}
			result, err := deps.ConversationService.ListMessages(r.Context(), principal, r.PathValue("conversationId"))
			if errors.Is(err, contentservice.ErrInvalidInput) {
				writeError(w, http.StatusUnprocessableEntity, "invalid_conversation_id")
				return
			}
			if errors.Is(err, contentservice.ErrNotFound) {
				writeError(w, http.StatusNotFound, "conversation_not_found")
				return
			}
			if err != nil {
				writeError(w, http.StatusInternalServerError, "messages_load_failed")
				return
			}
			writeData(w, r, http.StatusOK, map[string]any{"items": result})
			return
		}
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, err := deps.SessionService.Authenticate(r.Context(), r)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		if principal.UserType != "member" {
			writeError(w, http.StatusForbidden, "member_required")
			return
		}
		idempotencyKey, ok := requestIdempotencyKey(w, r)
		if !ok {
			writeError(w, http.StatusUnprocessableEntity, "idempotency_key_invalid")
			return
		}
		var input appendMessageRequest
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256*1024))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil {
			writeError(w, http.StatusUnprocessableEntity, "invalid_message_request")
			return
		}
		if strings.TrimSpace(input.Content) == "" {
			writeError(w, http.StatusUnprocessableEntity, "blank_content")
			return
		}
		conversationID := r.PathValue("conversationId")
		if deps.Store != nil && deps.Store.Pool != nil && agentquery.ValidUUID(conversationID) {
			var conversationStatus string
			statusErr := deps.Store.Pool.QueryRow(r.Context(), `SELECT status FROM content.conversations WHERE organization_id = $1::uuid AND id = $2::uuid`, principal.OrganizationID, conversationID).Scan(&conversationStatus)
			if statusErr == nil && conversationStatus == "archived" {
				writeError(w, http.StatusConflict, "conversation_archived")
				return
			}
		}
		result, err := deps.ConversationService.AppendMessage(r.Context(), principal, idempotencyKey, contentservice.AppendMessageInput{
			ConversationID: conversationID, Role: input.Role, Content: input.Content,
			ContentFormat: input.ContentFormat, ProviderConversationID: input.ProviderConversationID,
			ProviderMessageID: input.ProviderMessageID, Status: input.Status, ReplyToBlockID: input.ReplyToBlockID,
		})
		if errors.Is(err, contentservice.ErrInvalidInput) {
			writeError(w, http.StatusUnprocessableEntity, "invalid_message_request")
			return
		}
		if errors.Is(err, contentservice.ErrNotFound) {
			writeError(w, http.StatusNotFound, "conversation_not_found")
			return
		}
		if errors.Is(err, contentservice.ErrConflict) {
			writeError(w, http.StatusConflict, "message_conflict")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "message_create_failed")
			return
		}
		writeData(w, r, http.StatusCreated, result)
	}
}

func createDerivation(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, err := deps.SessionService.Authenticate(r.Context(), r)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		if principal.UserType != "member" {
			writeError(w, http.StatusForbidden, "member_required")
			return
		}
		idempotencyKey, ok := requestIdempotencyKey(w, r)
		if !ok {
			writeError(w, http.StatusUnprocessableEntity, "idempotency_key_invalid")
			return
		}
		var input createDerivationRequest
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64*1024))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil {
			writeError(w, http.StatusUnprocessableEntity, "invalid_derivation_request")
			return
		}
		result, err := deps.ConversationService.CreateDerivation(r.Context(), principal, idempotencyKey, contentservice.CreateDerivationInput{
			SourceConversationID: r.PathValue("conversationId"), SourceBlockRevisionIDs: input.SourceBlockRevisionIDs,
			ContextPolicy: input.ContextPolicy, Title: input.Title,
		})
		if errors.Is(err, contentservice.ErrInvalidInput) {
			writeError(w, http.StatusUnprocessableEntity, "invalid_derivation_request")
			return
		}
		if errors.Is(err, contentservice.ErrNotFound) {
			writeError(w, http.StatusNotFound, "derivation_source_not_found")
			return
		}
		if errors.Is(err, contentservice.ErrConflict) {
			writeError(w, http.StatusConflict, "derivation_conflict")
			return
		}
		if err != nil {
			log.Printf("derivation create failed: conversation=%s error=%v", r.PathValue("conversationId"), err)
			writeError(w, http.StatusInternalServerError, "derivation_create_failed")
			return
		}
		writeData(w, r, http.StatusCreated, result)
	}
}

func getDerivation(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, err := deps.SessionService.Authenticate(r.Context(), r)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		if principal.UserType != "member" {
			writeError(w, http.StatusForbidden, "member_required")
			return
		}
		result, err := deps.ConversationService.GetDerivation(r.Context(), principal, r.PathValue("derivationId"))
		if errors.Is(err, contentservice.ErrInvalidInput) {
			writeError(w, http.StatusUnprocessableEntity, "invalid_derivation_id")
			return
		}
		if errors.Is(err, contentservice.ErrNotFound) {
			writeError(w, http.StatusNotFound, "derivation_not_found")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "derivation_load_failed")
			return
		}
		writeData(w, r, http.StatusOK, result)
	}
}

func finalizeDerivation(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, err := deps.SessionService.Authenticate(r.Context(), r)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		if principal.UserType != "member" {
			writeError(w, http.StatusForbidden, "member_required")
			return
		}
		idempotencyKey, ok := requestIdempotencyKey(w, r)
		if !ok {
			writeError(w, http.StatusUnprocessableEntity, "idempotency_key_invalid")
			return
		}
		var input finalizeDerivationRequest
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32*1024))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil {
			writeError(w, http.StatusUnprocessableEntity, "invalid_derivation_finalize_request")
			return
		}
		result, err := deps.ConversationService.FinalizeDerivation(r.Context(), principal, idempotencyKey, r.PathValue("derivationId"), contentservice.FinalizeDerivationInput{
			Disposition: input.Disposition, TargetAssetID: input.TargetAssetID,
			ExpectedSourceAssetVersionID: input.ExpectedSourceAssetVersionID, ExpectedTargetAssetVersionID: input.ExpectedTargetAssetVersionID,
			MergeMode: input.MergeMode,
			AutoArchive: input.AutoArchive,
		})
		if errors.Is(err, contentservice.ErrInvalidInput) {
			writeError(w, http.StatusUnprocessableEntity, "invalid_derivation_finalize_request")
			return
		}
		if errors.Is(err, contentservice.ErrNotFound) {
			writeError(w, http.StatusNotFound, "derivation_or_target_not_found")
			return
		}
		if errors.Is(err, contentservice.ErrConflict) {
			writeError(w, http.StatusConflict, "derivation_finalize_conflict")
			return
		}
		if err != nil {
			log.Printf("derivation finalize failed: derivation=%s error=%v", r.PathValue("derivationId"), err)
			writeError(w, http.StatusInternalServerError, "derivation_finalize_failed")
			return
		}
		writeData(w, r, http.StatusCreated, result)
	}
}

func registerConversationMedia(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, err := deps.SessionService.Authenticate(r.Context(), r)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		if principal.UserType != "member" {
			writeError(w, http.StatusForbidden, "member_required")
			return
		}
		idempotencyKey, ok := requestIdempotencyKey(w, r)
		if !ok {
			writeError(w, http.StatusUnprocessableEntity, "idempotency_key_invalid")
			return
		}
		var input registerMediaRequest
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16*1024))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil {
			writeError(w, http.StatusUnprocessableEntity, "invalid_media_request")
			return
		}
		result, err := deps.ConversationService.RegisterMedia(r.Context(), principal, idempotencyKey, contentservice.RegisterMediaInput{
			ConversationID: r.PathValue("conversationId"), AttachmentID: input.AttachmentID,
			MediaKind: input.MediaKind, Language: input.Language, DurationMS: input.DurationMS,
		})
		if errors.Is(err, contentservice.ErrInvalidInput) {
			writeError(w, http.StatusUnprocessableEntity, "invalid_media_request")
			return
		}
		if errors.Is(err, contentservice.ErrNotFound) {
			writeError(w, http.StatusNotFound, "conversation_or_attachment_not_found")
			return
		}
		if errors.Is(err, contentservice.ErrConflict) {
			writeError(w, http.StatusConflict, "media_registration_conflict")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "media_registration_failed")
			return
		}
		writeData(w, r, http.StatusCreated, result)
	}
}

func getConversationMedia(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, err := deps.SessionService.Authenticate(r.Context(), r)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		if principal.UserType != "member" {
			writeError(w, http.StatusForbidden, "member_required")
			return
		}
		result, err := deps.ConversationService.GetMedia(r.Context(), principal, r.PathValue("mediaId"))
		if errors.Is(err, contentservice.ErrInvalidInput) {
			writeError(w, http.StatusUnprocessableEntity, "invalid_media_id")
			return
		}
		if errors.Is(err, contentservice.ErrNotFound) {
			writeError(w, http.StatusNotFound, "media_not_found")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "media_load_failed")
			return
		}
		writeData(w, r, http.StatusOK, result)
	}
}

func transcribeConversationMedia(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, err := deps.SessionService.Authenticate(r.Context(), r)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		if principal.UserType != "member" {
			writeError(w, http.StatusForbidden, "member_required")
			return
		}
		idempotencyKey, ok := requestIdempotencyKey(w, r)
		if !ok {
			writeError(w, http.StatusUnprocessableEntity, "idempotency_key_invalid")
			return
		}
		result, err := deps.ConversationService.RequestTranscription(r.Context(), principal, idempotencyKey, r.PathValue("mediaId"))
		if errors.Is(err, contentservice.ErrInvalidInput) {
			writeError(w, http.StatusUnprocessableEntity, "invalid_media_id")
			return
		}
		if errors.Is(err, contentservice.ErrNotFound) {
			writeError(w, http.StatusNotFound, "media_not_found")
			return
		}
		if errors.Is(err, contentservice.ErrConflict) {
			writeError(w, http.StatusConflict, "transcription_conflict")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "transcription_request_failed")
			return
		}
		writeData(w, r, http.StatusAccepted, result)
	}
}

func registerAgent(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, err := deps.SessionService.Authenticate(r.Context(), r)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		if principal.UserType != "member" {
			writeError(w, http.StatusForbidden, "member_required")
			return
		}
		allowed, err := deps.ScopeResolver.AllowedSystemResourceIDs(r.Context(), principal, "agent.manage")
		if err != nil {
			writeError(w, http.StatusInternalServerError, "authorization_scope_failed")
			return
		}
		if !containsScope(allowed, "system:agent-users") {
			writeError(w, http.StatusForbidden, "permission_denied")
			return
		}
		idempotencyKey, ok := requestIdempotencyKey(w, r)
		if !ok {
			writeError(w, http.StatusUnprocessableEntity, "idempotency_key_invalid")
			return
		}
		var input registerAgentRequest
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32*1024))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil {
			writeError(w, http.StatusUnprocessableEntity, "invalid_agent_request")
			return
		}
		expiresAt, ok := parseOptionalExpiry(input.ExpiresAt)
		if !ok {
			writeError(w, http.StatusUnprocessableEntity, "invalid_agent_request")
			return
		}
		result, err := deps.AdminService.RegisterAgent(r.Context(), principal, adminservice.RegisterAgentInput{
			DisplayName:     input.DisplayName,
			ApiKeyName:      input.ApiKeyName,
			ApplicationName: input.ApplicationName,
			ModelEndpointID: input.ModelEndpointID,
			RuntimeMode:     input.RuntimeMode,
			WorkflowKey:     input.WorkflowKey,
			Capabilities:    input.Capabilities,
			ExpiresAt:       expiresAt,
			IdempotencyKey:  idempotencyKey,
		})
		if errors.Is(err, adminservice.ErrInvalidInput) {
			writeError(w, http.StatusUnprocessableEntity, "invalid_agent_request")
			return
		}
		if errors.Is(err, adminservice.ErrConflict) {
			writeError(w, http.StatusConflict, "agent_registration_conflict")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "agent_registration_failed")
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusCreated, result)
	}
}

func replaceAgentModelPolicy(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, err := deps.SessionService.Authenticate(r.Context(), r)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		if principal.UserType != "member" {
			writeError(w, http.StatusForbidden, "member_required")
			return
		}
		allowed, err := deps.ScopeResolver.AllowedSystemResourceIDs(r.Context(), principal, "agent.manage")
		if err != nil {
			writeError(w, http.StatusInternalServerError, "authorization_scope_failed")
			return
		}
		if !containsScope(allowed, "system:agent-users") {
			writeError(w, http.StatusForbidden, "permission_denied")
			return
		}
		agentUserID := r.PathValue("agentUserId")
		if !agentquery.ValidUUID(agentUserID) {
			writeError(w, http.StatusUnprocessableEntity, "invalid_agent_user_id")
			return
		}
		idempotencyKey, ok := requestIdempotencyKey(w, r)
		if !ok {
			writeError(w, http.StatusUnprocessableEntity, "idempotency_key_invalid")
			return
		}
		var input replaceAgentModelPolicyRequest
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16*1024))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil {
			writeError(w, http.StatusUnprocessableEntity, "invalid_agent_policy_request")
			return
		}
		result, err := deps.AdminService.ReplaceAgentModelPolicy(r.Context(), principal, adminservice.ReplacePolicyInput{
			AgentUserID:     agentUserID,
			ResourceModelID: input.ResourceModelID,
			Actions:         input.Actions,
			DataScope:       input.DataScope,
			IdempotencyKey:  idempotencyKey,
		})
		if errors.Is(err, adminservice.ErrInvalidInput) {
			writeError(w, http.StatusUnprocessableEntity, "invalid_agent_policy_request")
			return
		}
		if errors.Is(err, adminservice.ErrPolicyNotFound) {
			writeError(w, http.StatusNotFound, "agent_or_model_not_found")
			return
		}
		if errors.Is(err, adminservice.ErrConflict) {
			writeError(w, http.StatusConflict, "agent_policy_conflict")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "agent_policy_update_failed")
			return
		}
		writeJSON(w, http.StatusOK, result)
	}
}

func rotateAgentAPIKey(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, err := deps.SessionService.Authenticate(r.Context(), r)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		if principal.UserType != "member" {
			writeError(w, http.StatusForbidden, "member_required")
			return
		}
		allowed, err := deps.ScopeResolver.AllowedSystemResourceIDs(r.Context(), principal, "agent.manage")
		if err != nil {
			writeError(w, http.StatusInternalServerError, "authorization_scope_failed")
			return
		}
		if !containsScope(allowed, "system:agent-users") {
			writeError(w, http.StatusForbidden, "permission_denied")
			return
		}
		agentUserID := r.PathValue("agentUserId")
		if !agentquery.ValidUUID(agentUserID) {
			writeError(w, http.StatusUnprocessableEntity, "invalid_agent_user_id")
			return
		}
		idempotencyKey, ok := requestIdempotencyKey(w, r)
		if !ok {
			writeError(w, http.StatusUnprocessableEntity, "idempotency_key_invalid")
			return
		}
		var input rotateAgentAPIKeyRequest
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16*1024))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil {
			writeError(w, http.StatusUnprocessableEntity, "invalid_agent_api_key_request")
			return
		}
		expiresAt, ok := parseOptionalExpiry(input.ExpiresAt)
		if !ok {
			writeError(w, http.StatusUnprocessableEntity, "invalid_agent_api_key_request")
			return
		}
		result, err := deps.AdminService.RotateAgentAPIKey(r.Context(), principal, adminservice.RotateAPIKeyInput{
			AgentUserID:    agentUserID,
			Name:           input.Name,
			ExpiresAt:      expiresAt,
			IdempotencyKey: idempotencyKey,
		})
		if errors.Is(err, adminservice.ErrInvalidInput) {
			writeError(w, http.StatusUnprocessableEntity, "invalid_agent_api_key_request")
			return
		}
		if errors.Is(err, adminservice.ErrAgentNotFound) {
			writeError(w, http.StatusNotFound, "agent_user_not_found")
			return
		}
		if errors.Is(err, adminservice.ErrConflict) {
			writeError(w, http.StatusConflict, "agent_api_key_rotation_conflict")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "agent_api_key_rotation_failed")
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusOK, result)
	}
}

func listAgentApplications(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, ok := requireAgentManagement(w, r, deps)
		if !ok {
			return
		}
		limit := 100
		if value := strings.TrimSpace(r.URL.Query().Get("limit")); value != "" {
			parsed, err := strconv.Atoi(value)
			if err != nil || parsed < 1 || parsed > 100 {
				writeError(w, http.StatusUnprocessableEntity, "invalid_agent_application_limit")
				return
			}
			limit = parsed
		}
		result, err := deps.AdminService.ListAgentApplications(r.Context(), principal, limit)
		if errors.Is(err, adminservice.ErrApplicationListInvalidInput) {
			writeError(w, http.StatusUnprocessableEntity, "invalid_agent_application_request")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "agent_application_list_failed")
			return
		}
		writeJSON(w, http.StatusOK, result)
	}
}

func getAgentApplication(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, ok := requireAgentManagement(w, r, deps)
		if !ok {
			return
		}
		applicationID := r.PathValue("applicationId")
		if !agentquery.ValidUUID(applicationID) {
			writeError(w, http.StatusUnprocessableEntity, "invalid_agent_application_id")
			return
		}
		result, err := deps.AdminService.GetAgentApplication(r.Context(), principal, applicationID)
		if errors.Is(err, adminservice.ErrApplicationListInvalidInput) {
			writeError(w, http.StatusUnprocessableEntity, "invalid_agent_application_id")
			return
		}
		if errors.Is(err, adminservice.ErrApplicationNotFound) {
			writeError(w, http.StatusNotFound, "agent_application_not_found")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "agent_application_read_failed")
			return
		}
		writeJSON(w, http.StatusOK, result)
	}
}

func updateAgentApplicationStatus(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, ok := requireAgentManagement(w, r, deps)
		if !ok {
			return
		}
		applicationID := r.PathValue("applicationId")
		if !agentquery.ValidUUID(applicationID) {
			writeError(w, http.StatusUnprocessableEntity, "invalid_agent_application_id")
			return
		}
		idempotencyKey, ok := requestIdempotencyKey(w, r)
		if !ok {
			writeError(w, http.StatusUnprocessableEntity, "idempotency_key_invalid")
			return
		}
		var input updateAgentApplicationStatusRequest
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16*1024))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil {
			writeError(w, http.StatusUnprocessableEntity, "invalid_agent_application_status_request")
			return
		}
		result, err := deps.AdminService.SetAgentApplicationStatus(r.Context(), principal, adminservice.SetApplicationStatusInput{
			ApplicationID:  applicationID,
			Status:         input.Status,
			IdempotencyKey: idempotencyKey,
		})
		if errors.Is(err, adminservice.ErrApplicationStatusInvalidInput) {
			writeError(w, http.StatusUnprocessableEntity, "invalid_agent_application_status_request")
			return
		}
		if errors.Is(err, adminservice.ErrApplicationNotFound) {
			writeError(w, http.StatusNotFound, "agent_application_not_found")
			return
		}
		if errors.Is(err, adminservice.ErrConflict) {
			writeError(w, http.StatusConflict, "agent_application_status_conflict")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "agent_application_status_update_failed")
			return
		}
		writeJSON(w, http.StatusOK, result)
	}
}

func requireAgentManagement(w http.ResponseWriter, r *http.Request, deps Dependencies) (auth.Principal, bool) {
	principal, err := deps.SessionService.Authenticate(r.Context(), r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return auth.Principal{}, false
	}
	if principal.UserType != "member" {
		writeError(w, http.StatusForbidden, "member_required")
		return auth.Principal{}, false
	}
	allowed, err := deps.ScopeResolver.AllowedSystemResourceIDs(r.Context(), principal, "agent.manage")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "authorization_scope_failed")
		return auth.Principal{}, false
	}
	if !containsScope(allowed, "system:agent-users") {
		writeError(w, http.StatusForbidden, "permission_denied")
		return auth.Principal{}, false
	}
	return principal, true
}

func parseOptionalExpiry(value *string) (*time.Time, bool) {
	if value == nil {
		return nil, true
	}
	trimmed := strings.TrimSpace(*value)
	if trimmed == "" {
		return nil, false
	}
	parsed, err := time.Parse(time.RFC3339, trimmed)
	if err != nil {
		return nil, false
	}
	return &parsed, true
}

func startAgentApplicationSession(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, err := deps.SessionService.Authenticate(r.Context(), r)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		if principal.UserType != "member" {
			writeError(w, http.StatusForbidden, "member_required")
			return
		}
		idempotencyKey, ok := requestIdempotencyKey(w, r)
		if !ok {
			writeError(w, http.StatusUnprocessableEntity, "idempotency_key_invalid")
			return
		}
		applicationID := r.PathValue("applicationId")
		if !agentquery.ValidUUID(applicationID) {
			writeError(w, http.StatusUnprocessableEntity, "invalid_agent_application_id")
			return
		}
		allowedApplications, err := deps.ScopeResolver.AllowedAgentApplicationIDs(r.Context(), principal, "agent.use")
		if err != nil {
			writeError(w, http.StatusInternalServerError, "authorization_scope_failed")
			return
		}
		result, err := deps.AgentAppService.Start(r.Context(), principal, allowedApplications, applicationID, idempotencyKey)
		if errors.Is(err, agentapp.ErrInvalidInput) {
			writeError(w, http.StatusUnprocessableEntity, "invalid_agent_application_request")
			return
		}
		if errors.Is(err, agentapp.ErrNotFound) {
			writeError(w, http.StatusNotFound, "agent_application_not_found")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "agent_session_start_failed")
			return
		}
		writeJSON(w, http.StatusCreated, result)
	}
}

func validateAgentSessionReferences(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		member, err := deps.SessionService.Authenticate(r.Context(), r)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		if member.UserType != "member" {
			writeError(w, http.StatusForbidden, "member_required")
			return
		}
		sessionID := r.PathValue("sessionId")
		if !agentquery.ValidUUID(sessionID) {
			writeError(w, http.StatusUnprocessableEntity, "invalid_agent_session_id")
			return
		}
		var input validateAgentReferencesRequest
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64*1024))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil || len(input.References) == 0 || len(input.References) > 50 {
			writeError(w, http.StatusUnprocessableEntity, "invalid_reference_request")
			return
		}
		agentPrincipal, err := deps.AgentAppService.ResolveActiveAgentPrincipal(r.Context(), member, sessionID)
		if errors.Is(err, agentapp.ErrInvalidInput) {
			writeError(w, http.StatusUnprocessableEntity, "invalid_agent_session_id")
			return
		}
		if errors.Is(err, agentapp.ErrNotFound) {
			writeError(w, http.StatusNotFound, "agent_session_not_found")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "agent_session_resolution_failed")
			return
		}
		result, err := validateAgentReferences(r.Context(), deps, agentPrincipal, input.References)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "reference_validation_failed")
			return
		}
		writeJSON(w, http.StatusOK, result)
	}
}

func validateAgentReferences(ctx context.Context, deps Dependencies, agentPrincipal auth.Principal, requested []validateAgentReferenceRequest) (validateAgentReferencesResponse, error) {
	allowedModels, err := deps.ScopeResolver.AllowedModelIDs(ctx, agentPrincipal, "asset.read")
	if err != nil {
		return validateAgentReferencesResponse{}, fmt.Errorf("resolve agent reference scope: %w", err)
	}
	result := validateAgentReferencesResponse{References: make([]agentquery.AssetReference, 0, len(requested))}
	seen := make(map[string]struct{}, len(requested))
	for _, item := range requested {
		assetID := strings.TrimSpace(item.AssetID)
		versionID := strings.TrimSpace(item.AssetVersionID)
		key := assetID + ":" + versionID
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		if !agentquery.ValidUUID(assetID) || !agentquery.ValidUUID(versionID) {
			result.RejectedCount++
			continue
		}
		reference, referenceErr := deps.QueryService.Reference(ctx, agentPrincipal, assetID, allowedModels)
		if errors.Is(referenceErr, agentquery.ErrReferenceNotFound) || (referenceErr == nil && reference.AssetVersionID != versionID) {
			result.RejectedCount++
			continue
		}
		if referenceErr != nil {
			return validateAgentReferencesResponse{}, referenceErr
		}
		result.References = append(result.References, reference)
	}
	return result, nil
}

func chatAgentSession(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		member, err := deps.SessionService.Authenticate(r.Context(), r)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		if member.UserType != "member" {
			writeError(w, http.StatusForbidden, "member_required")
			return
		}
		sessionID := r.PathValue("sessionId")
		if !agentquery.ValidUUID(sessionID) {
			writeError(w, http.StatusUnprocessableEntity, "invalid_agent_session_id")
			return
		}
		var input agentChatRequest
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32*1024))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil {
			writeError(w, http.StatusUnprocessableEntity, "invalid_agent_chat_request")
			return
		}
		input.Query = strings.TrimSpace(input.Query)
		input.ConversationID = strings.TrimSpace(input.ConversationID)
		if input.Query == "" || len(input.Query) > 10000 || len(input.ConversationID) > 200 {
			writeError(w, http.StatusUnprocessableEntity, "invalid_agent_chat_request")
			return
		}
		chatMessageKey := chatIdempotencyKey(r, sessionID, input.Query)
		binding, err := deps.AgentAppService.ResolveActiveSession(r.Context(), member, sessionID)
		if errors.Is(err, agentapp.ErrInvalidInput) {
			writeError(w, http.StatusUnprocessableEntity, "invalid_agent_session_id")
			return
		}
		if errors.Is(err, agentapp.ErrNotFound) {
			writeError(w, http.StatusNotFound, "agent_session_not_found")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "agent_session_resolution_failed")
			return
		}
		switch input.ResponseMode {
		case "", "answer", "answer_with_sources":
		default:
			writeError(w, http.StatusUnprocessableEntity, "invalid_agent_chat_request")
			return
		}
		chatOutcome := "error"
		chatAuditMetadata := map[string]any{
			"query_hash":      hashQuery(input.Query),
			"conversation_id": input.ConversationID,
			"runtime_mode":    binding.RuntimeMode,
			"answer_posture":  binding.AnswerPosture,
			"response_mode":   input.ResponseMode,
		}
		defer func() {
			if deps.Store != nil {
				auditCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				_ = deps.Store.RecordAgentChatAudit(auditCtx, binding.AgentPrincipal.OrganizationID, binding.AgentPrincipal.UserID, member.UserID, binding.AgentApplicationID, sessionID, chatOutcome, chatAuditMetadata)
			}
		}()
		if deps.AgentRuntime == nil {
			writeError(w, http.StatusServiceUnavailable, "agent_runtime_unavailable")
			return
		}
		history, err := loadAgentChatHistory(r.Context(), deps, member, input.ConversationID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "conversation_history_load_failed")
			return
		}
		if err := persistChatMessage(r.Context(), deps, member, chatMessageKey+"-human", input.ConversationID, contentservice.AppendMessageInput{
			Role: "user", Content: input.Query, ContentFormat: "plain_text", Status: "completed",
		}); err != nil {
			writeError(w, http.StatusInternalServerError, "conversation_message_persist_failed")
			return
		}
		result, err := deps.AgentRuntime.Chat(r.Context(), agentruntime.ChatRequest{
			OrganizationID: binding.AgentPrincipal.OrganizationID, AgentApplicationID: binding.AgentApplicationID,
			SessionID: sessionID, ConversationID: input.ConversationID, RuntimeMode: binding.RuntimeMode,
			AnswerPosture: binding.AnswerPosture, ResponseMode: input.ResponseMode,
			AgentPrincipal: binding.AgentPrincipal, Query: input.Query, History: history,
		})
		if err != nil {
			writeAgentRuntimeError(w, err)
			return
		}
		if err := persistChatMessage(r.Context(), deps, member, chatMessageKey+"-assistant", input.ConversationID, contentservice.AppendMessageInput{
			Role: "assistant", Content: result.Answer, ContentFormat: "markdown", Status: "completed",
			ProviderMessageID: result.MessageID, References: messageReferenceInputs(result.References),
		}); err != nil {
			writeError(w, http.StatusInternalServerError, "conversation_message_persist_failed")
			return
		}
		chatOutcome = "allowed"
		addAgentRuntimeAuditMetadata(chatAuditMetadata, result)
		writeData(w, r, http.StatusOK, agentChatResponse{
			Answer: result.Answer, Grounded: result.Grounded, ConversationID: result.ConversationID, MessageID: result.MessageID,
			References: result.References, RejectedReferenceCount: result.RejectedReferenceCount,
		})
	}
}

func messageReferenceInputs(references []agentquery.AssetReference) []contentservice.MessageReferenceInput {
	result := make([]contentservice.MessageReferenceInput, 0, len(references))
	for _, reference := range references {
		result = append(result, contentservice.MessageReferenceInput{
			AssetID: reference.AssetID, AssetVersionID: reference.AssetVersionID, Title: reference.Title,
			URL: reference.URL, SourceExcerpt: reference.SourceExcerpt, UpdatedAt: reference.UpdatedAt,
		})
	}
	return result
}

func loadAgentChatHistory(ctx context.Context, deps Dependencies, principal auth.Principal, conversationID string) ([]agentruntime.ChatMessage, error) {
	if !agentquery.ValidUUID(conversationID) || deps.ConversationService.Store == nil {
		return []agentruntime.ChatMessage{}, nil
	}
	messages, err := deps.ConversationService.ListMessages(ctx, principal, conversationID)
	if err != nil {
		return nil, err
	}
	history := make([]agentruntime.ChatMessage, 0, len(messages))
	for _, message := range messages {
		if message.Status != "completed" || (message.Role != "user" && message.Role != "assistant") || strings.TrimSpace(message.Content) == "" {
			continue
		}
		history = append(history, agentruntime.ChatMessage{Role: message.Role, Content: message.Content})
	}
	return history, nil
}

func writeAgentRuntimeError(w http.ResponseWriter, err error) {
	log.Printf("agent runtime error: %v", err)
	code := agentRuntimeErrorCode(err)
	switch code {
	case "invalid_agent_chat_request":
		writeError(w, http.StatusUnprocessableEntity, code)
	case "agent_runtime_requires_run":
		writeError(w, http.StatusConflict, code)
	case "agent_data_access_denied", "agent_model_scope_mismatch":
		writeError(w, http.StatusForbidden, code)
	case "agent_model_unavailable":
		writeError(w, http.StatusServiceUnavailable, code)
	default:
		writeError(w, http.StatusBadGateway, code)
	}
}

func agentRuntimeErrorCode(err error) string {
	switch {
	case errors.Is(err, agentruntime.ErrInvalidChatRequest), errors.Is(err, agentquery.ErrInvalidQuery):
		return "invalid_agent_chat_request"
	case errors.Is(err, agentruntime.ErrUnsupportedRuntimeMode):
		return "agent_runtime_requires_run"
	case errors.Is(err, agentruntime.ErrModelScopeMismatch):
		return "agent_model_scope_mismatch"
	case errors.Is(err, agentquery.ErrModelAccessDenied):
		return "agent_data_access_denied"
	case errors.Is(err, agentquery.ErrQueryScopeForbidden):
		// A scope compilation failure is a permission/configuration problem,
		// not a model fault; surfacing it as 502 model_failed misled everyone.
		return "agent_data_access_denied"
	case errors.Is(err, modelendpoint.ErrUnavailable):
		return "agent_model_unavailable"
	default:
		return "agent_model_failed"
	}
}

func addAgentRuntimeAuditMetadata(metadata map[string]any, result agentruntime.ChatResult) {
	metadata["message_id"] = result.MessageID
	metadata["reference_count"] = len(result.References)
	metadata["rejected_reference_count"] = result.RejectedReferenceCount
	metadata["retrieval_count"] = result.RetrievalCount
	metadata["policy_revision"] = result.PolicyRevision
	metadata["model_endpoint_id"] = result.ModelEndpointID
	metadata["model_endpoint_revision"] = result.ModelEndpointRevision
	metadata["provider_type"] = result.ProviderType
	metadata["model_name"] = result.ModelName
	metadata["model_request_id"] = result.ModelRequestID
	metadata["input_tokens"] = result.Usage.InputTokens
	metadata["output_tokens"] = result.Usage.OutputTokens
	metadata["total_tokens"] = result.Usage.TotalTokens
	metadata["reasoning_tokens"] = result.Usage.ReasoningTokens
	metadata["cached_input_tokens"] = result.Usage.CachedInputTokens
	metadata["total_latency_ms"] = result.TotalLatency.Milliseconds()
	metadata["first_token_latency_ms"] = result.FirstTokenLatency.Milliseconds()
}

func streamAgentSession(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		member, err := deps.SessionService.Authenticate(r.Context(), r)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		if member.UserType != "member" {
			writeError(w, http.StatusForbidden, "member_required")
			return
		}
		sessionID := r.PathValue("sessionId")
		if !agentquery.ValidUUID(sessionID) {
			writeError(w, http.StatusUnprocessableEntity, "invalid_agent_session_id")
			return
		}
		var input agentChatRequest
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32*1024))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil {
			writeError(w, http.StatusUnprocessableEntity, "invalid_agent_chat_request")
			return
		}
		input.Query = strings.TrimSpace(input.Query)
		input.ConversationID = strings.TrimSpace(input.ConversationID)
		if input.Query == "" || len(input.Query) > 10000 || len(input.ConversationID) > 200 {
			writeError(w, http.StatusUnprocessableEntity, "invalid_agent_chat_request")
			return
		}
		chatMessageKey := chatIdempotencyKey(r, sessionID, input.Query)
		binding, err := deps.AgentAppService.ResolveActiveSession(r.Context(), member, sessionID)
		if errors.Is(err, agentapp.ErrInvalidInput) {
			writeError(w, http.StatusUnprocessableEntity, "invalid_agent_session_id")
			return
		}
		if errors.Is(err, agentapp.ErrNotFound) {
			writeError(w, http.StatusNotFound, "agent_session_not_found")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "agent_session_resolution_failed")
			return
		}
		if deps.AgentRuntime == nil {
			writeError(w, http.StatusServiceUnavailable, "agent_runtime_unavailable")
			return
		}
		history, err := loadAgentChatHistory(r.Context(), deps, member, input.ConversationID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "conversation_history_load_failed")
			return
		}
		flusher, ok := w.(http.Flusher)
		if !ok {
			writeError(w, http.StatusInternalServerError, "streaming_not_supported")
			return
		}
		setSSEHeaders(w)
		if _, hasLastID := parseLastEventID(r); hasLastID {
			_ = writeSSE(w, flusher, "reset", map[string]string{"recovery": "/api/conversations/" + input.ConversationID + "/messages"})
			return
		}
		switch input.ResponseMode {
		case "", "answer", "answer_with_sources":
		default:
			_ = writeSSE(w, flusher, "error", map[string]string{"code": "invalid_agent_chat_request", "message": "invalid agent chat request"})
			return
		}
		chatOutcome := "error"
		chatAuditMetadata := map[string]any{
			"query_hash":      hashQuery(input.Query),
			"conversation_id": input.ConversationID,
			"stream":          true,
			"runtime_mode":    binding.RuntimeMode,
			"answer_posture":  binding.AnswerPosture,
			"response_mode":   input.ResponseMode,
		}
		defer func() {
			if deps.Store != nil {
				auditCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				_ = deps.Store.RecordAgentChatAudit(auditCtx, binding.AgentPrincipal.OrganizationID, binding.AgentPrincipal.UserID, member.UserID, binding.AgentApplicationID, sessionID, chatOutcome, chatAuditMetadata)
			}
		}()
		if err := persistChatMessage(r.Context(), deps, member, chatMessageKey+"-human", input.ConversationID, contentservice.AppendMessageInput{
			Role: "user", Content: input.Query, ContentFormat: "plain_text", Status: "completed",
		}); err != nil {
			_ = writeSSE(w, flusher, "error", map[string]string{"code": "conversation_message_persist_failed", "message": "conversation message persist failed"})
			return
		}
		streamMessageID := deterministicUUID(chatMessageKey)
		eventID := int64(1)
		if err := writeSSEWithID(w, flusher, eventID, "message.start", map[string]string{"message_id": streamMessageID}); err != nil {
			return
		}
		type streamTerminal struct {
			Result agentruntime.ChatResult
			Err    error
		}
		deltas := make(chan string, 64)
		terminal := make(chan streamTerminal, 1)
		go func() {
			var final agentruntime.ChatResult
			err := deps.AgentRuntime.StreamChat(r.Context(), agentruntime.ChatRequest{
				OrganizationID: binding.AgentPrincipal.OrganizationID, AgentApplicationID: binding.AgentApplicationID,
				SessionID: sessionID, ConversationID: input.ConversationID, MessageID: streamMessageID,
				RuntimeMode: binding.RuntimeMode, AnswerPosture: binding.AnswerPosture, ResponseMode: input.ResponseMode,
				AgentPrincipal: binding.AgentPrincipal, Query: input.Query, History: history,
			}, func(event agentruntime.StreamEvent) error {
				if event.Result != nil {
					final = *event.Result
				}
				if event.Delta != "" {
					select {
					case deltas <- event.Delta:
						return nil
					case <-r.Context().Done():
						return r.Context().Err()
					}
				}
				return nil
			})
			terminal <- streamTerminal{Result: final, Err: err}
		}()
		heartbeat := time.NewTicker(15 * time.Second)
		defer heartbeat.Stop()
		var final agentruntime.ChatResult
		var streamErr error
	streamLoop:
		for {
			select {
			case delta := <-deltas:
				eventID++
				if err := writeSSEWithID(w, flusher, eventID, "message.delta", map[string]string{"text": delta}); err != nil {
					return
				}
			case result := <-terminal:
				final, streamErr = result.Result, result.Err
				break streamLoop
			case <-heartbeat.C:
				if err := writeSSE(w, flusher, "heartbeat", map[string]any{}); err != nil {
					return
				}
			case <-r.Context().Done():
				return
			}
		}
		if streamErr != nil {
			code := agentRuntimeErrorCode(streamErr)
			_ = writeSSE(w, flusher, "error", map[string]string{"code": code, "message": "agent runtime failed"})
			return
		}
		if err := persistChatMessage(r.Context(), deps, member, chatMessageKey+"-assistant", input.ConversationID, contentservice.AppendMessageInput{
			Role: "assistant", Content: final.Answer, ContentFormat: "markdown", Status: "completed",
			ProviderMessageID: final.MessageID, References: messageReferenceInputs(final.References),
		}); err != nil {
			_ = writeSSE(w, flusher, "error", map[string]string{"code": "conversation_message_persist_failed", "message": "conversation message persist failed"})
			return
		}
		chatOutcome = "allowed"
		addAgentRuntimeAuditMetadata(chatAuditMetadata, final)
		for _, reference := range final.References {
			eventID++
			if err := writeSSEWithID(w, flusher, eventID, "reference", reference); err != nil {
				return
			}
		}
		eventID++
		if err := writeSSEWithID(w, flusher, eventID, "message.complete", map[string]any{
			"message_id": streamMessageID, "conversation_id": final.ConversationID,
			"grounded": final.Grounded,
			"rejected_reference_count": final.RejectedReferenceCount, "usage": final.Usage,
		}); err != nil {
			return
		}
	}
}

func persistChatMessage(ctx context.Context, deps Dependencies, principal auth.Principal, idempotencyKey, conversationID string, input contentservice.AppendMessageInput) error {
	if !agentquery.ValidUUID(conversationID) || deps.ConversationService.Store == nil {
		return nil
	}
	input.ConversationID = conversationID
	_, err := deps.ConversationService.AppendMessage(ctx, principal, idempotencyKey, input)
	return err
}

func chatIdempotencyKey(r *http.Request, sessionID, query string) string {
	if value := strings.TrimSpace(r.Header.Get("Idempotency-Key")); len(value) >= 16 && len(value) <= 180 {
		return "chat-" + value
	}
	return "chat-" + hashQuery(fmt.Sprintf("%s\x00%s\x00%d", sessionID, query, time.Now().UTC().UnixNano()))
}

func writeSSE(w http.ResponseWriter, flusher http.Flusher, event string, value any) error {
	payload, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, payload); err != nil {
		return err
	}
	flusher.Flush()
	return nil
}

func writeSSEWithID(w http.ResponseWriter, flusher http.Flusher, id int64, event string, value any) error {
	payload, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", id, event, payload); err != nil {
		return err
	}
	flusher.Flush()
	return nil
}

func deterministicUUID(value string) string {
	sum := sha256.Sum256([]byte(value))
	sum[6] = (sum[6] & 0x0f) | 0x40
	sum[8] = (sum[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", sum[0:4], sum[4:6], sum[6:8], sum[8:10], sum[10:16])
}

func createAsset(deps Dependencies) http.HandlerFunc {
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
		idempotencyKey, ok := requestIdempotencyKey(w, r)
		if !ok {
			writeError(w, http.StatusUnprocessableEntity, "idempotency_key_invalid")
			return
		}
		var input createAssetRequest
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil {
			writeError(w, http.StatusUnprocessableEntity, "invalid_asset_request")
			return
		}
		if !rejectBlankText(w, input.Title, input.Markdown) {
			return
		}
		allowedModels, err := deps.ScopeResolver.AllowedModelIDs(r.Context(), principal, "asset.create")
		if err != nil {
			writeError(w, http.StatusInternalServerError, "authorization_scope_failed")
			return
		}
		result, err := deps.AssetService.Create(r.Context(), principal, allowedModels, idempotencyKey, assetservice.CreateInput{
			ResourceModelID: input.ResourceModelID,
			WorkspaceID:     input.WorkspaceID,
			Title:           input.Title,
			Markdown:        input.Markdown,
			Fields:          input.Fields,
			Source:          input.Source,
		})
		writeAssetMutationError(w, err, "asset_create_failed")
		if err != nil {
			return
		}
		writeJSON(w, http.StatusCreated, result)
	}
}

func updateAsset(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, err := deps.Authenticator.Authenticate(r.Context(), r)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		if !requireAgentCapability(w, principal, "asset.edit") {
			return
		}
		idempotencyKey, ok := requestIdempotencyKey(w, r)
		if !ok {
			writeError(w, http.StatusUnprocessableEntity, "idempotency_key_invalid")
			return
		}
		expectedVersionID := strings.Trim(strings.TrimSpace(r.Header.Get("If-Match")), "\"")
		if !agentquery.ValidUUID(expectedVersionID) {
			writeError(w, http.StatusUnprocessableEntity, "if_match_required")
			return
		}
		assetID := r.PathValue("assetId")
		if !agentquery.ValidUUID(assetID) {
			writeError(w, http.StatusUnprocessableEntity, "invalid_asset_id")
			return
		}
		var input updateAssetRequest
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil {
			writeError(w, http.StatusUnprocessableEntity, "invalid_asset_request")
			return
		}
		if !rejectBlankText(w, input.Title, input.Markdown) {
			return
		}
		allowedModels, err := deps.ScopeResolver.AllowedModelIDs(r.Context(), principal, "asset.edit")
		if err != nil {
			writeError(w, http.StatusInternalServerError, "authorization_scope_failed")
			return
		}
		result, err := deps.AssetService.Update(r.Context(), principal, allowedModels, idempotencyKey, assetID, expectedVersionID, assetservice.UpdateInput{
			Title:    input.Title,
			Markdown: input.Markdown,
			Fields:   input.Fields,
		})
		writeAssetMutationError(w, err, "asset_update_failed")
		if err != nil {
			return
		}
		writeJSON(w, http.StatusOK, result)
	}
}

func writeAssetMutationError(w http.ResponseWriter, err error, fallback string) {
	if err == nil {
		return
	}
	switch {
	case errors.Is(err, assetservice.ErrNotFound):
		writeError(w, http.StatusNotFound, "asset_not_found")
	case errors.Is(err, assetservice.ErrConflict), errors.Is(err, assetservice.ErrIdempotencyConflict):
		writeError(w, http.StatusConflict, "asset_conflict")
	case errors.Is(err, assetservice.ErrInvalidInput):
		writeError(w, http.StatusUnprocessableEntity, "invalid_asset_request")
	default:
		writeError(w, http.StatusInternalServerError, fallback)
	}
}

type publishAssetRequest struct {
	VersionID string `json:"version_id"`
	// ScheduledAt optionally defers the publish to a future moment for
	// direct-policy assets (G4): a scheduled intent instead of an immediate
	// pointer switch.
	ScheduledAt *time.Time `json:"scheduled_at"`
}

func publishAsset(deps Dependencies) http.HandlerFunc {
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
		if !requireAgentCapability(w, principal, "asset.publish") {
			return
		}
		assetID := r.PathValue("assetId")
		if !agentquery.ValidUUID(assetID) {
			writeError(w, http.StatusUnprocessableEntity, "invalid_asset_id")
			return
		}
		var input publishAssetRequest
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16*1024))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil || !agentquery.ValidUUID(input.VersionID) {
			writeError(w, http.StatusUnprocessableEntity, "version_id_required")
			return
		}
		// A future scheduled_at defers the publish (G4): the intent lands as
		// a scheduled publication request executed at the due moment.
		if input.ScheduledAt != nil {
			request, err := deps.ReviewService.ScheduleDirect(r.Context(), principal, assetID, *input.ScheduledAt)
			if err != nil {
				writePublicationSubmitError(w, err)
				return
			}
			writeJSON(w, http.StatusAccepted, request)
			return
		}
		allowedModels, err := deps.ScopeResolver.AllowedModelIDs(r.Context(), principal, "asset.publish")
		if err != nil {
			writeError(w, http.StatusInternalServerError, "authorization_scope_failed")
			return
		}
		result, err := deps.AssetService.Publish(r.Context(), principal, allowedModels, assetID, input.VersionID)
		if errors.Is(err, assetservice.ErrNotFound) {
			writeError(w, http.StatusNotFound, "asset_not_found")
			return
		}
		if errors.Is(err, assetservice.ErrConflict) {
			writeError(w, http.StatusConflict, "asset_publish_conflict")
			return
		}
		// The agent-facing publish path enforces the same publishing policy
		// gates as the member surface; keep the error contract identical.
		if errors.Is(err, assetservice.ErrApprovalRequired) {
			writeError(w, http.StatusForbidden, "action_not_allowed")
			return
		}
		if errors.Is(err, assetservice.ErrConfirmationRequired) {
			writeError(w, http.StatusConflict, "human_confirmation_required")
			return
		}
		if errors.Is(err, assetservice.ErrAttachmentNotClean) {
			writeError(w, http.StatusConflict, "attachments_not_clean")
			return
		}
		if errors.Is(err, assetservice.ErrRequiredFieldMissing) {
			writeError(w, http.StatusConflict, "required_field_missing")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "asset_publish_failed")
			return
		}
		writeJSON(w, http.StatusOK, result)
	}
}

func archiveAsset(deps Dependencies) http.HandlerFunc {
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
		if !requireAgentCapability(w, principal, "asset.archive") {
			return
		}
		assetID := r.PathValue("assetId")
		if !agentquery.ValidUUID(assetID) {
			writeError(w, http.StatusUnprocessableEntity, "invalid_asset_id")
			return
		}
		allowedModels, err := deps.ScopeResolver.AllowedModelIDs(r.Context(), principal, "asset.archive")
		if err != nil {
			writeError(w, http.StatusInternalServerError, "authorization_scope_failed")
			return
		}
		result, err := deps.AssetService.Archive(r.Context(), principal, allowedModels, assetID)
		if errors.Is(err, assetservice.ErrNotFound) {
			writeError(w, http.StatusNotFound, "asset_not_found")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "asset_archive_failed")
			return
		}
		writeJSON(w, http.StatusOK, result)
	}
}

type agentSubmitPublicationInput struct {
	AssetID       string `json:"asset_id"`
	DraftRevision *int64 `json:"draft_revision"`
	Comment       string `json:"comment"`
	// ScheduledAt optionally defers publication (G4); approval still applies.
	ScheduledAt *time.Time `json:"scheduled_at"`
}

// submitPublicationRequest lets an agent submit an approval-policy asset for
// review (doc §3.5/§11.1): the AgentAccessPolicy must grant publication.submit
// and asset.write; the capability scope must carry publication.submit. Agents
// still never approve or reject.
func submitPublicationRequest(deps Dependencies) http.HandlerFunc {
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
		if !requireAgentCapability(w, principal, "publication.submit") {
			return
		}
		workspaceID := r.PathValue("workspaceId")
		if !agentquery.ValidUUID(workspaceID) {
			writeError(w, http.StatusUnprocessableEntity, "invalid_workspace_id")
			return
		}
		var input agentSubmitPublicationInput
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16*1024))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil || !agentquery.ValidUUID(input.AssetID) || input.DraftRevision == nil || *input.DraftRevision <= 0 {
			writeError(w, http.StatusUnprocessableEntity, "invalid_publication_submission")
			return
		}
		request, err := deps.ReviewService.Submit(r.Context(), principal, workspaceID, input.AssetID, *input.DraftRevision, r.Header.Get("Idempotency-Key"), input.Comment, input.ScheduledAt)
		if err != nil {
			writePublicationSubmitError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, request)
	}
}

func writePublicationSubmitError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, review.ErrForbidden):
		writeError(w, http.StatusForbidden, "action_not_allowed")
	case errors.Is(err, review.ErrNotFound):
		writeError(w, http.StatusNotFound, "asset_not_found")
	case errors.Is(err, review.ErrInvalidInput):
		writeError(w, http.StatusUnprocessableEntity, "invalid_publication_submission")
	case errors.Is(err, review.ErrConflict):
		writeError(w, http.StatusConflict, "publication_request_not_pending")
	case errors.Is(err, assetservice.ErrConfirmationRequired):
		writeError(w, http.StatusConflict, "human_confirmation_required")
	case errors.Is(err, assetservice.ErrAttachmentNotClean):
		writeError(w, http.StatusConflict, "attachments_not_clean")
	case errors.Is(err, assetservice.ErrRequiredFieldMissing):
		writeError(w, http.StatusConflict, "required_field_missing")
	default:
		writeError(w, http.StatusInternalServerError, "publication_submit_failed")
	}
}

func assetReferences(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, err := deps.Authenticator.Authenticate(r.Context(), r)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		if !requireAgentCapability(w, principal, "reference.read") {
			return
		}
		assetID := r.PathValue("assetId")
		if !agentquery.ValidUUID(assetID) {
			writeError(w, http.StatusNotFound, "asset_reference_not_found")
			return
		}
		allowedModels, err := deps.ScopeResolver.AllowedModelIDs(r.Context(), principal, "asset.read")
		if err != nil {
			writeError(w, http.StatusInternalServerError, "authorization_scope_failed")
			return
		}
		result, err := deps.QueryService.Reference(r.Context(), principal, assetID, allowedModels)
		if errors.Is(err, agentquery.ErrReferenceNotFound) {
			writeError(w, http.StatusNotFound, "asset_reference_not_found")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "asset_reference_failed")
			return
		}
		writeJSON(w, http.StatusOK, result)
	}
}

func createAgentTask(deps Dependencies) http.HandlerFunc {
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
		if !requireAgentCapability(w, principal, "agent.run") {
			return
		}
		idempotencyKey, ok := requestIdempotencyKey(w, r)
		if !ok {
			writeError(w, http.StatusUnprocessableEntity, "idempotency_key_invalid")
			return
		}
		var input createAgentTaskRequest
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32*1024))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil {
			writeError(w, http.StatusUnprocessableEntity, "invalid_agent_task_request")
			return
		}
		readableModels, err := deps.ScopeResolver.AllowedModelIDs(r.Context(), principal, "asset.read")
		if err != nil {
			writeError(w, http.StatusInternalServerError, "authorization_scope_failed")
			return
		}
		editableModels, err := deps.ScopeResolver.AllowedModelIDs(r.Context(), principal, "asset.edit")
		if err != nil {
			writeError(w, http.StatusInternalServerError, "authorization_scope_failed")
			return
		}
		result, err := deps.AgentTaskService.Create(r.Context(), principal, agenttask.CreateInput{
			AgentApplicationID: input.AgentApplicationID,
			Operation:          input.Operation,
			InputAssetIDs:      input.InputAssetIDs,
			IdempotencyKey:     idempotencyKey,
		}, readableModels, editableModels)
		if errors.Is(err, agenttask.ErrInvalidInput) || errors.Is(err, agenttask.ErrUnsupportedOperation) {
			writeError(w, http.StatusUnprocessableEntity, "invalid_agent_task_request")
			return
		}
		if errors.Is(err, agenttask.ErrNotFound) {
			writeError(w, http.StatusNotFound, "agent_task_target_not_found")
			return
		}
		if errors.Is(err, agenttask.ErrIdempotencyConflict) {
			writeError(w, http.StatusConflict, "agent_task_idempotency_conflict")
			return
		}
		if errors.Is(err, agenttask.ErrConflict) {
			writeError(w, http.StatusConflict, "agent_task_conflict")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "agent_task_create_failed")
			return
		}
		writeJSON(w, http.StatusAccepted, result)
	}
}

func getAgentTask(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, err := deps.Authenticator.Authenticate(r.Context(), r)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		if !requireAgentCapability(w, principal, "agent.run") {
			return
		}
		taskID := r.PathValue("taskId")
		if !agentquery.ValidUUID(taskID) {
			writeError(w, http.StatusNotFound, "agent_task_not_found")
			return
		}
		result, err := deps.AgentTaskService.Get(r.Context(), principal, taskID)
		if errors.Is(err, agenttask.ErrInvalidInput) {
			writeError(w, http.StatusUnprocessableEntity, "invalid_agent_task_id")
			return
		}
		if errors.Is(err, agenttask.ErrNotFound) {
			writeError(w, http.StatusNotFound, "agent_task_not_found")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "agent_task_read_failed")
			return
		}
		writeJSON(w, http.StatusOK, result)
	}
}

func downloadAttachment(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, err := deps.Authenticator.Authenticate(r.Context(), r)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		if !requireAgentCapability(w, principal, "reference.read") {
			return
		}
		serveAttachmentDownload(deps)(w, r, principal)
	}
}

func memberDownloadAttachment(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, err := deps.SessionService.Authenticate(r.Context(), r)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		serveAttachmentDownload(deps)(w, r, principal)
	}
}

func serveAttachmentDownload(deps Dependencies) func(http.ResponseWriter, *http.Request, auth.Principal) {
	return func(w http.ResponseWriter, r *http.Request, principal auth.Principal) {
		attachmentID := r.PathValue("attachmentId")
		if !agentquery.ValidUUID(attachmentID) {
			writeError(w, http.StatusNotFound, "attachment_not_found")
			return
		}
		outcome := "error"
		defer func() {
			if deps.Store != nil {
				auditCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				_ = deps.Store.RecordAttachmentDownload(auditCtx, principal.OrganizationID, principal.UserID, attachmentID, outcome)
			}
		}()
		download, err := deps.AttachmentService.OpenDownload(r.Context(), principal, attachmentID)
		if errors.Is(err, attachment.ErrNotFound) {
			outcome = "denied"
			writeError(w, http.StatusNotFound, "attachment_not_found")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "attachment_download_failed")
			return
		}
		defer download.Body.Close()
		mediaType := download.MediaType
		if mediaType == "" {
			mediaType = "application/octet-stream"
		}
		w.Header().Set("Content-Type", mediaType)
		filename := download.Filename
		if filename == "" {
			filename = attachmentID
		}
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": filename}))
		if download.ContentLength >= 0 {
			w.Header().Set("Content-Length", strconv.FormatInt(download.ContentLength, 10))
		}
		if download.ETag != "" {
			w.Header().Set("ETag", download.ETag)
		}
		if _, err := io.Copy(w, download.Body); err != nil {
			return
		}
		outcome = "allowed"
	}
}

func health(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	writeJSON(w, http.StatusOK, healthResponse{
		Status: "ok",
		Time:   time.Now().UTC().Format(time.RFC3339),
	})
}

// readyRoute adapts the readiness probe for the route registry; the full
// phase 3 readiness checks live in query.go.
func readyRoute(deps Dependencies) http.HandlerFunc {
	return ready(deps)
}

func hashQuery(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func containsScope(values []string, target string) bool {
	return contains(values, target) || contains(values, "system:*")
}

func requireAgentCapability(w http.ResponseWriter, principal auth.Principal, capability string) bool {
	if principal.UserType != "agent" {
		writeError(w, http.StatusForbidden, "agent_user_required")
		return false
	}
	if !principal.HasCapability(capability) {
		writeError(w, http.StatusForbidden, "capability_required")
		return false
	}
	return true
}

func withJSONDefaults(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	writeJSONValue(w, status, value)
}
