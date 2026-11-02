# Visual ML workbench

The workbench supports local model storage, visual architecture building, layer
assistance, dataset preparation, CPU training with evaluation metrics, and local
checkpoint inference. It follows the [roadmap in PR #78](https://github.com/shaktsin/umcode/pull/78).

- [Data and Inference guide](data-inference.md): local uploads, Hugging Face, saved preparation, safe weight import and local chat promotion.
- [Train guide](local-training.md): numeric/text training, progress, cancellation, evaluation and automatic checkpoint handoff.
- [Workflow screenshots](screenshots/data-inference/README.md): actual Data → Train → Inference → Chat flows.

Training completion proves execution, not useful answer quality. Small models
trained from scratch may still generate gibberish; see the Train guide's quality
limits. A pretrained model or fine-tuning pipeline is not included.

## Major workflows

| Workflow | What to try | Screenshot |
| --- | --- | --- |
| Create a text model | Workbench → New model → Text generation → choose Blank or a starter | [Text setup](screenshots/01-create-text.png) |
| Build visually | Drag layers, connect ports, select properties, move/delete nodes, undo/redo | [Text architecture](screenshots/03-text-builder.png) |
| Understand errors | Read the issue list; click a layer-specific issue to inspect its settings; invalid architectures cannot export | [Validation](screenshots/04-validation.png) |
| Inspect/export Python | Open Code, select a layer to highlight its code, then Export .py | [Generated code](screenshots/05-code-export.png) |
| Close/reopen safely | Back to models → reopen; unsaved architecture edits remain in memory and are marked on the card | [Draft navigation](screenshots/02-models-and-drafts.png) |
| Delete a model | Use the model card's trash icon or Delete model while editing; confirm or cancel | [Delete confirmation](screenshots/deletion/02-delete-confirmation.png) |
| Text classification | Classification + Text + Small text classifier | [Classifier](screenshots/06-text-classifier.png) |
| Create an image model | Classification + Vision (images) + Small CNN or Small Vision Transformer | [Vision setup](screenshots/07-create-vision.png) |
| Edit a CNN | Set image channels/height/width; configure convolutions, normalization and pooling | [CNN](screenshots/08-cnn-builder.png) |
| Edit a vision Transformer | Configure patch size, dimensions/heads/repeats and noncausal attention | [Vision Transformer](screenshots/09-vision-transformer.png) |
| Feature vectors | Classification + Feature vectors + Feature classifier | [Feature classifier](screenshots/10-feature-classifier.png) |
| Custom architecture | Custom + input type + Blank canvas; add and connect supported layers | [Custom vision](screenshots/11-custom-builder.png) |
| Narrow/dark desktop | At 760px, use the chat toggle and scroll the properties overlay | [Narrow layout](screenshots/12-narrow-dark.png) |
| Ask about a layer | Select a layer → Ask about this layer → submit a question with current draft context | [Layer question](screenshots/assistant/01-layer-question.png) |
| Review suggested edits | Ask for a change → Preview changes → inspect shapes/parameters → Apply changes → Undo/Redo or Save model | [Canvas preview](screenshots/assistant/03-preview.png) |
| Reject an outdated suggestion | Edit the draft after a suggestion; its Apply path is disabled until an updated suggestion is requested | [Changed draft](screenshots/assistant/05-stale-suggestion.png) |

The snapshots are actual UI captures from real-engine acceptance scripts using
temporary projects. Builder captures require no provider; assistant captures
use a local deterministic HTTP provider with test credentials, not a paid API.
Full screenshot gallery: [screenshots/README.md](screenshots/README.md).

## Task, input and architecture

- **Task:** Text generation, Classification, Custom. The saved task is immutable.
- **Input type:** Text, Vision (images), Feature vectors. This is stored in the
  architecture's Input node and remains editable there.
- **Starting point:** Blank or a relevant architecture starter. Starters are
  randomly initialized architectures, not pretrained models.

The shared palette includes embeddings, position embeddings, Linear, attention,
repeated residual Transformer blocks, normalization, activations, dropout and
sequence pooling. Vision adds Conv2d, BatchNorm2d, 2D max/average pooling, global
average pooling, Flatten and patch embedding. Causal masking is configurable;
old text models default to causal, and the vision Transformer starter uses
bidirectional attention.

Shapes, parameter counts and an explicitly labeled float32 weight estimate
update as the architecture changes. Image layers use `[batch, channels, height,
width]`; patch embedding converts images into sequences. Invalid spatial sizes,
pooling padding, patch divisibility and incompatible feature dimensions produce
actionable messages. This is tensor-shape validation, not complete task/loss or
dataset validation.

## Persistence and navigation

Models live under the selected project's `models/<id>/` folder, with metadata,
the saved architecture, adapter/code scaffolding and a reserved `runs/` folder.
The engine exposes five project-scoped RPCs for listing, creating, opening,
saving and deleting. Saves use atomic replacement and preserve exact JSON numeric literals;
bounded regular-file reads reject linked/oversized bundles.

Selection is remembered per project. Drafts remain in memory across model,
project and view navigation and reconnects. Back to models shows a model list;
unsaved drafts are marked on cards. **Save model writes to disk; closing a model
does not auto-save.** Unsaved drafts are session-only. Reloading a dirty model
requires confirmation. Stale async responses cannot apply to a different
project/selection, and edits made during save/reload remain recoverable.

**Delete model** is available on model cards and while a model is open. Its
confirmation names the model and explains that unsaved edits will be discarded.
Cancel keeps the model and draft. Confirm moves the complete saved bundle into
the project's `models/.trash/<id>-<unique>/` directory, with its architecture,
files and any saved datasets/checkpoints. This is project-local recovery storage,
separate from Finder Trash. The success message gives the recovery path; move
that folder back to `models/<original-id>/` when the original name is free to
recover it, then refresh. There is no in-app Restore action yet.

Only explicit app clients can invoke deletion. The expected model creation
timestamp rejects stale deletion requests when a name has been reused; late
responses cannot discard a replacement model's draft. Deleting the final model
shows an empty list. Projects stay empty until the user chooses **New model**.

Existing unsupported architecture content is shown read-only with an explanatory
message instead of being silently converted or overwritten. The collapsible
assistant opens beside wide layouts and as an overlay in narrow layouts.
An existing chat from another project is kept behind an explicit choice so it
cannot silently receive work intended for the selected project.

## Ask questions and review changes

1. Connect a provider in Settings → Providers and select an available chat model.
2. Open a model in Build, select a layer, and choose **Ask about this layer**.
   The composer identifies the included model/layer. Opening it makes no LLM call.
3. Ask a question or request a change, such as “Why do these shapes mismatch?”
   or “Add dropout after this layer.” Each submission includes the current
   architecture, selected settings, shapes and validation issues, including
   unsaved edits. Datasets, weights and project files are not sent automatically.
4. A suggestion lists the proposed settings/connections, shape changes, parameter
   counts and remaining issues. **Preview changes** highlights the candidate
   architecture without editing the draft. Cancel leaves the draft untouched.
5. **Apply changes** makes one undoable edit. **Save model** persists it separately.
   Valid partial architectures can retain visible issues while being built;
   invalid architectures still cannot export.

Suggestions are bound to the submitted project, model and draft. Manual edits,
reloads or navigation can make a suggestion outdated; ask for a new suggestion
instead of overwriting newer work. Completed proposals remain in chat history.
Stop/cancellation, provider errors and invalid proposals leave the model alone.

Chat uses your configured assistant provider. It does not train or serve the
architecture being built. Workbench turns can propose supported layer edits;
generic coding/file tools and software-task state remain in ordinary Chat.
Open a model before submitting a workbench question.

## Try locally

Build/run the engine and frontend from this checkout, using the same engine home
and WebSocket port in both terminals. Open an existing project folder in the UI.
No provider key is needed for model building, local training, or local-checkpoint chat.
Layer assistance and cloud chat require normal provider setup. Install the managed
PyTorch runtime as described in the Data/Inference guide before training or inference.

```sh
# Terminal 1, repository root
export UMCODE_HOME=/tmp/umcode-workbench-dev
export UMCODE_ENGINE_WS_PORT=19766
go run ./cmd/umcode engine
```

```sh
# Terminal 2, repository root
export UMCODE_HOME=/tmp/umcode-workbench-dev
export UMCODE_ENGINE_WS_PORT=19766
cd app/frontend
npm ci
npm run dev
```

For a local macOS bundle, run `make app-build` from the repository root. The
browser workflows have been exercised; a successful native build/signature
check does not establish native window/download acceptance.

## Validation

See [verification.md](verification.md) for commands and the captured results.
The browser smoke is `app/frontend/e2e/visual-workbench.mjs`, with the existing
`e2e/workbench.mjs` entry point retained. See the frontend
[browser-test instructions](../../app/frontend/e2e/README.md#visual-ml-workbench).

Generated Python modules are imported and executed with real PyTorch on CPU.
The checks compare inferred versus actual shapes/parameter counts, run
training-mode backward passes, check finite gradients, and retain the text
starter's causal-prefix check. These tests demonstrate architecture execution,
not trained-model quality.

## Current boundaries

- Local CPU training supports numeric and text tasks. Image training, GPU/MPS,
  remote compute, pretrained fine-tuning, resume and early stopping are deferred.
- Inference executes supported visual graphs with PyTorch. llama.cpp, vLLM and
  SGLang are shown with compatibility explanations but are unavailable for these
  custom state_dict graphs. Runtime installers and general pretrained loading are
  future work. This PR does not complete the full roadmap in #78.
- One Input-to-Output path is supported. Arbitrary branching, general nested
  containers and custom Python layers are not implemented. Transformer blocks
  contain their own residual attention/MLP structure and support repeats.
- Save persists the architecture. Export .py downloads the generated module;
  saving does not currently replace the bundle's reserved `model.py` scaffold.
  Adapter files are scaffolding, not an implemented model-execution contract.
- Exported weights start randomly. The memory number covers float32 parameter
  weights only, excluding activations, gradients and optimizer state.

## Data and local inference follow-up

The [Data and Inference implementation guide](data-inference.md) covers local and
Hugging Face datasets, saved preparation, PyTorch checkpoint tests, and promotion
into regular chat. See the [real workflow screenshots](screenshots/data-inference/README.md).
The [local Train guide](local-training.md) covers training and evaluation, with
completed runs available directly in Inference.
