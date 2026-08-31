// Package retrieval implements the projection pipeline: canonicalization,
// chunking, projection profiles, the run/head lifecycle and the fact-driven
// coordinator. Provider HTTP calls never run inside a database transaction
// and runtime code never falls back to an embedded fake embedding.
package retrieval

import (
	"errors"
	"fmt"
)

// Run lifecycle statuses (retrieval.projection_runs.status).
const (
	RunStatusQueued      = "queued"
	RunStatusBuilding    = "building"
	RunStatusLexicalSafe = "lexical_ready"
	RunStatusEmbedding   = "embedding"
	RunStatusReady       = "ready"
	RunStatusDegraded    = "degraded"
	RunStatusFailed      = "failed"
	RunStatusStale       = "stale"
)

// Profile lifecycle statuses (retrieval.projection_profiles.status).
const (
	ProfileStatusWarming = "warming"
	ProfileStatusActive  = "active"
	ProfileStatusRetired = "retired"
	ProfileStatusFailed  = "failed"
)

// Semantic sub-statuses (retrieval.projection_runs.semantic_status).
const (
	SemanticStatusDisabled = "disabled"
	SemanticStatusPending  = "pending"
	SemanticStatusReady    = "ready"
	SemanticStatusFailed   = "failed"
)

// Canonical segment source types (retrieval.chunks.source_type).
const (
	SourceTypeTitle    = "title"
	SourceTypeSummary  = "summary"
	SourceTypeMarkdown = "markdown"
	SourceTypeField    = "field"
	SourceTypeTag      = "tag"
)

// Fixed failure codes surfaced through retrieval.projection_runs.failure_code.
const (
	FailureCodeCanonicalization = "canonicalization_failed"
	FailureCodeEmbeddingFailed  = "embedding_failed"
	FailureCodeSemanticMissing  = "semantic_provider_unavailable"
)

// Domain errors.
var (
	// ErrProfileNotReady maps to HTTP 409 profile_not_ready: the warming
	// profile has not covered every eligible version yet.
	ErrProfileNotReady = errors.New("retrieval profile has not finished backfill")
	// ErrManifestMismatch reports a runtime embedding manifest that does not
	// match the profile identity; readiness must fail in that case.
	ErrManifestMismatch = errors.New("embedding manifest does not match the projection profile")
	// ErrUnknownManifestKey reports an unregistered deployment manifest key.
	ErrUnknownManifestKey = errors.New("unknown embedding manifest key")
	// ErrProfileLifecycle reports an invalid profile state transition.
	ErrProfileLifecycle = errors.New("invalid retrieval profile lifecycle transition")
	// ErrNoActiveProfile reports the absence of a serving profile.
	ErrNoActiveProfile = errors.New("organization has no active retrieval profile")
	// ErrRunNotFound reports a missing projection run.
	ErrRunNotFound = errors.New("projection run not found")
	// ErrInvalidScope reports a malformed rebuild scope.
	ErrInvalidScope = errors.New("invalid rebuild scope")
)

// ProviderError classifies embedding/reranker HTTP failures. Retryable errors
// (429/5xx/timeout) are rethrown to the queue; terminal errors (4xx schema or
// model errors) immediately mark the affected work degraded/failed.
type ProviderError struct {
	Code     string
	Terminal bool
	Err      error
}

func (e *ProviderError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("%s: %v", e.Code, e.Err)
	}
	return e.Code
}

func (e *ProviderError) Unwrap() error { return e.Err }

func retryableProviderError(code string, err error) error {
	return &ProviderError{Code: code, Terminal: false, Err: err}
}

func terminalProviderError(code string, err error) error {
	return &ProviderError{Code: code, Terminal: true, Err: err}
}

// IsTerminalProviderError reports whether err is a classified terminal
// provider failure.
func IsTerminalProviderError(err error) bool {
	var provider *ProviderError
	return errors.As(err, &provider) && provider.Terminal
}
