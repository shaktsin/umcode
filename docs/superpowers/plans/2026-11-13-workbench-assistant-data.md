# Workbench Assistant Data Work Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let the ML Workbench assistant import datasets (local/Hugging Face), inspect data across datasets, and run user-approved sandboxed Python scripts whose outputs become normal imported datasets with provenance.

**Architecture:** Three new assistant actions (`workbench.import_data`, `workbench.inspect_data`, `workbench.run_data_script`) wired into the existing `runWorkbenchAction` dispatch, with engine-side approval via `requestApproval` for the two side-effecting ones. Scripts run with the selected Workbench Python under `internal/sandbox` (Seatbelt/bwrap) in a throwaway workspace; `out/` files are fed through the existing `ImportData` path and registered with a new provenance record.

**Tech Stack:** Go (engine), Svelte 5 (frontend), existing `internal/sandbox`, existing Workbench Python runtime selection.

**Spec:** `docs/superpowers/specs/2026-11-13-workbench-assistant-data-design.md`

## Global Constraints

- Script runs REQUIRE a detected OS sandbox (`sandbox.Detect() != nil`); otherwise refuse with "Data scripts require the system sandbox (Seatbelt or bubblewrap), which is unavailable."
- Sandbox policy: Network=false; Readable = python runtime dir + workspace; Root (writable) = workspace only; DenyRead unchanged defaults.
- Python runs `-I -u`, wall-clock timeout 120 s, stdout+stderr each kept to 64 KiB (tail), script ≤ 32 KiB, 0–8 input datasets, workspace writes capped at 256 MiB.
- Script outputs pass existing import bounds (16 MiB, 1,000 files, 100,000 rows, 128 columns). Output formats: CSV/TSV/JSONL/TXT only (no images out).
- `run_data_script` approval is ALWAYS requested, risk `tools.RiskRed`, and is NEVER auto-answered by a remembered thread decision.
- `import_data` approval: one card per call (1–8 sources), risk `tools.RiskYellow`. `inspect_data` needs no approval.
- Assistant local imports read only paths inside the selected project folder; absolute paths and `..` escapes rejected.
- Scripts never modify or delete existing datasets; output is always a new dataset.
- No new Python dependencies; tool description must state pandas is unavailable (numpy, Pillow, torch, safetensors are).
- No pattern changes to trusted train/inference runners or `model.py`.

## Review Focus

- A script that exits 0 but writes nothing to `out/` with `expectOutput:"dataset"` → clear error result, no dataset registered (test in Task 3).
- A script whose output exceeds import bounds (rows/bytes/columns) → import validation error surfaced in the tool result, nothing registered (test in Task 3).
- A local import path escaping the project (`../x.csv`, absolute path, symlink out) → rejected before any read (test in Task 2).
- Sandboxed script attempting network or reading outside its workspace → fails (integration test, Task 3).
- Approval denied or expired → tool result is an explicit refusal string, no dataset created, model told not to retry unasked (test in Task 4).

---

### Task 1: Provenance type and script-dataset registration

**Files:**
- Modify: `internal/workbench/data_types.go` (add `DataProvenance`, add `Provenance *DataProvenance` to `Dataset`)
- Modify: `internal/workbench/data.go`
- Test: `internal/workbench/data_provenance_test.go`

**Interfaces:**
- Produces:
  - `type DataProvenance struct { CreatedBy string; Title string; Script string; InputDatasetIDs []string; StdoutSummary string }` (json tags camelCase; `CreatedBy` is `"assistant"`)
  - `func ImportDataWithProvenance(projectRoot, modelID string, source DataSource, files []DataFile, prov *DataProvenance) (Dataset, error)` — identical to `ImportData` but stores `prov` on the dataset's `metadata.json`. `ImportData` becomes a nil-prov wrapper.
  - Existing `readDataset` must round-trip the new optional field; old metadata without it still loads (no schema bump needed — field is optional).

- [ ] **Step 1: Write failing tests** in `data_provenance_test.go`:
  - `TestImportDataWithProvenanceRoundTrip`: import a 3-row CSV with a `DataProvenance{CreatedBy:"assistant", Title:"t", Script:"print(1)", InputDatasetIDs:[]string{"abc"}}`; `ReadData` returns the dataset with that provenance intact.
  - `TestImportDataWithoutProvenanceUnchanged`: plain `ImportData` yields `Provenance == nil` and existing metadata files (fixture without the field) load without error.
- [ ] **Step 2: Run tests, verify FAIL** — `go test ./internal/workbench/ -run Provenance -v` fails: undefined `DataProvenance`.
- [ ] **Step 3: Implement** the type, the field, and `ImportDataWithProvenance` (thread `prov` into `describeData`'s result before `writeNewDataJSON(stage, "metadata.json", ds)`).
- [ ] **Step 4: Run tests, verify PASS**, then `go test ./internal/workbench/` all green.
- [ ] **Step 5: Commit** `feat(workbench): dataset provenance for assistant-made data`

### Task 2: Engine import and inspect actions

**Files:**
- Modify: `internal/engine/workbench_assistant_actions.go` (specs + two handlers + dispatch cases)
- Create: `internal/engine/workbench_assistant_data.go`
- Test: `internal/engine/workbench_assistant_data_test.go`, extend `internal/server/workbench_assistant_actions_test.go`

**Interfaces:**
- Consumes: `workbenchContextFrom(ctx)`, `e.ImportWorkbenchData`, `workbench.ReadData`, `readDataRows` via a new exported `workbench.InspectDatasets`.
- Produces:
  - Tool spec `workbench.import_data`: args `{"sources":[{"type":"local","path":"…"}|{"type":"huggingface","repo":"…","revision":"…","files":["…"]}]}`, 1–8 items, args ≤ 16 KiB.
  - Tool spec `workbench.inspect_data`: args `{"datasetIds":[],"sample":5}`, sample ≤ 20, no approval.
  - `func (e *Engine) workbenchImportDataAction(ctx context.Context, args json.RawMessage) (string, error)` — per-source results JSON `{"sources":[{"ok":true,"datasetId","name","rows","columns"}|{"ok":false,"error"}]}`; partial success allowed.
  - `func (e *Engine) workbenchInspectDataAction(ctx context.Context, args json.RawMessage) (string, error)` — JSON per dataset: rows, columns `{name,kind,missing}`, up to `sample` rows (cells truncated to 120 chars), top-20 value counts for the configured/likely target column, plus `sharedColumns` across requested datasets; whole response truncated to 24 KiB with `"truncated":true`.
  - `func workbench.InspectDatasets(projectRoot, modelID string, ids []string, sample int) (InspectReport, error)` in `internal/workbench/data_inspect.go` (create; include its unit test in this task).
  - Local-path reading: resolve under project root via `os.OpenRoot(project.Root)`; reject absolute paths and any `..` element BEFORE opening.

- [ ] **Step 1: Write failing tests**:
  - `TestWorkbenchImportLocalFile`: project dir with `a.csv`; action imports it; result JSON has `ok:true`, rows match.
  - `TestWorkbenchImportRejectsEscape`: paths `"../x.csv"` and `"/etc/passwd"` → per-source `ok:false` containing "inside the project".
  - `TestWorkbenchImportPartialFailure`: one good file + one missing → one ok, one error, no overall error.
  - `TestWorkbenchInspectData`: two imported datasets sharing column `text` → report has both, `sharedColumns` contains `text`, sample rows truncated at 120 chars.
  - `TestWorkbenchInspectCap`: wide dataset → response ≤ 24 KiB and `truncated:true`.
- [ ] **Step 2: Run, verify FAIL** — `go test ./internal/engine/ ./internal/workbench/ -run 'WorkbenchImport|WorkbenchInspect|InspectDatasets' -v`.
- [ ] **Step 3: Implement** `InspectDatasets`, the two handlers, specs (Description text teaches: local paths are project-relative), and dispatch cases in `runWorkbenchAction`.
- [ ] **Step 4: Run tests, verify PASS**; `go build ./...`.
- [ ] **Step 5: Commit** `feat(workbench): assistant import_data and inspect_data actions`

### Task 3: Sandboxed data-script runner

**Files:**
- Create: `internal/workbench/data_script.go`
- Test: `internal/workbench/data_script_test.go` (unit, fake python) and `internal/workbench/data_script_integration_test.go` (build-tagged or skipped when no python/sandbox, like existing runner integration tests)

**Interfaces:**
- Consumes: `sandbox.Detect()`, `sandbox.Policy`, selected python path (same source as `TrainingManager`; pass it in), `ImportDataWithProvenance` (Task 1).
- Produces:
  - `type DataScriptRequest struct { ProjectRoot, ModelID, Python, Title, OutputName, Script string; InputDatasetIDs []string; ExpectOutput string }` (`ExpectOutput` ∈ `"dataset"|"report"`)
  - `type DataScriptResult struct { Stdout, Stderr string; Dataset *Dataset; DurationMS int64 }`
  - `func RunDataScript(ctx context.Context, req DataScriptRequest) (DataScriptResult, error)`
  - Workspace layout: tmp dir under the engine home → `inputs/<dataset-name>/` (copies of each input dataset's stored raw files, from the dataset store), empty `out/`, `job/script.py`; cwd = workspace; python args `-I -u job/script.py`; sandbox Policy{Root: workspace, Readable: [workspace, dir(python runtime)], Network:false}; rlimits mirroring `training_worker.go`'s; 120 s timeout; workspace removed after registration (script text lives in provenance, not workspace).
  - `errors.New("data scripts require the system sandbox…")` when `sandbox.Detect() == nil`.
  - On `"dataset"`: read `out/` files (CSV/TSV/JSONL/TXT only; anything else → error), base64 into `[]DataFile`, `ImportDataWithProvenance` with `DataSource{Kind:"assistant", Name: req.OutputName}` and provenance from the request + first 1 KiB of stdout as `StdoutSummary`.
- Note: `DataSource.Kind` value `"assistant"` must be accepted by `normalizeDataSource`; extend it.

- [ ] **Step 1: Write failing unit tests** (fake "python" = shell script that the test writes; skip on windows):
  - `TestRunDataScriptRefusesWithoutSandbox` (stub Detect via small seam: `var detectSandbox = sandbox.Detect`): error contains "sandbox".
  - `TestRunDataScriptDatasetRegistered`: fake python copies a fixture CSV into `out/`; result has `Dataset` with provenance `CreatedBy=="assistant"`, source kind `"assistant"`.
  - `TestRunDataScriptEmptyOutFails`: exit 0, empty `out/`, `ExpectOutput:"dataset"` → error "script produced no output files"; no dataset registered.
  - `TestRunDataScriptReportSkipsImport`: `ExpectOutput:"report"` with files in out/ → no dataset, stdout returned.
  - `TestRunDataScriptTimeout`: fake python sleeps; ctx/timeout 1 s (make timeout injectable) → error, stderr/stdout tails present.
  - `TestRunDataScriptOversizedOutput`: out/ CSV with >100,000 rows → import validation error propagated.
  - `TestRunDataScriptMissingInput`: unknown input dataset ID → error before execution.
- [ ] **Step 2: Run, verify FAIL** — `go test ./internal/workbench/ -run DataScript -v`.
- [ ] **Step 3: Implement `RunDataScript`** per the interface block.
- [ ] **Step 4: Write the integration test** (`TestDataScriptSandboxDenies`, skipped unless real python + `sandbox.Detect() != nil`): script tries `urllib.request.urlopen` and `open('/etc/hosts')` → both raise; a plain write to `out/ok.csv` succeeds.
- [ ] **Step 5: Run everything, verify PASS** — `go test ./internal/workbench/ -v -run DataScript`.
- [ ] **Step 6: Commit** `feat(workbench): sandboxed assistant data scripts`

### Task 4: Action wiring, approvals, status, answer guard

**Files:**
- Modify: `internal/engine/workbench_assistant_actions.go` (spec + dispatch for `workbench.run_data_script`; approval calls for it and `import_data`)
- Modify: `internal/engine/approvals.go` (exclude `workbench.run_data_script` from remembered decisions, alongside the computer-use exclusion)
- Modify: `internal/engine/workbench_assistant_status.go` (datasets gain `createdBy` from provenance; list cap stays 5)
- Modify: `internal/engine/workbench_answer_guard.go` (+ its test)
- Test: `internal/engine/workbench_assistant_actions_test.go`, `internal/engine/approvals_test.go`, `internal/server/workbench_assistant_actions_test.go`

**Interfaces:**
- Consumes: `RunDataScript` (Task 3), `e.requestApproval(ctx, sctx, turn, it, name, call.Args, risk, reason, summary)` — call it from inside `runWorkbenchAction` before executing `import_data`/`run_data_script` (the generic turn-loop approval does not cover workbench actions; this is the first workbench action to need one, so thread `sctx`/`turn`/`it` which are already in scope).
- Produces:
  - Tool spec `workbench.run_data_script`: args `{"title","inputDatasetIds":[],"outputName","script","expectOutput"}`; description documents the `inputs/`→`out/` contract, allowed output formats, no network, no pandas, 120 s limit.
  - Approval summary strings: import → `Import N datasets: <names>`; script → `Run data script "<title>": reads <input names or "nothing">, writes dataset "<outputName>"` with the full script in Args (already carried by `a.Args`).
  - Denied approval → tool output string `The user declined this action.` with `IsError:true` and no side effects.
  - Tool result on success: `{"action":"run_data_script","datasetId","rows","columns","durationMs","stdout":<≤8 KiB tail>}`.
  - Answer guard: a reply claiming a created dataset or finished script run without a completed matching tool result is rewritten the same way invented train runs are today.

- [ ] **Step 1: Write failing tests**:
  - `TestRunWorkbenchScriptRequiresApproval`: dispatch with approvals auto-denied → output contains "declined", no dataset in store.
  - `TestRunWorkbenchScriptNeverRemembered`: a remembered "allow" for the tool signature still produces a pending approval (assert `RememberedThreadDecision` is not consulted for this tool).
  - `TestImportActionSingleApprovalCard`: one `import_data` call with 2 sources → exactly one approval recorded.
  - Answer-guard test: assistant text claiming `created dataset reviews-balanced` with no tool result → guarded.
  - Status test: imported dataset with provenance shows `"createdBy":"assistant"` in the stage snapshot.
- [ ] **Step 2: Run, verify FAIL.**
- [ ] **Step 3: Implement** spec, dispatch, approval calls, remembered-decision exclusion, status field, guard clause. Also extend the existing `workbench.save_data` spec description with: "Set config.datasetId to target a specific imported dataset, including assistant-made ones."
- [ ] **Step 4: Run, verify PASS** — `go test ./internal/engine/ ./internal/server/` green.
- [ ] **Step 5: Commit** `feat(workbench): approval-gated assistant data actions`

### Task 5: Frontend — provenance in Data tab, chat flow

**Files:**
- Modify: `app/frontend/src/lib/workbench/dataTypes.ts` (+`provenance?: {createdBy, title, script, inputDatasetIds, stdoutSummary}` on the dataset type)
- Modify: `app/frontend/src/lib/components/workbench/DataTab.svelte`
- Modify: `app/frontend/src/lib/workbench/assistantStore.svelte.ts` (or `assistant.svelte.ts` where tool results are handled — follow the existing `workbench.train` success handling)
- Test: `app/frontend/src/lib/workbench/data.svelte.test.ts`, `app/frontend/src/lib/workbench/assistant.svelte.test.ts`

**Interfaces:**
- Consumes: dataset JSON now carrying `provenance`; tool result shape from Task 4.
- Produces:
  - Data tab: datasets with `provenance?.createdBy === 'assistant'` render an "Assistant" badge in the imported-datasets dropdown row area; selecting one shows a provenance panel: source dataset names (resolved from `inputDatasetIds` against the loaded list, fall back to the raw ID), created time, `stdoutSummary` first lines, and a read-only expandable script block (`<details>`, mono, no execution).
  - Chat: on a completed `workbench.run_data_script` result with a `datasetId`, refresh the data store and select that dataset (mirror the existing "successful assistant start opens Train and selects the run" mechanism).
  - Approval dock already renders workbench approvals generically; verify the script text (in args) renders in a scrollable `<pre>` — add rendering only if the generic card truncates it.

- [ ] **Step 1: Write failing tests**: badge shown for assistant provenance; provenance panel resolves input names; script-result handler selects the new dataset and reloads.
- [ ] **Step 2: Run, verify FAIL** — `npm test -- data.svelte` (per repo's frontend test command in `package.json`).
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run frontend tests, verify PASS**; `npm run check` clean.
- [ ] **Step 5: Commit** `feat(workbench): assistant dataset provenance UI`

### Task 6: E2E and docs

**Files:**
- Modify: `app/frontend/e2e/workbench-assistant.mjs`
- Modify: `docs/ml-workbench/data-inference.md`, `README.md` (one line in the Workbench bullet)

**Interfaces:** Consumes everything above; follows the existing mocked-engine e2e patterns in `workbench-assistant.mjs`.

- [ ] **Step 1: Add e2e scenario**: assistant turn issues `import_data` (approval card appears, approve), `inspect_data`, then `run_data_script` (card shows script, approve); Data tab shows the new dataset with the Assistant badge and provenance panel; a denied script approval leaves no dataset.
- [ ] **Step 2: Run e2e**, verify PASS (repo's e2e command, e.g. `node app/frontend/e2e/workbench-assistant.mjs` per Makefile/package scripts).
- [ ] **Step 3: Update docs**: data-inference.md gains an "Assistant data work" subsection (imports, inspection, approval-gated sandboxed scripts, provenance, bounds); keep the existing security-promise wording accurate (scripts run sandboxed and approved; the trusted runners still never run user model code).
- [ ] **Step 4: Full verification**: `go test ./...`, frontend tests, e2e.
- [ ] **Step 5: Commit** `docs+e2e: assistant data work`
