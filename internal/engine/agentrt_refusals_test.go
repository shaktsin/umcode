package engine

import (
	"encoding/json"
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
	f := classifyActionFailure("op", errAgentsNotEnabled)
	if f.Retryable || f.NextStep != agentsNotEnabledNextStep || f.Message != agentsNotEnabledMessage {
		t.Fatalf("agent refusal classified %+v", f)
	}
	g := classifyActionFailure("op", errorString("exit status 1"))
	if !g.Retryable || g.Category != "execution_failed" || g.NextStep != "Inspect the failure and current resource state before proposing a focused correction." {
		t.Fatalf("non-agent failure changed: %+v", g)
	}
}

func TestAgentToolRuleRefusalsNotRetryable(t *testing.T) {
	for _, c := range []struct{ tool, msg, next string }{
		{"agent.propose_plan", "agent.propose_plan is lead-only: agents and child tasks cannot propose plans, whatever their capabilities", "Only the lead"},
		{"agent.suggest", "agent.suggest refused: agents need a committed Git project (delegation requires an initial committed Git revision)", "committed Git project"},
		{"agent__propose_plan", "a plan has 1 to 8 agents, got 9", "Propose a plan with 1 to"},
		{"agent.suggest", "agent.suggest refused: only one per user message, and this message already has one; stop and let the user answer it", "Follow the rule"},
	} {
		f := classifyToolFailure("op", c.tool, errorString(c.msg))
		if f.Retryable || !strings.Contains(f.NextStep, c.next) || f.Message != c.msg || f.OperationID != "op" {
			t.Fatalf("%s %q classified %+v", c.tool, c.msg, f)
		}
	}
	// Non-agent tools keep the generic classification, whatever the text.
	g := classifyToolFailure("op", "shell.run", errorString("this script is lead-only"))
	if !g.Retryable || g.Category != "execution_failed" {
		t.Fatalf("non-agent failure changed: %+v", g)
	}
	// A real execution failure of an agent tool stays retryable.
	h := classifyToolFailure("op", "agent.spawn", errorString("database is locked"))
	if !h.Retryable || h.Category != "execution_failed" {
		t.Fatalf("agent execution failure: %+v", h)
	}
}

type errorString string

func (e errorString) Error() string { return string(e) }
