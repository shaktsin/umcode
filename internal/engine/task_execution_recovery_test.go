package engine

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/shaktsin/umcode/internal/llm"
	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/tools"
)

type executionEvidenceProvider struct {
	kinds []string
	taskScriptProvider
	mode, kind                  string
	executionCalls, intentCalls int
}

func (p *executionEvidenceProvider) Stream(ctx context.Context, cred llm.Credential, req llm.Request) (<-chan llm.Event, error) {
	out := make(chan llm.Event, 4)
	if strings.HasPrefix(req.System, "Privately classify") {
		p.intentCalls++
		var input struct {
			TaskID           string `json:"taskId"`
			ExpectedRevision int    `json:"expectedRevision"`
			OriginItemID     string `json:"originItemId"`
			Request          string `json:"request"`
		}
		_ = json.Unmarshal([]byte(req.Messages[0].JoinedText()), &input)
		kind, predicate := p.kind, "file_content"
		if len(p.kinds) > 0 {
			kind = p.kinds[0]
			p.kinds = p.kinds[1:]
		}
		if kind == "" {
			kind = "mutation"
		}
		if kind == "explanation" {
			predicate = "answer"
		}
		if kind == "inspection" {
			predicate = "exists"
		}
		resource := json.RawMessage(`{"type":"file","path":"notes.txt"}`)
		if kind == "explanation" {
			resource = json.RawMessage(`{}`)
		}
		proposal := protocol.TaskIntentProposal{TaskID: input.TaskID, ExpectedRevision: input.ExpectedRevision, OriginItemID: input.OriginItemID, Relation: "continue", Expectations: []protocol.TaskExpectationProposal{{Source: protocol.TaskSourceSpan{ItemID: input.OriginItemID, Start: 0, End: utf8.RuneCountInString(input.Request)}, Kind: kind, Predicate: predicate, Resource: resource}}}
		raw, _ := json.Marshal(proposal)
		out <- llm.Event{Type: llm.EventTextDelta, Text: string(raw)}
	} else {
		p.executionCalls++
		call := func(name, args string) {
			out <- llm.Event{Type: llm.EventToolCall, ToolCall: &llm.ToolCall{ID: "actual", Name: name, Args: json.RawMessage(args)}}
		}
		if p.mode == "recover" && p.executionCalls == 2 {
			call("file__write", `{"path":"notes.txt","content":"after"}`)
		} else if p.mode == "recover" && p.executionCalls == 3 {
			call("file__read", `{"path":"notes.txt"}`)
		} else if p.mode == "inspect" && p.executionCalls == 1 {
			call("file__read", `{"path":"notes.txt"}`)
		} else {
			text := "[Created notes.txt]\n[earlier tool call file.write: denied]\nI cannot write because of permission restrictions."
			if p.mode == "recover" && p.executionCalls >= 4 {
				text = "Updated and verified notes.txt."
			}
			if p.kind == "explanation" {
				text = "For example, '[Created notes.txt]' is only a quoted receipt."
			}
			if p.mode != "silent" {
				out <- llm.Event{Type: llm.EventTextDelta, Text: text}
			}
		}
	}
	out <- llm.Event{Type: llm.EventDone, StopReason: "end_turn", Usage: llm.Usage{Reported: true, InputTokens: 20, OutputTokens: 20}}
	close(out)
	return out, nil
}
func executionEvidenceFixture(t *testing.T, mode, kind string) (*Engine, protocol.Thread, string, *executionEvidenceProvider) {
	e, th, root, _ := taskRuntimeFixture(t, "write_verified", false)
	p := &executionEvidenceProvider{mode: mode, kind: kind}
	e.LLMs.Register(p)
	return e, th, root, p
}

// Prose is preserved as model output, never executed or certified by the engine.
func TestTaskNarrationWithoutExecution(t *testing.T) {
	e, th, root, p := executionEvidenceFixture(t, "narration", "mutation")
	turn := runTaskRuntimeTurn(t, e, th, "retry")
	body, _ := os.ReadFile(filepath.Join(root, "notes.txt"))
	if turn.Status != protocol.TurnCompleted || string(body) != "before" || p.executionCalls != 1 || p.intentCalls != 0 {
		t.Fatalf("narration caused execution/classification: status=%s bytes=%q calls=%d/%d", turn.Status, body, p.executionCalls, p.intentCalls)
	}
	items, _ := e.Store.ListItems(t.Context(), th.ID, 0)
	for _, it := range items {
		if it.Kind == protocol.ItemToolCall {
			t.Fatal("prose executed as a tool")
		}
		if it.Kind == protocol.ItemAgentMessage && strings.Contains(it.Text, "The model did not execute the requested action.") {
			t.Fatal("engine replaced model output")
		}
	}
	snap, _ := e.Store.ActiveExecutionTask(t.Context(), th.ID)
	if snap.Task.Status == "completed" || len(snap.Expectations) != 0 {
		t.Fatal("narration received engine semantic certification")
	}
}
func TestTaskNarrationRecovery(t *testing.T) {
	e, th, root, p := executionEvidenceFixture(t, "recover", "mutation")
	runTaskRuntimeTurn(t, e, th, "retry")
	finished := runTaskRuntimeTurn(t, e, th, "continue")
	body, _ := os.ReadFile(filepath.Join(root, "notes.txt"))
	if finished.Status != protocol.TurnCompleted || string(body) != "after" || p.executionCalls != 4 {
		t.Fatalf("recovery=%+v bytes=%q calls=%d", finished, body, p.executionCalls)
	}
	snap, _ := e.Store.ActiveExecutionTask(t.Context(), th.ID)
	if len(snap.Expectations) != 0 {
		t.Fatal("engine reconstructed an intent contract")
	}
	observed := false
	for _, op := range snap.Operations {
		if op.Status == "succeeded" && op.VerificationStatus == "passed" && len(op.EvidenceIDs) > 0 {
			observed = true
		}
	}
	if !observed {
		t.Fatal("actual read-back evidence was lost")
	}

}
func TestTaskExplanationNeedsNoTools(t *testing.T) {
	e, th, _, p := executionEvidenceFixture(t, "narration", "explanation")
	finished := runTaskRuntimeTurn(t, e, th, "retry")
	if finished.Status != protocol.TurnCompleted || p.executionCalls != 1 {
		t.Fatalf("explanation forced execution: %+v calls=%d", finished, p.executionCalls)
	}
}

func TestTaskCorrectionBudgetContinuity(t *testing.T) {
	e, th, turn, ctx, _ := expectationEngine(t, "mutation", "file_content")
	snap, _ := e.Store.ActiveExecutionTask(ctx, th.ID)
	for i := 0; i < 3; i++ {
		// Fresh engine values cannot replenish the persisted allowance.
		fresh := &Engine{Store: e.Store}
		_, retry, _, err := fresh.nextExecutionCorrection(ctx, snap.Task.ID, turn.ID)
		if err != nil || retry != (i < 2) {
			t.Fatalf("attempt=%d retry=%v err=%v", i, retry, err)
		}
	}
	next := turn
	next.ID = "new-user-retry"
	if err := e.Store.CreateTurn(ctx, next); err != nil {
		t.Fatal(err)
	}
	if _, retry, _, err := e.nextExecutionCorrection(ctx, snap.Task.ID, next.ID); err != nil || !retry {
		t.Fatal("new turn lost own bounded allowance", err)
	}
}

func TestTaskIntentMigrationThenInquiry(t *testing.T) {
	e, th, root, _ := taskRuntimeFixture(t, "write_verified", false)
	p := &executionEvidenceProvider{kinds: []string{"mutation", "inspection"}}
	e.LLMs.Register(p)
	turn := protocol.Turn{ID: "new-inquiry", ThreadID: th.ID, Status: "running"}
	if err := e.Store.CreateTurn(t.Context(), turn); err != nil {
		t.Fatal(err)
	}
	user := persistTaskTestUser(t, e, th, turn, "Does notes.txt exist?")
	ctx := tools.WithScope(t.Context(), &tools.Scope{Root: root, ProjectRoot: root, ProjectID: th.ProjectID, ThreadID: th.ID})
	res, err := e.resolveSelection(ctx, th, protocol.ModelSelection{Provider: "claude", Model: "deterministic"}, user.Text, 0)
	if err != nil {
		t.Fatal(err)
	}
	got, err := e.resolveTaskExpectations(ctx, ctx, th, turn, user, &res, newTurnBudget(res.limits), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Expectations) != 2 || got.Expectations[0].Kind != "mutation" || got.Expectations[0].RequestTurnID == turn.ID || !taskInquiryOnly(got, turn.ID) {
		t.Fatalf("migration forced old mutation into new inquiry: %+v", got.Expectations)
	}
}

func TestTaskSilentResponseDoesNotInventOutcome(t *testing.T) {
	e, th, _, p := executionEvidenceFixture(t, "silent", "mutation")
	turn := runTaskRuntimeTurn(t, e, th, "retry")
	if turn.Status != protocol.TurnCompleted || turn.Error != "" || p.executionCalls != 1 || p.intentCalls != 0 {
		t.Fatalf("empty response triggered semantic correction: status=%s calls=%d/%d error=%s", turn.Status, p.executionCalls, p.intentCalls, turn.Error)
	}
	snap, _ := e.Store.ActiveExecutionTask(t.Context(), th.ID)
	if len(snap.Operations) != 0 || snap.Task.Status == "completed" {
		t.Fatal("empty response certified effects")
	}
}
