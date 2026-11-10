# Prompt-First Agent Workspaces Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A user asks for agents in one message, approves one plan, and can use isolated agents when their Git checkout already contains edits.

**Architecture:** An ordinary lead turn can submit an inert plan card and end. Approval atomically creates an admitted task and queues its continuation. A private task checkout snapshots the selected project's visible state; agents integrate there. A journaled merge applies only accepted differences back to the selected checkout after per-path conflict checks.

**Tech Stack:** Go, SQLite migrations, Git plumbing, Svelte/TypeScript, Go and Vitest tests, browser fixtures.

**Spec:** `docs/superpowers/specs/2026-11-04-prompt-first-agent-workspaces-design.md`

## Global Constraints

- Before plan approval, no agent may run and no agent capability is granted.
- Do not silently commit, stash, delete or restage the user's files.
- Existing active tasks retain their original parent-root semantics.
- The user's project index bytes remain unchanged by capture and merge.
- A merge conflict or ambiguous crash leaves the private verified result available; recovery never reapplies it automatically.

## Review Focus

- User edits a candidate file while agents run: the merge refuses that path without overwriting it (Task 4).
- Plan approval is delivered twice or crashes after persistence: one task and one continuation result (Task 3).
- Snapshot source changes during capture: approval fails without granting authority (Task 2).
- Untracked `UMCODE.md` and staged content are included in the agent snapshot while original staging remains intact (Task 2).
- A symlink or case alias appears at a merge target: merge refuses before mutation (Task 4).

---

### Task 1: Inert plan proposal from an ordinary turn

**Files:**
- Modify: `internal/engine/agentrt_plan.go`, `internal/engine/turn.go`, `internal/engine/tool_selection_runtime.go`, `internal/tools/agentrt.go`, `internal/store/agentrt.go`
- Test: `internal/engine/agentrt_activation_test.go`, `internal/engine/agentrt_plan_test.go`, `internal/store/agentrt_test.go`

**Interfaces:**
- Produces: `Engine.proposeUnadmittedAgentPlan(ctx, proposal)` and a durable proposed plan with an empty `task_id`, bound to the proposing turn. The tool returns a stop result and grants nothing.
- Consumes: existing plan validation and the plan card renderer; current admitted-task proposals keep working.

- [ ] Add failing tests: an ordinary lead can propose a plan with no token; spawn and integration fail; the proposal ends the turn; repeat proposals in one turn are refused; existing admitted-task plan revisions still work.
- [ ] Run `go test ./internal/engine ./internal/store -run 'Test.*UnadmittedPlan' -count=1`; expect the new tests to fail.
- [ ] Implement the unadmitted proposal path and persisted card, with no task or approval response yet. Return a stop result immediately; keep the current admitted proposal path.
- [ ] Run focused and full engine/store tests; commit.

### Task 2: Private snapshot of an edited checkout

**Files:**
- Create: `internal/worktree/runtime_snapshot.go`, `internal/worktree/runtime_snapshot_test.go`
- Create: `internal/store/migrations/0039_task_workspaces.sql`, `internal/store/agent_task_workspace.go`, `internal/store/agent_task_workspace_test.go`
- Modify: `internal/delegation/runtime_workspaces.go`, `internal/engine/delegation.go`

**Interfaces:**
- Produces: `worktree.CaptureTaskSnapshot(ctx context.Context, source, destination string) (TaskSnapshot, error)`; `TaskSnapshot` includes source HEAD, private base commit, source manifest and untracked path list. `Store.AgentTaskWorkspace(ctx, taskID)` returns the persisted source/execution roots and manifest.
- Consumes: a selected Git project root; Task 3 will bind this snapshot to an approved task.

- [ ] Add failing tests for staged, unstaged, deleted and untracked inputs; source/index bytes unchanged; ignored inputs excluded; changes during capture rejected; unsafe links, gitlinks, conflict stages and special files refused.
- [ ] Run `go test ./internal/worktree ./internal/store -run 'Test.*(TaskSnapshot|TaskWorkspace)' -count=1`; expect failures.
- [ ] Implement a self-contained private checkout outside the selected project, copy the visible source state without hooks or filters, commit only there, capture a manifest before and after, and reject source drift.
- [ ] Persist snapshot identity and ownership in migration 0039. Add orphan cleanup for prepared snapshots not bound to any task.
- [ ] Run focused and full `internal/worktree` and `internal/store` suites; commit.

### Task 3: Atomic plan approval and distinct execution authority

**Files:**
- Modify: `internal/store/agentrt.go`, `internal/store/delegation.go`, `internal/protocol/delegation.go`, `internal/protocol/methods.go`, `internal/server/routes.go`
- Modify: `internal/engine/agentrt_plan.go`, `internal/engine/delegation_workspace.go`, `internal/engine/delegation.go`, `internal/engine/agentrt_runner.go`, `internal/engine/turn.go`, `internal/delegation/runtime_workspaces.go`
- Test: `internal/engine/agentrt_wiring_test.go`, `internal/engine/agentrt_runner_test.go`, `internal/delegation/runtime_views_test.go`, `internal/store/agentrt_test.go`, `internal/server/agent_plan_test.go`

**Interfaces:**
- Consumes: Task 1's inert plan and Task 2's private snapshot/workspace record.
- Produces: `Store.ApproveAgentPlanWithTask(ctx, planID, task, workspace)` as one transaction, `AgentActivationPlanApproval`, a plan response carrying `taskId`, and admitted lead/agents executing against the private parent while ownership/policy stay bound to the selected project.

- [ ] Add failing tests for approval replay and restart, source drift before approval, task/plan atomicity, dirty inputs visible to agents, lead verification on the private head, no tool access to the original checkout, and old tasks retaining their recorded parent root.
- [ ] Run `go test ./internal/engine ./internal/delegation ./internal/store ./internal/server -run 'Test.*(PlanApprovalAdmission|TaskParentAuthority|DirtyRuntimeView)' -count=1`; expect failures.
- [ ] Capture the Task 2 snapshot before the database transaction. Insert the task, workspace identity and approved plan atomically; clean orphan snapshots on restart. Replayed approval returns the same task ID. A changed source manifest refuses approval.
- [ ] Split selected-project root and private execution root checks in admission and `authorizeDelegationTool`. Route admitted lead file/shell tools to the task parent; retain sandbox and capability clamps.
- [ ] Run focused and full engine/delegation/store/server tests; commit.

### Task 4: Conflict-checked merge and recovery

**Files:**
- Create: `internal/worktree/runtime_merge.go`, `internal/worktree/runtime_merge_test.go`
- Create: `internal/store/migrations/0040_task_merge_journal.sql`, `internal/store/agent_task_merge.go`, `internal/store/agent_task_merge_test.go`
- Modify: `internal/engine/agentrt_integrate.go`, `internal/delegation/runtime_integration.go`, `internal/engine/engine.go`
- Test: `internal/engine/agentrt_integrate_test.go`, `internal/engine/agentrt_recovery_test.go`, `internal/delegation/runtime_integration_test.go`

**Interfaces:**
- Consumes: Task 2's snapshot manifest and Task 3's private integrated head.
- Produces: `worktree.MergeTaskResult(ctx, source, snapshot, head) (MergeReceipt, error)` and durable merge intent/outcome methods. Completion occurs only after receipt recording.

- [ ] Add failing tests for unrelated dirty edits, target-path edits, moved HEAD, untracked collision, aliases/symlinks, index-byte preservation, mid-apply failure, retry, and pre/post/ambiguous crash states.
- [ ] Run `go test ./internal/worktree ./internal/store ./internal/engine -run 'Test.*(TaskMerge|MergeRecovery)' -count=1`; expect failures.
- [ ] Implement pre/post path hashes, preflight and immediate recheck, bounded backup/rollback, and worktree-only writes. Journal intent before mutation and receipt after verification; never auto-reapply on recovery.
- [ ] Reorder `agent.integrate complete`: verified private head, journal/merge, receipt, terminal task state, then cleanup. A conflict leaves the task active and retryable.
- [ ] Run focused and full worktree/store/engine/delegation suites; commit.

### Task 5: Remove activation UI and finish the one-prompt flow

**Files:**
- Delete: `app/frontend/src/lib/components/AgentsMenu.svelte`, `app/frontend/src/lib/components/AgentSuggestion.svelte`
- Modify: `app/frontend/src/lib/components/Composer.svelte`, `app/frontend/src/lib/components/ItemView.svelte`, `app/frontend/src/lib/stores/chat.svelte.ts`, `app/frontend/src/lib/stores/delegation.svelte.ts`, `app/frontend/src/lib/stores/agentCards.svelte.ts`
- Modify: `internal/engine/agentrt_activation.go`, `internal/engine/agentrt_wiring.go`, `internal/server/routes.go`, `docs/subagent-development.md`
- Test: `app/frontend/src/lib/delegation.test.ts`, `app/frontend/src/lib/components/AgentPlan.test.ts`, `app/frontend/e2e/delegation.mjs`, `internal/server/agent_plan_test.go`

**Interfaces:**
- Consumes: Task 1's plan response task ID and Task 4's merge status.
- Produces: one prompt, one plan card, one approval, automatic single continuation, visible conflict/retry state, and no activation token or suggestion menu.

- [ ] Add failing frontend/server tests for prompt-to-plan approval, exactly one continuation, revise/decline/timeout, reconnect after approved plan, and dirty merge status.
- [ ] Run the focused Vitest/server/browser cases; expect failures.
- [ ] Remove the activation menu, suggestion card/tool, token RPC and related state. On approval queue the task-ID continuation once; on reconnect show Continue for an approved task lacking a continuation.
- [ ] Rewrite current docs and browser fixture around prompt-first agents and dirty checkout behavior.
- [ ] Run `go build ./...`, `go vet ./...`, all Go package tests, `npm test`, `npm run check`, `npm run build`, browser acceptance and macOS sandbox tests; commit and review the whole branch.
