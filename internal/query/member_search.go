package query

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"agentchunzhi/internal/auth"
	"agentchunzhi/internal/vectorvalue"

	"github.com/jackc/pgx/v5"
)

type MemberQueryRequest struct {
	WorkspaceID       string
	Mode              string
	Query             string
	ModelIDs          []string
	Visibility        []string
	PublicationStatus []string
	TopK              int
	Cursor            string
	Filters           map[string]any
}

func (s Service) QueryMember(ctx context.Context, principal auth.Principal, req MemberQueryRequest) (QueryResponse, error) {
	if principal.UserType != "member" || !ValidUUID(principal.OrganizationID) || !ValidUUID(principal.UserID) || !ValidUUID(req.WorkspaceID) {
		return QueryResponse{}, ErrModelAccessDenied
	}
	q := strings.TrimSpace(req.Query)
	mode := strings.ToLower(strings.TrimSpace(req.Mode))
	if mode == "" {
		mode = "hybrid"
	}
	if mode != "lexical" && mode != "semantic" && mode != "hybrid" {
		return QueryResponse{}, fmt.Errorf("%w: unsupported member query mode", ErrInvalidQuery)
	}
	if q == "" || len([]rune(q)) > 500 || strings.ContainsRune(q, '\x00') {
		return QueryResponse{}, fmt.Errorf("%w: query must be 1-500 characters", ErrInvalidQuery)
	}
	topK := req.TopK
	if topK == 0 {
		topK = 20
	}
	if topK < 1 || topK > 100 {
		return QueryResponse{}, fmt.Errorf("%w: top_k must be 1-100", ErrInvalidQuery)
	}
	if len(req.ModelIDs) > 100 {
		return QueryResponse{}, fmt.Errorf("%w: resource_model_ids must contain at most 100 entries", ErrInvalidQuery)
	}
	for _, value := range req.ModelIDs {
		if !ValidUUID(value) {
			return QueryResponse{}, fmt.Errorf("%w: resource_model_ids must be UUIDs", ErrInvalidQuery)
		}
	}
	if err := validateMemberQueryEnums(req.Visibility, req.PublicationStatus); err != nil {
		return QueryResponse{}, err
	}
	if s.Store == nil || s.Store.Pool == nil {
		return QueryResponse{}, errors.New("database store is not initialized")
	}
	role, models, err := s.memberQueryScope(ctx, principal, req.WorkspaceID, req.ModelIDs)
	if err != nil {
		return QueryResponse{}, err
	}
	filters, err := parseFilterPlan(req.Filters)
	if err != nil {
		return QueryResponse{}, err
	}
	if err := s.validateFilterFields(ctx, principal.OrganizationID, models, filters); err != nil {
		return QueryResponse{}, err
	}
	policyRevision, err := s.policyRevision(ctx, principal.OrganizationID)
	if err != nil {
		return QueryResponse{}, err
	}
	projectionFingerprint, err := s.projectionFingerprint(ctx, principal.OrganizationID, models)
	if err != nil {
		return QueryResponse{}, err
	}
	req.Mode = mode
	if req.Cursor != "" {
		return s.pageMemberSession(ctx, principal, req, role, q, filters, nil, models, topK, false, mode, policyRevision, projectionFingerprint)
	}

	var lexical, vector []candidate
	if mode == "lexical" || mode == "hybrid" {
		lexical, err = s.memberLexicalCandidates(ctx, principal, req, role, q, models, filters)
		if err != nil {
			return QueryResponse{}, err
		}
	}
	degraded := false
	if mode == "semantic" || mode == "hybrid" {
		vector, err = s.memberVectorCandidates(ctx, principal, req, role, q, models, filters)
		if err != nil {
			if mode == "semantic" {
				return QueryResponse{}, fmt.Errorf("%w: %v", ErrVectorUnavailable, err)
			}
			degraded = true
		}
	}
	mergeMode := mode
	if mode == "lexical" || degraded {
		mergeMode = "fulltext"
	}
	merged := mergeCandidates(lexical, vector, mergeMode)
	method := "lexical"
	if mode == "semantic" {
		method = "vector"
	} else if mode == "hybrid" && !degraded {
		method = "rrf"
	}
	if len(merged) > 0 && s.Reranker != nil {
		input := make([]RerankCandidate, min(50, len(merged)))
		for i := range input {
			input[i] = RerankCandidate{ID: merged[i].ChunkID, Text: merged[i].Snippet}
		}
		scores, rerankErr := s.Reranker.Rerank(ctx, q, input)
		if rerankErr != nil || len(scores) != len(input) {
			degraded = true
		} else {
			for i, score := range scores {
				merged[i].RerankScore = score
				merged[i].FinalScore = score
				merged[i].Method = "rerank"
			}
			sort.SliceStable(merged, func(i, j int) bool {
				if merged[i].FinalScore != merged[j].FinalScore {
					return merged[i].FinalScore > merged[j].FinalScore
				}
				return merged[i].ChunkID < merged[j].ChunkID
			})
			method = "rerank"
		}
	}
	merged = deduplicateMemberAssets(merged)
	return s.pageMemberSession(ctx, principal, req, role, q, filters, merged, models, topK, degraded, method, policyRevision, projectionFingerprint)
}

func validateMemberQueryEnums(visibility, publication []string) error {
	if len(visibility) > 5 || len(publication) > 3 {
		return fmt.Errorf("%w: too many enum filters", ErrInvalidQuery)
	}
	for _, value := range visibility {
		if value != "public" && value != "login" && value != "private" && value != "workspace" && value != "internal" {
			return fmt.Errorf("%w: invalid visibility", ErrInvalidQuery)
		}
	}
	for _, value := range publication {
		if value != "draft" && value != "published" && value != "archived" {
			return fmt.Errorf("%w: invalid publication_status", ErrInvalidQuery)
		}
	}
	return nil
}

func (s Service) memberQueryScope(ctx context.Context, principal auth.Principal, workspaceID string, requested []string) (string, []string, error) {
	var role string
	err := s.Store.Pool.QueryRow(ctx, `
		SELECT wm.role
		FROM content.workspace_members wm
		JOIN content.workspaces w ON w.id = wm.workspace_id AND w.organization_id = wm.organization_id
		WHERE wm.organization_id = $1::uuid AND wm.workspace_id = $2::uuid
		  AND wm.user_id = $3::uuid AND w.status = 'active'
	`, principal.OrganizationID, workspaceID, principal.UserID).Scan(&role)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil, ErrModelAccessDenied
	}
	if err != nil {
		return "", nil, fmt.Errorf("load member query workspace: %w", err)
	}
	rows, err := s.Store.Pool.Query(ctx, `
		SELECT rm.id::text
		FROM model.resource_models rm
		JOIN model.resource_model_versions mv ON mv.id = rm.current_version_id AND mv.status = 'published'
		WHERE rm.organization_id = $1::uuid AND rm.workspace_id = $2::uuid AND rm.status = 'active'
		  AND COALESCE(NULLIF(mv.policy #>> '{outlets,workspace,enabled}', '')::boolean, false)
		ORDER BY rm.id
	`, principal.OrganizationID, workspaceID)
	if err != nil {
		return "", nil, fmt.Errorf("load member query models: %w", err)
	}
	defer rows.Close()
	available := make([]string, 0)
	for rows.Next() {
		var modelID string
		if err := rows.Scan(&modelID); err != nil {
			return "", nil, err
		}
		available = append(available, modelID)
	}
	if err := rows.Err(); err != nil {
		return "", nil, err
	}
	if len(requested) == 0 {
		if len(available) == 0 {
			return "", nil, ErrModelAccessDenied
		}
		return role, available, nil
	}
	models := intersectModels(available, requested)
	if len(models) != len(uniqueStrings(requested)) {
		return "", nil, ErrModelAccessDenied
	}
	return role, models, nil
}

func (s Service) memberLexicalCandidates(ctx context.Context, principal auth.Principal, req MemberQueryRequest, role, q string, models []string, filters filterPlan) ([]candidate, error) {
	rows, err := s.Store.Pool.Query(ctx, `
		SELECT a.id::text, c.asset_version_id::text, c.id::text, c.projection_run_id::text,
		       pgroonga_score(c.tableoid, c.ctid), LEFT(regexp_replace(c.content,E'\\s+',' ','g'),500),
		       c.source_type,c.source_locator,c.char_start,c.char_end,
		       c.source_checksum,c.chunk_checksum,c.canonicalizer_version
		FROM retrieval.chunks c
		JOIN retrieval.projection_runs pr ON pr.id = c.projection_run_id AND pr.status = 'ready'
		JOIN retrieval.projection_configs pc ON pc.id = pr.projection_config_id
		 AND pc.status = 'active' AND pc.active_projection_generation = c.projection_generation
		 AND pc.chunker_version = c.chunker_version
		JOIN asset.assets a ON a.id = c.asset_id AND a.current_working_version_id = c.asset_version_id
		JOIN asset.asset_versions v ON v.id = c.asset_version_id
		JOIN model.resource_models rm ON rm.id = a.resource_model_id
		JOIN model.resource_model_versions mv ON mv.id = rm.current_version_id AND mv.status = 'published'
		WHERE c.organization_id = $1::uuid AND c.status = 'ready'
		  AND a.workspace_id = $2::uuid AND a.deleted_at IS NULL
		  AND a.resource_model_id = ANY($3::uuid[]) AND c.search_text &@~ $4
		  AND retrieval.matches_field_filters(v.fields, $5::jsonb)
		  AND retrieval.matches_field_filters(jsonb_build_object('tags', v.tags), $6::jsonb)
		  AND ($7::text IN ('owner','admin') OR a.visibility <> 'private' OR a.created_by = $8::uuid)
		  AND (cardinality($9::text[]) = 0 OR a.visibility = ANY($9::text[]))
		  AND (cardinality($10::text[]) = 0 OR a.publication_status = ANY($10::text[]))
		  AND COALESCE(NULLIF(mv.policy #>> '{outlets,workspace,enabled}', '')::boolean, false)
		  AND retrieval.quality_rank(v.quality) >= retrieval.quality_rank($11)
		ORDER BY pgroonga_score(c.tableoid,c.ctid) DESC, a.id, c.id
		LIMIT 200
	`, principal.OrganizationID, req.WorkspaceID, models, q, filters.fieldsJSON(), filters.tagsJSON(), role, principal.UserID, req.Visibility, req.PublicationStatus, filters.QualityGTE)
	if err != nil {
		return nil, fmt.Errorf("member lexical recall: %w", err)
	}
	defer rows.Close()
	out := make([]candidate, 0)
	for rows.Next() {
		var item candidate
		if err := rows.Scan(&item.AssetID, &item.AssetVersionID, &item.ChunkID, &item.ProjectionRunID, &item.LexicalScore, &item.Snippet, &item.SourceType, &item.Locator, &item.CharStart, &item.CharEnd, &item.SourceChecksum, &item.ChunkChecksum, &item.CanonicalizerVersion); err != nil {
			return nil, err
		}
		item.LexicalRank = len(out) + 1
		item.Source = "asset"
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s Service) memberVectorCandidates(ctx context.Context, principal auth.Principal, req MemberQueryRequest, role, q string, models []string, filters filterPlan) ([]candidate, error) {
	if s.Embeddings == nil {
		return nil, errors.New("embedding provider unavailable")
	}
	var modelName, modelVersion string
	var dimensions, configCount, identityCount int
	if err := s.Store.Pool.QueryRow(ctx, `
		SELECT count(*)::int, count(DISTINCT (model_name,model_version,dimensions))::int,
		       COALESCE(min(dimensions),0), COALESCE(min(model_name),''), COALESCE(min(model_version),'')
		FROM retrieval.projection_configs
		WHERE organization_id=$1::uuid AND resource_model_id=ANY($2::uuid[]) AND status='active'
	`, principal.OrganizationID, models).Scan(&configCount, &identityCount, &dimensions, &modelName, &modelVersion); err != nil {
		return nil, fmt.Errorf("load member embedding config: %w", err)
	}
	if configCount != len(models) || identityCount != 1 || dimensions != 1024 || modelName == "" || modelVersion == "" {
		return nil, errors.New("active member embedding config is unavailable or inconsistent")
	}
	vectors, err := s.Embeddings.Embed(ctx, []string{q})
	if err != nil || len(vectors) != 1 || len(vectors[0]) != dimensions {
		return nil, fmt.Errorf("member query embedding failed: %w", err)
	}
	literal, err := vectorvalue.Literal(vectors[0])
	if err != nil {
		return nil, fmt.Errorf("encode member query embedding: %w", err)
	}
	rows, err := s.Store.Pool.Query(ctx, `
		SELECT a.id::text,c.asset_version_id::text,c.id::text,c.projection_run_id::text,
		       1-(e.embedding <=> $4::vector(1024)), LEFT(regexp_replace(c.content,E'\\s+',' ','g'),500),
		       c.source_type,c.source_locator,c.char_start,c.char_end,
		       c.source_checksum,c.chunk_checksum,c.canonicalizer_version
		FROM retrieval.chunk_embeddings e
		JOIN retrieval.chunks c ON c.id=e.chunk_id AND c.status='ready'
		JOIN retrieval.projection_runs pr ON pr.id=c.projection_run_id AND pr.status='ready'
		JOIN retrieval.projection_configs pc ON pc.id=pr.projection_config_id
		 AND pc.status='active' AND pc.active_projection_generation=c.projection_generation
		 AND pc.chunker_version=c.chunker_version
		 AND e.model_name=pc.model_name AND e.model_version=pc.model_version
		 AND e.projection_generation=pc.active_projection_generation
		JOIN asset.assets a ON a.id=c.asset_id AND a.current_working_version_id=c.asset_version_id
		JOIN asset.asset_versions v ON v.id=c.asset_version_id
		JOIN model.resource_models rm ON rm.id=a.resource_model_id
		JOIN model.resource_model_versions mv ON mv.id=rm.current_version_id AND mv.status='published'
		WHERE e.organization_id=$1::uuid AND e.status='ready'
		  AND a.workspace_id=$2::uuid AND a.deleted_at IS NULL
		  AND a.resource_model_id=ANY($3::uuid[])
		  AND e.model_name=$5 AND e.model_version=$6
		  AND retrieval.matches_field_filters(v.fields,$7::jsonb)
		  AND retrieval.matches_field_filters(jsonb_build_object('tags', v.tags),$8::jsonb)
		  AND ($9::text IN ('owner','admin') OR a.visibility <> 'private' OR a.created_by=$10::uuid)
		  AND (cardinality($11::text[])=0 OR a.visibility=ANY($11::text[]))
		  AND (cardinality($12::text[])=0 OR a.publication_status=ANY($12::text[]))
		  AND COALESCE(NULLIF(mv.policy #>> '{outlets,workspace,enabled}','')::boolean,false)
		  AND retrieval.quality_rank(v.quality) >= retrieval.quality_rank($13)
		ORDER BY e.embedding <=> $4::vector(1024), a.id, c.id
		LIMIT 200
	`, principal.OrganizationID, req.WorkspaceID, models, literal, modelName, modelVersion, filters.fieldsJSON(), filters.tagsJSON(), role, principal.UserID, req.Visibility, req.PublicationStatus, filters.QualityGTE)
	if err != nil {
		return nil, fmt.Errorf("member vector recall: %w", err)
	}
	defer rows.Close()
	out := make([]candidate, 0)
	for rows.Next() {
		var item candidate
		if err := rows.Scan(&item.AssetID, &item.AssetVersionID, &item.ChunkID, &item.ProjectionRunID, &item.VectorScore, &item.Snippet, &item.SourceType, &item.Locator, &item.CharStart, &item.CharEnd, &item.SourceChecksum, &item.ChunkChecksum, &item.CanonicalizerVersion); err != nil {
			return nil, err
		}
		item.VectorRank = len(out) + 1
		item.Source = "asset"
		out = append(out, item)
	}
	return out, rows.Err()
}

func deduplicateMemberAssets(items []candidate) []candidate {
	seen := make(map[string]struct{}, len(items))
	result := make([]candidate, 0, len(items))
	for _, item := range items {
		if _, ok := seen[item.AssetID]; ok {
			continue
		}
		seen[item.AssetID] = struct{}{}
		result = append(result, item)
	}
	return result
}

func uniqueStrings(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if !containsString(result, value) {
			result = append(result, value)
		}
	}
	return result
}

func (s Service) pageMemberSession(ctx context.Context, principal auth.Principal, req MemberQueryRequest, role, q string, filters filterPlan, candidates []candidate, models []string, topK int, degraded bool, method string, policyRevision int64, projectionFingerprint string) (QueryResponse, error) {
	queryHash := memberSessionQueryHash(req, q, models)
	sessionID := ""
	offset := 0
	if req.Cursor != "" {
		cursor, err := s.decodeCursor(req.Cursor)
		if err != nil {
			return QueryResponse{}, err
		}
		sessionID, offset = cursor.SessionID, cursor.Ordinal
		var orgID, principalID, storedHash, storedMode, storedMethod, storedFingerprint string
		var storedDegraded bool
		var storedRevision int64
		err = s.Store.Pool.QueryRow(ctx, `
			SELECT organization_id::text,principal_id::text,query_hash,policy_revision,
			       projection_fingerprint,mode,degraded,ranking_method
			FROM retrieval.search_sessions WHERE id=$1::uuid AND expires_at>now()
		`, sessionID).Scan(&orgID, &principalID, &storedHash, &storedRevision, &storedFingerprint, &storedMode, &storedDegraded, &storedMethod)
		if err != nil || orgID != principal.OrganizationID || principalID != principal.UserID || storedHash != queryHash || storedMode != req.Mode || storedRevision != policyRevision || storedFingerprint != projectionFingerprint {
			return QueryResponse{}, ErrCursorInvalid
		}
		degraded, method = storedDegraded, storedMethod
	} else {
		if err := s.Store.Pool.QueryRow(ctx, `
			INSERT INTO retrieval.search_sessions
				(organization_id,principal_id,query_hash,policy_revision,projection_fingerprint,mode,degraded,ranking_method,expires_at)
			VALUES ($1::uuid,$2::uuid,$3,$4,$5,$6,$7,$8,now()+interval '5 minutes')
			RETURNING id::text
		`, principal.OrganizationID, principal.UserID, queryHash, policyRevision, projectionFingerprint, req.Mode, degraded, method).Scan(&sessionID); err != nil {
			return QueryResponse{}, fmt.Errorf("create member search session: %w", err)
		}
		for index, item := range candidates {
			if _, err := s.Store.Pool.Exec(ctx, `
				INSERT INTO retrieval.search_session_items
					(session_id,ordinal,asset_id,asset_version_id,chunk_id,lexical_rank,vector_rank,rrf_score,rerank_score,final_score,ranking_method)
				VALUES ($1::uuid,$2,$3::uuid,$4::uuid,$5::uuid,$6,$7,$8,$9,$10,$11)
			`, sessionID, index, item.AssetID, item.AssetVersionID, item.ChunkID, nullInt(item.LexicalRank), nullInt(item.VectorRank), nullFloat(item.RRFScore), nullFloat(item.RerankScore), item.FinalScore, item.Method); err != nil {
				return QueryResponse{}, fmt.Errorf("write member search session: %w", err)
			}
		}
	}

	if req.Cursor != "" {
		rows, err := s.Store.Pool.Query(ctx, `
			SELECT i.asset_id::text,i.asset_version_id::text,i.chunk_id::text,c.projection_run_id::text,
			       i.final_score,i.ranking_method,LEFT(regexp_replace(c.content,E'\\s+',' ','g'),500),
			       c.source_type,c.source_locator,c.char_start,c.char_end,c.source_checksum,c.chunk_checksum,c.canonicalizer_version
			FROM retrieval.search_session_items i
			JOIN retrieval.chunks c ON c.id=i.chunk_id AND c.status='ready'
			JOIN retrieval.projection_runs pr ON pr.id=c.projection_run_id AND pr.status='ready'
			JOIN retrieval.projection_configs pc ON pc.id=pr.projection_config_id
			 AND pc.status='active' AND pc.active_projection_generation=c.projection_generation AND pc.chunker_version=c.chunker_version
			JOIN asset.assets a ON a.id=i.asset_id AND a.current_working_version_id=i.asset_version_id
			JOIN asset.asset_versions v ON v.id=i.asset_version_id AND v.content_checksum=c.source_checksum
			JOIN model.resource_models rm ON rm.id=a.resource_model_id
			JOIN model.resource_model_versions mv ON mv.id=rm.current_version_id AND mv.status='published'
			WHERE i.session_id=$1::uuid AND i.ordinal >= $2
			  AND a.organization_id=$3::uuid AND a.workspace_id=$4::uuid AND a.deleted_at IS NULL
			  AND a.resource_model_id=ANY($5::uuid[])
			  AND retrieval.matches_field_filters(v.fields,$6::jsonb)
			  AND retrieval.matches_field_filters(jsonb_build_object('tags', v.tags),$7::jsonb)
			  AND ($8::text IN ('owner','admin') OR a.visibility <> 'private' OR a.created_by=$9::uuid)
			  AND (cardinality($10::text[])=0 OR a.visibility=ANY($10::text[]))
			  AND (cardinality($11::text[])=0 OR a.publication_status=ANY($11::text[]))
			  AND COALESCE(NULLIF(mv.policy #>> '{outlets,workspace,enabled}','')::boolean,false)
			  AND retrieval.quality_rank(v.quality) >= retrieval.quality_rank($12)
			ORDER BY i.ordinal LIMIT $13
		`, sessionID, offset, principal.OrganizationID, req.WorkspaceID, models, filters.fieldsJSON(), filters.tagsJSON(), role, principal.UserID, req.Visibility, req.PublicationStatus, filters.QualityGTE, topK+1)
		if err != nil {
			return QueryResponse{}, fmt.Errorf("page member search session: %w", err)
		}
		defer rows.Close()
		candidates = make([]candidate, 0, topK+1)
		for rows.Next() {
			var item candidate
			if err := rows.Scan(&item.AssetID, &item.AssetVersionID, &item.ChunkID, &item.ProjectionRunID, &item.FinalScore, &item.Method, &item.Snippet, &item.SourceType, &item.Locator, &item.CharStart, &item.CharEnd, &item.SourceChecksum, &item.ChunkChecksum, &item.CanonicalizerVersion); err != nil {
				return QueryResponse{}, err
			}
			item.Source = "asset"
			candidates = append(candidates, item)
		}
		if err := rows.Err(); err != nil {
			return QueryResponse{}, err
		}
	}

	page := candidates
	hasMore := len(page) > topK
	if hasMore {
		page = page[:topK]
	}
	end := offset + len(page)
	nextCursor := ""
	if hasMore {
		nextCursor = s.encodeCursor(cursorPayload{SessionID: sessionID, Ordinal: end})
	}
	items := make([]SearchItem, len(page))
	for index, item := range page {
		items[index] = SearchItem{
			AssetID: item.AssetID, AssetVersionID: item.AssetVersionID, ChunkID: item.ChunkID,
			ProjectionRunID: item.ProjectionRunID, Score: item.FinalScore, Snippet: item.Snippet,
			Source: "asset", SourceType: item.SourceType, SourceLocator: item.Locator,
			CharStart: item.CharStart, CharEnd: item.CharEnd, SourceChecksum: item.SourceChecksum,
			ChunkChecksum: item.ChunkChecksum, CanonicalizerVersion: item.CanonicalizerVersion,
			RankingMethod: item.Method,
		}
	}
	if err := s.enrichMemberItems(ctx, principal, req.WorkspaceID, items); err != nil {
		return QueryResponse{}, err
	}
	return QueryResponse{
		Mode: req.Mode, Degraded: degraded, Items: items, NextCursor: nextCursor, HasMore: hasMore,
		SessionID: sessionID, RankingMethod: method, PolicyRevision: policyRevision,
	}, nil
}

func (s Service) enrichMemberItems(ctx context.Context, principal auth.Principal, workspaceID string, items []SearchItem) error {
	if len(items) == 0 {
		return nil
	}
	versionIDs := make([]string, len(items))
	for index := range items {
		versionIDs[index] = items[index].AssetVersionID
	}
	rows, err := s.Store.Pool.Query(ctx, `
		SELECT v.id::text, COALESCE(v.title,''), COALESCE(LEFT(v.markdown,240),''),
		       v.fields, v.tags, a.updated_at
		FROM asset.asset_versions v
		JOIN asset.assets a ON a.id=v.asset_id AND a.current_working_version_id=v.id
		WHERE a.organization_id=$1::uuid AND a.workspace_id=$2::uuid AND v.id=ANY($3::uuid[])
	`, principal.OrganizationID, workspaceID, versionIDs)
	if err != nil {
		return fmt.Errorf("load member search metadata: %w", err)
	}
	defer rows.Close()
	type metadata struct {
		title, summary, updatedAt string
		fields                    map[string]any
		tags                      []string
	}
	byVersion := make(map[string]metadata, len(items))
	for rows.Next() {
		var versionID, title, summary string
		var updatedAt time.Time
		var fieldsJSON, tagsJSON []byte
		if err := rows.Scan(&versionID, &title, &summary, &fieldsJSON, &tagsJSON, &updatedAt); err != nil {
			return err
		}
		fields := map[string]any{}
		tags := []string{}
		_ = json.Unmarshal(fieldsJSON, &fields)
		_ = json.Unmarshal(tagsJSON, &tags)
		byVersion[versionID] = metadata{title: title, summary: summary, updatedAt: updatedAt.UTC().Format(time.RFC3339Nano), fields: fields, tags: tags}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for index := range items {
		value, ok := byVersion[items[index].AssetVersionID]
		if !ok {
			return ErrCursorInvalid
		}
		items[index].Title = value.title
		items[index].Summary = value.summary
		items[index].Fields = value.fields
		items[index].Tags = value.tags
		items[index].Highlights = map[string]any{}
		items[index].UpdatedAt = value.updatedAt
	}
	return nil
}

func memberSessionQueryHash(req MemberQueryRequest, q string, models []string) string {
	modelCopy := append([]string(nil), models...)
	visibility := append([]string(nil), req.Visibility...)
	publication := append([]string(nil), req.PublicationStatus...)
	sort.Strings(modelCopy)
	sort.Strings(visibility)
	sort.Strings(publication)
	value := strings.Join([]string{
		"member", req.WorkspaceID, req.Mode, q, strings.Join(modelCopy, "\x00"),
		strings.Join(visibility, "\x00"), strings.Join(publication, "\x00"), encodeFilterJSON(req.Filters),
	}, "\x00")
	return fmt.Sprintf("%x", sha256.Sum256([]byte(value)))
}
