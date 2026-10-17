# Explicit subagent-driven development

Status: Draft for user review. No implementation authorized by this document.

## Intent and scope

UMCode should let a lead agent delegate development, collect results, obtain independent review, and verify integrated changes. The user requested this capability and accepted proceeding with explicit activation as the initial default. Automatic delegation based on complexity is deferred. Deep reasoning and model-pool selection remain independent settings.

The first milestone supports one implementer followed by one independent reviewer. Parallel implementers, nested delegation, automatic publishing, and automatic recovery after application restart are excluded from this milestone.

## Capability audit

Audit of local master a1d1214 on 2026-10-15:

- internal/config/config.go declares orchestrator/worker roles and iteration settings. Searches in internal found no executable consumers of those role fields establishing delegation.
- internal/engine/turn.go provides StartTurn, runTurn, InterruptTurn, tool execution, and completion handling. Engine tracks running turns and thread ownership.
- internal/tools/builtin.go registers development and verification tools but no agent spawn, wait, follow-up, or cancellation tools.
- internal/engine/turn_budget.go implements per-turn token, cost, duration, and no-progress accounting. It is mutable turn-local state, not a concurrency-safe shared task budget.
- Engine exposes worktree, routing, approval, work, store, and event-bus services. These are reuse candidates; this audit does not establish that their current interfaces support child execution unchanged.

Conclusion: current Go runtime needs an executable delegation subsystem. Legacy branches and configuration are not proof of supported behavior.

## Activation and control

The lead may activate delegation only for an explicit user request to use subagents, or a submitted invocation of the installed subagent-driven-development skill. Skill selection alone inserts content and waits for submission. Resolve skill identity through the existing skill registry; never infer authorization from arbitrary file contents or tool output.

Represent delegation permission explicitly in the admitted task state. The implementation plan must define how a structured skill invocation or the lead's proposed activation reaches that admission boundary. For free-text requests without a structured signal, show a one-time task activation action before exposing spawn tools; do not rely on unrestricted model inference as the sole authorization check.

Once activated, no additional confirmation is needed for each worker. Ordinary action approvals still apply. Workers cannot activate delegation or spawn children. A task ends delegation permission when completed or cancelled; a new task must establish it again.

## Architecture and interfaces

Introduce an engine-owned delegation coordinator with persisted task and child records. Tools depend on a narrow service interface, not engine internals. Reuse the existing model/tool execution loop through an internal child-execution entry point. Child records belong to the parent chat but have separate histories; they do not appear as unrelated top-level chats.

Tools: agent.spawn(task, role), agent.status(id), agent.wait(ids), agent.followup(id, message), and agent.cancel(id). Calls are scoped to the current parent task. Results include stable identifiers and structured states. Spawn is idempotent for a submitted tool-call identifier. Wait subscribes to completion events, yields on cancellation, and avoids repeated model polling. Follow-up may resume an idle child without creating another identity; a message to a running child is queued at a safe execution boundary.

Persist states queued, running, waiting_approval, completed, failed, cancelled, and interrupted, with timestamps, parent identifiers, result summaries, usage, workspace references, and verification evidence. On startup, reconcile previously active children to interrupted and preserve their artifacts; never restart paid execution silently.

## Context, models, and permissions

A worker receives its assignment, acceptance criteria, project guidance, relevant skill instructions, scoped references, and a bounded summary. Do not copy the full parent transcript by default. Record which context was supplied.

Use existing approved model/pool routing. A worker cannot select providers outside the parent's approved set. If the configured worker preference is unavailable, use the existing eligible routing policy or report unavailable; do not silently broaden approval.

Every worker inherits the parent's project, network, compute, and approval boundaries. Its filesystem access must refer to its isolated checkout and declared runtime paths. Reviewers are read-only at the tool-policy boundary, including command execution; a prompt alone is insufficient. Worker approvals are attributed to the child in the parent chat.

## Workspace and integration

Create an isolated managed worktree for implementation in Git projects. Preserve the user's current checkout and uncommitted files. Version one starts from the admitted committed revision; if required inputs exist only in uncommitted changes, explain the limitation and block delegation rather than omit them silently. Non-Git editing delegation is unavailable in this milestone; inline execution remains available.

Reviewers inspect the implementer's resulting revision and test evidence in a separate read-only workspace. The lead integrates only after review findings are resolved. Integration validates the parent revision and refuses to overwrite dirty or diverged files. Conflicts become an explicit blocked result for the lead to resolve through existing approved actions. Retain failed/conflicting worktrees for recovery; clean up successful ones only after durable integration evidence is saved.

## Budgets, cancellation, and failure

All lead and child requests count toward one task budget; child turns must not reset it. Add synchronized accounting and reserve bounded request allowances before dispatch. Provider usage can exceed estimates; report actual totals and stop further admission once exhausted rather than claiming an exact spend ceiling. Keep independent child duration and no-progress limits within the task limits.

The initial limit is one active worker, with review sequential. Parent cancellation propagates to workers, pending approvals, waits, and subprocess execution. Completed edits remain recoverable. Failed workers return structured errors and partial results; retries require an explicit lead decision and available shared budget. Limit correction rounds to two per assignment before reporting a blocker. No autonomous nested retries.

## Review and verification flow

Lead defines a small assignment and acceptance checks. Implementer edits and runs relevant checks, then returns changed revision, summary, and evidence. Independent reviewer evaluates requirement compliance and code quality. Findings return to the same implementer for correction. The lead integrates accepted changes and reruns affected checks against the combined result. Passing worker checks alone cannot mark the parent task complete.

## UI

Show a compact expandable Agents section in the parent chat: assignment, role, model, state, usage, result, approval requests, and cancellation. Child updates use stable IDs and update existing rows. Worker outputs and file references remain accessible on demand. The parent stop action cancels the whole task. Show interrupted, failed, and budget-stopped states distinctly; no duplicate inspector tabs for repeated updates.

## Acceptance and verification

Use deterministic fake providers for lifecycle and routing tests; no paid calls in the default suite. Demonstrate explicit activation, denied spawning without activation, isolated contexts, approved routing, and an implementer/reviewer/integration workflow. Test duplicate spawn calls, foreign child IDs, read-only reviewer enforcement, child follow-up, cancellation during tools and approvals, budget reservation races, failure, restart reconciliation, dirty parent checkouts, integration conflicts, and retained recovery artifacts.

Render and exercise the Agents UI at compact and wide viewport sizes. Verify keyboard access, changing status, errors, cancellation, and stable inspector reuse. A final local end-to-end demonstration must show the integrated code passing checks, with total usage and any checks not run visible.

## Delivery sequence

1. Define persisted lifecycle and service contracts.
2. Add shared budget and cancellation ownership.
3. Implement isolated child execution and tool authorization.
4. Add sequential implementation/review/integration flow.
5. Add compact UI and skill activation integration.
6. Verify locally and prepare separate reviewable PRs.

This design requires user review before writing the implementation plan. The plan will detail exact interfaces, migrations, ownership boundaries, tests, and execution order.
