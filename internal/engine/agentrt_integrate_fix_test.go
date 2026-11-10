package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shaktsin/umcode/internal/agentrt"
	"github.com/shaktsin/umcode/internal/delegation"
	"github.com/shaktsin/umcode/internal/llm"
	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/store"
)

// finishingWriter runs a writer up to the runner's recording step: its
// checkout holds path=data, its revision is recorded with
// RecordFinishedRuntimeRevision, and it is still running (the scheduler has
// not written its terminal state yet).
func (env rvEnv) finishingWriter(t *testing.T, name, path, data string) protocol.RuntimeAgent {
	t.Helper()
	ctx, s, svc := t.Context(), env.e.Store, env.e.DelegationWorkspaceService
	a := env.liveAgent(t, name, protocol.AgentCapabilities{Files: protocol.FilesReadWrite})
	if _, err := s.EnsureRuntimeAgentExecution(ctx, a); err != nil {
		t.Fatal(err)
	}
	ref, err := svc.PrepareRuntime(ctx, env.task, a, true)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(ref.Root, path), []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	if a, err = s.RuntimeAgent(ctx, env.task.ID, a.ID); err != nil {
		t.Fatal(err)
	}
	snap, err := svc.SnapshotRuntime(ctx, env.task, a, ref)
	if err != nil {
		t.Fatal(err)
	}
	if a, err = s.RuntimeAgent(ctx, env.task.ID, a.ID); err != nil {
		t.Fatal(err)
	}
	a.Revision = snap.ResultRevision
	if err = s.UpdateRuntimeAgent(ctx, a, a.Version); err != nil {
		t.Fatal(err)
	}
	if a, err = s.RuntimeAgent(ctx, env.task.ID, a.ID); err != nil {
		t.Fatal(err)
	}
	ws, err := s.AgentWorkspace(ctx, ref.ID)
	if err != nil {
		t.Fatal(err)
	}
	if r, err := svc.RecordFinishedRuntimeRevision(ctx, env.task, a, ws); err != nil || r.State != store.RevisionSubmitted {
		t.Fatalf("record finished %s = %+v %v", name, r, err)
	}
	return a
}

// end persists the scheduler's terminal write for a.
func (env rvEnv) end(t *testing.T, id string, st protocol.AgentState) {
	t.Helper()
	a, err := env.e.Store.RuntimeAgent(t.Context(), env.task.ID, id)
	if err != nil {
		t.Fatal(err)
	}
	a.State = st
	if err = env.e.Store.UpdateRuntimeAgent(t.Context(), a, a.Version); err != nil {
		t.Fatal(err)
	}
}

// M2: a revision recorded by an agent that is still finishing keeps its
// place in the submission order, and cannot be accepted until it completes.
func TestAcceptWaitsForFinishingWriter(t *testing.T) {
	env := runtimeVerificationEnv(t)
	a := env.finishingWriter(t, "a", "a.txt", "from a\n") // seq 1, still running
	b := env.writer(t, "b", "b.txt", "from b\n")          // seq 2, completed
	if _, err := env.integrate(t, env.ctx, `{"action":"accept","agent":"`+b.ID+`"}`); err == nil || !strings.Contains(err.Error(), "submission order") || !strings.Contains(err.Error(), "still finishing") {
		t.Fatalf("accept b while a is finishing: err = %v", err)
	}
	if _, err := env.integrate(t, env.ctx, `{"action":"accept","agent":"`+a.ID+`"}`); err == nil || !strings.Contains(err.Error(), "still finishing") {
		t.Fatalf("accept a while it is finishing: err = %v", err)
	}
	env.end(t, a.ID, protocol.AgentStateCompleted)
	if out := env.mustIntegrate(t, `{"action":"accept","agent":"`+a.ID+`"}`); out.Revision.State != store.RevisionApplied {
		t.Fatalf("accept a = %+v", out)
	}
}

// Cancel wins the race after the revision was recorded: the revision is never
// integrable, says so on accept, is settled and does not block completion.
// Recording for an agent already cancelled is refused.
func TestCancelledAfterRecordIsNeverIntegrable(t *testing.T) {
	env := runtimeVerificationEnv(t)
	a := env.finishingWriter(t, "a", "a.txt", "from a\n")
	env.end(t, a.ID, protocol.AgentStateCancelled)
	if _, err := env.integrate(t, env.ctx, `{"action":"accept","agent":"`+a.ID+`"}`); err == nil || !strings.Contains(err.Error(), "was cancelled after submitting") {
		t.Fatalf("accept a cancelled agent's revision: err = %v", err)
	}
	st := env.mustIntegrate(t, `{"action":"status"}`)
	if !st.Settled {
		t.Fatalf("status = %+v, want settled", st)
	}
	if out := env.mustIntegrate(t, `{"action":"complete"}`); out.TaskState != protocol.AgentStateCompleted || !out.Settled {
		t.Fatalf("complete = %+v", out)
	}
	// The runner's record after the cancel already landed is refused.
	ctx, s := t.Context(), env.e.Store
	cur, err := s.RuntimeAgent(ctx, env.task.ID, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	ws, err := s.AgentWorkspace(ctx, cur.WorkspaceID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = env.e.DelegationWorkspaceService.RecordFinishedRuntimeRevision(ctx, env.task, cur, ws); err == nil || !strings.Contains(err.Error(), "cancelled") {
		t.Fatalf("record for a cancelled agent: err = %v", err)
	}
}

// M3: a submitted revision of an agent that ended interrupted (or failed)
// must be dropped explicitly before completion.
func TestCompleteRequiresDropOfInterruptedRevision(t *testing.T) {
	env := runtimeVerificationEnv(t)
	a := env.finishingWriter(t, "a", "a.txt", "from a\n")
	env.end(t, a.ID, protocol.AgentStateInterrupted)
	if st := env.mustIntegrate(t, `{"action":"status"}`); st.Settled {
		t.Fatalf("status = %+v, want unsettled", st)
	}
	if _, err := env.integrate(t, env.ctx, `{"action":"accept","agent":"`+a.ID+`"}`); err == nil || !strings.Contains(err.Error(), "ended interrupted") {
		t.Fatalf("accept an interrupted agent's revision: err = %v", err)
	}
	if _, err := env.integrate(t, env.ctx, `{"action":"complete"}`); err == nil || !strings.Contains(err.Error(), a.ID) || !strings.Contains(err.Error(), "drop it") {
		t.Fatalf("complete with an interrupted agent's revision: err = %v", err)
	}
	env.mustIntegrate(t, `{"action":"drop","agent":"`+a.ID+`","reason":"interrupted; redo it"}`)
	if out := env.mustIntegrate(t, `{"action":"complete"}`); out.TaskState != protocol.AgentStateCompleted {
		t.Fatalf("complete = %+v", out)
	}
}

// CleanupRuntime discards an accepted agent's checkout but keeps the checkout
// of a revision the lead resolved by hand.
func TestCleanupRuntimeKeepsManuallyResolvedWorkspace(t *testing.T) {
	env := runtimeVerificationEnv(t)
	tracked := strings.Fields(delegationGit(t, env.root, "ls-files"))[0]
	a := env.writer(t, "a", tracked, "from a\n")
	b := env.writer(t, "b", tracked, "from b\n")
	env.mustIntegrate(t, `{"action":"accept","agent":"`+a.ID+`"}`)
	env.passChecks(t)
	if out := env.mustIntegrate(t, `{"action":"accept","agent":"`+b.ID+`"}`); out.Revision.State != store.RevisionConflict {
		t.Fatalf("accept b = %+v, want a conflict", out)
	}
	if err := os.WriteFile(filepath.Join(env.root, tracked), []byte("from a and b\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	delegationGit(t, env.root, "add", tracked)
	if out := env.mustIntegrate(t, `{"action":"resolved","agent":"`+b.ID+`"}`); out.Revision.State != store.RevisionApplied || len(out.Checks) != 2 {
		t.Fatalf("resolved b = %+v", out)
	}
	env.passChecks(t)
	if out := env.mustIntegrate(t, `{"action":"complete"}`); out.TaskState != protocol.AgentStateCompleted {
		t.Fatalf("complete = %+v", out)
	}
	for id, wantCleaned := range map[string]bool{a.ID: true, b.ID: false} {
		w, err := env.e.Store.AgentWorkspace(t.Context(), env.revision(t, id).WorkspaceID)
		if err != nil || w.Cleaned != wantCleaned {
			t.Fatalf("workspace of %s = %+v %v, want cleaned=%v", id, w, err, wantCleaned)
		}
		if _, statErr := os.Stat(w.Root); (statErr == nil) == wantCleaned {
			t.Fatalf("workspace dir of %s exists=%v, want cleaned=%v", id, statErr == nil, wantCleaned)
		}
	}
}

// The scheduler persists `failed` with the blocker when the runner cannot
// record a finished writer's revision.
func TestSchedulerFailsWriterWhenRevisionNotRecorded(t *testing.T) {
	env := runtimeRunnerFixture(t)
	env.e.RuntimeWorkspaces = failingRecorder{managedChildWorkspaces{service: env.e.DelegationWorkspaceService}}
	env.p.script = func(n int, _ context.Context, _ llm.Request) []llm.Event {
		if n == 1 {
			return toolReply("w1", "file.write", `{"path":"new.txt","content":"agent"}`)
		}
		return textReply("done")
	}
	shared := delegation.NewBudget(protocol.ExecutionLimits{MaxTokens: 100000, MaxCostUSD: 100, MaxDurationMinutes: 10}, protocol.UsageTotals{}, nil)
	rt := env.runtimeFor(t, delegation.WithBudget(t.Context(), shared), writeCaps)
	res, err := rt.Spawn(t.Context(), agentrt.SpawnRequest{TaskID: env.task.ID, CallID: "record-fails", PlanEntry: "worker", Assignment: protocol.AgentAssignment{Prompt: "write"}})
	if err != nil {
		t.Fatal(err)
	}
	got := waitAgentTerminal(t, rt, env.task.ID, res.Agent.ID)
	if got.State != protocol.AgentStateFailed || !strings.Contains(got.Result.Blocker, "revision not recorded") {
		t.Fatalf("agent = %s %+v, want failed with the record blocker", got.State, got.Result)
	}
	if _, err = env.e.Store.AgentRevision(t.Context(), env.task.ID, got.ID); err == nil {
		t.Fatal("a revision exists for the failed agent")
	}
}
