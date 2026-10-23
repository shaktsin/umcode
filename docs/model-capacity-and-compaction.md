# Model capacity and automatic compaction

UMCode sends the original request to the coding model without an intent-classifier admission call. The model chooses its plan, checks and completion judgment. Tool permissions, sandbox boundaries, cancellation and shared resource budgets still apply. Historical intent/evidence data remains available for compatibility; it does not certify new model-driven turns.

## Configure a local endpoint

Open Settings → Models → Model context capacity. Select the credential and model used by the chat. Profiles are bound to a hashed provider/endpoint/model identity; credentials and endpoint secrets are not copied into diagnostic records.

For Ollama, select the Ollama backend. UMCode reads the loaded model's actual allocation from `/api/ps`, not its advertised maximum training context. A lower configured context window acts as a ceiling. For a generic OpenAI-compatible server without reliable runtime metadata, enter its actual serving context window. Unknown capacity produces an actionable settings error and leaves the request saved.

Capacity discovery is cached briefly and refreshed at turn admission, explicit Settings refresh, and confirmed context overflow. Changing an override invalidates the matching cache. Different credential/endpoint/model identities do not share observed allocation or estimate calibration.

The optional maximum response and response-reserve fields must be positive and fit within the serving window. By default, response reservation is the complexity response allowance capped at one quarter of the actual window and the known response maximum. Headroom is at least 256 tokens or 5% of the window, rounded up. System instructions, conversation, tool schemas/results, protocol overhead and media all count. Counts are conservative estimates unless a provider tokenizer supplies them; observed provider usage can increase headroom for later requests.

## Working history and progress

Before each model dispatch, UMCode checks the complete request against the selected endpoint's capacity. Large tool results, including the newest result, can be replaced in working history with bounded excerpts and source references. Full source bytes remain saved. `task.context` can page a chat-owned source by `item_id`, `offset` and `limit`; source access does not authorize replaying an action. A shortened user working copy explicitly says it is incomplete and points to the original.

Older live history can be summarized in bounded chunks while retaining the current interaction and tool call/result pairs. A model step allows at most two reduction passes, with at most eight summary calls per pass and the existing execution budget. Failure to reduce or persist a checkpoint stops accurately; there is no unrestricted fallback request. Checkpoints remain conversational source data, not higher-priority instructions.

Compaction is automatic. There is no button, menu, command-palette entry or shortcut for it. An indeterminate progress bar above the affected main or side composer shows “Automatically compacting context…” and the actual preparing, summarizing or restoring phase. There is no invented percentage or ETA. Reduced-motion preferences are respected. Ordered durable states handle reconnect, cancellation, failure and interrupted shutdown. Hidden child activity is not broadcast as a main-chat event.

The Token optimization master switch enables optional retrieval, reducers, progressive tools and context compilation together. Physical request capacity, saved originals and basic working-history compaction apply with either switch value. Disabling optimization does not restore mandatory intent or completion contracts.

## Routing, interruption and review

A direct model selection stays on that endpoint/model; credential retry cannot silently move to another endpoint. Cross-model fallback requires a selected or default pool and stays within enabled pool members. The selected pool/model remains distinct from the route actually used. A fallback request is prepared again for the new capacity.

A `length` stop alone does not establish context exhaustion. UMCode distinguishes a known output cap, corroborated context exhaustion and an unknown interruption. Partial text remains saved and marked as interrupted; incomplete calls are discarded. A confirmed context overflow allows at most one reduction retry for that model step. Completed tool actions are represented by their recorded results and are not replayed by this retry.

Review is optional and model/user-directed. A standalone reviewer receives an isolated snapshot of current lead changes, focused assignment and scoped references. It remains read-only under the existing tool allowlist. The engine does not require a structured semantic review report to finish ordinary chat execution. Explicit legacy integration/completion workflow APIs retain their opt-in contracts.

## Deterministic validation

These scenarios replace only the external model with scripted fixtures and use real storage, tool execution and request preparation. Fixture token counts are test measurements, not real provider bills or savings estimates.

| Scenario | Result | Recorded fixture measurements |
| --- | --- | --- |
| Goodreads scaffold edits plus requested grep | Exact expected file bytes; real grep has no matches and exits 1; response reports that result truthfully | 4 model calls, 2,500 input tokens, 160 output tokens, no classifier call; observed turn time about 0.83 s on this machine |
| 16K endpoint with a large Unicode file and “resume now” | Newest result bounded with retrievable original; continuation completes | Capacity enforced for each dispatch; no intent-category gate |
| Master switch both off and on | Requested file bytes written; no mandatory verification/intent contract | Zero classifier calls in either state |
| Output interruption and confirmed overflow | Incomplete calls never execute; confirmed overflow retries at most once with preserved result IDs | Deterministic provider errors/usage; no paid request |
| Optional standalone review | Separate snapshot contains current lead edits | Existing read-only permissions and shared admission retained |

The earlier user-reported failure was `resolve requested outcome: explanation cannot claim execution`. There is no instrumented pre-change run of the full scaffold scenario, so before/after savings percentages are intentionally unavailable. Full test, static-check, build and race outcomes are recorded in the implementation handoff.

## Reference implementation research

XERJ lexical searches returned UMCode's existing `turnRecorder`/nullable `RequestDiagnostics`, tool selection and workspace-snapshot helpers. Relevant peer passages were read at pinned Codex commit `9b39316997fd8bcecdb2007722f52ef858f0dbd8`: `codex-rs/core/src/tools/handlers/tool_search_spec.rs:116` (deferred discovery), `core/src/session/context_window.rs:53` (hard context cap versus compaction scope), `core/src/state/auto_compact_window.rs:77` (window transitions), `core/src/guardian/input_budget.rs:74` (bounded review input), and `core/src/compact_remote_v2.rs:286` (compaction routing lifetime). The pinned license is Apache-2.0. No peer source was copied. UMCode's serving-capacity adapters and permission requirements remain authoritative.
