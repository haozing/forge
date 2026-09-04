package agentruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"

	"agentchunzhi/internal/modelendpoint"
	"agentchunzhi/internal/store"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/jackc/pgx/v5"
)

type ResolvedModel struct {
	EndpointID string
	Revision   int64
	Model      model.ToolCallingChatModel
	Config     modelendpoint.RuntimeConfig
}

type ConfigSource interface {
	ForApplication(ctx context.Context, applicationID string) (modelendpoint.RuntimeConfig, error)
	ForEndpoint(ctx context.Context, endpointID string, revision int64) (modelendpoint.RuntimeConfig, error)
}

type ModelFactory interface {
	Build(ctx context.Context, config modelendpoint.RuntimeConfig, credential string) (model.ToolCallingChatModel, error)
}

type StructuredModelFactory interface {
	BuildStructured(ctx context.Context, config modelendpoint.RuntimeConfig, credential string, responseSchema json.RawMessage) (model.ToolCallingChatModel, error)
}

type SecretResolver interface {
	Resolve(ctx context.Context, reference string) (string, error)
}

type EnvironmentSecretResolver struct{}

func (EnvironmentSecretResolver) Resolve(_ context.Context, reference string) (string, error) {
	if !strings.HasPrefix(reference, "env://") {
		return "", errors.New("unsupported model secret reference")
	}
	name := strings.TrimPrefix(reference, "env://")
	value, ok := os.LookupEnv(name)
	if !ok || strings.TrimSpace(value) == "" {
		return "", errors.New("model secret is unavailable")
	}
	return strings.TrimSpace(value), nil
}

type ModelRegistry struct {
	Source     ConfigSource
	Cipher     *modelendpoint.CredentialCipher
	Secrets    SecretResolver
	Factory    ModelFactory
	MaxEntries int

	mu    sync.RWMutex
	cache map[string]model.ToolCallingChatModel
}

func (r *ModelRegistry) Resolve(ctx context.Context, applicationID string) (ResolvedModel, error) {
	if r == nil || r.Source == nil {
		return ResolvedModel{}, errors.New("model registry is not initialized")
	}
	config, err := r.Source.ForApplication(ctx, applicationID)
	if err != nil {
		return ResolvedModel{}, err
	}
	return r.resolve(ctx, config)
}

func (r *ModelRegistry) ResolveEndpoint(ctx context.Context, endpointID string, revision int64) (ResolvedModel, error) {
	if r == nil || r.Source == nil || strings.TrimSpace(endpointID) == "" || revision <= 0 {
		return ResolvedModel{}, errors.New("model registry is not initialized")
	}
	config, err := r.Source.ForEndpoint(ctx, endpointID, revision)
	if err != nil {
		return ResolvedModel{}, err
	}
	return r.resolve(ctx, config)
}

// ResolveOrganizationEndpoint resolves the organization's active endpoint
// by name at its current revision.
func (r *ModelRegistry) ResolveOrganizationEndpoint(ctx context.Context, organizationID, name string) (ResolvedModel, error) {
	if r == nil || r.Source == nil || strings.TrimSpace(organizationID) == "" || strings.TrimSpace(name) == "" {
		return ResolvedModel{}, errors.New("model registry is not initialized")
	}
	named, ok := r.Source.(OrganizationEndpointSource)
	if !ok {
		return ResolvedModel{}, errors.New("model config source does not support endpoint names")
	}
	config, err := named.ForOrganizationEndpoint(ctx, organizationID, name)
	if err != nil {
		return ResolvedModel{}, err
	}
	return r.resolve(ctx, config)
}

func (r *ModelRegistry) ResolveStructuredEndpoint(ctx context.Context, endpointID string, revision int64, responseSchema json.RawMessage) (ResolvedModel, error) {
	if r == nil || r.Source == nil || r.Cipher == nil || r.Factory == nil || strings.TrimSpace(endpointID) == "" || revision <= 0 {
		return ResolvedModel{}, errors.New("model registry is not initialized")
	}
	config, err := r.Source.ForEndpoint(ctx, endpointID, revision)
	if err != nil {
		return ResolvedModel{}, err
	}
	factory, ok := r.Factory.(StructuredModelFactory)
	if !ok {
		return ResolvedModel{}, errors.New("model adapter does not support structured output")
	}
	credential, err := r.resolveCredential(ctx, config)
	if err != nil {
		return ResolvedModel{}, err
	}
	chatModel, err := factory.BuildStructured(ctx, config, credential, responseSchema)
	if err != nil {
		return ResolvedModel{}, fmt.Errorf("build structured model endpoint %s revision %d: %w", config.EndpointID, config.Revision, err)
	}
	return ResolvedModel{EndpointID: config.EndpointID, Revision: config.Revision, Model: chatModel, Config: config}, nil
}

func (r *ModelRegistry) Check(ctx context.Context, endpointID string, revision int64) (modelendpoint.Capabilities, error) {
	if r == nil || r.Source == nil {
		return modelendpoint.Capabilities{}, errors.New("model registry is not initialized")
	}
	config, err := r.Source.ForEndpoint(ctx, endpointID, revision)
	if err != nil {
		return modelendpoint.Capabilities{}, err
	}
	resolved, err := r.resolve(ctx, config)
	if err != nil {
		return modelendpoint.Capabilities{}, err
	}
	if _, err := resolved.Model.Generate(ctx, []*schema.Message{schema.UserMessage("Reply exactly with OK.")}); err != nil {
		return modelendpoint.Capabilities{}, fmt.Errorf("generate health check: %w", err)
	}
	capabilities := modelendpoint.Capabilities{Generate: true}
	if config.Options.EnableStreaming {
		reader, err := resolved.Model.Stream(ctx, []*schema.Message{schema.UserMessage("Reply exactly with OK.")})
		if err != nil {
			return modelendpoint.Capabilities{}, fmt.Errorf("streaming health check: %w", err)
		}
		received := false
		for {
			message, receiveErr := reader.Recv()
			if errors.Is(receiveErr, io.EOF) {
				break
			}
			if receiveErr != nil {
				reader.Close()
				return modelendpoint.Capabilities{}, fmt.Errorf("receive streaming health check: %w", receiveErr)
			}
			received = received || message != nil
		}
		reader.Close()
		if !received {
			return modelendpoint.Capabilities{}, errors.New("streaming health check returned no messages")
		}
		capabilities.Streaming = true
	}
	if config.Options.EnableToolCalling {
		const probeTool = "agentchunzhi_capability_probe"
		toolModel, err := resolved.Model.WithTools([]*schema.ToolInfo{{Name: probeTool, Desc: "Call this tool to complete the capability probe."}})
		if err != nil {
			return modelendpoint.Capabilities{}, fmt.Errorf("bind tool calling health check: %w", err)
		}
		message, err := toolModel.Generate(ctx, []*schema.Message{schema.UserMessage("Call the capability probe tool now.")}, model.WithToolChoice(schema.ToolChoiceForced, probeTool))
		if err != nil {
			return modelendpoint.Capabilities{}, fmt.Errorf("tool calling health check: %w", err)
		}
		if message == nil || len(message.ToolCalls) == 0 || message.ToolCalls[0].Function.Name != probeTool {
			return modelendpoint.Capabilities{}, errors.New("tool calling health check returned no matching tool call")
		}
		capabilities.ToolCalling = true
	}
	if config.Options.StructuredOutputMode != "disabled" {
		probeSchema := json.RawMessage(`{"type":"object","properties":{"ok":{"type":"boolean"}},"required":["ok"],"additionalProperties":false}`)
		structured, err := r.ResolveStructuredEndpoint(ctx, endpointID, revision, probeSchema)
		if err != nil {
			return modelendpoint.Capabilities{}, fmt.Errorf("structured output health check: %w", err)
		}
		message, err := structured.Model.Generate(ctx, []*schema.Message{
			schema.SystemMessage("Return a JSON object that satisfies the supplied response schema."),
			schema.UserMessage(`Return {"ok":true}.`),
		})
		if err != nil {
			return modelendpoint.Capabilities{}, fmt.Errorf("structured output health check: %w", err)
		}
		var probe struct {
			OK bool `json:"ok"`
		}
		if message == nil || json.Unmarshal([]byte(strings.TrimSpace(visibleMessageContent(message))), &probe) != nil || !probe.OK {
			return modelendpoint.Capabilities{}, errors.New("structured output health check returned invalid JSON")
		}
		capabilities.StructuredOutput = true
	}
	return capabilities, nil
}

func (r *ModelRegistry) resolve(ctx context.Context, config modelendpoint.RuntimeConfig) (ResolvedModel, error) {
	if r.Cipher == nil || r.Factory == nil {
		return ResolvedModel{}, errors.New("model registry dependencies are not initialized")
	}
	key := fmt.Sprintf("%s:%d", config.EndpointID, config.Revision)
	r.mu.RLock()
	chatModel := r.cache[key]
	r.mu.RUnlock()
	if chatModel != nil {
		return ResolvedModel{EndpointID: config.EndpointID, Revision: config.Revision, Model: chatModel, Config: config}, nil
	}
	credential, err := r.resolveCredential(ctx, config)
	if err != nil {
		return ResolvedModel{}, err
	}
	chatModel, err = r.Factory.Build(ctx, config, credential)
	if err != nil {
		return ResolvedModel{}, fmt.Errorf("build model endpoint %s revision %d: %w", config.EndpointID, config.Revision, err)
	}
	r.mu.Lock()
	if r.cache == nil {
		r.cache = make(map[string]model.ToolCallingChatModel)
	}
	if existing := r.cache[key]; existing != nil {
		chatModel = existing
	} else {
		maxEntries := r.MaxEntries
		if maxEntries <= 0 {
			maxEntries = 100
		}
		if len(r.cache) >= maxEntries {
			for oldKey := range r.cache {
				delete(r.cache, oldKey)
				break
			}
		}
		r.cache[key] = chatModel
	}
	r.mu.Unlock()
	return ResolvedModel{EndpointID: config.EndpointID, Revision: config.Revision, Model: chatModel, Config: config}, nil
}

func (r *ModelRegistry) resolveCredential(ctx context.Context, config modelendpoint.RuntimeConfig) (string, error) {
	switch config.CredentialMode {
	case "encrypted":
		if config.CredentialKeyID != r.Cipher.KeyID() {
			return "", errors.New("model credential encryption key is unavailable")
		}
		return r.Cipher.Decrypt(config.Ciphertext, modelendpoint.CredentialAdditionalData(config.OrganizationID, config.EndpointID))
	case "secret_ref":
		if r.Secrets == nil {
			return "", errors.New("model secret resolver is not configured")
		}
		return r.Secrets.Resolve(ctx, config.SecretRef)
	default:
		return "", errors.New("unsupported model credential mode")
	}
}

type PostgresConfigSource struct {
	Store *store.Store
}

func (s PostgresConfigSource) ForApplication(ctx context.Context, applicationID string) (modelendpoint.RuntimeConfig, error) {
	if s.Store == nil || s.Store.Pool == nil {
		return modelendpoint.RuntimeConfig{}, errors.New("model config source is not initialized")
	}
	return scanRuntimeConfig(s.Store.Pool.QueryRow(ctx, runtimeConfigSelect+`
		JOIN integration.agent_applications a ON a.model_endpoint_id = e.id
		WHERE a.id = $1::uuid AND a.status = 'active' AND e.status = 'active'
		  AND r.revoked_at IS NULL
	`, applicationID))
}

func (s PostgresConfigSource) ForEndpoint(ctx context.Context, endpointID string, revision int64) (modelendpoint.RuntimeConfig, error) {
	if s.Store == nil || s.Store.Pool == nil {
		return modelendpoint.RuntimeConfig{}, errors.New("model config source is not initialized")
	}
	return scanRuntimeConfig(s.Store.Pool.QueryRow(ctx, runtimeConfigSelect+`
		WHERE e.id = $1::uuid AND r.revision = $2 AND r.revoked_at IS NULL
	`, endpointID, revision))
}

// ForOrganizationEndpoint resolves an organization's active endpoint by
// name at its current revision — the deployment-level way to designate a
// shared capability endpoint (image understanding) without a new
// management plane.
func (s PostgresConfigSource) ForOrganizationEndpoint(ctx context.Context, organizationID, name string) (modelendpoint.RuntimeConfig, error) {
	if s.Store == nil || s.Store.Pool == nil {
		return modelendpoint.RuntimeConfig{}, errors.New("model config source is not initialized")
	}
	return scanRuntimeConfig(s.Store.Pool.QueryRow(ctx, runtimeConfigSelect+`
		WHERE e.organization_id = $1::uuid AND e.name = $2 AND e.status = 'active'
		  AND r.revision = e.current_revision AND r.revoked_at IS NULL
	`, organizationID, name))
}

// OrganizationEndpointSource is implemented by config sources that can
// resolve an endpoint by organization + name.
type OrganizationEndpointSource interface {
	ForOrganizationEndpoint(ctx context.Context, organizationID, name string) (modelendpoint.RuntimeConfig, error)
}

const runtimeConfigSelect = `
	SELECT e.id::text, e.organization_id::text, r.revision, r.provider_type,
	       r.base_url, r.model_name, r.credential_mode, r.credential_ciphertext,
	       COALESCE(r.credential_key_id, ''), COALESCE(r.secret_ref, ''),
	       r.options, r.capabilities
	FROM integration.model_endpoints e
	JOIN integration.model_endpoint_revisions r ON r.model_endpoint_id = e.id
`

type configScanner interface {
	Scan(dest ...any) error
}

func scanRuntimeConfig(row configScanner) (modelendpoint.RuntimeConfig, error) {
	var config modelendpoint.RuntimeConfig
	var optionsJSON, capabilitiesJSON []byte
	if err := row.Scan(&config.EndpointID, &config.OrganizationID, &config.Revision,
		&config.ProviderType, &config.BaseURL, &config.ModelName, &config.CredentialMode,
		&config.Ciphertext, &config.CredentialKeyID, &config.SecretRef,
		&optionsJSON, &capabilitiesJSON); errors.Is(err, pgx.ErrNoRows) {
		return modelendpoint.RuntimeConfig{}, modelendpoint.ErrUnavailable
	} else if err != nil {
		return modelendpoint.RuntimeConfig{}, fmt.Errorf("load model runtime config: %w", err)
	}
	if err := json.Unmarshal(optionsJSON, &config.Options); err != nil {
		return modelendpoint.RuntimeConfig{}, fmt.Errorf("decode model runtime options: %w", err)
	}
	if err := json.Unmarshal(capabilitiesJSON, &config.Capabilities); err != nil {
		return modelendpoint.RuntimeConfig{}, fmt.Errorf("decode model runtime capabilities: %w", err)
	}
	return config, nil
}
