# Quality-First Token Efficiency — Phase 1 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `UMCODE.md` the only implicit instruction convention and add per-request context-layer and tool-schema token accounting, without changing what the model receives.

**Architecture:** Phase 1 of the spec only. Instruction discovery in `internal/projects/instructions.go` drops global/`AGENTS.md`/`CLAUDE.md` sources. `Engine.systemPrompt` is split into named layers that join to the same string as today; a pure `measureRequest` function reports tokens per layer, tools, conversation and tool results, logged at debug level once per model call. A checked-in golden fixture pins the baseline numbers.

**Tech Stack:** Go 1.24 (`log/slog`, `testing`), existing `llm.EstimateTokens`.

**Spec:** `docs/superpowers/specs/2026-09-30-quality-first-token-efficiency-design.md` (section "Phase 1: instruction and measurement foundation"). Phases 2–6 get separate plans.

## Global Constraints

- Implicit instruction files are only `<project>/UMCODE.md` and applicable nested `UMCODE.md`; stop scanning `AGENTS.md`, `CLAUDE.md`, `~/.umcode/AGENT.md`.
- Remove `agents.context_file` and `AGENT.md.template`; docs, tests, CLI labels and UI copy say `UMCODE.md`.
- Foreign files (`AGENTS.md`, `CLAUDE.md`) are never modified or deleted; no silent compatibility fallback.
- Accounting must not change model behavior: the system prompt string and request contents stay byte-identical.
- Normal chat stays quiet: accounting goes to the debug log only.
- Keep the existing 32 KB per-file instruction cap.
- No new dependencies, tables, or migrations in this phase.

## Review Focus

- `AGENTS.md`/`CLAUDE.md` present but no `UMCODE.md`: no instructions, no error, `Exists == false`.
- A directory named `UMCODE.md`: ignored, not read.
- Non-project chat with a `~/.umcode/AGENT.md` on disk: its contents never reach the prompt.
- Existing `config.yaml` that still has `agents.context_file`: loads without error (yaml decoding is non-strict).
- `UMCODE.md` over 32 KB: still truncated with a source note.
- Layered prompt with empty/whitespace hook context and with no project: joins identically to the old output.

## File Structure

- Modify `internal/projects/instructions.go`: discovery and titles.
- Modify `internal/projects/projects_test.go`, `internal/server/e2e_test.go`: replace foreign-file expectations.
- Modify `internal/config/config.go`, `config.example.yaml`; delete `AGENT.md.template`.
- Modify `internal/engine/engine.go`: layered prompt; prompt wording.
- Create `internal/engine/accounting.go` (+ `accounting_test.go`, `testdata/request_baseline.json`): measurement.
- Modify `internal/engine/turn.go`: log breakdown per model call.
- Modify `cmd/umcode/main.go`, `cmd/umcode/projects.go`, `README.md`, `GO_ENGINE.md`, `app/frontend/e2e/chat.mjs`: copy.

---

### Task 1: UMCODE.md-only instruction discovery

**Files:**
- Modify: `internal/projects/instructions.go` (`instructionNames`, `instructionFiles`, `firstInstructionFile`, `instructionFilesAt`, titles in `InstructionsFor`)
- Modify: `internal/projects/projects_test.go` (`TestInstructionsComposition`)
- Modify: `internal/server/e2e_test.go` (`TestProjectEditsDiffAndRevert` ~L529-560, project-instructions block ~L715-721)
- Modify: `cmd/umcode/main.go:51-53`, `cmd/umcode/projects.go:111,124`, `README.md:11`, `GO_ENGINE.md:63,69,70,204`, `app/frontend/e2e/chat.mjs:60`

**Interfaces:**
- Consumes: none.
- Produces: `InstructionsFor(ctx, p, hint)` returns sources with scopes only `"project"` and `"nested"`; no `"global"` scope exists afterwards.

- [ ] **Step 1: Rewrite `TestInstructionsComposition` first (failing)**
  Fixture: `cfg.Home/AGENT.md`="Global: be terse.", `root/AGENTS.md`="Project: run make test.", `root/web/CLAUDE.md`="Web: use Svelte 5 runes.", plus `root/UMCODE.md`="Project: UMCode guidance." and `root/web/UMCODE.md`="Web: nested UMCode." Assert: composed contains both UMCODE texts (nested only with hint `web/App.svelte`) and contains none of "Global: be terse", "run make test", "Svelte 5 runes"; sources are exactly `[project, nested]` with hint and `[project]` without. Add subtests: (a) only AGENTS.md present → `composed == ""`, `sources` empty, `Instructions(...).Exists == false`; (b) `root/UMCODE.md` is a directory → ignored; (c) 40 KB `UMCODE.md` → source `Error` contains "truncated to 32 KB". Keep the existing assertion that `Instructions(ctx,p,&text)` writes only `UMCODE.md` and leaves AGENTS.md/CLAUDE.md byte-identical.
- [ ] **Step 2: Run** `go test ./internal/projects -run TestInstructionsComposition -v` — Expected: FAIL (global/AGENTS still composed).
- [ ] **Step 3: Implement.** `instructionNames = []string{"UMCODE.md"}`; delete the global entry from `instructionFiles` and the `"global"` title; remove the foreign-file comment, replace with one stating `UMCODE.md` is the sole implicit instruction file. `firstInstructionFile` keeps its signature.
- [ ] **Step 4: Update server e2e tests.** In `TestProjectEditsDiffAndRevert` write `UMCODE.md` instead of `AGENTS.md` and update the comments. In the project-instructions e2e (~L715) replace the CLAUDE.md fallback block with: write `CLAUDE.md` "Prefer small commits.", assert `ins.Composed` does **not** contain it and `ins.Exists == false`.
- [ ] **Step 5: Copy.** `AGENT.md` → `UMCODE.md` in the CLI help/labels (`main.go`, `projects.go` incl. the `-f` flag text and the `"  AGENT.md %s"` label), `README.md`, `GO_ENGINE.md` (rewrite the L63 paragraph: project and nested `UMCODE.md` only, 32 KB cap, re-read on change, `--composed` prints what the agent receives; note foreign files are not read), and `chat.mjs`. Keep the `"agent"`/`"agentmd"` subcommand aliases.
- [ ] **Step 6: Run** `go test ./internal/projects ./internal/server ./cmd/... -count=1` — Expected: PASS.
- [ ] **Step 7: Commit** `git commit -m "feat: read only UMCODE.md as implicit project instructions"`

### Task 2: Remove global AGENT.md and `agents.context_file`

**Files:**
- Modify: `internal/engine/engine.go` (`systemPrompt` tail ~L820-827 and the UMCODE.md sentence ~L783)
- Modify: `internal/config/config.go:67,305-307`; `config.example.yaml:131-135`
- Delete: `AGENT.md.template`
- Test: `internal/engine/engine_prompt_test.go` (create), `internal/config/config_test.go` (add case)

**Interfaces:**
- Consumes: Task 1 (no global scope).
- Produces: `AgentsConfig` without `ContextFile`; `systemPrompt` never reads `Cfg.Home/AGENT.md`.

- [ ] **Step 1: Failing tests.** `TestSystemPromptIgnoresGlobalAgentMD` (engine, no project): write `Home/AGENT.md`="SECRET-GLOBAL", assert prompt lacks it and lacks "User context (AGENT.md)". `TestLegacyContextFileKeyIgnored` (config): YAML containing `agents:\n  context_file: ~/x.md` loads with nil error.
- [ ] **Step 2: Run** both — Expected: first FAIL, second PASS (non-strict yaml) — keep it as a regression guard.
- [ ] **Step 3: Implement.** Delete the global-file read block and the `ContextFile` field/expansion; remove the `context_file` stanza and its comment from `config.example.yaml`; `git rm AGENT.md.template`. In the prompt sentence replace "equivalent in purpose to Codex AGENTS.md" and "do not modify AGENTS.md or CLAUDE.md" with wording that says UMCODE.md is the only project instruction file UMCode reads and that other instruction files are not to be modified. Update the `systemPrompt` doc comment.
- [ ] **Step 4: Run** `go build ./... && go test ./internal/engine ./internal/config -count=1` — Expected: PASS.
- [ ] **Step 5: Commit** `git commit -m "feat: drop global AGENT.md and agents.context_file"`

### Task 3: Layered system prompt (byte-identical)

**Files:**
- Modify: `internal/engine/engine.go` (`systemPrompt`)
- Test: `internal/engine/engine_prompt_test.go`

**Interfaces:**
- Consumes: Task 2 `systemPrompt`.
- Produces:
  - `type promptLayer struct { Name, Text string }`
  - layer name constants `layerCore="core"`, `layerClock="clock"`, `layerSkills="skills"`, `layerPlugin="plugin"`, `layerProject="project"`, `layerInstructions="instructions"`, `layerNotice="notice"` (no-project notice)
  - `func (e *Engine) systemPromptLayers(ctx context.Context, userText string, proj *protocol.Project, instructionHint string, hookContext []string) []promptLayer`
  - `systemPrompt(...)` keeps its signature and returns the layers' texts concatenated.

- [ ] **Step 1: Golden-equivalence test (written against the pre-refactor behavior).** Before touching code, add `TestSystemPromptLayersJoinToPrompt` that for three inputs — no project; project with `UMCODE.md` and hint; project with hook context `["", "  ", "ctx A"]` — builds the engine prompt, and asserts `joined(layers) == systemPrompt(...)` plus that each expected layer name appears in order (`core, clock, skills?, plugin?, project, instructions` / `core, clock, …, notice`). To prove byte-identity against old behavior, first record the three expected strings from the current implementation into `internal/engine/testdata/system_prompt_{noproject,project,hooks}.txt` with the clock line stripped (assert via a helper that replaces the `Current time: …` line with `Current time: <t>`), commit them, then compare after refactor.
- [ ] **Step 2: Run** — Expected: FAIL to compile (`systemPromptLayers` undefined).
- [ ] **Step 3: Implement** `systemPromptLayers`: move each existing `b.WriteString` group into the matching layer, preserving exact text and order; `systemPrompt` becomes a `strings.Builder` join. The clock line is its own layer so later phases can see it breaks prefix caching (do not move or change it now).
- [ ] **Step 4: Run** `go test ./internal/engine -run SystemPrompt -count=1` — Expected: PASS against the recorded strings.
- [ ] **Step 5: Commit** `git commit -m "refactor: expose system prompt as named layers"`

### Task 4: Per-request token accounting and baseline fixture

**Files:**
- Create: `internal/engine/accounting.go`, `internal/engine/accounting_test.go`, `internal/engine/testdata/request_baseline.json`
- Modify: `internal/engine/turn.go` (loop at ~L438-452)

**Interfaces:**
- Consumes: `promptLayer`, `systemPromptLayers` (Task 3); `toolSpecTokens`, `estimateMessageTokens`, `tokens` from `context.go`/`turn.go`.
- Produces:
  ```go
  type RequestBreakdown struct {
      Layers           map[string]int `json:"layers"`           // tokens per prompt layer
      SystemTokens     int            `json:"systemTokens"`     // sum of Layers
      ToolSpecTokens   int            `json:"toolSpecTokens"`
      ToolCount        int            `json:"toolCount"`
      ConversationTokens int          `json:"conversationTokens"` // messages excluding tool results
      ToolResultTokens int            `json:"toolResultTokens"`
      TotalTokens      int            `json:"totalTokens"`
  }
  func measureRequest(layers []promptLayer, req llm.Request) RequestBreakdown
  ```

- [ ] **Step 1: Failing tests** in `accounting_test.go`:
  - `TestMeasureRequestSumsParts`: layers `core`=400 chars, `project`=800 chars; two tool specs; messages = one user text, one assistant with a tool call, one tool message with a 4000-char `Result`. Assert `SystemTokens == 300` (100+200 via `EstimateTokens`), `ToolCount == 2`, `ToolResultTokens == 1000`, and `TotalTokens == SystemTokens+ToolSpecTokens+ConversationTokens+ToolResultTokens`.
  - `TestMeasureRequestEmpty`: zero layers/messages/tools → all zeros, `Layers` non-nil.
  - `TestRequestBaselineGolden`: build a fixed synthetic request (constant strings only, no clock) and compare `json.MarshalIndent(measureRequest(...))` to `testdata/request_baseline.json`; rewrite the file when `UPDATE_GOLDEN=1`.
- [ ] **Step 2: Run** `go test ./internal/engine -run "MeasureRequest|RequestBaseline" -v` — Expected: FAIL (undefined).
- [ ] **Step 3: Implement `measureRequest`** in `accounting.go` as a pure function. Tool-result tokens are `tokens(m.Result)` for `RoleTool` messages; conversation tokens are `estimateMessageTokens(req.Messages) - ToolResultTokens`.
- [ ] **Step 4: Generate fixture** `UPDATE_GOLDEN=1 go test ./internal/engine -run RequestBaselineGolden`, inspect the JSON by eye, rerun without the variable — Expected: PASS.
- [ ] **Step 5: Wire into the turn loop.** Replace `req.System = e.systemPrompt(...)` with building `layers := e.systemPromptLayers(...)`, `req.System = joinLayers(layers)` (add `joinLayers([]promptLayer) string`, also used by `systemPrompt`), and after `trimToolResults` emit `log.Debug("context accounting", "turn", turn.ID, "breakdown", measureRequest(layers, req))` once per model call. No change to `req` contents.
- [ ] **Step 6: Run full verification:** `go vet ./... && go test ./... -count=1` — Expected: all PASS; `git diff --stat` shows no non-test change to request construction beyond the layer join.
- [ ] **Step 7: Commit** `git commit -m "feat: account context-layer and tool-schema tokens per request"`
