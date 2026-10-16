package server_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/shaktsin/umcode/internal/client"
	"github.com/shaktsin/umcode/internal/plugins"
	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/store"
)

func commitDelegationFixture(t *testing.T, h *harness) {
	t.Helper()
	for _, args := range [][]string{{"add", "."}, {"commit", "-m", "committed instructions"}} {
		out, err := exec.Command("git", append([]string{"-C", h.ws}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
}
func TestDelegationRPCAdmissionReplayAndAuthorization(t *testing.T) {
	h := newHarness(t, nil)
	commitDelegationFixture(t, h)
	h.addKey("claude", "test", "fake")
	var th, other protocol.Thread
	h.call(protocol.MethodThreadStart, protocol.ThreadStartParams{ProjectID: h.proj.ID, Title: "delegation"}, &th)
	h.call(protocol.MethodThreadStart, protocol.ThreadStartParams{ProjectID: h.proj.ID, Title: "other"}, &other)
	nonadmin, err := client.Dial(h.ctx, h.eng.Cfg.Runtime.SocketPath, "tool-client", false)
	if err != nil {
		t.Fatal(err)
	}
	defer nonadmin.Close()
	if err = nonadmin.Call(h.ctx, protocol.MethodDelegationEnable, protocol.ThreadIDParams{ThreadID: th.ID}, nil); err == nil {
		t.Fatal("non-app activation accepted")
	}
	var activation protocol.DelegationEnableResult
	h.call(protocol.MethodDelegationEnable, protocol.ThreadIDParams{ThreadID: th.ID}, &activation)
	if activation.ActivationToken == "" {
		t.Fatal("activation token missing")
	}
	h.callErr(protocol.MethodTurnStart, protocol.TurnStartParams{ThreadID: other.ID, Text: "foreign", DelegationActivationToken: activation.ActivationToken})
	h.fake.push(textReply("done"))
	var started protocol.TurnStartResult
	h.call(protocol.MethodTurnStart, protocol.TurnStartParams{ThreadID: th.ID, Text: "implement", DelegationActivationToken: activation.ActivationToken}, &started)
	h.waitTurn(started.Turn.ID, nil)
	task, err := h.eng.Store.AgentTaskForRootTurn(h.ctx, th.ID, started.Turn.ID)
	if err != nil || task.Activation != protocol.AgentActivationExplicitUser {
		t.Fatalf("admission missing: %+v %v", task, err)
	}
	h.callErr(protocol.MethodTurnStart, protocol.TurnStartParams{ThreadID: th.ID, Text: "replay", DelegationActivationToken: activation.ActivationToken})
	h.callErr(protocol.MethodTurnStart, protocol.TurnStartParams{ThreadID: other.ID, Text: "foreign task", DelegationTaskID: task.ID})
	if _, err = h.eng.Store.AgentTaskForRootTurn(h.ctx, other.ID, started.Turn.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("foreign root lookup: %v", err)
	}
}
func TestDelegationSubmittedInstalledSkill(t *testing.T) {
	h := newHarness(t, nil)
	commitDelegationFixture(t, h)
	h.addKey("claude", "test", "fake")
	root := t.TempDir()
	dir := filepath.Join(root, "skills", "subagent-driven-development")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	writeFixtureFile(t, filepath.Join(root, "plugin.json"), `{"$schema":"https://agent-plugins.org/schemas/1.0.0/plugin.schema.json","name":"superpowers","version":"1.0.0"}`)
	writeFixtureFile(t, filepath.Join(dir, "SKILL.md"), "---\nname: subagent-driven-development\ndescription: Delegates work\n---\nUse subagents.")
	installServerPlugin(t, h, root, plugins.InstallManaged)
	var th protocol.Thread
	h.call(protocol.MethodThreadStart, protocol.ThreadStartParams{ProjectID: h.proj.ID, Title: "skill"}, &th)
	// Registry inspection/selection must have no effect before submission.
	var info protocol.SkillGetResult
	h.call(protocol.MethodSkillGet, protocol.SkillNameParams{Name: "superpowers:subagent-driven-development", ProjectID: h.proj.ID}, &info)
	var count int
	if err := h.eng.Store.DB.QueryRow(`SELECT count(*) FROM agent_tasks`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("selection activated: %d %v", count, err)
	}
	h.fake.push(textReply("done"))
	var started protocol.TurnStartResult
	h.call(protocol.MethodTurnStart, protocol.TurnStartParams{ThreadID: th.ID, Text: "/superpowers:subagent-driven-development implement"}, &started)
	h.waitTurn(started.Turn.ID, nil)
	task, err := h.eng.Store.AgentTaskForRootTurn(h.ctx, th.ID, started.Turn.ID)
	if err != nil || task.Activation != protocol.AgentActivationInstalledSkill {
		t.Fatalf("submitted installed skill not admitted: %+v %v", task, err)
	}
}

// Cancellation must be an app-owned action scoped to the owning parent chat.
func TestDelegationRPCCancel(t *testing.T) {
	h := newHarness(t, nil)
	commitDelegationFixture(t, h)
	h.addKey("claude", "test", "fake")
	var th, other protocol.Thread
	h.call(protocol.MethodThreadStart, protocol.ThreadStartParams{ProjectID: h.proj.ID}, &th)
	h.call(protocol.MethodThreadStart, protocol.ThreadStartParams{ProjectID: h.proj.ID}, &other)
	var activation protocol.DelegationEnableResult
	h.call(protocol.MethodDelegationEnable, protocol.ThreadIDParams{ThreadID: th.ID}, &activation)
	h.fake.push(textReply("ready"))
	var started protocol.TurnStartResult
	h.call(protocol.MethodTurnStart, protocol.TurnStartParams{ThreadID: th.ID, Text: "work", DelegationActivationToken: activation.ActivationToken}, &started)
	h.waitTurn(started.Turn.ID, nil)
	task, err := h.eng.Store.AgentTaskForRootTurn(h.ctx, th.ID, started.Turn.ID)
	if err != nil {
		t.Fatal(err)
	}
	p := map[string]string{"threadId": th.ID, "taskId": task.ID}
	nonadmin, err := client.Dial(h.ctx, h.eng.Cfg.Runtime.SocketPath, "tool-client", false)
	if err != nil {
		t.Fatal(err)
	}
	defer nonadmin.Close()
	if err = nonadmin.Call(h.ctx, "delegation/cancel", p, nil); err == nil {
		t.Fatal("non-app cancellation accepted")
	}
	h.callErr("delegation/cancel", map[string]string{"threadId": other.ID, "taskId": task.ID})
	// Revocation must still work after the parent changes project membership.
	if _, err = h.eng.Store.DB.ExecContext(h.ctx, `UPDATE threads SET project_id='' WHERE id=?`, th.ID); err != nil {
		t.Fatal(err)
	}
	var result struct {
		Task protocol.AgentTask `json:"task"`
	}
	h.call("delegation/cancel", p, &result)
	if result.Task.State != protocol.AgentStateCancelled {
		t.Fatalf("task not cancelled: %+v", result)
	}
	h.call("delegation/cancel", p, &result)
	h.callErr(protocol.MethodTurnStart, protocol.TurnStartParams{ThreadID: th.ID, Text: "late resume", DelegationTaskID: task.ID})
}
