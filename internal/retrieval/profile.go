package retrieval

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

// sha256Hex is the shared hex SHA-256 helper for checksums/fingerprints.
func sha256Sum(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// Profile is one row of retrieval.projection_profiles. Immutable identity
// fields are fixed at creation; only status, revision and activation
// metadata may change.
type Profile struct {
	ID                    string
	OrganizationID        string
	Generation            int64
	ManifestKey           string
	CanonicalizerVersion  string
	ChunkerVersion        string
	TokenizerVersion      string
	SemanticEnabled       bool
	EmbeddingProviderKey  string
	EmbeddingModel        string
	EmbeddingModelVersion string
	EmbeddingDimensions   int
	DistanceMetric        string
	Status                string
	Revision              int64
	CreatedBy             string
	CreatedAt             time.Time
	ActivatedBy           string
	ActivatedAt           time.Time
	RetiredAt             time.Time
	FailureCode           string
}

// IsServing reports whether the profile may back queries (active) or is
// still being backfilled (warming). Runs are ensured for both.
func (p Profile) IsServing() bool {
	return p.Status == ProfileStatusActive || p.Status == ProfileStatusWarming
}

// ProfileManifestMeta renders the embedding identity columns for a manifest.
func ProfileManifestMeta(manifest EmbeddingManifest) (providerKey, model, modelVersion string, dimensions int) {
	return manifest.ProviderKey, manifest.Model, manifest.ModelVersion, manifest.Dimensions
}

// ManifestFingerprint exposes the manifest fingerprint used by readiness
// checks and worker heartbeats.
func ManifestFingerprint(manifest EmbeddingManifest) string {
	return manifest.Fingerprint()
}

// ValidateManifestForProfile verifies that the runtime manifest matches the
// immutable identity recorded on a profile. Any drift (provider key, model,
// version, dimensions, tokenizer) must fail startup/readiness instead of
// silently embedding with a different model (doc §13.1).
func ValidateManifestForProfile(profile Profile, manifest EmbeddingManifest) error {
	problems := make([]string, 0, 5)
	if profile.SemanticEnabled != (strings.TrimSpace(manifest.Key) != "") {
		problems = append(problems, fmt.Sprintf("semantic_enabled=%v does not match manifest presence", profile.SemanticEnabled))
	}
	if profile.SemanticEnabled {
		if profile.EmbeddingProviderKey != manifest.ProviderKey {
			problems = append(problems, fmt.Sprintf("provider key %q != %q", profile.EmbeddingProviderKey, manifest.ProviderKey))
		}
		if profile.EmbeddingModel != manifest.Model {
			problems = append(problems, fmt.Sprintf("model %q != %q", profile.EmbeddingModel, manifest.Model))
		}
		if profile.EmbeddingModelVersion != manifest.ModelVersion {
			problems = append(problems, fmt.Sprintf("model version %q != %q", profile.EmbeddingModelVersion, manifest.ModelVersion))
		}
		if profile.EmbeddingDimensions != manifest.Dimensions {
			problems = append(problems, fmt.Sprintf("dimensions %d != %d", profile.EmbeddingDimensions, manifest.Dimensions))
		}
		tokenizer := TokenizerV1
		if manifest.Tokenizer != nil {
			tokenizer = manifest.Tokenizer.Name()
		}
		if profile.TokenizerVersion != tokenizer {
			problems = append(problems, fmt.Sprintf("tokenizer %q != %q", profile.TokenizerVersion, tokenizer))
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("%w: %s", ErrManifestMismatch, strings.Join(problems, "; "))
	}
	return nil
}

// Run is the projection_runs row subset the pipeline mutates.
type Run struct {
	ID                     string
	OrganizationID         string
	WorkspaceID            string
	AssetID                string
	AssetVersionID         string
	ResourceModelID        string
	ResourceModelVersionID string
	ProjectionProfileID    string
	CanonicalChecksum      string
	Status                 string
	SemanticStatus         string
	ExpectedChunkCount     int
	ReadyChunkCount        int
	ExpectedEmbeddingCount int
	ReadyEmbeddingCount    int
	FailureCode            string
	FailureStage           string
	ProjectionGeneration   int64
}

// Terminal reports whether the run reached a final lifecycle state.
func (r Run) Terminal() bool {
	switch r.Status {
	case RunStatusReady, RunStatusDegraded, RunStatusFailed, RunStatusStale:
		return true
	}
	return false
}
