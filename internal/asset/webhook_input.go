package asset

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"agentchunzhi/internal/auth"
	"agentchunzhi/internal/authz"
	"agentchunzhi/internal/retrieval"
	"agentchunzhi/internal/store"

	"github.com/jackc/pgx/v5"
)

// Webhook input channel sentinels. They wrap the package-level sentinel errors
// so handlers can keep mapping ErrInvalidInput/ErrForbidden/ErrNotFound while
// still surfacing a specific error code for the two resolution failures.
var (
	ErrWebhookWorkspaceUnresolved = fmt.Errorf("%w: webhook target workspace unresolved", ErrInvalidInput)
	ErrWebhookDefaultModelMissing = fmt.Errorf("%w: workspace has no default resource model", ErrInvalidInput)
)

const webhookCreateOperation = "asset.webhook.create"

// WebhookAssetInput carries an inbound webhook asset push after handler-level
// authentication. WorkspaceID/ResourceModelID hold the target resolved by
// TransferService.ResolveWebhookTarget.
type WebhookAssetInput struct {
	WorkspaceID     string
	ResourceModelID string
	ExternalRef     string
	Title           *string
	Markdown        *string
	Fields          map[string]any
	Tags            []string
	ReceivedAt      time.Time
}

// WebhookTarget is the resolved (workspace, resource model) pair an inbound
// webhook posts into.
type WebhookTarget struct {
	WorkspaceID     string
	ResourceModelID string
}

// ResolveWebhookTarget decides where a webhook push lands. An explicit
// resource_model_id must exist and be either in the caller's allowed model
// scope or covered by its workspace access policy (403 otherwise). Without one,
// the workspace default model is used; when no workspace was supplied, the
// agent's access policies must point at exactly one workspace.
func (s TransferService) ResolveWebhookTarget(ctx context.Context, principal auth.Principal, requestedWorkspaceID, requestedModelID string) (WebhookTarget, error) {
	if s.Store == nil || s.Store.Pool == nil || s.Policy == nil {
		return WebhookTarget{}, ErrForbidden
	}
	requestedWorkspaceID = strings.TrimSpace(requestedWorkspaceID)
	requestedModelID = strings.TrimSpace(requestedModelID)
	if requestedModelID != "" {
		if !validID(requestedModelID) {
			return WebhookTarget{}, ErrInvalidInput
		}
		var modelWorkspace string
		if err := s.Store.Pool.QueryRow(ctx, `SELECT workspace_id::text FROM model.resource_models WHERE organization_id = $1::uuid AND id = $2::uuid`, principal.OrganizationID, requestedModelID).Scan(&modelWorkspace); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return WebhookTarget{}, ErrNotFound
			}
			return WebhookTarget{}, fmt.Errorf("load webhook resource model: %w", err)
		}
		if requestedWorkspaceID != "" && requestedWorkspaceID != modelWorkspace {
			return WebhookTarget{}, ErrInvalidInput
		}
		if err := authorizeWebhookModelAccess(ctx, s.Policy, s.Store, principal, modelWorkspace, requestedModelID); err != nil {
			return WebhookTarget{}, err
		}
		return WebhookTarget{WorkspaceID: modelWorkspace, ResourceModelID: requestedModelID}, nil
	}
	workspaceID := requestedWorkspaceID
	if workspaceID == "" {
		var candidates []string
		rows, err := s.Store.Pool.Query(ctx, `
			SELECT DISTINCT ap.workspace_id::text
			FROM content.agent_access_policies ap
			WHERE ap.organization_id = $1::uuid AND ap.agent_user_id = $2::uuid
			  AND ap.workspace_id IS NOT NULL
		`, principal.OrganizationID, principal.UserID)
		if err != nil {
			return WebhookTarget{}, fmt.Errorf("resolve webhook workspace candidates: %w", err)
		}
		for rows.Next() {
			var candidate string
			if err := rows.Scan(&candidate); err != nil {
				rows.Close()
				return WebhookTarget{}, fmt.Errorf("scan webhook workspace candidate: %w", err)
			}
			candidates = append(candidates, candidate)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return WebhookTarget{}, err
		}
		switch len(candidates) {
		case 1:
			workspaceID = candidates[0]
		case 0:
			return WebhookTarget{}, ErrWebhookWorkspaceUnresolved
		default:
			return WebhookTarget{}, fmt.Errorf("%w: agent has policies on %d workspaces, pass workspace_id", ErrInvalidInput, len(candidates))
		}
	}
	if !validID(workspaceID) {
		return WebhookTarget{}, ErrInvalidInput
	}
	// NOTE: no workspace-level policy precheck here -- admin-managed agent
	// policies are organization-scoped and invisible to Require(); the
	// default-model authorization below is the effective gate.
	var defaultModelID string
	if err := s.Store.Pool.QueryRow(ctx, `
		SELECT COALESCE(default_resource_model_id::text, '')
		FROM content.workspaces
		WHERE organization_id = $1::uuid AND id = $2::uuid AND status = 'active'
	`, principal.OrganizationID, workspaceID).Scan(&defaultModelID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return WebhookTarget{}, ErrForbidden
		}
		return WebhookTarget{}, fmt.Errorf("load webhook default model: %w", err)
	}
	if defaultModelID == "" {
		return WebhookTarget{}, ErrWebhookDefaultModelMissing
	}
	if err := authorizeWebhookModelAccess(ctx, s.Policy, s.Store, principal, workspaceID, defaultModelID); err != nil {
		return WebhookTarget{}, err
	}
	return WebhookTarget{WorkspaceID: workspaceID, ResourceModelID: defaultModelID}, nil
}

func authorizeWebhookModelAccess(ctx context.Context, policy authz.WorkspacePolicy, store *store.Store, principal auth.Principal, workspaceID, modelID string) error {
	_, policyErr := policy.Require(ctx, principal, workspaceID, modelID, "asset.create")
	if policyErr == nil {
		return nil
	}
	if !errors.Is(policyErr, authz.ErrWorkspaceForbidden) && !errors.Is(policyErr, authz.ErrWorkspaceNotFound) {
		return policyErr
	}
	// Admin-managed agent policies are organization-scoped (workspace_id NULL).
	// The per-workspace policy above cannot see them, so fall back to the same
	// org-level action check the open API uses before denying the push.
	if store != nil && store.Pool != nil {
		var ok bool
		if err := store.Pool.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM content.agent_access_policies ap
				WHERE ap.organization_id = $1::uuid
				  AND ap.agent_user_id = $2::uuid
				  AND 'create' = ANY (ap.actions)
				  AND (ap.resource_model_id IS NULL OR ap.resource_model_id = $3::uuid)
			)
		`, principal.OrganizationID, principal.UserID, modelID).Scan(&ok); err == nil && ok {
			return nil
		}
	}
	return ErrForbidden
}

// WebhookAssetInput carries the resolved webhook envelope. CreateFromWebhook
// walks the same service pipeline as Service.Create (idempotency reservation,
// raw input record, field validation, version insert, projection enqueue) but
// marks provenance via asset_versions.source and stores external_ref replays.
// The caller must have authorized input.ResourceModelID beforehand; it is
// passed as the sole scoped model to reuse Create's scoping invariants.
func (s Service) CreateFromWebhook(ctx context.Context, principal auth.Principal, input WebhookAssetInput) (AssetResult, bool, error) {
	replay := AssetResult{}
	resourceModelID := strings.TrimSpace(input.ResourceModelID)
	idempotencyKey := strings.TrimSpace(input.ExternalRef)
	if idempotencyKey != "" && !validIdempotencyKey(idempotencyKey) {
		return replay, false, ErrInvalidInput
	}
	// Without an external_ref each push is its own request; a random key keeps
	// Create's replay machinery satisfied without enabling accidental dedup.
	if idempotencyKey == "" {
		key, err := newWebhookIdempotencyKey()
		if err != nil {
			return replay, false, fmt.Errorf("generate webhook idempotency key: %w", err)
		}
		idempotencyKey = key
	}
	if !validID(resourceModelID) || len(allowedModelsFor(resourceModelID)) == 0 {
		return replay, false, ErrInvalidInput
	}
	if err := validateContent(input.Title, input.Markdown, &input.Fields); err != nil {
		return replay, false, err
	}
	receivedAt := input.ReceivedAt
	if receivedAt.IsZero() {
		receivedAt = time.Now()
	}
	versionSource := BuildWebhookVersionSource(input.ExternalRef, receivedAt)
	if s.Store == nil || s.Store.Pool == nil {
		return replay, false, errors.New("database store is not initialized")
	}
	envelope := struct {
		Title    *string        `json:"title"`
		Markdown *string        `json:"markdown"`
		Fields   map[string]any `json:"fields"`
		Tags     []string       `json:"tags"`
	}{input.Title, input.Markdown, input.Fields, input.Tags}
	requestHash := hashRequest(webhookCreateOperation, envelope)
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return replay, false, fmt.Errorf("begin webhook asset create: %w", err)
	}
	defer tx.Rollback(ctx)
	state, err := beginIdempotency(ctx, tx, principal, webhookCreateOperation, idempotencyKey, requestHash)
	if err != nil {
		return replay, false, err
	}
	if state.Replay {
		if err := json.Unmarshal(state.Body, &replay); err != nil {
			return replay, false, fmt.Errorf("decode idempotent webhook response: %w", err)
		}
		return replay, true, nil
	}
	fields := input.Fields
	if fields == nil {
		fields = map[string]any{}
	}
	contentChecksum := hashRequest("asset.content", struct {
		Title    *string        `json:"title"`
		Markdown *string        `json:"markdown"`
		Fields   map[string]any `json:"fields"`
	}{input.Title, input.Markdown, fields})
	sourcePayload, _ := json.Marshal(envelope)
	var rawInputID string
	if err := tx.QueryRow(ctx, `
		INSERT INTO asset.raw_inputs
			(organization_id, submitted_by, source_type, content_type, payload, content_checksum)
		VALUES ($1::uuid, $2::uuid, 'api', 'application/json', $3::jsonb, $4)
		RETURNING id::text
	`, principal.OrganizationID, principal.UserID, string(sourcePayload), contentChecksum).Scan(&rawInputID); err != nil {
		return replay, false, fmt.Errorf("record webhook raw input: %w", err)
	}
	models := allowedModelsFor(resourceModelID)
	var modelVersionID, workspaceID string
	var fieldSchema []byte
	if err := tx.QueryRow(ctx, `
		SELECT mv.id::text, mv.field_schema, rm.workspace_id::text
		FROM model.resource_models rm
		JOIN model.resource_model_versions mv ON mv.id = rm.current_version_id
		WHERE rm.id = $1::uuid
		  AND rm.organization_id = $2::uuid
		  AND rm.id::text = ANY($3::text[])
		  AND rm.status = 'active'
	`, resourceModelID, principal.OrganizationID, models).Scan(&modelVersionID, &fieldSchema, &workspaceID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return replay, false, ErrNotFound
		}
		return replay, false, fmt.Errorf("load webhook resource model version: %w", err)
	}
	if input.WorkspaceID != "" && input.WorkspaceID != workspaceID {
		return replay, false, ErrForbidden
	}
	if err := validateFields(fieldSchema, fields); err != nil {
		return replay, false, err
	}
	if err := validateAssetReferences(ctx, tx, principal, workspaceID, fieldSchema, fields); err != nil {
		return replay, false, err
	}
	tagsArg := "[]"
	if len(input.Tags) > 0 {
		tagsArg = string(mustJSON(input.Tags))
	}
	versionSourceArg := string(mustJSON(versionSource))
	if len(versionSourceArg) == 0 {
		versionSourceArg = "{}"
	}
	var assetID, versionID string
	if err := tx.QueryRow(ctx, `
		INSERT INTO asset.assets (organization_id, workspace_id, resource_model_id, created_by)
		VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid)
		RETURNING id::text
	`, principal.OrganizationID, workspaceID, resourceModelID, principal.UserID).Scan(&assetID); err != nil {
		return replay, false, fmt.Errorf("create webhook asset: %w", err)
	}
	if err := tx.QueryRow(ctx, `
		INSERT INTO asset.asset_versions
			(organization_id, workspace_id, asset_id, resource_model_id, resource_model_version_id, version_no,
			 workflow_status, quality, title, markdown, fields, source_raw_input_id, content_checksum, created_by,
			 tags, source)
		VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, $5::uuid, 1, 'draft', 'raw', $6, $7, $8::jsonb, $9::uuid, $10, $11::uuid, $12::jsonb, $13::jsonb)
		RETURNING id::text
	`, principal.OrganizationID, workspaceID, assetID, resourceModelID, modelVersionID, input.Title, input.Markdown, string(mustJSON(fields)), rawInputID, contentChecksum, principal.UserID, tagsArg, versionSourceArg).Scan(&versionID); err != nil {
		return replay, false, fmt.Errorf("create webhook asset version: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE asset.assets
		SET current_working_version_id = $2::uuid, updated_at = now()
		WHERE id = $1::uuid
	`, assetID, versionID); err != nil {
		return replay, false, fmt.Errorf("set webhook working version: %w", err)
	}
	if err := retrieval.EnqueueProjectionTx(ctx, tx, s.Events, principal.OrganizationID, versionID, retrieval.ProjectionRebuild); err != nil {
		return replay, false, fmt.Errorf("enqueue webhook projection: %w", err)
	}
	result, err := loadAssetTx(ctx, tx, assetID)
	if err != nil {
		return replay, false, err
	}
	if err := saveIdempotency(ctx, tx, principal, webhookCreateOperation, idempotencyKey, result, httpCreated); err != nil {
		return replay, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return replay, false, fmt.Errorf("commit webhook asset create: %w", err)
	}
	return result, false, nil
}

func allowedModelsFor(resourceModelID string) []string {
	if !validID(resourceModelID) {
		return nil
	}
	return []string{resourceModelID}
}

// BuildWebhookVersionSource renders the asset_versions.source marker documenting
// that this version arrived through the webhook channel.
func BuildWebhookVersionSource(externalRef string, receivedAt time.Time) map[string]any {
	source := map[string]any{
		"channel":     "webhook",
		"received_at": receivedAt.UTC().Format(time.RFC3339Nano),
	}
	if ref := strings.TrimSpace(externalRef); ref != "" {
		source["external_ref"] = ref
	}
	return source
}

func newWebhookIdempotencyKey() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}
