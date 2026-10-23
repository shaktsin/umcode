# Execution-evidence implementation and verification

Date: 2026-10-17
Implementation commit: `75a42dd`; retained locally on `codex/task-recovery-approvals`.

UMCode records requested outcomes separately from model narration. Actual permitted
file operations and fresh matching evidence establish completion; invented receipts
cannot. Corrections are limited to two responses per turn and do not grant authority.

## Independent review

One fresh read-only review covered `a3f463d..6df4605`. Five Important findings were
accepted and fixed in one pass; each permanent regression failed before its fix:

- Unfinished work remained retryable: `TestTaskExpectationIncompleteLifecycle`.
- New edits require evidence after their request: `TestTaskExpectationNewMutationRejectsPriorProof`.
- Requested symlink aliases retain their canonical binding: `TestTaskExpectationRequestedSymlinkBinding`.
- Trusted directory listings include freshness and clipping bounds: `TestTaskExpectationDirectoryListing`.
- Successful current inspections retain their answer: `TestTaskInquiryPreservesAnswer`.

No Critical or Minor findings. No second review was dispatched; regressions and the
complete suite verify the fix pass. Semantic interpretation/document completeness
and separately logged UI work were explicitly considered and ruled on below.

## Verification

- Full Go suite after all source changes: 2,023 passing cases, 36 packages, zero failures.
  Engine 711.282s; server 266.824s. Command: `GOCACHE=/tmp/umcode-go-cache go test -json ./... -count=1 -timeout=15m`.
- Full task/history/store race suite before the review fix: PASS, engine1199.595s/store186.744s.
- Changed-path race suite after the review fix: PASS, engine528.446s/tools2.008s.
  Command: `GOCACHE=/tmp/umcode-go-cache go test -race ./internal/engine ./internal/tools -run '^TestTaskExpectation|^TestTaskInquiryPreservesAnswer$|^TestTaskIntent|^TestFile' -count=1 -timeout=10m`.
- `go vet ./...`: PASS.
- Frontend: 86/86 tests; Svelte zero errors and zero warnings.
- Local macOS bundle built and strict signature verification passed; ad-hoc signing.
  Isolated compute/libkrun and opt-in Chromium Visual QA runtimes are not staged.

Tests use scripted providers with real filesystem, SQLite, approvals, and socket
transport. No paid live-model behavior or user project mutation is part of these tests.
Generic shell/plugin effects without supported verification remain unverified.
The running application was not stopped or launched; no push, PR, merge or release.

## Rulings I made

- Ruling: retain the dedicated current checkout under the previously established sandbox worktree fallback, preserving TASKS.md and the local legacy-denial correction — same approved local-build workflow, no suitable attached worktree for this branch — cost if wrong: less filesystem isolation; exact staging required.
- Task 2: Ruling: wire default turn intent-resolution together with the no-execution gate in Task5, after the independent resolver is verified — avoids partially changing every provider request before the real completion boundary exists — cost if wrong: delayed end-to-end integration evidence until Task5; no intermediate build will be delivered. Private role/schema and additive explicit input are implemented now.
- Task4: Ruling: consume existing private builtin proof capture directly instead of adding a no-op recordExpectationEvidence forwarding API — recorded outcome already owns result/proof persistence — cost if wrong: one less proposed helper signature; evaluator is the only new consumer. Actual matching RED reproduced missing APIs, then canonical /var→/private/var mismatch exposed and fixed before GREEN.
- Task5: Ruling: scripted legacy providers answer private interpretation separately from execution counters; file-recovery scripts supply an actual mutation contract, older dialogue/routing fixtures use explanation and retain all operation/authority assertions — new paid-model phase must not consume the next scripted execution event — cost if wrong: old fixtures do not evaluate natural-language classification; dedicated actual-turn/acceptance providers exercise it explicitly. Actual usage remains recorded, not hidden from budgets.
- Task7: Ruling: retain existing execution-task lifecycle for tool-free explanations while completing the conversation turn — a completed answer must not close unfinished multi-turn/delegated work — cost if wrong: an explanation-only execution task may remain internally active until replaced; current-request expectations still identify each new inquiry.
- Task7: Ruling: extend the synthetic evaluation script adapter to answer private intent from its explicit script without consuming execution steps, and add actual read-back to deterministic fixture — existing replay campaigns must exercise the new engine boundary honestly — cost if wrong: synthetic results now include interpretation and verification overhead, so old attempt-count comparisons need updated baselines. Live provider behavior is unchanged.
- Task7: Ruling: overlap the single read-only review with final long-running verification after focused acceptance passes — implementation is complete and reviewer sees committed code, while no build/completion claim precedes final green checks — cost if wrong: reviewer may need to assess an integration failure reported during review; no repeated review is dispatched.
- Final: Ruling: semantic interpretation and document completeness remain fallible — the engine enforces validated request provenance and actual evidence, not universal natural-language correctness — cost if wrong: a misclassified request or incomplete document can still require user correction.
- Final: Ruling: clickable prose files and generic warning/lifecycle visual redesign remain separately logged — approved evidence design explicitly excludes them — cost if wrong: those existing UI annoyances remain in this local build.
- Final: Ruling: after the broad task/store race suite passed, rerun race checks on the changed matcher/lifecycle/intent/file paths instead of repeating unchanged store concurrency tests — the final fixes change evidence matching and presentation, not shared-state primitives — cost if wrong: a new cross-feature race outside the changed paths could escape this targeted rerun; the complete non-race suite is still repeated.
- Task7: Ruling: build the separate local bundle while the post-review full Go suite finishes, after focused acceptance and vet are green — build outputs are isolated and no app is launched or delivered before final checks pass — cost if wrong: a failing final test would require correcting code and rebuilding before delivery.

## Deferred minors

None.
