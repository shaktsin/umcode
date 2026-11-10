# Agent Runtime Core (Plan 1 of 5) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build the general agent runtime core (records, capabilities, plan-bound admission, budget shares, concurrent scheduler, service and tools) beside the legacy delegation flow, shipped dark behind `agents.runtime_v2`.

**Architecture:** A new package `internal/agentrt` holds all runtime logic behind small interfaces (`AgentStore`, `Runner`). New SQLite tables store agents, approved plans and mailbox messages. New `agent.*` tools replace the legacy ones only when the flag is on; the legacy `internal/delegation` path is untouched. Real agent execution (workspaces, providers, sandbox) is Plan 2; here a fake `Runner` proves the core.

**Tech Stack:** Go (module `github.com/shaktsin/umcode`), SQLite via `internal/store`, standard `testing`.

**Spec:** `docs/superpowers/specs/2026-10-24-agent-runtime-design.md` (sections 4.1, 4.2, 4.4, 4.5, 4.9 and the flag from 6/Phase 1).

**Plan series (each produces working, testable software):**
1. This plan: runtime core.
2. Execution adapter: `Runner` over the existing child-execution machinery, isolated workspaces, capability-derived tool allowlist in `delegation_policy.go`, read-only shell sandbox spike (Phase 0), model fallback recording.
3. Multi-revision integration (spec 4.7).
4. Two-step activation: `agent.suggest`, `agent.propose_plan`, plan approval, chat-assigned models (spec 4.8, 4.10).
5. Frontend (spec 5) and cut-over (spec 6, Phase 5): legacy code is deleted as soon as the new flow passes its tests; old tables stay as read-only history.

## Global Constraints

- Capability strings are exactly: files `none|read|read-write`; shell `none|read-only|read-write`; network boolean; `spawnMax` integer.
- Default limits: `maxConcurrent=8`, `maxDepth=3`, `maxTotal=40`, `sliceShare=0.25`, network `false`.
- `shell: read-only` is granted only when `ResolveOptions.ShellReadOnlySupported` is true; otherwise it is downgraded to `none` and reported in `rejected`.
- A spawned agent's authority is `parent ∩ approved plan entry ∩ request` and never exceeds the parent's.
- Name and purpose are free text and never affect authority.
- New migration file is `0034_agent_runtime.sql`; the legacy tables and `internal/delegation` are not modified.
- Budget shares are fractions of the task budget; float comparisons use an epsilon of `1e-9`.
- No Settings UI and no role files anywhere.
- Run Go tests with `go test ./internal/<pkg>/ -run <Name> -v`; commit after each task.

## Review Focus

- Two `Spawn` calls with the same tool-call ID create exactly one agent (retries must be safe).
- Spawn names a plan entry that does not exist, or the plan is `proposed`/`superseded`, not `approved`: refused with the field named.
- Cancelling a parent while its child is still `queued` cancels the child, returns its share, and starts nothing.
- Engine restart with `queued`, `running` and `waiting_approval` agents: all become `interrupted`, none auto-resume.
- Model JSON sends an unknown or empty capability string (e.g. `files: "root"`): it is rejected or treated as `none`, never widened.
- Four agents with share `0.25` fit exactly in the lead's `1.0`; a fifth is refused despite float accumulation error.

---

### Task 1: Protocol types, migration, runtime-agent storage

**Files:**
- Create: `internal/protocol/agentrt.go`
- Create: `internal/store/migrations/0034_agent_runtime.sql`
- Create: `internal/store/agentrt.go`
- Test: `internal/store/agentrt_test.go`

**Interfaces:**
- Produces (`protocol`): `FileAccess` (`FilesNone|FilesRead|FilesReadWrite`), `ShellAccess` (`ShellNone|ShellReadOnly|ShellReadWrite`), `AgentCapabilities{Files FileAccess; Shell ShellAccess; Network bool; SpawnMax int}`, `PlanAgent{Name, Purpose, Summary string; Capabilities AgentCapabilities; Model string}`, `PlanLimits{MaxConcurrent, MaxDepth, MaxTotal int; SliceShare float64; Network bool}`, `DefaultPlanLimits() PlanLimits`, `PlanState` (`PlanProposed|PlanApproved|PlanSuperseded`), `AgentPlan{ID, ThreadID, TaskID string; Agents []PlanAgent; Limits PlanLimits; State PlanState; CreatedAt, ApprovedAt time.Time}`, `RuntimeAgent{ID, TaskID, ParentAgentID, Name, Purpose, PlanID string; Assignment AgentAssignment; Capabilities AgentCapabilities; ModelRequested, ModelActual string; RouteTrail []string; BudgetShare float64; Depth int; ContextMode, WorkspaceID, Revision string; State AgentState; Result AgentResult; Usage UsageTotals; Version int64; CreatedAt, UpdatedAt time.Time}`, `AgentMessage{ID, TaskID, FromAgentID, ToAgentID, Body string; CreatedAt time.Time; DeliveredAt *time.Time}`, `CapabilityRejection{Field, Reason string}`.
- Produces (`store.Store` methods): `CreateRuntimeAgent(ctx, a protocol.RuntimeAgent, callID string) (protocol.RuntimeAgent, error)` (idempotent per `(task, callID)`: returns the existing agent), `RuntimeAgentForCall(ctx, taskID, callID string) (protocol.RuntimeAgent, error)`, `RuntimeAgent(ctx, taskID, id string) (protocol.RuntimeAgent, error)`, `RuntimeAgents(ctx, taskID string) ([]protocol.RuntimeAgent, error)` (ordered by creation), `UpdateRuntimeAgent(ctx, a protocol.RuntimeAgent, expectedVersion int64) error` (mutable: state, model_actual, route_trail, workspace_id, revision, result, usage; bumps version; rejects invalid transitions using `validAgentTransition`), `ReconcileRuntimeAgents(ctx) error` (sets `queued`, `running`, `waiting_approval` to `interrupted`).

- [ ] **Step 1: Write the failing tests** in `internal/store/agentrt_test.go`. Add helper `runtimeFixture(t) (*Store, protocol.AgentTask)` mirroring `coordinatorFixture` in `internal/delegation/coordinator_test.go` (`Open`, `CreateThread`, `CreateTurn`, `CreateAgentTask`). Tests:
  - `TestCreateRuntimeAgentIsIdempotentByCallID`: create with `callID "c1"` twice; assert same `ID`, `len(RuntimeAgents) == 1`, `Version == 1`, `State == queued`.
  - `TestUpdateRuntimeAgentChecksVersionAndTransition`: update with `expectedVersion=1` to `running` succeeds and version becomes 2; repeating with stale version 1 returns an error and leaves version 2; `queued → completed` directly returns an error.
  - `TestRuntimeAgentJSONRoundTrip`: capabilities `{read-write, read-only, false, 2}`, `RouteTrail ["a","b"]`, `BudgetShare 0.25`, `Depth 1`, and `Assignment.AcceptanceChecks` survive create+read.
  - `TestReconcileRuntimeAgentsInterruptsActive`: agents in `queued`, `running`, `waiting_approval`, `completed`; after reconcile the first three are `interrupted`, `completed` unchanged.
- [ ] **Step 2: Run to verify failure:** `go test ./internal/store/ -run 'RuntimeAgent' -v` → FAIL (undefined).
- [ ] **Step 3: Implement** the protocol file (types and `DefaultPlanLimits() = {8,3,40,0.25,false}`), the migration (`agents` with `UNIQUE(task_id, call_id)` and FK `task_id → agent_tasks ON DELETE CASCADE`; `agent_plans`; `agent_messages` with `delivered_at` nullable; JSON columns for capabilities, assignment, result, usage, route trail, plan agents/limits), and the store methods in `internal/store/agentrt.go`. Follow the scan/insert style of `internal/store/delegation.go`; reuse `validAgentTransition`.
- [ ] **Step 4: Run to verify pass:** same command → PASS; also `go test ./internal/store/` (existing tests still pass).
- [ ] **Step 5: Commit** `git add internal/protocol/agentrt.go internal/store/ && git commit -m "feat: runtime agent protocol types and storage"`.

---

### Task 2: Plans and mailbox storage

**Files:**
- Modify: `internal/store/agentrt.go`
- Test: `internal/store/agentrt_test.go`

**Interfaces:**
- Consumes: Task 1 types and tables.
- Produces: `SaveAgentPlan(ctx, p protocol.AgentPlan) error` (insert or replace by `ID`), `ApproveAgentPlan(ctx, planID, taskID string) error` (sets `approved`, binds `TaskID`, marks any other `approved` plan of the same thread `superseded`), `ApprovedAgentPlanForTask(ctx, taskID string) (protocol.AgentPlan, error)` (returns `ErrNotFound` when none), `LatestApprovedAgentPlan(ctx, threadID string) (protocol.AgentPlan, error)`, `EnqueueAgentMessage(ctx, m protocol.AgentMessage) (protocol.AgentMessage, error)`, `PendingAgentMessages(ctx, taskID, toAgentID string) ([]protocol.AgentMessage, error)` (undelivered, oldest first), `AcknowledgeAgentMessage(ctx, taskID, id string) error`.

- [ ] **Step 1: Write the failing tests:**
  - `TestApproveAgentPlanSupersedesPreviousAndBindsTask`: save plans A and B for one thread; approve A for task T, then B for task T2; assert `A.State == superseded`, `ApprovedAgentPlanForTask(T2).ID == B`, `ApprovedAgentPlanForTask(T)` returns `ErrNotFound`, `LatestApprovedAgentPlan(thread).ID == B`.
  - `TestProposedPlanIsNotReturnedAsApproved`: a `proposed` plan yields `ErrNotFound` from both getters.
  - `TestAgentMessagesDeliverOnceInOrder`: enqueue m1, m2 to agent X and m3 to agent Y; `PendingAgentMessages(X) == [m1,m2]`; after `AcknowledgeAgentMessage(m1)` it is `[m2]`; acknowledging m1 again is a no-op without error.
- [ ] **Step 2: Run:** `go test ./internal/store/ -run 'AgentPlan|AgentMessages' -v` → FAIL.
- [ ] **Step 3: Implement** the methods; approval and supersede happen in one transaction.
- [ ] **Step 4: Run** the same command → PASS.
- [ ] **Step 5: Commit** `git commit -am "feat: agent plans and mailbox storage"`.

---

### Task 3: Capability algebra

**Files:**
- Create: `internal/agentrt/capabilities.go`
- Test: `internal/agentrt/capabilities_test.go`

**Interfaces:**
- Consumes: `protocol.AgentCapabilities`, `protocol.CapabilityRejection`.
- Produces: `type ResolveOptions struct{ ShellReadOnlySupported bool }`; `Intersect(a, b protocol.AgentCapabilities) protocol.AgentCapabilities` (min rank per field, network AND, min `SpawnMax`); `Within(child, parent protocol.AgentCapabilities) bool`; `Resolve(parent, entry, requested protocol.AgentCapabilities, opt ResolveOptions) (protocol.AgentCapabilities, []protocol.CapabilityRejection)` (`granted = Intersect(Intersect(parent, entry), requested)`; one rejection per field where `requested` exceeds `granted`, with `Field` `capabilities.files|shell|network|spawnMax`; unknown or empty strings rank as `none` and produce a rejection with reason `unknown value`); `AllowedTools(c protocol.AgentCapabilities) []string`; `RootCapabilities() protocol.AgentCapabilities` (`read-write`, `read-write`, network true, `SpawnMax 1000`).

- [ ] **Step 1: Write the failing tests:**
  - `TestResolveNeverExceedsParent`: enumerate every combination of the three file values × three shell values × network {false,true} × `SpawnMax` {0,2} for parent, entry and requested (sampled or full); assert `Within(granted, parent)` and `Within(granted, entry)` for all with `ShellReadOnlySupported=true`.
  - `TestResolveReportsDroppedNetwork`: parent network false, requested true → `granted.Network == false`, rejections contain `Field "capabilities.network"`.
  - `TestResolveDowngradesReadOnlyShellWhenUnsupported`: all ceilings `read-only`, `ShellReadOnlySupported=false` → `granted.Shell == none` and a rejection on `capabilities.shell`; with `true` → `read-only`.
  - `TestResolveTreatsUnknownStringsAsNone`: requested `Files: "root"`, `Shell: ""` → granted files/shell `none`, rejection reason contains `unknown value`.
  - `TestAllowedToolsFollowCapabilities`: `{read,none,false,0}` → exactly `file.read`, `file.list`, `file.search`, `skill.get_instructions`; adding `read-write` adds `file.write`, `file.edit`; `shell != none` adds `shell.run`, `verification.run`; `SpawnMax > 0` adds `agent.spawn`, `agent.list`, `agent.status`, `agent.wait`, `agent.message`, `agent.cancel`; `{none,...}` files returns no `file.*` tools.
- [ ] **Step 2: Run:** `go test ./internal/agentrt/ -run 'Resolve|AllowedTools' -v` → FAIL.
- [ ] **Step 3: Implement** with small rank helpers (`filesRank`, `shellRank`); `AllowedTools` returns a sorted slice.
- [ ] **Step 4: Run** → PASS.
- [ ] **Step 5: Commit** `git add internal/agentrt && git commit -m "feat: capability algebra for runtime agents"`.

---

### Task 4: Budget share ledger

**Files:**
- Create: `internal/agentrt/ledger.go`
- Test: `internal/agentrt/ledger_test.go`

**Interfaces:**
- Consumes: `protocol.RuntimeAgent`.
- Produces: `NewShareLedger() *ShareLedger`; `(*ShareLedger).Free(parentID string, parentShare float64) float64` (parent's share minus live children's shares; `parentID ""` is the lead with share `1.0`); `Reserve(agentID, parentID string, share float64)`; `Release(agentID string)`; `RebuildLedger(agents []protocol.RuntimeAgent) *ShareLedger` (counts agents in `queued|running|waiting_approval`); const `shareEpsilon = 1e-9`.

- [ ] **Step 1: Write the failing tests:**
  - `TestFourQuarterSharesFitInLead`: reserve four `0.25` children of `""`; `Free("", 1.0)` is within epsilon of 0; a fifth `0.25` does not fit (`Free < 0.25 - shareEpsilon`).
  - `TestReleaseReturnsShare`: reserve then release; `Free("", 1.0) == 1.0`.
  - `TestGrandchildSharesComeFromParentShare`: child `c` share `0.4` under lead; `Free("c", 0.4) == 0.4`; reserve grandchild `0.3` under `c`; `Free("c", 0.4)` ≈ `0.1` and `Free("", 1.0)` stays ≈ `0.6`.
  - `TestRebuildIgnoresTerminalAgents`: completed and cancelled agents do not hold share after rebuild.
- [ ] **Step 2: Run:** `go test ./internal/agentrt/ -run 'Ledger|Quarter|Share|Rebuild' -v` → FAIL.
- [ ] **Step 3: Implement** with a mutex and `map[agentID]entry{parent, share}`.
- [ ] **Step 4: Run** → PASS.
- [ ] **Step 5: Commit** `git commit -am "feat: budget share ledger"` (add the new files first).

---

### Task 5: Plan-bound admission

**Files:**
- Create: `internal/agentrt/admission.go`
- Test: `internal/agentrt/admission_test.go`

**Interfaces:**
- Consumes: Task 3 `Resolve`, `ResolveOptions`; protocol plan/agent types.
- Produces:
  - `type SpawnRequest struct{ TaskID, CallID, CallerAgentID, Name, PlanEntry string; Assignment protocol.AgentAssignment; Capabilities *protocol.AgentCapabilities; Model, ContextMode string; BudgetShare float64 }` (`PlanEntry` is the plan agent's `Name`; `Capabilities == nil` means "use the plan entry's").
  - `type AdmissionInput struct{ Plan protocol.AgentPlan; Caller protocol.RuntimeAgent; All []protocol.RuntimeAgent; ParentShareFree float64; Options ResolveOptions; Request SpawnRequest }`.
  - `type Admission struct{ Entry protocol.PlanAgent; Granted protocol.AgentCapabilities; Rejected []protocol.CapabilityRejection; Model string; Share float64; Depth int }`.
  - `type AdmissionError struct{ Field, Message string }` with `Error()`.
  - `func Admit(in AdmissionInput) (Admission, error)`.
- Check order (first failure wins, each names its `Field`): `plan` (state must be `approved`), `planEntry` (exists in plan), `spawn` (caller `SpawnMax` > number of caller's direct children in `All`), `depth` (`Caller.Depth+1 <= Limits.MaxDepth`), `total` (`len(All) < Limits.MaxTotal`), `model` (empty means the entry's model; otherwise must equal it, message ends "Propose a revised plan."), `budget.share` (default `Limits.SliceShare`; `<= ParentShareFree + shareEpsilon`), then capabilities via `Resolve` (partial grant, never an error). `Limits.Network == false` forces `Granted.Network = false` with a rejection.

- [ ] **Step 1: Write the failing tests** (table test `TestAdmitRefusals` plus focused ones):
  - proposed plan → `Field "plan"`; unknown `PlanEntry` → `Field "planEntry"`; caller with `SpawnMax 1` already having one child → `"spawn"`; `Caller.Depth=3`, `MaxDepth=3` → `"depth"`; `len(All)=40` → `"total"`; `Model "opus"` against entry `qwen-coder` → `"model"` and message contains `revised plan`; share `0.5` with `ParentShareFree 0.25` → `"budget.share"`.
  - `TestAdmitDefaultsFromPlanEntry`: nil capabilities, empty model, zero share → `Granted == Intersect(parent, entry)`, `Model == entry.Model`, `Share == 0.25`, `Depth == Caller.Depth+1`.
  - `TestAdmitPartialGrantStillAdmits`: request network true with `Limits.Network=false` → admitted, `Granted.Network == false`, rejection on `capabilities.network`.
- [ ] **Step 2: Run:** `go test ./internal/agentrt/ -run Admit -v` → FAIL.
- [ ] **Step 3: Implement** `Admit` as a straight sequence of checks; no I/O.
- [ ] **Step 4: Run** → PASS.
- [ ] **Step 5: Commit** `git commit -am "feat: plan-bound admission"` (after `git add`).

---

### Task 6: Scheduler (queueing, concurrency, cascade cancel)

**Files:**
- Create: `internal/agentrt/store.go` (the `AgentStore` interface only)
- Create: `internal/agentrt/scheduler.go`
- Test: `internal/agentrt/scheduler_test.go`

**Interfaces:**
- Consumes: Task 1/2 store methods.
- Produces:
  - `type AgentStore interface` with exactly the `store.Store` methods from Tasks 1 and 2 (`CreateRuntimeAgent`, `RuntimeAgentForCall`, `RuntimeAgent`, `RuntimeAgents`, `UpdateRuntimeAgent`, `ReconcileRuntimeAgents`, `ApprovedAgentPlanForTask`, `EnqueueAgentMessage`, `PendingAgentMessages`, `AcknowledgeAgentMessage`).
  - `type Runner interface{ Run(ctx context.Context, a protocol.RuntimeAgent) (protocol.AgentResult, error) }`.
  - `NewScheduler(s AgentStore, r Runner, base context.Context, onChange func(protocol.RuntimeAgent)) *Scheduler`.
  - `(*Scheduler).Submit(ctx, a protocol.RuntimeAgent, maxConcurrent int)` (agent already stored as `queued`; starts it now or leaves it queued), `Cancel(ctx, taskID, agentID string) error` (cancels the agent and all descendants: running ones via their context, queued ones directly to `cancelled`; terminal agents are a no-op), `Changed(taskID string) <-chan struct{}` (closed and replaced on every state change in the task), `Active(taskID string) int`.
  - Behaviour: a finished or cancelled agent promotes the oldest queued agent of the same task; the completion result and any runner error are persisted (`completed`, or `failed` with `Result.Blocker` set to the error text).

- [ ] **Step 1: Write the failing tests** with a fake `Runner` driven by channels and the store from a `runtimeFixture` (open store in `t.TempDir()`):
  - `TestSchedulerQueuesBeyondMaxConcurrent`: submit 3 agents with `maxConcurrent=2`; assert two `running` and one `queued`; release one runner; the third becomes `running`.
  - `TestSchedulerPersistsFailure`: runner returns an error → state `failed`, `Result.Blocker` contains the message.
  - `TestCancelCascadesToDescendants`: parent running, child queued under it (`ParentAgentID` set), grandchild running; cancelling the parent leaves all three `cancelled` (or the running ones' contexts cancelled and then `cancelled`), and no further `Run` call starts.
  - `TestChangedChannelFiresOnTransition`: `Changed(task)` returns an open channel that closes when an agent changes state.
- [ ] **Step 2: Run:** `go test ./internal/agentrt/ -run Scheduler -v` → FAIL.
- [ ] **Step 3: Implement** with one mutex, per-task queues in creation order, a `cancel func` per running agent, and persistence through `UpdateRuntimeAgent` using the stored version (reload before each update). Call `onChange` after each persisted transition. Run with `go test -race ./internal/agentrt/ -run Scheduler`.
- [ ] **Step 4: Run** `go test -race ./internal/agentrt/ -run Scheduler -v` → PASS.
- [ ] **Step 5: Commit** `git add internal/agentrt && git commit -m "feat: concurrent agent scheduler"`.

---

### Task 7: Runtime service

**Files:**
- Create: `internal/agentrt/runtime.go`
- Test: `internal/agentrt/runtime_test.go`

**Interfaces:**
- Consumes: Tasks 3–6.
- Produces:
  - `type SpawnResult struct{ Agent protocol.RuntimeAgent; Granted protocol.AgentCapabilities; Rejected []protocol.CapabilityRejection; QueuedReason string }`.
  - `type WaitMode string` with `WaitAny`, `WaitAll`.
  - `type Service interface{ Spawn(ctx, SpawnRequest) (SpawnResult, error); List(ctx, taskID string) ([]protocol.RuntimeAgent, error); Status(ctx, taskID, id string) (protocol.RuntimeAgent, error); Wait(ctx, taskID string, ids []string, mode WaitMode) ([]protocol.RuntimeAgent, error); Message(ctx, taskID, fromID, toID, body string) (protocol.AgentMessage, error); Cancel(ctx, taskID, id string) error }`.
  - `NewRuntime(s AgentStore, r Runner, base context.Context, opts ...Option) *Runtime` implementing `Service`, plus `(*Runtime).Recover(ctx) error` (calls `ReconcileRuntimeAgents`) and `type Option` with `WithResolveOptions(ResolveOptions)` and `WithEvents(func(protocol.RuntimeAgent))`.
- Behaviour of `Spawn`: return the existing agent when `(TaskID, CallID)` already exists; the empty `CallerAgentID` is the lead (`RootCapabilities()`, `Depth 0`, share `1.0`); load the approved plan with `ApprovedAgentPlanForTask`; call `Admit`; reserve the share in a ledger rebuilt from stored agents; create the agent as `queued` with `ModelRequested = Admission.Model`; `Submit` to the scheduler with `Plan.Limits.MaxConcurrent`; set `QueuedReason` to `capacity: N/M running` when it stayed queued. `Message` rejects unknown or terminal targets. `Wait(any)` returns when any listed agent is terminal; `Wait(all)` when all are; both honour `ctx` cancellation. A mutex serialises `Spawn` (the lifecycle lock).

- [ ] **Step 1: Write the failing tests** (fake runner, approved plan saved via `SaveAgentPlan` + `ApproveAgentPlan`):
  - `TestSpawnIsIdempotentByCallID`: two concurrent `Spawn` calls with the same `CallID` return the same agent `ID`; `List` has one agent; the fake runner started once.
  - `TestSpawnRefusesUnapprovedPlan`: plan left `proposed` → `*AdmissionError` with `Field "plan"`; no agent stored.
  - `TestSpawnReportsPartialGrant`: request network true, plan `Limits.Network=false` → `SpawnResult.Rejected` non-empty and the agent is created.
  - `TestFifthQuarterShareRefused`: four spawns at share `0.25` succeed; the fifth fails with `Field "budget.share"`; after cancelling one, the fifth succeeds.
  - `TestSpawnQueuesAtCapacityWithReason`: `MaxConcurrent=1`; second spawn returns `State queued` and `QueuedReason` starting `capacity:`.
  - `TestCancelParentWhileChildQueuedReturnsShare`: parent runs, child queued; `Cancel(parent)` → both `cancelled`, a new spawn at the freed share succeeds, runner never started the child.
  - `TestWaitAnyAndAll`: with two agents, `Wait(any)` returns after the first completes; `Wait(all)` only after both.
  - `TestMessageDeliversToMailbox`: `Message(a→b)` appears in `PendingAgentMessages(b)`; message to a `completed` agent errors.
  - `TestRecoverInterruptsActiveAgents`: agents left `running`/`queued` in the store become `interrupted` after `Recover`, and the runner is not invoked.
- [ ] **Step 2: Run:** `go test -race ./internal/agentrt/ -run 'Spawn|Cancel|Wait|Message|Recover' -v` → FAIL.
- [ ] **Step 3: Implement** `Runtime`; compose `Admit`, the ledger and the scheduler; keep ledger state derived from the store (`RebuildLedger`) at each spawn so restarts and cancels never drift.
- [ ] **Step 4: Run** → PASS; also `go test -race ./internal/agentrt/`.
- [ ] **Step 5: Commit** `git add internal/agentrt && git commit -m "feat: runtime service"`.

---

### Task 8: Model-facing tools

**Files:**
- Create: `internal/tools/agentrt.go`
- Test: `internal/tools/agentrt_test.go`

**Interfaces:**
- Consumes: `agentrt.Service`, `tools.AgentAuthorize` and `WithAgentCallID` from `internal/tools/delegation.go`.
- Produces: `NewRuntimeAgentTools(svc agentrt.Service, authorize AgentAuthorize, caller func(context.Context) string) []Tool` returning tools named `agent.spawn`, `agent.list`, `agent.status`, `agent.wait`, `agent.message`, `agent.cancel` (`Name()`, `Description()`, `Schema()`, `Assess()` returning `RiskGreen`, `Call()`, `SelectionMetadata()` returning `("core","engine")`, as the legacy `agentTool` does).
- Spawn schema (`additionalProperties:false`; required `planEntry`, `assignment`): `name` string, `planEntry` string, `assignment{prompt (minLength 1), acceptanceChecks[], contextRefs[]}`, `capabilities{files enum, shell enum, network boolean, spawnMax integer}`, `model` string, `context` enum `none|summary|full`, `budget{share number 0..1}`. Wait schema: `ids[1..32]`, `mode` enum `any|all` (default `any`). Message schema: `to`, `message`.

- [ ] **Step 1: Write the failing tests** with a fake `agentrt.Service`:
  - `TestSpawnToolRequiresStableCallID`: no `WithAgentCallID` in ctx → error `stable tool call ID required`.
  - `TestSpawnToolRejectsUnknownFields`: args with `"role":"reviewer"` → decode error (no role field exists).
  - `TestSpawnToolPassesRequestAndCaller`: fake records the `SpawnRequest`; assert `TaskID` from `authorize`, `CallerAgentID` from `caller`, `CallID` from ctx, `Capabilities` non-nil only when supplied, and the JSON result contains `granted` and `rejected`.
  - `TestWaitToolDefaultsToAny`, `TestWaitToolRejectsMoreThan32IDs`.
  - `TestToolNamesAreExactlyTheRuntimeSet`: names equal the six above; none is `agent.followup`.
- [ ] **Step 2: Run:** `go test ./internal/tools/ -run 'Spawn|Wait|RuntimeSet' -v` → FAIL.
- [ ] **Step 3: Implement** mirroring `agentTool` in `delegation.go` (strict `json.Decoder` with `DisallowUnknownFields`, single object, JSON-marshalled results).
- [ ] **Step 4: Run** → PASS; `go test ./internal/tools/`.
- [ ] **Step 5: Commit** `git add internal/tools && git commit -m "feat: runtime agent tools"`.

---

### Task 9: Flag-gated engine wiring

**Files:**
- Modify: `internal/config/config.go` (`AgentsConfig`: add `RuntimeV2 bool \`yaml:"runtime_v2"\``)
- Modify: `config.example.yaml` (document `agents.runtime_v2: false` under the existing agents section)
- Modify: `internal/engine/engine.go:212-218` (registration block)
- Create: `internal/engine/agentrt_wiring.go`
- Test: `internal/engine/agentrt_wiring_test.go`

**Interfaces:**
- Consumes: `agentrt.NewRuntime`, `tools.NewRuntimeAgentTools`, `e.authorizeDelegationTool`.
- Produces: `type unavailableRunner struct{}` implementing `agentrt.Runner` whose `Run` returns the error `agent execution is not available yet (runtime_v2 execution adapter pending)`; field `Engine.AgentRuntime *agentrt.Runtime`; function `(e *Engine) registerAgentRuntime(reg *tools.Registry, base context.Context)`.
- Behaviour: when `o.Config.Agents.RuntimeV2` is true, register the six runtime tools (and call `Recover`) **instead of** the legacy `tools.NewAgentTools` set; when false, behaviour is exactly as today (legacy tools, no runtime). The `caller` function returns `""` (lead) in this plan.

- [ ] **Step 1: Write the failing tests** using the engine test constructor already used by other delegation tests in `internal/engine/*_test.go` (find it with `grep -n "New(" internal/engine/*_test.go`):
  - `TestRuntimeV2FlagOffKeepsLegacyTools`: tool registry contains `agent.followup` and not `agent.message`.
  - `TestRuntimeV2FlagOnRegistersRuntimeTools`: registry contains exactly the six runtime tool names (`agent.spawn`, `agent.list`, `agent.status`, `agent.wait`, `agent.message`, `agent.cancel`) and not `agent.followup`.
  - `TestRuntimeV2SpawnFailsClearlyWithoutExecutionAdapter`: with the flag on and an approved plan fixture, a spawned agent ends `failed` with `Result.Blocker` containing `execution adapter pending`.
- [ ] **Step 2: Run:** `go test ./internal/engine/ -run RuntimeV2 -v` → FAIL.
- [ ] **Step 3: Implement** the config field, the example comment, `registerAgentRuntime`, and the `if/else` in `engine.go` around the existing `NewAgentTools` loop (keep `NewAgentWorkflowTools` registration unchanged in both modes).
- [ ] **Step 4: Run** `go test ./internal/engine/ -run RuntimeV2 -v` → PASS; then the full suite: `go test ./...` → PASS (legacy delegation tests unchanged).
- [ ] **Step 5: Commit** `git add internal/config config.example.yaml internal/engine && git commit -m "feat: gate agent runtime v2 behind agents.runtime_v2"`.

---

## Self-review notes

- **Spec coverage (this plan's scope):** 4.1 data model → Tasks 1–2; 4.2 capability algebra and tool derivation (function only; allowlist wiring is Plan 2) → Task 3; 4.4 admission → Task 5; 4.5 scheduler/lifecycle/recovery → Tasks 6–7; 4.6 mailbox storage and `agent.message` → Tasks 2, 7, 8; 4.9 tool set minus `agent.suggest`/`propose_plan`/`integrate` → Task 8; flag from section 6 → Task 9. Not in this plan by design: 4.3 note, 4.7 integration (Plan 3), 4.8/4.10 (Plan 4), section 5 UI (Plan 5), sandbox spike (Plan 2).
- **Type consistency:** `SpawnRequest`, `AdmissionInput`, `Admission`, `AgentStore`, `Runner`, `Service` and `ShareLedger` are defined once (Tasks 5, 6, 4, 7) and used with those names later; store method names match between Tasks 1–2 and the `AgentStore` interface in Task 6.
