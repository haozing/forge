package workspace

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"agentchunzhi/internal/auth"
	"agentchunzhi/internal/store"

	"github.com/jackc/pgx/v5"
)

var (
	ErrInvalidInput = errors.New("invalid workspace input")
	ErrForbidden    = errors.New("workspace access denied")
	ErrNotFound     = errors.New("workspace not found")
	ErrConflict     = errors.New("workspace conflict")
)

type Service struct {
	Store *store.Store
}

type Counts struct {
	PendingConversations int64 `json:"pending_conversations"`
	Documents            int64 `json:"documents"`
	PendingReviews       int64 `json:"pending_reviews"`
	RunningTaskRuns      int64 `json:"running_task_runs"`
}

type Stats struct {
	AssetsTotal            int64     `json:"assets_total"`
	AssetsPublished        int64     `json:"assets_published"`
	AssetsPendingReview    int64     `json:"assets_pending_review"`
	AssetsCreatedThisMonth int64     `json:"assets_created_this_month"`
	DocumentsTotal         int64     `json:"documents_total"`
	TaskRunSuccessRate     float64   `json:"task_run_success_rate"`
	GeneratedAt            time.Time `json:"generated_at"`
}

type ActivityItem struct {
	ID         string         `json:"event_id"`
	EventType  string         `json:"event_type"`
	Actor      Member         `json:"actor"`
	ObjectType string         `json:"object_type"`
	ObjectID   string         `json:"object_id,omitempty"`
	Summary    string         `json:"summary"`
	Metadata   map[string]any `json:"metadata,omitempty"`
	CreatedAt  time.Time      `json:"created_at"`
}

type ActivityPage struct {
	Items      []ActivityItem `json:"items"`
	HasMore    bool           `json:"has_more"`
	NextCursor string         `json:"next_cursor,omitempty"`
}

type Summary struct {
	ID                   string    `json:"id"`
	Name                 string    `json:"name"`
	Description          string    `json:"description"`
	Role                 string    `json:"role"`
	DefaultResourceModel string    `json:"default_resource_model_id,omitempty"`
	DefaultVisibility    string    `json:"default_visibility"`
	Counts               Counts    `json:"counts"`
	UpdatedAt            time.Time `json:"updated_at"`
}

type Member struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
	LoginName   string `json:"login_name,omitempty"`
	Role        string `json:"role"`
}

type AgentApplication struct {
	ID              string   `json:"id"`
	Name            string   `json:"name"`
	ModelEndpointID string   `json:"model_endpoint_id"`
	ProviderType    string   `json:"provider_type"`
	ModelName       string   `json:"model_name"`
	RuntimeMode     string   `json:"runtime_mode"`
	Status          string   `json:"status"`
	Capabilities    []string `json:"capabilities"`
	BoundAgentUser  string   `json:"bound_agent_user_id"`
}

type Settings struct {
	Name                   string `json:"name"`
	DefaultVisibility      string `json:"default_visibility"`
	DefaultResourceModelID string `json:"default_resource_model_id,omitempty"`
	Description            string `json:"description"`
}

func (s Service) validatePrincipal(principal auth.Principal) error {
	if principal.UserType != "member" || strings.TrimSpace(principal.UserID) == "" || strings.TrimSpace(principal.OrganizationID) == "" {
		return ErrForbidden
	}
	if s.Store == nil || s.Store.Pool == nil {
		return errors.New("database store is not initialized")
	}
	return nil
}

func validateID(value string) bool {
	value = strings.TrimSpace(value)
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

func (s Service) membership(ctx context.Context, principal auth.Principal, workspaceID string) (string, error) {
	if err := s.validatePrincipal(principal); err != nil {
		return "", err
	}
	if !validateID(workspaceID) {
		return "", ErrInvalidInput
	}
	var role string
	err := s.Store.Pool.QueryRow(ctx, `
		SELECT wm.role
		FROM content.workspace_members wm
		JOIN content.workspaces w ON w.organization_id = wm.organization_id AND w.id = wm.workspace_id
		WHERE wm.organization_id = $1::uuid
		  AND wm.workspace_id = $2::uuid
		  AND wm.user_id = $3::uuid
		  AND w.status = 'active'
	`, principal.OrganizationID, workspaceID, principal.UserID).Scan(&role)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrForbidden
	}
	if err != nil {
		return "", fmt.Errorf("load workspace membership: %w", err)
	}
	return role, nil
}

func (s Service) List(ctx context.Context, principal auth.Principal) ([]Summary, error) {
	if err := s.validatePrincipal(principal); err != nil {
		return nil, err
	}
	rows, err := s.Store.Pool.Query(ctx, `
		SELECT w.id::text, w.name, w.description, wm.role,
		       COALESCE(w.default_resource_model_id::text, ''), w.default_visibility,
		       w.updated_at,
		       (SELECT count(*) FROM content.conversations c
		          WHERE c.organization_id = w.organization_id AND c.workspace_id = w.id AND c.status = 'active'),
		       (SELECT count(*) FROM content.containers c
		          WHERE c.organization_id = w.organization_id AND c.workspace_id = w.id
		            AND c.kind = 'document' AND c.status = 'active'),
		       (SELECT count(*) FROM asset.asset_reviews ar
		          WHERE ar.organization_id = w.organization_id AND ar.workspace_id = w.id AND ar.status = 'pending'),
		       (SELECT count(*) FROM automation.runs r
		        WHERE r.organization_id = w.organization_id AND r.workspace_id = w.id AND r.status IN ('queued', 'running'))
		FROM content.workspaces w
		JOIN content.workspace_members wm ON wm.organization_id = w.organization_id AND wm.workspace_id = w.id
		WHERE w.organization_id = $1::uuid AND wm.user_id = $2::uuid AND w.status = 'active'
		ORDER BY w.updated_at DESC, w.id
	`, principal.OrganizationID, principal.UserID)
	if err != nil {
		return nil, fmt.Errorf("list workspaces: %w", err)
	}
	defer rows.Close()
	items := make([]Summary, 0)
	for rows.Next() {
		var item Summary
		if err := rows.Scan(&item.ID, &item.Name, &item.Description, &item.Role,
			&item.DefaultResourceModel, &item.DefaultVisibility, &item.UpdatedAt,
			&item.Counts.PendingConversations, &item.Counts.Documents,
			&item.Counts.PendingReviews, &item.Counts.RunningTaskRuns); err != nil {
			return nil, fmt.Errorf("scan workspace: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate workspaces: %w", err)
	}
	return items, nil
}

func (s Service) Get(ctx context.Context, principal auth.Principal, workspaceID string) (Summary, error) {
	if _, err := s.membership(ctx, principal, workspaceID); err != nil {
		return Summary{}, err
	}
	var item Summary
	err := s.Store.Pool.QueryRow(ctx, `
		SELECT w.id::text, w.name, w.description, wm.role,
		       COALESCE(w.default_resource_model_id::text, ''), w.default_visibility,
		       w.updated_at,
		       (SELECT count(*) FROM content.conversations c WHERE c.organization_id = w.organization_id AND c.workspace_id = w.id AND c.status = 'active'),
		       (SELECT count(*) FROM content.containers c WHERE c.organization_id = w.organization_id AND c.workspace_id = w.id AND c.kind = 'document' AND c.status = 'active'),
		       (SELECT count(*) FROM asset.asset_reviews ar WHERE ar.organization_id = w.organization_id AND ar.workspace_id = w.id AND ar.status = 'pending'),
		       (SELECT count(*) FROM automation.runs r WHERE r.organization_id = w.organization_id AND r.workspace_id = w.id AND r.status IN ('queued', 'running'))
		FROM content.workspaces w
		JOIN content.workspace_members wm ON wm.organization_id = w.organization_id AND wm.workspace_id = w.id AND wm.user_id = $3::uuid
		WHERE w.organization_id = $1::uuid AND w.id = $2::uuid AND w.status = 'active'
	`, principal.OrganizationID, workspaceID, principal.UserID).Scan(&item.ID, &item.Name, &item.Description, &item.Role,
		&item.DefaultResourceModel, &item.DefaultVisibility, &item.UpdatedAt,
		&item.Counts.PendingConversations, &item.Counts.Documents, &item.Counts.PendingReviews, &item.Counts.RunningTaskRuns)
	if errors.Is(err, pgx.ErrNoRows) {
		return Summary{}, ErrNotFound
	}
	if err != nil {
		return Summary{}, fmt.Errorf("get workspace: %w", err)
	}
	return item, nil
}

func (s Service) Member(ctx context.Context, principal auth.Principal, workspaceID string) (Member, error) {
	if _, err := s.membership(ctx, principal, workspaceID); err != nil {
		return Member{}, err
	}
	var item Member
	err := s.Store.Pool.QueryRow(ctx, `
		SELECT u.id::text, u.display_name, COALESCE(u.login_name, ''), wm.role
		FROM content.workspace_members wm
		JOIN identity.users u ON u.id = wm.user_id
		WHERE wm.organization_id = $1::uuid AND wm.workspace_id = $2::uuid AND wm.user_id = $3::uuid
	`, principal.OrganizationID, workspaceID, principal.UserID).Scan(&item.ID, &item.DisplayName, &item.LoginName, &item.Role)
	if errors.Is(err, pgx.ErrNoRows) {
		return Member{}, ErrNotFound
	}
	if err != nil {
		return Member{}, fmt.Errorf("get workspace member: %w", err)
	}
	return item, nil
}

func (s Service) AgentApplications(ctx context.Context, principal auth.Principal, workspaceID string) ([]AgentApplication, error) {
	if _, err := s.membership(ctx, principal, workspaceID); err != nil {
		return nil, err
	}
	rows, err := s.Store.Pool.Query(ctx, `
		SELECT aa.id::text, aa.name, aa.model_endpoint_id::text, mer.provider_type,
		       mer.model_name, aa.runtime_mode, aa.status, aa.capabilities, aa.bound_agent_user_id::text
		FROM content.workspace_agent_applications wa
		JOIN integration.agent_applications aa ON aa.organization_id = wa.organization_id AND aa.id = wa.agent_application_id
		JOIN integration.model_endpoints me ON me.id = aa.model_endpoint_id AND me.status = 'active'
		JOIN integration.model_endpoint_revisions mer ON mer.model_endpoint_id = me.id AND mer.revision = me.current_revision AND mer.revoked_at IS NULL
		WHERE wa.organization_id = $1::uuid AND wa.workspace_id = $2::uuid AND wa.enabled = true AND aa.status = 'active'
		ORDER BY aa.name, aa.id
	`, principal.OrganizationID, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("list workspace agent applications: %w", err)
	}
	defer rows.Close()
	items := make([]AgentApplication, 0)
	for rows.Next() {
		var item AgentApplication
		var capabilities []byte
		if err := rows.Scan(&item.ID, &item.Name, &item.ModelEndpointID, &item.ProviderType, &item.ModelName, &item.RuntimeMode, &item.Status, &capabilities, &item.BoundAgentUser); err != nil {
			return nil, fmt.Errorf("scan workspace agent application: %w", err)
		}
		if len(capabilities) > 0 && json.Unmarshal(capabilities, &item.Capabilities) != nil {
			item.Capabilities = []string{}
		}
		if item.Capabilities == nil {
			item.Capabilities = []string{}
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate workspace agent applications: %w", err)
	}
	return items, nil
}

func (s Service) Settings(ctx context.Context, principal auth.Principal, workspaceID string) (Settings, error) {
	if _, err := s.membership(ctx, principal, workspaceID); err != nil {
		return Settings{}, err
	}
	var result Settings
	err := s.Store.Pool.QueryRow(ctx, `
		SELECT w.name, w.default_visibility, COALESCE(w.default_resource_model_id::text, ''), w.description
		FROM content.workspaces w
		JOIN content.workspace_members wm ON wm.organization_id = w.organization_id AND wm.workspace_id = w.id AND wm.user_id = $3::uuid
		WHERE w.organization_id = $1::uuid AND w.id = $2::uuid
	`, principal.OrganizationID, workspaceID, principal.UserID).Scan(&result.Name, &result.DefaultVisibility, &result.DefaultResourceModelID, &result.Description)
	if errors.Is(err, pgx.ErrNoRows) {
		return Settings{}, ErrNotFound
	}
	if err != nil {
		return Settings{}, fmt.Errorf("get workspace settings: %w", err)
	}
	return result, nil
}

func (s Service) UpdateSettings(ctx context.Context, principal auth.Principal, workspaceID string, input Settings) (Settings, error) {
	role, err := s.membership(ctx, principal, workspaceID)
	if err != nil {
		return Settings{}, err
	}
	if role != "owner" && role != "admin" {
		return Settings{}, ErrForbidden
	}
	input.Name = strings.TrimSpace(input.Name)
	input.DefaultVisibility = strings.TrimSpace(input.DefaultVisibility)
	if input.Name == "" || (input.DefaultVisibility != "public" && input.DefaultVisibility != "login" && input.DefaultVisibility != "private" && input.DefaultVisibility != "workspace" && input.DefaultVisibility != "internal") {
		return Settings{}, ErrInvalidInput
	}
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return Settings{}, fmt.Errorf("begin workspace settings update: %w", err)
	}
	defer tx.Rollback(ctx)
	var revision int64
	err = tx.QueryRow(ctx, `
		UPDATE content.workspaces
		SET name = $3, description = $4, default_visibility = $5, default_resource_model_id = NULLIF($6, '')::uuid, updated_at = now()
		WHERE organization_id = $1::uuid AND id = $2::uuid
		RETURNING name, default_visibility, COALESCE(default_resource_model_id::text, ''), description
	`, principal.OrganizationID, workspaceID, input.Name, input.Description, input.DefaultVisibility, input.DefaultResourceModelID).Scan(&input.Name, &input.DefaultVisibility, &input.DefaultResourceModelID, &input.Description)
	if errors.Is(err, pgx.ErrNoRows) {
		return Settings{}, ErrNotFound
	}
	if err != nil {
		return Settings{}, fmt.Errorf("update workspace settings: %w", err)
	}
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(max(revision), 0) + 1 FROM content.workspace_settings_revisions WHERE organization_id = $1::uuid AND workspace_id = $2::uuid
	`, principal.OrganizationID, workspaceID).Scan(&revision); err != nil {
		return Settings{}, fmt.Errorf("allocate workspace settings revision: %w", err)
	}
	settingsJSON, _ := json.Marshal(input)
	if _, err := tx.Exec(ctx, `
		INSERT INTO content.workspace_settings_revisions (organization_id, workspace_id, revision, settings, changed_by)
		VALUES ($1::uuid, $2::uuid, $3, $4::jsonb, $5::uuid)
	`, principal.OrganizationID, workspaceID, revision, string(settingsJSON), principal.UserID); err != nil {
		return Settings{}, fmt.Errorf("record workspace settings revision: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Settings{}, fmt.Errorf("commit workspace settings update: %w", err)
	}
	return input, nil
}

func (s Service) Counts(ctx context.Context, principal auth.Principal, workspaceID string) (Counts, error) {
	item, err := s.Get(ctx, principal, workspaceID)
	if err != nil {
		return Counts{}, err
	}
	return item.Counts, nil
}

func (s Service) Stats(ctx context.Context, principal auth.Principal, workspaceID string) (Stats, error) {
	if _, err := s.membership(ctx, principal, workspaceID); err != nil {
		return Stats{}, err
	}
	var result Stats
	err := s.Store.Pool.QueryRow(ctx, `
		SELECT
		  (SELECT count(*) FROM asset.assets WHERE organization_id = $1::uuid AND workspace_id = $2::uuid),
		  (SELECT count(*) FROM asset.assets WHERE organization_id = $1::uuid AND workspace_id = $2::uuid AND publication_status = 'published'),
		  (SELECT count(*) FROM asset.asset_reviews WHERE organization_id = $1::uuid AND workspace_id = $2::uuid AND status = 'pending'),
		  (SELECT count(*) FROM asset.assets WHERE organization_id = $1::uuid AND workspace_id = $2::uuid AND created_at >= date_trunc('month', now())),
		  (SELECT count(*) FROM content.containers WHERE organization_id = $1::uuid AND workspace_id = $2::uuid AND kind = 'document' AND status = 'active'),
		  COALESCE((SELECT count(*) FILTER (WHERE status = 'succeeded')::float8 / NULLIF(count(*) FILTER (WHERE status IN ('succeeded', 'failed')), 0) FROM automation.runs WHERE organization_id = $1::uuid AND workspace_id = $2::uuid), 0)
	`, principal.OrganizationID, workspaceID).Scan(&result.AssetsTotal, &result.AssetsPublished, &result.AssetsPendingReview, &result.AssetsCreatedThisMonth, &result.DocumentsTotal, &result.TaskRunSuccessRate)
	if err != nil {
		return Stats{}, fmt.Errorf("get workspace stats: %w", err)
	}
	result.GeneratedAt = time.Now().UTC()
	return result, nil
}

func (s Service) Activity(ctx context.Context, principal auth.Principal, workspaceID, cursor string, limit int) (ActivityPage, error) {
	if _, err := s.membership(ctx, principal, workspaceID); err != nil {
		return ActivityPage{}, err
	}
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	createdBefore, idBefore, err := decodeActivityCursor(cursor)
	if err != nil {
		return ActivityPage{}, ErrInvalidInput
	}
	rows, err := s.Store.Pool.Query(ctx, `
		SELECT al.id::text, al.action, COALESCE(u.id::text, ''), COALESCE(u.display_name, ''),
		       COALESCE(al.resource_type, ''), COALESCE(al.resource_id::text, ''), al.metadata, al.created_at
		FROM audit.audit_log al
		LEFT JOIN identity.users u ON u.id = al.actor_user_id
		LEFT JOIN asset.assets a ON a.organization_id = al.organization_id AND a.id = al.resource_id
		WHERE al.organization_id = $1::uuid
		  AND ((al.resource_type = 'asset' AND a.workspace_id = $2::uuid) OR al.metadata->>'workspace_id' = $2::text)
		  AND ($3 = '' OR (al.created_at, al.id) < (NULLIF($4, '')::timestamptz, NULLIF($5, '')::uuid))
		ORDER BY al.created_at DESC, al.id DESC
		LIMIT $6
	`, principal.OrganizationID, workspaceID, cursor, createdBefore, idBefore, limit+1)
	if err != nil {
		return ActivityPage{}, fmt.Errorf("list workspace activity: %w", err)
	}
	defer rows.Close()
	page := ActivityPage{Items: make([]ActivityItem, 0, limit+1)}
	for rows.Next() {
		var item ActivityItem
		var metadata []byte
		if err := rows.Scan(&item.ID, &item.EventType, &item.Actor.ID, &item.Actor.DisplayName, &item.ObjectType, &item.ObjectID, &metadata, &item.CreatedAt); err != nil {
			return ActivityPage{}, fmt.Errorf("scan workspace activity: %w", err)
		}
		item.Metadata = map[string]any{}
		_ = json.Unmarshal(metadata, &item.Metadata)
		item.Summary = item.EventType
		page.Items = append(page.Items, item)
	}
	if err := rows.Err(); err != nil {
		return ActivityPage{}, fmt.Errorf("iterate workspace activity: %w", err)
	}
	if len(page.Items) > limit {
		page.HasMore = true
		page.Items = page.Items[:limit]
		last := page.Items[len(page.Items)-1]
		page.NextCursor = encodeActivityCursor(last.CreatedAt, last.ID)
	}
	return page, nil
}

func encodeActivityCursor(createdAt time.Time, id string) string {
	raw, _ := json.Marshal(map[string]string{"created_at": createdAt.UTC().Format(time.RFC3339Nano), "id": id})
	return base64.RawURLEncoding.EncodeToString(raw)
}

func decodeActivityCursor(cursor string) (string, string, error) {
	if strings.TrimSpace(cursor) == "" {
		return "", "", nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return "", "", err
	}
	var value struct {
		CreatedAt string `json:"created_at"`
		ID        string `json:"id"`
	}
	if err := json.Unmarshal(raw, &value); err != nil || !validateID(value.ID) {
		return "", "", errors.New("invalid activity cursor")
	}
	if _, err := time.Parse(time.RFC3339Nano, value.CreatedAt); err != nil {
		return "", "", err
	}
	return value.CreatedAt, value.ID, nil
}

func (s Service) Preferences(ctx context.Context, principal auth.Principal) (map[string]any, error) {
	if err := s.validatePrincipal(principal); err != nil {
		return nil, err
	}
	var raw []byte
	err := s.Store.Pool.QueryRow(ctx, `SELECT preferences FROM content.member_preferences WHERE organization_id = $1::uuid AND user_id = $2::uuid`, principal.OrganizationID, principal.UserID).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return map[string]any{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get member preferences: %w", err)
	}
	result := map[string]any{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &result); err != nil {
			return nil, fmt.Errorf("decode member preferences: %w", err)
		}
	}
	return result, nil
}

func (s Service) UpdatePreferences(ctx context.Context, principal auth.Principal, preferences map[string]any) (map[string]any, error) {
	if err := s.validatePrincipal(principal); err != nil {
		return nil, err
	}
	if preferences == nil {
		preferences = map[string]any{}
	}
	raw, err := json.Marshal(preferences)
	if err != nil {
		return nil, ErrInvalidInput
	}
	_, err = s.Store.Pool.Exec(ctx, `
		INSERT INTO content.member_preferences (user_id, organization_id, preferences, updated_at)
		VALUES ($1::uuid, $2::uuid, $3::jsonb, now())
		ON CONFLICT (user_id) DO UPDATE SET preferences = EXCLUDED.preferences, updated_at = now()
	`, principal.UserID, principal.OrganizationID, string(raw))
	if err != nil {
		return nil, fmt.Errorf("update member preferences: %w", err)
	}
	return preferences, nil
}
