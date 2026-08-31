package httpapi

// identity.go — phase 1 member identity surface: email login, session
// management, /me profile and preferences, password change/reset and the
// anonymous invitation resolve/accept endpoints. Handlers authenticate, parse,
// call domain services and map errors; there is no direct SQL here.

import (
	"errors"
	"math"
	"net"
	"net/http"
	"strconv"
	"strings"

	"agentchunzhi/internal/auth"
	"agentchunzhi/internal/authz"
	"agentchunzhi/internal/identity"
	"agentchunzhi/internal/organization"
	"agentchunzhi/internal/workspace"
)

// ---------- shared middleware and helpers ----------

// withOriginPolicy enforces the CSRF Origin allowlist for every unsafe
// request on the cookie-authenticated surfaces (/api and /api/public):
// cookie-authenticated writes must never succeed from a foreign origin.
// The API-key surfaces (/api/open, /api/admin) are out of scope. Missing or
// non-matching Origin headers are rejected; GET/HEAD/OPTIONS stay open.
func withOriginPolicy(allowed []string, next http.Handler) http.Handler {
	allowedSet := make(map[string]struct{}, len(allowed))
	for _, origin := range allowed {
		origin = strings.TrimSpace(origin)
		if origin == "" || origin == "*" {
			continue // "*" is a production configuration error, never a grant
		}
		allowedSet[origin] = struct{}{}
	}
	if len(allowedSet) == 0 {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if unsafeOriginCheckedMethod(r.Method) && OriginCheckedPath(r.URL.Path) {
			origin := strings.TrimSpace(r.Header.Get("Origin"))
			if _, ok := allowedSet[origin]; !ok {
				w.Header().Add("Vary", "Origin")
				writeError(w, http.StatusForbidden, "origin_not_allowed")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func unsafeOriginCheckedMethod(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return false
	default:
		return true
	}
}

func OriginCheckedPath(path string) bool {
	if strings.HasPrefix(path, "/api/open/") || strings.HasPrefix(path, "/api/admin/") {
		return false
	}
	return strings.HasPrefix(path, "/api/")
}

// effectiveClientAddr derives the address used for rate limiting keys.
// RemoteAddr is trusted unless the direct peer is an operator-vouched trusted
// proxy, in which case the first X-Forwarded-For hop wins.
func effectiveClientAddr(r *http.Request, trustedCIDRs []string) string {
	remote := strings.TrimSpace(r.RemoteAddr)
	if host, _, err := net.SplitHostPort(remote); err == nil {
		remote = host
	}
	ip := net.ParseIP(remote)
	if ip == nil || len(trustedCIDRs) == 0 {
		return r.RemoteAddr
	}
	for _, cidr := range trustedCIDRs {
		_, network, err := net.ParseCIDR(strings.TrimSpace(cidr))
		if err != nil || !network.Contains(ip) {
			continue
		}
		forwarded := firstForwardedFor(r)
		if forwarded != "" {
			return forwarded
		}
		break
	}
	return r.RemoteAddr
}

func firstForwardedFor(r *http.Request) string {
	header := strings.TrimSpace(r.Header.Get("X-Forwarded-For"))
	if header == "" {
		return ""
	}
	first, _, _ := strings.Cut(header, ",")
	first = strings.TrimSpace(first)
	if len(first) > 128 {
		return ""
	}
	return first
}

// requireWorkspaceSession authenticates a member session and resolves the session id
// behind the cookie (needed by revocation commands and self-identification in
// the session list). Member routes accept session cookies only.
func requireWorkspaceSession(w http.ResponseWriter, r *http.Request, deps Dependencies) (auth.Principal, string, bool) {
	principal, err := deps.SessionService.Authenticate(r.Context(), r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "authentication_required")
		return auth.Principal{}, "", false
	}
	if principal.UserType != auth.UserTypeMember {
		writeError(w, http.StatusForbidden, "member_session_required")
		return auth.Principal{}, "", false
	}
	sessionID, err := deps.SessionService.CurrentSessionID(r.Context(), r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "authentication_required")
		return auth.Principal{}, "", false
	}
	return principal, sessionID, true
}

// requireIfMatch enforces the If-Match precondition on PATCH/PUT: missing
// answers 428 precondition_required. The value is returned verbatim (quotes
// included); comparison sites use ifMatchWildcard to honor the "*" wildcard.
func requireIfMatch(w http.ResponseWriter, r *http.Request) (string, bool) {
	value := strings.TrimSpace(r.Header.Get("If-Match"))
	if value == "" {
		writeError(w, http.StatusPreconditionRequired, "precondition_required")
		return "", false
	}
	return value, true
}

// ifMatchWildcard reports whether a (possibly ETag-quoted) If-Match value is
// the "*" wildcard: the precondition then only demands that the resource
// exists, so revision equality checks are skipped.
func ifMatchWildcard(value string) bool {
	return strings.Trim(value, "\"") == "*"
}

func writeNoContent(w http.ResponseWriter) {
	w.WriteHeader(http.StatusNoContent)
}

func writeRateLimited(w http.ResponseWriter, retryAfter float64) {
	seconds := int64(math.Ceil(retryAfter))
	if seconds < 1 {
		seconds = 1
	}
	w.Header().Set("Retry-After", strconv.FormatInt(seconds, 10))
	writeError(w, http.StatusTooManyRequests, "rate_limited")
}

// DomainError maps identity/organization/workspace domain errors onto the
// HTTP status/code contract.
func DomainError(w http.ResponseWriter, err error) {
	switch {
	case err == nil:
		return
	case errors.Is(err, identity.ErrNotFound), errors.Is(err, organization.ErrNotFound), errors.Is(err, workspace.ErrNotFound):
		writeError(w, http.StatusNotFound, "resource_not_found")
	case errors.Is(err, identity.ErrInvalidInput), errors.Is(err, organization.ErrInvalidInput),
		errors.Is(err, workspace.ErrInvalidInput), errors.Is(err, workspace.ErrInvalidEmail):
		writeError(w, http.StatusUnprocessableEntity, "validation_failed")
	case errors.Is(err, identity.ErrPreferencesRev):
		writeError(w, http.StatusPreconditionFailed, "revision_mismatch")
	case errors.Is(err, identity.ErrWrongPassword):
		writeError(w, http.StatusForbidden, "wrong_password")
	case errors.Is(err, identity.ErrTokenInvalid), errors.Is(err, organization.ErrInvitationInvalid),
		errors.Is(err, organization.ErrInvitationExpired), errors.Is(err, organization.ErrInvitationConsumed):
		writeError(w, http.StatusUnprocessableEntity, "invalid_token")
	case errors.Is(err, organization.ErrEmailUnavailable):
		writeError(w, http.StatusConflict, "email_unavailable")
	case errors.Is(err, organization.ErrInvitationExists):
		writeError(w, http.StatusConflict, "invitation_exists")
	case errors.Is(err, organization.ErrMembershipExists):
		writeError(w, http.StatusConflict, "membership_exists")
	case errors.Is(err, organization.ErrLastOrgAdmin), errors.Is(err, organization.ErrLastWorkspaceAdmin),
		errors.Is(err, workspace.ErrLastAdminRequired):
		writeError(w, http.StatusConflict, "last_admin_required")
	case errors.Is(err, workspace.ErrAmbiguousMember):
		writeError(w, http.StatusConflict, "ambiguous_member")
	case errors.Is(err, organization.ErrConflict), errors.Is(err, workspace.ErrConflict):
		writeError(w, http.StatusConflict, "state_conflict")
	case errors.Is(err, organization.ErrWorkspaceArchived):
		writeError(w, http.StatusConflict, "workspace_archived")
	case errors.Is(err, identity.ErrForbidden), errors.Is(err, organization.ErrForbidden), errors.Is(err, workspace.ErrForbidden):
		writeError(w, http.StatusForbidden, "action_not_allowed")
	default:
		writeError(w, http.StatusInternalServerError, "internal_error")
	}
}

// AllowedActions lists the organization governance actions implied by the
// caller's organization role. Workspace actions require membership and are
// evaluated per workspace; they are not part of the profile.
func AllowedActions(organizationRole string) []string {
	actions := []string{authz.ActionOrganizationRead}
	if organizationRole == authz.OrganizationRoleAdmin {
		actions = append(actions,
			authz.ActionOrganizationManage,
			authz.ActionOrganizationMemberRead,
			authz.ActionOrganizationMemberManage,
			authz.ActionOrganizationInvitationMng,
			authz.ActionWorkspaceCreate,
			authz.ActionWorkspaceArchive,
			authz.ActionWorkspaceRestore,
		)
	}
	return actions
}

// MeResponse is the profile summary used by login, accept, /me and the
// invitation acceptance flows.
type MeResponse struct {
	identity.Profile
	Organization   OrganizationRef `json:"organization"`
	AllowedActions []string          `json:"allowed_actions"`
}

type OrganizationRef struct {
	ID   string `json:"id"`
	Slug string `json:"slug"`
	Name string `json:"name"`
}

func OrganizationRefFrom(item organization.Organization) OrganizationRef {
	return OrganizationRef{ID: item.ID, Slug: item.Slug, Name: item.Name}
}

// MeSummary assembles the authenticated member summary.
func MeSummary(w http.ResponseWriter, r *http.Request, deps Dependencies, principal auth.Principal, status int) {
	if deps.IdentityService == nil || deps.IdentityService.Store == nil || deps.IdentityService.Store.Pool == nil {
		writeData(w, r, status, map[string]any{
			"id": principal.UserID, "organization_id": principal.OrganizationID, "user_type": principal.UserType,
		})
		return
	}
	profile, err := deps.IdentityService.Me(r.Context(), principal.UserID)
	if err != nil {
		DomainError(w, err)
		return
	}
	summary := MeResponse{Profile: profile, AllowedActions: AllowedActions(profile.OrganizationRole)}
	if deps.OrganizationService.Store != nil && deps.OrganizationService.Store.Pool != nil {
		if org, orgErr := deps.OrganizationService.Get(r.Context(), principal); orgErr == nil {
			summary.Organization = OrganizationRefFrom(org)
		}
	}
	writeETag(w, profile.ETag)
	writeData(w, r, status, summary)
}

// ---------- sessions ----------

type LoginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

// CreateSession is the anonymous email login. Every attempt consumes both
// the email and the IP throttle buckets before credentials are checked; the
// email bucket is cleared on success. Failures answer a uniform 401.
func CreateSession(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		var input LoginRequest
		if !decodeBody(w, r, &input, 16*1024) {
			return
		}
		email, err := organization.NormalizeEmail(input.Email)
		if err != nil {
			writeError(w, http.StatusUnprocessableEntity, "validation_failed")
			return
		}
		ipPrefix := auth.ClientIPPrefix(effectiveClientAddr(r, deps.TrustedProxyCIDRs))
		if deps.LoginThrottle != nil && deps.LoginThrottle.Store != nil && deps.LoginThrottle.Store.Pool != nil {
			allowed, retryAfter, throttleErr := deps.LoginThrottle.CheckAndIncrement(r.Context(), auth.BucketLoginEmail, email, auth.LoginEmailLimit)
			if throttleErr != nil {
				writeError(w, http.StatusInternalServerError, "internal_error")
				return
			}
			if !allowed {
				writeRateLimited(w, retryAfter.Seconds())
				return
			}
			allowed, retryAfter, throttleErr = deps.LoginThrottle.CheckAndIncrement(r.Context(), auth.BucketLoginIP, ipPrefix, auth.LoginIPLimit)
			if throttleErr != nil {
				writeError(w, http.StatusInternalServerError, "internal_error")
				return
			}
			if !allowed {
				writeRateLimited(w, retryAfter.Seconds())
				return
			}
		}
		session, loginErr := deps.SessionService.LoginEmail(r.Context(), email, ipPrefix, r.UserAgent(), input.Password)
		if loginErr != nil {
			writeError(w, http.StatusUnauthorized, "invalid_credentials")
			return
		}
		if deps.LoginThrottle != nil && deps.LoginThrottle.Store != nil && deps.LoginThrottle.Store.Pool != nil {
			_ = deps.LoginThrottle.Clear(r.Context(), auth.BucketLoginEmail, email)
		}
		auth.SetSessionCookie(w, r, session)
		MeSummary(w, r, deps, session.Principal, http.StatusOK)
	}
}

// DeleteCurrentSession logs the caller out; the cookie is always cleared.
func DeleteCurrentSession(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		if _, _, ok := requireWorkspaceSession(w, r, deps); !ok {
			return
		}
		if _, ok := requireIdempotencyKey(w, r); !ok {
			return
		}
		if err := deps.SessionService.Logout(r.Context(), r); err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error")
			return
		}
		auth.ClearSessionCookie(w, r)
		writeNoContent(w)
	}
}

// ListSessions returns the caller's active sessions with the current one
// flagged.
func ListSessions(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, sessionID, ok := requireWorkspaceSession(w, r, deps)
		if !ok {
			return
		}
		items, err := deps.SessionService.ListSessions(r.Context(), principal.UserID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error")
			return
		}
		for index := range items {
			items[index].Current = items[index].ID == sessionID
		}
		writeData(w, r, http.StatusOK, map[string]any{"items": items, "page": CursorPage{NextCursor: nil, HasMore: false}})
	}
}

// DeleteSession revokes one of the caller's own sessions. Sessions of other
// users answer 404 without revealing existence.
func DeleteSession(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, _, ok := requireWorkspaceSession(w, r, deps)
		if !ok {
			return
		}
		if _, ok := requireIdempotencyKey(w, r); !ok {
			return
		}
		sessionID := r.PathValue("sessionId")
		if !requirePathUUID(w, sessionID) {
			return
		}
		revoked, err := deps.SessionService.RevokeSession(r.Context(), principal.UserID, sessionID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error")
			return
		}
		if !revoked {
			writeError(w, http.StatusNotFound, "resource_not_found")
			return
		}
		writeNoContent(w)
	}
}

// ---------- me ----------

// GetMe returns the profile, the organization reference and the allowed
// organization governance actions.
func GetMe(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, _, ok := requireWorkspaceSession(w, r, deps)
		if !ok {
			return
		}
		MeSummary(w, r, deps, principal, http.StatusOK)
	}
}

type PatchMeRequest struct {
	DisplayName string `json:"display_name"`
}

func PatchMe(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, _, ok := requireWorkspaceSession(w, r, deps)
		if !ok {
			return
		}
		expected, ok := requireIfMatch(w, r)
		if !ok {
			return
		}
		if ifMatchWildcard(expected) {
			// If-Match "*" only demands the profile exists; resolve the current
			// revision so the optimistic check inside the service passes.
			current, err := deps.IdentityService.Me(r.Context(), principal.UserID)
			if err != nil {
				DomainError(w, err)
				return
			}
			expected = current.ETag
		}
		var input PatchMeRequest
		if !decodeBody(w, r, &input, 16*1024) {
			return
		}
		profile, err := deps.IdentityService.UpdateDisplayName(r.Context(), principal.UserID, expected, input.DisplayName)
		if err != nil {
			DomainError(w, err)
			return
		}
		writeETag(w, profile.ETag)
		writeData(w, r, http.StatusOK, profile)
	}
}

type ChangePasswordRequest struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
}

// ChangePassword replaces the password and revokes every session except the
// current one.
func ChangePassword(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, sessionID, ok := requireWorkspaceSession(w, r, deps)
		if !ok {
			return
		}
		if _, ok := requireIdempotencyKey(w, r); !ok {
			return
		}
		var input ChangePasswordRequest
		if !decodeBody(w, r, &input, 16*1024) {
			return
		}
		if err := auth.ValidatePassword(input.NewPassword); err != nil {
			writeError(w, http.StatusUnprocessableEntity, "validation_failed")
			return
		}
		revoked, err := deps.IdentityService.ChangePassword(r.Context(), &deps.SessionService, principal.UserID, sessionID, input.CurrentPassword, input.NewPassword)
		if err != nil {
			DomainError(w, err)
			return
		}
		writeData(w, r, http.StatusOK, map[string]any{"revoked_sessions": revoked})
	}
}

func GetPreferences(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, _, ok := requireWorkspaceSession(w, r, deps)
		if !ok {
			return
		}
		preferences, err := deps.IdentityService.Preferences(r.Context(), principal.UserID)
		if err != nil {
			DomainError(w, err)
			return
		}
		writeETag(w, strconv.FormatInt(preferences.Revision, 10))
		writeData(w, r, http.StatusOK, preferences)
	}
}

type PatchPreferencesRequest struct {
	DefaultWorkspaceID        *string `json:"default_workspace_id"`
	Timezone                  *string `json:"timezone"`
	EmailNotificationsEnabled *bool   `json:"email_notifications_enabled"`
}

func PatchPreferences(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, _, ok := requireWorkspaceSession(w, r, deps)
		if !ok {
			return
		}
		expected, ok := requireIfMatch(w, r)
		if !ok {
			return
		}
		if ifMatchWildcard(expected) {
			// If-Match "*" only demands the preferences row exists; resolve the
			// current revision so the optimistic check inside the service passes.
			current, err := deps.IdentityService.Preferences(r.Context(), principal.UserID)
			if err != nil {
				DomainError(w, err)
				return
			}
			expected = strconv.FormatInt(current.Revision, 10)
		}
		var input PatchPreferencesRequest
		if !decodeBody(w, r, &input, 16*1024) {
			return
		}
		patch := identity.PreferencesPatch{
			DefaultWorkspaceID:        input.DefaultWorkspaceID,
			Timezone:                  input.Timezone,
			EmailNotificationsEnabled: input.EmailNotificationsEnabled,
		}
		preferences, err := deps.IdentityService.PatchPreferences(r.Context(), principal.UserID, expected, patch)
		if err != nil {
			DomainError(w, err)
			return
		}
		writeETag(w, strconv.FormatInt(preferences.Revision, 10))
		writeData(w, r, http.StatusOK, preferences)
	}
}

// ---------- password reset ----------

type PasswordResetRequest struct {
	Email string `json:"email"`
}

// RequestPasswordReset always answers 202 so the endpoint cannot be used to
// enumerate accounts; throttling protects the mail pipeline instead.
func RequestPasswordReset(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		var input PasswordResetRequest
		if !decodeBody(w, r, &input, 16*1024) {
			return
		}
		email, err := organization.NormalizeEmail(input.Email)
		if err != nil {
			// Malformed addresses get the same indistinguishable answer.
			writeData(w, r, http.StatusAccepted, map[string]any{"status": "accepted"})
			return
		}
		ipPrefix := auth.ClientIPPrefix(effectiveClientAddr(r, deps.TrustedProxyCIDRs))
		if deps.LoginThrottle != nil && deps.LoginThrottle.Store != nil && deps.LoginThrottle.Store.Pool != nil {
			allowed, retryAfter, throttleErr := deps.LoginThrottle.CheckAndIncrement(r.Context(), auth.BucketResetEmail, email, auth.ResetEmailLimit)
			if throttleErr == nil && !allowed {
				writeRateLimited(w, retryAfter.Seconds())
				return
			}
			allowed, retryAfter, throttleErr = deps.LoginThrottle.CheckAndIncrement(r.Context(), auth.BucketResetIP, ipPrefix, auth.ResetIPLimit)
			if throttleErr == nil && !allowed {
				writeRateLimited(w, retryAfter.Seconds())
				return
			}
		}
		if deps.IdentityService != nil && deps.IdentityService.Store != nil && deps.IdentityService.Store.Pool != nil {
			if err := deps.IdentityService.RequestPasswordReset(r.Context(), email); err != nil {
				writeError(w, http.StatusInternalServerError, "internal_error")
				return
			}
		}
		writeData(w, r, http.StatusAccepted, map[string]any{"status": "accepted"})
	}
}

type PasswordResetResolveRequest struct {
	Token string `json:"token"`
}

func ResolvePasswordReset(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		var input PasswordResetResolveRequest
		if !decodeBody(w, r, &input, 16*1024) {
			return
		}
		maskedEmail, err := deps.IdentityService.ResolvePasswordReset(r.Context(), strings.TrimSpace(input.Token))
		if err != nil {
			DomainError(w, err)
			return
		}
		writeData(w, r, http.StatusOK, map[string]any{"email": maskedEmail})
	}
}

type PasswordResetCompleteRequest struct {
	Token       string `json:"token"`
	NewPassword string `json:"new_password"`
}

func CompletePasswordReset(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		// Public one-time-token surface: the reset token is single-use, which
		// is the replay boundary (idempotency middleware excludes /api/public).
		var input PasswordResetCompleteRequest
		if !decodeBody(w, r, &input, 16*1024) {
			return
		}
		if err := deps.IdentityService.CompletePasswordReset(r.Context(), &deps.SessionService, strings.TrimSpace(input.Token), input.NewPassword); err != nil {
			DomainError(w, err)
			return
		}
		writeData(w, r, http.StatusOK, map[string]any{"status": "password_reset_completed"})
	}
}

// ---------- invitation resolve / accept (anonymous) ----------

type ResolveInvitationRequest struct {
	Token string `json:"token"`
}

func ResolveInvitation(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		var input ResolveInvitationRequest
		if !decodeBody(w, r, &input, 16*1024) {
			return
		}
		ipPrefix := auth.ClientIPPrefix(effectiveClientAddr(r, deps.TrustedProxyCIDRs))
		if deps.LoginThrottle != nil && deps.LoginThrottle.Store != nil && deps.LoginThrottle.Store.Pool != nil {
			allowed, retryAfter, throttleErr := deps.LoginThrottle.CheckAndIncrement(r.Context(), auth.BucketInvitationIP, ipPrefix, auth.InvitationIPLimit)
			if throttleErr != nil {
				writeError(w, http.StatusInternalServerError, "internal_error")
				return
			}
			if !allowed {
				writeRateLimited(w, retryAfter.Seconds())
				return
			}
		}
		resolved, err := deps.InvitationService.Resolve(r.Context(), strings.TrimSpace(input.Token))
		if err != nil {
			DomainError(w, err)
			return
		}
		writeData(w, r, http.StatusOK, resolved)
	}
}

type AcceptInvitationRequest struct {
	Token       string `json:"token"`
	DisplayName string `json:"display_name"`
	Password    string `json:"password"`
}

// AcceptInvitation activates the invited member and starts the initial
// session in one request.
func AcceptInvitation(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		// Public one-time-token surface: the invitation token itself is the
		// replay boundary (idempotency middleware excludes /api/public).
		var input AcceptInvitationRequest
		if !decodeBody(w, r, &input, 16*1024) {
			return
		}
		ipPrefix := auth.ClientIPPrefix(effectiveClientAddr(r, deps.TrustedProxyCIDRs))
		if deps.LoginThrottle != nil && deps.LoginThrottle.Store != nil && deps.LoginThrottle.Store.Pool != nil {
			allowed, retryAfter, throttleErr := deps.LoginThrottle.CheckAndIncrement(r.Context(), auth.BucketInvitationIP, ipPrefix, auth.InvitationIPLimit)
			if throttleErr != nil {
				writeError(w, http.StatusInternalServerError, "internal_error")
				return
			}
			if !allowed {
				writeRateLimited(w, retryAfter.Seconds())
				return
			}
		}
		result, err := deps.InvitationService.Accept(r.Context(), organization.AcceptInput{
			Token: strings.TrimSpace(input.Token), DisplayName: input.DisplayName, Password: input.Password,
		})
		if err != nil {
			DomainError(w, err)
			return
		}
		session, err := deps.InvitationService.CreateSessionFor(r.Context(), &deps.SessionService, ipPrefix, r.UserAgent(), result.UserID)
		if err == nil {
			auth.SetSessionCookie(w, r, session)
		}
		writeData(w, r, http.StatusOK, map[string]any{
			"user_id": result.UserID, "email": result.Email,
			"organization_id": result.OrganizationID, "workspace_ids": result.WorkspaceIDs,
		})
	}
}
