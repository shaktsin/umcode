# Workbench assistant data work — design

Date: 2026-11-13 · Status: draft for approval · Base: origin/master @ 1c7be0b

## Intent and why

The ML Workbench assistant can today edit the Build graph, re-save Data
preparation config, start/stop training and test checkpoints. It cannot look
at the data itself, import data, or change data beyond the Data tab's fixed
options (columns, task, split percentages, seed). Users want to drive the
whole data stage from chat: "import these files and that Hugging Face
dataset", "what does the data look like across all of them?", "merge them,
drop the bad rows, balance the classes, get it ready for training" — and see
the result in the Data tab as if they had done it by hand.

## Goals

- The assistant can import datasets: local files and Hugging Face repos,
  several in one turn.
- The assistant can describe imported data: rows, columns, types, missing
  values, class distribution, duplicates — across one or many datasets.
- The assistant can transform or generate data by writing a Python script
  that runs locally, sandboxed, after explicit user approval; outputs become
  a normal imported dataset visible in the Data tab with provenance
  (source datasets, the script, a summary of what changed).
- Everything the assistant does lands in the same stores and UI the manual
  flow uses; the user can continue by hand at any point.

## Non-goals

- Scripts producing final train/validation/test artifacts directly. The
  existing prepare pipeline (tokenizer, standardization fitted on train
  rows, splits) stays the only producer of prepared artifacts, because
  Inference must replay the recorded transform; an arbitrary script's
  preprocessing could not be replayed at predict time.
- Network access from scripts (HF downloads go through the existing engine
  path, never through script code).
- GPU, Parquet, dataset scripts, archives — unchanged from current import
  limits.
- Changing the main-chat agent's tooling; this is Workbench-assistant only.

## Anti-goals

- Silently running model-written code. Every script run requires a fresh
  user approval showing the code. "Remember this decision" must NOT apply
  to script approvals (each script is different; a remembered allow would
  rubber-stamp arbitrary future code).
- Breaking the existing security posture: engine never executes user or
  model Python outside the sandbox described here; the trusted
  train/inference runners are unchanged.
- Mutating or deleting existing imported datasets. Scripts read copies and
  write new files only.

## Constraints

- Existing import bounds apply to script outputs: 16 MiB total, 1,000
  files, 100,000 rows, 128 columns per dataset. Script workspace writes are
  capped at 256 MiB before import validation.
- Script runtime: the managed/selected Workbench Python (same selection as
  Train/Inference, `internal/workbench/runtime.go`). numpy/Pillow/torch
  from `requirements.txt` are importable; no new dependencies are added.
  pandas is NOT available — the design must not assume it.
- Sandbox: `internal/sandbox` (Seatbelt on macOS, bwrap on Linux) with
  Readable = {python runtime, workspace}, Root = workspace (writable),
  Network = false, DenyRead = credential dirs. Where `sandbox.Detect()`
  returns nil (no mechanism available), script runs are REFUSED, not run
  unconfined — this is stricter than agent shell commands and deliberate.
  Python runs with `-I -u`, a wall-clock timeout (default 120 s), an
  output-size budget on stdout/stderr (64 KiB kept), and address-space /
  CPU rlimits mirroring the training worker's limits.
- Approval plumbing: `engine.requestApproval` with a new tool name; risk
  "high"; the args shown must include the full script text, the input
  dataset names and the output dataset name.
- Assistant actions run inside the existing Workbench turn context
  (`workbench_assistant_actions.go`); the same guards apply (no delegated
  execution, snapshot revision checks for Build; dataset-version checks for
  data actions, see below).
- Local file import by the assistant reads only paths inside the selected
  project folder (relative paths resolved under the project root via
  `os.Root`-style confinement, consistent with `data_storage.go`).

## Approaches considered

1. **Fixed transform vocabulary** (named operations: merge, balance,
   dedupe, filter…). Safe, previewable, but bounded by what we implement;
   cannot generate data or do novel cleaning. Rejected as primary by the
   user.
2. **Assistant-written Python → new dataset** (chosen). Script runs
   sandboxed on copies of chosen datasets; outputs are re-imported through
   the existing validated import path and appear as a normal dataset with
   provenance. Preparation (tokenize/standardize/split) remains the
   existing pipeline on top.
3. **Assistant-written Python → prepared artifacts directly.** Rejected:
   bypasses validation and breaks the record-and-replay guarantee that
   Inference depends on.

The user chose 2; 1 can still be layered later as sugar over 2 if wanted.

## Design

### New assistant actions (tool specs in `workbenchActionSpecs`)

1. `workbench.import_data`
   - Args: `sources: [{type: "local", path} | {type: "huggingface", repo,
     revision?, files?}]` (1–8 sources).
   - Local paths are project-relative; absolute paths and `..` escapes are
     rejected. Files are read by the engine (not sent from the frontend)
     and fed through the existing import pipeline (`ImportWorkbenchData`),
     one dataset per source, same formats and bounds as the UI.
   - HF reuses the existing `WorkbenchHuggingFace` inspect + import path,
     including the stored token; when `files` is omitted the engine imports
     all supported native files within bounds, else the named ones.
   - Requires approval: one card per tool call, listing all of the call's
     sources (the existing `requestApproval` is per call, so batching the
     sources into one call needs no new approval plumbing); risk "medium".
     Reason: it reads project files / downloads data.
   - Result: per-source dataset ID, name, row count, columns, or a
     per-source error. Partial success is allowed and reported per source.

2. `workbench.inspect_data`
   - Args: `datasetIds: []` (empty = all imported), `sample?: int ≤ 20`.
   - No approval. Returns, per dataset, from stored import metadata plus a
     bounded engine-side scan: row count, columns with inferred types,
     missing-value counts, up to `sample` example rows (cells truncated to
     120 chars), and for a candidate label column the value distribution
     (top 20 values). Also returns cross-dataset column compatibility
     (which datasets share which columns). Response capped at 24 KiB; the
     engine truncates with a note rather than failing.
   - This powers "how does the data look?" without a script round-trip.

3. `workbench.run_data_script`
   - Args: `title` (≤ 80 chars), `inputDatasetIds: []` (0–8; empty is
     allowed: pure generation), `outputName` (dataset name, validated like
     import names), `script` (≤ 32 KiB UTF-8 Python), `expectOutput:
     "dataset" | "report"`.
   - Approval: always, risk "high", never remembered. The approval card
     shows title, the full script, input dataset names, output name, and
     the standing guarantees (sandboxed, no network, reads copies, writes
     one new dataset).
   - Execution: the engine builds a fresh workspace directory:
     `inputs/<dataset-name>/…` (copies of each input dataset's stored
     files, read-only), `out/` (empty, writable). It writes the script to
     `job/script.py` and runs the selected Python `-I -u job/script.py`
     under the sandbox policy above, cwd = workspace. A small preamble is
     NOT injected; the contract is documented to the model in the tool
     description: read from `inputs/`, write CSV/TSV/JSONL/TXT to `out/`,
     print analysis to stdout.
   - On exit 0 with `expectOutput: "dataset"`: files in `out/` are fed to
     the existing `ImportData` path, which already builds one dataset from
     many files (as HF multi-file and image-folder imports do today) with
     the same parse and bound rules; the result is registered as one new
     dataset named `outputName`, with provenance `{createdBy: "assistant",
     script, title, inputDatasetIds, stdoutSummary, createdAt}` stored
     alongside the dataset. With `expectOutput:
     "report"` no dataset is created; stdout is the result.
   - On nonzero exit, timeout, or empty/invalid `out/`: the tool result
     carries stderr/stdout tails (bounded) so the assistant can fix the
     script and ask again — a new approval each time.
   - Tool result to the model: stdout tail (≤ 8 KiB), dataset ID/rows/
     columns when created, duration.

4. `workbench.save_data` (existing) needs no schema change: `DataConfig`
   already carries `DatasetID`, and dataset selection is already
   first-class in the Data tab's dropdown. The assistant targets the
   script-made dataset by setting `config.datasetId`; the tool
   description is updated to say so.

### Status context

The saved-stage status the assistant already receives
(`workbench_assistant_status.go`) is extended with the imported-dataset
list (ID, name, rows, columns, provenance.createdBy) so the assistant
knows what exists without calling inspect.

### Concurrency and staleness

Script runs and imports are serialized per model via the existing data-root
locking in `internal/workbench/data*.go`. Imported datasets are already
immutable and content-addressed (the dataset ID is a digest of the files;
re-importing identical content returns the existing dataset), so inputs
cannot change under a script — they can only disappear (model deletion).
`run_data_script` records input dataset IDs at approval time and refuses
the run if one no longer exists at execution. One script run at a time per
project.

### Frontend

- Data tab: datasets with `provenance.createdBy == "assistant"` get an
  "Assistant" badge; selecting one shows a provenance panel: source
  dataset names, created time, change summary (first stdout lines), and a
  "View script" expander showing the stored script read-only.
- WorkbenchChat: approval cards for the two new approval-bearing tools
  render through the existing approval dock; the script approval shows the
  code in a scrollable monospace block. After a successful run the Data
  tab selection moves to the new dataset (same mechanism as the existing
  "successful assistant start opens Train" behaviour).
- Answer guard (`workbench_answer_guard.go`): claims of a created dataset
  or a finished script run must be backed by a completed tool result, as
  with training starts today.

### Decisions made by the designer (overridable)

- Scripts cannot modify or delete existing datasets; output is always a
  new dataset. Because datasets are content-addressed and immutable,
  re-running with the same `outputName` simply registers another dataset
  entry carrying that display name (or returns the existing one when the
  content is identical); any preparation pinned to the old dataset keeps
  working. No versioning subsystem is added.
- No sandbox mechanism available → script feature is disabled with a clear
  message, not degraded.
- Image datasets are supported as script inputs (files are copies), but
  script outputs are tabular/text only in v1; image-folder outputs are
  future work.
- stdout is treated as untrusted model-visible data; it is never rendered
  as HTML and is truncated at fixed budgets.
- The embedded trusted runners and `model.py` placeholder are untouched.

## Left to the builder

- Exact rlimit values (mirror training worker; pick concrete numbers).
- Workspace lifecycle/cleanup policy (tmp under the engine home, removed
  after registration; keep the script text in the dataset store, not the
  workspace).
- Wire format details of provenance in `internal/workbench/data_storage.go`
  and its schema version bump.
- Tool descriptions/prompt text teaching the model the `inputs/`→`out/`
  contract and the no-pandas constraint.
- Test plan: unit tests per action, server-level tests mirroring
  `workbench_assistant_actions_test.go`, a sandbox integration test that
  proves network and out-of-workspace reads fail, and e2e coverage in
  `workbench-assistant.mjs`.
