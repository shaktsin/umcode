package engine

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/shaktsin/umcode/internal/agentrt"
	"github.com/shaktsin/umcode/internal/compute"
	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/sandbox"
	"github.com/shaktsin/umcode/internal/tools"
)

// shellChecks caches the read-only self-check results, one per confinement
// mechanism for the life of the process. A check that ended because its
// context ended is not cached.
type shellChecks struct {
	mu      sync.Mutex
	results map[string]error
}

func (c *shellChecks) do(ctx context.Context, key string, run func(context.Context) error) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err, ok := c.results[key]; ok {
		return err
	}
	err := run(ctx)
	if ctx.Err() != nil {
		return err
	}
	if c.results == nil {
		c.results = map[string]error{}
	}
	c.results[key] = err
	return err
}

// reset forgets every cached result. A test seam.
func (c *shellChecks) reset() {
	c.mu.Lock()
	c.results = nil
	c.mu.Unlock()
}

// shellOffer is what a project's runtime agents may get for a shell.
type shellOffer struct {
	Confinement tools.Confinement
	// ReadOnly reports that a read-only shell can be enforced: the confinement
	// the project would use passed its read-only self-check.
	ReadOnly bool
	// Reason says why there is no shell at all (Confinement none) or why the
	// read-only shell is not offered.
	Reason string
}

// runtimeConfinementWhy is runtimeConfinement with the reason for none.
func (e *Engine) runtimeConfinementWhy(scope *tools.Scope) (tools.Confinement, string) {
	vmCheck := e.agentVMCheck
	if vmCheck == nil {
		vmCheck = compute.CheckHostAvailability
	}
	hostSandbox := e.agentSandbox
	if hostSandbox == nil {
		hostSandbox = func() sandbox.Sandbox {
			if e.Cfg != nil && strings.EqualFold(e.Cfg.Tools.HostSandbox, "off") {
				return nil
			}
			return detectHostSandbox()
		}
	}
	var vmErr error
	if scope != nil && scope.UseCompute {
		vmErr = vmCheck()
	}
	sb := hostSandbox()
	conf := tools.ShellConfinement(scope, scope != nil && scope.UseCompute && vmErr == nil, sb)
	switch {
	case conf != tools.ConfNone:
		return conf, ""
	case scope == nil:
		return conf, "no project scope"
	case scope.UseCompute:
		return conf, fmt.Sprintf("the project's microVM is unavailable (%v); commands never fall back to the host sandbox", vmErr)
	}
	return conf, "no command sandbox (bwrap or sandbox-exec) is available on this host"
}

// shellOfferFor resolves the confinement a project would use and runs its
// read-only self-check (once per process, on first need).
func (e *Engine) shellOfferFor(ctx context.Context, scope *tools.Scope) shellOffer {
	conf, why := e.runtimeConfinementWhy(scope)
	offer := shellOffer{Confinement: conf, Reason: why}
	var err error
	switch conf {
	case tools.ConfMicroVM:
		err = e.shellChecks.do(ctx, "microvm", e.vmReadOnlyCheck)
	case tools.ConfOSSandbox:
		sb := e.hostSandboxForCheck()
		err = e.shellChecks.do(ctx, "os:"+sb.Name(), func(context.Context) error { return e.osReadOnlyCheck(sb) })
	default:
		return offer
	}
	if err != nil {
		offer.Reason = err.Error()
		return offer
	}
	offer.ReadOnly = true
	return offer
}

func (e *Engine) hostSandboxForCheck() sandbox.Sandbox {
	if e.agentSandbox != nil {
		return e.agentSandbox()
	}
	if e.Cfg != nil && strings.EqualFold(e.Cfg.Tools.HostSandbox, "off") {
		return nil
	}
	return detectHostSandbox()
}

func (e *Engine) osReadOnlyCheck(sb sandbox.Sandbox) error {
	if e.agentSandboxROCheck != nil {
		return e.agentSandboxROCheck(sb)
	}
	return sandbox.SelfCheckReadOnly(sb, os.TempDir())
}

// vmReadOnlyCheck probes a real guest: it tries to create a file under the
// read-only workspace mount, which must fail. Unverified without a hypervisor.
func (e *Engine) vmReadOnlyCheck(ctx context.Context) error {
	if e.agentVMROCheck != nil {
		return e.agentVMROCheck(ctx)
	}
	runner := e.agentCompute
	if runner == nil {
		runner = compute.NewBundledRunner()
	}
	return compute.SelfCheckReadOnly(ctx, runner, os.TempDir())
}

// projectScope is the part of a project's scope that decides its shell
// confinement, built from the same setting the turn scope uses.
func (e *Engine) projectShellScope(ctx context.Context, projectID string) (*tools.Scope, error) {
	p, err := e.Projects.Get(ctx, projectID)
	if err != nil {
		return nil, err
	}
	return &tools.Scope{ProjectID: p.ID, UseCompute: boolOr(p.Tools.Compute, false)}, nil
}

// runtimeResolveOptions computes the capability resolution options for a
// task at admission time, from the confinement its project would use. It
// fails closed: when the project cannot be read, no shell is offered.
func (e *Engine) runtimeResolveOptions(ctx context.Context, taskID string) agentrt.ResolveOptions {
	task, err := e.Store.AgentTask(ctx, taskID)
	if err != nil {
		return agentrt.ResolveOptions{ShellUnavailable: fmt.Sprintf("the task could not be read: %v", err)}
	}
	scope, err := e.projectShellScope(ctx, task.ProjectID)
	if err != nil {
		return agentrt.ResolveOptions{ShellUnavailable: fmt.Sprintf("the project could not be read: %v", err)}
	}
	offer := e.shellOfferFor(ctx, scope)
	opts := agentrt.ResolveOptions{ShellReadOnlySupported: offer.ReadOnly}
	switch {
	case offer.Confinement == tools.ConfNone:
		opts.ShellUnavailable = offer.Reason
	case !offer.ReadOnly:
		opts.ShellReadOnlyReason = offer.Reason
	}
	if offer.Reason != "" {
		e.logShellDowngrade(task.ProjectID, offer)
	}
	return opts
}

// logShellDowngrade logs a project's shell downgrade once per distinct reason.
func (e *Engine) logShellDowngrade(projectID string, offer shellOffer) {
	if e.Log == nil {
		return
	}
	if _, seen := e.shellDowngrades.LoadOrStore(projectID+"\x00"+offer.Reason, struct{}{}); seen {
		return
	}
	e.Log.Warn("runtime agent shell downgraded", "project", projectID, "confinement", string(offer.Confinement), "read_only_offered", offer.ReadOnly, "reason", offer.Reason)
}

// AgentConfinementSummary says in plain words where an agent's commands run,
// for the plan card. reason is why the shell is unavailable, if it is.
func AgentConfinementSummary(conf tools.Confinement, shell protocol.ShellAccess, reason string) string {
	switch {
	case (shell == protocol.ShellNone || shell == "") && reason == "":
		return "No shell access"
	case reason != "":
		return fmt.Sprintf("No shell available (%s)", reason)
	case conf == tools.ConfNone:
		return "No shell available (no confined shell on this project)"
	}
	where := "sandbox"
	if conf == tools.ConfMicroVM {
		where = "microVM"
	}
	if shell == protocol.ShellReadOnly {
		return "Commands run in a read-only " + where
	}
	return fmt.Sprintf("Commands run in a %s and can change only the agent's own copy of the project", where)
}

// ConfinementSummary describes, for the plan card, where a shell of the given
// level would run for the project's agents, including the self-check outcome.
func (e *Engine) ConfinementSummary(ctx context.Context, projectID string, shell protocol.ShellAccess) string {
	if shell == protocol.ShellNone || shell == "" {
		return AgentConfinementSummary(tools.ConfNone, shell, "")
	}
	scope, err := e.projectShellScope(ctx, projectID)
	if err != nil {
		return AgentConfinementSummary(tools.ConfNone, shell, fmt.Sprintf("the project could not be read: %v", err))
	}
	offer := e.shellOfferFor(ctx, scope)
	switch {
	case offer.Confinement == tools.ConfNone:
		return AgentConfinementSummary(offer.Confinement, shell, offer.Reason)
	case shell == protocol.ShellReadOnly && !offer.ReadOnly:
		return AgentConfinementSummary(offer.Confinement, shell, offer.Reason)
	}
	return AgentConfinementSummary(offer.Confinement, shell, "")
}
