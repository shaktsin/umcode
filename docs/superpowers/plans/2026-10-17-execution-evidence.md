# Execution Evidence Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [x]`) syntax for tracking.

**Goal:** Prevent model narration from completing requested actions without real execution and verification evidence.

**Architecture:** Extend ExecutionTask with persisted requested outcomes and reconcile them against existing engine operations. Resolve intent separately from execution, replay history as identified data, and allow two corrective responses when requested work has not executed. Existing authority checks remain on every real call.

**Tech Stack:** Go, SQLite/modernc, existing provider-neutral LLM requests, Svelte/TypeScript, Go tests and Vitest. No new dependency.

**Spec:** `docs/superpowers/specs/2026-10-16-execution-evidence-design.md` — approved by user 2026-10-17.

## Global Constraints

- Existing engine approvals remain authoritative.
- Expectations express desired outcomes and never grant action authority.
- No commands or tool calls are extracted from prose and executed as recovery.
- Allow at most two additional corrective model responses per turn, within existing time, cost, context, cancellation, and round limits.
- A later explicit user retry may start a new bounded turn while retaining the same unfinished task.
- Preserve raw model text, failed/denied invocation history, and uncertain-effect safety.
- Ordinary discussion can finish without tools; missing resources can satisfy an existence inquiry but not a creation request.
- No automatic live-database repair, paid live-model run, provider change, release, or publication.
- Preserve the uncommitted legacy-denial fix in `task_outcomes.go`, its test, and all existing `TASKS.md` edits. Establish their baseline before implementation; stage only exact task-owned files.
- Retain native execution, as previously selected by the user. Implementation waits for review of this plan.

## Review Focus

1. New inspection requests within an unfinished write task must not silently erase the write or require the write before answering the inspection (Task 2).
2. A truncated read, symlink retarget, or stale file observation must not certify requested contents (Task 4).
3. Compiled context or provider fallback must not reintroduce assistant-authored fake receipts or orphan tool-result messages (Task 3).
4. Cancellation between reserving a correction and receiving its response must not create an unlimited retry allowance (Task 5).
5. A quoted error or real denial of another action must not become a claim that all tools are prohibited (Tasks 4–6).

## File structure and dependency order

- `internal/protocol/task_expectations.go`: expectation and evaluation DTOs.
- `internal/store/task_expectations.go`, migration `0031_task_expectations.sql`: CAS expectation batches, evidence resolution, per-turn correction reservations.
- `internal/engine/task_intent.go`: private schema-constrained interpretation, provenance and scope validation; no execution.
- `internal/engine/history_evidence.go`: bounded labelled historical data using existing provider message capabilities.
- `internal/engine/task_expectations.go`: evidence matching and current-turn evaluation, alongside existing operation reconciliation.
- `internal/engine/task_execution_recovery.go`: bounded correction and engine-authored terminal outcome.
- Existing `turn.go`: narrow integration points for intent, history, no-call termination, and private model calls.
- Existing task context/protocol/UI modules: additive fields and authoritative outcome display.
- Tests live next to the owning code, plus public socket acceptance tests.

Tasks 2 and 3 consume Task 1. Task 4 consumes Tasks 1–3. Task 5 consumes Tasks 2–4. Tasks 6–7 consume Task 5. Do not ship intermediate commits as independently complete fixes.

## Shared interface contract

Add the following DTOs in Task 1, using lower-camel JSON names:

- `TaskSourceSpan { ItemID string; Start, End int }`: rune offsets, half-open, into the exact persisted user item. Do not copy long original requests into metadata.
- `TaskExpectation { ID, TaskID, OriginItemID, RequestTurnID string; Revision int; Source TaskSourceSpan; Kind, Predicate, Status string; Resource json.RawMessage; AllowExisting bool; EvidenceIDs []string }`.
- Kinds: `explanation`, `inspection`, `mutation`. Predicates: `answer`, `exists`, `read`, `file_content`, `operation_verified`. Status: `pending`, `satisfied`, `superseded`. A negative existence observation satisfies `exists`, with its result retained.
- Explanation uses resource `{}`; other kinds require a strict typed envelope: `{type: file|operation, path?: string, tool?: string, target?: string, expectedSHA256?: string}`. Engine-derived canonical bindings are stored separately in the same expectation data, never accepted as model authority.
- `TaskIntentProposal { TaskID string; ExpectedRevision int; OriginItemID string; Relation string; Expectations []TaskExpectationProposal; Question string }`.
- `TaskExpectationProposal { Source TaskSourceSpan; Kind, Predicate string; Resource json.RawMessage; AllowExisting bool }` contains no status, approval, evidence, implementation identity, or engine binding fields.
- `TaskExpectationEvaluation { Status string; Missing []string; EvidenceIDs []string; Failure *ActionFailure }` with statuses `satisfied`, `not_executed`, `unverified`, `blocked`, `clarify`.
- `TaskSnapshot.Expectations []TaskExpectation` and `ExecutionTask.ExpectationsVersion int` are additive; version 0 is unresolved legacy intent, version 1 is the validated contract.

Bounds chosen for implementation: 16 expectations per batch, 16 KiB proposal, 64 evidence references per expectation, 2 KiB clarification text. Original tool payload and file sizes keep their existing independent limits.

---

### Task 1: Durable expectations and correction reservations

**Files:** Create protocol/store expectation modules and tests, `internal/store/migrations/0031_task_expectations.sql`; modify `internal/protocol/task.go`, `internal/store/task.go`.

**Interfaces:**
- Consumes: existing `ExecutionTask`, `TaskSnapshot`, `ErrTaskConflict`, transaction helpers and ownership checks.
- Produces: DTOs above; `(*Store).SetTaskExpectations(ctx context.Context, taskID string, expectedRevision int, originItemID string, requestTurnID string, proposals []protocol.TaskExpectationProposal) (protocol.TaskSnapshot, error)`; `(*Store).ResolveTaskExpectation(ctx context.Context, id string, expectedRevision int, evidenceIDs []string) (protocol.TaskExpectation, error)`; `(*Store).ReserveTaskCorrection(ctx context.Context, turnID string) (int, bool, error)`.

- [x] **Step 1: Write persistence regression tests.** `TestTaskExpectationsPersistence` asserts reopen retains source, target, pending state, and task version; CAS stale updates and cross-thread origins fail without partial writes. `TestTaskExpectationLimits` rejects oversized/unknown fields and forged evidence ownership. `TestTaskCorrectionReservation` asserts two successful atomic reservations, a third denied, reopen does not reset, and a different turn starts at zero. Run concurrent reservations and require exactly two accepted.
- [x] **Step 2: Run RED.** `GOCACHE=/tmp/umcode-go-cache go test ./internal/store ./internal/protocol -run 'TestTaskExpectation|TestTaskCorrectionReservation' -count=1`.
  Expected: missing DTO/API failures before implementation.
- [x] **Step 3: Implement additive storage.** Create expectation rows with task FK, revision, status and bounded JSON; create turn correction rows with turn FK and count CHECK 0..2. Set expectations and task version/revision atomically; make repeated identical origin/request-turn batches idempotent and reject divergent duplicate batches; append new inquiry expectations without superseding unresolved mutation expectations. Only existing approved scope replacement may supersede a contract. Resolve evidence only for same-thread persisted tool/result items; engine checks semantics in Task 4. Expose expectations in the same read transaction as TaskSnapshot. Old tasks default to version 0.
- [x] **Step 4: Run GREEN.** Repeat Step 2, then `GOCACHE=/tmp/umcode-go-cache go test ./internal/store ./internal/protocol -count=1`.
  Expected: PASS including legacy migrations/rollback; original item and operation history unchanged.
- [x] **Step 5: Commit exact task files.** Message: `feat: persist requested outcomes and correction budgets`.

### Task 2: Resolve requested outcomes independently of narration

**Files:** Create `internal/engine/task_intent.go`, `task_intent_test.go`; modify `turn.go`, `task_context.go`, `task_context_tool.go`, protocol `methods.go` and `task_expectations.go`.

**Interfaces:**
- Consumes: Task 1 DTOs and SetTaskExpectations; existing `callModel`, `resolved`, `turnBudget`, `currentTaskUser`, task scope confirmation.
- Produces: `(*Engine).resolveTaskExpectations(ctx, sctx context.Context, th protocol.Thread, turn protocol.Turn, user protocol.Item, res *resolved, budget *turnBudget, explicit *protocol.TaskIntentProposal) (protocol.TaskSnapshot, error)`; `(*Engine).validateTaskIntent(ctx context.Context, th protocol.Thread, turn protocol.Turn, proposal protocol.TaskIntentProposal) ([]protocol.TaskExpectationProposal, error)`; `taskIntentSchema() json.RawMessage`.

- [x] **Step 1: Write resolver tests.** `TestTaskIntentResolution` scripts strict proposals for save-file, inspect-existence, read-headings, explanation, mixed requests and ambiguous requests. Assert exact target and predicate persisted, origin spans match original text, no model-supplied approval/status accepted, and tool-free action narration is never a proposal. `TestTaskIntentContinuity` asserts retry reuses intent without another classifier call; old task version 0 resolves from its original user item; a new inspection adds a turn obligation without deleting pending writes. `TestTaskIntentScopeValidation` rejects path escape, other-thread origin, invalid span, mismatched revision, unknown predicates, and silent mutation-to-explanation replacement.
- [x] **Step 2: Run RED.** `GOCACHE=/tmp/umcode-go-cache go test ./internal/engine -run '^TestTaskIntent' -count=1`.
  Expected: missing resolver/schema APIs.
- [x] **Step 3: Implement private interpretation.** Add optional `TurnStartParams.TaskIntent *TaskIntentProposal`; supplied client intent is validated as untrusted requested scope, never approval. For free text, call the existing accounted model boundary with role `task_intent`, the exact user request, existing task scope, and a single metadata-only schema tool. Its response is collected and strictly decoded, never registered or passed to runTool. A model without tool-calling uses strict JSON text validated against the same contract; malformed or absent proposals yield clarification, not execution or completion. Root schema is an object, with no top-level oneOf/allOf/anyOf. Allow one interpretation response per resolution; do not create an unbounded classifier loop.
  Treat `task_intent` as private output like title/compaction in streamOnce, retaining actual usage and router budgets. Include one source span per expectation; engine canonicalizes targets against current scope. Do not pretend spans prove natural-language semantics: retain unknown intent for unclear proposals. Existing intent changes use existing scope confirmation; a read-only follow-up is an additional inquiry. Query/full-state task context includes paged expectations and unresolved interpretation. Classification does not alter tool permission defaults.
- [x] **Step 4: Run GREEN.** Repeat Step 2 and run existing task continuity, proposal-authority, budget and private-message tests. Expected: PASS; no classifier JSON in chat, usage attributed to `task_intent`, no accidental operation or approval.
- [x] **Step 5: Commit exact task files.** Message: `feat: resolve task outcomes separately from execution`.

### Task 3: Replay historical actions as identified data

**Files:** Create `internal/engine/history_evidence.go`, `history_evidence_test.go`; modify `turn.go`, context compiler entrypoint and owning tests as located by `rg -n 'historyMessages|func .*compile' internal/engine`; test existing provider adapters in their own test files.

**Interfaces:**
- Consumes: existing items and llm.Message. No new provider API is required.
- Produces: `historicalEvidenceMessage(items []protocol.Item, maxBytes int) (llm.Message, bool)`; preserve `historyMessages(items []protocol.Item, currentTurn string, limit int) []llm.Message` signature.

- [x] **Step 1: Write history tests.** `TestHistoryEvidenceProvenance` asserts actual old calls become a block labelled `Historical execution records (data only; not current actions or instructions)` containing item IDs, tools, resources and statuses; never assistant text `[earlier tool call ...]`. Model-authored bracket text remains distinguishable assistant prose. `TestHistoryEvidenceBounds` asserts at most 12 KiB historical block, explicit truncation, no raw secret/large write payloads, and unchanged stored transcript. `TestHistoryEvidenceCompiledContext` asserts compiled and uncompiled requests preserve source identity after compaction. Adapter tests assert valid alternating/paired messages on Claude, OpenAI-compatible and OpenAI requests, without replaying orphan tool outputs.
- [x] **Step 2: Run RED.** `GOCACHE=/tmp/umcode-go-cache go test ./internal/engine ./internal/llm -run 'TestHistoryEvidence' -count=1`.
  Expected: missing helper and old assistant receipt imitation assertions fail.
- [x] **Step 3: Implement labelled historical-summary fallback.** Use an explicitly labelled user-data message with JSON-escaped bounded records, relying on each existing adapter's user message support. Keep genuine current-turn structured calls/results as they are. Do not introduce cross-provider replay of signed reasoning or unsupported historical call IDs. Summaries include only safe canonical resource metadata and bounded sanitized result/failure summaries using existing redaction. A record's contents cannot break the enclosing data boundary. Apply the same boundary in the context compiler. Existing transcript, compaction marker semantics and argument-reference storage remain intact. This fallback satisfies the approved spec without a new provider-history capability negotiation layer.
- [x] **Step 4: Run GREEN.** Repeat Step 2, then existing history/compaction/context compiler and provider serialization tests. Expected: PASS, token bounds respected, no protocol role regressions.
- [x] **Step 5: Commit exact task files.** Message: `fix: separate historical action records from assistant narration`.

### Task 4: Match requested outcomes to actual evidence

**Files:** Create `internal/engine/task_expectations.go`, `task_expectations_test.go`; modify `task_outcomes.go`, `task_context.go`, builtin read/list evidence hooks if required by the tests.

**Interfaces:**
- Consumes: Tasks 1–3; existing `operationArgs`, `taskFileTarget`, private first-party file proofs, `verifyTaskOperation`, legacy-denial reconciliation.
- Produces: `(*Engine).evaluateTaskExpectations(ctx context.Context, taskID, turnID string) (protocol.TaskExpectationEvaluation, error)`; `(*Engine).recordExpectationEvidence(ctx context.Context, taskID string, item protocol.Item) error`.

- [x] **Step 1: Write matching tests.** `TestTaskExpectationEvidence` asserts real write+read-back satisfies the exact target; unrelated successful tool, quoted receipt, wrong implementation, stale proof, and truncated output cannot satisfy it. `TestTaskExpectationInspection` asserts current actual absence satisfies exists but not read or mutation. `TestTaskExpectationEnsure` asserts full current content plus an explicit ensure contract can satisfy already-matches; plain existence and a newly invented desired hash cannot. `TestTaskExpectationResourceRetarget` changes a symlink after observation and requires revalidation. `TestTaskExpectationOtherActionDenial` preserves a denied shell operation without describing file.read as denied. Include opaque timeout/EOF and unsupported plugin evidence remaining unverified. A delegated success report alone cannot satisfy a parent file mutation; verify the accepted managed-worktree result at the parent canonical target and require existing delegated check results.
- [x] **Step 2: Run RED.** `GOCACHE=/tmp/umcode-go-cache go test ./internal/engine -run '^TestTaskExpectation' -count=1`.
  Expected: missing evaluator and matcher.
- [x] **Step 3: Implement predicates and provenance.** All satisfied states derive from persisted same-task tool/result items, current canonical bindings, actual implementation and verification status. File proof uses full private first-party bytes/hash, not the displayed output. For existence, add a private builtin observation of checked path/existence, including authoritative not-found; do not reinterpret arbitrary tool error text as a filesystem observation. Keep the latest request-turn expectation subset for answering an inquiry, while unfinished parent obligations remain on the task.
  Mutation requires successful matching effect plus matching verification; adapter-unsupported generic `operation_verified` never becomes satisfied by exit status alone. Preserve unknown effects and real requirements. Unsupported content-completeness claims remain unverified, even if a file was written. An inferred document outline can inform the model but cannot certify semantic completeness; only supplied verifiable predicates and actual checks are reported as verified. Existing operation reconciliation runs first, so no expectation can bypass its blockers or inherited child restrictions. Historical evidence may support prior effects; current inquiry evidence must postdate that inquiry unless an engine-verifiable unchanged revision proves freshness.
- [x] **Step 4: Run GREEN.** Repeat Step 2 plus `TestTaskOutcomes`, `TestTaskLegacyWorkflow`, `TestTaskConfiguredReview`, `TestTaskInterrupted`, and delegated requirement tests. Expected: PASS, unknown outcomes and failed attempts preserved.
- [x] **Step 5: Commit exact task files.** Message: `feat: verify requested outcomes against execution evidence`.

### Task 5: Bounded correction before accepting completion

**Files:** Create `internal/engine/task_execution_recovery.go`, `task_execution_recovery_test.go`; modify `turn.go`, `task_outcomes.go`, `turn_budget.go` only if its existing accounting requires integration.

**Interfaces:**
- Consumes: evaluateTaskExpectations and ReserveTaskCorrection; existing callModel, modelOutput, task/workflow recovery states and budget.stopReason.
- Produces: `(*Engine).nextExecutionCorrection(ctx context.Context, taskID, turnID string) (message string, retry bool, evaluation protocol.TaskExpectationEvaluation, err error)`; `(*Engine).finalizeExecutionExpectation(ctx context.Context, turn protocol.Turn, messageID string, evaluation protocol.TaskExpectationEvaluation) error`.

- [x] **Step 1: Write actual-turn regressions.** `TestTaskNarrationWithoutExecution` scripts a save request followed only by fake creation/read/permission text; assert initial response plus at most two corrective responses, no actual tool invocation, no created file and no authoritative completion. `TestTaskNarrationRecovery` corrects using a real write/read and completes with evidence. `TestTaskCorrectionBudgetContinuity` tests reconnect/fallback, same-turn restart accounting and cancellation after reservation. `TestTaskExplanationNeedsNoTools` covers ordinary discussion and quoted marker examples without corrective calls. `TestTaskCorrectionRespectsBlockers` covers genuine denial, pending approval, rejected review, no-actions requirement and unknown shell effects, with no automatic replay. Include a successful irrelevant read followed by fake success.
- [x] **Step 2: Run RED.** `GOCACHE=/tmp/umcode-go-cache go test ./internal/engine -run '^TestTask(Narration|Correction|Explanation)' -count=1`.
  Expected: existing no-call branch accepts unsupported completion or misses correction limits.
- [x] **Step 3: Implement the turn boundary.** Before a tool-free response ends, evaluate current-turn obligations and outstanding task obligations appropriate to the request. A satisfied inquiry may finish its turn without completing its unfinished parent write. If missing execution is recoverable, atomically reserve a correction before the next model response; append current missing targets and actual evidence, retain the previous text as provisional audit, and continue through ordinary callModel/runTool. If a correction emits tools, those effects and their result-processing rounds use existing budgets; count only additional no-execution corrective responses, not the read-back continuation after a real operation. Never reset the reserved count on provider fallback. At exhaustion, emit the exact copy `The model did not execute the requested action.` and authoritative outcome `not_executed`; existing unverified effects get `unverified`, actual blockers retain typed failures. Mark unmet terminal turns failed while the task remains unfinished. Budget exhaustion/cancellation use the existing pause/interrupt outcomes and do not claim success.
  Preserve raw provisional text in item data before replacing the final unsupported summary. Permission explanations in the engine summary reference only actual denial IDs, specific tool and target. Handle `no_execution` explicitly; do not blanket-fail explanations. Avoid putting internal classifier proposals or historical imitation into authoritative status. Keep child scheduling/completion under the existing delegated-task reports and checks, with current reviewer/parent restrictions. Parent requested outcomes require actual accepted effect evidence; child-written success text cannot satisfy them or redefine parent intent. Do not import child-thread source items as parent user authority.
- [x] **Step 4: Run GREEN.** Repeat Step 2 plus task recovery, approvals, execution-budget and child safety regressions. Expected: PASS and maximum two correction reservations in persisted state.
- [x] **Step 5: Commit exact task files.** Message: `fix: recover unexecuted tasks before accepting model completion`.

### Task 6: Display authoritative outcomes without fabricating restrictions

**Files:** Modify `app/frontend/src/lib/taskOutcome.ts`, `taskOutcome.test.ts`, `types.ts`, `components/ChatColumn.svelte`, `components/ItemView.svelte` only where needed; create `taskExecutionOutcome.test.ts`; update `docs/task-recovery.md`.

**Interfaces:**
- Consumes: existing Item.Data plus engine-authored `taskOutcome` metadata `{status, expectationIds, evidenceIds, denialIds}` and preserved `provisionalText`.
- Produces: `executionOutcomePresentation(items: Item[], turnStatus: string): {text: string; details: string; show: boolean}` in taskOutcome.ts.

- [x] **Step 1: Write presentation tests.** `taskExecutionOutcome.test.ts` asserts not-executed explains missing action, unverified distinguishes executed-but-unchecked effects, actual denial cites its action, provisional fake receipts do not become file-change widgets, normal explanation is unchanged, and stream replacement/reload gives identical authoritative result. Preserve real file links/results and correlated error deduplication. Do not implement the separately logged generic clickable-prose or warning/lifecycle redesign.
- [x] **Step 2: Run RED.** `npm --prefix app/frontend test -- taskExecutionOutcome`.
  Expected: missing presentation function/outcome support.
- [x] **Step 3: Implement bounded presentation.** Render existing authoritative engine outcomes consistently; keep raw provisional text and real tool details accessible through the current audit mechanism. Do not synthesize clickable artifact receipts from bracketed model prose. Document the two-correction rule, missing-execution outcome, extra private classification call, approval invariants, and the limits of intent/content interpretation.
- [x] **Step 4: Run GREEN.** `npm --prefix app/frontend test` and `npm --prefix app/frontend run check`.
  Expected: all tests PASS; zero Svelte errors/warnings.
- [x] **Step 5: Commit exact task files.** Message: `fix: present evidence-backed task outcomes`.

### Task 7: Public acceptance, whole-branch verification, local build

**Files:** Create `internal/server/task_execution_evidence_e2e_test.go`; extend owning engine/history/provider tests and user docs only for discovered acceptance gaps.

**Interfaces:**
- Consumes: Tasks 1–6 via actual turn.start/socket, store and filesystem; scripted providers recognize the private intent phase separately from execution responses.
- Produces: verified local branch and optional local test app; no push, PR, release, or change to the running app.

- [x] **Step 1: Add public regression tests.** `TestExecutionEvidenceEndToEnd` covers the reported save/retry/read sequence with fake markers, correction, successful real read-back and no invented permissions on Claude, OpenAI-compatible and OpenAI provider identities. Reopen the store and compact between turns. `TestExecutionEvidenceAuthority` covers auto-all without bypassing explicit requirements, normal action approval, real denial and unknown effects. Add large-request and archived-argument cases; full source remains retrievable and budgets bounded. Assert no production user file is edited by tests.
- [x] **Step 2: Run acceptance RED where new cases expose gaps.** `GOCACHE=/tmp/umcode-go-cache go test ./internal/server -run '^TestExecutionEvidence' -count=1`.
  Expected: any added failing acceptance identifies a specific violated predicate; if all pass immediately, record them as integration verification rather than claiming a new RED.
- [x] **Step 3: Fix only acceptance defects under TDD and run GREEN.** Repeat Step 2. Expected: PASS for all provider identities with genuine socket/store/filesystem boundaries.
- [x] **Step 4: Run final checks on the final tree.**
  `GOCACHE=/tmp/umcode-go-cache go test ./... -count=1 -timeout=15m`
  `GOCACHE=/tmp/umcode-go-cache go test -race ./internal/engine ./internal/store -run 'TestTask|TestExecutionEvidence|TestHistoryEvidence' -count=1 -timeout=20m`
  `GOCACHE=/tmp/umcode-go-cache go vet ./...`
  `npm --prefix app/frontend test`
  `npm --prefix app/frontend run check`
  Expected: all commands exit 0; no race report; zero Svelte errors/warnings. Run real sockets and nested delegated-command cases with host access if the sandbox prevents them; diagnose and record failed runs before retrying. Capture logs without repeatedly rerunning green suites.
- [x] **Step 5: Commit acceptance files.** Message: `test: cover evidence-backed completion across providers`.
- [x] **Step 6: Apply the native execution final-review workflow.** One fresh read-only whole-branch review; re-grade findings by user effect, fix accepted Critical/Important findings with reproductions and rerun affected/final checks. Preserve unrelated edits and do not dispatch repeated reviewers for the same fix pass. Report decisions and any deferred minors.
- [x] **Step 7: Build for local testing after final verification.**
  `UMCODE_APP_OUT="$PWD/bin/UMCode-local-execution-evidence.app" GOCACHE=/tmp/umcode-go-cache npm_config_cache=/tmp/umcode-npm-cache app/build/macos/bundle.sh`
  `codesign --verify --deep --strict --verbose=2 bin/UMCode-local-execution-evidence.app`
  Expected: build and strict signature verification succeed. Report ad-hoc signing and any optional runtime omissions. Reveal the app in Finder; do not launch, stop the user's app, or publish it.

## Plan self-review and handoff

- [x] Spec coverage: intent/provenance/persistence (1–2), history (3), evidence and compatibility (4), bounded recovery and approvals (5), truthful display (6), restart/providers/public acceptance/local delivery (7).
- [x] Interfaces: DTOs originate in Task 1; resolution, matching and recovery signatures consumed consistently.
- [x] Review Focus: each of the five conditions is assigned to a named task test.
- [x] No provider execution is inferred from prose; no requirement or permission downgrade.
- [x] Private interpretation usage and budget are included; natural-language semantics remain explicitly fallible.
- [x] Native preference preserved; no implementation begins before plan review.
- [x] User review of this plan, then native implementation.
