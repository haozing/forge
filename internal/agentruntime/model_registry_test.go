package agentruntime

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"testing"

	"agentchunzhi/internal/modelendpoint"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

type fakeConfigSource struct {
	config modelendpoint.RuntimeConfig
}

func (f fakeConfigSource) ForApplication(context.Context, string) (modelendpoint.RuntimeConfig, error) {
	return f.config, nil
}

func (f fakeConfigSource) ForEndpoint(context.Context, string, int64) (modelendpoint.RuntimeConfig, error) {
	return f.config, nil
}

type fakeFactory struct {
	builds *int
}

func (f fakeFactory) Build(context.Context, modelendpoint.RuntimeConfig, string) (model.ToolCallingChatModel, error) {
	*f.builds++
	return fakeChatModel{}, nil
}

type fakeChatModel struct{}

func (fakeChatModel) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	return schema.AssistantMessage("OK", nil), nil
}

func (fakeChatModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return nil, nil
}

func (fakeChatModel) WithTools([]*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return fakeChatModel{}, nil
}

type capabilityFactory struct{}

func (capabilityFactory) Build(context.Context, modelendpoint.RuntimeConfig, string) (model.ToolCallingChatModel, error) {
	return capabilityChatModel{}, nil
}

func (capabilityFactory) BuildStructured(context.Context, modelendpoint.RuntimeConfig, string, json.RawMessage) (model.ToolCallingChatModel, error) {
	return capabilityChatModel{structured: true}, nil
}

type capabilityChatModel struct {
	toolName   string
	structured bool
}

func (m capabilityChatModel) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	if m.structured {
		return schema.AssistantMessage(`{"ok":true}`, nil), nil
	}
	if m.toolName != "" {
		return schema.AssistantMessage("", []schema.ToolCall{{
			ID: "probe", Type: "function", Function: schema.FunctionCall{Name: m.toolName, Arguments: `{}`},
		}}), nil
	}
	return schema.AssistantMessage("OK", nil), nil
}

func (capabilityChatModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("OK", nil)}), nil
}

func (m capabilityChatModel) WithTools(tools []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	if len(tools) > 0 {
		m.toolName = tools[0].Name
	}
	return m, nil
}

func TestModelRegistryCachesByEndpointRevision(t *testing.T) {
	key := base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
	cipher, err := modelendpoint.NewCredentialCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	config := modelendpoint.RuntimeConfig{
		EndpointID: "endpoint-1", OrganizationID: "org-1", Revision: 1,
		CredentialMode: "encrypted", CredentialKeyID: cipher.KeyID(),
	}
	config.Ciphertext, err = cipher.Encrypt("secret", modelendpoint.CredentialAdditionalData(config.OrganizationID, config.EndpointID))
	if err != nil {
		t.Fatal(err)
	}
	builds := 0
	registry := &ModelRegistry{Source: fakeConfigSource{config}, Cipher: cipher, Factory: fakeFactory{&builds}}
	if _, err := registry.Resolve(context.Background(), "application-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Resolve(context.Background(), "application-1"); err != nil {
		t.Fatal(err)
	}
	if builds != 1 {
		t.Fatalf("expected one build, got %d", builds)
	}
}

func TestModelRegistryMeasuresConfiguredCapabilities(t *testing.T) {
	key := base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
	cipher, err := modelendpoint.NewCredentialCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	config := modelendpoint.RuntimeConfig{
		EndpointID: "endpoint-1", OrganizationID: "org-1", Revision: 2,
		CredentialMode: "encrypted", CredentialKeyID: cipher.KeyID(),
		Options: modelendpoint.Options{
			EnableStreaming: true, EnableToolCalling: true, StructuredOutputMode: "json_object",
		},
	}
	config.Ciphertext, err = cipher.Encrypt("secret", modelendpoint.CredentialAdditionalData(config.OrganizationID, config.EndpointID))
	if err != nil {
		t.Fatal(err)
	}
	registry := &ModelRegistry{Source: fakeConfigSource{config}, Cipher: cipher, Factory: capabilityFactory{}}
	capabilities, err := registry.Check(context.Background(), config.EndpointID, config.Revision)
	if err != nil {
		t.Fatalf("measure endpoint capabilities: %v", err)
	}
	if !capabilities.Generate || !capabilities.Streaming || !capabilities.ToolCalling || !capabilities.StructuredOutput {
		t.Fatalf("measured capabilities = %+v", capabilities)
	}
}
