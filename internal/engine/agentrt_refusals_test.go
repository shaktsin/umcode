package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/tools"
)

// UX fix 1: agent.* calls in turns that are not admitted are refused with a
// short, actionable message and honest failure metadata.

// lastToolItem returns the newest tool item of the chat for tool.
func (x *activationE2E) lastToolItem(t *testing.T, tool string) protocol.Item {
	t.Helper()
	items, err := x.e.Store.ListItems(t.Context(), x.th.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	for i := len(items) - 1; i >= 0; i-- {
		if it := items[i]; it.Tool != nil && tools.FromWire(it.Tool.Name) == tool {
			return it
		}
	}
	t.Fatalf("no %s item", tool)
	return protocol.Item{}
}

func itemFailure(t *testing.T, it protocol.Item) protocol.ActionFailure {
	t.Helper()
	var d struct {
		ActionFailure protocol.ActionFailure `json:"actionFailure"`
	}
	if err := json.Unmarshal(it.Data, &d); err != nil {
		t.Fatalf("item data %s: %v", it.Data, err)
	}
	return d.ActionFailure
}

func TestAgentToolOutsideAdmittedTurnIsActionable(t *testing.T) {
	for _, tool := range []string{"agent.propose_plan", "agent.spawn"} {
		t.Run(tool, func(t *testing.T) {
			x := newActivationE2E(t, nil)
			x.setLead("not admitted", e2eLeadStep{tool, `{"agents":[` + planEntry("worker", "read A", "read", "") + `]}`})
			x.turn(t, protocol.TurnStartParams{Text: "not admitted"})
			res := x.leadResults(t, tool)
			if len(res) != 1 || !res[0].IsError || !strings.Contains(res[0].Result, "approves an agent plan") {
				t.Fatalf("lead told %+v, want the approved-plan rule", res)
			}
			it := x.lastToolItem(t, tool)
			if it.Tool.Error != agentsNotEnabledMessage {
				t.Fatalf("item error %q, want %q", it.Tool.Error, agentsNotEnabledMessage)
			}
			f := itemFailure(t, it)
			if f.Retryable || f.NextStep != agentsNotEnabledNextStep {
				t.Fatalf("failure %+v, want not retryable with the not-enabled next step", f)
			}
		})
	}
}

func TestAgentRuleRefusalFailureMetadata(t *testing.T) {
	f := classifyToolFailure("op", "agent.propose_plan", errAgentsNotEnabled)
	if f.Retryable || !f.Advisory || f.NextStep != agentsNotEnabledNextStep || f.Message != agentsNotEnabledMessage || f.OperationID != "op" {
		t.Fatalf("agent refusal classified %+v", f)
	}
	if w := classifyToolFailure("op", "agent__spawn", fmt.Errorf("spawn: %w", errAgentsNotEnabled)); w.Retryable || !w.Advisory {
		t.Fatalf("wrapped agent refusal classified %+v", w)
	}
	g := classifyActionFailure("op", errorString("exit status 1"))
	if !g.Retryable || g.Category != "execution_failed" || g.NextStep != genericNextStep {
		t.Fatalf("non-agent failure changed: %+v", g)
	}
}

const genericNextStep = "Inspect the failure and current resource state before proposing a focused correction."

// I1: refusals are recognised by identity and only for agent tools; text
// that merely quotes a refusal (a failing test printing the constant, a cat
// of the source) keeps the generic, blocking classification.
func TestRefusalWordingNeverReclassifies(t *testing.T) {
	quoted := errorString("--- FAIL: TestX\n    got " + agentsNotEnabledMessage + "\nexit status 1")
	for _, f := range []protocol.ActionFailure{
		classifyActionFailure("op", quoted),
		classifyToolFailure("op", "shell.run", quoted),
		classifyToolFailure("op", "agent.propose_plan", errorString(agentsNotEnabledMessage)),
		// A typed refusal reported by a non-agent tool is not an agent rule.
		classifyToolFailure("op", "shell.run", errAgentsNotEnabled),
	} {
		if !f.Retryable || f.Advisory || f.Category != "execution_failed" || f.NextStep != genericNextStep {
			t.Fatalf("quoted refusal classified %+v, want the generic retryable failure", f)
		}
	}
	// Rule wording in a real agent tool failure stays a real failure.
	for _, msg := range []string{"wait: child said this tool is lead-only", "integrate: git: not a committed git checkout", "accept: git repository root moved"} {
		if f := classifyToolFailure("op", "agent.integrate", errorString(msg)); !f.Retryable || f.Advisory || f.Category != "execution_failed" {
			t.Fatalf("%q classified %+v", msg, f)
		}
	}
}

func TestAgentToolRuleRefusalsNotRetryable(t *testing.T) {
	x := newActivationE2E(t, nil)
	agentCtx := withRuntimeAgent(withChildExecution(WithDelegationChild(t.Context()), &childExecution{runtime: &runtimeExecution{agent: protocol.RuntimeAgent{ID: "a1"}}}), "a1")
	_, leadOnly := x.e.proposeAgentPlan(agentCtx, tools.PlanProposal{})
	_, suggestLeadOnly := x.e.suggestAgents(agentCtx, "r")
	notGit := x.suggestRefusalForTest(t)
	for _, c := range []struct {
		err  error
		next string
	}{{leadOnly, "Only the lead"}, {suggestLeadOnly, "Only the lead"}, {notGit, "committed Git project"}} {
		f := classifyToolFailure("op", "agent.suggest", c.err)
		if f.Retryable || f.Advisory || !strings.Contains(f.NextStep, c.next) || f.Message != c.err.Error() {
			t.Fatalf("%v classified %+v", c.err, f)
		}
	}
}

// suggestRefusalForTest makes the chat's project non-Git and returns
// agent.suggest's refusal.
func (x *activationE2E) suggestRefusalForTest(t *testing.T) error {
	t.Helper()
	if err := os.RemoveAll(filepath.Join(x.root, ".git")); err != nil {
		t.Fatal(err)
	}
	ctx := tools.WithScope(context.WithValue(t.Context(), executionTurnKey{}, protocol.Turn{ID: "trn_x", ThreadID: x.th.ID}), &tools.Scope{ThreadID: x.th.ID, ProjectID: x.th.ProjectID, Root: x.root})
	_, err := x.e.suggestAgents(ctx, "r")
	if err == nil {
		t.Fatal("agent.suggest accepted a non-Git project")
	}
	return err
}

// I1 end to end: a non-agent tool whose failure output quotes the refusal is
// recorded as the real, blocking failure it is.
func TestQuotedRefusalInToolOutputStaysBlocking(t *testing.T) {
	x := newActivationE2E(t, nil)
	x.e.Tools.Add(&failingTool{name: "probe.fail", err: errorString("--- FAIL: TestX: " + agentsNotEnabledMessage)})
	x.setLead("run the probe", e2eLeadStep{"probe.fail", `{}`})
	x.turn(t, protocol.TurnStartParams{Text: "run the probe"})
	it := x.lastToolItem(t, "probe.fail")
	f := itemFailure(t, it)
	if !f.Retryable || f.Advisory || f.Category != "execution_failed" || !strings.Contains(f.Message, "--- FAIL: TestX") {
		t.Fatalf("probe failure recorded %+v, want the real, blocking failure", f)
	}
}

type failingTool struct {
	name string
	err  error
}

func (f *failingTool) Name() string            { return f.name }
func (f *failingTool) Description() string     { return "fails" }
func (f *failingTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (f *failingTool) Assess(json.RawMessage) (tools.Risk, string) {
	return tools.RiskGreen, "probe"
}
func (f *failingTool) Call(context.Context, json.RawMessage) (string, error) { return "", f.err }

type errorString string

func (e errorString) Error() string { return string(e) }

func TestSuggestInAdmittedTurnNamesItsRule(t *testing.T) {
	x := newActivationE2E(t, nil)
	token := x.enable(t, x.suggestTurn(t))
	x.setLead("admitted suggest", e2eLeadStep{"agent.suggest", `{"reason":"again"}`})
	x.turn(t, protocol.TurnStartParams{Text: "admitted suggest", DelegationActivationToken: token,
		Override: protocol.ModelSelection{Provider: protocol.PoolProvider, Model: "e2e-pool", Complexity: protocol.ComplexityHigh}})
	res := x.leadResults(t, "agent.suggest")
	last := res[len(res)-1]
	if !last.IsError || !strings.Contains(last.Result, "use agent.propose_plan") {
		t.Fatalf("lead told %+v, want the admitted-turn rule", last)
	}
	f := itemFailure(t, x.lastToolItem(t, "agent.suggest"))
	if f.Retryable || !strings.Contains(f.NextStep, "agent.propose_plan") {
		t.Fatalf("failure %+v, want not retryable naming agent.propose_plan", f)
	}
}
