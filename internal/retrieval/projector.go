package retrieval

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"agentchunzhi/internal/eventing"
	"agentchunzhi/internal/store"
	"agentchunzhi/internal/vectorvalue"

	"github.com/jackc/pgx/v5"
)

const (
	ProjectionConsumer         = "retrieval.projection"
	ProjectionRebuild          = "rebuild"
	ProjectionDelete           = "delete"
	DefaultEmbeddingModel      = "hash-embedding"
	DefaultEmbeddingVersion    = "v1"
	DefaultEmbeddingDimensions = 1024
	DefaultChunkerVersion      = "semantic-v1"
)

type Projector struct {
	Store          *store.Store
	Embeddings     EmbeddingProvider
	EmbeddingModel string
	EmbeddingVer   string
	Dimensions     int
	ChunkerVersion string
}

func (p Projector) provider() EmbeddingProvider {
	if p.Embeddings != nil {
		return p.Embeddings
	}
	return HashEmbeddingProvider{Dimensions: p.dimensions()}
}
func (p Projector) dimensions() int {
	if p.Dimensions > 0 {
		return p.Dimensions
	}
	return DefaultEmbeddingDimensions
}
func (p Projector) model() (string, string) {
	model, version := p.EmbeddingModel, p.EmbeddingVer
	if model == "" {
		model = DefaultEmbeddingModel
	}
	if version == "" {
		version = DefaultEmbeddingVersion
	}
	return model, version
}
func (p Projector) chunker() string {
	if p.ChunkerVersion != "" {
		return p.ChunkerVersion
	}
	return DefaultChunkerVersion
}

func (p Projector) Rebuild(ctx context.Context, assetVersionID string) error {
	if p.Store == nil || p.Store.Pool == nil {
		return errors.New("database store is not initialized")
	}
	tx, err := p.Store.Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin retrieval projection rebuild: %w", err)
	}
	defer tx.Rollback(ctx)
	if err := p.upsertProjectionTx(ctx, tx, assetVersionID); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit retrieval projection rebuild: %w", err)
	}
	return nil
}

func (p Projector) Delete(ctx context.Context, assetVersionID string) error {
	if p.Store == nil || p.Store.Pool == nil {
		return errors.New("database store is not initialized")
	}
	if _, err := p.Store.Pool.Exec(ctx, `UPDATE retrieval.chunks SET status = 'deleted', search_text = '', content = '', updated_at = now() WHERE asset_version_id = $1::uuid`, assetVersionID); err != nil {
		return fmt.Errorf("delete retrieval chunks: %w", err)
	}
	if _, err := p.Store.Pool.Exec(ctx, `UPDATE retrieval.chunk_embeddings SET status = 'deleted', updated_at = now() WHERE projection_run_id IN (SELECT id FROM retrieval.projection_runs WHERE asset_version_id = $1::uuid)`, assetVersionID); err != nil {
		return fmt.Errorf("delete retrieval embeddings: %w", err)
	}
	if _, err := p.Store.Pool.Exec(ctx, `UPDATE retrieval.projection_runs SET status = 'stale', updated_at = now() WHERE asset_version_id = $1::uuid AND status <> 'stale'`, assetVersionID); err != nil {
		return fmt.Errorf("delete retrieval projection runs: %w", err)
	}
	return nil
}

func UpsertProjectionTx(ctx context.Context, tx pgx.Tx, assetVersionID string) error {
	return (Projector{}).upsertProjectionTx(ctx, tx, assetVersionID)
}

func (p Projector) upsertProjectionTx(ctx context.Context, tx pgx.Tx, assetVersionID string) error {
	var orgID, assetID, modelID, sourceChecksum, title, markdown, fields string
	if err := tx.QueryRow(ctx, `SELECT av.organization_id::text, av.asset_id::text, av.resource_model_id::text, av.content_checksum, COALESCE(av.title,''), COALESCE(av.markdown,''), av.fields::text FROM asset.asset_versions av WHERE av.id = $1::uuid`, assetVersionID).Scan(&orgID, &assetID, &modelID, &sourceChecksum, &title, &markdown, &fields); err != nil {
		return fmt.Errorf("load asset version for retrieval projection: %w", err)
	}
	canonical := strings.TrimSpace(strings.Join([]string{title, markdown, fields}, "\n"))
	if canonical == "" {
		canonical = " "
	}
	chunks := splitCanonical(canonical)
	model, version := p.model()
	dimensions, chunker := p.dimensions(), p.chunker()
	var configID string
	err := tx.QueryRow(ctx, `SELECT id::text FROM retrieval.projection_configs WHERE organization_id=$1::uuid AND resource_model_id=$2::uuid AND status='active' ORDER BY active_projection_generation DESC LIMIT 1`, orgID, modelID).Scan(&configID)
	if errors.Is(err, pgx.ErrNoRows) {
		err = tx.QueryRow(ctx, `INSERT INTO retrieval.projection_configs (organization_id,resource_model_id,model_name,model_version,dimensions,chunker_version,active_projection_generation,status,activated_at) VALUES ($1::uuid,$2::uuid,$3,$4,$5,$6,1,'active',now()) RETURNING id::text`, orgID, modelID, model, version, dimensions, chunker).Scan(&configID)
	}
	if err != nil {
		return fmt.Errorf("ensure active retrieval projection config: %w", err)
	}
	var generation int64
	if err := tx.QueryRow(ctx, `SELECT active_projection_generation FROM retrieval.projection_configs WHERE id=$1::uuid`, configID).Scan(&generation); err != nil {
		return fmt.Errorf("load retrieval projection generation: %w", err)
	}
	var runID string
	if err := tx.QueryRow(ctx, `INSERT INTO retrieval.projection_runs (organization_id,resource_model_id,asset_version_id,source_checksum,canonicalizer_version,projection_config_id,status,started_at) VALUES ($1::uuid,$2::uuid,$3::uuid,$4,'v1',$5::uuid,'running',now()) ON CONFLICT (asset_version_id,source_checksum,canonicalizer_version,projection_config_id) DO UPDATE SET status='running',error_code=NULL,started_at=now(),updated_at=now() RETURNING id::text`, orgID, modelID, assetVersionID, sourceChecksum, configID).Scan(&runID); err != nil {
		return fmt.Errorf("ensure retrieval projection run: %w", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM retrieval.chunks WHERE projection_run_id=$1::uuid`, runID); err != nil {
		return fmt.Errorf("clear retrieval chunks: %w", err)
	}
	for _, chunk := range chunks {
		if _, err := tx.Exec(ctx, `INSERT INTO retrieval.chunks (organization_id,asset_id,resource_model_id,asset_version_id,projection_run_id,projection_generation,chunker_version,ordinal,content,search_text,canonical_text,char_start,char_end,source_type,source_locator,source_checksum,chunk_checksum,canonicalizer_version,status) VALUES ($1::uuid,$2::uuid,$3::uuid,$4::uuid,$5::uuid,$6,$7,$8,$9,$9,$9,$10,$11,'asset','{"type":"asset"}'::jsonb,$12,$13,'v1','ready')`, orgID, assetID, modelID, assetVersionID, runID, generation, chunker, chunk.Ordinal, chunk.Text, chunk.Start, chunk.End, sourceChecksum, chunk.Checksum); err != nil {
			return fmt.Errorf("write retrieval chunk: %w", err)
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE retrieval.projection_runs SET expected_chunk_count=$2,ready_chunk_count=$2,expected_embedding_count=$2,ready_embedding_count=0,updated_at=now() WHERE id=$1::uuid`, runID, len(chunks)); err != nil {
		return fmt.Errorf("update retrieval projection counters: %w", err)
	}
	embeddings, embeddingErr := p.provider().Embed(ctx, chunkTexts(chunks))
	if embeddingErr == nil && len(embeddings) == len(chunks) {
		for i, embedding := range embeddings {
			if len(embedding) != dimensions {
				embeddingErr = fmt.Errorf("embedding dimension %d does not equal %d", len(embedding), dimensions)
				break
			}
			literal, err := vectorvalue.Literal(embedding)
			if err != nil {
				embeddingErr = err
				break
			}
			if _, err := tx.Exec(ctx, `INSERT INTO retrieval.chunk_embeddings (chunk_id,organization_id,model_name,model_version,dimensions,projection_generation,projection_run_id,embedding,status) SELECT c.id,$1::uuid,$2,$3,$4,c.projection_generation,c.projection_run_id,$5::vector(1024),'ready' FROM retrieval.chunks c WHERE c.projection_run_id=$6::uuid AND c.ordinal=$7 ON CONFLICT (chunk_id,model_name,model_version,projection_generation) DO UPDATE SET embedding=EXCLUDED.embedding,status='ready',error_code=NULL,updated_at=now()`, orgID, model, version, dimensions, literal, runID, i); err != nil {
				return fmt.Errorf("write chunk embedding: %w", err)
			}
		}
	}
	if embeddingErr != nil {
		if _, err := tx.Exec(ctx, `DELETE FROM retrieval.chunk_embeddings WHERE projection_run_id=$1::uuid`, runID); err != nil {
			return fmt.Errorf("clear partial embeddings: %w", err)
		}
		if _, err := tx.Exec(ctx, `UPDATE retrieval.projection_runs SET error_code='embedding_unavailable',updated_at=now() WHERE id=$1::uuid`, runID); err != nil {
			return fmt.Errorf("record embedding failure: %w", err)
		}
		if _, err := tx.Exec(ctx, `UPDATE retrieval.projection_runs SET expected_embedding_count=0,ready_embedding_count=0,updated_at=now() WHERE id=$1::uuid`, runID); err != nil {
			return fmt.Errorf("close degraded embedding counters: %w", err)
		}
	} else if _, err := tx.Exec(ctx, `UPDATE retrieval.projection_runs SET ready_embedding_count=expected_embedding_count,updated_at=now() WHERE id=$1::uuid`, runID); err != nil {
		return fmt.Errorf("close embedding counters: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE retrieval.projection_runs SET status='ready',completed_at=now(),updated_at=now() WHERE id=$1::uuid`, runID); err != nil {
		return fmt.Errorf("complete retrieval projection: %w", err)
	}
	return nil
}

type canonicalChunk struct {
	Ordinal  int
	Text     string
	Start    int
	End      int
	Checksum string
}

func chunkTexts(chunks []canonicalChunk) []string {
	result := make([]string, len(chunks))
	for i, c := range chunks {
		result[i] = c.Text
	}
	return result
}
func splitCanonical(value string) []canonicalChunk {
	runes := []rune(value)
	const target, overlap = 1600, 200
	chunks := make([]canonicalChunk, 0, (len(runes)/target)+1)
	start := 0
	for start < len(runes) {
		end := start + target
		if end > len(runes) {
			end = len(runes)
		}
		if end < len(runes) {
			for i := end; i > start+target/2; i-- {
				if strings.ContainsRune("\n 。！？", runes[i-1]) {
					end = i
					break
				}
			}
		}
		text := strings.TrimSpace(string(runes[start:end]))
		if text == "" {
			text = " "
		}
		sum := sha256.Sum256([]byte(text))
		chunks = append(chunks, canonicalChunk{len(chunks), text, start, end, hex.EncodeToString(sum[:])})
		if end == len(runes) {
			break
		}
		start = end - overlap
		if start < 0 {
			start = end
		}
	}
	return chunks
}

func EnqueueProjectionTx(ctx context.Context, tx pgx.Tx, events eventing.EventStore, organizationID, assetVersionID, operation string) error {
	if operation != ProjectionRebuild && operation != ProjectionDelete {
		return fmt.Errorf("unsupported retrieval projection operation %q", operation)
	}
	_, err := events.AppendTx(ctx, tx, eventing.Event{OrganizationID: organizationID, EventType: "asset.retrieval_projection_requested", AggregateType: "asset_version", AggregateID: assetVersionID, AggregateVersion: 1, PayloadVersion: 1, Payload: map[string]string{"asset_version_id": assetVersionID, "operation": operation}})
	return err
}
