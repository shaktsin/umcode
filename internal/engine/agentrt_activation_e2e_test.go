package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shaktsin/umcode/internal/config"
	"github.com/shaktsin/umcode/internal/llm"
	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/store"
	"github.com/shaktsin/umcode/internal/tools"
)

// Plan 4 Task 7: the activation flow end to end. Only the LLM provider and
// the verification tools are scripted; turns, tool gates, approvals, the
// suggestion and plan RPCs, the agent runtime, real Git workspaces and the
// integration run as in production.

const (
	e2eA       = "package a\n\nfunc A() int { return 0 }\n"
	e2eB       = "package b\n\nfunc B() int { return 0 }\n"
	e2eFromA   = "package a\n\nfunc A() int { return 1 }\n"
	e2eFromB   = "package b\n\nfunc B() int { return 2 }\n"
	e2eSuggest = "this needs several independent pieces"
	e2eBuild   = "build it with agents"
)

// e2eLeadStep is one scripted tool call of the lead in a user turn.
type e2eLeadStep struct{ tool, args string }

// e2eLeadCall records a tool call the scripted lead made.
type e2eLeadCall struct{ id, tool string }

type activationE2E struct {
	e       *Engine
	th      protocol.Thread
	root    string
	base    string // the committed base revision
	p       *runtimeScriptProvider
	planner *stubVerificationTool
	runner  *stubVerificationTool
	events  *planEvents

	mu        sync.Mutex
	leadSteps map[string][]e2eLeadStep // keyed by a phrase of the user message
	leadCalls []e2eLeadCall
	agentRuns map[string]int // model calls per plan entry
	agentRun  map[string][]fakeStep

	task protocol.AgentTask
	lead context.Context // the lead's admitted tool context once a task exists
}

// newActivationE2E builds an engine over a Git project
// holding a.go and b.go, a two-model pool (claude/deterministic and
// claude/second), stubbed verification tools and the scripted provider.
func newActivationE2E(t *testing.T, configure func(*config.Config)) *activationE2E {
	t.Helper()
	e, th, root, _ := delegationFixtureWith(t, configure)
	for name, data := range map[string]string{"a.go": e2eA, "b.go": e2eB} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	delegationGit(t, root, "add", ".")
	delegationGit(t, root, "commit", "-m", "a.go and b.go")
	if _, err := e.SetRoutingConfig(t.Context(), protocol.RoutingConfig{
		Models: []protocol.ConfiguredModel{
			{ID: "e2e-det", Name: "Deterministic", Provider: "claude", Model: "deterministic", Enabled: true},
			{ID: "e2e-second", Name: "Second", Provider: "claude", Model: "second", Enabled: true},
		},
		Pools: []protocol.ModelPool{{ID: "e2e-pool", Name: "E2E pool", Models: []string{"e2e-det", "e2e-second"}, Strategy: protocol.PoolPriority, Enabled: true}},
	}); err != nil {
		t.Fatal(err)
	}
	x := &activationE2E{e: e, th: th, root: root, base: delegationGit(t, root, "rev-parse", "HEAD"),
		p:         &runtimeScriptProvider{},
		planner:   &stubVerificationTool{name: "verification.plan", out: rvPlanTwo},
		runner:    &stubVerificationTool{name: "verification.run"},
		events:    &planEvents{thread: th.ID},
		leadSteps: map[string][]e2eLeadStep{}, agentRuns: map[string]int{}, agentRun: map[string][]fakeStep{}}
	x.p.script = x.script
	e.LLMs.Register(x.p)
	e.Tools.Add(x.planner)
	e.Tools.Add(x.runner)
	e.Bus.Add(x.events)
	t.Cleanup(func() { e.Bus.Remove(x.events.ID()) })
	return x
}

// script answers every model call: runtime agents follow their plan entry's
// steps, the lead follows the steps registered for its user message (one
// step per tool result received so far in the turn).
func (x *activationE2E) script(_ int, ctx context.Context, req llm.Request) []llm.Event {
	if id, ok := runtimeAgentFrom(ctx); ok && id != "" {
		return x.agentReply(ctx, id)
	}
	last := -1
	for i, m := range req.Messages {
		if m.Role == llm.RoleUser {
			last = i
		}
	}
	if last < 0 {
		return textReply("ok")
	}
	user := req.Messages[last].JoinedText()
	done := 0
	for _, m := range req.Messages[last+1:] {
		if m.Role == llm.RoleTool {
			done++
		}
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	for key, steps := range x.leadSteps {
		if !strings.Contains(user, key) {
			continue
		}
		if done >= len(steps) {
			return textReply("lead finished: " + key)
		}
		id := fmt.Sprintf("lead-%d", len(x.leadCalls))
		x.leadCalls = append(x.leadCalls, e2eLeadCall{id: id, tool: steps[done].tool})
		return toolReply(id, steps[done].tool, steps[done].args)
	}
	return textReply("ok")
}

func (x *activationE2E) agentReply(ctx context.Context, id string) []llm.Event {
	taskID := x.task.ID
	if taskID == "" {
		if task, err := x.e.Store.ActiveAgentTask(context.WithoutCancel(ctx), x.th.ID, x.th.ProjectID); err == nil {
			taskID = task.ID
		}
	}
	a, err := x.e.Store.RuntimeAgent(context.WithoutCancel(ctx), taskID, id)
	if err != nil {
		return []llm.Event{{Type: llm.EventError, Err: err}}
	}
	x.mu.Lock()
	idx := x.agentRuns[a.PlanEntry]
	x.agentRuns[a.PlanEntry]++
	steps := x.agentRun[a.PlanEntry]
	x.mu.Unlock()
	if idx >= len(steps) {
		return textReply(a.PlanEntry + " done")
	}
	if s := steps[idx]; s.Tool != "" {
		return toolReply(fmt.Sprintf("%s-%d", a.PlanEntry, idx), s.Tool, s.Args)
	}
	return textReply(steps[idx].Text)
}

func (x *activationE2E) setLead(key string, steps ...e2eLeadStep) {
	x.mu.Lock()
	x.leadSteps[key] = steps
	x.mu.Unlock()
}

func (x *activationE2E) setAgent(entry string, steps ...fakeStep) {
	x.mu.Lock()
	x.agentRun[entry] = steps
	x.mu.Unlock()
}

// leadResults returns the results the lead received for its calls of tool, in
// call order (read from the provider requests that followed them); calls
// without a result yet are left out.
func (x *activationE2E) leadResults(t *testing.T, tool string) []llm.Message {
	t.Helper()
	x.mu.Lock()
	calls := append([]e2eLeadCall(nil), x.leadCalls...)
	x.mu.Unlock()
	reqs, _ := x.p.snapshot()
	var out []llm.Message
	for _, c := range calls {
		if c.tool != tool {
			continue
		}
		found := false
		for i := len(reqs) - 1; i >= 0 && !found; i-- {
			for _, m := range reqs[i].Messages {
				if m.Role == llm.RoleTool && m.ToolCallID == c.id {
					out = append(out, m)
					found = true
					break
				}
			}
		}
		// A call still waiting (a pending plan card) has no result yet.
	}
	return out
}

// startTurn starts a user turn and returns its completion channel.
func (x *activationE2E) startTurn(t *testing.T, p protocol.TurnStartParams) (protocol.Turn, <-chan protocol.Turn) {
	t.Helper()
	p.ThreadID = x.th.ID
	wait := make(chan protocol.Turn, 1)
	tr, err := x.e.startTurn(t.Context(), turnRequest{TurnStartParams: p, wait: wait})
	if err != nil {
		t.Fatal(err)
	}
	return tr, wait
}

func waitTurnDone(t *testing.T, wait <-chan protocol.Turn, want string) protocol.Turn {
	t.Helper()
	select {
	case done := <-wait:
		if done.Status != want {
			t.Fatalf("turn ended %s (%s), want %s", done.Status, done.Error, want)
		}
		return done
	case <-time.After(30 * time.Second):
		t.Fatal("turn did not finish")
	}
	return protocol.Turn{}
}

func (x *activationE2E) turn(t *testing.T, p protocol.TurnStartParams) {
	t.Helper()
	_, wait := x.startTurn(t, p)
	waitTurnDone(t, wait, protocol.TurnCompleted)
}

func (x *activationE2E) suggestionCount(t *testing.T) int {
	t.Helper()
	var n int
	if err := x.e.Store.DB.QueryRow(`SELECT count(*) FROM agent_suggestions WHERE thread_id=?`, x.th.ID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// suggestTurn creates a legacy suggestion directly for compatibility tests.
// New model turns never offer agent.suggest.
func (x *activationE2E) suggestTurn(t *testing.T) string {
	t.Helper()
	turn, wait := x.startTurn(t, protocol.TurnStartParams{Text: "legacy suggestion setup"})
	waitTurnDone(t, wait, protocol.TurnCompleted)
	ctx := tools.WithScope(context.WithValue(t.Context(), executionTurnKey{}, turn), &tools.Scope{ThreadID: x.th.ID, ProjectID: x.th.ProjectID, Root: x.root})
	out, err := x.e.suggestAgents(ctx, "the parser and the printer can be built in parallel")
	if err != nil {
		t.Fatalf("legacy suggestion setup: %v", err)
	}
	return suggestionID(t, out)
}

// enable answers the suggestion card with enable and returns the token.
func (x *activationE2E) enable(t *testing.T, suggestion string) string {
	t.Helper()
	res, err := x.e.RespondAgentSuggestion(t.Context(), protocol.AgentSuggestionRespondParams{SuggestionID: suggestion, Decision: protocol.SuggestionEnable})
	if err != nil || res.Suggestion.State != store.SuggestionEnabled || res.ActivationToken == "" {
		t.Fatalf("enable: %+v %v", res, err)
	}
	return res.ActivationToken
}

// proposeTurn starts the admitted turn (token) on the two-model pool in which
// the lead proposes plans (one step per proposal); it returns the turn and
// its completion channel. The turn blocks on the first plan card.
func (x *activationE2E) proposeTurn(t *testing.T, token string, plans ...string) (protocol.Turn, <-chan protocol.Turn) {
	t.Helper()
	steps := make([]e2eLeadStep, 0, len(plans))
	for _, p := range plans {
		steps = append(steps, e2eLeadStep{"agent.propose_plan", p})
	}
	x.setLead(e2eBuild, steps...)
	return x.startTurn(t, protocol.TurnStartParams{Text: e2eBuild, DelegationActivationToken: token,
		Override: protocol.ModelSelection{Provider: protocol.PoolProvider, Model: "e2e-pool", Complexity: protocol.ComplexityHigh}})
}

// admitted records the task the token admitted and the lead's tool context.
func (x *activationE2E) admitted(t *testing.T) {
	t.Helper()
	task, err := x.e.Store.ActiveAgentTask(t.Context(), x.th.ID, x.th.ProjectID)
	if err != nil {
		t.Fatalf("no admitted task: %v", err)
	}
	x.task = task
	x.lead = tools.WithScope(withDelegationTask(t.Context(), task), &tools.Scope{ThreadID: x.th.ID, ProjectID: x.th.ProjectID, Root: x.root})
}

func (x *activationE2E) pendingCard(t *testing.T, e *Engine, skip ...string) (protocol.Approval, protocol.PlanCard) {
	t.Helper()
	a := planEnv{e: e}.pendingPlanApproval(t, skip...)
	return a, planCardOf(t, a)
}

func (x *activationE2E) call(t *testing.T, tool, callID, args string) (string, error) {
	t.Helper()
	return callAgentTool(t, x.e, x.lead, tool, callID, args)
}

func (x *activationE2E) spawn(t *testing.T, entry string) string {
	t.Helper()
	out, err := x.call(t, "agent.spawn", "spawn-"+entry, `{"name":"`+entry+`","planEntry":"`+entry+`","assignment":{"prompt":"do your part"}}`)
	if err != nil {
		t.Fatalf("spawn %s: %v", entry, err)
	}
	var res struct {
		ID string `json:"id"`
	}
	if err = json.Unmarshal([]byte(out), &res); err != nil || res.ID == "" {
		t.Fatalf("spawn %s output %q: %v", entry, out, err)
	}
	return res.ID
}

func (x *activationE2E) waitAgent(t *testing.T, id string) protocol.RuntimeAgent {
	t.Helper()
	ctx, cancel := context.WithTimeout(x.lead, 60*time.Second)
	defer cancel()
	if _, err := callAgentTool(t, x.e, ctx, "agent.wait", "wait-"+id, `{"ids":["`+id+`"],"mode":"all"}`); err != nil {
		t.Fatal(err)
	}
	a, err := x.e.Store.RuntimeAgent(t.Context(), x.task.ID, id)
	if err != nil {
		t.Fatal(err)
	}
	if a.State != protocol.AgentStateCompleted {
		t.Fatalf("%s ended %s: %+v", a.Name, a.State, a.Result)
	}
	return a
}

func (x *activationE2E) integrate(t *testing.T, args string) (integrateOut, error) {
	t.Helper()
	raw, err := x.call(t, "agent.integrate", "integrate-"+args, args)
	var out integrateOut
	if err == nil {
		if jerr := json.Unmarshal([]byte(raw), &out); jerr != nil {
			t.Fatalf("output %q: %v", raw, jerr)
		}
	}
	return out, err
}

func (x *activationE2E) mustIntegrate(t *testing.T, args string) integrateOut {
	t.Helper()
	out, err := x.integrate(t, args)
	if err != nil {
		t.Fatalf("agent.integrate %s: %v", args, err)
	}
	return out
}

// verify plays the lead's verification.run with the given check statuses.
func (x *activationE2E) verify(t *testing.T, status ...string) {
	t.Helper()
	x.runner.set(runResults(status...))
	target, err := x.e.beginDelegationVerification(x.lead, "verification.run")
	if err != nil || target == nil {
		t.Fatalf("begin verification = %v %v", target, err)
	}
	if err = x.e.finishDelegationVerification(x.lead, "verification.run", target, x.runner.out); err != nil {
		t.Fatal(err)
	}
}

// agentModels returns the model of every request made by agent id.
func (x *activationE2E) agentModels(id string) []string {
	reqs, agents := x.p.snapshot()
	var out []string
	for i, a := range agents {
		if a == id {
			out = append(out, reqs[i].Model)
		}
	}
	return out
}

// agentToolResult returns the result agent id received for its call callID.
func (x *activationE2E) agentToolResult(t *testing.T, id, callID string) string {
	t.Helper()
	reqs, agents := x.p.snapshot()
	for i, a := range agents {
		if a != id {
			continue
		}
		for _, m := range reqs[i].Messages {
			if m.Role == llm.RoleTool && m.ToolCallID == callID {
				return m.Result
			}
		}
	}
	t.Fatalf("agent %s never received the result of %s", id, callID)
	return ""
}

func planEntry(name, purpose, files, model string) string {
	m := ""
	if model != "" {
		m = fmt.Sprintf(`,"model":%q`, model)
	}
	return fmt.Sprintf(`{"name":%q,"purpose":%q,"summary":%q,"capabilities":{"files":%q,"shell":"none","network":false,"spawnMax":0}%s}`, name, purpose, purpose, files, m)
}

// e2eTwoWritersAndReviewer is the plan of the main flow.
var e2eTwoWritersAndReviewer = `{"agents":[` + strings.Join([]string{
	planEntry("alpha", "change A", "read-write", "claude/deterministic"),
	planEntry("beta", "change B", "read-write", "claude/deterministic"),
	planEntry("reviewer", "review the integrated A", "read", ""),
}, ",") + `]}`

// approvedFlow runs suggest => enable => token => turn/start => propose_plan
// => approve (with models edits) and returns the approved card.
func (x *activationE2E) approvedFlow(t *testing.T, plan string, models map[string]string) protocol.PlanCard {
	t.Helper()
	token := x.enable(t, x.suggestTurn(t))
	_, wait := x.proposeTurn(t, token, plan)
	a, card := x.pendingCard(t, x.e)
	if a.Status != "pending" {
		t.Fatalf("card %+v", a)
	}
	res, err := x.e.RespondAgentPlan(t.Context(), protocol.AgentPlanRespondParams{PlanID: card.PlanID, Decision: protocol.PlanDecisionApprove, Models: models})
	if err != nil || res.Plan.State != protocol.PlanApproved {
		t.Fatalf("approve: %+v %v", res, err)
	}
	waitTurnDone(t, wait, protocol.TurnCompleted)
	x.admitted(t)
	return card
}

func TestE2ESuggestEnablePlanApproveSpawnIntegrate(t *testing.T) {
	x := newActivationE2E(t, nil)
	x.setAgent("alpha", fakeStep{Tool: "file.write", Args: `{"path":"a.go","content":` + jsonString(e2eFromA) + `}`}, fakeStep{Text: "alpha changed A"})
	x.setAgent("beta", fakeStep{Tool: "file.write", Args: `{"path":"b.go","content":` + jsonString(e2eFromB) + `}`}, fakeStep{Text: "beta changed B"})
	x.setAgent("reviewer", fakeStep{Tool: "file.read", Args: `{"path":"a.go"}`}, fakeStep{Text: "A looks right"})

	// Step 1: the lead suggests agents in an ordinary turn; nothing starts.
	suggestion := x.suggestTurn(t)
	if _, err := x.e.Store.ActiveAgentTask(t.Context(), x.th.ID, x.th.ProjectID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a suggestion admitted a task: %v", err)
	}
	// The user enables: the token admits the next turn.
	token := x.enable(t, suggestion)

	// Step 2: the lead proposes the plan; the turn blocks on the card.
	_, wait := x.proposeTurn(t, token, e2eTwoWritersAndReviewer)
	a, card := x.pendingCard(t, x.e)
	var models []string
	for _, m := range card.Models {
		models = append(models, m.Name)
	}
	if !slices.Equal(models, []string{"claude/deterministic", "claude/second"}) {
		t.Fatalf("card models = %v, want the frozen pool", models)
	}
	if len(card.Agents) != 3 || len(card.Notices) != 0 {
		t.Fatalf("card = %+v", card)
	}
	if !x.events.sawState(card.PlanID, protocol.PlanProposed) {
		t.Fatal("no agent/plan/updated for the proposal")
	}
	select {
	case done := <-wait:
		t.Fatalf("turn ended while the card was pending: %+v", done)
	case <-time.After(100 * time.Millisecond):
	}
	if p, _ := x.e.Store.AgentPlan(t.Context(), card.PlanID); p.State != protocol.PlanProposed {
		t.Fatalf("plan %q before the decision, want proposed", p.State)
	}
	// The user approves, moving beta to the pool's second model.
	res, err := x.e.RespondAgentPlan(t.Context(), protocol.AgentPlanRespondParams{PlanID: card.PlanID, Decision: protocol.PlanDecisionApprove, Models: map[string]string{"beta": "second"}})
	if err != nil || res.Plan.State != protocol.PlanApproved {
		t.Fatalf("approve: %+v %v", res, err)
	}
	waitTurnDone(t, wait, protocol.TurnCompleted)
	if got, _ := x.e.Store.GetApproval(t.Context(), a.ID); got.Status != "approved" {
		t.Fatalf("card status %q", got.Status)
	}
	if !x.events.sawState(card.PlanID, protocol.PlanApproved) {
		t.Fatal("no agent/plan/updated for the approval")
	}
	var lead struct {
		State  string `json:"state"`
		Agents []struct {
			Name, Model string
		} `json:"agents"`
	}
	results := x.leadResults(t, "agent.propose_plan")
	if err := json.Unmarshal([]byte(results[0].Result), &lead); err != nil || lead.State != "approved" || len(lead.Agents) != 3 {
		t.Fatalf("lead's plan result %q: %v", results[0].Result, err)
	}
	approvedModel := map[string]string{}
	for _, ag := range lead.Agents {
		approvedModel[ag.Name] = ag.Model
	}
	if approvedModel["alpha"] != "claude/deterministic" || approvedModel["beta"] != "claude/second" || approvedModel["reviewer"] != "" {
		t.Fatalf("approved models = %v", approvedModel)
	}
	x.admitted(t)

	// Spawn both writers; both work on the base revision.
	alpha := x.waitAgent(t, x.spawn(t, "alpha"))
	beta := x.waitAgent(t, x.spawn(t, "beta"))
	for _, ag := range []protocol.RuntimeAgent{alpha, beta} {
		r, err := x.e.Store.AgentRevision(t.Context(), x.task.ID, ag.ID)
		if err != nil || r.State != store.RevisionSubmitted || r.BaseRevision != x.base {
			t.Fatalf("%s revision = %+v %v", ag.Name, r, err)
		}
	}
	// Requested vs actual models, and the model each agent was really run on.
	for _, c := range []struct {
		a     protocol.RuntimeAgent
		model string
	}{{alpha, "deterministic"}, {beta, "second"}} {
		if c.a.ModelRequested != "claude/"+c.model || c.a.ModelActual != "claude/"+c.model {
			t.Fatalf("%s requested %q actual %q (trail %v)", c.a.Name, c.a.ModelRequested, c.a.ModelActual, c.a.RouteTrail)
		}
		for _, m := range x.agentModels(c.a.ID) {
			if m != c.model {
				t.Fatalf("%s ran on %q, want %q", c.a.Name, m, c.model)
			}
		}
	}

	// Accept alpha and verify the integrated state.
	if out := x.mustIntegrate(t, `{"action":"accept","agent":"`+alpha.ID+`"}`); out.Revision.State != store.RevisionApplied {
		t.Fatalf("accept alpha = %+v", out)
	}
	x.verify(t, "passed", "passed")

	// A read-only reviewer spawned after the first accept sees the integrated
	// tree; the project checkout is untouched by the spawn.
	beforeSpawn := rtCheckoutState(t, x.root)
	reviewer := x.waitAgent(t, x.spawn(t, "reviewer"))
	if got := x.agentToolResult(t, reviewer.ID, "reviewer-0"); !strings.Contains(got, "return 1") {
		t.Fatalf("reviewer read %q, want alpha's integrated change", got)
	}
	if after := rtCheckoutState(t, x.root); after != beforeSpawn {
		t.Fatalf("spawning the reviewer changed the project checkout:\n%s\n---\n%s", beforeSpawn, after)
	}
	// The reviewer's entry named no model and the frozen scope is a pool, so
	// nothing was prefilled: the runner records the pool model it picked.
	if reviewer.ModelRequested != "" || reviewer.ModelActual != "claude/deterministic" {
		t.Fatalf("reviewer requested %q actual %q", reviewer.ModelRequested, reviewer.ModelActual)
	}

	// Accept beta: a different file, so no conflict.
	if out := x.mustIntegrate(t, `{"action":"accept","agent":"`+beta.ID+`"}`); out.Revision.State != store.RevisionApplied {
		t.Fatalf("accept beta = %+v", out)
	}
	x.verify(t, "passed", "passed")
	done := x.mustIntegrate(t, `{"action":"complete"}`)
	if done.TaskState != protocol.AgentStateCompleted || done.CleanupWarning != "" {
		t.Fatalf("complete = %+v", done)
	}

	// The project checkout is exactly the combined tree, staged, on the base.
	if head := delegationGit(t, x.root, "rev-parse", "HEAD"); head != x.base {
		t.Fatalf("HEAD moved: %s -> %s", x.base, head)
	}
	if staged := delegationGit(t, x.root, "write-tree"); staged != done.HeadTree {
		t.Fatalf("staged tree %s, want the integrated head %s", staged, done.HeadTree)
	}
	for name, want := range map[string]string{"a.go": e2eFromA, "b.go": e2eFromB} {
		if data, err := os.ReadFile(filepath.Join(x.root, name)); err != nil || string(data) != want {
			t.Fatalf("%s = %q %v", name, data, err)
		}
		if staged := delegationGit(t, x.root, "show", ":"+name); staged != strings.TrimSpace(want) {
			t.Fatalf("staged %s = %q", name, staged)
		}
	}
	if st := delegationGit(t, x.root, "status", "--porcelain"); st != "M  a.go\nM  b.go" {
		t.Fatalf("status = %q", st)
	}
}

// restartedEngine starts a second engine on x's store and secrets, as after a
// restart, with x's scripted provider.
func restartedEngine(t *testing.T, x *activationE2E) (*Engine, *runtimeScriptProvider) {
	t.Helper()
	cfg := config.Default(t.TempDir())
	cfg.LLM.Provider = "claude"
	cfg.LLM.Model = "deterministic"
	p := &runtimeScriptProvider{script: x.script}
	reg := llm.NewRegistry()
	reg.Register(p)
	restarted, err := New(t.Context(), Options{Config: cfg, Store: x.e.Store, Secrets: x.e.Secrets, LLMs: reg, DisableMCP: true, DisableScheduler: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { restarted.Shutdown(context.Background()) })
	return restarted, p
}

// suggestTurnOn runs suggestTurn on another engine (a restarted one).
func (x *activationE2E) suggestTurnOn(t *testing.T, e *Engine, p *runtimeScriptProvider) string {
	t.Helper()
	prevE, prevP := x.e, x.p
	x.e, x.p = e, p
	defer func() { x.e, x.p = prevE, prevP }()
	return x.suggestTurn(t)
}

// rtCheckoutState fingerprints the project checkout: HEAD, raw index bytes
// and status.
func rtCheckoutState(t *testing.T, root string) string {
	t.Helper()
	index, err := os.ReadFile(filepath.Join(root, ".git", "index"))
	if err != nil {
		t.Fatal(err)
	}
	return delegationGit(t, root, "rev-parse", "HEAD") + "\n" + fmt.Sprintf("%x", index) + "\n" + delegationGit(t, root, "status", "--porcelain")
}

func TestE2EDeclineThenNoRepeatSuggestion(t *testing.T) {
	x := newActivationE2E(t, nil)
	suggestion := x.suggestTurn(t)
	res, err := x.e.RespondAgentSuggestion(t.Context(), protocol.AgentSuggestionRespondParams{SuggestionID: suggestion, Decision: protocol.SuggestionDecline})
	if err != nil || res.Suggestion.State != store.SuggestionDeclined || res.ActivationToken != "" {
		t.Fatalf("decline: %+v %v", res, err)
	}
	// A stale model call to the removed tool is refused; no second card is
	// stored and nothing starts.
	x.setLead(e2eSuggest, e2eLeadStep{"agent.suggest", `{"reason":"parallel work"}`})
	x.turn(t, protocol.TurnStartParams{Text: e2eSuggest})
	results := x.leadResults(t, "agent.suggest")
	if len(results) != 1 || !results[0].IsError || !strings.Contains(results[0].Result, "no longer permitted") {
		t.Fatalf("second suggestion = %+v", results)
	}
	if n := x.suggestionCount(t); n != 1 {
		t.Fatalf("stored %d suggestions", n)
	}
	if _, err := x.e.Store.ActiveAgentTask(t.Context(), x.th.ID, x.th.ProjectID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a task was admitted: %v", err)
	}
	// Once the user enables agents themselves, the lead may suggest again.
	if _, err := x.e.PrepareDelegation(t.Context(), x.th.ID); err != nil {
		t.Fatal(err)
	}
	if id := x.suggestTurn(t); id == suggestion {
		t.Fatal("the declined suggestion was reused")
	}
	if n := x.suggestionCount(t); n != 2 {
		t.Fatalf("stored %d suggestions", n)
	}
}

func TestE2EReviseThenSecondProposal(t *testing.T) {
	x := newActivationE2E(t, nil)
	first := `{"agents":[` + planEntry("alpha", "change A", "read-write", "") + "," + planEntry("beta", "change B", "read-write", "") + `]}`
	second := `{"agents":[` + planEntry("solo", "change A and B", "read-write", "") + `]}`
	token := x.enable(t, x.suggestTurn(t))
	_, wait := x.proposeTurn(t, token, first, second)
	a1, card1 := x.pendingCard(t, x.e)
	if _, err := x.e.RespondAgentPlan(t.Context(), protocol.AgentPlanRespondParams{PlanID: card1.PlanID, Decision: protocol.PlanDecisionRevise, Feedback: "one writer is enough"}); err != nil {
		t.Fatal(err)
	}
	// The lead gets the feedback and proposes again in the same turn.
	a2, card2 := x.pendingCard(t, x.e, a1.ID)
	if card2.PlanID == card1.PlanID || len(card2.Agents) != 1 || card2.Agents[0].Name != "solo" {
		t.Fatalf("second card = %+v", card2)
	}
	results := x.leadResults(t, "agent.propose_plan")
	if results[0].Result != `{"state":"revise","feedback":"one writer is enough"}` {
		t.Fatalf("lead's first plan result = %q", results[0].Result)
	}
	if p, _ := x.e.Store.AgentPlan(t.Context(), card1.PlanID); p.State != protocol.PlanSuperseded {
		t.Fatalf("first plan = %q, want superseded", p.State)
	}
	if !x.events.sawState(card1.PlanID, protocol.PlanSuperseded) {
		t.Fatal("no agent/plan/updated for the superseded plan")
	}
	// The superseded card can never be approved.
	_, err := x.e.RespondAgentPlan(t.Context(), protocol.AgentPlanRespondParams{PlanID: card1.PlanID, Decision: protocol.PlanDecisionApprove})
	requireRefusal(t, err, "plan was superseded")
	if _, err = x.e.RespondAgentPlan(t.Context(), protocol.AgentPlanRespondParams{PlanID: card2.PlanID, Decision: protocol.PlanDecisionApprove}); err != nil {
		t.Fatal(err)
	}
	waitTurnDone(t, wait, protocol.TurnCompleted)
	if got, _ := x.e.Store.GetApproval(t.Context(), a2.ID); got.Status != "approved" {
		t.Fatalf("second card %q", got.Status)
	}
	results = x.leadResults(t, "agent.propose_plan")
	if len(results) != 2 || !strings.Contains(results[1].Result, `"state":"approved"`) {
		t.Fatalf("lead's plan results = %+v", results)
	}
	x.admitted(t)
	plan, err := x.e.Store.ApprovedAgentPlanForTask(t.Context(), x.task.ID)
	if err != nil || plan.ID != card2.PlanID {
		t.Fatalf("approved plan = %+v %v", plan, err)
	}
	var approved int
	if err = x.e.Store.DB.QueryRow(`SELECT count(*) FROM agent_plans WHERE thread_id=? AND state='approved'`, x.th.ID).Scan(&approved); err != nil || approved != 1 {
		t.Fatalf("approved plans = %d %v", approved, err)
	}
	// Spawning follows the approved revision only.
	if _, err = x.call(t, "agent.spawn", "spawn-alpha", `{"name":"alpha","planEntry":"alpha","assignment":{"prompt":"go"}}`); err == nil {
		t.Fatal("spawned an entry of the superseded plan")
	}
	x.waitAgent(t, x.spawn(t, "solo"))
}

func TestE2EAmendAfterFailedCombinedChecks(t *testing.T) {
	x := newActivationE2E(t, nil)
	x.setAgent("alpha", fakeStep{Tool: "file.write", Args: `{"path":"a.go","content":` + jsonString(e2eFromA) + `}`}, fakeStep{Text: "alpha changed A"})
	x.setAgent("beta", fakeStep{Tool: "file.write", Args: `{"path":"b.go","content":` + jsonString(e2eFromB) + `}`}, fakeStep{Text: "beta changed B"})
	plan := `{"agents":[` + planEntry("alpha", "change A", "read-write", "") + "," + planEntry("beta", "change B", "read-write", "") + `]}`
	x.approvedFlow(t, plan, nil)
	alpha := x.waitAgent(t, x.spawn(t, "alpha"))
	beta := x.waitAgent(t, x.spawn(t, "beta"))
	x.mustIntegrate(t, `{"action":"accept","agent":"`+alpha.ID+`"}`)
	x.verify(t, "passed", "passed")
	x.mustIntegrate(t, `{"action":"accept","agent":"`+beta.ID+`"}`)
	// The combined result fails its checks: completion is refused with the
	// amend advice.
	x.verify(t, "failed", "passed")
	_, err := x.integrate(t, `{"action":"complete"}`)
	if err == nil || !strings.Contains(err.Error(), `agent.integrate {action:"amend"}`) {
		t.Fatalf("complete with failing checks: %v", err)
	}
	// The lead fixes beta's file in the project checkout, stages it, amends.
	const fixed = "package b\n\nfunc B() int { return 3 }\n"
	if err = os.WriteFile(filepath.Join(x.root, "b.go"), []byte(fixed), 0o644); err != nil {
		t.Fatal(err)
	}
	delegationGit(t, x.root, "add", "b.go")
	staged := delegationGit(t, x.root, "write-tree")
	out := x.mustIntegrate(t, `{"action":"amend","reason":"B must return 3 with A's change"}`)
	if out.HeadTree != staged || out.Amendments != 1 || out.Verified || out.AppliedCount != 2 {
		t.Fatalf("amend = %+v", out)
	}
	for _, id := range []string{alpha.ID, beta.ID} {
		if r, _ := x.e.Store.AgentRevision(t.Context(), x.task.ID, id); r.State != store.RevisionApplied {
			t.Fatalf("revision of %s = %s", id, r.State)
		}
	}
	x.verify(t, "passed", "passed")
	done := x.mustIntegrate(t, `{"action":"complete"}`)
	if done.TaskState != protocol.AgentStateCompleted || done.Amendments != 1 {
		t.Fatalf("complete = %+v", done)
	}
	// beta's original result exists only in its workspace now: completion keeps
	// the applied agents' workspaces and says why.
	if !strings.Contains(done.CleanupWarning, "amended 1 time(s)") || !strings.Contains(done.CleanupWarning, beta.ID) {
		t.Fatalf("cleanup warning = %q", done.CleanupWarning)
	}
	for _, ag := range []protocol.RuntimeAgent{alpha, beta} {
		r, _ := x.e.Store.AgentRevision(t.Context(), x.task.ID, ag.ID)
		w, err := x.e.Store.AgentWorkspace(t.Context(), r.WorkspaceID)
		if err != nil || w.Cleaned {
			t.Fatalf("workspace of %s = %+v %v, want retained", ag.Name, w, err)
		}
	}
	r, _ := x.e.Store.AgentRevision(t.Context(), x.task.ID, beta.ID)
	w, _ := x.e.Store.AgentWorkspace(t.Context(), r.WorkspaceID)
	if data, err := os.ReadFile(filepath.Join(w.Root, "b.go")); err != nil || string(data) != e2eFromB {
		t.Fatalf("beta's workspace b.go = %q %v", data, err)
	}
	if got := delegationGit(t, x.root, "write-tree"); got != done.HeadTree || got != staged {
		t.Fatalf("checkout tree %s, head %s, staged fix %s", got, done.HeadTree, staged)
	}
}

func TestE2ERestartWithPendingPlan(t *testing.T) {
	x := newActivationE2E(t, nil)
	plan := `{"agents":[` + planEntry("worker", "change A", "read-write", "") + `]}`
	token := x.enable(t, x.suggestTurn(t))
	turn, wait := x.proposeTurn(t, token, plan)
	a, card := x.pendingCard(t, x.e)
	x.admitted(t)

	// The engine restarts on the same store while the card is pending.
	restarted, p2 := restartedEngine(t, x)
	var err error
	if got, _ := x.e.Store.GetApproval(t.Context(), a.ID); got.Status != "expired" {
		t.Fatalf("card after restart %q, want expired", got.Status)
	}
	if p, _ := x.e.Store.AgentPlan(t.Context(), card.PlanID); p.State != protocol.PlanProposed {
		t.Fatalf("plan after restart %q, want proposed", p.State)
	}
	_, err = restarted.RespondAgentPlan(t.Context(), protocol.AgentPlanRespondParams{PlanID: card.PlanID, Decision: protocol.PlanDecisionApprove})
	requireRefusal(t, err, "expired")
	if _, err = callAgentTool(t, restarted, x.lead, "agent.spawn", "spawn-early", `{"name":"worker","planEntry":"worker","assignment":{"prompt":"go"}}`); err == nil {
		t.Fatal("spawn accepted without an approved plan")
	}
	if _, err = x.e.Store.ApprovedAgentPlanForTask(t.Context(), x.task.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("approved plan after restart: %v", err)
	}
	// Startup interrupts the unfinished task, as for every delegation task
	// (agents and tasks are never resumed after a restart).
	if task, _ := x.e.Store.AgentTask(t.Context(), x.task.ID); task.State != protocol.AgentStateInterrupted {
		t.Fatalf("task after restart = %s, want interrupted", task.State)
	}
	// The old process is gone: end its turn the way a dying process does,
	// without a user stop, and let it settle.
	x.e.mu.Lock()
	old := x.e.activeTurns[turn.ID]
	x.e.mu.Unlock()
	if old == nil {
		t.Fatal("the proposing turn is not running")
	}
	old.cancel()
	select {
	case <-wait:
	case <-time.After(30 * time.Second):
		t.Fatal("the old turn did not end")
	}
	if p, _ := x.e.Store.AgentPlan(t.Context(), card.PlanID); p.State != protocol.PlanProposed {
		t.Fatalf("stale plan %q, want proposed", p.State)
	}

	// A new proposal is possible: the next message, the lead suggests again,
	// the user enables, the lead proposes, the user approves, agents start.
	x.e, x.p = restarted, p2
	x.e.Tools.Add(x.planner)
	x.e.Tools.Add(x.runner)
	token = x.enable(t, x.suggestTurn(t))
	_, wait = x.proposeTurn(t, token, plan)
	a2, card2 := x.pendingCard(t, restarted, a.ID)
	if card2.PlanID == card.PlanID {
		t.Fatal("the new proposal reused the stale plan")
	}
	if _, err = restarted.RespondAgentPlan(t.Context(), protocol.AgentPlanRespondParams{PlanID: card2.PlanID, Decision: protocol.PlanDecisionApprove}); err != nil {
		t.Fatal(err)
	}
	waitTurnDone(t, wait, protocol.TurnCompleted)
	if got, _ := restarted.Store.GetApproval(t.Context(), a2.ID); got.Status != "approved" {
		t.Fatalf("new card %q", got.Status)
	}
	stale := x.task.ID
	x.admitted(t)
	if x.task.ID == stale {
		t.Fatal("the interrupted task was reused")
	}
	if p, _ := restarted.Store.AgentPlan(t.Context(), card.PlanID); p.State != protocol.PlanProposed {
		t.Fatalf("stale plan %q, want still proposed", p.State)
	}
	if _, err = restarted.RespondAgentPlan(t.Context(), protocol.AgentPlanRespondParams{PlanID: card.PlanID, Decision: protocol.PlanDecisionApprove}); err == nil {
		t.Fatal("the stale plan was approved after a newer one")
	}
	x.waitAgent(t, x.spawn(t, "worker"))
}
