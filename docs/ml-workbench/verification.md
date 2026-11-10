# Verification record

The following checks were run on the PR implementation on macOS arm64, using
Chrome for browser interactions and PyTorch 2.15 for generated modules.

| Check | Result |
| --- | --- |
| `npm run check` in `app/frontend` | Zero errors and warnings |
| `npm test` in `app/frontend` | 239 tests passed in 46 files (including model deletion regressions) |
| Real-engine visual workbench smoke | Passed all workflows and 18 light/dark layouts; no browser errors |
| Real-engine assistant smoke | Passed local HTTP provider transport, context/proposal/apply/failure/navigation workflows and 6 light/dark layouts; no browser errors |
| `go test -count=1 ./internal/workbench` | Passed |
| `go test -count=1 -run '^TestWorkbench' ./internal/server` | Passed |
| Focused assistant engine/server tests | Passed, including coding-task/Work/evidence preservation, pending coding approvals, disabled executable hooks and disabled typed delegation activation |
| Existing normal chat/optimization/context-compiler server regressions | Passed |
| `go vet ./internal/workbench ./internal/protocol ./internal/engine ./internal/server` | Passed |
| Broader Go suite | Not fully green: three existing server-test failures reproduced on the base branch; details below |
| Generated PyTorch modules | Seven imports, forward/backward checks and parameter counts passed |
| `make app-build` | arm64 bundle built and ad-hoc signed |
| `codesign --verify --deep --strict` on the built bundle | Passed |
| `git diff --check` | Passed |

## Reproduce the changed paths

From the repository root:

```sh
go test -count=1 ./internal/workbench
go test -count=1 -run '^TestWorkbench' ./internal/server
go test -count=1 -run '^TestWorkbenchAssistant' ./internal/engine ./internal/server
go vet ./internal/workbench ./internal/protocol ./internal/engine ./internal/server
cd app/frontend
npm ci
npm run check
npm test
```

## Model deletion update

The deletion-only PR update was checked independently from the local Data-tab
work. Frontend tests, Svelte checks and the production build passed; changed
Go packages passed vet. Focused workbench/server integration passed, and
`go test -race ./internal/workbench ./internal/server -run 'Test(DeleteModel|Workbench(Delete|StaleDelete))' -count=1`
passed (1.195s / 24.464s). Storage tests cover recovery bytes, other-model
preservation, reused IDs, missing models, traversal and symlink rejection. RPC
and client tests cover explicit app permission, project isolation, failed-delete
draft preservation, navigation, stale deletion and replacement model drafts.

Real-engine PR-only browser acceptance passed toolbar/card deletion, named
confirmation, cancellation with unsaved edits, saved graph/data/checkpoint
recovery, empty state after final deletion and reload, with no page errors.
The [deletion gallery](screenshots/deletion/README.md) uses the PR's actual app
and engine in isolated fixtures. The visual acceptance script includes these
checks through `e2e/model-deletion.mjs` and expects an empty project to stay on
its model list until New model is clicked.

The existing full-Go-suite limitations below remain documented. A native app
bundle/signature was not rebuilt for this deletion-only update; the native
build results elsewhere in this record describe the earlier builder/assistant
revision. Model deletion does not add Data loading/preparation or training.

For the real-engine UI and generated modules, see the
[browser-test commands](../../app/frontend/e2e/README.md#visual-ml-workbench).
The acceptance script creates/cleans up its own engine home and projects. It
uses a real engine, filesystem, WebSocket transport and Svelte app. The builder
needs no provider; the assistant script starts a local deterministic provider
and exercises the actual OpenAI-compatible HTTP/SSE boundary. Neither requires
user credentials or a paid provider. Model data in the snapshots is test data.

Assistant acceptance covers selected-layer and exact unsaved context, explanatory
answers, settings changes, layer insertion/reconnection, read-only Preview/Cancel,
Apply/Undo/Redo/Save, partial models, stale text/model/project protection, forbidden
tool calls, context/provider failures, cancellation during context preparation
and provider streaming, disabled submission without a model, and history reload.
It tests 760/1180/1440px in both themes. The fake provider establishes UI/runtime
correctness; it does not establish answer quality from an actual LLM.

Independent review found three backend isolation gaps. Each was reproduced by a
failing real-RPC regression and fixed: advisory suggestions now bypass coding
workflow observation/approval paths, executable plugin hooks, and textual skill
delegation admission. Installed-hook and installed-skill positive controls verify
ordinary Chat still supports those features. The full affected Go regression
selection, vet, browser acceptance and native build/signature passed afterward.

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

The broad results below are from the builder verification before assistant
support. The assistant revision was checked with the focused/new Go suites and
existing normal-chat regressions above; the full repository suite was not rerun
for that addition. A green full Go suite is not claimed.

The initial `go test -count=1 -timeout=180s ./...` run passed the other packages
but exhausted its three-minute per-package budget in `internal/delegation`,
`internal/engine`, `internal/server` and `internal/store`. The tests running at
the timeout had been active for only 1–11 seconds; the budget applied to their
entire package suite. No assertion failure was reported in that run.

Those four database-heavy packages were rerun with a longer budget:

```sh
go test -count=1 -timeout=20m ./internal/delegation ./internal/engine ./internal/server ./internal/store
```

`internal/delegation` passed in 332.750s, `internal/engine` in 505.777s and
`internal/store` in 416.327s. `internal/server` finished in 340.031s with three
failures. All three also reproduced on the unchanged base commit
`12f40f1a2c4495f2da47dfafb0b1036117bf41e0` in a separate detached worktree:

- `TestShellEditAfterPassKeepsWorkOpen` and `TestWorkGetActiveOnly`: their
  sandboxed `npm run test` fails before the intended check because Node cannot
  read `/opt/homebrew/etc/openssl@4/openssl.cnf` (`Operation not permitted`).
  The same two failures reproduced on both the base and PR branches.
- `TestOptimizationBundleAcrossChats`: intermittent `retrieval=false` assertion.
  It passed in the first targeted rerun on both branches, then reproduced in a
  five-run baseline check. The five-run PR check passed.

Targeted reproduction commands, run on both the base and PR checkouts:

```sh
go test -count=1 -timeout=3m -run '^(TestOptimizationBundleAcrossChats|TestShellEditAfterPassKeepsWorkOpen|TestWorkGetActiveOnly)$' ./internal/server
go test -count=5 -timeout=3m -run '^TestOptimizationBundleAcrossChats$' ./internal/server
```

No full-suite success is claimed. The existing sandbox and optimization behavior
was left unchanged; workbench-specific storage/RPC, frontend, browser and
generated-module checks passed separately.

## Limits of this verification

- Browser workflows and native build/signature are verified. The rebuilt native
  app was not launched as part of these checks, so native
  window/download behavior is not established by these checks.
- Data preparation, training jobs, compute connectors, checkpoint inference,
  evaluations and pretrained weights are outside this implementation and are
  not represented as completed workflows.
- A memory estimate is float32 parameter weights only; these checks do not
  establish runtime capacity, training speed or model quality.
