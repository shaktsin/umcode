# Phase 5: Designed Workflow and Curated Memory

**Status:** Approved conversational design; pending written-spec review

**Date:** 2026-10-07

**Parent spec:** `2026-09-30-quality-first-token-efficiency-design.md`

**Builds on:** Phase 2 Work Model, Phase 3 evidence lifecycle and vault, Phase 4a context compiler, and Phase 4b tool-result reducers

## Summary

Phase 5 makes UMCode choose and record the smallest sufficient solution before it implements meaningful work, adds a Designed workflow for high-risk or architecturally significant changes, and promotes verified durable project knowledge into `UMCODE.md` after successful work.

The implementation reuses the existing SQLite Work graph. A single compact `work.update` tool submits transactional semantic deltas for requirements, alternatives, decisions, tasks, unknowns, and memory candidates. Deterministic engine rules classify workflow depth, validate graph transitions, enforce approval gates, and expose only the active subgraph to the context compiler. No additional model call is introduced.

Phase 5 is delivered in two independently reviewable parts:

- **Phase 5a:** solution selector, Designed workflow, decision/task graph, approval gates, and `work.update`.
- **Phase 5b:** memory-candidate qualification and automatic, conflict-safe promotion to existing `UMCODE.md` scopes.

Normal chat remains quiet. The graph, gate machinery, memory provenance, and diagnostics are available on demand but do not produce routine chat messages or repository planning files.

## Approved product decisions

This phase records these explicit decisions from design review:

1. Use the existing relational Work graph and transactional graph deltas rather than an event-sourced projection or one document blob per work.
2. Use one batched `work.update` tool rather than separate decision, task, criteria, and memory tools.
3. Require explicit approval only at risk gates or when materially different architectural alternatives require a human choice.
4. Automatically promote eligible memory after successful completion.
5. Automatically replace an unchanged UMCode-generated entry when newer authoritative evidence proves it stale.
6. Auto-promote verified project facts and explicit user-approved decisions; never promote inferred preferences or generic advice.
7. Write to the nearest existing applicable `UMCODE.md`, falling back to the project root; never create nested instruction files automatically.
8. Deliver one overall design as Phase 5a followed by Phase 5b.

Decision 4 deliberately supersedes the parent spec's initial requirement for Project Settings review before modifying `UMCODE.md`. The safeguards in this spec—strict eligibility, generated-entry ownership, compare-and-swap, provenance, size limits, and conflict fallback—are required because promotion is automatic.

## Goals

- Prefer existing capabilities and the smallest sufficient implementation without simplifying away quality, safety, or verification.
- Persist requirements, non-goals, alternatives, approved decisions, tasks, dependencies, unknowns, and memory candidates across compaction, restart, and provider fallback.
- Escalate high-risk or architecturally significant work to Designed deterministically, without a classification model call.
- Prevent Designed implementation from starting before required decisions and approvals are resolved.
- Keep semantic graph updates compact, transactional, revision-checked, and recoverable.
- Give the context compiler a small active work subgraph rather than replaying design prose and completed task details.
- Automatically add concise, verified, durable project knowledge to `UMCODE.md` without overwriting user-authored or externally edited text.
- Preserve complete decision, evidence, and memory provenance in SQLite and the vault while keeping repository memory small.
- Support coding and general-purpose work with the same graph model.

## Non-goals

- A persistent Work, graph, or memory-review panel; that belongs to Phase 6.
- Automatically creating nested `UMCODE.md` files.
- Promoting active task state, transcripts, temporary failures, failed guesses, branch-specific facts, generic advice, or inferred user preferences.
- Replacing human-authored or externally edited instruction text automatically.
- Introducing a graph database, vector database, embeddings, or a new service.
- Generating a separate design document or task file for every work item.
- Using an additional model call for classification, solution selection, graph maintenance, or memory promotion.
- Enabling Phase 5 by default before Phase 6 quality and efficiency gates pass.
- Changing the existing safety policy for destructive tool actions.

## Quality and token invariants

1. **No quality downgrade.** Solution minimization cannot remove root-cause analysis, security checks, accessibility, data-loss protection, explicit requirements, or risk-appropriate verification.
2. **No silent gate bypass.** A required Designed decision or approval cannot be omitted under token pressure or bypassed by a mutating tool call.
3. **One semantic source of truth.** Active work state lives in SQLite. Model-facing packets are projections, not an independently editable plan.
4. **One model call.** Phase 5 adds tool use and deterministic validation but never invokes another model to classify, plan, summarize, or promote memory.
5. **Batch semantic updates.** The agent updates the graph at phase boundaries, not after every thought or tool call.
6. **Active subgraph only.** Rejected options, superseded decisions, completed task detail, and stale candidates do not consume routine prompt space.
7. **User-authored memory wins.** Automatic promotion owns only entries it previously generated and that remain byte-identical to the recorded version.
8. **Fail closed for gates and file writes.** Ambiguity, stale revisions, invalid graph transitions, concurrent file changes, or missing evidence cause rejection or a pending conflict, never a partial graph or file mutation.

## Architecture

Phase 5 adds four focused logical components while preserving the one-process local architecture:

1. **Workflow classifier** — deterministic Direct, Guided, and Designed escalation.
2. **Semantic graph updater** — validates and transactionally applies `work.update` batches.
3. **Gate evaluator** — derives task readiness and blocks mutation while required Designed gates are unresolved.
4. **Memory promoter** — qualifies completed-work candidates and merges eligible entries into `UMCODE.md` atomically.

`internal/work` owns classification, graph validation, lifecycle rules, readiness, and completion checks. `internal/store` owns transactional persistence. The engine exposes the tool, applies runtime gates, and connects approvals. `internal/ctxcompiler` renders the active projection. A focused memory package owns qualification and file merging without importing the engine.

## Phase 5a: solution selector and Designed workflow

### Deterministic workflow classification

`works.workflow_depth` adds `designed`. Depth is monotonic:

```text
direct -> guided -> designed
```

It never downgrades, including after a decision is rejected or a risk disappears.

Existing Guided escalation remains. Designed is triggered when deterministic evidence shows any of:

- Public API, protocol, file-format, or persisted data-model change.
- Database migration or compatibility change.
- Security, identity, authentication, authorization, secret handling, or trust-boundary change.
- Money movement or billing behavior.
- Destructive or difficult-to-recover operation.
- Cross-subsystem change with multiple affected ownership boundaries.
- Unclear or contradictory acceptance criteria.
- Two or more materially different viable architectural alternatives.
- Explicit user request for a design or architectural decision.

Turn-start rules may recognize explicit request language and known high-risk scope without a model. Repository inspection may reveal hidden complexity; the active agent then escalates through `work.update`. Obvious path/tool signals such as migrations and public protocol schemas also escalate deterministically. A model cannot request a downgrade.

### Solution-selection policy

Before a Guided or Designed implementation task becomes ready, the graph contains one active solution decision recording the first sufficient rung:

1. The change does not need to exist.
2. The repository already contains the capability.
3. The standard library satisfies it.
4. The native platform satisfies it.
5. An already-installed dependency satisfies it.
6. Minimum new code is required.

The decision links to the acceptance criteria it serves and the inspected evidence that supports it. Every proposed new abstraction, dependency, configuration key, or file identifies the criterion that necessitates it.

The engine validates structure and evidence references; it does not independently judge whether prose reasoning is persuasive. Designed work with materially different options requires an approved decision. Guided work records a selected decision but does not require user approval unless another risk gate applies.

### Graph model

Phase 5 retains the current tables and adds a monotonic `works.revision` integer for optimistic concurrency.

New node kinds:

- `requirement`
- `non_goal`
- `option`
- `decision`
- `task`
- `unknown`
- `memory_candidate`

Existing `goal`, `criterion`, `artifact`, and `fact` remain valid.

Lifecycle states:

```text
decision: proposed -> approved | rejected -> superseded
task: pending -> ready -> in_progress -> completed | failed | blocked
unknown: open -> resolved | accepted_risk
memory_candidate: pending -> promoted | conflicted | stale | rejected
```

Terminal states cannot move backward. A replacement creates a new node or revision and marks the old node superseded rather than rewriting history.

New edge relations:

- `depends_on`: task to prerequisite task
- `supports`: evidence-bearing node to decision, requirement, candidate, or claim
- `contradicts`: two incompatible semantic nodes
- `selects`: decision to chosen option
- `implements`: task to requirement or decision
- `verifies`: criterion to requirement, task, decision, or candidate
- `candidate_for`: memory candidate to the durable fact or approved decision it represents

Edges remain within one work. Cross-work memory provenance uses stored identifiers, not graph edges that weaken cascade boundaries.

### `work.update`

`work.update` is a compact internal tool for Guided and Designed semantic changes. When `models.designed_workflow` is enabled, its schema is available for work-backed turns. Direct work is instructed not to call it unless classification or semantic structure changes.

Conceptual request:

```json
{
  "work_id": "wrk_...",
  "expected_revision": 4,
  "nodes": [
    {
      "ref": "client-local-ref",
      "id": "optional-existing-node-id",
      "kind": "task",
      "title": "Implement migration",
      "content": {},
      "from_status": "pending",
      "to_status": "ready",
      "evidence_ids": []
    }
  ],
  "edges": [
    {"from": "client-local-ref", "relation": "implements", "to": "wnd_..."}
  ],
  "rationale": "compact phase-level reason"
}
```

The exact implementation schema may separate creates and transitions, but it must preserve these semantics:

- One work and expected work revision per call.
- Stable client-local references allow nodes created in the batch to be linked without a second call.
- Existing-node transitions provide the expected current state or node revision.
- Evidence is referenced by ID and never copied into the update.
- Rationale is length-bounded, redacted before persistence, and not treated as evidence.
- Batch node, edge, and text sizes are bounded.

Validation rejects the whole batch for:

- Missing, foreign, stale, or duplicate work/node/evidence identifiers.
- Unsupported node kinds, statuses, transitions, or edge relations.
- Cross-work links.
- Task dependency cycles, including cycles created through existing edges.
- A selected option not owned by the decision's work.
- Approval-dependent transitions without approval evidence.
- A memory candidate without a supported durable-fact or approved-decision source.
- Stale work or node revision.
- Secret-like candidate content.

The store applies a valid batch in one SQLite transaction and increments `works.revision` once. Tool output reports only the new revision and counts of created, transitioned, and linked records. It does not replay the graph.

### Task readiness and completion

A task is `ready` only when:

- Every `depends_on` task is completed.
- Every required decision it implements is selected and, when gated, approved.
- Every blocking unknown is resolved or explicitly accepted as a risk through an approval.
- Applicable acceptance criteria exist for nontrivial implementation work.

The engine derives readiness after each successful batch; the agent cannot force an illegal ready state.

A work cannot complete while any required criterion is unresolved, required decision is proposed, required task is pending/ready/in-progress/blocked/failed, or blocking unknown is open. Existing verification freshness rules continue to apply.

### Approval gates

Explicit approval is required only for:

- Public contracts or persisted schema/migration choices.
- Security, authorization, secret-handling, or trust-boundary choices.
- Destructive or difficult-to-recover plans.
- Money movement or billing behavior.
- A choice among materially different architectural alternatives.
- Accepting a material unresolved risk.

Routine internal decisions are recorded and continue silently.

A gated decision remains `proposed` and creates a non-rememberable approval request tied to its node ID and exact revision. Approval atomically transitions it to `approved`. Denial transitions it to `rejected`, leaves dependent tasks blocked, and permits a replacement option. Architectural approvals cannot be remembered across works because the choice and evidence are work-specific.

The existing approval transport and timeout behavior are reused, but a workflow approval has a distinct audit kind from a tool-action approval.

### Runtime enforcement

When a Designed work has an unresolved required gate, read-only discovery tools remain available while workspace-mutating and external side-effect tools return a compact deterministic gate error before execution. Existing safety policy still applies after the workflow gate opens.

The enforcement decision uses persisted state, not only prompt instructions. Gate database read failures fail closed for Designed mutation. Non-gating observation recording remains best-effort as in earlier phases.

Guided work may mutate after its solution decision and active task are recorded. Direct work keeps today's path and does not acquire a synthetic plan.

### Context compiler projection

When the context compiler is enabled, its Work packet adds only:

- Current workflow depth and work revision.
- Active requirements and criteria.
- Approved or selected solution decision and chosen option.
- Active task and its incomplete dependencies.
- Blocking unknowns and unresolved gate state.
- Definition of completion.

Rejected options, superseded decisions, completed task detail, promoted/stale/rejected candidates, and full rationales are omitted from the routine packet. They remain queryable from the canonical graph.

Required gates, explicit user decisions, the active task, and blocking unknowns are P0 and cannot be removed under token pressure. The chosen design and applicable invariants are P1. A compiler failure falls back to the canonical history path without changing graph state or gate enforcement.

## Phase 5b: automatic curated memory

### Candidate shape

A memory candidate is a structured node whose content includes:

```text
category          capability | command | boundary | invariant | convention | path | approved_decision
semantic_key      stable, project-scoped identity
text              concise proposed Markdown bullet text
scope_paths       repository-relative paths the fact applies to
evidence_ids      active supporting evidence
source_revision   repository revision or workspace fingerprint
replaces_memory   optional prior generated memory ID
```

Allowed content is limited to verified project facts and explicit user-approved decisions. Inferred preferences, generic advice, task status, temporary failures, guesses, full output, secrets, and branch-local facts are rejected.

Candidate text and semantic keys are length-bounded and redacted before persistence. Redaction is not permission to promote a secret-bearing candidate: secret detection rejects it.

### Qualification

Promotion runs only after the work has completed successfully. A candidate qualifies only when all are true:

- Its source node is an active fact or approved decision.
- Every referenced evidence record exists, is active, and is available when a vault object is required.
- Applicable verification criteria passed and remain fresh at the final workspace/environment fingerprint.
- The source revision still matches the completed work's final revision.
- The candidate is project-specific, concise, and allowed by category.
- The semantic key is absent, already current, or names an unchanged generated entry eligible for authoritative replacement.
- Promotion will not exceed the target file's soft size limit.

Already-current candidates become `promoted` without a file rewrite. Candidates invalidated before completion become `stale`. Ambiguous or externally edited conflicts become `conflicted` and remain inspectable.

### Placement

For nonempty `scope_paths`, the promoter finds the nearest existing `UMCODE.md` that applies to all scoped paths. If no nested file qualifies, it uses `<project>/UMCODE.md`. A root file may be created if absent. Nested `UMCODE.md` files are never created automatically.

Cross-cutting facts and approved decisions target the root file. A candidate with disjoint scopes that cannot share one existing nested instruction file also targets the root.

The root and each nested file have a soft target of approximately 4 KB. When promotion would exceed the target, the candidate remains pending; automatic promotion does not evict human-authored content or lower-value memory in Phase 5.

### Managed section and ownership

Automatic entries use one compact section:

```markdown
## Verified project memory

- Use `go test ./...` for the complete Go suite.
```

SQLite, not hidden Markdown metadata, stores the semantic key, exact generated text, text hash, target path, evidence references, source revision, and promotion history.

The promoter owns an existing entry only when the exact recorded generated text is still present in the recorded target and the current text hash matches. A user edit, move, rewrite, or deletion removes automatic ownership and converts a replacement attempt into a conflict. Text outside the managed section is always user-authored.

### Minimal merge and stale replacement

Promotion performs:

1. Read and hash the current target file.
2. Parse only the exact managed heading and bullet region; malformed or duplicate managed sections cause a conflict.
3. Normalize line endings for comparison but preserve the file's existing line-ending style on write.
4. Suppress exact normalized duplicates anywhere in the file.
5. Insert a new bullet or replace the exact unchanged generated bullet named by `replaces_memory`.
6. Re-check the file hash immediately before writing.
7. Write a sibling temporary file, preserve existing permissions, fsync, and atomically rename.
8. Record the project file change and append a promotion revision with before/after hashes and provenance.

Only a newer candidate backed by authoritative active evidence may replace a prior generated entry. The old memory row remains `superseded`; its concise prior text and provenance remain in SQLite, while any supporting raw evidence remains governed by vault retention.

Human-authored or externally edited conflicts are never automatically replaced, even when evidence is newer. They remain `conflicted` for Phase 6 review.

### Promotion persistence

An additive memory table records one historical row per semantic entry revision:

```text
project_memories
  id, project_id, work_id, candidate_node_id
  semantic_key, category, target_path
  text, text_hash, status
  source_revision, evidence_json
  file_hash_before, file_hash_after
  superseded_by, created_at, promoted_at
```

Memory-row statuses are `active`, `superseded`, `conflicted`, and `pending_repair`. A successful replacement inserts a new active row and marks the prior active row superseded in the same transaction; rows are never deleted by promotion. Active uniqueness is `(project_id, semantic_key, status=active)` at the service layer and through an appropriate partial index where supported by the bundled SQLite version. Historical and conflicted rows remain queryable. The source candidate becomes `promoted` only when its corresponding memory row is active or its content was already current.

The `UMCODE.md` write is recorded through the existing project-change mechanism so normal diff and revert surfaces include it. It is linked to the completed work as a memory promotion, not recorded as a product artifact that would stale the work's just-passed verification criteria.

## Configuration and rollout

Both capabilities default off:

```yaml
models:
  designed_workflow: false

memory:
  auto_promote: false
```

`memory.auto_promote` requires the Phase 5a graph capability because candidates and provenance originate there. If automatic promotion is enabled while Designed workflow is disabled, the engine leaves it inactive and emits a safe configuration diagnostic rather than writing files.

Phase 5a and 5b can therefore be measured independently, but 5b cannot bypass 5a's semantic and evidence validation.

No UI, default chat item, notification, or mandatory prompt is added. Existing `work/get` exposes the nodes and edges of one work. Phase 6 adds project-wide memory review, correction, retention, and deletion surfaces.

## Failure behavior

- Invalid or stale `work.update`: reject the entire batch with compact field-level reasons.
- Store failure during a required gate check: block mutation and report the gate is unavailable.
- Store failure during non-gating observation: count/log and continue under existing best-effort behavior.
- Approval timeout or denial: leave dependent tasks blocked; never infer approval.
- Context compilation failure: fall back to history; persisted gates still apply.
- Missing, stale, unavailable, or contradictory evidence: do not promote the candidate.
- Candidate conflict or target parse ambiguity: mark conflicted; do not write.
- Concurrent target edit: abort compare-and-swap, re-read once, then mark conflicted if the merge is no longer exact.
- Temporary write, fsync, permission, or rename failure: leave the original file intact and candidate pending with a safe diagnostic.
- Promotion persistence failure before rename: do not write.
- Persistence failure after a successful rename: record an audit-repair marker and reconcile from the exact generated text/hash on startup; never rewrite blindly.
- Oversize target: leave candidate pending; do not evict memory automatically.

Diagnostics contain IDs, states, counts, paths, revisions, and hashes. They never log candidate text, evidence content, tool output, secrets, or approval rationale.

## Security and privacy

- `work.update` accepts semantic data only for the active work and cannot reference another work's private graph.
- Candidate and rationale text pass through existing redaction before SQLite persistence.
- Secret-like memory candidates are rejected rather than merely redacted and promoted.
- Evidence content is referenced by ID; raw vault objects are not copied into graph nodes or `UMCODE.md`.
- Scope paths are resolved within the project root; symlink and traversal checks use the existing resolved-path rules.
- Memory writes respect project ACLs and never target foreign instruction files.
- `AGENTS.md`, `CLAUDE.md`, and global `AGENT.md` remain untouched and unscanned.
- Workflow approval signatures are work- and revision-specific and cannot be remembered or replayed for another work.

## Observability and token accounting

Developer diagnostics record:

- Workflow-depth transitions and trigger class.
- `work.update` created/transitioned/linked counts, rejection class, and serialized request/result token estimates.
- Gate blocks, approvals, denials, and timeouts by node ID and revision.
- Work-packet tokens attributable to decisions, active tasks, dependencies, and unknowns.
- Candidate qualification outcomes by reason.
- Memory file before/after byte counts and promotion/replacement/conflict counts.

No diagnostic records semantic text or evidence bodies.

Phase 6 ablations compare baseline, Phase 5a, and Phase 5a+5b for successful-task quality, input/output tokens, tool rounds, repeated exploration, unsupported completion claims, and memory correctness. The cost of the `work.update` tool schema and calls is included, not hidden.

## Testing

### Phase 5a deterministic tests

- Direct remains Direct; existing Guided triggers still escalate; every Designed trigger escalates; depth never downgrades.
- Solution rungs and required criterion/evidence links validate without a model call.
- Migration upgrades fresh and existing databases and preserves old Work records.
- Valid batches commit atomically and increment the work revision exactly once.
- Invalid transitions, stale revisions, cross-work references, duplicate refs, and oversized fields roll back completely.
- Dependency cycles are rejected against both batch-local and existing edges.
- Derived readiness changes only when dependencies, decisions, and unknowns allow it.
- Completion rejects unresolved required nodes.
- Workflow approval is node/revision specific, non-rememberable, auditable, and handles approval, denial, and timeout.
- Designed mutation is blocked before approval and permitted afterward; read-only investigation remains available.
- Required-gate database failure blocks mutation.
- `work.update` result stays compact and contains no evidence text.

### Context tests

- Active requirements, approved decision, chosen option, active task, incomplete dependencies, and blocking unknowns render with correct P0/P1 priority.
- Rejected options, superseded decisions, completed task detail, and inactive candidates are absent.
- Compaction, provider fallback, and restart reconstruct the same active projection from SQLite.
- Flag-off prompt, tool set, request accounting, and transcript goldens remain unchanged.
- Tool-schema and update-call token costs are attributed.

### Phase 5b memory tests

- Eligibility matrix covers every allowed category and every rejection condition.
- Only completed, verified, fresh work promotes memory.
- Approved decisions may promote; proposed/rejected/superseded decisions cannot.
- Secret-like, generic, temporary, branch-local, stale, contradictory, or unverified candidates never write.
- Placement chooses the nearest existing applicable file, falls back to root, and never creates a nested file.
- Root creation, first managed section, insertion, duplicate suppression, and line-ending/permission preservation work.
- Unchanged generated stale text is replaced by newer authoritative evidence; old provenance remains.
- Human or external edits produce a conflict and remain byte-identical.
- Concurrent file changes fail compare-and-swap without lost updates.
- Malformed/duplicate managed sections and oversize files remain unchanged.
- Every injected write failure preserves the original file and recoverable candidate state.
- Promotion appears in project diff/revert history but does not stale the completed work's product criteria.
- Flag-off and invalid-config paths never write.

### End-to-end tests

- A Guided coding task records the chosen solution, task graph, verification, and compact Work packet without approval noise.
- A Designed public-schema task cannot mutate before approval, resumes after approval, and survives engine restart.
- Rejected architecture leaves dependent tasks blocked and permits a replacement decision.
- Successful work auto-promotes a verified command to the existing root `UMCODE.md`; the next turn receives it once.
- A newer successful command supersedes an unchanged generated entry while preserving history.
- An externally edited entry is not overwritten and becomes conflicted.
- General-purpose Designed work uses the same graph and gates but never attempts project memory without a project.

## Delivery decomposition

### Phase 5a

- Add additive schema/revision support and new protocol constants/types.
- Implement deterministic Designed classification and solution-selection validation.
- Implement transactional graph batches and the `work.update` tool.
- Implement derived task readiness, completion rules, and approval records.
- Enforce mutation gates in the engine.
- Extend the context compiler's active Work packet and accounting.
- Keep `models.designed_workflow` off by default.

### Phase 5b

- Add memory-candidate validation and `project_memories` persistence.
- Implement completion-triggered qualification and placement.
- Implement exact managed-section merge, stale generated-entry replacement, compare-and-swap, and atomic writes.
- Integrate project diff/revert history without reopening completed work.
- Add memory diagnostics and `memory.auto_promote`, off by default.

Each phase receives its own implementation plan, feature branch, task-level review loop, end-to-end proof, and pull request. Phase 5b branches from merged Phase 5a because its validated candidates and graph provenance are a hard dependency.

## Acceptance criteria

Phase 5 is complete when:

- Workflow depth deterministically escalates through Designed and never downgrades.
- Every meaningful Guided/Designed implementation records a smallest-sufficient solution decision linked to criteria and evidence.
- Designed mutation cannot cross an unresolved required risk/choice gate.
- One transactional, revision-checked `work.update` tool maintains valid decision/task/unknown/candidate graph state without extra model calls.
- The context compiler sends the active work subgraph and omits superseded/completed design noise.
- Completion cannot be claimed while required decisions, tasks, unknowns, criteria, or evidence are unresolved.
- Eligible verified project facts and approved decisions automatically reach the nearest existing applicable `UMCODE.md` or project root.
- Newer authoritative evidence replaces only unchanged UMCode-generated stale entries.
- Human-authored or externally edited text is never overwritten automatically.
- Memory writes are atomic, provenance is auditable, and failures preserve the original file.
- Normal chat remains no noisier than before and both capabilities remain independently disableable for Phase 6 ablations.
