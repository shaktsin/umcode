package tools

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/shaktsin/umcode/internal/protocol"
)

type agentServiceSpy struct {
	calls      int
	task, call string
}

func (s *agentServiceSpy) Spawn(_ context.Context, task, call string, _ protocol.AgentRole, _ protocol.AgentAssignment) (protocol.AgentChild, error) {
	s.calls++
	s.task, s.call = task, call
	return protocol.AgentChild{ID: "child", TaskID: task}, nil
}
func (s *agentServiceSpy) Status(context.Context, string, string) (protocol.AgentChild, error) {
	return protocol.AgentChild{}, nil
}
func (s *agentServiceSpy) Wait(context.Context, string, []string) ([]protocol.AgentChild, error) {
	return nil, nil
}
func (s *agentServiceSpy) Followup(context.Context, string, string, string, string) (protocol.AgentChild, error) {
	return protocol.AgentChild{}, nil
}
func (s *agentServiceSpy) Cancel(context.Context, string, string) error { return nil }
func TestAgentToolsLifecycle(t *testing.T) {
	spy := &agentServiceSpy{}
	authorize := func(ctx context.Context) (protocol.AgentTask, error) {
		if ctx.Value("admitted") != true {
			return protocol.AgentTask{}, errors.New("not admitted")
		}
		return protocol.AgentTask{ID: "owned"}, nil
	}
	ts := NewAgentTools(spy, authorize)
	if len(ts) != 5 {
		t.Fatal("missing lifecycle tools")
	}
	for _, tool := range ts {
		var s map[string]any
		if err := json.Unmarshal(tool.Schema(), &s); err != nil || s["type"] != "object" {
			t.Fatalf("schema %s: %s", tool.Name(), tool.Schema())
		}
		for _, key := range []string{"task_id", "path", "model", "provider"} {
			if _, ok := s["properties"].(map[string]any)[key]; ok {
				t.Fatalf("authority argument %s", key)
			}
		}
		if _, err := tool.Call(t.Context(), json.RawMessage(`{}`)); err == nil {
			t.Fatalf("unguarded guessed tool %s", tool.Name())
		}
	}
	ctx := WithAgentCallID(context.WithValue(t.Context(), "admitted", true), "wire-call")
	if _, err := ts[0].Call(ctx, json.RawMessage(`{"role":"implementer","assignment":{"prompt":"work"}}`)); err != nil {
		t.Fatal(err)
	}
	if spy.calls != 1 || spy.task != "owned" || spy.call != "wire-call" {
		t.Fatalf("scope: %+v", spy)
	}
	if _, err := ts[0].Call(ctx, json.RawMessage(`{"role":"implementer","assignment":{"prompt":"work"},"model":"escape"}`)); err == nil {
		t.Fatal("unknown model argument accepted")
	}
}
