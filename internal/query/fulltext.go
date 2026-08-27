package query

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"agentchunzhi/internal/auth"
	"agentchunzhi/internal/vectorvalue"
)

var (
	ErrModelAccessDenied = errors.New("model access denied")
	ErrWorkspaceMissing  = errors.New("workspace not found")
)
var ErrCursorInvalid = errors.New("invalid search cursor")
var ErrVectorUnavailable = errors.New("vector retrieval unavailable")

type SearchItem struct {
	AssetID              string          `json:"asset_id"`
	AssetVersionID       string          `json:"asset_version_id"`
	ChunkID              string          `json:"chunk_id,omitempty"`
	ProjectionRunID      string          `json:"projection_run_id,omitempty"`
	Score                float64         `json:"score"`
	Snippet              string          `json:"snippet"`
	Source               string          `json:"source"`
	SourceType           string          `json:"source_type,omitempty"`
	SourceLocator        json.RawMessage `json:"source_locator,omitempty"`
	CharStart            int             `json:"char_start,omitempty"`
	CharEnd              int             `json:"char_end,omitempty"`
	SourceChecksum       string          `json:"source_checksum,omitempty"`
	ChunkChecksum        string          `json:"chunk_checksum,omitempty"`
	CanonicalizerVersion string          `json:"canonicalizer_version,omitempty"`
	RankingMethod        string          `json:"ranking_method,omitempty"`
	Title                string          `json:"title"`
	Summary              string          `json:"summary"`
	Fields               map[string]any  `json:"fields"`
	Tags                 []string        `json:"tags"`
	Highlights           map[string]any  `json:"highlights"`
	UpdatedAt            string          `json:"updated_at"`
}
type QueryRequest struct {
	Mode     string
	Query    string
	ModelIDs []string
	TopK     int
	Cursor   string
	Filters  map[string]any
}
type QueryResponse struct {
	Mode           string       `json:"mode"`
	Degraded       bool         `json:"degraded"`
	Items          []SearchItem `json:"items"`
	NextCursor     string       `json:"next_cursor,omitempty"`
	HasMore        bool         `json:"has_more"`
	SessionID      string       `json:"session_id,omitempty"`
	RankingMethod  string       `json:"ranking_method"`
	PolicyRevision int64        `json:"policy_revision"`
}
type candidate struct {
	AssetID, AssetVersionID, ChunkID, ProjectionRunID, Snippet, SourceType, SourceChecksum, ChunkChecksum, CanonicalizerVersion string
	Source                                                                                                                      string
	Locator                                                                                                                     json.RawMessage
	CharStart, CharEnd                                                                                                          int
	LexicalRank, VectorRank                                                                                                     int
	LexicalScore, VectorScore, RRFScore, RerankScore, FinalScore                                                                float64
	Method                                                                                                                      string
}
type Reranker interface {
	Rerank(context.Context, string, []RerankCandidate) ([]float64, error)
}
type RerankCandidate struct {
	ID   string
	Text string
}

func (s Service) Query(ctx context.Context, principal auth.Principal, req QueryRequest, allowed []string) (QueryResponse, error) {
	q := strings.TrimSpace(req.Query)
	if len(allowed) == 0 {
		return QueryResponse{}, ErrModelAccessDenied
	}
	if len(req.ModelIDs) > 100 {
		return QueryResponse{}, fmt.Errorf("%w: model_ids must contain at most 100 entries", ErrInvalidQuery)
	}
	for _, modelID := range req.ModelIDs {
		if !ValidUUID(modelID) {
			return QueryResponse{}, fmt.Errorf("%w: model_ids must be UUIDs", ErrInvalidQuery)
		}
	}
	mode := strings.ToLower(strings.TrimSpace(req.Mode))
	if mode == "" {
		mode = "hybrid"
	}
	if mode == "" {
		mode = "hybrid"
	}
	// Align with the product-doc vocabulary on every channel: fulltext is the
	// documented alias of the internal lexical ranking; member and open APIs
	// must accept the same set of mode names.
	if mode == "lexical" {
		mode = "fulltext"
	}
	if mode != "structured" && mode != "fulltext" && mode != "semantic" && mode != "hybrid" {
		return QueryResponse{}, fmt.Errorf("%w: unsupported query mode", ErrInvalidQuery)
	}
	if (mode != "structured" && q == "") || len([]rune(q)) > 500 || strings.ContainsRune(q, '\x00') {
		return QueryResponse{}, fmt.Errorf("%w: query must be 1-500 characters unless mode is structured", ErrInvalidQuery)
	}
	req.Mode = mode
	topK := req.TopK
	if topK == 0 {
		topK = 10
	}
	if topK < 1 || topK > 100 {
		return QueryResponse{}, fmt.Errorf("%w: top_k must be 1-100", ErrInvalidQuery)
	}
	models := allowed
	if len(req.ModelIDs) > 0 {
		models = intersectModels(allowed, req.ModelIDs)
		if len(models) == 0 {
			return QueryResponse{}, ErrModelAccessDenied
		}
	}
	if s.Store == nil || s.Store.Pool == nil {
		return QueryResponse{}, errors.New("database store is not initialized")
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
	if req.Cursor != "" {
		return s.pageSession(ctx, principal, req, q, filters, nil, topK, false, mode, policyRevision, projectionFingerprint, models)
	}
	var lexical, vector []candidate
	if mode == "structured" || mode == "fulltext" || mode == "hybrid" {
		lexical, err = s.lexicalCandidates(ctx, principal, q, models, filters, mode == "hybrid")
		if err != nil {
			return QueryResponse{}, err
		}
	}
	degraded := false
	if mode == "semantic" || mode == "hybrid" {
		vector, err = s.vectorCandidates(ctx, principal, q, models, filters, mode == "hybrid")
		if err != nil {
			if mode == "hybrid" {
				degraded = true
			} else {
				return QueryResponse{}, fmt.Errorf("%w: %v", ErrVectorUnavailable, err)
			}
		}
	}
	mergeMode := mode
	if mode == "hybrid" && degraded {
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
		in := make([]RerankCandidate, min(50, len(merged)))
		for i := range in {
			in[i] = RerankCandidate{ID: merged[i].ChunkID, Text: merged[i].Snippet}
		}
		scores, e := s.Reranker.Rerank(ctx, q, in)
		if e != nil || len(scores) != len(in) {
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
	} else if mode == "hybrid" || mode == "semantic" {
		degraded = true
	}
	return s.pageSession(ctx, principal, req, q, filters, merged, topK, degraded, method, policyRevision, projectionFingerprint, models)
}

func (s Service) lexicalCandidates(ctx context.Context, p auth.Principal, q string, models []string, filters filterPlan, hybrid bool) ([]candidate, error) {
	rows, err := s.Store.Pool.Query(ctx, `
		SELECT a.id::text, c.asset_version_id::text, c.id::text, c.projection_run_id::text,
		       pgroonga_score(c.tableoid,c.ctid),
		       LEFT(regexp_replace(c.content,E'\\s+',' ','g'),500),
		       c.source_type,c.source_locator,c.char_start,c.char_end,
		       c.source_checksum,c.chunk_checksum,c.canonicalizer_version
		FROM retrieval.chunks c
		JOIN retrieval.projection_runs pr ON pr.id=c.projection_run_id AND pr.status='ready'
		JOIN retrieval.projection_configs pc ON pc.id=pr.projection_config_id
		 AND pc.status='active' AND pc.active_projection_generation=c.projection_generation
		 AND pc.chunker_version=c.chunker_version
		JOIN asset.assets a ON a.id=c.asset_id AND a.current_published_version_id=c.asset_version_id
		JOIN asset.asset_versions v ON v.id=c.asset_version_id
		JOIN model.resource_model_versions mv ON mv.id=v.resource_model_version_id AND mv.status='published'
		WHERE c.organization_id=$1::uuid AND c.status='ready'
		  AND a.publication_status='published' AND a.resource_model_id=ANY($3::uuid[])
		  AND ($2 = '' OR c.search_text &@~ $2)
		  AND retrieval.matches_field_filters(v.fields,$4::jsonb)
		  AND retrieval.matches_field_filters(jsonb_build_object('tags',v.tags),$5::jsonb)
		  AND COALESCE(NULLIF(mv.policy #>> '{outlets,agent_tool,enabled}','')::boolean,false)
		  AND COALESCE(NULLIF(mv.policy #>> '{outlets,fulltext,enabled}','')::boolean,false)
		  AND (NOT $7::boolean OR COALESCE(NULLIF(mv.policy #>> '{outlets,semantic,enabled}','')::boolean,false))
		  AND retrieval.quality_rank(v.quality) >= GREATEST(
		        retrieval.quality_rank(COALESCE(NULLIF(mv.policy #>> '{outlets,agent_tool,min_quality}',''),'raw')),
		        retrieval.quality_rank(COALESCE(NULLIF(mv.policy #>> '{outlets,fulltext,min_quality}',''),'raw')),
		        retrieval.quality_rank($6),
		        CASE WHEN $7::boolean THEN retrieval.quality_rank(COALESCE(NULLIF(mv.policy #>> '{outlets,semantic,min_quality}',''),'raw')) ELSE 1 END)
		ORDER BY pgroonga_score(c.tableoid,c.ctid) DESC,a.id,c.id
		LIMIT 100
	`, p.OrganizationID, q, models, filters.fieldsJSON(), filters.tagsJSON(), filters.QualityGTE, hybrid)
	if err != nil {
		return nil, fmt.Errorf("lexical recall: %w", err)
	}
	defer rows.Close()
	out := []candidate{}
	rank := 0
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.AssetID, &c.AssetVersionID, &c.ChunkID, &c.ProjectionRunID, &c.LexicalScore, &c.Snippet, &c.SourceType, &c.Locator, &c.CharStart, &c.CharEnd, &c.SourceChecksum, &c.ChunkChecksum, &c.CanonicalizerVersion); err != nil {
			return nil, err
		}
		rank++
		c.LexicalRank = rank
		c.Source = "asset"
		out = append(out, c)
	}
	return out, rows.Err()
}
func (s Service) vectorCandidates(ctx context.Context, p auth.Principal, q string, models []string, filters filterPlan, hybrid bool) ([]candidate, error) {
	if s.Embeddings == nil {
		return nil, fmt.Errorf("embedding provider unavailable")
	}
	var modelName, modelVersion string
	var dimensions, configCount, identityCount int
	if err := s.Store.Pool.QueryRow(ctx, `
		SELECT count(*)::int,
		       count(DISTINCT (model_name,model_version,dimensions))::int,
		       COALESCE(min(dimensions), 0), COALESCE(min(model_name), ''), COALESCE(min(model_version), '')
		FROM retrieval.projection_configs
		WHERE organization_id = $1::uuid AND resource_model_id = ANY($2::uuid[]) AND status = 'active'
	`, p.OrganizationID, models).Scan(&configCount, &identityCount, &dimensions, &modelName, &modelVersion); err != nil {
		return nil, fmt.Errorf("load active embedding config: %w", err)
	}
	if configCount != len(models) || identityCount != 1 || dimensions <= 0 || modelName == "" || modelVersion == "" {
		return nil, fmt.Errorf("active embedding config is unavailable or inconsistent")
	}
	vs, e := s.Embeddings.Embed(ctx, []string{q})
	if e != nil || len(vs) != 1 {
		return nil, fmt.Errorf("query embedding failed: %w", e)
	}
	if len(vs[0]) != dimensions {
		return nil, fmt.Errorf("query embedding dimension %d does not equal active dimension %d", len(vs[0]), dimensions)
	}
	literal, e := vectorvalue.Literal(vs[0])
	if e != nil {
		return nil, fmt.Errorf("encode query embedding: %w", e)
	}
	rows, e := s.Store.Pool.Query(ctx, `
		SELECT a.id::text,c.asset_version_id::text,c.id::text,c.projection_run_id::text,
		       1-(e.embedding <=> $2::vector(1024)),
		       LEFT(regexp_replace(c.content,E'\\s+',' ','g'),500),
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
		JOIN asset.assets a ON a.id=c.asset_id AND a.current_published_version_id=c.asset_version_id
		JOIN asset.asset_versions v ON v.id=c.asset_version_id
		JOIN model.resource_model_versions mv ON mv.id=v.resource_model_version_id AND mv.status='published'
		WHERE e.organization_id=$1::uuid AND e.status='ready'
		  AND e.model_name=$7 AND e.model_version=$8
		  AND a.publication_status='published' AND a.resource_model_id=ANY($3::uuid[])
		  AND retrieval.matches_field_filters(v.fields,$4::jsonb)
		  AND retrieval.matches_field_filters(jsonb_build_object('tags',v.tags),$5::jsonb)
		  AND COALESCE(NULLIF(mv.policy #>> '{outlets,agent_tool,enabled}','')::boolean,false)
		  AND COALESCE(NULLIF(mv.policy #>> '{outlets,semantic,enabled}','')::boolean,false)
		  AND (NOT $9::boolean OR COALESCE(NULLIF(mv.policy #>> '{outlets,fulltext,enabled}','')::boolean,false))
		  AND retrieval.quality_rank(v.quality) >= GREATEST(
		        retrieval.quality_rank(COALESCE(NULLIF(mv.policy #>> '{outlets,agent_tool,min_quality}',''),'raw')),
		        retrieval.quality_rank(COALESCE(NULLIF(mv.policy #>> '{outlets,semantic,min_quality}',''),'raw')),
		        retrieval.quality_rank($6),
		        CASE WHEN $9::boolean THEN retrieval.quality_rank(COALESCE(NULLIF(mv.policy #>> '{outlets,fulltext,min_quality}',''),'raw')) ELSE 1 END)
		ORDER BY e.embedding <=> $2::vector(1024),a.id,c.id
		LIMIT 100
	`, p.OrganizationID, literal, models, filters.fieldsJSON(), filters.tagsJSON(), filters.QualityGTE, modelName, modelVersion, hybrid)
	if e != nil {
		return nil, fmt.Errorf("vector recall: %w", e)
	}
	defer rows.Close()
	out := []candidate{}
	rank := 0
	for rows.Next() {
		var c candidate
		if e := rows.Scan(&c.AssetID, &c.AssetVersionID, &c.ChunkID, &c.ProjectionRunID, &c.VectorScore, &c.Snippet, &c.SourceType, &c.Locator, &c.CharStart, &c.CharEnd, &c.SourceChecksum, &c.ChunkChecksum, &c.CanonicalizerVersion); e != nil {
			return nil, e
		}
		rank++
		c.VectorRank = rank
		c.Source = "asset"
		out = append(out, c)
	}
	return out, rows.Err()
}
func mergeCandidates(lexical, vector []candidate, mode string) []candidate {
	by := map[string]candidate{}
	for _, c := range lexical {
		by[c.ChunkID] = c
	}
	for _, v := range vector {
		c, ok := by[v.ChunkID]
		if !ok {
			c = v
		} else {
			c.VectorScore = v.VectorScore
			c.VectorRank = v.VectorRank
		}
		by[v.ChunkID] = c
	}
	out := make([]candidate, 0, len(by))
	for _, c := range by {
		if mode == "hybrid" {
			if c.LexicalRank > 0 {
				c.RRFScore += 1 / (60 + float64(c.LexicalRank))
			}
			if c.VectorRank > 0 {
				c.RRFScore += 1 / (60 + float64(c.VectorRank))
			}
			c.FinalScore = c.RRFScore
			c.Method = "rrf"
		} else if mode == "semantic" {
			c.FinalScore = c.VectorScore
			c.Method = "vector"
		} else {
			c.FinalScore = c.LexicalScore
			c.Method = "lexical"
		}
		out = append(out, c)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].FinalScore != out[j].FinalScore {
			return out[i].FinalScore > out[j].FinalScore
		}
		return out[i].ChunkID < out[j].ChunkID
	})
	return out
}

func (s Service) pageSession(ctx context.Context, p auth.Principal, req QueryRequest, q string, filters filterPlan, candidates []candidate, topK int, degraded bool, method string, policyRevision int64, projectionFingerprint string, allowedModels []string) (QueryResponse, error) {
	queryHash := sessionQueryHash(req, q, allowedModels)
	sessionID := ""
	offset := 0
	if req.Cursor != "" {
		c, e := s.decodeCursor(req.Cursor)
		if e != nil {
			return QueryResponse{}, e
		}
		sessionID, offset = c.SessionID, c.Ordinal
		var org, principalID, storedHash, storedMode, storedMethod, storedFingerprint string
		var storedDegraded bool
		var storedRevision int64
		if e := s.Store.Pool.QueryRow(ctx, `SELECT organization_id::text,principal_id::text,query_hash,policy_revision,projection_fingerprint,mode,degraded,ranking_method FROM retrieval.search_sessions WHERE id=$1::uuid AND expires_at>now()`, sessionID).Scan(&org, &principalID, &storedHash, &storedRevision, &storedFingerprint, &storedMode, &storedDegraded, &storedMethod); e != nil || org != p.OrganizationID || principalID != p.UserID || storedHash != queryHash || storedMode != req.Mode || storedRevision != policyRevision || storedFingerprint != projectionFingerprint {
			return QueryResponse{}, ErrCursorInvalid
		}
		degraded, method = storedDegraded, storedMethod
	} else {
		if e := s.Store.Pool.QueryRow(ctx, `INSERT INTO retrieval.search_sessions (organization_id,principal_id,query_hash,policy_revision,projection_fingerprint,mode,degraded,ranking_method,expires_at) VALUES ($1::uuid,$2::uuid,$3,$4,$5,$6,$7,$8,now()+interval '5 minutes') RETURNING id::text`, p.OrganizationID, p.UserID, queryHash, policyRevision, projectionFingerprint, req.Mode, degraded, method).Scan(&sessionID); e != nil {
			return QueryResponse{}, fmt.Errorf("create search session: %w", e)
		}
		for i, c := range candidates {
			if _, e := s.Store.Pool.Exec(ctx, `INSERT INTO retrieval.search_session_items (session_id,ordinal,asset_id,asset_version_id,chunk_id,lexical_rank,vector_rank,rrf_score,rerank_score,final_score,ranking_method) VALUES ($1::uuid,$2,$3::uuid,$4::uuid,$5::uuid,$6,$7,$8,$9,$10,$11)`, sessionID, i, c.AssetID, c.AssetVersionID, c.ChunkID, nullInt(c.LexicalRank), nullInt(c.VectorRank), nullFloat(c.RRFScore), nullFloat(c.RerankScore), c.FinalScore, c.Method); e != nil {
				return QueryResponse{}, fmt.Errorf("write search session: %w", e)
			}
		}
	}
	if req.Cursor != "" {
		rows, e := s.Store.Pool.Query(ctx, `SELECT i.asset_id::text,i.asset_version_id::text,i.chunk_id::text,c.projection_run_id::text,i.final_score,i.ranking_method,LEFT(regexp_replace(c.content,E'\\s+',' ','g'),500),c.source_type,c.source_locator,c.char_start,c.char_end,c.source_checksum,c.chunk_checksum,c.canonicalizer_version FROM retrieval.search_session_items i JOIN retrieval.chunks c ON c.id=i.chunk_id JOIN retrieval.projection_runs pr ON pr.id=c.projection_run_id AND pr.status='ready' JOIN retrieval.projection_configs pc ON pc.id=pr.projection_config_id AND pc.status='active' AND pc.active_projection_generation=c.projection_generation AND pc.chunker_version=c.chunker_version JOIN asset.assets a ON a.id=i.asset_id AND a.current_published_version_id=i.asset_version_id JOIN asset.asset_versions v ON v.id=i.asset_version_id JOIN model.resource_model_versions mv ON mv.id=v.resource_model_version_id AND mv.status='published' WHERE i.session_id=$1::uuid AND i.ordinal >= $2 AND a.organization_id=$3::uuid AND a.resource_model_id=ANY($4::uuid[]) AND v.content_checksum=c.source_checksum AND retrieval.matches_field_filters(v.fields || jsonb_build_object('tags',v.tags),$5::jsonb) AND COALESCE(NULLIF(mv.policy #>> '{outlets,agent_tool,enabled}','')::boolean,false) AND CASE WHEN $6::text='structured' THEN retrieval.quality_rank(v.quality) >= GREATEST(retrieval.quality_rank(COALESCE(NULLIF(mv.policy #>> '{outlets,agent_tool,min_quality}',''),'raw')),retrieval.quality_rank($8)) WHEN $6::text='fulltext' OR ($6::text='hybrid' AND $7::boolean) THEN COALESCE(NULLIF(mv.policy #>> '{outlets,fulltext,enabled}','')::boolean,false) AND retrieval.quality_rank(v.quality) >= GREATEST(retrieval.quality_rank(COALESCE(NULLIF(mv.policy #>> '{outlets,agent_tool,min_quality}',''),'raw')),retrieval.quality_rank(COALESCE(NULLIF(mv.policy #>> '{outlets,fulltext,min_quality}',''),'raw')),retrieval.quality_rank($8)) WHEN $6::text='semantic' THEN COALESCE(NULLIF(mv.policy #>> '{outlets,semantic,enabled}','')::boolean,false) AND retrieval.quality_rank(v.quality) >= GREATEST(retrieval.quality_rank(COALESCE(NULLIF(mv.policy #>> '{outlets,agent_tool,min_quality}',''),'raw')),retrieval.quality_rank(COALESCE(NULLIF(mv.policy #>> '{outlets,semantic,min_quality}',''),'raw')),retrieval.quality_rank($8)) ELSE COALESCE(NULLIF(mv.policy #>> '{outlets,fulltext,enabled}','')::boolean,false) AND COALESCE(NULLIF(mv.policy #>> '{outlets,semantic,enabled}','')::boolean,false) AND retrieval.quality_rank(v.quality) >= GREATEST(retrieval.quality_rank(COALESCE(NULLIF(mv.policy #>> '{outlets,agent_tool,min_quality}',''),'raw')),retrieval.quality_rank(COALESCE(NULLIF(mv.policy #>> '{outlets,fulltext,min_quality}',''),'raw')),retrieval.quality_rank(COALESCE(NULLIF(mv.policy #>> '{outlets,semantic,min_quality}',''),'raw')),retrieval.quality_rank($8)) END ORDER BY i.ordinal LIMIT $9`, sessionID, offset, p.OrganizationID, allowedModels, filters.allJSON(), req.Mode, degraded, filters.QualityGTE, topK+1)
		if e != nil {
			return QueryResponse{}, e
		}
		defer rows.Close()
		candidates = []candidate{}
		for rows.Next() {
			var c candidate
			if e := rows.Scan(&c.AssetID, &c.AssetVersionID, &c.ChunkID, &c.ProjectionRunID, &c.FinalScore, &c.Method, &c.Snippet, &c.SourceType, &c.Locator, &c.CharStart, &c.CharEnd, &c.SourceChecksum, &c.ChunkChecksum, &c.CanonicalizerVersion); e != nil {
				return QueryResponse{}, e
			}
			c.Source = "asset"
			candidates = append(candidates, c)
		}
	}
	end := offset + topK
	page := candidates
	if req.Cursor == "" {
		if end > len(candidates) {
			end = len(candidates)
		}
		if end < offset {
			end = offset
		}
		page = candidates[offset:end]
	} else {
		hasMore := len(candidates) > topK
		if hasMore {
			page = candidates[:topK]
		}
		if hasMore {
			end = offset + topK
		} else {
			end = offset + len(candidates)
		}
	}
	hasMore := end < len(candidates) || (req.Cursor != "" && len(candidates) > topK)
	next := ""
	if hasMore {
		next = s.encodeCursor(cursorPayload{sessionID, end})
	}
	items := make([]SearchItem, len(page))
	for i, c := range page {
		items[i] = SearchItem{AssetID: c.AssetID, AssetVersionID: c.AssetVersionID, ChunkID: c.ChunkID, ProjectionRunID: c.ProjectionRunID, Score: c.FinalScore, Snippet: c.Snippet, Source: c.Source, SourceType: c.SourceType, SourceLocator: c.Locator, CharStart: c.CharStart, CharEnd: c.CharEnd, SourceChecksum: c.SourceChecksum, ChunkChecksum: c.ChunkChecksum, CanonicalizerVersion: c.CanonicalizerVersion, RankingMethod: c.Method}
	}
	return QueryResponse{Mode: req.Mode, Degraded: degraded, Items: items, NextCursor: next, HasMore: hasMore, SessionID: sessionID, RankingMethod: method, PolicyRevision: policyRevision}, nil
}

func (s Service) policyRevision(ctx context.Context, organizationID string) (int64, error) {
	var revision int64
	if err := s.Store.Pool.QueryRow(ctx, `SELECT COALESCE((SELECT revision FROM "authorization".policy_revisions WHERE organization_id=$1::uuid), 1)`, organizationID).Scan(&revision); err != nil {
		return 0, fmt.Errorf("load policy revision: %w", err)
	}
	return revision, nil
}

func (s Service) projectionFingerprint(ctx context.Context, organizationID string, models []string) (string, error) {
	var fingerprint string
	if err := s.Store.Pool.QueryRow(ctx, `
		SELECT COALESCE(string_agg(
			resource_model_id::text || ':' || active_projection_generation::text || ':' ||
			COALESCE(model_name,'') || ':' || COALESCE(model_version,'') || ':' || chunker_version,
			',' ORDER BY resource_model_id), 'none')
		FROM retrieval.projection_configs
		WHERE organization_id=$1::uuid AND resource_model_id=ANY($2::uuid[]) AND status='active'
	`, organizationID, models).Scan(&fingerprint); err != nil {
		return "", fmt.Errorf("load projection fingerprint: %w", err)
	}
	return fingerprint, nil
}

func sessionQueryHash(req QueryRequest, q string, allowedModels []string) string {
	models := append([]string(nil), allowedModels...)
	sort.Strings(models)
	value := req.Mode + "\x00" + q + "\x00" + strings.Join(models, "\x00") + "\x00" + encodeFilterJSON(req.Filters)
	return fmt.Sprintf("%x", sha256.Sum256([]byte(value)))
}

type cursorPayload struct {
	SessionID string `json:"s"`
	Ordinal   int    `json:"o"`
}

func (s Service) cursorSecret() []byte {
	if s.CursorSecret != "" {
		return []byte(s.CursorSecret)
	}
	return []byte("agentchunzhi-r3-cursor-secret")
}
func (s Service) encodeCursor(p cursorPayload) string {
	b, _ := json.Marshal(p)
	m := hmac.New(sha256.New, s.cursorSecret())
	m.Write(b)
	return base64.RawURLEncoding.EncodeToString(append(b, m.Sum(nil)...))
}
func (s Service) decodeCursor(v string) (cursorPayload, error) {
	raw, e := base64.RawURLEncoding.DecodeString(v)
	if e != nil || len(raw) < sha256.Size {
		return cursorPayload{}, ErrCursorInvalid
	}
	b, sig := raw[:len(raw)-sha256.Size], raw[len(raw)-sha256.Size:]
	m := hmac.New(sha256.New, s.cursorSecret())
	m.Write(b)
	if !hmac.Equal(sig, m.Sum(nil)) {
		return cursorPayload{}, ErrCursorInvalid
	}
	var p cursorPayload
	if json.Unmarshal(b, &p) != nil || !ValidUUID(p.SessionID) || p.Ordinal < 0 {
		return cursorPayload{}, ErrCursorInvalid
	}
	return p, nil
}
func nullInt(v int) any {
	if v <= 0 {
		return nil
	}
	return v
}
func nullFloat(v float64) any {
	if v == 0 {
		return nil
	}
	return v
}
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
func intersectModels(a, b []string) []string {
	set := map[string]bool{}
	for _, v := range a {
		set[v] = true
	}
	out := []string{}
	for _, v := range b {
		if set[v] && !containsString(out, v) {
			out = append(out, v)
		}
	}
	return out
}
func containsString(a []string, t string) bool {
	for _, v := range a {
		if v == t {
			return true
		}
	}
	return false
}
func encodeFilterJSON(filters map[string]any) string {
	if len(filters) == 0 {
		return "{}"
	}
	value, err := json.Marshal(filters)
	if err != nil {
		return "{}"
	}
	return string(value)
}
