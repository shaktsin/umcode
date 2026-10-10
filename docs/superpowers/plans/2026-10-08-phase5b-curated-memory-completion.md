# Phase 5b completion record

Implemented on `codex/phase5b-curated-memory`. The approved design and implementation plan are adjacent. Configuration remains default-off; enable both `models.designed_workflow` and `memory.auto_promote`. No push, PR, or merge performed.

The implementation qualifies canonical completed Work sources, resolves applicable UMCODE.md targets, merges only proven generated entries, prepares exact snapshots transactionally, writes through directory descriptors, records undo history, and recovers interrupted operations. Promotion adds no model calls. Actual usage accounting remains unchanged.

## Review fixes

Whole-branch review found five Important issues, all addressed with failing then passing regressions: production fact reachability; exact source/guidance/evidence binding; Markdown ownership boundaries; adopted duplicate lifecycle; missing-versus-empty recovery identity. Scoped review found two related source residuals: working-directory loss and redacted command projection. Both are resolved by preserving ordinary verification records while excluding non-root, redacted, or already-masked commands from durable source creation. Root command positive cases remain covered.

## Deterministic benefit test

The test uses real verification.run, production observation and work.update candidate creation. Baseline: 8 model calls, 2 discoveries, 6 tool rounds. Treatment: 7 calls, 1 discovery, 5 rounds. Second-turn request bytes: 68,531 versus 38,429 (30,102 saved). Both completed and actually verified 2/2 tasks with identical answer/artifact. These are fixture measurements, not hosted billing claims.

## Rulings and costs

- Added optional MergeInput.TargetPath to validate target identity; service always supplies it. Cost if wrong: remove an additive API field.
- Darwin/Linux use a descriptor-anchored writer; other platforms fail closed pending unsupported_platform. Cost: promotion unavailable there until a secure writer exists.
- Final inode/hash/parent checks precede Renameat. Unix provides no atomic hash-conditional rename. Cost: an uncooperative external writer can race in the final syscall interval.
- Updated root GO_ENGINE.md, the existing operator document, rather than nonexistent docs/GO_ENGINE.md. Cost: documentation relocation only.
- Positive guidance is restricted to exact typed verified commands and approved decision titles. Other categories fail closed. Cost: fewer eligible memories until more typed projections exist.
- Ambiguous raw HTML conflicts conservatively. Cost: safe-but-complex documents can require manual updates.
- User duplicates persist as UserOwned without granting rewrite authority; ambiguous replacement duplicates conflict before supersession. Cost: manual resolution for those replacements.
- Only empty or dot command directories produce facts, and redacted/already-masked commands are excluded. Cost: equivalent absolute-root invocations and scoped commands are conservatively ineligible.

## Deferred minors

- Add an independent Store-connection reservation concurrency test.
- Freeze terminal conflict error metadata on repeated failure calls.

## Verification

Final full suite, vet, and diff checks are recorded after completion below. Optional full-package race testing was interrupted earlier; no whole-branch race claim is made.

Final verification on 2026-10-10: `GOCACHE=/private/tmp/umcode-go-cache go test ./... -count=1 -timeout=10m` passed all packages, including engine (87.147s), memory (43.454s), store (68.783s), work (86.377s), and server (95.573s). `go vet ./...` and `git diff --check` passed. Source regression RED/GREEN includes nested directories, newly redacted commands, and already-masked commands; all TestVerifiedCommandObservation cases passed (5.441s). Scoped review residuals were resolved inline after reviewer/implementer quota interruption; no additional broad review cycle was run.
