package query

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"agentchunzhi/internal/access"
	"agentchunzhi/internal/auth"
	"agentchunzhi/internal/retrieval"
	"agentchunzhi/internal/store"

	"github.com/jackc/pgx/v5"
	"golang.org/x/sync/errgroup"
)

// requestIDContextKey carries the middleware-assigned correlation id into the
// audit row without crossing package boundaries.
type requestIDContextKey struct{}

// WithRequestID stores the request id for the audit trail.
func WithRequestID(ctx context.Context, requestID string) context.Context {
	return context.WithValue(ctx, requestIDContextKey{}, requestID)
}

// RequestIDFrom reads the request id (empty when absent).
func RequestIDFrom(ctx context.Context) string {
	if value, ok := ctx.Value(requestIDContextKey{}).(string); ok {
		return value
	}
	return ""
}

// Service is the single unified query service of phase 3. Every transport —
// member workspace, organization, OpenAPI and the in-process agent bridge —
// funnels through Execute with a freshly compiled QueryAccessScope;
// repositories never derive permissions themselves (doc §4.1/§18).
type Service struct {
	// Store is the database pool.
	Store *store.Store
	// Embeddings is the deployment-side query embedding provider. It stays nil
	// when the runtime manifest is incomplete; semantic recall then reports
	// semantic_provider_unavailable. There is no hash fallback.
	Embeddings retrieval.EmbeddingProvider
	// Reranker is optional; hybrid falls back to plain RRF without it.
	Reranker Reranker
	// CursorSecret signs pagination cursors (SEARCH_CURSOR_SECRET).
	CursorSecret string
	// QueryHashSecret signs request hashes, scope fingerprints and citation
	// tokens (QUERY_HASH_SECRET). It defaults to CursorSecret in unit tests.
	QueryHashSecret string
	// SessionTTL is the search session lifetime (RETRIEVAL_SESSION_TTL).
	SessionTTL time.Duration
	// QueryTimeout is the total request budget (RETRIEVAL_QUERY_TIMEOUT).
	QueryTimeout time.Duration
}

func (s Service) hashSecret() string {
	if s.QueryHashSecret != "" {
		return s.QueryHashSecret
	}
	return s.CursorSecret
}

func (s Service) compiler() ScopeCompiler {
	return newCompiler(s.Store, s.hashSecret())
}

// ScopeSource recompiles the current scope at execution time so membership and
// policy changes between audit begin and scope compile are honored
// (doc §10.1: audit first, then compile).
type ScopeSource func(ctx context.Context) (QueryAccessScope, error)

// WorkspaceQuery compiles ForWorkspaceMember and executes the request.
func (s Service) WorkspaceQuery(ctx context.Context, principal auth.Principal, workspaceID string, req Request) (Response, error) {
	return s.Execute(ctx, principal, ChannelWorkspace,
		func(ctx context.Context) (QueryAccessScope, error) {
			return s.compiler().ForWorkspaceMember(ctx, principal, workspaceID)
		}, req)
}

// OrganizationQuery compiles ForOrganizationMember and executes the request.
func (s Service) OrganizationQuery(ctx context.Context, principal auth.Principal, req Request) (Response, error) {
	return s.Execute(ctx, principal, ChannelWorkspace,
		func(ctx context.Context) (QueryAccessScope, error) {
			return s.compiler().ForOrganizationMember(ctx, principal)
		}, req)
}

// OpenAPIQuery compiles ForOpenAPI and executes the request.
func (s Service) OpenAPIQuery(ctx context.Context, principal auth.Principal, req Request) (Response, error) {
	return s.Execute(ctx, principal, ChannelOpenAPI,
		func(ctx context.Context) (QueryAccessScope, error) {
			return s.compiler().ForOpenAPI(ctx, principal)
		}, req)
}

// AgentQuery compiles ForAgent and executes the request; phase 4 rewires the
// built-in agent onto this entry point (doc §11.2).
func (s Service) AgentQuery(ctx context.Context, principal auth.Principal, req Request) (Response, error) {
	return s.Execute(ctx, principal, ChannelAgent,
		func(ctx context.Context) (QueryAccessScope, error) {
			return s.compiler().ForAgent(ctx, principal, req.ResourceModelIDs)
		}, req)
}

// Execute implements the phase 3 query execution flow (doc §10.1): audit
// begin, scope compile, validation, plan, recall, ranking, session persist,
// final authorization, audit complete.
func (s Service) Execute(ctx context.Context, principal auth.Principal, channel QueryChannel, compile ScopeSource, req Request) (Response, error) {
	if s.Store == nil || s.Store.Pool == nil {
		return Response{}, errors.New("database store is not initialized")
	}
	budget := s.QueryTimeout
	if budget <= 0 {
		budget = DefaultQueryTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()

	normalized := NormalizedRequest(req)
	requestHash := RequestHash(normalized, s.hashSecret())
	// Transport-level decode check: the audit row carries a CHECK-constrained
	// mode enum, so an alias or garbage mode must be rejected before the
	// started row is written.
	if !ValidMode(normalized.Mode) {
		return Response{}, ErrInvalidQueryMode
	}
	subjectKind := SubjectMember
	if principal.UserType == auth.UserTypeAgent {
		subjectKind = SubjectAgent
	}

	// 1. Audit begin (before scope compile and business validation).
	executionID, err := BeginQueryExecution(ctx, s.Store, principal.OrganizationID,
		subjectKind, principal.UserID, string(channel),
		RequestIDFrom(ctx), requestHash, normalized.Mode, nil)
	if err != nil {
		return Response{}, err
	}

	// 2. Compile the current scope.
	latency := stageLatency{}
	scopeStart := time.Now()
	scope, compileErr := compile(ctx)
	latency.observe(StageScope, scopeStart)
	if compileErr == nil {
		if bindErr := BindExecutionWorkspaces(ctx, s.Store, executionID,
			principal.OrganizationID, scope.WorkspaceIDs); bindErr != nil {
			compileErr = bindErr
		}
	}
	if compileErr != nil {
		if auditErr := CompleteQueryExecution(ctx, s.Store, executionID, false,
			httpCode(compileErr), "", "", false, nil, executionCounts{}, "", 0,
			"", "", latency, ""); auditErr != nil {
			return Response{}, auditErr
		}
		return Response{}, compileErr
	}

	response, execErr := s.run(ctx, scope, normalized, executionID, latency, requestHash)
	if execErr != nil {
		if auditErr := CompleteQueryExecution(ctx, s.Store, executionID, false,
			httpCode(execErr), "", "", false, nil, executionCounts{}, "", 0,
			"", "", latency, ""); auditErr != nil {
			return Response{}, auditErr
		}
		return Response{}, execErr
	}
	return response, nil
}

// httpCode resolves the fixed error code of a failure chain.
func httpCode(err error) string {
	_, code := HTTPStatus(err)
	return code
}

// run performs everything between the audit begin and the final audit
// complete. Non-nil errors are finalized by the caller.
func (s Service) run(ctx context.Context, scope QueryAccessScope, req Request, executionID string, latency stageLatency, requestHash string) (Response, error) {
	if scope.Empty() {
		return Response{}, ErrQueryScopeForbidden
	}
	// 3. Decode/normalize/validate.
	validateStart := time.Now()
	if err := validateRequest(scope, &req); err != nil {
		return Response{}, err
	}
	latency.observe(StageValidate, validateStart)

	// 4. Typed field filters and tag resolution.
	filters, err := s.compileFilters(ctx, scope, req)
	if err != nil {
		return Response{}, err
	}
	tags, err := resolveTagFilterKeys(ctx, s.Store, scope.OrganizationID, scope.WorkspaceIDs, req)
	if err != nil {
		return Response{}, err
	}

	// Cursor pages bypass recall: they replay the frozen session snapshot.
	if req.Cursor != "" {
		return s.pageSession(ctx, scope, req, executionID, latency, requestHash, filters, tags)
	}

	// 5. Policy-gated plan.
	policies, err := loadModelPolicies(ctx, s.Store, scope.OrganizationID, scope.Channel, scope.ResourceModelIDs)
	if err != nil {
		return Response{}, err
	}
	plan, err := buildPlan(req.Mode, scope, req.ResourceModelIDs, policies)
	if err != nil {
		return Response{}, err
	}

	// 6. Candidate recall.
	switch req.Mode {
	case ModeStructured:
		return s.executeStructured(ctx, scope, req, executionID, latency, filters, tags)
	case ModeFulltext:
		return s.executeFulltext(ctx, scope, req, plan, executionID, latency, filters, tags)
	case ModeSemantic:
		return s.executeSemantic(ctx, scope, req, plan, executionID, latency, filters, tags)
	default:
		return s.executeHybrid(ctx, scope, req, plan, executionID, latency, filters, tags)
	}
}

// compileFilters decodes and type-checks the typed field filters against the
// immutable ResourceModelVersion schema (doc §6.3).
func (s Service) compileFilters(ctx context.Context, scope QueryAccessScope, req Request) ([]compiledFieldFilter, error) {
	if len(req.FieldFilters) == 0 {
		return nil, nil
	}
	filterModels := make([]string, 0, len(req.FieldFilters))
	for _, filter := range req.FieldFilters {
		if !containsString(filterModels, filter.ResourceModelID) {
			filterModels = append(filterModels, filter.ResourceModelID)
		}
	}
	schemas, err := loadFieldSchema(ctx, s.Store, scope.OrganizationID, filterModels)
	if err != nil {
		return nil, err
	}
	return compileFieldFilters(req.FieldFilters, schemas)
}

// executeStructured runs the authoritative main-data plan (doc §10.2).
func (s Service) executeStructured(ctx context.Context, scope QueryAccessScope, req Request, executionID string, latency stageLatency, filters []compiledFieldFilter, tags resolvedTagFilter) (Response, error) {
	recallStart := time.Now()
	candidates, err := StructuredRecall(ctx, s.Store, scope, req, filters, tags)
	latency.observe(StageLexical, recallStart)
	if err != nil {
		return Response{}, err
	}
	sessionID := newSessionID()
	items := make([]sessionItemRow, len(candidates))
	for index, candidate := range candidates {
		items[index] = sessionItemRow{
			WorkspaceID:    candidate.WorkspaceID,
			Ordinal:        index,
			AssetID:        candidate.AssetID,
			AssetVersionID: candidate.AssetVersionID,
			RankingMethod:  RankingStructured,
		}
	}
	session, err := s.persistSession(ctx, scope, req, req.Mode, req.Mode, RankingStructured, false, nil, "", 0, sessionID, items)
	if err != nil {
		return Response{}, err
	}
	page, state, err := s.buildPage(ctx, scope, req, req.Mode, req.ResourceModelIDs, session, items, 0, filters, tags)
	if err != nil {
		return Response{}, err
	}
	response := Response{
		RequestedMode:      req.Mode,
		ExecutedMode:       req.Mode,
		RankingMethod:      RankingStructured,
		DegradationReasons: []string{},
		SessionID:          session.ID,
		Items:              page,
		Page:               state.page(),
	}
	if err := CompleteQueryExecution(ctx, s.Store, executionID, true, "",
		req.Mode, RankingStructured, false, []string{},
		executionCounts{ResourceModelCount: len(scope.ResourceModelIDs), ResultCount: len(page)},
		"", 0, "", "", latency, session.ID); err != nil {
		return Response{}, err
	}
	return response, nil
}

// executeFulltext runs the PGroonga plan (doc §10.3).
func (s Service) executeFulltext(ctx context.Context, scope QueryAccessScope, req Request, plan plan, executionID string, latency stageLatency, filters []compiledFieldFilter, tags resolvedTagFilter) (Response, error) {
	profile, err := activeProfile(ctx, s.Store, scope.OrganizationID)
	if err != nil {
		if errors.Is(err, retrieval.ErrNoActiveProfile) {
			return Response{}, ErrRetrievalProfileUnavailable
		}
		return Response{}, err
	}
	window := candidateWindow(req.TopK)
	recallStart := time.Now()
	lexical, err := LexicalRecall(ctx, s.Store, scope, req, plan, profile, filters, tags, window)
	latency.observe(StageLexical, recallStart)
	if err != nil {
		return Response{}, fmt.Errorf("%w: %v", ErrRetrievalUnavailable, err)
	}
	return s.rankAndPersist(ctx, scope, req, plan, executionID, latency, filters, tags, profile, lexical, nil)
}

// executeSemantic runs the HNSW plan (doc §10.4).
func (s Service) executeSemantic(ctx context.Context, scope QueryAccessScope, req Request, plan plan, executionID string, latency stageLatency, filters []compiledFieldFilter, tags resolvedTagFilter) (Response, error) {
	profile, err := activeProfile(ctx, s.Store, scope.OrganizationID)
	if err != nil {
		if errors.Is(err, retrieval.ErrNoActiveProfile) {
			return Response{}, ErrRetrievalProfileUnavailable
		}
		return Response{}, err
	}
	if !profile.SemanticEnabled {
		return Response{}, ErrRetrievalProfileUnavailable
	}
	embedStart := time.Now()
	vectorLiteral, dimensions, err := EmbedQueryLiteral(s.Embeddings, ctx, req.Query)
	latency.observe(StageEmbedQuery, embedStart)
	if err != nil {
		return Response{}, err
	}
	if dimensions != retrieval.DefaultEmbeddingDimensions {
		return Response{}, ErrSemanticProviderUnavailable
	}
	window := candidateWindow(req.TopK)
	recallStart := time.Now()
	semantic, err := SemanticRecall(ctx, s.Store, scope, req, plan, profile.ID, vectorLiteral, filters, tags, window)
	latency.observe(StageSemantic, recallStart)
	if err != nil {
		return Response{}, fmt.Errorf("%w: %v", ErrSemanticProviderUnavailable, err)
	}
	if incomplete, incompleteErr := SemanticProjectionIncomplete(ctx, s.Store, scope.OrganizationID, profile, scope.WorkspaceIDs); incompleteErr == nil && incomplete {
		plan.DegradationReasons = append(plan.DegradationReasons, ReasonSemanticProjectionPartial)
	}
	return s.rankAndPersist(ctx, scope, req, plan, executionID, latency, filters, tags, profile, nil, semantic)
}

// executeHybrid runs both branches in parallel and fuses them (doc §10.5).
func (s Service) executeHybrid(ctx context.Context, scope QueryAccessScope, req Request, plan plan, executionID string, latency stageLatency, filters []compiledFieldFilter, tags resolvedTagFilter) (Response, error) {
	profile, err := activeProfile(ctx, s.Store, scope.OrganizationID)
	if err != nil {
		if errors.Is(err, retrieval.ErrNoActiveProfile) {
			return Response{}, ErrRetrievalProfileUnavailable
		}
		return Response{}, err
	}
	window := candidateWindow(req.TopK)

	var (
		lexical     []chunkCandidate
		semantic    []chunkCandidate
		lexicalErr  error
		semanticErr error
		vectorLit   string
		dimensions  int
	)
	if len(plan.SemanticModels) > 0 {
		embedStart := time.Now()
		vectorLit, dimensions, semanticErr = EmbedQueryLiteral(s.Embeddings, ctx, req.Query)
		latency.observe(StageEmbedQuery, embedStart)
		if semanticErr != nil {
			// The embedding failure degrades hybrid to fulltext; it must not
			// cancel the parallel lexical branch.
			semanticErr = ErrSemanticProviderUnavailable
		} else if dimensions != retrieval.DefaultEmbeddingDimensions {
			semanticErr = ErrSemanticProviderUnavailable
		}
	}
	group, groupCtx := errgroup.WithContext(ctx)
	if len(plan.FulltextModels) > 0 {
		group.Go(func() error {
			recallStart := time.Now()
			lexical, lexicalErr = LexicalRecall(groupCtx, s.Store, scope, req, plan, profile, filters, tags, window)
			latency.observe(StageLexical, recallStart)
			return nil // branch errors are degradation signals, not cancels
		})
	}
	if len(plan.SemanticModels) > 0 && semanticErr == nil {
		group.Go(func() error {
			recallStart := time.Now()
			semantic, semanticErr = SemanticRecall(groupCtx, s.Store, scope, req, plan, profile.ID, vectorLit, filters, tags, window)
			latency.observe(StageSemantic, recallStart)
			return nil
		})
	}
	if err := group.Wait(); err != nil {
		return Response{}, err
	}

	executedMode, reasons, fatal := hybridOutcome(plan.ExecutedMode, plan.DegradationReasons,
		len(plan.FulltextModels) > 0, len(plan.SemanticModels) > 0, lexicalErr, semanticErr)
	if fatal {
		return Response{}, ErrRetrievalUnavailable
	}
	// A branch failure with no surviving recall path of the executed mode is a
	// hard provider failure, not a silently empty degraded page (doc §10.6).
	if lexicalErr == nil && semanticErr != nil && len(plan.FulltextModels) == 0 {
		return Response{}, ErrSemanticProviderUnavailable
	}
	if lexicalErr != nil && len(plan.SemanticModels) == 0 {
		return Response{}, ErrRetrievalUnavailable
	}
	plan.ExecutedMode = executedMode
	plan.DegradationReasons = reasons
	if plan.ExecutedMode == ModeFulltext {
		plan.SemanticModels = nil
	}
	if plan.ExecutedMode == ModeSemantic {
		plan.FulltextModels = nil
	}
	// A partial semantic projection stays visible and marks degraded; a
	// policy-sanctioned single branch is not a technical degradation.
	if len(plan.SemanticModels) > 0 {
		if incomplete, incompleteErr := SemanticProjectionIncomplete(ctx, s.Store, scope.OrganizationID, profile, scope.WorkspaceIDs); incompleteErr == nil && incomplete {
			plan.DegradationReasons = append(plan.DegradationReasons, ReasonSemanticProjectionPartial)
		}
	}
	return s.rankAndPersist(ctx, scope, req, plan, executionID, latency, filters, tags, profile, lexical, semantic)
}

// rankAndPersist fuses, optionally reranks, collapses to assets, persists the
// session and builds the first page.
func (s Service) rankAndPersist(ctx context.Context, scope QueryAccessScope, req Request, plan plan, executionID string, latency stageLatency, filters []compiledFieldFilter, tags resolvedTagFilter, profile retrieval.Profile, lexical, semantic []chunkCandidate) (Response, error) {
	fuseStart := time.Now()
	fused := fuseCandidates(lexical, semantic)
	latency.observe(StageFuse, fuseStart)

	rankingMethod := RankingRRF
	switch plan.ExecutedMode {
	case ModeFulltext:
		rankingMethod = RankingFulltext
		for index := range fused {
			fused[index].finalScore = fused[index].chunk.Score
		}
		sortFusedByBranchScore(fused)
	case ModeSemantic:
		rankingMethod = RankingSemantic
		for index := range fused {
			fused[index].finalScore = fused[index].chunk.Score
		}
		sortFusedByBranchScore(fused)
	default:
		for index := range fused {
			fused[index].finalScore = fused[index].rrf
		}
		sortFusedByRRF(fused)
	}

	degraded := len(plan.DegradationReasons) > 0

	// Optional rerank of the top candidates (doc §10.5): the input is already
	// scope-filtered, only query text and chunk content leave the process.
	if plan.ExecutedMode == ModeHybrid && s.Reranker != nil && len(fused) > 0 {
		limit := RerankCandidateLimit
		if len(fused) < limit {
			limit = len(fused)
		}
		input := make([]RerankCandidate, limit)
		for index := range input {
			input[index] = RerankCandidate{ID: fused[index].chunk.ChunkID, Text: fused[index].chunk.Content}
		}
		rerankStart := time.Now()
		scores, rerankErr := s.Reranker.Rerank(ctx, req.Query, input)
		latency.observe(StageRerank, rerankStart)
		if rerankErr != nil || len(scores) != limit {
			degraded = true
			if !containsString(plan.DegradationReasons, ReasonRerankerUnavailable) {
				plan.DegradationReasons = append(plan.DegradationReasons, ReasonRerankerUnavailable)
			}
		} else {
			for index := 0; index < limit; index++ {
				fused[index].rerank = scores[index]
				fused[index].finalScore = scores[index]
			}
			sortFusedByRerank(fused)
			rankingMethod = RankingRerank
		}
	}

	assets := truncateCandidates(collapseAssets(fused))

	// Session snapshot with citation tokens (session id first so the tokens can
	// bind to it inside the same transaction).
	sessionID := newSessionID()
	items := make([]sessionItemRow, len(assets))
	for index, asset := range assets {
		item := sessionItemRow{
			WorkspaceID:    asset.WorkspaceID,
			Ordinal:        index,
			AssetID:        asset.AssetID,
			AssetVersionID: asset.AssetVersionID,
			PrimaryChunkID: asset.Primary.chunk.ChunkID,
			LexicalRank:    asset.Primary.chunk.LexicalRank,
			SemanticRank:   asset.Primary.chunk.SemanticRank,
			RRFScore:       asset.Primary.rrf,
			RerankScore:    asset.Primary.rerank,
			HasRerankScore: asset.Primary.rerank != 0,
			FinalScore:     asset.Primary.finalScore,
			RankingMethod:  rankingMethod,
			SourceType:     asset.Primary.chunk.SourceType,
			Locator:        compactJSON(asset.Primary.chunk.SourceLocator),
			CharStart:      asset.Primary.chunk.CharStart,
			CharEnd:        asset.Primary.chunk.CharEnd,
			Excerpt:        truncateRunes(strings.Join(strings.Fields(asset.Primary.chunk.Content), " "), CitationExcerptRunes),
			SourceChecksum: asset.Primary.chunk.SourceChecksum,
			ChunkChecksum:  asset.Primary.chunk.ChunkChecksum,
			CanonicalizerVersion: asset.Primary.chunk.CanonicalizerVersion,
		}
		citationID, tokenErr := buildCitationToken(s.hashSecret(), citationPayload{
			SessionID:      sessionID,
			Ordinal:        index,
			AssetVersionID: asset.AssetVersionID,
			SourceChecksum: item.SourceChecksum,
			ChunkChecksum:  item.ChunkChecksum,
		})
		if tokenErr != nil {
			return Response{}, tokenErr
		}
		item.CitationID = citationID
		items[index] = item
	}
	session, err := s.persistSession(ctx, scope, req, plan.RequestedMode, plan.ExecutedMode, rankingMethod, degraded, plan.DegradationReasons, profile.ID, profile.Generation, sessionID, items)
	if err != nil {
		return Response{}, err
	}
	page, state, err := s.buildPage(ctx, scope, req, plan.ExecutedMode, req.ResourceModelIDs, session, items, 0, filters, tags)
	if err != nil {
		return Response{}, err
	}
	response := Response{
		RequestedMode:      plan.RequestedMode,
		ExecutedMode:       plan.ExecutedMode,
		RankingMethod:      rankingMethod,
		Degraded:           degraded,
		DegradationReasons: plan.DegradationReasons,
		SessionID:          session.ID,
		Items:              page,
		Page:               state.page(),
		Index:              s.indexInfo(ctx, scope.OrganizationID, profile.ID, profile.Generation),
	}
	counts := executionCounts{
		ResourceModelCount: len(scope.ResourceModelIDs),
		LexicalCandidates:  len(lexical),
		SemanticCandidates: len(semantic),
		FusedCandidates:    len(fused),
		ResultCount:        len(page),
	}
	if err := CompleteQueryExecution(ctx, s.Store, executionID, true, "",
		plan.ExecutedMode, rankingMethod, degraded, plan.DegradationReasons, counts,
		profile.ID, profile.Generation, embeddingIdentity(s.Embeddings),
		rerankerIdentity(s.Reranker), latency, session.ID); err != nil {
		return Response{}, err
	}
	return response, nil
}

// persistSession writes the session and all items in one transaction
// (doc §12.4: any item failure rolls the whole session back).
func (s Service) persistSession(ctx context.Context, scope QueryAccessScope, req Request, requestedMode, executedMode, rankingMethod string, degraded bool, reasons []string, profileID string, generation int64, sessionID string, items []sessionItemRow) (sessionSnapshotRow, error) {
	snapshot := sessionSnapshotRow{
		ID:                  sessionID,
		OrganizationID:      scope.OrganizationID,
		SubjectKind:         scope.SubjectKind,
		SubjectID:           scope.SubjectID,
		Channel:             string(scope.Channel),
		RequestHash:         RequestHash(req, s.hashSecret()),
		ScopeFingerprint:    fmt.Sprintf("%x", scope.ScopeFingerprint),
		PolicyRevision:      scope.PolicyRevision,
		RequestedMode:       requestedMode,
		ExecutedMode:        executedMode,
		RankingMethod:       rankingMethod,
		Degraded:            degraded,
		DegradationReasons:  reasons,
		ProjectionProfileID: profileID,
		ProjectionGen:       generation,
		ResultCount:         len(items),
		ExpiresAt:           sessionExpiry(s.SessionTTL),
	}
	if len(snapshot.DegradationReasons) == 0 {
		snapshot.DegradationReasons = []string{}
	}
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return sessionSnapshotRow{}, fmt.Errorf("begin search session: %w", err)
	}
	defer tx.Rollback(ctx)
	if err := persistSessionTx(ctx, tx, snapshot, items); err != nil {
		return sessionSnapshotRow{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return sessionSnapshotRow{}, fmt.Errorf("commit search session: %w", err)
	}
	return snapshot, nil
}

// indexInfo renders the projection index block of full-text-class modes.
func (s Service) indexInfo(ctx context.Context, organizationID, profileID string, generation int64) *IndexInfo {
	if profileID == "" {
		return nil
	}
	var indexedThrough time.Time
	err := s.Store.Pool.QueryRow(ctx, `
		SELECT COALESCE(max(completed_at), to_timestamp(0))
		FROM retrieval.projection_runs
		WHERE organization_id = $1::uuid AND projection_profile_id = $2::uuid
		  AND status IN ('ready', 'degraded')
	`, organizationID, profileID).Scan(&indexedThrough)
	if err != nil {
		return nil
	}
	info := &IndexInfo{ProfileGeneration: generation}
	if indexedThrough.After(time.Unix(0, 0)) {
		info.IndexedThrough = &indexedThrough
		info.LagSeconds = time.Since(indexedThrough).Seconds()
	}
	return info
}

func embeddingIdentity(provider retrieval.EmbeddingProvider) string {
	if provider == nil {
		return ""
	}
	manifest := provider.Manifest()
	return manifest.ProviderKey + "/" + manifest.Model + "/" + manifest.ModelVersion
}

func rerankerIdentity(reranker Reranker) string {
	if http, ok := reranker.(HTTPReranker); ok {
		return http.ModelVersion
	}
	return ""
}

// pageState is the pagination outcome of one page build.
type pageState struct {
	NextOrdinal int
	HasMore     bool
	NextCursor  string
}

func (p pageState) page() Page {
	return Page{NextCursor: p.NextCursor, HasMore: p.HasMore}
}

// finalAuthBatch bounds one final-authorization batch.
const finalAuthBatch = 100

// buildPage final-authorizes the next topK snapshot items after afterOrdinal
// and renders the response items. Filtered candidates are silently skipped
// until the page is filled or the snapshot is exhausted (doc §10.8). The next
// cursor points at "last checked ordinal + 1", not the last returned item.
func (s Service) buildPage(ctx context.Context, scope QueryAccessScope, req Request, executedMode string, executedModels []string, session sessionSnapshotRow, items []sessionItemRow, afterOrdinal int, filters []compiledFieldFilter, tags resolvedTagFilter) ([]Item, pageState, error) {
	codec := newCursorCodec(s.CursorSecret)
	pageItems := []Item{}
	checked := afterOrdinal - 1
	lastOrdinal := afterOrdinal - 1
	if len(items) > 0 {
		lastOrdinal = items[len(items)-1].Ordinal
	}
	index := 0
	for index < len(items) && len(pageItems) < req.TopK {
		if items[index].Ordinal < afterOrdinal {
			index++
			continue
		}
		chunkEnd := index + finalAuthBatch
		if chunkEnd > len(items) {
			chunkEnd = len(items)
		}
		batch := items[index:chunkEnd]
		pairs := make([][2]string, 0, len(batch))
		for _, item := range batch {
			pairs = append(pairs, [2]string{item.AssetID, item.AssetVersionID})
		}
		authorized, err := FinalAuthorize(ctx, s.Store, scope, req, executedMode, executedModels, filters, tags, pairs)
		if err != nil {
			return nil, pageState{}, err
		}
		var survivors [][2]string
		for _, pair := range pairs {
			if authorized[pair[0]+"\x00"+pair[1]] {
				survivors = append(survivors, pair)
			}
		}
		rows, err := loadAuthorizedAssets(ctx, s.Store, scope, req, executedMode, executedModels, filters, tags, survivors)
		if err != nil {
			return nil, pageState{}, err
		}
		byVersion := make(map[string]structuredCandidate, len(rows))
		for _, row := range rows {
			byVersion[row.AssetVersionID] = row
		}
		versionIDs := make([]string, 0, len(batch))
		for _, pair := range survivors {
			versionIDs = append(versionIDs, pair[1])
		}
		summaries, err := loadTagSummaries(ctx, s.Store, scope.OrganizationID, versionIDs)
		if err != nil {
			return nil, pageState{}, err
		}
		for _, item := range batch {
			checked = item.Ordinal
			candidate, ok := byVersion[item.AssetVersionID]
			if !ok {
				// The candidate failed the final authorization; it is skipped
				// silently — no 403, no skip count (doc §5.5).
				continue
			}
			pageItems = append(pageItems, s.renderItem(candidate, item, summaries[item.AssetVersionID]))
		}
		index = chunkEnd
	}
	state := pageState{NextOrdinal: checked + 1, HasMore: checked < lastOrdinal}
	if state.HasMore {
		token, err := codec.sign(cursorPayload{
			SessionID:   session.ID,
			NextOrdinal: state.NextOrdinal,
			ExpiresAt:   session.ExpiresAt,
		})
		if err != nil {
			return nil, pageState{}, err
		}
		state.NextCursor = token
	}
	return pageItems, state, nil
}

// renderItem merges the authorized main data with the snapshot item payload
// into the public response shape (doc §6.5).
func (s Service) renderItem(candidate structuredCandidate, item sessionItemRow, tags []TagSummary) Item {
	rendered := Item{
		AssetID:            candidate.AssetID,
		AssetVersionID:     candidate.AssetVersionID,
		WorkspaceID:        candidate.WorkspaceID,
		ResourceModelID:    candidate.ResourceModelID,
		Title:              candidate.Title,
		Summary:            candidate.Summary,
		Visibility:         candidate.Visibility,
		Origin:             candidate.Origin,
		ConfirmationStatus: candidate.ConfirmationStatus,
		PublishedAt:        candidate.PublishedAt,
		Tags:               tags,
	}
	if rendered.Tags == nil {
		rendered.Tags = []TagSummary{}
	}
	if item.RankingMethod == RankingStructured {
		return rendered
	}
	if item.FinalScore != 0 {
		rendered.Score = scorePointer(item.FinalScore)
	}
	if item.SourceType != "" {
		rendered.Citation = &Citation{
			CitationID:          item.CitationID,
			SourceType:          item.SourceType,
			SourceLocator:       compactJSON(item.Locator),
			CharStart:           item.CharStart,
			CharEnd:             item.CharEnd,
			Excerpt:             item.Excerpt,
			SourceChecksum:      item.SourceChecksum,
			ChunkChecksum:       item.ChunkChecksum,
			CanonicalizerVersion: item.CanonicalizerVersion,
		}
	}
	return rendered
}

// pageSession replays a frozen snapshot for cursor pagination (doc §10.8).
func (s Service) pageSession(ctx context.Context, scope QueryAccessScope, req Request, executionID string, latency stageLatency, requestHash string, filters []compiledFieldFilter, tags resolvedTagFilter) (Response, error) {
	codec := newCursorCodec(s.CursorSecret)
	payload, err := codec.verify(req.Cursor)
	if err != nil {
		return Response{}, err
	}
	session, err := loadSession(ctx, s.Store, scope.OrganizationID, payload.SessionID)
	if err != nil {
		return Response{}, err
	}
	now := time.Now().UTC()
	if now.After(session.ExpiresAt) || now.After(payload.ExpiresAt) {
		return Response{}, ErrSearchSessionExpired
	}
	// Subject, channel and request binding (doc §10.8 step 2). The scope
	// fingerprint is deliberately not compared: permission changes filter the
	// snapshot instead of invalidating the whole page.
	if session.SubjectKind != scope.SubjectKind || session.SubjectID != scope.SubjectID ||
		session.Channel != string(scope.Channel) || session.RequestHash != requestHash {
		return Response{}, ErrCursorInvalid
	}
	items, err := loadSessionItemsPage(ctx, s.Store, session.ID, payload.NextOrdinal, MaxSessionAssets)
	if err != nil {
		return Response{}, err
	}
	pageStart := time.Now()
	page, state, err := s.buildPage(ctx, scope, req, session.ExecutedMode, req.ResourceModelIDs, session, items, payload.NextOrdinal, filters, tags)
	latency.observe(StageFinalAuth, pageStart)
	if err != nil {
		return Response{}, err
	}
	// The stored canonicalizer version is absent from the session snapshot for
	// paged items; hydrate the primary citations from the immutable chunks.
	if err := s.hydrateCitationVersions(ctx, page); err != nil {
		return Response{}, err
	}
	response := Response{
		RequestedMode:      session.RequestedMode,
		ExecutedMode:       session.ExecutedMode,
		RankingMethod:      session.RankingMethod,
		Degraded:           session.Degraded,
		DegradationReasons: session.DegradationReasons,
		SessionID:          session.ID,
		Items:              page,
		Page:               state.page(),
		Index:              s.indexInfo(ctx, scope.OrganizationID, session.ProjectionProfileID, session.ProjectionGen),
	}
	counts := executionCounts{
		ResourceModelCount: len(scope.ResourceModelIDs),
		ResultCount:        len(page),
	}
	if err := CompleteQueryExecution(ctx, s.Store, executionID, true, "",
		session.ExecutedMode, session.RankingMethod, session.Degraded,
		session.DegradationReasons, counts, session.ProjectionProfileID,
		session.ProjectionGen, embeddingIdentity(s.Embeddings),
		rerankerIdentity(s.Reranker), latency, session.ID); err != nil {
		return Response{}, err
	}
	return response, nil
}

// hydrateCitationVersions fills the canonicalizer version of paged citations
// from the still-immutable chunk rows; a cleaned-up chunk filters the item.
func (s Service) hydrateCitationVersions(ctx context.Context, items []Item) error {
	for index := range items {
		if items[index].Citation == nil || items[index].Citation.CitationID == "" {
			continue
		}
		var canonicalizer string
		err := s.Store.Pool.QueryRow(ctx, `
			SELECT canonicalizer_version FROM retrieval.chunks
			WHERE source_checksum = $1 AND chunk_checksum = $2
			LIMIT 1
		`, items[index].Citation.SourceChecksum, items[index].Citation.ChunkChecksum).Scan(&canonicalizer)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				items[index].Citation.CanonicalizerVersion = ""
				continue
			}
			return fmt.Errorf("hydrate citation version: %w", err)
		}
		items[index].Citation.CanonicalizerVersion = canonicalizer
	}
	return nil
}

// ValidateCitationRefs implements POST /api/open/v2/references/validate: every
// ref must pass HMAC, session subject, snapshot and current published pointer
// checks; a single failure rejects the whole batch with 404 (doc §11.2).
func (s Service) ValidateCitationRefs(ctx context.Context, principal auth.Principal, refs []string) ([]ValidatedReference, error) {
	if len(refs) == 0 || len(refs) > MaxCitationRefs {
		return nil, ErrCitationRefNotFound
	}
	validated := make([]ValidatedReference, 0, len(refs))
	for _, ref := range refs {
		payload, err := verifyCitationToken(s.hashSecret(), ref)
		if err != nil {
			return nil, ErrCitationRefNotFound
		}
		reference, err := s.validateCitationRef(ctx, principal, ref, payload)
		if err != nil {
			return nil, err
		}
		if reference == nil {
			return nil, ErrCitationRefNotFound
		}
		validated = append(validated, *reference)
	}
	return validated, nil
}

// validateCitationRef re-verifies one citation against the session snapshot
// and the current main data.
func (s Service) validateCitationRef(ctx context.Context, principal auth.Principal, ref string, payload citationPayload) (*ValidatedReference, error) {
	var assetID, versionID, storedCitation, sourceChecksum, chunkChecksum string
	err := s.Store.Pool.QueryRow(ctx, `
		SELECT i.asset_id::text, i.asset_version_id::text, i.citation_id,
		       COALESCE(i.citation_source_checksum,''), COALESCE(i.citation_chunk_checksum,'')
		FROM retrieval.search_session_items i
		JOIN retrieval.search_sessions ss ON ss.organization_id = i.organization_id
		  AND ss.id = i.session_id
		WHERE i.session_id = $1::uuid AND i.ordinal = $2
		  AND ss.organization_id = $3::uuid
		  AND ss.subject_id = $4::uuid
		  AND ss.expires_at > now()
	`, payload.SessionID, payload.Ordinal, principal.OrganizationID, principal.UserID).Scan(
		&assetID, &versionID, &storedCitation, &sourceChecksum, &chunkChecksum)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load citation snapshot: %w", err)
	}
	// The presented token must be exactly the stored citation id and the
	// snapshot checksums must match the token payload.
	if storedCitation != ref || versionID != payload.AssetVersionID ||
		sourceChecksum != payload.SourceChecksum || chunkChecksum != payload.ChunkChecksum {
		return nil, nil
	}
	// The current published pointer must still be the cited version, and the
	// asset must still be visible to the caller: workspace-private assets
	// require an active membership in the owning workspace at validation time.
	var current, visibility, workspaceID string
	err = s.Store.Pool.QueryRow(ctx, `
		SELECT a.current_published_version_id::text, a.visibility, a.workspace_id::text
		FROM asset.assets a
		WHERE a.organization_id = $1::uuid AND a.id = $2::uuid
		  AND a.publication_status = 'published' AND a.deleted_at IS NULL
	`, principal.OrganizationID, assetID).Scan(&current, &visibility, &workspaceID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load cited asset: %w", err)
	}
	if current != versionID {
		return nil, nil
	}
	if visibility == access.VisibilityWorkspace {
		var member bool
		err = s.Store.Pool.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1
				FROM content.workspace_members wm
				JOIN content.workspaces w ON w.organization_id = wm.organization_id
				  AND w.id = wm.workspace_id AND w.status = 'active'
				JOIN identity.users u ON u.id = wm.user_id AND u.user_type = 'member'
				  AND u.status = 'active'
				WHERE wm.organization_id = $1::uuid AND wm.workspace_id = $2::uuid
				  AND wm.user_id = $3::uuid
			)
		`, principal.OrganizationID, workspaceID, principal.UserID).Scan(&member)
		if err != nil {
			return nil, fmt.Errorf("load cited asset membership: %w", err)
		}
		if !member {
			return nil, nil
		}
	}
	return &ValidatedReference{CitationRef: ref, AssetID: assetID, AssetVersionID: versionID}, nil
}
