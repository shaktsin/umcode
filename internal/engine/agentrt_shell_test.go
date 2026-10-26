package engine

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

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

func setProjectNetwork(t *testing.T, e *Engine, projectID string, on bool) {
	t.Helper()
	if _, err := e.Store.UpdateProject(t.Context(), protocol.ProjectUpdateParams{ProjectID: projectID, Tools: &protocol.ProjectTools{Network: &on}}); err != nil {
		t.Fatal(err)
	}
}

func networkRejection(res agentrt.SpawnResult) string {
	for _, r := range res.Rejected {
		if r.Field == "capabilities.network" {
			return r.Reason
		}
	}
	return ""
}

// C1: the project's network setting is a ceiling at admission: an agent that
// asks for network in a project without it is refused network, visibly.
func TestAdmissionNetworkCeilingFromProject(t *testing.T) {
	env := runtimeRunnerFixture(t)
	limits := protocol.DefaultPlanLimits()
	limits.Network = true
	netCaps := protocol.AgentCapabilities{Files: protocol.FilesRead, Shell: protocol.ShellNone, Network: true}
	env.approvePlan(t, limits, protocol.PlanAgent{Name: "fetcher", Purpose: "fetch", Capabilities: netCaps})
	rt := agentrt.NewRuntime(env.e.Store, instantRunner{}, env.e.baseCtx, agentrt.WithResolveOptionsFunc(env.e.runtimeResolveOptions))
	env.e.AgentRuntime = rt
	(&shellSeams{}).install(env.e, fakeSandbox{}, nil)
	spawn := func(call string, caps protocol.AgentCapabilities) agentrt.SpawnResult {
		t.Helper()
		res, err := rt.Spawn(t.Context(), agentrt.SpawnRequest{TaskID: env.task.ID, CallID: call, PlanEntry: "fetcher", Capabilities: &caps, Assignment: protocol.AgentAssignment{Prompt: "go"}})
		if err != nil {
			t.Fatal(err)
		}
		return res
	}

	setProjectNetwork(t, env.e, env.th.ProjectID, false)
	res := spawn("off-on", netCaps)
	if res.Granted.Network || networkRejection(res) == "" {
		t.Fatalf("project off, agent on: granted %+v rejected %+v", res.Granted, res.Rejected)
	}

	setProjectNetwork(t, env.e, env.th.ProjectID, true)
	res = spawn("on-on", netCaps)
	if !res.Granted.Network || networkRejection(res) != "" {
		t.Fatalf("project on, agent on: granted %+v rejected %+v", res.Granted, res.Rejected)
	}
	res = spawn("on-off", protocol.AgentCapabilities{Files: protocol.FilesRead, Shell: protocol.ShellNone})
	if res.Granted.Network || networkRejection(res) != "" {
		t.Fatalf("project on, agent off: granted %+v rejected %+v", res.Granted, res.Rejected)
	}
}

// I4: one in-flight check per key, run outside the lock on its own context;
// callers wait with theirs; other keys are not blocked; only a pass is cached.
func TestShellChecksSingleflightOutsideLock(t *testing.T) {
	var c shellChecks
	block := make(chan struct{})
	var runs atomic.Int32
	slow := func(ctx context.Context) error {
		runs.Add(1)
		select {
		case <-block:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	results := make(chan error, 2)
	for range 2 {
		go func() { results <- c.do(context.Background(), context.Background(), "microvm", slow) }()
	}
	// Another key is not blocked behind the in-flight probe.
	done := make(chan error, 1)
	go func() { done <- c.do(context.Background(), context.Background(), "os:fake", func(context.Context) error { return nil }) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a check for another key waited behind the in-flight probe")
	}
	// A caller whose context ends stops waiting at once.
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err := c.do(ctx, context.Background(), "microvm", slow); err == nil || time.Since(start) > 2*time.Second {
		t.Fatalf("waiter with an ended context: err %v after %v", err, time.Since(start))
	}
	close(block)
	for range 2 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	if n := runs.Load(); n != 1 {
		t.Fatalf("probe ran %d times, want once for concurrent callers", n)
	}
	// A pass is cached.
	if err := c.do(context.Background(), context.Background(), "microvm", slow); err != nil || runs.Load() != 1 {
		t.Fatalf("cached pass: err %v runs %d", err, runs.Load())
	}
}

func TestShellChecksFailureIsRetried(t *testing.T) {
	var c shellChecks
	var runs atomic.Int32
	fail := errors.New("transient VM failure")
	run := func(context.Context) error {
		if runs.Add(1) == 1 {
			return fail
		}
		return nil
	}
	if err := c.do(context.Background(), context.Background(), "microvm", run); !errors.Is(err, fail) {
		t.Fatalf("first: %v", err)
	}
	if err := c.do(context.Background(), context.Background(), "microvm", run); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if err := c.do(context.Background(), context.Background(), "microvm", run); err != nil || runs.Load() != 2 {
		t.Fatalf("after pass: err %v runs %d", err, runs.Load())
	}
}

// A failed microVM probe does not disable the read-only shell until restart:
// the next admission probes again.
func TestFailedVMProbeRetriedOnNextAdmission(t *testing.T) {
	env, rt := shellFixture(t)
	seams := &shellSeams{vmErr: errors.New("microVM read-only self-check: the guest did not boot")}
	seams.install(env.e, fakeSandbox{}, nil)
	setProjectCompute(t, env.e, env.th.ProjectID, true)
	if res := spawnScout(t, env, rt, "vm-fail"); res.Granted.Shell != protocol.ShellNone {
		t.Fatalf("failed probe: granted %+v", res.Granted)
	}
	seams.vmErr = nil
	if res := spawnScout(t, env, rt, "vm-ok"); res.Granted.Shell != protocol.ShellReadOnly {
		t.Fatalf("retried probe: granted %+v rejected %+v", res.Granted, res.Rejected)
	}
	if n := seams.vmChecks.Load(); n != 2 {
		t.Fatalf("VM probe ran %d times, want 2 (failure not cached)", n)
	}
}

// A first microVM probe that hangs stalls neither a spawn that needs another
// confinement, nor the plan card's summary for it, nor Runtime.Close.
func TestBlockingVMProbeStallsNothingElse(t *testing.T) {
	env, rt := shellFixture(t)
	seams := &shellSeams{}
	seams.install(env.e, fakeSandbox{}, nil)
	entered := make(chan struct{})
	release := make(chan struct{})
	var relOnce sync.Once
	unblock := func() { relOnce.Do(func() { close(release) }) }
	defer unblock()
	var once sync.Once
	env.e.agentVMROCheck = func(ctx context.Context) error {
		once.Do(func() { close(entered) })
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	setProjectCompute(t, env.e, env.th.ProjectID, true)
	vmSpawn := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, err := rt.Spawn(ctx, agentrt.SpawnRequest{TaskID: env.task.ID, CallID: "vm", PlanEntry: "scout", Assignment: protocol.AgentAssignment{Prompt: "look"}})
		vmSpawn <- err
	}()
	<-entered

	// The project now uses the OS sandbox: its spawn and summary go ahead.
	setProjectCompute(t, env.e, env.th.ProjectID, false)
	type spawned struct {
		res agentrt.SpawnResult
		err error
	}
	quick := make(chan spawned, 1)
	go func() {
		res, err := rt.Spawn(t.Context(), agentrt.SpawnRequest{TaskID: env.task.ID, CallID: "os", PlanEntry: "scout", Assignment: protocol.AgentAssignment{Prompt: "look"}})
		quick <- spawned{res, err}
	}()
	select {
	case q := <-quick:
		res := q.res
		if q.err != nil {
			t.Fatal(q.err)
		}
		if res.Granted.Shell != protocol.ShellReadOnly {
			t.Fatalf("os sandbox spawn: granted %+v rejected %+v", res.Granted, res.Rejected)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a spawn needing the OS sandbox waited behind the microVM probe")
	}
	summary := make(chan string, 1)
	go func() { summary <- env.e.ConfinementSummary(context.Background(), env.th.ProjectID, protocol.ShellReadOnly) }()
	select {
	case got := <-summary:
		if got != "Commands run in a read-only sandbox" {
			t.Fatalf("summary %q", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ConfinementSummary waited behind the microVM probe")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	start := time.Now()
	_ = rt.Close(ctx)
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("Close took %v with a probe in flight", d)
	}
	// The probe finishes after Close: that spawn is refused, not admitted.
	unblock()
	select {
	case err := <-vmSpawn:
		if !errors.Is(err, agentrt.ErrClosed) {
			t.Fatalf("spawn whose probe finished after Close: %v, want ErrClosed", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("the microVM spawn never returned")
	}
}
