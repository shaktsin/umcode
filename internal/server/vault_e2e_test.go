package server_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/shaktsin/umcode/internal/protocol"
)

// shellEditScenario passes a planned check, then edits a tracked file through
// the shell. It returns the thread, the work and the tools approvals were asked for.
func shellEditScenario(t *testing.T) (*harness, protocol.Thread, protocol.Work, []string) {
	t.Helper()
	if _, err := exec.LookPath("npm"); err != nil {
		t.Skip("npm not installed")
	}
	h := newHarness(t, nil)
	h.call(protocol.MethodProjectUpdate, protocol.ProjectUpdateParams{ProjectID: h.proj.ID,
		Tools: &protocol.ProjectTools{Network: boolPtr(true)}}, &h.proj)
	os.WriteFile(filepath.Join(h.ws, "package.json"), []byte(`{"scripts":{"test":"true"}}`), 0o600)
	os.WriteFile(filepath.Join(h.ws, "tracked.txt"), []byte("one\n"), 0o600)
	for _, args := range [][]string{{"add", "-A"}, {"commit", "-m", "files"}} {
		if out, err := exec.Command("git", append([]string{"-C", h.ws}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	h.addKey("claude", "k", "sk-1")
	h.fake.push(
		toolReply("verification__plan", `{}`),
		toolReply("verification__run", `{"checks":[{"label":"test","command":"npm run test","reason":"unit"}]}`),
		toolReply("shell__run", `{"command":"echo two >> tracked.txt"}`),
		textReply("Done."),
	)
	th := startWorkThread(h)
	var res protocol.TurnStartResult
	h.call(protocol.MethodTurnStart, protocol.TurnStartParams{ThreadID: th.ID, Text: "check and tweak"}, &res)
	var asked []string
	turn, _, _ := h.waitTurn(res.Turn.ID, func(a protocol.Approval) bool {
		asked = append(asked, a.Tool)
		return true
	})
	if turn.Status != protocol.TurnCompleted {
		t.Fatalf("turn = %+v", turn)
	}
	works := listWorks(h, th.ID)
	if len(works) != 1 {
		t.Fatalf("works = %+v", works)
	}
	return h, th, works[0], asked
}

func TestShellEditAfterPassKeepsWorkOpen(t *testing.T) {
	h, _, w, asked := shellEditScenario(t)
	var d protocol.WorkDetail
	h.call(protocol.MethodWorkGet, protocol.WorkGetParams{WorkID: w.ID}, &d)
	if d.Work.Status != protocol.WorkOpen {
		t.Fatalf("work status = %s, want open after a shell edit following the pass", d.Work.Status)
	}
	stale := 0
	for _, n := range d.Nodes {
		if n.Kind == protocol.NodeCriterion && n.Status == protocol.StatusStale {
			stale++
		}
	}
	if stale != 1 {
		t.Fatalf("stale criteria = %d: %+v", stale, d.Nodes)
	}
	shell := 0
	for _, tool := range asked {
		switch tool {
		case "shell.run":
			shell++
		case "verification.run":
		default:
			t.Fatalf("unexpected approval for %q: fingerprints and GC must never ask", tool)
		}
	}
	if shell != 1 {
		t.Fatalf("shell approvals = %d, want exactly 1 (asked %v)", shell, asked)
	}
}

func TestWorkGetActiveOnly(t *testing.T) {
	h, _, w, _ := shellEditScenario(t)
	var all, active protocol.WorkDetail
	h.call(protocol.MethodWorkGet, protocol.WorkGetParams{WorkID: w.ID}, &all)
	h.call(protocol.MethodWorkGet, protocol.WorkGetParams{WorkID: w.ID, ActiveOnly: true}, &active)
	stale := 0
	for _, e := range all.Evidence {
		if e.StaleAt != nil {
			stale++
		}
	}
	if stale == 0 {
		t.Fatalf("scenario produced no stale evidence: %+v", all.Evidence)
	}
	if len(active.Evidence) != len(all.Evidence)-stale {
		t.Fatalf("active %d, all %d, stale %d", len(active.Evidence), len(all.Evidence), stale)
	}
	for _, e := range active.Evidence {
		if e.StaleAt != nil || e.Availability == protocol.AvailUnavailable {
			t.Fatalf("inactive evidence in activeOnly: %+v", e)
		}
	}
}

func TestVaultStats(t *testing.T) {
	h := newHarness(t, nil)
	var empty protocol.VaultStats
	h.call(protocol.MethodVaultStats, struct{}{}, &empty)
	if empty.Objects != 0 || empty.Bytes != 0 {
		t.Fatalf("empty vault stats = %+v", empty)
	}
	h2, _, _, _ := shellEditScenario(t)
	var s protocol.VaultStats
	h2.call(protocol.MethodVaultStats, struct{}{}, &s)
	if s.Objects < 1 || s.Bytes <= 0 {
		t.Fatalf("stats after a verified attempt = %+v", s)
	}
}
