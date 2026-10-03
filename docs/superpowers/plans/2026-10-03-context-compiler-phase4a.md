# Context Compiler (Phase 4a) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Send the model a compiled work packet, evidence packet and short interaction tail instead of replaying the transcript, behind a flag, with a fallback to today's history path whenever compilation is not trustworthy.

**Architecture:** A pure `internal/ctxcompiler` package turns a `protocol.WorkDetail` plus the thread's items into `[]llm.Message`. The engine reads those two things, calls `Compile` before each model call in the turn loop, and uses `e.history(...)` when the flag is off, no work is open, or `Compile` declines. Nothing about the system prompt, tool specs, compaction or tool-result trimming changes.

**Tech Stack:** Go 1.24, SQLite via `internal/store`, `internal/llm` message types, existing `internal/engine` turn loop.

**Spec:** `docs/superpowers/specs/2026-10-03-context-compiler-phase4a-design.md`

## Global Constraints

- Zero friction: no new approvals, prompts, chat items, notifications or required settings; nothing may block a turn. A compiler error, declined compile or panic is logged, counted, and falls back to history.
- `internal/ctxcompiler` is pure: it imports only `internal/protocol`, `internal/llm`, `internal/vault` and the standard library. No store, no engine, no context, no I/O.
- Flag `models.context_compiler` defaults to false; with it false the request must be byte-identical to today's.
- Token estimates use the existing estimator only: `llm.EstimateTokens` via the engine's `tokens`/`estimateMessageTokens`.
- Packet budget is 15% of the model context window; tail budget is 25%. Tail length is 10 user/assistant pairs. All three are package constants, not config.
- Text entering a packet passes through `vault.Redact`.
- P0 (goal, criteria and statuses, unresolved failures) is never dropped.
- Commit messages end with the two trailer lines used in this repo's recent commits.

## Review Focus

- A packet that presents a stale or superseded result as current (Task 2 status-rendering tests; Task 3 active-evidence test).
- Tail selection dropping a message the current request depends on — a bare "yes", "the second one", an answer to a mid-turn clarification (Task 4 answered-question tests).
- The fallback not triggering when the compiled request is worse, or triggering so often nothing is saved (Task 5 sanity tests; Task 6 size-floor e2e test).
- A secret in a row written before Phase 3's redaction reaching a packet (Task 2 redaction test).
- Role alternation or a non-user first message breaking a provider (Task 4 alternation tests).
- Per-call recompilation adding noticeable latency inside a long tool loop (Task 6 engine test asserting compile count and no extra store reads beyond work detail and items).

---

### Task 1: Package skeleton, types and budget arithmetic

**Files:**
- Create: `internal/ctxcompiler/compiler.go`
- Test: `internal/ctxcompiler/compiler_test.go`

**Interfaces:**
- Consumes: `protocol.WorkDetail`, `protocol.Item`, `llm.Message`, `llm.EstimateTokens`.
- Produces:
  - `type Input struct { Detail protocol.WorkDetail; Items []protocol.Item; TurnID string; Window int; HistoryTokens int }`
  - `type Drop struct { Class, Reason string; Count int }`
  - `type Report struct { Criteria, Evidence, TailMessages int; WorkPacketTokens, EvidencePacketTokens, TailTokens int; Drops []Drop; Declined string }`
  - `type Result struct { Messages []llm.Message; Report Report }`
  - `func Compile(in Input) (Result, bool)` — in this task it returns `Result{}, false` with `Report.Declined` set for every input; later tasks fill it in.
  - `const packetFraction = 0.15`, `const tailFraction = 0.25`, `const tailPairs = 10`
  - `func packetBudget(window int) int`, `func tailBudget(window int) int` — `int(float64(window) * fraction)`; a window of 0 or less yields 0, which means unbounded (used by tests and by models with no declared window).
  - `func estimate(msgs []llm.Message) int` — sum of `llm.EstimateTokens` over text parts; an image part counts 1500, matching `engine.estimateMessageTokens`.

- [ ] **Step 1: Write the failing tests**

```go
func TestBudgetsFromWindow(t *testing.T) {
	if got := packetBudget(200000); got != 30000 {
		t.Fatalf("packetBudget = %d, want 30000", got)
	}
	if got := tailBudget(200000); got != 50000 {
		t.Fatalf("tailBudget = %d, want 50000", got)
	}
	if packetBudget(0) != 0 || tailBudget(-1) != 0 {
		t.Fatal("a non-positive window must yield 0, meaning unbounded")
	}
}

func TestEstimateCountsTextAndImages(t *testing.T) {
	msgs := []llm.Message{
		llm.Text(llm.RoleUser, strings.Repeat("a", 400)),
		{Role: llm.RoleUser, Parts: []llm.Part{{Type: "image", MimeType: "image/png", DataB64: "x"}}},
	}
	if got, want := estimate(msgs), 100+1500; got != want {
		t.Fatalf("estimate = %d, want %d", got, want)
	}
}

func TestCompileDeclinesWithoutAGoal(t *testing.T) {
	res, ok := Compile(Input{Window: 200000})
	if ok || res.Report.Declined == "" {
		t.Fatalf("ok = %v, declined = %q", ok, res.Report.Declined)
	}
}
```

- [ ] **Step 2: Run** `go test ./internal/ctxcompiler -run "Budgets|Estimate|DeclinesWithout" -count=1` — Expected: FAIL, build error on undefined `packetBudget`, `estimate`, `Compile`.

- [ ] **Step 3: Implement** the types, the three constants, `packetBudget`, `tailBudget`, `estimate`, and a `Compile` that declines with `Report.Declined = "no goal"` whenever `in.Detail.Work.Goal` is empty.

- [ ] **Step 4: Run** the same command — Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/ctxcompiler && git commit -m "feat: add context compiler package skeleton and budgets"
```

---

### Task 2: Work packet

**Files:**
- Modify: `internal/ctxcompiler/compiler.go`
- Create: `internal/ctxcompiler/work_packet.go`
- Test: `internal/ctxcompiler/work_packet_test.go`

**Interfaces:**
- Consumes: Task 1 types; `protocol.NodeCriterion`, `protocol.StatusStale`, `work.StatusSuperseded` value `"superseded"` (compare against the string literal, not the `work` package, to keep this package free of that dependency), `protocol.AttemptPassed`.
- Produces: `func workPacket(d protocol.WorkDetail) (text string, criteria int)` — the `## Work` section of the synthetic message.

Rendering, exactly:

```text
## Work
Goal: <goal>
Criteria:
- <title> — <status> (`<command>`)
- <title> — needs re-run, a change landed after it passed (`<command>`)
Unresolved:
- <title>: <attempt summary>
Changed files: a.go, b.go
```

Rules: superseded criteria are omitted entirely; a criterion whose status is `stale` renders with the `needs re-run` wording and never the word `passed`; `Unresolved` lists, for each criterion whose latest attempt is not `passed`, that attempt's `Summary`; `Changed files` lists the distinct `SourceURI` of `file_change` evidence, sorted, omitted when empty; every rendered string passes through `vault.Redact`.

- [ ] **Step 1: Write the failing tests**

```go
func TestWorkPacketRendersCriteriaAndStatuses(t *testing.T) // goal line present; passed criterion shows "passed"; superseded criterion absent
func TestStaleCriterionNeverRendersAsPassed(t *testing.T)    // text contains "needs re-run" and not "passed"
func TestUnresolvedListsLatestFailedAttemptSummary(t *testing.T)
func TestChangedFilesAreDistinctAndSorted(t *testing.T)
func TestWorkPacketRedactsOldSecrets(t *testing.T) // criterion command "API_TOKEN=sk-abcdefghijklmnopqrstuvwxyz0123 npm test" -> no "sk-abcdef" in output
```

- [ ] **Step 2: Run** `go test ./internal/ctxcompiler -run "WorkPacket|Stale|Unresolved|ChangedFiles" -count=1` — Expected: FAIL (undefined `workPacket`).

- [ ] **Step 3: Implement** `workPacket` in `work_packet.go`.

- [ ] **Step 4: Run** the same command — Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/ctxcompiler && git commit -m "feat: render the work packet"
```

---

### Task 3: Evidence packet and active evidence

**Files:**
- Create: `internal/ctxcompiler/evidence_packet.go`
- Test: `internal/ctxcompiler/evidence_packet_test.go`

**Interfaces:**
- Consumes: Task 1 types; `protocol.AvailUnavailable`, `protocol.EvidenceVerificationOutput`, `protocol.EvidenceToolError`.
- Produces:
  - `func evidencePacket(d protocol.WorkDetail, active []protocol.Evidence) (text string, rows int, p2 []int)` — the `## Evidence` section. `p2` holds the indexes of lines that may be dropped first (passing-check lines).
  - `const maxToolErrors = 5`

Active evidence is **not** recomputed here: the engine passes `work.ActiveEvidence(d)` in. `Input` gains `Active []protocol.Evidence`; when it is nil the compiler treats every non-stale evidence row as active, so the package stays usable without the engine.

Rendering: a failed check line is `- FAILED <sourceUri>: <summary> [full output: vault <hash[:8]>]`, with the bracket omitted when `VaultHash` is empty; a passing check line is `- ok <sourceUri>`; an unavailable row renders `- FAILED <sourceUri>: <summary> [full output not retained]`; tool errors render `- tool <sourceUri> failed: <summary>`, newest first, at most `maxToolErrors`.

- [ ] **Step 1: Write the failing tests**

```go
func TestEvidencePacketIncludesOnlyActiveRows(t *testing.T)
func TestFailedCheckCarriesVaultReference(t *testing.T)
func TestMissingHashYieldsNoReference(t *testing.T)
func TestUnavailableRowIsMarkedNotRetained(t *testing.T)
func TestToolErrorsAreNewestFirstAndCapped(t *testing.T) // 7 rows in, 5 out, newest first
func TestPassingLinesAreMarkedP2(t *testing.T)            // p2 indexes point at "ok " lines only
```

- [ ] **Step 2: Run** `go test ./internal/ctxcompiler -run "Evidence|FailedCheck|MissingHash|Unavailable|ToolErrors|PassingLines" -count=1` — Expected: FAIL (undefined `evidencePacket`).

- [ ] **Step 3: Implement** `evidencePacket` and add `Active []protocol.Evidence` to `Input`.

- [ ] **Step 4: Run** the same command — Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/ctxcompiler && git commit -m "feat: render the evidence packet from active evidence"
```

---

### Task 4: Interaction tail

**Files:**
- Create: `internal/ctxcompiler/tail.go`
- Test: `internal/ctxcompiler/tail_test.go`

**Interfaces:**
- Consumes: Task 1 types; `protocol.ItemUserMessage`, `protocol.ItemAgentMessage`, `protocol.ItemInboundEvent`, `protocol.ItemContextCompaction`, `protocol.ItemToolCall`, `protocol.ItemFileChange`, `protocol.ItemCompleted`, `protocol.ItemInProgress`.
- Produces: `func tail(items []protocol.Item, currentTurn string, budget int) (msgs []llm.Message, dropped int)`

Rules, in order:
1. Skip items whose `TurnID == currentTurn` or whose `Status == protocol.ItemInProgress`, as `historyMessages` does today.
2. If a `contextCompaction` item exists, start from the newest one: its text becomes the single first user message, `"Earlier conversation summary (the full transcript remains visible in UMCode):\n" + text`, and nothing before it is considered.
3. Keep only `userMessage`, `inboundEvent` (as user) and completed `agentMessage` (as assistant). `toolCall` and `fileChange` items are dropped and counted.
4. Keep the newest `tailPairs` user messages and the assistant messages between them.
5. Answered-question rule: if the last non-empty line of the newest kept assistant message ends with `?`, also keep that message and the next user message after it when either fell outside step 4.
6. Merge consecutive same-role text with `\n\n`, drop leading non-user messages, and if the result ends with a user message append an assistant `"(no reply)"` — matching today's `appendText` and `history` behavior.
7. If `budget > 0` and the estimate exceeds it, drop whole oldest pairs until it fits, never dropping the newest pair.

- [ ] **Step 1: Write the failing tests**

```go
func TestTailKeepsLastTenPairs(t *testing.T)               // 30 pairs in, 10 user messages out
func TestTailDropsToolAndFileChangeItems(t *testing.T)     // dropped count matches, no "[earlier tool call" text
func TestTailKeepsAnsweredQuestionOutsideWindow(t *testing.T) // old assistant "...which one?" + "the second one" survive
func TestTailStartsAtNewestCompaction(t *testing.T)
func TestTailStartsWithUserAndAlternates(t *testing.T)
func TestTailSkipsCurrentTurnAndInProgress(t *testing.T)
func TestTailDropsOldestPairsToFitBudget(t *testing.T)
```

- [ ] **Step 2: Run** `go test ./internal/ctxcompiler -run Tail -count=1` — Expected: FAIL (undefined `tail`).

- [ ] **Step 3: Implement** `tail`.

- [ ] **Step 4: Run** the same command — Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/ctxcompiler && git commit -m "feat: build the interaction tail"
```

---

### Task 5: Compile, priority dropping and sanity checks

**Files:**
- Modify: `internal/ctxcompiler/compiler.go`
- Test: `internal/ctxcompiler/compile_test.go`

**Interfaces:**
- Consumes: Tasks 1–4.
- Produces: the real `Compile`. Message layout: one `llm.RoleUser` message `"Current work state\n\n" + workPacket + "\n\n" + evidencePacket`, then the tail. The caller appends the new user message.

Order of operations:
1. Decline with `"no goal"` when the goal is empty.
2. Build the work packet (P0) and the evidence packet.
3. If the two together exceed `packetBudget(Window)`, drop the `p2` lines from the evidence packet, then the oldest P1 lines (tool errors, oldest first), recording a `Drop` per class with reason `"packet budget"`.
4. If the work packet alone still exceeds the budget, decline with `"P0 over budget"`.
5. Build the tail with `tailBudget(Window)`.
6. If `HistoryTokens > 0` and `estimate(messages) >= HistoryTokens`, decline with `"not smaller than history"`.
7. Fill `Report` and return.

- [ ] **Step 1: Write the failing tests**

```go
func TestCompileLayoutIsPacketThenTail(t *testing.T)      // msgs[0] is user and contains "## Work" and "## Evidence"; msgs[1:] is the tail
func TestCompileDropsP2BeforeP1(t *testing.T)             // tiny window: "ok " lines gone, FAILED lines kept, Drops records both classes in order
func TestCompileDeclinesWhenP0OverBudget(t *testing.T)    // Declined == "P0 over budget"
func TestCompileDeclinesWhenNotSmallerThanHistory(t *testing.T)
func TestCompileReportCountsTokensAndRows(t *testing.T)
```

- [ ] **Step 2: Run** `go test ./internal/ctxcompiler -count=1` — Expected: FAIL on the new tests only.

- [ ] **Step 3: Implement** `Compile` per the order above.

- [ ] **Step 4: Run** `go test ./internal/ctxcompiler -count=1` — Expected: PASS, whole package.

- [ ] **Step 5: Commit**

```bash
git add internal/ctxcompiler && git commit -m "feat: compile packets and tail with priority dropping"
```

---

### Task 6: Engine wiring, config flag and accounting

**Files:**
- Modify: `internal/engine/turn.go`, `internal/engine/engine.go`, `internal/engine/accounting.go`, `internal/config/config.go`, `GO_ENGINE.md`
- Test: `internal/engine/compiler_test.go`, `internal/config/config_test.go`, `internal/server/compiler_e2e_test.go`

**Interfaces:**
- Consumes: Task 5 `ctxcompiler.Compile`, `work.ActiveEvidence`, `store.OpenWorkForThread`, `store.GetWorkDetail`, `store.ListItems`.
- Produces:
  - `config.ModelsConfig` gains `ContextCompiler bool` (`yaml:"context_compiler"`), default false.
  - `(e *Engine) compileMessages(ctx context.Context, th protocol.Thread, turnID string, window, historyTokens int) ([]llm.Message, bool)` — returns false whenever the flag is off, no work is open, a store read fails, `Compile` declines, or the compiler panics. A panic is recovered here; every false path with a cause logs at debug and increments `e.compilerFailures`.
  - `Engine.compilerFailures atomic.Int64`.
  - `RequestBreakdown` gains `WorkPacketTokens`, `EvidencePacketTokens int`, subtracted from `ConversationTokens` so `TotalTokens` is unchanged; `measureRequest` gains a `packets RequestPackets` parameter (`type RequestPackets struct{ Work, Evidence int }`), zero when the history path ran.

Wiring in `runTurn`: after `msgs, err := e.history(...)` succeeds, compute `historyTokens := estimateMessageTokens(msgs)`; if `compileMessages` returns true, replace `msgs` with its result. Inside the tool loop, before `req.Messages = msgs`, recompile the same way from the current store state, keeping the live tool-result messages of this turn appended after the compiled prefix. Automatic compaction, `trimToolResults` and the system prompt stay exactly as they are.

- [ ] **Step 1: Write the failing tests**

```go
// internal/engine/compiler_test.go
func TestFlagOffRequestUnchanged(t *testing.T)            // compare req.Messages against e.history output, byte for byte
func TestFlagOnUsesPacketsAndShortTail(t *testing.T)
func TestFlagOnWithoutOpenWorkUsesHistory(t *testing.T)
func TestCompileDeclineFallsBackAndCounts(t *testing.T)   // goal cleared in the store -> history used, compilerFailures == 1
func TestCompilerPanicRecovered(t *testing.T)             // injected panic hook -> history used, turn completes
func TestMidTurnEditMakesCriterionStaleInNextCall(t *testing.T) // pass, file.edit, next compiled request says "needs re-run"
func TestBreakdownReportsPacketTokens(t *testing.T)

// internal/config/config_test.go
func TestContextCompilerFlagDefaultsFalse(t *testing.T)

// internal/server/compiler_e2e_test.go
func TestChatItemsUnchangedWithCompilerOnAndOff(t *testing.T)
func TestCompiledRequestIsSmallerThanHistory(t *testing.T) // same thread both ways; compiled estimate < history estimate
```

The panic hook is an unexported package variable in `internal/engine`, `compileHook func()`, called at the top of `compileMessages` and set only by tests.

- [ ] **Step 2: Run** `go test ./internal/engine ./internal/config ./internal/server -run "Flag|Compile|Compiler|MidTurnEdit|Breakdown|ChatItemsUnchanged" -count=1` — Expected: FAIL.

- [ ] **Step 3: Implement** the config flag, `compileMessages`, the two call sites in `runTurn`, the accounting fields, and a `GO_ENGINE.md` paragraph under the work-record section: what the compiler sends, that the flag is off by default, that stale criteria are shown as needing a re-run, that the full transcript stays stored and visible, and that a failure falls back silently.

- [ ] **Step 4: Run** `go vet ./... && go test ./... -count=1` — Expected: PASS across all packages.

- [ ] **Step 5: Commit**

```bash
git add internal/engine internal/config internal/server GO_ENGINE.md && git commit -m "feat: wire the context compiler into the turn loop behind a flag"
```
