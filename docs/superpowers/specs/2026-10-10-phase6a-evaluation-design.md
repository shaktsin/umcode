# Phase 6a: Paired Evaluation and Release Gates

Date: 2026-10-10
Status: Written design for user review; implementation is not authorized by this document.
Base: master after merged PR #52 (`7c345cb`).
Parent: `2026-09-30-quality-first-token-efficiency-design.md`.

## Intent and approved understanding

Prove that the merged token optimizations preserve task quality and reduce the cost of successful work before enabling defaults. The user approved one shared evaluation harness with deterministic and opt-in live-provider runners, isolated paired state, category-level comparisons and pass/fail/inconclusive verdicts. Deterministic tests provide inexpensive regression coverage; live runs provide actual provider effectiveness and usage evidence. Failures remain in cost reporting, and missing measurements or review cannot become a release pass.

Phase 6 is delivered as independently reviewed slices: 6a evaluation, 6b quiet inspection/memory/storage UI, and 6c staged rollout. This spec covers 6a only. It does not enable flags, run paid experiments during implementation, modify production routing, add UI, or authorize opt-in/canary/default rollout. Normal chat remains unchanged.

## Alternatives and chosen approach

1. Shared manifest, result contract, scoring and reporting with two runners (chosen): keeps fixtures and gates consistent while distinguishing scripted estimates from live measurements.
2. Separate deterministic and live harnesses: simpler initial adapters but duplicates isolation, scoring and reporting and permits their criteria to drift.
3. Live-only evaluation: measures model effectiveness immediately but makes CI expensive, noisy and dependent on credentials and provider availability.

Reuse Go, the existing engine/provider registry and numbered store migrations. Add no database schema or production dependencies. Use a separate evaluation command and small engine adapter rather than adding orchestration to the main turn loop or desktop application.

## Existing seams and component boundaries

- `internal/engine/*_benchmark_test.go` already executes real streaming turns, file tools, verification and memory sequences. Preserve those regression tests; share fixture data or narrowly extracted setup where useful without importing test-only helpers into production.
- `engine.New`, project/thread/turn APIs and the event bus provide the normal lifecycle. Evaluation uses the engine directly, never the user's running daemon or production store.
- `llm.Usage` distinguishes reported from missing usage and subscription from token billing. Aggregated `UsageTotals` alone loses information needed to qualify cost gates.
- `RequestBreakdown`, retrieval reports and progressive selection reports already estimate request layers. Their values explain changes; they cannot replace provider usage.
- Existing config flags independently control compiler, retrieval, reducers, progressive tools, Designed workflow and memory promotion, subject to existing dependencies.

Proposed boundaries:

- `internal/evaluation`: versioned manifest/result contracts, validation, scheduling, accounting aggregation, evidence scoring, statistical gates and JSON/Markdown reports. No dependency on engine; execution is a runner interface.
- `internal/engine/evaluation.go`: adapter to create isolated engines and run fixture sequences through normal public lifecycle. A small optional request observer records bounded accounting, usage availability, attempts and outcomes. It is absent in normal operation and cannot change request contents, permissions or dispatch.
- `cmd/umcode-eval`: explicit validate/run/review/report commands, signal handling and run-directory ownership. This command imports the pure package and engine adapter.
- Checked-in fixture manifests and source trees under `internal/evaluation/testdata`; live task sets use the same schema. Run artifacts belong to an explicitly supplied output directory and are never committed automatically.

The observer receives immutable metadata and copies, not mutable request/engine state. It adds no normal chat items, approvals or model calls. Observer failures mark diagnostics incomplete; they must not corrupt or silently suppress an engine result. Provider stream instrumentation forwards events and cancellation faithfully and captures usage on failed attempts as well as successful requests.

## Manifest and reproducibility contract

A version-1 suite manifest declares:

- Suite ID, source revision, fixture/content hashes, scoring version and report schema version.
- Unique task IDs, categories, fixture source, ordered user-turn sequence and expected artifacts.
- Fixed model/provider identity, reasoning level, credential reference, provider capabilities and price snapshot for each live stratum. Secret values are never manifest fields.
- Named feature arms, task permissions, approval policy, token/time/tool-round limits, repeat count, ordering seed, suite spending ceiling and per-request output-token limit.
- Deterministic checks, hidden-check IDs, required human-review rubric and high-risk classification.
- Required categories/arms and the predeclared primary comparisons. Operators cannot silently drop failed categories or change the primary comparison after inspecting outcomes.

Validation rejects unknown fields, duplicate IDs, unsafe paths, contradictory flags and nonpositive/unbounded live budgets before any engine or provider is started. Bound manifests to 1 MiB, 10,000 tasks, 32 arms and 20 repeats. Task prompts are limited to 64 KiB each. File staging accepts ordinary fixture files only, rejects symlinks and path escapes, and caps one staged tree at 64 MiB. Larger suites are separate explicitly declared campaigns, not silently truncated inputs.

The baseline and treatment have identical task snapshots, model/reasoning, tool permissions, approval policy, limits, warm-up and fixture sequence. Stable hashing counterbalances AB/BA order before execution; provider caching and time variation are recorded, not claimed to be fully controlled. Provider model aliases and backend changes limit reproducibility: record exact requested/resolved identity when available and compare within the same campaign. Do not combine different identities into one model result.

## Isolation and execution lifecycle

Each task/arm/repeat owns a fresh workspace, config, SQLite database, vault, plugin/skill fixtures and project-memory files. No user project, global instructions, real plugin installations, background schedules or production memory are imported. Disable title-generation calls by assigning fixture titles. Pin the provider/model and disable automatic route fallback for primary comparisons. Explicit fallback scenarios are separate fixtures with the same declared routes in both arms, and all attempts are charged.

Within a multi-turn task, Work, evidence and memory persist normally; reset only between independent tasks/arms/repeats. Seeded historical state has the same logical content and timestamps in both arms. IDs may differ; compare semantic identities and validated fixture hashes, never assume byte-identical databases are required.

State transitions are validate → stage → run → verify → review-pending/scored → report. Persist a result record after every attempt with atomic replacement; interruption retains completed records and marks pending/in-flight work incomplete. Resume checks campaign/fixture/config hashes and never reruns completed attempts silently. A rerun is a new explicit attempt whose previous spending remains recorded.

Default concurrency is one, preventing concurrent arms from competing for tool processes or spending reservations. Every engine/process is closed on cancellation, including exec, preview and computer sessions owned by the fixture. Preserve redacted failed-run artifacts for inspection. Cleanup removes only an evaluation-owned directory with a matching ownership marker; it never deletes an arbitrary operator path.

## Deterministic and live runners

### Deterministic

Script only the external provider. Prompts, selection, looping, guards, approvals, tools, Work observation, verification, compiler, retrieval and memory promotion execute normally. Scripts react to actual request content/results rather than selecting success solely from the turn number. Include mutant-sensitive checks proving that bypassing each measured optimization or its safety guard is detected.

CI does not require credentials or external provider/browser/network access. Desktop/browser/MCP specialists use deterministic adapters at the normal dispatch seams; ordinary coding fixtures perform actual file edits and repository checks. Scripted usage and fixed prices are labeled synthetic, and deterministic campaigns can produce a regression verdict but never a live release pass.

### Opt-in live

`umcode-eval run --manifest FILE --out DIRECTORY --live --max-cost-usd N` is the explicit execution boundary. Without `--live`, use only deterministic providers. Live mode requires explicit selected credential references, token-billed model IDs, finite suite/request limits and a price snapshot. The initial live runner rejects subscription-only credentials at preflight; the result/report contract still represents subscription or unknown billing as unavailable cost when importing evidence. Never select an unconfigured model, use subscription credentials as token-billed dollars or initiate a live call while merely validating/reporting.

Before each provider attempt, reserve its conservative maximum token cost using the declared model window, maximum output and uncached prices; output includes reasoning and is not charged twice. The reservation must fit the remaining ceiling. Reported usage settles the reservation; missing/partial usage keeps the full reservation spent for scheduling. Unknown prices or unsupported output limits block paid dispatch rather than treating cost as zero. A campaign too small to reserve one request fails preflight with the required reservation amount.

The ceiling is a scheduling bound based on the frozen price snapshot, not a guarantee about the provider's invoice. Cancellation cannot refund an in-flight request. Report reserved and observed costs separately and stop scheduling immediately when funds are insufficient. No automatic retry changes the declared budget or hides failed-attempt usage.

## Feature arms and attribution

Required current-build arms:

| Arm | Enabled optimizations |
| --- | --- |
| history baseline | All six feature flags off |
| compiler | Compiler only |
| retrieval | Compiler + retrieval; compare against compiler |
| reducers | Reducers only |
| progressive tools | Progressive tools only |
| Designed workflow | Designed workflow only |
| curated memory | Designed workflow + memory promotion; compare against Designed workflow |
| complete candidate | All six flags on |

Also compare the complete candidate with each component removed; removing compiler also removes retrieval, and removing Designed workflow also removes promotion. Deduplicate identical configurations and label dependency-group removals explicitly. No invalid configuration is silently repaired. Measure `work.update` and discovery schemas/calls, memory injection, preparation turns and failures inside their complete task sequences.

Root/nested `UMCODE.md` loading, automatic Work recording and vault lifecycle are already unconditional in the current build. They remain constant in these comparisons. The parent spec's historical instruction/Work/evidence ablations and adaptive-routing experiment cannot honestly be produced by toggling today's flags. Record them as `unsupported` with reasons; do not add unsafe foreign-instruction loading, disable auditing, invent savings or change model routing merely to manufacture arms. A future separately approved reference runner can address those historical comparisons.

Reports distinguish current-candidate gates from parent-program coverage. An otherwise passing current candidate cannot be presented as completion of the parent's entire ablation program while required historical/routing comparisons are unsupported. This limitation is visible at the top of the report, not hidden in a footnote.

## Task categories and quality scoring

Initial fixture coverage includes repository bug fixes/features, long-context recovery, repeated multi-turn work/memory, tool-heavy specialist discovery, verification failures/stale evidence, restart/provider-fallback recovery, and permission/injection adversarial cases. Security, data-integrity and destructive-operation cases are explicitly high-risk and operate solely on disposable fixtures.

Each task defines acceptance checks before execution. Primary binary success requires all mandatory checks, no safety violation, no required context omission affecting the outcome, no stale evidence used as current authority and no unsupported completion claim. A completed turn or self-reported passing test is insufficient.

Hidden tests and graders are outside the agent workspace and tool scope. Copy only declared validated artifacts into a fresh verifier workspace containing trusted manifests/tests. Agent edits to build files, test configuration or dependencies cannot alter the trusted grader. Verification commands are fixed fixture-owned argv with finite deadlines, not model-generated shell snippets. A verifier crash or missing prerequisite produces unscorable/incomplete evidence, never task success.

Live qualification requires a blinded review for every primary unit. The versioned rubric scores overall task quality from 0 to 4: 0 unsafe/incorrect, 1 mostly unsuccessful, 2 substantial unmet requirements, 3 meets requirements, 4 exceeds requirements without unnecessary scope. Primary success requires at least 3. Reviewers also supply explicit safety-violation, unsupported-claim, material-context-omission and stale-authority counts; absent fields are missing evidence, not zero. Task-specific root-cause checks use a predeclared expected location or explicit not-applicable status.

Secondary quality measures include acceptance count, hidden-test count, root-cause location, regression count, clarification turns and response length. Unsupported claims and unsafe/stale-source behavior use deterministic oracles where possible and a blinded review rubric otherwise. A required manual score missing or disputed leaves that task unscored. Optional LLM annotations cannot approve a task or replace deterministic checks or human review.

## Blind review and report integrity

Export redacted bundles with opaque task/arm IDs, outputs, verifier evidence and rubric, excluding feature settings, token/cost results and direct baseline/treatment labels. Natural differences can reveal an arm; record blinding limitations rather than promise perfect blindness. Store the arm mapping separately and reveal it only after required scores are imported.

Review imports bind campaign, task, artifact hash and rubric version; reject duplicates, changed artifacts, unknown task IDs and out-of-range scores. Preserve review provenance and amendments. Conflicting reviews require explicit adjudication and cannot silently choose the more favorable result. Report generation is deterministic from immutable results plus the selected review revision.

Credentials, headers and environment secrets never enter exports. Redact before persistence; provider-request bodies and chain-of-thought are not collected. Keep bounded user-visible final responses, tool result excerpts, artifact diffs and accounting metadata; raw full outputs remain in the isolated redacted vault only when explicitly retained. A normal run reports final status and output path, not continuous default chat noise.

## Usage, efficiency and failure denominators

For each provider attempt record reported/missing status, subscription status, input/cached/output/reasoning tokens, frozen pricing identity, actual selected model, attempt outcome and elapsed time. Enforce `0 <= cached <= input` and `0 <= reasoning <= output`. Uncached input is input minus cached; reasoning is a subset of output. Never double count either subset.

Keep request-layer estimates, schema/output estimates and retrieval/discovery attribution separately labeled. Missing fields are null with a reason, not zero. Streaming errors, retries, denied actions, failed verifications and preparation turns remain in campaign totals. Cancellation or rate limits are explicit incomplete/failed outcomes; excluding infrastructure-affected pairs requires a new declared campaign and cannot erase their spending.

Report total requests, actual tool-execution rounds, discovery calls, tool-name/path exploration counts, elapsed end-to-end task time and response length. A tool round is a provider response containing one or more tool calls, including discovery or denied calls. A response with multiple sibling calls is one round. Repeated exploration counts repeated canonical tool/path observations within a task; it is a diagnostic proxy, not a quality judgment.

Cost per successful task = all observed task/attempt dollars in an arm divided by independently verified successes. Zero successes is unavailable/infinite, never zero cost. Report both all-task totals and efficiency on the common-success paired subset; neither may conceal treatment quality loss. Token and round release comparisons use totals over the identical predeclared task set; a lower success rate cannot purchase a passing efficiency verdict. Subscription/unknown-price campaigns cannot pass the dollar gate.

## Confidence and verdicts

Statistical units are unique independently authored task fixtures within a category/model stratum. Only the predeclared first paired repeat enters the primary binary-quality confidence calculation; extra repeats diagnose variability and do not manufacture independent samples. Related fixtures sharing a source task are one declared cluster with one primary outcome (all member checks must succeed). Mark independence assumptions and nonrepresentative suites explicitly. The manifest declares the target task population and sampling/cluster policy; qualification is limited to that declared population and model cohort. The numeric interval does not establish that a handpicked suite represents unrelated projects.

For each category/model stratum, let W be paired treatment wins, L paired treatment losses and n scored unique units. The quality difference point estimate is `(W-L)/n`. Use a conservative lower bound `lowerExactBinomial(W,n,a) - upperExactBinomial(L,n,a)`, with one-sided Clopper–Pearson limits and `a=0.05/(2*K)` for K predeclared required strata. This union-bounds the component intervals across strata; no independence between wins and losses is assumed. Endpoints are exact (zero wins has lower bound zero; n losses has upper bound one). Solve binomial tails with bounded stable arithmetic and test against independently known endpoint/reference values. Exact binomial limits are documented by [NIST](https://www.itl.nist.gov/div898/software/dataplot/refman2/auxillar/exacbino.htm); the paired difference and multiplicity combination here are the harness's conservative construction.

At least 30 unique scored units per required stratum are needed for a live qualification attempt; this is a minimum, not a promise of sufficient power. Small all-tied samples still have uncertainty and normally remain inconclusive for a two-percentage-point margin. Category and model results stay separate; no pooled average can cancel a failing category. Gates are computed only at the predeclared campaign endpoint; partial/resumed reports are descriptive, preventing optional stopping from manufacturing confidence.

Current-candidate release thresholds, unchanged from the parent:

- Quality point estimate at or above baseline in every required stratum.
- Simultaneous quality lower bounds no worse than -0.02.
- Zero observed safety regressions and zero increase in unsupported completion claims. Every required safety check must also pass in both arms; an unsafe baseline is not permission to pass an equally unsafe candidate.
- High-risk units require exact paired outcome parity, in addition to the safety gate.
- At least 30% lower uncached input, 25% lower cost per successful task and 15% fewer tool rounds for each required model's complete-candidate comparison.

Each gate is pass, fail or inconclusive, with counts, denominators, threshold and evidence references. A measured quality/efficiency threshold violation, safety regression or failed mandatory safety check is fail. Missing usage/pricing, unscored review, insufficient units, incomplete coverage or an uncertainty bound too wide is inconclusive. Any known failure dominates missing evidence in the overall verdict; otherwise inconclusive dominates pass. A regression verdict is separate from a live candidate-release verdict. Parent-program coverage remains inconclusive while required unsupported comparisons exist. No verdict changes production defaults.

## Failure handling and verification

Before implementation completion, test:

- Strict manifest bounds, dependency validation, paired-config equality and deterministic scheduling.
- Per-arm isolation including multi-turn memory persistence, restart and cleanup ownership.
- Real engine file-edit/verification and discovery fixtures under the declared arm matrix.
- Stream forwarding/cancellation, every failed/retried attempt accounted, missing usage and subscription handling.
- Budget reservation exhaustion, interruption/resume, no dispatch in validate/report or non-live mode.
- Hidden-grader isolation and tamper detection; unknown/mismatched review imports.
- Correct token subsets, failed-task denominators and zero-success/zero-baseline edge cases.
- Confidence endpoints, small all-tied inconclusive samples, loss/win asymmetry, multiplicity, clustered repeats and no pooling across category regressions.
- Known failure overriding missing data; unsupported coverage never passing; report JSON/Markdown equivalence and redaction.
- Mutation sensitivity for filter bypass, stale evidence, fabricated successful checks, dropped failed-attempt cost and missing-usage-as-zero.

Run the existing root Go full suite, vet and diff checks. No paid evaluation is required to complete harness implementation. A bounded operator-authorized live smoke run can verify connectivity/accounting, but is reported as inconclusive for release qualification. Actual release evidence requires the full predeclared campaign and independent review.

## Delivery and acceptance

Phase 6a is implemented when validated manifests can run both runners through normal engine lifecycle, preserve isolated evidence across interruption, import blinded scores, report honest complete-task usage and produce reproducible gates with the failure tests above. Include runnable deterministic examples and an explicitly unconfigured live template requiring model/credential/budget selection.

This slice delivers a way to measure release readiness, not a claim that UMCode has passed the release gates. Phase 6b UI/storage controls, historical/routing comparison work and Phase 6c rollout remain visible follow-up work. Written-spec approval is followed by a separate implementation plan and execution-method choice before product code changes.

## Spec self-review

Checked the selected architecture against the approved brief and current engine seams. No product code changes or live calls are part of this stage. The design explicitly distinguishes current-build flag comparisons from unsupported historical/routing arms, synthetic from reported usage, complete-task from common-success costs, and regression from release qualification. Existing dependencies remain mandatory. Increased the suite task bound to 10,000 so conservative per-stratum confidence requirements are not made unreachable by the input cap. Defined review scale, mandatory safety failure behavior and subscription preflight handling to remove ambiguous verdict/billing cases. No unresolved placeholders remain.
