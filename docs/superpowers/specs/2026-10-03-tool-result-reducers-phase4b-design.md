# Tool-Result Reducers (Token Efficiency Phase 4b)

**Status:** Design approved in conversation; pending written-spec review

**Date:** 2026-10-03

**Parent spec:** `2026-09-30-quality-first-token-efficiency-design.md` (Phase 4: context compiler)

**Builds on:** Phase 3 (vault and evidence lifecycle, #45) and Phase 4a (context packets and interaction tail, #46)

## Summary

Tool results are currently sent back to the model exactly as the tool returns them. Large shell logs, repeated passing checks, search matches, browser diagnostics, and accessibility trees therefore consume tokens on every subsequent model call in the same turn. The existing 64 KiB clip and 75%-of-window trimming protect the context limit, but they act late and are not aware of the result's meaning.

Phase 4b adds deterministic, tool-specific reducers. A reducer creates a smaller model-facing view while the canonical result continues to drive the transcript, UI, hooks, Work Model, evidence ledger, and vault. Reduction never calls a model, never changes tool behavior, and declines whenever the tool format cannot be interpreted safely.

This slice implements reducers only. Retrieval and progressive tool loading remain later Phase 4 work so each optimization can be measured independently.

## Goals

- Reduce tokens from large, repetitive tool results before the next model call.
- Preserve failures, verdicts, actionable diagnostics, source locations, state identifiers, and artifact references needed for correct follow-up work.
- Keep the canonical result unchanged everywhere except the model request.
- Make reduction independently feature-flagged and measurable for Phase 6 ablations.
- Fall back to the original result on an unknown format, malformed structured output, reducer error, panic, empty reduction, or non-saving reduction.
- Keep flag-off model requests byte-identical to current `master`.

## Non-goals

- Changing tool execution, approval, sandbox, hook, UI, transcript, export, Work Model, evidence, or vault behavior.
- Summarizing results with another LLM call.
- Reducing file contents that the model explicitly requested through `file.read`.
- Interpreting arbitrary MCP or plugin output whose schema UMCode does not own.
- Graph/FTS5 retrieval, progressive tool loading, or phase-aware tool selection.
- Replacing Phase 4a packets, automatic compaction, or `trimToolResults`.
- Enabling any optimization by default before the paired benchmark passes.

## Quality-first invariants

1. **One canonical result.** `protocol.ToolCallData.Output`, errors, exports, hook invocations, Work Model observations, evidence records, and vault objects receive the existing result, not the reduction.
2. **Model-only projection.** Only `llm.Message.Result` for the live tool loop may receive reduced text.
3. **No invented claims.** Reducers select, group, count, and label existing data. They do not infer causes, success, or correctness.
4. **Failures outrank successes.** A failure, denial, non-zero exit, panic, console error, failed request, blocked/not-run check, or truncation notice is retained ahead of passing detail.
5. **Identity is mandatory.** Call IDs, session/observation IDs, paths with line numbers, URLs, commands, exit codes, statuses, artifact paths, and continuation instructions survive whenever present.
6. **Visible omission.** Every lossy reduction states what class and quantity was omitted and how to retrieve a narrower or fresh result. It never silently truncates.
7. **Conservative fallback.** If required fields cannot fit or safe reduction is uncertain, send the original result.
8. **Independent rollout.** `models.tool_result_reducers` defaults to false and does not require `models.context_compiler`.

## Engine boundary

Reduction happens after the tool has executed and after its canonical output has been made available to the recorder, but before the next model message is appended:

```text
tool.Call
  -> canonical output
       -> transcript / UI
       -> hooks
       -> Work Model / evidence / vault
       -> reducer
            -> reduced model result, or canonical fallback
                 -> llm.Message{Role: tool}
```

`toolRunResult` gains a model-facing result and a reduction report while retaining `Output` as the canonical result:

```go
type toolRunResult struct {
    Output      string
    ModelOutput string
    IsError     bool
    HookContext []string
    Reduction   toolreduce.Report
}
```

`runTool` resolves the canonical dotted tool name, executes the current recording and hook flow with `Output`, then invokes the reducer only when the flag is enabled. The turn loop appends `ModelOutput` to the in-memory `llm.Message`. Stored items remain untouched.

Earlier-turn tool calls are already represented by short provider-neutral notes in `historyMessages`, so reducers apply only to live results in the current tool loop. Phase 4a also retains only the current turn's live results beside its compiled prefix. No reduced result needs database persistence.

## Pure reducer package

Create `internal/toolreduce` with no engine, store, network, filesystem, vault, or model dependency:

```go
type Input struct {
    Name    string
    Args    json.RawMessage
    Output  string
    IsError bool
    Budget  int // estimated tokens; zero means use the package default
}

type Report struct {
    Strategy       string
    OriginalTokens int
    SentTokens     int
    Omitted        map[string]int
    Declined       string
}

func Reduce(in Input) (text string, report Report, applied bool)
```

The package uses `llm.EstimateTokens`, but performs no I/O. The registry dispatches on canonical dotted tool names. Reducers return candidate text; the registry validates UTF-8, required markers, size, and savings before reporting `applied=true`.

### Common guards

- Results at or below 2,000 estimated tokens pass through unchanged.
- The normal target is 2,000 tokens. Failed or blocked results may use up to 4,000 tokens so diagnostics are not traded for cost.
- A candidate that is empty, invalid UTF-8, larger than the original, or saves less than 10% is declined.
- A reducer panic is recovered at the engine boundary and sends the original result.
- Reducers never read `RawSink`. Pre-clip output remains a vault concern and cannot accidentally be reintroduced into a prompt.
- Consecutive duplicate lines and redundant blank space may be removed losslessly for every supported text result. Non-consecutive duplication is preserved unless a tool-specific rule says otherwise.

The thresholds are package constants, not user settings. Phase 6 may tune them from benchmark evidence.

## Reducer families

### Verification results

Applies to `verification.run` and `browser.verify`.

The reducer parses the owned JSON schema and emits:

- Overall status, command count, elapsed time, and summary.
- One compact line per passing check: label, status, duration, and exit code when meaningful.
- Full available detail for failed, blocked, or not-run checks up to the failure budget: label, command, directory, exit code, reason, diagnostics, and the diagnostic tail.
- Browser console errors, failed requests, and artifacts with paths and kinds.
- Existing truncation flags and vault/artifact references.

Passing command output is omitted after its verdict. Failed output is never replaced by a pass count. If JSON decoding or required-field validation fails, the original result is sent.

### Shell and execution-session results

Applies to `shell.run`, `exec.start`, `exec.write`, and `exec.stop` when their output exceeds the threshold.

The reducer preserves, in original order:

- Session ID, process state, working directory, command metadata, exit code, and sandbox/policy notices.
- The first bounded block, so invocation and startup failures remain visible.
- Lines containing case-insensitive failure signals such as `error`, `fail`, `panic`, `fatal`, `denied`, `blocked`, `timeout`, or compiler-style path/line diagnostics.
- The final bounded block, where test runners and build tools normally print verdicts.
- A count of omitted middle lines and bytes.

Consecutive repeated progress lines collapse to one line plus a repetition count. A result containing an unrecognized control/session format is unchanged. A non-zero or unknown exit receives the larger failure budget.

### Search results

Applies to `file.search` and the owned structured output of `web.search`.

For `file.search`, paths, line numbers, match text, context lines, and the final totals/truncation line are recognized. When over budget, matches are selected round-robin across distinct files so one large file cannot hide all other candidate locations. Original ordering within each file is retained. The result reports omitted matches and instructs the model to narrow the path, glob, or pattern before relying on omitted content.

For `web.search`, every retained result keeps title, URL, source, and snippet together. Error, truncation, and pagination/continuation information is mandatory. Unknown web-search shapes are unchanged.

### Visual and Computer Use reports

Applies to `visual.start`, `visual.inspect`, `visual.act`, `computer.list`, `computer.start`, `computer.inspect`, `computer.act`, and `computer.stop` when their owned JSON reports exceed the threshold.

The reducer keeps:

- Status, framework, URL/app/window identity, observation ID, and screenshot dimensions/reference.
- Focused and enabled actionable controls before disabled or static elements.
- Console errors, failed requests, broken images, overflow/layout failures, action errors, and diagnostics.
- Artifact paths and kinds.
- The result of the most recent action and enough current state to verify whether it succeeded.
- Counts by omitted control or diagnostic class.

If an observation ID, status, failure diagnostic, or action result would be dropped, reduction is declined. Image evidence appended separately by the engine is unchanged.

### Web fetch, MCP, plugins, and file reads

`web.fetch` may receive lossless whitespace normalization and consecutive boilerplate deduplication only. UMCode has no query intent at this boundary, so it must not choose arbitrary page passages and discard the rest.

Arbitrary MCP and plugin results are unchanged unless a future plugin supplies an explicit reducer together with its owned result schema. Generic JSON key selection is forbidden because keys do not reveal semantic importance.

`file.read`, `file.list`, file mutations, preview controls, verification planning, and short scalar results are unchanged. Those tools are already bounded or their exact content is the reason they were called.

## Accounting and diagnostics

The existing `ToolResultTokens` remains the number actually sent in the request. `RequestBreakdown` gains:

```text
ToolResultOriginalTokens
ToolResultSavedTokens
ToolResultsReduced
```

The turn loop keeps reports keyed by tool-call ID. `measureRequest` counts only reports for tool messages present in that specific request. `ToolResultOriginalTokens` treats an unchanged result's current tokens as its original size and uses the report's original size for an applied reduction. `ToolResultSavedTokens` is reducer-only (`report.OriginalTokens - report.SentTokens`); later emergency trimming does not receive Phase 4b credit. `ToolResultTokens` is measured from the final request after any emergency trim. This keeps ablation attribution honest and prevents a provider retry from inflating savings.

No result text is logged. The developer log records tool name, strategy, original tokens, sent tokens, omission counts, and decline reason. Recovered reducer panics increment an engine-owned `ToolReducerFailures` counter, parallel to the Phase 4a compiler-failure counter.

Flag-off accounting and request contents remain byte-identical except that the new JSON fields are zero/omitted.

## Interaction with existing context controls

The order for each model request is:

1. Phase 4a compiles packets and the interaction tail when enabled.
2. Live current-turn tool messages already contain their model-facing reductions.
3. `trimToolResults` remains the emergency 75%-of-window backstop and may replace older large reduced results with its existing stub.
4. Request accounting measures the final request.

Reducers do not alter the stable system prefix or tool schemas, so prompt-cache behavior is unchanged. They also do not make compaction or Phase 4a mandatory.

## Configuration and rollout

Add `models.tool_result_reducers` as a boolean defaulting to false. There is no UI, protocol route, approval, prompt, or chat message in this phase.

Rollout order:

1. Merge with the flag off.
2. Run deterministic and integration tests.
3. Enable in paired fixtures and developer diagnostics.
4. Measure Phase 4b alone and Phase 4a+4b together.
5. Keep disabled by default until Phase 6 quality and efficiency gates pass.

## Failure behavior

- Unsupported tool or small result: return original, with no report noise at info level.
- Malformed known structured result: return original and record a debug decline reason.
- Reducer error or panic: recover, count the failure, log metadata without result text, and return original.
- Mandatory fields exceed the budget: return original rather than omit them.
- Candidate is not materially smaller: return original.
- Accounting failure: send the selected result; diagnostics must never block a turn.

No reducer failure changes a tool item, turn status, hook outcome, Work Model event, evidence record, or vault object.

## Testing

All product behavior follows RED-GREEN TDD.

### Unit and golden tests (`internal/toolreduce`)

- Below-threshold and unsupported results are byte-identical.
- Malformed JSON for an owned structured tool is unchanged.
- Verification: every status and failed/not-run detail survives; passing output collapses; artifacts survive.
- Shell: exit/session/sandbox metadata, diagnostic lines, and tail survive; repeated progress collapses; omission count is correct.
- Search: at least one match from each represented file survives before extra matches; path/line pairs remain intact; totals and truncation survive.
- Visual/computer: observation ID, current action result, enabled controls, failures, screenshots, and artifacts survive; omitted classes are counted.
- Web fetch normalization is lossless except repeated identical boilerplate; arbitrary MCP/plugin JSON remains unchanged.
- UTF-8 boundaries remain valid.
- Empty, larger, under-10%-saving, and mandatory-field-dropping candidates decline.
- Reducer output is deterministic for identical input.

### Engine tests

- Flag off: live model tool result, transcript item, hooks, Work observation, and accounting match current `master`.
- Flag on: the model sees the reduction while transcript, export, hook, Work evidence, and vault hash see the canonical output.
- Unknown and malformed outputs reach the model unchanged.
- Reducer panic falls back and the turn completes.
- Multiple model calls account only for tool messages present in each request.
- Existing `trimToolResults` still runs after reduction and keeps call/result pairing.
- Context compiler on and off both use the same live reduced result.

### End-to-end tests

- An oversized passing verification result is materially smaller in the second model request while the exported transcript retains the canonical result.
- An oversized failing verification result retains the failing command, exit code, error tail, and artifacts.
- A multi-file search retains source diversity and prompts narrowing when matches are omitted.
- A Computer Use observation retains the observation ID and actionable controls needed for the next action.

### Release measurements

For fixtures whose known-tool output exceeds the threshold:

- Median tool-result tokens sent fall at least 40%.
- No deterministic acceptance result changes.
- No increase in tool re-runs caused by missing result context.
- No increase in unsupported completion claims.
- Stored transcript/evidence hashes are identical with the flag off and on.

These are Phase 4b measurements, not authority to enable by default. The parent spec's full paired benchmark remains the release gate.

## Review focus

- Canonical output accidentally replaced by reduced output in storage, hooks, evidence, export, or UI.
- A reducer hiding a failure, denial, non-zero exit, artifact, observation/session ID, source location, or truncation notice.
- A generic reducer being applied to arbitrary MCP/plugin semantics.
- Reduction causing extra tool calls that erase the token saving.
- Savings accounting measuring results no longer present in the request.
- Flag-off requests differing from `master`.
- Reducer work adding meaningful latency to the synchronous tool loop.

## Deferred Phase 4 work

- Graph and FTS5 retrieval of repository evidence.
- Progressive phase-specific tool loading.
- Plugin-provided reducer contracts and schema versioning.
- Retrieval of raw vault objects by the model.
- Benchmark-driven dynamic budgets or relevance models.

These remain separate so reducer quality and savings can be attributed without confounding changes.
