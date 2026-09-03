package asset

import (
	"agentchunzhi/internal/noteblocks"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"agentchunzhi/internal/access"
	"agentchunzhi/internal/auth"
	"agentchunzhi/internal/authz"
	"agentchunzhi/internal/eventing"
	"agentchunzhi/internal/store"
	"agentchunzhi/internal/tag"

	"github.com/jackc/pgx/v5"
)

type MemberService struct {
	Store  *store.Store
	Events *eventing.EventStore
	Policy authz.WorkspacePolicy
}

type MemberAssetListInput struct {
	Query             string
	ResourceModelID   string
	ContentKind       string
	Visibility        string
	PublicationStatus string
	ReviewStatus      string
	Filters           map[string]any
	CreatedBy         string
	ContainerID       string
	ParentAssetID     string
	// TagsAny/TagsAll/TagsNone are normalized tag key groups; they resolve to
	// workspace tag IDs before the list SQL is built. Legacy top-level "tags"
	// input is rejected by the handler, never silently applied.
	TagsAny  []string
	TagsAll  []string
	TagsNone []string
	Sort     string
	Limit    int
	Cursor   string
}

type MemberAssetPage struct {
	Items      []MemberAsset
	HasMore    bool
	NextCursor string
}

type MemberAssetInput struct {
	ResourceModelID string         `json:"resource_model_id"`
	Visibility      string         `json:"visibility"`
	Title           *string        `json:"title"`
	Summary         *string        `json:"summary"`
	Markdown        *string        `json:"markdown"`
	Fields          map[string]any `json:"fields"`
	Tags            []string       `json:"tags"`
	Source          map[string]any `json:"source"`
	ContainerIDs    []string       `json:"container_ids"`
	ParentAssetID   *string        `json:"parent_asset_id"`
	// InitialRelations is server-side only (json:"-"): relations the first
	// version materializes, used by the duplicate command to record the
	// duplicate edge. Clients cannot set it through JSON.
	InitialRelations []RelationMaterial `json:"-"`
}

type MemberAssetPatch struct {
	Title         *string         `json:"title"`
	Summary       *string         `json:"summary"`
	Markdown      *string         `json:"markdown"`
	Fields        *map[string]any `json:"fields"`
	Tags          *[]string       `json:"tags"`
	Visibility    *string         `json:"visibility"`
	Source        *map[string]any `json:"source"`
	ContainerIDs  *[]string       `json:"container_ids"`
	ParentAssetID OptionalString  `json:"parent_asset_id"`
}

type OptionalString struct {
	Set   bool
	Value *string
}

func (value *OptionalString) UnmarshalJSON(raw []byte) error {
	value.Set = true
	if string(raw) == "null" {
		value.Value = nil
		return nil
	}
	var decoded string
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return err
	}
	value.Value = &decoded
	return nil
}

type Actor struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
}

type MemberAsset struct {
	ID                        string         `json:"id"`
	WorkspaceID               string         `json:"workspace_id"`
	ResourceModelID           string         `json:"resource_model_id"`
	ContentKind               string         `json:"content_kind,omitempty"`
	ResourceModelVersionID    string         `json:"resource_model_version_id"`
	Title                     *string        `json:"title"`
	Summary                   string         `json:"summary"`
	Markdown                  *string        `json:"markdown,omitempty"`
	Fields                    map[string]any `json:"fields"`
	Visibility                string         `json:"visibility"`
	PublicationStatus         string         `json:"publication_status"`
	Origin                    string         `json:"origin"`
	ConfirmationStatus        string         `json:"confirmation_status"`
	CurrentWorkingVersionID   string         `json:"current_working_version_id"`
	CurrentPublishedVersionID *string        `json:"current_published_version_id"`
	DraftRevision             int64          `json:"draft_revision"`
	DraftCommittedRevision    int64          `json:"draft_committed_revision"`
	ContainerIDs              []string       `json:"container_ids"`
	ParentAssetID             *string        `json:"parent_asset_id"`
	CreatedBy                 Actor          `json:"created_by"`
	PublishedAt               *time.Time     `json:"published_at,omitempty"`
	UpdatedAt                 time.Time      `json:"updated_at"`
	ETag                      string         `json:"etag"`
	sortValue                 string
}

func (s MemberService) require(ctx context.Context, principal auth.Principal, workspaceID, modelID, action string) (authz.Scope, error) {
	if principal.UserType != auth.UserTypeMember || s.Store == nil || s.Store.Pool == nil {
		return authz.Scope{}, ErrForbidden
	}
	if s.Policy == nil {
		return authz.Scope{}, ErrForbidden
	}
	scope, err := s.Policy.Require(ctx, principal, workspaceID, modelID, action)
	if errors.Is(err, authz.ErrWorkspaceForbidden) || errors.Is(err, authz.ErrWorkspaceNotFound) {
		return authz.Scope{}, ErrForbidden
	}
	return scope, err
}

func (s MemberService) List(ctx context.Context, principal auth.Principal, workspaceID string, input MemberAssetListInput) ([]MemberAsset, error) {
	page, err := s.ListPage(ctx, principal, workspaceID, input)
	if err != nil {
		return nil, err
	}
	return page.Items, nil
}

type memberAssetCursor struct {
	Sort      string `json:"sort"`
	UpdatedAt string `json:"updated_at"`
	Value     string `json:"value"`
	ID        string `json:"id"`
}

type memberAssetSort struct {
	Name  string
	Desc  bool
	Expr  string
	Value string
}

func normalizeMemberAssetSort(value string) (memberAssetSort, error) {
	switch value {
	case "", "updated_at:desc":
		return memberAssetSort{Name: "updated_at:desc", Desc: true, Expr: "a.updated_at", Value: "a.updated_at"}, nil
	case "updated_at:asc":
		return memberAssetSort{Name: "updated_at:asc", Expr: "a.updated_at", Value: "a.updated_at"}, nil
	case "title:asc":
		return memberAssetSort{Name: "title:asc", Expr: "v.title", Value: "v.title"}, nil
	case "title:desc":
		return memberAssetSort{Name: "title:desc", Desc: true, Expr: "v.title", Value: "v.title"}, nil
	default:
		return memberAssetSort{}, ErrInvalidInput
	}
}

func encodeAssetCursor(sortName string, item MemberAsset) string {
	updatedAt := item.UpdatedAt.UTC().Format(time.RFC3339Nano)
	value := updatedAt
	if strings.HasPrefix(sortName, "title") {
		if item.Title != nil {
			value = *item.Title
		} else {
			value = ""
		}
	}
	raw, _ := json.Marshal(memberAssetCursor{Sort: sortName, UpdatedAt: updatedAt, Value: value, ID: item.ID})
	return base64.RawURLEncoding.EncodeToString(raw)
}

func decodeAssetCursor(value, sortName string) (memberAssetCursor, error) {
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return memberAssetCursor{}, ErrInvalidInput
	}
	var cursor memberAssetCursor
	if err := json.Unmarshal(raw, &cursor); err != nil {
		return memberAssetCursor{}, ErrInvalidInput
	}
	if cursor.Sort != sortName || !validID(cursor.ID) {
		return memberAssetCursor{}, ErrInvalidInput
	}
	return cursor, nil
}

func (s MemberService) ListPage(ctx context.Context, principal auth.Principal, workspaceID string, input MemberAssetListInput) (MemberAssetPage, error) {
	if !validID(workspaceID) || (input.ResourceModelID != "" && !validID(input.ResourceModelID)) ||
		(input.ContainerID != "" && !validID(input.ContainerID)) || (input.ParentAssetID != "" && !validID(input.ParentAssetID)) {
		return MemberAssetPage{}, ErrInvalidInput
	}
	if !validMemberAssetListEnums(input) {
		return MemberAssetPage{}, ErrInvalidInput
	}
	scope, err := s.require(ctx, principal, workspaceID, input.ResourceModelID, authz.ActionAssetRead)
	if err != nil {
		return MemberAssetPage{}, err
	}
	limit := input.Limit
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	sortKey, err := normalizeMemberAssetSort(input.Sort)
	if err != nil {
		return MemberAssetPage{}, err
	}
	var cursor memberAssetCursor
	if strings.TrimSpace(input.Cursor) != "" {
		cursor, err = decodeAssetCursor(input.Cursor, sortKey.Name)
		if err != nil {
			return MemberAssetPage{}, ErrInvalidInput
		}
	}
	// Working scope: CMS lists read the current working version of every
	// non-deleted asset visible to the membership.
	join := "JOIN asset.asset_versions v ON v.organization_id = a.organization_id AND v.id = a.current_working_version_id"
	cursorColumn := "a.updated_at"
	if strings.HasPrefix(sortKey.Name, "title") {
		cursorColumn = "v.title"
	}
	direction := "DESC"
	if !sortKey.Desc {
		direction = "ASC"
	}
	// A row constructor cannot carry sort directions; use a plain column list.
	orderBy := fmt.Sprintf("%s %s, a.id %s", cursorColumn, direction, direction)
	where := []string{
		"a.organization_id = $1::uuid",
		"a.workspace_id = $2::uuid",
		"a.deleted_at IS NULL",
		"w.status = 'active'",
		// Conversation-bound notes stay out of the document library until
		// they are published or harvested into standalone documents.
		`NOT (a.publication_status = 'draft' AND EXISTS (
			SELECT 1 FROM content.note_bindings nb
			WHERE nb.organization_id = a.organization_id AND nb.note_asset_id = a.id))`,
	}
	args := []any{principal.OrganizationID, workspaceID}
	arg := func(value any) string {
		args = append(args, value)
		return fmt.Sprintf("$%d", len(args))
	}
	if input.ResourceModelID != "" {
		where = append(where, "a.resource_model_id = "+arg(input.ResourceModelID)+"::uuid")
	}
	if input.Visibility != "" {
		where = append(where, "a.visibility = "+arg(input.Visibility))
	}
	if input.PublicationStatus != "" {
		where = append(where, "a.publication_status = "+arg(input.PublicationStatus))
	}
	if input.CreatedBy != "" {
		if !validID(input.CreatedBy) {
			return MemberAssetPage{}, ErrInvalidInput
		}
		where = append(where, "a.created_by = "+arg(input.CreatedBy)+"::uuid")
	}
	if input.Query != "" {
		pattern := "%" + strings.ToLower(input.Query) + "%"
		where = append(where, "(lower(v.title) LIKE "+arg(pattern)+" OR lower(v.markdown) LIKE "+arg(pattern)+")")
	}
	if len(input.Filters) > 0 {
		predicates, _, err := normalizeMemberFilters(input.Filters, nil)
		if err != nil {
			return MemberAssetPage{}, err
		}
		params := &predicateParams{args: args}
		fragment, err := compileFieldPredicatesSQL(params, predicates, "v.fields")
		if err != nil {
			return MemberAssetPage{}, err
		}
		args = params.args
		if fragment != "" {
			where = append(where, fragment)
		}
	}
	if input.ContainerID != "" {
		where = append(where, `EXISTS (SELECT 1 FROM content.container_assets ca
			WHERE ca.organization_id = a.organization_id AND ca.asset_id = a.id
			  AND ca.container_id = `+arg(input.ContainerID)+"::uuid)")
	}
	if input.ParentAssetID != "" {
		where = append(where, `EXISTS (SELECT 1 FROM content.document_parents dp
			WHERE dp.organization_id = a.organization_id AND dp.child_asset_id = a.id
			  AND dp.parent_asset_id = `+arg(input.ParentAssetID)+"::uuid)")
	}
	// Relational tag filters: EXISTS/NOT EXISTS over the working version, the
	// same fixed shape the tag facet counts use.
	tagFilter, err := s.resolveTagFilterKeys(ctx, principal, workspaceID, input)
	if err != nil {
		return MemberAssetPage{}, err
	}
	if len(tagFilter.Any) > 0 {
		where = append(where, fmt.Sprintf(
			"EXISTS (SELECT 1 FROM asset.asset_version_tags fx WHERE fx.asset_version_id = a.current_working_version_id AND fx.tag_id = ANY(%s::uuid[]))", arg(tagFilter.Any)))
	}
	for _, id := range tagFilter.All {
		where = append(where, fmt.Sprintf(
			"EXISTS (SELECT 1 FROM asset.asset_version_tags fa%s WHERE fa%s.asset_version_id = a.current_working_version_id AND fa%s.tag_id = ANY(%s::uuid[]))", id, id, id, arg([]string{id})))
	}
	if len(tagFilter.None) > 0 {
		where = append(where, fmt.Sprintf(
			"NOT EXISTS (SELECT 1 FROM asset.asset_version_tags fn WHERE fn.asset_version_id = a.current_working_version_id AND fn.tag_id = ANY(%s::uuid[]))", arg(tagFilter.None)))
	}
	if cursor.ID != "" {
		comparison := "<"
		if !sortKey.Desc {
			comparison = ">"
		}
		where = append(where, fmt.Sprintf(
			"(%s, a.id) %s (%s::text, %s::uuid)", cursorColumn, comparison, arg(cursor.Value), arg(cursor.ID)))
	}
	query := fmt.Sprintf(`
		SELECT a.id::text, a.workspace_id::text, a.resource_model_id::text,
		       COALESCE(rm.content_kind, ''), v.resource_model_version_id::text,
		       v.title, v.summary, v.markdown, v.fields, a.visibility,
		       a.publication_status, v.origin, v.confirmation_status,
		       a.current_working_version_id::text, a.current_published_version_id::text,
		       d.revision, d.committed_revision,
		       a.published_at, a.updated_at, v.content_checksum,
		       COALESCE(u.display_name, ''), a.created_by::text
		FROM asset.assets a
		JOIN content.workspaces w ON w.organization_id = a.organization_id AND w.id = a.workspace_id
		JOIN asset.asset_drafts d ON d.organization_id = a.organization_id AND d.asset_id = a.id
		%s
		LEFT JOIN model.resource_models rm ON rm.organization_id = a.organization_id AND rm.id = a.resource_model_id
		LEFT JOIN identity.users u ON u.id = a.created_by
		WHERE %s
		ORDER BY %s
		LIMIT %d
	`, join, strings.Join(where, " AND "), orderBy, limit+1)
	rows, err := s.Store.Pool.Query(ctx, query, args...)
	if err != nil {
		return MemberAssetPage{}, fmt.Errorf("list member assets: %w", err)
	}
	defer rows.Close()
	page := MemberAssetPage{Items: make([]MemberAsset, 0, limit+1)}
	for rows.Next() {
		item, err := scanMemberAssetRow(rows)
		if err != nil {
			return MemberAssetPage{}, err
		}
		page.Items = append(page.Items, item)
	}
	if err := rows.Err(); err != nil {
		return MemberAssetPage{}, fmt.Errorf("iterate member assets: %w", err)
	}
	if len(page.Items) > limit {
		page.HasMore = true
		page.Items = page.Items[:limit]
		last := page.Items[len(page.Items)-1]
		page.NextCursor = encodeAssetCursor(sortKey.Name, last)
	}
	_ = scope.Role
	return page, nil
}

func scanMemberAssetRow(rows interface{ Scan(...any) error }) (MemberAsset, error) {
	var item MemberAsset
	var publishedAt *time.Time
	var createdByName string
	if err := rows.Scan(&item.ID, &item.WorkspaceID, &item.ResourceModelID,
		&item.ContentKind, &item.ResourceModelVersionID,
		&item.Title, &item.Summary, &item.Markdown, &item.Fields, &item.Visibility,
		&item.PublicationStatus, &item.Origin, &item.ConfirmationStatus,
		&item.CurrentWorkingVersionID, &item.CurrentPublishedVersionID,
		&item.DraftRevision, &item.DraftCommittedRevision,
		&publishedAt, &item.UpdatedAt, &item.ETag,
		&createdByName, &item.CreatedBy.ID); err != nil {
		return MemberAsset{}, fmt.Errorf("scan member asset: %w", err)
	}
	if item.Fields == nil {
		item.Fields = map[string]any{}
	}
	item.CreatedBy.DisplayName = createdByName
	item.PublishedAt = publishedAt
	return item, nil
}

func validMemberAssetListEnums(input MemberAssetListInput) bool {
	if input.Visibility != "" && !access.Valid(input.Visibility) {
		return false
	}
	switch input.PublicationStatus {
	case "", PublicationDraft, PublicationPublished, PublicationArchived:
	default:
		return false
	}
	switch input.Sort {
	case "", "updated_at:desc", "updated_at:asc", "title:asc", "title:desc":
	default:
		return false
	}
	return true
}

func (s MemberService) Get(ctx context.Context, principal auth.Principal, assetID string) (MemberAsset, error) {
	if !validID(assetID) {
		return MemberAsset{}, ErrInvalidInput
	}
	var workspaceID, modelID string
	err := s.Store.Pool.QueryRow(ctx, `
		SELECT workspace_id::text, resource_model_id::text FROM asset.assets
		WHERE organization_id = $1::uuid AND id = $2::uuid AND deleted_at IS NULL
	`, principal.OrganizationID, assetID).Scan(&workspaceID, &modelID)
	if errors.Is(err, pgx.ErrNoRows) {
		return MemberAsset{}, ErrNotFound
	}
	if err != nil {
		return MemberAsset{}, err
	}
	if _, err := s.require(ctx, principal, workspaceID, modelID, authz.ActionAssetRead); err != nil {
		return MemberAsset{}, err
	}
	rows, err := s.Store.Pool.Query(ctx, `
		SELECT a.id::text, a.workspace_id::text, a.resource_model_id::text,
		       COALESCE(rm.content_kind, ''), v.resource_model_version_id::text,
		       v.title, v.summary, v.markdown, v.fields, a.visibility,
		       a.publication_status, v.origin, v.confirmation_status,
		       a.current_working_version_id::text, a.current_published_version_id::text,
		       d.revision, d.committed_revision,
		       a.published_at, a.updated_at, v.content_checksum,
		       COALESCE(u.display_name, ''), a.created_by::text
		FROM asset.assets a
		JOIN asset.asset_versions v ON v.organization_id = a.organization_id AND v.id = a.current_working_version_id
		JOIN asset.asset_drafts d ON d.organization_id = a.organization_id AND d.asset_id = a.id
		LEFT JOIN model.resource_models rm ON rm.organization_id = a.organization_id AND rm.id = a.resource_model_id
		LEFT JOIN identity.users u ON u.id = a.created_by
		WHERE a.organization_id = $1::uuid AND a.id = $2::uuid
	`, principal.OrganizationID, assetID)
	if err != nil {
		return MemberAsset{}, err
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return MemberAsset{}, err
		}
		return MemberAsset{}, ErrNotFound
	}
	item, err := scanMemberAssetRow(rows)
	if err != nil {
		return MemberAsset{}, err
	}
	containerIDs, parentID, err := loadMemberAssetRelations(ctx, s.Store, principal.OrganizationID, assetID)
	if err != nil {
		return MemberAsset{}, err
	}
	item.ContainerIDs = containerIDs
	item.ParentAssetID = parentID
	return item, nil
}

func loadMemberAssetRelations(ctx context.Context, db *store.Store, organizationID, assetID string) ([]string, *string, error) {
	containerIDs := []string{}
	rows, err := db.Pool.Query(ctx, `
		SELECT container_id::text FROM content.container_assets
		WHERE organization_id = $1::uuid AND asset_id = $2::uuid
		ORDER BY container_id
	`, organizationID, assetID)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, nil, err
		}
		containerIDs = append(containerIDs, id)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	var parent *string
	err = db.Pool.QueryRow(ctx, `
		SELECT parent_asset_id::text FROM content.document_parents
		WHERE organization_id = $1::uuid AND child_asset_id = $2::uuid
	`, organizationID, assetID).Scan(&parent)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, err
	}
	return containerIDs, parent, nil
}

// Create inserts the asset, its first sealed version and the shared draft in
// one transaction. The draft starts clean at revision 1.
func (s MemberService) Create(ctx context.Context, principal auth.Principal, workspaceID, idempotencyKey string, input MemberAssetInput) (MemberAsset, error) {
	if !validID(workspaceID) || !validID(input.ResourceModelID) {
		return MemberAsset{}, ErrInvalidInput
	}
	scope, err := s.require(ctx, principal, workspaceID, input.ResourceModelID, authz.ActionAssetWrite)
	if err != nil {
		return MemberAsset{}, err
	}
	_ = scope
	var title string
	if input.Title != nil {
		title = strings.TrimSpace(*input.Title)
	}
	var summary string
	if input.Summary != nil {
		summary = strings.TrimSpace(*input.Summary)
	}
	var markdown string
	if input.Markdown != nil {
		markdown = *input.Markdown
	}
	fields := input.Fields
	if fields == nil {
		fields = map[string]any{}
	}
	if err := ValidateContent(&title, &markdown, &fields); err != nil {
		return MemberAsset{}, ErrInvalidInput
	}
	var resourceModelVersionID string
	var policyRaw []byte
	if err := s.Store.Pool.QueryRow(ctx, `
		SELECT COALESCE(m.current_version_id::text, ''), COALESCE(v.policy, '{}'::jsonb)
		FROM model.resource_models m
		JOIN model.resource_model_versions v ON v.organization_id = m.organization_id AND v.id = m.current_version_id
		WHERE m.organization_id = $1::uuid AND m.id = $2::uuid AND m.status = 'active'
	`, principal.OrganizationID, input.ResourceModelID).Scan(&resourceModelVersionID, &policyRaw); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return MemberAsset{}, ErrInvalidInput
		}
		return MemberAsset{}, err
	}
	if resourceModelVersionID == "" {
		return MemberAsset{}, ErrInvalidInput
	}
	// The requested visibility must sit inside the bound model policy's
	// allow-list; an empty request falls back to the workspace default.
	visibility, err := memberAssetVisibility(policyRaw, input.Visibility)
	if err != nil {
		return MemberAsset{}, err
	}
	var schemaBytes []byte
	if err := s.Store.Pool.QueryRow(ctx, `
		SELECT field_schema FROM model.resource_model_versions WHERE id = $1::uuid
	`, resourceModelVersionID).Scan(&schemaBytes); err != nil {
		return MemberAsset{}, err
	}
	fields = applyDefaults(schemaBytes, fields)
	if err := ValidateFields(schemaBytes, fields); err != nil {
		return MemberAsset{}, ErrInvalidInput
	}

	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return MemberAsset{}, err
	}
	defer tx.Rollback(ctx)
	// Working pointer and draft are set after version 1 exists; deferred
	// constraint triggers refuse to commit an asset without them.
	var assetID string
	if err := tx.QueryRow(ctx, `
		INSERT INTO asset.assets (organization_id, workspace_id, resource_model_id, visibility, created_by)
		VALUES ($1::uuid, $2::uuid, $3::uuid, $4, $5::uuid)
		RETURNING id::text
	`, principal.OrganizationID, workspaceID, input.ResourceModelID, visibility, principal.UserID).Scan(&assetID); err != nil {
		return MemberAsset{}, fmt.Errorf("insert asset: %w", err)
	}
	material := VersionMaterial{
		OrganizationID:         principal.OrganizationID,
		WorkspaceID:            workspaceID,
		AssetID:               assetID,
		ResourceModelID:        input.ResourceModelID,
		ResourceModelVersionID: resourceModelVersionID,
		Origin:                 OriginHuman,
		ConfirmationStatus:     ConfirmationUnconfirmed,
		Title:                  title,
		Summary:                summary,
		Markdown:               markdown,
		Fields:                 fields,
		Relations:              input.InitialRelations,
		CreatedBy:              principal.UserID,
	}
	versionID, _, err := CreateVersionTx(ctx, tx, material)
	if err != nil {
		return MemberAsset{}, err
	}
	if err := attachRelationsTx(ctx, tx, principal, workspaceID, assetID, input.ContainerIDs, input.ParentAssetID); err != nil {
		return MemberAsset{}, err
	}
	var draftID string
	if err := tx.QueryRow(ctx, `
		INSERT INTO asset.asset_drafts
			(organization_id, workspace_id, asset_id, base_version_id, revision, committed_revision,
			 title, summary, markdown, fields, origin, updated_by)
		VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, 1, 1, $5, $6, $7, $8::jsonb, $9, $10::uuid)
		RETURNING id::text
	`, principal.OrganizationID, workspaceID, assetID, versionID, title, summary, markdown, string(mustJSON(fields)), OriginHuman, principal.UserID).Scan(&draftID); err != nil {
		return MemberAsset{}, fmt.Errorf("insert draft: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE asset.assets
		SET current_working_version_id = $3::uuid, draft_id = $4::uuid
		WHERE organization_id = $1::uuid AND id = $2::uuid
	`, principal.OrganizationID, assetID, versionID, draftID); err != nil {
		return MemberAsset{}, fmt.Errorf("link asset pointers: %w", err)
	}
	// The fresh draft inherits version provenance for tags (empty at create).
	if err := initializeDraftTagsFromVersionTx(ctx, tx, principal.OrganizationID, draftID, versionID); err != nil {
		return MemberAsset{}, err
	}
	row, err := LoadLifecycleTx(ctx, tx, principal.OrganizationID, assetID)
	if err != nil {
		return MemberAsset{}, err
	}
	if err := AppendAssetEventTx(ctx, tx, s.Events, row, principal, eventing.EventAssetVersionCreated, eventing.PayloadVersionV1, eventing.AssetVersionCreatedPayload{
		AssetID:     assetID,
		VersionID:   versionID,
		VersionNo:   1,
		WorkspaceID: workspaceID,
	}); err != nil {
		return MemberAsset{}, err
	}
	RecordAssetAuditTx(ctx, tx, principal.OrganizationID, workspaceID, principal, "asset.create", assetID, map[string]any{
		"workspace_id": workspaceID,
		"version_id":   versionID,
	})
	if err := tx.Commit(ctx); err != nil {
		return MemberAsset{}, err
	}
	return s.Get(ctx, principal, assetID)
}

// Update autosaves the shared draft; versions are only created by commit
// paths. expectedVersionID carries the If-Match draft revision from handler
// middleware.
func (s MemberService) Update(ctx context.Context, principal auth.Principal, assetID, expectedRevision, idempotencyKey string, input MemberAssetPatch) (MemberAsset, error) {
	if !validID(assetID) {
		return MemberAsset{}, ErrInvalidInput
	}
	patch := DraftPatch{Title: input.Title, Summary: input.Summary, Markdown: input.Markdown, Fields: input.Fields, Visibility: input.Visibility}
	if _, err := s.AutosaveDraft(ctx, principal, assetID, expectedRevision, patch); err != nil {
		return MemberAsset{}, err
	}
	if input.ContainerIDs != nil || input.ParentAssetID.Set {
		var workspaceID, modelID string
		err := s.Store.Pool.QueryRow(ctx, `
			SELECT workspace_id::text, resource_model_id::text FROM asset.assets
			WHERE organization_id = $1::uuid AND id = $2::uuid
		`, principal.OrganizationID, assetID).Scan(&workspaceID, &modelID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return MemberAsset{}, ErrNotFound
			}
			return MemberAsset{}, err
		}
		if _, err := s.require(ctx, principal, workspaceID, modelID, authz.ActionAssetWrite); err != nil {
			return MemberAsset{}, err
		}
		tx, err := s.Store.Pool.Begin(ctx)
		if err != nil {
			return MemberAsset{}, err
		}
		defer tx.Rollback(ctx)
		if err := attachRelationsTx(ctx, tx, principal, workspaceID, assetID, derefStringSlice(input.ContainerIDs), optionalValue(input.ParentAssetID)); err != nil {
			return MemberAsset{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return MemberAsset{}, err
		}
	}
	return s.Get(ctx, principal, assetID)
}

func derefStringSlice(value *[]string) []string {
	if value == nil {
		return nil
	}
	return *value
}

func optionalValue(value OptionalString) *string {
	if !value.Set {
		return nil
	}
	return value.Value
}

// PublishingPolicy is the publishing block of a ResourceModelVersion policy.
// It is always read from the immutable version an asset version binds to
// (asset_versions.resource_model_version_id), never from the model head: a
// model upgrade must not reinterpret assets created under earlier versions.
type PublishingPolicy struct {
	Mode                     string
	RequiredFields           []string
	RequireCleanAttachments  bool
	RequireHumanConfirmation bool
}

// PublishPolicyForAssetTx returns the publishing policy of the immutable
// ResourceModelVersion bound to the asset's current working version.
func PublishPolicyForAssetTx(ctx context.Context, tx pgx.Tx, organizationID, assetID string) (PublishingPolicy, error) {
	var raw []byte
	err := tx.QueryRow(ctx, `
		SELECT COALESCE(v.policy, '{}'::jsonb)
		FROM asset.assets a
		JOIN asset.asset_versions av ON av.organization_id = a.organization_id AND av.id = a.current_working_version_id
		JOIN model.resource_model_versions v ON v.organization_id = av.organization_id AND v.id = av.resource_model_version_id
		WHERE a.organization_id = $1::uuid AND a.id = $2::uuid
	`, organizationID, assetID).Scan(&raw)
	if err != nil {
		return PublishingPolicy{}, fmt.Errorf("load publishing policy: %w", err)
	}
	return decodePublishingPolicy(raw)
}

// loadModelPolicyTx reads the policy of the model head's current version; an
// unknown model hides as NotFound. The empty policy means "no restrictions".
func loadModelPolicyTx(ctx context.Context, tx pgx.Tx, organizationID, modelID string) ([]byte, error) {
	var raw []byte
	err := tx.QueryRow(ctx, `
		SELECT COALESCE(v.policy, '{}'::jsonb)
		FROM model.resource_models m
		LEFT JOIN model.resource_model_versions v ON v.organization_id = m.organization_id AND v.id = m.current_version_id
		WHERE m.organization_id = $1::uuid AND m.id = $2::uuid
	`, organizationID, modelID).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("load model policy: %w", err)
	}
	return raw, nil
}

// decodePublishingPolicy parses the publishing block of a ResourceModelVersion
// policy. Omitting the block is intentional semantics, not a gap: a custom
// model version without a publishing section decodes as direct mode with both
// gates and required fields disabled, matching the phase-0 canonical policy
// where direct is the baseline mode. Only model versions that explicitly opt
// in get the approval queue or the publish gates.
func decodePublishingPolicy(raw []byte) (PublishingPolicy, error) {
	var document struct {
		Publishing struct {
			Mode                     string   `json:"mode"`
			RequiredFields           []string `json:"required_fields"`
			RequireCleanAttachments  bool     `json:"require_clean_attachments"`
			RequireHumanConfirmation bool     `json:"require_human_confirmation"`
		} `json:"publishing"`
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &document); err != nil {
			return PublishingPolicy{}, fmt.Errorf("decode publishing policy: %w", err)
		}
	}
	policy := PublishingPolicy{
		Mode:                     document.Publishing.Mode,
		RequiredFields:           document.Publishing.RequiredFields,
		RequireCleanAttachments:  document.Publishing.RequireCleanAttachments,
		RequireHumanConfirmation: document.Publishing.RequireHumanConfirmation,
	}
	if policy.Mode == "" {
		policy.Mode = PublishingModeDirect
	}
	return policy, nil
}

// publishGateFacts are the per-version facts the immutable policy gates judge.
type publishGateFacts struct {
	HumanConfirmed     bool
	UncleanAttachments int64
	Fields             map[string]any
}

// gate rejects versions that miss the policy's publish preconditions. The
// sentinels are the shared asset errors so every surface map them alike.
func (p PublishingPolicy) gate(facts publishGateFacts) error {
	for _, name := range p.RequiredFields {
		value, ok := facts.Fields[name]
		if !ok || !requiredFieldValuePresent(value) {
			return ErrRequiredFieldMissing
		}
	}
	if p.RequireHumanConfirmation && !facts.HumanConfirmed {
		return ErrConfirmationRequired
	}
	if p.RequireCleanAttachments && facts.UncleanAttachments > 0 {
		return ErrAttachmentNotClean
	}
	return nil
}

// requiredFieldValuePresent judges one policy-required field value: strings
// must be non-blank after trimming, arrays and objects non-empty; any other
// non-nil scalar counts as present.
func requiredFieldValuePresent(value any) bool {
	switch typed := value.(type) {
	case nil:
		return false
	case string:
		return strings.TrimSpace(typed) != ""
	case []any:
		return len(typed) > 0
	case map[string]any:
		return len(typed) > 0
	default:
		return true
	}
}

// EnsurePublishableVersionTx enforces the required-fields,
// human-confirmation and clean-attachment gates of policy against the sealed
// version. Callers run it inside their publish transaction so the checks and
// the pointer switch commit together.
func EnsurePublishableVersionTx(ctx context.Context, tx pgx.Tx, organizationID, versionID string, policy PublishingPolicy) error {
	var facts publishGateFacts
	var fieldsRaw []byte
	err := tx.QueryRow(ctx, `
		SELECT
		  COALESCE((SELECT confirmation_status = 'human_confirmed' FROM asset.asset_versions
		   WHERE organization_id = $1::uuid AND id = $2::uuid), false),
		  (SELECT count(*) FROM asset.asset_version_attachments lva
		   JOIN asset.attachments att ON att.organization_id = lva.organization_id AND att.id = lva.attachment_id
		   WHERE lva.organization_id = $1::uuid AND lva.asset_version_id = $2::uuid
		     AND (att.status <> 'clean' OR att.deleted_at IS NOT NULL)),
		  COALESCE((SELECT fields FROM asset.asset_versions
		   WHERE organization_id = $1::uuid AND id = $2::uuid), '{}'::jsonb)
	`, organizationID, versionID).Scan(&facts.HumanConfirmed, &facts.UncleanAttachments, &fieldsRaw)
	if err != nil {
		return fmt.Errorf("load publish gate facts: %w", err)
	}
	facts.Fields = map[string]any{}
	if len(fieldsRaw) > 0 {
		if err := json.Unmarshal(fieldsRaw, &facts.Fields); err != nil {
			return fmt.Errorf("decode publish gate fields: %w", err)
		}
	}
	return policy.gate(facts)
}

// Publish executes a direct-policy publish: commit the dirty draft, then
// switch the published pointer. Approval-policy assets return a conflict and
// must go through a PublicationRequest.
func (s MemberService) Publish(ctx context.Context, principal auth.Principal, workspaceID, assetID, expectedDraftRevision, idempotencyKey string) (MemberAsset, error) {
	if !validID(assetID) {
		return MemberAsset{}, ErrInvalidInput
	}
	var modelID string
	if err := s.Store.Pool.QueryRow(ctx, `
		SELECT resource_model_id::text FROM asset.assets
		WHERE organization_id = $1::uuid AND id = $2::uuid AND deleted_at IS NULL
	`, principal.OrganizationID, assetID).Scan(&modelID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return MemberAsset{}, ErrNotFound
		}
		return MemberAsset{}, err
	}
	if _, err := s.require(ctx, principal, workspaceID, modelID, authz.ActionAssetPublish); err != nil {
		return MemberAsset{}, err
	}
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return MemberAsset{}, err
	}
	defer tx.Rollback(ctx)
	// Lock order is tree container -> asset -> draft (edit paths take the
	// same order), matching CommitDraft.
	if _, isNote, noteErr := noteContainerIDTx(ctx, tx, principal.OrganizationID, assetID); noteErr != nil {
		return MemberAsset{}, noteErr
	} else if isNote {
		if _, _, err := noteblocks.LoadTreeByAssetTx(ctx, tx, principal.OrganizationID, assetID); err != nil {
			return MemberAsset{}, err
		}
	}
	row, err := LoadLifecycleTx(ctx, tx, principal.OrganizationID, assetID)
	if err != nil {
		return MemberAsset{}, err
	}
	if row.PublicationStatus == PublicationArchived {
		return MemberAsset{}, ErrAssetArchived
	}
	policy, err := PublishPolicyForAssetTx(ctx, tx, row.OrganizationID, assetID)
	if err != nil {
		return MemberAsset{}, err
	}
	if policy.Mode != PublishingModeDirect {
		return MemberAsset{}, ErrApprovalRequired
	}
	draft, err := LoadDraftTx(ctx, tx, row.OrganizationID, assetID, expectedDraftRevision)
	if err != nil {
		return MemberAsset{}, err
	}
	freeze := commitDraftStrategy(ctx, tx, row.OrganizationID, assetID)
	result, err := freeze(ctx, tx, s.Events, principal, row, draft)
	if err != nil {
		return MemberAsset{}, err
	}
	row = result.Asset
	if err := EnsurePublishableVersionTx(ctx, tx, row.OrganizationID, row.CurrentWorkingVersionID, policy); err != nil {
		return MemberAsset{}, err
	}
	previous := row.CurrentPublishedVersionID
	row, err = SetPublishedPointerTx(ctx, tx, row, row.CurrentWorkingVersionID)
	if err != nil {
		return MemberAsset{}, err
	}
	if err := AppendAssetEventTx(ctx, tx, s.Events, row, principal, eventing.EventAssetPublished, eventing.PayloadVersionV1, eventing.AssetPublishedPayload{
		AssetID:           row.ID,
		VersionID:         row.CurrentWorkingVersionID,
		PreviousVersionID: derefOrEmpty(previous),
		WorkspaceID:       row.WorkspaceID,
	}); err != nil {
		return MemberAsset{}, err
	}
	RecordAssetAuditTx(ctx, tx, row.OrganizationID, row.WorkspaceID, principal, "asset.publish", row.ID,
		MergeAgentProvenance(map[string]any{
			"workspace_id": row.WorkspaceID,
			"version_id":   row.CurrentWorkingVersionID,
		}, agentProvenanceTx(ctx, tx, row.OrganizationID, row.CurrentWorkingVersionID)))
	if err := tx.Commit(ctx); err != nil {
		return MemberAsset{}, err
	}
	return s.Get(ctx, principal, assetID)
}

func derefOrEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// Archive clears the published pointer, cancels pending publication requests
// and keeps the draft frozen but intact.
func (s MemberService) Archive(ctx context.Context, principal auth.Principal, assetID, idempotencyKey string) (MemberAsset, error) {
	if !validID(assetID) {
		return MemberAsset{}, ErrInvalidInput
	}
	var workspaceID, modelID string
	if err := s.Store.Pool.QueryRow(ctx, `
		SELECT workspace_id::text, resource_model_id::text FROM asset.assets
		WHERE organization_id = $1::uuid AND id = $2::uuid AND deleted_at IS NULL
	`, principal.OrganizationID, assetID).Scan(&workspaceID, &modelID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return MemberAsset{}, ErrNotFound
		}
		return MemberAsset{}, err
	}
	if _, err := s.require(ctx, principal, workspaceID, modelID, authz.ActionAssetArchive); err != nil {
		return MemberAsset{}, err
	}
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return MemberAsset{}, err
	}
	defer tx.Rollback(ctx)
	row, err := LoadLifecycleTx(ctx, tx, principal.OrganizationID, assetID)
	if err != nil {
		return MemberAsset{}, err
	}
	if row.PublicationStatus == PublicationArchived {
		return MemberAsset{}, ErrConflict
	}
	previous := row.CurrentPublishedVersionID
	if _, err := CancelPendingRequestsTx(ctx, tx, s.Events, row, principal, "asset_archived"); err != nil {
		return MemberAsset{}, err
	}
	row, err = ClearPublishedPointerTx(ctx, tx, row)
	if err != nil {
		return MemberAsset{}, err
	}
	if err := AppendAssetEventTx(ctx, tx, s.Events, row, principal, eventing.EventAssetArchived, eventing.PayloadVersionV1, eventing.AssetArchivedPayload{
		AssetID:           row.ID,
		PreviousVersionID: derefOrEmpty(previous),
		WorkspaceID:       row.WorkspaceID,
	}); err != nil {
		return MemberAsset{}, err
	}
	RecordAssetAuditTx(ctx, tx, row.OrganizationID, row.WorkspaceID, principal, "asset.archive", row.ID, map[string]any{
		"workspace_id": row.WorkspaceID,
	})
	if err := tx.Commit(ctx); err != nil {
		return MemberAsset{}, err
	}
	return s.Get(ctx, principal, assetID)
}

// Restore returns an archived asset to draft; it never republishes.
func (s MemberService) Restore(ctx context.Context, principal auth.Principal, assetID, idempotencyKey string) (MemberAsset, error) {
	if !validID(assetID) {
		return MemberAsset{}, ErrInvalidInput
	}
	var workspaceID, modelID string
	if err := s.Store.Pool.QueryRow(ctx, `
		SELECT workspace_id::text, resource_model_id::text FROM asset.assets
		WHERE organization_id = $1::uuid AND id = $2::uuid AND deleted_at IS NULL
	`, principal.OrganizationID, assetID).Scan(&workspaceID, &modelID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return MemberAsset{}, ErrNotFound
		}
		return MemberAsset{}, err
	}
	if _, err := s.require(ctx, principal, workspaceID, modelID, authz.ActionAssetArchive); err != nil {
		return MemberAsset{}, err
	}
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return MemberAsset{}, err
	}
	defer tx.Rollback(ctx)
	row, err := LoadLifecycleTx(ctx, tx, principal.OrganizationID, assetID)
	if err != nil {
		return MemberAsset{}, err
	}
	row, err = RestoreToDraftTx(ctx, tx, row)
	if err != nil {
		return MemberAsset{}, err
	}
	if err := AppendAssetEventTx(ctx, tx, s.Events, row, principal, eventing.EventAssetRestored, eventing.PayloadVersionV1, eventing.AssetRestoredPayload{
		AssetID:     row.ID,
		WorkspaceID: row.WorkspaceID,
	}); err != nil {
		return MemberAsset{}, err
	}
	RecordAssetAuditTx(ctx, tx, row.OrganizationID, row.WorkspaceID, principal, "asset.restore", row.ID, map[string]any{
		"workspace_id": row.WorkspaceID,
	})
	if err := tx.Commit(ctx); err != nil {
		return MemberAsset{}, err
	}
	return s.Get(ctx, principal, assetID)
}

// ConfirmVersion creates a derived version of an unconfirmed snapshot and
// records the human confirmation on the new snapshot only.
func (s MemberService) ConfirmVersion(ctx context.Context, principal auth.Principal, versionID, idempotencyKey string) (MemberAssetVersion, error) {
	if !validID(versionID) {
		return MemberAssetVersion{}, ErrInvalidInput
	}
	var organizationID, workspaceID, assetID, modelID string
	var origin, title, summary, markdown string
	var fields []byte
	var confirmed bool
	err := s.Store.Pool.QueryRow(ctx, `
		SELECT v.organization_id::text, v.workspace_id::text, v.asset_id::text, v.resource_model_id::text,
		       v.origin, v.title, v.summary, v.markdown, v.fields,
		       (v.confirmation_status = 'human_confirmed')
		FROM asset.asset_versions v
		WHERE v.id = $1::uuid
	`, versionID).Scan(&organizationID, &workspaceID, &assetID, &modelID,
		&origin, &title, &summary, &markdown, &fields, &confirmed)
	if errors.Is(err, pgx.ErrNoRows) {
		return MemberAssetVersion{}, ErrNotFound
	}
	if err != nil {
		return MemberAssetVersion{}, err
	}
	if confirmed {
		return MemberAssetVersion{}, ErrConflict
	}
	// Idempotency: a prior confirm of the same source already derived a
	// human_confirmed child — return that snapshot instead of deriving an
	// identical twin (the same source cannot be confirmed twice).
	var derivedChild string
	err = s.Store.Pool.QueryRow(ctx, `
		SELECT v.id::text
		FROM asset.asset_versions v
		WHERE v.organization_id = $1::uuid AND v.parent_version_id = $2::uuid
		  AND v.confirmation_status = 'human_confirmed'
		ORDER BY v.created_at
		LIMIT 1
	`, organizationID, versionID).Scan(&derivedChild)
	if err == nil {
		return s.GetVersion(ctx, principal, derivedChild)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return MemberAssetVersion{}, err
	}
	if _, err := s.require(ctx, principal, workspaceID, modelID, authz.ActionAssetConfirm); err != nil {
		return MemberAssetVersion{}, err
	}
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return MemberAssetVersion{}, err
	}
	defer tx.Rollback(ctx)
	row, err := LoadLifecycleTx(ctx, tx, organizationID, assetID)
	if err != nil {
		return MemberAssetVersion{}, err
	}
	if row.PublicationStatus == PublicationArchived {
		return MemberAssetVersion{}, ErrAssetArchived
	}
	tagIDs, err := loadVersionTagIDs(ctx, tx, versionID)
	if err != nil {
		return MemberAssetVersion{}, err
	}
	attachmentIDs, err := loadVersionAttachmentIDs(ctx, tx, versionID)
	if err != nil {
		return MemberAssetVersion{}, err
	}
	coverID, err := loadVersionCoverID(ctx, tx, versionID)
	if err != nil {
		return MemberAssetVersion{}, err
	}
	// The derived snapshot binds the model head's current version; its field
	// schema gates the carried-over fields inside the same transaction.
	var modelVersionID string
	var schemaBytes []byte
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(m.current_version_id::text, ''), COALESCE(v.field_schema, '{}'::jsonb)
		FROM model.resource_models m
		LEFT JOIN model.resource_model_versions v ON v.organization_id = m.organization_id AND v.id = m.current_version_id
		WHERE m.organization_id = $1::uuid AND m.id = $2::uuid
	`, organizationID, modelID).Scan(&modelVersionID, &schemaBytes); err != nil {
		return MemberAssetVersion{}, err
	}
	decodedFields := map[string]any{}
	_ = json.Unmarshal(fields, &decodedFields)
	decodedFields = applyDefaults(schemaBytes, decodedFields)
	if err := ValidateFields(schemaBytes, decodedFields); err != nil {
		return MemberAssetVersion{}, ErrInvalidInput
	}
	newVersionID, _, err := CreateVersionTx(ctx, tx, VersionMaterial{
		OrganizationID:         organizationID,
		WorkspaceID:            workspaceID,
		AssetID:                assetID,
		ResourceModelID:        modelID,
		ResourceModelVersionID: modelVersionID,
		ParentVersionID:        versionID,
		Origin:                 origin,
		ConfirmationStatus:     ConfirmationHumanConfirmed,
		Title:                  title,
		Summary:                summary,
		Markdown:               markdown,
		Fields:                 decodedFields,
		TagIDs:                 tagIDs,
		AttachmentIDs:          attachmentIDs,
		CoverAttachmentID:      coverID,
		CreatedBy:              principal.UserID,
	})
	if err != nil {
		return MemberAssetVersion{}, err
	}
	// The confirmed snapshot becomes the working copy and the draft rebases;
	// pending requests must be re-submitted against the new version.
	if _, err := CancelPendingRequestsTx(ctx, tx, s.Events, row, principal, reviewCancelReasonNewVersion); err != nil {
		return MemberAssetVersion{}, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE asset.asset_drafts
		-- PG UPDATE assignments all read the OLD row: committed must track the new revision.
		SET base_version_id = $3::uuid, revision = revision + 1, committed_revision = revision + 1,
		    updated_by = $4::uuid, updated_at = now()
		WHERE organization_id = $1::uuid AND asset_id = $2::uuid
	`, organizationID, assetID, newVersionID, principal.UserID); err != nil {
		return MemberAssetVersion{}, fmt.Errorf("rebase draft after confirm: %w", err)
	}
	// The rebased draft starts unconfirmed and mirrors the new snapshot's tags
	// exactly — stale draft-only tags from before the confirm do not survive.
	if _, err := tx.Exec(ctx, `
		DELETE FROM asset.asset_draft_tags
		WHERE organization_id = $1::uuid
		  AND asset_draft_id IN (
		      SELECT id FROM asset.asset_drafts
		      WHERE organization_id = $1::uuid AND asset_id = $2::uuid
		  )
	`, organizationID, assetID); err != nil {
		return MemberAssetVersion{}, fmt.Errorf("clear draft tags after confirm: %w", err)
	}
	// The confirmed snapshot becomes the working copy.
	if _, err := tx.Exec(ctx, `
		UPDATE asset.assets
		SET current_working_version_id = $3::uuid, revision = revision + 1, updated_at = now()
		WHERE organization_id = $1::uuid AND id = $2::uuid
	`, organizationID, assetID, newVersionID); err != nil {
		return MemberAssetVersion{}, fmt.Errorf("advance working pointer after confirm: %w", err)
	}
	// The rebased draft starts unconfirmed and inherits the new snapshot's tags.
	if err := initializeDraftTagsFromVersionTx(ctx, tx, organizationID, func() string {
		var draftID string
		_ = tx.QueryRow(ctx, `SELECT id::text FROM asset.asset_drafts WHERE organization_id = $1::uuid AND asset_id = $2::uuid`, organizationID, assetID).Scan(&draftID)
		return draftID
	}(), newVersionID); err != nil {
		return MemberAssetVersion{}, err
	}
	RecordAssetAuditTx(ctx, tx, organizationID, workspaceID, principal, "asset.version.confirmed", assetID, map[string]any{
		"workspace_id":      workspaceID,
		"source_version_id": versionID,
		"version_id":        newVersionID,
	})
	if err := tx.Commit(ctx); err != nil {
		return MemberAssetVersion{}, err
	}
	return s.GetVersion(ctx, principal, newVersionID)
}

// normalizeMemberFilters converts the dynamic field-filter input into typed
// predicates; internal/asset/field_predicate.go renders them as parameterized
// SQL. Tags are no longer a JSON field: relational tag filters arrive with the
// tag domain.
func normalizeMemberFilters(filters map[string]any, directTags []string) ([]fieldPredicate, []byte, error) {
	_ = directTags
	if len(filters) == 0 {
		return nil, nil, nil
	}
	fields, _ := filters["fields"].(map[string]any)
	if len(fields) == 0 {
		return nil, nil, nil
	}
	predicates := make([]fieldPredicate, 0)
	for field, rawOperations := range fields {
		operations, err := normalizeAssetPredicate(field, rawOperations)
		if err != nil {
			return nil, nil, err
		}
		predicates = append(predicates, operations...)
	}
	if len(predicates) == 0 {
		return nil, nil, nil
	}
	return predicates, nil, nil
}

func normalizeAssetPredicate(field string, rawOperations any) ([]fieldPredicate, error) {
	operations, ok := rawOperations.(map[string]any)
	if !ok {
		return nil, ErrInvalidInput
	}
	predicates := make([]fieldPredicate, 0, len(operations))
	for operator, rawValue := range operations {
		switch operator {
		case "eq", "neq", "in", "contains", "contains_any", "gte", "lte", "exists":
		default:
			return nil, ErrInvalidInput
		}
		if value, ok := rawValue.([]any); ok {
			if len(value) == 0 || len(value) > 100 {
				return nil, ErrInvalidInput
			}
		}
		predicates = append(predicates, fieldPredicate{Field: field, Operator: operator, Value: rawValue})
	}
	if len(predicates) == 0 || len(predicates) > 8 {
		return nil, ErrInvalidInput
	}
	return predicates, nil
}

var (
	ErrApprovalRequired     = errors.New("publication requires approval")
	ErrConfirmationRequired = errors.New("version requires human confirmation")
	// ErrRequiredFieldMissing reports a version whose fields lack a non-empty
	// value for a field the publishing policy declares required.
	ErrRequiredFieldMissing = errors.New("version is missing a policy-required field")
	// ErrUnknownTagKey reports a tag filter key that resolves to no tag in the
	// workspace: misspellings fail loudly instead of silently matching nothing.
	ErrUnknownTagKey = errors.New("unknown tag key")
)

// resolveTagFilterKeys normalizes the any/all/none key groups (dedupe, size
// and contradiction rules from the tag domain) and resolves every key to a
// workspace tag ID with one inline query per non-empty group. Archived tags
// resolve too so historical versions stay filterable.
func (s MemberService) resolveTagFilterKeys(ctx context.Context, principal auth.Principal, workspaceID string, input MemberAssetListInput) (tag.ResolvedFilter, error) {
	normalized, err := tag.NormalizeFilter(tag.KeyFilter{Any: input.TagsAny, All: input.TagsAll, None: input.TagsNone})
	if err != nil {
		return tag.ResolvedFilter{}, err
	}
	resolve := func(keys []string) ([]string, error) {
		if len(keys) == 0 {
			return nil, nil
		}
		rows, err := s.Store.Pool.Query(ctx, `
			SELECT id::text FROM asset.tags
			WHERE organization_id = $1::uuid AND workspace_id = $2::uuid AND normalized_key = ANY($3::text[])
		`, principal.OrganizationID, workspaceID, keys)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		ids := []string{}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return nil, err
			}
			ids = append(ids, id)
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
		if len(ids) != len(keys) {
			return nil, ErrUnknownTagKey
		}
		return ids, nil
	}
	anyIDs, err := resolve(normalized.Any)
	if err != nil {
		return tag.ResolvedFilter{}, err
	}
	allIDs, err := resolve(normalized.All)
	if err != nil {
		return tag.ResolvedFilter{}, err
	}
	noneIDs, err := resolve(normalized.None)
	if err != nil {
		return tag.ResolvedFilter{}, err
	}
	return tag.ResolvedFilter{Any: anyIDs, All: allIDs, None: noneIDs}, nil
}
