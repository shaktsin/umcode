# Phase 4d: Progressive Tool Loading Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Reduce per-request tool-schema overhead while keeping permitted specialist tools discoverable and preserving authorization.

**Architecture:** Freeze a resolved permitted catalog per turn, select a stable core/family subset, and load additional schemas via an engine-owned discovery tool. Execution continues through existing guards/approval/workflow paths; monotonic fallback exposes the full permitted catalog.

**Tech Stack:** Go 1.24.0 (toolchain go1.24.7), existing tool registry/plugin snapshots/provider adapters, no new dependencies.

**Spec:** `docs/superpowers/specs/2026-10-10-phase4d-progressive-tools-design.md` (approved in conversation). Execute after Phase 4c verification.

## Global Constraints

- `models.progressive_tools: false`, independently configurable from compiler, retrieval, reducers and Designed workflow.
- No engine-generated provider call for selection/discovery. Model-initiated discovery costs are included in total task measurements.
- Core tools remain available; work.update retains its existing feature gate. Selection is advertisement, never permission.
- Query <=512 bytes, exact names <=8, descriptions <=256 bytes, results <=8, additional loaded tools <=64 before full-catalog fallback.
- Discovery sees only the current turn's permitted catalog and cannot execute another tool, acquire plugins, alter permissions or trigger a new approval prompt.
- Stable canonical schema ordering. Same-phase selection freezes; explicit additions are monotonic for the turn, with pruning only next turn.
- No product UI, model routing, installation, new capabilities, default rollout or changes to provider-reported usage.
- Preserve existing flag-off schemas/prompt/dispatch exactly; no dependencies or Go-version increase.

## Review Focus

1. Omitted or unknown-intent tools must remain reachable without guessing names (Tasks 2/3/5).
2. Catalog/plugin/wire-name collisions must not load a different implementation than advertised (Tasks 1/4).
3. Discovery, guessed direct calls and full-catalog fallback must not reveal disabled capabilities (Tasks 3/4).
4. Existing sessions must retain inspect/stop tools across new turns, phase transitions and failures (Tasks 2/4).
5. Schema savings must include discovery/prompt overhead and preserve streamed call/result pairing across provider requests (Tasks 4/5).

## File structure and interfaces

Create pure `internal/toolselect/{catalog,select,discover}.go` plus tests. This package imports llm for ToolSpec but never engine/tools/store. Engine adapters freeze actual tools and metadata into it. New engine files `tool_catalog.go`, `tool_selection.go`, `tool_discovery.go` keep selection responsibilities out of the large turn.go. Keep existing execution lifecycle in runTool with narrowly defined integration hooks.

Types: `toolselect.Entry{CanonicalName,WireName,Family,Origin string; Spec llm.ToolSpec}`; `Catalog{Generation string; Entries []Entry}`; `Signals{Request string; Depth,TaskID string; ExplicitNames,RequiredFamilies,PlannedCheckFamilies []string}`; `State` owns immutable Catalog, loaded canonical-name set, phase identity and full-fallback bool; `Report` counts catalog/exposed/schema tokens/discovery/additions and fallback enum. Generation is SHA256 of sorted canonical names/origins/exact schema bytes, not time. Metadata is trusted origin/registry metadata; descriptions are redacted before query result projection.

Allowed families are core, web, browser, computer, `mcp:<server-identity>`, `plugin:<plugin-identity>`, other. Preserve explicit server/plugin identity rather than splitting arbitrary tool names at underscores. Raw user text is bounded to 8 KiB for deterministic keyword matching; metadata strings are limited to 4 KiB, schemas to 256 KiB and catalog to 4,096 entries. Exceeding safe selection limits returns full baseline-catalog fallback; never truncate execution's existing catalog.

### Task 1: Freeze the permitted catalog and configuration contract

**Files:**
- Create: `internal/toolselect/catalog.go`, `internal/toolselect/catalog_test.go`, `internal/engine/tool_catalog.go`
- Modify: `internal/config/config.go`, config tests, `internal/engine/turn.go`, plugin snapshot metadata adapters

**Interfaces:**
- Produces: `toolselect.NewCatalog(entries []Entry) (Catalog,error)`.
- Produces engine helper: `(*Engine).permittedTurnCatalog(ctx context.Context, snapshot pluginSnapshot, project *protocol.Project) ([]turnTool,toolselect.Catalog,error)`.
- Produces Models.ProgressiveTools bool. Existing turnTool gains canonical origin/family metadata where needed; do not export live tool implementations into toolselect.

- [ ] **Step 1: Write failing tests**

`TestProgressiveToolsConfigIndependent`: default false; combinations with compiler/retrieval/reducers/Designed workflow remain valid.
`TestCatalogOrderAndCollisions`: source enumeration reorder yields identical generation/spec bytes; static-over-plugin precedence preserved; tools.discover reserved; duplicate provider wire names fail safe.
`TestPermittedCatalogScope`: disabled computer use/MCP servers/plugins are absent from both selectable and discoverable sets. Plugin tools belong only to acquired project snapshot; no new acquisition occurs in selection.

- [ ] **Step 2: Observe RED**

Run: `GOCACHE=/private/tmp/umcode-go-cache go test ./internal/toolselect ./internal/config ./internal/engine -run "TestCatalog|TestPermittedCatalog|TestProgressiveToolsConfig" -count=1`. Expected: the named assertions fail because the new behavior is absent. Compilation failure while adding a new API is setup only; obtain a behavioral failure before implementing its body.

- [ ] **Step 3: Implement the deliverable**

Freeze resolved tools exactly once, using existing turnTools precedence and existing visibility rules. Share canonical permission predicates between initial advertisement and discovery; maintain existing plugin policy without inventing permissive exceptions. Validate origins and wire-name uniqueness after existing precedence resolution. Only the progressive branch uses new catalog construction; disabled branch keeps its original assembly. Catalog errors select the baseline full permitted list; malformed full catalog still follows existing turn failure behavior rather than executing a guessed identity.

- [ ] **Step 4: Verify GREEN**

Run the Step 2 command. Expected: exit 0 and all named tests pass. Run existing tests for the changed package before committing; record exact output in the execution ledger.

- [ ] **Step 5: Commit**

Stage only the files above and required test fixtures. Commit message: `feat(tools): freeze permitted catalog for progressive selection`.

### Task 2: Implement deterministic initial selection and stable phase state

**Files:**
- Create: `internal/toolselect/select.go`, `internal/toolselect/select_test.go`, `internal/engine/tool_selection.go`
- Modify read-only session adapters in `internal/tools/execsession.go`, `internal/preview/manager.go`, `internal/visualqa/manager.go`, `internal/computeruse/manager.go`

**Interfaces:**
- Consumes: Task 1 Catalog/Signals.
- Produces: `toolselect.Start(catalog Catalog, signals Signals) (*State,Report,error)`, `(*State).Advance(signals Signals) Report`, `(*State).Specs() []llm.ToolSpec`, `(*State).Pin(name string) error`, `(*State).Fallback(reason string)`.
- Produces engine helper: `(*Engine).requiredToolFamilies(threadID string) []string`; ExecManager exposes read-only `ActiveForThread(threadID string) bool`; preview ownership uses List snapshots, and visual/computer ownership uses existing ForThread methods.

- [ ] **Step 1: Write failing tests**

`TestInitialCoreAndFamilySelection`: all present file.*, shell.run, exec lifecycle, verification.plan/run, skill discovery/loading and permitted work.update retained. Explicit web/frontend/planned-browser/desktop/MCP/plugin signals load matching permitted families. Unknown intent has core plus discovery, no full specialist catalog.
`TestSelectionPhaseStable`: request reclassification cannot remove shown tools; phase/task transitions add only; identical signals give identical schema bytes; next turn prunes unrelated tools.
`TestSelectionRequiredSessions`: existing exec/preview/visual/computer session keeps inspect/finish/stop family on a new turn even without keywords. Foreign-thread sessions do not activate families.
`TestSelectionBoundedMetadata`: oversized input, unknown family and deterministic lexical ambiguity fall back or retain discovery without panic.

- [ ] **Step 2: Observe RED**

Run: `GOCACHE=/private/tmp/umcode-go-cache go test ./internal/toolselect ./internal/engine -run "TestInitialCore|TestSelection" -count=1`. Expected: the named assertions fail because the new behavior is absent. Compilation failure while adding a new API is setup only; obtain a behavioral failure before implementing its body.

- [ ] **Step 3: Implement the deliverable**

Implement the exact core list in the spec and explicit metadata-based family mapping. Keep all registered exec lifecycle core tools. Web/frontend/desktop lexical rules are conservative tested tables; they may add families but never remove core or required sessions. Family matching uses origin identities, canonical/wire names and bounded metadata, not retrieved text. Read active sessions through mutex-protected in-memory state scoped by actual thread owner; return booleans only, no session contents. Advance on trusted Work depth/task identity changes; loaded union survives until turn completion. Fallback monotonically exposes the entire permitted Catalog.

- [ ] **Step 4: Verify GREEN**

Run the Step 2 command. Expected: exit 0 and all named tests pass. Run existing tests for the changed package before committing; record exact output in the execution ledger.

- [ ] **Step 5: Commit**

Stage only the files above and required test fixtures. Commit message: `feat(tools): select stable core and specialist tool families`.

### Task 3: Add bounded discover-and-load protocol

**Files:**
- Create: `internal/toolselect/discover.go`, `internal/toolselect/discover_test.go`, `internal/engine/tool_discovery.go`

**Interfaces:**
- Consumes: State/Catalog.
- Produces: `DiscoverArgs{Query string; Names []string; Cursor string}` and `DiscoverResult{Entries []DiscoveryEntry; NextCursor string}` with JSON snake_case names.
- Produces: `(*State).Discover(argsJSON json.RawMessage) (DiscoverResult,error)`; entries include canonical_name, wire_name, family, description, loaded.
- Engine discovery adapter implements tools.Tool for name tools.discover, description/schema/Assess/Call; it closes over only this turn State.

- [ ] **Step 1: Write failing tests**

`TestDiscoveryLoadsExactAndLexicalMatches`: canonical/wire names load the same entry once, query matches bounded redacted metadata, <=8 entries and <=256 description bytes; Specs contains new schema on next request.
`TestDiscoveryStrictArguments`: unknown JSON fields, malformed JSON, query >512 bytes, >8 names and neither query nor names return bounded errors; schema has additionalProperties=false and max limits.
`TestDiscoveryPaginationScoped`: cursor binds catalog generation/query/offset; altered, foreign-turn or stale cursor errors; complete pages reach every permitted match without duplicates.
`TestDiscoveryPermissionAndCaps`: forbidden names indistinguishable from absent; matching >64 additional tools activates full permitted fallback; no target Call/Assess/plugin acquisition occurs.
`TestDiscoverySecretMetadata`: synthetic tokens in descriptions/query never appear in result or log.

- [ ] **Step 2: Observe RED**

Run: `GOCACHE=/private/tmp/umcode-go-cache go test ./internal/toolselect -run TestDiscovery -count=1`. Expected: the named assertions fail because the new behavior is absent. Compilation failure while adding a new API is setup only; obtain a behavioral failure before implementing its body.

- [ ] **Step 3: Implement the deliverable**

Use a versioned opaque cursor encoded from query hash, generation, per-turn random nonce and offset, authenticated with a per-turn ephemeral HMAC key. Canonical generation alone cannot reject a cursor from another turn with identical catalog. Sort query results by exact-name/family/lexical score/name, no embeddings. Strict decoder rejects unknown fields and trailing JSON. Named loads return bounded entries without a cursor unless query also paginates; deduplicate exact/query overlaps. Return loaded=true for successful additions; full schemas exist only in State.Specs. Internal failures activate fallback; malformed input alone stays a normal error. Discovery adapter is read-only, never an invocation proxy.

- [ ] **Step 4: Verify GREEN**

Run the Step 2 command. Expected: exit 0 and all named tests pass. Run existing tests for the changed package before committing; record exact output in the execution ledger.

- [ ] **Step 5: Commit**

Stage only the files above and required test fixtures. Commit message: `feat(tools): discover and load omitted schemas safely`.

### Task 4: Integrate selection into streaming turn lifecycle and accounting

**Files:**
- Modify: `internal/engine/turn.go`, `internal/engine/engine.go` (`systemPromptLayers`), `internal/engine/accounting.go`
- Modify: `internal/engine/tool_catalog.go`, `tool_selection.go`, `tool_discovery.go`
- Create: `internal/engine/progressive_tools_test.go`

**Interfaces:**
- Consumes: Tasks 1–3; immutable execution catalog and State.
- Produces turn-scoped tool resolution: `resolveTurnTool(catalog []turnTool, name string) (turnTool,bool)` used only by the progressive path.
- Produces `ToolSelectionAccounting` containing full/exposed schema estimates, added count, discovery calls, phase ID and fallback enum; existing usage fields remain unchanged.

- [ ] **Step 1: Write failing tests**

`TestProgressiveFlagOffRequestEquivalent`: full serialized request/system prompt/dispatch equals previous behavior with flag false.
`TestProgressiveDiscoveryNextRequest`: streamed response calls discovery; normal item/result pair stored; next provider request has matching schema exactly once and no target action ran during discovery.
`TestProgressiveGuessedToolAndRevocation`: unadvertised known permitted call follows normal guards/approval then pins schema; forbidden direct/discovery calls and capability revoked mid-turn stay denied.
`TestProgressiveSnapshotIdentity`: source changes mid-turn cannot swap advertised implementation; name collisions resolve to same frozen winner; next turn sees refreshed snapshot.
`TestProgressiveFallbackAndPairing`: panic/internal selection failure switches full permitted catalog without another provider call; discovery plus sibling call in same response preserves IDs/results and next-request-only schema update.
`TestProgressiveAccounting`: count full/exposed schema, prompt and discovery result overhead; no double counts or provider usage mutation, no query/secret logging.

- [ ] **Step 2: Observe RED**

Run: `GOCACHE=/private/tmp/umcode-go-cache go test ./internal/engine -run TestProgressive -count=1`. Expected: the named assertions fail because the new behavior is absent. Compilation failure while adding a new API is setup only; obtain a behavioral failure before implementing its body.

- [ ] **Step 3: Implement the deliverable**

Create State at turn setup. Before every provider call refresh req.Tools from State.Specs in stable order; update trusted phase/session additions without pruning. Route tools.discover through ordinary item/result/cancellation lifecycle while marking it workflow discovery; no new approval, gate escalation, plugin action, plugin tool hooks or observational fact. Progressive dispatch resolves against the same frozen actual-tool catalog; ordinary permission/guard/workflow/approval checks still run before actions, including current policy revocation. Pin successful name resolution for the next request even when the tool later fails. Add the spec's concise discovery instruction in an enabled-only prompt layer; never list omitted catalog names there. Selector/discovery panic recovery has bounded enum logging and fallback. Include discover tool overhead in full versus selected comparisons, with baseline totals defined explicitly.

- [ ] **Step 4: Verify GREEN**

Run the Step 2 command. Expected: exit 0 and all named tests pass. Run existing tests for the changed package before committing; record exact output in the execution ledger.

- [ ] **Step 5: Commit**

Stage only the files above and required test fixtures. Commit message: `feat(engine): load tool schemas progressively during turns`.

### Task 5: Prove task-level savings and specialist discoverability

**Files:**
- Create: `internal/engine/progressive_tools_benchmark_test.go` and deterministic testdata fixtures
- Modify: `GO_ENGINE.md`, engine lifecycle regression tests

**Interfaces:**
- Consumes: complete progressive tool loading with real engine dispatch.
- Produces: independent baseline/treatment fixtures and activation/fallback documentation.

- [ ] **Step 1: Write failing tests**

`TestProgressiveCodingSavingsWithoutQualityLoss`: actual file inspection/edit/verification succeeds with baseline and treatment; same artifact and verified result, fewer total schema tokens including prompt/discovery overhead, no engine-added provider call.
`TestProgressiveSpecialistDirectAndDiscovery`: one fixture explicitly requests a permitted specialist, another uses unknown intent then tools.discover; both reach a real deterministic fake specialist through normal dispatch with correct schema, approval behavior and matching result. Record total calls/rounds/tokens even if discovery case costs more.
`TestProgressiveMutationDetected`: bypass filtering temporarily; coding schema-savings assertion fails, restore exact bytes.
`TestPhase4FlagsIndependent`: retrieval alone, progressive alone, both, neither; failures in one retain the other's existing semantics.

- [ ] **Step 2: Observe RED**

Run: `GOCACHE=/private/tmp/umcode-go-cache go test ./internal/engine -run "TestProgressiveCoding|TestProgressiveSpecialist|TestPhase4Flags" -count=1 -v`. Expected: the named assertions fail because the new behavior is absent. Compilation failure while adding a new API is setup only; obtain a behavioral failure before implementing its body.

- [ ] **Step 3: Implement the deliverable**

Use streaming provider fixtures and actual runTurn, not an isolated schema count. Specialist implementations may be deterministic fakes but must enter the real registry/plugin snapshot and execution permission seams. Keep baseline catalog identical across paired runs. Document configuration, core/discovery, phase union, permission limits, snapshot/fallback behavior, metadata limits, diagnostics and measured fixture numbers. Do not claim universal savings or Phase 6 release gates.

- [ ] **Step 4: Verify GREEN**

Run the Step 2 command. Expected: exit 0 and all named tests pass. Run existing tests for the changed package before committing; record exact output in the execution ledger.

- [ ] **Step 5: Commit**

Stage only the files above and required test fixtures. Commit message: `test(tools): validate progressive schema savings and reachability`.

### Task 6: Review and final Phase 4 verification

- [ ] Review final slice against spec/plan and request one whole-slice review under the chosen execution method; focus permissions, catalog identity, session cleanup and provider call/result ordering.
- [ ] Resolve Important/Critical findings with observed RED/GREEN regressions; ledger explicit rulings and deferred minors.
- [ ] Run `GOCACHE=/private/tmp/umcode-go-cache go test ./... -count=1 -timeout=10m`, `GOCACHE=/private/tmp/umcode-go-cache go vet ./...`, and `git diff --check`. Expected: exit 0; use sandbox escalation for shell/loopback tests if required.
- [ ] Confirm independent flag-off compatibility and record both slices' fixture measurements in a durable completion record. Keep defaults off; Phase 6 owns broad ablations/UI/rollout.
- [ ] Follow the finishing skill. No push, PR or merge without explicit user instruction.

## Plan self-review

Both plans cover the approved scope, with explicit source/scoping contracts, defaults, budgets and fallback. Retrieval precedes schema selection but has no runtime dependency on it. Task APIs use protocol/retrieval/toolselect types without importing engine/store into pure packages. Session/cursor/authentication implementation choices above make the spec's lifecycle isolation concrete. Final review and tests remain required; these plans do not claim implemented behavior.
