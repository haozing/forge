package query

import (
	"context"
	"errors"
	"fmt"
	"time"

	"agentchunzhi/internal/store"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// sessionSnapshotRow is the search_sessions row subset the paginator needs.
type sessionSnapshotRow struct {
	ID                  string
	OrganizationID      string
	SubjectKind         string
	SubjectID           string
	Channel             string
	RequestHash         string
	ScopeFingerprint    string
	PolicyRevision      int64
	RequestedMode       string
	ExecutedMode        string
	RankingMethod       string
	Degraded            bool
	DegradationReasons  []string
	ProjectionProfileID string
	ProjectionGen       int64
	ResultCount         int
	ExpiresAt           time.Time
}

// sessionItemRow is one snapshot item of the frozen result list.
type sessionItemRow struct {
	WorkspaceID          string
	Ordinal              int
	AssetID              string
	AssetVersionID       string
	PrimaryChunkID       string
	CitationID           string
	LexicalRank          int
	SemanticRank         int
	RRFScore             float64
	RerankScore          float64
	FinalScore           float64
	RankingMethod        string
	SourceType           string
	Locator              []byte
	CharStart            int
	CharEnd              int
	Excerpt              string
	SourceChecksum       string
	ChunkChecksum        string
	CanonicalizerVersion string
	HasRerankScore       bool
}

// persistSessionTx writes the session and every item inside ONE transaction
// (doc §12.4: any item failure rolls the whole session back). The session id
// is generated client-side so citation tokens can be computed before insert.
func persistSessionTx(ctx context.Context, tx pgx.Tx, snapshot sessionSnapshotRow, items []sessionItemRow) error {
	if _, err := tx.Exec(ctx, `
		INSERT INTO retrieval.search_sessions
			(id, organization_id, subject_kind, subject_id, channel, request_hash,
			 scope_fingerprint_at_create, policy_revision_at_create, requested_mode,
			 executed_mode, ranking_method, degraded, degradation_reasons,
			 projection_profile_id, projection_generation, result_count, expires_at)
		VALUES ($1::uuid, $2::uuid, $3, $4::uuid, $5, $6, $7, $8, $9, $10, $11, $12, $13,
		        NULLIF($14,'')::uuid, $15, $16, $17)
	`, snapshot.ID, snapshot.OrganizationID, snapshot.SubjectKind, snapshot.SubjectID,
		snapshot.Channel, snapshot.RequestHash, snapshot.ScopeFingerprint,
		snapshot.PolicyRevision, snapshot.RequestedMode, snapshot.ExecutedMode,
		snapshot.RankingMethod, snapshot.Degraded, snapshot.DegradationReasons,
		nullString(snapshot.ProjectionProfileID), snapshot.ProjectionGen,
		snapshot.ResultCount, snapshot.ExpiresAt); err != nil {
		return fmt.Errorf("insert search session: %w", err)
	}
	for _, item := range items {
		if _, err := tx.Exec(ctx, `
			INSERT INTO retrieval.search_session_items
				(session_id, organization_id, workspace_id, ordinal, asset_id, asset_version_id,
				 primary_chunk_id, citation_id, lexical_rank, semantic_rank, rrf_score,
				 rerank_score, final_score, ranking_method, citation_source_type,
				 citation_source_locator, citation_char_start, citation_char_end,
				 citation_excerpt, citation_source_checksum, citation_chunk_checksum)
			VALUES ($1::uuid, $2::uuid, $3::uuid, $4, $5::uuid, $6::uuid, NULLIF($7,'')::uuid,
			        NULLIF($8,''), $9, $10, $11, $12, $13, $14, NULLIF($15,''),
			        $16::jsonb, $17, $18, NULLIF($19,''), NULLIF($20,''), NULLIF($21,''))
		`, snapshot.ID, snapshot.OrganizationID, item.WorkspaceID, item.Ordinal,
			item.AssetID, item.AssetVersionID, item.PrimaryChunkID, item.CitationID,
			nullableInt(item.LexicalRank > 0, item.LexicalRank),
			nullableInt(item.SemanticRank > 0, item.SemanticRank),
			nullableFloat(item.RRFScore > 0, item.RRFScore),
			nullableFloat(item.HasRerankScore, item.RerankScore),
			item.FinalScore, item.RankingMethod,
			item.SourceType, item.Locator,
			nullableInt(item.SourceType != "", item.CharStart),
			nullableInt(item.SourceType != "", item.CharEnd),
			item.Excerpt, item.SourceChecksum, item.ChunkChecksum); err != nil {
			return fmt.Errorf("insert search session item: %w", err)
		}
	}
	return nil
}

// nullString renders an empty string as SQL NULL.
func nullString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func nullableInt(condition bool, value int) any {
	if !condition {
		return nil
	}
	return value
}

func nullableFloat(condition bool, value float64) any {
	if !condition {
		return nil
	}
	return value
}

// loadSession loads one session row for cursor verification.
func loadSession(ctx context.Context, store *store.Store, organizationID, sessionID string) (sessionSnapshotRow, error) {
	var row sessionSnapshotRow
	var fingerprint, profileID string
	var reasons []string
	err := store.Pool.QueryRow(ctx, `
		SELECT id::text, organization_id::text, subject_kind, subject_id::text, channel,
		       request_hash, scope_fingerprint_at_create, policy_revision_at_create,
		       requested_mode, executed_mode, ranking_method, degraded,
		       degradation_reasons, COALESCE(projection_profile_id::text,''),
		       COALESCE(projection_generation, 0), result_count, expires_at
		FROM retrieval.search_sessions
		WHERE organization_id = $1::uuid AND id = $2::uuid
	`, organizationID, sessionID).Scan(&row.ID, &row.OrganizationID, &row.SubjectKind,
		&row.SubjectID, &row.Channel, &row.RequestHash, &fingerprint, &row.PolicyRevision,
		&row.RequestedMode, &row.ExecutedMode, &row.RankingMethod, &row.Degraded,
		&reasons, &profileID, &row.ProjectionGen, &row.ResultCount, &row.ExpiresAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return sessionSnapshotRow{}, ErrCursorInvalid
		}
		return sessionSnapshotRow{}, fmt.Errorf("load search session: %w", err)
	}
	row.ScopeFingerprint = fingerprint
	row.ProjectionProfileID = profileID
	row.DegradationReasons = reasons
	return row, nil
}

// loadSessionItemsPage reads one batch of snapshot items starting at the
// requested ordinal.
func loadSessionItemsPage(ctx context.Context, store *store.Store, sessionID string, afterOrdinal, limit int) ([]sessionItemRow, error) {
	rows, err := store.Pool.Query(ctx, `
		SELECT ordinal, asset_id::text, asset_version_id::text,
		       workspace_id::text,
		       COALESCE(primary_chunk_id::text,''), COALESCE(citation_id,''),
		       COALESCE(lexical_rank, 0), COALESCE(semantic_rank, 0),
		       COALESCE(rrf_score, 0), rerank_score, COALESCE(final_score, 0),
		       ranking_method, COALESCE(citation_source_type,''),
		       citation_source_locator, COALESCE(citation_char_start, 0),
		       COALESCE(citation_char_end, 0), COALESCE(citation_excerpt,''),
		       COALESCE(citation_source_checksum,''), COALESCE(citation_chunk_checksum,'')
		FROM retrieval.search_session_items
		WHERE session_id = $1::uuid AND ordinal >= $2
		ORDER BY ordinal
		LIMIT $3
	`, sessionID, afterOrdinal, limit)
	if err != nil {
		return nil, fmt.Errorf("load search session items: %w", err)
	}
	defer rows.Close()
	items := []sessionItemRow{}
	for rows.Next() {
		var item sessionItemRow
		var rerankScore *float64
		var locator []byte
		if err := rows.Scan(&item.Ordinal, &item.AssetID, &item.AssetVersionID,
			&item.WorkspaceID, &item.PrimaryChunkID, &item.CitationID, &item.LexicalRank,
			&item.SemanticRank, &item.RRFScore, &rerankScore, &item.FinalScore,
			&item.RankingMethod, &item.SourceType, &locator, &item.CharStart,
			&item.CharEnd, &item.Excerpt, &item.SourceChecksum, &item.ChunkChecksum); err != nil {
			return nil, fmt.Errorf("scan search session item: %w", err)
		}
		if rerankScore != nil {
			item.RerankScore = *rerankScore
			item.HasRerankScore = true
		}
		item.Locator = locator
		items = append(items, item)
	}
	return items, rows.Err()
}

// newSessionID generates the client-side session id.
func newSessionID() string {
	return uuid.NewString()
}

// sessionExpiry computes the session lifetime.
func sessionExpiry(ttl time.Duration) time.Time {
	if ttl <= 0 {
		ttl = DefaultSessionTTL
	}
	return time.Now().UTC().Add(ttl)
}
