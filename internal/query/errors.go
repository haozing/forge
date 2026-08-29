package query

import "errors"

// HTTP error contract (doc §11.5). Every domain failure carries one fixed
// status/code pair; provider details never reach the wire.
var (
	// ErrQueryScopeForbidden maps to 403/404 query_scope_forbidden per the
	// anti-leak rules of the calling surface.
	ErrQueryScopeForbidden = NewAPIError(403, "query_scope_forbidden", "query scope forbidden")
	// ErrResourceNotFound maps to 404 resource_not_found.
	ErrResourceNotFound = NewAPIError(404, "resource_not_found", "resource not found")
	// ErrInvalidQueryMode maps to 422 invalid_query_mode: only structured,
	// fulltext, semantic and hybrid are accepted (no lexical/vector aliases).
	ErrInvalidQueryMode = NewAPIError(422, "invalid_query_mode", "unsupported query mode")
	// ErrQueryTextRequired maps to 422 query_text_required.
	ErrQueryTextRequired = NewAPIError(422, "query_text_required", "query text is required for full-text modes")
	// ErrStructuredQueryTextNotAllowed maps to 422 structured_query_text_not_allowed.
	ErrStructuredQueryTextNotAllowed = NewAPIError(422, "structured_query_text_not_allowed", "structured queries must not carry query text")
	// ErrInvalidFieldFilter maps to 422 invalid_field_filter.
	ErrInvalidFieldFilter = NewAPIError(422, "invalid_field_filter", "field filter does not match the resource model schema")
	// ErrInvalidTagFilter maps to 422 invalid_tag_filter.
	ErrInvalidTagFilter = NewAPIError(422, "invalid_tag_filter", "tag filter keys conflict, exceed the limit or do not resolve")
	// ErrQueryModeNotEnabled maps to 422 query_mode_not_enabled.
	ErrQueryModeNotEnabled = NewAPIError(422, "query_mode_not_enabled", "the requested retrieval mode is disabled for the requested model")
	// ErrInvalidVisibility maps to 422 invalid_visibility: the request may only
	// narrow the scope visibility, never widen it.
	ErrInvalidVisibility = NewAPIError(422, "invalid_visibility", "visibility filter exceeds the allowed scope")
	// ErrInvalidRequest maps to 422 invalid_query_request for generic request
	// validation failures outside the enumerated codes.
	ErrInvalidRequest = NewAPIError(422, "invalid_query_request", "query request failed validation")
	// ErrProfileNotReady maps to 409 profile_not_ready (warming gate).
	ErrProfileNotReady = NewAPIError(409, "profile_not_ready", "retrieval profile has not finished warming")
	// ErrRebuildAlreadyRunning maps to 409 rebuild_already_running.
	ErrRebuildAlreadyRunning = NewAPIError(409, "rebuild_already_running", "a rebuild for this scope is already running")
	// ErrSearchSessionExpired maps to 410 search_session_expired.
	ErrSearchSessionExpired = NewAPIError(410, "search_session_expired", "search session expired")
	// ErrCursorInvalid maps to 422 invalid_cursor.
	ErrCursorInvalid = NewAPIError(422, "invalid_cursor", "search cursor failed verification")
	// ErrRetrievalProfileUnavailable maps to 503 retrieval_profile_unavailable.
	ErrRetrievalProfileUnavailable = NewAPIError(503, "retrieval_profile_unavailable", "no active retrieval profile for the organization")
	// ErrSemanticProviderUnavailable maps to 503 semantic_provider_unavailable.
	ErrSemanticProviderUnavailable = NewAPIError(503, "semantic_provider_unavailable", "semantic embedding provider is unavailable")
	// ErrRetrievalUnavailable maps to 503 retrieval_unavailable.
	ErrRetrievalUnavailable = NewAPIError(503, "retrieval_unavailable", "no recall path of the requested mode is available")
	// ErrQueryAuditUnavailable maps to 503 query_audit_unavailable.
	ErrQueryAuditUnavailable = NewAPIError(503, "query_audit_unavailable", "query audit could not be recorded")
	// ErrCitationRefNotFound maps to 404 when a citation reference fails
	// validation; validation never leaks which part failed.
	ErrCitationRefNotFound = NewAPIError(404, "citation_ref_not_found", "citation reference could not be validated")
	// ErrUnauthenticated maps to 401 authentication_required.
	ErrUnauthenticated = NewAPIError(401, "authentication_required", "authentication required")
)

// Legacy sentinel errors kept for the agent runtime integration surface.
var (
	// ErrInvalidQuery reports a structurally invalid query request.
	ErrInvalidQuery = errors.New("invalid query")
	// ErrModelAccessDenied reports an empty or unauthorized model scope.
	ErrModelAccessDenied = errors.New("model access denied")
	// ErrReferenceNotFound hides both missing assets and unauthorized ones.
	ErrReferenceNotFound = errors.New("asset reference not found")
)

// APIError is one fixed HTTP error of the query contract.
type APIError struct {
	Status  int
	Code    string
	Message string
}

// NewAPIError builds a fixed error. Instances are package-level singletons;
// callers wrap them with %w and map them through HTTPStatus.
func NewAPIError(status int, code, message string) *APIError {
	return &APIError{Status: status, Code: code, Message: message}
}

// Error implements error.
func (e *APIError) Error() string { return e.Code + ": " + e.Message }

// HTTPStatus resolves the wire status/code of an error chain. Unknown errors
// fall back to 503 retrieval_unavailable so provider internals never leak.
func HTTPStatus(err error) (int, string) {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.Status, apiErr.Code
	}
	if errors.Is(err, ErrModelAccessDenied) || errors.Is(err, ErrInvalidQuery) {
		return 422, "invalid_query_request"
	}
	return 503, "retrieval_unavailable"
}
