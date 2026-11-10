package engine

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/shaktsin/umcode/internal/agentrt"
	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/sandbox"
	"github.com/shaktsin/umcode/internal/tools"
)

var scoutCaps = protocol.AgentCapabilities{Files: protocol.FilesRead, Shell: protocol.ShellReadOnly}

// shellSeams installs fake confinement signals and counts the read-only
// self-checks that actually run.
type shellSeams struct {
	osChecks, vmChecks atomic.Int32
	osErr, vmErr       error
}

func (s *shellSeams) install(e *Engine, sb sandbox.Sandbox, vmErr error) {
	e.agentSandbox = func() sandbox.Sandbox { return sb }
	e.agentVMCheck = func() error { return vmErr }
	e.agentSandboxROCheck = func(sandbox.Sandbox) error { s.osChecks.Add(1); return s.osErr }
	e.agentVMROCheck = func(context.Context) error { s.vmChecks.Add(1); return s.vmErr }
}

// setProjectCompute flips the toggle in the store; Projects.Update would run
// the real KVM preflight.
func setProjectCompute(t *testing.T, e *Engine, projectID string, on bool) {
	t.Helper()
	if _, err := e.Store.UpdateProject(t.Context(), protocol.ProjectUpdateParams{ProjectID: projectID, Tools: &protocol.ProjectTools{Compute: &on}}); err != nil {
		t.Fatal(err)
	}
}

func spawnScout(t *testing.T, env runtimeRunnerEnv, rt *agentrt.Runtime, call string) agentrt.SpawnResult {
	t.Helper()
	res, err := rt.Spawn(t.Context(), agentrt.SpawnRequest{TaskID: env.task.ID, CallID: call, PlanEntry: "scout", Assignment: protocol.AgentAssignment{Prompt: "look"}})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func shellRejection(res agentrt.SpawnResult) string {
	for _, r := range res.Rejected {
		if r.Field == "capabilities.shell" {
			return r.Reason
		}
	}
	return ""
}

// instantRunner finishes every agent at once: these tests only look at what
// admission granted.
type instantRunner struct{}

func (instantRunner) Run(context.Context, protocol.RuntimeAgent) (protocol.AgentResult, error) {
	return protocol.AgentResult{}, nil
}

func shellFixture(t *testing.T) (runtimeRunnerEnv, *agentrt.Runtime) {
	t.Helper()
	env := runtimeRunnerFixture(t)
	env.approvePlan(t, protocol.DefaultPlanLimits(), protocol.PlanAgent{Name: "scout", Purpose: "investigate", Capabilities: scoutCaps})
	rt := agentrt.NewRuntime(env.e.Store, instantRunner{}, env.e.baseCtx,
		agentrt.WithResolveOptionsFunc(env.e.runtimeResolveOptions))
	env.e.AgentRuntime = rt
	return env, rt
}

func TestShellReadOnlyDowngradedWithoutSandbox(t *testing.T) {
	env, rt := shellFixture(t)
	(&shellSeams{}).install(env.e, nil, nil)
	res := spawnScout(t, env, rt, "no-sandbox")
	if res.Granted.Shell != protocol.ShellNone {
		t.Fatalf("read-only shell without a sandbox must be none, granted %+v", res.Granted)
	}
	if reason := shellRejection(res); !strings.Contains(reason, "sandbox") {
		t.Fatalf("rejection does not say why: %q (%+v)", reason, res.Rejected)
	}
}

func TestShellReadOnlyOfferedPerProject(t *testing.T) {
	env, rt := shellFixture(t)
	seams := &shellSeams{}
	seams.install(env.e, fakeSandbox{}, nil)

	// Microvm toggle off, OS sandbox passes its self-check: read-only offered.
	res := spawnScout(t, env, rt, "os")
	if res.Granted.Shell != protocol.ShellReadOnly || shellRejection(res) != "" {
		t.Fatalf("OS sandbox project: granted %+v rejected %+v", res.Granted, res.Rejected)
	}
	// The self-check ran once and is cached for the process.
	spawnScout(t, env, rt, "os-again")
	if n := seams.osChecks.Load(); n != 1 {
		t.Fatalf("OS self-check ran %d times, want once", n)
	}
	if seams.vmChecks.Load() != 0 {
		t.Fatal("the microVM probe ran for a project that does not use it")
	}

	// Toggle on and the VM is unavailable: none, never the OS sandbox.
	setProjectCompute(t, env.e, env.th.ProjectID, true)
	seams.install(env.e, fakeSandbox{}, nil)
	env.e.agentVMCheck = func() error { return errors.New("this Linux host has no /dev/kvm device") }
	res = spawnScout(t, env, rt, "vm-down")
	if res.Granted.Shell != protocol.ShellNone || !strings.Contains(shellRejection(res), "/dev/kvm") {
		t.Fatalf("unavailable VM: granted %+v rejected %+v", res.Granted, res.Rejected)
	}

	// Toggle on, VM available and its guest enforces read-only: offered, and
	// the guest probe runs lazily once.
	env.e.agentVMCheck = func() error { return nil }
	res = spawnScout(t, env, rt, "vm-up")
	spawnScout(t, env, rt, "vm-up-again")
	if res.Granted.Shell != protocol.ShellReadOnly {
		t.Fatalf("available VM: granted %+v rejected %+v", res.Granted, res.Rejected)
	}
	if n := seams.vmChecks.Load(); n != 1 {
		t.Fatalf("VM probe ran %d times, want once (lazy, cached)", n)
	}
}

func TestShellReadOnlyDowngradedWhenSelfCheckFails(t *testing.T) {
	for name, conf := range map[string]bool{"os sandbox": false, "microVM": true} {
		t.Run(name, func(t *testing.T) {
			env, rt := shellFixture(t)
			seams := &shellSeams{osErr: errors.New("sandbox read-only self-check: a file was created in the read-only root"), vmErr: errors.New("microVM read-only self-check: a file was created in the read-only workspace")}
			seams.install(env.e, fakeSandbox{}, nil)
			setProjectCompute(t, env.e, env.th.ProjectID, conf)
			res := spawnScout(t, env, rt, "bad")
			if res.Granted.Shell != protocol.ShellNone || !strings.Contains(shellRejection(res), "self-check") {
				t.Fatalf("granted %+v rejected %+v", res.Granted, res.Rejected)
			}
		})
	}
}

func TestAgentConfinementSummary(t *testing.T) {
	cases := []struct {
		conf   tools.Confinement
		shell  protocol.ShellAccess
		reason string
		want   string
	}{
		{tools.ConfMicroVM, protocol.ShellReadOnly, "", "Commands run in a read-only microVM"},
		{tools.ConfOSSandbox, protocol.ShellReadOnly, "", "Commands run in a read-only sandbox"},
		{tools.ConfMicroVM, protocol.ShellReadWrite, "", "Commands run in a microVM and can change only the agent's own copy of the project"},
		{tools.ConfOSSandbox, protocol.ShellReadWrite, "", "Commands run in a sandbox and can change only the agent's own copy of the project"},
		{tools.ConfNone, protocol.ShellReadOnly, "no sandbox available", "No shell available (no sandbox available)"},
		{tools.ConfNone, protocol.ShellReadWrite, "", "No shell available (no confined shell on this project)"},
		{tools.ConfOSSandbox, protocol.ShellNone, "", "No shell access"},
		{tools.ConfOSSandbox, "", "", "No shell access"},
		{tools.ConfOSSandbox, protocol.ShellReadOnly, "sandbox read-only self-check failed", "No shell available (sandbox read-only self-check failed)"},
	}
	for _, c := range cases {
		if got := AgentConfinementSummary(c.conf, c.shell, c.reason); got != c.want {
			t.Errorf("AgentConfinementSummary(%s, %s, %q) = %q, want %q", c.conf, c.shell, c.reason, got, c.want)
		}
	}
}

func TestConfinementSummaryForProject(t *testing.T) {
	env := runtimeRunnerFixture(t)
	seams := &shellSeams{}
	seams.install(env.e, fakeSandbox{}, nil)
	if got := env.e.ConfinementSummary(t.Context(), env.th.ProjectID, protocol.ShellReadOnly); got != "Commands run in a read-only sandbox" {
		t.Fatalf("summary = %q", got)
	}
	seams.osErr = errors.New("sandbox read-only self-check failed")
	env.e.shellChecks.reset()
	if got := env.e.ConfinementSummary(t.Context(), env.th.ProjectID, protocol.ShellReadOnly); got != "No shell available (sandbox read-only self-check failed)" {
		t.Fatalf("summary = %q", got)
	}
	if got := env.e.ConfinementSummary(t.Context(), env.th.ProjectID, protocol.ShellReadWrite); !strings.HasPrefix(got, "Commands run in a sandbox") {
		t.Fatalf("read-write needs no read-only check: %q", got)
	}
}
