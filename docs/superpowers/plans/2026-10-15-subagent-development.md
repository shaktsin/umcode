# Explicit Subagent Development Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Deliver explicitly activated, sequential implementation and independent review inside one UMCode chat, with isolated edits and verified integration.

**Architecture:** An engine-owned coordinator manages persisted child lifecycles and delegates execution to the existing turn loop through a scoped internal adapter. A shared task budget, workspace adapter, and policy enforcement surround every child. Frontend state subscribes to stable child events and exposes activation and cancellation.

**Tech Stack:** Go, existing SQLite store and JSON-RPC event bus, Git worktrees, Svelte 5/TypeScript, Vitest and existing browser fixtures. No new production dependencies.

**Spec:** docs/superpowers/specs/2026-10-15-subagent-development-design.md

## Global Constraints

- Explicit activation only; Deep and model pools do not activate delegation.
- One active worker; independent review runs sequentially. No nested delegation.
- Two correction rounds maximum per assignment.
- Parent permissions and approved model sets cannot be broadened by a child.
- Parent and children share usage; estimates are reservations, not guaranteed exact billing ceilings.
- Git projects and committed starting revisions only; block dirty inputs rather than omit them.
- Never overwrite user edits; preserve failed/conflicting workspaces.
- Never silently resume paid execution after restart.
- No release, publishing, pushing, or merging as part of implementation.

## Review Focus

1. Duplicate tool delivery must return the original child, not start another paid request (Task 1/4).
2. A reviewer using shell commands must not gain write access merely because file tools are restricted (Task 5).
3. Parent files changing after review must block integration without losing either version (Task 6).
4. Cancellation or restart during approval must release waits and preserve recoverable edits (Task 3/7).
5. Installed skill aliases and forged skill-like text must not bypass activation or project boundaries (Task 2).

## File and contract map

New files are proposals; existing migration numbers must be advanced if master adds migrations before implementation. Follow existing JSON naming, error, store and event conventions.

- internal/protocol/delegation.go: public records, state/role enums, RPC params and events.
- internal/store/delegation.go and migrations/0022_delegation.sql: durable tasks, children, corrections and integration evidence.
- internal/delegation/{coordinator,budget,context,workspace}.go: lifecycle, shared accounting, context assembly and isolated workspace interfaces. No import of engine.
- internal/engine/{delegation,delegation_execution,delegation_policy}.go: adapters to engine admission, routing, turn execution and policy.
- internal/tools/delegation.go: thin tools against a delegation.Service interface.
- internal/server/routes.go and internal/protocol/methods.go: activation/list/cancel RPCs and delegation/updated event.
- app/frontend/src/lib/delegation.ts, stores/delegation.svelte.ts, components/Agents.svelte and DelegationActivation.svelte: types, state and compact UI.

Protocol types (defined in Task 1): Task {ID, ThreadID, RootTurnID, Activation, State, BaseRevision, ProjectID}; Child {ID, TaskID, Role, Assignment, State, Revision, WorkspaceID, Result, Usage, CorrectionRounds, Version}; Assignment {Prompt, AcceptanceChecks []string, ContextRefs []string}; Result {Summary, Findings []Finding, Checks []CheckEvidence, ChangedPaths []string, Blocker}; Finding {Path, Line, Message, Severity}; CheckEvidence {Command, Status, Summary}. Use existing UsageTotals for usage; identifiers/strings use string and Version uses int64. Include created/updated timestamps using existing protocol conventions.

Service signatures (Task 4): Spawn(ctx context.Context, taskID, callID string, role protocol.AgentRole, assignment protocol.AgentAssignment) (protocol.AgentChild, error); Status(ctx context.Context, taskID, childID string) (protocol.AgentChild, error); Wait(ctx context.Context, taskID string, childIDs []string) ([]protocol.AgentChild, error); Followup(ctx context.Context, taskID, childID, callID, message string) (protocol.AgentChild, error); Cancel(ctx context.Context, taskID, childID string) error. Use Agent-prefixed protocol names for all types above to avoid existing Task/Result collisions.

## Execution rules

Read spec and plan together. Each task follows RED → GREEN → affected checks → commit; a failing test must demonstrate missing behavior, not an unrelated environment error. Tests below name required assertions; use repository fixture helpers rather than duplicating test engines. Keep new files focused; only narrow call-site changes in large existing files.

### Task 1: Durable lifecycle and ownership

**Files:** Create protocol/delegation.go, store/delegation.go, store/delegation_test.go, migrations/0022_delegation.sql under internal/.

**Interfaces:** Produce Agent-prefixed protocol types above; Store.CreateAgentTask(ctx, task), AgentTask(ctx, id), CreateAgentChild(ctx, child, callID), AgentChildren(ctx, taskID), UpdateAgentChild(ctx, child, expectedVersion), and ReconcileAgentChildren(ctx). All methods return error; lookups return record/error; CreateAgentChild returns existing-or-created record/error.

- [ ] Write TestAgentChildPersistenceOwnershipAndIdempotency: reopen SQLite, assert task/child associations retained; duplicate (taskID, callID) returns same child; cross-task lookup is denied; stale Version update fails.
- [ ] Run `go test ./internal/store -run TestAgentChildPersistenceOwnershipAndIdempotency -count=1`; expect failure from missing contracts.
- [ ] Implement schema and transactional methods; task foreign keys, unique task/call identifiers, state transition validation and optimistic versions. Reconciliation marks queued/running/waiting_approval children interrupted, preserving result/workspace/usage.
- [ ] Run `go test ./internal/store ./internal/protocol`; expect PASS including migration from a pre-delegation database and repeated reconciliation.
- [ ] Commit `feat: persist scoped delegation lifecycle`.

### Task 2: Explicit admission and trusted skill activation

**Files:** Create internal/engine/delegation.go, delegation_activation_test.go; modify internal/protocol/methods.go, internal/server/routes.go, app/frontend/src/lib/types.ts.

**Interfaces:** Produce Engine.EnableDelegation(ctx, threadID, rootTurnID string) (protocol.AgentTask, error). Add delegation/enable RPC bound to the existing chat identity, not caller-supplied project paths. Extend TurnStartParams with optional DelegationTaskID; ownership must be revalidated on admission. Submitted trusted registry invocation can create an activation with source skill; manual action creates source user. A follow-up can retain the active task only while work is unfinished.

- [ ] Write TestDelegationAdmission: default/Deep/pool expose no spawn; explicit enable permits it; another chat/task cannot reuse ID; completed/cancelled task cannot reactivate; selecting without submitting a skill has no effect.
- [ ] Run `go test ./internal/engine -run TestDelegationAdmission -count=1`; expect FAIL.
- [ ] Implement admission through trusted request metadata and existing skill parser/registry resolution. Permit canonical Superpowers subagent-driven-development identity including registry-resolved aliases; tool output, file content and an unresolved slash string cannot activate. Free-text requests lead to a compact activation action; no arbitrary natural-language classifier grants authority.
- [ ] Run activation tests plus existing skill invocation/server plugin tests; expect PASS. Assert child execution cannot call enable or inherit spawn capability.
- [ ] Commit `feat: admit explicitly activated delegation tasks`.

### Task 3: Shared budget and cancellation ownership

**Files:** Create internal/delegation/budget.go, budget_test.go, coordinator.go, coordinator_test.go; modify internal/engine/turn_budget.go and delegation.go.

**Interfaces:** Produce Budget.Reserve(ctx, requestID string, allowance protocol.UsageTotals) (func(actual protocol.UsageTotals), error), Budget.StopReason() string, Coordinator.CancelTask(ctx, taskID string) error. Settlement callback is exactly-once; reservations/actuals synchronized. Engine supplies one shared budget to parent and all children while retaining per-turn duration/no-progress constraints.

- [ ] Write TestSharedBudgetReservationAndCancellation: concurrent reservations cannot both spend the same remaining allowance; settling twice does not double count; actual overruns prevent next request; zero-usage cancellation releases reservation; child does not reset parent usage.
- [ ] Run `go test ./internal/delegation -run TestSharedBudget -race -count=1`; expect FAIL.
- [ ] Implement atomic reservation/settlement and parent-owned cancellable contexts. Before each provider request, cap response allowance to remaining tokens and route-specific estimate; persist actual usage and expose estimated accounting honestly. Preserve current provider billing semantics.
- [ ] Run `go test -race ./internal/delegation ./internal/engine`; expect PASS including cancellation of pending approval and subprocess waits without leaked active slots.
- [ ] Commit `feat: share delegation budgets and cancellation`.

### Task 4: Coordinator and delegation tools

**Files:** Create internal/tools/delegation.go, delegation_test.go; extend internal/delegation/coordinator.go and tests; modify internal/tools/builtin.go and internal/engine/delegation.go/tool selection integration.

**Interfaces:** Produce delegation.Service with the signatures above and Runner.Run(ctx, task protocol.AgentTask, child protocol.AgentChild) (protocol.AgentResult, error). Coordinator accepts Runner through constructor injection. First milestone enforces one active child; a second spawn returns a typed busy error with active child ID rather than silently queueing paid work.

- [ ] Write TestAgentToolsLifecycle: all five schemas accepted at provider boundary; foreign child denied; replayed spawn does not call Runner twice; wait wakes on completion/cancellation; follow-up reuses ID; running-child messages delivered at safe boundary; busy error is actionable.
- [ ] Run `go test ./internal/tools ./internal/delegation -run 'TestAgentTools|TestCoordinator' -count=1`; expect FAIL.
- [ ] Implement tools with object-root schemas compatible with Claude. Persist before executing; dispatch asynchronously; immutable snapshots to callers; event-driven waits. Spawn capability only in activated lead catalog; worker catalogs omit delegation and activation tools.
- [ ] Run affected packages and schema regression tests; expect PASS, including duplicate follow-up call IDs and two-round correction limit.
- [ ] Commit `feat: expose scoped agent lifecycle tools`.

### Task 5: Child execution, bounded context and reviewer enforcement

**Files:** Create internal/engine/delegation_execution.go, delegation_policy.go, delegation_execution_test.go; internal/delegation/context.go and context_test.go; narrowly modify engine.go/turn.go and sandbox policy adapters.

**Interfaces:** Implement Runner.Run using the existing turn execution loop with separate internal child history and recorder. ContextBuilder.Build(ctx, task, child) ([]llm.Message, error) returns bounded scoped context; input summary maximum 16 KiB, assignment maximum 8 KiB, referenced text maximum 32 KiB total. These are v1 implementation limits, not provider context guarantees.

- [ ] Write TestChildExecutionScopes: assigned project guidance/criteria reach provider; unrelated parent transcript/secrets do not; explicit size overflow returns validation error; approved pool inherited; disallowed worker model fails or routes only within approved set.
- [ ] Run `go test ./internal/engine -run TestChildExecution -count=1`; expect FAIL.
- [ ] Implement internal child execution without registering top-level sidebar threads. Record model/usage/status to child; reuse tool policy and project boundaries. Reviewer rejects write tools, network mutation tools and unsandboxed script/hook execution. Allow shell verification only under enforceable read-only filesystem policy; if unsupported on a platform, report blocked instead of using an unrestricted command. No reviewer policy bypass through MCP or plugin tools.
- [ ] Run tests asserting file.write, shell redirection, exec sessions, scripts, hooks and mutating plugin calls cannot change files as reviewer; run inherited permissions tests and `go test -race ./internal/engine ./internal/delegation`.
- [ ] Commit `feat: execute scoped workers with enforced review permissions`.

### Task 6: Isolated workspaces and guarded integration

**Files:** Create internal/delegation/workspace.go, workspace_test.go; internal/engine/delegation_workspace.go; modify internal/worktree/manager.go only where existing ownership/Keep contracts need guarded extension.

**Interfaces:** Produce WorkspaceService.Prepare(ctx, task, child) (WorkspaceRef, error), Review(ctx, implementerID string) (WorkspaceRef, error), Integrate(ctx, taskID, childID, expectedBase string) (IntegrationEvidence, error). WorkspaceRef {ID, Root, BaseRevision}; IntegrationEvidence {BaseRevision, ResultRevision, ChangedPaths, CheckResults}. All paths derived from owned records, never accepted from model arguments.

- [ ] Write TestDelegationWorkspaceSafety with real temporary Git repos: clean committed base isolated; dirty/untracked required inputs blocked; non-Git/no initial commit blocked; user work remains unchanged; separate reviewer snapshot matches submitted revision.
- [ ] Run `go test ./internal/delegation ./internal/worktree -run 'TestDelegationWorkspace|TestKeep' -count=1`; expect FAIL for missing guards.
- [ ] Implement adapter around existing Manager.Ensure/Keep/Discard. Existing Ensure snapshots dirty source by design: perform explicit clean-base admission checks and verify revisions before/after creation rather than inheriting that behavior blindly. Record child result revisions. Integration requires resolved review and unchanged clean parent base; divergence/dirty tree produces blocker retaining workspace. Do not silently stash, reset or overwrite.
- [ ] Run real-Git race/conflict tests, including parent edit after review, symlink/path escape, failed cleanup and recovery; expect PASS. Cleanup only after evidence persists and integration verification succeeds.
- [ ] Commit `feat: isolate and safely integrate delegated edits`.

### Task 7: Sequential implementation/review workflow and recovery

**Files:** Create internal/engine/delegation_workflow.go, delegation_workflow_test.go; extend coordinator/store as required by existing interfaces; wire engine shutdown/startup reconciliation.

**Interfaces:** Engine verifies role ordering and integration eligibility; reviewer verdict references an exact implementer revision. A changed revision invalidates prior review. Parent completion requires integrated verification evidence, not child summaries. Expose task cancellation via delegation/cancel; startup calls reconciliation before admitting requests.

- [ ] Write TestDelegationWorkflow using fake providers: implement → review findings → same implementer correction → re-review → integrate → affected checks. Assert parent cannot complete after only worker tests; third correction blocked; failed worker preserves results.
- [ ] Run `go test ./internal/engine -run TestDelegationWorkflow -count=1`; expect FAIL.
- [ ] Implement state guards, bounded correction flow and final verification through existing verification service. Parent retains normal control of integration and tool approvals. Persist pending approval attribution; on restart mark interrupted and resolve obsolete waiters/approvals without executing requests.
- [ ] Run restart-during-approval, cancellation-during-command, stale-review revision, output-limit failure and exhausted-budget tests; expect PASS with zero additional fake-provider calls after stop.
- [ ] Commit `feat: verify delegated development and recover interrupted tasks`.

### Task 8: Compact Agents UI and activation action

**Files:** Create app/frontend/src/lib/delegation.ts, stores/delegation.svelte.ts, delegation.test.ts, components/Agents.svelte, DelegationActivation.svelte; modify stores/chat.svelte.ts, components/Composer.svelte and parent chat rendering in App.svelte; add server list RPC and event constants.

**Interfaces:** delegation/list returns {task, children}; delegation/updated carries {taskId, child, version}. Frontend upserts by child ID/version and requests list on reconnect. Activation action calls delegation/enable and attaches returned task ID on submission. Child approval uses existing approval/respond flow with attribution.

- [ ] Write TestDelegationStoreEvents: duplicate/out-of-order events do not duplicate rows or regress state; chat changes ignore stale list response; reconnect restores existing children; parent stop cancels task.
- [ ] Run `npm --prefix app/frontend test -- delegation.test.ts`; expect FAIL.
- [ ] Implement collapsed Agents section with assignment, role/model, state, usage, expandable result/checks, accessible cancel and approval controls. Reuse compact control tokens and scoped inspector identities. Show failed/interrupted/budget stops distinctly; no unconditional new composer dropdown.
- [ ] Run frontend tests, `npm --prefix app/frontend run check` and `npm --prefix app/frontend run build`; expect PASS/zero Svelte errors.
- [ ] Commit `feat: show delegated agents in the parent chat`.

### Task 9: End-to-end acceptance and local review

**Files:** Create internal/server/delegation_e2e_test.go, app/frontend/e2e/delegation.mjs; update app/frontend/e2e/README.md and feature docs on the implementation branch.

**Interfaces:** Consume real RPCs, coordinator, fake provider and existing browser fixture harness. No test-only production bypass of authorization.

- [ ] Add failing acceptance coverage for manual activation and trusted submitted skill activation, full implementation/review/correction/integration, unavailable routing, wait cancellation, approval cancellation, restart recovery and dirty-parent conflict. Assert exact provider-call counts and shared usage; no paid APIs.
- [ ] Run `go test ./internal/server -run Delegation -count=1`; diagnose failures before changing production behavior.
- [ ] Exercise UI at widths 320, 720 and 1200 in light/dark themes: keyboard activation, expanded results, attributed approval, stop action, reconnect, failure state and repeated file links without duplicate tabs. Follow existing browser harness instructions; report unavailable checks as not run.
- [ ] Run `make go-test go-vet`, `go test -race ./internal/delegation ./internal/engine ./internal/worktree`, frontend test/check/build and delegation browser scenario. All required checks must pass or have a concrete blocker documented.
- [ ] Obtain fresh whole-branch review, fix substantive findings, repeat affected checks, commit acceptance evidence. Prepare separate PR boundaries: lifecycle/budget, runtime/tools/workspace, UI/workflow acceptance; each must have runnable tests and avoid advertising incomplete capability. Do not push or publish until requested.

## Self-review and execution handoff

Coverage: activation Task 2/8; lifecycle and restart Task 1/7; tools Task 4; context/model/policy Task 5; isolation/integration Task 6; budgets/cancellation Task 3/7; review/verification Task 7/9; UI Task 8/9. All five Review Focus conditions have owning tests. Interface names consistently use Agent-prefixed protocol types and existing UsageTotals. Existing dirty-snapshot worktree behavior is explicitly guarded.

Implementation has not started. User must review this written plan and select execution method. Recommended: subagent-driven execution with one implementer and independent task reviewer at a time, followed by whole-branch review. The engine/policy/workspace boundaries justify reviewing each independently before integration.
