# Vault and Evidence Lifecycle Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Store full verification output in a redacting content-addressed vault, make evidence go stale when files, the workspace or the environment change (including shell-made edits), and retire old data automatically, with no new approvals or prompts.

**Architecture:** A new `internal/vault` package owns hashing, redaction and atomic files. A new `internal/fingerprint` package computes workspace and environment fingerprints. `internal/work` records attempts transactionally with vault links and fingerprints, defines staleness and active evidence in one place, and runs GC derived from database references. The engine passes unclipped verification results through a context sink and wires startup scan and daily GC.

**Tech Stack:** Go 1.24, SQLite via `internal/store` (numbered migrations), `crypto/sha256`, `os/exec` for git.

**Spec:** `docs/superpowers/specs/2026-10-02-vault-evidence-phase3-design.md` (parent: `2026-09-30-quality-first-token-efficiency-design.md`; builds on `2026-10-02-work-model-phase2-design.md`).

## Global Constraints

- No new approvals, prompts, chat items, notifications or prompt text. Fingerprints and GC never pass through the tool approval gate.
- Nothing blocks a turn: vault, redaction, fingerprint, staleness and GC are best-effort and time-bounded; failures are logged and counted in `work.Service.Failures`; the turn and tool result are unchanged.
- Every config key is optional with a working default: `storage.vault_max_object_bytes` (8 MB), `storage.retention_raw_days` (30), `storage.retention_stale_days` (30), `storage.retention_blob_days` (90), `storage.retention_redacted_days` (7).
- Vault layout `<vault_dir>/objects/<first 2 hex>/<sha256 hex>`; hash covers the **redacted** bytes; write is temp file, fsync, rename.
- Workspace fingerprint: taken only when a verification attempt is recorded and at turn end; 2 s budget; non-git manifest capped at 5,000 files, ignoring `.git`, `node_modules` and the vault directory; timeout invalidates nothing.
- Evidence summary is the **last** 2 KB of output (`summaryLimit` stays 2048 bytes).
- Migration `0014` is additive; existing rows keep working (`vault_hash = ''`, `availability = 'none'`).
- GC never touches project files and never deletes an object referenced by open or active evidence.
- Go commands use `GOPROXY=direct GOSUMDB=off GOFLAGS=-buildvcs=false` where the module proxy is unreachable.

## Review Focus

- A secret shape the redactor misses, or a secret in tool arguments copied into an evidence summary. (Task 1 table-driven redaction test; Task 4 summary-redaction test)
- GC deleting an object still referenced, including across a restart or a crash mid-run. (Task 5 property test and crash tests)
- A fingerprint or vault call running past its budget and delaying a turn. (Task 3 timeout test; Task 4 turn-never-blocks test)
- Any new approval or prompt path introduced by the fingerprint, GC or startup scan. (Task 6 end-to-end test with auto-approval off)
- Attempt, evidence and vault link committing atomically; a crash leaving evidence without an attempt. (Task 2 rollback test; Task 4 failure-injection test)
- The fingerprint walk following a symlink out of the project root. (Task 3 symlink test)

---

## File Structure

- Create `internal/vault/vault.go`, `internal/vault/redact.go`, `internal/vault/vault_test.go`: object store.
- Create `internal/fingerprint/fingerprint.go`, `internal/fingerprint/fingerprint_test.go`: workspace and environment fingerprints.
- Create `internal/store/migrations/0014_vault_evidence.sql`; modify `internal/store/work.go`; create `internal/store/vault.go`, `internal/store/vault_test.go`: persistence.
- Modify `internal/protocol/work.go`, `internal/protocol/methods.go`: new fields, `vault/stats`, `activeOnly`.
- Modify `internal/work/work.go`, `internal/work/rules.go`, `internal/work/parse.go`; create `internal/work/staleness.go`, `internal/work/gc.go` and tests: recording, staleness, GC.
- Modify `internal/tools/verification.go`, `internal/tools/builtin.go`; create `internal/tools/rawsink.go`: unclipped result side channel.
- Modify `internal/engine/engine.go`, `internal/engine/turn.go`, `internal/server/routes.go`, `internal/config/config.go`, `GO_ENGINE.md`; create `internal/server/vault_e2e_test.go`: wiring.

---

### Task 1: Vault package

**Files:**
- Create: `internal/vault/vault.go`, `internal/vault/redact.go`
- Test: `internal/vault/vault_test.go`

**Interfaces:**
- Consumes: nothing from earlier tasks.
- Produces:
  - `type Object struct{ Hash string; Size, OriginalSize int64; Class string; Truncated bool }` with `Class` one of `vault.ClassPlain = "plain"`, `vault.ClassRedacted = "redacted"`.
  - `type Vault struct{ Dir string; MaxObjectBytes int64 }` (0 means 8 MB).
  - `func (v *Vault) Put(data []byte) (Object, error)`, `func (v *Vault) Get(hash string) ([]byte, error)`, `func (v *Vault) Has(hash string) bool`, `func (v *Vault) Delete(hash string) error`, `func (v *Vault) Walk(fn func(hash string) error) error` (every object file on disk).
  - `var ErrMissing, ErrCorrupt error`.
  - `func Redact(s []byte) (out []byte, changed bool)`.

- [ ] **Step 1: Write the failing tests** in `vault_test.go` (use `t.TempDir()`):
  - `TestPutDedupes`: two `Put` calls with identical bytes return the same `Hash`; exactly one file exists under `objects/`.
  - `TestGetVerifiesHash`: after `Put`, overwrite the file with other bytes → `Get` returns `ErrCorrupt` and nil data; `Delete` the file → `Get` returns `ErrMissing`.
  - `TestRedactionTable`: table over private-key block (`-----BEGIN RSA PRIVATE KEY-----…`), `sk-` + 30 chars, `ghp_` + 36 chars, `AKIA` + 16 chars, `xoxb-…`, `Authorization: Bearer abc.def.ghi`, `API_TOKEN=hunter2hunter2`, `password: s3cretvalue`; each yields `changed == true`, the raw secret absent from the output, and a non-secret line (`go test ./... ok`) preserved byte-for-byte.
  - `TestPutRedactsBeforeHash`: `Put` of text containing `API_TOKEN=hunter2hunter2` returns `Class == ClassRedacted`; no file under the vault directory contains `hunter2hunter2`; the hash equals SHA-256 of the redacted bytes.
  - `TestPutSizeCap`: `Vault{MaxObjectBytes: 1000}` with 5,000 bytes → `Truncated`, `OriginalSize == 5000`, stored bytes ≤ 1000 plus marker, tail of input preserved.
  - `TestPutAtomicOnReadOnlyDir`: vault dir made read-only (`chmod 0500`; skip if running as root) → `Put` returns an error and leaves no temp file.
  - `TestWalkListsObjects`: after two distinct `Put`s, `Walk` yields both hashes.

- [ ] **Step 2: Run** `go test ./internal/vault -count=1` — Expected: FAIL (package does not compile).

- [ ] **Step 3: Implement** `vault.go` and `redact.go` per the Interfaces block. `Put` order: redact, clip head-and-tail at `MaxObjectBytes` (65/35 split like `tools.clipAt`, valid UTF-8, insert a one-line truncation marker), hash, `MkdirAll(objects/ab)`, write temp in the same directory, `fsync`, `rename`; renaming onto an existing hash is a no-op. `Get` re-hashes the bytes and compares. `Redact` is a fixed ordered list of compiled regexps replacing matches with `[REDACTED]` (key-name rule: `(?i)\b[A-Z0-9_]*(KEY|TOKEN|SECRET|PASSWORD|PASSWD)[A-Z0-9_]*\s*[=:]\s*\S+`, keeping the name and replacing only the value).

- [ ] **Step 4: Run** `go test ./internal/vault -count=1 -race` — Expected: PASS.

- [ ] **Step 5: Commit** `git add internal/vault && git commit -m "feat: add content-addressed vault with redaction"`

---

### Task 2: Migration, protocol fields and store layer

**Files:**
- Create: `internal/store/migrations/0014_vault_evidence.sql`, `internal/store/vault.go`
- Modify: `internal/store/work.go`, `internal/protocol/work.go`
- Test: `internal/store/vault_test.go`, `internal/store/work_test.go`

**Interfaces:**
- Consumes: Phase 2 store (`AddEvidence`, `AddVerificationAttempt`, `UpdateWorkNode`, `GetWorkDetail`).
- Produces (protocol): `Evidence` gains `VaultHash`, `EnvFingerprint`, `Availability` (`json:"vaultHash,omitempty"`, `"envFingerprint,omitempty"`, `"availability"`); constants `protocol.AvailNone = "none"`, `AvailAvailable = "available"`, `AvailUnavailable = "unavailable"`, `protocol.StatusStale = "stale"`, `protocol.StatusSuperseded = "superseded"`; `VerificationAttempt` gains `FingerprintID string` (`json:"fingerprintId,omitempty"`); `type Fingerprint struct{ ID, WorkID, TurnID, Kind, Value string; Paths []string; TakenAt time.Time }` with `protocol.FingerprintVerification = "verification"`, `FingerprintTurnEnd = "turn_end"`; `WorkDetail` gains `Fingerprints []Fingerprint` (never nil in JSON); `type VaultObjectRow struct{ Hash, Class, Status string; Size, OriginalSize int64; Truncated bool; CreatedAt, LastReferencedAt time.Time }`; `type VaultStats struct{ Objects int; Bytes, EligibleBytes int64 }` (`json` lower-camel).
- Produces (store, on `*Store`):
  - `UpsertVaultObject(ctx, o protocol.VaultObjectRow) error`, `GetVaultObject(ctx, hash string) (protocol.VaultObjectRow, error)`, `SetVaultObjectStatus(ctx, hash, status string) error`, `TouchVaultObject(ctx, hash string, at time.Time) error`, `ListVaultObjects(ctx) ([]protocol.VaultObjectRow, error)`, `DeleteVaultObject(ctx, hash string) error`.
  - `RecordAttempt(ctx, in RecordAttemptInput) (protocol.VerificationAttempt, error)` where `RecordAttemptInput{Attempt protocol.VerificationAttempt; Evidence protocol.Evidence; Object *protocol.VaultObjectRow; Fingerprint *protocol.Fingerprint; Criterion *CriterionUpdate}` and `CriterionUpdate{NodeID, Status string; Revision int; At time.Time}`; one transaction inserting the vault row (upsert), fingerprint, evidence, attempt (with `EvidenceID` and `FingerprintID` set) and the criterion update; rolls back entirely on any error.
  - `AddFingerprint(ctx, f protocol.Fingerprint) (protocol.Fingerprint, error)`, `MarkEvidenceStale(ctx, evidenceIDs []string, at time.Time) error`, `ReferencedVaultHashes(ctx) (map[string]bool, error)` (hashes of evidence on works with `status = 'open'` or evidence with `stale_at IS NULL`), `SupersedeNodes`-free: reuse `UpdateWorkNode` for `stale`/`superseded`.
  - `GetWorkDetail` populates the new evidence columns and `Fingerprints`.

- [ ] **Step 1: Write the failing tests:**
  - `TestMigration0014Additive` (store_test style): open a DB, insert a Phase 2 style evidence row through the old `AddEvidence`; it reads back with `Availability == "none"` and empty `VaultHash`.
  - `TestRecordAttemptCommitsAtomically`: `RecordAttempt` with a vault row, fingerprint, evidence, attempt and criterion update → `GetWorkDetail` shows all five linked (`attempt.EvidenceID == evidence.ID`, `attempt.FingerprintID == fingerprint.ID`, criterion status updated, `evidence.VaultHash` set).
  - `TestRecordAttemptRollsBack`: input with an invalid `Criterion.NodeID` that violates nothing but a duplicate `Attempt.ID` already inserted → error returned; afterward no new evidence, fingerprint or vault row exists.
  - `TestVaultObjectCRUD`: upsert is idempotent; `SetVaultObjectStatus` flips to `missing`; `ListVaultObjects` returns rows; `DeleteVaultObject` removes.
  - `TestReferencedVaultHashes`: a stale evidence row on a completed work is not referenced; the same hash on an open work is; an active evidence row on a completed work is.
  - `TestMarkEvidenceStale`: sets `stale_at` and `GetWorkDetail` returns it.

- [ ] **Step 2: Run** `go test ./internal/store -run "Migration0014|RecordAttempt|VaultObject|Referenced|MarkEvidence" -count=1` — Expected: FAIL (undefined).

- [ ] **Step 3: Implement** the migration (`ALTER TABLE evidence ADD COLUMN vault_hash/env_fingerprint/availability`; `ALTER TABLE verification_attempts ADD COLUMN fingerprint_id`; `CREATE TABLE vault_objects(...)`, `work_fingerprints(...)` with `ON DELETE CASCADE` to `works`) and the store methods and protocol types above. `RecordAttempt` uses `s.DB.BeginTx`; extend `AddEvidence` and `scan` code to include the new columns, defaulting empty `Availability` to `none`.

- [ ] **Step 4: Run** `go test ./internal/store -count=1` — Expected: PASS (including all Phase 2 tests).

- [ ] **Step 5: Commit** `git add internal/store internal/protocol && git commit -m "feat: add vault index, fingerprints and atomic attempt recording"`

---

### Task 3: Fingerprint package

**Files:**
- Create: `internal/fingerprint/fingerprint.go`
- Test: `internal/fingerprint/fingerprint_test.go`

**Interfaces:**
- Consumes: nothing from earlier tasks.
- Produces:
  - `type Workspace struct{ Value string; Paths []string }`
  - `func TakeWorkspace(ctx context.Context, root, vaultDir string) (Workspace, bool)` — second result false on timeout, error, or empty root. Applies a 2 s budget internally.
  - `func Diff(before, after Workspace) []string` — sorted changed paths between two fingerprints (git: changed and untracked path lists; non-git: manifest entries that differ).
  - `func Environment(ctx context.Context, root string, tool func(ctx context.Context) string) string` — short hex hash over GOOS/GOARCH, git `HEAD` when a repo, and the optional tool-version callback result; `UseCompute` host identity is passed by the caller through the callback string.
  - `const MaxManifestFiles = 5000`.

- [ ] **Step 1: Write the failing tests** (build temp projects; git cases skip when `git` is not on PATH):
  - `TestGitWorkspaceChangesWithEdit`: init repo, commit file, take fingerprint A; edit the file via `os.WriteFile`; fingerprint B differs and `Diff(A, B)` equals `["a.txt"]`.
  - `TestNonGitManifestChanges`: plain folder; create a file → A; modify content (size change) → B; `Diff` lists it; changing nothing yields equal values.
  - `TestIgnoresGitNodeModulesVault`: files created under `node_modules/`, `.git/` (non-git folder) and the vault dir do not change the fingerprint.
  - `TestManifestCap`: more than `MaxManifestFiles` files → returns ok with the manifest capped (value stable across two calls) rather than failing.
  - `TestSymlinkOutsideRootNotFollowed`: a symlink in the project pointing to a directory outside root; changing a file outside root does not change the fingerprint.
  - `TestBudgetTimeout`: a context already cancelled (or a hook shrinking the budget to 1 ns) → `(Workspace{}, false)` quickly, no panic.
  - `TestEnvironmentStableAndSensitive`: same inputs → same value; different tool-callback result → different value.

- [ ] **Step 2: Run** `go test ./internal/fingerprint -count=1` — Expected: FAIL (does not compile).

- [ ] **Step 3: Implement** `fingerprint.go`. Git detection via `git -C root rev-parse --show-toplevel` succeeding; use `exec.CommandContext` with a derived 2 s context; hash `HEAD` + `status --porcelain=v1 -z` + `diff` with SHA-256, paths from the porcelain list. Non-git: `filepath.WalkDir` with `fs.SkipDir` on ignored names, `d.Type()&fs.ModeSymlink` skipped, file count capped, sorted manifest hashed. Expose a package-level `var budget = 2 * time.Second` so tests can shrink it.

- [ ] **Step 4: Run** `go test ./internal/fingerprint -count=1 -race` — Expected: PASS.

- [ ] **Step 5: Commit** `git add internal/fingerprint && git commit -m "feat: add workspace and environment fingerprints"`

---

### Task 4: Recording, staleness and the unclipped result sink

**Files:**
- Create: `internal/tools/rawsink.go`, `internal/work/staleness.go`
- Modify: `internal/engine/turn.go` (only the `End` call, to pass the project root so the build stays green), `internal/tools/verification.go`, `internal/tools/builtin.go` (the `verification.run` return at ~L193), `internal/work/work.go`, `internal/work/rules.go`, `internal/work/parse.go`
- Test: `internal/tools/rawsink_test.go`, `internal/work/staleness_test.go`, `internal/work/work_test.go`

**Interfaces:**
- Consumes: Task 1 `vault.Vault`, `vault.Redact`; Task 2 store methods; Task 3 `fingerprint.TakeWorkspace`, `Diff`, `Environment`.
- Produces:
  - `tools.WithRawSink(ctx context.Context) (context.Context, *tools.RawSink)`; `type RawSink struct{ Text string }`; `tools.SetRaw(ctx context.Context, text string)` (no-op when the ctx has no sink). `verification.run` and `browser.verify` call `SetRaw` with their unclipped JSON before returning the clipped string.
  - `work.Observation` gains `Raw string` (unclipped output; recorder prefers it over `Output` when parsing).
  - `work.Service` gains fields `Vault *vault.Vault`, `VaultDir string`, `ToolVersion func(ctx context.Context) string`; both optional (nil `Vault` records summary-only evidence with `availability = none`).
  - `func Staleness(d protocol.WorkDetail, currentEnv string) (stale map[string]bool)` in `staleness.go`: criterion node IDs that are stale (rules 1–4 of the spec). `Unresolved(d)` now treats `stale` criteria and criteria returned by `Staleness` as unresolved (signature unchanged: `Unresolved(d protocol.WorkDetail) []string`).
  - `func ActiveEvidence(d protocol.WorkDetail) []protocol.Evidence` (no `StaleAt`, not on a superseded criterion node, `Availability != unavailable`).
  - `Service.End` additionally takes the turn-end fingerprint, records `file_change` evidence (summary `workspace`) for changed paths versus the latest `verification` fingerprint, writes `stale` back to criteria and `stale_at` to their evidence, then applies the close rule.
  - `EscalatesToGuided` returns true for `exec.start` and `exec.write` when risk is not green.

- [ ] **Step 1: Write the failing tests:**
  - `tools`: `TestRawSinkCapturesUnclippedVerificationRun`: run `verification.run` through a fake check producing > 64 KB output with `WithRawSink`; `sink.Text` is valid JSON containing every result; the returned string is clipped.
  - `work`, `TestTruncatedRunStillRecordsAttempts`: `Observation{Tool: "verification.run", Output: <invalid truncated JSON>, Raw: <valid JSON with two results>}` → two attempts recorded.
  - `TestSummaryIsTailAndRedacted`: result output of 5 KB whose first 100 bytes are `API_TOKEN=hunter2hunter2` and whose last line is `FAIL pkg 0.1s` → evidence summary ≤ 2048 bytes, ends with `FAIL pkg 0.1s`, contains no `hunter2hunter2`.
  - `TestAttemptLinksVaultObject`: with a `Vault` in a temp dir, a passing attempt gets evidence with `VaultHash` set, `Availability == available`, a `vault_objects` row, and `Vault.Get(hash)` returns the redacted full output.
  - `TestVaultFailureStillRecords`: `Vault.Dir` pointing at a path under a regular file → attempt and evidence are recorded with empty `VaultHash`, `Availability == none`, summary mentions that full output was not retained, `Failures > 0`, no error returned to the caller's tool path.
  - `TestRecordedAttemptCarriesFingerprints`: attempt has `FingerprintID` of a `verification` fingerprint; evidence has a non-empty `EnvFingerprint`.
  - `TestShellEditMakesCriterionStale` (staleness_test): plan and passing run, then `End` with a changed workspace fingerprint (inject through `Service.Workspace func(ctx, root string) (fingerprint.Workspace, bool)` set in the test) → criterion `stale`, `file_change` evidence recorded for the changed paths, work stays open.
  - `TestUnchangedFingerprintKeepsPass`, `TestFingerprintTimeoutInvalidatesNothing` (injected hook returns `false`), `TestEnvironmentChangeMakesPassStale`, `TestNewerAttemptSupersedes`, `TestActiveEvidenceExcludesStaleSupersededUnavailable`.
  - `TestExecEscalatesToGuided`: `exec.start` yellow → guided; green → not.
  - Review Focus: `TestNoApprovalOrBlocking`: with the fingerprint hook sleeping past budget, `Observe` returns within the budget plus slack and records the attempt.

- [ ] **Step 2: Run** `go test ./internal/tools ./internal/work -count=1` — Expected: FAIL (undefined `WithRawSink`, `Raw`, `Staleness`, …).

- [ ] **Step 3: Implement.** `rawsink.go` stores the sink in the context under an unexported key. Recorder: prefer `Raw` over `Output` in `parseRun`/`parseBrowser`; summary becomes `tailText(redacted output, summaryLimit)` (valid UTF-8 boundary); `recordAttempt` performs `Vault.Put` first (error → counted, empty hash), then one `Store.RecordAttempt` call. `Service.Workspace` defaults to `func(ctx, root) { return fingerprint.TakeWorkspace(ctx, root, s.VaultDir) }`. Take a `verification` fingerprint when recording an attempt; take a `turn_end` fingerprint in `End` (the engine passes the project root through a new `EndInfo` argument: change `End` to `End(ctx, threadID, turnStatus string, paused bool, root string)` and update the `finishTurn` caller in `internal/engine/turn.go` in this task, passing the turn's project root). Staleness is computed from `d.Attempts`, `d.Evidence`, `d.Fingerprints` and `currentEnv` (from `fingerprint.Environment`, cached per work in memory).

- [ ] **Step 4: Run** `go test ./internal/tools ./internal/work -count=1 -race` — Expected: PASS (Phase 2 tests updated only for the new `End` argument).

- [ ] **Step 5: Commit** `git add internal/tools internal/work && git commit -m "feat: vault-backed attempts, staleness and shell-edit detection"`

---

### Task 5: Retention, garbage collection and reconciliation

**Files:**
- Create: `internal/work/gc.go`
- Modify: `internal/config/config.go`
- Test: `internal/work/gc_test.go`, `internal/config/config_test.go`

**Interfaces:**
- Consumes: Task 1 `vault.Vault` (`Walk`, `Has`, `Delete`), Task 2 `ListVaultObjects`, `ReferencedVaultHashes`, `SetVaultObjectStatus`, `UpsertVaultObject`, `DeleteVaultObject`.
- Produces:
  - `type Retention struct{ Raw, Stale, Blob, Redacted time.Duration }`, `func DefaultRetention() Retention` (30d, 30d, 90d, 7d).
  - `type GCReport struct{ Adopted, MarkedMissing, Deleted int; FreedBytes int64 }`
  - `func RunGC(ctx context.Context, st *store.Store, v *vault.Vault, ret Retention, now time.Time) (GCReport, error)`: (1) reconcile: files with no row get a row (class `plain`) and `last_referenced_at = now`; rows whose file is gone become `missing`; (2) collect: delete an object only when its hash is not in `ReferencedVaultHashes` and its age (since `last_referenced_at`) exceeds the retention for its class; deleting removes the file first, then the row; stops early on `ctx` expiry.
  - `func (s *Service) StartGC(ctx context.Context, ret Retention)`: runs `RunGC` once immediately and then every 24 h until `ctx` is done; errors are logged and counted.
  - Config: `StorageConfig` gains `VaultMaxObjectBytes int64`, `RetentionRawDays`, `RetentionStaleDays`, `RetentionBlobDays`, `RetentionRedactedDays int` (yaml `vault_max_object_bytes`, `retention_raw_days`, `retention_stale_days`, `retention_blob_days`, `retention_redacted_days`); zero means default.

- [ ] **Step 1: Write the failing tests:**
  - `TestGCDeletesUnreferencedPastRetention`: object with no referencing evidence and `last_referenced_at` 40 days ago (class `plain`) → file and row deleted, `FreedBytes` correct.
  - `TestGCKeepsReferenced`: same object referenced by evidence on an open work → kept regardless of age.
  - `TestGCKeepsWithinRetention`: unreferenced but 5 days old → kept; a `redacted` object 8 days old → deleted.
  - `TestGCAdoptsOrphanFile`: file in `objects/` with no row → row created, file kept this run.
  - `TestGCMarksMissing`: row with no file → status `missing`; evidence referencing it reads `unavailable` through `GetWorkDetail` (availability derived from row status).
  - `TestGCCrashMidRunThenResume`: inject a hook that returns an error after deleting the file but before deleting the row; a second `RunGC` completes, nothing referenced lost, no orphan row remains.
  - `TestGCPropertyNeverDeletesReferenced` (seeded `math/rand`, 200 random reference graphs, random ages): after `RunGC`, every hash in `ReferencedVaultHashes` still has its file.
  - `TestGCHonorsContextDeadline`: an already-expired context returns promptly without deleting.
  - `TestRetentionDefaultsFromConfig`: zero config values give the defaults; explicit values override.

- [ ] **Step 2: Run** `go test ./internal/work ./internal/config -run "GC|Retention" -count=1` — Expected: FAIL (undefined).

- [ ] **Step 3: Implement** `gc.go` and the config keys; `GetWorkDetail` (Task 2) sets `Availability = unavailable` when the referenced `vault_objects.status` is `missing` or the row is absent while `vault_hash != ''`. The crash hook is an unexported package variable `gcAfterFileDelete func() error` set only by tests.

- [ ] **Step 4: Run** `go test ./internal/work ./internal/config ./internal/store -count=1 -race` — Expected: PASS.

- [ ] **Step 5: Commit** `git add internal/work internal/config internal/store && git commit -m "feat: automatic vault retention and garbage collection"`

---

### Task 6: Engine wiring, protocol surfaces and end-to-end checks

**Files:**
- Modify: `internal/engine/engine.go`, `internal/engine/turn.go`, `internal/server/routes.go`, `internal/protocol/methods.go`, `GO_ENGINE.md`
- Test: `internal/engine/work_test.go`, `internal/server/vault_e2e_test.go`

**Interfaces:**
- Consumes: Tasks 1–5.
- Produces: `protocol.MethodVaultStats = "vault/stats"`; `WorkGetParams` gains `ActiveOnly bool` (`json:"activeOnly,omitempty"`); `(e *Engine) VaultStats(ctx context.Context) (protocol.VaultStats, error)`; `(e *Engine) GetWork(ctx, workID string, activeOnly bool)`.

- [ ] **Step 1: Write the failing tests:**
  - `engine`, `TestRunToolPassesUnclippedResultToWork`: a fake `verification.run` tool that calls `tools.SetRaw` with valid JSON while returning a clipped invalid string → work records the attempts.
  - `engine`, `TestFinishTurnPassesRootAndClosesStale`: with an injected `Work.Workspace` hook that reports a change at turn end after a passing run, the work remains open and the criterion is `stale`.
  - `server`, `TestShellEditAfterPassKeepsWorkOpen` (e2e, `newHarness`, thread approval mode **normal**, no auto-approve): script `file__write` is not used; the agent runs `verification.plan` then `verification.run` (passing fake command), then a `shell__run` that `sed -i`s a tracked file (approved through the harness's `onApproval` callback — the only approval in the flow); after the turn `work/get` shows the criterion `stale` and the work `open`. Count approval requests: exactly one (the shell call); none from fingerprints or GC.
  - `server`, `TestWorkGetActiveOnly`: `activeOnly` omits stale and unavailable evidence.
  - `server`, `TestVaultStats`: after one verified attempt, `vault/stats` reports `objects >= 1` and `bytes > 0`; with no vault activity it reports zeros, not an error.
  - `server`, `TestChatOutputUnchangedPhase3`: plain text turn still yields exactly user message + agent message.

- [ ] **Step 2: Run** `go test ./internal/engine ./internal/server -run "Unclipped|PassesRoot|ShellEdit|ActiveOnly|VaultStats|ChatOutputUnchanged" -count=1` — Expected: FAIL.

- [ ] **Step 3: Implement.** In `engine.New`, build `vault.Vault{Dir: cfg.Storage.VaultDir, MaxObjectBytes: cfg.Storage.VaultMaxObjectBytes}`, set `work.Service.Vault`/`VaultDir`/`ToolVersion`, and call `e.Work.StartGC(base, work.RetentionFromConfig(cfg.Storage))` in a goroutine tied to the engine base context. In `runTool`, wrap `ctx` with `tools.WithRawSink` per call and pass `sink.Text` as `Observation.Raw`. Register `vault/stats` next to the work routes; `work/get` threads `ActiveOnly`. Add the `GO_ENGINE.md` paragraph: what is stored, redaction, retention defaults and keys, `work/get activeOnly`, `vault/stats`, and "no prompts, no approvals".

- [ ] **Step 4: Run** `go vet ./... && go test ./... -count=1` — Expected: PASS across all packages.

- [ ] **Step 5: Commit** `git add internal/engine internal/server internal/protocol GO_ENGINE.md && git commit -m "feat: wire vault, staleness and GC into the engine"`
