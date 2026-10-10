# Phase 4 completion record

## Phase 4c retrieval — complete

Isolated native worktree: `/Users/shaktsin/.codex/worktrees/phase4-retrieval-tools/umcode`; implementation base `515cc72`, verified head `ca262a8`. Seven native plan tasks completed. Commits cover contracts/config, scoped redacted index migration, deterministic graph/lexical selection, real file observation with secure revalidation, compiler/accounting integration, usefulness fixtures and review fixes.

Validation: exact-final `go test ./... -count=1 -timeout=10m`, `go vet ./...`, and `git diff --check` passed. Both configuration defaults remain off. Provider-reported usage is unchanged. Deterministic real-tool/provider fixture: baseline 4 model requests/~23,966 cumulative estimated input tokens; retrieval 3/15,566, same edited artifact and actual passing repository tests. Restart and external edit validation pass. Temporarily disabling selection produced 4/~23,966 for both arms and failed the savings assertion; restored production passed.

Fresh gpt-6-astra whole-slice review found obsolete ad-hoc passing evidence, message-overhead budget overflow, canonical passing-evidence duplicates, inactive excerpt parents, partial retrieval on secure-read failures and incomplete diagnostics. All six were fixed in one pass with permanent observed RED/GREEN regressions. No deferred review findings remain.

Rulings made:
- The legacy migration preservation test now checks migration 16's presence instead of asserting it is the maximum, preserving all its before/after data assertions while allowing additive migration 17. Cost if wrong: a missing migration 16 is still detected, but future migrations are permitted.
- The workflow read-failure fixture renames `works` instead of dropping it. New cascading FTS dependencies make DROP deterministically lock; both fail-closed and flag-off behavior remain asserted.
- Historical transcript candidates use current thread/project identity and have no Work ownership; canonical sources retain strict Work identity. Requiring Work ownership for transcript rows would silently lose eligible context.
- Exact observed-file hash/workspace mismatches omit the stale/foreign excerpt as the spec requires; secure-read and containment errors discard the entire retrieval attempt. Unsupported platforms omit excerpts.
- Integration choices are deferred until both approved Phase 4 slices are complete; the user authorized continuous native implementation of retrieval followed by progressive tools. No push, PR or merge is authorized by this implementation request.

## Phase 4d progressive tools — in progress

Execute the approved plan only after the verified retrieval slice above. Independent default-off flag, frozen permitted catalog, bounded discovery and immutable dispatch identity remain required.
