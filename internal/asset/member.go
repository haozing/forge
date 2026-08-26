package asset

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"agentchunzhi/internal/auth"
	"agentchunzhi/internal/authz"
	"agentchunzhi/internal/eventing"
	"agentchunzhi/internal/resourcemodel"
	"agentchunzhi/internal/retrieval"
	"agentchunzhi/internal/store"

	"github.com/jackc/pgx/v5"
)

type MemberService struct {
	Store  *store.Store
	Events eventing.EventStore
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
	Tags              []string
	Sort              string
	Limit             int
	Cursor            string
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
	Markdown        *string        `json:"markdown"`
	Fields          map[string]any `json:"fields"`
	Tags            []string       `json:"tags"`
	Source          map[string]any `json:"source"`
	ContainerIDs    []string       `json:"container_ids"`
	ParentAssetID   *string        `json:"parent_asset_id"`
}

type MemberAssetPatch struct {
	Title         *string         `json:"title"`
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
	Tags                      []string       `json:"tags"`
	Source                    map[string]any `json:"source"`
	Visibility                string         `json:"visibility"`
	PublicationStatus         string         `json:"publication_status"`
	ReviewStatus              string         `json:"review_status"`
	Quality                   string         `json:"quality"`
	CurrentWorkingVersionID   string         `json:"current_working_version_id"`
	CurrentPublishedVersionID *string        `json:"current_published_version_id"`
	ContainerIDs              []string       `json:"container_ids"`
	ParentAssetID             *string        `json:"parent_asset_id"`
	CreatedBy                 Actor          `json:"created_by"`
	UpdatedAt                 time.Time      `json:"updated_at"`
	ETag                      string         `json:"etag"`
	sortValue                 string
}

type ReviewSubmission struct {
	ReviewID       string `json:"review_id"`
	AssetID        string `json:"asset_id"`
	AssetVersionID string `json:"asset_version_id"`
	Status         string `json:"status"`
}

func (s MemberService) require(ctx context.Context, principal auth.Principal, workspaceID, modelID, action string) (authz.Scope, error) {
	if principal.UserType != "member" || s.Store == nil || s.Store.Pool == nil {
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

func (s MemberService) ListPage(ctx context.Context, principal auth.Principal, workspaceID string, input MemberAssetListInput) (MemberAssetPage, error) {
	if !validID(workspaceID) || (input.ResourceModelID != "" && !validID(input.ResourceModelID)) ||
		(input.ContainerID != "" && !validID(input.ContainerID)) || (input.ParentAssetID != "" && !validID(input.ParentAssetID)) {
		return MemberAssetPage{}, ErrInvalidInput
	}
	if !validMemberAssetListEnums(input) {
		return MemberAssetPage{}, ErrInvalidInput
	}
	scope, err := s.require(ctx, principal, workspaceID, input.ResourceModelID, "asset.read")
	if err != nil {
		return MemberAssetPage{}, err
	}
	limit := input.Limit
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	input.Query = strings.TrimSpace(input.Query)
	sortSpec, err := normalizeMemberAssetSort(input.Sort)
	if err != nil {
		return MemberAssetPage{}, err
	}
	cursor, err := decodeAssetCursor(input.Cursor, sortSpec.Name)
	if err != nil {
		return MemberAssetPage{}, err
	}
	fieldFilters, tagFilters, err := normalizeMemberFilters(input.Filters, input.Tags)
	if err != nil {
		return MemberAssetPage{}, err
	}
	if err := s.validateMemberFilterFields(ctx, principal.OrganizationID, workspaceID, input.ResourceModelID, fieldFilters); err != nil {
		return MemberAssetPage{}, err
	}
	querySQL := `
		SELECT a.id::text, a.workspace_id::text, a.resource_model_id::text, rm.content_kind, v.resource_model_version_id::text,
		       v.title, COALESCE(left(v.markdown, 240), ''), v.markdown, v.fields, v.tags, v.source,
		       a.visibility, a.publication_status, v.review_status, v.quality,
		       a.current_working_version_id::text, a.current_published_version_id,
		       u.id::text, u.display_name, a.updated_at,
		       COALESCE((SELECT jsonb_agg(ca.container_id::text ORDER BY ca.container_id) FROM content.container_assets ca WHERE ca.organization_id = a.organization_id AND ca.workspace_id = a.workspace_id AND ca.asset_id = a.id), '[]'::jsonb),
		       (SELECT dp.parent_asset_id::text FROM content.document_parents dp WHERE dp.organization_id = a.organization_id AND dp.workspace_id = a.workspace_id AND dp.child_asset_id = a.id),
		       ` + sortSpec.Expression + `
		FROM asset.assets a
		JOIN asset.asset_versions v ON v.id = a.current_working_version_id
		JOIN identity.users u ON u.id = a.created_by
		JOIN model.resource_models rm ON rm.id = a.resource_model_id
		WHERE a.organization_id = $1::uuid AND a.workspace_id = $2::uuid AND a.deleted_at IS NULL
		  AND ($3 = '' OR a.resource_model_id = NULLIF($3, '')::uuid)
		  AND ($4 = '' OR v.title ILIKE '%' || $4 || '%' OR v.markdown ILIKE '%' || $4 || '%')
		  AND ($5 = '' OR a.visibility = $5)
		  AND ($6 = '' OR a.publication_status = $6)
		  AND ($7 = '' OR v.review_status = $7)
		  AND ($8 = '' OR ($8 = 'me' AND a.created_by = $9::uuid) OR ($8 <> 'me' AND a.created_by = NULLIF($8, '')::uuid))
		  AND ($10 IN ('owner', 'admin') OR a.visibility <> 'private' OR a.created_by = $9::uuid)
		  AND ($11 = '' OR rm.content_kind = $11)
		  AND ($12 = '' OR EXISTS (SELECT 1 FROM content.container_assets ca WHERE ca.organization_id = a.organization_id AND ca.workspace_id = a.workspace_id AND ca.asset_id = a.id AND ca.container_id = NULLIF($12, '')::uuid))
		  AND ($13 = '' OR EXISTS (SELECT 1 FROM content.document_parents dp WHERE dp.organization_id = a.organization_id AND dp.workspace_id = a.workspace_id AND dp.child_asset_id = a.id AND dp.parent_asset_id = NULLIF($13, '')::uuid))
		  AND retrieval.matches_field_filters(v.fields, $14::jsonb)
		  AND retrieval.matches_field_filters(jsonb_build_object('tags', v.tags), $15::jsonb)
		  AND ($16 = false OR ` + sortSpec.CursorPredicate + `)
		ORDER BY ` + sortSpec.OrderBy + `
		LIMIT $20`
	rows, err := s.Store.Pool.Query(ctx, querySQL, principal.OrganizationID, workspaceID, input.ResourceModelID, input.Query, input.Visibility, input.PublicationStatus, input.ReviewStatus, input.CreatedBy, principal.UserID, scope.Role, input.ContentKind, input.ContainerID, input.ParentAssetID, string(fieldFilters), string(tagFilters), input.Cursor != "", cursor.UpdatedAt, cursor.Value, cursor.ID, limit+1)
	if err != nil {
		return MemberAssetPage{}, fmt.Errorf("list member assets: %w", err)
	}
	defer rows.Close()
	items := make([]MemberAsset, 0, limit+1)
	for rows.Next() {
		item, err := scanMemberAsset(rows)
		if err != nil {
			return MemberAssetPage{}, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return MemberAssetPage{}, fmt.Errorf("iterate member assets: %w", err)
	}
	page := MemberAssetPage{Items: items}
	if len(page.Items) > limit {
		page.HasMore = true
		page.Items = page.Items[:limit]
		last := page.Items[len(page.Items)-1]
		page.NextCursor = encodeAssetCursor(sortSpec.Name, last)
	}
	return page, nil
}

type memberAssetCursor struct {
	Sort      string `json:"sort"`
	UpdatedAt string `json:"updated_at,omitempty"`
	Value     string `json:"value,omitempty"`
	ID        string `json:"id"`
}

type memberAssetSort struct {
	Name            string
	Expression      string
	CursorPredicate string
	OrderBy         string
}

func normalizeMemberAssetSort(value string) (memberAssetSort, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	switch value {
	case "", "updated_at:desc", "updated_at_desc", "-updated_at":
		return memberAssetSort{Name: "updated_at:desc", Expression: "a.updated_at::text", CursorPredicate: "(a.updated_at, a.id) < (NULLIF($17, '')::timestamptz, NULLIF($19, '')::uuid) AND $18::text = $18::text", OrderBy: "a.updated_at DESC, a.id DESC"}, nil
	case "updated_at:asc", "updated_at_asc", "updated_at":
		return memberAssetSort{Name: "updated_at:asc", Expression: "a.updated_at::text", CursorPredicate: "(a.updated_at, a.id) > (NULLIF($17, '')::timestamptz, NULLIF($19, '')::uuid) AND $18::text = $18::text", OrderBy: "a.updated_at ASC, a.id ASC"}, nil
	case "title:asc", "title_asc", "title":
		return memberAssetSort{Name: "title:asc", Expression: "lower(COALESCE(v.title, ''))", CursorPredicate: "(lower(COALESCE(v.title, '')), a.id) > ($18::text, NULLIF($19, '')::uuid) AND NULLIF($17::text, '') IS NULL", OrderBy: "lower(COALESCE(v.title, '')) ASC, a.id ASC"}, nil
	case "title:desc", "title_desc", "-title":
		return memberAssetSort{Name: "title:desc", Expression: "lower(COALESCE(v.title, ''))", CursorPredicate: "(lower(COALESCE(v.title, '')), a.id) < ($18::text, NULLIF($19, '')::uuid) AND NULLIF($17::text, '') IS NULL", OrderBy: "lower(COALESCE(v.title, '')) DESC, a.id DESC"}, nil
	default:
		return memberAssetSort{}, ErrInvalidInput
	}
}

func encodeAssetCursor(sortName string, item MemberAsset) string {
	payload := memberAssetCursor{Sort: sortName, ID: item.ID, Value: item.sortValue}
	if strings.HasPrefix(sortName, "updated_at:") {
		payload.UpdatedAt = item.UpdatedAt.UTC().Format(time.RFC3339Nano)
	}
	raw, _ := json.Marshal(payload)
	return base64.RawURLEncoding.EncodeToString(raw)
}

func decodeAssetCursor(value, sortName string) (memberAssetCursor, error) {
	if strings.TrimSpace(value) == "" {
		return memberAssetCursor{}, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return memberAssetCursor{}, ErrInvalidInput
	}
	var payload memberAssetCursor
	if err := json.Unmarshal(raw, &payload); err != nil || !validID(payload.ID) || payload.Sort != sortName {
		return memberAssetCursor{}, ErrInvalidInput
	}
	if strings.HasPrefix(sortName, "updated_at:") {
		if _, err := time.Parse(time.RFC3339Nano, payload.UpdatedAt); err != nil {
			return memberAssetCursor{}, ErrInvalidInput
		}
	}
	return payload, nil
}

func (s MemberService) Get(ctx context.Context, principal auth.Principal, assetID string) (MemberAsset, error) {
	if !validID(assetID) {
		return MemberAsset{}, ErrInvalidInput
	}
	var workspaceID, modelID string
	err := s.Store.Pool.QueryRow(ctx, `SELECT COALESCE(workspace_id::text, ''), resource_model_id::text FROM asset.assets WHERE organization_id = $1::uuid AND id = $2::uuid`, principal.OrganizationID, assetID).Scan(&workspaceID, &modelID)
	if errors.Is(err, pgx.ErrNoRows) || workspaceID == "" {
		return MemberAsset{}, ErrNotFound
	}
	if err != nil {
		return MemberAsset{}, fmt.Errorf("load member asset scope: %w", err)
	}
	scope, err := s.require(ctx, principal, workspaceID, modelID, "asset.read")
	if err != nil {
		return MemberAsset{}, err
	}
	row := s.Store.Pool.QueryRow(ctx, `
		SELECT a.id::text, a.workspace_id::text, a.resource_model_id::text, rm.content_kind, v.resource_model_version_id::text,
		       v.title, COALESCE(left(v.markdown, 240), ''), v.markdown, v.fields, v.tags, v.source,
		       a.visibility, a.publication_status, v.review_status, v.quality,
		       a.current_working_version_id::text, a.current_published_version_id,
		       u.id::text, u.display_name, a.updated_at,
		       COALESCE((SELECT jsonb_agg(ca.container_id::text ORDER BY ca.container_id) FROM content.container_assets ca WHERE ca.organization_id = a.organization_id AND ca.workspace_id = a.workspace_id AND ca.asset_id = a.id), '[]'::jsonb),
		       (SELECT dp.parent_asset_id::text FROM content.document_parents dp WHERE dp.organization_id = a.organization_id AND dp.workspace_id = a.workspace_id AND dp.child_asset_id = a.id),
		       a.updated_at::text
		FROM asset.assets a JOIN asset.asset_versions v ON v.id = a.current_working_version_id JOIN identity.users u ON u.id = a.created_by JOIN model.resource_models rm ON rm.id = a.resource_model_id
		WHERE a.organization_id = $1::uuid AND a.id = $2::uuid AND a.workspace_id = $3::uuid AND a.deleted_at IS NULL
	`, principal.OrganizationID, assetID, workspaceID)
	item, err := scanMemberAsset(row)
	if err != nil {
		return MemberAsset{}, err
	}
	if item.Visibility == "private" && item.CreatedBy.ID != principal.UserID && scope.Role != "owner" && scope.Role != "admin" {
		return MemberAsset{}, ErrNotFound
	}
	return item, nil
}

func (s MemberService) Create(ctx context.Context, principal auth.Principal, workspaceID, idempotencyKey string, input MemberAssetInput) (MemberAsset, error) {
	if _, err := s.require(ctx, principal, workspaceID, input.ResourceModelID, "asset.write"); err != nil {
		return MemberAsset{}, err
	}
	if !validIdempotencyKey(idempotencyKey) || !validID(input.ResourceModelID) {
		return MemberAsset{}, ErrInvalidInput
	}
	input.Visibility = strings.TrimSpace(input.Visibility)
	if input.Fields == nil {
		input.Fields = map[string]any{}
	}
	if input.Tags == nil {
		input.Tags = []string{}
	}
	if input.Source == nil {
		input.Source = map[string]any{}
	}
	var modelVersionID, contentKind string
	var fieldSchema, formSchema, listSchema, policy []byte
	err := s.Store.Pool.QueryRow(ctx, `
		SELECT mv.id::text, rm.content_kind, mv.field_schema, mv.form_schema, mv.list_schema, mv.policy
		FROM model.resource_models rm JOIN model.resource_model_versions mv ON mv.id = rm.current_version_id
		WHERE rm.organization_id = $1::uuid AND rm.workspace_id = $2::uuid AND rm.id = $3::uuid AND rm.status = 'active' AND mv.status = 'published'
	`, principal.OrganizationID, workspaceID, input.ResourceModelID).Scan(&modelVersionID, &contentKind, &fieldSchema, &formSchema, &listSchema, &policy)
	if errors.Is(err, pgx.ErrNoRows) {
		return MemberAsset{}, ErrNotFound
	}
	if err != nil {
		return MemberAsset{}, fmt.Errorf("load member asset model version: %w", err)
	}
	input.Visibility, err = memberAssetVisibility(policy, input.Visibility)
	if err != nil {
		return MemberAsset{}, err
	}
	if err := resourcemodel.Validate(contentKind, decodeJSONMap(fieldSchema), decodeJSONMap(formSchema), decodeJSONMap(listSchema), decodeJSONMap(policy)); err != nil {
		return MemberAsset{}, err
	}
	if err := validateContent(input.Title, input.Markdown, &input.Fields); err != nil {
		return MemberAsset{}, err
	}
	if err := validateFields(fieldSchema, input.Fields); err != nil {
		return MemberAsset{}, err
	}
	checksum := hashRequest("member.asset.content", struct {
		Title    *string
		Markdown *string
		Fields   map[string]any
		Tags     []string
		Source   map[string]any
	}{input.Title, input.Markdown, input.Fields, input.Tags, input.Source})
	requestHash := hashRequest("member.asset.create", struct {
		WorkspaceID string
		Input       MemberAssetInput
	}{workspaceID, input})
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return MemberAsset{}, fmt.Errorf("begin member asset create: %w", err)
	}
	defer tx.Rollback(ctx)
	if err := validateAssetReferences(ctx, tx, principal, workspaceID, fieldSchema, input.Fields); err != nil {
		return MemberAsset{}, err
	}
	if body, replay, err := reserveMemberIdempotency(ctx, tx, principal, "member.asset.create", idempotencyKey, requestHash); err != nil {
		return MemberAsset{}, err
	} else if replay {
		var item MemberAsset
		if err := json.Unmarshal(body, &item); err != nil {
			return MemberAsset{}, fmt.Errorf("decode idempotent member asset response: %w", err)
		}
		return item, nil
	}
	var assetID, versionID string
	if err := tx.QueryRow(ctx, `INSERT INTO asset.assets (organization_id, workspace_id, resource_model_id, visibility, publication_status, created_by) VALUES ($1::uuid, $2::uuid, $3::uuid, $4, 'draft', $5::uuid) RETURNING id::text`, principal.OrganizationID, workspaceID, input.ResourceModelID, input.Visibility, principal.UserID).Scan(&assetID); err != nil {
		return MemberAsset{}, fmt.Errorf("create member asset: %w", err)
	}
	if err := tx.QueryRow(ctx, `INSERT INTO asset.asset_versions (organization_id, workspace_id, asset_id, resource_model_id, resource_model_version_id, version_no, workflow_status, quality, title, markdown, fields, tags, source, content_checksum, created_by) VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, $5::uuid, 1, 'draft', 'raw', $6, $7, $8::jsonb, $9::jsonb, $10::jsonb, $11, $12::uuid) RETURNING id::text`, principal.OrganizationID, workspaceID, assetID, input.ResourceModelID, modelVersionID, input.Title, input.Markdown, mustJSON(input.Fields), mustJSON(input.Tags), mustJSON(input.Source), checksum, principal.UserID).Scan(&versionID); err != nil {
		return MemberAsset{}, fmt.Errorf("create member asset version: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE asset.assets SET current_working_version_id = $2::uuid, updated_at = now() WHERE id = $1::uuid`, assetID, versionID); err != nil {
		return MemberAsset{}, fmt.Errorf("set member asset working version: %w", err)
	}
	if err := replaceMemberAssetRelationsTx(ctx, tx, principal, workspaceID, assetID, contentKind, input.ContainerIDs, input.ParentAssetID); err != nil {
		return MemberAsset{}, err
	}
	if err := retrieval.EnqueueProjectionTx(ctx, tx, s.Events, principal.OrganizationID, versionID, retrieval.ProjectionRebuild); err != nil {
		return MemberAsset{}, fmt.Errorf("enqueue member asset projection: %w", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO audit.audit_log (organization_id, actor_user_id, initiator_user_id, action, resource_type, resource_id, result, metadata) VALUES ($1::uuid, $2::uuid, $2::uuid, 'asset.create', 'asset', $3::uuid, 'allowed', jsonb_build_object('workspace_id', $4::text, 'principal_type', 'member'))`, principal.OrganizationID, principal.UserID, assetID, workspaceID); err != nil {
		return MemberAsset{}, fmt.Errorf("record member asset create audit: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return MemberAsset{}, fmt.Errorf("commit member asset create: %w", err)
	}
	result, err := s.Get(ctx, principal, assetID)
	if err != nil {
		return MemberAsset{}, err
	}
	if err := saveMemberIdempotency(ctx, s.Store, principal, "member.asset.create", idempotencyKey, result, httpCreated); err != nil {
		return MemberAsset{}, err
	}
	return result, nil
}

func (s MemberService) Update(ctx context.Context, principal auth.Principal, assetID, expectedVersionID, idempotencyKey string, input MemberAssetPatch) (MemberAsset, error) {
	if !validID(assetID) || !validID(expectedVersionID) || !validIdempotencyKey(idempotencyKey) {
		return MemberAsset{}, ErrInvalidInput
	}
	current, err := s.Get(ctx, principal, assetID)
	if err != nil {
		return MemberAsset{}, err
	}
	if _, err := s.require(ctx, principal, current.WorkspaceID, current.ResourceModelID, "asset.write"); err != nil {
		return MemberAsset{}, err
	}
	if current.CurrentWorkingVersionID != expectedVersionID && strings.Trim(expectedVersionID, "\"") != current.ETag {
		return MemberAsset{}, ErrConflict
	}
	title, markdown, fields, tags, source := current.Title, current.Markdown, current.Fields, current.Tags, current.Source
	containerIDs, parentAssetID := current.ContainerIDs, current.ParentAssetID
	visibility := current.Visibility
	if input.Title != nil {
		title = input.Title
	}
	if input.Markdown != nil {
		markdown = input.Markdown
	}
	if input.Fields != nil {
		fields = *input.Fields
	}
	if input.Tags != nil {
		tags = *input.Tags
	}
	if input.Source != nil {
		source = *input.Source
	}
	if input.Visibility != nil {
		visibility = strings.TrimSpace(*input.Visibility)
	}
	if input.ContainerIDs != nil {
		containerIDs = *input.ContainerIDs
	}
	if input.ParentAssetID.Set {
		parentAssetID = input.ParentAssetID.Value
	}
	var contentKind, modelVersionID string
	var fieldSchema, formSchema, listSchema, policy []byte
	if err := s.Store.Pool.QueryRow(ctx, `SELECT rm.content_kind, mv.id::text, mv.field_schema, mv.form_schema, mv.list_schema, mv.policy FROM model.resource_models rm JOIN model.resource_model_versions mv ON mv.id = rm.current_version_id WHERE rm.organization_id = $1::uuid AND rm.id = $2::uuid AND rm.status = 'active' AND mv.status = 'published'`, principal.OrganizationID, current.ResourceModelID).Scan(&contentKind, &modelVersionID, &fieldSchema, &formSchema, &listSchema, &policy); err != nil {
		return MemberAsset{}, fmt.Errorf("load member asset schema: %w", err)
	}
	visibility, err = memberAssetVisibility(policy, visibility)
	if err != nil {
		return MemberAsset{}, err
	}
	if err := resourcemodel.Validate(contentKind, decodeJSONMap(fieldSchema), decodeJSONMap(formSchema), decodeJSONMap(listSchema), decodeJSONMap(policy)); err != nil {
		return MemberAsset{}, err
	}
	if err := validateContent(title, markdown, &fields); err != nil {
		return MemberAsset{}, err
	}
	if err := validateFields(fieldSchema, fields); err != nil {
		return MemberAsset{}, err
	}
	checksum := hashRequest("member.asset.content", struct {
		Title    *string
		Markdown *string
		Fields   map[string]any
		Tags     []string
		Source   map[string]any
	}{title, markdown, fields, tags, source})
	requestHash := hashRequest("member.asset.update", struct {
		AssetID         string
		ExpectedVersion string
		Input           MemberAssetPatch
	}{assetID, expectedVersionID, input})
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return MemberAsset{}, fmt.Errorf("begin member asset update: %w", err)
	}
	defer tx.Rollback(ctx)
	if err := validateAssetReferences(ctx, tx, principal, current.WorkspaceID, fieldSchema, fields); err != nil {
		return MemberAsset{}, err
	}
	if body, replay, err := reserveMemberIdempotency(ctx, tx, principal, "member.asset.update", idempotencyKey, requestHash); err != nil {
		return MemberAsset{}, err
	} else if replay {
		var item MemberAsset
		if err := json.Unmarshal(body, &item); err != nil {
			return MemberAsset{}, fmt.Errorf("decode idempotent member asset update response: %w", err)
		}
		return item, nil
	}
	var versionNo int
	if err := tx.QueryRow(ctx, `SELECT version_no FROM asset.asset_versions WHERE id = $1::uuid AND asset_id = $2::uuid FOR UPDATE`, expectedVersionID, assetID).Scan(&versionNo); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return MemberAsset{}, ErrConflict
		}
		return MemberAsset{}, fmt.Errorf("lock member asset version: %w", err)
	}
	var versionID string
	if err := tx.QueryRow(ctx, `INSERT INTO asset.asset_versions (organization_id, workspace_id, asset_id, resource_model_id, resource_model_version_id, version_no, workflow_status, quality, title, markdown, fields, tags, source, parent_version_id, content_checksum, created_by) VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, $5::uuid, $6, 'draft', 'raw', $7, $8, $9::jsonb, $10::jsonb, $11::jsonb, $12::uuid, $13, $14::uuid) RETURNING id::text`, principal.OrganizationID, current.WorkspaceID, assetID, current.ResourceModelID, modelVersionID, versionNo+1, title, markdown, mustJSON(fields), mustJSON(tags), mustJSON(source), expectedVersionID, checksum, principal.UserID).Scan(&versionID); err != nil {
		return MemberAsset{}, fmt.Errorf("create member asset revision: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE asset.assets SET current_working_version_id = $2::uuid, visibility = $3, updated_at = now() WHERE id = $1::uuid`, assetID, versionID, visibility); err != nil {
		return MemberAsset{}, fmt.Errorf("set member asset revision: %w", err)
	}
	if err := replaceMemberAssetRelationsTx(ctx, tx, principal, current.WorkspaceID, assetID, contentKind, containerIDs, parentAssetID); err != nil {
		return MemberAsset{}, err
	}
	if err := retrieval.EnqueueProjectionTx(ctx, tx, s.Events, principal.OrganizationID, versionID, retrieval.ProjectionRebuild); err != nil {
		return MemberAsset{}, fmt.Errorf("enqueue updated member asset projection: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE asset.asset_reviews SET status = 'superseded', reviewed_at = now() WHERE asset_version_id = $1::uuid AND status = 'pending'`, expectedVersionID); err != nil {
		return MemberAsset{}, fmt.Errorf("supersede member asset review: %w", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO audit.audit_log (organization_id, actor_user_id, initiator_user_id, action, resource_type, resource_id, result, metadata) VALUES ($1::uuid, $2::uuid, $2::uuid, 'asset.update', 'asset', $3::uuid, 'allowed', jsonb_build_object('workspace_id', $4::text, 'principal_type', 'member'))`, principal.OrganizationID, principal.UserID, assetID, current.WorkspaceID); err != nil {
		return MemberAsset{}, fmt.Errorf("record member asset update audit: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return MemberAsset{}, fmt.Errorf("commit member asset update: %w", err)
	}
	result, err := s.Get(ctx, principal, assetID)
	if err != nil {
		return MemberAsset{}, err
	}
	if err := saveMemberIdempotency(ctx, s.Store, principal, "member.asset.update", idempotencyKey, result, httpOK); err != nil {
		return MemberAsset{}, err
	}
	return result, nil
}

func (s MemberService) SubmitReview(ctx context.Context, principal auth.Principal, assetID, versionID, idempotencyKey string, comment string) (ReviewSubmission, error) {
	if !validID(assetID) || !validID(versionID) || !validIdempotencyKey(idempotencyKey) {
		return ReviewSubmission{}, ErrInvalidInput
	}
	current, err := s.Get(ctx, principal, assetID)
	if err != nil {
		return ReviewSubmission{}, err
	}
	if current.CurrentWorkingVersionID != versionID {
		return ReviewSubmission{}, ErrConflict
	}
	if _, err := s.require(ctx, principal, current.WorkspaceID, current.ResourceModelID, "asset.write"); err != nil {
		return ReviewSubmission{}, err
	}
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return ReviewSubmission{}, fmt.Errorf("begin review submission: %w", err)
	}
	defer tx.Rollback(ctx)
	var reviewID string
	err = tx.QueryRow(ctx, `INSERT INTO asset.asset_reviews (organization_id, workspace_id, asset_id, asset_version_id, status, submitted_by, comment) VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, 'pending', $5::uuid, $6) RETURNING id::text`, principal.OrganizationID, current.WorkspaceID, assetID, versionID, principal.UserID, strings.TrimSpace(comment)).Scan(&reviewID)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "duplicate") {
			return ReviewSubmission{}, ErrConflict
		}
		return ReviewSubmission{}, fmt.Errorf("create asset review: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE asset.asset_versions SET review_status = 'pending' WHERE id = $1::uuid`, versionID); err != nil {
		return ReviewSubmission{}, fmt.Errorf("mark asset review pending: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return ReviewSubmission{}, fmt.Errorf("commit review submission: %w", err)
	}
	return ReviewSubmission{ReviewID: reviewID, AssetID: assetID, AssetVersionID: versionID, Status: "pending"}, nil
}

func (s MemberService) Archive(ctx context.Context, principal auth.Principal, assetID, idempotencyKey string) (MemberAsset, error) {
	if !validID(assetID) || !validIdempotencyKey(idempotencyKey) {
		return MemberAsset{}, ErrInvalidInput
	}
	current, err := s.Get(ctx, principal, assetID)
	if err != nil {
		return MemberAsset{}, err
	}
	if _, err := s.require(ctx, principal, current.WorkspaceID, current.ResourceModelID, "asset.archive"); err != nil {
		return MemberAsset{}, err
	}
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return MemberAsset{}, fmt.Errorf("begin member asset archive: %w", err)
	}
	defer tx.Rollback(ctx)
	var previousPublishedID *string
	if err := tx.QueryRow(ctx, `SELECT current_published_version_id::text FROM asset.assets WHERE organization_id = $1::uuid AND id = $2::uuid FOR UPDATE`, principal.OrganizationID, assetID).Scan(&previousPublishedID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return MemberAsset{}, ErrNotFound
		}
		return MemberAsset{}, fmt.Errorf("lock member asset for archive: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE asset.assets SET publication_status = 'archived', current_published_version_id = NULL, updated_at = now() WHERE organization_id = $1::uuid AND id = $2::uuid`, principal.OrganizationID, assetID); err != nil {
		return MemberAsset{}, fmt.Errorf("archive member asset: %w", err)
	}
	if previousPublishedID != nil {
		if err := retrieval.EnqueueProjectionTx(ctx, tx, s.Events, principal.OrganizationID, *previousPublishedID, retrieval.ProjectionDelete); err != nil {
			return MemberAsset{}, fmt.Errorf("enqueue archived member asset projection deletion: %w", err)
		}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO audit.audit_log (organization_id, actor_user_id, initiator_user_id, action, resource_type, resource_id, result, metadata) VALUES ($1::uuid, $2::uuid, $2::uuid, 'asset.archive', 'asset', $3::uuid, 'allowed', jsonb_build_object('workspace_id', $4::text, 'principal_type', 'member'))`, principal.OrganizationID, principal.UserID, assetID, current.WorkspaceID); err != nil {
		return MemberAsset{}, fmt.Errorf("record member asset archive audit: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return MemberAsset{}, fmt.Errorf("commit member asset archive: %w", err)
	}
	return s.Get(ctx, principal, assetID)
}

func (s MemberService) Publish(ctx context.Context, principal auth.Principal, assetID, versionID, idempotencyKey string) (MemberAsset, error) {
	if !validID(assetID) || !validID(versionID) || !validIdempotencyKey(idempotencyKey) {
		return MemberAsset{}, ErrInvalidInput
	}
	current, err := s.Get(ctx, principal, assetID)
	if err != nil {
		return MemberAsset{}, err
	}
	if current.CurrentWorkingVersionID != versionID {
		return MemberAsset{}, ErrConflict
	}
	if _, err := s.require(ctx, principal, current.WorkspaceID, current.ResourceModelID, "asset.publish"); err != nil {
		return MemberAsset{}, err
	}
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return MemberAsset{}, fmt.Errorf("begin member asset publish: %w", err)
	}
	defer tx.Rollback(ctx)
	var hasUnsafeAttachments bool
	if err := tx.QueryRow(ctx, `
		WITH RECURSIVE version_lineage(id) AS (
			SELECT $2::uuid
			UNION ALL
			SELECT av.parent_version_id
			FROM asset.asset_versions av
			JOIN version_lineage child ON av.id = child.id
			WHERE av.parent_version_id IS NOT NULL
		)
		SELECT EXISTS (
			SELECT 1
			FROM asset.attachments at
			WHERE at.organization_id = $1::uuid
			  AND at.deleted_at IS NULL
			  AND at.scan_status <> 'clean'
			  AND (
				at.asset_version_id IN (SELECT id FROM version_lineage)
				OR EXISTS (
					SELECT 1 FROM asset.attachment_links al
					WHERE al.attachment_id = at.id
					  AND al.asset_version_id IN (SELECT id FROM version_lineage)
				)
			  )
		)
	`, principal.OrganizationID, versionID).Scan(&hasUnsafeAttachments); err != nil {
		return MemberAsset{}, fmt.Errorf("check member attachment scan status: %w", err)
	}
	if hasUnsafeAttachments {
		return MemberAsset{}, fmt.Errorf("%w: all attachments must be clean before publish", ErrConflict)
	}
	previousPublishedID := current.CurrentPublishedVersionID
	if _, err := tx.Exec(ctx, `UPDATE asset.assets SET current_published_version_id = $2::uuid, publication_status = 'published', updated_at = now() WHERE organization_id = $1::uuid AND id = $3::uuid`, principal.OrganizationID, versionID, assetID); err != nil {
		return MemberAsset{}, fmt.Errorf("publish member asset: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE asset.asset_versions SET quality = 'human_confirmed' WHERE id = $1::uuid`, versionID); err != nil {
		return MemberAsset{}, fmt.Errorf("mark member asset quality: %w", err)
	}
	if previousPublishedID != nil && *previousPublishedID != versionID {
		if err := retrieval.EnqueueProjectionTx(ctx, tx, s.Events, principal.OrganizationID, *previousPublishedID, retrieval.ProjectionDelete); err != nil {
			return MemberAsset{}, fmt.Errorf("enqueue previous member asset projection deletion: %w", err)
		}
	}
	if err := retrieval.EnqueueProjectionTx(ctx, tx, s.Events, principal.OrganizationID, versionID, retrieval.ProjectionRebuild); err != nil {
		return MemberAsset{}, fmt.Errorf("enqueue published member asset projection: %w", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO audit.audit_log (organization_id, actor_user_id, initiator_user_id, action, resource_type, resource_id, result, metadata) VALUES ($1::uuid, $2::uuid, $2::uuid, 'asset.publish', 'asset', $3::uuid, 'allowed', jsonb_build_object('workspace_id', $4::text, 'principal_type', 'member', 'review_required', false))`, principal.OrganizationID, principal.UserID, assetID, current.WorkspaceID); err != nil {
		return MemberAsset{}, fmt.Errorf("record member asset publish audit: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return MemberAsset{}, fmt.Errorf("commit member asset publish: %w", err)
	}
	return s.Get(ctx, principal, assetID)
}

func scanMemberAsset(row interface{ Scan(...any) error }) (MemberAsset, error) {
	var item MemberAsset
	var fields, tags, source, containerIDs []byte
	err := row.Scan(&item.ID, &item.WorkspaceID, &item.ResourceModelID, &item.ContentKind, &item.ResourceModelVersionID, &item.Title, &item.Summary, &item.Markdown, &fields, &tags, &source, &item.Visibility, &item.PublicationStatus, &item.ReviewStatus, &item.Quality, &item.CurrentWorkingVersionID, &item.CurrentPublishedVersionID, &item.CreatedBy.ID, &item.CreatedBy.DisplayName, &item.UpdatedAt, &containerIDs, &item.ParentAssetID, &item.sortValue)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return MemberAsset{}, ErrNotFound
		}
		return MemberAsset{}, fmt.Errorf("scan member asset: %w", err)
	}
	item.Fields = decodeJSONMap(fields)
	item.Source = decodeJSONMap(source)
	item.Tags = decodeStringSlice(tags)
	item.ContainerIDs = decodeStringSlice(containerIDs)
	item.ETag = item.CurrentWorkingVersionID
	return item, nil
}

func decodeJSONMap(raw []byte) map[string]any {
	result := map[string]any{}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &result)
	}
	return result
}
func decodeStringSlice(raw []byte) []string {
	result := []string{}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &result)
	}
	return result
}

func normalizeMemberFilters(filters map[string]any, directTags []string) ([]byte, []byte, error) {
	for key := range filters {
		if key != "fields" && key != "tags" {
			return nil, nil, ErrInvalidInput
		}
	}
	fieldPredicates := make([]map[string]any, 0)
	if rawFields, ok := filters["fields"]; ok {
		fields, ok := rawFields.(map[string]any)
		if !ok || len(fields) > 20 {
			return nil, nil, ErrInvalidInput
		}
		for field, rawOperations := range fields {
			predicates, err := normalizeAssetPredicate(field, rawOperations, false)
			if err != nil {
				return nil, nil, err
			}
			fieldPredicates = append(fieldPredicates, predicates...)
		}
	}
	tagPredicates := make([]map[string]any, 0)
	if rawTags, ok := filters["tags"]; ok {
		predicates, err := normalizeAssetPredicate("tags", rawTags, true)
		if err != nil {
			return nil, nil, err
		}
		tagPredicates = append(tagPredicates, predicates...)
	}
	if len(directTags) > 0 {
		if len(directTags) > 100 {
			return nil, nil, ErrInvalidInput
		}
		values := make([]string, 0, len(directTags))
		for _, raw := range directTags {
			value := strings.TrimSpace(raw)
			if value == "" || len([]rune(value)) > 100 {
				return nil, nil, ErrInvalidInput
			}
			values = append(values, value)
		}
		tagPredicates = append(tagPredicates, map[string]any{"field": "tags", "operator": "contains_any", "value": values})
	}
	if len(fieldPredicates) > 40 || len(tagPredicates) > 8 {
		return nil, nil, ErrInvalidInput
	}
	return mustJSON(fieldPredicates), mustJSON(tagPredicates), nil
}

func normalizeAssetPredicate(field string, rawOperations any, tags bool) ([]map[string]any, error) {
	field = strings.TrimSpace(field)
	operations, ok := rawOperations.(map[string]any)
	if field == "" || len(field) > 100 || !ok || len(operations) == 0 || len(operations) > 8 {
		return nil, ErrInvalidInput
	}
	result := make([]map[string]any, 0, len(operations))
	for operator, value := range operations {
		if operator != "eq" && operator != "neq" && operator != "in" && operator != "contains" && operator != "contains_any" && operator != "gte" && operator != "lte" && operator != "exists" {
			return nil, ErrInvalidInput
		}
		if tags && operator != "eq" && operator != "neq" && operator != "in" && operator != "contains" && operator != "contains_any" && operator != "exists" {
			return nil, ErrInvalidInput
		}
		if operator == "in" || operator == "contains_any" {
			if count, ok := filterArrayLength(value); !ok || count < 1 || count > 100 {
				return nil, ErrInvalidInput
			}
		}
		if (operator == "gte" || operator == "lte") && !assetFilterComparable(value) {
			return nil, ErrInvalidInput
		}
		if operator == "exists" {
			if _, ok := value.(bool); !ok {
				return nil, ErrInvalidInput
			}
		}
		if tags && !validTagFilterValue(operator, value) {
			return nil, ErrInvalidInput
		}
		result = append(result, map[string]any{"field": field, "operator": operator, "value": value})
	}
	return result, nil
}

func filterArrayLength(value any) (int, bool) {
	switch values := value.(type) {
	case []any:
		return len(values), true
	case []string:
		return len(values), true
	default:
		return 0, false
	}
}

func assetFilterComparable(value any) bool {
	if _, ok := value.(string); ok {
		return true
	}
	_, ok := numericValue(value)
	return ok
}

func validTagFilterValue(operator string, value any) bool {
	if operator == "exists" {
		_, ok := value.(bool)
		return ok
	}
	if operator == "in" || operator == "contains_any" {
		switch values := value.(type) {
		case []any:
			for _, item := range values {
				if _, ok := item.(string); !ok {
					return false
				}
			}
			return true
		case []string:
			return true
		default:
			return false
		}
	}
	_, ok := value.(string)
	return ok
}

func validMemberAssetListEnums(input MemberAssetListInput) bool {
	if input.Visibility != "" && input.Visibility != "public" && input.Visibility != "login" && input.Visibility != "private" && input.Visibility != "workspace" && input.Visibility != "internal" {
		return false
	}
	if input.PublicationStatus != "" && input.PublicationStatus != "draft" && input.PublicationStatus != "published" && input.PublicationStatus != "archived" {
		return false
	}
	if input.ReviewStatus != "" && input.ReviewStatus != "none" && input.ReviewStatus != "pending" && input.ReviewStatus != "approved" && input.ReviewStatus != "rejected" && input.ReviewStatus != "superseded" {
		return false
	}
	if input.ContentKind != "" && input.ContentKind != "record" && input.ContentKind != "document" && input.ContentKind != "faq" && input.ContentKind != "note" {
		return false
	}
	return input.CreatedBy == "" || input.CreatedBy == "me" || validID(input.CreatedBy)
}

func (s MemberService) validateMemberFilterFields(ctx context.Context, organizationID, workspaceID, resourceModelID string, raw []byte) error {
	var predicates []struct {
		Field string `json:"field"`
	}
	if err := json.Unmarshal(raw, &predicates); err != nil {
		return ErrInvalidInput
	}
	for _, predicate := range predicates {
		var allowed bool
		if err := s.Store.Pool.QueryRow(ctx, `
			SELECT count(*) > 0 AND COALESCE(bool_and(
				COALESCE(mv.field_schema->'properties', '{}'::jsonb) ? $4
				OR EXISTS (
					SELECT 1 FROM jsonb_array_elements(COALESCE(mv.field_schema->'fields', '[]'::jsonb)) field
					WHERE field->>'key' = $4
				)
			), false)
			FROM model.resource_models rm
			JOIN model.resource_model_versions mv ON mv.id = rm.current_version_id AND mv.status = 'published'
			WHERE rm.organization_id = $1::uuid AND rm.workspace_id = $2::uuid AND rm.status = 'active'
			  AND ($3 = '' OR rm.id = NULLIF($3, '')::uuid)
		`, organizationID, workspaceID, resourceModelID, predicate.Field).Scan(&allowed); err != nil {
			return fmt.Errorf("validate member asset filter schema: %w", err)
		}
		if !allowed {
			return ErrInvalidInput
		}
	}
	return nil
}

func reserveMemberIdempotency(ctx context.Context, tx pgx.Tx, principal auth.Principal, operation, key, requestHash string) ([]byte, bool, error) {
	var id string
	err := tx.QueryRow(ctx, `
		INSERT INTO system.idempotency_keys
			(organization_id, subject_id, operation, idempotency_key, request_hash, expires_at)
		VALUES ($1::uuid, $2::uuid, $3, $4, $5, now() + interval '24 hours')
		ON CONFLICT (organization_id, subject_id, operation, idempotency_key) DO NOTHING
		RETURNING id::text
	`, principal.OrganizationID, principal.UserID, operation, key, requestHash).Scan(&id)
	if err == nil {
		return nil, false, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, false, fmt.Errorf("reserve member idempotency key: %w", err)
	}
	var storedHash string
	var body []byte
	if err := tx.QueryRow(ctx, `
		SELECT request_hash, response_body
		FROM system.idempotency_keys
		WHERE organization_id = $1::uuid AND subject_id = $2::uuid
		  AND operation = $3 AND idempotency_key = $4
		FOR UPDATE
	`, principal.OrganizationID, principal.UserID, operation, key).Scan(&storedHash, &body); err != nil {
		return nil, false, fmt.Errorf("load member idempotency key: %w", err)
	}
	if storedHash != requestHash {
		return nil, false, ErrConflict
	}
	if len(body) == 0 {
		return nil, false, ErrConflict
	}
	return body, true, nil
}

func saveMemberIdempotency(ctx context.Context, db *store.Store, principal auth.Principal, operation, key string, response any, status int) error {
	body, err := json.Marshal(response)
	if err != nil {
		return fmt.Errorf("encode member idempotent response: %w", err)
	}
	if _, err := db.Pool.Exec(ctx, `
		UPDATE system.idempotency_keys
		SET response_status = $5, response_body = $6::jsonb
		WHERE organization_id = $1::uuid AND subject_id = $2::uuid
		  AND operation = $3 AND idempotency_key = $4
	`, principal.OrganizationID, principal.UserID, operation, key, status, string(body)); err != nil {
		return fmt.Errorf("save member idempotent response: %w", err)
	}
	return nil
}
