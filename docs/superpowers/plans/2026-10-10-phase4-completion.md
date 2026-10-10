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

## Phase 4d progressive tools — complete

Implementation base `6858b71`; feature commits `b85f98c`, `e296b59`, `29ccf0b`, `cb87cd2`, `f109ec1`, followed by the permission review fix commit. All six native tasks completed in the same isolated managed worktree. Catalogs freeze actual registry/plugin winners, metadata and exact schema bytes; the selector retains core and owned-session lifecycle tools and monotonically adds trusted families. Engine-owned bounded discovery loads schemas on the next provider request, with authenticated turn/query/catalog/permission-scoped pagination. Current permissions and frozen implementation availability are rechecked at execution, including after approval waits. Discovery does not execute targets, invoke action hooks, request approvals or create verification facts.

Exact-final validation: `GOCACHE=/private/tmp/umcode-go-cache go test ./... -count=1 -timeout=10m` passed (33 tested packages); `go vet ./...` and `git diff --check` passed. Defaults remain off; no dependencies, Go-version change, UI changes or provider-usage credits. All four retrieval/progressive flag combinations and cross-feature panic isolation pass. Flag-off schema/prompt/dispatch regressions pass.

Deterministic real coding fixture: baseline and selected each perform four model requests/three tool rounds, inspect/edit the same artifact and execute an actual passing repository `go test ./...`. Cumulative schema estimates: 32,404 → 4,832; total estimated input including prompt/results: 50,718 → 23,266. Temporarily bypassing filtering yields 32,868 schema/51,302 input treatment and fails the savings assertion; exact selector bytes restored and fixture passes. Direct specialist: two requests in both arms, estimated input 5,594 → 5,598. Unknown-intent discovery: two → three requests, 5,592 → 7,671. These demonstrate optimization sensitivity, reachability and discovery cost, not universal provider savings.

Fresh gpt-6-astra whole-slice review found three Important issues, no Critical or Minor findings. The single consolidated fix pass reproduced and fixed inherited Computer Use lost on refresh (`TestProgressiveInheritedComputerSession`, with a real owned session), stale execution capabilities and approval-boundary revocation (`TestProgressiveCurrentScopeAtAction`, `TestProgressiveRevocationDuringApproval`, plus MCP regression), and full-catalog fallback retaining advertisements on policy-read failure (`TestProgressiveFallbackPolicyFailure`). The primary regressions were observed RED→GREEN; final full suite is green. No deferred minors remain.

Additional rulings, in execution order:
- Progressive MCP project allowlists use exact registered server identity when available, preserving underscores; disabled mode retains its previous assembly. Cost if wrong: an entry may be omitted, with permission-scoped full fallback retained.
- Permission revocation may remove already shown schemas, taking priority over turn cache stability. Policy-read failure restricts entries until the next turn. Cost if wrong: temporary capability omission rather than broadened authority.
- Task5 proves behavior already implemented in Tasks1–4; mutation of the production filter supplies its observed failing test rather than introducing a stub. Cost if wrong: mutation alone cannot expose unrelated defects, covered separately by dispatch and flag regressions.
- Retrieval internals were excluded from the second review because Phase4c already received its own fresh review and verified fixes; interactions and independent flags were reviewed here. Cost if wrong: a cross-slice defect could escape that focused review, mitigated by the final full suite.
- Broad model effectiveness, universal savings and rollout remain Phase6, as requested. Cost if wrong: deterministic savings may not generalize; defaults stay off and no rollout claim is made.
- Existing trusted-autonomy network exceptions and tool sandbox rules remain authoritative while execution scope refreshes current project settings. Cost if wrong: users expecting stricter toggles still receive the existing trusted-mode behavior.

Both approved Phase4 slices are complete. Phase6 broad ablations, release targets, canary/default enablement and quiet UI remain separate. Worktree stays detached and intact pending the user's integration choice; no push, PR or merge performed.
