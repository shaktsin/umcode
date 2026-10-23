package engine

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shaktsin/umcode/internal/delegation"
	"github.com/shaktsin/umcode/internal/llm"
	"github.com/shaktsin/umcode/internal/protocol"
)

// Keep the real child history, correction scheduling and automatic compaction
// path; replace only the provider so no paid request or network is needed.
type childCompactionProvider struct {
	hold        bool
	normalCalls atomic.Int32
	stopClosed  atomic.Bool
	entered     chan context.Context
	stopped     chan struct{}
	release     chan struct{}
}

func (*childCompactionProvider) ID() string { return "claude" }
func (*childCompactionProvider) ListModels(context.Context, llm.Credential) ([]string, error) {
	return nil, nil
}
func (p *childCompactionProvider) Stream(ctx context.Context, _ llm.Credential, req llm.Request) (<-chan llm.Event, error) {
	if reply, ok := testIntentReply(req, "explanation", "answer", ""); ok {
		return reply, nil
	}
	ch := make(chan llm.Event, 2)
	if strings.HasPrefix(req.System, "Summarize this coding conversation") || strings.HasPrefix(req.System, "Summarize conversation data") {
		select {
		case p.entered <- ctx:
		default:
		}
		if p.hold {
			go func() {
				defer func() {
					if p.stopClosed.CompareAndSwap(false, true) {
						close(p.stopped)
					}
				}()
				defer close(ch)
				select {
				case <-ctx.Done():
					ch <- llm.Event{Type: llm.EventError, Err: ctx.Err()}
				case <-p.release:
					ch <- llm.Event{Type: llm.EventTextDelta, Text: "CHILD_PRIVATE_SUMMARY"}
				}
				ch <- llm.Event{Type: llm.EventDone, Usage: llm.Usage{InputTokens: 3, OutputTokens: 1, Reported: true}}
			}()
		} else {
			ch <- llm.Event{Type: llm.EventTextDelta, Text: "CHILD_PRIVATE_SUMMARY"}
			ch <- llm.Event{Type: llm.EventDone, Usage: llm.Usage{InputTokens: 3, OutputTokens: 1, Reported: true}}
			close(ch)
			if p.stopClosed.CompareAndSwap(false, true) {
				close(p.stopped)
			}
		}
		return ch, nil
	}
	text := "corrected result"
	if p.normalCalls.Add(1) == 1 {
		text = strings.Repeat("CHILD_PRIVATE_HISTORY ", 30_000)
	}
	ch <- llm.Event{Type: llm.EventTextDelta, Text: text}
	ch <- llm.Event{Type: llm.EventDone, Usage: llm.Usage{InputTokens: 3, OutputTokens: 1, Reported: true}}
	close(ch)
	return ch, nil
}

// Direct compaction publication leaks a hidden item to this all-thread
// subscriber; passing the storage context prevents individual cancellation
// from reaching the held compaction provider and workspace finalizer.
func TestChildExecutionAutomaticCompaction(t *testing.T) {
	for _, hold := range []bool{false, true} {
		name := "hidden events"
		if hold {
			name = "individual cancellation"
		}
		t.Run(name, func(t *testing.T) {
			e, th, _, _ := delegationFixture(t)
			_, err := e.SetRoutingConfig(t.Context(), protocol.RoutingConfig{Models: []protocol.ConfiguredModel{{ID: "bounded", Provider: "claude", Model: "claude-haiku-4-5-20251001", Enabled: true}}})
			if err != nil {
				t.Fatal(err)
			}
			th, err = e.SetThreadSettings(t.Context(), protocol.ThreadSetSettingsParams{ThreadID: th.ID, Settings: protocol.ModelSelection{Provider: "claude", Model: "claude-haiku-4-5-20251001"}})
			if err != nil {
				t.Fatal(err)
			}
			task := childExecutionTask(t, e, th)
			finalized := make(chan bool, 2)
			e.DelegationWorkspaces = finalizingChildWorkspace{childWorkspaceFixture{t.TempDir()}, e, false, finalized}
			p := &childCompactionProvider{hold: hold, entered: make(chan context.Context, 1), stopped: make(chan struct{}), release: make(chan struct{})}
			defer close(p.release) // unblock the old broken path before test cleanup
			e.LLMs.Register(p)
			sub := &childSubscriber{make(chan childEvent, 256)}
			e.Bus.Add(sub)
			child, err := e.DelegationCoordinator.Spawn(t.Context(), task.ID, "spawn", protocol.AgentRoleReviewer, protocol.AgentAssignment{Prompt: "inspect"})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			if _, err = e.DelegationCoordinator.Wait(ctx, task.ID, []string{child.ID}); err != nil {
				t.Fatal(err)
			}
			if !<-finalized {
				t.Fatal("first child did not stop before finalization")
			}
			if _, err = e.DelegationCoordinator.Followup(t.Context(), task.ID, child.ID, "correction", "CORRECTION_AFTER_LARGE_HISTORY"); err != nil {
				t.Fatal(err)
			}
			var providerCtx context.Context
			select {
			case providerCtx = <-p.entered:
			case <-time.After(5 * time.Second):
				t.Fatal("real automatic compaction not reached")
			}
			if hold {
				if err = e.DelegationCoordinator.Cancel(t.Context(), task.ID, child.ID); err != nil {
					t.Fatal(err)
				}
				select {
				case <-providerCtx.Done():
				case <-time.After(time.Second):
					t.Fatal("individual child cancellation did not reach compaction provider")
				}
				select {
				case <-p.stopped:
				case <-time.After(time.Second):
					t.Fatal("compaction stream did not exit")
				}
				select {
				case okay := <-finalized:
					if !okay {
						t.Fatal("finalizer inherited cancellation or ran before actual stop")
					}
				case <-time.After(5 * time.Second):
					t.Fatal("cancelled compaction retained actual worker slot")
				}
				owner, _, ownerErr := e.DelegationCoordinator.Task(t.Context(), task, e.turnLimits)
				if ownerErr != nil || owner.Err() != nil {
					t.Fatalf("individual cancellation stopped task owner: %v", ownerErr)
				}
				if delegation.BudgetFromContext(providerCtx) == nil {
					t.Fatal("compaction lost shared budget")
				}
			}
			got, err := e.DelegationCoordinator.Wait(ctx, task.ID, []string{child.ID})
			if err != nil {
				t.Fatal(err)
			}
			if hold && got[0].State != protocol.AgentStateCancelled {
				t.Fatalf("cancelled correction %+v", got)
			}
			if !hold && (got[0].State != protocol.AgentStateCompleted || got[0].Result.Summary != "corrected result") {
				t.Fatalf("compacted correction %+v", got)
			}
			execution, err := e.Store.AgentExecution(t.Context(), task.ID, child.ID)
			if err != nil {
				t.Fatal(err)
			}
			usage, err := e.Store.UsageTotalsWhere(t.Context(), "thread_id = ? AND role = ?", execution.ThreadID, "compaction")
			if err != nil || (usage.Requests < 1 || usage.Requests > 8) {
				t.Fatalf("compaction usage not recovered after execution: %+v %v", usage, err)
			}
			items, err := e.Store.ListItems(t.Context(), execution.ThreadID, 0)
			if err != nil {
				t.Fatal(err)
			}
			summaries := 0
			for _, it := range items {
				if it.Kind == protocol.ItemContextCompaction {
					summaries++
				}
			}
			if !hold && summaries != 1 {
				t.Fatalf("internal summary count %d", summaries)
			}
			if hold && summaries != 0 {
				t.Fatal("cancelled compaction persisted a completed summary")
			}
			for len(sub.events) > 0 {
				ev := <-sub.events
				var eventThread string
				switch params := ev.params.(type) {
				case protocol.ItemEvent:
					eventThread = params.Item.ThreadID
				case protocol.ItemDelta:
					eventThread = params.ThreadID
				case protocol.TurnEvent:
					eventThread = params.Turn.ThreadID
				case protocol.RouteChangedEvent:
					eventThread = params.ThreadID
				case protocol.ThreadEvent:
					eventThread = params.Thread.ID
				}
				if eventThread == execution.ThreadID {
					t.Errorf("ordinary hidden-child event leaked: %s", ev.method)
				}
			}
		})
	}
}

func (*childCompactionProvider) RuntimeCapacity(context.Context, llm.Credential, string) (llm.RuntimeCapacity, error) {
	return llm.RuntimeCapacity{ContextWindow: 200000}, nil
}
