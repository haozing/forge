package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"time"

	"agentchunzhi/internal/store"

	"github.com/jackc/pgx/v5"
)

// SessionCookieConfig is fixed once at process startup from configuration and
// must never be derived from the request. Production uses the __Host- prefix
// (Secure, Path=/, no Domain); development keeps the plain name.
var SessionCookieConfig = struct {
	Name   string
	Secure bool
}{Name: "agent_session", Secure: false}

type SessionService struct {
	Store *store.Store
	// AuditHook, when set, receives session lifecycle audit events instead of
	// the default fire-and-forget database writer. It exists so tests can
	// capture events without a database.
	AuditHook func(SessionAuditEvent)
}

type Session struct {
	Principal
	Token     string
	ID        string
	ExpiresAt time.Time
	// IdleExpires and AbsoluteExpires are the dual session lifetimes;
	// ExpiresAt aliases AbsoluteExpires for the legacy cookie writer.
	IdleExpires     time.Time
	AbsoluteExpires time.Time
}

// Authenticate resolves the member session cookie. Phase 1 contract: the
// session is only valid while its idle and absolute lifetimes hold, the user
// stays active and the owning organization stays active — so disabling a
// member or suspending an organization revokes access immediately.
func (s SessionService) Authenticate(ctx context.Context, r *http.Request) (Principal, error) {
	principal, _, err := s.authenticateSession(ctx, r)
	return principal, err
}

// authenticateSession resolves the principal plus the session id behind the
// cookie and refreshes last_seen_at (throttled). handlers use the session
// id for revocation commands and self-identification in session listings.
func (s SessionService) authenticateSession(ctx context.Context, r *http.Request) (Principal, string, error) {
	if s.Store == nil || s.Store.Pool == nil {
		return Principal{}, "", ErrUnauthenticated
	}
	cookie, err := r.Cookie(SessionCookieConfig.Name)
	if err != nil || cookie.Value == "" {
		return Principal{}, "", ErrUnauthenticated
	}
	var principal Principal
	var sessionID string
	err = s.Store.Pool.QueryRow(ctx, `
		SELECT u.id, u.organization_id, u.user_type, s.id::text
		FROM identity.sessions s
		JOIN identity.users u ON u.id = s.user_id
		JOIN organization.organizations o ON o.id = u.organization_id
		WHERE s.token_hash = $1
		  AND s.revoked_at IS NULL
		  AND s.idle_expires_at > now()
		  AND s.absolute_expires_at > now()
		  AND u.status = 'active'
		  AND o.status = 'active'
	`, hashSessionToken(cookie.Value)).Scan(&principal.UserID, &principal.OrganizationID, &principal.UserType, &sessionID)
	if err != nil {
		return Principal{}, "", ErrUnauthenticated
	}
	s.TouchSession(ctx, sessionID)
	return principal, sessionID, nil
}

// Logout revokes the current browser session. It is intentionally idempotent:
// an absent, expired, or already-revoked cookie is treated as logged out.
func (s SessionService) Logout(ctx context.Context, r *http.Request) error {
	if s.Store == nil || s.Store.Pool == nil {
		return nil
	}
	cookie, err := r.Cookie(SessionCookieConfig.Name)
	if err != nil || cookie.Value == "" {
		return nil
	}
	var userID, organizationID string
	err = s.Store.Pool.QueryRow(ctx, `
		UPDATE identity.sessions s
		SET revoked_at = COALESCE(s.revoked_at, now())
		FROM identity.users u
		WHERE u.id = s.user_id AND s.token_hash = $1
		RETURNING u.id::text, u.organization_id::text
	`, hashSessionToken(cookie.Value)).Scan(&userID, &organizationID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err == nil {
		s.reportSessionEvent(SessionAuditEvent{Action: SessionLogout, Result: "allowed", OrganizationID: organizationID, UserID: userID})
	}
	return err
}

func ClearSessionCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookieConfig.Name,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		Expires:  time.Unix(1, 0).UTC(),
		HttpOnly: true,
		Secure:   r.TLS != nil || SessionCookieConfig.Secure,
		SameSite: http.SameSiteLaxMode,
	})
}

func SetSessionCookie(w http.ResponseWriter, r *http.Request, session Session) {
	// Legacy sessions carry ExpiresAt; phase 1 sessions carry AbsoluteExpires.
	expires := session.ExpiresAt
	if expires.IsZero() {
		expires = session.AbsoluteExpires
	}
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookieConfig.Name,
		Value:    session.Token,
		Path:     "/",
		Expires:  expires,
		MaxAge:   int(time.Until(expires).Seconds()),
		HttpOnly: true,
		Secure:   r.TLS != nil || SessionCookieConfig.Secure,
		SameSite: http.SameSiteLaxMode,
	})
}

func hashSessionToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
