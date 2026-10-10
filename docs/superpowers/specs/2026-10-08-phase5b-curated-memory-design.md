# Phase 5b Curated Project Memory Design

**Status:** Draft for review

**Date:** 2026-10-08

**Depends on:** Phase 5a Designed Workflow, merged in PR #50

**Parent design:** `docs/superpowers/specs/2026-10-07-phase5-designed-workflow-memory-design.md`

## Purpose

Phase 5b turns verified, durable knowledge from completed Work graphs into concise project guidance in `UMCODE.md`. Its purpose is to reduce repeated discovery, repeated tool calls, and repeated prompt tokens without lowering task quality or allowing stale or speculative statements to become instructions.

Promotion is deterministic and adds no model call. It is synchronous after successful Work completion, opt-in, conflict-safe, auditable, and independent from the completion transaction. A promotion failure never changes a completed Work back to open or failed.

## User outcome

When the feature is enabled, facts such as a verified test command, an invariant enforced by code, or an explicitly approved architectural decision can become durable guidance for later tasks. The next applicable turn receives the guidance once through the existing root-to-leaf `UMCODE.md` composition path.

The feature must not turn `UMCODE.md` into a transcript, task log, cache of tool output, or collection of generic advice. Incorrect or stale promotion is a correctness failure, not an acceptable token-saving tradeoff.

## Goals

- Qualify Phase 5a `memory_candidate` nodes only after their Work completes successfully.
- Promote verified project facts and approved decisions without another model call.
- Write to the nearest existing applicable `UMCODE.md`, falling back to the project root.
- Preserve user-authored and externally edited text byte-for-byte.
- Replace only exact, unchanged UMCode-generated entries.
- Recover deterministically from process interruption between filesystem and SQLite updates.
- Record provenance, conflicts, byte changes, and estimated token impact.
- Keep the feature disabled by default and preserve exact flag-off behavior.

## Non-goals

- Project-wide memory review, editing, retention, or deletion UI; those remain Phase 6.
- Automatic creation of nested `UMCODE.md` files.
- Eviction, ranking, or summarization of existing memory to make room.
- Reading, writing, importing, or reconciling `AGENTS.md`, `CLAUDE.md`, or global `AGENT.md`.
- Promoting task status, temporary failures, inferred preferences, guesses, branch-local observations, raw logs, or evidence bodies.
- Letting `work.update` create observational facts, criteria, evidence, or active memory rows directly.
- Using an additional model call to classify, rewrite, merge, or repair memory.

## Configuration and activation

```yaml
models:
  designed_workflow: true

memory:
  auto_promote: false
  target_file_bytes: 4096
```

`memory.auto_promote` defaults to `false`. Promotion runs only when it and `models.designed_workflow` are both true. Enabling automatic promotion while Designed workflow is disabled produces a safe configuration diagnostic and performs no write.

`target_file_bytes` defaults to 4096 and is a soft ceiling for the complete target file after the proposed merge. It is not permission to truncate, rewrite, or evict user content. A non-positive or unreasonably large configured value is rejected during configuration validation rather than silently normalized.

## Components and responsibilities

### Candidate qualifier

`internal/memory` owns deterministic candidate decoding and eligibility. It receives canonical Work, node, evidence, verification, project, and final workspace-revision data. It returns a qualified immutable proposal or a compact outcome reason; it does not read or write project files.

### Placement resolver

The resolver receives validated repository-relative scope paths and the current project instruction-file inventory. It chooses the nearest existing `UMCODE.md` applicable to every scoped path. Cross-cutting or disjoint scopes use the project root. If the root file does not exist, the root may be created. A nested file is never created automatically.

### Managed-section merger

The merger is a pure function over current bytes, active memory rows, and qualified proposals. It parses one exact managed section, proves ownership through byte-identical recorded entries, suppresses duplicates, enforces the size target, and returns new bytes plus an explicit change set. It never performs I/O.

### Promotion coordinator

The coordinator runs after the Work completion transaction commits. It serializes promotions per project, performs qualification and placement, persists a prepared operation, performs compare-and-swap file replacement through the existing project-change recorder, finalizes memory rows and candidate states, and emits metadata-only diagnostics.

### Recovery reconciler

Startup recovery examines incomplete promotion operations and current target hashes. It retries or finalizes only when the current file equals a recorded before or after state. Any third state becomes a conflict; recovery never reconstructs or overwrites content speculatively.

## Candidate contract

A `memory_candidate` contains bounded structured content:

```text
category          capability | command | boundary | invariant | convention | path | approved_decision
semantic_key      stable project-scoped identity
text              concise proposed Markdown bullet text
scope_paths       repository-relative paths where the knowledge applies
source_revision   final repository revision or workspace fingerprint
replaces_memory   optional prior generated memory ID
```

Evidence is referenced through the node's canonical `EvidenceIDs`; evidence text is never copied into the candidate or output. Candidate strings are redacted on a copied request before persistence, but redaction cannot make a secret-bearing candidate eligible: secret detection rejects the candidate.

`semantic_key` is a bounded machine identity, not display prose. It is unique among active memory rows for one project. Candidate text is one normalized Markdown bullet and cannot contain headings, HTML blocks, managed markers, control characters, absolute paths outside the project, or multiple entries.

## Qualification

Promotion runs only after successful Work completion. A candidate qualifies only when all of the following hold:

- The candidate remains active and pending at the completed Work's final revision.
- It has exactly one active `candidate_for` source in the same Work.
- The source is an engine-owned durable fact or an active approved decision.
- Every referenced evidence row exists, belongs to the Work, is active, and has any required vault object available.
- Applicable criteria passed and remain fresh at the final workspace and environment fingerprints.
- The candidate's `source_revision` matches the Work's final recorded revision or fingerprint.
- The category, semantic key, text, and scope paths are structurally valid and project-specific.
- The content is concise, durable across future tasks, and contains no secret-like material or raw evidence.
- The semantic key is absent, already current, or explicitly replaces an unchanged active generated entry.
- The resulting target file does not exceed its configured soft size target.

Deterministic rejection rules exclude generic advice, inferred preferences, temporary task state, temporary failures, guesses, unsupported claims, full output, secrets, and branch-local facts. Qualification uses typed source/category rules, graph relations, evidence state, fingerprints, and bounded lexical checks; it never asks a model whether prose is persuasive.

Outcomes are:

- `promoted`: a matching active memory row exists, with or without a file rewrite.
- `rejected`: the candidate is structurally unsafe, generic, secret-like, unsupported, or otherwise permanently ineligible.
- `stale`: evidence, source, revision, or verification freshness no longer matches.
- `conflicted`: current user/external/generated content prevents a provably safe merge.
- `pending`: eligible but blocked by size, filesystem, or another retryable failure.

## Placement

All scope paths are normalized repository-relative paths, resolved through existing project containment and symlink protections, and checked to exist at qualification time.

For nonempty scopes, the resolver selects the deepest existing `UMCODE.md` whose directory is an ancestor of every scope path. If none qualifies, or the scopes are disjoint, it selects `<project>/UMCODE.md`. Empty scope and approved cross-cutting decisions also select the root.

The root file may be created. Nested files may only be selected when they already exist. No other instruction filename is considered.

## Managed section and ownership

Automatic entries live in one section:

```markdown
## Verified project memory

<!-- umcode:generated -->
- Use `go test ./...` for the complete Go suite.
```

The marker contains no semantic key or provenance. SQLite is authoritative for semantic identity and ownership.

The promoter owns an entry only when all of these match an active memory row:

- target path;
- exact recorded generated bullet bytes after line-ending normalization;
- recorded text hash;
- the entry remains within the single well-formed managed section.

Text outside the managed section is always user-authored. If a generated entry is edited, moved, rewritten, or deleted, automatic ownership ends. A later replacement becomes `conflicted`; UMCode does not restore or overwrite it. Duplicate or malformed managed sections also conflict without a write.

## Merge algorithm

For each target file, under a per-project serialization boundary:

1. Read bytes, permissions, and line-ending style; compute `before_hash`.
2. Load active generated-memory rows for the exact target path.
3. Parse at most one exact managed section and reconcile every recorded entry.
4. Treat exact normalized duplicate text anywhere in the file as already current without adding a second copy.
5. Insert a new entry, or replace only the exact unchanged entry named by `replaces_memory`.
6. Render deterministically while preserving unrelated bytes and the existing line-ending style.
7. Reject the merge if the complete result exceeds `target_file_bytes`.
8. Re-read and compare the current hash immediately before writing.
9. Write a sibling temporary file, apply the original permissions when present, fsync the file, atomically rename, and fsync the parent directory where supported.
10. Record the change through the existing project-change recorder so normal diff and undo surfaces include it.

The merge never sorts, reformats, or rewrites user-authored content. It does not evict old memory in Phase 5b.

## Persistence

Migration 0016 is additive and introduces historical memory rows and recoverable operations.

```text
project_memories
  id, project_id, work_id, candidate_node_id
  semantic_key, category, target_path
  text, text_hash, status
  source_revision, evidence_json
  file_hash_before, file_hash_after
  superseded_by, created_at, promoted_at

memory_promotion_ops
  id, project_id, work_id, candidate_node_id, memory_id
  thread_id, turn_id
  target_path, state
  file_hash_before, file_hash_after
  before_bytes, after_bytes
  error_class, created_at, updated_at

file_changes
  add nullable promotion_op_id
```

Memory-row statuses are `active`, `superseded`, `conflicted`, and `pending_repair`. Rows are historical and never deleted by automatic promotion. The service and a supported SQLite partial unique index enforce at most one active row per `(project_id, semantic_key)`.

Operation states are `prepared`, `file_written`, `committed`, `conflicted`, and `pending_repair`. Operation byte snapshots are bounded by the instruction-file limit and are needed only for exact recovery and project undo integration; they are not model context or diagnostic output.

`file_changes.promotion_op_id` is empty for ordinary tool edits and uniquely identifies the promotion operation for memory writes. A partial unique index over nonempty values makes project-change recording idempotent during restart recovery. The operation retains the originating thread and completion turn so the existing diff, undo, and file-change item surfaces attribute the write correctly.

A replacement inserts the new row and marks the prior row `superseded` in one SQLite transaction. The candidate becomes `promoted` only when its corresponding memory row is active or the exact desired content was already current.

## Filesystem/SQLite commit protocol and recovery

SQLite and the filesystem cannot share a transaction. The coordinator therefore uses a recoverable protocol:

1. In one SQLite transaction, validate current candidate/work/memory predicates and create a `prepared` operation containing bounded before/after bytes and hashes.
2. Re-check `before_hash`; if it changed, mark the operation and candidate conflicted without writing.
3. Atomically replace the file and record the existing project file change with the operation ID as its idempotency key.
4. Mark the operation `file_written`.
5. In one SQLite transaction, activate the new memory row, supersede any replaced row, transition the candidate to `promoted`, and mark the operation `committed`.

On startup, each incomplete operation is reconciled:

- current hash equals `before_hash`: retry the exact prepared write;
- current hash equals `after_hash`: finish the project-change record and SQLite commit idempotently through `promotion_op_id`;
- current hash equals neither: mark the operation, memory row, and candidate conflicted without a write.

A temporary write, permission, fsync, or rename failure leaves the original file intact and the operation `pending_repair`. A persistence failure after rename leaves enough exact state for restart reconciliation. Completed Work state never changes.

## Triggering and lifecycle

The Work completion transaction commits first. The engine then invokes the promoter synchronously for the completed Work. The promoter processes a fixed snapshot of eligible candidate IDs in deterministic graph-ID order.

Promotion latency is included in the completion operation but does not change its semantic result. A promotion error is reported as compact memory diagnostics, not as Work failure. Retrying completion or restarting is idempotent through candidate state, semantic-key uniqueness, exact text hashes, and operation recovery.

Projectless Work never attempts promotion. Works without candidates return without touching project files or querying promotion tables beyond the minimum feature-gated check.

## Security and privacy

- Candidate, scope, and generated text sizes are bounded before persistence.
- Secret-like candidates are rejected rather than promoted in redacted form.
- Raw evidence, tool output, approval rationale, and candidate text never enter logs or audit diagnostics.
- Evidence is referenced by immutable identity and availability, not copied into `UMCODE.md`.
- Target and scope paths use existing project containment, symlink, and ACL rules.
- The temporary file is created in the target directory with restrictive initial permissions.
- Only `UMCODE.md` is read or written; foreign instruction files remain untouched and unscanned.
- User-authored and externally modified content always wins over generated state.

## Diagnostics and accounting

Metadata-only diagnostics record:

- candidate counts by `promoted`, `rejected`, `stale`, `conflicted`, and `pending` reason;
- target path plus before/after byte counts and hashes;
- inserted, replaced, unchanged, and conflict counts;
- source Work/candidate/revision and evidence identities;
- estimated instruction tokens added;
- estimated future context tokens avoided through duplicate suppression or authoritative replacement;
- recovery operations completed, retried, conflicted, or pending repair.

No diagnostic includes candidate text, generated bullet text, evidence content, tool output, secrets, or approval rationale. Existing request totals continue to include the resulting `UMCODE.md` prompt tokens normally; savings are never subtracted from usage accounting.

## Failure behavior

- Missing, stale, unavailable, contradictory, or foreign evidence: do not promote; mark stale or rejected as appropriate.
- Secret-like or structurally invalid candidate: reject permanently.
- Ambiguous source, semantic key, placement, or managed section: conflict without a file write.
- Concurrent external edit: re-read once; proceed only if the merge remains exact, otherwise conflict.
- Oversize result: remain pending; do not evict or truncate.
- Read-only target, temporary-file, fsync, or rename failure: preserve original bytes and mark pending repair.
- SQLite failure before rename: do not write.
- SQLite failure after rename: reconcile from recorded exact hashes/bytes on restart.
- Project-change recording failure: retain a pending-repair operation and reconcile idempotently; never invent a successful audit record.
- Invalid feature combination: diagnose and perform no promotion reads or writes.

## Testing strategy

### Deterministic unit tests

- Eligibility covers every allowed category and every permanent, stale, conflict, and retryable outcome.
- Approved decisions qualify; proposed, rejected, and superseded decisions do not.
- Secret-like, generic, temporary, branch-local, unsupported, contradictory, and stale candidates never produce a write proposal.
- Placement selects the nearest common existing file, falls back to root, permits root creation, and never creates nested files.
- Merge tests cover first section creation, insertion, normalized duplicate suppression, exact replacement, malformed/duplicate sections, line endings, permissions, and size overflow.
- A user-edited, moved, deleted, or rewritten generated entry is never considered owned.
- Feature-off and invalid-config paths perform no promotion I/O.

### Store and crash tests

- Fresh and upgraded databases preserve all Phase 5a records.
- Active semantic-key uniqueness, historical supersession, candidate transition, and operation creation/finalization are atomic.
- Injected failures at every commit-protocol boundary preserve original bytes or leave a recoverable exact state.
- Restart recovery handles before-hash, after-hash, and third-state files deterministically and idempotently.
- Concurrent Works targeting one file cannot lose an update.

### End-to-end tests

- Successful Work promotes a verified command into an existing root `UMCODE.md`; the next turn receives it exactly once.
- A scoped fact selects the nearest existing nested file; absent nested files are not created.
- A newer candidate replaces an unchanged generated entry and preserves the old row.
- An externally edited entry remains byte-identical and becomes conflicted.
- Promotion appears in project diff/undo history without staling completed product criteria.
- Projectless Work and feature-off configurations never attempt memory writes.
- No scenario adds a model call.

### Token/quality validation

An A/B benchmark runs representative repeated tasks with identical Phase 5a behavior:

- baseline: `memory.auto_promote: false`;
- treatment: `memory.auto_promote: true`.

Measure total input/output tokens, instruction tokens added, repeated discovery calls, total tool rounds, completion rate, verification success, unsupported completion claims, and incorrect/stale/conflicted memory incidents. Treatment succeeds only when repeated context or tool work decreases without reducing completion or verification quality. Any incorrect automatic memory is a correctness failure.

## Rollout

Phase 5b ships off by default. Initial enablement is explicit per installation or controlled test cohort. Rollout should compare baseline, Phase 5a, and Phase 5a+5b before changing defaults.

There is no mandatory UI or chat notification. Existing Work inspection exposes candidates; developer diagnostics expose metadata-only promotion outcomes. Phase 6 may add review, correction, retention, deletion, and manual repair surfaces.

## Acceptance criteria

Phase 5b is complete when:

- Only eligible, verified, fresh project facts and approved decisions promote after successful Work completion.
- Promotion is inactive unless both Designed workflow and automatic promotion are enabled.
- Placement selects the nearest existing applicable `UMCODE.md`, may create only the root, and never touches foreign instruction files.
- User-authored or externally edited text is never overwritten automatically.
- Newer authoritative evidence replaces only an exact unchanged generated entry while preserving history.
- Filesystem writes are compare-and-swap, atomic, permission-preserving, recorded in project diff/undo history, and recoverable after interruption.
- Candidate and memory states remain consistent and retries are idempotent.
- Completed Work remains completed when promotion fails.
- The next applicable turn receives promoted guidance exactly once without another model call.
- Flag-off behavior remains compatible, diagnostics contain no semantic content, and all promotion overhead is visible in accounting.
- A/B validation demonstrates reduced repeated context or tool work without lower completion or verification quality.
