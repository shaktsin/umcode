package engine

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/shaktsin/umcode/internal/llm"
	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/tools"
)

func TestDelegationToolAuthority(t *testing.T) {
	e, th, root, p := delegationFixture(t)
	activation, err := e.PrepareDelegation(t.Context(), th.ID)
	if err != nil {
		t.Fatal(err)
	}
	turn := delegationTurn(t, e, protocol.TurnStartParams{ThreadID: th.ID, Text: "implement", DelegationActivationToken: activation.ActivationToken})
	task, err := e.Store.AgentTask(t.Context(), p.last())
	if err != nil {
		t.Fatal(err)
	}
	admitted := tools.WithScope(withDelegationTask(t.Context(), task), &tools.Scope{ThreadID: th.ID, ProjectID: th.ProjectID, Root: root})
	for _, ctx := range []context.Context{t.Context(), admitted, WithDelegationChild(admitted)} {
		catalog, _, err := e.permittedTurnCatalog(ctx, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		count := 0
		for _, c := range catalog {
			if strings.HasPrefix(c.tool.Name(), "agent.") {
				count++
			}
		}
		_, allowed := DelegationTaskFromContext(ctx)
		if allowed && count != 5 || !allowed && count != 0 {
			t.Fatalf("catalog count %d allowed %v", count, allowed)
		}
	}
	for _, name := range []string{"agent.spawn", "agent__spawn", "delegation.enable"} {
		r := e.runTool(WithDelegationChild(admitted), t.Context(), th, turn, llm.ToolCall{ID: "guessed", Name: name, Args: json.RawMessage(`{"role":"implementer","assignment":{"prompt":"nested"}}`)}, nil)
		if !r.IsError {
			t.Fatalf("nested guessed tool allowed: %s", name)
		}
	}
	tool, ok := e.Tools.Get("agent.spawn")
	if !ok {
		t.Fatal("missing spawn")
	}
	forged := task
	forged.ThreadID = "other"
	if _, err = tool.Call(tools.WithAgentCallID(tools.WithScope(withDelegationTask(t.Context(), forged), tools.ScopeFrom(admitted)), "forged"), json.RawMessage(`{}`)); err == nil {
		t.Fatal("foreign admitted marker accepted")
	}
	if _, err = e.Store.DB.ExecContext(t.Context(), `UPDATE threads SET project_id='' WHERE id=?`, th.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = tool.Call(tools.WithAgentCallID(admitted, "moved"), json.RawMessage(`{}`)); err == nil {
		t.Fatal("stale durable project owner accepted")
	}
}
