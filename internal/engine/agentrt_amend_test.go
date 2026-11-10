package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/store"
	"github.com/shaktsin/umcode/internal/tools"
)

// amendAdvice is the instruction every not-verified refusal must carry.
const amendAdvice = `fix the files in the project checkout, stage them, then agent.integrate {action:"amend"}`

// twoAppliedFailing accepts a and b (a verified in between) and records a
// failing check for the combined head.
func twoAppliedFailing(t *testing.T, status ...string) (rvEnv, protocol.RuntimeAgent, protocol.RuntimeAgent) {
	t.Helper()
	env := runtimeVerificationEnv(t)
	a := env.writer(t, "a", "a.txt", "from a\n")
	b := env.writer(t, "b", "b.txt", "from b\n")
	env.mustIntegrate(t, `{"action":"accept","agent":"`+a.ID+`"}`)
	env.passChecks(t)
	env.mustIntegrate(t, `{"action":"accept","agent":"`+b.ID+`"}`)
	if len(status) == 0 {
		status = []string{"failed", "passed"}
	}
	env.runner.set(runResults(status...))
	if err := env.run(env.ctx, "verification.run", env.runner); err != nil {
		t.Fatal(err)
	}
	return env, a, b
}

func (env rvEnv) stageFix(t *testing.T, path, data string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(env.root, path), []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	delegationGit(t, env.root, "add", path)
	return delegationGit(t, env.root, "write-tree")
}

func TestAmendAdoptsStagedFix(t *testing.T) {
	env, a, b := twoAppliedFailing(t)
	if _, err := env.integrate(t, env.ctx, `{"action":"complete"}`); err == nil || !strings.Contains(err.Error(), "failed") {
		t.Fatalf("complete with failing checks: %v", err)
	}
	revA, revB := env.revision(t, a.ID), env.revision(t, b.ID)
	fixed := env.stageFix(t, "a.txt", "from a, fixed for b\n")
	out := env.mustIntegrate(t, `{"action":"amend","reason":"go test fails on the combined result"}`)
	if out.HeadTree != fixed || out.AppliedCount != 2 || out.Amendments != 1 || out.Verified {
		t.Fatalf("amend = %+v, want head %s", out, fixed)
	}
	if !strings.HasPrefix(out.Verification, "planned") || len(out.Checks) != 2 {
		t.Fatalf("amend did not plan the new head: %q %+v", out.Verification, out.Checks)
	}
	for _, c := range out.Checks {
		if c.Status == "passed" || c.Status == "failed" {
			t.Fatalf("workflow not reset: %+v", out.Checks)
		}
	}
	h, w := env.head(t)
	if h.HeadTree != fixed || w.HeadTree != fixed || h.Amendments != 1 || h.AmendReason != "go test fails on the combined result" {
		t.Fatalf("head = %+v workflow %+v", h, w)
	}
	if got := env.revision(t, a.ID); got.State != store.RevisionApplied || got.Version != revA.Version {
		t.Fatalf("a = %+v", got)
	}
	if got := env.revision(t, b.ID); got.State != store.RevisionApplied || got.Version != revB.Version {
		t.Fatalf("b = %+v", got)
	}
	env.passChecks(t)
	if done := env.mustIntegrate(t, `{"action":"complete"}`); done.TaskState != protocol.AgentStateCompleted || done.Amendments != 1 {
		t.Fatalf("complete = %+v", done)
	}
}

func TestAmendLeadOnlyAndWritable(t *testing.T) {
	env, _, _ := twoAppliedFailing(t)
	env.stageFix(t, "a.txt", "fixed\n")
	before, _ := env.head(t)
	amend := `{"action":"amend","reason":"fix"}`
	strong := protocol.AgentCapabilities{Files: protocol.FilesReadWrite, Shell: protocol.ShellReadWrite, Network: true, SpawnMax: 3}
	coordinator := env.liveAgent(t, "coordinator", strong)
	if _, err := env.integrate(t, env.agentCtx(t, coordinator), amend); err == nil || !strings.Contains(err.Error(), "lead") {
		t.Fatalf("agent amend: %v", err)
	}
	if _, err := env.integrate(t, withChildExecution(WithDelegationChild(env.ctx), &childExecution{task: env.task}), amend); err == nil || !strings.Contains(err.Error(), "lead") {
		t.Fatalf("legacy child amend: %v", err)
	}
	scope := *tools.ScopeFrom(env.ctx)
	scope.ReadOnly = true
	if _, err := env.integrate(t, tools.WithScope(env.ctx, &scope), amend); err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Fatalf("read-only amend: %v", err)
	}
	if _, err := env.integrate(t, env.ctx, `{"action":"amend"}`); err == nil || !strings.Contains(err.Error(), "reason") {
		t.Fatalf("amend without reason: %v", err)
	}
	if after, _ := env.head(t); after.Version != before.Version || after.Amendments != 0 {
		t.Fatalf("refused amends moved the head: %+v", after)
	}
	if out := env.mustIntegrate(t, amend); out.Amendments != 1 {
		t.Fatalf("lead amend = %+v", out)
	}
}

func TestDeadEndTextPointsToAmend(t *testing.T) {
	for _, status := range []string{"failed", "blocked"} {
		t.Run(status, func(t *testing.T) {
			env, _, _ := twoAppliedFailing(t, status, "passed")
			c := env.writer(t, "c", "c.txt", "from c\n")
			check := func(args string) {
				t.Helper()
				_, err := env.integrate(t, env.ctx, args)
				if err == nil || !strings.Contains(err.Error(), amendAdvice) || !strings.Contains(err.Error(), "cancel") {
					t.Fatalf("%s refusal: %v", args, err)
				}
				if strings.Contains(err.Error(), "applied revisions are final") || strings.Contains(err.Error(), "cannot be dropped or amended") {
					t.Fatalf("%s still states the dead end: %v", args, err)
				}
			}
			check(`{"action":"accept","agent":"` + c.ID + `"}`)
			env.mustIntegrate(t, `{"action":"drop","agent":"`+c.ID+`","reason":"later"}`)
			check(`{"action":"complete"}`)
		})
	}
	env := runtimeVerificationEnv(t)
	tool, ok := env.e.Tools.Get("agent.integrate")
	if !ok {
		t.Fatal("agent.integrate not registered")
	}
	d := tool.Description()
	if !strings.Contains(d, amendAdvice) || !strings.Contains(d, "cancel the task") || strings.Contains(d, "Applied revisions are final") {
		t.Fatalf("description: %s", d)
	}
	if !strings.Contains(string(tool.Schema()), `"amend"`) {
		t.Fatalf("schema lacks amend: %s", tool.Schema())
	}
}

// The cancel report of an amended task says the integrated result also holds
// the lead's own amendments (count and latest reason), not only agent work.
func TestCancelReportMentionsAmendments(t *testing.T) {
	env, _, _ := twoAppliedFailing(t)
	env.stageFix(t, "a.txt", "first fix\n")
	env.mustIntegrate(t, `{"action":"amend","reason":"first attempt"}`)
	env.stageFix(t, "a.txt", "second fix\n")
	env.mustIntegrate(t, `{"action":"amend","reason":"lint still fails"}`)
	res := env.cancel(t)
	r := res.Integration
	if r == nil {
		t.Fatal("cancel result has no integration report")
	}
	if r.Amendments != 2 || r.AmendReason != "lint still fails" {
		t.Fatalf("report = %+v, want 2 amendments with the latest reason", r)
	}
	for _, want := range []string{"amended 2 time(s) by the lead", "lint still fails", "nothing was reverted"} {
		if !strings.Contains(r.Message, want) {
			t.Fatalf("report message %q lacks %q", r.Message, want)
		}
	}
}

// Without an amendment the report says nothing about amendments.
func TestCancelReportWithoutAmendments(t *testing.T) {
	env, _, _ := twoAppliedFailing(t)
	r := env.cancel(t).Integration
	if r == nil || r.Amendments != 0 || strings.Contains(r.Message, "amended") {
		t.Fatalf("report = %+v", r)
	}
}
