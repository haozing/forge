package workflows

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

var (
	ErrWorkflowNotFound = errors.New("workflow is not registered")
	ErrInvalidWorkflow  = errors.New("invalid workflow definition")
)

// Input is the only value a workflow receives from the coordinator. Domain
// services remain behind the workflow adapter and are never selected by data
// stored in PostgreSQL.
type Input struct {
	OrganizationID     string
	WorkspaceID        string
	RunID              string
	AgentApplicationID string
	ModelEndpointID    string
	ModelRevision      int64
	AssetIDs           []string
	Title              string
	Markdown           string
	FieldSchema        json.RawMessage
	Values             map[string]any
	// ExistingSummary is the source version's current summary; the extractor
	// compares against it so only changed summaries become suggestions.
	ExistingSummary string
	// TagCandidates is the workspace's active tag vocabulary (doc §2 D5):
	// the model may only reuse one of these keys or flag a genuinely new key
	// with IsNew.
	TagCandidates []TagCandidate
	// SourceTags are the tags already carried by the source version; the
	// prompt uses them for preserve semantics.
	SourceTags []TagCandidate
	// RelationCandidates is the retrieval-backed relatable asset whitelist
	// (doc §11.1): relation suggestions may only target these asset ids.
	// Empty means fail-closed — no relation suggestions at all.
	RelationCandidates []RelationCandidate
	// extraction carries the raw model extraction between graph nodes. It is
	// package-private plumbing and never crosses the workflow IO boundary.
	extraction *CandidateExtraction
}

// TagCandidate is one entry of the constrained tag vocabulary.
type TagCandidate struct{ Key, DisplayName string }

// RelationCandidate is one relatable asset from the retrieval whitelist.
type RelationCandidate struct{ AssetID, Title, Snippet string }

// SuggestedTag is one tag suggestion: either a vocabulary hit (IsNew=false,
// key from TagCandidates) or a proposed new key (IsNew=true, normalized).
type SuggestedTag struct {
	Key         string
	DisplayName string
	IsNew       bool
	Confidence  float64
}

// SuggestedRelation is one relation suggestion; TargetAssetID is guaranteed
// to come from the RelationCandidates whitelist and RelationType from the
// fixed five-value vocabulary.
type SuggestedRelation struct {
	TargetAssetID string
	RelationType  string
	Confidence    float64
}

// RelationTypes is the closed relation vocabulary for agent suggestions.
var RelationTypes = []string{"related_to", "references", "derived_from", "cites", "continues_from"}

type Output struct {
	WorkflowKey     string
	CodeVersion     int64
	Candidate       map[string]any
	Summary         *string
	FieldConfidence map[string]float64
	Tags            []SuggestedTag
	Relations       []SuggestedRelation
	Values          map[string]any
	InputTokens     int
	OutputTokens    int
}

// CandidateExtraction is the validated model output of the asset_prepare
// extraction node: fields plus the phase 4 suggestion slots. Every violation
// of the tag/relation whitelists is dropped at decode time, never fatal.
type CandidateExtraction struct {
	Fields          map[string]any
	Summary         *string
	FieldConfidence map[string]float64
	Tags            []SuggestedTag
	Relations       []SuggestedRelation
	InputTokens     int
	OutputTokens    int
}

type CandidateExtractor interface {
	ExtractCandidate(context.Context, Input) (CandidateExtraction, error)
}

type Runnable interface {
	Invoke(context.Context, Input) (Output, error)
}

type Definition struct {
	Key         string
	CodeVersion int64
	Checksum    string
	Run         Runnable
}

type Registry struct {
	definitions map[string]Definition
}

func NewRegistry(definitions ...Definition) (Registry, error) {
	registry := Registry{definitions: make(map[string]Definition, len(definitions))}
	for _, definition := range definitions {
		definition.Key = strings.TrimSpace(definition.Key)
		if definition.Key == "" || definition.CodeVersion <= 0 || definition.Run == nil {
			return Registry{}, ErrInvalidWorkflow
		}
		if definition.Checksum == "" {
			definition.Checksum = checksum(definition.Key, definition.CodeVersion)
		}
		if _, exists := registry.definitions[definition.Key]; exists {
			return Registry{}, fmt.Errorf("%w: duplicate key %s", ErrInvalidWorkflow, definition.Key)
		}
		registry.definitions[definition.Key] = definition
	}
	return registry, nil
}

func (r Registry) Resolve(key string) (Definition, error) {
	definition, ok := r.definitions[strings.TrimSpace(key)]
	if !ok {
		return Definition{}, ErrWorkflowNotFound
	}
	return definition, nil
}

func (r Registry) Keys() []string {
	keys := make([]string, 0, len(r.definitions))
	for key := range r.definitions {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func DefaultRegistry() (Registry, error) {
	assetPrepare, err := NewAssetPrepareGraph()
	if err != nil {
		return Registry{}, err
	}
	definitions := []Definition{{Key: "asset_prepare", CodeVersion: 1, Run: assetPrepare}}
	for _, key := range []string{"asset_publish", "asset_archive", "asset_import", "asset_reindex", "asset_transcribe", "note_sync"} {
		graph, graphErr := NewFixedWorkflowGraph(key)
		if graphErr != nil {
			return Registry{}, graphErr
		}
		definitions = append(definitions, Definition{Key: key, CodeVersion: 1, Run: graph})
	}
	return NewRegistry(definitions...)
}

func checksum(key string, version int64) string {
	digest := sha256.Sum256([]byte(fmt.Sprintf("%s:%d", key, version)))
	return hex.EncodeToString(digest[:])
}
