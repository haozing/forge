package llmrec

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// scriptedModel 是确定性内层模型：按调用序返回预设响应，并记录收到的消息。
type scriptedModel struct {
	mu        sync.Mutex
	responses []*schema.Message
	received  [][]*schema.Message
	calls     int
}

func (s *scriptedModel) Generate(_ context.Context, input []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.received = append(s.received, input)
	if s.calls >= len(s.responses) {
		return nil, errors.New("scripted model exhausted")
	}
	resp := s.responses[s.calls]
	s.calls++
	return resp, nil
}

func (s *scriptedModel) Stream(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return nil, errors.New("not implemented")
}

func (s *scriptedModel) WithTools(tools []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return s, nil
}

func userMsg(content string) *schema.Message {
	return &schema.Message{Role: schema.User, Content: content}
}

func TestRecordThenReplayRoundtrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rec.jsonl")
	inner := &scriptedModel{responses: []*schema.Message{
		schema.AssistantMessage("first answer", nil),
		schema.AssistantMessage("second answer", nil),
	}}
	recorder := NewRecorder(inner, path)
	ctx := context.Background()

	if _, err := recorder.Generate(ctx, []*schema.Message{userMsg("问题一")}); err != nil {
		t.Fatalf("record 1: %v", err)
	}
	if _, err := recorder.Generate(ctx, []*schema.Message{userMsg("问题二")}); err != nil {
		t.Fatalf("record 2: %v", err)
	}
	if inner.calls != 2 {
		t.Fatalf("inner called %d times, want 2", inner.calls)
	}

	replayer := NewReplayer(path)
	for _, tc := range []struct{ query, want string }{
		{"问题一", "first answer"},
		{"问题二", "second answer"},
	} {
		resp, err := replayer.Generate(ctx, []*schema.Message{userMsg(tc.query)})
		if err != nil {
			t.Fatalf("replay %q: %v", tc.query, err)
		}
		if resp.Content != tc.want {
			t.Fatalf("replay %q = %q, want %q", tc.query, resp.Content, tc.want)
		}
	}
	if inner.calls != 2 {
		t.Fatalf("replay must not touch inner model, calls=%d", inner.calls)
	}
}

func TestReplayerUnknownHashFails(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rec.jsonl")
	inner := &scriptedModel{responses: []*schema.Message{schema.AssistantMessage("a", nil)}}
	recorder := NewRecorder(inner, path)
	if _, err := recorder.Generate(context.Background(), []*schema.Message{userMsg("known")}); err != nil {
		t.Fatalf("record: %v", err)
	}
	replayer := NewReplayer(path)
	_, err := replayer.Generate(context.Background(), []*schema.Message{userMsg("different")})
	if err == nil {
		t.Fatal("unknown hash must error, got nil")
	}
}

func TestRecordFailureDoesNotWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rec.jsonl")
	inner := &scriptedModel{} // 无响应：Generate 必失败
	recorder := NewRecorder(inner, path)
	if _, err := recorder.Generate(context.Background(), []*schema.Message{userMsg("x")}); err == nil {
		t.Fatal("expected inner failure")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("failed call must not create recording file, stat err=%v", err)
	}
}

func TestBindToolsChangesHash(t *testing.T) {
	ctx := context.Background()
	inner := &scriptedModel{responses: []*schema.Message{schema.AssistantMessage("with tool", nil)}}
	recorder := NewRecorder(inner, filepath.Join(t.TempDir(), "rec.jsonl"))
	bound, err := recorder.WithTools([]*schema.ToolInfo{{Name: "probe"}})
	if err != nil {
		t.Fatalf("bind: %v", err)
	}
	if _, err := bound.Generate(ctx, []*schema.Message{userMsg("q")}); err != nil {
		t.Fatalf("generate bound: %v", err)
	}
	// 同样消息、无工具绑定的回放必须不命中（工具定义参与哈希）。
	unbound, err := NewReplayer(recorder.Path).Generate(ctx, []*schema.Message{userMsg("q")})
	if err == nil {
		t.Fatalf("unbound replay must miss, got %q", unbound.Content)
	}
	// 带同样工具声明的 Replayer 才命中。
	replayer, err := NewReplayer(recorder.Path).WithTools([]*schema.ToolInfo{{Name: "probe"}})
	if err != nil {
		t.Fatalf("replayer with tools: %v", err)
	}
	boundReplay, err := replayer.Generate(ctx, []*schema.Message{userMsg("q")})
	if err != nil {
		t.Fatalf("bound replay: %v", err)
	}
	if boundReplay.Content != "with tool" {
		t.Fatalf("bound replay = %q", boundReplay.Content)
	}
}

func TestStreamUnsupported(t *testing.T) {
	recorder := NewRecorder(&scriptedModel{}, filepath.Join(t.TempDir(), "rec.jsonl"))
	if _, err := recorder.Stream(context.Background(), nil); err == nil {
		t.Fatal("stream must be unsupported on recorder")
	}
	replayer := NewReplayer(filepath.Join(t.TempDir(), "missing.jsonl"))
	if _, err := replayer.Stream(context.Background(), nil); err == nil {
		t.Fatal("stream must be unsupported on replayer")
	}
}
