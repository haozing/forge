package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

type Risk string

const (
	ReadOnly  Risk = "read"
	LowWrite  Risk = "low_write"
	HighWrite Risk = "high_write"
)

var (
	ErrToolNotAllowed = errors.New("tool is not allowed")
	ErrToolBudget     = errors.New("tool call budget exceeded")
	ErrApprovalNeeded = errors.New("tool approval is required")
)

type Authorizer func(context.Context, string, Risk, map[string]any) error

type ApprovalRequest struct {
	ToolName        string `json:"tool_name"`
	ToolCallID      string `json:"tool_call_id"`
	Risk            Risk   `json:"risk"`
	ArgumentsDigest string `json:"arguments_digest"`
}

type ApprovalDecision struct {
	Approved bool   `json:"approved"`
	Reason   string `json:"reason,omitempty"`
}

type approvalState struct {
	ArgumentsInJSON string
	ArgumentsDigest string
}

type Definition struct {
	Name         string
	Risk         Risk
	Capabilities []string
	Tool         tool.BaseTool
}

type Policy struct {
	AllowedNames        map[string]bool
	AllowedCapabilities map[string]bool
	AllowLowWrite       bool
	AllowHighWrite      bool
	ApprovalRisks       map[Risk]bool
	MaxCalls            int
	UsedCalls           int
	MaxDuration         time.Duration
	Authorize           Authorizer
}

type Registry struct {
	mu          sync.RWMutex
	definitions map[string]Definition
}

func NewRegistry() *Registry {
	return &Registry{definitions: make(map[string]Definition)}
}

func (r *Registry) Register(definition Definition) error {
	if r == nil {
		return errors.New("tool registry is nil")
	}
	definition.Name = strings.TrimSpace(definition.Name)
	if definition.Name == "" || definition.Tool == nil || (definition.Risk != ReadOnly && definition.Risk != LowWrite && definition.Risk != HighWrite) {
		return errors.New("invalid tool definition")
	}
	info, err := definition.Tool.Info(context.Background())
	if err != nil {
		return fmt.Errorf("load tool %s definition: %w", definition.Name, err)
	}
	if info == nil || info.Name != definition.Name {
		return fmt.Errorf("tool definition name %s does not match implementation", definition.Name)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.definitions[definition.Name]; exists {
		return fmt.Errorf("tool %s is already registered", definition.Name)
	}
	r.definitions[definition.Name] = definition
	return nil
}

func (r *Registry) Tools(ctx context.Context, policy Policy) ([]tool.BaseTool, error) {
	if r == nil {
		return nil, errors.New("tool registry is nil")
	}
	r.mu.RLock()
	definitions := make([]Definition, 0, len(r.definitions))
	for _, definition := range r.definitions {
		definitions = append(definitions, definition)
	}
	r.mu.RUnlock()
	sort.Slice(definitions, func(i, j int) bool { return definitions[i].Name < definitions[j].Name })
	result := make([]tool.BaseTool, 0, len(definitions))
	budget := &callBudget{max: policy.MaxCalls, calls: policy.UsedCalls}
	for _, definition := range definitions {
		if !allowed(definition, policy) {
			continue
		}
		result = append(result, &guarded{definition: definition, policy: policy, budget: budget})
	}
	return result, nil
}

func allowed(definition Definition, policy Policy) bool {
	if len(policy.AllowedNames) > 0 && !policy.AllowedNames[definition.Name] {
		return false
	}
	if definition.Risk == LowWrite && !policy.AllowLowWrite {
		return false
	}
	if definition.Risk == HighWrite && !policy.AllowHighWrite {
		return false
	}
	for _, capability := range definition.Capabilities {
		if len(policy.AllowedCapabilities) > 0 && !policy.AllowedCapabilities[capability] {
			return false
		}
	}
	return true
}

type guarded struct {
	definition Definition
	policy     Policy
	budget     *callBudget
}

type callBudget struct {
	mu    sync.Mutex
	max   int
	calls int
}

func (b *callBudget) take() bool {
	if b == nil {
		return true
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.max > 0 && b.calls >= b.max {
		return false
	}
	b.calls++
	return true
}

func (g guarded) Info(ctx context.Context) (*schema.ToolInfo, error) {
	return g.definition.Tool.Info(ctx)
}

func (g *guarded) InvokableRun(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
	if g == nil {
		return "", ErrToolNotAllowed
	}
	wasInterrupted, hasState, state := tool.GetInterruptState[approvalState](ctx)
	if !wasInterrupted {
		if !g.budget.take() {
			return "", ErrToolBudget
		}
	} else if !hasState {
		return "", errors.New("tool approval checkpoint has no state")
	}
	if wasInterrupted {
		argumentsInJSON = state.ArgumentsInJSON
	}
	var arguments map[string]any
	if err := json.Unmarshal([]byte(argumentsInJSON), &arguments); err != nil {
		return "", fmt.Errorf("decode %s arguments: %w", g.definition.Name, err)
	}
	if (g.definition.Risk == LowWrite || g.definition.Risk == HighWrite) && g.policy.Authorize == nil {
		return "", ErrApprovalNeeded
	}
	if g.policy.Authorize != nil {
		if err := g.policy.Authorize(ctx, g.definition.Name, g.definition.Risk, arguments); err != nil {
			return "", err
		}
	}
	if approvalRequired(g.definition.Risk, g.policy) {
		digest := argumentDigest(argumentsInJSON)
		request := &ApprovalRequest{
			ToolName: g.definition.Name, ToolCallID: compose.GetToolCallID(ctx),
			Risk: g.definition.Risk, ArgumentsDigest: digest,
		}
		if !wasInterrupted {
			return "", tool.StatefulInterrupt(ctx, request, approvalState{
				ArgumentsInJSON: argumentsInJSON, ArgumentsDigest: digest,
			})
		}
		if state.ArgumentsDigest != digest {
			return "", errors.New("tool approval arguments checksum mismatch")
		}
		isTarget, hasDecision, decision := tool.GetResumeContext[*ApprovalDecision](ctx)
		if !isTarget {
			return "", tool.StatefulInterrupt(ctx, request, state)
		}
		if !hasDecision || decision == nil {
			return "", errors.New("tool approval resumed without a decision")
		}
		if !decision.Approved {
			result, _ := json.Marshal(map[string]any{
				"ok": false, "code": "approval_rejected", "reason": cleanReason(decision.Reason),
			})
			return string(result), nil
		}
	}
	invokable, ok := g.definition.Tool.(tool.InvokableTool)
	if !ok {
		return "", fmt.Errorf("tool %s is not invokable", g.definition.Name)
	}
	toolCtx := ctx
	var cancel context.CancelFunc
	if g.policy.MaxDuration > 0 {
		toolCtx, cancel = context.WithTimeout(ctx, g.policy.MaxDuration)
		defer cancel()
	}
	return invokable.InvokableRun(toolCtx, argumentsInJSON, opts...)
}

func approvalRequired(risk Risk, policy Policy) bool {
	if policy.ApprovalRisks != nil {
		return policy.ApprovalRisks[risk]
	}
	return risk == HighWrite
}

func argumentDigest(argumentsInJSON string) string {
	digest := sha256.Sum256([]byte(argumentsInJSON))
	return hex.EncodeToString(digest[:])
}

func cleanReason(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 500 {
		value = value[:500]
	}
	return value
}

func init() {
	schema.Register[*ApprovalRequest]()
	schema.Register[*ApprovalDecision]()
	schema.Register[approvalState]()
}

var _ tool.BaseTool = guarded{}
var _ tool.InvokableTool = (*guarded)(nil)
