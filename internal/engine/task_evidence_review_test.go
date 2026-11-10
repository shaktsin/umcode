package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/shaktsin/umcode/internal/llm"
	"github.com/shaktsin/umcode/internal/protocol"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTaskExpectationNewMutationRejectsPriorProof(t *testing.T) {
	e, th, old, ctx, _ := expectationEngine(t, "mutation", "file_content")
	expectationTool(t, e, th, old, ctx, "file.write", `{"path":"notes.txt","content":"old requested content"}`)
	expectationTool(t, e, th, old, ctx, "file.read", `{"path":"notes.txt"}`)
	next := old
	next.ID = "review-next"
	if err := e.Store.CreateTurn(ctx, next); err != nil {
		t.Fatal(err)
	}
	user := persistTaskTestUser(t, e, th, next, "Replace notes.txt with completely different requested content")
	snap, _ := e.Store.ActiveExecutionTask(ctx, th.ID)
	p := intentProposal(snap.Task, user, "mutation", "file_content")
	if _, err := e.resolveTaskExpectations(ctx, ctx, th, next, user, nil, nil, &p); err != nil {
		t.Fatal(err)
	}
	got := expectationResult(t, e, th, next, ctx)
	if got.Status == "satisfied" {
		t.Fatalf("new requested edit accepted with ZERO new operations: %+v", got)
	}
	expectationTool(t, e, th, next, ctx, "file.write", `{"path":"notes.txt","content":"new requested content"}`)
	expectationTool(t, e, th, next, ctx, "file.read", `{"path":"notes.txt"}`)
	if got := expectationResult(t, e, th, next, ctx); got.Status != "satisfied" {
		t.Fatalf("new real edit rejected: %+v", got)
	}
}
func TestTaskExpectationIncompleteLifecycle(t *testing.T) {
	e, th, turn, ctx, _ := expectationEngine(t, "mutation", "file_content")
	expectationTool(t, e, th, turn, ctx, "file.write", `{"path":"other.txt","content":"other task work"}`)
	expectationTool(t, e, th, turn, ctx, "file.read", `{"path":"other.txt"}`)
	got := expectationResult(t, e, th, turn, ctx)
	if got.Status == "satisfied" {
		t.Fatal("bad fixture")
	}
	snap, _ := e.Store.ActiveExecutionTask(ctx, th.ID)
	outcome, err := e.reconcileTaskOutcome(ctx, snap.Task.ID)
	if err != nil {
		t.Fatal(err)
	}
	snap, _ = e.Store.ActiveExecutionTask(ctx, th.ID)
	if snap.Task.Status == "completed" {
		t.Fatalf("missing request=%+v but lifecycle=%s outcome=%s", got, snap.Task.Status, outcome)
	}
	finished := runTaskRuntimeTurn(t, e, th, "retry")
	if finished.Status != protocol.TurnCompleted {
		t.Fatalf("unfinished task could not retry: %+v", finished)
	}
}
func TestTaskExpectationRequestedSymlinkBinding(t *testing.T) {
	for _, absolute := range []bool{false, true} {
		t.Run(fmt.Sprint(absolute), func(t *testing.T) {
			e, th, old, ctx, root := expectationEngine(t, "mutation", "file_content")
			if err := os.Symlink("notes.txt", filepath.Join(root, "linked.txt")); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "other.txt"), []byte("wrong content"), 0600); err != nil {
				t.Fatal(err)
			}
			next := old
			next.ID = "review-link"
			if err := e.Store.CreateTurn(ctx, next); err != nil {
				t.Fatal(err)
			}
			user := persistTaskTestUser(t, e, th, next, "Write requested content to linked.txt")
			snap, _ := e.Store.ActiveExecutionTask(ctx, th.ID)
			p := intentProposal(snap.Task, user, "mutation", "file_content")
			requested := "linked.txt"
			if absolute {
				requested = filepath.Join(root, requested)
			}
			p.Expectations[0].Resource, _ = json.Marshal(protocol.TaskResource{Type: "file", Path: requested})
			if _, err := e.resolveTaskExpectations(ctx, ctx, th, next, user, nil, nil, &p); err != nil {
				t.Fatal(err)
			}
			expectationTool(t, e, th, next, ctx, "file.write", `{"path":"notes.txt","content":"requested content"}`)
			expectationTool(t, e, th, next, ctx, "file.read", `{"path":"notes.txt"}`)
			if err := os.Remove(filepath.Join(root, "linked.txt")); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("other.txt", filepath.Join(root, "linked.txt")); err != nil {
				t.Fatal(err)
			}
			got := expectationResult(t, e, th, next, ctx)
			if got.Status == "satisfied" {
				t.Fatalf("retargeted user-requested path certified: %+v", got)
			}
		})
	}
}

func TestTaskExpectationDirectoryListing(t *testing.T) {
	for _, mode := range []string{"explicit", "default_path", "stale", "bounded"} {
		t.Run(mode, func(t *testing.T) {
			e, th, old, ctx, root := expectationEngine(t, "mutation", "file_content")
			next := old
			next.ID = "listing-turn"
			if err := e.Store.CreateTurn(ctx, next); err != nil {
				t.Fatal(err)
			}
			user := persistTaskTestUser(t, e, th, next, "List the files in the project root")
			snap, _ := e.Store.ActiveExecutionTask(ctx, th.ID)
			p := intentProposal(snap.Task, user, "inspection", "list")
			p.Expectations[0].Resource = json.RawMessage(`{"type":"file","path":"."}`)
			if _, err := e.resolveTaskExpectations(ctx, ctx, th, next, user, nil, nil, &p); err != nil {
				t.Fatal(err)
			}
			if mode == "bounded" {
				for i := 0; i < 300; i++ {
					if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("%03d-", i)+strings.Repeat("x", 230)), nil, 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
			args := `{"path":"."}`
			if mode == "default_path" {
				args = `{}`
			}
			if out := expectationTool(t, e, th, next, ctx, "file.list", args); out.IsError {
				t.Fatal(out.Output)
			}
			if mode == "stale" {
				if err := os.WriteFile(filepath.Join(root, "added.txt"), []byte("new"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			got := expectationResult(t, e, th, next, ctx)
			want := "satisfied"
			if mode == "stale" || mode == "bounded" {
				want = "not_executed"
			}
			if got.Status != want {
				t.Fatalf("listing outcome=%+v want=%s", got, want)
			}
		})
	}
}

type evidenceInquiryProvider struct{ taskScriptProvider }

func (p *evidenceInquiryProvider) Stream(_ context.Context, _ llm.Credential, req llm.Request) (<-chan llm.Event, error) {
	if reply, ok := testIntentReply(req, "inspection", "exists", "other.txt"); ok {
		return reply, nil
	}
	p.calls++
	out := make(chan llm.Event, 2)
	if p.calls == 1 {
		out <- llm.Event{Type: llm.EventToolCall, ToolCall: &llm.ToolCall{ID: "read", Name: "file__read", Args: json.RawMessage(`{"path":"other.txt"}`)}}
	} else {
		out <- llm.Event{Type: llm.EventTextDelta, Text: "other.txt exists and contains hello."}
	}
	out <- llm.Event{Type: llm.EventDone, StopReason: "end_turn", Usage: llm.Usage{Reported: true, InputTokens: 20, OutputTokens: 20}}
	close(out)
	return out, nil
}
func TestTaskInquiryPreservesAnswer(t *testing.T) {
	e, th, old, ctx, root := expectationEngine(t, "mutation", "file_content")
	expectationTool(t, e, th, old, ctx, "file.write", `{"path":"notes.txt","content":"not verified yet"}`)
	if err := os.WriteFile(filepath.Join(root, "other.txt"), []byte("hello"), 0600); err != nil {
		t.Fatal(err)
	}
	e.LLMs.Register(&evidenceInquiryProvider{})
	turn := runTaskRuntimeTurn(t, e, th, "Does other.txt exist?")
	items, _ := e.Store.ListItems(ctx, th.ID, 0)
	found := false
	for _, it := range items {
		if it.TurnID == turn.ID && it.Kind == protocol.ItemAgentMessage {
			t.Logf("answer: %s metadata: %s", it.Text, it.Data)
			if strings.Contains(it.Text, "other.txt exists") {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("completed inspection answer erased, turn=%+v", turn)
	}
}
