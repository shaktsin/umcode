# Phase 6a Paired Evaluation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Provide isolated deterministic and explicitly budgeted live evaluation of the automatic optimization bundle, with reproducible quality and efficiency verdicts.

**Architecture:** A pure Go evaluation package owns versioned contracts, validation, campaigns, accounting, grading and reports. An engine adapter executes fixture sequences through normal turn admission and dispatch; a separate CLI composes them. Primary comparisons use saved master settings; private immutable engine policies support ablations without exposing product component controls.

**Tech Stack:** Go, standard library JSON/filesystem/process APIs, existing SQLite engine store, provider registry and redaction utilities. No new dependencies or database schema.

**Spec:** `docs/superpowers/specs/2026-10-10-phase6a-evaluation-design.md`, revised 2026-10-11 and approved by the user.

## Global Constraints

- One product master switch; all six components and automatic workflow run together when on. Existing action permissions and explicit human-review requirements remain effective.
- Never use the user's running daemon, project, store, vault, memory, plugins or schedules for evaluation. No paid execution during implementation.
- Manifest limits: 1 MiB, 10,000 tasks, 32 arms, 20 repeats, 64 KiB per prompt; fixture tree limit 64 MiB. Reject symlinks, path escapes, unknown fields, duplicate IDs and invalid dependencies.
- Default concurrency is one. Preserve all failed/retried attempt costs. Persist after every attempt; never silently rerun completed attempts.
- Model, reasoning, permissions, budgets and initial fixture state are paired identically. Primary comparisons disable route fallback; explicit fallback cases declare identical routes.
- Live dispatch requires `--live`, selected token-billed credential references, supported output limits, frozen prices and finite request/suite budgets. Validate/report never dispatch. Subscription-only credentials fail live preflight.
- Missing usage/prices/scores are unavailable with a reason, never zero. Cached input and reasoning output are subsets and must not be double counted.
- Quality point estimate at or above baseline in every required stratum; simultaneous lower bound at least -0.02; zero safety regression or increased unsupported claims; high-risk units require exact paired outcome parity.
- Efficiency targets per required model: at least 30% lower uncached input, 25% lower cost per successful task, 15% fewer tool rounds. At least 30 unique scored primary units per required stratum; this minimum does not guarantee enough statistical power.
- Deterministic verdicts cannot qualify live release. Historical instruction/Work/evidence and routing ablations remain explicitly unsupported. Nothing changes rollout defaults.

## Review Focus

1. Crash after dispatch but before a result write: preserve an incomplete charged reservation; never silently repeat the request (Task 4).
2. A fixture changes a build manifest or points at the grader: trusted checks remain outside tool scope, and altered prerequisites cannot manufacture success (Task 5).
3. Stream errors after partial usage or tool calls: forward events and retain every attempt's usage and round attribution (Task 3).
4. A valid-looking review targets replaced artifacts: hash-bound imports reject it and retain amendment/conflict history (Task 6).
5. A good pooled average hides one failing category, or missing evidence hides a known failure: compute separate gates and fail dominates inconclusive (Task 7).

## File Map and Dependency Order

Create focused files under `internal/evaluation`: `manifest.go` (schema/validation), `arms.go` (policy dependencies), `schedule.go` (pair ordering), `result.go` (attempt/result contract), `usage.go` (accounting), `budget.go` (reservations), `campaign.go` (state machine), `workspace.go` (owned staging), `verify.go` (trusted verifier), `review.go` (blind export/import), `statistics.go` (intervals), `gates.go` (verdicts), `report.go` (JSON/Markdown). Each has a matching `_test.go`.

Create `internal/engine/evaluation.go` and `evaluation_provider.go` with matching tests; modify narrowly `engine.go`, `optimization.go` and `turn.go` for a private evaluation policy and metadata observer. Create `cmd/umcode-eval/main.go`, `main_test.go`, deterministic fixtures and live template under `internal/evaluation/testdata/`, and `docs/evaluation.md`.

Order: 1 contracts → 2 isolated engine → 3 accounting/stream capture → 4 campaign/budget → 5 verifier → 6 blind review → 7 gates/reports → 8 CLI → 9 complete fixtures/qualification. Tasks 5–7 consume Task 1 contracts; Task 8 composes all preceding tasks. No unrelated refactoring.

### Task 1: Validated manifests, arms, schedule and result contracts

**Files:** Create `internal/evaluation/{manifest,arms,schedule,result}.go` and matching tests.

**Interfaces:** Produces `DecodeManifest(io.Reader) (Manifest,error)`, `Validate(Manifest) error`, `Schedule(Manifest) ([]Unit,error)`, `EffectivePolicy(Arm) (optimization.Policy,error)`. `Manifest` contains version, suite/source/content hashes, models/prices, tasks, named arms, required comparisons, repeats, ordering seed, permissions, limits, cluster/sampling declarations and scoring/review versions. `Unit` identifies task/cluster, arm, model, repeat and pair order. `Runner` exposes `Run(context.Context, RunInput) (TaskResult,error)`; `RunInput` contains manifest/unit, isolated workspace path and attempt sink `func(Attempt) error`.

`TaskResult` contains unit identity, configuration/artifact hashes, final status, bounded response, attempts, check results, effective policies and incomplete reasons. `Attempt` contains unique dispatch ID, provider/model/pricing identity, reported/subscription indicators, token subsets, reserved/observed costs, outcome, elapsed time, schema/layer estimates, actual tool rounds and failure reason. Nullable measurements use pointers plus reasons. `CheckResult` contains check ID, status, evidence hash and safety/unsupported-claim/stale-authority/context-omission counts; absent counts remain missing.

- [ ] Write `TestManifestBoundsAndStrictness` with exact limits above, one-over-bound fixtures, unknown/duplicate fields (including duplicate JSON keys), duplicate IDs, nonfinite prices and unsafe paths; assert invalid inputs fail before any runner invocation.
- [ ] Write `TestArmDependenciesAndProductModes`: persisted off/on map to `optimization.All(false/true)`; retrieval without compiler, promotion without Designed and automatic without Designed fail; every Designed evaluation arm includes automatic behavior. Deduplicated group-removal arms retain explicit attribution labels.
- [ ] Write `TestPairedSchedule`: identical seed produces identical AB/BA order; models/reasoning/permissions/limits differ only where explicitly allowed; repeats/cluster members cannot become extra primary independent units.
- [ ] Run `go test ./internal/evaluation -run 'TestManifest|TestArm|TestPaired' -count=1`; observe RED for missing contracts/behavior.
- [ ] Implement contracts and pure validation/scheduling. Store arm mode explicitly as product-off, product-on, or evaluation override; unsupported historical arms have reasons. Use stable hashes rather than process-random map order.
- [ ] Run the same tests; expect PASS. Commit `feat: define validated paired evaluation contracts`.

### Task 2: Isolated engine execution and private policy admission

**Files:** Create `internal/engine/evaluation.go`, `evaluation_test.go`; modify `engine.go`, `optimization.go`, `turn.go`; create `internal/evaluation/workspace.go`, `workspace_test.go`.

**Interfaces:** Consumes Task 1 contracts. Produces `engine.NewEvaluationRunner() evaluation.Runner`, `evaluation.Stage(root string, manifest Manifest, unit Unit) (string,error)`, `evaluation.CleanupOwned(path, campaignID string) error`. The runner constructs new engines with an evaluation-owned config/store/vault, fixture titles, disabled scheduler and deterministic specialist adapters. Private `Engine.evaluationPolicy *optimization.Policy` is set only by that constructor, cloned by value and consulted by `resolveOptimizationPolicy` before turn admission; it is not an ordinary `Options` field or an RPC setting. Reopen reconstructs the same validated override. Product arms set the saved master through `SetTokenOptimization` and leave the override nil.

- [ ] Write `TestEvaluationIsolation`: two arms cannot see each other's edits, stores, vault, credentials or generated memory; multi-turn state persists within an arm; restart retains effective seven-field policy and fixture state. Ordinary engine settings remain unchanged.
- [ ] Write `TestEvaluationAdmissionPolicy`: caller context alone is insufficient; the private override reaches actual admitted requests and Work consumers; product-off/on use persisted settings; ablations never write global settings. Assert normal engine behavior is unchanged.
- [ ] Write `TestEvaluationWorkspaceOwnership`: reject symlink files/directories, path traversal and >64 MiB staging; wrong/missing ownership marker prevents cleanup; source fixtures remain byte-identical. Interrupt staging leaves only an owned partial directory.
- [ ] Run focused engine/evaluation tests; observe RED. Implement bounded staging and normal engine project/thread/turn lifecycle, waiting on actual terminal events; shutdown closes owned exec/preview/computer sessions. No test-only engine helper imports.
- [ ] Run `go test ./internal/engine ./internal/evaluation -run TestEvaluation -count=1`; expect PASS. Commit `feat: execute evaluation arms in isolated engines`.

### Task 3: Observe provider attempts and account complete task usage

**Files:** Create `internal/engine/evaluation_provider.go`, `evaluation_provider_test.go`, `internal/evaluation/usage.go`, `usage_test.go`; modify the provider-attempt boundary in `internal/engine/turn.go`.

**Interfaces:** Produces `evaluation.Aggregate([]TaskResult) (UsageSummary,error)` and private `observeEvaluationAttempt(context.Context, evaluation.Attempt)`; the Task 2 runner forwards immutable copied metadata to the RunInput attempt sink. Provider decoration retains `llm.Provider` signatures. No provider request body, credential, header or reasoning content enters results. UsageSummary separates all-task totals, common-success paired totals, per-stratum missing counts and diagnostic estimates.

- [ ] Write `TestEvaluationStreamForwarding`: immediate Stream error, partial text/tool events then error, EventDone usage, cancellation and retry each preserve event order/cancellation and exactly one terminal attempt record. Multiple sibling tool calls count as one round; denied and discovery rounds remain counted. No-usage errors remain missing rather than reported zero.
- [ ] Write `TestEvaluationTokenSubsets`: input=100, cached=40 gives uncached=60; output=20, reasoning=8 is charged as 20. Negative tokens/cached>input/reasoning>output fail. Subscription and absent prices yield unavailable dollar costs with reasons.
- [ ] Write `TestEvaluationFailureDenominators`: failed preparation/retries contribute to total dollars; zero successes yields unavailable/infinite cost per success; common-success views cannot erase failures. Repeated canonical tool/path observations are labeled diagnostics, not quality.
- [ ] Run focused tests RED; implement copied observation at each actual provider attempt, including immediate failure and retry paths. Preserve actual resolved model and frozen pricing ID; keep request-layer estimates separate.
- [ ] Run `go test ./internal/engine ./internal/evaluation -run 'TestEvaluationStream|TestEvaluationToken|TestEvaluationFailure' -count=1`; expect PASS. Commit `feat: capture complete evaluation attempt accounting`.

### Task 4: Bounded campaigns, reservations and interruption recovery

**Files:** Create `internal/evaluation/{budget,campaign}.go`, matching tests.

**Interfaces:** Consumes Runner/Schedule/Stage/Attempt. Produces `RunCampaign(context.Context, Manifest, Runner, CampaignOptions) (Campaign,error)`, `LoadCampaign(path string) (Campaign,error)`. CampaignOptions carries output path, live authorization, spending ceiling and resume flag. `Budget.Reserve(dispatchID string, maximumUSD float64) error`, `Budget.Settle(dispatchID string, observedUSD *float64) error`; writes reservations atomically before dispatch. Runner calls the attempt sink after each attempt, including failure. Campaign stores immutable config/fixture hashes, owned paths, phase and dispatch IDs.

- [ ] Write `TestCampaignBudget`: conservative reservation uses model window, maximum output and uncached prices; insufficient ceiling prevents provider invocation. Unknown prices, unsupported output limits, unconfigured/subscription credentials fail preflight. Missing/partial usage retains full reservation; duplicate settlement cannot refund twice.
- [ ] Write `TestCampaignCrashResume`: interrupt after reservation/before dispatch and after dispatch/before result; reopening preserves reserved spending and marks uncertain work incomplete. Completed attempts are not rerun; explicit new attempts retain old spending. Changed config/fixture hashes reject resume.
- [ ] Write `TestCampaignCancellationOwnership`: default concurrency one; cancellation stops new dispatch, forwards cancellation, closes engine-owned processes and preserves bounded redacted artifacts. Cleanup cannot remove arbitrary paths.
- [ ] Run focused tests RED; implement validate→stage→run→verify→review-pending/scored→report state persistence with atomic temp-file rename, checked writes and explicit incomplete states. Reject output paths that collide with source/user state.
- [ ] Run `go test ./internal/evaluation -run 'TestCampaign' -count=1`; expect PASS. Commit `feat: persist bounded resumable evaluation campaigns`.

### Task 5: Independent trusted artifact verification

**Files:** Create `internal/evaluation/verify.go`, `verify_test.go`, trusted testdata fixtures.

**Interfaces:** Produces `Verify(context.Context, Task, artifactDir, verifierRoot string) ([]CheckResult,error)`. Task declares allowlisted artifact paths, trusted check IDs/argv, deadlines and prerequisite hashes. Trusted grading source lives outside agent/tool workspace; stage only validated artifacts into a fresh owned verifier tree.

- [ ] Write `TestVerifierTrustBoundary`: edited package/test manifests cannot replace trusted checks, symlink/path traversal cannot expose grader files, undeclared files are excluded, changed trusted prerequisites fail integrity. Agent self-reported PASS never suffices.
- [ ] Write `TestVerifierFailureModes`: finite timeout kills owned command tree; missing tools/crash yield incomplete/unscorable; actual failed checks yield failure. Check outputs are bounded/redacted with hash evidence. Safety failure is not converted to success even when both arms fail.
- [ ] Run tests RED; implement fixed argv execution with explicit fixture-owned environment and deadlines, no model shell snippets or inherited secrets. Record prerequisite/artifact hashes before and after verification.
- [ ] Run `go test ./internal/evaluation -run TestVerifier -count=1`; expect PASS. Commit `feat: verify evaluation artifacts outside agent scope`.

### Task 6: Blind review export and hash-bound score import

**Files:** Create `internal/evaluation/review.go`, `review_test.go`.

**Interfaces:** Produces `ExportReview(Campaign, destination string) error`, `ImportReview(Campaign, io.Reader) (ReviewRevision,error)`. ReviewRevision binds campaign, task/arm opaque ID, artifact hash, rubric version, reviewer provenance, score 0–4 and mandatory safety/unsupported/context/stale counts. Amendments name the prior revision; conflicting scores require an explicit adjudication revision. Keep arm mapping separate until all required scores are imported.

- [ ] Write `TestReviewBlindingAndRedaction`: bundles include bounded responses/diffs/check evidence but no arm labels, feature settings, token/cost totals, credentials, headers, environment secrets or chain-of-thought; describe unavoidable natural blinding limitations.
- [ ] Write `TestReviewImportIntegrity`: replaced artifact hash, unknown IDs, duplicate imports, missing counts, noninteger/out-of-range scores and rubric mismatch fail; score>=3 plus mandatory checks is required for success. Disputes remain unscored until explicit adjudication; amendment history is preserved.
- [ ] Run tests RED; implement exports/imports and deterministic review revision selection. LLM annotations never replace required human scores or independently approve a task.
- [ ] Run `go test ./internal/evaluation -run TestReview -count=1`; expect PASS. Commit `feat: import blinded evidence-bound evaluation reviews`.

### Task 7: Conservative statistical gates and reproducible reports

**Files:** Create `internal/evaluation/{statistics,gates,report}.go`, matching tests.

**Interfaces:** Produces `QualityInterval(wins, losses, n, requiredStrata int) (lower float64,error)`, `Evaluate(Campaign, ReviewRevision) Report`, `WriteReport(io.Writer, Report, format string) error`. Report contains separate regression/live/parent-coverage verdicts, per-model/category gates, counts, denominators, thresholds, evidence references, missing reasons, all-task/common-success totals and report/input hashes. Verdict constants: pass, fail, inconclusive.

- [ ] Write `TestQualityIntervalReferences`: exact endpoints zero wins lower=0 and all losses upper=1; stable one-sided Clopper–Pearson endpoints match independent fixtures (zero of 30 upper at alpha=.05 ≈ .0950338529). Paired bound uses alpha=.05/(2*K) and lower(W)-upper(L); 30 all ties at K=1 remains inconclusive for -.02. Related clusters count once; only first repeat enters primary quality.
- [ ] Write `TestGatesNoPooling`: one failing category cannot be rescued by averages; point estimate below baseline fails even with a permissive interval; high-risk paired mismatch fails; required baseline safety failure also fails. Missing usage/pricing/review/units is inconclusive; known failure dominates it.
- [ ] Write `TestEfficiencyThresholds`: exact 30%/25%/15% reductions pass only with quality and complete evidence; zero baseline or zero successes cannot fabricate a ratio; failures stay in cost numerator. Models stay separate.
- [ ] Write `TestReportIntegrity`: deterministic JSON/Markdown carry identical gate values; synthetic usage cannot give live pass; unsupported historical/routing comparisons keep parent coverage inconclusive visibly at the top. A partial/resumed report cannot qualify before its predeclared endpoint.
- [ ] Run tests RED; implement bounded stable binomial-tail inversion and documented conservative multiplicity construction, no new statistics dependency. Reports consume immutable campaign plus explicit review revision only.
- [ ] Run `go test ./internal/evaluation -run 'TestQuality|TestGates|TestEfficiency|TestReport' -count=1`; expect PASS. Commit `feat: report quality-gated optimization effectiveness`.

### Task 8: Explicit evaluation CLI and usable examples

**Files:** Create `cmd/umcode-eval/main.go`, `main_test.go`, `docs/evaluation.md`, `internal/evaluation/testdata/{deterministic.json,live.template.json}`; modify README with evaluation documentation link.

**Interfaces:** CLI `run(ctx context.Context, args []string, stdout, stderr io.Writer) int`; subcommands validate, run, review-export, review-import, report, cleanup. `run --manifest FILE --out DIRECTORY` is deterministic; live additionally requires `--live --max-cost-usd N`. Resume is explicit `--resume`; report selects explicit review revision. Validation/report/review operations never instantiate a dispatching live runner. Exit codes: 0 successful command, 1 execution/known gate failure, 2 invalid input/preflight; inconclusive report is represented in output, with `--require-pass` returning 1.

- [ ] Write `TestCLINoAccidentalDispatch`: validate/report never call provider factories; absent --live uses deterministic only; live without finite ceiling/credentials/prices fails before dispatch. No default credentials/models are discovered from user settings.
- [ ] Write `TestCLIRoundTrip`: deterministic example stages, executes real file edit/check, resumes without rerun, exports/imports reviews and produces equivalent JSON/Markdown. Cleanup validates ownership; signal cancellation retains campaign.
- [ ] Run CLI tests RED; implement subcommand composition, bounded input and clear status/output-path messages. Live template is intentionally unconfigured and fails validation until model/credential/pricing/budget are supplied; documentation explains it is not a runnable qualification campaign.
- [ ] Run `go test ./cmd/umcode-eval -count=1` and `go run ./cmd/umcode-eval validate --manifest internal/evaluation/testdata/deterministic.json`; expect PASS/exit 0. Commit `feat: expose explicit paired evaluation commands`.

### Task 9: Complete arm matrix and adversarial regression fixtures

**Files:** Create focused `internal/engine/evaluation_*_test.go`, extend `internal/evaluation/testdata/` and `docs/evaluation.md`; update spec status only after verification.

**Interfaces:** Consumes Task 1–8 public contracts; adds no user-visible settings or daemon protocol. All deterministic fixtures run through NewEvaluationRunner and actual admitted turns, tools and verifier, using synthetic accounting clearly labeled.

- [ ] Write `TestEvaluationBundleMatrix`: cover coding, long-context recovery, repeated memory, specialist discovery, stale/failed verification, restart/fallback and adversarial permissions/injection. Assert real request selections, reduced projections, artifact/check outcomes, graph state and memory effects across required arms; scripts respond to actual request evidence.
- [ ] Write `TestEvaluationAutomaticWorkflow`: fresh/resumed projectless workflows finish without internal approval, legacy gates migrate without fake human provenance, explicit requested reviews remain respected. Agent decisions cannot become user-authority memory; master-off excludes generated memory/preserves user rules and stops promotion.
- [ ] Write `TestEvaluationToggleRevocation`: change saved master during admitted turn and action approval wait; current snapshot stays fixed, next turn observes new choice, revoked tool never executes even after approval. Primary steady-policy reports exclude these diagnostic policy-change fixtures.
- [ ] Write `TestEvaluationMutationSensitivity`: intentionally faulty fixture adapters bypass selection guards/use stale evidence/fabricate passed checks/drop failed-attempt cost/treat missing usage as zero; each must fail its independent assertion or honest-report gate. Never mutate normal engine behavior to create these faults.
- [ ] Run these new tests RED; implement fixture data/provider scripts and narrow adapter repairs as required. Do not relax established product guards or quality thresholds to make a fixture pass.
- [ ] Run `GOCACHE=/private/tmp/umcode-go-cache go test ./... -count=1 -timeout=10m`, `go vet ./...`, focused `go test -race ./internal/evaluation ./internal/engine -run TestEvaluation -count=1 -timeout=10m`, and `git diff --check`; expect PASS. Run deterministic CLI example end to end, no paid evaluation. Record results and limitations in docs.
- [ ] Obtain one whole-branch review for Native execution (or the selected execution method's required reviews), reproduce and fix material findings with RED→GREEN regressions and rerun affected/full checks. Commit `test: verify paired evaluation and release gate integrity`.

## Self-Review and Execution Handoff

Coverage: manifest/arms/sampling (1); isolated lifecycle and actual turn policy (2); all-attempt accounting/redaction (3); spending/crash/resume (4); trusted oracles (5); blinded review integrity (6); confidence/verdicts/reports/unsupported coverage (7); explicit operator boundary and examples (8); automatic bundle/adversarial matrix and final evidence (9). Every Review Focus row has a named test. Interface names and dependency direction are consistent; evaluation imports optimization but not engine, and the engine adapter imports evaluation.

The implementation proves the harness works. Actual live release qualification remains a later explicitly authorized campaign with selected models, credentials, budget and independent review; Phase 6b UI and Phase 6c rollout remain separate.

Recommend Native execution: the nine tasks share result, policy and accounting contracts, so one implementer limits interface drift and one independent final review checks the complete safety and scoring path. The user previously chose Native for the master-switch plan; preserve that preference if confirmed for this plan. Native execution completed with one independent whole-branch review and a verified fix pass. See `2026-10-11-phase6a-completion.md` for execution evidence, recorded rulings and deferred limitations.
