# Workbench Assistant Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ask questions about the current model/layer and preview/apply supported architecture changes through chat.

**Architecture:** Existing chat turns receive an optional bounded architecture snapshot. A scoped proposal tool returns typed edits, which the frontend validates and applies through the existing editor history after revision checks. Preview never changes the draft or disk.

**Tech Stack:** Go protocol/engine, Svelte 5, TypeScript, Svelte Flow, Vitest and real-engine Playwright scripts.

**Spec:** `docs/superpowers/specs/2026-10-31-workbench-assistant-design.md`

## Global Constraints

- Maximum snapshot: 1 MiB transport, 200 nodes and 200 edges.
- Maximum proposal: 32 operations and 64 KiB.
- No raw JSON UI, arbitrary Python, full-graph replacement, autonomous edits or autosave.
- Proposal tools never write the filesystem; only the explicit existing Save action persists a draft.
- Workbench turns expose/execute only the proposal tool; ordinary chat behavior is unchanged.
- All changes in one proposal form one undo entry. Partial architectures may retain visible shape/incompleteness errors.
- Draft, project and model must still match at Preview and Apply. Selection changes alone are allowed.

## Review Focus

- A same-project model switch or same-model reload during a delayed reply cannot retarget Apply (Tasks 1, 3, 4).
- Unsupported/extra operation fields or extreme values cannot sneak past the layer setting limits (Tasks 1, 2).
- A provider hallucinating another registered tool during a workbench turn cannot execute it (Task 2).
- Cancelled/failed tool calls never render an actionable proposal (Tasks 2, 3).
- Existing manual edits and assistant edits share undo, and preview cannot reset history or move real nodes (Tasks 1, 3, 4).

## Shared transport contract

Optional `turn/start.workbench`: `{projectId,modelId,modelName,graphJson,revision,selectedNodeId?,summary}`.
`revision` is SHA-256 of the exact graphJson UTF-8 text (lowercase hex). The engine checks it and replaces modelName with the bundle name. Summary is bounded to 16 KiB and is reference data, not instructions.

Proposal tool canonical name `workbench.propose_edit` (wire `workbench__propose_edit`) takes `{explanation,operations}`. Its completed item has `data.workbenchProposal`:
`{projectId,modelId,modelName,revision,explanation,operations}` bound by the engine to admitted context. The user-message item has `data.workbenchContext` with the submitted context. Explain-only replies do not need a tool call.

Operation union:
- `{type:'update',nodeId,config}` merges supported settings into a node.
- `{type:'add',ref,kind,config?}` adds a supported layer with application-assigned ID/position. `ref` can be used in later operations and cannot collide with an existing ID.
- `{type:'remove',nodeId}` removes the node and incident edges.
- `{type:'connect',source,target}` connects node IDs or earlier add references.
- `{type:'disconnect',edgeId}` removes an existing edge.

Reject unknown fields, invalid settings/types/ranges, unknown IDs, cycles/branches and no-effect proposals. Apply the whole batch to a clone; reject the whole batch on any invalid operation.

### Task 1: Validated proposals and editor bridge

**Files:** create `app/frontend/src/lib/workbench/assistant.ts`, `assistant.test.ts`, `assistant.svelte.ts`, `assistant.svelte.test.ts`, `assistantStore.svelte.ts`.

**Interfaces:**
- Export `WorkbenchContext`, `WorkbenchProposal`, `WorkbenchOperation` types from `assistant.ts`.
- Export `graphRevision(text: string): Promise<string>`, `buildContext(projectId, model, text, selectedNodeId): Promise<WorkbenchContext>`, and `previewProposal(text: string, proposal: WorkbenchProposal): ProposalPreview`.
- `ProposalPreview` contains candidate `graph`, before/after `Analysis`, human-readable `changes` and affected node IDs.
- `WorkbenchAssistantState` accepts `readCurrent: () => {projectId, modelId, modelName, text} | null`; it exposes selectedNodeId, preview, error, focusRequest, `capture()`, `bind(commit: (graph: Graph) => void): () => void`, `showProposal(proposal)`, `apply()`, `cancel()` and `ask()`.
- A preview stores its exact base text and identity; Apply checks those again synchronously after any awaited hash and calls the bound editor's commit once.
- `assistantStore.svelte.ts` exports `workbenchAssistant` wired to the existing workbench store.

- [ ] Write tests for multi-setting edits, insertion with references, removal/reconnection, partial models, range/type/unknown-field rejection, cyclic/branch edits, limits, no effects, and unchanged source after failure.
- [ ] Run `npm test -- src/lib/workbench/assistant.test.ts`; expect missing module/exports failure before implementation.
- [ ] Implement strict validation using existing layer definitions, graph helpers and analyzer. Use SHA-256 via Web Crypto; put the supported layer schema and current analysis into the bounded context summary.
- [ ] Write controller tests for capture, one commit, cancel/no commit, stale text/model/project rejection, delayed hash races, bind/unbind and selection-only changes.
- [ ] Run the new controller tests RED, implement the controller, then run both new suites GREEN.
- [ ] Commit the domain/controller and run the two suites as task completion verification.

### Task 2: Scoped backend context and proposal tool

**Files:** create `internal/protocol/workbench_assistant.go`, `internal/engine/workbench_assistant.go`, focused engine/server tests; modify `internal/protocol/methods.go`, `internal/engine/engine.go`, `turn.go`, `tool_selection_runtime.go`.

**Interfaces:** consumes Shared transport contract; produces completed item metadata above. Workbench context travels in both live request context and durable user-message item data. Normal turns omit it.

- [ ] Add tests for valid context hashing/bounds, mismatched project/model, selected ID not in graph, invalid format, oversized summaries, strict bounded proposal parsing, and proposals without active context.
- [ ] Run focused tests RED before adding types/functions/tool.
- [ ] Add optional turn context and a proposal tool. Validate before turn admission/provider calls, bind context to the turn and append it as reference data to the LLM input. Add clear instructions for architecture guidance and proposal use.
- [ ] Register the tool, filter it out of normal turns, and use an explicit one-tool catalog for workbench turns. Reject all other tool calls before execution, regardless of progressive discovery.
- [ ] Store immutable context on the user item and completed proposal metadata on the successful tool item; no metadata on failed/cancelled tool calls.
- [ ] Add real-RPC/fake-provider tests that capture context, return a proposal, prove disk graph unchanged, reopen item history, and attempt a forbidden shell/file tool. Run RED then GREEN against the new behavior.
- [ ] Run focused engine/server tests and vet; commit.

### Task 3: Chat, selection, proposal cards and canvas preview

**Files:** modify `App.svelte`, `Workbench.svelte`, `WorkbenchChat.svelte`, `ChatColumn.svelte`, `Composer.svelte`, `stores/chat.svelte.ts`, `types.ts`, `workbench/VisualBuilder.svelte`, `LayerInspector.svelte`, `ModelCanvas.svelte`; create `workbench/WorkbenchProposal.svelte` and focused component tests.

**Interfaces:** consumes Task 1 singleton/controller and Task 2 metadata. Extend `ThreadView.send` and `chat.send` with an optional typed workbench context while retaining existing signatures for ordinary callers. `ChatColumn`/`Composer` get `workbenchMode=false` props.

- [ ] Add tests proving context is submitted only from the workbench composer and capture errors preserve input; run RED before implementation.
- [ ] Make the workbench chat collapsible at wide and narrow sizes, default closed, with a safely remembered preference. Expose an open action so Ask about this layer opens/focuses without submitting.
- [ ] Add selected-layer Ask action and bind the VisualBuilder's existing apply/commit function to the controller. Clear binding/selection on unmount or identity change.
- [ ] Add immutable context labels in chat history and a readable completed-proposal card with shape/parameter changes and validation issues. Render no actionable card for in-progress/failed/denied tool items.
- [ ] Add read-only candidate preview on the canvas, affected-layer highlighting, Cancel and Apply. Guard all canvas mutations/shortcuts/palette/property actions while previewing; Apply invokes the existing history-preserving commit exactly once.
- [ ] Test Apply/Undo/Redo and Preview/Cancel, stale proposal labels and disabled actions, no selection/context leakage into normal chat, and collapsible chat. Run check and all frontend tests GREEN; commit.

### Task 4: End-to-end acceptance, local runtime and PR #91 update

**Files:** create `app/frontend/e2e/workbench-assistant.mjs`; extend e2e/product docs and add captured workflow screenshots under `docs/ml-workbench/screenshots/assistant/`.

**Interfaces:** real engine on temporary home/projects, existing Playwright setup, deterministic fake provider exposing the normal configured provider API.

- [ ] Write the real-engine workflow test and run it RED against the missing integration before completing remaining integration details.
- [ ] Exercise actual selection/question capture, unsaved context, explanations, preview/cancel, settings edit and layer insertion, Apply/Undo/Redo, save/reopen, stale manual edit/model/project switches, provider failure, malicious tool attempt and narrow/dark layouts.
- [ ] Run frontend check/all tests, focused Go tests/vet, real-engine browser workflow and native build/signature checks; report any inherited broader failures accurately.
- [ ] Get one fresh whole-branch review, resolve consequential findings with RED→GREEN regressions, and rerun affected verification.
- [ ] Build the local engine/app and refresh only the isolated preview processes. Preserve user models and any unrelated native app. Give a usable local URL and exact clicks; explain provider setup if required.
- [ ] Fast-forward the original `feat/ml-workbench-p0` branch to the verified assistant work and push normally. Update existing PR #91 with the final scope, verification and real screenshots, then verify links/state. Do not open another PR or merge.

## Execution notes

The user approved the design, asked to build/test locally, and selected the
existing PR #91 for integration. Execute inline with one fresh final review;
no further design/plan approval gate. Keep the local test environment available.
