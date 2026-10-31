# Layer-by-layer assistance in the ML workbench

Status: approved addition to PR #91; builder check found no important open
questions. Implementation and acceptance verification are in progress.

## Intent and size

Help someone understand and improve the architecture they are building without
leaving the visual canvas or entering JSON. A selected layer is the natural
starting point for questions about dimensions, settings, purpose and next steps.
The assistant can explain and propose changes; the user previews and applies
them to the draft.

This is a medium feature added to the visual workbench PR. It crosses chat
input, protocol/engine handling, proposal validation, the builder UI and tests.
It reuses provider routing, chat streaming/history, supported layer definitions,
shape analysis, parameter counts, draft persistence and undo. It introduces no
new ML runtime, training or inference implementation. Exact file/line counts
and a duration are not estimated before the implementation plan.

PR #91 includes both the visual builder and architecture assistance. It remains
a builder milestone; training, inference and deployment are separate work.

## Decisions from the conversation

- Keep chat useful alongside the builder for layer-by-layer questions.
- Use the current architecture, selected layer/settings, input/output shapes
  and validation issues, including unsaved changes.
- Explain, propose, preview, Apply and Undo; Save remains a separate action.
- Recheck a proposal if the user changes the model while waiting for a reply.
- Include this work in the existing PR #91.

## Approaches

1. Contextual explanations only: reuse chat with a model snapshot, leaving all
   edits manual. This is smaller, but a suggested fix still has to be translated
   back into settings and connections by the user.
2. Contextual explanations plus structured, reviewable edits: send a model
   snapshot, let the assistant propose supported operations, and validate and
   preview them before a user applies them. This is the recommended approach
   accepted as the direction in the conversation. It requires more integration
   and testing but completes the suggested-change workflow.

Automatic edits and arbitrary code patches do not fit the accepted preview and
Apply interaction. They are outside this design.

## Scope and interaction

Chat is collapsible at all widths; narrow layouts retain an overlay. Remember
the open/closed preference locally. A layer's properties offer **Ask about this
layer**, which opens chat, focuses its input and makes the selected layer clear.
It does not send a message or incur a model request until the user submits.

The composer shows a compact model/layer context label. The submitted message
keeps that context label even if the user subsequently selects a different
layer. Questions without a selected layer use the whole current model.
Use the existing project chat and its provider selection, not a new conversation
database or a new LLM connection. Every workbench turn carries its own explicit
model context; a response/proposal is bound to that turn's model and project.
Reuse the transcript and provider without creating or changing the conversation's
software execution-task/work records. Model guidance is advisory until Apply.
Switching models does not rewrite previous message labels or retarget proposals.
The existing wrong-project chat guard remains in force.

The assistant can answer questions without proposing an edit. When it proposes
one, show a human-readable card with its explanation, affected layers/settings,
before/after shapes, parameter-count change, and validation issues before/after.
No raw operation JSON is a user interaction.

**Preview** displays the candidate architecture on the canvas with affected
layers highlighted, a clear preview label and Apply/Cancel actions. The preview
allows inspection/pan/zoom, but cannot mutate the actual draft. Cancel restores
the draft view and leaves history untouched. Apply commits the complete change
as one existing undo entry, marks the model unsaved and reruns normal analysis
and Python generation. Saving is the existing explicit Save model action.

## Context and proposal transport

Extend the existing turn submission with optional workbench context rather than
embedding invisible instructions in the user's question. Context contains the
project/model identity, the exact submitted graph snapshot, selected node ID,
derived shapes/issues/parameter summary and a revision identifier bound to that
snapshot. Include supported layer kinds and setting constraints compactly so
the assistant can produce meaningful operations. Do not send datasets, weights,
credentials or generated Python automatically.

Capture context at submission, once per user turn. Selecting/moving a layer or
editing a setting does not make background LLM calls. Bound the snapshot by the
existing graph limits (1 MiB transport, 200 nodes and 200 edges); reject an
oversized context clearly instead of silently truncating its architecture.
Ordinary chat turns carry no workbench context and retain their existing behavior.

The engine checks that the thread project matches the submitted project, that
the model exists inside that project, and that context has a valid bounded
structure. Snapshot contents and client-derived analysis are reference data,
not authoritative instructions. The engine binds proposal provenance to the
admitted turn, not to model-supplied project/model IDs.

Expose a dedicated proposal tool only for a turn with valid workbench context.
Use the existing provider tool-call mechanism and durable chat item data to
return/display a proposal; do not scrape JSON from prose/code fences. In this
mode the advertised and executable tools are limited to that proposal mechanism;
generic file/shell/plugin/delegation actions cannot bypass Preview/Apply. The
normal provider, routing, streaming, interruption and error handling remain.
The proposal tool records a suggestion and never writes the model or filesystem.

The initial operation vocabulary covers adding a supported layer, changing
supported settings, removing a layer, and connecting/disconnecting existing
ports. New-layer references can be used by later operations in the same batch;
the application assigns actual IDs/positions. Removing a layer removes its
incident edges as the current editor does; reconnecting neighbors must be an
explicit operation in the same proposal. Changing layer kind is remove plus add.
There is no arbitrary Python, arbitrary setting name, file path or full-graph
replacement operation. Bound each proposal to 32 operations and 64 KiB.

The frontend strictly validates all operations against current supported kinds,
setting types/ranges and graph limits, applies the complete batch to a clone,
then uses the existing graph reader/analyzer. Reject malformed, unsupported,
cyclic or branching edits without changing the draft. Shape/incomplete-model
issues are shown explicitly: a well-formed partial architecture may still be
applied while building layer by layer, matching the existing manual builder.
It must not be presented as ready to run, and export retains its existing
validation gate. Do not silently repair or omit operations that were proposed.

## Stale proposals, navigation and failure behavior

Bind each proposal to the project, model and exact draft revision used by its
turn. A revision can be a deterministic fingerprint of the submitted graph;
the exact internal representation is a builder choice. Compare at preview and
again atomically at Apply. If the draft has changed, show **Model changed since
this suggestion** and disable Apply; the user can ask for an updated suggestion.
Do not merge or rebase model-generated edits automatically in this version.
Changing only selection does not invalidate the graph revision.

A project/model switch, closed model, reconnect or delayed response cannot
apply to a different draft. A returned proposal may remain visible in chat with
its original model label, but Apply requires the original model and matching
draft. Applying a proposal twice without an intervening undo is prevented.
Undo/redo use the same editor history as manual edits.

Cancellation, provider failures, malformed proposals and interrupted tool calls
leave the draft and disk unchanged. A proposal is actionable only after its
complete tool result is recorded. No provider configured uses the existing
connect-provider flow and does not block manual building. Unsupported legacy
graphs keep their current protection and cannot receive assistant edits.

## Exclusions

- No Data/Train/Inference implementation, checkpoints, pretrained model import,
  deployment, or promotion of a workbench architecture into a chat provider.
- No new layer types, branching graphs, custom Python layers, hidden autosave,
  autonomous edits, whole-model replacement or automatic stale-proposal merges.
- No automatic project-file/dataset browsing or screenshots sent to the LLM.
- No promise that a reasonable architecture is trained, accurate or fast.

## Verification and review artifacts

Use focused tests for strict operations, shape/parameter diffs, partial models,
invalid fields/numbers, all-or-nothing application, undo/redo, repeated Apply,
revision mismatch and project/model navigation. Protocol/engine tests cover
bounded context, correct turn provenance, unavailable tools outside workbench
turns, prevention of alternate tool execution, failure/cancellation, and durable
proposal reconstruction from chat history. Existing ordinary chat tests must pass.

Use a deterministic fake provider with the real engine and app for end-to-end
acceptance: select a layer and ask, inspect the captured unsaved context, receive
an explanation/proposal, Preview/Cancel, Apply/Undo/Redo, save/reopen, and stale
proposal rejection after a manual edit or project/model switch. The actual
provider API is not required for these tests; a separate live-model trial may
assess answer usefulness without claiming it is deterministic correctness.

Capture screenshots for selected-layer questions, a proposal/diff, highlighted
preview, applied unsaved changes, stale-suggestion state and narrow/dark layouts.
Run frontend checking/tests, focused Go protocol/engine/server tests, real-engine
browser acceptance, and a native build check appropriate to the actual changes.
PR #91's verification report already records three unrelated server-test failures
reproduced on its base; do not claim a green full Go suite without rerunning it.

## Builder discretion and approval

Internal helper names, component extraction, exact operation field names,
canvas highlighting style and revision encoding are implementation choices.
They may be settled without changing the interaction and bounds above.
The independent builder check returned "nothing important left" and the user
approved implementation. Verification and screenshots are recorded with the
product documentation in `docs/ml-workbench/`.
