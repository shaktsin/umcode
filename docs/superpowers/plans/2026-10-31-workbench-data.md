# Local Data tab implementation plan

Spec: `docs/superpowers/specs/2026-10-31-workbench-data-design.md`.

User asked for immediate local implementation of the reviewed mock and chose local uploads + Hugging Face. All changes stay local; no push/PR/publish. Use the isolated `ml-workbench-data` worktree based on PR #91.

## Global constraints

Use the exact shared JSON types/RPC shapes in the spec. Keep source imports, preparation, and graph edits separate. No credential serialization. 16 MiB/file-set limit; 1,000 files; 100,000 rows; 128 columns. No dataset scripts, code execution, arbitrary URL import, or mocked training success.

## Review focus

- Source/model switching during pending operations cannot replace a newer selection.
- Path traversal, symlinks, and malformed datasets cannot modify data outside this model or corrupt its saved selection.
- Normalization/splitting cannot leak held-out rows into fitted statistics or include the target among inputs.
- HF redirects/auth errors cannot leak a token or download unbounded data.
- A preview/import does not appear saved or graph-compatible until the relevant operation actually succeeds.

### Task 1: Persistent imports and preparation

Files: `internal/workbench/data_types.go`, `data.go`, `data_test.go`.

Interfaces: consumes existing model bundle guards; produces `ImportData`, `ReadData`, `PrepareData` and the shared data types from the spec.

1. Write and run failing behavioral tests for CSV quoting/BOM/TSV/JSONL/text, image-folder labels, deterministic import identity, persistent reopen, invalid/oversized data, target leakage, numeric train-only normalization, deterministic disjoint splits and safe storage paths.
2. Implement the bounded parsers and atomic content-addressed persistence. Run `go test ./internal/workbench` to green.
3. Implement preparation, split artifacts, metadata, and selected-version persistence. Re-run the package suite.

### Task 2: Hugging Face and scoped RPCs

Files: `internal/workbench/huggingface.go`, `huggingface_test.go`, `internal/protocol/workbench_data.go`, `internal/engine/workbench_data.go`, `internal/server/routes.go`, `internal/server/workbench_data_test.go`.

Interfaces: consumes Task 1 public functions/types. Exposes the four RPCs in the spec. Network adapter uses an injectable HTTP client for tests, public fixed HF origins in production, and the engine's existing Secrets store.

1. Write and run failing tests for real request/response processing against a fake HTTP server: search, inspect/resolve SHA, pinned file download, invalid repository/file paths, authentication/authorization errors, redirects and size limits.
2. Implement the client and validated secret-store connection. Avoid token logging and cache URLs with auth data.
3. Add typed RPC bindings and a real engine/project fixture integration test for import/get/prepare/reopen and project isolation. Run targeted Go package/server tests.

### Task 3: Data tab UI and state

Files: `app/frontend/src/lib/workbench/dataTypes.ts`, `data.svelte.ts`, `data.ts`, their tests, `components/workbench/DataTab.svelte`, and `components/Workbench.svelte`.

Interfaces: consumes the four RPCs and exact JSON shapes in the spec; accepts current project/model/task/graph context. Uses `app.call(method, params, 120000)` for imports/searches.

1. Write and run failing tests for context generations, import-versus-save state, compatibility rules, and recommended mapping excluding targets/IDs.
2. Implement state and pure compatibility/mapping helpers.
3. Implement real examples/local upload/HF search+inspect+file selection/account-token controls, actual row preview, mappings/splits/preparation, and persistence notices. Wire into Workbench only for Data; keep the other tabs truthful.
4. Run frontend tests/check/build. Guard against stale completions and unmounts.

### Task 4: Local acceptance and handoff

Files: local acceptance script/report under this plan's scratch directory; local user guide under `docs/ml-workbench/`.

1. Run integrated backend and frontend checks once changes settle; review the diff and address material findings with regression tests.
2. Build the engine; start it with a dedicated local home/port and run Vite against it in this worktree. Create an isolated demo project and model through actual RPCs, never alter existing user models.
3. Test local file import, one real pinned HF example import, mappings/preparation, refresh/reopen persistence, validation errors and navigation. Capture evidence and leave services available for the user.
4. Report the exact local URL and supported limits/formats. Keep changes unpushed; do not change PR #91 or #92.
