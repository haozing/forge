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
)

const sessionCookieName = "agent_session"

type SessionService struct {
	Store *store.Store
}

type Session struct {
	Principal
	Token     string
	ExpiresAt time.Time
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
	if err != nil || !VerifyPassword(password, passwordHash) {
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
		return Session{}, fmt.Errorf("create session: %w", err)
	}
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
	_, err = s.Store.Pool.Exec(ctx, `
		UPDATE identity.sessions
		SET revoked_at = COALESCE(revoked_at, now())
		WHERE session_hash = $1
	`, hashSessionToken(cookie.Value))
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
