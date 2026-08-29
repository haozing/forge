// Package identity owns the member's own-account surface: profile display
// name, explicit preferences and the password change/reset flows. Sessions
// stay in internal/auth.
package identity

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"agentchunzhi/internal/auth"
	"agentchunzhi/internal/eventing"
	"agentchunzhi/internal/notification"
	"agentchunzhi/internal/store"

	"github.com/jackc/pgx/v5"
)

var (
	ErrNotFound       = errors.New("identity resource not found")
	ErrInvalidInput   = errors.New("invalid identity input")
	ErrForbidden      = errors.New("identity action forbidden")
	ErrWrongPassword  = errors.New("current password does not match")
	ErrTokenInvalid   = errors.New("password reset token invalid")
	ErrPreferencesRev = errors.New("preferences revision mismatch")
)

type Service struct {
	Store  *store.Store
	Events *eventing.EventStore
	// Password reset delivery dependencies.
	Cipher          *notification.Cipher
	KeyVersion      int32
	BaseURL         string
	OrganizationName string
}

type Profile struct {
	ID               string     `json:"id"`
	Email            string     `json:"email"`
	DisplayName      string     `json:"display_name"`
	OrganizationRole string     `json:"organization_role"`
	Status           string     `json:"status"`
	LastLoginAt      *time.Time `json:"last_login_at"`
	Revision         int64      `json:"revision"`
	ETag             string     `json:"etag"`
}

func (s Service) validID(value string) bool {
	if len(value) != 36 {
		return false
	}
	for index, char := range value {
		if index == 8 || index == 13 || index == 18 || index == 23 {
			if char != '-' {
				return false
			}
			continue
		}
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') && (char < 'A' || char > 'F') {
			return false
		}
	}
	return true
}

// Me returns the current member profile.
func (s Service) Me(ctx context.Context, userID string) (Profile, error) {
	if !s.validID(userID) {
		return Profile{}, ErrInvalidInput
	}
	var item Profile
	err := s.Store.Pool.QueryRow(ctx, `
		SELECT id::text, COALESCE(email, ''), display_name, COALESCE(organization_role, ''), status,
		       last_login_at, revision
		FROM identity.users WHERE id = $1::uuid AND user_type = 'member'
	`, userID).Scan(&item.ID, &item.Email, &item.DisplayName, &item.OrganizationRole,
		&item.Status, &item.LastLoginAt, &item.Revision)
	if errors.Is(err, pgx.ErrNoRows) {
		return Profile{}, ErrNotFound
	}
	item.ETag = fmt.Sprint(item.Revision)
	return item, err
}

// UpdateDisplayName patches the display name with optimistic concurrency.
func (s Service) UpdateDisplayName(ctx context.Context, userID, expectedRevision, displayName string) (Profile, error) {
	if !s.validID(userID) {
		return Profile{}, ErrInvalidInput
	}
	displayName = strings.TrimSpace(displayName)
	if displayName == "" || len([]rune(displayName)) > 80 {
		return Profile{}, ErrInvalidInput
	}
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return Profile{}, err
	}
	defer tx.Rollback(ctx)
	if err := expectRevision(ctx, tx, userID, expectedRevision); err != nil {
		return Profile{}, err
	}
	var item Profile
	err = tx.QueryRow(ctx, `
		UPDATE identity.users SET display_name = $2, revision = revision + 1, updated_at = now()
		WHERE id = $1::uuid AND user_type = 'member'
		RETURNING id::text, COALESCE(email, ''), display_name, COALESCE(organization_role, ''), status, last_login_at, revision
	`, userID, displayName).Scan(&item.ID, &item.Email, &item.DisplayName, &item.OrganizationRole,
		&item.Status, &item.LastLoginAt, &item.Revision)
	if errors.Is(err, pgx.ErrNoRows) {
		return Profile{}, ErrNotFound
	}
	item.ETag = fmt.Sprint(item.Revision)
	if err := tx.Commit(ctx); err != nil {
		return Profile{}, err
	}
	return item, nil
}

func expectRevision(ctx context.Context, tx pgx.Tx, userID, expected string) error {
	if expected == "" {
		return ErrInvalidInput
	}
	var revision int64
	if err := tx.QueryRow(ctx, `SELECT revision FROM identity.users WHERE id = $1::uuid`, userID).Scan(&revision); err != nil {
		return err
	}
	if fmt.Sprint(revision) != strings.Trim(expected, "\"") {
		return ErrPreferencesRev
	}
	return nil
}

// expectPreferencesRevision is the If-Match gate for preferences: it compares
// against identity.user_preferences.revision, which is the same revision the
// preferences GET returns. A missing row behaves like revision 1 so the first
// patch matches the synthesized default representation.
func expectPreferencesRevision(ctx context.Context, tx pgx.Tx, userID, expected string) error {
	if expected == "" {
		return ErrInvalidInput
	}
	var revision int64
	err := tx.QueryRow(ctx, `SELECT revision FROM identity.user_preferences WHERE user_id = $1::uuid`, userID).Scan(&revision)
	if errors.Is(err, pgx.ErrNoRows) {
		revision = 1
	} else if err != nil {
		return err
	}
	if fmt.Sprint(revision) != strings.Trim(expected, "\"") {
		return ErrPreferencesRev
	}
	return nil
}

// ---------- preferences ----------

type Preferences struct {
	DefaultWorkspaceID        string    `json:"default_workspace_id,omitempty"`
	Timezone                  string    `json:"timezone"`
	EmailNotificationsEnabled bool      `json:"email_notifications_enabled"`
	Revision                  int64     `json:"revision"`
	UpdatedAt                 time.Time `json:"updated_at"`
}

var timezonePattern = regexp.MustCompile(`^[A-Za-z]+(/[A-Za-z0-9_+\-]+)+$|^UTC$`)

func (s Service) Preferences(ctx context.Context, userID string) (Preferences, error) {
	if !s.validID(userID) {
		return Preferences{}, ErrInvalidInput
	}
	var item Preferences
	var defaultWorkspace *string
	err := s.Store.Pool.QueryRow(ctx, `
		SELECT COALESCE(default_workspace_id::text, ''), timezone, email_notifications_enabled, revision, updated_at
		FROM identity.user_preferences WHERE user_id = $1::uuid
	`, userID).Scan(&defaultWorkspace, &item.Timezone, &item.EmailNotificationsEnabled, &item.Revision, &item.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Preferences{Timezone: "UTC", EmailNotificationsEnabled: true, Revision: 1, UpdatedAt: time.Now().UTC()}, nil
	}
	if err != nil {
		return Preferences{}, err
	}
	if defaultWorkspace != nil {
		item.DefaultWorkspaceID = *defaultWorkspace
	}
	return item, nil
}

type PreferencesPatch struct {
	DefaultWorkspaceID        *string `json:"default_workspace_id"`
	Timezone                  *string `json:"timezone"`
	EmailNotificationsEnabled *bool   `json:"email_notifications_enabled"`
}

// PatchPreferences is the concrete implementation used by handlers.
func (s Service) PatchPreferences(ctx context.Context, userID, expectedRevision string, patch PreferencesPatch) (Preferences, error) {
	if !s.validID(userID) {
		return Preferences{}, ErrInvalidInput
	}
	if patch.Timezone != nil && !timezonePattern.MatchString(*patch.Timezone) {
		return Preferences{}, ErrInvalidInput
	}
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return Preferences{}, err
	}
	defer tx.Rollback(ctx)
	if err := expectPreferencesRevision(ctx, tx, userID, expectedRevision); err != nil {
		return Preferences{}, err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO identity.user_preferences (user_id) VALUES ($1::uuid)
		ON CONFLICT (user_id) DO NOTHING
	`, userID); err != nil {
		return Preferences{}, err
	}
	if patch.DefaultWorkspaceID != nil {
		if *patch.DefaultWorkspaceID == "" {
			if _, err := tx.Exec(ctx, `
				UPDATE identity.user_preferences SET default_workspace_id = NULL, revision = revision + 1, updated_at = now()
				WHERE user_id = $1::uuid
			`, userID); err != nil {
				return Preferences{}, err
			}
		} else {
			if !s.validID(*patch.DefaultWorkspaceID) {
				return Preferences{}, ErrInvalidInput
			}
			var ok bool
			if err := tx.QueryRow(ctx, `
				SELECT EXISTS (
					SELECT 1 FROM content.workspace_members wm
					JOIN content.workspaces w ON w.organization_id = wm.organization_id AND w.id = wm.workspace_id
					WHERE wm.user_id = $1::uuid AND wm.workspace_id = $2::uuid AND w.status = 'active'
				)
			`, userID, *patch.DefaultWorkspaceID).Scan(&ok); err != nil {
				return Preferences{}, err
			}
			if !ok {
				return Preferences{}, ErrForbidden
			}
			if _, err := tx.Exec(ctx, `
				UPDATE identity.user_preferences
				SET default_workspace_id = $2::uuid, revision = revision + 1, updated_at = now()
				WHERE user_id = $1::uuid
			`, userID, *patch.DefaultWorkspaceID); err != nil {
				return Preferences{}, err
			}
		}
	}
	if patch.Timezone != nil {
		if _, err := tx.Exec(ctx, `
			UPDATE identity.user_preferences SET timezone = $2, revision = revision + 1, updated_at = now()
			WHERE user_id = $1::uuid
		`, userID, *patch.Timezone); err != nil {
			return Preferences{}, err
		}
	}
	if patch.EmailNotificationsEnabled != nil {
		if _, err := tx.Exec(ctx, `
			UPDATE identity.user_preferences SET email_notifications_enabled = $2, revision = revision + 1, updated_at = now()
			WHERE user_id = $1::uuid
		`, userID, *patch.EmailNotificationsEnabled); err != nil {
			return Preferences{}, err
		}
	}
	var item Preferences
	var defaultWorkspace *string
	err = tx.QueryRow(ctx, `
		SELECT COALESCE(default_workspace_id::text, ''), timezone, email_notifications_enabled, revision, updated_at
		FROM identity.user_preferences WHERE user_id = $1::uuid
	`, userID).Scan(&defaultWorkspace, &item.Timezone, &item.EmailNotificationsEnabled, &item.Revision, &item.UpdatedAt)
	if err != nil {
		return Preferences{}, err
	}
	if defaultWorkspace != nil {
		item.DefaultWorkspaceID = *defaultWorkspace
	}
	if err := tx.Commit(ctx); err != nil {
		return Preferences{}, err
	}
	return item, nil
}

// ---------- password change / reset ----------

// ChangePassword verifies the current password, replaces the hash and revokes
// every other session of the caller.
func (s Service) ChangePassword(ctx context.Context, sessions *auth.SessionService, userID, currentSessionID, currentPassword, newPassword string) (int64, error) {
	var passwordHash string
	err := s.Store.Pool.QueryRow(ctx, `
		SELECT password_hash FROM identity.users WHERE id = $1::uuid AND user_type = 'member'
	`, userID).Scan(&passwordHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, err
	}
	if !auth.VerifyPassword(currentPassword, passwordHash) {
		return 0, ErrWrongPassword
	}
	if err := auth.ValidatePassword(newPassword); err != nil {
		return 0, ErrInvalidInput
	}
	hash, err := auth.HashPassword(newPassword)
	if err != nil {
		return 0, err
	}
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `
		UPDATE identity.users SET password_hash = $2, revision = revision + 1, updated_at = now()
		WHERE id = $1::uuid
	`, userID, hash); err != nil {
		return 0, err
	}
	revoked, err := sessions.RevokeAllOthers(ctx, userID, currentSessionID)
	if err != nil {
		return 0, err
	}
	store.AppendAuditTx(ctx, tx, store.NewAuditEntry("identity.password.changed", "", userID, "user", userID, map[string]any{"method": "change", "revoked_session_count": revoked}), "")
	if err := appendIdentityEvent(ctx, tx, s.Events, userID, eventing.EventIdentityPasswordChanged, map[string]any{
		"user_id": userID, "method": "change", "revoked_session_count": revoked,
	}); err != nil {
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return revoked, nil
}

func appendIdentityEvent(ctx context.Context, tx pgx.Tx, events *eventing.EventStore, organizationID string, eventType string, payload map[string]any) error {
	if events == nil {
		return errors.New("event store is not initialized")
	}
	raw, err := eventing.EncodePayload(payload)
	if err != nil {
		return err
	}
	_, err = events.AppendTx(ctx, tx, eventing.Event{
		OrganizationID:   organizationID,
		EventType:        eventType,
		AggregateType:    "user",
		AggregateID:      payloadUserID(payload),
		AggregateVersion: 1,
		PayloadVersion:   eventing.PayloadVersionV1,
		Actor:            map[string]any{"type": "password_reset"},
		Payload:          raw,
	})
	return err
}

func payloadUserID(payload map[string]any) string {
	if id, ok := payload["user_id"].(string); ok {
		return id
	}
	return "00000000-0000-4000-8000-000000000000"
}

func hashToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// RequestPasswordReset creates the one-time token and its encrypted delivery.
// The answer is always 202-shaped: the caller cannot learn whether the email
// exists.
func (s Service) RequestPasswordReset(ctx context.Context, email string) error {
	normalized := strings.ToLower(strings.TrimSpace(email))
	var userID, organizationID string
	err := s.Store.Pool.QueryRow(ctx, `
		SELECT id::text, organization_id::text FROM identity.users
		WHERE email = $1 AND user_type = 'member' AND status = 'active'
	`, normalized).Scan(&userID, &organizationID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil // indistinguishable response; no enumeration
	}
	if err != nil {
		return err
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return err
	}
	token := hex.EncodeToString(raw)
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	// Previous unconsumed tokens die immediately.
	if _, err := tx.Exec(ctx, `
		UPDATE identity.password_resets SET consumed_at = now()
		WHERE user_id = $1::uuid AND consumed_at IS NULL AND expires_at > now()
	`, userID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO identity.password_resets (user_id, token_hash, expires_at)
		VALUES ($1::uuid, $2, now() + interval '30 minutes')
	`, userID, hashToken(token)); err != nil {
		return err
	}
	link, err := notification.JoinBaseURL(s.BaseURL, "/reset-password?token="+token)
	if err != nil {
		return err
	}
	payload, _ := json.Marshal(map[string]any{
		"token": token, "link": link, "organization_name": s.OrganizationName, "email": normalized,
	})
	_, ciphertext, err := s.Cipher.Encrypt(userID, notification.TemplatePasswordReset, payload)
	if err != nil {
		return err
	}
	if _, err := notification.Enqueue(ctx, tx, organizationID, notification.TemplatePasswordReset, normalized, s.KeyVersion, ciphertext); err != nil {
		return err
	}
	store.AppendAuditTx(ctx, tx, store.NewAuditEntry("identity.password.reset_requested", organizationID, userID, "user", userID, nil), "")
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	return nil
}

// ResolvePasswordReset validates a token and returns the masked email.
func (s Service) ResolvePasswordReset(ctx context.Context, token string) (string, error) {
	if token == "" {
		return "", ErrTokenInvalid
	}
	var email string
	err := s.Store.Pool.QueryRow(ctx, `
		SELECT u.email FROM identity.password_resets r
		JOIN identity.users u ON u.id = r.user_id
		WHERE r.token_hash = $1 AND r.consumed_at IS NULL AND r.expires_at > now()
	`, hashToken(token)).Scan(&email)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrTokenInvalid
	}
	if err != nil {
		return "", err
	}
	at := strings.Index(email, "@")
	if at <= 0 {
		return "***", nil
	}
	return email[:1] + "***" + email[at:], nil
}

// CompletePasswordReset consumes the token, sets the new password and revokes
// every session of the user.
func (s Service) CompletePasswordReset(ctx context.Context, sessions *auth.SessionService, token, newPassword string) error {
	if token == "" {
		return ErrTokenInvalid
	}
	if err := auth.ValidatePassword(newPassword); err != nil {
		return ErrInvalidInput
	}
	hash, err := auth.HashPassword(newPassword)
	if err != nil {
		return err
	}
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var userID, organizationID string
	err = tx.QueryRow(ctx, `
		UPDATE identity.password_resets SET consumed_at = now()
		WHERE id = (
			SELECT id FROM identity.password_resets
			WHERE token_hash = $1 AND consumed_at IS NULL AND expires_at > now()
			FOR UPDATE
		)
		RETURNING user_id::text
	`, hashToken(token)).Scan(&userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrTokenInvalid
	}
	if err != nil {
		return err
	}
	if err := tx.QueryRow(ctx, `SELECT organization_id::text FROM identity.users WHERE id = $1::uuid`, userID).Scan(&organizationID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE identity.users SET password_hash = $2, revision = revision + 1, updated_at = now()
		WHERE id = $1::uuid
	`, userID, hash); err != nil {
		return err
	}
	revoked, err := sessions.RevokeAll(ctx, userID)
	if err != nil {
		return err
	}
	store.AppendAuditTx(ctx, tx, store.NewAuditEntry("identity.password.reset", organizationID, userID, "user", userID, map[string]any{"method": "reset", "revoked_session_count": revoked}), "")
	if err := appendIdentityEvent(ctx, tx, s.Events, organizationID, eventing.EventIdentityPasswordChanged, map[string]any{
		"user_id": userID, "method": "reset", "revoked_session_count": revoked,
	}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
