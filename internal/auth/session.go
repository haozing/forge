package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"time"

	"agentchunzhi/internal/store"

	"github.com/jackc/pgx/v5"
)

const sessionCookieName = "agent_session"

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
	// IdleExpires and AbsoluteExpires are the v2 dual session lifetimes;
	// ExpiresAt aliases AbsoluteExpires for the legacy cookie writer.
	IdleExpires     time.Time
	AbsoluteExpires time.Time
}

func (s SessionService) Login(ctx context.Context, loginName, password string) (Session, error) {
	if s.Store == nil || s.Store.Pool == nil {
		return Session{}, errors.New("database store is not initialized")
	}
	var principal Principal
	var passwordHash string
	err := s.Store.Pool.QueryRow(ctx, `
		SELECT u.id, u.organization_id, u.user_type, u.password_hash
		FROM identity.users u
		WHERE u.login_name = $1
		  AND u.user_type = 'member'
		  AND u.status = 'active'
	`, loginName).Scan(&principal.UserID, &principal.OrganizationID, &principal.UserType, &passwordHash)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		// Unknown login name: no user details are available for the record.
		s.reportSessionEvent(SessionAuditEvent{Action: SessionLogin, Result: "denied", LoginName: loginName, Reason: ReasonUnknownLoginName})
		return Session{}, errors.New("invalid credentials")
	case err != nil:
		return Session{}, errors.New("invalid credentials")
	}
	if !VerifyPassword(password, passwordHash) {
		s.reportSessionEvent(SessionAuditEvent{Action: SessionLogin, Result: "denied", OrganizationID: principal.OrganizationID, UserID: principal.UserID, LoginName: loginName, Reason: ReasonInvalidCredentials})
		return Session{}, errors.New("invalid credentials")
	}
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		return Session{}, fmt.Errorf("generate session token: %w", err)
	}
	token := hex.EncodeToString(tokenBytes)
	expiresAt := time.Now().UTC().Add(24 * time.Hour)
	_, err = s.Store.Pool.Exec(ctx, `
		INSERT INTO identity.sessions (user_id, session_hash, expires_at)
		VALUES ($1::uuid, $2, $3)
	`, principal.UserID, hashSessionToken(token), expiresAt)
	if err != nil {
		s.reportSessionEvent(SessionAuditEvent{Action: SessionLogin, Result: "error", OrganizationID: principal.OrganizationID, UserID: principal.UserID, LoginName: loginName, Reason: ReasonSessionCreateFailed})
		return Session{}, fmt.Errorf("create session: %w", err)
	}
	s.reportSessionEvent(SessionAuditEvent{Action: SessionLogin, Result: "allowed", OrganizationID: principal.OrganizationID, UserID: principal.UserID, LoginName: loginName})
	return Session{Principal: principal, Token: token, ExpiresAt: expiresAt}, nil
}

func (s SessionService) Authenticate(ctx context.Context, r *http.Request) (Principal, error) {
	if s.Store == nil || s.Store.Pool == nil {
		return Principal{}, ErrUnauthenticated
	}
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil || cookie.Value == "" {
		return Principal{}, ErrUnauthenticated
	}
	var principal Principal
	err = s.Store.Pool.QueryRow(ctx, `
		SELECT u.id, u.organization_id, u.user_type
		FROM identity.sessions s
		JOIN identity.users u ON u.id = s.user_id
		WHERE s.session_hash = $1
		  AND s.revoked_at IS NULL
		  AND s.expires_at > now()
		  AND u.status = 'active'
	`, hashSessionToken(cookie.Value)).Scan(&principal.UserID, &principal.OrganizationID, &principal.UserType)
	if err != nil {
		return Principal{}, ErrUnauthenticated
	}
	return principal, nil
}

// Logout revokes the current browser session. It is intentionally idempotent:
// an absent, expired, or already-revoked cookie is treated as logged out.
func (s SessionService) Logout(ctx context.Context, r *http.Request) error {
	if s.Store == nil || s.Store.Pool == nil {
		return nil
	}
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil || cookie.Value == "" {
		return nil
	}
	var userID, organizationID string
	err = s.Store.Pool.QueryRow(ctx, `
		UPDATE identity.sessions s
		SET revoked_at = COALESCE(s.revoked_at, now())
		FROM identity.users u
		WHERE u.id = s.user_id AND s.session_hash = $1
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
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		Expires:  time.Unix(1, 0).UTC(),
		HttpOnly: true,
		Secure:   r.TLS != nil,
		SameSite: http.SameSiteLaxMode,
	})
}

func SetSessionCookie(w http.ResponseWriter, r *http.Request, session Session) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    session.Token,
		Path:     "/",
		Expires:  session.ExpiresAt,
		MaxAge:   int(time.Until(session.ExpiresAt).Seconds()),
		HttpOnly: true,
		Secure:   r.TLS != nil,
		SameSite: http.SameSiteLaxMode,
	})
}

func hashSessionToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
