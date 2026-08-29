package auth

// identity.go — phase 1 additive session core: email login, dual-expiry
// sessions with device metadata and the password policy. Legacy
// session.go login remains until the phase 1 handler swap deletes it.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

// Password policy: 12-128 Unicode code points, UTF-8 up to 512 bytes, never
// trimmed or normalized.
const (
	MinPasswordRunes = 12
	MaxPasswordRunes = 128
	MaxPasswordBytes = 512
)

func ValidatePassword(password string) error {
	runes := utf8.RuneCountInString(password)
	if runes < MinPasswordRunes || runes > MaxPasswordRunes {
		return errors.New("password must be 12-128 characters")
	}
	if len(password) > MaxPasswordBytes {
		return errors.New("password exceeds 512 bytes")
	}
	if !utf8.ValidString(password) {
		return errors.New("password must be valid UTF-8")
	}
	return nil
}

// Session lifecycle constants (v2 contract).
const (
	SessionIdleTTL     = 24 * time.Hour
	SessionAbsoluteTTL = 7 * 24 * time.Hour
	// LastSeenThrottle limits session touch writes.
	LastSeenThrottle = 5 * time.Minute
)

// LoginEmail authenticates a member by globally unique normalized email and
// verifies user and organization status in the same lookup.
func (s SessionService) LoginEmail(ctx context.Context, email, ipPrefix, userAgent, password string) (Session, error) {
	if s.Store == nil || s.Store.Pool == nil {
		return Session{}, errors.New("database store is not initialized")
	}
	var principal Principal
	var passwordHash string
	err := s.Store.Pool.QueryRow(ctx, `
		SELECT u.id, u.organization_id, u.user_type, u.password_hash
		FROM identity.users u
		JOIN organization.organizations o ON o.id = u.organization_id
		WHERE u.email = $1 AND u.user_type = 'member'
		  AND u.status = 'active' AND o.status = 'active'
	`, email).Scan(&principal.UserID, &principal.OrganizationID, &principal.UserType, &passwordHash)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		s.reportSessionEvent(SessionAuditEvent{Action: SessionLogin, Result: "denied", LoginName: email, Reason: ReasonInvalidCredentials})
		return Session{}, errors.New("invalid credentials")
	case err != nil:
		return Session{}, errors.New("invalid credentials")
	}
	if !VerifyPassword(password, passwordHash) {
		s.reportSessionEvent(SessionAuditEvent{Action: SessionLogin, Result: "denied", OrganizationID: principal.OrganizationID, UserID: principal.UserID, LoginName: email, Reason: ReasonInvalidCredentials})
		return Session{}, errors.New("invalid credentials")
	}
	session, err := s.CreateSession(ctx, principal.UserID, ipPrefix, userAgent)
	if err != nil {
		return Session{}, err
	}
	session.Principal = principal
	if _, err := s.Store.Pool.Exec(ctx, `
		UPDATE identity.users SET last_login_at = now(), updated_at = now() WHERE id = $1::uuid
	`, principal.UserID); err != nil {
		return Session{}, fmt.Errorf("record last login: %w", err)
	}
	s.reportSessionEvent(SessionAuditEvent{Action: SessionLogin, Result: "allowed", OrganizationID: principal.OrganizationID, UserID: principal.UserID, LoginName: email})
	return session, nil
}

// CreateSession inserts a dual-expiry session row for an existing member and
// returns the raw token (only the hash is stored).
func (s SessionService) CreateSession(ctx context.Context, userID, ipPrefix, userAgent string) (Session, error) {
	if s.Store == nil || s.Store.Pool == nil {
		return Session{}, errors.New("database store is not initialized")
	}
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		return Session{}, fmt.Errorf("generate session token: %w", err)
	}
	token := hex.EncodeToString(tokenBytes)
	now := time.Now().UTC()
	session := Session{
		Token:           token,
		AbsoluteExpires: now.Add(SessionAbsoluteTTL),
		IdleExpires:     now.Add(SessionIdleTTL),
	}
	session.Principal.UserID = userID
	if userAgent == "" {
		userAgent = "unknown"
	}
	if len(userAgent) > 300 {
		userAgent = userAgent[:300]
	}
	err := s.Store.Pool.QueryRow(ctx, `
		INSERT INTO identity.sessions
			(user_id, token_hash, idle_expires_at, absolute_expires_at, ip_prefix, user_agent)
		VALUES ($1::uuid, $2, $3, $4, NULLIF($5, ''), $6)
		RETURNING id::text
	`, userID, hashSessionToken(token), session.IdleExpires, session.AbsoluteExpires, ipPrefix, userAgent).Scan(&session.ID)
	if err != nil {
		return Session{}, fmt.Errorf("create session: %w", err)
	}
	return session, nil
}

// ListSessions returns the caller's active sessions for the management view.
type SessionInfo struct {
	ID        string     `json:"id"`
	CreatedAt time.Time  `json:"created_at"`
	LastSeen  time.Time  `json:"last_seen_at"`
	ExpiresAt time.Time  `json:"absolute_expires_at"`
	IPPrefix  string     `json:"ip_prefix,omitempty"`
	UserAgent string     `json:"user_agent,omitempty"`
	Current   bool       `json:"is_current"`
	RevokedAt *time.Time `json:"-"`
}

func (s SessionService) ListSessions(ctx context.Context, userID string) ([]SessionInfo, error) {
	if s.Store == nil || s.Store.Pool == nil {
		return nil, errors.New("database store is not initialized")
	}
	rows, err := s.Store.Pool.Query(ctx, `
		SELECT id::text, created_at, last_seen_at, absolute_expires_at,
		       COALESCE(ip_prefix, ''), COALESCE(user_agent, '')
		FROM identity.sessions
		WHERE user_id = $1::uuid AND revoked_at IS NULL AND absolute_expires_at > now()
		ORDER BY created_at DESC
	`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]SessionInfo, 0)
	for rows.Next() {
		var item SessionInfo
		if err := rows.Scan(&item.ID, &item.CreatedAt, &item.LastSeen, &item.ExpiresAt, &item.IPPrefix, &item.UserAgent); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// RevokeSession removes one of the user's own sessions.
func (s SessionService) RevokeSession(ctx context.Context, userID, sessionID string) (bool, error) {
	commandTag, err := s.Store.Pool.Exec(ctx, `
		UPDATE identity.sessions SET revoked_at = now()
		WHERE user_id = $1::uuid AND id = $2::uuid AND revoked_at IS NULL
	`, userID, sessionID)
	if err != nil {
		return false, err
	}
	return commandTag.RowsAffected() > 0, nil
}

// RevokeAllOthers implements the password-change rule: keep the current
// session, revoke every other.
func (s SessionService) RevokeAllOthers(ctx context.Context, userID, currentSessionID string) (int64, error) {
	commandTag, err := s.Store.Pool.Exec(ctx, `
		UPDATE identity.sessions SET revoked_at = now()
		WHERE user_id = $1::uuid AND revoked_at IS NULL AND id <> $2::uuid
	`, userID, currentSessionID)
	if err != nil {
		return 0, err
	}
	return commandTag.RowsAffected(), nil
}

// RevokeAll implements the password-reset rule.
func (s SessionService) RevokeAll(ctx context.Context, userID string) (int64, error) {
	commandTag, err := s.Store.Pool.Exec(ctx, `
		UPDATE identity.sessions SET revoked_at = now()
		WHERE user_id = $1::uuid AND revoked_at IS NULL
	`, userID)
	if err != nil {
		return 0, err
	}
	return commandTag.RowsAffected(), nil
}

// TouchSession refreshes last_seen_at at most once per throttle window.
func (s SessionService) TouchSession(ctx context.Context, sessionID string) {
	if s.Store == nil || s.Store.Pool == nil {
		return
	}
	_, _ = s.Store.Pool.Exec(ctx, `
		UPDATE identity.sessions
		SET last_seen_at = now(), idle_expires_at = now() + make_interval(secs => $3)
		WHERE id = $1::uuid AND last_seen_at < now() - make_interval(secs => $2)
	`, sessionID, LastSeenThrottle.Seconds(), SessionIdleTTL.Seconds())
}

// CurrentSessionID resolves the session id behind the request cookie without
// loading the principal again. Expired or revoked sessions answer
// ErrUnauthenticated.
func (s SessionService) CurrentSessionID(ctx context.Context, r *http.Request) (string, error) {
	if s.Store == nil || s.Store.Pool == nil {
		return "", ErrUnauthenticated
	}
	cookie, err := r.Cookie(SessionCookieConfig.Name)
	if err != nil || cookie.Value == "" {
		return "", ErrUnauthenticated
	}
	var id string
	err = s.Store.Pool.QueryRow(ctx, `
		SELECT id::text FROM identity.sessions
		WHERE token_hash = $1 AND revoked_at IS NULL
		  AND idle_expires_at > now() AND absolute_expires_at > now()
	`, hashSessionToken(cookie.Value)).Scan(&id)
	if err != nil {
		return "", ErrUnauthenticated
	}
	return id, nil
}
