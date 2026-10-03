# Vault and Evidence Lifecycle (Token Efficiency Phase 3)

**Status:** Design approved in conversation; pending written-spec review

**Date:** 2026-10-02

**Parent spec:** `2026-09-30-quality-first-token-efficiency-design.md` (Phase 3: vault and evidence lifecycle)

**Builds on:** Phase 2 (`2026-10-02-work-model-phase2-design.md`, merged as #43)

## Summary

Phase 2 records a work graph but keeps only a short excerpt of each output and has one staleness rule. Phase 3 makes that evidence trustworthy and bounded. Full outputs go into a content-addressed vault (secrets redacted first), evidence goes stale when the workspace or environment changes under it, and old data is retired automatically. It also closes the Phase 2 gaps: file changes made through the shell, and attempts lost when verification output is truncated.

Phase 4 (context compiler) will send the model only the facts that are still true. Phase 3 defines "still true" (**active evidence**) so that compiler cannot hand the model a stale "tests passed".

## Goals

- Store full verification output and failed-tool output once, by content hash, in the configured vault.
- Never persist secrets: redact before hashing and writing.
- Never present missing or corrupt vault objects as valid evidence.
- Mark criteria and evidence stale when files, the workspace or the environment change, including changes made through shell commands.
- Define active evidence in one place and expose it read-only.
- Retire stale data in two stages (leave active use, then garbage-collect after retention) without ever deleting something still referenced.
- Prove recovery behavior with failure-injection tests.

## Non-goals

- Contradiction resolution (running the cheapest authoritative check, promoting a result, pausing high-risk dependent work). Phase 3 only keeps both observations and marks the older one superseded.
- Stopping workspace mutation when the audit database fails (needs an action ledger; later phase).
- Context compilation or any change to the request sent to the model (Phase 4).
- UI for the vault, retention controls or purge actions (Phase 6). Only read-only protocol surfaces ship here.
- Memory promotion, decisions, Designed workflow (Phase 5).
- A user-facing approval, prompt or required setting of any kind (see Zero-friction rules).

## Zero-friction rules

These are requirements, not preferences.

- **No new approvals.** The workspace fingerprint (`git status`/`git diff` or a stat walk) is engine-internal and read-only. It does not pass through the tool approval gate and never asks the user anything.
- **Nothing blocks a turn.** Vault writes, redaction, fingerprints, staleness updates and GC are best-effort and time-bounded. A failure or timeout is logged, counted in `Failures`, and the turn and tool call continue unchanged.
- **No settings required.** Every default works unconfigured. Retention periods and the object size cap are optional keys under `storage`.
- **No new chat output.** No new items, notifications or prompt text. Users see nothing new unless they inspect.
- **No deletion prompts.** GC touches only engine-owned vault objects and database rows. It never touches project files.

## Vault (`internal/vault`)

```go
type Vault struct{ Dir string } // storage.vault_dir

func (v *Vault) Put(data []byte) (Object, error)
func (v *Vault) Get(hash string) ([]byte, error) // ErrMissing, ErrCorrupt
func (v *Vault) Has(hash string) bool
func (v *Vault) Delete(hash string) error
```

- **Layout:** `<vault_dir>/objects/<first 2 hex of hash>/<sha256 hex>`.
- **Redact before hash.** `Put` runs the redactor first. It masks private-key blocks, common token shapes (`sk-…`, `ghp_…`, `AKIA…`, `xox…`), `Authorization:` and `Bearer` values, and `NAME=value` lines whose name looks secret (`KEY`, `TOKEN`, `SECRET`, `PASSWORD`). The hash covers the redacted bytes, so identical redacted content is stored once and the original never touches disk. `Object.Class` is `redacted` if anything was masked, otherwise `plain`.
- **Atomic write.** Temp file in the same directory, fsync, rename. Renaming onto an existing hash is a no-op (deduplication).
- **Verified reads.** `Get` re-hashes. A missing file returns `ErrMissing`; a mismatch returns `ErrCorrupt`. Partial bytes are never returned.
- **Size cap.** Default 8 MB (`storage.vault_max_object_bytes`). Larger input is clipped head-and-tail (the verdict is at the tail), `Object.Truncated` is true and `Object.OriginalSize` is recorded.
- **Index.** `vault_objects(hash PK, size, original_size, class, truncated, created_at, last_referenced_at, status)` with `status` in `available`, `missing`. The row is written only after the file is durable.

### What is stored

- Full output of each `verification.run` result and of `browser.verify`.
- Error text of failed tools.
- Evidence rows keep a short summary, now the **last** 2 KB (the verdict is at the tail), and link to the object through `vault_hash`.

### Failure behavior

- If `Put` fails (disk full, permissions), the evidence row is still written with an empty `vault_hash`, `availability = none`, and a summary noting that full output was not retained. The turn is unaffected.
- Evidence whose object is `missing` or `corrupt` is reported `unavailable`, never `available`.
- A crash between file write and index row leaves an orphan file. The startup scan adopts it (writes the row) or removes it if unreferenced and past retention.

## Evidence and schema (migration `0014`, additive)

- `evidence` gains `vault_hash TEXT NOT NULL DEFAULT ''`, `env_fingerprint TEXT NOT NULL DEFAULT ''`, and `availability TEXT NOT NULL DEFAULT 'none'` (`none`, `available`, `unavailable`). The existing `source_revision` and `stale_at` columns are now populated.
- `work_nodes.status` for criteria gains `stale` (alongside `pending`, `passed`, `failed`, `superseded`). This fixes criteria reading `passed` after a later change.
- `work_fingerprints(id PK, work_id FK, turn_id, kind, value, paths_json, taken_at)` stores workspace fingerprints. `kind` is `verification` or `turn_end`. Cascades with the work.
- `verification_attempts` gains `fingerprint_id TEXT` pointing at the `verification` fingerprint taken with that attempt.
- Recording an attempt, its evidence, its vault link and its fingerprint happens in one SQLite transaction (resolves the Phase 2 non-transactional minor). The vault object is written first; if the transaction fails the object becomes an orphan for the scanner.
- `GetWorkDetail` checks `rows.Err()` on every loop (already fixed in #44).

## Recording changes

- The engine passes the structured per-check results of `verification.run` to the recorder **before** the 64 KB output clip. Recording no longer depends on parsing a clipped JSON string, so truncated output no longer drops attempts. Parsing the output string remains as a fallback.
- Shell-made changes are covered by the workspace fingerprint below, not by parsing commands.
- Depth escalation also includes `exec.start` and `exec.write` (non-green), matching `shell.run`.

## Workspace fingerprint

Taken at exactly two moments: when a verification attempt is recorded (`verification`), and when the turn ends (`turn_end`).

- **Git projects:** `HEAD` commit, plus a hash of `git status --porcelain` and `git diff`, plus the changed path list from `git diff --name-only` and untracked files from status.
- **Non-git folders:** a manifest of relative path, size and mtime, capped at 5,000 files, ignoring `.git`, `node_modules` and the vault directory.
- **Budget:** 2 s per fingerprint. On timeout or error the fingerprint is skipped and nothing is invalidated. No approval, no prompt.

If the `turn_end` fingerprint differs from the fingerprint stored with a passing attempt, the changed paths are recorded as `file_change` evidence (summary `workspace`), which makes the criterion stale through the existing rule. This catches `sed -i`, formatters, `git checkout` and codegen. A change made and reverted within the same turn is harmless and not detected.

## Environment fingerprint

A short hash over host or isolated-compute identity, OS and architecture, the project's `HEAD` revision when git, and the tool version captured once per work (for example `go version`). It is stored on every attempt's evidence and shown by `work/get`. A criterion's pass counts only while the current environment fingerprint equals the attempt's.

## Staleness (`work.Staleness`)

One function, computed on read and written back at turn end.

1. The newest attempt per criterion wins; older attempts remain (append-only).
2. A passing attempt is stale if a later `file_change` evidence exists (Phase 2 rule).
3. It is stale if the workspace fingerprint rule above recorded a later change.
4. It is stale if its environment fingerprint differs from the current one.
5. Stale criteria are written back with status `stale`. Evidence from superseded attempts and evidence of stale criteria gets `stale_at`.
6. **Active evidence** is evidence with no `stale_at`, not superseded, and `availability` is not `unavailable` (`none` means a summary-only row and stays active). `work/get` accepts `activeOnly` to filter on this.

`Unresolved(d)` continues to define when a work may close; stale criteria count as unresolved.

## Retention and garbage collection

Two stages, automatic, no prompts.

- **Stage 1 (immediate):** stale and superseded evidence leaves active use and default listings.
- **Stage 2 (GC):** a vault object is deleted only when no evidence row references its hash among works that are open or whose evidence is still active, and its retention has expired.
- **References are derived** from `evidence.vault_hash` at GC time, not counted, so they cannot drift.
- **Defaults** (`storage.retention_*`, all optional): redundant raw output 30 days, stale or superseded observations 30 days, large unreferenced objects 90 days, `redacted` objects 7 days.
- **Schedule:** at engine startup and at most once a day in the background, with a time budget. An interrupted run is safe; the next run finishes it.
- **Reconciliation:** a file with no index row is adopted or removed; an index row with no file becomes `missing`, and its evidence reads `unavailable`.

## Protocol

- `work/get` gains optional `activeOnly bool`. Evidence entries add `vaultHash`, `availability`, `envFingerprint`; criterion nodes may report `stale`.
- `vault/stats` (read-only): object count, total bytes, and bytes eligible for GC. No mutating vault method ships in this phase.

## Testing

All new behavior follows TDD; each test is written and seen failing first.

**Unit**
- Vault: single storage of identical input; distinct hashes; size cap clips and sets `truncated`; `Get` returns `ErrMissing`/`ErrCorrupt`, never partial bytes; redaction masks the listed secret shapes and the raw secret never appears on disk; redacted objects are classified `redacted`.
- Staleness: newer attempt supersedes; later file change makes a pass stale; turn-end fingerprint change from a shell edit makes it stale and records the changed paths; environment change makes it stale; active evidence excludes stale, superseded and unavailable rows; fingerprint timeout invalidates nothing.
- Fingerprint: git and non-git folders, file cap, ignored directories, time budget.
- Recorder: truncated `verification.run` output still records attempts; summary is the tail; attempt, evidence, link and fingerprint commit atomically.

**Failure injection**
- Read-only or full vault directory: evidence is written with empty `vault_hash`, the turn succeeds, the failure is counted.
- Crash between file write and index row: the startup scan reconciles it.
- Crash mid-GC: the next run finishes, nothing referenced is lost.
- Vault file deleted by hand: evidence reads `unavailable`, never `available`.
- Corrupted object: `Get` fails verification and evidence reads `unavailable`.
- Closed database during recording: the tool result is unchanged.
- Property test: GC never deletes an object referenced by open or active evidence across random reference graphs.

**Engine and end-to-end**
- The agent passes tests, then runs `sed -i` through the shell: the work stays open and the criterion is `stale`, with no approval prompt anywhere in the flow.
- `work/get` shows `stale` and `unavailable`; `activeOnly` filters correctly; `vault/stats` reports sensible totals.
- A plain chat turn produces the same items as before.

## Review focus

- Redaction misses (secret shapes not covered; secrets in tool arguments stored as evidence summaries).
- GC deleting an object that is still referenced, including across restart.
- Any fingerprint or vault call that can block a turn beyond its budget.
- Any new approval or prompt path introduced by the fingerprint or GC.
- Attempt, evidence and vault link atomicity under crash.
- The fingerprint walk following symlinks out of the project root.

## Rollout and risk

Migration `0014` is additive; older databases and works keep working, with empty `vault_hash` and `availability = none`. Recording stays best-effort, so a vault problem degrades to Phase 2 behavior rather than failing a turn. The vault directory is deletable by the user; affected evidence then reads `unavailable`. Phase 4 consumes active evidence; until it ships, nothing in the model request changes.
