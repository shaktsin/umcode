# Phase 5b Curated Project Memory Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Deterministically promote verified, durable facts from completed Designed Work into concise `UMCODE.md` guidance so later tasks repeat less discovery and consume fewer tokens without weakening verification or overwriting user content.

**Architecture:** Add a feature-gated `internal/memory` pipeline after Work completion. Pure qualification, placement, and merge functions produce an immutable proposal; `internal/store` persists recoverable promotion operations; a per-project coordinator applies a compare-and-swap atomic file replacement through an idempotent project-change recorder; startup recovery reconciles interrupted operations by exact hashes. SQLite owns provenance and semantic identity, while `UMCODE.md` remains the only instruction surface and the existing root-to-leaf composer remains unchanged.

**Tech Stack:** Go, SQLite migration 0016, existing Work graph/project recorder/instruction composer, table-driven Go tests, deterministic fake-LLM end-to-end tests

**Spec:** `docs/superpowers/specs/2026-10-08-phase5b-curated-memory-design.md`

## Global Constraints

- Phase 5b only. Do not add a memory UI, manual review workflow, retention/eviction, model-based summarization, or Phase 6 repair tools.
- `memory.auto_promote` defaults to `false`; promotion is active only when it and `models.designed_workflow` are both true. The flag-off path must not query promotion tables, inspect candidates, or touch project files.
- Add no model call. Qualification, placement, merging, conflict handling, and recovery are deterministic.
- Only `UMCODE.md` may be read or written. Never scan or modify `AGENTS.md`, `CLAUDE.md`, or global `AGENT.md`.
- A completed Work stays completed even when promotion is rejected, stale, conflicted, pending repair, or fails unexpectedly.
- Preserve user-authored bytes and existing line endings. Replace only an exact generated bullet whose path, normalized bytes, and stored hash still match one active memory row.
- Serialize promotions by project in-process, but still use filesystem compare-and-swap because external editors and other processes are outside that lock.
- Persist bounded before/after bytes before filesystem mutation. Restart recovery must decide exclusively from recorded before/after hashes and current bytes.
- Promotion-generated file changes must be idempotent by operation ID and visible to existing diff/undo surfaces.
- Candidate text, generated bullets, evidence contents, tool output, secrets, and approval rationale must never enter logs or diagnostics.
- Bounds are bytes, not runes: semantic keys 128 bytes, candidate text 512 bytes, at most 16 scope paths of 512 bytes each, and complete instruction files at most 32 KiB. The configurable target defaults to 4096 and must be 1024–32768 bytes.
- Retain the Phase 5a legacy `scope` candidate field for persisted/in-flight compatibility. New requests use `scope_paths`; specifying both is invalid, and qualification canonicalizes legacy `scope` to one path.
- Every task follows Red → Green → refactor and ends in one focused commit. Run the named focused tests before each commit.

## Review Focus

- A path or symlink swap between placement, prepare, and rename must never escape the project root or write a non-`UMCODE.md` target.
- Duplicate/malformed managed sections, marker-looking user text, mixed newline styles, and edited/moved/deleted generated bullets must conflict without rewriting unrelated bytes.
- Concurrent completed Works with the same semantic key or target file must not create two active rows or lose an update.
- A crash after rename but before file-change/finalization must recover exactly once; repeated recovery must not duplicate history or mutate an unrelated third file state.
- Candidate evidence, approval, verification, and final workspace revision must be rechecked in the prepare transaction so qualification cannot commit stale facts.
- Flag-off, projectless, invalid feature-combination, and Works-without-candidates paths must perform no promotion filesystem I/O and add no model call.
- The deterministic A/B test must show fewer repeated discovery/tool rounds while asserting identical successful output and verification—not merely a smaller prompt.

---

### Task 1: Add configuration, protocol contracts, and migration 0016

**Files:**

- Create: `internal/store/migrations/0016_curated_memory.sql`
- Create: `internal/protocol/memory.go`
- Create: `internal/store/memory.go`
- Create: `internal/store/memory_test.go`
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`
- Modify: `internal/store/projects.go`
- Modify: `internal/store/work_test.go`

**Interfaces:**

- Consumes: config loading, Phase 5a Work/candidate IDs, existing `file_changes` records.
- Produces: bounded memory configuration, persistence DTOs, additive tables, operation-idempotent file-change storage.

- [ ] **Step 1: Write failing config and migration tests**

Add:

- `TestMemoryConfigDefaultsDisabled` proving `AutoPromote == false` and `TargetFileBytes == 4096` from `Default` and from a config that omits `memory`.
- `TestMemoryConfigValidation` accepting 1024 and 32768 and rejecting explicit 0, negative, 1023, and 32769. Because `Default` is unmarshaled into, omission retains 4096 while explicit zero overwrites it and fails validation; do not silently repair zero in `finalize`.
- `TestCuratedMemoryMigrationAndRoundTrip` against a fresh database.
- `TestCuratedMemoryMigrationPreservesDesignedWorkflow` using the existing pre-migration fixture pattern to prove migration 0016 preserves Phase 5a Work graph data.
- `TestPromotionFileChangeIsIdempotent` proving two inserts with the same nonempty promotion operation return the same row and one history entry, while ordinary empty-ID changes remain unrestricted.

Run:

```bash
go test ./internal/config ./internal/store -run 'Test(MemoryConfig|CuratedMemoryMigration|PromotionFileChange)' -count=1
```

Expected: FAIL because the config, tables, DTOs, and idempotency key do not exist.

- [ ] **Step 2: Define the public data-only contracts**

In `internal/protocol/memory.go`, add constants for categories, candidate outcomes, memory statuses, and operation states plus:

```go
type ProjectMemory struct {
    ID, ProjectID, WorkID, CandidateNodeID string
    SemanticKey, Category, TargetPath, Text, TextHash string
    Status, SourceRevision, EvidenceJSON string
    FileHashBefore, FileHashAfter, SupersededBy string
    CreatedAt time.Time
    PromotedAt *time.Time
}

type MemoryPromotionOp struct {
    ID, ProjectID, WorkID, CandidateNodeID, MemoryID string
    ThreadID, TurnID, TargetPath, State string
    FileHashBefore, FileHashAfter string
    BeforeBytes, AfterBytes []byte
    ErrorClass string
    CreatedAt, UpdatedAt time.Time
}
```

Keep filesystem/pure-function types inside `internal/memory`; protocol types are persistence and engine-boundary data only.

- [ ] **Step 3: Add the additive schema and store round trips**

Migration 0016 creates the two tables in the spec, project/operation lookup indexes, and a partial unique index allowing one `project_memories.status = 'active'` row per `(project_id, semantic_key)`. Use a project foreign key with cascade, but keep Work, candidate-node, thread, turn, and evidence provenance as immutable string IDs without foreign keys: deleting a source thread/Work must not delete durable project memory or its audit trail. Extend `DeleteProject` to remove promotion operations and memory rows in dependency order. Add nullable `promotion_op_id` to `file_changes` and a partial unique index where it is nonempty.

Add scanners and minimal CRUD in `internal/store/memory.go` for later tasks:

```go
func (s *Store) GetProjectMemory(ctx context.Context, id string) (protocol.ProjectMemory, error)
func (s *Store) ListActiveProjectMemories(ctx context.Context, projectID, targetPath string) ([]protocol.ProjectMemory, error)
func (s *Store) ListIncompleteMemoryPromotionOps(ctx context.Context) ([]protocol.MemoryPromotionOp, error)
```

Extend `store.FileChange` with `PromotionOpID string`. Make `RecordFileChange` use `INSERT ... ON CONFLICT(promotion_op_id) WHERE promotion_op_id IS NOT NULL AND promotion_op_id <> '' DO NOTHING`, then load and return the existing row ID on conflict.

- [ ] **Step 4: Wire and validate configuration**

Add `Config.Memory MemoryConfig` and:

```go
type MemoryConfig struct {
    AutoPromote     bool `yaml:"auto_promote"`
    TargetFileBytes int  `yaml:"target_file_bytes"`
}
```

Set the default in `Default`, validate the byte range, and deliberately do not reject `auto_promote: true` with Designed disabled—the engine must start safely and emit the inactive diagnostic in Task 7.

Run:

```bash
gofmt -w internal/protocol/memory.go internal/store/memory.go internal/store/memory_test.go internal/config/config.go internal/config/config_test.go internal/store/projects.go internal/store/work_test.go
go test ./internal/config ./internal/store -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/store/migrations/0016_curated_memory.sql internal/protocol/memory.go internal/store/memory.go internal/store/memory_test.go internal/config/config.go internal/config/config_test.go internal/store/projects.go internal/store/work_test.go
git commit -m "feat: add curated memory persistence"
```

### Task 2: Extend and qualify memory candidates as a pure function

**Files:**

- Create: `internal/memory/qualify.go`
- Create: `internal/memory/qualify_test.go`
- Modify: `internal/work/graph.go`
- Modify: `internal/work/graph_test.go`
- Modify: `internal/tools/work.go`
- Modify: `internal/tools/work_test.go`

**Interfaces:**

- Consumes: completed `protocol.WorkDetail`, final workspace revision/fingerprint, project root, active memories.
- Produces: immutable `memory.Proposal` or a metadata-only `memory.Outcome`; performs no I/O.

- [ ] **Step 1: Write failing candidate-contract tests**

Extend Phase 5a graph tests so `memory_candidate` accepts:

```go
type MemoryCandidateContent struct {
    Category       string   `json:"category"`
    SemanticKey    string   `json:"semantic_key"`
    Text           string   `json:"text"`
    ScopePaths     []string `json:"scope_paths,omitempty"`
    SourceRevision string   `json:"source_revision"`
    ReplacesMemory string   `json:"replaces_memory,omitempty"`
    Scope          string   `json:"scope,omitempty"` // legacy only
}
```

Test allowed fields/types, byte/count bounds, allowed categories, duplicate/absolute/escaping scope paths, both scope forms together, multiline/marker/heading/control-character bullets, secret-like strings, and the updated `work.update` schema. Legacy `scope` remains accepted but is not advertised in the new tool schema.

- [ ] **Step 2: Write the qualification matrix**

Use these pure contracts:

```go
type QualifyInput struct {
    Detail protocol.WorkDetail
    Candidate protocol.WorkNode
    ProjectRoot, FinalRevision string
    ActiveMemories []protocol.ProjectMemory
}

type Proposal struct {
    WorkID, CandidateNodeID, SemanticKey, Category, Text, SourceRevision string
    ScopePaths, EvidenceIDs []string
    ReplacesMemory string
}

type Outcome struct { Status, Reason string }
func Qualify(in QualifyInput) (Proposal, Outcome)
```

Table tests cover every allowed category and prove qualification requires: completed Work; active pending candidate; exactly one active same-Work `candidate_for` source; engine-owned active fact or approved decision; all candidate evidence present, active, same-Work, and available when vault-backed; passed/fresh applicable criteria; matching final revision; valid durable project-specific content; and absent/current/explicitly replaceable semantic identity.

Permanent unsafe/unsupported/generic/temporary/branch-local/secret cases return `rejected`; stale source/evidence/verification/revision returns `stale`; ambiguous source or edited replacement ownership returns `conflicted`; already-current exact memory returns `promoted` with no rewrite. Outcome reasons are fixed enums and contain no semantic text.

- [ ] **Step 3: Implement canonical decoding and qualification**

Normalize text to exactly `- ` plus one trimmed single-line bullet; normalize and sort scope paths after validating with existing project containment helpers. Copy and sort evidence IDs. Never include evidence summaries/bodies in `Proposal`.

Keep structural checks in Phase 5a `PrepareUpdate`; re-run all freshness and ownership predicates in `Qualify` because candidate creation may precede completion.

Run:

```bash
gofmt -w internal/memory/qualify.go internal/memory/qualify_test.go internal/work/graph.go internal/work/graph_test.go internal/tools/work.go internal/tools/work_test.go
go test ./internal/memory ./internal/work ./internal/tools -run 'Test(Candidate|Qualify|WorkUpdateSchema)' -count=1
```

Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add internal/memory/qualify.go internal/memory/qualify_test.go internal/work/graph.go internal/work/graph_test.go internal/tools/work.go internal/tools/work_test.go
git commit -m "feat: qualify curated memory candidates"
```

### Task 3: Resolve the safest applicable UMCODE.md target

**Files:**

- Create: `internal/memory/placement.go`
- Create: `internal/memory/placement_test.go`
- Modify: `internal/projects/instructions.go`
- Modify: `internal/projects/projects_test.go`

**Interfaces:**

- Consumes: project root, canonical scope paths, inventory of existing `UMCODE.md` files.
- Produces: a contained root-relative target path and whether root creation is required; performs no write.

- [ ] **Step 1: Write failing placement tests**

Test:

- empty/cross-cutting scopes choose root;
- one nested scope chooses the deepest existing ancestor `UMCODE.md`;
- multiple scopes choose their deepest common existing ancestor;
- disjoint scopes fall back to root;
- a missing nested file is never proposed for creation;
- a missing root may be proposed for creation;
- directories named `UMCODE.md`, symlinks escaping root, absolute paths, `..`, missing scope paths, and foreign instruction names are rejected/conflicted.

Add a narrow project helper that inventories only `UMCODE.md` using the same containment/symlink rules as instruction composition. Do not expose the old foreign-file scan behavior.

- [ ] **Step 2: Implement the pure resolver**

```go
type PlacementInput struct {
    ProjectRoot string
    ScopePaths []string
    Existing []string // canonical root-relative UMCODE.md paths
}
type Placement struct { RelativePath string; CreateRoot bool }
func ResolvePlacement(in PlacementInput) (Placement, Outcome)
```

Sort inventory and scopes before comparison so input enumeration order cannot affect the result. Return fixed reasons only.

Run:

```bash
gofmt -w internal/memory/placement.go internal/memory/placement_test.go internal/projects/instructions.go internal/projects/projects_test.go
go test ./internal/memory ./internal/projects -run 'Test(ResolvePlacement|InstructionInventory)' -count=1
```

Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add internal/memory/placement.go internal/memory/placement_test.go internal/projects/instructions.go internal/projects/projects_test.go
git commit -m "feat: resolve curated memory placement"
```

### Task 4: Implement the ownership-safe managed-section merger

**Files:**

- Create: `internal/memory/merge.go`
- Create: `internal/memory/merge_test.go`

**Interfaces:**

- Consumes: current file bytes, active rows for one target, qualified proposals, target byte ceiling.
- Produces: deterministic after-bytes, inserted/replaced/unchanged IDs, hashes, or a fixed conflict/pending outcome; performs no I/O.

- [ ] **Step 1: Write failing parser and ownership tests**

Use:

```go
type MergeInput struct {
    Current []byte
    Active []protocol.ProjectMemory
    Proposals []Proposal
    TargetBytes int
}
type MergeResult struct {
    After []byte
    Inserted, Replaced, Unchanged []string
    BeforeHash, AfterHash string
}
func Merge(in MergeInput) (MergeResult, Outcome)
```

Cover empty/root creation, appending the exact heading/marker, LF and CRLF preservation, no final newline, unrelated bytes before/after the section, duplicate suppression anywhere in the file, deterministic candidate-ID order, and byte—not rune—limits.

Ownership cases must prove only a byte-identical recorded bullet in the single well-formed managed section is replaceable. Edited, moved, deleted, duplicated, hash-mismatched, or target-mismatched entries conflict. Duplicate headings/markers, marker-before-heading, nested headings, extra marker-looking content, and malformed section boundaries conflict without output bytes.

- [ ] **Step 2: Implement parsing, reconciliation, and rendering**

Use SHA-256 hex for full before/after hashes and stored bullet text hashes. Normalize line endings only for ownership/duplicate comparison; render in the detected file style and preserve every unrelated byte slice exactly. Never sort existing bullets or repair malformed content.

An exact duplicate outside the section is `unchanged` and creates/activates the semantic memory row without adding another bullet. A result over `TargetBytes` returns `pending/size_limit` and no after-bytes.

Run:

```bash
gofmt -w internal/memory/merge.go internal/memory/merge_test.go
go test ./internal/memory -run 'TestMerge' -count=1
```

Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add internal/memory/merge.go internal/memory/merge_test.go
git commit -m "feat: merge verified project memory"
```

### Task 5: Make promotion preparation and finalization transactional

**Files:**

- Modify: `internal/store/memory.go`
- Modify: `internal/store/memory_test.go`
- Modify: `internal/store/work.go`

**Interfaces:**

- Consumes: a completed Work ID, candidate/proposal, target, exact before/after snapshots and hashes.
- Produces: one prepared operation; later exactly-once active-memory/candidate/operation transitions.

- [ ] **Step 1: Write failing transaction and concurrency tests**

Add tests for:

- prepare atomically rechecking completed Work, pending active candidate, evidence membership/availability, final revision, replacement ownership, and current active semantic key;
- stale predicates inserting no operation/memory row;
- two concurrent prepares for one `(project_id, semantic_key)` yielding one promotable operation;
- finalization atomically activating the new row, superseding the replaced row, transitioning the candidate to `promoted`, and committing the operation;
- conflict/pending-repair transitions updating the operation, candidate, and provisional memory consistently;
- repeated finalization returning the already committed result without another state transition.

- [ ] **Step 2: Add explicit store commands**

```go
type PrepareMemoryPromotion struct { /* IDs, proposal, target, exact snapshots/hashes */ }
func (s *Store) PrepareMemoryPromotion(ctx context.Context, in PrepareMemoryPromotion) (protocol.MemoryPromotionOp, error)
func (s *Store) MarkMemoryFileWritten(ctx context.Context, opID string) error
func (s *Store) CommitMemoryPromotion(ctx context.Context, opID string, promotedAt time.Time) error
func (s *Store) FailMemoryPromotion(ctx context.Context, opID, state, candidateStatus, errorClass string) error
```

Introduce typed `ErrMemoryConflict` and `ErrMemoryStale`; callers map them to fixed metadata outcomes. Re-query all mutable predicates inside the prepare transaction rather than trusting Task 2's earlier snapshot.

For exact duplicates, store before==after and allow finalization without a filesystem step. For replacement, insert the provisional row and identify the prior row during prepare; enforce the partial unique active index at commit.

Run:

```bash
gofmt -w internal/store/memory.go internal/store/memory_test.go internal/store/work.go
go test ./internal/store -run 'TestMemory(Promotion|Prepare|Finalize|Concurrency)' -count=1
```

Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add internal/store/memory.go internal/store/memory_test.go internal/store/work.go
git commit -m "feat: transact curated memory promotion"
```

### Task 6: Implement atomic writes, project history, and crash recovery

**Files:**

- Create: `internal/memory/service.go`
- Create: `internal/memory/service_test.go`
- Create: `internal/memory/recovery.go`
- Create: `internal/memory/recovery_test.go`
- Modify: `internal/projects/changes.go`
- Create: `internal/projects/changes_test.go`

**Interfaces:**

- Consumes: store commands, project/instruction services, a completed Work snapshot, originating thread/turn IDs.
- Produces: synchronous promotion report, atomic `UMCODE.md` change, idempotent diff/undo row, recoverable operation states.

- [ ] **Step 1: Write failing coordinator boundary tests**

Define:

```go
type Request struct { Project protocol.Project; WorkID, ThreadID, TurnID string }
type Report struct { Promoted, Rejected, Stale, Conflicted, Pending, Inserted, Replaced, Unchanged int; BytesBefore, BytesAfter int }
type Service struct { /* store, projects, target bytes, per-project locks, injected fs seam */ }
func (s *Service) PromoteCompleted(ctx context.Context, req Request) Report
func (s *Service) Recover(ctx context.Context) Report
```

Tests inject failures before prepare, before recheck, temp creation/write/chmod/fsync, before rename, after rename, during file-change recording, and during finalization. Assert original bytes remain for all pre-rename failures; post-rename failures retain exact recoverable state; Work remains completed throughout.

Add concurrency tests with two Works targeting one file and an external edit between read and CAS. No update may be lost, and third-state bytes must remain untouched.

- [ ] **Step 2: Add strict promotion recording to the project recorder**

```go
func (r *Recorder) RecordPromotion(ctx context.Context, promotionOpID, abs string, before *string) (protocol.FileChangeData, error)
```

Unlike existing best-effort `Record`, this returns persistence errors. It uses `FileChange.PromotionOpID`, emits only after the idempotent row exists, and returns the same payload without duplicate emission when recovery finds an existing row. Preserve ordinary `Record` behavior.

- [ ] **Step 3: Implement contained compare-and-swap atomic replacement**

Under a per-project mutex: resolve the target again through `projects.Resolve`, reject a symlink/non-regular-file swap, compare the full current hash to `before_hash`, create a restrictive sibling temp file, write and fsync it, copy existing permissions when applicable (use `0644` for a newly created root file), rename atomically, and fsync the parent where supported. Remove only this call's temp on failure.

After rename, call `RecordPromotion`, mark file-written, then finalize. A zero-byte-change duplicate skips temp/rename but still finalizes provenance.

- [ ] **Step 4: Implement startup reconciliation**

For each incomplete op in stable creation/ID order:

- current hash == before: retry the recorded exact after-bytes, record history, finalize;
- current hash == after: idempotently record history and finalize;
- otherwise: mark op/memory/candidate conflicted and never write.

`pending_repair` operations use the same rules. Verify repeated `Recover` calls are no-ops after success/conflict and create one file-change row.

Run:

```bash
gofmt -w internal/memory/service.go internal/memory/service_test.go internal/memory/recovery.go internal/memory/recovery_test.go internal/projects/changes.go internal/projects/changes_test.go
go test ./internal/memory ./internal/projects -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/memory/service.go internal/memory/service_test.go internal/memory/recovery.go internal/memory/recovery_test.go internal/projects/changes.go internal/projects/changes_test.go
git commit -m "feat: apply recoverable memory promotions"
```

### Task 7: Trigger promotion after Work completion and expose safe diagnostics

**Files:**

- Modify: `internal/work/work.go`
- Modify: `internal/work/work_test.go`
- Modify: `internal/engine/engine.go`
- Modify: `internal/engine/turn.go`
- Modify: `internal/engine/workflow_test.go`
- Create: `internal/engine/memory_test.go`

**Interfaces:**

- Consumes: the Work ID newly completed by `work.Service.End`, engine project/thread/turn context.
- Produces: one synchronous promoter invocation before turn-completed publication, metadata-only counters/logs, startup recovery.

- [ ] **Step 1: Write failing lifecycle/feature-gate tests**

Change the Work boundary to:

```go
func (s *Service) End(ctx context.Context, threadID, turnStatus string, paused bool, root string) (completedWorkID string, err error)
```

Tests prove it returns a nonempty ID only when this call newly closes the Work; no-open, already-closed, blocked, failed, interrupted, and paused cases return empty.

Engine tests prove:

- both flags on + project Work + candidate invokes promotion exactly once after `CloseWork` commits and before turn-completed publication;
- flag off, Designed off, projectless Work, no candidate, and no newly completed Work perform no promotion store or filesystem access;
- `auto_promote: true` with Designed off logs one metadata-only inactive diagnostic, exposes no semantic text, and continues safely;
- promoter panic/error increments a failure counter/log classification but the turn and Work remain completed;
- startup calls recovery only when both flags are valid and enabled.

- [ ] **Step 2: Wire the service and startup recovery**

Add `Memory *memory.Service` and atomic promotion/recovery failure counters to `Engine`. Construct the service only for the valid enabled combination. Run recovery during `New` after stale-turn/approval housekeeping and before serving work; a recovery error is logged and does not prevent startup.

In `finishTurn`, use the ID returned by `Work.End`; if nonempty, load the project and call `PromoteCompleted` synchronously with the current thread/turn IDs. The memory service loads the completed canonical Work detail and derives its last recorded workspace/repository fingerprint itself. Keep promotion outside the Work close transaction. Publish turn completion regardless of the report.

Log only counts, status/reason enums, IDs, paths, byte counts, and hashes. Estimate instruction tokens as `(bytes+3)/4`; do not subtract savings from request usage.

Run:

```bash
gofmt -w internal/work/work.go internal/work/work_test.go internal/engine/engine.go internal/engine/turn.go internal/engine/workflow_test.go internal/engine/memory_test.go
go test ./internal/work ./internal/engine -run 'Test(EndReturnsCompletedWork|Memory|Promotion|Recovery)' -count=1
```

Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add internal/work/work.go internal/work/work_test.go internal/engine/engine.go internal/engine/turn.go internal/engine/workflow_test.go internal/engine/memory_test.go
git commit -m "feat: promote memory after work completion"
```

### Task 8: Prove end-to-end behavior and token/quality benefit

**Files:**

- Modify: `internal/engine/memory_test.go`
- Create: `internal/engine/memory_benchmark_test.go`
- Modify: `docs/GO_ENGINE.md`

**Interfaces:**

- Consumes: the complete Phase 5b pipeline and existing prompt instruction composition.
- Produces: regression proof that later turns receive memory exactly once and repeat less discovery without quality loss; operator documentation.

- [ ] **Step 1: Add failing end-to-end scenarios**

Drive real store/engine/project services with deterministic fake providers and temporary repositories. Cover:

- verified root command promotion and exact-once appearance in the next turn prompt;
- nearest existing nested `UMCODE.md` placement without nested creation;
- exact replacement preserving the historical row;
- externally edited generated bullet preserved byte-for-byte and conflicted;
- promotion appearing in project diff and undo without staling already completed product criteria;
- restart after rename completing one history row and one active memory;
- no added model call in every scenario.

Run:

```bash
go test ./internal/engine -run 'TestCuratedMemoryE2E' -count=1
```

Expected: FAIL until all integration paths are connected.

- [ ] **Step 2: Add a deterministic A/B benefit test**

`TestCuratedMemoryReducesRepeatedDiscoveryWithoutQualityLoss` runs the same two-turn coding scenario twice:

- baseline: auto-promotion off;
- treatment: auto-promotion on.

The scripted provider requests the discovery tool only when the verified command is absent from project instructions. Assert identical final answer/changed artifact, identical passing verification and supported-completion status, no promotion-specific model call (treatment model calls must be no greater than baseline), and treatment with fewer discovery calls/tool rounds and fewer cumulative second-turn input bytes across all loop requests after accounting for the concise promoted bullet. Record token estimates as test diagnostics; do not make network calls or depend on model variance.

- [ ] **Step 3: Document activation, guarantees, and observability**

Update `docs/GO_ENGINE.md` with the two required flags, default-off rollout, exact `UMCODE.md` ownership boundary, failure/recovery behavior, status counters, and the A/B validation command. State explicitly that foreign instruction files are never scanned and that no model call is added.

Run:

```bash
gofmt -w internal/engine/memory_test.go internal/engine/memory_benchmark_test.go
go test ./... -count=1
git diff --check
```

Expected: PASS with no race-independent flakes.

- [ ] **Step 4: Commit**

```bash
git add internal/engine/memory_test.go internal/engine/memory_benchmark_test.go docs/GO_ENGINE.md
git commit -m "test: validate curated memory benefits"
```

### Task 9: Final review and branch verification

**Files:**

- Review: all Phase 5b commits and files above
- Modify: only defects found by review

**Interfaces:**

- Consumes: complete branch against the Phase 5b spec and Review Focus.
- Produces: independently reviewed, fully verified branch ready for PR creation only after user approval.

- [ ] **Step 1: Request whole-branch code review**

Use `superpowers:requesting-code-review`. Review the branch diff against the spec, Global Constraints, and every Review Focus item. Require explicit inspection of all transaction boundaries, path/symlink checks, generated-entry ownership rules, diagnostic redaction, feature-off I/O, and crash points.

- [ ] **Step 2: Resolve findings with TDD**

For every valid finding, add or strengthen a failing regression test, run it red, implement the smallest correction, and rerun it green. Use `superpowers:receiving-code-review` when feedback is ambiguous or technically questionable. Commit each independent correction separately.

- [ ] **Step 3: Run fresh verification**

Use `superpowers:verification-before-completion`, then run:

```bash
go test ./... -count=1
go vet ./...
git diff --check origin/master...HEAD
git status --short
```

Inspect the final diff and confirm: no content-bearing logs, no foreign instruction reads, no model call, no uncommitted files, and every acceptance criterion has a named test.

- [ ] **Step 4: Finish the development branch**

Use `superpowers:finishing-a-development-branch`. Do not push, open, merge, or close a pull request without the user's separate instruction.
