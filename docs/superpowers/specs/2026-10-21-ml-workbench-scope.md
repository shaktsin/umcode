# ML Workbench: scope and roadmap

Status: **draft for review**. This is the scope-level document for the whole
initiative. It is not a per-phase design. Each phase below gets its own spec,
then its own implementation plan, after you approve this scope.

Mockups: [`docs/superpowers/mockups/ml-workbench/`](../mockups/ml-workbench/README.md)
(nine screens, with PNG previews and per-screen check lists).

## 1. Goal

Add a workbench next to chat where you can prepare data, design a PyTorch
model visually, train or finetune it on a local or remote GPU, then evaluate
and try the trained model, without leaving UMCode. Chat can act on the same
objects (edit the graph, launch a run, evaluate a checkpoint).

Success looks like this loop working end to end for a small text model:
create a model, pick data, train on a GPU, see the loss live, evaluate, and
chat with the result.

## 2. Who it is for, and assumptions

- Developers who know what a model, a loss and a GPU are. Not a no-code tool.
- Remote GPUs are machines you can reach over SSH (later also Slurm). Not a
  managed cloud in v1.
- Finetuning (loading existing weights) matters as much as training from scratch.
- Everything stays local-first: models, runs and dataset manifests live in the
  project folder. Large data stays where it already is.

## 3. Decisions

### Agreed in brainstorming

| Decision | Source |
|---|---|
| A workbench column beside chat, with a drag-and-drop layer canvas and the PyTorch code behind it | your request |
| Data sources that you register and index; connectors for local or remote GPUs | your request |
| Zoom, pan and a minimap on the model viewer | your request |
| The task (text generation, classification, custom) is chosen **first**, when a model is created, and drives Build, Train and Inference | your correction |
| Evaluate and Inference work for any model through a small **adapter** (declared input and output types and metrics) | your choice (option B) |
| The adapter comes from a **template** (`adapter.py` generated from Text generation or Classification, editable, chat can write one) | your choice (option 3) |

### Proposed, please confirm

| Proposal | Why |
|---|---|
| A **Data** tab before Build, Train, Inference, holding a visual preparation pipeline and charts | you asked to see data preparation and visualization |
| The task is fixed once chosen; changing it needs a new model or a rebuilt head | keeps loss, data mapping and adapter consistent |
| The graph is the source of truth and code is generated from it. Hand-written layers go through a "Custom layer" escape hatch | avoids two-way sync, the hardest part of this design |
| Model bundle on disk: `models/<name>/{model.json, graph.json, model.py, adapter.py, runs/}` | everything for one model in one folder, diff-friendly |
| Datasets are manifests of shards with immutable numbered versions; each run records its dataset version | makes large data and reproducibility possible later |
| Data stays where it is; your Mac keeps manifests, stats from a sample and pipeline definitions | big data cannot move to a laptop |
| A capacity planner (memory, time, cost estimates before launch) | avoids launching runs that cannot fit |
| v1 data preparation: length filter, near-duplicate removal, format, split, tokenize | smallest useful pipeline; PII redaction and oversampling come later |

## 4. Out of scope for v1

- Hosted serving or deployment of a model (no API endpoint)
- Kubernetes and on-demand cloud GPU connectors
- Embedding map of documents
- PII redaction and oversampling steps
- Image, audio and video tasks beyond what the adapter schema allows
- Hyperparameter search, multi-user collaboration, experiment tracking servers
- Distributed data preparation (arrives with Phase 5)

## 5. How it fits UMCode today

Observed in the repo (read-only look, to be re-checked in each phase spec):

- Desktop app is Wails with a Go engine and a Svelte 5 frontend.
- Shell layout is a rail, a main chat column and auxiliary panels
  (`app/frontend/src/lib/ui/shellLayout.ts`, `components/Rail.svelte`,
  `stores/app.svelte.ts`). The workbench would be a new view in the rail.
- Existing Go packages that are relevant: `internal/retrieval` (indexing),
  `internal/mcp`, `internal/plugins` (templates could ship as plugins later),
  `internal/policy` and approvals (remote execution is sensitive and should use
  them), `internal/compute` (a local libkrun VM with no GPU path, so not the
  place for GPU jobs).
- Proposed new backend packages: one for the model bundle, graph and codegen,
  one for connectors and jobs. Proposed new frontend folder for the workbench.
  Names and boundaries are decided in the Phase 0 spec.
- Each phase plan starts with a reference-coding search (existing code and
  prior art) before any code changes.

## 6. Phases

Recommended order: P0, P1, P2, P3, P4, P5. After P2 there is a working
end-to-end loop (build, train, watch). P3 makes it testable. P4 improves the
data. P5 adds scale. Each phase is independently shippable.

### P0. Foundations
- Model bundle format and storage, RPC for list, create, open, save.
- Workbench view in the shell with tabs, the task badge and empty states.
- **Done when:** creating a model writes the bundle; reopening the app reloads
  it; the shell chrome matches mocks 00 and 01.

### P1. Build
- New model screen: task, name, starting point (mock 00).
- Canvas: palette, drag and drop, connect, select, properties, containers
  (Repeat × N), zoom, pan, fit, minimap, undo and redo (mock 01).
- Shape inference with inline errors, parameter count, memory estimate.
- Code generation from the graph; code pane highlights the selected layer;
  export `.py`.
- Starters: GPT-2 small and a small classifier.
- **Done when:** the GPT-2 starter builds, a layer edit regenerates code, and a
  test imports the generated module and runs a forward pass on random input on CPU.

### P2. Compute and Train (thin slice)
- Connectors: This Mac (CPU or MPS) and an SSH machine. Test connection checks
  reachability, CUDA and torch version. Secrets in the Keychain. Remote
  execution goes through existing approvals.
- Job runner: send the bundle to the target, run, stream logs and metrics,
  pause and stop, sync checkpoints back.
- Data: a local folder of text, mapped by task. No preparation yet.
- Train screen: settings, live loss chart, log, GPU panel (mocks 02, 03).
- **Done when:** a small model finetunes on this Mac and on one SSH GPU box,
  loss streams live, stop works, and a checkpoint lands in `runs/<n>/`.

### P3. Inference and Evaluate
- `adapter.py` created with the model. Templates: Text generation,
  Classification. Schema validation with "Fix with chat".
- Playground: load a checkpoint on a connector, generate, compare with the
  base model, history, "Add to eval set" (mock 04).
- Evaluate: eval jobs on a held-out slice or the golden set; metrics registry
  (loss, perplexity, accuracy, F1); results table, worst examples, per-slice
  breakdown; results saved under `runs/<n>/evals/` (mock 05).
- Chat tools to run an evaluation or a generation.
- **Done when:** after a P2 run the playground answers from the checkpoint,
  and an evaluation produces the comparison table and worst examples.

### P4. Data preparation and visualization
- Local folder sources with an index; profile computed from a sample.
- Charts: length histogram, category mix; before and after row preview.
- Pipeline v1: length filter, near-duplicates, format as text, stratified
  split, tokenize and pack; preview on a sample; apply to save a numbered
  dataset version; runs record the version (mocks 03, 06).
- **Done when:** a pipeline over a 50,000-row folder produces a new dataset
  version, training uses it, and "Compare with previous version" works.

### P5. Scale
Needs its own brainstorming round first. P0 to P4 must not block it:
manifest-based datasets, immutable versions, and a connector interface that
declares capabilities (GPU count, scheduler, cost rate).
- Capacity planner and the cluster options table (mock 08), budget cap, stop
  if slower than a limit, estimate corrected from measured speed.
- S3 and GCS sources with manifests, sharded datasets, streaming loader,
  region locality check (mock 07).
- Slurm connector (submit, queue state, logs) and distributed preparation.
- **Done when:** a sharded dataset trains on a Slurm cluster and the planner's
  estimate can be compared with the measured run.

### Later
PII redaction, oversampling, embedding map, Kubernetes and on-demand cloud,
plugin-shipped templates and tasks, image and audio tasks.

## 7. Mockups by phase

| # | Screen | Phase |
|---|---|---|
| 00 | New model, choose the task | P1 |
| 01 | Build: canvas, code, chat | P1 |
| 02 | Train: live run on a remote GPU | P2 |
| 03 | Data sources and GPU connectors | P2 (compute), P4 (sources) |
| 04 | Inference: playground | P3 |
| 05 | Inference: evaluate | P3 |
| 06 | Data: prepare and visualize | P4 |
| 07 | Data at scale | P5 |
| 08 | Train plan: size the cluster | P5 |

Numbers in the mockups (times, costs, rates, loss values) are illustrative.

## 8. Open questions

1. Should the graph stay the single source of truth with code generated one
   way (proposed), or must hand edits to the code flow back into the graph?
2. Remote execution: plain SSH with a script (proposed for P2) or an agent
   installed on the remote machine (the connector mock mentions a runner)?
3. Python environments on remote machines: use what is installed, or create a
   managed environment (uv or venv) per model?
4. Where does the workbench live in the rail, and is chat always visible beside it?
5. Classification: where do label names come from, and how is that shown? Not mocked yet.
6. How much of Apple MPS do we support in v1, given gaps in some operations?
7. Should changing a model's task ever be allowed on an existing model?

## 9. Main risks

- **Remote environments differ** (torch and CUDA versions, missing packages).
  Mitigation: the connection test reports them; P2 starts with one known setup.
- **Connections drop during long runs.** Mitigation: runs reattach and resume
  from the last checkpoint; the job does not depend on the app staying open.
- **Graph editor effort.** Dragging, connecting and shape inference are a lot
  of UI. Mitigation: P1 ships a small layer set and grows it.
- **Scope creep from scale.** Mitigation: P5 is a separate phase with its own
  design.

## 10. How we work and check

- Per phase: spec, review, plan, implementation. Each phase starts with a
  reference-coding search.
- Mockups live in the repo next to this document and are the visual reference.
  Each phase's pull request is checked against the screens listed above using
  the per-screen check lists in the mockup README.
- Differences between the build and a mockup are recorded in
  `docs/superpowers/mockups/ml-workbench/DEVIATIONS.md` as accepted or to fix.
