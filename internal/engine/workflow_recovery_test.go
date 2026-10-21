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

type workflowRecoveryProvider struct {
	mode    string
	calls   int
	results []string
}

func (*workflowRecoveryProvider) ID() string { return "claude" }
func (*workflowRecoveryProvider) ListModels(context.Context, llm.Credential) ([]string, error) {
	return []string{"deterministic"}, nil
}
func (p *workflowRecoveryProvider) Stream(_ context.Context, _ llm.Credential, req llm.Request) (<-chan llm.Event, error) {
	if reply, ok := testIntentReply(req, "explanation", "answer", ""); ok {
		return reply, nil
	}
	p.calls++
	for i := len(req.Messages) - 1; i >= 0; i-- {
		if req.Messages[i].Role == llm.RoleTool {
			p.results = append(p.results, req.Messages[i].Result)
			break
		}
	}
	ch := make(chan llm.Event, 2)
	call := func(name string, args any) {
		raw, _ := json.Marshal(args)
		ch <- llm.Event{Type: llm.EventToolCall, ToolCall: &llm.ToolCall{ID: store.NewID("call"), Name: name, Args: raw}}
	}
	switch p.calls {
	case 1:
		if p.mode == "spec_write" {
			call("file__write", map[string]string{"path": "docs/specs/goodreads.md", "content": "Data Access\nprofiles references auth.users"})
		} else {
			call("file__edit", map[string]string{"path": "docs/specs/goodreads.md", "old_string": "API Endpoints\nUsers", "new_string": "Data Access\nprofiles references auth.users"})
		}
	case 2:
		call("file__read", map[string]string{"path": "docs/specs/goodreads.md"})
	default:
		ch <- llm.Event{Type: llm.EventTextDelta, Text: "Updated and read back the specification."}
	}
	ch <- llm.Event{Type: llm.EventDone, StopReason: "end_turn", Usage: llm.Usage{Reported: true, InputTokens: 20, OutputTokens: 20}}
	close(ch)
	return ch, nil
}

func TestWorkflowRecoveryActualTurn(t *testing.T) {
	for _, mode := range []string{"spec", "spec_write"} {
		t.Run(mode, func(t *testing.T) {
			cfg := config.Default(t.TempDir())
			cfg.LLM.Provider, cfg.LLM.Model = "claude", "deterministic"
			cfg.Models.DesignedWorkflow = true
			st, err := store.Open(t.Context(), filepath.Join(cfg.Home, "test.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = st.Close() })
			provider := &workflowRecoveryProvider{mode: mode}
			reg := llm.NewRegistry()
			reg.Register(provider)
			e, err := New(t.Context(), Options{Config: cfg, Store: st, Secrets: secrets.NewMemoryStore(), LLMs: reg, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), DisableScheduler: true, DisableMCP: true})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { e.Shutdown(context.Background()) })
			if _, err = e.Creds.Add(t.Context(), protocol.CredentialAddParams{Provider: "claude", Secret: "test", IsDefault: true}); err != nil {
				t.Fatal(err)
			}
			root := t.TempDir()
			for _, p := range []string{"docs/specs", "src"} {
				if err = os.MkdirAll(filepath.Join(root, p), 0700); err != nil {
					t.Fatal(err)
				}
			}
			if err = os.WriteFile(filepath.Join(root, "docs/specs/goodreads.md"), []byte("API Endpoints\nUsers"), 0600); err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(filepath.Join(root, "src/auth.go"), []byte("before"), 0600); err != nil {
				t.Fatal(err)
			}
			proj, err := e.Projects.Create(t.Context(), protocol.ProjectCreateParams{Root: root})
			if err != nil {
				t.Fatal(err)
			}
			th, err := st.CreateThread(t.Context(), protocol.Thread{Title: "workflow regression", ProjectID: proj.ID, ApprovalMode: "auto_all"})
			if err != nil {
				t.Fatal(err)
			}
			w, err := st.CreateWork(t.Context(), protocol.Work{ThreadID: th.ID, ProjectID: proj.ID, WorkflowDepth: "designed", Goal: "Existing incomplete design"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = st.AddWorkNode(t.Context(), protocol.WorkNode{WorkID: w.ID, Kind: "goal", Title: "Existing incomplete design", Status: "active"}); err != nil {
				t.Fatal(err)
			}
			text := "Modify authentication in src/auth.go"
			if strings.HasPrefix(mode, "spec") {
				text = "Revise docs/specs/goodreads.md: replace Users with profiles referencing auth.users."
			}
			wait := make(chan protocol.Turn, 1)
			_, err = e.startTurn(t.Context(), turnRequest{TurnStartParams: protocol.TurnStartParams{ThreadID: th.ID, Text: text, Override: protocol.ModelSelection{Provider: "claude", Model: "deterministic", Complexity: protocol.ComplexityStandard}}, wait: wait})
			if err != nil {
				t.Fatal(err)
			}
			var finished protocol.Turn
			select {
			case finished = <-wait:
			case <-time.After(90 * time.Second):
				t.Fatal("turn did not finish")
			}
			if strings.HasPrefix(mode, "spec") {
				b, err := os.ReadFile(filepath.Join(root, "docs/specs/goodreads.md"))
				if err != nil || string(b) != "Data Access\nprofiles references auth.users" {
					t.Fatalf("requested specification remained blocked: %q (%v), turn=%+v", b, err, finished)
				}
				if finished.Status != protocol.TurnCompleted || provider.calls != 3 {
					t.Fatalf("spec flow: %+v, calls=%d", finished, provider.calls)
				}
				if len(provider.results) != 2 || !strings.Contains(provider.results[1], "profiles references auth.users") {
					t.Fatalf("spec read-back missing: %v", provider.results)
				}

			}
		})
	}
}

func (*workflowRecoveryProvider) RuntimeCapacity(context.Context, llm.Credential, string) (llm.RuntimeCapacity, error) {
	return llm.RuntimeCapacity{ContextWindow: 200000}, nil
}
