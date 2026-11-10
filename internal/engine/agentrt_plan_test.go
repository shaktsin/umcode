package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/shaktsin/umcode/internal/agentrt"
	"github.com/shaktsin/umcode/internal/llm"
	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/store"
	"github.com/shaktsin/umcode/internal/tools"
)

type planEnv struct {
	e     *Engine
	th    protocol.Thread
	root  string
	turn  protocol.Turn
	task  protocol.AgentTask
	lead  context.Context
	seams *shellSeams
	// endTurn ends the fixture's live activated turn and waits for it.
	endTurn func()
}

// planFixture admits a delegation task without any plan and
// returns the lead's tool context for it. Confinement is faked: a host
// sandbox whose read-only self-check passes.
func planFixture(t *testing.T) planEnv {
	t.Helper()
	e, th, root, p := delegationFixture(t)
	seams := &shellSeams{}
	seams.install(e, fakeSandbox{}, nil)
	activation, err := e.PrepareDelegation(t.Context(), th.ID)
	if err != nil {
		t.Fatal(err)
	}
	// The plan is proposed inside the activated turn, which stays running.
	turn, endTurn := liveAdmittedTurn(t, e, protocol.TurnStartParams{ThreadID: th.ID, Text: "implement", DelegationActivationToken: activation.ActivationToken})
	task, err := e.Store.AgentTask(t.Context(), p.last())
	if err != nil {
		t.Fatal(err)
	}
	lead := tools.WithScope(withDelegationTask(context.WithValue(t.Context(), executionTurnKey{}, turn), task), &tools.Scope{ThreadID: th.ID, ProjectID: th.ProjectID, Root: root})
	return planEnv{e: e, th: th, root: root, turn: turn, task: task, lead: lead, seams: seams, endTurn: endTurn}
}

func (env planEnv) tool(t *testing.T) tools.Tool {
	t.Helper()
	tool, ok := env.e.Tools.Get("agent.propose_plan")
	if !ok {
		t.Fatal("agent.propose_plan not registered")
	}
	return tool
}

type proposeResult struct {
	out string
	err error
}

// proposeAsync calls agent.propose_plan in the background (it blocks on the
// user's decision).
func (env planEnv) proposeAsync(t *testing.T, ctx context.Context, args string) <-chan proposeResult {
	t.Helper()
	tool := env.tool(t)
	ch := make(chan proposeResult, 1)
	go func() {
		out, err := tool.Call(ctx, json.RawMessage(args))
		ch <- proposeResult{out, err}
	}()
	return ch
}

// pendingPlanApproval waits for a pending agent_plan approval other than the
// ones listed in skip.
func (env planEnv) pendingPlanApproval(t *testing.T, skip ...string) protocol.Approval {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		list, err := env.e.Store.ListApprovals(t.Context(), "pending")
		if err != nil {
			t.Fatal(err)
		}
		for _, a := range list {
			if a.Kind == "agent_plan" && !slices.Contains(skip, a.ID) {
				env.e.mu.Lock()
				_, waiting := env.e.approvals[a.ID]
				env.e.mu.Unlock()
				if waiting {
					return a
				}
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("no pending agent_plan approval")
	return protocol.Approval{}
}

func planCardOf(t *testing.T, a protocol.Approval) protocol.PlanCard {
	t.Helper()
	if a.Kind != "agent_plan" || a.Tool != "agent.propose_plan" {
		t.Fatalf("approval kind %q tool %q", a.Kind, a.Tool)
	}
	var card protocol.PlanCard
	if err := json.Unmarshal(a.Args, &card); err != nil || card.PlanID == "" {
		t.Fatalf("plan card %s: %v", a.Args, err)
	}
	return card
}

func (env planEnv) planCount(t *testing.T) int {
	t.Helper()
	var n int
	if err := env.e.Store.DB.QueryRow(`SELECT count(*) FROM agent_plans WHERE thread_id=?`, env.th.ID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func (env planEnv) setPool(t *testing.T, scope store.AgentModelScope) {
	t.Helper()
	data, err := json.Marshal(scope)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = env.e.Store.DB.Exec(`UPDATE agent_model_scopes SET scope_json=? WHERE task_id=?`, string(data), env.task.ID); err != nil {
		t.Fatal(err)
	}
}

func recv(t *testing.T, ch <-chan proposeResult) proposeResult {
	t.Helper()
	select {
	case r := <-ch:
		return r
	case <-time.After(10 * time.Second):
		t.Fatal("agent.propose_plan did not return")
	}
	return proposeResult{}
}

func cardAgent(t *testing.T, card protocol.PlanCard, name string) protocol.PlanCardAgent {
	t.Helper()
	for _, a := range card.Agents {
		if a.Name == name {
			return a
		}
	}
	t.Fatalf("card has no agent %q: %+v", name, card.Agents)
	return protocol.PlanCardAgent{}
}

func hasNotice(card protocol.PlanCard, parts ...string) bool {
	for _, n := range card.Notices {
		ok := true
		for _, p := range parts {
			ok = ok && strings.Contains(n, p)
		}
		if ok {
			return true
		}
	}
	return false
}

const onePlan = `{"agents":[{"name":"worker","purpose":"implement the parser","summary":"writes code","capabilities":{"files":"read-write","shell":"none","network":false,"spawnMax":0}}]}`

func TestProposePlanLeadOnly(t *testing.T) {
	env := planFixture(t)
	full := protocol.AgentCapabilities{Files: protocol.FilesReadWrite, Shell: protocol.ShellReadWrite, Network: true, SpawnMax: 8}
	tool := env.tool(t)
	for name, ctx := range map[string]context.Context{
		"runtime agent": withRuntimeAgent(withChildExecution(WithDelegationChild(env.lead), &childExecution{runtime: &runtimeExecution{agent: protocol.RuntimeAgent{ID: "a1", Capabilities: full}}}), "a1"),
		"legacy child":  withChildExecution(WithDelegationChild(env.lead), &childExecution{}),
		"child marker":  WithDelegationChild(env.lead),
	} {
		_, err := tool.Call(ctx, json.RawMessage(onePlan))
		requireRefused(t, err, "lead-only")
		_, err = env.e.proposeAgentPlan(ctx, tools.PlanProposal{Agents: []protocol.PlanAgent{{Name: "worker", Purpose: "p", Capabilities: protocol.AgentCapabilities{Files: protocol.FilesRead, Shell: protocol.ShellNone}}}})
		requireRefused(t, err, "lead-only")
		call := llm.ToolCall{ID: "c1", Name: tools.ToWire("agent.propose_plan"), Args: json.RawMessage(onePlan)}
		if r := env.e.runTool(ctx, t.Context(), env.th, env.turn, call, nil); !r.IsError {
			t.Fatalf("%s: runTool accepted agent.propose_plan: %s", name, r.Output)
		}
	}
	if slices.Contains(agentrt.AllowedTools(full), "agent.propose_plan") {
		t.Fatal("capabilities grant agent.propose_plan")
	}
	// Writable tools are required (read-only or plan mode refuses).
	ro := tools.WithScope(env.lead, &tools.Scope{ThreadID: env.th.ID, ProjectID: env.th.ProjectID, Root: env.root, ReadOnly: true})
	_, err := tool.Call(ro, json.RawMessage(onePlan))
	requireRefused(t, err, "writable")
	// A turn that is not admitted for delegation is refused.
	plain := tools.WithScope(context.WithValue(t.Context(), executionTurnKey{}, env.turn), &tools.Scope{ThreadID: env.th.ID, ProjectID: env.th.ProjectID, Root: env.root})
	_, err = tool.Call(plain, json.RawMessage(onePlan))
	requireRefused(t, err, "approves an agent plan")
	if p := env.e.permittedToolFor(plain, turnTool{tool: tool}, nil); p {
		t.Fatal("agent.propose_plan offered without admission")
	}
	if !env.e.permittedToolFor(env.lead, turnTool{tool: tool}, nil) {
		t.Fatal("agent.propose_plan not offered in the admitted lead turn")
	}
	if n := env.planCount(t); n != 0 {
		t.Fatalf("refused proposals stored %d plans", n)
	}
	// A task that is no longer active is refused.
	if err = env.e.Store.UpdateAgentTaskState(t.Context(), env.task.ID, env.task.ThreadID, env.task.ProjectID, protocol.AgentStateRunning, protocol.AgentStateCancelled); err != nil {
		t.Fatal(err)
	}
	_, err = tool.Call(env.lead, json.RawMessage(onePlan))
	if err == nil || !strings.Contains(err.Error(), "no longer active") {
		t.Fatalf("proposal for a cancelled task: want refusal naming the rule, got %v", err)
	}
	if n := env.planCount(t); n != 0 {
		t.Fatalf("inactive task stored %d plans", n)
	}
}

func TestProposePlanValidation(t *testing.T) {
	env := planFixture(t)
	tool := env.tool(t)
	agent := func(name string) string {
		return fmt.Sprintf(`{"name":%q,"purpose":"p","summary":"s","capabilities":{"files":"read","shell":"none","network":false,"spawnMax":0}}`, name)
	}
	many := make([]string, 13)
	for i := range many {
		many[i] = agent(fmt.Sprintf("a%d", i))
	}
	limits := func(l string) string { return `{"agents":[` + agent("w") + `],"limits":` + l + `}` }
	for name, tc := range map[string]struct{ args, want string }{
		"no agents":          {`{"agents":[]}`, "1 to 12"},
		"missing agents":     {`{}`, "1 to 12"},
		"thirteen agents":    {`{"agents":[` + strings.Join(many, ",") + `]}`, "1 to 12"},
		"empty name":         {`{"agents":[` + agent("  ") + `]}`, "name"},
		"long name":          {`{"agents":[` + agent(strings.Repeat("n", 41)) + `]}`, "40"},
		"duplicate name":     {`{"agents":[` + agent("Worker") + `,` + agent("worker") + `]}`, "unique"},
		"unknown field":      {`{"agents":[` + agent("w") + `],"roles":["x"]}`, "unknown field"},
		"unknown agent key":  {`{"agents":[{"name":"w","purpose":"p","role":"reviewer","capabilities":{"files":"read","shell":"none","network":false,"spawnMax":0}}]}`, "unknown field"},
		"missing caps":       {`{"agents":[{"name":"w","purpose":"p"}]}`, "capabilities"},
		"partial caps":       {`{"agents":[{"name":"w","purpose":"p","capabilities":{"files":"read"}}]}`, "capabilities.shell"},
		"negative spawn":     {`{"agents":[{"name":"w","purpose":"p","capabilities":{"files":"read","shell":"none","network":false,"spawnMax":-1}}]}`, "spawnMax"},
		"no purpose":         {`{"agents":[{"name":"w","capabilities":{"files":"read","shell":"none","network":false,"spawnMax":0}}]}`, "purpose"},
		"maxConcurrent":      {limits(`{"maxConcurrent":9}`), "maxConcurrent"},
		"maxDepth":           {limits(`{"maxDepth":4}`), "maxDepth"},
		"maxTotal":           {limits(`{"maxTotal":41}`), "maxTotal"},
		"sliceShare high":    {limits(`{"sliceShare":0.6}`), "sliceShare"},
		"sliceShare low":     {limits(`{"sliceShare":-0.1}`), "sliceShare"},
		"negative total":     {limits(`{"maxTotal":-1}`), "maxTotal"},
		"unknown limits key": {limits(`{"maxChildren":2}`), "unknown field"},
	} {
		_, err := tool.Call(env.lead, json.RawMessage(tc.args))
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s: want refusal naming %q, got %v", name, tc.want, err)
		}
	}
	// network in limits needs the project's effective network.
	off := false
	if _, err := env.e.Store.UpdateProject(t.Context(), protocol.ProjectUpdateParams{ProjectID: env.th.ProjectID, Tools: &protocol.ProjectTools{Network: &off}}); err != nil {
		t.Fatal(err)
	}
	_, err := tool.Call(env.lead, json.RawMessage(limits(`{"network":true}`)))
	requireRefused(t, err, "network")
	if n := env.planCount(t); n != 0 {
		t.Fatalf("refused proposals stored %d plans", n)
	}

	// An unknown capability string never widens: it is granted as none and
	// reported, and the stored plan keeps the clamped grant.
	ch := env.proposeAsync(t, env.lead, `{"agents":[{"name":"odd","purpose":"p","summary":"s","capabilities":{"files":"admin","shell":"root","network":false,"spawnMax":0}}],"limits":{"maxConcurrent":2}}`)
	a := env.pendingPlanApproval(t)
	card := planCardOf(t, a)
	odd := cardAgent(t, card, "odd")
	if odd.Granted.Files != protocol.FilesNone || odd.Granted.Shell != protocol.ShellNone {
		t.Fatalf("unknown capability widened: %+v", odd.Granted)
	}
	if len(odd.Rejected) < 2 || !hasNotice(card, "odd", "unknown value") {
		t.Fatalf("unknown values not reported: %+v notices %v", odd.Rejected, card.Notices)
	}
	if card.Limits.MaxConcurrent != 2 || card.Limits.MaxDepth != 3 || card.Limits.MaxTotal != 40 || card.Limits.SliceShare != 0.25 {
		t.Fatalf("limits not normalised: %+v", card.Limits)
	}
	stored, err := env.e.Store.AgentPlan(t.Context(), card.PlanID)
	if err != nil || stored.State != protocol.PlanProposed || stored.TaskID != env.task.ID || stored.Agents[0].Capabilities.Files != protocol.FilesNone || stored.Agents[0].Capabilities.Shell != protocol.ShellNone {
		t.Fatalf("stored plan %+v %v", stored, err)
	}
	if _, err = env.e.RespondApproval(t.Context(), a.ID, false, false, "test"); err != nil {
		t.Fatal(err)
	}
	if r := recv(t, ch); r.err != nil {
		t.Fatal(r.err)
	}
}

func TestProposePlanClampsAndNotices(t *testing.T) {
	t.Run("no sandbox", func(t *testing.T) {
		env := planFixture(t)
		(&shellSeams{}).install(env.e, nil, nil)
		ch := env.proposeAsync(t, env.lead, `{"agents":[`+
			`{"name":"writer","purpose":"implement","summary":"s","capabilities":{"files":"read-write","shell":"read-write","network":false,"spawnMax":0}},`+
			`{"name":"reader","purpose":"review","summary":"s","capabilities":{"files":"read","shell":"none","network":false,"spawnMax":0}}]}`)
		a := env.pendingPlanApproval(t)
		card := planCardOf(t, a)
		w := cardAgent(t, card, "writer")
		if w.Granted.Shell != protocol.ShellNone || w.Granted.Files != protocol.FilesReadWrite {
			t.Fatalf("writer granted %+v", w.Granted)
		}
		if !hasNotice(card, "writer: shell was dropped because no sandbox (macOS seatbelt / Linux bubblewrap) is available on this machine") {
			t.Fatalf("notices %q", card.Notices)
		}
		if r := cardAgent(t, card, "reader"); len(r.Rejected) != 0 {
			t.Fatalf("reader rejected %+v", r.Rejected)
		}
		if len(card.Notices) != 1 {
			t.Fatalf("notices = %q, want only the shell notice", card.Notices)
		}
		stored, err := env.e.Store.AgentPlan(t.Context(), card.PlanID)
		if err != nil || stored.Agents[0].Capabilities.Shell != protocol.ShellNone {
			t.Fatalf("stored plan grants more than the card shows: %+v %v", stored.Agents, err)
		}
		if _, err = env.e.RespondApproval(t.Context(), a.ID, false, false, "test"); err != nil {
			t.Fatal(err)
		}
		recv(t, ch)
	})
	t.Run("confined shell rules", func(t *testing.T) {
		env := planFixture(t)
		seams := &shellSeams{osErr: errors.New("read-only probe failed")}
		env.e.shellChecks.reset()
		seams.install(env.e, fakeSandbox{}, nil)
		ch := env.proposeAsync(t, env.lead, `{"agents":[`+
			`{"name":"scout","purpose":"look","summary":"s","capabilities":{"files":"read","shell":"read-only","network":false,"spawnMax":0}},`+
			`{"name":"half","purpose":"build","summary":"s","capabilities":{"files":"read","shell":"read-write","network":false,"spawnMax":0}},`+
			`{"name":"blind","purpose":"run","summary":"s","capabilities":{"files":"none","shell":"read-write","network":false,"spawnMax":0}},`+
			`{"name":"net","purpose":"fetch","summary":"s","capabilities":{"files":"read-write","shell":"read-write","network":true,"spawnMax":0}}]}`)
		a := env.pendingPlanApproval(t)
		card := planCardOf(t, a)
		if g := cardAgent(t, card, "scout").Granted; g.Shell != protocol.ShellNone {
			t.Fatalf("scout granted %+v", g)
		}
		if g := cardAgent(t, card, "half").Granted; g.Shell != protocol.ShellNone {
			t.Fatalf("half granted %+v (read-write needs files read-write; read-only unsupported)", g)
		}
		if g := cardAgent(t, card, "blind").Granted; g.Shell != protocol.ShellNone {
			t.Fatalf("blind granted %+v", g)
		}
		if g := cardAgent(t, card, "net").Granted; g.Shell != protocol.ShellReadWrite || g.Network {
			t.Fatalf("net granted %+v", g)
		}
		if !hasNotice(card, "scout", "read-only shell", "read-only probe failed") {
			t.Fatalf("scout notice missing: %q", card.Notices)
		}
		if !hasNotice(card, "half", "read-write shell requires files") || !hasNotice(card, "blind", "shell not granted") {
			t.Fatalf("half/blind notices missing: %q", card.Notices)
		}
		if !hasNotice(card, "net", "network") {
			t.Fatalf("network notice missing: %q", card.Notices)
		}
		// Every rejection is a notice.
		total := 0
		for _, ag := range card.Agents {
			total += len(ag.Rejected)
			for _, r := range ag.Rejected {
				if !hasNotice(card, ag.Name, r.Reason) {
					t.Fatalf("rejection %+v of %s has no notice: %q", r, ag.Name, card.Notices)
				}
			}
		}
		if len(card.Notices) < total {
			t.Fatalf("%d notices for %d rejections", len(card.Notices), total)
		}
		if _, err := env.e.RespondApproval(t.Context(), a.ID, false, false, "test"); err != nil {
			t.Fatal(err)
		}
		recv(t, ch)
	})
}

func TestProposePlanClampsWithReadOnlyShell(t *testing.T) {
	env := planFixture(t)
	ch := env.proposeAsync(t, env.lead, `{"agents":[`+
		`{"name":"half","purpose":"build","summary":"s","capabilities":{"files":"read","shell":"read-write","network":false,"spawnMax":0}},`+
		`{"name":"blind","purpose":"run","summary":"s","capabilities":{"files":"none","shell":"read-only","network":false,"spawnMax":0}}]}`)
	a := env.pendingPlanApproval(t)
	card := planCardOf(t, a)
	if g := cardAgent(t, card, "half").Granted; g.Shell != protocol.ShellReadOnly || g.Files != protocol.FilesRead {
		t.Fatalf("half granted %+v, want a read-only shell", g)
	}
	if g := cardAgent(t, card, "blind").Granted; g.Shell != protocol.ShellNone {
		t.Fatalf("blind granted %+v", g)
	}
	if !hasNotice(card, "half", "read-write shell requires files") || !hasNotice(card, "blind", "files \"none\" allows no shell") {
		t.Fatalf("notices %q", card.Notices)
	}
	if _, err := env.e.RespondApproval(t.Context(), a.ID, false, false, "test"); err != nil {
		t.Fatal(err)
	}
	recv(t, ch)
}

func TestProposePlanModelsFromPoolOnly(t *testing.T) {
	env := planFixture(t)
	env.setPool(t, store.AgentModelScope{Provider: "claude", Model: "deterministic", Candidates: []protocol.ConfiguredModel{
		{ID: "cfg-a", Provider: "claude", Model: "deterministic", Enabled: true},
		{ID: "cfg-b", Provider: "claude", Model: "second", Enabled: true},
		{ID: "cfg-h", Provider: "claude", Model: "hidden", Enabled: false},
		{ID: "cfg-o", Provider: "openai", Model: "gpt-x", Enabled: true},
	}})
	entry := func(name, model string) string {
		return fmt.Sprintf(`{"name":%q,"purpose":"p","summary":"s","capabilities":{"files":"read","shell":"none","network":false,"spawnMax":0},"model":%q}`, name, model)
	}
	for _, m := range []string{"claude/hidden", "nope"} {
		_, err := env.tool(t).Call(env.lead, json.RawMessage(`{"agents":[`+entry("w", m)+`]}`))
		if err == nil || !strings.Contains(err.Error(), m) || !strings.Contains(err.Error(), "pool") {
			t.Fatalf("model %s: want refusal naming it and the pool, got %v", m, err)
		}
	}
	if n := env.planCount(t); n != 0 {
		t.Fatalf("refused proposals stored %d plans", n)
	}
	ch := env.proposeAsync(t, env.lead, `{"agents":[`+entry("a", "cfg-b")+`,`+entry("b", "openai/gpt-x")+`]}`)
	ap := env.pendingPlanApproval(t)
	card := planCardOf(t, ap)
	var names []string
	for _, m := range card.Models {
		names = append(names, m.Name)
	}
	if !slices.Equal(names, []string{"claude/deterministic", "claude/second", "openai/gpt-x"}) {
		t.Fatalf("offered models = %v", names)
	}
	for _, m := range card.Models {
		if m.Name == "openai/gpt-x" && (m.Available || m.Why == "") {
			t.Fatalf("gpt-x shown available: %+v", m)
		}
		if m.Name == "claude/second" && (!m.Available || !m.ToolsOK) {
			t.Fatalf("second shown unavailable: %+v", m)
		}
	}
	if cardAgent(t, card, "a").Model != "claude/second" || cardAgent(t, card, "b").Model != "openai/gpt-x" {
		t.Fatalf("models not canonical: %+v", card.Agents)
	}
	if !hasNotice(card, "b", "openai/gpt-x", "unavailable") {
		t.Fatalf("unavailable model notice missing: %q", card.Notices)
	}
	if _, err := env.e.RespondApproval(t.Context(), ap.ID, false, false, "test"); err != nil {
		t.Fatal(err)
	}
	recv(t, ch)

	// A pool model that cannot use tools is shown and flagged for an entry
	// that needs tools.
	plan := protocol.AgentPlan{ID: "p", Limits: protocol.DefaultPlanLimits(), Agents: []protocol.PlanAgent{
		{Name: "coder", Purpose: "p", Capabilities: protocol.AgentCapabilities{Files: protocol.FilesReadWrite, Shell: protocol.ShellNone}, Model: "claude/notools"},
		{Name: "talker", Purpose: "p", Capabilities: protocol.AgentCapabilities{Files: protocol.FilesNone, Shell: protocol.ShellNone}, Model: "claude/notools"},
	}}
	pool := []modelRoute{{Provider: "claude", Model: "notools", Tools: false}}
	c := planCardFor(plan, pool, agentrt.RootCapabilities(), agentrt.ResolveOptions{ShellReadOnlySupported: true}, nil)
	if len(c.Models) != 1 || c.Models[0].ToolsOK {
		t.Fatalf("models %+v", c.Models)
	}
	if !hasNotice(c, "coder", "claude/notools", "cannot use tools") || hasNotice(c, "talker") {
		t.Fatalf("incompatibility notices %q", c.Notices)
	}
}

func TestProposePlanPrefillsFromPreviousAssignments(t *testing.T) {
	env := planFixture(t)
	env.setPool(t, store.AgentModelScope{Provider: "claude", Model: "deterministic", Candidates: []protocol.ConfiguredModel{
		{Provider: "claude", Model: "deterministic", Enabled: true},
		{Provider: "claude", Model: "second", Enabled: true},
	}})
	prev := protocol.AgentPlan{ID: "plan-prev", ThreadID: env.th.ID, Limits: protocol.DefaultPlanLimits(), Agents: []protocol.PlanAgent{
		{Name: "worker", Purpose: "p", Capabilities: protocol.AgentCapabilities{Files: protocol.FilesRead, Shell: protocol.ShellNone}, Model: "claude/second"},
	}}
	if err := env.e.Store.SaveAgentPlan(t.Context(), prev); err != nil {
		t.Fatal(err)
	}
	if err := env.e.Store.ApproveAgentPlan(t.Context(), prev.ID, env.task.ID); err != nil {
		t.Fatal(err)
	}
	ch := env.proposeAsync(t, env.lead, `{"agents":[`+
		`{"name":"Worker","purpose":"p","summary":"s","capabilities":{"files":"read","shell":"none","network":false,"spawnMax":0}},`+
		`{"name":"fresh","purpose":"p","summary":"s","capabilities":{"files":"read","shell":"none","network":false,"spawnMax":0}}]}`)
	ap := env.pendingPlanApproval(t)
	card := planCardOf(t, ap)
	if m := cardAgent(t, card, "Worker").Model; m != "claude/second" {
		t.Fatalf("Worker model = %q, want the previous assignment", m)
	}
	if m := cardAgent(t, card, "fresh").Model; m != "claude/deterministic" {
		t.Fatalf("fresh model = %q, want the thread's model", m)
	}
	if !slices.Equal(card.Previous, []protocol.PreviousAssignment{{PlanID: "plan-prev", Name: "worker", Model: "claude/second"}}) {
		t.Fatalf("previous = %+v", card.Previous)
	}
	stored, err := env.e.Store.AgentPlan(t.Context(), card.PlanID)
	if err != nil || stored.Agents[0].Model != "claude/second" || stored.Agents[1].Model != "claude/deterministic" {
		t.Fatalf("stored %+v %v", stored.Agents, err)
	}
	if _, err = env.e.RespondApproval(t.Context(), ap.ID, false, false, "test"); err != nil {
		t.Fatal(err)
	}
	recv(t, ch)
}

func TestProposePlanSupersedesEarlierProposal(t *testing.T) {
	t.Run("pending card", func(t *testing.T) {
		env := planFixture(t)
		first := env.proposeAsync(t, env.lead, onePlan)
		a1 := env.pendingPlanApproval(t)
		second := env.proposeAsync(t, env.lead, onePlan)
		a2 := env.pendingPlanApproval(t, a1.ID)
		r1 := recv(t, first)
		if r1.err == nil || !strings.Contains(r1.err.Error(), "superseded") {
			t.Fatalf("first proposal = %q %v, want superseded", r1.out, r1.err)
		}
		got, err := env.e.Store.GetApproval(t.Context(), a1.ID)
		if err != nil || got.Status != "expired" {
			t.Fatalf("first approval %+v %v", got, err)
		}
		if _, err = env.e.RespondApproval(t.Context(), a1.ID, true, false, "test"); err == nil {
			t.Fatal("stale card approved")
		}
		p1, _ := env.e.Store.AgentPlan(t.Context(), planCardOf(t, a1).PlanID)
		if p1.State != protocol.PlanSuperseded {
			t.Fatalf("first plan state %q", p1.State)
		}
		if _, err = env.e.RespondApproval(t.Context(), a2.ID, true, false, "test"); err != nil {
			t.Fatal(err)
		}
		if r := recv(t, second); r.err != nil || !strings.Contains(r.out, `"approved"`) {
			t.Fatalf("second = %q %v", r.out, r.err)
		}
		var n int
		if err = env.e.Store.DB.QueryRow(`SELECT count(*) FROM agent_plans WHERE task_id=? AND state='approved'`, env.task.ID).Scan(&n); err != nil || n != 1 {
			t.Fatalf("approved plans = %d %v", n, err)
		}
		if plan, err := env.e.Store.ApprovedAgentPlanForTask(t.Context(), env.task.ID); err != nil || plan.ID != planCardOf(t, a2).PlanID {
			t.Fatalf("approved plan %+v %v", plan, err)
		}
	})
	t.Run("answered card", func(t *testing.T) {
		env := planFixture(t)
		first := env.proposeAsync(t, env.lead, onePlan)
		a1 := env.pendingPlanApproval(t)
		if _, err := env.e.decideAgentPlan(t.Context(), planCardOf(t, a1).PlanID, planRevise, "use two agents", "test"); err != nil {
			t.Fatal(err)
		}
		recv(t, first)
		second := env.proposeAsync(t, env.lead, onePlan)
		a2 := env.pendingPlanApproval(t, a1.ID)
		p1, _ := env.e.Store.AgentPlan(t.Context(), planCardOf(t, a1).PlanID)
		if p1.State != protocol.PlanSuperseded {
			t.Fatalf("revised plan state %q after a new proposal", p1.State)
		}
		if _, err := env.e.RespondApproval(t.Context(), a2.ID, false, false, "test"); err != nil {
			t.Fatal(err)
		}
		recv(t, second)
	})
}

func TestProposePlanBlocksUntilDecision(t *testing.T) {
	t.Run("turn", func(t *testing.T) {
		env := planFixture(t)
		p := &runtimeScriptProvider{}
		env.e.LLMs.Register(p)
		p.script = func(n int, ctx context.Context, req llm.Request) []llm.Event {
			if n == 1 {
				return toolReply("pp1", "agent.propose_plan", onePlan)
			}
			return textReply("planned")
		}
		// This subtest runs its own activated turn: end the fixture's (its
		// empty task closes with it).
		env.endTurn()
		activation, err := env.e.PrepareDelegation(t.Context(), env.th.ID)
		if err != nil {
			// The fixture's task is still active: finish it first.
			if err = env.e.Store.UpdateAgentTaskState(t.Context(), env.task.ID, env.task.ThreadID, env.task.ProjectID, protocol.AgentStateRunning, protocol.AgentStateCompleted); err != nil {
				t.Fatal(err)
			}
			if activation, err = env.e.PrepareDelegation(t.Context(), env.th.ID); err != nil {
				t.Fatal(err)
			}
		}
		wait := make(chan protocol.Turn, 1)
		if _, err = env.e.startTurn(t.Context(), turnRequest{TurnStartParams: protocol.TurnStartParams{ThreadID: env.th.ID, Text: "build it with agents", DelegationActivationToken: activation.ActivationToken}, wait: wait}); err != nil {
			t.Fatal(err)
		}
		a := env.pendingPlanApproval(t)
		select {
		case done := <-wait:
			t.Fatalf("turn finished while the plan card was pending: %+v", done)
		case <-time.After(200 * time.Millisecond):
		}
		card := planCardOf(t, a)
		if st, _ := env.e.Store.AgentPlan(t.Context(), card.PlanID); st.State != protocol.PlanProposed {
			t.Fatalf("plan state while pending = %q", st.State)
		}
		if _, err = env.e.RespondApproval(t.Context(), a.ID, true, false, "test"); err != nil {
			t.Fatal(err)
		}
		select {
		case done := <-wait:
			if done.Status != protocol.TurnCompleted {
				t.Fatalf("turn %+v", done)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("turn did not finish after the decision")
		}
		reqs, _ := p.snapshot()
		var result string
		for _, m := range reqs[len(reqs)-1].Messages {
			if m.Role == llm.RoleTool && m.ToolCallID == "pp1" {
				result = m.Result
			}
		}
		var out struct {
			PlanID string `json:"planId"`
			State  string `json:"state"`
			Agents []struct {
				Name     string                         `json:"name"`
				Granted  protocol.AgentCapabilities     `json:"granted"`
				Rejected []protocol.CapabilityRejection `json:"rejected"`
				Model    string                         `json:"model"`
			} `json:"agents"`
		}
		if err = json.Unmarshal([]byte(result), &out); err != nil || out.State != "approved" || out.PlanID != card.PlanID || len(out.Agents) != 1 || out.Agents[0].Name != "worker" || out.Agents[0].Granted.Files != protocol.FilesReadWrite || out.Agents[0].Model == "" {
			t.Fatalf("tool result %q: %v", result, err)
		}
		plan, err := env.e.Store.AgentPlan(t.Context(), card.PlanID)
		if err != nil || plan.State != protocol.PlanApproved || plan.TaskID == "" {
			t.Fatalf("plan %+v %v", plan, err)
		}
	})
	t.Run("declined", func(t *testing.T) {
		env := planFixture(t)
		ch := env.proposeAsync(t, env.lead, onePlan)
		a := env.pendingPlanApproval(t)
		if _, err := env.e.RespondApproval(t.Context(), a.ID, false, false, "test"); err != nil {
			t.Fatal(err)
		}
		r := recv(t, ch)
		if r.err != nil || r.out != `{"state":"declined","message":"The user declined agents. Continue without them."}` {
			t.Fatalf("declined = %q %v", r.out, r.err)
		}
		if _, err := env.e.Store.ApprovedAgentPlanForTask(t.Context(), env.task.ID); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("declined plan approved: %v", err)
		}
	})
	t.Run("revise", func(t *testing.T) {
		env := planFixture(t)
		ch := env.proposeAsync(t, env.lead, onePlan)
		a := env.pendingPlanApproval(t)
		if _, err := env.e.decideAgentPlan(t.Context(), planCardOf(t, a).PlanID, planRevise, "use qwen for the worker", "test"); err != nil {
			t.Fatal(err)
		}
		r := recv(t, ch)
		if r.err != nil || r.out != `{"state":"revise","feedback":"use qwen for the worker"}` {
			t.Fatalf("revise = %q %v", r.out, r.err)
		}
		if got, _ := env.e.Store.GetApproval(t.Context(), a.ID); got.Status != "denied" {
			t.Fatalf("approval status %q", got.Status)
		}
	})
	t.Run("interrupted", func(t *testing.T) {
		env := planFixture(t)
		ctx, cancel := context.WithCancel(env.lead)
		ch := env.proposeAsync(t, ctx, onePlan)
		a := env.pendingPlanApproval(t)
		cancel()
		if r := recv(t, ch); r.err == nil {
			t.Fatalf("interrupted proposal = %q", r.out)
		}
		if got, _ := env.e.Store.GetApproval(t.Context(), a.ID); got.Status != "expired" {
			t.Fatalf("approval status %q", got.Status)
		}
		if p, _ := env.e.Store.AgentPlan(t.Context(), planCardOf(t, a).PlanID); p.State != protocol.PlanProposed {
			t.Fatalf("plan state %q", p.State)
		}
	})
}

func TestSpawnStillRefusedUntilApproved(t *testing.T) {
	env := planFixture(t)
	spawn := func(id string) toolRunResult {
		t.Helper()
		call := llm.ToolCall{ID: id, Name: tools.ToWire("agent.spawn"), Args: json.RawMessage(`{"planEntry":"worker","assignment":{"prompt":"look around"}}`)}
		return env.e.runTool(env.lead, t.Context(), env.th, env.turn, call, nil)
	}
	ch := env.proposeAsync(t, env.lead, `{"agents":[{"name":"worker","purpose":"look","summary":"s","capabilities":{"files":"read","shell":"none","network":false,"spawnMax":0}}]}`)
	a := env.pendingPlanApproval(t)
	if r := spawn("s1"); !r.IsError || !strings.Contains(r.Output, "plan") {
		t.Fatalf("spawn with a proposed plan = %+v", r)
	}
	if _, err := env.e.RespondApproval(t.Context(), a.ID, true, false, "test"); err != nil {
		t.Fatal(err)
	}
	if r := recv(t, ch); r.err != nil {
		t.Fatal(r.err)
	}
	if r := spawn("s2"); r.IsError {
		t.Fatalf("spawn after approval refused: %s", r.Output)
	}
}
