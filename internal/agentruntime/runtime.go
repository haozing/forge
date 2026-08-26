package agentruntime

import (
	"context"
	"errors"
	"strings"
	"time"

	"agentchunzhi/internal/auth"
	agentquery "agentchunzhi/internal/query"
)

var (
	ErrInvalidChatRequest     = errors.New("invalid agent chat request")
	ErrUnsupportedRuntimeMode = errors.New("agent runtime mode requires a persistent run")
	ErrModelScopeMismatch     = errors.New("model endpoint organization does not match agent session")
)

type ChatMessage struct {
	Role    string
	Content string
}

type ChatRequest struct {
	OrganizationID     string
	AgentApplicationID string
	SessionID          string
	ConversationID     string
	MessageID          string
	RuntimeMode        string
	AgentPrincipal     auth.Principal
	Query              string
	History            []ChatMessage
}

type Usage struct {
	InputTokens       int `json:"input_tokens"`
	OutputTokens      int `json:"output_tokens"`
	TotalTokens       int `json:"total_tokens"`
	ReasoningTokens   int `json:"reasoning_tokens,omitempty"`
	CachedInputTokens int `json:"cached_input_tokens,omitempty"`
}

type ChatResult struct {
	Answer                 string                      `json:"answer"`
	ConversationID         string                      `json:"conversation_id,omitempty"`
	MessageID              string                      `json:"message_id"`
	References             []agentquery.AssetReference `json:"references"`
	RejectedReferenceCount int                         `json:"rejected_reference_count"`
	Usage                  Usage                       `json:"usage"`
	ModelEndpointID        string                      `json:"model_endpoint_id"`
	ModelEndpointRevision  int64                       `json:"model_endpoint_revision"`
	ProviderType           string                      `json:"provider_type"`
	ModelName              string                      `json:"model_name"`
	ModelRequestID         string                      `json:"model_request_id,omitempty"`
	RetrievalCount         int                         `json:"retrieval_count"`
	PolicyRevision         int64                       `json:"policy_revision"`
	TotalLatency           time.Duration               `json:"-"`
	FirstTokenLatency      time.Duration               `json:"-"`
}

type StreamEvent struct {
	Delta  string
	Result *ChatResult
}

type ChatRuntime interface {
	Chat(context.Context, ChatRequest) (ChatResult, error)
	StreamChat(context.Context, ChatRequest, func(StreamEvent) error) error
}

func validateChatRequest(req ChatRequest) error {
	req.Query = strings.TrimSpace(req.Query)
	if req.RuntimeMode != "rag" {
		return ErrUnsupportedRuntimeMode
	}
	if req.AgentPrincipal.UserType != "agent" || req.AgentPrincipal.UserID == "" || req.AgentPrincipal.OrganizationID == "" ||
		req.OrganizationID == "" || req.OrganizationID != req.AgentPrincipal.OrganizationID || req.AgentApplicationID == "" ||
		req.SessionID == "" || req.Query == "" || len([]rune(req.Query)) > 10000 || strings.ContainsRune(req.Query, '\x00') {
		return ErrInvalidChatRequest
	}
	return nil
}
