package asset

// preparation.go — phase 4 suggestion stream for the asset_prepare workflow.
// The graph returns an extraction; this service re-checks permissions, then
// lands the output as a pending suggestion set (fields/summary/tags/relations)
// plus one auditable agent_processing_results row inside a single
// transaction. Nothing else moves: no version is created, the working pointer
// stays put, drafts are untouched and pending publication requests survive
// (doc §2 D1/D2). Members accept or reject suggestions; commit materializes.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"agentchunzhi/internal/auth"
	"agentchunzhi/internal/authz"
	"agentchunzhi/internal/eventing"
	"agentchunzhi/internal/store"
	"agentchunzhi/internal/tag"
	"agentchunzhi/internal/workflows"

	"github.com/jackc/pgx/v5"
)

var ErrPreparationPermissionRevoked = errors.New("asset preparation permission has been revoked")

// defaultFieldConfidence is used when the model did not report a confidence
// for a suggested field (mirrors the extractor default).
const defaultFieldConfidence = 0.5

// RelationCandidateSource supplies RAG-backed relation candidates; agents
// never touch the database or retrieval projections directly (doc §8.2).
type RelationCandidateSource interface {
	Candidates(ctx context.Context, principal auth.Principal, workspaceID, queryText string, limit int) ([]workflows.RelationCandidate, error)
}

type PrepareRequest struct {
	OrganizationID     string
	WorkspaceID        string
	AgentUserID        string
	AgentApplicationID string
	ModelEndpointID    string
	ModelRevision      int64
	RunID              string
	AssetVersionID     string
}

type PrepareResult struct {
	ProcessingResultID string
	Counts             map[string]int // field/summary/tag/relation
	InputTokens        int
	OutputTokens       int
}

type AssetPreparationService struct {
	Store       *store.Store
	Events      eventing.EventStore
	Permissions authz.ScopeResolver
	Workflow    workflows.Runnable
	// Relations is the retrieval-backed whitelist source; nil (or a retrieval
	// error) is fail-closed — no relation suggestions at all (doc §11.1).
	Relations RelationCandidateSource
	// Tags records tag suggestions through the tag domain service; when its
	// store is not wired the tag slot is skipped instead of failing the run.
	Tags tag.SuggestionService
}

func (s AssetPreparationService) Prepare(ctx context.Context, req PrepareRequest) (PrepareResult, error) {
	if s.Store == nil || s.Store.Pool == nil || s.Workflow == nil || strings.TrimSpace(req.AssetVersionID) == "" ||
		strings.TrimSpace(req.RunID) == "" ||
		strings.TrimSpace(req.AgentUserID) == "" || strings.TrimSpace(req.AgentApplicationID) == "" ||
		strings.TrimSpace(req.ModelEndpointID) == "" || req.ModelRevision <= 0 {
		return PrepareResult{}, errors.New("asset preparation service is not initialized")
	}
	metadata, err := s.loadSource(ctx, req)
	if err != nil {
		return PrepareResult{}, err
	}
	principal := auth.Principal{OrganizationID: metadata.OrganizationID, UserID: req.AgentUserID, UserType: "agent"}
	if err := s.checkPermission(ctx, principal, metadata.ResourceModelID); err != nil {
		return PrepareResult{}, err
	}
	relationCandidates := s.relationCandidates(ctx, principal, metadata)
	values := cloneMap(metadata.Fields)
	output, err := s.Workflow.Invoke(ctx, workflows.Input{
		OrganizationID:     metadata.OrganizationID,
		WorkspaceID:        metadata.WorkspaceID,
		RunID:              req.RunID,
		AgentApplicationID: req.AgentApplicationID,
		ModelEndpointID:    req.ModelEndpointID,
		ModelRevision:      req.ModelRevision,
		AssetIDs:           []string{metadata.AssetID},
		Title:              pointerValue(metadata.Title),
		Markdown:           pointerValue(metadata.Markdown),
		FieldSchema:        append(json.RawMessage(nil), metadata.FieldSchema...),
		Values:             values,
		ExistingSummary:    metadata.Summary,
		TagCandidates:      metadata.TagCandidates,
		SourceTags:         metadata.SourceTags,
		RelationCandidates: relationCandidates,
	})
	if err != nil {
		return PrepareResult{}, fmt.Errorf("execute asset_prepare graph: %w", err)
	}
	fields := cloneMap(output.Candidate)
	delete(fields, "asset_ids")
	if err := ValidateFields(metadata.FieldSchema, fields); err != nil {
		return PrepareResult{}, fmt.Errorf("validate asset preparation fields: %w", err)
	}
	if err := ValidateContent(metadata.Title, metadata.Markdown, &fields); err != nil {
		return PrepareResult{}, fmt.Errorf("validate asset preparation content: %w", err)
	}
	// Re-check the permission after the model call so a policy revoked while
	// the graph ran cannot land a suggestion.
	if err := s.checkPermission(ctx, principal, metadata.ResourceModelID); err != nil {
		return PrepareResult{}, err
	}
	result, err := s.persistSuggestions(ctx, req, metadata, output, fields, relationCandidates)
	if err != nil {
		return PrepareResult{}, err
	}
	result.InputTokens = output.InputTokens
	result.OutputTokens = output.OutputTokens
	return result, nil
}

type preparationMetadata struct {
	OrganizationID  string
	WorkspaceID     string
	AssetID         string
	ResourceModelID string
	ModelVersionID  string
	VersionNo       int
	Title           *string
	Markdown        *string
	Summary         string
	Fields          map[string]any
	FieldSchema     []byte
	// TagCandidates is the workspace's active tag vocabulary; SourceTags are
	// the tags already on the source version (preserve semantics).
	TagCandidates []workflows.TagCandidate
	SourceTags    []workflows.TagCandidate
	Sealed        bool
}

func (s AssetPreparationService) loadSource(ctx context.Context, req PrepareRequest) (preparationMetadata, error) {
	var metadata preparationMetadata
	var fields []byte
	err := s.Store.Pool.QueryRow(ctx, `
		SELECT av.organization_id::text, a.workspace_id::text, av.asset_id::text,
		       av.resource_model_id::text, av.resource_model_version_id::text,
		       av.version_no, av.title, av.markdown, av.summary, av.fields, mv.field_schema,
		       (av.sealed_at IS NOT NULL)
		FROM asset.asset_versions av
		JOIN asset.assets a ON a.organization_id = av.organization_id AND a.id = av.asset_id
		JOIN model.resource_model_versions mv ON mv.id = av.resource_model_version_id
		JOIN integration.agent_applications aa ON aa.id = $5::uuid
		  AND aa.organization_id = av.organization_id
		  AND aa.bound_agent_user_id = $2::uuid
		  AND aa.model_endpoint_id = $6::uuid
		  AND aa.status = 'active' AND aa.runtime_mode = 'workflow' AND aa.workflow_key = 'asset_prepare'
		JOIN integration.model_endpoints me ON me.id = aa.model_endpoint_id AND me.organization_id = aa.organization_id AND me.status = 'active'
		JOIN integration.model_endpoint_revisions mer ON mer.model_endpoint_id = me.id AND mer.revision = $7
		  AND mer.revoked_at IS NULL
		WHERE av.id = $1::uuid AND av.organization_id = $3::uuid AND a.workspace_id = $4::uuid
	`, req.AssetVersionID, req.AgentUserID, req.OrganizationID, req.WorkspaceID,
		req.AgentApplicationID, req.ModelEndpointID, req.ModelRevision).Scan(
		&metadata.OrganizationID, &metadata.WorkspaceID, &metadata.AssetID,
		&metadata.ResourceModelID, &metadata.ModelVersionID, &metadata.VersionNo, &metadata.Title,
		&metadata.Markdown, &metadata.Summary, &fields, &metadata.FieldSchema, &metadata.Sealed)
	if errors.Is(err, pgx.ErrNoRows) {
		return preparationMetadata{}, ErrNotFound
	}
	if err != nil {
		return preparationMetadata{}, fmt.Errorf("load asset preparation source: %w", err)
	}
	if !metadata.Sealed {
		return preparationMetadata{}, fmt.Errorf("%w: preparation source version is not sealed", ErrConflict)
	}
	metadata.Fields = map[string]any{}
	if len(fields) > 0 {
		_ = json.Unmarshal(fields, &metadata.Fields)
	}
	metadata.SourceTags, err = s.loadVersionTagCandidates(ctx, metadata.OrganizationID, req.AssetVersionID)
	if err != nil {
		return preparationMetadata{}, err
	}
	metadata.TagCandidates, err = s.loadWorkspaceTagCandidates(ctx, metadata.OrganizationID, metadata.WorkspaceID)
	if err != nil {
		return preparationMetadata{}, err
	}
	return metadata, nil
}

// loadVersionTagCandidates reads the tag set carried by the source version so
// the prompt can state what already exists (preserve semantics).
func (s AssetPreparationService) loadVersionTagCandidates(ctx context.Context, organizationID, versionID string) ([]workflows.TagCandidate, error) {
	rows, err := s.Store.Pool.Query(ctx, `
		SELECT t.normalized_key, t.display_name
		FROM asset.asset_version_tags avt
		JOIN asset.tags t ON t.organization_id = avt.organization_id AND t.id = avt.tag_id
		WHERE avt.organization_id = $1::uuid AND avt.asset_version_id = $2::uuid
		ORDER BY t.normalized_key
	`, organizationID, versionID)
	if err != nil {
		return nil, fmt.Errorf("load source version tags: %w", err)
	}
	defer rows.Close()
	return collectTagCandidates(rows)
}

// loadWorkspaceTagCandidates reads the constrained tag vocabulary injected
// into the extraction prompt (doc §2 D5).
func (s AssetPreparationService) loadWorkspaceTagCandidates(ctx context.Context, organizationID, workspaceID string) ([]workflows.TagCandidate, error) {
	rows, err := s.Store.Pool.Query(ctx, `
		SELECT normalized_key, display_name
		FROM asset.tags
		WHERE organization_id = $1::uuid AND workspace_id = $2::uuid AND status = 'active'
		ORDER BY normalized_key
		LIMIT 200
	`, organizationID, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("load workspace tag candidates: %w", err)
	}
	defer rows.Close()
	return collectTagCandidates(rows)
}

func collectTagCandidates(rows pgx.Rows) ([]workflows.TagCandidate, error) {
	candidates := make([]workflows.TagCandidate, 0, 16)
	for rows.Next() {
		var candidate workflows.TagCandidate
		if err := rows.Scan(&candidate.Key, &candidate.DisplayName); err != nil {
			return nil, fmt.Errorf("scan tag candidate: %w", err)
		}
		candidates = append(candidates, candidate)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate tag candidates: %w", err)
	}
	return candidates, nil
}

// relationCandidates builds the relatable asset whitelist from retrieval
// (doc §11.1): the source title plus the first 500 characters of its summary,
// top 20 hits, the asset itself excluded. Fail-closed and quiet — a missing
// source or a retrieval error yields no candidates at all.
func (s AssetPreparationService) relationCandidates(ctx context.Context, principal auth.Principal, metadata preparationMetadata) []workflows.RelationCandidate {
	if s.Relations == nil {
		return nil
	}
	var query strings.Builder
	query.WriteString(strings.TrimSpace(pointerValue(metadata.Title)))
	if summary := strings.TrimSpace(metadata.Summary); summary != "" {
		if runes := []rune(summary); len(runes) > 500 {
			summary = string(runes[:500])
		}
		query.WriteString("\n")
		query.WriteString(summary)
	}
	if strings.TrimSpace(query.String()) == "" {
		return nil
	}
	candidates, err := s.Relations.Candidates(ctx, principal, metadata.WorkspaceID, query.String(), 20)
	if err != nil {
		return nil
	}
	filtered := make([]workflows.RelationCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.AssetID == metadata.AssetID {
			continue
		}
		filtered = append(filtered, candidate)
	}
	return filtered
}

func (s AssetPreparationService) checkPermission(ctx context.Context, principal auth.Principal, modelID string) error {
	if s.Permissions.Store == nil {
		return ErrPreparationPermissionRevoked
	}
	for _, action := range []string{"asset.read", "asset.edit"} {
		allowed, err := s.Permissions.AllowedModelIDs(ctx, principal, action)
		if err != nil {
			return fmt.Errorf("check asset preparation permission: %w", err)
		}
		if !containsString(allowed, modelID) {
			return ErrPreparationPermissionRevoked
		}
	}
	return nil
}

// persistSuggestions lands the whole suggestion set in one transaction:
// processing result first, then field/summary/tag/relation suggestions, then
// the result's summary/diff views, audit entry and outbox fact. Run retries
// are idempotent through the (run, …) unique keys; member decisions on
// accepted rows are never overwritten. No pointer, draft or version moves.
func (s AssetPreparationService) persistSuggestions(ctx context.Context, req PrepareRequest, metadata preparationMetadata, output workflows.Output, fields map[string]any, relationCandidates []workflows.RelationCandidate) (PrepareResult, error) {
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return PrepareResult{}, err
	}
	defer tx.Rollback(ctx)
	row, err := LoadLifecycleTx(ctx, tx, metadata.OrganizationID, metadata.AssetID)
	if err != nil {
		return PrepareResult{}, err
	}
	if row.PublicationStatus == PublicationArchived {
		return PrepareResult{}, ErrAssetArchived
	}
	actor := auth.Principal{OrganizationID: metadata.OrganizationID, UserID: req.AgentUserID, UserType: "agent"}

	// The processing result is the auditable envelope of the run (doc §2 D4).
	// rule_version stays empty until the workflow runnable exposes its
	// definition checksum.
	var resultID string
	if err := tx.QueryRow(ctx, `
		INSERT INTO integration.agent_processing_results
			(organization_id, workspace_id, run_id, asset_id, input_version_id,
			 agent_user_id, agent_application_id, rule_version,
			 input_tokens, output_tokens, completed_at)
		VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, $5::uuid, $6::uuid, $7::uuid, $8, $9, $10, now())
		ON CONFLICT (run_id, asset_id) DO UPDATE SET
			input_tokens = EXCLUDED.input_tokens,
			output_tokens = EXCLUDED.output_tokens,
			completed_at = now()
		RETURNING id::text
	`, metadata.OrganizationID, metadata.WorkspaceID, req.RunID, metadata.AssetID, req.AssetVersionID,
		req.AgentUserID, req.AgentApplicationID, "", output.InputTokens, output.OutputTokens).Scan(&resultID); err != nil {
		return PrepareResult{}, fmt.Errorf("insert agent processing result: %w", err)
	}
	citation := extractionCitation(req.AssetVersionID, pointerValue(metadata.Title))

	fieldCount := 0
	fieldDiff := make([]map[string]any, 0, len(fields))
	confidences := make([]float64, 0, len(fields)+len(output.Tags)+len(output.Relations)+1)
	for _, key := range sortedKeys(fields) {
		encoded, err := json.Marshal(fields[key])
		if err != nil {
			return PrepareResult{}, fmt.Errorf("encode suggested field %q: %w", key, err)
		}
		var previousValue any
		if existing, ok := metadata.Fields[key]; ok {
			encodedPrevious, err := json.Marshal(existing)
			if err != nil {
				return PrepareResult{}, fmt.Errorf("encode previous field %q: %w", key, err)
			}
			previousValue = encodedPrevious
		}
		confidence := defaultFieldConfidence
		if value, ok := output.FieldConfidence[key]; ok {
			confidence = value
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO asset.asset_field_suggestions
				(organization_id, workspace_id, source_version_id, run_id, kind, field_key,
				 value, previous_value, confidence, citation)
			VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, 'field', $5, $6::jsonb, $7::jsonb, $8, $9::jsonb)
			ON CONFLICT (source_version_id, run_id, kind, field_key) DO UPDATE SET
				value = EXCLUDED.value,
				previous_value = EXCLUDED.previous_value,
				confidence = EXCLUDED.confidence,
				citation = EXCLUDED.citation
			WHERE asset.asset_field_suggestions.status = 'pending'
		`, metadata.OrganizationID, metadata.WorkspaceID, req.AssetVersionID, req.RunID,
			key, encoded, previousValue, confidence, citation); err != nil {
			return PrepareResult{}, fmt.Errorf("insert field suggestion %q: %w", key, err)
		}
		fieldCount++
		confidences = append(confidences, confidence)
		fieldDiff = append(fieldDiff, map[string]any{
			"key": key, "old": rawOrNull(previousValue), "new": json.RawMessage(encoded), "confidence": confidence,
		})
	}

	summaryCount := 0
	if output.Summary != nil && *output.Summary != metadata.Summary {
		encoded, err := json.Marshal(*output.Summary)
		if err != nil {
			return PrepareResult{}, fmt.Errorf("encode suggested summary: %w", err)
		}
		var previousValue any
		if metadata.Summary != "" {
			encodedPrevious, err := json.Marshal(metadata.Summary)
			if err != nil {
				return PrepareResult{}, fmt.Errorf("encode previous summary: %w", err)
			}
			previousValue = encodedPrevious
		}
		confidence := defaultFieldConfidence
		if value, ok := output.FieldConfidence["summary"]; ok {
			confidence = value
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO asset.asset_field_suggestions
				(organization_id, workspace_id, source_version_id, run_id, kind, field_key,
				 value, previous_value, confidence, citation)
			VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, 'summary', '', $5::jsonb, $6::jsonb, $7, $8::jsonb)
			ON CONFLICT (source_version_id, run_id, kind, field_key) DO UPDATE SET
				value = EXCLUDED.value,
				previous_value = EXCLUDED.previous_value,
				confidence = EXCLUDED.confidence,
				citation = EXCLUDED.citation
			WHERE asset.asset_field_suggestions.status = 'pending'
		`, metadata.OrganizationID, metadata.WorkspaceID, req.AssetVersionID, req.RunID,
			encoded, previousValue, confidence, citation); err != nil {
			return PrepareResult{}, fmt.Errorf("insert summary suggestion: %w", err)
		}
		summaryCount = 1
		confidences = append(confidences, confidence)
	}

	tagCount := 0
	if s.Tags.Store != nil {
		for _, suggested := range output.Tags {
			if _, err := s.Tags.CreatePendingTx(ctx, tx, metadata.OrganizationID, metadata.WorkspaceID,
				req.AssetVersionID, suggested.Key, suggested.DisplayName, suggested.Confidence,
				req.RunID, req.AgentApplicationID, citation, suggested.IsNew); err != nil {
				return PrepareResult{}, fmt.Errorf("insert tag suggestion %q: %w", suggested.Key, err)
			}
			tagCount++
			confidences = append(confidences, suggested.Confidence)
		}
	}

	relationSnippets := make(map[string]string, len(relationCandidates))
	for _, candidate := range relationCandidates {
		relationSnippets[candidate.AssetID] = candidate.Snippet
	}
	relationCount := 0
	for _, relation := range output.Relations {
		relationCitation := retrievalCitation(relation.TargetAssetID, relationSnippets[relation.TargetAssetID])
		if _, err := tx.Exec(ctx, `
			INSERT INTO asset.asset_relation_suggestions
				(organization_id, workspace_id, source_version_id, run_id, target_asset_id,
				 relation_type, confidence, citation)
			VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, $5::uuid, $6, $7, $8::jsonb)
			ON CONFLICT (source_version_id, run_id, target_asset_id, relation_type) DO UPDATE SET
				confidence = EXCLUDED.confidence,
				citation = EXCLUDED.citation
			WHERE asset.asset_relation_suggestions.status = 'pending'
		`, metadata.OrganizationID, metadata.WorkspaceID, req.AssetVersionID, req.RunID,
			relation.TargetAssetID, relation.RelationType, relation.Confidence, relationCitation); err != nil {
			return PrepareResult{}, fmt.Errorf("insert relation suggestion %q: %w", relation.TargetAssetID, err)
		}
		relationCount++
		confidences = append(confidences, relation.Confidence)
	}

	counts := map[string]int{"field": fieldCount, "summary": summaryCount, "tag": tagCount, "relation": relationCount}
	summaryJSON, err := json.Marshal(counts)
	if err != nil {
		return PrepareResult{}, fmt.Errorf("encode suggestion summary: %w", err)
	}
	diffJSON, err := json.Marshal(fieldDiff)
	if err != nil {
		return PrepareResult{}, fmt.Errorf("encode field diff: %w", err)
	}
	var overallConfidence any
	if len(confidences) > 0 {
		total := 0.0
		for _, confidence := range confidences {
			total += confidence
		}
		overallConfidence = total / float64(len(confidences))
	}
	if _, err := tx.Exec(ctx, `
		UPDATE integration.agent_processing_results
		SET suggestion_summary = $2::jsonb, field_diff = $3::jsonb, overall_confidence = $4
		WHERE id = $1::uuid
	`, resultID, summaryJSON, diffJSON, overallConfidence); err != nil {
		return PrepareResult{}, fmt.Errorf("update agent processing result views: %w", err)
	}
	RecordAssetAuditTx(ctx, tx, metadata.OrganizationID, metadata.WorkspaceID, actor, "asset.version.prepare_suggested", metadata.AssetID, map[string]any{
		"workspace_id":         metadata.WorkspaceID,
		"source_version_id":    req.AssetVersionID,
		"processing_result_id": resultID,
		"counts":               counts,
	})
	if err := AppendAssetEventTx(ctx, tx, &s.Events, row, actor, eventing.EventAgentProcessingCompleted, eventing.PayloadVersionV1, eventing.AgentProcessingCompletedPayload{
		AssetID:            metadata.AssetID,
		RunID:              req.RunID,
		InputVersionID:     req.AssetVersionID,
		ProcessingResultID: resultID,
		Counts:             counts,
	}); err != nil {
		return PrepareResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return PrepareResult{}, err
	}
	return PrepareResult{ProcessingResultID: resultID, Counts: counts}, nil
}

// extractionCitation cites the sealed input version plus the title snippet the
// model actually saw.
func extractionCitation(versionID, title string) []byte {
	runes := []rune(strings.TrimSpace(title))
	if len(runes) > 80 {
		runes = runes[:80]
	}
	return mustMarshalCitation([]map[string]any{{"kind": "extraction", "ref": versionID, "snippet": string(runes)}})
}

// retrievalCitation cites the retrieval hit that whitelisted the relation
// target (doc §11.1: 命中片段即 citation).
func retrievalCitation(targetAssetID, snippet string) []byte {
	runes := []rune(strings.TrimSpace(snippet))
	if len(runes) > 300 {
		runes = runes[:300]
	}
	return mustMarshalCitation([]map[string]any{{"kind": "retrieval", "ref": targetAssetID, "snippet": string(runes)}})
}

func mustMarshalCitation(value any) []byte {
	encoded, err := json.Marshal(value)
	if err != nil {
		return []byte("[]")
	}
	return encoded
}

// rawOrNull renders a previous_value parameter as raw JSON for field_diff.
func rawOrNull(value any) json.RawMessage {
	if encoded, ok := value.([]byte); ok && len(encoded) > 0 {
		return json.RawMessage(encoded)
	}
	return json.RawMessage("null")
}

func sortedKeys(values map[string]any) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func cloneMap(input map[string]any) map[string]any {
	result := make(map[string]any, len(input))
	for key, value := range input {
		result[key] = value
	}
	return result
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func pointerValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
