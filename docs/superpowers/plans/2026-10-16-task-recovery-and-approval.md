# Task Recovery and Engine Approvals Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Resume authorized unfinished tasks reliably while preserving engine-controlled permissions, explicit workflow approvals, and evidence-based outcomes.

**Architecture:** Add revision-checked task, operation, and requirement records beside existing work graphs and result items. Integrate them through one engine authority path; inferred planning depth becomes advisory, while explicit requirements and existing action policy remain enforced. Resume from persisted task context rather than message/path exceptions or automatic command replay.

**Tech Stack:** Go, SQLite through the existing store, JSON protocol, Svelte/TypeScript, Go tests and Vitest. No new dependencies or paid provider tests.

**Spec:** `docs/superpowers/specs/2026-10-16-task-recovery-and-approval-design.md`

## Global Constraints

- The turn engine remains the authority for permissions, approvals, execution, and recorded results.
- It does not grant new tool permissions or automatically approve actions.
- Existing approvals, tool guards, project containment, hooks, scoped tool selection, revision checks, child authority, and result items are retained.
- No approval mode default is changed here.
- Rejection never becomes approval through retry, a different model, or another tool.
- Do not blindly replay saved mutations.
- Migrate additively without rewriting live files or deleting graph/approval history.
- No paid live-model tests are required to establish deterministic correctness.
- Leave the unrelated `TASKS.md` modification untouched; stage exact task files only. Work from a dedicated branch/worktree at execution time, retaining commit `64eef99` and the approved spec.

## Review Focus

- A multi-action turn partially succeeds before disconnection: retain completed effects and inspect uncertain effects before any retry (Tasks 1, 5, 6).
- An approval expires or permissions narrow while the dialog is open: revalidate and execute nothing outside current authority (Task 4).
- A model cites an unrelated user message, skill output, or another project's approval: no authority is gained from that citation (Tasks 2, 3).
- Several unfinished actions exist when the user says retry: ask one clarification instead of choosing a destructive replay (Task 5).
- The same blocker arrives through message updates, turn error, and history reload: show one primary alert while preserving distinct errors (Task 7).

## File ownership and interfaces

Keep new responsibilities in focused files; modify large `turn.go` only at integration points.
All protocol definitions below live in `internal/protocol/task.go`. IDs are strings,
revisions are integers, timestamps follow existing protocol conventions, and structured
JSON fields use `json.RawMessage`.

- `Task`: ID, ThreadID, WorkID, ProjectID, OriginItemID, Revision, Objective,
  Scope (JSON), Phase, Status, ContinuationItemID, CreatedAt, UpdatedAt.
- `TaskOperation`: ID, TaskID, TurnID, ItemID, Revision, Tool, Args (JSON),
  ImplementationID, Resources (JSON), Status, VerificationStatus, ResultItemID,
  EvidenceIDs ([]string), SupersedesID, Failure (*ActionFailure).
- `TaskRequirement`: ID, TaskID, Revision, Origin (`user`, `policy`, `legacy`),
  OriginRef, Scope (JSON), Condition (JSON), Status, ApprovalID.
- `ActionFailure`: Category, OperationID, BlockerID, Message, Retryable,
  NextStep, State (JSON containing relevant revision/status predicates).
- `TaskSnapshot`: Task, Operations ([]TaskOperation), Requirements ([]TaskRequirement).
- `TaskTransition`: ExpectedRevision, Status, Phase, ContinuationItemID.
- `OperationTransition`: ExpectedRevision, Status, VerificationStatus,
  ResultItemID, EvidenceIDs, SupersedesID, Failure.
- `TaskResolution`: Relation (`new`, `continue`, `clarify`), TaskID, UserItemID,
  Question. It conveys no permission grant.
- `RequirementProposal`: TaskID, Origin, OriginRef, Scope, Condition, ExpectedRevision.
- `AuthorityDecision`: Outcome (`allow`, `ask`, `deny`, `blocked`), Failure,
  RequirementIDs, ApprovalIDs.

Engine-only state-changing functions are not exposed as unrestricted model tools.
Reuse existing work-update tools and approval transport; models may propose intent,
requirements, and verification through a bounded engine-handled `task.propose` tool
defined in Task 2. Tool output is never an approval credential.

### Task 1: Durable task and operation ledger

**Files:** Create `internal/protocol/task.go`, `internal/store/migrations/0030_task_recovery.sql`, `internal/store/task.go`, `internal/store/task_test.go`.

**Interfaces:** Store methods `CreateTask(ctx context.Context, task protocol.Task) (protocol.Task, error)`, `ActiveTask(ctx context.Context, threadID string) (protocol.TaskSnapshot, error)`, `TransitionTask(ctx context.Context, id string, change protocol.TaskTransition) (protocol.Task, error)`, `CreateTaskOperation(ctx context.Context, op protocol.TaskOperation) (protocol.TaskOperation, error)`, `TransitionTaskOperation(ctx context.Context, id string, change protocol.OperationTransition) (protocol.TaskOperation, error)`. Return `store.ErrNotFound` for absent current task and add `store.ErrTaskConflict` for stale revisions.

- [ ] Write `TestTaskLedger` table tests: one current task per thread; all specified task/operation/verification statuses round-trip; stale transitions change zero rows; a new task supersedes the old atomically; unrelated thread/project IDs are rejected; closing/reopening the DB retains state. Assert old work, item, and approval rows remain unchanged.
- [ ] Run `go test ./internal/store -run '^TestTaskLedger$' -count=1`; confirm RED before implementation.
- [ ] Implement additive task, operation, and requirement tables with foreign keys, current-task uniqueness, bounded JSON fields using the existing work-update byte limit, transactional compare-and-swap transitions, and operation/turn/item ownership validation. Evidence references must belong to the same task/thread or its authorized delegated lineage. Use engine-generated IDs.
- [ ] Add `TestTaskLedgerInterruptedOperation`: persist one succeeded and one running operation, reopen DB, and assert succeeded remains succeeded and running remains distinguishable from success. Task 5 tests the engine recovery transition to outcome_unknown; the store must never replay operations.
- [ ] Run `go test ./internal/store ./internal/protocol -count=1`; confirm GREEN and commit `feat: persist revision-checked task recovery state`.

### Task 2: Resolve task continuity and expose bounded task context

**Files:** Create `internal/engine/task_context.go`, `internal/engine/task_context_test.go`, `internal/engine/task_propose.go`; modify `internal/engine/turn.go`, `internal/engine/context.go`, `internal/engine/workflow_context.go`, `internal/engine/delegation_tools.go` where selected-tool registration is assembled.

**Interfaces:** Engine methods `resolveTask(ctx context.Context, th protocol.Thread, user protocol.Item) (protocol.TaskResolution, error)`, `taskContext(ctx context.Context, threadID string) (string, error)`, `applyTaskProposal(ctx context.Context, turn protocol.Turn, args json.RawMessage) (string, error)`; Task 1 supplies persistence. The `task.propose` engine tool accepts exactly `{task_id, expected_revision, relation?, user_item_id?, requirement?, verification?}`; each optional payload is validated in the owning task below. It cannot set operation outcome, approve anything, or update permission policy.

- [ ] Write `TestTaskContinuity`: new objective saves its originating user item; retry/continue/resume with an optional slash-skill prefix references the active task; a changed model retains task identity; new objective supersedes without approval transfer. Assert no active task plus “retry” returns clarify. Use fixed fixtures, not a live model.
- [ ] Run `go test ./internal/engine -run '^TestTaskContinuity$' -count=1`; confirm RED.
- [ ] Implement deterministic handling of explicit continuation forms. For ordinary follow-up messages retain current context provisionally; the model may propose a new/continued task using the current user item only. Reject references to older, assistant, tool-output, or other-thread items as new authority. Ambiguous destructive scope changes produce clarification before actions. Task context is a separate bounded prompt layer, rebuilt from the store after compaction, not approval text copied from historical summaries.
- [ ] Test `TestTaskProposalCannotGrantAuthority`: fabricated origins, a quoted skill response, unrelated approval IDs, and `task.propose` fields claiming completion or policy changes are rejected. Test stale proposals refresh state and no mutation runs while continuity is unresolved.
- [ ] Run `go test ./internal/engine -run '^TestTask(Continuity|ProposalCannotGrantAuthority)$' -count=1`; confirm GREEN and commit `feat: preserve active task context across turns`.

### Task 3: Explicit requirements and compatible legacy work

**Files:** Create `internal/store/task_requirements.go`, `internal/store/task_requirements_test.go`, `internal/engine/task_requirements.go`, `internal/engine/task_requirements_test.go`; modify `internal/work/rules.go`, `internal/work/work.go`, `internal/engine/workflow.go`, `internal/engine/task_propose.go`.

**Interfaces:** Store methods `PutTaskRequirement(ctx context.Context, proposal protocol.RequirementProposal) (protocol.TaskRequirement, error)`, `TaskRequirements(ctx context.Context, taskID string) ([]protocol.TaskRequirement, error)`; engine methods `validateTaskRequirement(ctx context.Context, th protocol.Thread, proposal protocol.RequirementProposal) (protocol.TaskRequirement, error)`, `recoverLegacyTask(ctx context.Context, th protocol.Thread, user protocol.Item) (protocol.TaskSnapshot, error)`.

- [ ] Write `TestExplicitTaskRequirements`: inferred depth alone is advisory; a recorded planning-only instruction applies to implementation; a real configured requirement is enforced; model proposals cannot approve or remove requirements. Scope explicit requirements to task/resources/phases rather than the entire chat.
- [ ] Run `go test ./internal/engine ./internal/store -run '^TestExplicitTaskRequirements$|^TestLegacyTaskRecovery$' -count=1`; confirm RED.
- [ ] Persist provenance only from engine-validated current user proposals or configured trusted policy. User-imposed constraints take effect conservatively; removing or broadening them requires a concrete user confirmation through existing approval transport. Registering an ambiguous interpretation requires confirmation before affected actions. No keyword match alone can grant authority. Inferred depth remains available to planning/context but stops producing implicit human gates once Task 4 integrates requirements.
- [ ] Add `TestLegacyTaskRecovery`: inferred-depth-only graph receives advisory status; existing pending/rejected approval retains its scope; unknown legacy provenance requires one explicit resolution, retained across restart. No graph deletion, auto-approval, file writes, or action replay occurs during migration. Recover lazily on the next user interaction.
- [ ] Run `go test ./internal/store ./internal/work ./internal/engine -run 'TestExplicitTaskRequirements|TestLegacyTaskRecovery|Test.*Provenance|Test.*Graph' -count=1`; confirm GREEN and commit `feat: record explicit workflow requirements and provenance`.

### Task 4: One action authority path

**Files:** Create `internal/engine/task_authority.go`, `internal/engine/task_authority_test.go`; modify `internal/engine/turn.go`, `internal/engine/workflow.go`, `internal/engine/approvals.go`, `internal/engine/workflow_recovery.go`, `internal/engine/workflow_recovery_test.go`.

**Interfaces:** Engine method `checkTaskAuthority(ctx context.Context, th protocol.Thread, task protocol.TaskSnapshot, operation protocol.TaskOperation) (protocol.AuthorityDecision, error)`. Consume requirements from Task 3, operation identity from Task 1, and existing policy/approval logic. `task.propose` conveys no alternate execution path.

- [ ] Write `TestTaskAuthority`: arbitrary-path file edits and permitted shell/plugin operations run without graph approval; explicit requirement blocks only its scope; denied tool/scope/readonly/guard/hook still denies; approvals accept/reject/expire without bypass. Use real temp projects and scripted providers, including the original Goodreads scenario with a bare retry.
- [ ] Run `go test ./internal/engine -run '^TestTaskAuthority$' -count=1`; confirm RED on the current implicit gate behavior.
- [ ] Integrate the authority decision in `runTool` while preserving existing guard/hook semantics, scoped selection, approval transport, and child restrictions. Persist operation before execution. Revalidate current authority after any approval wait, including ordinary parent turns. Replace `requestedSpecificationEdit`, its regexes, and current-request context key with the generic requirement check. Do not remove unrelated typed-blocker or completion protection.
- [ ] Add `TestTaskAuthorityChangedWhileWaiting`: narrow policy, switch project/scope, expire approval, or replace selected implementation during wait; assert zero unauthorized effects. Assert switching file.edit to shell cannot bypass a still-applicable requirement or rejection. Opaque shell effects require sufficient scoped authority or clarification; never guess they are harmless.
- [ ] Run `go test ./internal/engine -run 'TestTaskAuthority|Test.*Approval|Test.*Workflow|Test.*Policy' -count=1 -timeout=8m`; update obsolete workflow fixture expectations only when inferred gates intentionally become advisory, preserving explicit-gate tests. Commit `fix: separate action permissions from inferred workflow depth`.

### Task 5: Typed recovery without blind replay

**Files:** Create `internal/engine/task_recovery.go`, `internal/engine/task_recovery_test.go`; modify `internal/engine/task_context.go`, `internal/engine/workflow_recovery.go`, `internal/tools/work.go`, `internal/tools/work_test.go`.

**Interfaces:** Engine methods `taskRecoveryContext(ctx context.Context, snapshot protocol.TaskSnapshot) (string, error)`, `classifyActionFailure(operationID string, err error) protocol.ActionFailure`, `recoverInterruptedTask(ctx context.Context, taskID string) error`. Store implements interruption transition through Task 1 interfaces. No model-callable replay function.

- [ ] Write `TestTaskRecovery` cases for every failure category in the spec: stale_state returns affected work/node revisions and predicates; edit_conflict requests reread; missing_resource requests location check; approval_rejected and policy_denied stop; execution_failed reports failure; verification_failed preserves mutation evidence; outcome_unknown requires inspection.
- [ ] Run `go test ./internal/engine ./internal/tools -run '^TestTaskRecovery$' -count=1`; confirm RED.
- [ ] Implement bounded recovery context with the latest resource and permission state. Use three failed equivalent attempts per unchanged cause within a turn as the maximum recovery budget; unchanged permission/approval blockers stop immediately on continuation. Reuse existing typed error sources where available; do not misclassify provider failures as tool failures. Stale `work.update` failures expose trusted revisions rather than suggesting fabricated completion.
- [ ] Add `TestTaskRetryDoesNotReplay`: after restart one action succeeded and another has uncertain outcome; no shell/file/external call runs until inspected. Add `TestTaskAmbiguousRetry`: several unrelated pending objectives return exactly one clarification. Assert resource reread occurs after a matching failure and no unrelated successful action clears a blocker.
- [ ] Run `go test ./internal/engine ./internal/tools -run 'TestTaskRecovery|TestTaskRetryDoesNotReplay|TestTaskAmbiguousRetry|TestWorkUpdate' -count=1`; confirm GREEN and commit `fix: recover failed actions according to their cause`.

### Task 6: Evidence-based outcomes and delegated continuity

**Files:** Create `internal/engine/task_outcomes.go`, `internal/engine/task_outcomes_test.go`; modify `internal/engine/turn.go`, `internal/engine/task_propose.go`, `internal/engine/delegation_execution.go`, `internal/engine/delegation_policy.go`; extend `internal/engine/delegation_manual_compaction_test.go`.

**Interfaces:** Engine methods `recordTaskOutcome(ctx context.Context, operationID string, result protocol.Item) error`, `reconcileTaskOutcome(ctx context.Context, taskID string) (string, error)`, `verifyTaskOperation(ctx context.Context, operationID string, evidenceIDs []string, supersedesID string) error`. The verification proposal payload is `{operation_id, evidence_ids, supersedes_id?}`; it references actual recorded tool results, never arbitrary model text.

- [ ] Write `TestTaskOutcomes`: successful write plus valid readback supports verified completion; write without verification is unverified; failed readback is verification_failed; verification denial reports action ran but check blocked; partial success survives final failure; false model summaries are replaced by supported outcomes. Assertions include actual temp-file bytes and persisted items.
- [ ] Run `go test ./internal/engine -run '^TestTaskOutcomes$' -count=1`; confirm RED.
- [ ] Record actual invocation/result identity and verification relationships. Verify readback resources and expected outcome from the original operation; shell evidence must contain a successful check relevant to the outcome. Require clarification when the requested outcome cannot be established. A changed payload/tool can supersede an old operation only within the same authorized outcome and with supporting verification; unrelated operations cannot. Keep significant partial effects visible.
- [ ] Add `TestDelegatedTaskRecovery`: readonly child cannot mutate, child cannot approve itself/remove parent requirements, revoked parent permissions remain revoked after restart/manual compaction, changed provider preserves restrictions. Completed operations remain recorded; no automatic child restart or duplicate external action occurs.
- [ ] Run `go test ./internal/engine -run 'TestTaskOutcomes|TestDelegatedTaskRecovery|TestDelegation.*(Policy|Compaction|Authority)' -count=1 -timeout=8m`; confirm GREEN and commit `fix: derive task outcomes from execution evidence`.

### Task 7: One blocker with inspectable details

**Files:** Modify `internal/protocol/types.go`, `app/frontend/src/lib/types.ts`, `app/frontend/src/lib/components/ChatColumn.svelte`, `app/frontend/src/lib/components/ChatError.svelte`, `app/frontend/src/lib/chatError.ts`, `app/frontend/src/lib/chatError.test.ts`; create `app/frontend/src/lib/taskOutcome.ts`, `app/frontend/src/lib/taskOutcome.test.ts`.

**Interfaces:** Add optional `actionFailure` to result/error item data using protocol `ActionFailure`. Export `primaryTaskFailure(items: Item[], turnError: string): ActionFailure | null` from `taskOutcome.ts`; old text-only error formatting remains supported. Use operation/blocker IDs for deduplication, not message text alone.

- [ ] Write Vitest `task blocker presentation`: a failed summary, error item, and turn error for the same blocker yield one primary alert; different failures remain inspectable; streaming replacement by item ID and historical reload produce the same result; legacy string errors still display; successful edit with blocked verification says exactly that.
- [ ] Run `npm --prefix app/frontend test -- taskOutcome chatError`; confirm RED.
- [ ] Implement optional structured failure rendering and deduplication only for correlated blockers. Keep original tool-result audit entries in the work log and technical details; do not hide unrelated provider errors or delete historical items. Display a concise reason and next step without exposing revision bookkeeping in the primary message.
- [ ] Run `npm --prefix app/frontend test -- taskOutcome chatError` and `npm --prefix app/frontend run check`; confirm GREEN and commit `fix: present one actionable task blocker in chat`.

### Task 8: End-to-end acceptance, review, and local build

**Files:** Create `internal/server/task_recovery_e2e_test.go`; update `internal/server/work_e2e_test.go`, `internal/engine/optimization_turn_test.go`, and remaining affected tests for intentionally changed advisory-depth semantics; create `docs/task-recovery.md` with the final behavior and local testing procedure.

**Interfaces:** Public turn/item/approval flows only; no fixture may directly clear permissions to manufacture acceptance. Fixture setup may create historical graphs and approvals before the tested interaction.

- [ ] Write `TestTaskRecoveryEndToEnd`: user asks for a change, inferred designed work exists, next turn says `/superpowers:using-superpowers retry`, provider proposes file.write, engine executes, provider reads back, task completes with evidence. Repeat with ordinary source/document paths and alternative provider identity. A parallel table of explicit requirements and action denials must instead retain blocked/rejected status and unchanged bytes.
- [ ] Run `go test ./internal/server -run '^TestTaskRecoveryEndToEnd$' -count=1`; confirm RED before finishing missing integration and GREEN afterward. Add reopen-store and outcome_unknown subcases through public flows.
- [ ] Run `go test ./... -count=1 -timeout=15m`, `go vet ./...`, and focused `go test -race ./internal/engine ./internal/store -run 'TestTask|TestDelegatedTaskRecovery' -count=1 -timeout=10m`; inspect every failure. Then run frontend tests, check, and build. Do not weaken explicit approval tests to make advisory-depth changes pass.
- [ ] Obtain independent whole-branch review after checks; resolve concrete findings and rerun only affected checks. Commit exact acceptance/documentation files as `test: verify generic task recovery and approval boundaries`.
- [ ] Build a separate local bundle using `UMCODE_APP_OUT="$PWD/bin/UMCode-local-task-recovery.app" GOCACHE=/tmp/umcode-go-cache npm_config_cache=/tmp/umcode-npm-cache app/build/macos/bundle.sh`. Verify codesign, bundled engine version/commit, architecture, and bundle metadata. Disclose optional runtime omissions. Do not launch or restart the user's current app automatically.
- [ ] Report verified behavior, limitations, branch/commits, and the local build path. No push, PR update, release, live database surgery, or Goodreads file mutation is included in this execution approval.

## Handoff and self-review

Execute tasks in order: storage → continuity → requirements → authority → recovery
→ outcomes → presentation → acceptance. All eight tasks belong to one task-lifecycle
change; independently switching permission enforcement before persisted provenance
exists is prohibited. Intermediate commits are reviewable but not separate releases.

Coverage: spec state/migration = Tasks 1–3; approval/turn sequence = Task 4;
retry/failures = Task 5; results/delegation = Task 6; single blocker = Task 7;
compatibility and rollout = Task 8. All five Review Focus conditions have named
tests in their owning tasks. Interfaces reference only definitions above or
existing protocol/store types. Review the spec and this plan before execution.

Recommended execution: **Native**, sequential in this session with one fresh
whole-branch reviewer. The tasks depend tightly on shared authority and persistence
interfaces; sequential implementation keeps those decisions consistent without
repeated context handoffs. Subagent-driven execution remains available if selected.
