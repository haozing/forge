package config

import (
	"os"
	"strconv"
	"strings"
)

type Config struct {
	Environment                     string
	HTTPAddr                        string
	DatabaseURL                     string
	MigrationPath                   string
	OSSRegion                       string
	OSSBucket                       string
	OSSEndpoint                     string
	OSSPrefix                       string
	AttachmentMaxBytes              int64
	AttachmentClamAVAddr            string
	AttachmentScanTimeoutSeconds    int
	ASREndpoint                     string
	ASRToken                        string
	ASRModel                        string
	ASRProvider                     string
	ASRRegion                       string
	ASREngine                       string
	TencentSecretID                 string
	TencentSecretKey                string
	ASRTimeoutSeconds               int
	EmbeddingEndpoint               string
	EmbeddingToken                  string
	EmbeddingModel                  string
	EmbeddingProtocol               string
	EmbeddingDimension              int
	RerankerEndpoint                string
	RerankerToken                   string
	RerankerModelVersion            string
	RerankerProtocol                string
	SearchCursorSecret              string
	AgentModelSecretEncryptionKey   string
	AgentCheckpointEncryptionKey    string
	AgentModelAllowedHosts          []string
	AgentModelDefaultTimeout        int
	AgentModelMaxCacheEntries       int
	AgentModelMaxConcurrentRequests int
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
		EmbeddingEndpoint:               os.Getenv("EMBEDDING_ENDPOINT"),
		EmbeddingToken:                  os.Getenv("EMBEDDING_TOKEN"),
		EmbeddingModel:                  envOrDefault("EMBEDDING_MODEL", "hash-embedding"),
		EmbeddingProtocol:               envOrDefault("EMBEDDING_PROTOCOL", "generic"),
		EmbeddingDimension:              int(envInt64OrDefault("EMBEDDING_DIMENSION", 1024)),
		RerankerEndpoint:                os.Getenv("RERANKER_ENDPOINT"),
		RerankerToken:                   os.Getenv("RERANKER_TOKEN"),
		RerankerModelVersion:            envOrDefault("RERANKER_MODEL_VERSION", "bge-reranker-v2-m3"),
		RerankerProtocol:                envOrDefault("RERANKER_PROTOCOL", "generic"),
		SearchCursorSecret:              envOrDefault("SEARCH_CURSOR_SECRET", "agentchunzhi-r3-cursor-secret"),
		AgentModelSecretEncryptionKey:   strings.TrimSpace(os.Getenv("AGENT_MODEL_SECRET_ENCRYPTION_KEY")),
		AgentCheckpointEncryptionKey:    strings.TrimSpace(os.Getenv("AGENT_CHECKPOINT_ENCRYPTION_KEY")),
		AgentModelAllowedHosts:          envCSV("AGENT_MODEL_ALLOWED_HOSTS"),
		AgentModelDefaultTimeout:        int(envInt64OrDefault("AGENT_MODEL_DEFAULT_TIMEOUT_SECONDS", 120)),
		AgentModelMaxCacheEntries:       int(envInt64OrDefault("AGENT_MODEL_MAX_CACHE_ENTRIES", 100)),
		AgentModelMaxConcurrentRequests: int(envInt64OrDefault("AGENT_MODEL_MAX_CONCURRENT_REQUESTS", 20)),
	}
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
