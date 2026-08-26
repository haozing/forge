package agentruntime

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	runtimetools "agentchunzhi/internal/agentruntime/tools"
	"agentchunzhi/internal/modelendpoint"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

type deterministicReActModel struct {
	mu        sync.Mutex
	toolInfos []*schema.ToolInfo
}

func (m *deterministicReActModel) Generate(_ context.Context, input []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	if len(input) == 0 {
		return nil, errors.New("empty model input")
	}
	last := input[len(input)-1]
	if last.Role == schema.Tool {
		return schema.AssistantMessage("published after approval", nil), nil
	}
	return schema.AssistantMessage("", []schema.ToolCall{{
		ID: "call-publish-1", Type: "function",
		Function: schema.FunctionCall{Name: "publish_asset", Arguments: `{"asset_id":"asset-1"}`},
	}}), nil
}

func (m *deterministicReActModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	message, err := m.Generate(ctx, input, opts...)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{message}), nil
}

func (m *deterministicReActModel) WithTools(infos []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	m.mu.Lock()
	m.toolInfos = append([]*schema.ToolInfo(nil), infos...)
	m.mu.Unlock()
	return m, nil
}

type reactResolver struct{ model model.ToolCallingChatModel }

func (r reactResolver) Resolve(context.Context, string) (ResolvedModel, error) {
	return ResolvedModel{
		EndpointID: "endpoint-react", Revision: 3, Model: r.model,
		Config: modelendpoint.RuntimeConfig{
			OrganizationID: "organization-react", ProviderType: modelendpoint.ProviderOpenAI,
			ModelName: "react-test", Options: modelendpoint.Options{EnableToolCalling: true},
			Capabilities: modelendpoint.Capabilities{ToolCalling: true},
		},
	}, nil
}

type publishTestTool struct {
	mu    sync.Mutex
	calls int
}

func (t *publishTestTool) Info(context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: "publish_asset", Desc: "Publish an approved internal asset",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"asset_id": {Type: schema.String, Required: true},
		}),
	}, nil
}

func (t *publishTestTool) InvokableRun(context.Context, string, ...tool.Option) (string, error) {
	t.mu.Lock()
	t.calls++
	t.mu.Unlock()
	return `{"ok":true}`, nil
}

type memoryCheckPointStore struct {
	mu   sync.Mutex
	data map[string][]byte
}

func newMemoryCheckPointStore() *memoryCheckPointStore {
	return &memoryCheckPointStore{data: make(map[string][]byte)}
}

func (s *memoryCheckPointStore) Set(_ context.Context, key string, value []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[key] = append([]byte(nil), value...)
	return nil
}

func (s *memoryCheckPointStore) Get(_ context.Context, key string) ([]byte, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.data[key]
	return append([]byte(nil), value...), ok, nil
}

func TestReActExecutorPersistsApprovalAndResumesWithExactInterrupt(t *testing.T) {
	backend := &publishTestTool{}
	authorizations := 0
	newRegistry := func() *runtimetools.Registry {
		registry := runtimetools.NewRegistry()
		if err := registry.Register(runtimetools.Definition{
			Name: "publish_asset", Risk: runtimetools.HighWrite,
			Capabilities: []string{"asset.publish"}, Tool: backend,
		}); err != nil {
			t.Fatal(err)
		}
		return registry
	}
	policy := runtimetools.Policy{
		AllowedCapabilities: map[string]bool{"asset.publish": true}, AllowHighWrite: true,
		MaxCalls: 12, Authorize: func(context.Context, string, runtimetools.Risk, map[string]any) error {
			authorizations++
			return nil
		},
	}
	checkpointStore := newMemoryCheckPointStore()
	req := ReActRequest{
		OrganizationID: "organization-react", RunID: "run-react", AgentApplicationID: "application-react",
		CheckpointID: "checkpoint-react", Query: "publish asset-1", ToolPolicy: policy,
		CheckPointStore: checkpointStore,
	}
	firstModel := &deterministicReActModel{}
	first := ReActExecutor{Models: reactResolver{model: firstModel}, Tools: newRegistry()}
	var firstEvents []ReActEvent
	interrupted, err := first.Execute(context.Background(), req, func(event ReActEvent) error {
		firstEvents = append(firstEvents, event)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !interrupted.Interrupted || len(interrupted.Interrupts) == 0 || backend.calls != 0 {
		t.Fatalf("expected approval interrupt before side effect: result=%+v calls=%d", interrupted, backend.calls)
	}
	var interruptID string
	for _, item := range interrupted.Interrupts {
		if item.IsRootCause {
			interruptID = item.ID
		}
	}
	if interruptID == "" {
		t.Fatal("root-cause interrupt ID was not exposed")
	}
	if len(firstEvents) < 2 || firstEvents[0].Type != "tool_started" || firstEvents[len(firstEvents)-1].Type != "waiting" {
		t.Fatalf("unexpected initial events: %+v", firstEvents)
	}

	secondModel := &deterministicReActModel{}
	second := ReActExecutor{Models: reactResolver{model: secondModel}, Tools: newRegistry()}
	var resumedEvents []ReActEvent
	completed, err := second.ResumeApproval(context.Background(), req, interruptID, runtimetools.ApprovalDecision{Approved: true}, func(event ReActEvent) error {
		resumedEvents = append(resumedEvents, event)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if completed.Interrupted || completed.Answer != "published after approval" || backend.calls != 1 {
		t.Fatalf("unexpected resumed result: result=%+v calls=%d", completed, backend.calls)
	}
	if authorizations != 2 {
		t.Fatalf("permission must be rechecked after resume, got %d checks", authorizations)
	}
	if len(resumedEvents) < 3 || resumedEvents[0].Type != "tool_finished" || resumedEvents[len(resumedEvents)-1].Type != "complete" {
		t.Fatalf("unexpected resumed events: %+v", resumedEvents)
	}
}

func TestReActExecutorRejectsEndpointWithoutToolCalling(t *testing.T) {
	resolver := reactResolver{model: &deterministicReActModel{}}
	resolved, _ := resolver.Resolve(context.Background(), "")
	resolved.Config.Capabilities.ToolCalling = false
	executor := ReActExecutor{
		Models: fixedResolvedModel{value: resolved}, Tools: runtimetools.NewRegistry(),
	}
	_, err := executor.Execute(context.Background(), ReActRequest{
		OrganizationID: "organization-react", RunID: "run", AgentApplicationID: "app",
		CheckpointID: "checkpoint", Query: "query", CheckPointStore: newMemoryCheckPointStore(),
	}, nil)
	if !errors.Is(err, ErrReActUnavailable) {
		t.Fatalf("expected unavailable model error, got %v", err)
	}
}

type fixedResolvedModel struct{ value ResolvedModel }

func (r fixedResolvedModel) Resolve(context.Context, string) (ResolvedModel, error) {
	return r.value, nil
}

func TestReActExecutorEnforcesTotalDuration(t *testing.T) {
	registry := runtimetools.NewRegistry()
	backend := &publishTestTool{}
	if err := registry.Register(runtimetools.Definition{Name: "publish_asset", Risk: runtimetools.ReadOnly, Tool: backend}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	executor := ReActExecutor{Models: reactResolver{model: &deterministicReActModel{}}, Tools: registry, MaxTotalDuration: time.Millisecond}
	_, err := executor.Execute(ctx, ReActRequest{
		OrganizationID: "organization-react", RunID: "run", AgentApplicationID: "app", CheckpointID: "checkpoint",
		Query: strings.Repeat("q", 2), CheckPointStore: newMemoryCheckPointStore(),
	}, nil)
	if err == nil {
		t.Fatal("cancelled run must not succeed")
	}
}
