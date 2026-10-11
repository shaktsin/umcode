package engine

import (
	"context"
	"errors"
	"github.com/shaktsin/umcode/internal/evaluation"
	"github.com/shaktsin/umcode/internal/llm"
	"testing"
)

type evalStreamFixture struct {
	events []llm.Event
	err    error
}

func (*evalStreamFixture) ID() string { return "claude" }
func (*evalStreamFixture) ListModels(context.Context, llm.Credential) ([]string, error) {
	return nil, nil
}
func (p *evalStreamFixture) Stream(ctx context.Context, _ llm.Credential, _ llm.Request) (<-chan llm.Event, error) {
	if p.err != nil {
		return nil, p.err
	}
	ch := make(chan llm.Event, len(p.events))
	for _, e := range p.events {
		ch <- e
	}
	close(ch)
	return ch, nil
}
func TestEvaluationStreamForwarding(t *testing.T) {
	for _, tc := range []struct {
		name     string
		events   []llm.Event
		err      error
		reported bool
		rounds   int64
	}{{"immediate", nil, errors.New("failure"), false, 0}, {"partial", []llm.Event{{Type: llm.EventTextDelta, Text: "partial"}, {Type: llm.EventToolCall, ToolCall: &llm.ToolCall{Name: "denied"}}, {Type: llm.EventToolCall, ToolCall: &llm.ToolCall{Name: "discovery"}}, {Type: llm.EventError, Err: errors.New("stream failure")}}, nil, false, 1}, {"done", []llm.Event{{Type: llm.EventDone, Usage: llm.Usage{Reported: true, InputTokens: 100, OutputTokens: 20}}}, nil, true, 0}} {
		t.Run(tc.name, func(t *testing.T) {
			var attempts []evaluation.Attempt
			provider := &evaluationProvider{inner: &evalStreamFixture{events: tc.events, err: tc.err}, model: evaluation.Model{ID: "model", Window: 1000, MaxOutput: 100, Price: evaluation.Price{ID: "p", Input: 1, Output: 2}}, synthetic: true, sink: func(a evaluation.Attempt) error { attempts = append(attempts, a); return nil }}
			ch, err := provider.Stream(t.Context(), llm.Credential{}, llm.Request{Model: "model", MaxTokens: 100})
			if tc.err != nil && err == nil {
				t.Fatal("error swallowed")
			}
			var n int
			if ch != nil {
				for range ch {
					n++
				}
			}
			if n != len(tc.events) || len(attempts) != 1 || attempts[0].Reported != tc.reported || attempts[0].ToolRounds != tc.rounds {
				t.Fatal(n, attempts)
			}
		})
	}
}
