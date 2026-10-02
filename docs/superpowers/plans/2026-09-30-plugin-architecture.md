# Unified Plugin Architecture Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make plugins the only user-facing extension unit while supporting portable Agent Plugin, Codex, and Claude packages containing skills, MCP servers, hooks, and visible compatibility metadata.

**Architecture:** Parse every supported package through a format adapter into one normalized `plugins.Package`, install managed packages atomically, and compile the enabled plugins for a project into an immutable reference-counted capability snapshot. Existing skill and MCP runtimes remain the execution owners; a new hook runtime integrates normalized lifecycle events into the engine without allowing plugins to bypass UMCode policy or approvals.

**Tech Stack:** Go 1.24, SQLite migrations, JSON/YAML manifest parsing, existing UMCode JSON-RPC server and tool registry, Svelte 5, TypeScript, Vitest.

**Spec:** `docs/superpowers/specs/2026-09-30-plugin-architecture-design.md`

## Global Constraints

- The preferred public package is a root Agent Plugins `plugin.json`; do not invent a separate UMCode manifest.
- Detection precedence is portable root manifest, Codex compatibility manifest, Claude manifest, then a structured rejection.
- Managed plugin payloads live under `~/.umcode/plugins/cache/<source-id>/<plugin-name>/<version-or-content-digest>/`; mutable data lives under `~/.umcode/plugins/data/<source-id>/<plugin-name>/`.
- `source-id` is the first 16 lowercase hex characters of SHA-256 over the canonical source locator; an absent or path-unsafe version uses the first 16 hex characters of the content digest.
- Installations are user-global; enablement and non-secret settings are per project in SQLite.
- Inspection tokens are random, single-use after success, digest-bound, and expire after 15 minutes.
- Plugin secrets use keys `plugin/<plugin-id>/<setting-name>` in the existing `secrets.Store`; secrets never enter SQLite, diagnostics, audit details, or model context.
- Hook envelope/result version is `1`; arguments/output/error are limited to 64 KiB each, block reason to 4 KiB, warning to 8 KiB, context to 32 KiB, stdout to 256 KiB, stderr to 64 KiB.
- Hook timeout defaults to 10 seconds and cannot exceed 60 seconds; observational hook concurrency is limited to four processes per event.
- Only `TurnStart` and `BeforeToolUse` hooks can block. No hook may bypass a built-in guard, policy denial, or approval requirement.
- A turn retains one immutable plugin snapshot until completion; activation failure preserves the prior snapshot.
- Built-in tools stay outside plugins. Legacy standalone skills and configured MCP servers remain backend-compatible but disappear from the desktop Plugins screen.
- No third-party install-time scripts are executed.
- New filesystem writes must reject absolute paths, `..` traversal, and symlink escapes from the package root.

## Review Focus

- A local or Git source that changes after inspection must be rejected by `plugin/install`, not install different bytes; pinned by `TestInstallRejectsChangedSourceAfterInspection` in Task 3.
- Absolute paths, `..`, and symlink escapes must be rejected before any executable component starts; pinned by `TestLoadPackageRejectsEscapingResources` in Task 1 and `TestInstallRejectsSymlinkEscape` in Task 3.
- A malicious `BeforeToolUse` hook must not convert a policy denial or required approval into an allowed call; pinned by `TestPluginHookCannotBypassToolPolicy` in Task 6.
- Enabling, disabling, reloading, or updating a plugin during a turn must not mutate that turn's tools, skills, or hooks; pinned by `TestTurnRetainsPluginSnapshotAcrossReload` in Task 6.
- A failed staged update or required component activation must leave the prior cached version and capability snapshot usable, with spawned MCP processes closed; pinned by `TestActivationFailureRollsBackAndClosesProcesses` in Task 5.

---

## File Structure

New backend units:

- `internal/plugins/model.go` — normalized package, components, diagnostics, source metadata, health.
- `internal/plugins/adapter.go` — detection order and adapter interface.
- `internal/plugins/adapter_agent.go` — portable Agent Plugins parser and Codex overlay merge.
- `internal/plugins/adapter_codex.go` — legacy `.codex-plugin/plugin.json` parser.
- `internal/plugins/adapter_claude.go` — `.claude-plugin/plugin.json` parser and Claude event mapping.
- `internal/plugins/validate.go` — names, schemas, containment, symlinks, duplicates, secrets, required unsupported components.
- `internal/plugins/source.go` — canonical local/Git source materialization and content digest.
- `internal/plugins/installer.go` — inspection tokens, managed/linked install, staging, atomic activation, rollback.
- `internal/plugins/manager.go` — global installation operations and project snapshot cache.
- `internal/plugins/snapshot.go` — immutable reference-counted skills/MCP/hooks capability snapshot.
- `internal/hooks/types.go` — normalized declarations, event envelope, result, limits.
- `internal/hooks/runner.go` — matching, subprocess execution, limits, ordering, outcomes.
- `internal/store/plugins.go` — installed plugin, project enablement/settings, and hook-run persistence.
- `internal/protocol/plugins.go` — plugin RPC request/response contracts.

Modified backend units:

- `internal/skills/skills.go`, `internal/skills/tools.go`, `internal/skills/runtime.go` — context-bound plugin skill snapshots while retaining legacy roots.
- `internal/mcp/manager.go`, `internal/mcp/transport.go` — plugin-owned process metadata and deterministic close verification.
- `internal/engine/engine.go`, `internal/engine/turn.go`, `internal/engine/compaction.go` — snapshot acquisition and hook events.
- `internal/server/routes.go`, `cmd/umcode/main.go`, `cmd/umcode/extensions.go` — plugin RPC and CLI.

Frontend units:

- `app/frontend/src/lib/plugins.ts` — typed plugin RPC client and view grouping.
- `app/frontend/src/lib/plugins.test.ts` — inspect/install flow and presentation contract.
- `app/frontend/src/lib/components/PluginInventory.svelte` — nested plugin components and diagnostics.
- `app/frontend/src/lib/panels/Extensions.svelte` — plugin-only installation and management screen.
- `app/frontend/src/lib/panels/Extensions.test.ts` — source/render contract excluding standalone sections.
- `app/frontend/src/lib/types.ts` — plugin protocol types.

## Task 1: Normalize Portable, Codex, and Claude Packages

**Files:**
- Create: `internal/hooks/types.go`
- Create: `internal/plugins/model.go`
- Create: `internal/plugins/adapter.go`
- Create: `internal/plugins/adapter_agent.go`
- Create: `internal/plugins/adapter_codex.go`
- Create: `internal/plugins/adapter_claude.go`
- Create: `internal/plugins/validate.go`
- Create: `internal/plugins/testdata/portable/plugin.json`
- Create: `internal/plugins/testdata/portable/skills/greet/SKILL.md`
- Create: `internal/plugins/testdata/portable/mcp.json`
- Create: `internal/plugins/testdata/portable/hooks/hooks.json`
- Create: `internal/plugins/testdata/codex/.codex-plugin/plugin.json`
- Create: `internal/plugins/testdata/claude/.claude-plugin/plugin.json`
- Create: `internal/plugins/adapters_test.go`

**Interfaces:**
- Produces: `plugins.LoadPackage(root string) (plugins.Package, error)`.
- Produces: `plugins.ValidatePackage(pkg plugins.Package) []plugins.Diagnostic`.
- Produces: structured `plugins.Error` values so later layers preserve diagnostic fields without parsing error strings.
- Produces: `plugins.Package` with `ID`, `Name`, `Version`, `Format`, `Root`, `Skills`, `MCPServers`, `Hooks`, `Components`, `Settings`, and `Diagnostics`.
- Produces: normalized `hooks.Declaration` and `hooks.Event` values used by Tasks 4-6.

- [ ] **Step 1: Write adapter contract and containment tests**

Add tests named `TestAdaptersNormalizeEquivalentCapabilities`, `TestPortableManifestWinsOverCodexOverlayIdentity`, `TestUnsupportedComponentsRemainVisible`, and `TestLoadPackageRejectsEscapingResources`. Assert all three valid fixtures normalize the same skill, MCP server, and hook; Claude agents remain an unsupported component; portable identity wins over its overlay; and absolute, `..`, and escaping-symlink resources return a `validate/path_escape` diagnostic.

- [ ] **Step 2: Run the adapter tests and verify RED**

Run: `go test ./internal/plugins -run 'TestAdapters|TestPortable|TestUnsupported|TestLoadPackage' -count=1`

Expected: FAIL because `internal/plugins` and `LoadPackage` do not exist.

- [ ] **Step 3: Define normalized types**

Implement these exact public shapes in `internal/plugins/model.go` and `internal/hooks/types.go`:

```go
type Format string
const (FormatAgent Format = "agent"; FormatCodex Format = "codex"; FormatClaude Format = "claude")
type Severity string
type Diagnostic struct { Code string; Phase string; Severity Severity; Component string; Message string; Remediation string }
type Error struct { Diagnostic Diagnostic; Cause error }
func (e *Error) Error() string
func (e *Error) Unwrap() error
type Package struct { ID string; Name string; Version string; Format Format; Root string; Skills []SkillComponent; MCPServers []MCPComponent; Hooks []hooks.Declaration; Components []Component; Settings []Setting; Diagnostics []Diagnostic }
type Adapter interface { Format() Format; Detect(root string) (bool, error); Load(root string) (Package, error) }
func LoadPackage(root string) (Package, error)
func ValidatePackage(pkg Package) []Diagnostic
```

Use `config.MCPServerConfig` inside `MCPComponent`. Use package-relative resource paths in adapter output and resolve them only through one containment helper in `validate.go`.

- [ ] **Step 4: Implement the three adapters**

Portable packages discover `skills/` and `mcp.json`, then merge only supported `extensions.com.openai` or `.codex-plugin/plugin.json` overlay fields. Codex packages accept manifest-declared skill roots, MCP path/object declarations, and hook path/inline declarations. Claude packages discover conventional `skills/`, `.mcp.json`, and `hooks/hooks.json`, map supported hook events, and inventory agents/LSP/commands as supported metadata or unsupported components according to the spec.

- [ ] **Step 5: Run adapter tests and the existing parsers**

Run: `go test ./internal/plugins ./internal/skills ./internal/mcp -count=1`

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/hooks/types.go internal/plugins
git commit -m "feat: normalize portable codex and claude plugins"
```

## Task 2: Persist Installations, Project Enablement, and Hook Audit

**Files:**
- Create: `internal/store/migrations/0011_plugins.sql`
- Create: `internal/store/plugins.go`
- Create: `internal/store/plugins_test.go`
- Modify: `internal/store/projects.go`

**Interfaces:**
- Consumes: normalized string identity and diagnostic JSON from Task 1; persistence does not import `internal/plugins`.
- Produces: `store.PluginInstallation`, `store.ProjectPlugin`, and `store.PluginHookRun`.
- Produces: CRUD methods consumed by Tasks 3, 5, and 7.

- [ ] **Step 1: Write migration and persistence tests**

Add `TestPluginInstallationPersistence`, `TestProjectPluginEnablementAndSettings`, `TestDeleteProjectCascadesPluginEnablement`, and `TestPluginHookRunPersistence`. Assert installation provenance survives reopening a file-backed database, settings round-trip without secrets, deleting a project cascades its enablement, and hook records filter by plugin/project/turn.

- [ ] **Step 2: Run store tests and verify RED**

Run: `go test ./internal/store -run 'TestPlugin' -count=1`

Expected: FAIL because migration `0011_plugins.sql` and plugin store methods do not exist.

- [ ] **Step 3: Add the plugin schema**

Create tables `plugin_installations`, `project_plugins`, and `plugin_hook_runs`. Use `plugin_installations.id` as the stable `source-id/plugin-name` key and `project_plugins(project_id, plugin_id)` as the composite primary key. Cascade project deletion and installation deletion into `project_plugins`; the uninstall service checks for enabled projects before deleting the installation. Keep `plugin_hook_runs.plugin_id` as indexed historical identity without a deleting foreign key so audit rows survive uninstall.

- [ ] **Step 4: Implement store interfaces**

Add exact methods:

```go
func (s *Store) UpsertPluginInstallation(ctx context.Context, p PluginInstallation) error
func (s *Store) GetPluginInstallation(ctx context.Context, id string) (PluginInstallation, error)
func (s *Store) ListPluginInstallations(ctx context.Context) ([]PluginInstallation, error)
func (s *Store) DeletePluginInstallation(ctx context.Context, id string) error
func (s *Store) SetProjectPlugin(ctx context.Context, p ProjectPlugin) error
func (s *Store) ProjectPlugin(ctx context.Context, projectID, pluginID string) (ProjectPlugin, error)
func (s *Store) ListProjectPlugins(ctx context.Context, projectID string) ([]ProjectPlugin, error)
func (s *Store) ProjectsUsingPlugin(ctx context.Context, pluginID string) ([]string, error)
func (s *Store) RecordPluginHookRun(ctx context.Context, run PluginHookRun) error
func (s *Store) ListPluginHookRuns(ctx context.Context, pluginID, projectID, turnID string, limit int) ([]PluginHookRun, error)
```

- [ ] **Step 5: Run store tests and full migration tests**

Run: `go test ./internal/store -count=1`

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/store/migrations/0011_plugins.sql internal/store/plugins.go internal/store/plugins_test.go internal/store/projects.go
git commit -m "feat: persist plugin installations and enablement"
```

## Task 3: Inspect and Install Plugins Atomically

**Files:**
- Create: `internal/plugins/source.go`
- Create: `internal/plugins/installer.go`
- Create: `internal/plugins/installer_test.go`

**Interfaces:**
- Consumes: `LoadPackage`, `ValidatePackage`, and Task 2 store methods.
- Produces: `plugins.NewInstaller(home string, st *store.Store, now func() time.Time) *Installer`.
- Produces: `(*Installer).Inspect(ctx context.Context, req InspectRequest) (Inspection, error)`.
- Produces: `(*Installer).Install(ctx context.Context, token, projectID string) (store.PluginInstallation, error)`.
- Produces: `(*Installer).Reload(ctx context.Context, pluginID string) (store.PluginInstallation, error)` and `(*Installer).Uninstall(ctx context.Context, pluginID string, disableProjects bool) error`.

- [ ] **Step 1: Write inspection and atomic-store tests**

Add `TestInspectDoesNotMutate`, `TestManagedInstallCopiesAndSurvivesSourceRemoval`, `TestInstallRejectsChangedSourceAfterInspection`, `TestInstallRejectsSymlinkEscape`, `TestFailedActivationPreservesPreviousVersion`, `TestInspectionTokenExpiresAndIsSingleUseAfterSuccess`, `TestLinkedReloadKeepsPreviousPackageOnValidationFailure`, and `TestUninstallRefusesEnabledProjects`.

- [ ] **Step 2: Run installer tests and verify RED**

Run: `go test ./internal/plugins -run 'TestInspect|TestManaged|TestInstall|TestFailedActivation|TestInspection|TestLinked|TestUninstall' -count=1`

Expected: FAIL because `Installer` is undefined.

- [ ] **Step 3: Implement source materialization and digesting**

Define:

```go
type InstallMode string
const (InstallManaged InstallMode = "managed"; InstallLinked InstallMode = "linked")
type InspectRequest struct { Source string; Mode InstallMode }
type Inspection struct { Token string; ExpiresAt time.Time; Source string; SourceID string; Digest string; Package Package; RequiresApproval bool }
```

Canonicalize local paths with `filepath.EvalSymlinks` and canonicalize Git URLs without credentials. Clone Git sources with `git clone --depth 1 --` into a temp directory. Hash sorted relative paths, modes, and file bytes while excluding `.git`, `.venv`, `node_modules`, and plugin cache/data folders.

- [ ] **Step 4: Implement managed staging and linked records**

Managed install must copy into a temporary sibling directory, reject symlinks, re-run `LoadPackage` and digest verification inside staging, then rename into the version path before updating SQLite. On update failure, keep the old installation row and root. Linked install records the canonical source root and reloads through the same validation path without copying.

- [ ] **Step 5: Run installer and store tests**

Run: `go test ./internal/plugins ./internal/store -count=1`

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/plugins/source.go internal/plugins/installer.go internal/plugins/installer_test.go
git commit -m "feat: inspect and install plugins atomically"
```

## Task 4: Execute Normalized Hooks Safely

**Files:**
- Create: `internal/hooks/runner.go`
- Create: `internal/hooks/runner_test.go`
- Modify: `internal/procutil` files only if a reusable bounded-process helper is required.

**Interfaces:**
- Consumes: `hooks.Declaration` and limits from Task 1.
- Produces: `hooks.NewRunner(recorder Recorder) *Runner`.
- Produces: `(*Runner).Run(ctx context.Context, set Set, invocation Invocation) Outcome`.
- Produces: `hooks.Set`, `hooks.Invocation`, `hooks.Outcome`, and `hooks.Record` used by Tasks 5-6.

- [ ] **Step 1: Write hook contract tests**

Add `TestHookEnvelopeVersionAndBounds`, `TestBlockingHooksRunDeterministically`, `TestObservationalHooksRespectConcurrencyLimit`, `TestRequiredBeforeHookFailsClosed`, `TestOptionalBeforeHookWarnsAndContinues`, `TestAfterHookFailureCannotChangeResult`, `TestHookRejectsOversizedAndMalformedResult`, and `TestHookEnvironmentDoesNotLeakEngineSecrets`.

- [ ] **Step 2: Run hook tests and verify RED**

Run: `go test ./internal/hooks -count=1`

Expected: FAIL because `Runner` is undefined.

- [ ] **Step 3: Implement the runner contract**

Use these signatures:

```go
type Recorder func(context.Context, Record) error
type Invocation struct { Version int; Event Event; PluginID string; PluginVersion string; ProjectID string; ProjectRoot string; ThreadID string; TurnID string; ToolName string; ToolArgs json.RawMessage; ToolOutput string; ToolError string; At time.Time }
type Outcome struct { Blocked bool; Reason string; Context []string; Warnings []string; Records []Record }
type Set struct { Declarations []Declaration }
func NewRunner(recorder Recorder) *Runner
func (r *Runner) Run(ctx context.Context, set Set, invocation Invocation) Outcome
```

Build a minimal environment from the same safe base keys used by skills and MCP. Resolve executable paths inside the plugin root, use `procutil.Prepare`, apply timeout/output caps, validate version `1` JSON, and redact configured secret values before recording outcomes.

- [ ] **Step 4: Run hook tests with the race detector**

Run: `go test -race ./internal/hooks -count=1`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/hooks internal/procutil
git commit -m "feat: run plugin hooks with bounded semantics"
```

## Task 5: Compile Immutable Project Capability Snapshots

**Files:**
- Create: `internal/plugins/snapshot.go`
- Create: `internal/plugins/manager.go`
- Create: `internal/plugins/manager_test.go`
- Modify: `internal/skills/skills.go`
- Modify: `internal/skills/tools.go`
- Modify: `internal/skills/runtime.go`
- Modify: `internal/skills/skills_test.go`
- Modify: `internal/mcp/manager.go`
- Modify: `internal/mcp/mcp_test.go`

**Interfaces:**
- Consumes: installed packages and project enablement from Tasks 2-3, hook runner from Task 4.
- Produces: `plugins.NewManager(plugins.Options) (*plugins.Manager, error)`.
- Produces: `(*plugins.Manager).Acquire(ctx context.Context, projectID string) (*plugins.Snapshot, error)` and `(*plugins.Manager).Invalidate(projectID string)`.
- Produces: snapshot methods `Tools() []tools.Tool`, `Tool(name string) (tools.Tool, bool)`, `SkillSnapshot() *skills.Snapshot`, `Hooks() hooks.Set`, and `Release()`.

- [ ] **Step 1: Write skill overlay and snapshot lifecycle tests**

Add `TestPluginSkillsAreNamespacedAndContextBound`, `TestSnapshotIncludesNamespacedMCPTools`, `TestSnapshotReferenceKeepsOldVersionAlive`, `TestDisableAffectsOnlyNewSnapshots`, and `TestActivationFailureRollsBackAndClosesProcesses`. The failure test must use a required fake MCP server, assert the prior tool still works, and assert the failed process exits.

- [ ] **Step 2: Run focused tests and verify RED**

Run: `go test ./internal/plugins ./internal/skills ./internal/mcp -run 'TestPluginSkills|TestSnapshot|TestDisable|TestActivation' -count=1`

Expected: FAIL because snapshot APIs do not exist.

- [ ] **Step 3: Add context-bound skill snapshots**

Implement:

```go
type skills.Root struct { PluginID string; Paths []string; Config map[string]any }
type skills.Snapshot struct { /* immutable compiled skills */ }
func CompileSnapshot(roots []Root) (*Snapshot, []error)
func WithSnapshot(ctx context.Context, snapshot *Snapshot) context.Context
```

Change `Registry.Catalog`, `Registry.Match`, `Registry.Get`, instruction-tool lookup, `RunScript`, and `EnvFor` to union legacy registry skills with the snapshot found in context. Plugin skill names are `<plugin-name>:<authored-skill-name>`; validate the authored name before applying the namespace. Legacy call sites without a snapshot retain current behavior.

- [ ] **Step 4: Implement reference-counted plugin snapshots**

`Manager` loads enabled packages, compiles skill roots, prefixes MCP server names as `<plugin-name>__<server-name>`, starts a dedicated `mcp.Manager` for the snapshot, compiles hooks, and publishes the snapshot only after required components are healthy. Cache snapshots by project plus the ordered `(plugin ID, digest, settings)` generation. Retired snapshots close MCP managers only when their reference count reaches zero.

- [ ] **Step 5: Run focused and package tests**

Run: `go test -race ./internal/plugins ./internal/skills ./internal/mcp -count=1`

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/plugins/manager.go internal/plugins/manager_test.go internal/plugins/snapshot.go internal/skills internal/mcp
git commit -m "feat: compile project plugin capability snapshots"
```

## Task 6: Integrate Plugin Snapshots and Hooks into the Engine

**Files:**
- Modify: `internal/engine/engine.go`
- Modify: `internal/engine/turn.go`
- Modify: `internal/engine/compaction.go`
- Create: `internal/engine/plugin_hooks_test.go`
- Modify: `internal/engine/context_test.go`
- Modify: `internal/engine/tool_allowed_test.go`

**Interfaces:**
- Consumes: Task 5 `Manager.Acquire` and `Snapshot` APIs and Task 4 hook runner outcomes.
- Produces: engine turns whose system prompt, tool list, tool dispatch, and hooks use one acquired snapshot.
- Produces: `toolRunResult{Output string; IsError bool; HookContext []string}` as the return type of plugin-aware tool execution.

- [ ] **Step 1: Write engine lifecycle and policy tests**

Add `TestTurnStartHookContextReachesModel`, `TestBeforeToolHookCanBlock`, `TestAfterToolAndFailureHooksObserveFinalOutcome`, `TestPluginHookCannotBypassToolPolicy`, `TestTurnCompleteHookRunsOnFailureAndSuccess`, `TestCompactionHooksRunForManualAndAutomaticCompaction`, and `TestTurnRetainsPluginSnapshotAcrossReload`.

- [ ] **Step 2: Run engine tests and verify RED**

Run: `go test ./internal/engine -run 'TestTurnStartHook|TestBeforeToolHook|TestAfterTool|TestPluginHook|TestTurnCompleteHook|TestCompactionHooks|TestTurnRetains' -count=1`

Expected: FAIL because `Engine` has no plugin manager or hook integration.

- [ ] **Step 3: Wire manager lifecycle into `Engine`**

Add `Plugins *plugins.Manager` and `Hooks *hooks.Runner` to `Engine`. Construct them in `New` with `Config.Home`, `Store`, `Secrets`, logger, and existing legacy registries. Close the plugin manager during `Shutdown` after active turns release snapshots.

- [ ] **Step 4: Acquire and use one snapshot per turn**

Acquire after the project workspace and tool scope are resolved, attach its skill snapshot to `ctx` and `sctx`, merge snapshot tools with `e.Tools.All()` when building model specs, resolve tool calls against the snapshot before returning unknown-tool errors, and release exactly once after final hook delivery.

- [ ] **Step 5: Add hook event boundaries**

Run `TurnStart` before the first model request, `BeforeToolUse` after built-in `Guard.Forbidden` and before `gate.Check`, `AfterToolUse` or `ToolUseFailed` after persistence of the tool item, and `TurnComplete` before snapshot release. Carry returned context into subsequent `systemPrompt` calls. Wrap both manual and automatic `compactLocked` calls with `BeforeCompaction` and `AfterCompaction` using the relevant project snapshot.

- [ ] **Step 6: Run engine and policy suites**

Run: `go test -race ./internal/engine ./internal/policy ./internal/tools -count=1`

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/engine
git commit -m "feat: run plugin capabilities and hooks in turns"
```

## Task 7: Expose Plugin RPC and CLI Operations

**Files:**
- Create: `internal/protocol/plugins.go`
- Modify: `internal/protocol/tasks.go`
- Modify: `internal/server/routes.go`
- Create: `internal/server/plugins_test.go`
- Modify: `cmd/umcode/main.go`
- Modify: `cmd/umcode/extensions.go`

**Interfaces:**
- Consumes: Task 3 installer and Task 5 manager invalidation/listing.
- Produces: `plugin/inspect`, `plugin/install`, `plugin/list`, `plugin/get`, `plugin/setEnabled`, `plugin/configure`, `plugin/reload`, and `plugin/uninstall`.
- Produces: typed protocol models consumed by Task 8.

- [ ] **Step 1: Write RPC contract tests**

Add `TestPluginInspectInstallListGet`, `TestPluginConfigureStoresSecretsOutsideDatabase`, `TestPluginSetEnabledInvalidatesProjectSnapshot`, `TestPluginReloadLinkedOnly`, `TestPluginUninstallRequiresExplicitProjectDisable`, and `TestPluginErrorsContainPhaseCodeAndRemediation`.

- [ ] **Step 2: Run server tests and verify RED**

Run: `go test ./internal/server -run 'TestPlugin' -count=1`

Expected: FAIL because plugin protocol methods are undefined.

- [ ] **Step 3: Define protocol contracts**

Create types `PluginInfo`, `PluginComponentInfo`, `PluginDiagnostic`, `PluginInspection`, and parameter/result structs for every method. `PluginConfigureParams` contains `Settings map[string]any` and write-only `Secrets map[string]string`; no response type includes secret values. Error data includes `phase`, `code`, `pluginId`, `component`, and `remediation`.

- [ ] **Step 4: Implement routes and audit events**

Bind all eight methods. After enable/configure/reload/install/uninstall, call `Plugins.Invalidate` for affected projects. Audit operations with source locators stripped of credentials. Keep existing `skill/*` and `mcp/*` routes unchanged but do not call them from new code.

- [ ] **Step 5: Add plugin CLI commands**

Add `umcode plugin inspect SOURCE`, `install SOURCE [--linked] [--project ID]`, `list [--project ID]`, `show ID`, `enable ID --project ID`, `disable ID --project ID`, `reload ID`, and `remove ID [--disable-projects]`. Preserve legacy `skill` and `mcp` commands during the deprecation window.

- [ ] **Step 6: Run protocol, server, and CLI build checks**

Run: `go test ./internal/protocol ./internal/server ./cmd/umcode -count=1 && go build ./cmd/umcode`

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/protocol/plugins.go internal/protocol/tasks.go internal/server/routes.go internal/server/plugins_test.go cmd/umcode
git commit -m "feat: expose plugin management API and CLI"
```

## Task 8: Replace the Extensions UI with Plugin-Only Management

**Files:**
- Create: `app/frontend/src/lib/plugins.ts`
- Create: `app/frontend/src/lib/plugins.test.ts`
- Create: `app/frontend/src/lib/components/PluginInventory.svelte`
- Modify: `app/frontend/src/lib/panels/Extensions.svelte`
- Create: `app/frontend/src/lib/panels/Extensions.test.ts`
- Modify: `app/frontend/src/lib/types.ts`

**Interfaces:**
- Consumes: Task 7 plugin RPC JSON shapes.
- Produces: `createPluginClient(call)` with typed `inspect`, `install`, `list`, `get`, `setEnabled`, `configure`, `reload`, and `uninstall` methods.
- Produces: a plugin-only screen with nested capability inventory.

- [ ] **Step 1: Write frontend contract tests**

In `plugins.test.ts`, inject a fake RPC call and assert inspect precedes install, install uses the inspection token, secret fields never appear in returned view data, and every method starts with `plugin/`. In `Extensions.test.ts`, import `Extensions.svelte?raw` and assert it contains plugin inspection/install controls while excluding calls to `skill/list`, `skill/install`, `mcp/list`, `mcp/restart`, and `tool/list`, and excluding standalone headings `Skills`, `MCP servers`, `Built-in plugins`, and `Tools available to the agent`.

- [ ] **Step 2: Run Vitest and verify RED**

Run: `cd app/frontend && npm test -- --run src/lib/plugins.test.ts src/lib/panels/Extensions.test.ts`

Expected: FAIL because the plugin client does not exist and the current panel contains legacy sections.

- [ ] **Step 3: Add TypeScript protocol and client models**

Mirror Task 7 JSON fields exactly. `createPluginClient` accepts `(method: string, params?: unknown) => Promise<unknown>` so tests do not depend on the global app store. Add pure grouping helpers that return skills, MCP servers, hooks, and unsupported capabilities nested under one plugin.

- [ ] **Step 4: Build the inspect-before-install UI**

Replace `Extensions.svelte` with source input, managed/linked mode choice, Inspect action, review state, explicit Install action, current-project enable toggles, configure/reload/uninstall actions, aggregate health, and `PluginInventory`. Render compatibility diagnostics and hook failures within the owning plugin card/detail. Remove built-in, standalone skill/MCP, and generic tool sections completely.

- [ ] **Step 5: Run frontend tests and type checks**

Run: `cd app/frontend && npm test && npm run check && npm run build`

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add app/frontend/src/lib/plugins.ts app/frontend/src/lib/plugins.test.ts app/frontend/src/lib/components/PluginInventory.svelte app/frontend/src/lib/panels/Extensions.svelte app/frontend/src/lib/panels/Extensions.test.ts app/frontend/src/lib/types.ts
git commit -m "feat: make the extensions UI plugin only"
```

## Task 9: Prove Installation and Runtime Integration End to End

**Files:**
- Create: `internal/server/plugin_fixture_test.go`
- Create: `internal/server/plugin_e2e_test.go`
- Modify: `internal/server/e2e_test.go`
- Modify: `internal/mcp/mcp_test.go` only if the shared fake MCP helper is extracted.

**Interfaces:**
- Consumes: all prior task interfaces through public JSON-RPC and the normal turn loop.
- Produces: an E2E fixture process mode for a skill helper, stdio MCP server, and hook helper inside the server test binary.

- [ ] **Step 1: Add the portable full-flow E2E test**

Create `TestPluginEndToEndInstallUseDisableUninstall`. Generate a portable plugin whose skill wrapper, MCP declaration, and hooks invoke the test binary through explicit fixture modes. Through the socket client: inspect; install; enable/configure; remove the source directory; start a turn; assert hook context is in the first model request; execute `<plugin>:<skill>`; invoke the namespaced MCP tool; verify before/after hook audit rows and completed tool items; disable; assert the next turn lacks plugin capabilities; uninstall; assert cached code is removed and data is retained.

- [ ] **Step 2: Run the portable E2E and verify RED**

Run: `go test ./internal/server -run TestPluginEndToEndInstallUseDisableUninstall -count=1 -v`

Expected: FAIL at the first incomplete integration boundary exposed by the test.

- [ ] **Step 3: Complete fixture helpers and integration gaps**

Add one `TestMain` for `internal/server` that dispatches `UMCODE_PLUGIN_TEST_HELPER=skill|mcp|hook` before `m.Run()`. The fake MCP server implements initialize, tools/list, and tools/call. The hook helper decodes envelope version `1`, returns context for `TurnStart`, records `BeforeToolUse`, and returns continue for observational events. Do not bypass the real installer, manager, skill runtime, MCP transport, hook runner, engine, or RPC client.

- [ ] **Step 4: Add format and failure E2E cases**

Add `TestCodexPluginInstallsAndRuns`, `TestClaudePluginInstallsAndRuns`, `TestPluginBeforeToolHookBlocks`, and `TestPluginActivationRollbackKeepsPreviousVersion`. The adapter cases must reach at least one runtime capability, not stop at inspection.

- [ ] **Step 5: Run server E2E tests with race detection**

Run: `go test -race ./internal/server -run 'TestPlugin' -count=1 -v`

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/server/plugin_fixture_test.go internal/server/plugin_e2e_test.go internal/server/e2e_test.go internal/mcp/mcp_test.go
git commit -m "test: cover plugin installation and usage end to end"
```

## Task 10: Document the Plugin Workflow and Run Release Verification

**Files:**
- Modify: `README.md`
- Modify: `config.example.yaml`
- Modify: `GO_ENGINE.md`
- Modify: `plans/IMPLEMENTATION_STATUS.md` only to replace stale claims that MCP is unimplemented.

**Interfaces:**
- Consumes: shipped CLI, UI, package formats, and migration behavior from Tasks 1-9.
- Produces: accurate user and developer guidance; no new runtime interface.

- [ ] **Step 1: Update documentation**

Document plugin-only UI installation, portable/Codex/Claude compatibility, inspect-before-install, managed versus linked mode, user-global installation with per-project enablement, hook trust, CLI commands, cache/data locations, and legacy standalone backend compatibility. Keep legacy config examples labeled as compatibility paths rather than the primary workflow.

- [ ] **Step 2: Run stale-language checks**

Run: `rg -n 'MCP support is scaffolded|placeholder for future|No MCP protocol support|Skills and MCP' README.md GO_ENGINE.md config.example.yaml plans/IMPLEMENTATION_STATUS.md`

Expected: no stale claim that MCP is unimplemented; remaining “Skills and MCP” text must describe plugin components or legacy compatibility.

- [ ] **Step 3: Run formatting and static checks**

Run: `gofmt -w cmd internal && go vet ./...`

Expected: PASS with no files left requiring `gofmt`.

- [ ] **Step 4: Run backend verification**

Run: `go test -race ./...`

Expected: PASS.

- [ ] **Step 5: Run frontend verification**

Run: `cd app/frontend && npm run check && npm test && npm run build`

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add README.md config.example.yaml GO_ENGINE.md plans/IMPLEMENTATION_STATUS.md
git add -u cmd internal app/frontend
git commit -m "docs: document unified plugin workflow"
```

## Execution Selection

The user selected **Native inline execution** by invoking `superpowers:executing-plans`. After this plan is reviewed, execute Tasks 1-10 in this session using the required TDD, ledger, per-task verification, and final fresh whole-branch review workflow.
