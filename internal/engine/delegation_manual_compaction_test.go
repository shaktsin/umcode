package engine

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/shaktsin/umcode/internal/delegation"
	"github.com/shaktsin/umcode/internal/llm"
	"github.com/shaktsin/umcode/internal/protocol"
)

func TestManualCompactionDelegationBudget(t *testing.T) {
	for _, mode := range []string{"success", "exhausted", "stopped", "missing_ledger", "foreign_project", "ordinary"} {
		t.Run(mode, func(t *testing.T) {
			e, th, _, _ := delegationFixture(t)
			var task protocol.AgentTask
			var shared *delegation.Budget
			if mode == "ordinary" {
				delegationTurn(t, e, protocol.TurnStartParams{ThreadID: th.ID, Text: "ordinary conversation"})
			} else {
				task = childExecutionTask(t, e, th)
				var err error
				_, shared, err = e.DelegationCoordinator.Task(t.Context(), task, protocol.ExecutionLimits{})
				if err != nil {
					t.Fatal(err)
				}
			}
			switch mode {
			case "exhausted":
				settle, err := shared.Reserve(t.Context(), "exhaust", protocol.UsageTotals{})
				if err != nil {
					t.Fatal(err)
				}
				settle(protocol.UsageTotals{InputTokens: 1000000000})
			case "stopped":
				if _, err := e.Store.DB.Exec(`CREATE TRIGGER refuse_cancel BEFORE UPDATE OF state ON agent_tasks BEGIN SELECT RAISE(ABORT,'retained cancellation'); END`); err != nil {
					t.Fatal(err)
				}
				if err := e.DelegationCoordinator.CancelTask(t.Context(), task.ID); err == nil {
					t.Fatal("fixture cancellation unexpectedly persisted")
				}
			case "missing_ledger":
				if _, err := e.Store.DB.Exec(`DELETE FROM agent_task_budgets WHERE task_id=?`, task.ID); err != nil {
					t.Fatal(err)
				}
			case "foreign_project":
				if _, err := e.Store.DB.Exec(`UPDATE threads SET project_id='' WHERE id=?`, th.ID); err != nil {
					t.Fatal(err)
				}
			}
			before := protocol.UsageTotals{}
			if shared != nil {
				before = shared.Usage()
			}
			p := &budgetProvider{events: []llm.Event{{Type: llm.EventTextDelta, Text: "Manual summary"}, {Type: llm.EventDone, Usage: llm.Usage{InputTokens: 10, OutputTokens: 20, Reported: true}}}}
			e.LLMs.Register(p)
			err := e.CompactThread(t.Context(), th.ID)
			if mode != "success" && mode != "ordinary" {
				if err == nil || len(p.requests) != 0 {
					t.Fatalf("%s dispatched outside task owner: calls=%d err=%v", mode, len(p.requests), err)
				}
				if mode == "missing_ledger" {
					var count int
					if err = e.Store.DB.QueryRow(`SELECT count(*) FROM agent_task_budgets WHERE task_id=?`, task.ID).Scan(&count); err != nil || count != 0 {
						t.Fatalf("compaction created unlimited ledger: %d %v", count, err)
					}
				}
				return
			}
			if err != nil || len(p.requests) != 1 {
				t.Fatalf("manual compaction: calls=%d err=%v", len(p.requests), err)
			}
			if p.budgets[0] != shared {
				t.Fatal("public compaction did not inherit exact shared budget")
			}
			if shared != nil {
				after := shared.Usage()
				if after.Requests != before.Requests+1 || after.InputTokens != before.InputTokens+10 || after.OutputTokens != before.OutputTokens+20 {
					t.Fatalf("manual provider usage uncharged: before=%+v after=%+v", before, after)
				}
			}
			items, err := e.Store.ListItems(t.Context(), th.ID, 0)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, item := range items {
				if item.Kind == protocol.ItemContextCompaction && strings.Contains(item.Text, "Manual summary") {
					found = true
				}
			}
			if !found {
				t.Fatal("successful compaction summary missing")
			}
		})
	}
}

func TestManualCompactionDelegationCancellation(t *testing.T) {
	for _, mode := range []string{"task", "rpc"} {
		t.Run(mode, func(t *testing.T) {
			e, th, _, _ := delegationFixture(t)
			task := childExecutionTask(t, e, th)
			owner, shared, err := e.DelegationCoordinator.Task(t.Context(), task, protocol.ExecutionLimits{})
			if err != nil {
				t.Fatal(err)
			}
			before := shared.Usage()
			provider := &waitingDelegationProvider{started: make(chan context.Context, 1)}
			e.LLMs.Register(provider)
			rpc, cancel := context.WithCancel(t.Context())
			defer cancel()
			finished := make(chan error, 1)
			go func() { finished <- e.CompactThread(rpc, th.ID) }()
			var paid context.Context
			select {
			case paid = <-provider.started:
			case <-time.After(10 * time.Second):
				t.Fatal("compaction provider did not start")
			}
			if delegation.BudgetFromContext(paid) != shared {
				cancel()
				<-finished
				t.Fatal("manual compaction provider lost task budget")
			}
			if mode == "task" {
				if err = e.DelegationCoordinator.CancelTask(t.Context(), task.ID); err != nil {
					t.Fatal(err)
				}
			} else {
				cancel()
			}
			select {
			case err = <-finished:
				if err == nil {
					t.Fatal("cancelled compaction completed")
				}
			case <-time.After(5 * time.Second):
				t.Fatal("manual provider ignored cancellation")
			}
			if paid.Err() == nil {
				t.Fatal("paid provider context remained live")
			}
			if mode == "rpc" && owner.Err() != nil {
				t.Fatal("RPC cancellation revoked the whole task")
			}
			if mode == "task" && owner.Err() == nil {
				t.Fatal("task cancellation did not stop its owner")
			}
			if shared.Usage().Requests != before.Requests+1 {
				t.Fatal("cancelled provider attempt was not settled")
			}
			e.mu.Lock()
			_, locked := e.threadTurns[th.ID]
			e.mu.Unlock()
			if locked {
				t.Fatal("cancelled compaction retained chat lock")
			}
		})
	}
}
