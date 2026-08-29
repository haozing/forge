package retrieval

import (
	"context"
	"errors"
	"fmt"

	"agentchunzhi/internal/store"
)

// ProfileService implements the profile lifecycle: create (warming),
// backfill activation with coverage gate, retire and organization bootstrap.
type ProfileService struct {
	Store *store.Store
	// Manifests maps deployment-registered manifest keys to their identity.
	// Only keys present here may be selected by organization admins.
	Manifests map[string]EmbeddingManifest
	// DefaultManifestKey is used by EnsureProfilesForOrganization.
	DefaultManifestKey string
}

// Coverage reports the backfill state of one profile.
type Coverage struct {
	EligibleVersions int
	CoveredVersions  int
	Ready            bool
}

// Create registers a new warming profile for the organization. The manifest
// key must be registered on the deployment side.
func (s ProfileService) Create(ctx context.Context, organizationID, manifestKey, createdBy string) (Profile, error) {
	if s.Store == nil || s.Store.Pool == nil {
		return Profile{}, errors.New("database store is not initialized")
	}
	manifest, ok := s.Manifests[manifestKey]
	if !ok {
		return Profile{}, fmt.Errorf("%w: %q", ErrUnknownManifestKey, manifestKey)
	}
	repo := ProfileRepository{Store: s.Store}
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return Profile{}, fmt.Errorf("begin profile create: %w", err)
	}
	defer tx.Rollback(ctx)
	if err := LockOrganizationProfilesTx(ctx, tx, organizationID); err != nil {
		return Profile{}, err
	}
	profile, err := repo.CreateProfileTx(ctx, tx, organizationID, manifestKey, true, manifest, createdBy)
	if err != nil {
		return Profile{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Profile{}, fmt.Errorf("commit profile create: %w", err)
	}
	return profile, nil
}

// Activate promotes a warming profile once every eligible version is covered
// by a ready run. Otherwise it fails with ErrProfileNotReady (HTTP 409
// profile_not_ready). The whole switch is one transaction under the
// organization profile lock.
func (s ProfileService) Activate(ctx context.Context, organizationID, profileID, activatedBy string) (Profile, error) {
	if s.Store == nil || s.Store.Pool == nil {
		return Profile{}, errors.New("database store is not initialized")
	}
	repo := ProfileRepository{Store: s.Store}
	profile, err := repo.GetProfile(ctx, organizationID, profileID)
	if err != nil {
		return Profile{}, fmt.Errorf("load retrieval profile: %w", err)
	}
	if profile.Status != ProfileStatusWarming {
		return Profile{}, fmt.Errorf("%w: profile is %s, want warming", ErrProfileLifecycle, profile.Status)
	}
	coverage, err := s.Coverage(ctx, organizationID, profileID)
	if err != nil {
		return Profile{}, err
	}
	if !coverage.Ready {
		return Profile{}, fmt.Errorf("%w: %d of %d eligible versions are ready",
			ErrProfileNotReady, coverage.CoveredVersions, coverage.EligibleVersions)
	}
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return Profile{}, fmt.Errorf("begin profile activation: %w", err)
	}
	defer tx.Rollback(ctx)
	if err := LockOrganizationProfilesTx(ctx, tx, organizationID); err != nil {
		return Profile{}, err
	}
	// Re-check the warming status under the lock: a concurrent activation may
	// have promoted or retired the profile already.
	current, err := repo.GetProfile(ctx, organizationID, profileID)
	if err != nil {
		return Profile{}, err
	}
	if current.Status != ProfileStatusWarming {
		return Profile{}, fmt.Errorf("%w: profile is %s, want warming", ErrProfileLifecycle, current.Status)
	}
	if _, _, err := repo.ActivateProfileTx(ctx, tx, organizationID, profileID, activatedBy); err != nil {
		return Profile{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Profile{}, fmt.Errorf("commit profile activation: %w", err)
	}
	return repo.GetProfile(ctx, organizationID, profileID)
}

// Retire moves a profile out of serving; grace cleanup reclaims its data.
func (s ProfileService) Retire(ctx context.Context, organizationID, profileID string) error {
	if s.Store == nil || s.Store.Pool == nil {
		return errors.New("database store is not initialized")
	}
	repo := ProfileRepository{Store: s.Store}
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin profile retire: %w", err)
	}
	defer tx.Rollback(ctx)
	if err := LockOrganizationProfilesTx(ctx, tx, organizationID); err != nil {
		return err
	}
	if err := repo.RetireProfileTx(ctx, tx, organizationID, profileID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Coverage compares the eligible published versions against the ready runs
// of one profile. Semantic-enabled profiles require full ready runs; degraded
// runs do not satisfy the activation gate (doc §9.6).
func (s ProfileService) Coverage(ctx context.Context, organizationID, profileID string) (Coverage, error) {
	if s.Store == nil || s.Store.Pool == nil {
		return Coverage{}, errors.New("database store is not initialized")
	}
	eligible, err := CountEligibleVersions(ctx, s.Store.Pool, organizationID)
	if err != nil {
		return Coverage{}, fmt.Errorf("count eligible versions: %w", err)
	}
	covered, err := CountCoveredVersions(ctx, s.Store.Pool, organizationID, profileID)
	if err != nil {
		return Coverage{}, fmt.Errorf("count covered versions: %w", err)
	}
	coverage := Coverage{EligibleVersions: eligible, CoveredVersions: covered}
	coverage.Ready = eligible > 0 && covered >= eligible
	if eligible == 0 {
		// An organization without published assets is trivially covered.
		coverage.Ready = true
	}
	return coverage, nil
}

// EnsureProfilesForOrganization bootstraps the initial profile for an
// organization. When no profile exists yet it creates one from the default
// manifest: activated immediately for a fresh organization without eligible
// assets, otherwise left warming so the backfill flow can activate it. The
// wiring into the OrganizationProvisioner is optional (stage 3); the function
// is idempotent and safe to call on every organization creation.
func (s ProfileService) EnsureProfilesForOrganization(ctx context.Context, organizationID string) (Profile, bool, error) {
	if s.Store == nil || s.Store.Pool == nil {
		return Profile{}, false, errors.New("database store is not initialized")
	}
	manifestKey := s.DefaultManifestKey
	if manifestKey == "" {
		for key := range s.Manifests {
			manifestKey = key
			break
		}
	}
	manifest, ok := s.Manifests[manifestKey]
	if !ok {
		return Profile{}, false, fmt.Errorf("%w: default key %q", ErrUnknownManifestKey, manifestKey)
	}
	repo := ProfileRepository{Store: s.Store}
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return Profile{}, false, fmt.Errorf("begin profile bootstrap: %w", err)
	}
	defer tx.Rollback(ctx)
	if err := LockOrganizationProfilesTx(ctx, tx, organizationID); err != nil {
		return Profile{}, false, err
	}
	var existing int
	if err := tx.QueryRow(ctx, `
		SELECT count(*) FROM retrieval.projection_profiles
		WHERE organization_id = $1::uuid AND status IN ('active','warming')
	`, organizationID).Scan(&existing); err != nil {
		return Profile{}, false, fmt.Errorf("count organization profiles: %w", err)
	}
	if existing > 0 {
		if err := tx.Rollback(ctx); err != nil {
			return Profile{}, false, err
		}
		profile, err := repo.GetActiveProfile(ctx, organizationID)
		if errors.Is(err, ErrNoActiveProfile) {
			return Profile{}, false, nil
		}
		return profile, false, err
	}
	eligible, err := CountEligibleVersions(ctx, tx, organizationID)
	if err != nil {
		return Profile{}, false, fmt.Errorf("count eligible versions: %w", err)
	}
	profile, err := repo.CreateProfileTx(ctx, tx, organizationID, manifestKey, true, manifest, "")
	if err != nil {
		return Profile{}, false, err
	}
	activated := false
	if eligible == 0 {
		if _, _, err := repo.ActivateProfileTx(ctx, tx, organizationID, profile.ID, ""); err != nil {
			return Profile{}, false, err
		}
		activated = true
	}
	if err := tx.Commit(ctx); err != nil {
		return Profile{}, false, fmt.Errorf("commit profile bootstrap: %w", err)
	}
	if !activated {
		profile, err = repo.GetProfile(ctx, organizationID, profile.ID)
		if err != nil {
			return Profile{}, false, err
		}
	}
	return profile, activated, nil
}

// StartValidation reports whether a freshly built runtime manifest may serve
// the given profile. It is the readiness hook shared by api and worker.
func StartValidation(profile Profile, manifest EmbeddingManifest) error {
	return ValidateManifestForProfile(profile, manifest)
}
