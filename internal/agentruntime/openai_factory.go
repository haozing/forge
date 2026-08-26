package agentruntime

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"agentchunzhi/internal/modelendpoint"

	"github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/components/model"
	einojsonschema "github.com/eino-contrib/jsonschema"
)

type OpenAIModelFactory struct {
	AllowedHosts []string
	Resolver     *net.Resolver
	Limiter      *ModelRequestLimiter
}

// ModelRequestLimiter bounds outbound model requests for one service process.
// A streaming request owns its slot until the response body is consumed or closed.
type ModelRequestLimiter struct {
	slots chan struct{}
}

func NewModelRequestLimiter(maxConcurrent int) *ModelRequestLimiter {
	if maxConcurrent <= 0 {
		maxConcurrent = 20
	}
	return &ModelRequestLimiter{slots: make(chan struct{}, maxConcurrent)}
}

func (f OpenAIModelFactory) Build(ctx context.Context, config modelendpoint.RuntimeConfig, credential string) (model.ToolCallingChatModel, error) {
	return f.build(ctx, config, credential, nil)
}

func (f OpenAIModelFactory) BuildStructured(ctx context.Context, config modelendpoint.RuntimeConfig, credential string, responseSchema json.RawMessage) (model.ToolCallingChatModel, error) {
	responseFormat, err := structuredResponseFormat(config.Options.StructuredOutputMode, responseSchema)
	if err != nil {
		return nil, err
	}
	return f.build(ctx, config, credential, responseFormat)
}

func (f OpenAIModelFactory) build(ctx context.Context, config modelendpoint.RuntimeConfig, credential string, responseFormat *openai.ChatCompletionResponseFormat) (model.ToolCallingChatModel, error) {
	if config.ProviderType != modelendpoint.ProviderOpenAI && config.ProviderType != modelendpoint.ProviderOpenAICompatible {
		return nil, errors.New("unsupported model provider")
	}
	if err := modelendpoint.ValidateBaseURL(config.BaseURL, f.AllowedHosts); err != nil {
		return nil, err
	}
	if strings.TrimSpace(credential) == "" {
		return nil, errors.New("model credential is empty")
	}
	timeout := time.Duration(config.Options.TimeoutSeconds) * time.Second
	maxTokens := config.Options.MaxOutputTokens
	transport := http.RoundTripper(&http.Transport{
		Proxy:             nil,
		DialContext:       safeDialContext(f.Resolver),
		ForceAttemptHTTP2: true,
		TLSClientConfig:   &tls.Config{MinVersion: tls.VersionTLS12},
	})
	if f.Limiter != nil {
		transport = modelLimitedRoundTripper{base: transport, limiter: f.Limiter}
	}
	chatModel, err := openai.NewChatModel(ctx, &openai.ChatModelConfig{
		APIKey:              credential,
		BaseURL:             config.BaseURL,
		Model:               config.ModelName,
		MaxCompletionTokens: &maxTokens,
		Temperature:         config.Options.Temperature,
		ResponseFormat:      responseFormat,
		ExtraFields:         modelExtraFields(config.Options),
		HTTPClient: &http.Client{
			Timeout:   timeout,
			Transport: transport,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("create Eino OpenAI ChatModel: %w", err)
	}
	return chatModel, nil
}

func modelExtraFields(options modelendpoint.Options) map[string]any {
	if options.ThinkingMode == "" {
		return nil
	}
	return map[string]any{
		"thinking": map[string]any{"type": options.ThinkingMode},
	}
}

func structuredResponseFormat(mode string, responseSchema json.RawMessage) (*openai.ChatCompletionResponseFormat, error) {
	switch mode {
	case "json_object":
		return &openai.ChatCompletionResponseFormat{Type: openai.ChatCompletionResponseFormatTypeJSONObject}, nil
	case "json_schema":
		var parsed einojsonschema.Schema
		if len(responseSchema) == 0 || json.Unmarshal(responseSchema, &parsed) != nil {
			return nil, errors.New("structured output JSON schema is invalid")
		}
		return &openai.ChatCompletionResponseFormat{
			Type: openai.ChatCompletionResponseFormatTypeJSONSchema,
			JSONSchema: &openai.ChatCompletionResponseFormatJSONSchema{
				Name: "agentchunzhi_structured_output", JSONSchema: &parsed, Strict: false,
			},
		}, nil
	case "disabled":
		return nil, errors.New("structured output is disabled")
	default:
		return nil, errors.New("unsupported structured output mode")
	}
}

type modelLimitedRoundTripper struct {
	base    http.RoundTripper
	limiter *ModelRequestLimiter
}

func (t modelLimitedRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	select {
	case t.limiter.slots <- struct{}{}:
	case <-request.Context().Done():
		return nil, request.Context().Err()
	}
	release := func() { <-t.limiter.slots }
	response, err := t.base.RoundTrip(request)
	if err != nil {
		release()
		return nil, err
	}
	if response.Body == nil {
		release()
		return response, nil
	}
	response.Body = &releaseModelResponseBody{ReadCloser: response.Body, release: release}
	return response, nil
}

type releaseModelResponseBody struct {
	io.ReadCloser
	release func()
	once    sync.Once
}

func (b *releaseModelResponseBody) Read(buffer []byte) (int, error) {
	n, err := b.ReadCloser.Read(buffer)
	if errors.Is(err, io.EOF) {
		b.once.Do(b.release)
	}
	return n, err
}

func (b *releaseModelResponseBody) Close() error {
	err := b.ReadCloser.Close()
	b.once.Do(b.release)
	return err
}

func safeDialContext(resolver *net.Resolver) func(context.Context, string, string) (net.Conn, error) {
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, fmt.Errorf("split model address: %w", err)
		}
		addresses, err := resolver.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, fmt.Errorf("resolve model address: %w", err)
		}
		for _, candidate := range addresses {
			if !candidate.IP.IsGlobalUnicast() || candidate.IP.IsPrivate() || candidate.IP.IsLoopback() || candidate.IP.IsLinkLocalUnicast() {
				continue
			}
			return dialer.DialContext(ctx, network, net.JoinHostPort(candidate.IP.String(), port))
		}
		return nil, errors.New("model endpoint resolved only to blocked addresses")
	}
}
