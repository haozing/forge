package config

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// Phase 1 mailer providers.
const (
	MailerProviderSMTP    = "smtp"
	MailerProviderCapture = "capture"
)

// DefaultEmbeddingDimensions is the fixed semantic ABI (doc §13.1).
const DefaultEmbeddingDimensions = 1024

type Config struct {
	Environment                  string
	HTTPAddr                     string
	DatabaseURL                  string
	MigrationPath                string
	OSSRegion                    string
	OSSBucket                    string
	OSSEndpoint                  string
	OSSPrefix                    string
	AttachmentMaxBytes           int64
	AttachmentClamAVAddr         string
	AttachmentScanTimeoutSeconds int
	ASREndpoint                  string
	ASRToken                     string
	ASRModel                     string
	ASRProvider                  string
	ASRRegion                    string
	ASREngine                    string
	TencentSecretID              string
	TencentSecretKey             string
	ASRTimeoutSeconds            int
	EmbeddingEndpoint            string
	EmbeddingToken               string
	EmbeddingModel               string
	EmbeddingProtocol            string
	EmbeddingDimension           int
	RerankerEndpoint             string
	RerankerToken                string
	RerankerModelVersion         string
	RerankerProtocol             string
	SearchCursorSecret           string
	// Phase 3 retrieval runtime configuration.
	QueryHashSecret                 string
	EmbeddingManifestKey            string
	EmbeddingModelVersion           string
	RerankerModel                   string
	RetrievalEmbeddingAllowlist     []string
	RetrievalSessionTTL             time.Duration
	RetrievalQueryTimeout           time.Duration
	AgentModelSecretEncryptionKey   string
	AgentCheckpointEncryptionKey    string
	AgentModelAllowedHosts          []string
	AgentModelAllowPrivateEgress     bool
	AgentModelDefaultTimeout        int
	AgentModelMaxCacheEntries       int
	AgentModelMaxConcurrentRequests int
	// Phase 1 member surface configuration.
	MemberAllowedOrigins           []string
	PublicAppBaseURL               string
	RateLimitHMACKey               string
	EmailDeliveryKeys              string
	EmailDeliveryCurrentKeyVersion string
	MailerProvider                 string
	MailFrom                       string
	SMTPHost                       string
	SMTPPort                       string
	SMTPUsername                   string
	SMTPPassword                   string
	TrustedProxyCIDRs              []string
}

func Load() Config {
	return Config{
		Environment:                     envOrDefault("APP_ENV", "development"),
		HTTPAddr:                        envOrDefault("HTTP_ADDR", ":8080"),
		DatabaseURL:                     os.Getenv("DATABASE_URL"),
		MigrationPath:                   envOrDefault("MIGRATION_PATH", "db/migrations"),
		OSSRegion:                       os.Getenv("OSS_REGION"),
		OSSBucket:                       os.Getenv("OSS_BUCKET"),
		OSSEndpoint:                     os.Getenv("OSS_ENDPOINT"),
		OSSPrefix:                       envOrDefault("OSS_PREFIX", "attachments/"),
		AttachmentMaxBytes:              envInt64OrDefault("ATTACHMENT_MAX_BYTES", 50*1024*1024),
		AttachmentClamAVAddr:            strings.TrimSpace(os.Getenv("ATTACHMENT_CLAMAV_ADDR")),
		AttachmentScanTimeoutSeconds:    int(envInt64OrDefault("ATTACHMENT_SCAN_TIMEOUT_SECONDS", 120)),
		ASREndpoint:                     os.Getenv("ASR_ENDPOINT"),
		ASRToken:                        os.Getenv("ASR_TOKEN"),
		ASRModel:                        envOrDefault("ASR_MODEL", "whisper-1"),
		ASRProvider:                     envOrDefault("ASR_PROVIDER", "http"),
		ASRRegion:                       envOrDefault("ASR_REGION", "ap-beijing"),
		ASREngine:                       envOrDefault("ASR_ENGINE", "16k_zh"),
		TencentSecretID:                 os.Getenv("TENCENTCLOUD_SECRET_ID"),
		TencentSecretKey:                os.Getenv("TENCENTCLOUD_SECRET_KEY"),
		ASRTimeoutSeconds:               int(envInt64OrDefault("ASR_TIMEOUT_SECONDS", 300)),
		EmbeddingEndpoint:               strings.TrimSpace(os.Getenv("RETRIEVAL_EMBEDDING_ENDPOINT")),
		EmbeddingToken:                  os.Getenv("RETRIEVAL_EMBEDDING_TOKEN"),
		EmbeddingModel:                  strings.TrimSpace(os.Getenv("RETRIEVAL_EMBEDDING_MODEL")),
		EmbeddingModelVersion:           strings.TrimSpace(os.Getenv("RETRIEVAL_EMBEDDING_MODEL_VERSION")),
		EmbeddingManifestKey:            strings.TrimSpace(os.Getenv("RETRIEVAL_EMBEDDING_MANIFEST_KEY")),
		EmbeddingProtocol:               envOrDefault("RETRIEVAL_EMBEDDING_PROTOCOL", "generic"),
		EmbeddingDimension:              int(envInt64OrDefault("RETRIEVAL_EMBEDDING_DIMENSIONS", DefaultEmbeddingDimensions)),
		RerankerEndpoint:                strings.TrimSpace(os.Getenv("RETRIEVAL_RERANKER_ENDPOINT")),
		RerankerToken:                   os.Getenv("RETRIEVAL_RERANKER_TOKEN"),
		RerankerModel:                   strings.TrimSpace(os.Getenv("RETRIEVAL_RERANKER_MODEL")),
		RerankerModelVersion:            strings.TrimSpace(os.Getenv("RETRIEVAL_RERANKER_MODEL_VERSION")),
		RerankerProtocol:                strings.TrimSpace(envOrDefault("RETRIEVAL_RERANKER_PROTOCOL", "generic")),
		SearchCursorSecret:              os.Getenv("SEARCH_CURSOR_SECRET"),
		QueryHashSecret:                 os.Getenv("QUERY_HASH_SECRET"),
		RetrievalEmbeddingAllowlist:     envCSV("RETRIEVAL_EMBEDDING_ALLOWLIST"),
		RetrievalSessionTTL:             envDurationOrDefault("RETRIEVAL_SESSION_TTL", 10*time.Minute),
		RetrievalQueryTimeout:           envDurationOrDefault("RETRIEVAL_QUERY_TIMEOUT", 5*time.Second),
		AgentModelSecretEncryptionKey:   strings.TrimSpace(os.Getenv("AGENT_MODEL_SECRET_ENCRYPTION_KEY")),
		AgentCheckpointEncryptionKey:    strings.TrimSpace(os.Getenv("AGENT_CHECKPOINT_ENCRYPTION_KEY")),
		AgentModelAllowedHosts:          envCSV("AGENT_MODEL_ALLOWED_HOSTS"),
		AgentModelAllowPrivateEgress:    strings.EqualFold(strings.TrimSpace(os.Getenv("AGENT_MODEL_ALLOW_PRIVATE_EGRESS")), "true"),
		AgentModelDefaultTimeout:        int(envInt64OrDefault("AGENT_MODEL_DEFAULT_TIMEOUT_SECONDS", 120)),
		AgentModelMaxCacheEntries:       int(envInt64OrDefault("AGENT_MODEL_MAX_CACHE_ENTRIES", 100)),
		AgentModelMaxConcurrentRequests: int(envInt64OrDefault("AGENT_MODEL_MAX_CONCURRENT_REQUESTS", 20)),

		MemberAllowedOrigins:           envCSV("MEMBER_ALLOWED_ORIGINS"),
		PublicAppBaseURL:               strings.TrimRight(strings.TrimSpace(os.Getenv("PUBLIC_APP_BASE_URL")), "/"),
		RateLimitHMACKey:               os.Getenv("RATE_LIMIT_HMAC_KEY"),
		EmailDeliveryKeys:              os.Getenv("EMAIL_DELIVERY_KEYS"),
		EmailDeliveryCurrentKeyVersion: strings.TrimSpace(os.Getenv("EMAIL_DELIVERY_CURRENT_KEY_VERSION")),
		MailerProvider:                 strings.ToLower(strings.TrimSpace(envOrDefault("MAILER_PROVIDER", MailerProviderCapture))),
		MailFrom:                       strings.TrimSpace(os.Getenv("MAIL_FROM")),
		SMTPHost:                       strings.TrimSpace(os.Getenv("SMTP_HOST")),
		SMTPPort:                       strings.TrimSpace(envOrDefault("SMTP_PORT", "587")),
		SMTPUsername:                   strings.TrimSpace(os.Getenv("SMTP_USERNAME")),
		SMTPPassword:                   os.Getenv("SMTP_PASSWORD"),
		TrustedProxyCIDRs:              envCSV("TRUSTED_PROXY_CIDRS"),
	}
}

// IsProduction reports whether the process runs with production enforcement.
func (c Config) IsProduction() bool {
	return strings.EqualFold(strings.TrimSpace(c.Environment), "production")
}

// EmailKeyRing parses EMAIL_DELIVERY_KEYS ("1:<base64 32 bytes>,2:<...>") into
// the versioned key ring. The current version is EMAIL_DELIVERY_CURRENT_KEY_VERSION
// or, when unset, the first version listed.
func (c Config) EmailKeyRing() (map[string][]byte, string, error) {
	keys := make(map[string][]byte)
	firstVersion := ""
	for _, part := range strings.Split(c.EmailDeliveryKeys, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		version, raw, ok := strings.Cut(part, ":")
		version = strings.TrimSpace(version)
		raw = strings.TrimSpace(raw)
		if !ok || version == "" || raw == "" {
			return nil, "", fmt.Errorf("EMAIL_DELIVERY_KEYS entry %q must use VERSION:<base64 32-byte key>", part)
		}
		key, err := base64.StdEncoding.DecodeString(raw)
		if err != nil {
			return nil, "", fmt.Errorf("EMAIL_DELIVERY_KEYS version %s is not valid base64: %w", version, err)
		}
		if len(key) != 32 {
			return nil, "", fmt.Errorf("EMAIL_DELIVERY_KEYS version %s must decode to 32 bytes, got %d", version, len(key))
		}
		if _, duplicated := keys[version]; duplicated {
			return nil, "", fmt.Errorf("EMAIL_DELIVERY_KEYS defines version %s twice", version)
		}
		if firstVersion == "" {
			firstVersion = version
		}
		keys[version] = key
	}
	if len(keys) == 0 {
		return nil, "", errors.New("EMAIL_DELIVERY_KEYS must define at least one key as VERSION:<base64 32-byte key>")
	}
	current := c.EmailDeliveryCurrentKeyVersion
	if current == "" {
		current = firstVersion
	}
	if _, ok := keys[current]; !ok {
		return nil, "", fmt.Errorf("EMAIL_DELIVERY_CURRENT_KEY_VERSION %q is not present in EMAIL_DELIVERY_KEYS", current)
	}
	return keys, current, nil
}

// Validate enforces the phase 1 fail-fast rules. APP_ENV=production requires
// the full member-surface configuration; development only rejects broken
// values that would never work.
func (c Config) Validate() error {
	production := c.IsProduction()

	if production {
		if len(c.MemberAllowedOrigins) == 0 {
			return errors.New("MEMBER_ALLOWED_ORIGINS is required in production")
		}
		for _, origin := range c.MemberAllowedOrigins {
			if strings.TrimSpace(origin) == "*" {
				return errors.New("MEMBER_ALLOWED_ORIGINS must not contain * in production")
			}
		}
		if !strings.EqualFold(c.MailerProvider, MailerProviderSMTP) {
			return errors.New("MAILER_PROVIDER must be smtp in production")
		}
	}

	if len(c.MemberAllowedOrigins) > 0 {
		for _, origin := range c.MemberAllowedOrigins {
			origin = strings.TrimSpace(origin)
			if origin == "*" {
				continue
			}
			parsed, err := url.Parse(origin)
			if err != nil || parsed.Scheme == "" || parsed.Host == "" {
				return fmt.Errorf("MEMBER_ALLOWED_ORIGINS entry %q must be an absolute origin", origin)
			}
		}
	}

	if strings.TrimSpace(c.PublicAppBaseURL) != "" {
		parsed, err := url.Parse(c.PublicAppBaseURL)
		if err != nil || parsed.Scheme == "" || parsed.Host == "" {
			return errors.New("PUBLIC_APP_BASE_URL must be an absolute URL")
		}
		if production && parsed.Scheme != "https" {
			return errors.New("PUBLIC_APP_BASE_URL must use https in production")
		}
	} else if production {
		return errors.New("PUBLIC_APP_BASE_URL is required in production")
	}

	if len(c.RateLimitHMACKey) > 0 && len(c.RateLimitHMACKey) < 32 {
		return errors.New("RATE_LIMIT_HMAC_KEY must be at least 32 bytes")
	}
	if production && c.AgentModelAllowPrivateEgress {
		return errors.New("AGENT_MODEL_ALLOW_PRIVATE_EGRESS must not be enabled in production")
	}
	if production && len(c.RateLimitHMACKey) == 0 {
		return errors.New("RATE_LIMIT_HMAC_KEY is required in production (at least 32 bytes)")
	}

	if _, _, err := c.EmailKeyRing(); err != nil {
		return err
	}

	switch c.MailerProvider {
	case MailerProviderSMTP:
		if c.SMTPHost == "" {
			return errors.New("SMTP_HOST is required when MAILER_PROVIDER=smtp")
		}
		if c.MailFrom == "" {
			return errors.New("MAIL_FROM is required when MAILER_PROVIDER=smtp")
		}
	case MailerProviderCapture:
		// Development/test only; production is rejected above.
	default:
		return fmt.Errorf("MAILER_PROVIDER must be %s or %s", MailerProviderSMTP, MailerProviderCapture)
	}

	// Phase 3 retrieval secrets: never a built-in default, production fails
	// fast when missing, every environment rejects weak material.
	for name, secret := range map[string]string{
		"SEARCH_CURSOR_SECRET": c.SearchCursorSecret,
		"QUERY_HASH_SECRET":    c.QueryHashSecret,
	} {
		if len(secret) > 0 && len(secret) < 32 {
			return fmt.Errorf("%s must be at least 32 bytes", name)
		}
		if production && len(secret) == 0 {
			return fmt.Errorf("%s is required in production (at least 32 random bytes)", name)
		}
	}
	if c.SearchCursorSecret == c.QueryHashSecret && len(c.QueryHashSecret) > 0 {
		return errors.New("QUERY_HASH_SECRET must differ from SEARCH_CURSOR_SECRET")
	}

	// Embedding manifest: production fails fast on missing required values;
	// incomplete manifests elsewhere only mark semantic unavailable (the
	// readiness report surfaces SemanticUnavailable()).
	if production && !c.SemanticConfigured() {
		return errors.New("RETRIEVAL_EMBEDDING_* manifest is required in production (manifest key, endpoint, token, model, model version, dimensions=1024, protocol)")
	}
	if c.SemanticConfigured() && c.EmbeddingDimension != DefaultEmbeddingDimensions {
		return fmt.Errorf("RETRIEVAL_EMBEDDING_DIMENSIONS must be %d", DefaultEmbeddingDimensions)
	}
	if partial := c.embeddingPartialFields(); len(partial) > 0 && len(partial) < 7 {
		return fmt.Errorf("RETRIEVAL_EMBEDDING_* manifest is partial: %s must all be set together", strings.Join(partial, ", "))
	}

	// Reranker is optional but must be configured as a complete group.
	if partial := c.rerankerPartialFields(); len(partial) > 0 && len(partial) < 5 {
		return fmt.Errorf("RETRIEVAL_RERANKER_* configuration is partial: %s must all be set together", strings.Join(partial, ", "))
	}
	return nil
}

// SemanticConfigured reports whether the semantic embedding manifest is
// complete enough to build a provider.
func (c Config) SemanticConfigured() bool {
	return len(c.embeddingPartialFields()) == 0 && c.EmbeddingDimension == DefaultEmbeddingDimensions
}

// SemanticUnavailable reports the readiness gap for an incomplete semantic
// manifest: not fatal, but reported by /readyz (doc §13.1).
func (c Config) SemanticUnavailable() bool { return !c.SemanticConfigured() }

func (c Config) embeddingPartialFields() []string {
	fields := make([]string, 0, 7)
	if strings.TrimSpace(c.EmbeddingManifestKey) == "" {
		fields = append(fields, "RETRIEVAL_EMBEDDING_MANIFEST_KEY")
	}
	if strings.TrimSpace(c.EmbeddingEndpoint) == "" {
		fields = append(fields, "RETRIEVAL_EMBEDDING_ENDPOINT")
	}
	if strings.TrimSpace(c.EmbeddingToken) == "" {
		fields = append(fields, "RETRIEVAL_EMBEDDING_TOKEN")
	}
	if strings.TrimSpace(c.EmbeddingModel) == "" {
		fields = append(fields, "RETRIEVAL_EMBEDDING_MODEL")
	}
	if strings.TrimSpace(c.EmbeddingModelVersion) == "" {
		fields = append(fields, "RETRIEVAL_EMBEDDING_MODEL_VERSION")
	}
	if c.EmbeddingDimension <= 0 {
		fields = append(fields, "RETRIEVAL_EMBEDDING_DIMENSIONS")
	}
	if strings.TrimSpace(c.EmbeddingProtocol) == "" {
		fields = append(fields, "RETRIEVAL_EMBEDDING_PROTOCOL")
	}
	return fields
}

func (c Config) rerankerPartialFields() []string {
	fields := make([]string, 0, 5)
	if strings.TrimSpace(c.RerankerEndpoint) == "" {
		fields = append(fields, "RETRIEVAL_RERANKER_ENDPOINT")
	}
	if strings.TrimSpace(c.RerankerToken) == "" {
		fields = append(fields, "RETRIEVAL_RERANKER_TOKEN")
	}
	if strings.TrimSpace(c.RerankerModel) == "" {
		fields = append(fields, "RETRIEVAL_RERANKER_MODEL")
	}
	if strings.TrimSpace(c.RerankerModelVersion) == "" {
		fields = append(fields, "RETRIEVAL_RERANKER_MODEL_VERSION")
	}
	if strings.TrimSpace(c.RerankerProtocol) == "" {
		fields = append(fields, "RETRIEVAL_RERANKER_PROTOCOL")
	}
	return fields
}

func envOrDefault(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func envInt64OrDefault(key string, fallback int64) int64 {
	value, err := strconv.ParseInt(os.Getenv(key), 10, 64)
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}

// envDurationOrDefault parses Go duration strings ("10m", "90s"); invalid or
// non-positive values fall back to the default.
func envDurationOrDefault(key string, fallback time.Duration) time.Duration {
	value, err := time.ParseDuration(strings.TrimSpace(os.Getenv(key)))
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}

func envCSV(key string) []string {
	parts := strings.Split(os.Getenv(key), ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if value := strings.TrimSpace(part); value != "" {
			result = append(result, value)
		}
	}
	return result
}
