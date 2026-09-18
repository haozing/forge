// Package llmrec — LLM 调用的录制与回放（借鉴 agentblog infra/llm 的
// Recorder/Replayer 模式）：把真实模型调用录制为 JSONL（规范化请求哈希 →
// 响应），CI 用 Replayer 按哈希精确回放，让"真实模型行为"成为可离线回归
// 的固定资产。
//
// 用法：nightly 真模型评估时用 Recorder 包装 ToolCallingChatModel；录制
// 文件入库为测试资产；CI 用 NewReplayer 包装跑同一测试，零真实调用。
// 只覆盖 Generate；Stream 显式不支持（录制语义只对完整响应有意义）。
package llmrec

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// JSONLPair 是录制文件的一行：规范化请求哈希 + 请求消息 + 响应。
type JSONLPair struct {
	Hash     string            `json:"hash"`
	Messages []*schema.Message `json:"messages"`
	Response *schema.Message   `json:"response"`
}

var errStreamingUnsupported = errors.New("llmrec: streaming is not supported; use Generate")

// ---------- 规范化哈希 ----------

type hashedMessage struct {
	Role       string   `json:"role"`
	Content    string   `json:"content"`
	Name       string   `json:"name,omitempty"`
	ToolCallID string   `json:"tool_call_id,omitempty"`
	ToolCalls  []string `json:"tool_calls,omitempty"`
}

// hashRequest 计算一段对话 + 绑定工具声明的规范化哈希：只取影响模型输出
// 的稳定字段，忽略 temperature 等选项。
func hashRequest(messages []*schema.Message, bound []*schema.ToolInfo) string {
	out := make([]hashedMessage, 0, len(messages))
	for _, m := range messages {
		if m == nil {
			continue
		}
		h := hashedMessage{Role: string(m.Role), Content: m.Content, Name: m.Name, ToolCallID: m.ToolCallID}
		for _, tc := range m.ToolCalls {
			h.ToolCalls = append(h.ToolCalls, tc.Function.Name+"\x00"+tc.Function.Arguments)
		}
		out = append(out, h)
	}
	boundNames := make([]string, 0, len(bound))
	for _, def := range bound {
		if def == nil {
			continue
		}
		boundNames = append(boundNames, def.Name+"\x00"+def.Desc)
	}
	payload := struct {
		Messages []hashedMessage `json:"messages"`
		Bound    []string        `json:"bound,omitempty"`
	}{Messages: out, Bound: boundNames}
	raw, err := json.Marshal(payload)
	if err != nil {
		// 消息含不可序列化内容时退化为显式标记——宁可偶发回放未命中，
		// 不可静默错配。
		return "unserializable"
	}
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}

// ---------- Recorder ----------

// Recorder 包装真实模型，把每次 (messages, response) 对追加写入 JSONL。
// 失败不落盘。并发安全；BindTools 后的派生模型共享同一个录制文件。
type Recorder struct {
	Inner model.ToolCallingChatModel
	Path  string
	bound []*schema.ToolInfo
	mu    sync.Mutex
}

var _ model.ToolCallingChatModel = (*Recorder)(nil)

// NewRecorder wraps inner; the JSONL file is created on first record.
func NewRecorder(inner model.ToolCallingChatModel, path string) *Recorder {
	return &Recorder{Inner: inner, Path: path}
}

func (r *Recorder) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	resp, err := r.Inner.Generate(ctx, input, opts...)
	if err != nil {
		return nil, err // 失败不落盘
	}
	hash := hashRequest(input, r.bound)
	pair := JSONLPair{Hash: hash, Messages: input, Response: resp}
	line, err := json.Marshal(pair)
	if err != nil {
		return resp, fmt.Errorf("llmrec: marshal record: %w", err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	f, err := os.OpenFile(r.Path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return resp, fmt.Errorf("llmrec: open record file: %w", err)
	}
	defer f.Close()
	if _, err := f.Write(append(line, '\n')); err != nil {
		return resp, fmt.Errorf("llmrec: write record: %w", err)
	}
	return resp, nil
}

// Stream 不支持录制：流式响应没有单点完整语义。
func (r *Recorder) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return nil, errStreamingUnsupported
}

// BindTools 委托内层模型，并把绑定声明纳入哈希（工具定义影响输出）。
func (r *Recorder) WithTools(tools []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	bound, err := r.Inner.WithTools(tools)
	if err != nil {
		return nil, err
	}
	return &Recorder{Inner: bound, Path: r.Path, bound: tools}, nil
}

// ---------- Replayer ----------

// Replayer 从 JSONL 离线回放：按规范化请求哈希精确匹配；未命中即报错
// （宁可失败，不可返回错配响应）。
type Replayer struct {
	path  string
	mu    sync.Mutex
	load  sync.Once
	pairs map[string]*JSONLPair
	err   error
	bound []*schema.ToolInfo
}

var _ model.ToolCallingChatModel = (*Replayer)(nil)

// NewReplayer lazily loads the JSONL file on first Generate.
func NewReplayer(path string) *Replayer {
	return &Replayer{path: path}
}

// NewReplayerWithPairs builds an in-memory replayer (tests / preloaded sets).
func NewReplayerWithPairs(pairs map[string]*JSONLPair) *Replayer {
	return &Replayer{pairs: pairs}
}

func (r *Replayer) loadOnce() error {
	r.load.Do(func() {
		f, err := os.Open(r.path)
		if err != nil {
			r.err = fmt.Errorf("llmrec: open replay file: %w", err)
			return
		}
		defer f.Close()
		r.pairs = map[string]*JSONLPair{}
		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 0, 1024*1024), 32*1024*1024)
		for scanner.Scan() {
			line := scanner.Bytes()
			if len(line) == 0 {
				continue
			}
			var pair JSONLPair
			if err := json.Unmarshal(line, &pair); err != nil {
				r.err = fmt.Errorf("llmrec: decode replay line: %w", err)
				return
			}
			r.pairs[pair.Hash] = &pair
		}
		r.err = scanner.Err()
	})
	return r.err
}

func (r *Replayer) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	if err := r.loadOnce(); err != nil {
		return nil, err
	}
	hash := hashRequest(input, r.bound)
	pair, ok := r.pairs[hash]
	if !ok {
		return nil, fmt.Errorf("llmrec: no recorded response for hash %s (recording out of date?)", hash[:16])
	}
	return pair.Response, nil
}

// Stream 不支持回放。
func (r *Replayer) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return nil, errStreamingUnsupported
}

// BindTools 回放模型忽略真实绑定，但把工具声明纳入哈希以保持键一致。
func (r *Replayer) WithTools(tools []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return &Replayer{path: r.path, pairs: r.pairs, err: r.err, bound: tools}, nil
}
