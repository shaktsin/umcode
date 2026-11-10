package engine

import (
	"strings"
	"testing"

	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/store"
)

// No refusal may suggest spawning a new agent once work is integrated: the
// spawn gate (gap G1) refuses it.
func TestRefusalsAfterApplyNeverSuggestNewAgent(t *testing.T) {
	env := runtimeVerificationEnv(t)
	a := env.writer(t, "a", "a.txt", "from a\n")
	c := env.finishingWriter(t, "c", "c.txt", "from c\n")
	env.end(t, c.ID, protocol.AgentStateCancelled)
	env.mustIntegrate(t, `{"action":"accept","agent":"`+a.ID+`"}`)
	env.passChecks(t)
	_, err := env.integrate(t, env.ctx, `{"action":"accept","agent":"`+c.ID+`"}`)
	if err == nil || !strings.Contains(err.Error(), "was cancelled after submitting") {
		t.Fatalf("accept cancelled agent: %v", err)
	}
	if strings.Contains(err.Error(), "new agent") || !strings.Contains(err.Error(), "new delegated task") {
		t.Fatalf("refusal after an apply must point to a new delegated task, not a new agent: %v", err)
	}
}

// Before anything is applied a new agent can still be spawned, so the advice
// stays.
func TestCancelledAgentRefusalBeforeApplyKeepsNewAgent(t *testing.T) {
	env := runtimeVerificationEnv(t)
	c := env.finishingWriter(t, "c", "c.txt", "from c\n")
	env.end(t, c.ID, protocol.AgentStateCancelled)
	_, err := env.integrate(t, env.ctx, `{"action":"accept","agent":"`+c.ID+`"}`)
	if err == nil || !strings.Contains(err.Error(), "start a new agent") {
		t.Fatalf("accept cancelled agent before any apply: %v", err)
	}
}

// M6 + I2: the "not verified" accept refusal says why, and, when the head
// cannot be verified, that applied revisions are final.
func TestAcceptRefusalExplainsUnverifiedHead(t *testing.T) {
	env := runtimeVerificationEnv(t)
	a := env.writer(t, "a", "a.txt", "from a\n")
	b := env.writer(t, "b", "b.txt", "from b\n")
	env.mustIntegrate(t, `{"action":"accept","agent":"`+a.ID+`"}`)
	accept := `{"action":"accept","agent":"` + b.ID + `"}`
	_, err := env.integrate(t, env.ctx, accept)
	if err == nil || !strings.Contains(err.Error(), "not verified") || !strings.Contains(err.Error(), "check go test ./... is not_run") || !strings.Contains(err.Error(), "verification.run") {
		t.Fatalf("unrun checks: %v", err)
	}
	env.runner.set(runResults("failed", "passed"))
	if err := env.run(env.ctx, "verification.run", env.runner); err != nil {
		t.Fatal(err)
	}
	_, err = env.integrate(t, env.ctx, accept)
	if err == nil || !strings.Contains(err.Error(), "check go test ./... is failed") || !strings.Contains(err.Error(), "applied revisions are final") || !strings.Contains(err.Error(), "cancel") {
		t.Fatalf("failed check: %v", err)
	}
	if got := env.revision(t, b.ID); got.State != store.RevisionSubmitted {
		t.Fatalf("b = %s", got.State)
	}
}

func TestAcceptRefusalExplainsNoChecksPlanned(t *testing.T) {
	env := runtimeVerificationEnv(t)
	env.planner.set(`{"checks":[],"visual_qa":""}`)
	a := env.writer(t, "a", "a.txt", "from a\n")
	b := env.writer(t, "b", "b.txt", "from b\n")
	env.mustIntegrate(t, `{"action":"accept","agent":"`+a.ID+`"}`)
	_, err := env.integrate(t, env.ctx, `{"action":"accept","agent":"`+b.ID+`"}`)
	if err == nil || !strings.Contains(err.Error(), "no checks planned") || !strings.Contains(err.Error(), "applied revisions are final") {
		t.Fatalf("no checks: %v", err)
	}
}

// I2: the dead end is stated in the tool description.
func TestIntegrateDescriptionStatesAppliedIsFinal(t *testing.T) {
	env := runtimeVerificationEnv(t)
	tool, ok := env.e.Tools.Get("agent.integrate")
	if !ok {
		t.Fatal("agent.integrate not registered")
	}
	d := tool.Description()
	for _, want := range []string{"Applied revisions are final", "cancel the task", "new delegated task"} {
		if !strings.Contains(d, want) {
			t.Fatalf("description lacks %q: %s", want, d)
		}
	}
	if strings.Contains(d, "new agent with") {
		t.Fatalf("description offers a new agent: %s", d)
	}
}

// M5: an earlier revision stuck applying is settled by accepting it again (or
// restoring the checkout), not by drop or resolved, which both refuse it.
func TestOrderRefusalForApplyingRevision(t *testing.T) {
	env := runtimeVerificationEnv(t)
	a := env.writer(t, "a", "a.txt", "from a\n")
	b := env.writer(t, "b", "b.txt", "from b\n")
	env.markApplying(t, a.ID)
	_, err := env.integrate(t, env.ctx, `{"action":"accept","agent":"`+b.ID+`"}`)
	if err == nil || !strings.Contains(err.Error(), "accept agent "+a.ID+" again") || !strings.Contains(err.Error(), "restore") {
		t.Fatalf("order refusal: %v", err)
	}
	if strings.Contains(err.Error(), "drop or resolve") {
		t.Fatalf("advises drop/resolve for an applying revision: %v", err)
	}
}

// M1: failing to list the integrations to reconcile is logged; the engine
// still starts.
func TestStartupSurvivesReconcileListFailure(t *testing.T) {
	env := runtimeVerificationEnv(t)
	if _, err := env.e.Store.DB.Exec(`DROP TABLE agent_integration_head`); err != nil {
		t.Fatal(err)
	}
	_, logs := env.restartLogged(t)
	if !strings.Contains(logs.String(), "list runtime integrations to reconcile") {
		t.Fatalf("list failure not logged:\n%s", logs.String())
	}
}
