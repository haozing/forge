package organization

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"agentchunzhi/internal/auth"
	"agentchunzhi/internal/authz"
	"agentchunzhi/internal/eventing"
	"agentchunzhi/internal/store"

	"github.com/jackc/pgx/v5"
)

// Service implements organization governance: profile, member lifecycle and
// the organization-scoped workspace lifecycle. Every write commits business
// data, audit and outbox facts in one transaction and takes locks in the
// fixed Organization -> Workspace(s) order.
type Service struct {
	Store  *store.Store
	Events *eventing.EventStore
}

type Organization struct {
	ID        string    `json:"id"`
	Slug      string    `json:"slug"`
	Name      string    `json:"name"`
	Status    string    `json:"status"`
	Revision  int64     `json:"revision"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	ETag      string    `json:"etag"`
}

type Member struct {
	UserID           string     `json:"id"`
	Email            string     `json:"email"`
	DisplayName      string     `json:"display_name"`
	Status           string     `json:"status"`
	OrganizationRole string     `json:"organization_role"`
	LastLoginAt      *time.Time `json:"last_login_at"`
	CreatedAt        time.Time  `json:"created_at"`
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

// organizationRole loads the caller's organization role; agents are never
// members and therefore never govern.
func (s Service) organizationRole(ctx context.Context, principal auth.Principal) (string, error) {
	if principal.UserType != auth.UserTypeMember || s.Store == nil || s.Store.Pool == nil {
		return "", ErrForbidden
	}
	var role *string
	err := s.Store.Pool.QueryRow(ctx, `
		SELECT organization_role FROM identity.users
		WHERE organization_id = $1::uuid AND id = $2::uuid AND user_type = 'member' AND status = 'active'
	`, principal.OrganizationID, principal.UserID).Scan(&role)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrForbidden
	}
	if err != nil {
		return "", fmt.Errorf("load organization role: %w", err)
	}
	if role == nil {
		return "", ErrForbidden
	}
	return *role, nil
}

// RequireOrganizationAction is the single gate for organization governance
// actions. Only organization admins pass; membership is not consulted here.
func (s Service) RequireOrganizationAction(ctx context.Context, principal auth.Principal, action string) error {
	role, err := s.organizationRole(ctx, principal)
	if err != nil {
		return err
	}
	switch action {
	case authz.ActionOrganizationRead:
		return nil // every active member may read the organization profile
	case authz.ActionOrganizationManage, authz.ActionOrganizationMemberRead,
		authz.ActionOrganizationMemberManage, authz.ActionOrganizationInvitationMng,
		authz.ActionWorkspaceCreate, authz.ActionWorkspaceArchive, authz.ActionWorkspaceRestore,
		authz.ActionAuditRead:
		if role != authz.OrganizationRoleAdmin {
			return ErrForbidden
		}
		return nil
	default:
		return ErrForbidden
	}
}

// Get returns the caller's organization.
func (s Service) Get(ctx context.Context, principal auth.Principal) (Organization, error) {
	if err := s.RequireOrganizationAction(ctx, principal, authz.ActionOrganizationRead); err != nil {
		return Organization{}, err
	}
	var item Organization
	err := s.Store.Pool.QueryRow(ctx, `
		SELECT id::text, slug, name, status, revision, created_at, updated_at
		FROM organization.organizations WHERE id = $1::uuid
	`, principal.OrganizationID).Scan(&item.ID, &item.Slug, &item.Name, &item.Status,
		&item.Revision, &item.CreatedAt, &item.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Organization{}, ErrNotFound
	}
	if err != nil {
		return Organization{}, err
	}
	item.ETag = fmt.Sprint(item.Revision)
	return item, nil
}

// Update changes the organization display name.
func (s Service) Update(ctx context.Context, principal auth.Principal, name string) (Organization, error) {
	if err := s.RequireOrganizationAction(ctx, principal, authz.ActionOrganizationManage); err != nil {
		return Organization{}, err
	}
	name = strings.TrimSpace(name)
	if name == "" || len([]rune(name)) > 100 {
		return Organization{}, ErrInvalidInput
	}
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return Organization{}, err
	}
	defer tx.Rollback(ctx)
	var item Organization
	err = tx.QueryRow(ctx, `
		UPDATE organization.organizations
		SET name = $2, revision = revision + 1, updated_at = now()
		WHERE id = $1::uuid
		RETURNING id::text, slug, name, status, revision, created_at, updated_at
	`, principal.OrganizationID, name).Scan(&item.ID, &item.Slug, &item.Name, &item.Status,
		&item.Revision, &item.CreatedAt, &item.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Organization{}, ErrNotFound
	}
	if err != nil {
		return Organization{}, err
	}
	item.ETag = fmt.Sprint(item.Revision)
	store.AppendAuditTx(ctx, tx, store.NewAuditEntry("organization.updated", principal.OrganizationID, principal.UserID, "organization", item.ID, map[string]any{"name": name}), "")
	if err := appendOrgEvent(ctx, tx, s.Events, principal, "", eventing.EventOrganizationUpdated, item.ID, item.Revision, map[string]any{"changed_fields": []string{"name"}}); err != nil {
		return Organization{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Organization{}, err
	}
	return item, nil
}

func appendOrgEvent(ctx context.Context, tx pgx.Tx, events *eventing.EventStore, principal auth.Principal, workspaceID, eventType, aggregateID string, aggregateVersion int64, payload map[string]any) error {
	if events == nil {
		return errors.New("event store is not initialized")
	}
	raw, err := eventing.EncodePayload(payload)
	if err != nil {
		return err
	}
	_, err = events.AppendTx(ctx, tx, eventing.Event{
		OrganizationID:   principal.OrganizationID,
		WorkspaceID:      workspaceID,
		EventType:        eventType,
		AggregateType:    "organization",
		AggregateID:      aggregateID,
		AggregateVersion: aggregateVersion,
		PayloadVersion:   eventing.PayloadVersionV1,
		Actor:            eventing.ActorFromPrincipal(principal),
		Payload:          raw,
	})
	return err
}

// ListMembers returns the full member directory; organization admins only.
func (s Service) ListMembers(ctx context.Context, principal auth.Principal, limit int, cursor string) ([]Member, string, error) {
	if err := s.RequireOrganizationAction(ctx, principal, authz.ActionOrganizationMemberRead); err != nil {
		return nil, "", err
	}
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	// Keyset cursor "<RFC3339Nano created_at>|<user_id>"; the whole tuple is
	// ordered so equal created_at cannot loop a page.
	cursorCreated := ""
	cursorID := ""
	if cursor != "" {
		parts := strings.SplitN(cursor, "|", 2)
		if len(parts) != 2 || !s.validID(parts[1]) {
			return nil, "", ErrInvalidInput
		}
		if _, err := time.Parse(time.RFC3339Nano, parts[0]); err != nil {
			return nil, "", ErrInvalidInput
		}
		cursorCreated, cursorID = parts[0], parts[1]
	}
	rows, err := s.Store.Pool.Query(ctx, `
		SELECT id::text, COALESCE(email, ''), display_name, status, COALESCE(organization_role, ''),
		       last_login_at, created_at, revision
		FROM identity.users
		WHERE organization_id = $1::uuid AND user_type = 'member' AND status <> 'deleted'
		  AND ($3 = '' OR (created_at, id) > (NULLIF($4, '')::timestamptz, $3::uuid))
		ORDER BY created_at, id
		LIMIT $2
	`, principal.OrganizationID, limit+1, cursorID, cursorCreated)
	if err != nil {
		return nil, "", fmt.Errorf("list organization members: %w", err)
	}
	defer rows.Close()
	items := make([]Member, 0, limit+1)
	for rows.Next() {
		var item Member
		if err := rows.Scan(&item.UserID, &item.Email, &item.DisplayName, &item.Status,
			&item.OrganizationRole, &item.LastLoginAt, &item.CreatedAt, &item.Revision); err != nil {
			return nil, "", err
		}
		item.ETag = fmt.Sprint(item.Revision)
		items = append(items, item)
	}
	next := ""
	if len(items) > limit {
		items = items[:limit]
		last := items[len(items)-1]
		next = last.CreatedAt.UTC().Format(time.RFC3339Nano) + "|" + last.UserID
	}
	return items, next, rows.Err()
}

// GetMember returns one member of the organization.
func (s Service) GetMember(ctx context.Context, principal auth.Principal, userID string) (Member, error) {
	if err := s.RequireOrganizationAction(ctx, principal, authz.ActionOrganizationMemberRead); err != nil {
		return Member{}, err
	}
	if !s.validID(userID) {
		return Member{}, ErrInvalidInput
	}
	var item Member
	err := s.Store.Pool.QueryRow(ctx, `
		SELECT id::text, COALESCE(email, ''), display_name, status, COALESCE(organization_role, ''),
		       last_login_at, created_at, revision
		FROM identity.users
		WHERE organization_id = $1::uuid AND id = $2::uuid AND user_type = 'member'
	`, principal.OrganizationID, userID).Scan(&item.UserID, &item.Email, &item.DisplayName,
		&item.Status, &item.OrganizationRole, &item.LastLoginAt, &item.CreatedAt, &item.Revision)
	if errors.Is(err, pgx.ErrNoRows) {
		return Member{}, ErrNotFound
	}
	if err != nil {
		return Member{}, err
	}
	item.ETag = fmt.Sprint(item.Revision)
	return item, nil
}
