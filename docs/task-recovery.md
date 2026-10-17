# Task recovery and approval behavior

UMCode saves the current objective, operation results and explicit requirements
per chat. `retry`, `continue`, `resume` and `proceed` restore that context; they
never execute archived commands or grant permissions. `New task:` explicitly
starts a separate objective. Other proposed scope changes use a concrete
confirmation dialog. Historical tasks and approvals remain recorded.

Planning depth and missing graph bookkeeping are advisory by default. Current
project boundaries, tool selection, guards, hooks, chat approval mode and explicit
task requirements still apply. `policy.require_workflow_approval: true` opts into
the reviewed work-graph flow. A real recorded workflow approval satisfies review;
otherwise the engine requests concrete task review bound to the selected solution.
Changing that solution invalidates that review. Token optimization cannot
auto-approve this requirement. A model cannot declare that its own planning
decision constitutes user approval.

`task.propose` can propose continuity, a restrictive requirement or existing
verification evidence. Requirements cite the current user item; their interpreted
scope is confirmed separately from approval to act. An explicit preparation
exception names an exact file via `except_paths`, regardless of directory or
extension. The engine binds that target and checks it again before use; shell and
opaque plugin operations cannot inherit a file exception.

A successful write and a passed check are separate facts. Built-in file readback
checks full bytes, including when the displayed output is clipped. Failed attempts
retain their original status when a verified alternative satisfies their outcome.
Unrelated successful actions do not clear failures. A write without verification
remains unverified; a failed or denied check preserves and reports earlier changes.
Opaque actions without a trusted verification contract remain unverified rather
than receive a fabricated completion claim.

Recoverable failures receive at most three equivalent failed attempts per
unchanged cause in a turn. Unchanged approval and permission blockers stop before
another model call on retry. Several uncertain resource targets produce one
clarification. Interrupted mutations are marked `outcome_unknown`; inspect their
effects before further mutation. No command is automatically replayed on restart.
Legacy task creation imports constraints and relevant result history atomically.
For an opaque interrupted action, task.propose can request explicit acknowledgement
of the inspected state. The dialog shows the original action, inspection reason
and evidence. User acknowledgement permits further work without replay; the
original invocation remains outcome_unknown and is never relabeled successful.

Delegated work inherits the admitted lead task and its current restrictions,
including readonly reviewer scopes. Children cannot approve their own actions or
change lead requirements. A model or compaction change does not reset authority.

Chat shows one correlated primary blocker. Original tool results remain in the
work log and technical details, and unrelated errors remain visible. Old clients
can continue reading the original text error fields.

## Local verification

Use a temporary Go cache and run:

```sh
GOCACHE=/tmp/umcode-go-cache go test ./... -count=1 -timeout=15m
GOCACHE=/tmp/umcode-go-cache go vet ./...
GOCACHE=/tmp/umcode-go-cache go test -race ./internal/engine ./internal/store -run 'TestTask|TestDelegatedTaskRecovery' -count=1 -timeout=10m
npm --prefix app/frontend test
npm --prefix app/frontend run check
npm --prefix app/frontend run build
```

Socket and nested sandbox integration tests need a host environment that allows
temporary Unix sockets and the real Go toolchain. Test providers are scripted;
file effects, SQLite persistence, approvals and socket transport are real. These
checks do not exercise paid live-model behavior.

Build a separate local bundle with:

```sh
UMCODE_APP_OUT="$PWD/bin/UMCode-local-task-recovery.app" GOCACHE=/tmp/umcode-go-cache npm_config_cache=/tmp/umcode-npm-cache app/build/macos/bundle.sh
```

This local bundle does not replace the running app and is not a notarized release.
