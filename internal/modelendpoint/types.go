package modelendpoint

import "time"

const (
	ProviderOpenAI           = "openai"
	ProviderOpenAICompatible = "openai_compatible"
)

type Options struct {
	TimeoutSeconds       int      `json:"timeout_seconds"`
	MaxInputTokens       int      `json:"max_input_tokens"`
	MaxOutputTokens      int      `json:"max_output_tokens"`
	Temperature          *float32 `json:"temperature,omitempty"`
	EnableToolCalling    bool     `json:"enable_tool_calling"`
	EnableStreaming      bool     `json:"enable_streaming"`
	StructuredOutputMode string   `json:"structured_output_mode"`
	ThinkingMode         string   `json:"thinking_mode,omitempty"`
}

func (o Options) WithDefaults(defaultTimeout int) Options {
	if defaultTimeout <= 0 {
		defaultTimeout = 120
	}
	if o.TimeoutSeconds == 0 {
		o.TimeoutSeconds = defaultTimeout
	}
	if o.MaxInputTokens == 0 {
		o.MaxInputTokens = 32000
	}
	if o.MaxOutputTokens == 0 {
		o.MaxOutputTokens = 4096
	}
	if o.StructuredOutputMode == "" {
		o.StructuredOutputMode = "json_schema"
	}
	return o
}

type Capabilities struct {
	Generate         bool `json:"generate"`
	Streaming        bool `json:"streaming"`
	ToolCalling      bool `json:"tool_calling"`
	StructuredOutput bool `json:"structured_output"`
}

type Endpoint struct {
	ID                  string       `json:"id"`
	OrganizationID      string       `json:"organization_id"`
	Name                string       `json:"name"`
	CurrentRevision     int64        `json:"current_revision"`
	Status              string       `json:"status"`
	ProviderType        string       `json:"provider_type"`
	BaseURL             string       `json:"base_url"`
	ModelName           string       `json:"model_name"`
	CredentialMode      string       `json:"credential_mode"`
	HasCredential       bool         `json:"has_credential"`
	CredentialKeyID     string       `json:"credential_key_id,omitempty"`
	SecretRef           string       `json:"secret_ref,omitempty"`
	Options             Options      `json:"options"`
	Capabilities        Capabilities `json:"capabilities"`
	ConfigChecksum      string       `json:"config_checksum"`
	LastVerifiedAt      *time.Time   `json:"last_verified_at,omitempty"`
	LastHealthErrorCode string       `json:"last_health_error_code,omitempty"`
	CreatedAt           time.Time    `json:"created_at"`
	UpdatedAt           time.Time    `json:"updated_at"`
}

type RuntimeConfig struct {
	EndpointID      string
	OrganizationID  string
	Revision        int64
	ProviderType    string
	BaseURL         string
	ModelName       string
	CredentialMode  string
	Ciphertext      []byte
	CredentialKeyID string
	SecretRef       string
	Options         Options
	Capabilities    Capabilities
}

type CreateInput struct {
	Name           string
	ProviderType   string
	BaseURL        string
	ModelName      string
	APIKey         string
	SecretRef      string
	Options        Options
	IdempotencyKey string
}

type ReplaceInput struct {
	EndpointID     string
	Name           string
	ProviderType   string
	BaseURL        string
	ModelName      string
	APIKey         string
	SecretRef      string
	Options        Options
	IdempotencyKey string
}
