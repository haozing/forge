package retrieval

import (
	"fmt"
	"net/url"
	"strings"

	"agentchunzhi/internal/config"
)

// DefaultEmbeddingAllowlist is the HTTPS endpoint allowlist used when the
// configuration does not define one.
var DefaultEmbeddingAllowlist = []string{"api.openai.com", "dashscope.aliyuncs.com"}

// RegistryConfig is the deployment-side provider configuration shared by the
// API and the worker through BuildFromConfig (doc §13.1).
type RegistryConfig struct {
	ManifestKey  string
	Endpoint     string
	Token        string
	Model        string
	ModelVersion string
	Dimensions   int
	Protocol     string
	AllowHosts   []string

	Reranker RerankerConfig
}

// RerankerConfig groups the optional reranker settings; partial groups are a
// configuration error.
type RerankerConfig struct {
	Endpoint     string
	Token        string
	Model        string
	ModelVersion string
	Protocol     string
}

// RegistryFromConfig maps the phase 3 environment variables onto the
// registry configuration.
func RegistryFromConfig(cfg config.Config) RegistryConfig {
	allowHosts := cfg.RetrievalEmbeddingAllowlist
	if len(allowHosts) == 0 {
		allowHosts = DefaultEmbeddingAllowlist
	}
	return RegistryConfig{
		ManifestKey:  cfg.EmbeddingManifestKey,
		Endpoint:     cfg.EmbeddingEndpoint,
		Token:        cfg.EmbeddingToken,
		Model:        cfg.EmbeddingModel,
		ModelVersion: cfg.EmbeddingModelVersion,
		Dimensions:   cfg.EmbeddingDimension,
		Protocol:     cfg.EmbeddingProtocol,
		AllowHosts:   allowHosts,
		Reranker: RerankerConfig{
			Endpoint:     cfg.RerankerEndpoint,
			Token:        cfg.RerankerToken,
			Model:        cfg.RerankerModel,
			ModelVersion: cfg.RerankerModelVersion,
			Protocol:     cfg.RerankerProtocol,
		},
	}
}

// ValidateProviderEndpoint enforces the SSRF allowlist: only explicit HTTPS
// hosts on the deployment allowlist are reachable. Loopback HTTP endpoints
// (httptest in tests, local inference gateways) are treated as explicitly
// registered intranet addresses and permitted.
func ValidateProviderEndpoint(endpoint string, allowHosts []string) error {
	if endpoint == "" {
		return fmt.Errorf("provider endpoint is not configured")
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Host == "" {
		return fmt.Errorf("provider endpoint %q is not a valid URL", endpoint)
	}
	host := strings.ToLower(parsed.Hostname())
	if strings.EqualFold(parsed.Scheme, "http") {
		if host == "127.0.0.1" || host == "::1" || host == "localhost" {
			return nil
		}
		return fmt.Errorf("provider endpoint %q must use https", endpoint)
	}
	if !strings.EqualFold(parsed.Scheme, "https") {
		return fmt.Errorf("provider endpoint %q must use https", endpoint)
	}
	for _, allowed := range allowHosts {
		allowed = strings.ToLower(strings.TrimSpace(allowed))
		if allowed == "" {
			continue
		}
		if host == allowed || strings.HasSuffix(host, "."+allowed) {
			return nil
		}
	}
	return fmt.Errorf("provider endpoint host %q is not on the allowlist", host)
}

// SemanticConfigured reports whether the embedding manifest is complete.
func (c RegistryConfig) SemanticConfigured() bool {
	return strings.TrimSpace(c.ManifestKey) != "" &&
		strings.TrimSpace(c.Endpoint) != "" &&
		strings.TrimSpace(c.Model) != "" &&
		strings.TrimSpace(c.ModelVersion) != "" &&
		strings.TrimSpace(c.Protocol) != "" &&
		c.Dimensions == DefaultEmbeddingDimensions
}

// BuildFromConfig resolves the embedding/reranker providers from the phase 3
// configuration. semanticAvailable reports whether the semantic manifest is
// complete and validated; partial manifests yield (nil, false, reranker,
// nil) so structured/fulltext keep working while readiness reports the gap.
func BuildFromConfig(cfg config.Config) (EmbeddingProvider, bool, RerankerProvider, error) {
	registry := RegistryFromConfig(cfg)

	reranker, err := buildReranker(registry)
	if err != nil {
		return nil, false, nil, err
	}
	if !registry.SemanticConfigured() {
		return nil, false, reranker, nil
	}
	manifest, err := registry.Manifest()
	if err != nil {
		return nil, false, reranker, err
	}
	provider, err := NewHTTPEmbeddingProvider(manifest, registry.Endpoint, registry.Token, registry.Protocol, registry.AllowHosts)
	if err != nil {
		return nil, false, reranker, err
	}
	return provider, true, reranker, nil
}

// Manifest renders the registry embedding identity.
func (c RegistryConfig) Manifest() (EmbeddingManifest, error) {
	manifest, err := EmbeddingManifest{
		Key:          c.ManifestKey,
		ProviderKey:  ProviderKeyFromEndpoint(c.Endpoint),
		Model:        c.Model,
		ModelVersion: c.ModelVersion,
		Dimensions:   c.Dimensions,
		Tokenizer:    NewWordTokenizer(),
		MaxTokens:    MaxChunkTokens,
	}.Normalize()
	if err != nil {
		return manifest, fmt.Errorf("embedding manifest is invalid: %w", err)
	}
	return manifest, nil
}

// RegisteredManifests returns the deployment manifest map used to validate
// profile creation keys.
func (c RegistryConfig) RegisteredManifests() (map[string]EmbeddingManifest, error) {
	if !c.SemanticConfigured() {
		return map[string]EmbeddingManifest{}, nil
	}
	manifest, err := c.Manifest()
	if err != nil {
		return nil, err
	}
	return map[string]EmbeddingManifest{manifest.Key: manifest}, nil
}

// ProviderKeyFromEndpoint derives the stable provider identity recorded on
// profiles from the endpoint host.
func ProviderKeyFromEndpoint(endpoint string) string {
	parsed, err := url.Parse(strings.TrimSpace(endpoint))
	if err != nil || parsed.Host == "" {
		return "http"
	}
	return strings.ToLower(parsed.Hostname())
}

func buildReranker(registry RegistryConfig) (RerankerProvider, error) {
	group := []string{
		registry.Reranker.Endpoint, registry.Reranker.Token, registry.Reranker.Model,
		registry.Reranker.ModelVersion, registry.Reranker.Protocol,
	}
	configured := 0
	for _, value := range group {
		if strings.TrimSpace(value) != "" {
			configured++
		}
	}
	if configured == 0 {
		return nil, nil
	}
	if configured < len(group) {
		return nil, fmt.Errorf("RERANKER configuration is partial: all RETRIEVAL_RERANKER_* variables must be set together")
	}
	manifest := RerankerManifest{
		Key:          fmt.Sprintf("%s@%s", registry.Reranker.Model, registry.Reranker.ModelVersion),
		ProviderKey:  ProviderKeyFromEndpoint(registry.Reranker.Endpoint),
		Model:        registry.Reranker.Model,
		ModelVersion: registry.Reranker.ModelVersion,
	}
	return NewHTTPRerankerProvider(manifest, registry.Reranker.Endpoint, registry.Reranker.Token,
		registry.Reranker.Protocol, registry.AllowHosts)
}
