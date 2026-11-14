# Data preparation and local inference

This follow-up to the visual builder implements the **Data** and **Inference** tabs.
The local Train follow-up now supports small numeric/text models with evaluation metrics.
See [local training](local-training.md). Checkpoints trained outside the app can also be imported.

## Data

Import local CSV, TSV, JSONL, text, or labeled PNG/JPEG folders. The tab shows actual
rows, inferred columns, missing values and source provenance. Hugging Face supports
public native files, repository search/inspection, pinned revisions, example datasets,
and an optional read token for private/gated data. Tokens use the engine's secret
store and are not written into projects or browser storage. Dataset scripts, archives,
Parquet and other hubs are not supported in this version.

Choose input/target columns, task, train/validation/test percentages and a split seed.
Text uses a UTF-8 byte tokenizer with 259 IDs; numeric standardization fits statistics
on training rows only. Image preparation records resize/channel rules and source
assets. **Save preparation** stores a versioned recipe and prepared split artifacts
with this model. Importing a dataset or clicking **Save model** does not save preparation.
The original imported files remain available. Graph compatibility messages identify
settings to review in Build; preparation does not train or automatically rewrite layers.

Imports are bounded to 16 MiB, 1,000 files, 100,000 rows and 128 columns. Prepared
artifacts are bounded to 256 MiB. Writes use pinned nonsymlink directories and atomic
publication. Draft settings survive tab/model/project navigation and are separated by
model creation timestamp, including deletion followed by reuse of a model name.

## Inference

1. Save the visual graph and matching Data preparation.
2. Import a named `.pt`/`.pth` tensor state_dict (modern `torch.save` ZIP) or
   `.safetensors`, up to 64 MiB. Weights must match the exported visual builder's layer
   keys and tensor shapes. Arbitrary Hugging Face model files cannot be substituted.
3. Select **PyTorch**, load/test the checkpoint, and supply a prompt, raw numeric
   features or an image according to the saved input schema.
4. A successful causal text-generation test enables **Promote to Chat**. **Open in
   Chat** selects the pinned checkpoint in the normal model picker under **Local
   Workbench**, alongside configured cloud models.

Imports pin immutable copies of the graph, preparation and weights. Later Build or
Data edits do not silently change a checkpoint. PyTorch uses `weights_only=True`;
there is no unsafe pickle fallback or execution of project Python. The runner builds
only the supported palette, validates dimensions/allocations before materialization,
and verifies real finite outputs. Context, image and generation limits are enforced.
The first runner is CPU-only with at most two resident workers, 16 million parameters,
8 million activation elements, 8 MiB images and 1–256 generated tokens per request.

**Test architecture** needs no checkpoint: it executes random weights and reports
actual shapes and parameter count, clearly marked untrained. It produces no saved
checkpoint or learned answer and cannot enable chat promotion.

Chat promotion requires a matching, unexpired server-side test receipt. Local chat
requires no cloud credentials, uses only plain text conversation, and never falls back
to a cloud provider. Tools, attachments, agent delegation and thinking controls are
not exposed for local checkpoints. Promotions survive engine restarts; workers reload
pinned weights on demand. Removing a promotion preserves weights. Model deletion
removes its promotion and unloads resident workers; a recreated model cannot inherit
old drafts, checkpoints or promotions.

## Install the local runtime

The project Workbench assistant can read saved Data, Build, Train and Inference
status, including the latest training failure. When asked, it can save supported
native Build edits and Data preparation, start or stop a local run, and test a
saved checkpoint. The Build action handles existing layer settings, removal and
connections; adding layers and imported-model edits still use a proposal for
review. A started run is reported as running until its saved run record says it
completed. Assistant saves reject a model or saved preparation that changed
while the chat turn was in progress. A newer unsaved Build draft stays in the
editor when an assistant save completes.

Build → Code is a generated preview of the current graph. Its heading says
whether it shows the saved graph or an unsaved draft. The project `model.py`
file is a placeholder and is not used by local Train. Train settings, including
batch size, are separate from Build. For supported single-path native graphs,
Train estimates peak training memory against a quarter of currently reclaimable
system memory, reserving space for PyTorch, the dataset, parameters, gradients,
optimizer state and activations. It also applies a separate per-batch CPU work
limit. An oversized start is blocked before writing a failed run. If system
memory cannot be read, Train falls back to the old 8 million element batch cap.
The limit is recalculated when training starts because other apps can change
available memory. The assistant receives the current limit with saved stage
status so it can choose a valid batch when asked to retry.

An assistant training start must produce a completed `workbench.train` tool
result and a saved run record. Workbench replies are checked before display so
an invented run ID or unsupported claim of a new start is not presented as an
action. A successful assistant start opens Train, selects the new run, and
polls its progress until it finishes or fails.

In **Train → Python runtime → Set up runtime**, UMCode scans the project virtual
environment, active Python environments, common system locations, and its managed
environment. Select a ready interpreter or paste an absolute Python executable
path. The selection is shared by Train and Inference and persists across restarts.
If packages are missing, choose **Install in UMCode runtime**. UMCode creates or
updates its isolated environment and installs the fixed dependencies from
`internal/workbench/pytorch/requirements.txt`; it does not change an existing
user environment. This requires network access and may take several minutes.

For a manual install into the default engine home's managed folder:

```sh
python3 -m venv ~/.umcode/runtimes/pytorch
~/.umcode/runtimes/pytorch/bin/python -m pip install -r internal/workbench/pytorch/requirements.txt
```

For a custom `UMCODE_HOME`, use its `runtimes/pytorch/bin/python`. Alternatively set
`UMCODE_WORKBENCH_PYTHON` to the environment's Python executable when starting the
engine. The trusted runner is embedded in the Go binary. It imports torch, numpy,
Pillow and safetensors, never user model code.

The runtime dropdown has no Auto option. llama.cpp, vLLM and SGLang are visible with
compatibility explanations and disabled for these custom state_dict graphs. GGUF and
general pretrained-model loading and remote compute are future work.

## Small trained samples

The developer utility can create synthetic trained weights and CSVs:

```sh
~/.umcode/runtimes/pytorch/bin/python internal/workbench/pytorch/generate_fixtures.py --output /tmp/workbench-samples
```

It creates no app project or seeded models. Its README explains the exact matching
Build settings and Data preparation. The text fixture memorizes eight greetings;
`Hello` at temperature 0 and 64 output tokens produces
`Hello! I am a tiny local model.` The numeric fixture learns y=3x+2; x=14 produces
approximately 44 after saved normalization. These establish inference plumbing,
not general-purpose model quality. Imported weights do not prove training provenance.

## Verification and limits

[Workflow screenshots](screenshots/data-inference/README.md) show the real app and
engine in an isolated project with synthetic examples, empty in-memory credentials,
and a real public Hugging Face repository. No user projects or chats are in the captures.

- Full frontend suite: **288 tests** after review fixes; Svelte check and production build.
- Real Python runners: **25 tests**, including training, held-out metrics, actually trained predictions, malformed
  and unsafe weights, graph/input limits, images, byte generation and IPC recovery.
- Real Go worker integration: forward shapes, matching weights, normalization,
  unload, cancellation and shutdown; focused workbench/engine/server race tests.
- Real browser checks: local upload, persisted preparation, token-free public Hub
  inspection/download, untrained architecture checks, weight import, learned prompt
  response, promotion, and regular chat with no provider key.
- Independent review covered storage, allocation bounds, model incarnation races,
  receipt/promotion pinning, local-only dispatch and lifecycle. The reserved numeric
  column-name crash was reproduced by a failing regression and fixed.

The fresh full repository Go run has two baseline failures:
`TestShellEditAfterPassKeepsWorkOpen` and `TestWorkGetActiveOnly`. Both reproduce on
the unchanged builder checkout and stem from sandboxed Node being unable to read
`/opt/homebrew/etc/openssl@4/openssl.cnf`. The sandbox was not weakened. All other
packages pass. `TestWebSocketTransport` is run separately with the idle local engine
briefly stopped because the test hardcodes its port. The full suite is not claimed green.

Bundle mutations and inference are serialized by a model-operation lock. An unload or
delete can wait for an in-flight request, bounded by the 90-second worker timeout.
Chat Stop cancels the running request directly. The production build reports the
existing Vite chunk-size warning. The local macOS app bundle was rebuilt after
the assistant action changes.
