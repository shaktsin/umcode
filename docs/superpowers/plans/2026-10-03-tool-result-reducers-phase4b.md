# Tool-Result Reducers (Phase 4b) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Send compact, deterministic views of oversized known-tool results to the model while preserving the canonical result unchanged in UMCode's transcript, hooks, Work Model, evidence ledger, exports, and vault.

**Architecture:** A pure `internal/toolreduce` registry owns format-aware reducers and conservative acceptance guards. The engine invokes it only after canonical recording and hook observation, stores the report in turn-local memory keyed by tool-call ID, and appends only the model projection to the live LLM message. A separate flag and request accounting make Phase 4b independently measurable from the Phase 4a context compiler and emergency context trimming.

**Tech Stack:** Go 1.24+, standard-library JSON/text processing, existing `internal/llm` token estimator, existing engine/config/server test harnesses.

**Spec:** `docs/superpowers/specs/2026-10-03-tool-result-reducers-phase4b-design.md`

## Global Constraints

- `models.tool_result_reducers` defaults to false and is independent of `models.context_compiler`.
- Flag off must leave the model request and every canonical consumer byte-identical to current `master`.
- Reducers are deterministic and make no model, network, filesystem, store, vault, or engine calls.
- Normal target is 2,000 estimated tokens; error/failed/blocked results may use 4,000; candidates saving less than 10% decline.
- Unknown tools, arbitrary MCP/plugin results, explicit `file.read`, malformed owned JSON, empty/invalid/larger candidates, and unsafe schema changes pass through unchanged.
- Canonical `ToolCallData.Output`, errors, UI, transcript, export, hooks, Work observations, evidence, and vault input never receive reduced text.
- Mandatory failures, denials, non-zero exits, commands, source locations, session/observation IDs, action results, diagnostics, artifacts, and truncation notices may not be silently omitted.
- Existing compaction, Phase 4a compilation, and `trimToolResults` remain unchanged and run in their current order.
- Do not add dependencies or user-facing settings, approvals, prompts, chat output, protocol routes, or UI in this phase.

## Review Focus

- Owned JSON gains an unknown non-empty field: decline instead of silently dropping possibly important new semantics (Tasks 2 and 4 tests).
- Shell output uses CRLF, ANSI color around `FAIL`, or one oversized UTF-8 line: detect the failure without corrupting or splitting text (Task 3 tests).
- A nominally passed browser result contains console/request diagnostics: retain the diagnostics rather than summarizing only the pass (Task 2 test).
- Multiple live tool calls are reduced and an older one is emergency-trimmed: match accounting by call ID and attribute only reducer savings to Phase 4b (Task 6 test).
- Configuration or logger is nil, or a reducer panics: return canonical output, count the recovery when possible, and complete the turn (Task 5 tests).

---

### Task 1: Reducer contract, registry, and acceptance guards

**Files:**
- Create: `internal/toolreduce/reduce.go`
- Create: `internal/toolreduce/reduce_test.go`

**Interfaces:**
- Consumes: `llm.EstimateTokens(string) int64`, canonical dotted tool name, original args/output/error state.
- Produces:
  - `type Input struct { Name string; Args json.RawMessage; Output string; IsError bool; Budget int }`
  - `type Report struct { Strategy string; OriginalTokens, SentTokens int; Omitted map[string]int; Declined string }`
  - `func Reduce(Input) (text string, report Report, applied bool)`
  - Internal `type candidate struct { text string; omitted map[string]int; required []string }`
  - Internal reducers with signatures `func reduceVerification(Input) candidate`, `func reduceShell(Input) candidate`, `func reduceSearch(Input) candidate`, and `func reduceReport(Input) candidate`; Tasks 2–4 fill them.
  - Constants `defaultBudgetTokens = 2000`, `failureBudgetTokens = 4000`, `minimumSavingsPercent = 10`.

- [ ] **Step 1: Write failing registry/guard tests**

Add table-driven tests:

```go
func TestReducePassesThroughSmallAndUnsupportedResults(t *testing.T)
func TestAcceptCandidateRejectsEmptyInvalidLargerAndLowSavingText(t *testing.T)
func TestAcceptCandidateRequiresEveryMandatoryMarker(t *testing.T)
func TestAcceptCandidateKeepsValidUTF8AtBudget(t *testing.T)
func TestErrorInputUsesFailureBudget(t *testing.T)
```

Use hand-derived strings and a small explicit `Input.Budget` in guard tests. Assert pass-through is byte-identical, `applied == false`, and `Report.Declined` names the guard. For a valid candidate, assert `OriginalTokens`, `SentTokens`, `Strategy`, and omission counts exactly.

- [ ] **Step 2: Run the tests to verify RED**

Run: `go test ./internal/toolreduce -run 'Test(Reduce|Accept|Error)' -count=1`

Expected: FAIL because the package and interfaces do not exist.

- [ ] **Step 3: Implement the contract and conservative registry**

`Reduce` computes the applicable budget (`Input.Budget`, else 4,000 for `IsError`, else 2,000), passes below-budget results unchanged, dispatches only owned tool names, then validates the candidate. Initial reducer functions return empty candidates, so supported names still decline until later tasks implement them. Use token estimates, not byte counts, for acceptance; crop helpers must cut on rune boundaries.

- [ ] **Step 4: Run package tests to verify GREEN**

Run: `go test ./internal/toolreduce -count=1`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/toolreduce
git commit -m "feat: add tool-result reducer contract and guards"
```

---

### Task 2: Verification and browser-result reducers

**Files:**
- Create: `internal/toolreduce/verification.go`
- Create: `internal/toolreduce/verification_test.go`
- Modify: `internal/toolreduce/reduce.go`

**Interfaces:**
- Consumes: Task 1 `Input`, `candidate`, and acceptance guards; owned JSON emitted by `verification.run` and `browser.verify`.
- Produces: `func reduceVerification(Input) candidate` with deterministic plain-text output and required-marker validation.

- [ ] **Step 1: Write failing verification reducer tests**

Add real JSON fixtures matching the tool structs and these tests:

```go
func TestVerificationReducerKeepsEveryVerdictAndFailureDetail(t *testing.T)
func TestVerificationReducerCollapsesPassingOutput(t *testing.T)
func TestVerificationReducerKeepsBlockedAndNotRunReasons(t *testing.T)
func TestBrowserReducerKeepsDiagnosticsAndArtifactsOnPass(t *testing.T)
func TestVerificationReducerDeclinesMalformedOrUnknownSchema(t *testing.T)
func TestVerificationReducerDeclinesWhenMandatoryFailureExceedsBudget(t *testing.T)
```

Assert the reduced form contains overall status; one line for every check label/status/command; failure exit code, error and diagnostic tail; browser framework, console/request diagnostics and every artifact path/kind. Passing command output must be absent. Add a non-empty unknown field at both top-level and result-entry level and assert byte-identical fallback, protecting schema evolution.

- [ ] **Step 2: Run tests to verify RED**

Run: `go test ./internal/toolreduce -run 'Test(Verification|Browser)' -count=1`

Expected: FAIL because `reduceVerification` has no implementation.

- [ ] **Step 3: Implement owned-schema parsing and rendering**

Replace the Task 1 `reduceVerification` stub with the implementation. Use local decode structs plus `map[string]json.RawMessage` field validation; do not import `internal/tools`. Render exact prefixes `verification: <status>` and `browser verification: <status>`. Passing checks render a single `PASS` line. Failed, blocked and not-run checks render status, label, command, directory, reason/error, exit code, duration, then the bounded diagnostic tail. Populate `candidate.required` with every non-pass label/status and every diagnostic/artifact marker.

- [ ] **Step 4: Run package tests to verify GREEN**

Run: `go test ./internal/toolreduce -count=1`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/toolreduce/verification.go internal/toolreduce/verification_test.go internal/toolreduce/reduce.go
git commit -m "feat: reduce verification tool results"
```

---

### Task 3: Shell, execution-session, and search reducers

**Files:**
- Create: `internal/toolreduce/text.go`
- Create: `internal/toolreduce/text_test.go`
- Create: `internal/toolreduce/search.go`
- Create: `internal/toolreduce/search_test.go`
- Modify: `internal/toolreduce/reduce.go`

**Interfaces:**
- Consumes: Task 1 contract; plain text from `shell.run`/`exec.*`; `file.search` line output; owned `web.search` JSON.
- Produces:
  - `func reduceShell(Input) candidate`
  - `func reduceSearch(Input) candidate`
  - Internal `func selectText(lines []string, budget int, failure bool) candidate`
  - Internal `func diagnosticLine(string) bool`, which strips ANSI only for classification and preserves selected original text.

- [ ] **Step 1: Write failing shell/session tests**

```go
func TestShellReducerKeepsMetadataDiagnosticsAndTail(t *testing.T)
func TestShellReducerCollapsesConsecutiveProgressOnly(t *testing.T)
func TestShellReducerFindsANSIFailureInCRLFOutput(t *testing.T)
func TestShellReducerKeepsUTF8ValidWithOneOversizedLine(t *testing.T)
func TestExecReducerDeclinesUnknownControlFormat(t *testing.T)
```

Fixtures must include `exit_code`, session ID/state, sandbox notice, repeated progress, an ANSI-colored `FAIL`, compiler `path:line` diagnostics, a unique final verdict, and multibyte text. Assert original order among retained lines, exact omitted-line/byte counts, and no collapse of non-consecutive duplicates.

- [ ] **Step 2: Write failing search tests**

```go
func TestFileSearchReducerRoundRobinsAcrossFiles(t *testing.T)
func TestFileSearchReducerKeepsContextWithItsMatch(t *testing.T)
func TestFileSearchReducerKeepsTotalsAndTruncation(t *testing.T)
func TestWebSearchReducerKeepsWholeResultRecords(t *testing.T)
func TestWebFetchAndArbitraryStructuredResultsStayUnchanged(t *testing.T)
```

Use a low explicit budget and enough matches to force reduction. Assert every represented file gets one hit before any file gets a second, path/line/context groups remain intact, original order per file is retained, and the omission instruction tells the model to narrow path/glob/pattern. Each retained web result must keep title, URL and snippet together.

- [ ] **Step 3: Run tests to verify RED**

Run: `go test ./internal/toolreduce -run 'Test(Shell|Exec|FileSearch|WebSearch|WebFetch)' -count=1`

Expected: FAIL because text/search reducers are not implemented.

- [ ] **Step 4: Implement bounded text selection and search grouping**

Replace the Task 1 `reduceShell` and `reduceSearch` stubs. Normalize CRLF for parsing but retain selected line text. Select mandatory metadata and diagnostics first, then bounded head and tail blocks; shrink optional blocks until the token target fits. For `file.search`, parse hit/context groups and cycle over file queues. For `web.search`, validate the exact `title`, `url`, `snippet` object schema. `web.fetch`, `file.read`, MCP, plugin and unknown structured output remain pass-through.

- [ ] **Step 5: Run package tests to verify GREEN**

Run: `go test ./internal/toolreduce -count=1`

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/toolreduce
git commit -m "feat: reduce shell and search tool results"
```

---

### Task 4: Visual QA and Computer Use report reducers

**Files:**
- Create: `internal/toolreduce/reports.go`
- Create: `internal/toolreduce/reports_test.go`
- Modify: `internal/toolreduce/reduce.go`

**Interfaces:**
- Consumes: Task 1 contract; JSON shapes from `internal/visualqa.Report` and `internal/computeruse.Report`, decoded into local reducer structs to keep the package independent.
- Produces: `func reduceReport(Input) candidate` for owned `visual.*` and `computer.*` names.

- [ ] **Step 1: Write failing report reducer tests**

```go
func TestVisualReducerKeepsSessionSnapshotFailuresAndArtifacts(t *testing.T)
func TestComputerReducerPrioritizesFocusedThenEnabledControls(t *testing.T)
func TestComputerReducerKeepsObservationAndLastAction(t *testing.T)
func TestComputerReducerCountsOmittedControlClasses(t *testing.T)
func TestReportReducerDeclinesMissingIdentityOrUnknownSchema(t *testing.T)
```

Build complete realistic JSON fixtures. Assert status/framework/session/observation identity, URL/app/window state, last-action result, screenshot dimensions/path, diagnostics and artifacts survive. Under a forced budget, focused controls come first, then enabled controls in original order, then disabled/static controls; omission counts distinguish each class. Add unknown non-empty fields and assert canonical fallback. For action/inspect results, missing `observation_id` must decline rather than produce an unusable next-action context.

- [ ] **Step 2: Run tests to verify RED**

Run: `go test ./internal/toolreduce -run 'Test(Visual|Computer|Report)' -count=1`

Expected: FAIL because report reduction is not implemented.

- [ ] **Step 3: Implement local report decoding and priority rendering**

Replace the Task 1 `reduceReport` stub. Mirror all currently documented fields from `visualqa.Report`, `computeruse.Report`, nested state/control/action/artifact structures, and reject unknown non-empty fields. Keep snapshot failure sections and diagnostics before ordinary visible text. Add every required identity, diagnostic and artifact string to `candidate.required` so Task 1 guards can reject unsafe candidates.

- [ ] **Step 4: Run package tests to verify GREEN**

Run: `go test ./internal/toolreduce -count=1`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/toolreduce/reports.go internal/toolreduce/reports_test.go internal/toolreduce/reduce.go
git commit -m "feat: reduce visual and computer reports"
```

---

### Task 5: Engine integration with canonical-result isolation

**Files:**
- Modify: `internal/config/config.go:128-142` (`ModelsConfig`)
- Modify: `internal/config/config_test.go:84-103` (flag defaults/loading)
- Create: `internal/engine/reducer.go`
- Create: `internal/engine/reducer_test.go`
- Modify: `internal/engine/engine.go:55-85` (`Engine` counters)
- Modify: `internal/engine/turn.go:498-510,893-898,964-1095` (append model output; `toolRunResult`; reduction after canonical consumers)
- Modify: `internal/engine/plugin_hooks_test.go` (canonical hook evidence)
- Modify: `internal/engine/work_test.go` (canonical Work evidence)

**Interfaces:**
- Consumes: `toolreduce.Reduce` and Task 1 `Report`.
- Produces:
  - `ModelsConfig.ToolResultReducers bool` with YAML key `tool_result_reducers`.
  - `toolRunResult` fields `ModelOutput string` and `Reduction toolreduce.Report`, while `Output` remains canonical.
  - `func (e *Engine) reduceToolResult(name string, args json.RawMessage, output string, isError bool) (model string, report toolreduce.Report)`.
  - `Engine.toolReducerFailures atomic.Int64`.
  - Package test seam `var reduceHook func()` immediately before `toolreduce.Reduce`.

- [ ] **Step 1: Write failing config and engine isolation tests**

```go
func TestToolResultReducersFlagDefaultsFalse(t *testing.T)
func TestReduceToolResultFlagOffIsByteIdentical(t *testing.T)
func TestRunToolKeepsCanonicalStoredAndModelProjectionSeparate(t *testing.T)
func TestReducerDoesNotChangeHookOutput(t *testing.T)
func TestReducerDoesNotChangeWorkEvidenceOrVaultHash(t *testing.T)
func TestReducerPanicFallsBackCountsAndCompletes(t *testing.T)
func TestReducerHandlesNilConfigAndLogger(t *testing.T)
```

Use an oversized owned verification fixture containing a unique passing-output marker that the reducer removes and a failure marker it retains. Assert `toolRunResult.Output`, stored item, hook capture, Work evidence/vault object are canonical; only `ModelOutput` is compact. The panic test sets `reduceHook = func(){ panic("boom") }`, expects canonical model output and increments the counter without escaping.

- [ ] **Step 2: Run tests to verify RED**

Run: `go test ./internal/config ./internal/engine -run 'Test(ToolResult|ReduceTool|RunToolKeeps|Reducer)' -count=1`

Expected: FAIL on missing flag, fields and reducer wrapper.

- [ ] **Step 3: Implement config and the recovered engine wrapper**

Follow `compile.go`'s panic-recovery pattern. Return canonical output when config is nil/off, output is empty, reduction declines, or a panic occurs. Log only tool name and numeric report metadata; never log result text. Check `e.Log != nil` before logging.

- [ ] **Step 4: Wire `runTool` and the live message append**

Keep the current `observe` ordering: Work observation and plugin hooks receive canonical `output`; reduction occurs only when constructing the returned `toolRunResult`. In the turn loop append `ModelOutput` to `llm.Message.Result`, but keep `results` for loop detection based on canonical `Output` so optimization cannot change no-progress behavior.

- [ ] **Step 5: Run focused and full tests**

Run: `go test ./internal/config ./internal/engine -count=1`

Expected: PASS.

Run: `go test ./...`

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/config internal/engine
git commit -m "feat: send reduced tool results only to models"
```

---

### Task 6: Per-request accounting, end-to-end proof, and documentation

**Files:**
- Modify: `internal/engine/accounting.go`
- Modify: `internal/engine/accounting_test.go`
- Modify: `internal/engine/compiler_test.go` (new accounting argument and Phase 4a coexistence)
- Modify: `internal/engine/turn.go:451-510` (turn-local report map and request accounting)
- Create: `internal/server/reducer_e2e_test.go`
- Modify: `internal/engine/testdata/request_baseline.json` only if zero-value `omitempty` verification proves the flag-off golden remains semantically identical; prefer no golden change.
- Modify: `GO_ENGINE.md:144-149`

**Interfaces:**
- Consumes: Task 5 `toolRunResult.Reduction`, final `req.Messages`, current `RequestPackets`.
- Produces:
  - `type ToolReductions map[string]toolreduce.Report` keyed by `ToolCallID`.
  - `RequestBreakdown` fields `ToolResultOriginalTokens`, `ToolResultSavedTokens`, `ToolResultsReduced`, all `json:",omitempty"`.
  - `func measureRequest(layers []promptLayer, req llm.Request, packets RequestPackets, reductions ToolReductions) RequestBreakdown`.

- [ ] **Step 1: Write failing accounting tests**

```go
func TestMeasureRequestAttributesReducerSavingsByCallID(t *testing.T)
func TestMeasureRequestSeparatesEmergencyTrimFromReducerSavings(t *testing.T)
func TestMeasureRequestIgnoresReportsForAbsentToolMessages(t *testing.T)
func TestMeasureRequestHandlesMultipleReducedCallsWithoutCrossAttribution(t *testing.T)
func TestRequestBaselineGoldenStillOmitsZeroReducerFields(t *testing.T)
```

Construct two tool messages with different IDs and reports. Emergency-trim one result before measurement. Assert `ToolResultTokens` equals final sent text, `ToolResultOriginalTokens` uses report originals only for matching IDs, `ToolResultSavedTokens` equals the sum of `OriginalTokens-SentTokens` from applied reducers (not the emergency stub saving), and absent report IDs add nothing.

- [ ] **Step 2: Run accounting tests to verify RED**

Run: `go test ./internal/engine -run 'TestMeasureRequest|TestRequestBaseline' -count=1`

Expected: FAIL on the missing fields/signature.

- [ ] **Step 3: Implement request-local report tracking and accounting**

Initialize one `ToolReductions` map per turn. Store a report only when reduction applied, keyed by the model call's ID. Pass it to every `measureRequest` call. Measurement scans only final request tool messages, calculates final sent tokens from `m.Result`, and uses report before/after values only for reducer attribution. Update existing call sites and tests with an empty map.

- [ ] **Step 4: Write failing end-to-end tests**

In `reducer_e2e_test.go`, add a test-only tool implementing `tools.Tool` with canonical name `verification.run` and a large deterministic owned JSON result, then replace the harness registry entry before the turn. Add:

```go
func TestReducerFlagOffRequestAndTranscriptAreCanonical(t *testing.T)
func TestReducerFlagOnShrinksSecondRequestButPreservesTranscript(t *testing.T)
func TestReducerWorksWithContextCompilerOnAndOff(t *testing.T)
func TestFailingReductionKeepsCommandExitTailAndArtifact(t *testing.T)
```

The fake provider's second script inspects its request. Assert at least 40% fewer tool-result characters/tokens for the oversized fixture, while `thread/read` and `ExportThread` contain the unique canonical marker. Run coexistence subtests with only reducer on and with both Phase 4a/reducer on. The failure fixture must retain failing command, non-zero exit, final error tail and artifact path.

- [ ] **Step 5: Run end-to-end tests to verify RED**

Run: `go test ./internal/server -run 'TestReducer' -count=1`

Expected: FAIL until request accounting/wiring and fixtures are complete.

- [ ] **Step 6: Complete end-to-end wiring and document the flag**

Fix only integration gaps exposed by the tests. Add a concise `GO_ENGINE.md` paragraph explaining the model-only projection, canonical retention, supported result families, independent off-by-default flag, fallbacks, and accounting fields. Do not add UI or user prompts.

- [ ] **Step 7: Run verification**

Run: `go test ./internal/toolreduce ./internal/config ./internal/engine ./internal/server -count=1`

Expected: PASS.

Run: `go vet ./...`

Expected: PASS.

Run: `go test ./...`

Expected: PASS.

Run: `git diff --check`

Expected: no output.

- [ ] **Step 8: Commit**

```bash
git add GO_ENGINE.md internal/engine internal/server
git commit -m "feat: account and verify tool-result reductions"
```

---

## Final branch review

- [ ] Use `superpowers:requesting-code-review` with a fresh reviewer against `origin/master`.
- [ ] Fix Critical and Important findings in one TDD pass; document any explicit rulings.
- [ ] Re-run `go test ./...`, `go vet ./...`, and `git diff --check` with fresh output.
- [ ] Use `superpowers:finishing-a-development-branch` and let the user choose merge, PR, or preservation.
