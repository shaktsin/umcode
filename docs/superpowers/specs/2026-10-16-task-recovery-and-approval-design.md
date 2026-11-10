# Task recovery and engine-controlled approvals

Date: 2026-10-16
Status: Proposed for user review

## Intent and success criteria

UMCode must reliably continue authorized work across turns, models, compaction,
and application restarts. A user saying “retry” should not need to repeat the
original filename or repair internal workflow bookkeeping. The turn engine
remains the authority for permissions, approvals, execution, and recorded results.

This design replaces prompt-wording and specification-directory exceptions with
general task state and explicit enforcement rules. It applies to file edits,
shell actions, plugin tools, and delegated work. It does not grant new tool
permissions or automatically approve actions.

Success means that an ordinary permitted operation is not blocked solely because
an inferred planning graph lacks an approved decision; genuine user or configured
policy requirements remain enforced; retry resumes the correct unfinished task;
and completion claims are supported by execution and verification evidence.

## Current failure and code boundaries

`internal/work/rules.go` derives workflow depth from request and observation
signals. `internal/work/work.go` persists that depth and graph observations.
`internal/engine/workflow.go` currently uses depth and graph readiness to gate
non-discovery tools before normal action permissions are checked in
`internal/engine/turn.go`.

The recent recovery fix adds typed workflow blockers and prevents false final
success, but `internal/engine/workflow_recovery.go` authorizes specification
preparation from the current message's wording and path. A new “retry” turn
therefore loses that exception. The actual Qwen turn also sent an invalid
`work.update` transition without node revision predicates, then changed tools
without resolving its blocker.

Existing approvals, tool guards, project containment, hooks, scoped tool
selection, revision checks, child authority, and result items are retained.
Existing work graphs remain useful planning and observation records.

## Selected approach

Separate three concepts:

1. **Task state:** the user's objective, intended scope, unfinished operations,
   actual results, and relationship to prior turns.
2. **Action permissions:** whether the proposed action may run under current
   project, chat, tool, hook, and approval policy.
3. **Workflow requirements:** explicit prerequisites on a particular task or
   phase, such as “review the design before implementing.”

Do not replace the current regex with a broader regex or add directory-based
permission bypasses. Do not disable the approval engine. Inferred planning depth
can recommend planning and improve context, but cannot create human approval
requirements by itself.

## Persisted task and operation state

Add additive store records associated with the existing thread, work, turn, and
item IDs. One thread has at most one current task, with prior tasks retained.
An engine-assigned task revision protects state transitions from stale updates.

A task records its originating user item, objective, scope constraints, phase,
status, continuation source, and requirement references. Statuses are active,
blocked, awaiting_approval, completed, cancelled, and superseded. A phase is a
planning aid; it conveys no authority on its own.

An operation records the proposed canonical tool and arguments, implementation
identity, affected resources when known, permission outcome, workflow blockers,
execution status, result item, and verification evidence. Operation statuses are
proposed, blocked, awaiting_approval, running, succeeded, failed, cancelled, and
outcome_unknown. Verification is separately pending, passed, failed, or blocked.

The engine owns these statuses. Model-provided intent and completion statements
are proposals, not persisted evidence. Task scope must retain explicit user
constraints such as “no code yet”; the model cannot broaden or remove them.
An ambiguous scope or phase change requires clarification, not inferred approval.
Task intent is not itself a substitute for action policy checks.

Use existing result items as evidence rather than duplicating large tool outputs
or introducing unbounded prompt history. Operation arguments receive the same
local storage protections and retention as existing tool-call arguments.

## Requirement provenance and approvals

Every enforced workflow requirement must identify its scope, originating user
item or trusted policy setting, revision, required condition, and approval record
when applicable. Requirements can originate from an explicit user instruction or
configured application/project policy. Model proposals and inferred risk/depth
can recommend requirements but cannot declare them human-mandated.

The engine validates references and provenance. Arbitrary text in a file, tool
output, skill response, or model-generated work node cannot grant approval.
Explicit skill constraints remain part of the task when the user invokes that
skill; model claims that such constraints were met require supporting evidence.

Normal action approval remains handled by the existing approval mechanism.
An approval applies only to its recorded action or explicitly recorded scope.
Do not convert an action-specific approval into permanent permission. Retry
reuses approvals only where existing semantics permit reuse and all scope,
revision, policy, expiry, and implementation checks still hold. Rejection never
becomes approval through retry, a different model, or another tool.

When an ordinary permitted action has no applicable explicit workflow
requirement, missing graph bookkeeping cannot block it. Policy denial and
guard/hook denial still block it. No approval mode default is changed here.

## Turn and tool sequence

1. Persist the incoming user message and resolve its relationship to the active
   task before constructing model context.
2. For continuation, load a bounded task summary, pending operations, requirements,
   and evidence. For a new objective, create a new task and supersede the old
   current task without deleting its history.
3. Give the model that state and the available tools. Its proposed tool action
   is persisted before execution.
4. The engine checks current scope, tool availability, guards, hooks, action
   permissions, and applicable explicit workflow requirements. Preserve existing
   hook semantics; hooks cannot authorize a policy-denied action. Revalidate
   mutable authority immediately before execution after any approval wait.
5. If approval is required, persist and display a concrete request. If denied or
   blocked, record a structured reason and stop equivalent attempts.
6. If allowed, execute through the selected tool and persist its actual result.
7. Propose appropriate verification and run it through the same checks. Record
   evidence separately from mutation success.
8. Finalize the turn from operation outcomes. Incomplete or blocked work must
   not receive an engine-supported completion claim.

Workflow bookkeeping can be proposed through `work.update`, but it is not a
prerequisite for every file or shell action. Existing revision and graph validation
remain mandatory when updating that graph.

## Retry and continuation

Resolve clear continuation messages such as “retry,” including an invoked skill
prefix, against the saved current task. Ordinary follow-up language can be
interpreted using task context, but the engine validates any proposed task or
scope transition. Do not reconstruct approval authority from an unrestricted
scan of conversation text.

Retry first refreshes current project, permissions, requirements, tool identity,
and resource state. If its blocker remains, explain the blocker once and do not
spend another model loop trying equivalent workarounds. If it is recoverable,
provide the model the unfinished objective and the specific recovery step.

Do not blindly replay saved mutations. Reread files after an edit mismatch;
refresh graph revisions after a stale update; wait for an actual approval when
required. An uncertain prior execution must be inspected before replay, especially
for non-idempotent shell or external actions. If several pending operations make
the intended continuation ambiguous, ask one focused question.

Cancellation stops execution and persists state without granting permission to
resume automatically. A later explicit retry can resume after revalidation.
A changed objective cannot inherit unrelated approvals from the previous task.

## Failures, recovery, and completion

Use a common structured failure contract containing category, affected operation,
reason, retryability, and a concrete next step. Categories include policy_denied,
approval_required, approval_rejected, workflow_requirement, stale_state,
edit_conflict, missing_resource, execution_failed, verification_failed, and
outcome_unknown. Provider errors remain distinct from tool errors.

For graph recovery, return the affected work/node revisions and expected states
needed to refresh and correct a batch. Never recommend marking arbitrary nodes
completed or manufacturing an approved solution. Approval-dependent recovery
routes through the engine's approval flow.

Equivalent blocked attempts must remain blocked across tools and turns while the
cause is unchanged. Known resource/action equivalence should be used; opaque
shell or plugin effects must not be assumed safer or equivalent merely from text.
Keep a bounded within-turn retry budget for genuinely recoverable failures.

An unrelated successful action does not resolve a pending operation. A changed
implementation of the same requested outcome may resolve it only with explicit
supersession and verification evidence, rather than requiring byte-identical
arguments forever. The engine must distinguish completed, partially completed,
blocked, and unverified outcomes. Verification should be proportional to the
requested action, not an additional mandatory test suite for every edit.

Show one primary blocker in chat with optional technical details. Preserve the
full audit trail without rendering the same error as multiple independent alerts.
Streaming commentary may describe intentions; final persisted success claims
must be reconciled against actual outcomes.

## Existing work and compatibility

Migrate additively without rewriting live files or deleting graph/approval history.
Inferred depth on old work remains advisory. Existing pending or rejected approval
records remain restrictive in their recorded scope. Known trusted policy or user
requirements retain enforcement. Requirements whose provenance cannot be
established must be surfaced for one explicit resolution before restricted work
continues; do not silently approve them or preserve an unexplained global lock.

Create task context lazily from recorded user requests and result items on the
next interaction. Do not replay old operations as part of migration. Replace the
current specification-path exception once the generic gate behavior is in place.

Delegated work inherits the parent's task constraints and a narrower or equal
authority scope. A child cannot approve its own work, change parent requirements,
or regain permissions revoked in the parent. Persisted continuation must retain
these boundaries after restart or compaction.

## Verification and rollout

Use real engine/store/temp-project tests with fake model responses to reproduce
the reported request, inferred designed depth, blocked writes, and short retry.
Exercise file and non-file actions at arbitrary project paths across providers.

Required cases: permitted edit without graph approval; explicit planning-only
constraint; real approval accept/reject/expiry; changed permission during a wait;
equivalent shell workaround denial; stale graph update; edit mismatch and reread;
verified replacement operation; unrelated success; partial success; ambiguous
retry; cancellation; restart and compaction; changed model; new objective;
uncertain prior execution; legacy approval migration; parent/child restrictions;
single blocker presentation; and verification denied after a successful edit.

Run focused races for persisted task transitions, approvals, and cancellation,
then relevant engine/work/store/server/UI checks. The original regression must
fail before the change and pass afterward. Validate an actual local app build
without modifying the user's live project or restarting their current app.

Ship the work as focused changes: persisted state, generic gate integration,
continuation/recovery, and result presentation/compatibility. Maintain a single
authority path; do not introduce a temporary bypass that grants permissions.
No paid live-model tests are required to establish deterministic correctness.

## Non-goals

This change does not enable all tools by default, redesign model pooling, change
skill installation, implement new delegation features, or add automatic deployment.
It does not automatically complete the user's Goodreads specification or alter
the user's live workflow records during development.
