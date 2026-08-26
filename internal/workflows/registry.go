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
}

type Output struct {
	WorkflowKey  string
	CodeVersion  int64
	Candidate    map[string]any
	Values       map[string]any
	InputTokens  int
	OutputTokens int
}

type CandidateExtraction struct {
	Fields       map[string]any
	InputTokens  int
	OutputTokens int
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
