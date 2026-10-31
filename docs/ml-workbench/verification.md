# Verification record

The following checks were run on the PR implementation on macOS arm64, using
Chrome for browser interactions and PyTorch 2.15 for generated modules.

| Check | Result |
| --- | --- |
| `npm run check` in `app/frontend` | Zero errors and warnings |
| `npm test` in `app/frontend` | 207 tests passed in 43 files |
| Real-engine visual workbench smoke | Passed all workflows and 18 light/dark layouts; no browser errors |
| `go test -count=1 ./internal/workbench` (part of broad run) | Passed |
| `go test -count=1 -run '^TestWorkbench' ./internal/server` | Passed |
| `go vet ./internal/workbench ./internal/protocol ./internal/engine ./internal/server` | Passed |
| Generated PyTorch modules | Seven imports, forward/backward checks and parameter counts passed |
| `make app-build` | arm64 bundle built and ad-hoc signed |
| `codesign --verify --deep --strict` on the built bundle | Passed |
| `git diff --check` | Passed |

## Reproduce the changed paths

From the repository root:

```sh
go test -count=1 ./internal/workbench
go test -count=1 -run '^TestWorkbench' ./internal/server
go vet ./internal/workbench ./internal/protocol ./internal/engine ./internal/server
cd app/frontend
npm ci
npm run check
npm test
```

For the real-engine UI and generated modules, see the
[browser-test commands](../../app/frontend/e2e/README.md#visual-ml-workbench).
The acceptance script creates/cleans up its own engine home and projects. It
uses a real engine, filesystem, WebSocket transport and Svelte app; it does not
require an LLM provider. Model data in the committed snapshots is test data.

## Generated-module checks

| Module | Input | Output | Actual/inferred parameters |
| --- | --- | --- | ---: |
| Small text | `[2,8]` token IDs | `[2,8,32]` | 9,152 |
| Text classifier | `[2,8]` token IDs | `[2,3]` | 835 |
| Small CNN | `[2,3,32,32]` | `[2,10]` | 5,450 |
| Small vision Transformer | `[2,3,32,32]` | `[2,10]` | 29,418 |
| Feature classifier | `[2,16]` | `[2,3]` | 643 |
| CNN with odd rectangular input/stride | `[2,3,31,29]` | `[2,10]` | 5,450 |
| CNN with average pooling and flattening | `[2,3,32,32]` | `[2,10]` | 87,050 |

The verifier checks finite outputs, training-mode backward gradients, and
causal-prefix independence for the text starter. This establishes runnable
architecture/code generation, not trained-model quality.

## Broader Go suite

The initial `go test -count=1 -timeout=180s ./...` run passed the other packages
but exhausted its three-minute per-package budget in `internal/delegation`,
`internal/engine`, `internal/server` and `internal/store`. The tests running at
the timeout had been active for only 1–11 seconds; the budget applied to their
entire package suite. No assertion failure was reported in that run.

Those four database-heavy packages are being rerun with a longer budget:

```sh
go test -count=1 -timeout=20m ./internal/delegation ./internal/engine ./internal/server ./internal/store
```

Status: extended package verification in progress.

## Limits of this verification

- Browser workflows and native build/signature are verified. The new native
  app was not launched over the existing running UMCode instance, so native
  window/download behavior is not established by these checks.
- Data preparation, training jobs, compute connectors, checkpoint inference,
  evaluations and pretrained weights are outside this implementation and are
  not represented as completed workflows.
- A memory estimate is float32 parameter weights only; these checks do not
  establish runtime capacity, training speed or model quality.
