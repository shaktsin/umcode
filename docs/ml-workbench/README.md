# Visual ML workbench

This change implements local model storage and visual architecture building from
the [ML workbench roadmap in PR #78](https://github.com/shaktsin/umcode/pull/78).
It is a builder milestone. Data loading/preparation, training, checkpoints and
in-app inference are not implemented.

## Major workflows

| Workflow | What to try | Screenshot |
| --- | --- | --- |
| Create a text model | Workbench → New model → Text generation → choose Blank or a starter | [Text setup](screenshots/01-create-text.png) |
| Build visually | Drag layers, connect ports, select properties, move/delete nodes, undo/redo | [Text architecture](screenshots/03-text-builder.png) |
| Understand errors | Read the issue list; click a layer-specific issue to inspect its settings; invalid architectures cannot export | [Validation](screenshots/04-validation.png) |
| Inspect/export Python | Open Code, select a layer to highlight its code, then Export .py | [Generated code](screenshots/05-code-export.png) |
| Close/reopen safely | Back to models → reopen; unsaved architecture edits remain in memory and are marked on the card | [Draft navigation](screenshots/02-models-and-drafts.png) |
| Text classification | Classification + Text + Small text classifier | [Classifier](screenshots/06-text-classifier.png) |
| Create an image model | Classification + Vision (images) + Small CNN or Small Vision Transformer | [Vision setup](screenshots/07-create-vision.png) |
| Edit a CNN | Set image channels/height/width; configure convolutions, normalization and pooling | [CNN](screenshots/08-cnn-builder.png) |
| Edit a vision Transformer | Configure patch size, dimensions/heads/repeats and noncausal attention | [Vision Transformer](screenshots/09-vision-transformer.png) |
| Feature vectors | Classification + Feature vectors + Feature classifier | [Feature classifier](screenshots/10-feature-classifier.png) |
| Custom architecture | Custom + input type + Blank canvas; add and connect supported layers | [Custom vision](screenshots/11-custom-builder.png) |
| Narrow/dark desktop | At 760px, use the chat toggle and scroll the properties overlay | [Narrow layout](screenshots/12-narrow-dark.png) |

The snapshots are actual UI captures from the real-engine acceptance script,
using temporary projects and no provider credentials. They are not mockups.
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
The engine exposes four project-scoped RPCs for listing, creating, opening and
saving. Saves use atomic replacement and preserve exact JSON numeric literals;
bounded regular-file reads reject linked/oversized bundles.

Selection is remembered per project. Drafts remain in memory across model,
project and view navigation and reconnects. Back to models shows a model list;
unsaved drafts are marked on cards. **Save model writes to disk; closing a model
does not auto-save.** Unsaved drafts are session-only. Reloading a dirty model
requires confirmation. Stale async responses cannot apply to a different
project/selection, and edits made during save/reload remain recoverable.

Existing unsupported architecture content is shown read-only with an explanatory
message instead of being silently converted or overwritten. Wide layouts keep
ordinary project chat beside the builder; narrow layouts offer it as an overlay.
An existing chat from another project is kept behind an explicit choice so it
cannot silently receive work intended for the selected project.

## Try locally

Build/run the engine and frontend from this checkout, using the same engine home
and WebSocket port in both terminals. Open an existing project folder in the UI.
No provider key is needed for model building; chat requires normal provider setup.

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

- Data, Train and Inference deliberately display unavailable states. A
  [Train screenshot](screenshots/13-training-placeholder.png) makes that boundary
  visible. This PR does not complete the full roadmap in #78.
- No dataset upload/preparation, training jobs, compute/GPU connectors,
  checkpoints, evaluation, inference playground or model-aware chat tools.
- One Input-to-Output path is supported. Arbitrary branching, general nested
  containers and custom Python layers are not implemented. Transformer blocks
  contain their own residual attention/MLP structure and support repeats.
- Save persists the architecture. Export .py downloads the generated module;
  saving does not currently replace the bundle's reserved `model.py` scaffold.
  Adapter files are scaffolding, not an implemented model-execution contract.
- Exported weights start randomly. The memory number covers float32 parameter
  weights only, excluding activations, gradients and optimizer state.
