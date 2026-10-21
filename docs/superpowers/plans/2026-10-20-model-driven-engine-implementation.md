# Model-driven Engine Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let the coding model direct work while UMCode reliably manages execution and small context windows, including visible automatic compaction with no manual UI controls.

**Architecture:** Remove semantic admission/completion gates from the existing loop. Centralize endpoint capacity and complete-request budgeting, then connect bounded context, compaction, discovery, routing, and optional reviewers to that boundary. Persist factual operations and compaction lifecycle state independently of model conclusions.

**Tech Stack:** Existing Go engine, SQLite store, provider-neutral streaming adapters, Svelte 5/TypeScript frontend, Go tests and Vitest. Follow `go.mod` and existing lockfiles; no new product dependencies.

**Spec:** `docs/superpowers/specs/2026-10-20-model-driven-engine-design.md` (including automatic-only compaction and visible progress amendments).

## Global Constraints

- The model owns task interpretation, organization, verification strategy, and semantic completion; it may delegate review.
- No mandatory `task_intent` model call, semantic correction loop, inferred completion contract, or provisional rewriting of assistant responses.
- Actual permissions, workspace isolation, cancellation, credential scopes, and shared execution budgets remain enforced.
- A directly selected model has no alternate model; pool fallback stays inside the selected pool.
- Persist original requests and available tool results unchanged; bound only the submitted working representation.
- `estimatedInput + reservedOutput + estimationMargin <= effectiveContextWindow` before every model call.
- Default output reservation: preset allowance capped at one quarter of the effective window and a known provider output maximum. Margin: larger of 256 tokens and 5% of the window, rounded up.
- Optional efficiency features activate automatically under the single master switch; physical context checks apply with either switch value.
- No manual compaction button, menu action, palette action, slash command, or keyboard shortcut anywhere in the product UI.
- Show a read-only progress bar above the affected main/side-chat composer while automatic compaction runs. Default label: `Automatically compacting context…`; do not fabricate percentages or ETAs.
- Automated tests use fake providers and in-process fixtures; no live provider calls.
- Preserve the pre-existing uncommitted `TASKS.md` changes. Stage only task-owned paths. Do not rewrite the user's local model configuration or production database during tests.

## Review Focus

- Endpoint aliases, secrets embedded in URLs, and two credentials addressing one server: capacity keys must not leak secrets or conflate distinct runtime allocations (Tasks 3–4).
- Mixed Unicode and image input: underestimation must not treat non-text input as free or drop the current request to fit (Tasks 5–7).
- Permission-denied legacy tasks followed by `resume now`: remove semantic blockers without granting denied execution or replaying uncertain effects (Tasks 1–2).
- Delayed terminal notifications after a newer compaction attempt or after reconnect: stale updates cannot hide active work or leave a permanent spinner (Tasks 8–9).
- Pool fallback from a larger to a smaller model after completed mutations: re-fit context without repeating effects or persisting the fallback as the user's choice (Tasks 6 and 10).

## Execution and file boundaries

Use an isolated managed worktree for implementation, per the user's earlier workspace choice. Start from the approved design branch containing this plan; do not copy `TASKS.md` modifications into it. Read the using-git-worktrees skill at execution time. Keep changes to `turn.go` as integration points; put new capacity, budgeting, progress, and migration behavior in focused modules.

Task interfaces below are decisions for new code, not claims that those symbols already exist. Additive protocol fields must remain readable by older clients. New SQL migrations follow the current `0031_task_expectations.sql`; if another approved change occupies a number, renumber the two new migrations in order without changing their contents.

Each task uses its own red/green cycle and commit. The supplied assertions define the meaningful test behavior; use existing fixtures and add test-local helpers where necessary. No runtime test is satisfied merely by searching source text for a function name.

### Task 1: Restore model-driven execution

**Files:** Modify `internal/engine/turn.go`, `task_context.go`, `task_authority.go`, `task_outcomes.go`, `task_execution_recovery.go`, `task_propose.go`; update `task_runtime_test.go`, `task_outcomes_test.go`, `task_execution_recovery_test.go`, `task_intent_test.go`, `intent_fixture_test.go`; create `internal/engine/model_driven_turn_test.go`.

**Interfaces:** Retain `runTurn`, `callModel`, and actual operation recording. Ordinary turns no longer call `prepareExecutionExpectations`, `nextExecutionCorrection`, `markExecutionProvisional`, or `finalizeExecutionExpectation`. Add constant `modelDrivenExecutionPolicy = "model_driven_v1"` in new `internal/engine/execution_policy.go`; Task 2 persists it.

- [ ] **Write failing tests:** `TestModelDrivenTurnSkipsIntentClassifier`, `TestModelDrivenTurnPreservesAssistantResponse`, `TestModelDrivenTurnReturnsToolFailureToModel`, and `TestModelDrivenTurnDoesNotRequireWorkGraphCompletion`. Script providers that record every request and answer or edit directly. Assertions include:

  ```go
  if len(provider.requests) != 1 { t.Fatalf("extra classification/correction call: %d", len(provider.requests)) }
  if message.Text != "I cannot complete this because the command failed." { t.Fatal(message.Text) }
  if finished.Status != protocol.TurnCompleted { t.Fatal(finished.Status) }
  ```

  Also prove a failed/denied tool remains failed/denied in the ledger; turn ending does not relabel it successful.
- [ ] **Run red:** `go test ./internal/engine -run '^TestModelDrivenTurn' -count=1`. Expect current extra calls, semantic error, or overwritten answer.
- [ ] **Implement:** Remove semantic admission and completion branches, including aggregate outcome rewriting elsewhere in `turn.go`. Model-generated requirements, work graphs, and failed checks remain advisory context. Retain actual permission gates and concrete tool errors. Stop injecting a separate classifier into test fixtures that exercise ordinary turns. Update previous tests whose intended contract is superseded; preserve their execution, provenance, and permission assertions.
- [ ] **Run green:** the focused command, then `go test -timeout 20m ./internal/engine`. Expect pass; no replacement classifier retry.
- [ ] **Commit:** `refactor: make chat execution model driven`.

### Task 2: Adopt legacy tasks without semantic blockers

**Files:** Modify `internal/protocol/task.go`, `internal/store/task.go`, `internal/engine/execution_policy.go`, `task_context.go`, `task_recovery.go`, `task_requirements.go`, `task_execution_recovery.go`; create `internal/engine/model_driven_legacy_test.go`, `internal/store/task_policy_test.go`; update `internal/server/task_recovery_e2e_test.go`, `task_execution_evidence_e2e_test.go` and frontend `app/frontend/src/lib/taskOutcome.ts`, `taskOutcome.test.ts`, `taskExecutionOutcome.test.ts`.

**Interfaces:** Add `ExecutionPolicy string` to `protocol.ExecutionTask`'s JSON. Add `Store.AdoptModelDrivenTask(ctx context.Context, taskID string) (protocol.TaskSnapshot, error)`. It transactionally sets the policy and retires obsolete interpretation approvals without deleting historical expectation/operation rows or granting tool permission. Add `Engine.adoptExecutionPolicy(ctx context.Context, threadID string) error` called before ordinary execution state is assembled.

- [ ] **Write failing tests:** `TestAdoptModelDrivenTaskPreservesOperations`, `TestLegacySemanticFailureDoesNotBlockResumeNow`, `TestLegacyPermissionDenialStillApplies`, `TestLegacyUnknownMutationIsNotReplayed`, `TestLegacyIntentInputIsAdvisory`. Seed the old errors and pending expectations; assert a new coding request reaches the provider, archived rows are unchanged, actual approval state is not approved, and mutation call count remains zero unless the model explicitly calls it under valid permissions.
- [ ] **Run red:** `go test ./internal/store ./internal/engine ./internal/server -run 'Test(AdoptModelDriven|LegacySemantic|LegacyPermission|LegacyUnknown|LegacyIntent)' -count=1`.
- [ ] **Implement:** Preserve old rows, original text, provisional text, and factual results. Do not convert arbitrary `blocked` or `unknown` effects into success. Remove exact continuation phrases as an admission requirement. Keep legacy `TaskIntent` fields readable but unused for authority. Attribute model-reported completion separately from turn status; remove frontend engine-certified semantic failure messages for new-policy turns. No database deletion migration is required: execution policy lives in existing task JSON.
- [ ] **Run green:** focused Go tests, then `npm --prefix app/frontend test -- taskExecutionOutcome.test.ts taskOutcome.test.ts`. Expect preserved actual failures and no new semantic gates.
- [ ] **Commit:** `fix: resume legacy chats without semantic contracts`.

### Task 3: Persist endpoint-specific capacity profiles

**Files:** Create `internal/protocol/model_capacity.go`, `internal/models/capacity.go`, `internal/store/model_capacity.go`, `internal/store/model_capacity_test.go`, `internal/store/migrations/0032_model_capacity_profiles.sql`; modify `internal/server/routes.go`, `internal/protocol/methods.go`, `internal/engine/engine.go`; create `internal/engine/model_capacity.go`, `model_capacity_test.go`; modify frontend `types.ts`, `panels/settings/Models.svelte`; create `modelCapacity.test.ts`.

**Interfaces:** `models.Capacity` has `Window int`, `MaxOutput int`, `Source string`, `ObservedAt time.Time`, and `EndpointID string`. `protocol.ModelCapacityProfile` fields are `Provider`, `EndpointID`, `Model`, `Backend` (`string`), and `ContextWindow`, `MaxOutputTokens`, `ResponseReserveTokens` (`*int`, omitted when unset); JSON uses lower camel case. Backend values are `generic` or `ollama`. Add RPCs `model/capacity/get` and `model/capacity/set`; requests carry `CredentialID`, `Model`, and an optional `Profile` for set. Resolve the endpoint from the configured credential, never return secret URL parts, and reject a supplied profile bound to another endpoint. Add `models.EndpointIdentity(provider, baseURL string) (string, error)`, `Store.GetModelCapacityProfile(ctx context.Context, provider, endpointID, model string) (protocol.ModelCapacityProfile, error)`, and `Store.SetModelCapacityProfile(ctx context.Context, profile protocol.ModelCapacityProfile) error`.

- [ ] **Write failing tests:** `TestEndpointIdentityRedactsSecrets`, `TestEndpointCapacityProfilesAreIsolated`, `TestCapacityProfileRoundTrip`, `TestCapacitySettingsRejectInvalidWindow`. Assert distinct endpoints preserve 8192 and 32768 independently; scheme/host normalization is stable; URL credentials/query secrets never appear in IDs or RPC results; capacity/output fields are positive when supplied. UI serialization omits an unset override instead of submitting zero.
- [ ] **Run red:** `go test ./internal/models ./internal/store ./internal/engine -run 'Test(Endpoint|CapacityProfile|CapacitySettings)' -count=1`; `npm --prefix app/frontend test -- modelCapacity.test.ts`.
- [ ] **Implement:** Use a SHA-256 identity over normalized endpoint information; strip userinfo/query/fragment from anything displayed or logged. Keep endpoint paths meaningful. Store profiles under `(provider, endpoint_id, model)`; resolve configured credentials server-side so callers cannot probe arbitrary URLs. Provide persistent context/output fields in existing Model settings; no chat activation prompts. Reuse existing settings authorization and validation.
- [ ] **Run green:** focused commands, `go test ./internal/server -run 'Capacity' -count=1`, and `npm --prefix app/frontend run check`.
- [ ] **Commit:** `feat: configure endpoint specific model capacity`.

### Task 4: Resolve actual runtime capacity

**Files:** Create `internal/llm/local_capacity.go`, `local_capacity_test.go`; modify `internal/engine/model_capacity.go`, `model_capacity_test.go`, `internal/models/models.go`; update `internal/server/routes.go` and capacity settings UI for refresh/source display.

**Interfaces:** `llm.RuntimeCapacity` has `ContextWindow int` and `MaxOutputTokens int`. Add `llm.DiscoverRuntimeCapacity(ctx context.Context, client *http.Client, cred llm.Credential, backend, model string) (RuntimeCapacity, error)`. Add `Engine.resolveCapacity(ctx context.Context, res resolved) (models.Capacity, error)` and typed `ErrCapacityUnknown`. Backend `generic` does not probe vendor endpoints.

- [ ] **Write failing tests:** `TestOllamaLoadedCapacity`, `TestCapacityUsesLowerRuntimeAndOverride`, `TestCapacityDiscoveryFailurePreservesOverride`, `TestUnknownLocalCapacityIsActionable`, `TestCapacityRefreshInvalidatesPriorAllocation`. An `httptest` fixture serves `/api/ps` with a matching model and `context_length:32768`; a configured override of 16384 resolves to 16384. A missing/unloaded model must not borrow another model's allocation.
- [ ] **Run red:** `go test ./internal/llm ./internal/engine -run 'Test(OllamaLoaded|CapacityUses|CapacityDiscovery|UnknownLocal|CapacityRefresh)' -count=1`.
- [ ] **Implement:** Support the explicit `ollama` backend with the read-only runtime response described by [Ollama's API](https://docs.ollama.com/api/ps). Derive the native root only from an explicit compatible `/v1` endpoint configuration; unsupported proxy paths require an override. Preserve the configured authorization, bound response size, use a 2-second request timeout, and disable cross-origin redirects. Refresh on explicit refresh and at turn admission; bound repeated lookups within a turn with a 30-second cache invalidated on a context-limit error. Use no advertised local maximum as a runtime allocation. Generic local backends use overrides until a validated adapter exists; cloud catalog metadata may supply cloud capacity.
- [ ] **Run green:** focused commands, `go test ./internal/llm ./internal/models -count=1`. All tests remain local fixtures.
- [ ] **Commit:** `feat: resolve loaded local model context capacity`.

### Task 5: Calculate complete-request budgets

**Files:** Create `internal/engine/request_budget.go`, `request_budget_test.go`; modify `internal/llm/llm.go`, `internal/protocol/turn_details.go`, `internal/engine/context.go`.

**Interfaces:** Add `requestBudgetReport` with `Window`, `EstimatedInput`, `ReservedOutput`, `Margin`, and per-component counts (all `int`), `EstimateOrigin string`. Add `budgetRequest(req llm.Request, capacity models.Capacity, configuredReserve int) (llm.Request, requestBudgetReport, error)`; it returns a cloned request and typed `requestCapacityError`, never edits history in place. Add optional `llm.RequestTokenCounter` interface: `CountRequestTokens(context.Context, llm.Credential, llm.Request) (int, error)`; lack of support uses conservative estimates and explicit margins.

- [ ] **Write failing tests:** `TestRequestBudgetIncludesAllComponents`, `TestRequestBudgetReserveDefaults`, `TestRequestBudgetPreservesUnicodeAndImages`, `TestRequestBudgetDoesNotMutateOriginal`, `TestRequestBudgetUnknownCapacity`. Use real JSON tool schemas and current-user input. Required assertions:

  ```go
  if report.ReservedOutput != 8192 || report.Margin != 1639 { t.Fatal(report) } // 32768-window, 16384 preset
  if report.EstimatedInput+report.ReservedOutput+report.Margin > report.Window { t.Fatal(report) }
  ```

  Assert the known-budget fixture rejects an oversized incoming message before dispatch and image inputs receive nonzero estimates.
- [ ] **Run red:** `go test ./internal/engine -run '^TestRequestBudget' -count=1`.
- [ ] **Implement:** Include system layers, all messages/parts/thinking, tool schemas/results, and serialization overhead. Apply spec reservation/margin math; cap output by known provider maximum. Treat configured reserve as a target subject to physical capacity, not permission to overrun. Add conservative provider-usage calibration keyed by endpoint/model; do not replace unknown measurements with zero or promise exact counts when estimated.
- [ ] **Run green:** focused tests plus existing `Test.*Context` tests. Verify a plain-text estimator cannot erase image/audio costs and provider token discrepancies increase required headroom.
- [ ] **Commit:** `feat: budget complete model requests with response headroom`.

### Task 6: Enforce budgeting at provider dispatch and fallback

**Files:** Create `internal/engine/request_prepare.go`, `request_prepare_test.go`; modify `turn.go`, `engine.go`, `compaction.go`, and all direct provider stream callers discovered with `rg -n '\.Stream\(' internal`; update relevant memory/delegation adapters without adding semantic gates.

**Interfaces:** `Engine.prepareModelRequest(ctx, sctx context.Context, turn protocol.Turn, res *resolved, req llm.Request, phase string, budget *turnBudget) (llm.Request, requestBudgetReport, error)`. Phases distinguish ordinary execution, compaction, review, and retained background work. Preserve existing public `callModel` signature; prepare after selecting the actual route and immediately before each provider dispatch. Compaction phase performs fitting without recursively invoking compaction.

- [ ] **Write failing tests:** `TestEveryProviderDispatchIsBudgeted`, `TestFallbackRepreparesForSmallerWindow`, `TestCapacityFailureRetainsCurrentRequest`, `TestBudgetingRetainsSharedLimits`. Fake providers reject any request whose fixture-counted input plus output exceeds their capacity. Cover parent, child, compaction, and a background path; assert zero dispatches when input cannot fit.
- [ ] **Run red:** `go test ./internal/engine -run 'Test(EveryProvider|FallbackReprepares|CapacityFailure|BudgetingRetains)' -count=1`.
- [ ] **Implement:** Resolve capacity per actual endpoint/credential, prepare once per attempt, debit all helper model calls to the proper existing budgets, and record preparation diagnostics. Unknown local capacity returns a persistent-settings error while preserving user input. Until Task 7 supplies fitting, fail oversized calls accurately rather than sending them. Update deterministic test harnesses to supply explicit fake-model capacities rather than adding a production bypass for test providers. Do not remove provider-specific valid reasoning/output constraints.
- [ ] **Run green:** focused tests; `go test -timeout 20m ./internal/engine ./internal/memory ./internal/delegation`.
- [ ] **Commit:** `feat: enforce capacity checks on every model dispatch`.

### Task 7: Bound live context and compact within turns

**Files:** Modify `internal/engine/context.go`, `compile.go`, `reducer.go`, `compaction.go`, `task_context.go`, `task_context_tool.go`, `request_prepare.go`; create `internal/engine/live_compaction.go`, `live_compaction_test.go`; update context/compiler/reducer/compaction tests and source retrieval tests.

**Interfaces:** `Engine.compactLiveMessages(ctx, sctx context.Context, turn protocol.Turn, res *resolved, msgs []llm.Message, budget *turnBudget) ([]llm.Message, error)`. The caller passes the actual current-turn messages; the function cannot rebuild only older persisted turns. Add source reference metadata for omitted output using the existing retrieval/context tool. Keep `CompactThread` as a backend compatibility entry point without a product UI caller.

- [ ] **Write failing tests:** `TestLiveCompactionRetainsCurrentToolRound`, `TestRecentLargeResultIsBounded`, `TestOriginalRequestRetrievalIsUnchanged`, `TestCompactionRequestFitsSmallWindow`, `TestCompactionFailureHasNoUnboundedFallback`, `TestCompactionPreservesInstructionProvenance`. Cover 8192/16384/32768 windows, Unicode, and a user message larger than remaining space. Assert original source bytes are unchanged and every call ID has its matching result.
- [ ] **Run red:** `go test ./internal/engine -run 'Test(LiveCompaction|RecentLarge|OriginalRequest|CompactionRequest|CompactionFailure|CompactionPreserves)' -count=1`.
- [ ] **Implement:** Fit optional retrieved packets first, then bound tool output with retrievable references, then summarize older context while retaining the live interaction. Remove the absolute newest-six-results exemption. Budget summaries and any required chunks; at most two compaction passes per model step and eight summary calls per pass, bounded further by existing execution budgets. Detect no reduction and fail specifically. Preserve user decisions, source provenance, child references, uncertainty, and current output; compiler fallback must pass the same budget. Do not summarize untrusted source text into higher-priority instructions.
- [ ] **Run green:** focused tests, existing compiler/retrieval/reducer tests, and `go test ./internal/ctxcompiler ./internal/retrieval`. Verify compaction can happen between tool rounds with the master switch off while optional compiler behavior remains off.
- [ ] **Commit:** `feat: compact live tool conversations within model capacity`.

### Task 8: Publish durable automatic-compaction lifecycle

**Files:** Create `internal/protocol/compaction.go`, `internal/store/compaction.go`, `compaction_test.go`, `internal/store/migrations/0033_compaction_runs.sql`, `internal/engine/compaction_progress.go`, `compaction_progress_test.go`; modify engine `compaction.go`, `live_compaction.go`, `engine.go` (including `ReadThread`), `internal/protocol/methods.go` (`ThreadReadResult`), and `internal/server/routes.go`.

**Interfaces:** `protocol.CompactionState` fields: `ID`, `ThreadID`, `TurnID` (`string`), `Sequence` (`int64`), `Status` (`preparing|summarizing|restoring|completed|failed|cancelled|interrupted`), `StartedAt`, `UpdatedAt` (`time.Time`), optional `Error` (`string`). Add `thread/compactionUpdated` event and optional `compaction` field to the `thread/read` result. Store `SaveCompactionState(ctx context.Context, state protocol.CompactionState) error` rejects stale sequence updates; `LatestCompactionState(ctx context.Context, threadID string) (*protocol.CompactionState, error)` supplies snapshot state.

- [ ] **Write failing tests:** `TestCompactionLifecycleOrdersUpdates`, `TestCompactionCompletesAfterCheckpointInstallation`, `TestCompactionTerminatesOnFailureAndCancel`, `TestCompactionSnapshotAfterReconnect`, `TestCompactionStartupSettlesInterruptedWork`. Assert terminal state is not emitted before checkpoint installation and a previous attempt cannot update the current one. Simulate failure before provider streaming and during checkpoint persistence.
- [ ] **Run red:** `go test ./internal/store ./internal/engine ./internal/server -run '^TestCompaction(Lifecycle|Completes|Terminates|Snapshot|Startup)' -count=1`.
- [ ] **Implement:** Emit start/phase events around actual work, persist ordered states, settle every exit including panic recovery/timeout/cancellation, and reconcile orphaned active rows at startup. Keep this state separate from semantic task contracts and legacy historical compaction items. Do not stream generated summary tokens as percentage progress. No extra model calls or dedicated cancellation mechanism.
- [ ] **Run green:** focused tests plus store migration tests. Ensure failed durable writes cannot yield a misleading success notification; a reconnect receives a truthful snapshot or actual storage error.
- [ ] **Commit:** `feat: publish durable automatic compaction progress`.

### Task 9: Show progress and remove manual UI controls

**Files:** Create `app/frontend/src/lib/compaction.ts`, `compaction.test.ts`, `components/CompactionProgress.svelte`, `components/CompactionProgress.test.ts`; modify `types.ts`, `stores/chat.svelte.ts`, `stores/chat.test.ts`, `components/ChatColumn.svelte`, `components/ItemView.svelte`; audit frontend menus/keybindings and command registries.

**Interfaces:** TypeScript `CompactionState` mirrors Task 8. Export pure `applyCompactionUpdate(current: CompactionState | null, incoming: CompactionState): CompactionState` and `compactionLabel(state: CompactionState): string`. Add `ThreadView.compaction` reactive state; hydrate from `thread/read` and apply `thread/compactionUpdated` by matching thread/turn and sequence. `CompactionProgress` consumes `state: CompactionState | null`.

- [ ] **Write failing tests:** `ignores stale attempt updates`, `hydrates active compaction after reconnect`, `renders automatic progress in main and side chat`, `clears bar on every terminal state`, and `exposes no manual compaction action`. Assert `role="progressbar"`, accessible label, no `aria-valuenow` for unknown progress, exact label `Automatically compacting context…`, and no invocation of `thread/compact` from product UI. Use runtime component/store tests; supplement with an audit of command/menu/keyboard registrations.
- [ ] **Run red:** `npm --prefix app/frontend test -- compaction.test.ts CompactionProgress.test.ts chat.test.ts`.
- [ ] **Implement:** Place the status row immediately above each affected composer; use an indeterminate bar and actual preparing/summarizing/restoring labels. Respect reduced motion and polite phase announcements. Terminal events clear active animation and preserve diagnostic records. Remove the old `compactChat` handler, its `compacting` state, and the `Compact chat` button. Do not remove unrelated component `compact` layout props. Existing stop-turn behavior remains available. Use the existing Vitest setup and Svelte server rendering for markup assertions, with pure state/reducer tests for events; do not add a DOM-testing dependency merely for this component.
- [ ] **Run green:** focused tests, `npm --prefix app/frontend run check`, `npm --prefix app/frontend run build`. Visually inspect main and side chat at narrow and normal widths with fake lifecycle events; verify reconnect and failure states without real provider calls.
- [ ] **Commit:** `feat: show automatic compaction progress without manual controls`.

### Task 10: Bound tool exposure and restrict fallback

**Files:** Modify `internal/engine/tool_selection.go`, `tool_selection_runtime.go`, `routing.go`, `turn.go`, `request_prepare.go`; update `progressive_tools_test.go`, `progressive_policy_test.go`, `routing_test.go`, `internal/server/e2e_test.go`; create `internal/engine/capacity_routing_test.go`.

**Interfaces:** `configuredFrom(provider, model string)` returns one direct candidate; `poolModels` remains the source of pool membership. Reuse existing discovery tool schema and authorization; include source retrieval in the compact core surface. Selection failures use a budgeted direct-tool fallback, never an unrestricted append-all-tools dispatch.

- [ ] **Write failing tests:** `TestDirectModelHasNoCrossProviderFallback`, `TestPoolFallbackStaysWithinMembership`, `TestFallbackDoesNotChangePersistedSelection`, `TestDiscoveryWorksWithoutNativeSearch`, `TestDiscoveryFallbackStillFits`, `TestFallbackAfterMutationDoesNotReplay`. Assert a Qwen selection cannot call a configured Claude provider, and a pool's smaller model receives a newly fitted request including already executed results.
- [ ] **Run red:** `go test ./internal/engine ./internal/server -run 'Test(DirectModel|PoolFallbackStays|FallbackDoesNot|DiscoveryWorks|DiscoveryFallback|FallbackAfterMutation)' -count=1`.
- [ ] **Implement:** Keep selected versus resolved route fields distinct, rebuild request capacity on route changes, avoid optional integration schema bulk with progressive selection on, and validate discovered tools against current permissions. Permit same-model credential retries only within the same endpoint/model identity and existing authorization; they are not cross-model fallback. Preserve a selected/default pool as a pool on subsequent turns.
- [ ] **Run green:** focused tests plus existing routing, progressive-tool, and policy tests. Test both master-switch states; lack of native search must not require enabling another feature.
- [ ] **Commit:** `fix: scope fallback and tool loading to selected model capacity`.

### Task 11: Distinguish interrupted output and keep review optional

**Files:** Create `internal/engine/output_interruption.go`, `output_interruption_test.go`, `model_driven_review_test.go`; modify engine `turn.go`, `delegation_execution.go`, `delegation.go`, `internal/delegation/context.go`, `internal/protocol/turn_details.go`, and frontend `app/frontend/src/lib/chatError.ts`, `chatError.test.ts` for interruption reasons.

**Interfaces:** Add `outputInterruptionKind` constants `context_exhausted`, `output_cap`, `unknown`; `classifyOutputInterruption(stopReason string, usage llm.Usage, report requestBudgetReport, providerContextError bool) outputInterruptionKind`. Ordinary `length` without corroborating evidence is `unknown`, not confirmed context exhaustion. Retain actual user/provider selection for reviewer requests and use Task 6 preparation for child dispatches.

- [ ] **Write failing tests:** `TestLengthStopDoesNotInventContextCause`, `TestContextOverflowHasOneReductionRetry`, `TestIncompleteCallsNeverExecute`, `TestInterruptedRetryDoesNotReplayCompletedCalls`, `TestFocusedReviewIsOptionalAndBudgeted`, `TestReviewCannotWidenParentPermissions`. Assert retry attempts stop after one confirmed-overflow reduction per model step; an optional reviewer receives focused original requirements and references, not a mandatory full transcript or completion contract.
- [ ] **Run red:** `go test ./internal/engine -run 'Test(LengthStop|ContextOverflow|IncompleteCalls|InterruptedRetry|FocusedReview|ReviewCannot)' -count=1`.
- [ ] **Implement:** Preserve partial text as interrupted, discard incomplete calls, report accurate causes, and let the model decide semantic recovery after factual failures. Do not retry a streaming attempt that already ran a mutation automatically. Reuse existing fresh-child context capability and resource admission; retain configured local concurrency and shared limits rather than spawn reviewers by default.
- [ ] **Run green:** focused tests and existing delegation budget, cancellation, isolation, and tool-argument serialization regressions.
- [ ] **Commit:** `fix: recover interrupted output without semantic review gates`.

### Task 12: Complete diagnostics and scenario validation

**Files:** Modify `internal/protocol/turn_details.go`, `internal/engine/turn_details.go`, `turn_details_test.go`, `internal/store/turn_details.go`, frontend `app/frontend/src/lib/turnDetails.ts`, `turnDetails.test.ts` and `components/TurnDetails.svelte`; create `internal/server/model_driven_context_e2e_test.go` and `docs/model-capacity-and-compaction.md` with scenario results and user-facing settings behavior.

**Interfaces:** Extend `RequestDiagnostics` additively with capacity/source, input estimate origin, output reservation/actual, margin, reduction and retry reason. Reuse `turn/details`; unknown values remain absent/unavailable. Add deterministic scenario tests using the existing `newHarness` and scripted providers; report observed metrics rather than invented savings.

- [ ] **Write failing tests:** `TestModelDrivenScaffoldEditsAndGrep`, `TestSmallContextContinuation`, `TestMasterSwitchDoesNotRestoreSemanticGates`, `TestTurnDetailsSeparatesEstimatedAndMeasuredUsage`. Cover the user's exact scaffold-plan changes and grep output, a large targeted edit, a long original request, permission-denied recovery, and optional review. An empty grep result with exit status 1 must be presented truthfully and may correctly satisfy the user's check; it must not create an engine-semantic failure. Scenario assertions check resulting bytes and real command output rather than generic runtime expectation contracts.
- [ ] **Run red:** `go test ./internal/server -run 'Test(ModelDrivenScaffold|SmallContext|MasterSwitchDoesNot|TurnDetailsSeparates)' -count=1`; `npm --prefix app/frontend test -- turnDetails.test.ts`.
- [ ] **Implement:** Fill diagnostic fields from actual lifecycle/budget information; show selected versus used route and unavailable measurements accurately. Document endpoint overrides and automatic-only compaction. Record pre/post scenario totals for calls, input/output tokens, retries, elapsed time, and correctness using deterministic fixture counts; do not compare fixture cost as actual provider billing.
- [ ] **Run green and final checks:** `go test -timeout 20m ./...`; `go vet ./...`; `npm --prefix app/frontend test`; `npm --prefix app/frontend run check`; `npm --prefix app/frontend run build`; `git diff --check`. Run `gofmt` on changed Go files before these checks. For race-sensitive progress/state paths additionally run `go test -race -timeout 90m ./internal/engine ./internal/store ./internal/delegation`; do not rerun unrelated suites without a new reason. Expect all applicable checks pass; document actual environment blockers explicitly.
- [ ] **Commit:** `test: validate model driven small context execution`.

## Review and handoff gates

Wave 1: Tasks 1–2 restore the execution boundary and legacy continuation. Wave 2: Tasks 3–7 add capacity and fitting. Wave 3: Tasks 8–9 provide automatic progress and remove manual controls. Wave 4: Tasks 10–12 complete routing/review integration and validation. Each wave runs affected package tests before proceeding; whole-repository checks run once after the final change, with failures repaired and relevant checks repeated.

At review, compare every acceptance scenario in the spec to its owning task above. In particular, verify there is no path through an old recovery hook, child admission rule, work graph, or frontend status interpreter that reinstates semantic completion enforcement. Tests for actual security and execution controls must remain.

The written plan requires user review and selection of execution method before product code starts. Recommended method: native execution in this session with one independent final review, because the sequential tasks share request-preparation and lifecycle interfaces and copying a fresh implementation/review context for every task would add overhead. Subagent-driven task-by-task implementation remains available if the user prefers it. Use the user's existing preference for an isolated worktree with either method.
