package engine

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shaktsin/umcode/internal/config"
	"github.com/shaktsin/umcode/internal/llm"
	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/secrets"
	"github.com/shaktsin/umcode/internal/store"
)

type taskScriptProvider struct {
	before     func()
	beforeRead func()
	mode       string
	calls      int
	systems    []string
}

func (*taskScriptProvider) ID() string { return "claude" }
func (*taskScriptProvider) ListModels(context.Context, llm.Credential) ([]string, error) {
	return []string{"deterministic"}, nil
}
func (p *taskScriptProvider) Stream(_ context.Context, _ llm.Credential, req llm.Request) (<-chan llm.Event, error) {
	p.calls++
	if p.calls == 1 && p.before != nil {
		p.before()
	}
	p.systems = append(p.systems, req.System)
	out := make(chan llm.Event, 4)
	call := func(name string, args any) {
		raw, _ := json.Marshal(args)
		out <- llm.Event{Type: llm.EventToolCall, ToolCall: &llm.ToolCall{ID: store.NewID("call"), Name: name, Args: raw}}
	}
	text := func(s string) { out <- llm.Event{Type: llm.EventTextDelta, Text: s} }
	switch p.mode {
	case "summary_only":
		text("I successfully updated and verified the file.")
	case "read_denied", "read_failed":
		if p.calls == 1 {
			call("file__write", map[string]string{"path": "notes.txt", "content": "after"})
		} else if p.calls == 2 {
			if p.beforeRead != nil {
				p.beforeRead()
			}
			path := "notes.txt"
			if p.mode == "read_denied" {
				path = "../notes.txt"
			}
			call("file__read", map[string]string{"path": path})
		} else {
			text("I successfully updated and verified the file.")
		}
	case "invalid":
		text("I successfully updated and verified the file.")
		if p.calls == 1 {
			call("file__update", map[string]string{"path": "notes.txt", "content": "after"})
		}
	case "large":
		if p.calls == 1 {
			call("file__write", map[string]string{"path": "notes.txt", "content": strings.Repeat("new content\n", 16000)})
		} else if p.calls == 2 {
			call("file__read", map[string]string{"path": "notes.txt"})
		} else {
			text("Updated and verified.")
		}
	case "selection":
		text("I successfully updated the file.")
		if p.calls == 1 {
			call("mcp_demo__write", map[string]string{"path": "notes.txt", "content": "after"})
		}
	case "unverified":
		if p.calls == 1 {
			call("file__write", map[string]string{"path": "notes.txt", "content": "after"})
		} else {
			text("I successfully updated and verified the file.")
		}
	case "denied":
		text("I successfully updated the file.")
		call("file__edit", map[string]string{"path": "notes.txt", "old_string": "before", "new_string": "after"})
	case "loop":
		if p.calls == 2 {
			call("file__read", map[string]string{"path": "notes.txt"})
		} else if p.calls <= 4 {
			if p.calls == 4 {
				text("I successfully updated the file.")
			}
			call("file__edit", map[string]string{"path": "notes.txt", "old_string": "missing " + string(rune('0'+p.calls)), "new_string": "after"})
		} else {
			text("Unexpected extra retry")
		}
	case "edit_conflict", "alternate_write", "unrelated":
		switch p.calls {
		case 1:
			call("file__edit", map[string]string{"path": "notes.txt", "old_string": "outdated", "new_string": "after"})
		case 2:
			call("file__read", map[string]string{"path": "notes.txt"})
		case 3:
			if p.mode == "alternate_write" {
				call("file__write", map[string]string{"path": "notes.txt", "content": "after"})
			} else {
				replacement := "after"
				if p.mode == "unrelated" {
					replacement = "before\n// unrelated comment"
				}
				call("file__edit", map[string]string{"path": "notes.txt", "old_string": "before", "new_string": replacement})
			}
		case 4:
			call("file__read", map[string]string{"path": "notes.txt"})
		default:
			text("I successfully updated the file.")
		}
	default:
		switch p.calls {
		case 1:
			call("file__write", map[string]string{"path": "notes.txt", "content": "after"})
		case 2:
			call("file__read", map[string]string{"path": "notes.txt"})
		default:
			text("Updated and verified notes.txt.")
		}
	}
	out <- llm.Event{Type: llm.EventDone, StopReason: "end_turn", Usage: llm.Usage{Reported: true, InputTokens: 20, OutputTokens: 20}}
	close(out)
	return out, nil
}
func taskRuntimeFixture(t *testing.T, mode string, restrict bool) (*Engine, protocol.Thread, string, *taskScriptProvider) {
	t.Helper()
	cfg := config.Default(t.TempDir())
	cfg.LLM.Provider = "claude"
	cfg.LLM.Model = "deterministic"
	cfg.Models.DesignedWorkflow = true
	st, err := store.Open(t.Context(), filepath.Join(cfg.Home, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	provider := &taskScriptProvider{mode: mode}
	reg := llm.NewRegistry()
	reg.Register(provider)
	e, err := New(t.Context(), Options{Config: cfg, Store: st, Secrets: secrets.NewMemoryStore(), LLMs: reg, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), DisableScheduler: true, DisableMCP: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.Shutdown(context.Background()) })
	if _, err := e.Creds.Add(t.Context(), protocol.CredentialAddParams{Provider: "claude", Secret: "test", IsDefault: true}); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "notes.txt"), []byte("before"), 0600); err != nil {
		t.Fatal(err)
	}
	project, err := e.Projects.Create(t.Context(), protocol.ProjectCreateParams{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	th, err := st.CreateThread(t.Context(), protocol.Thread{Title: "task recovery", ProjectID: project.ID, ApprovalMode: "auto_all"})
	if err != nil {
		t.Fatal(err)
	}
	w, err := st.CreateWork(t.Context(), protocol.Work{ThreadID: th.ID, ProjectID: project.ID, Goal: "Update notes.txt", WorkflowDepth: "designed"})
	if err != nil {
		t.Fatal(err)
	}
	prior := protocol.Turn{ID: store.NewID("trn"), ThreadID: th.ID, Status: "completed", StartedAt: time.Now()}
	if err := st.CreateTurn(t.Context(), prior); err != nil {
		t.Fatal(err)
	}
	user := protocol.Item{ID: store.NewID("itm"), ThreadID: th.ID, TurnID: prior.ID, Seq: 1, Kind: protocol.ItemUserMessage, Status: protocol.ItemCompleted, Text: "Update notes.txt with the requested correction", CreatedAt: time.Now()}
	if err := st.SaveItem(t.Context(), user); err != nil {
		t.Fatal(err)
	}
	task, err := st.CreateExecutionTask(t.Context(), protocol.ExecutionTask{ThreadID: th.ID, ProjectID: project.ID, WorkID: w.ID, OriginItemID: user.ID, Objective: user.Text})
	if err != nil {
		t.Fatal(err)
	}
	if restrict {
		if _, err := e.validateTaskRequirement(t.Context(), th, protocol.RequirementProposal{TaskID: task.ID, ExpectedRevision: task.Revision, Origin: "user", OriginRef: user.ID, Scope: json.RawMessage(`{}`), Condition: json.RawMessage(`{"type":"no_actions"}`)}); err != nil {
			t.Fatal(err)
		}
	}
	return e, th, root, provider
}
func runTaskRuntimeTurn(t *testing.T, e *Engine, th protocol.Thread, text string) protocol.Turn {
	t.Helper()
	wait := make(chan protocol.Turn, 1)
	if _, err := e.startTurn(t.Context(), turnRequest{TurnStartParams: protocol.TurnStartParams{ThreadID: th.ID, Text: text, Override: protocol.ModelSelection{Provider: "claude", Model: "deterministic", Complexity: protocol.ComplexityStandard}}, wait: wait}); err != nil {
		t.Fatal(err)
	}
	select {
	case finished := <-wait:
		return finished
	case <-time.After(30 * time.Second):
		t.Fatal("runtime turn did not finish")
		return protocol.Turn{}
	}
}
func TestTaskRecoveryActualTurn(t *testing.T) {
	for _, mode := range []string{"denied", "loop"} {
		t.Run(mode, func(t *testing.T) {
			e, th, root, provider := taskRuntimeFixture(t, mode, mode == "denied")
			finished := runTaskRuntimeTurn(t, e, th, "/superpowers:using-superpowers retry")
			if finished.Status != protocol.TurnFailed {
				t.Fatalf("unresolved operation completed: %+v", finished)
			}
			expected := 1
			if mode == "loop" {
				expected = 4
			}
			if provider.calls != expected {
				t.Fatalf("no-progress guard escaped: calls=%d want=%d", provider.calls, expected)
			}
			b, err := os.ReadFile(filepath.Join(root, "notes.txt"))
			if err != nil || string(b) != "before" {
				t.Fatalf("denied/failed operation modified file: %q %v", b, err)
			}
			items, err := e.Store.ListItems(t.Context(), th.ID, 0)
			if err != nil {
				t.Fatal(err)
			}
			for _, item := range items {
				if item.Kind == protocol.ItemAgentMessage && item.Status == protocol.ItemCompleted && strings.Contains(item.Text, "successfully updated") {
					t.Fatal("unsupported success remained in chat")
				}
			}
			if !strings.Contains(provider.systems[0], "Update notes.txt with the requested correction") {
				t.Fatal("retry lost original objective")
			}
		})
	}
}

func TestTaskRetryUnchangedBlocker(t *testing.T) {
	e, th, root, p := taskRuntimeFixture(t, "denied", true)
	first := runTaskRuntimeTurn(t, e, th, "retry")
	if first.Status != protocol.TurnFailed {
		t.Fatal("first denial not recorded")
	}
	calls := p.calls
	second := runTaskRuntimeTurn(t, e, th, "retry")
	if second.Status != protocol.TurnFailed || p.calls != calls {
		t.Fatalf("unchanged blocker consumed another model call: status=%s calls=%d before=%d", second.Status, p.calls, calls)
	}
	b, err := os.ReadFile(filepath.Join(root, "notes.txt"))
	if err != nil || string(b) != "before" {
		t.Fatal("retry changed denied file", err)
	}
}

func TestTaskSelectionFailureActualTurn(t *testing.T) {
	for _, progressive := range []bool{false, true} {
		t.Run(map[bool]string{false: "ordinary", true: "progressive"}[progressive], func(t *testing.T) {
			e, th, root, p := taskRuntimeFixture(t, "selection", false)
			if _, err := e.SetTokenOptimization(t.Context(), protocol.TokenOptimizationParams{Enabled: progressive}); err != nil {
				t.Fatal(err)
			}
			tool := &engineTestTool{name: "mcp_demo__write", output: "changed"}
			e.Tools.Add(tool)
			p.before = func() {
				if _, err := e.Projects.Update(t.Context(), protocol.ProjectUpdateParams{ProjectID: th.ProjectID, Tools: &protocol.ProjectTools{MCPServers: []string{"other"}}}); err != nil {
					t.Error(err)
				}
			}
			finished := runTaskRuntimeTurn(t, e, th, "retry")
			if finished.Status != protocol.TurnFailed || tool.calls.Load() != 0 {
				t.Fatalf("selection denial concealed: status=%s effects=%d", finished.Status, tool.calls.Load())
			}
			b, err := os.ReadFile(filepath.Join(root, "notes.txt"))
			if err != nil || string(b) != "before" {
				t.Fatal("selection denied action changed file", err)
			}

		})
	}
}
