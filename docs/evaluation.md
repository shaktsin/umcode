# Paired token optimization evaluation

`umcode-eval` runs isolated campaigns against the saved global optimization master switch. It does not change normal app settings. Master-on enables the automatic bundle; private evaluation arms can remove dependent groups for attribution. Normal action approvals still apply. No paid calls occur unless `run --live` is explicitly supplied.

## Deterministic example

From the repository root:

```sh
go run ./cmd/umcode-eval validate --manifest internal/evaluation/testdata/deterministic.json
go run ./cmd/umcode-eval run --manifest internal/evaluation/testdata/deterministic.json --out /tmp/umcode-evaluation
go run ./cmd/umcode-eval run --manifest internal/evaluation/testdata/deterministic.json --out /tmp/umcode-evaluation --resume
go run ./cmd/umcode-eval report --out /tmp/umcode-evaluation --format markdown
go run ./cmd/umcode-eval review-export --out /tmp/umcode-evaluation --destination /tmp/umcode-review
go run ./cmd/umcode-eval cleanup --out /tmp/umcode-evaluation --campaign deterministic
```

The example executes a real file write through admitted engine turns for master-off and master-on. A separate trusted script checks the staged artifact. Synthetic provider accounting is labeled and cannot qualify a live release. The one-task example cannot establish quality noninferiority or token savings. Equal synthetic tool rounds are expected.

## Live campaigns

`internal/evaluation/testdata/live.template.json` is intentionally invalid and unconfigured. Supply explicit model/provider, complexity preset (`quick`, `standard`, or `deep`), API-key environment variable reference, context/output limits, frozen USD-per-million pricing, campaign budget, representative tasks, immutable fixture/trusted grader hashes, and declared comparisons. Never put secrets in the manifest. No user credential discovery occurs. Subscription credentials cannot support dollar qualification.

```sh
go run ./cmd/umcode-eval run --manifest /path/to/configured.json --out /tmp/live-evaluation --live --max-cost-usd 5
```

Every paid dispatch reserves its conservative maximum durably before invocation. Missing usage retains the reservation. Cancellation preserves the campaign; resume marks uncertain units incomplete and never silently repeats paid work. Start a separately budgeted campaign to retry uncertain work. Output must be outside source fixtures. Cleanup requires an ownership marker and matching campaign identity.

Trusted verifier roots must be outside the staged agent workspace. Only declared artifacts enter the verifier workspace, with fixed manifest argv and bounded process time/output. A model's own claim that tests passed is not evidence.

## Independent review and reports

Review export withholds arm labels, accounting, policy and schedule mapping. Natural output differences may reveal treatment. Review all runs used in live success denominators, including repeats; only first repeats contribute independent quality samples. Keep the private campaign away from reviewers. Exported artifacts and responses are bounded and redacted.

Review revision JSON contains `id`, `campaign`, `rubric_version`, `reviewer`, and `scores`. Each score requires `opaque_id`, `artifact_hash`, integer `score` (0–4), and nonnegative counts `safety_violations`, `unsupported_claims`, `context_omissions`, `stale_authority`. Opaque IDs and artifact hashes come from exported bundles. Scores of at least 3 plus passing mandatory checks and zero adverse counts constitute successful tasks. Missing scores remain unknown. Revisions are immutable; amendments name `prior_id`; conflicting reviewers require explicit adjudication.

```sh
go run ./cmd/umcode-eval review-import --out /tmp/live-evaluation --scores /path/to/revision.json
go run ./cmd/umcode-eval report --out /tmp/live-evaluation --review-revision reviewer-v1 --format json --require-pass
```

JSON and Markdown contain the same complete measurements and provenance. Exit 2 means invalid input/preflight, exit 1 means execution/known regression failure or a nonpassing `--require-pass` gate. A normal report may represent inconclusive evidence with exit 0.

Quality is paired by independent cluster within model/category, with simultaneous conservative exact-binomial bounds. Thirty independent units per stratum is a minimum, not a power guarantee. Release requires nondecreasing point quality, lower bound at least −2 percentage points, high-risk parity, no safety regression, and efficiency reductions of 30% uncached input, 25% total cost per successful task and 15% tool rounds. Failures and retries remain in cost totals; common-success views are diagnostic. Missing usage, scores or incomplete endpoints cannot pass. Historical instruction/Work/evidence and adaptive-routing comparisons remain unsupported, so parent-program coverage is inconclusive. Phase 6b UI/storage and Phase 6c rollout remain separate.
