# Work Model and Verification Ledger (Token Efficiency Phase 2)

**Status:** Design approved in conversation; pending written-spec review

**Date:** 2026-10-02

**Parent spec:** `2026-09-30-quality-first-token-efficiency-design.md` (Phase 2: Work Model and verification ledger)

**Builds on:** Phase 1 (`feat/token-efficiency-phase1`)

## Summary

UMCode records what the engine can observe about a unit of user work — the goal, the checks it must pass, the files it changed, the verification attempts and the failures — in SQLite, without any extra model call and without changing what the model receives or what the user sees in chat. Later phases read this record to resume work, build compact context and validate completion claims.

## Goals

- Persist a structured Work record per objective, spanning turns until resolved.
- Record engine-observable events automatically from existing tool paths.
- Make verification attempts append-only and traceable to criteria and evidence.
- Classify workflow depth deterministically (Direct, Guided) with no LLM call.
- Expose the record on demand through read-only protocol methods.
- Keep normal chat unchanged: no new items, notifications or prompt text.

## Non-goals

- A model-facing `work.update` tool (Phase 4/5).
- Raw evidence blobs, the vault, staleness propagation, retention (Phase 3).
- Context compilation or any change to the request sent to the model (Phase 4).
- Designed workflow, decision nodes, memory candidates (Phase 5).
- UI for the record (Phase 6).
- Stopping mutation when the audit database fails (needs the Phase 3 action ledger).

## Lifecycle

A **work** belongs to one thread and spans turns until resolved.

1. On turn start, if the thread has an `open` work the turn continues it; otherwise a new work opens with `goal` = the turn's user text (stored as a `goal` node).
2. On turn end the work is evaluated:
   - `completed` — the turn status is `completed` **and** no criterion is unresolved (see below).
   - otherwise it stays `open` (interrupted, paused, failed, or unresolved criteria).
3. Archiving a thread marks its `open` work `abandoned`. Deleting a thread deletes its works (cascade).

A criterion is **unresolved** when it has no verification attempt, its latest attempt's status is not `passed`, or an `artifact` change was recorded after its latest passing attempt. A work with no criteria is resolved when its turn completed.

## Workflow depth

`works.workflow_depth` is `direct` or `guided`. A work starts `direct` and escalates to `guided` on the first of: a workspace-mutating tool call (file write/edit, or a shell command assessed above green risk), or a `verification.plan` call. It never downgrades. `designed` is reserved and never assigned in Phase 2. No model call is made.

## Schema

Migration `internal/store/migrations/0013_work_model.sql`, additive, `CREATE TABLE IF NOT EXISTS`, timestamps as RFC3339 `TEXT` like existing tables, IDs from `store.NewID`.

```text
works(id, thread_id→threads ON DELETE CASCADE, project_id→projects ON DELETE SET NULL,
      kind, status[open|completed|abandoned], workflow_depth[direct|guided],
      goal, created_at, completed_at)
work_nodes(id, work_id→works CASCADE, kind, title, content_json, status,
           confidence, revision, valid_from, valid_until, superseded_by,
           created_at, updated_at)
work_edges(work_id→works CASCADE, from_node_id, relation, to_node_id)
evidence(id, work_id→works CASCADE, node_id, kind, source_uri, source_revision,
         content_hash, summary, confidence, observed_at, stale_at)
verification_attempts(id, work_id→works CASCADE, criterion_node_id NULL, check_type,
         command, environment_json, status[passed|failed|blocked|not_run],
         exit_code NULL, evidence_id NULL, started_at, finished_at)
```

Indexes: `works(thread_id, status)`, `work_nodes(work_id, kind)`, `work_edges(work_id, from_node_id)`, `evidence(work_id)`, `verification_attempts(work_id, criterion_node_id, started_at)`.

Node kinds used in Phase 2: `goal`, `criterion`, `artifact`, `fact`. Edge relations: `requires` (goal → criterion) and `serves` (artifact → goal). Node status: criteria `pending|passed|failed|blocked`; artifacts `active`; facts `active`.

## Recording

A new package `internal/work` owns the store access and rules (`Service`). The engine calls it at three seams; the package never imports the engine.

| Seam | Records |
|---|---|
| Turn start | open or continue the work; add `goal` node when opening |
| `runTool` completion | file write/edit → `artifact` node + `file_change` evidence (path, post-change content hash); `verification.plan` → one `criterion` node per planned check with `requires` edge; `verification.run` / `browser.verify` → `verification_attempts` row (criterion matched by command string, else NULL) + `verification_output` evidence (summary capped at 2 KB, no raw output stored); failed or denied tool → `fact` node with `{tool, error}` as a blocker plus `tool_error` evidence; depth escalation |
| Turn finish | evaluate and set `completed_at`/status per the lifecycle |

Recording is best-effort in Phase 2: an error is logged and counted and never fails the turn or the tool call. Evidence stores a summary, hash and provenance only; a hash with no stored object must never be presented as retrievable raw evidence. Fact nodes for failed tools do not by themselves keep a work open.

## Protocol

Read-only, no notifications:

- `work/list` `{threadId}` → works newest first with id, status, depth, goal, timestamps.
- `work/get` `{workId}` → the work with its nodes, edges, evidence and verification attempts.

Types live in `internal/protocol`. Absence of a work is an empty list, not an error.

## Compatibility and safety

- Existing threads have no works; behavior is unchanged until a new turn runs.
- Phase 1 golden system prompts and request accounting must stay byte-identical: nothing here touches the model request.
- Recording never reads or stores secrets beyond the 2 KB capped summary of verification output; shell command text is stored as the criterion/attempt command only.

## Testing

- Store: migration applies on a fresh and an existing database; cascade deletes; append-only attempts.
- `internal/work` unit tests: open/continue/close rules, depth escalation (never downgrades), unresolved-criterion rules including "artifact changed after passing attempt reopens".
- Engine integration (existing fake-provider e2e harness): edit + passing verification completes the work; failing verification leaves it open and the next turn continues it; interrupted turn leaves it open; a failed non-verification tool does not keep it open; recording failure does not fail the turn.
- Protocol: `work/list` and `work/get` on empty, open and completed works.
- Regression: Phase 1 prompt goldens unchanged.

## Acceptance criteria

- A completed, fully verified turn yields one `completed` work whose criteria, attempts and artifacts are retrievable via `work/get`.
- A failed or unrun check leaves the work `open` and the next turn in the thread continues it.
- A file change after a passing check makes that criterion unresolved again.
- No change in chat output, prompt text or model requests.
