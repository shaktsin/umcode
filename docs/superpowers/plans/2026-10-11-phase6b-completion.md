# Phase 6b Quiet Controls Completion

Phase 6b delivers on-demand turn diagnostics, safe project-memory review, and Vault retention controls behind the existing global token-optimization master switch.

Turn details read the policy snapshot captured when a turn was admitted and show available request, context, tool, token, and cost diagnostics. Missing measurements remain unavailable, partial pricing is labeled, and opening the inspector does not make another provider request. Ordinary optimization use adds no approval prompt.

Project memory can be reviewed, corrected, and retired. Corrected instructions become user-owned project guidance; retiring one removes its review entry while preserving the instruction in `UMCODE.md`. Generated bullets are removed only when ownership and current file content allow safe removal. Review and explicit memory management remain available with optimization disabled.

Vault storage settings validate and persist four retention periods, report indexed Vault object counts and bytes, and run cleanup with a result based on actual deletions. Active work and evidence supporting live promoted memory remain pinned through source-thread deletion and collection. The UI clarifies that Vault statistics are not total application disk usage. Project-wide purge, forced deletion of referenced evidence, and paid rollout qualification remain deferred.

## Verification

- `GOCACHE=/private/tmp/umcode-go-cache go test ./... -count=1 -timeout=10m` — passed (full suite; localhost access was enabled for existing `httptest` authentication cases).
- `GOCACHE=/private/tmp/umcode-go-cache go vet ./...` — passed.
- Focused race checks across engine, memory, work, and store for turn inspection, memory lifecycle, Vault evidence retention, and GC references — passed.
- `npm test` — 8 files and 25 tests passed.
- `npm run check` — zero errors and zero warnings.
- `npm run build` — passed.
- `git diff --check` — passed.
- Manual settings-panel visual check at standard and narrow widths; automated frontend coverage verifies inspector/settings response states and stale-request behavior.

## Review note

The whole branch was reviewed against the accepted Phase 6b spec and the plan's ownership, cleanup, missing-measurement, and no-extra-approval constraints. An independent subagent review was unavailable under the active execution constraints, so regression tests and self-review were used. No live paid qualification was run.
