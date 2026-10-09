# Phase 5a Designed Workflow Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a deterministic Designed workflow that records the smallest-sufficient solution, maintains a revision-checked decision/task graph, blocks mutation behind unresolved material-choice gates, and gives the context compiler only the active work state without adding a model call.

**Architecture:** Extend the existing SQLite Work graph rather than creating a second planning system. `internal/work` remains the semantic owner: it classifies depth, validates graph deltas, derives readiness, and decides completion. `internal/store` applies already-validated deltas and workflow approval outcomes atomically under optimistic concurrency. A single `work.update` tool exposes compact batched deltas, while `internal/engine` owns approval transport and fail-closed runtime enforcement. The context compiler renders a priority-aware projection from persisted state; feature-off behavior stays byte-for-byte compatible.

**Tech Stack:** Go, SQLite migrations, existing JSON-RPC/tool protocol, existing approval bus/store, table-driven Go tests

**Spec:** `docs/superpowers/specs/2026-10-07-phase5-designed-workflow-memory-design.md`

## Global Constraints

- Phase 5a only. Do not implement `project_memories`, `UMCODE.md` promotion, merge logic, or `memory.auto_promote`; those belong to Phase 5b after this branch merges.
- `models.designed_workflow` defaults to `false`. With the flag off, tool exposure, prompts, classification, completion, approvals, request accounting, and transcript behavior must remain unchanged.
- Add no model call. Classification, validation, readiness, gate evaluation, and context projection must be deterministic.
- Preserve one canonical graph in SQLite. Do not write per-task spec, plan, or graph files into user projects.
- Apply each `work.update` batch atomically and increment `works.revision` exactly once. A workflow approval outcome is a separate atomic state change and increments the work revision exactly once.
- Never copy evidence text into graph-update requests or results. Refer to immutable evidence by ID.
- Redact bounded rationale before persistence. Reject oversized batches, invalid identifiers, unsupported states/relations, stale revisions, cross-work references, cycles, and invalid solution/evidence structures before any partial write.
- Workflow approvals are work-, node-, and node-revision-specific and never remembered. Existing tool-action approval semantics remain unchanged.
- A Designed gate read error blocks mutation. Observation recording outside the gate remains best-effort as today.
- Keep routine output compact: successful `work.update` results contain only the final work revision and created/transitioned/linked counts.
- Every task follows Red → Green → refactor and ends in one focused commit. Run the named focused tests before each commit.

## Review Focus

- Optimistic concurrency must cover every semantic state change; stale clients must not partly mutate the graph.
- Graph validation must be pure and deterministic, with no hidden dependence on transcript order or model prose.
- Readiness and completion are derived from persisted graph/evidence state, never trusted from a requested task status.
- Approval response and node transition must share one SQLite transaction so a crash cannot leave an approved approval beside a proposed decision.
- Runtime gating must occur before hooks, ordinary policy approval, and tool execution; its read-only allowlist must not accidentally admit mutation-capable plugin or MCP tools.
- Flag-off golden prompts and exposed tool sets must remain unchanged.
- Context projection must retain P0 gates/explicit decisions/active task/blocking unknowns while omitting rejected, superseded, completed, and inactive detail.
- Token accounting must expose the schema, call-argument, and result cost of `work.update`; Phase 5 savings must not hide its overhead.

---

### Task 1: Add the additive schema, protocol contract, and feature flag

**Files:**

- Create: `internal/store/migrations/0015_designed_workflow.sql`
- Modify: `internal/protocol/work.go`
- Modify: `internal/protocol/types.go`
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`
- Modify: `internal/store/work.go`
- Modify: `internal/store/work_test.go`

- [ ] **Step 1: Write failing migration and round-trip tests**

Add tests covering both a fresh database and an existing database upgraded through migration 0014:

- `TestDesignedWorkflowMigrationAndRoundTrip` asserts old works load with `Revision == 1`, new works persist a caller-supplied revision, and `GetWorkDetail` returns node-evidence references.
- `TestDesignedWorkflowMigrationPreservesExistingWork` inserts a Phase 2 work before applying migration 0015, migrates, and verifies all prior work/node/edge/evidence/attempt data is unchanged.
- Extend approval round-trip coverage to assert default `Kind == "tool"` and optional workflow identity fields.
- `TestDesignedWorkflowFlagDefaultsFalse` mirrors the existing compiler/reducer flag tests and proves `models: {designed_workflow: true}` opts in.

Run:

```bash
go test ./internal/store ./internal/config -run 'TestDesignedWorkflow(Migration|Flag)' -count=1
```

Expected: FAIL because the migration, fields, and flag do not exist.

- [ ] **Step 2: Add migration 0015**

The migration must be additive:

```sql
ALTER TABLE works ADD COLUMN revision INTEGER NOT NULL DEFAULT 1;
ALTER TABLE approvals ADD COLUMN kind TEXT NOT NULL DEFAULT 'tool';
ALTER TABLE approvals ADD COLUMN work_id TEXT NOT NULL DEFAULT '';
ALTER TABLE approvals ADD COLUMN node_id TEXT NOT NULL DEFAULT '';
ALTER TABLE approvals ADD COLUMN node_revision INTEGER NOT NULL DEFAULT 0;

CREATE TABLE work_node_evidence (
    work_id TEXT NOT NULL REFERENCES works(id) ON DELETE CASCADE,
    node_id TEXT NOT NULL REFERENCES work_nodes(id) ON DELETE CASCADE,
    evidence_id TEXT NOT NULL REFERENCES evidence(id) ON DELETE CASCADE,
    PRIMARY KEY (work_id, node_id, evidence_id)
);
```

Add indexes for `work_node_evidence(work_id, node_id)` and workflow approvals by `(work_id, node_id, status)`. Do not rebuild existing tables.

- [ ] **Step 3: Define the Phase 5a protocol vocabulary**

In `internal/protocol/work.go`:

- Add `DepthDesigned`.
- Add node kinds `requirement`, `non_goal`, `option`, `decision`, `task`, `unknown`, and `memory_candidate` (the candidate kind is defined now for forward-compatible graph validation, but promotion is not implemented).
- Add relations `depends_on`, `supports`, `contradicts`, `selects`, `implements`, `verifies`, and `candidate_for`.
- Add the lifecycle status constants from the approved spec.
- Add `Revision int` to `Work`.
- Add `EvidenceIDs []string` to `WorkNode` and populate it from the join table in details.
- Define bounded tool DTOs: `WorkUpdateRequest`, `WorkNodeChange`, `WorkEdgeChange`, `WorkUpdateResult`, and internal `WorkflowGate`.
- Define persistence-only `PreparedWorkUpdate`, `WorkNodeTransition`, and `WorkNodeEvidenceLink` structs here as well. `internal/work` constructs them and `internal/store` consumes them; putting these data-only contracts in `internal/protocol` avoids the forbidden `store -> work -> store` import cycle.

Use separate create/transition fields in `WorkNodeChange`:

```go
type WorkNodeChange struct {
    Ref              string          `json:"ref,omitempty"`
    ID               string          `json:"id,omitempty"`
    Kind             string          `json:"kind,omitempty"`
    Title            string          `json:"title,omitempty"`
    Content          json.RawMessage `json:"content,omitempty"`
    ExpectedRevision int             `json:"expected_revision,omitempty"`
    FromStatus       string          `json:"from_status,omitempty"`
    ToStatus         string          `json:"to_status,omitempty"`
    EvidenceIDs      []string        `json:"evidence_ids,omitempty"`
}
```

`WorkUpdateResult` contains `revision`, `created`, `transitioned`, and `linked` only. Keep gate metadata internal and excluded from JSON.

In `internal/protocol/types.go`, extend `Approval` with `Kind`, `WorkID`, `NodeID`, and `NodeRevision`.

- [ ] **Step 4: Wire storage scans and the flag**

- Extend `workCols`, `scanWork`, `CreateWork`, and `GetWorkDetail` for work revision and node-evidence rows.
- Extend approval create/list scans for the additive identity fields while preserving default tool approvals.
- Add `DesignedWorkflow bool `yaml:"designed_workflow"`` to `ModelsConfig`.

Run:

```bash
gofmt -w internal/protocol/work.go internal/protocol/types.go internal/config/config.go internal/config/config_test.go internal/store/work.go internal/store/work_test.go
go test ./internal/store ./internal/config -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/store/migrations/0015_designed_workflow.sql internal/protocol/work.go internal/protocol/types.go internal/config/config.go internal/config/config_test.go internal/store/work.go internal/store/work_test.go
git commit -m "feat: add designed workflow schema"
```

### Task 2: Implement pure classification, graph validation, readiness, and completion rules

**Files:**

- Create: `internal/work/graph.go`
- Create: `internal/work/graph_test.go`
- Modify: `internal/work/rules.go`
- Modify: `internal/work/work.go`
- Modify: `internal/work/work_test.go`

- [ ] **Step 1: Write the classification matrix first**

Add `TestWorkflowDepthClassification` and `TestWorkflowDepthNeverDowngrades` for these pure signatures:

```go
func InitialDepth(userText string) string
func ObservedDepth(current string, detail protocol.WorkDetail, observation Observation) string
func MaxDepth(a, b string) string
```

The table must prove:

- ordinary Q&A remains Direct;
- existing write/yellow-shell/verification triggers still reach Guided;
- explicit design/architecture requests reach Designed at turn start;
- edits to migration paths, `internal/protocol`, public schema/file-format surfaces, security/auth/secret/trust-boundary surfaces, billing/payment surfaces, and destructive recovery paths reach Designed;
- two or more changed top-level ownership areas reach Designed only when the observation is mutating;
- Designed never becomes Guided or Direct.

Run:

```bash
go test ./internal/work -run 'TestWorkflowDepth' -count=1
```

Expected: FAIL because the functions and `DepthDesigned` behavior are absent.

- [ ] **Step 2: Write graph-validation tests**

Add table-driven tests around:

```go
func PrepareUpdate(detail protocol.WorkDetail, req protocol.WorkUpdateRequest, now time.Time) (protocol.PreparedWorkUpdate, error)
```

Assert the prepared value contains the work ID/expected revision plus resolved creates, transitions, edges, evidence links, derived readiness transitions, and internal gate descriptors defined in Task 1.

Cover:

- all allowed kinds, states, transitions, and relations;
- duplicate client refs and duplicate IDs;
- missing/foreign nodes and evidence;
- stale node revisions/from-states;
- cross-work edges and evidence;
- selection edges that do not connect a decision to an option in the same work;
- solution rung range 1–6 and required `verifies`/evidence support links;
- gate metadata accepted only for approved gate kinds;
- direct work may only use an update that escalates it to Guided/Designed;
- rationale/node/edge/count/text bounds;
- candidate validation structure, without any Phase 5b promotion side effect.

Use stable typed validation errors with a field/class code so the tool can return compact reasons without graph contents.

- [ ] **Step 3: Write cycle/readiness/completion tests**

Add:

```go
func DeriveTaskStatuses(detail protocol.WorkDetail) map[string]string
func CompletionBlockers(detail protocol.WorkDetail) []string
func PendingWorkflowGates(detail protocol.WorkDetail) []protocol.WorkflowGate
```

Tests must prove:

- a batch-local cycle and a cycle closed through an existing `depends_on` edge are rejected;
- requested `ready` is ignored unless every prerequisite task is completed, required decision is selected/approved, blocking unknown is resolved or approved as accepted risk, and a nontrivial task has a criterion;
- task state only advances along the approved lifecycle and terminal states never reopen;
- completion remains blocked by unresolved required criteria, proposed required decisions, nonterminal required tasks, open blocking unknowns, or stale verification;
- rejected/superseded optional nodes do not block completion;
- Direct completion retains the current behavior;
- Guided/Designed completion requires a rung-1–6 decision and active task graph when the feature is enabled.

Run:

```bash
go test ./internal/work -run 'Test(PrepareUpdate|DependencyCycle|DerivedReadiness|CompletionBlockers|WorkflowDepth)' -count=1
```

Expected: FAIL before implementation, then PASS after implementing the pure rules.

- [ ] **Step 4: Integrate classification and completion behind the flag**

Add `DesignedWorkflow bool` to `work.Service`.

- In `Begin`, use `InitialDepth` only when enabled.
- In `Observe`, use monotonic `ObservedDepth` only when enabled; retain the existing Guided escalation path when disabled.
- In `End`, use `CompletionBlockers` for Guided/Designed work only when enabled; retain `Unresolved` exactly when disabled or Direct.

Run:

```bash
gofmt -w internal/work/graph.go internal/work/graph_test.go internal/work/rules.go internal/work/work.go internal/work/work_test.go
go test ./internal/work -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/work/graph.go internal/work/graph_test.go internal/work/rules.go internal/work/work.go internal/work/work_test.go
git commit -m "feat: validate designed work graphs"
```

### Task 3: Apply graph deltas transactionally with optimistic concurrency

**Files:**

- Modify: `internal/store/work.go`
- Modify: `internal/store/work_test.go`
- Modify: `internal/work/work.go`
- Modify: `internal/work/work_test.go`

- [ ] **Step 1: Write atomicity and concurrency tests**

Define the persistence boundary:

```go
func (s *Store) ApplyWorkUpdate(ctx context.Context, update protocol.PreparedWorkUpdate) (protocol.WorkUpdateResult, error)
func (s *Service) Update(ctx context.Context, threadID string, req protocol.WorkUpdateRequest) (protocol.WorkUpdateResult, []protocol.WorkflowGate, error)
```

The prepared structs are data-only contracts in `internal/protocol`; semantic construction and validation stay in `internal/work`.

Tests:

- `TestApplyWorkUpdateCommitsOneRevision` creates nodes, transitions an existing node, adds edges/evidence links, derives readiness, and asserts the work revision increments once.
- `TestApplyWorkUpdateStaleRevisionRollsBack` uses an old expected revision and asserts no rows changed.
- `TestApplyWorkUpdateConstraintFailureRollsBack` injects a bad final insert and asserts earlier creates/transitions did not persist.
- `TestApplyWorkUpdateRejectsForeignRowsInsideTransaction` proves ownership is rechecked in the transaction, not trusted from pre-validation.
- `TestServiceUpdateConcurrentWinner` launches two requests at the same revision and asserts exactly one commits.
- `TestServiceUpdateRedactsRationale` verifies secrets never reach persisted node content or audit data.

Run:

```bash
go test ./internal/store ./internal/work -run 'Test(ApplyWorkUpdate|ServiceUpdate)' -count=1
```

Expected: FAIL because no batch API exists.

- [ ] **Step 2: Implement one transactional store method**

Inside a single `BeginTx`:

1. Compare-and-swap `works.revision` using `WHERE id = ? AND revision = ?`.
2. Verify every existing node and evidence row belongs to that work.
3. Insert generated nodes and their evidence references.
4. Apply status/revision transitions with expected node revision and from-status predicates.
5. Insert edges.
6. Apply derived readiness transitions prepared by `internal/work`.
7. Commit, returning only counts and the incremented revision.

Map zero affected rows to a typed conflict error. Roll back on every other error.

- [ ] **Step 3: Implement the service orchestration**

`Service.Update` must:

- require the request's work to be the calling thread's current open work;
- load `WorkDetail` once;
- call `PrepareUpdate` and derive statuses;
- call the transactional store method;
- return gate descriptors separately from the compact JSON result;
- record only IDs, revision, counts, and rejection class in diagnostics.

Run:

```bash
gofmt -w internal/store/work.go internal/store/work_test.go internal/work/work.go internal/work/work_test.go
go test ./internal/store ./internal/work -count=1
```

Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add internal/store/work.go internal/store/work_test.go internal/work/work.go internal/work/work_test.go
git commit -m "feat: apply work graph updates atomically"
```

### Task 4: Expose one compact `work.update` tool and conditional instructions

**Files:**

- Create: `internal/tools/work.go`
- Create: `internal/tools/work_test.go`
- Modify: `internal/tools/tools.go`
- Modify: `internal/engine/engine.go`
- Modify: `internal/engine/engine_prompt_test.go`
- Modify: `internal/engine/tool_allowed_test.go`

- [ ] **Step 1: Write tool contract tests**

Introduce an interface that keeps `internal/tools` independent of the concrete work service:

```go
type WorkUpdater interface {
    Update(context.Context, string, protocol.WorkUpdateRequest) (protocol.WorkUpdateResult, []protocol.WorkflowGate, error)
}

type WorkflowUpdateTool interface {
    Tool
    Apply(context.Context, json.RawMessage) (protocol.WorkUpdateResult, []protocol.WorkflowGate, error)
}
```

Add tests proving:

- the JSON schema requires `work_id` and `expected_revision`, bounds arrays/strings, and exposes no memory-promotion operation;
- `Call`/`Apply` require a scoped thread and pass that thread to `WorkUpdater`;
- successful JSON contains only revision/counts, never node text, rationale, evidence text, or gate rationale;
- typed validation/conflict errors become compact actionable errors;
- `Assess` is green because this tool only mutates the internal graph; product mutation remains separately gated.

Run:

```bash
go test ./internal/tools -run 'TestWorkUpdate' -count=1
```

Expected: FAIL because the tool does not exist.

- [ ] **Step 2: Register only when enabled**

Construct one `work.Service` before engine assembly, set `DesignedWorkflow` from config, and register `NewWorkUpdate(workService)` only when `models.designed_workflow` is true. Reuse that same service in `Engine.Work`.

Add a conditional `layerWorkflow` system-prompt layer that tells the model, compactly:

- Direct work should not call `work.update` unless semantic structure or classification changes.
- Guided/Designed implementation must record the first sufficient solution rung, linked criteria/evidence, current task, and blocking unknowns.
- The engine derives readiness and enforces gates.

Do not change the core prompt. Assert every existing flag-off prompt golden is byte-identical and that the workflow layer appears only when enabled.

- [ ] **Step 3: Run focused tests**

```bash
gofmt -w internal/tools/work.go internal/tools/work_test.go internal/tools/tools.go internal/engine/engine.go internal/engine/engine_prompt_test.go internal/engine/tool_allowed_test.go
go test ./internal/tools ./internal/engine -run 'Test(WorkUpdate|SystemPrompt|PromptLayers|ToolAllowed)' -count=1
```

Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add internal/tools/work.go internal/tools/work_test.go internal/tools/tools.go internal/engine/engine.go internal/engine/engine_prompt_test.go internal/engine/tool_allowed_test.go
git commit -m "feat: add compact work update tool"
```

### Task 5: Make workflow approvals atomic and enforce gates before mutation

**Files:**

- Modify: `internal/store/records.go`
- Modify: `internal/store/store_test.go`
- Modify: `internal/engine/approvals.go`
- Modify: `internal/engine/approvals_test.go`
- Create: `internal/engine/workflow.go`
- Create: `internal/engine/workflow_test.go`
- Modify: `internal/engine/turn.go`
- Modify: `internal/engine/work_test.go`

- [ ] **Step 1: Write atomic workflow-approval tests**

Add store methods:

```go
func (s *Store) DecideWorkflowApproval(ctx context.Context, id, status, by string) (int, error)
```

The method transactionally:

- loads a pending `kind=workflow` approval;
- compares its work/node/node-revision identity;
- changes `proposed -> approved` or `proposed -> rejected`;
- increments the node revision and work revision once;
- finalizes the approval row;
- rolls everything back on stale identity or any injected error.

Tests prove approve, deny, duplicate response, stale node revision, crash/rollback behavior, and returned final work revision.

Run:

```bash
go test ./internal/store -run 'TestDecideWorkflowApproval' -count=1
```

Expected: FAIL before implementation.

- [ ] **Step 2: Write engine approval behavior tests**

Split the existing transport into reusable persistence/wait logic and add:

```go
func (e *Engine) requestWorkflowApproval(ctx, sctx context.Context, turn protocol.Turn, item protocol.Item, gate protocol.WorkflowGate) (bool, error)
```

Tests must prove:

- request records `Kind == "workflow"`, exact work/node/node revision, and a distinct audit event;
- approval atomically changes the decision to approved before waking the tool loop;
- denial changes it to rejected and leaves dependent tasks blocked;
- timeout expires the approval but leaves the node proposed;
- `Remember: true` is ignored for workflow approvals and creates no thread decision;
- restart expires a pending workflow approval without changing its node;
- replacement decisions can be proposed after rejection.

- [ ] **Step 3: Write fail-closed runtime-gate tests**

Add pure classification and persisted gate lookup:

```go
func workflowDiscoveryTool(name string) bool
func (e *Engine) checkWorkflowGate(ctx context.Context, threadID, tool string) error
```

The allowlist contains only known read-only discovery plus `work.update`: `file.read`, `file.list`, `file.search`, `web.search`, `web.fetch`, `verification.plan`, `computer.list`, `computer.inspect`, and `visual.inspect`. Unknown plugin/MCP tools are blocked while a gate is pending because their side effects are not provably read-only.

Tests prove:

- Direct/Guided and ungated Designed work retain current behavior;
- pending Designed gates allow the discovery list and `work.update`;
- file edits/writes, shell/exec, verification execution, browser/visual actions, Computer Use actions, task tools, unknown plugin tools, and MCP tools are blocked before hooks, policy approval, and `Call`;
- approval opens the gate and existing safety policy then runs normally;
- a forced gate-query database error blocks a mutation with a compact deterministic error and never calls the tool;
- the feature-off path performs no gate query and is unchanged.

- [ ] **Step 4: Integrate gated `work.update` execution**

In `runTool`:

1. Resolve the tool.
2. Check the persisted workflow gate before guard/hooks/policy/tool execution.
3. For a `WorkflowUpdateTool`, call `Apply` exactly once.
4. Persist and request each returned workflow gate using its exact node revision; process each independently.
5. Let `RespondApproval` call `DecideWorkflowApproval` for workflow approvals and the existing `DecideApproval` path for tool approvals.
6. Re-read the work revision after gate decisions and serialize the final compact `WorkUpdateResult`.
7. On denial/expiry, return a concise result naming only gate status and node ID; do not expose rationale/evidence.

Run:

```bash
gofmt -w internal/store/records.go internal/store/store_test.go internal/engine/approvals.go internal/engine/approvals_test.go internal/engine/workflow.go internal/engine/workflow_test.go internal/engine/turn.go internal/engine/work_test.go
go test ./internal/store ./internal/engine -run 'Test(DecideWorkflowApproval|WorkflowApproval|WorkflowGate|WorkUpdate)' -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/store/records.go internal/store/store_test.go internal/engine/approvals.go internal/engine/approvals_test.go internal/engine/workflow.go internal/engine/workflow_test.go internal/engine/turn.go internal/engine/work_test.go
git commit -m "feat: enforce designed workflow gates"
```

### Task 6: Compile the active graph and attribute Phase 5a token cost

**Files:**

- Modify: `internal/ctxcompiler/compiler.go`
- Modify: `internal/ctxcompiler/work_packet.go`
- Modify: `internal/ctxcompiler/work_packet_test.go`
- Modify: `internal/ctxcompiler/compiler_test.go`
- Modify: `internal/engine/compile.go`
- Modify: `internal/engine/compiler_test.go`
- Modify: `internal/engine/accounting.go`
- Modify: `internal/engine/accounting_test.go`

- [ ] **Step 1: Write active-projection tests**

Add `DesignedWorkflow bool` to `ctxcompiler.Input`. When false, `workPacket` must produce the current byte-for-byte output.

When true, tests must assert a deterministic two-priority projection:

- P0: workflow depth/revision, unresolved gate, explicit approved decision, active task, incomplete task dependencies, blocking unknowns, active criteria, and definition of completion;
- P1: selected option/solution rung, applicable requirement invariants, and compact supporting evidence IDs/URIs only.

Rejected options, superseded decisions, completed task detail, resolved unknowns, and inactive memory candidates must be absent. Sort semantic lines by stable graph identity, not insertion timing.

Budget tests prove P1 is dropped before P0 and compilation declines rather than removing required gates or completion state.

Run:

```bash
go test ./internal/ctxcompiler -run 'Test(Designed|WorkPacket|Compile)' -count=1
```

Expected: FAIL before the projection exists.

- [ ] **Step 2: Implement projection and engine wiring**

- Pass `e.Cfg.Models.DesignedWorkflow` from `internal/engine/compile.go`.
- Extend `Report`/`RequestPackets` only as needed to keep P0/P1 and evidence accounting explicit.
- Preserve compiler fallback: a projection error or budget refusal uses canonical history without mutating graph or weakening runtime gates.
- Add a restart reconstruction test that closes/reopens the store and gets the identical packet from SQLite.

- [ ] **Step 3: Attribute schema, call, and result tokens**

Extend `RequestBreakdown` with:

```go
WorkUpdateSpecTokens   int `json:"workUpdateSpecTokens,omitempty"`
WorkUpdateCallTokens   int `json:"workUpdateCallTokens,omitempty"`
WorkUpdateResultTokens int `json:"workUpdateResultTokens,omitempty"`
```

Measure the matching tool spec, `work.update` tool-call name/arguments, and paired tool result while retaining each value in existing totals. Add tests that sum the fields correctly and remain zero when the tool is absent or the flag is off.

Run:

```bash
gofmt -w internal/ctxcompiler/compiler.go internal/ctxcompiler/work_packet.go internal/ctxcompiler/work_packet_test.go internal/ctxcompiler/compiler_test.go internal/engine/compile.go internal/engine/compiler_test.go internal/engine/accounting.go internal/engine/accounting_test.go
go test ./internal/ctxcompiler ./internal/engine -run 'Test(Designed|Compiler|RequestAccounting|MeasureRequest)' -count=1
```

Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add internal/ctxcompiler/compiler.go internal/ctxcompiler/work_packet.go internal/ctxcompiler/work_packet_test.go internal/ctxcompiler/compiler_test.go internal/engine/compile.go internal/engine/compiler_test.go internal/engine/accounting.go internal/engine/accounting_test.go
git commit -m "feat: compile active designed work state"
```

### Task 7: Prove restart, compatibility, and end-to-end behavior

**Files:**

- Modify: `internal/server/work_e2e_test.go`
- Modify: `internal/server/compiler_e2e_test.go`
- Modify: `GO_ENGINE.md`

- [ ] **Step 1: Add end-to-end scenarios**

Use the real server protocol, fake deterministic LLM events, real SQLite store, approval notifications, and actual built-in tool registry.

Add:

- `TestGuidedWorkflowRecordsSolutionTaskAndVerification`: a Guided coding turn records a rung decision, task/criterion links, verification, compact packet, no workflow approval, and successful completion.
- `TestDesignedWorkflowBlocksMutationUntilApproval`: a public-schema request starts Designed, `work.update` proposes a gated decision, a premature `file.write` is blocked, approval atomically opens the gate, and the retried mutation executes.
- `TestDesignedWorkflowApprovalSurvivesRestart`: persist a proposed gated decision, restart the engine, reconstruct the same graph/packet, and confirm mutation stays blocked until a fresh node-specific approval is resolved.
- `TestRejectedDecisionKeepsTaskBlockedAndAllowsReplacement`: deny the first decision, verify dependent task remains blocked, then approve a replacement decision and derive readiness.
- `TestDesignedWorkflowGeneralPurposeNoProject`: the same graph/gate behavior works without a project and performs no file/memory action.
- `TestDesignedWorkflowFlagOffCompatibility`: compare tool list, prompt golden, work lifecycle, completion, and request accounting against the pre-Phase-5 fixture.

Run:

```bash
go test ./internal/server -run 'Test(GuidedWorkflow|DesignedWorkflow)' -count=1
```

Expected: FAIL until all integration seams are correct, then PASS.

- [ ] **Step 2: Document the opt-in contract**

Update `GO_ENGINE.md` with:

- `models.designed_workflow: true` configuration;
- deterministic Direct → Guided → Designed escalation;
- the six solution rungs and `work.update` purpose;
- which decisions require non-rememberable approval;
- fail-closed mutation behavior and read-only discovery allowance;
- active context projection and token-accounting fields;
- explicit note that automatic curated memory is not in Phase 5a.

- [ ] **Step 3: Run the full verification matrix**

```bash
go test ./internal/protocol ./internal/config ./internal/store ./internal/work ./internal/tools ./internal/ctxcompiler ./internal/engine ./internal/server -count=1
go test ./... -count=1
go vet ./...
```

Expected: all PASS with no race-independent flakes or golden drift in flag-off mode.

- [ ] **Step 4: Inspect the final diff for scope and noise**

```bash
git diff --check origin/master...HEAD
git diff --stat origin/master...HEAD
git status --short
```

Confirm no Phase 5b implementation, generated user-project files, unrelated formatting, secrets, or untracked artifacts are present.

- [ ] **Step 5: Commit**

```bash
git add internal/server/work_e2e_test.go internal/server/compiler_e2e_test.go GO_ENGINE.md
git commit -m "test: prove designed workflow end to end"
```

### Task 8: Review the completed branch before handoff

**Files:**

- Review all files changed since `origin/master`

- [ ] **Step 1: Request a specification-compliance review**

Use `superpowers:requesting-code-review`. The reviewer must compare the branch to the approved Phase 5 spec and this plan, with special attention to the Global Constraints and Review Focus above.

- [ ] **Step 2: Resolve every substantive finding with TDD**

For each accepted finding, first add or strengthen a failing regression test, make the minimum fix, rerun the focused package, and commit the repair separately. Use `superpowers:receiving-code-review` before acting on feedback.

- [ ] **Step 3: Run completion verification from a clean status**

Use `superpowers:verification-before-completion`, then rerun:

```bash
go test ./... -count=1
go vet ./...
git diff --check origin/master...HEAD
git status --short --branch
```

Expected: tests and vet PASS; diff check is empty; status shows only the intended ahead commits and no uncommitted files.

- [ ] **Step 4: Prepare the Phase 5a handoff**

Summarize behavior, migration/rollback implications, verification evidence, review findings, remaining risks, and the exact feature flag. Do not start Phase 5b until Phase 5a is merged.
