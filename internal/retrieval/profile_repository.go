package retrieval

import (
	"context"
	"errors"
	"fmt"

	"agentchunzhi/internal/store"

	"github.com/jackc/pgx/v5"
)

// rowQuerier is the narrow query surface shared by pools and transactions.
type rowQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// ProfileRepository owns all retrieval.projection_profiles SQL.
type ProfileRepository struct {
	Store *store.Store
}

// ListProfiles returns every profile of the organization, newest generation
// first.
func (r ProfileRepository) ListProfiles(ctx context.Context, organizationID string) ([]Profile, error) {
	rows, err := r.Store.Pool.Query(ctx, `
		SELECT id::text, organization_id::text, generation, manifest_key,
		       canonicalizer_version, chunker_version, tokenizer_version,
		       semantic_enabled, COALESCE(embedding_provider_key,''), COALESCE(embedding_model,''),
		       COALESCE(embedding_model_version,''), COALESCE(embedding_dimensions,0),
		       COALESCE(distance_metric,''), status, revision, COALESCE(created_by::text,''),
		       created_at, COALESCE(activated_by::text,''), COALESCE(activated_at, to_timestamp(0)),
		       COALESCE(retired_at, to_timestamp(0)), COALESCE(failure_code,'')
		FROM retrieval.projection_profiles
		WHERE organization_id = $1::uuid
		ORDER BY generation DESC
	`, organizationID)
	if err != nil {
		return nil, fmt.Errorf("list retrieval profiles: %w", err)
	}
	defer rows.Close()
	return scanProfiles(rows)
}

// GetProfile loads one profile.
func (r ProfileRepository) GetProfile(ctx context.Context, organizationID, profileID string) (Profile, error) {
	row := r.Store.Pool.QueryRow(ctx, `
		SELECT id::text, organization_id::text, generation, manifest_key,
		       canonicalizer_version, chunker_version, tokenizer_version,
		       semantic_enabled, COALESCE(embedding_provider_key,''), COALESCE(embedding_model,''),
		       COALESCE(embedding_model_version,''), COALESCE(embedding_dimensions,0),
		       COALESCE(distance_metric,''), status, revision, COALESCE(created_by::text,''),
		       created_at, COALESCE(activated_by::text,''), COALESCE(activated_at, to_timestamp(0)),
		       COALESCE(retired_at, to_timestamp(0)), COALESCE(failure_code,'')
		FROM retrieval.projection_profiles
		WHERE organization_id = $1::uuid AND id = $2::uuid
	`, organizationID, profileID)
	return scanProfile(row)
}

// GetActiveProfile loads the serving profile; ErrNoActiveProfile when absent.
func (r ProfileRepository) GetActiveProfile(ctx context.Context, organizationID string) (Profile, error) {
	row := r.Store.Pool.QueryRow(ctx, `
		SELECT id::text, organization_id::text, generation, manifest_key,
		       canonicalizer_version, chunker_version, tokenizer_version,
		       semantic_enabled, COALESCE(embedding_provider_key,''), COALESCE(embedding_model,''),
		       COALESCE(embedding_model_version,''), COALESCE(embedding_dimensions,0),
		       COALESCE(distance_metric,''), status, revision, COALESCE(created_by::text,''),
		       created_at, COALESCE(activated_by::text,''), COALESCE(activated_at, to_timestamp(0)),
		       COALESCE(retired_at, to_timestamp(0)), COALESCE(failure_code,'')
		FROM retrieval.projection_profiles
		WHERE organization_id = $1::uuid AND status = 'active'
	`, organizationID)
	profile, err := scanProfile(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Profile{}, ErrNoActiveProfile
	}
	return profile, err
}

// GetWarmingProfile loads the warming profile, if any.
func (r ProfileRepository) GetWarmingProfile(ctx context.Context, organizationID string) (Profile, error) {
	row := r.Store.Pool.QueryRow(ctx, `
		SELECT id::text, organization_id::text, generation, manifest_key,
		       canonicalizer_version, chunker_version, tokenizer_version,
		       semantic_enabled, COALESCE(embedding_provider_key,''), COALESCE(embedding_model,''),
		       COALESCE(embedding_model_version,''), COALESCE(embedding_dimensions,0),
		       COALESCE(distance_metric,''), status, revision, COALESCE(created_by::text,''),
		       created_at, COALESCE(activated_by::text,''), COALESCE(activated_at, to_timestamp(0)),
		       COALESCE(retired_at, to_timestamp(0)), COALESCE(failure_code,'')
		FROM retrieval.projection_profiles
		WHERE organization_id = $1::uuid AND status = 'warming'
	`, organizationID)
	profile, err := scanProfile(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Profile{}, pgx.ErrNoRows
	}
	return profile, err
}

// CreateProfileTx inserts a warming profile as the next generation.
func (r ProfileRepository) CreateProfileTx(ctx context.Context, tx pgx.Tx, organizationID, manifestKey string, semanticEnabled bool, manifest EmbeddingManifest, createdBy string) (Profile, error) {
	canonicalizer, chunker, tokenizer := AlgorithmVersions(manifest)
	providerKey, model, modelVersion := "", "", ""
	dimensions, distance := 0, ""
	if semanticEnabled {
		normalized, err := manifest.Normalize()
		if err != nil {
			return Profile{}, err
		}
		manifest = normalized
		providerKey, model, modelVersion, dimensions = ProfileManifestMeta(normalized)
		distance = "cosine"
	}
	generation := int64(1)
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(max(generation), 0) + 1
		FROM retrieval.projection_profiles
		WHERE organization_id = $1::uuid
	`, organizationID).Scan(&generation); err != nil {
		return Profile{}, fmt.Errorf("next retrieval profile generation: %w", err)
	}
	profile := Profile{
		OrganizationID:        organizationID,
		Generation:            generation,
		ManifestKey:           manifestKey,
		CanonicalizerVersion:  canonicalizer,
		ChunkerVersion:        chunker,
		TokenizerVersion:      tokenizer,
		SemanticEnabled:       semanticEnabled,
		EmbeddingProviderKey:  providerKey,
		EmbeddingModel:        model,
		EmbeddingModelVersion: modelVersion,
		EmbeddingDimensions:   dimensions,
		DistanceMetric:        distance,
		Status:                ProfileStatusWarming,
		Revision:              1,
		CreatedBy:             createdBy,
	}
	err := tx.QueryRow(ctx, `
		INSERT INTO retrieval.projection_profiles
			(organization_id, generation, manifest_key, canonicalizer_version, chunker_version,
			 tokenizer_version, semantic_enabled, embedding_provider_key, embedding_model,
			 embedding_model_version, embedding_dimensions, distance_metric, status, created_by)
		VALUES ($1::uuid, $2, $3, $4, $5, $6, $7, NULLIF($8,''), NULLIF($9,''), NULLIF($10,''),
		        NULLIF($11,0), NULLIF($12,''), 'warming', NULLIF($13,'')::uuid)
		RETURNING id::text
	`, organizationID, generation, manifestKey, canonicalizer, chunker, tokenizer,
		semanticEnabled, providerKey, model, modelVersion, dimensions, distance,
		createdBy).Scan(&profile.ID)
	if err != nil {
		return Profile{}, fmt.Errorf("create retrieval profile: %w", err)
	}
	return profile, nil
}

// ActivateProfileTx performs the atomic promotion inside a transaction that
// already holds the organization profile lock. Returns the activated profile
// id and the retired predecessor id (empty when none).
func (r ProfileRepository) ActivateProfileTx(ctx context.Context, tx pgx.Tx, organizationID, profileID, activatedBy string) (string, string, error) {
	var retiredID string
	tag, err := tx.Exec(ctx, `
		UPDATE retrieval.projection_profiles
		SET status = 'retired', retired_at = now(), revision = revision + 1
		WHERE organization_id = $1::uuid AND status = 'active' AND id <> $2::uuid
	`, organizationID, profileID)
	if err != nil {
		return "", "", fmt.Errorf("retire previous active profile: %w", err)
	}
	if tag.RowsAffected() > 0 {
		if err := tx.QueryRow(ctx, `
			SELECT id::text FROM retrieval.projection_profiles
			WHERE organization_id = $1::uuid AND status = 'retired'
			ORDER BY retired_at DESC LIMIT 1
		`, organizationID).Scan(&retiredID); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return "", "", fmt.Errorf("locate retired profile: %w", err)
		}
	}
	if _, err := tx.Exec(ctx, `
		UPDATE retrieval.projection_profiles
		SET status = 'active', activated_at = now(), activated_by = NULLIF($3,'')::uuid, revision = revision + 1
		WHERE organization_id = $1::uuid AND id = $2::uuid AND status = 'warming'
	`, organizationID, profileID, activatedBy); err != nil {
		return "", "", fmt.Errorf("activate retrieval profile: %w", err)
	}
	return profileID, retiredID, nil
}

// RetireProfileTx moves a profile straight to retired (grace cleanup follows).
func (r ProfileRepository) RetireProfileTx(ctx context.Context, tx pgx.Tx, organizationID, profileID string) error {
	_, err := tx.Exec(ctx, `
		UPDATE retrieval.projection_profiles
		SET status = 'retired', retired_at = now(), revision = revision + 1
		WHERE organization_id = $1::uuid AND id = $2::uuid AND status IN ('active','warming','failed')
	`, organizationID, profileID)
	return err
}

// MarkProfileFailedTx records the failure code on a warming profile.
func (r ProfileRepository) MarkProfileFailedTx(ctx context.Context, tx pgx.Tx, organizationID, profileID, failureCode string) error {
	_, err := tx.Exec(ctx, `
		UPDATE retrieval.projection_profiles
		SET status = 'failed', failure_code = $3, revision = revision + 1
		WHERE organization_id = $1::uuid AND id = $2::uuid AND status = 'warming'
	`, organizationID, profileID, failureCode)
	return err
}

// LockOrganizationProfilesTx takes the per-organization profile lock used by
// activation and bootstrap so two processes cannot promote two profiles.
func LockOrganizationProfilesTx(ctx context.Context, tx pgx.Tx, organizationID string) error {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, "retrieval-profile:"+organizationID); err != nil {
		return fmt.Errorf("lock retrieval profiles: %w", err)
	}
	return nil
}

// CountEligibleVersions counts the currently published asset versions whose
// bound resource model version policy enables fulltext or semantic retrieval
// with at least one enabled channel (doc §7.1).
func CountEligibleVersions(ctx context.Context, q rowQuerier, organizationID string) (int, error) {
	var count int
	err := q.QueryRow(ctx, `
		SELECT count(*)
		FROM asset.assets a
		JOIN asset.asset_versions v
			ON v.organization_id = a.organization_id AND v.id = a.current_published_version_id
		JOIN model.resource_model_versions mv
			ON mv.organization_id = a.organization_id AND mv.id = v.resource_model_version_id
		WHERE a.organization_id = $1::uuid
		  AND a.publication_status = 'published'
		  AND a.deleted_at IS NULL
		  AND a.current_published_version_id IS NOT NULL
		  AND (
		        COALESCE((mv.policy #>> '{retrieval,fulltext,enabled}')::boolean, false)
		        OR COALESCE((mv.policy #>> '{retrieval,semantic,enabled}')::boolean, false)
		      )
		  AND EXISTS (
		        SELECT 1
		        FROM jsonb_object_keys(COALESCE(mv.policy->'channels','{}'::jsonb)) AS channel
		        WHERE COALESCE((mv.policy #>>
			      ARRAY['channels', channel, 'enabled'])::boolean, false)
		      )
	`, organizationID).Scan(&count)
	return count, err
}

// CountCoveredVersions counts eligible versions that already have a ready run
// for the given profile.
func CountCoveredVersions(ctx context.Context, q rowQuerier, organizationID, profileID string) (int, error) {
	var count int
	err := q.QueryRow(ctx, `
		SELECT count(*)
		FROM asset.assets a
		JOIN asset.asset_versions v
			ON v.organization_id = a.organization_id AND v.id = a.current_published_version_id
		JOIN model.resource_model_versions mv
			ON mv.organization_id = a.organization_id AND mv.id = v.resource_model_version_id
		JOIN retrieval.projection_runs run
			ON run.organization_id = a.organization_id
			AND run.asset_version_id = v.id
			AND run.projection_profile_id = $2::uuid
			AND run.status = 'ready'
		WHERE a.organization_id = $1::uuid
		  AND a.publication_status = 'published'
		  AND a.deleted_at IS NULL
		  AND a.current_published_version_id IS NOT NULL
		  AND (
		        COALESCE((mv.policy #>> '{retrieval,fulltext,enabled}')::boolean, false)
		        OR COALESCE((mv.policy #>> '{retrieval,semantic,enabled}')::boolean, false)
		      )
		  AND EXISTS (
		        SELECT 1
		        FROM jsonb_object_keys(COALESCE(mv.policy->'channels','{}'::jsonb)) AS channel
		        WHERE COALESCE((mv.policy #>>
			      ARRAY['channels', channel, 'enabled'])::boolean, false)
		      )
	`, organizationID, profileID).Scan(&count)
	return count, err
}

func scanProfile(row pgx.Row) (Profile, error) {
	var profile Profile
	err := row.Scan(&profile.ID, &profile.OrganizationID, &profile.Generation, &profile.ManifestKey,
		&profile.CanonicalizerVersion, &profile.ChunkerVersion, &profile.TokenizerVersion,
		&profile.SemanticEnabled, &profile.EmbeddingProviderKey, &profile.EmbeddingModel,
		&profile.EmbeddingModelVersion, &profile.EmbeddingDimensions,
		&profile.DistanceMetric, &profile.Status, &profile.Revision, &profile.CreatedBy,
		&profile.CreatedAt, &profile.ActivatedBy, &profile.ActivatedAt,
		&profile.RetiredAt, &profile.FailureCode)
	if err != nil {
		return Profile{}, err
	}
	return profile, nil
}

func scanProfiles(rows pgx.Rows) ([]Profile, error) {
	profiles := make([]Profile, 0, 4)
	for rows.Next() {
		var profile Profile
		if err := rows.Scan(&profile.ID, &profile.OrganizationID, &profile.Generation, &profile.ManifestKey,
			&profile.CanonicalizerVersion, &profile.ChunkerVersion, &profile.TokenizerVersion,
			&profile.SemanticEnabled, &profile.EmbeddingProviderKey, &profile.EmbeddingModel,
			&profile.EmbeddingModelVersion, &profile.EmbeddingDimensions,
			&profile.DistanceMetric, &profile.Status, &profile.Revision, &profile.CreatedBy,
			&profile.CreatedAt, &profile.ActivatedBy, &profile.ActivatedAt,
			&profile.RetiredAt, &profile.FailureCode); err != nil {
			return nil, err
		}
		profiles = append(profiles, profile)
	}
	return profiles, rows.Err()
}

// AlgorithmVersions derives the immutable algorithm identities recorded on
// the profile from the runtime manifest.
func AlgorithmVersions(manifest EmbeddingManifest) (canonicalizer, chunker, tokenizer string) {
	tokenizer = TokenizerV1
	if manifest.Tokenizer != nil && manifest.Tokenizer.Name() != "" {
		tokenizer = manifest.Tokenizer.Name()
	}
	return CanonicalizerV1, ChunkerV1, tokenizer
}
