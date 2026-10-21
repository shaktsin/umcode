package engine

import (
	"context"
	"errors"
	"github.com/shaktsin/umcode/internal/evaluation"
	"github.com/shaktsin/umcode/internal/llm"
	"testing"
	"time"
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

type evalSilentFixture struct{ evalStreamFixture }

func (*evalSilentFixture) Stream(context.Context, llm.Credential, llm.Request) (<-chan llm.Event, error) {
	return make(chan llm.Event), nil
}
func TestEvaluationStreamSilentCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	recorded := make(chan evaluation.Attempt, 1)
	p := &evaluationProvider{inner: &evalSilentFixture{}, model: evaluation.Model{MaxOutput: 10}, synthetic: true, sink: func(a evaluation.Attempt) error { recorded <- a; return nil }}
	ch, err := p.Stream(ctx, llm.Credential{}, llm.Request{MaxTokens: 10})
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatal("silent provider ignored cancellation")
	}
	select {
	case a := <-recorded:
		if a.Outcome != "cancelled" || a.Reported {
			t.Fatal(a)
		}
	case <-time.After(time.Second):
		t.Fatal("missing terminal attempt")
	}
}

type evalLateTerminal struct {
	evalStreamFixture
	exited chan struct{}
}

func (p *evalLateTerminal) Stream(ctx context.Context, _ llm.Credential, _ llm.Request) (<-chan llm.Event, error) {
	ch := make(chan llm.Event)
	p.exited = make(chan struct{})
	go func() {
		defer close(p.exited)
		defer close(ch)
		<-ctx.Done()
		ch <- llm.Event{Type: llm.EventError, Err: ctx.Err()}
	}()
	return ch, nil
}
func TestEvaluationCancellationDrainsTerminal(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	inner := &evalLateTerminal{}
	p := &evaluationProvider{inner: inner, synthetic: true, model: evaluation.Model{MaxOutput: 10}}
	ch, err := p.Stream(ctx, llm.Credential{}, llm.Request{MaxTokens: 10})
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	for range ch {
	}
	select {
	case <-inner.exited:
	case <-time.After(time.Second):
		t.Fatal("provider stranded sending terminal event")
	}
}

func (*evalStreamFixture) RuntimeCapacity(context.Context, llm.Credential, string) (llm.RuntimeCapacity, error) {
	return llm.RuntimeCapacity{ContextWindow: 200000}, nil
}
