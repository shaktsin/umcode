# Model-driven execution with capacity-aware context

Date: 2026-10-20

Status: Written specification approved by the user on 2026-10-20, including automatic-only compaction with visible progress. Implementation plan pending user review and execution-method selection.

Source baseline: UMCode `88ce931`; local Codex reference checkout `c7234c1174`.

## 1. Intent and success criteria

UMCode must let the selected model understand the user's request, organize the work, choose tools and checks, and judge whether the requested result is complete. The model may delegate review to a subagent. The engine supplies reliable execution, persistence, context management, permissions, and resource accounting.

The user explicitly rejected mandatory model-generated intent contracts, internal task-category gates, engine judgments about semantic completion, and compulsory workflow stages. A clear file-edit request must reach the coding model without a preliminary classification call. This applies to local and cloud models and is independent of the Token optimization setting.

The engine must accommodate the local server's actual context allocation. Users should not need to choose Quick, repeat an unchanged request, start a fresh chat, or split ordinary edits merely to avoid avoidable engine overhead. No design can make an arbitrarily small model or window complete every task; remaining limitations must be identified accurately.

Success means more correctly completed tasks with fewer retries and lower total token use and elapsed time. Token savings alone do not establish success.

## 2. Scope and architectural choice

Adapt the existing execution loop rather than build a separate local-model engine or patch each classifier error. Reuse the compiler, retrieval, reducers, tool selection, transcript store, diagnostics, and delegation infrastructure where they support the boundaries below.

Included:

- Remove mandatory intent extraction and semantic completion enforcement from interactive parent and child execution.
- Retain factual operation records and recoverable conversation state.
- Resolve endpoint-specific model capacity and budget every outbound model request.
- Bound working history and tool output; compact within a running turn.
- Make context compaction automatic and remove manual compaction controls from every UI surface.
- Show automatic compaction in the affected chat with a read-only progress bar while it is running.
- Keep tool discovery and original-source retrieval available to the model.
- Restrict fallback to the selected pool; a direct model selection has no alternate model.
- Keep review optional and model/user-directed.
- Migrate legacy blocked tasks without restoring removed gates.

Excluded: changing provider permissions, replacing the scheduler, redesigning memory promotion, adding mandatory reviewers, introducing a separate judge service, or guaranteeing semantic correctness through engine checks. Existing explicit workflow APIs may remain available, but their graph and evidence state cannot become prerequisites for ordinary chat execution.

This specification supersedes earlier token-optimization designs where they require engine-owned semantic task contracts, workflow completion gates, or an unsafe unbudgeted fallback. Other unrelated behavior remains outside this change.

## 3. Ownership boundaries

| Concern | Model | Engine |
| --- | --- | --- |
| Meaning of the request | Interpret original instructions and seek clarification when needed | Preserve and deliver instructions with their provenance |
| Work organization | Choose plans, steps, tools, and reviewers | Provide those capabilities without compulsory stages |
| Execution | Propose concrete tool calls | Validate arguments, enforce real permissions, run tools, record results |
| Verification | Choose checks and interpret their significance | Return observed output, exit status, diffs, and freshness metadata |
| Completion | Explain whether the requested work is complete | Distinguish response/turn lifecycle from model-reported task status |
| Capacity | Request further sources as needed | Manage the actual context window, output allowance, and execution budgets |

A successful write establishes that bytes were written; a successful build establishes that the recorded command exited successfully in its recorded environment. Neither establishes that every natural-language requirement was fulfilled. Model and reviewer conclusions must be attributed accordingly.

The model cannot grant itself tool permissions or widen workspace access. Removing inferred semantic gates does not remove sandboxing, credential scopes, cancellation, child workspace isolation, or shared execution budgets. User instructions remain instructions to the model; engine safety policy must come from actual configured permissions and approval decisions, not a classifier's interpretation of prose.

## 4. Model-driven turn lifecycle

1. Persist incoming user input unchanged, resolve the explicit model or pool, and restore factual execution state.
2. Assemble model instructions, the current request, a bounded working history, and available tool definitions.
3. Apply capacity checks to the complete request and compact/reduce context when needed.
4. Call the selected model. Dispatch complete, valid tool calls through existing permission and execution controls.
5. Persist the full available tool results and append bounded representations to working history. Repeat request preparation before the next model call.
6. End the turn when the model returns without further work, subject to cancellation, pending user input, and explicitly configured runtime hooks. Record that the response ended; do not certify semantic task completion.

Remove ordinary-turn dependence on `prepareExecutionExpectations`, `resolveTaskExpectations`, `nextExecutionCorrection`, and `finalizeExecutionExpectation`. In particular, do not replace classifier failures with classifier retries. Remove semantic correction prompts, provisional rewriting of assistant text, and mandatory evidence loops based on inferred expectations.

Failed tools return factual errors to the model, which may recover or explain the failure. A model can end a response explaining a tool failure without the engine inventing another semantic failure. Protocol errors, exhausted resource budgets, provider failures, and execution interruptions remain engine-observed events.

Model-written plans and checkpoints are advisory state. They cannot create approvals or determine admission to the next tool call. Any retained work-graph or verification tools are available capabilities, not required ceremony.

## 5. Capacity resolution

Introduce a capability record keyed by provider, normalized endpoint identity, and model ID. Provider/model alone is insufficient when two servers allocate different windows to the same model. Never put credentials or endpoint query secrets into diagnostic identity strings.

The record contains total context tokens, optional maximum response tokens, the source and time of observation, supported discovery/token-counting capabilities, and an optional persisted capacity override.

Resolve total capacity as follows:

- Prefer trustworthy runtime metadata about the loaded model from a supported server adapter. Discovery is read-only, bounded, and uses the configured endpoint and its existing authorization.
- A configured per-endpoint/model capacity can supply missing runtime capacity or lower it. It cannot exceed a known runtime allocation; when both exist, use the smaller value.
- Catalog metadata can supply capacity where appropriate, but an advertised architectural maximum is only an upper bound for a local server, not evidence of its loaded allocation.
- When no usable capacity is known for a local endpoint, report that capacity is unknown and provide a persistent model-settings field. Do not assign a large fictional window or repeatedly ask for a value in chat. Preserve the user's request so the turn can be retried after configuration.

Refresh on endpoint/model changes, model reload evidence, and explicit settings refresh. Invalidate capacity and request-estimation state on relevant changes. Metadata lookup failure must not discard a previously valid override. A lower newly observed runtime limit wins on the next request.

No provider-specific discovery endpoint is assumed universal. The implementation plan must select supported adapters against their actual response contracts, with fixtures. Unknown OpenAI-compatible servers use the override path when reliable discovery is unavailable.

## 6. Request budgeting

One request-preparation boundary applies before every outbound call: main execution, compaction, optional reviewer/child calls, and any retained background model calls. A route change requires re-preparation against the new model's capacity. Shared task cost/time/token budgets remain separate from a single request's physical context capacity.

Define:

`estimatedInput + reservedOutput + estimationMargin <= effectiveContextWindow`

Input accounting includes system/developer layers, user messages and attachments, tool schemas, conversation, tool results, summaries, retrieved material, and protocol serialization overhead. The current user request is included before the first compaction decision. The loop cannot rely only on history length or fixed trim fractions.

Prefer a supported provider tokenizer when available. Otherwise use a conservative estimator, an explicit margin, and calibration from actual reported input usage. Diagnostics must distinguish measured values from estimates; this is not an exact-token guarantee. Unknown image/audio costs must not be treated as zero.

Default output reservation is the selected complexity preset's response allowance capped at one quarter of the effective window and at a known provider response maximum. The default estimation margin is the larger of 256 tokens and 5% of the effective window, rounded up. Persisted per-model response settings may override the reservation target while remaining subject to the physical budget. These are resource defaults, not workflow requirements.

Prepare context to leave the reservation available before dispatch; do not send an almost-full prompt and treat a tiny generation allowance as normal success. If required instructions, the current user message, and the minimum executable tool surface cannot fit with the reservation, stop with a specific capacity explanation and preserve all input. Do not silently remove requirements or route to a different provider.

The provider's actual response cap must not exceed the prepared reserved output. Quick/Standard/Deep affect resource preferences but never enlarge the physical window.

## 7. Working history, retrieval, and compaction

Maintain separate representations: immutable persisted originals and the bounded context submitted to the model. Tool output reductions and history compaction affect the latter; they must not replace original user messages or available tool payloads in storage.

Under pressure, omit redundant retrieved material and superseded observations, bound older tool output, then compact older conversation into a model-generated checkpoint. Retain complete call/result relationships, relevant recent activity, current tool results, the current request, and references to original sources. Recent large tool results also have a budget; the existing fixed exemption for the newest six results cannot override capacity.

Checkpoint content includes the model's working objective and plan, user decisions and constraints with source references, changed files, performed checks and their results, unresolved failures, uncertain operations, and active child references. Label model conclusions as summaries rather than authoritative engine contracts. Preserve instruction provenance; tool and document text does not become user authority through summarization.

Provide a small core retrieval surface, reusing `task.context` and source retrieval where possible, that can page original requests and full stored results. Every omission marker supplies a source reference and explains that more material exists. A truncated summary must never imply that the original user request ended there.

Compaction occurs automatically when capacity management requires it, at turn admission or between tool rounds. It must not depend on a UI button, user command, confirmation, or approval. The running turn must retain its new tool results, active child state, cancellation, and shared budget. Summarization itself uses a budgeted request with output headroom. If it needs chunking, each chunk obeys the same rule; it cannot call the same overfull request to summarize itself. Use bounded attempts and detect lack of reduction.

When compaction fails, use a simpler budgeted history representation only if it preserves required input and fits. Otherwise return the actual capacity/compaction limitation. Never fall back to an unbounded request.

## 8. Tools, optimization setting, and review

The existing Token optimization master switch remains the single activation control for optional efficiency features. When enabled, progressive tools, retrieval selection, reducers, and context compilation run automatically without component opt-ins or chat approvals. Adapt those components so their packets are useful information rather than semantic admission rules. Do not introduce compulsory workflows through the optimization policy.

Baseline context accounting, physical capacity enforcement, persisted originals, and bounded working output apply with either switch value. Disabling optional optimization does not allow an over-capacity request or restore the removed classifier/completion gates. Basic history compaction remains a capacity mechanism; optional graph-guided context compilation is distinct. Existing explicit permissions still apply to tools selected through discovery.

Expose a compact core containing execution/file capabilities appropriate to the model, source retrieval, and tool discovery. With progressive loading enabled, keep optional integration schemas out until discovered. Discovery must work without native provider-hosted tool search; reuse UMCode's ordinary tool-call mechanism for compatible local models. If optional discovery is unavailable, a bounded direct-tool fallback must still pass the request budget. Never expose a tool that is unavailable or unauthorized.

The coding model or user may request a reviewer. The reviewer receives a focused assignment, relevant original requirements, diff/file references, and execution results, and can retrieve more. Fresh context must be available; copying the entire parent transcript is not mandatory. Reviewer findings are returned to the coding model, which decides how to address them. There is no mandatory judge and no engine approval of semantic completeness.

Use existing agent admission, workspace isolation, shared budgets, cancellation, and ownership. Bound concurrency by configured resource capacity, especially when children share one local inference server. A reviewer does not automatically change providers; its explicit model/pool selection follows the same routing rules.

## 9. Routing and recovery

A direct model selection yields exactly that endpoint/model as its candidate set. An explicitly selected pool, including a persisted/default pool applied to a chat, yields only its enabled members. Do not append globally configured models or retain a prior fallback as the next turn's direct selection. Persist selection separately from the route actually used and show accurate route changes.

A provider `length` stop is not automatically proof of context exhaustion. Use available reason metadata and observed input/output counts to distinguish a configured output cap, confirmed context exhaustion, and an unknown output interruption. Avoid advising users to raise output limits when the context is full.

Discard incomplete tool calls. Preserve partial assistant text as interrupted output, not a completion claim. On a confirmed context overflow before execution, allow at most one context-reduction retry per model step. A second overflow stops with an accurate error and retained state. Streaming tool dispatch must track which complete calls already executed so a retry never automatically replays mutations.

An output-cap interruption that is not confirmed context overflow is reported as incomplete. Subsequent continuation is model-directed and starts from recorded effects. The engine never automatically retries uncertain mutations or treats a repeated command as harmless based on a guessed intent.

Natural continuations such as `resume now` reach the coding model with recent factual state; exact-string recognition cannot gate execution. Preserve model checkpoints and original requests without extracting new expectations. Uncertain operations are shown as uncertain. Permission denials remain denials under their actual scope and lifetime; a new model plan cannot clear them.

## 10. Legacy state and compatibility

Retain old task, expectation, correction, operation, and evidence rows for audit and historical display. Version the ordinary-chat execution policy so old expectation versions, inferred requirements, graph statuses, and semantic failures no longer control admission, retries, or response completion under the new policy.

Keep factual operation statuses and original input bindings. An interrupted operation without a reliable result stays unknown; migration must not relabel it succeeded, verified, or permission-approved. Retain actual configured permissions and genuine approval decisions with their existing validity.

Retire pending approvals created solely for task interpretation, inferred requirements, or automatic plan/design acceptance as obsolete, without granting execution permission. Reconstruct any real tool permission need through the existing permission system rather than an inferred task rule. Do not copy model-inferred requirements into new security controls.

If an old assistant message was rewritten provisionally, retain both versions and identify historical engine intervention; do not edit history to claim that actions ran. On the next turn expose the original user request, recent actual actions, and an attributed model summary. No legacy semantic blocker may force the user to abandon the chat.

Keep serialized legacy fields readable during transition. A legacy `TaskIntent` field may be accepted for compatibility as advisory input, but it must not create authority or a required contract. Emit deprecation diagnostics rather than rejecting an otherwise valid chat request. Do not expose obsolete tools as required recovery steps. Scheduled-task configuration remains separate from interactive intent classification.

Update frontend wording so turn ended, command succeeded, checks passed, and model-reported task completion are distinguishable. Remove any current badge or text that implies engine-certified semantic completion. Do not remove command errors or overwrite the model's answer with an inferred semantic failure.

## 11. Components and interfaces

| Boundary | Responsibility | Existing integration points |
| --- | --- | --- |
| Capability resolver | Endpoint/model capacity, provenance, overrides, invalidation | `internal/models`, provider adapters, routing/settings protocol |
| Request preparer | Full input accounting, output reservation, fitting, diagnostics | `internal/engine/turn.go`, `context.go`, `compile.go`, `compaction.go` |
| Context store and assembler | Originals, bounded history, checkpoints, source paging | `task_context*`, retrieval, compiler, reducers, store |
| Model execution loop | Model/tool continuation and factual lifecycle | `turn.go`, task runtime/recovery/outcomes |
| Tool surface | Core tools, progressive discovery, bounded direct fallback | `tool_selection.go`, `tool_selection_runtime.go` |
| Review/delegation | Optional focused child requests and results | Existing delegation and agent tool infrastructure |

The capability resolver returns structured metadata without assembling prompts. The request preparer takes a selected capability record, a complete candidate request, and an execution budget; it returns a budgeted request with an inclusion report or a typed capacity error. Only the context assembler selects/reduces history. Request fitting must not be spread across provider adapters with inconsistent accounting. No component may infer semantic completion as an execution prerequisite.

## 12. Diagnostics and product behavior

Extend turn diagnostics to record effective capacity and its source, selected versus actual model, reserved and actual output, input estimates versus measured usage, tool schema size, included/omitted source counts, compaction attempts and reductions, retries, and why a turn ended. Unknown measurements remain unavailable rather than zero.

Normal optimization runs automatically without prompts. Automatic compaction must visibly report its activity as described below; other routine reductions remain quiet. Existing on-demand turn details explain reductions and routing. Surface a limitation when the request cannot proceed, an output was interrupted, or settings lack usable capacity. Do not add per-task activation questions or optimization approval dialogs.

There must be no manual context-compaction control anywhere in the product UI: no button, menu action, command-palette action, slash command, or keyboard shortcut that triggers compaction. Remove the existing `Compact chat` button and its frontend handler/state in `app/frontend/src/lib/components/ChatColumn.svelte`, and audit the remaining UI surfaces for equivalent actions. The automatic progress display is required; transcript markers and turn diagnostics may also remain. None may trigger compaction or require acknowledgment. If compaction fails, explain the actual failure rather than offering a manual compaction action. Existing backend manual-compaction entry points may remain for compatibility or internal tests, but must have no product UI trigger; deleting those APIs is not required by this design.

Show a compact status row immediately above the composer in each visible affected main-chat or side-chat view. It contains a progress bar and the label `Automatically compacting context…`. When supplied by actual engine lifecycle events, phase text may change to `Preparing context…`, `Summarizing context…`, and `Restoring context…`. Show the indicator for real compaction, including admission-time and mid-turn work, rather than for every inexpensive tool-output reduction. It is read-only, non-modal, and needs no user action. Use the existing turn cancellation control; do not add a dedicated compaction button.

The default bar is indeterminate: animate it to communicate ongoing work without a numerical percentage or estimated remaining time. Streamed summary tokens and elapsed time do not establish a completion percentage. A determinate bar is allowed only when the engine has a measurable total and completed amount for the work represented by the bar, such as a labeled set of known chunks; a chunk-local measure must not imply overall completion. Respect reduced-motion settings, provide an accessible progressbar label, omit `aria-valuenow` for indeterminate progress, and announce meaningful phase changes through a polite live region without announcing every animation frame.

Publish factual compaction start, phase, and terminal events independently of model-generated text. Bind updates to the thread, active turn, and compaction attempt with ordering information. Persist or expose active state through the normal chat snapshot so reconnecting/reopening a chat restores the progress display; ignore stale updates from earlier attempts. On success, clear the active bar only after the checkpoint is installed and return to the turn's normal running state. Failure, cancellation, timeout, and interrupted-session recovery must settle the state and clear the active bar rather than leave it spinning or show false success. If a budgeted fallback permits continuation, report that actual outcome. Keep a completed/failed diagnostic record as appropriate. These status updates must not require additional model calls.

Metrics include total calls/tokens/cost and elapsed time per completed scenario, unnecessary retries, context failures, and scenario correctness. Review calls count toward total cost. Do not invent savings percentages or treat fewer tokens as proof of better results.

## 13. Acceptance and validation

Use deterministic fake providers and server fixtures for automated tests; no network calls in those tests. A manual local-server smoke test is supplemental and must disclose the actual configured window. Update old semantic-gate tests to assert the new boundary rather than deleting coverage for actual execution and permission behavior.

Required scenarios:

1. A normal file-edit-and-command request reaches the coding model without a `task_intent` request and returns actual tool output. An unrelated engine category cannot reject it.
2. Discussion and clarification responses end normally without inferred mutation expectations or mandatory checks.
3. Failed tools remain visible; a response explaining failure is not rewritten into a different semantic error.
4. Legacy chats blocked on `explanation cannot claim execution`, missing expectations, or evidence contracts can continue. Historical operations, real denials, and uncertain effects remain correctly represented.
5. Long original requests remain retrievable unchanged. Summary boundaries never masquerade as the end of the request.
6. Simulated 8k, 16k, and 32k windows budget complete requests, including the current user message, schema overhead, and newly returned tool output. No submitted request exceeds the fixture's known capacity. Scenarios too large for a window return a specific capacity limitation without losing input.
7. A large recent file read cannot bypass fitting because it is among the newest six results. Mid-turn compaction preserves tool call/result pairs, current actions, and child references.
8. Compaction requests themselves fit. Ineffective compaction terminates within the documented bounds without an unbounded fallback.
9. Two endpoints serving the same model retain distinct capacities. Overrides, lower runtime limits, endpoint changes, unknown metadata, and provider token-estimation discrepancies have coverage.
10. Direct selection never falls back to a globally configured model. Pool fallback stays within pool membership and re-prepares against the destination's capacity without changing persisted user selection.
11. Context overflow, output cap, and unknown truncation yield distinct diagnostics. Partial tool calls never execute and already completed effects are not replayed by engine recovery.
12. Optional reviewers can use fresh focused context and return findings without becoming a required completion gate. Existing shared budgets, cancellation, isolation, and permission checks still pass.
13. With the master switch on, optional optimizations activate without extra approvals; with it off, physical context checks remain active and removed semantic gates do not return.
14. Frontend coverage verifies the main chat and side-chat UI expose no manual context-compaction action. Audit menus, command registries, slash commands, and keyboard bindings for equivalent triggers. An engine integration scenario crosses the context threshold after a tool result and compacts/continues without any UI event, confirmation, or approval. Read-only markers and diagnostics remain usable.
15. UI and engine-event coverage verifies that admission-time and mid-turn compaction automatically show the progress bar in the affected chat, real phase events update its label, and successful installation restores normal running status. Verify failure, cancellation, timeout, stale-attempt updates, reconnect/snapshot restoration, and interrupted recovery. Indeterminate progress must have no fabricated percentage or ETA, support reduced motion and accessible announcements, and never require an additional model call.

Compare the same representative tasks before and after the change: the Goodreads scaffold-plan edit, continuation after a failed mutation, a large-file targeted edit, a long requirements request, and an optional focused review. Evaluate resulting files/output against scenario-specific checks and record tokens, retries, and duration. These are development acceptance checks, not generic runtime contracts imposed on user requests.

## 14. Delivery boundaries

One coordinated architectural change is divided into sequential implementation milestones: remove semantic gates with legacy compatibility; add capacity resolution and centralized budgeting; integrate bounded history/compaction and discovery; finish routing, optional review integration, diagnostics, and regression validation. Each milestone must preserve the ownership boundary and actual execution controls. Partial delivery must not claim all small-context failures are solved.

The written implementation plan will choose concrete symbols, migrations, settings/UI fields, test commands, and execution method after this specification is reviewed. Product implementation has not started.

## 15. Codex references and deliberate differences

The inspected local Codex checkout provides reference behavior, not a dependency:

- `codex-rs/core/src/session/turn.rs`: model/tool continuation, turn ending, and mid-turn compaction.
- `codex-rs/core/src/tools/handlers/plan.rs`: model-owned plan updates.
- `codex-rs/core/src/context_manager/history.rs`: bounded working tool output with full persisted payloads.
- `codex-rs/core/src/tools/spec_plan.rs` and `tools/handlers/tool_search.rs`: conditional deferred tool exposure and local search.
- `codex-rs/core/src/session/review.rs` and multi-agent handlers: separate review and configurable child context.
- `codex-rs/protocol/src/openai_models.rs`: model-specific usable windows and compaction thresholds.

Do not copy its 272k fallback window for unknown model IDs. Account for incoming user input and all request components before dispatch, including the pre-compaction accounting gap noted in the inspected source. UMCode's local-model support requires its own validated endpoint capacity and conservative estimation behavior. Codex's configurable stop/approval hooks do not justify reinstating a mandatory UMCode semantic gate.
