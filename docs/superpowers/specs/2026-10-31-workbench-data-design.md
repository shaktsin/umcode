# Local Data tab

User direction: implement the approved Data concept locally for testing, with **local uploads and Hugging Face first**. Do not push, publish, or create a PR. Continue from the visual builder and assistant in PR #91; preserve PR #92's hosted mock unchanged. The existing mock and the user's explicit implementation instruction authorize this local build; do not restart design approvals.

## Outcome

Import real CSV/TSV, flat JSONL, plain text, or PNG/JPEG image folders. Preview actual rows, choose input and target fields, choose preparation and deterministic train/validation/test splits, and save an immutable prepared version inside the selected model bundle. Reopening/restarting restores imported data and its saved preparation. No JSON editing.

Public Hugging Face search, repository inspection, file selection, downloads pinned to a commit, and three user-provided example repositories work without a token when public/ungated. Optional read-token connection uses the existing secrets store and returns only account identity/status. Gated/private resources report an actionable access error. This iteration supports native text/CSV/JSONL/image files; Parquet-only, audio, nested annotations, and custom dataset scripts are explicitly unsupported for import. Do not execute dataset code or pretend the other hubs are connected.

## Chosen approach and alternatives

Use Go's standard-library parsers and image metadata decoding, project-scoped RPCs, and Svelte UI. This reuses the existing engine/storage boundary, works without a Python install, and makes real import immediately testable. A Python/Datasets/Arrow environment would expand format support but require runtime installation before the Data tab can work; defer that environment to training/inference. A browser-only importer would lose persistent provenance and shared preparation; do not use it.

## Data model / shared interface

Storage ownership: `internal/workbench/data.go`, `data_types.go`, and tests. Public functions:

```go
func ImportData(projectRoot, modelID string, source DataSource, files []DataFile) (Dataset, error)
func ReadData(projectRoot, modelID, datasetID string) (DataState, error)
func PrepareData(projectRoot, modelID string, config DataConfig) (DataSelection, error)
```

All JSON uses lower camel case. Types:

- `DataFile {name:string, contentBase64:string}` (browser input or backend HF download); paths may describe class folders but must be safe relative paths.
- `DataSource {kind:"local"|"huggingface"|"example", name:string, repo?:string, revision?:string, files?:string[]}`. Never includes credentials.
- `DataColumn {name:string, kind:"number"|"text"|"image", missing:number, uniqueCount:number, examples:string[]}`.
- `Dataset {id:string, name:string, source:DataSource, format:"table"|"text"|"images", rowCount:number, byteCount:number, columns:DataColumn[], preview:Record<string,string>[], createdAt:string}`. ID is content-addressed; repeat imports deduplicate. Preview is capped at 20 rows and 256 characters per cell. `ReadData` returns metadata summaries without previews in `datasets`, plus full selected `dataset`.
- `DataConfig {datasetId:string, mode:"features"|"text"|"image", purpose:"classification"|"text-generation"|"regression", inputColumns:string[], targetColumn:string, scale:"none"|"standard", trainPercent:number, validationPercent:number, testPercent:number, seed:number, stratify:boolean, sequenceLength:number, imageWidth:number, imageHeight:number, imageChannels:number}`. Blank target is allowed for text-generation from a single text column; response target enables prompt+response data. Non-applicable settings may have zero values and are normalized by preparation.
- `DataSelection {version:string, datasetId:string, config:DataConfig, counts:{train:number,validation:number,test:number}, classes:string[], statistics:{column:string,mean:number,std:number}[], createdAt:string, warnings:string[]}`.
- `DataState {datasets:Dataset[], dataset:Dataset|null, selection:DataSelection|null}`.

Persist under `models/<model-id>/data/`: content-addressed imported sources/rows/metadata, immutable prepared versions, and atomic `selection.json`. Numeric normalization is fitted on training rows only, targets are excluded from inputs, class maps are deterministic, and splits are disjoint/reproducible. Prepared artifacts include split JSONL files and their transformation metadata. Text preparation uses an explicit UTF-8 byte vocabulary (PAD=0,BOS=1,EOS=2,byte+3; vocabulary 259); it is never mislabeled as a pretrained tokenizer. Image preparation stores source assets and resize/channel recipe for the eventual training loader. Source files and previous versions stay immutable.

Limits: 16 MiB decoded input per import, 1,000 files, 100,000 rows, 128 columns; reject malformed/empty/ragged data, unsafe paths, nonfinite numeric values, unsupported formats, and archives/custom scripts. Model data paths must remain confined to the project, including symlink replacement attempts. Secrets never go in project files, URLs, RPC results, or audit payloads.

## RPC contract

All data calls below require `projectId` and `modelId`; existing engine project lookup/bundle validation enforces the scope.

- `workbench/data/get {projectId,modelId,datasetId?}` → `DataState`.
- `workbench/data/import {projectId,modelId,source:DataSource,files?:DataFile[]}` → `Dataset`. For HF sources the engine downloads `source.files` itself at the resolved revision. Local uploads send base64 bytes, capped before RPC; no filesystem path supplied by the browser is read.
- `workbench/data/prepare {projectId,modelId,config:DataConfig}` → `DataSelection`.
- `workbench/data/huggingface {projectId,modelId,action:"search"|"inspect"|"status"|"connect"|"disconnect",query?,repo?,secret?}` → for search `{datasets:[{id,downloads,likes,private,gated}]}`; inspect `{id,sha,private,gated,files:[{name,size,supported}],license:string}`; status/connect/disconnect `{connected:boolean,username:string}`. Connect validates a user read token before storing it. Default examples have pinned revisions. Search/inspect/import timeout is at most 90 seconds; the UI permits 120-second RPC waits.

## UI

Keep the approved appearance and the existing project/model navigation. Only show actual supported sources: Examples, Local files, Hugging Face. Explain other hubs are later without fake actionable integrations. Local files begins empty until selection; no retained example table under an empty upload dropzone. Display import errors, current loading stage and size limit, actual rows/columns/missing counts, source revision, imported-dataset picker, mapping controls, split percentages/seed, and Save preparation. Saved preparation is distinct from an imported preview and from model-graph Save.

Compute compatibility from the current graph: input modality, numeric feature count, class output width, image dimensions, byte vocabulary/sequence requirements. Show concrete issues and a Back to Build action, never silently change the graph. Dataset configuration may be saved while incompatible, but never claim it is ready to train. Train and Inference remain clear placeholders.

Use generation/context guards: switching source/project/model or reconnecting cannot apply a stale result to the active selection. An already-started import belongs only to its original explicit project/model. Token inputs are cleared after submission; no localStorage secrets. Errors must preserve existing saved/imported data.

## Verification and local handoff

TDD for parsing/preparation/storage, HF request construction/auth/failure behavior, RPC project isolation, and UI state/compatibility. Use temporary fixtures and fake HTTP at the network boundary. Then perform a real public HF example download, native backend and browser acceptance, persistence reload, and responsive checks. Run the frontend suite/check/build and relevant Go suites; report known unrelated baseline failures if broad checks encounter them. Launch an isolated engine and UI for the user with a dedicated home/project. Preserve their existing models and the prior running app.
