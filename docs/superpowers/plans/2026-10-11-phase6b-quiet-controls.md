# Phase 6b Quiet Controls Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans for native execution, or superpowers:subagent-driven-development if the user selects that method. Steps use checkbox syntax for tracking.

**Goal:** Deliver quiet turn inspection, safe project memory management, and vault retention controls.

**Architecture:** Three sequential, independently testable deliverables extend existing flows: turn details, memory review, and storage controls. Protocol/store contracts precede engine integration and frontend presentation. The memory service owns file reconciliation; the collector owns reclamation.

**Tech Stack:** Go, SQLite migrations, JSON-RPC, Svelte 5, TypeScript, Vitest.

**Spec:** `docs/superpowers/specs/2026-10-11-phase6b-quiet-controls-design.md`

## Global Constraints

- One global master switch enables all optimization components automatically. No component opt-ins or extra optimization approvals are introduced.
- Normal action permissions remain effective.
- Missing values are nullable or carry an availability state, never inferred from zero.
- Retention values are positive whole-day values bounded to 1–3650 days; defaults: raw 30, stale 30, blob 90, redacted 7.
- No paid qualification, default changes, claimed percentage savings, project purge, or forced deletion of referenced objects.
- Inspection and explicit management remain accessible with optimization disabled.
- Existing active turns retain already-admitted context; subsequent turns observe changes.
- Work in the existing isolated checkout on `codex/phase6b-quiet-controls`; use `/private/tmp/umcode-go-cache` for Go cache. Do not edit the primary checkout.

## Review Focus

- Multi-model fallback with one unknown price must produce partial cost, not an apparently complete total (Task 2).
- Delayed frontend requests must not populate another turn/project or resurrect a closed inspector tab (Tasks 3 and 6).
- An instruction bullet edited outside the application must survive retirement and produce an explicit remaining-instruction warning (Task 5).
- Source chat deletion must not orphan evidence supporting live memory (Task 7).
- A new reference between collection snapshot and deletion must prevent reclamation (Task 7).

## Delivery A: Turn inspection

### Task 1: Versioned turn-detail storage and protocol

**Files:** Create `internal/protocol/turn_details.go`, `internal/store/turn_details.go`, `internal/store/turn_details_test.go`, `internal/store/migrations/0019_turn_details.sql`; modify `internal/protocol/methods.go`.

**Interfaces:** Produce `TurnDetailsParams{ThreadID, TurnID string}`, `TurnDetailsResult{Turn Turn; Diagnostics *TurnDiagnostics; Availability string}` and `Store.SaveTurnDiagnostics(ctx context.Context, turnID string, d protocol.TurnDiagnostics) error`, `Store.ReadTurnDetails(ctx context.Context, p protocol.TurnDetailsParams) (protocol.TurnDetailsResult, error)`.

`TurnDiagnostics` version 1 contains admission policy/source, bounded request diagnostics, omission counts and completeness. Request diagnostics identify phase/ordinal/model, nullable token/cost measurements with origins and availability, selected/submitted source references, schema names, call counts and fallback/reducer summaries. Limits: 128 request records, 128 source references and 128 tool names per record; omitted counts remain visible. Raw bodies are excluded. Cost states are `known`, `partial`, `unavailable`; diagnostic availability is `recorded`, `partial`, `not_recorded`. Store data lives separately from message items and cascades with turn deletion.

- [x] Write `TestTurnDetailsRoundTrip`, `TestTurnDetailsLegacy`, `TestTurnDetailsWrongThread`, `TestTurnDetailsBounds`: assert version/policy survive reopen, legacy diagnostics are nil with `not_recorded`, mismatched ownership fails, and cap overflow increments omitted counts.
- [x] Run `GOCACHE=/private/tmp/umcode-go-cache go test ./internal/store -run TestTurnDetails -count=1`; verify failure before implementation.
- [x] Implement migration and typed contracts, shared normalization before persistence, scoped SQL lookup and legacy usage loading. Reject unsupported diagnostic versions on writes.
- [x] Rerun the focused tests; expect PASS. Check JSON round trips distinguish absent measurements from measured zero.
- [x] Commit `feat: persist versioned turn inspection snapshots`.

### Task 2: Capture real execution diagnostics and expose inspection

**Files:** Create `internal/engine/turn_details.go`, `internal/engine/turn_details_test.go`; modify `internal/engine/turn.go`, `internal/engine/compile.go`, `internal/engine/retrieval.go`, `internal/engine/compaction.go`, `internal/engine/title.go`, `internal/server/routes.go`, and accounting helpers identified from `measureRequest`/`InsertUsage` call sites.

**Interfaces:** Consume Task 1. Produce `Engine.TurnDetails(ctx context.Context, p protocol.TurnDetailsParams) (protocol.TurnDetailsResult, error)` and JSON-RPC `turn/details`. Internal per-turn recorder: `recordRequest(d protocol.RequestDiagnostics)` and `flush(ctx context.Context) error`, owned by the turn rather than global mutable state.

- [x] Write `TestTurnDetailsAdmissionSnapshot`, `TestTurnDetailsPartialPricing`, `TestTurnDetailsInterrupted`, `TestTurnDetailsSensitiveBodies`, `TestTurnDetailsCaptureFailure`: assert toggling settings does not alter prior policy; known plus unknown prices yield partial cost; failed requests retain available usage; secret prompt/tool bodies are absent; persistence failure does not fail the task.
- [x] Run `GOCACHE=/private/tmp/umcode-go-cache go test ./internal/engine -run TestTurnDetails -count=1`; verify RED.
- [x] Snapshot policy at admission; persist initial snapshot and incremental bounded request records. Capture accounting at existing request/context/tool boundaries, distinguish retrieval selection from submitted membership, preserve usage provenance at pricing time and record all attributable model phases. For unidentifiable legacy/auxiliary coverage mark partial rather than asserting complete totals. Flush on success, failure and interruption with a bounded cleanup context. Add typed route and ownership errors.
- [x] Rerun engine tests and add server route coverage for valid/mismatched IDs; expect PASS.
- [x] Commit `feat: expose recorded turn optimization details`.

### Task 3: Response action and inspector UI

**Files:** Modify `app/frontend/src/lib/types.ts`, `components/ItemView.svelte`, `components/Inspector.svelte`, `stores/inspector.svelte.ts`; create `components/TurnDetails.svelte`, `turnDetails.ts`, `turnDetails.test.ts` under `app/frontend/src/lib`.

**Interfaces:** Consume `turn/details`; produce `inspector.openTurnDetails(threadId: string, turnId: string): Promise<void>`. Tab key is `turn-details:<turnId>` and kind is `turnDetails`. A pure controller owns request generation and typed result state; no new state framework.

- [x] Write `deduplicates same turn`, `ignores stale and closed-tab responses`, `renders unknown separately from zero`, `refreshes partial running turn`, `retries failures` controller tests with deferred RPC promises and fixtures from Task 1.
- [x] Run `npm test -- turnDetails.test.ts` in `app/frontend`; verify RED.
- [x] Add response `⋯` menu with “Turn details”, accessible keyboard/focus behavior and existing copy action. Implement inspector tab and overview, usage, context, tools, collapsed developer sections. Use exact copy “Not recorded for this turn” and “Cost unavailable”; label partial/estimated values. Reopen refreshes running turns; deleted turns show a clear unavailable error. Do not auto-open or add chat notifications.
- [x] Rerun tests and `npm run check`; expect PASS. Inspect long IDs/source lists at narrow width for wrapping and bounded scrolling.
- [x] Commit `feat: add on-demand turn details inspector`.

## Delivery B: Memory review and management

### Task 4: Memory management contracts and durable lineage

**Files:** Create `internal/protocol/memory_management.go`, `internal/store/memory_management.go`, `internal/store/memory_management_test.go`, `internal/store/migrations/0020_memory_management.sql`; modify `internal/protocol/methods.go`, `internal/store/memory.go`.

**Interfaces:** Produce `MemoryListParams{ProjectID, Cursor string; IncludeHistory bool; Limit int}`, `MemoryListResult{Entries []MemoryReviewEntry; NextCursor string}`, `MemoryChangeParams{ProjectID, MemoryID, ExpectedTextHash, ExpectedTargetHash, Text string}` and `MemoryChangeResult{Entry MemoryReviewEntry; InstructionRemains bool; Warning string}`. API names: `project/memory/list`, `project/memory/correct`, `project/memory/retire`. List defaults 50, maximum 100, stable `(created_at,id)` cursor. DTOs use explicit lower-camel JSON fields rather than exposing internal promotion snapshots.

Persist revision/lineage, user correction provenance and retirement tombstones keyed by project/semantic key/source revision/text hash. Changes create immutable audit entries. Corrected successor is explicitly user-authored and unverified; it cannot masquerade as an automatically verified promotion. Produce `Store.ListMemoryReview(ctx context.Context, p protocol.MemoryListParams) (protocol.MemoryListResult, error)` and transactional helpers used only by Task 5.

- [x] Write `TestMemoryReviewPagination`, `TestMemoryReviewProjectIsolation`, `TestMemoryRetirementLineage`: assert active/history separation, stable pages with same timestamps, invalid cursors rejected and tombstones distinguish unchanged candidates from genuinely new evidence.
- [x] Run `GOCACHE=/private/tmp/umcode-go-cache go test ./internal/store -run 'TestMemoryReview|TestMemoryRetirement' -count=1`; verify RED.
- [x] Implement DTOs, migration and scoped queries. Keep audit/provenance records after source work deletion. Define mutation prepare/file-written/commit states and explicit correction/retirement operation kind for recovery.
- [x] Rerun tests; expect PASS.
- [x] Commit `feat: add project memory review and change lineage`.

### Task 5: Recoverable correction and retirement service

**Files:** Create `internal/memory/manage.go`, `internal/memory/manage_test.go`, `internal/engine/memory_management.go`; modify `internal/memory/merge.go`, `recovery.go`, `qualify.go`, `service.go`, `internal/store/memory_management.go`, `internal/server/routes.go`.

**Interfaces:** Consume Task 4. Produce `Service.Correct(ctx context.Context, p protocol.MemoryChangeParams) (protocol.MemoryChangeResult, error)` and `Service.Retire(ctx context.Context, p protocol.MemoryChangeParams) (protocol.MemoryChangeResult, error)`; engine wrappers resolve project roots and reject wrong-project IDs. Mutation locks coordinate with promotion for the same target.

- [x] Write `TestManageMemoryCorrect`, `TestManageMemoryRetireGenerated`, `TestManageMemoryUserOwned`, `TestManageMemoryExternalEdit`, `TestManageMemoryRecovery`, `TestManageMemorySuppressOldCandidate`: assert exact unrelated bytes unchanged, successor provenance unverified/user-authored, generated bullet removed only with ownership proof, externally edited/adopted text survives with `InstructionRemains`, stale hash conflicts, restart repairs each crash boundary and old candidates cannot re-promote.
- [x] Run `GOCACHE=/private/tmp/umcode-go-cache go test ./internal/memory -run TestManageMemory -count=1`; verify RED.
- [x] Implement validated correction text bounded to existing instruction limits and restricted to a single valid memory bullet. Use compare-and-swap/atomic file replacement and durable journal. Retire the record even when user-owned text must remain, returning the warning. Reject concurrent stale mutations; extend startup recovery and qualification tombstone checks. Check active promotion/correction operations before allowing conflicting changes.
- [x] Rerun memory/store tests; add engine/server tests proving management works with optimization disabled and remains project-scoped. Expect PASS.
- [x] Commit `feat: safely correct and retire project memory`.

### Task 6: Project memory UI

**Files:** Create `app/frontend/src/lib/memoryReview.ts`, `memoryReview.test.ts`, `panels/ProjectMemory.svelte`; modify `panels/ProjectSettings.svelte`, `types.ts`.

**Interfaces:** Consume Task 4 RPC contracts. Produce controller methods `load(projectId: string)`, `loadMore()`, `correct(entry: MemoryReviewEntry, text: string)`, `retire(entry: MemoryReviewEntry)` and state for rows/cursor/busy/error/conflict. Carry expected hashes from the fetched entry, never current mutable UI state alone.

- [x] Write `ignores old project responses`, `paginates without duplicates`, `keeps failed edits`, `refreshes conflicts`, `shows instruction remains`, `does not claim correction verification` with deferred mocked RPC.
- [x] Run `npm test -- memoryReview.test.ts`; verify RED.
- [x] Implement active/history list, provenance links, explicit correction editor and retirement action. No automatic review prompts in chats. Preserve editor contents on failures and show conflict refresh. For remaining instructions offer existing inspector file navigation scoped to the selected project. Disable duplicate mutation submissions and reset state on project change.
- [x] Rerun tests and `npm run check`; expect PASS.
- [x] Commit `feat: add project memory review controls`.

## Delivery C: Storage management

### Task 7: Durable memory evidence references and collector safety

**Files:** Create `internal/store/migrations/0021_memory_vault_refs.sql`; modify `internal/store/vault.go`, `internal/store/memory.go`, `internal/store/memory_management.go`, `internal/work/gc.go`, `internal/work/service.go`; extend `internal/store/vault_test.go`, `internal/work/gc_test.go`, memory promotion tests.

**Interfaces:** Produce durable `memory_vault_refs(memory_id, vault_hash)` with foreign keys/unique key; `ReferencedVaultHashes` includes live memory and pending mutation references independent of source evidence rows. Backfill resolvable existing active/pending memory evidence before collection. Unresolvable historical evidence is reported as unavailable rather than fabricated. Promotion/correction preparation pins evidence before source deletion can remove it.

Use one shared vault lifecycle lock through the work service for reference-changing operations and collection, plus final transactional reference recheck; document lock order memory-target then vault-lifecycle then database. Never hold a database transaction while acquiring those locks. Audit all reference writers and deletion paths; do not rely solely on a test-hook snapshot.

- [x] Write `TestLiveMemoryPinsVaultAfterSourceDeletion`, `TestRetiredMemoryReleasesOnlyUnusedReferences`, `TestGCConcurrentReferenceProtection`, `TestPendingMemoryMutationPinsEvidence`, `TestMemoryReferenceBackfill` with aged objects. Assert live/pending/new references survive, expired truly unreferenced objects are reclaimed, and two memories sharing a hash cannot release each other's pin.
- [x] Run `GOCACHE=/private/tmp/umcode-go-cache go test ./internal/store ./internal/work ./internal/memory -run 'TestLiveMemory|TestRetiredMemory|TestGCConcurrent|TestPendingMemory|TestMemoryReference' -count=1`; verify RED.
- [x] Implement durable pins, backfill and lock/recheck protocol across promotion, management, deletion and GC. Keep collector reconciliation/crash recovery behavior intact; do not touch project files.
- [x] Rerun focused tests and race-check changed lifecycle paths; expect PASS.
- [x] Commit `fix: preserve live memory evidence during vault collection`.

### Task 8: Retention settings and manual cleanup API/UI

**Files:** Create `internal/protocol/storage.go`, `internal/engine/storage.go`, `internal/engine/storage_test.go`, `app/frontend/src/lib/storageSettings.ts`, `storageSettings.test.ts`, `panels/settings/StorageControls.svelte`; modify `internal/protocol/methods.go`, `internal/engine/engine.go`, `internal/work/gc.go`, `internal/server/routes.go`, `panels/settings/TokenOptimization.svelte`, frontend `types.ts`.

**Interfaces:** Produce `RetentionDays{Raw, Stale, Blob, Redacted int}` with explicit JSON names, `StorageSettingsResult{Retention RetentionDays; Source string; Stats VaultStats}`, `Engine.StorageSettings(ctx context.Context) (protocol.StorageSettingsResult, error)`, `Engine.SetStorageRetention(ctx context.Context, p protocol.RetentionDays) (protocol.StorageSettingsResult, error)`, `Engine.CleanupStorage(ctx context.Context) (protocol.StorageCleanupResult, error)`. RPC: `settings/storage/get`, `settings/storage/setRetention`, `storage/cleanup`; cleanup result includes deleted count, freed bytes and refreshed stats. Settings key `storage.retention_days` stores the complete validated object atomically.

- [x] Write `TestStorageRetentionValidation`, `TestStorageRetentionPersistence`, `TestStorageCollectorPolicySnapshot`, `TestStorageCleanupReport`: reject missing/fractional/0/negative/3651-day values, preserve old settings on rejection, resolve exact defaults/config precedence, survive reopen, apply updates without restart and report actual cleanup. Add controller tests for disconnect/save failure/stale load/duplicate cleanup and measured zero eligible bytes.
- [x] Run `GOCACHE=/private/tmp/umcode-go-cache go test ./internal/engine -run TestStorage -count=1` and frontend `npm test -- storageSettings.test.ts`; verify RED.
- [x] Replace startup-fixed retention with a shared validated policy provider used by stats/manual/background runs; every pass snapshots once. Implement typed routes and global vault-only UI with four retention fields and “Clean up now”. Keep controls available when optimization is off. Display busy/error/retry and actual freed bytes, avoiding success claims before RPC completes.
- [x] Rerun focused Go/frontend tests and check; expect PASS.
- [x] Commit `feat: add vault retention and cleanup controls`.

### Task 9: Integration verification and documentation

**Files:** Extend `internal/engine/turn_details_test.go`, `memory_management_test.go`, `storage_test.go`; update user-facing docs under `docs/` and add `docs/superpowers/plans/2026-10-11-phase6b-completion.md`.

- [x] Add integration tests linking real admitted turns to recorded inspector details, correction/retirement to subsequent context, and promoted-memory evidence to cleanup after source deletion. Assert master switch off still permits inspection/management and ordinary chats receive no new optimization approval events.
- [x] Run focused integration tests; expect PASS. Repair failures in owning task modules and retain regression coverage.
- [x] Run `GOCACHE=/private/tmp/umcode-go-cache go test ./... -count=1 -timeout=10m`, `go vet ./...`, targeted race tests for memory/GC/recorder concurrency, then frontend `npm test`, `npm run check`, `npm run build`, and `git diff --check`. Record exact results and unresolved limitations; do not repeatedly rerun passing suites without new changes.
- [x] Verify inspector/settings states through frontend controller fixtures and engine integration tests; manually inspect the storage controls at standard and narrow widths. Confirm opening details makes no provider call.
- [x] Document shipped controls and deferred purge/live qualification; review the whole branch against the accepted spec, resolve material findings, and rerun affected checks.
- [x] Commit `docs: document Phase 6b controls and verification`. Stop with reviewable committed changes; push/PR only when requested.

## Self-review record

Spec coverage maps to Tasks 1–3 (turn details), 4–6 (memory), 7–8 (storage), and 9 (cross-feature acceptance). Shared signatures, RPC names, ownership/conflict fields and policy sources are fixed above. All five review-focus conditions have owning tests. No implementation code or dependencies are introduced by this plan. Native execution is recommended because the interfaces and file/reference lifecycle are tightly coupled and one implementer can preserve those invariants across the sequence.
