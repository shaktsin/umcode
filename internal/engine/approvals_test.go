package engine

import (
	"context"
	"encoding/json"
	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/store"
	"testing"
	"time"
)

func TestOrdinaryToolApprovalAuditRetainsExistingDetails(t *testing.T) {
	e, th, turn, st := pluginHookEngine(t)
	a := protocol.Approval{ID: store.NewID("apr"), ThreadID: th.ID, TurnID: turn.ID, Tool: "shell.run", Args: json.RawMessage(`{"command":"npm test"}`), Risk: "yellow", ActionSummary: "ordinary tool summary", Status: "pending", CreatedAt: time.Now(), ExpiresAt: time.Now()}
	_, _ = e.persistAndWaitApproval(t.Context(), context.Background(), a, 0, "approval.request")
	var raw string
	if err := st.DB.QueryRow(`SELECT details_json FROM audit_log WHERE event_type='approval.request'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var details map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &details); err != nil {
		t.Fatal(err)
	}
	if len(details) != 5 || string(details["summary"]) != `"ordinary tool summary"` || string(details["args"]) != `{"command":"npm test"}` || string(details["risk"]) != `"yellow"` {
		t.Fatalf("ordinary audit changed: %s", raw)
	}
}

func TestToolApprovalStillRemembersExactDecision(t *testing.T) {
	e, th, turn, st := pluginHookEngine(t)
	a := protocol.Approval{ID: store.NewID("apr"), ThreadID: th.ID, TurnID: turn.ID, Tool: "shell.run", Args: json.RawMessage(`{"command":"npm test"}`), Status: "pending", CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour)}
	if err := st.CreateApproval(t.Context(), a); err != nil {
		t.Fatal(err)
	}
	if _, err := e.RespondApproval(t.Context(), a.ID, true, true, "user"); err != nil {
		t.Fatal(err)
	}
	if got := st.RememberedThreadDecision(t.Context(), th.ID, "shell.run", "npm test"); got != "allow" {
		t.Fatalf("tool decision=%q", got)
	}
	if got := st.RememberedThreadDecision(t.Context(), th.ID, "shell.run", "npm deploy"); got != "" {
		t.Fatalf("tool signature generalized: %q", got)
	}
}

func TestApprovalSignatureKeepsShellCommandsExact(t *testing.T) {
	first := ApprovalSignature("shell.run", json.RawMessage(`{"command":"npm run build"}`))
	second := ApprovalSignature("shell.run", json.RawMessage(`{"command":"npm run deploy"}`))
	if first != "npm run build" || second != "npm run deploy" || first == second {
		t.Fatalf("remembered command signatures were not exact: %q, %q", first, second)
	}
}

func TestApprovalSignatureTrimsCommandWhitespaceAndKeepsExactFilePath(t *testing.T) {
	command := ApprovalSignature("shell.run", json.RawMessage(`{"command":"  npm test  "}`))
	path := ApprovalSignature("file.write", json.RawMessage(`{"path":"src/main.ts"}`))
	if command != "npm test" || path != "src/main.ts" {
		t.Fatalf("signatures = command %q, path %q", command, path)
	}
}

func TestIsComputerUseToolNeverRemembered(t *testing.T) {
	for _, tool := range []string{"computer.act", "computer.start", "computer.inspect", "computer.stop"} {
		if !isComputerUseTool(tool) {
			t.Fatalf("%s should be recognized as a Computer Use tool", tool)
		}
	}
	for _, tool := range []string{"shell.run", "file.write", "computerlike.thing"} {
		if isComputerUseTool(tool) {
			t.Fatalf("%s should not be treated as a Computer Use tool", tool)
		}
	}
}
