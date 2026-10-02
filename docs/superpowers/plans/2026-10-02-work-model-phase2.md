# Work Model and Verification Ledger Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Persist a structured Work record per objective (goal, criteria, artifacts, verification attempts, failures) from engine-observable events, readable through two read-only protocol methods, with zero change to chat, prompts or model requests.

**Architecture:** Protocol types plus migration `0013` and a thin store layer; a new `internal/work` package owns the recording and closing rules and is keyed by thread ID; the engine calls it at three seams (turn start in `runTurn`, the `observe` closure in `runTool`, `finishTurn`) and the server exposes `work/list` and `work/get`.

**Tech Stack:** Go 1.24, SQLite via the existing store and numbered migrations, `encoding/json`, `crypto/sha256`.

**Spec:** `docs/superpowers/specs/2026-10-02-work-model-phase2-design.md` (parent: `2026-09-30-quality-first-token-efficiency-design.md`, Phase 2).

## Global Constraints

- Migration is additive: `CREATE TABLE IF NOT EXISTS`, timestamps RFC3339 `TEXT` via `store.FormatTime`, IDs via `store.NewID`.
- Work status is `open | completed | abandoned`; workflow depth is `direct | guided` (`designed` is never assigned).
- Node kinds used: `goal`, `criterion`, `artifact`, `fact`; edge relations: `requires` (goal → criterion), `serves` (artifact → goal).
- Verification attempt status is `passed | failed | blocked | not_run`; attempts are append-only (no update or delete API).
- Evidence stores summary, content hash and provenance only; verification output summary is capped at 2 KB (2048 bytes); no raw output is stored.
- Recording is best-effort: an error is logged and counted, and never fails the turn or the tool call.
- No change to chat items, notifications, the system prompt or model requests: Phase 1 golden prompts and `request_baseline.json` must stay byte-identical.
- A failed or denied tool records a `fact` node but does not by itself keep a work open.
- Tests run in a cloud copy where Go modules are fetched with `GOPROXY=direct GOSUMDB=off GOFLAGS=-buildvcs=false`.

## Review Focus

- A file edited twice in one work → one `artifact` node (revision bumped), not duplicates. (Task 2)
- `verification.run` with a command that matches no planned criterion → attempt stored with NULL criterion; it never affects closing. (Task 2)
- Non-JSON, empty or truncated output from `verification.plan` / `verification.run` / `browser.verify` → no panic, nothing recorded, tool result unaffected. (Task 2)
- Store write failure (closed database) → turn and tool call still succeed and the failure counter increments. (Task 3)
- Interrupted, paused, or failed turns leave the work open; a thread archived while open → `abandoned`; a thread deleted → works cascade; a no-project chat still gets a work with empty `project_id`. (Tasks 1 and 3)

---

### Task 1: Protocol types, migration and store layer

**Files:**
- Create: `internal/protocol/work.go`, `internal/store/migrations/0013_work_model.sql`, `internal/store/work.go`
- Test: `internal/store/work_test.go`

**Interfaces:**
- Consumes: `store.NewID`, `store.FormatTime`, `store.ParseTime`, `store.ErrNotFound`.
- Produces (protocol): string constants `WorkOpen`, `WorkCompleted`, `WorkAbandoned`, `DepthDirect`, `DepthGuided`, `NodeGoal`, `NodeCriterion`, `NodeArtifact`, `NodeFact`, `RelRequires`, `RelServes`, `AttemptPassed`, `AttemptFailed`, `AttemptBlocked`, `AttemptNotRun`, `EvidenceFileChange`, `EvidenceVerificationOutput`, `EvidenceToolError`; structs
  `Work{ID, ThreadID, ProjectID, Kind, Status, WorkflowDepth, Goal string; CreatedAt time.Time; CompletedAt *time.Time}`,
  `WorkNode{ID, WorkID, Kind, Title string; Content json.RawMessage; Status string; Confidence float64; Revision int; ValidFrom time.Time; ValidUntil *time.Time; SupersededBy string; CreatedAt, UpdatedAt time.Time}`,
  `WorkEdge{WorkID, FromNodeID, Relation, ToNodeID string}`,
  `Evidence{ID, WorkID, NodeID, Kind, SourceURI, SourceRevision, ContentHash, Summary string; Confidence float64; ObservedAt time.Time; StaleAt *time.Time}`,
  `VerificationAttempt{ID, WorkID, CriterionNodeID, CheckType, Command string; Environment json.RawMessage; Status string; ExitCode *int; EvidenceID string; StartedAt, FinishedAt time.Time}`,
  `WorkDetail{Work Work; Nodes []WorkNode; Edges []WorkEdge; Evidence []Evidence; Attempts []VerificationAttempt}` (all with lower-camel JSON tags, slices never nil in JSON).
- Produces (store, on `*Store`):
  `CreateWork(ctx, w protocol.Work) (protocol.Work, error)` (assigns ID `wrk_…`, CreatedAt, status `open` and depth `direct` when empty),
  `OpenWorkForThread(ctx, threadID string) (protocol.Work, bool, error)`,
  `SetWorkDepth(ctx, workID, depth string) error`, `CloseWork(ctx, workID, status string, at time.Time) error`,
  `AbandonOpenWorks(ctx, threadID string, at time.Time) error`,
  `AddWorkNode(ctx, n protocol.WorkNode) (protocol.WorkNode, error)`, `UpdateWorkNode(ctx, id, status string, revision int, at time.Time) error`,
  `AddWorkEdge(ctx, e protocol.WorkEdge) error`, `AddEvidence(ctx, e protocol.Evidence) (protocol.Evidence, error)`,
  `AddVerificationAttempt(ctx, a protocol.VerificationAttempt) (protocol.VerificationAttempt, error)`,
  `ListWorks(ctx, threadID string) ([]protocol.Work, error)` (newest first), `GetWorkDetail(ctx, workID string) (protocol.WorkDetail, error)` (attempts ordered by `started_at, rowid`; `store.ErrNotFound` when absent).

- [ ] **Step 1: Write failing tests in `work_test.go`** (use `Open(ctx, ":memory:")`, a created thread):
  - `TestWorkMigrationAndRoundTrip`: create a work, a goal node, a criterion node, a `requires` edge, an evidence row, an attempt with `ExitCode` 1 and `Status` `failed`; `GetWorkDetail` returns each with equal fields and `len(Nodes)==2`.
  - `TestOpenWorkForThread`: no work → `ok == false`; after `CreateWork` → `ok == true`; after `CloseWork(..., WorkCompleted, t)` → `ok == false`.
  - `TestAbandonOpenWorksOnlyOpen`: two works in the thread, one completed; `AbandonOpenWorks` leaves the completed one `completed` and the other `abandoned`.
  - `TestDeleteThreadCascadesWorks`: delete the thread via the store's thread deletion; `GetWorkDetail` → `ErrNotFound` and no rows remain in `work_nodes`, `evidence`, `verification_attempts`.
  - `TestNoProjectWorkAllowed`: `ProjectID == ""` is stored as NULL and read back as `""`.
  - `TestAttemptsOrderedAndAppendOnly`: three attempts for one criterion inserted with identical `StartedAt` come back in insertion order.
- [ ] **Step 2: Run** `go test ./internal/store -run "Work" -v` — Expected: FAIL (undefined / missing tables).
- [ ] **Step 3: Implement** the types, the migration (tables, columns, FKs `ON DELETE CASCADE` to `threads`/`works`, `project_id` nullable `ON DELETE SET NULL`, and the five indexes named in the spec) and the store methods. Follow the style of `threads.go` (scan helpers, `b2i` not needed). Empty-string foreign values become NULL for `project_id`, `criterion_node_id`, `evidence_id`, `node_id`.
- [ ] **Step 4: Run** `go test ./internal/store -count=1` — Expected: PASS (also proves older migrations still apply).
- [ ] **Step 5: Commit** `git add internal/protocol/work.go internal/store && git commit -m "feat: add work model schema and store layer"`

### Task 2: Work service (recording and closing rules)

**Files:**
- Create: `internal/work/work.go` (service, Begin/Observe/End), `internal/work/parse.go` (tool-output parsing), `internal/work/rules.go` (unresolved criteria, escalation)
- Test: `internal/work/work_test.go`

**Interfaces:**
- Consumes: Task 1 store methods and protocol types.
- Produces:
  ```go
  type Service struct {
      Store    *store.Store
      Log      *slog.Logger
      Now      func() time.Time   // defaults to time.Now().UTC() when nil
      Failures atomic.Int64       // best-effort recording errors
  }
  type Observation struct {
      Tool   string          // dotted name, e.g. "file.write"
      Args   json.RawMessage
      Output string
      Err    string          // non-empty when the tool failed or was denied
      Risk   string          // "green" | "yellow" | "red"
      Root   string          // project root, "" without a project
  }
  func (s *Service) Begin(ctx context.Context, th protocol.Thread, text string) error
  func (s *Service) Observe(ctx context.Context, threadID string, o Observation) error
  func (s *Service) End(ctx context.Context, threadID, turnStatus string, paused bool) error
  func Unresolved(d protocol.WorkDetail) []string   // titles of unresolved criteria
  func EscalatesToGuided(tool, risk string) bool
  ```
  `Begin`/`Observe`/`End` find the thread's open work themselves; with none open `Observe` and `End` are no-ops returning nil.

- [ ] **Step 1: Write failing tests** (real `store.Open(":memory:")`, injected `Now` that advances 1 s per call):
  - `TestBeginOpensThenContinues`: first `Begin` creates one work (`goal` = text, depth `direct`, one `goal` node); second `Begin` on the same thread creates no new work. After closing the work, `Begin` opens a new one with the new text.
  - `TestBeginNoProjectThread`: thread without `ProjectID` → work with empty `ProjectID`.
  - `TestEscalatesToGuided`: true for `file.write`, `file.edit`, `verification.plan`, `verification.run`, `browser.verify`, and `shell.run` with risk `yellow` or `red`; false for `file.read`, `file.search`, `web.fetch`, and `shell.run` with risk `green` (`verification.run` and `browser.verify` run shell commands above green risk, so they escalate too). `Observe` of `file.write` flips a `direct` work to `guided`; a later `file.read` never flips it back.
  - `TestFileWriteRecordsArtifact`: create `<root>/a.txt`, observe `file.write` args `{"path":"a.txt"}`; detail has one `artifact` node titled `a.txt`, one `serves` edge to the goal node, one `file_change` evidence with `SourceURI "a.txt"` and `ContentHash` equal to the hex SHA-256 of the file. A second `file.edit` of the same path keeps **one** artifact node with `Revision == 2` and adds a second evidence row.
  - `TestPlanCreatesCriteria`: observe `verification.plan` output `{"checks":[{"label":"unit","command":"go test ./..."},{"label":"vet","command":"go vet ./..."}]}` → two `criterion` nodes (status `pending`, `Content` contains the command) each with a `requires` edge from the goal; observing the same plan again adds none.
  - `TestRunRecordsAttemptsAndMatchesCriteria`: after the plan above, observe `verification.run` output `{"status":"failed","results":[{"label":"unit","command":"go test ./...","status":"failed","exit_code":1,"output":"FAIL"},{"label":"vet","command":"go vet ./...","status":"passed"}]}` → two attempts (`failed` with `ExitCode` 1, `passed`), each linked to its criterion and to a `verification_output` evidence row; criterion statuses become `failed` and `passed`.
  - `TestAdHocRunHasNullCriterion` (Review Focus): `verification.run` with a command matching no criterion → attempt with `CriterionNodeID == ""`, and `Unresolved` is unaffected.
  - `TestOutputSummaryCappedAt2KB`: result output of 10,000 bytes → evidence `Summary` length ≤ 2048.
  - `TestBrowserVerifyAttempt`: output `{"status":"not_run","command":"npx playwright test","reason":"requires isolated compute"}` → one attempt with `CheckType "browser"`, `Status "not_run"`.
  - `TestFailedToolRecordsFactOnly`: `Observe` with `Err "boom"` for `file.search` → one `fact` node and one `tool_error` evidence; the work still resolves on a completed turn.
  - `TestMalformedOutputIgnored` (Review Focus): `verification.plan`/`verification.run`/`browser.verify` with `""`, `"not json"` and `"{"` output → nil error, no criteria/attempts/evidence added.
  - `TestEndRules`: table — completed + no criteria → `completed` with `completed_at` set; completed + failed criterion → `open`; completed + pending (never run) criterion → `open`; completed + all passed → `completed`; status `interrupted`, `failed`, or `paused == true` → `open`.
  - `TestFileChangeAfterPassReopens`: plan → run all passed → `End` would complete; then observe `file.edit` (later clock) → `Unresolved` lists the criteria and `End` leaves the work `open`; re-running the checks (later attempt) resolves it again.
- [ ] **Step 2: Run** `go test ./internal/work -v` — Expected: FAIL (package does not compile).
- [ ] **Step 3: Implement.** `Begin` = `OpenWorkForThread` else `CreateWork` + goal node. `Observe` dispatches on `o.Tool`: apply `EscalatesToGuided` first (only when `Err == ""` or tool is verification), then `file.write`/`file.edit` (path from args; hash via `os.ReadFile(filepath.Join(Root, path))`, empty hash if unreadable), `verification.plan`, `verification.run`, `browser.verify` (parse with `encoding/json`; on parse error return nil), and the failed-tool fact path. Criterion match is exact command-string equality. `Unresolved`: a criterion is unresolved if it has no attempt, its latest attempt (max `StartedAt`, ties by slice order) is not `passed`, or a `file_change` evidence has `ObservedAt` after that attempt's `FinishedAt`. `End` closes via `CloseWork(..., WorkCompleted, now)` only when `turnStatus == protocol.TurnCompleted && !paused && len(Unresolved(d)) == 0`. Every public method increments `Failures` and logs on store error while still returning it.
- [ ] **Step 4: Run** `go test ./internal/work -count=1 -v` — Expected: PASS (all tests above).
- [ ] **Step 5: Commit** `git add internal/work && git commit -m "feat: record work, criteria, artifacts and verification attempts"`

### Task 3: Engine wiring

**Files:**
- Modify: `internal/engine/engine.go` (`Engine` struct field `Work *work.Service`, set in `New`; `UpdateThread` ~L341; `pausedTurns` bookkeeping), `internal/engine/turn.go` (`runTurn` after `log := ...` ~L292; the `pause` closure ~L293; `runTool` `observe` closure ~L947 and the `risk, summary := tool.Assess` line ~L974; `finishTurn` before `release()` ~L1073)
- Test: `internal/engine/work_test.go`

**Interfaces:**
- Consumes: Task 2 `Service`, `Observation`, `Begin`, `Observe`, `End`; `Engine.Store`, `Engine.Log`.
- Produces: `Engine.Work *work.Service`; `(e *Engine) markPaused(turnID string)` and `(e *Engine) takePaused(turnID string) bool` over an `e.pausedTurns map[string]bool` guarded by `e.mu`; `takePaused` returns and clears the flag and is called only from `finishTurn`.

- [ ] **Step 1: Write failing tests** (package `engine`, reuse `pluginHookEngine(t)`; set `e.Work = &work.Service{Store: st, Log: e.Log}`; register `engineTestTool`s named `file.write`, `verification.plan`, `verification.run` in `e.Tools` with canned JSON outputs; call `e.runTool` and `e.finishTurn` directly):
  - `TestRunToolObservedByWork`: `Begin`, then `e.runTool` on a `verification.plan` test tool → detail has the criteria; a tool that returns an error → one `fact` node; a denied tool (`forbidden: true`) → one `fact` node.
  - `TestFinishTurnClosesOrKeepsOpen`: after a passing run `e.finishTurn(..., nil, release)` → work `completed`; with a failing run → `open`, and a new `Begin` continues the same work ID; with `context.Canceled` → status `interrupted`, work stays `open`; with `e.markPaused(turn.ID)` first → stays `open`.
  - `TestRecordingFailureDoesNotFailTool`: close the store's DB after `Begin`, run a tool → `toolRunResult` is unchanged (`Output` as returned, `IsError` false) and `e.Work.Failures.Load() > 0`.
  - `TestArchiveAbandonsOpenWork`: open work, `e.UpdateThread(ctx, th.ID, map[string]any{"archived": true})` → work `abandoned`.
  - `TestWorkDoesNotChangePrompt`: run the existing Phase 1 golden tests unchanged (`go test ./internal/engine -run SystemPrompt`).
- [ ] **Step 2: Run** `go test ./internal/engine -run "Work" -v` — Expected: FAIL (field/methods undefined).
- [ ] **Step 3: Implement the wiring.** In `New`, set `Work: &work.Service{Store: o.Store, Log: o.Logger}`. In `runTurn` call `e.Work.Begin(sctx, th, p.Text)` (ignore the error; the service logs and counts). In `runTool` declare `riskLevel := ""` before `observe`, assign `riskLevel = string(risk)` after assessment, and inside `observe` call `e.Work.Observe(sctx, th.ID, work.Observation{Tool: name, Args: call.Args, Output: output, Err: toolError, Risk: riskLevel, Root: scopeRoot(ctx)})` where `scopeRoot` returns `tools.ScopeFrom(ctx).Root` or `""`. The `pause` closure calls `e.markPaused(turn.ID)`. `finishTurn` calls `e.Work.End(ctx, th.ID, turn.Status, e.takePaused(turn.ID))` after `Store.FinishTurn` and before `release()`. `UpdateThread` calls `Store.AbandonOpenWorks` when `cols["archived"] == true`. All calls tolerate a nil `e.Work` (tests that build `Engine` literals).
- [ ] **Step 4: Run** `go vet ./... && go test ./... -count=1` — Expected: PASS everywhere, including Phase 1 golden prompt and baseline tests.
- [ ] **Step 5: Commit** `git add internal/engine && git commit -m "feat: wire work recording into turn and tool paths"`

### Task 4: Protocol methods, routes and end-to-end check

**Files:**
- Modify: `internal/protocol/methods.go` (method constants and param/result types), `internal/engine/engine.go` (`ListWorks`, `GetWork`), `internal/server/routes.go` (two routes next to the approval routes ~L216), `GO_ENGINE.md` (short "Work record" paragraph)
- Test: `internal/server/work_e2e_test.go`

**Interfaces:**
- Consumes: Task 1 `ListWorks`/`GetWorkDetail`, Task 3 wiring.
- Produces: `protocol.MethodWorkList = "work/list"`, `protocol.MethodWorkGet = "work/get"`; `WorkListParams{ThreadID string "threadId"}`, `WorkListResult{Works []Work "works"}`, `WorkGetParams{WorkID string "workId"}` (result is `WorkDetail`); `(e *Engine) ListWorks(ctx, threadID string) (protocol.WorkListResult, error)` (empty slice, never nil), `(e *Engine) GetWork(ctx, workID string) (protocol.WorkDetail, error)` (unknown ID → `protocol.CodeInvalidParams` error).

- [ ] **Step 1: Write failing e2e tests** (reuse `newHarness`, `addKey`, `toolReply`, `textReply`, `waitTurn`):
  - `TestWorkListEmptyThread`: new thread → `work/list` returns `{"works":[]}` (JSON array, not null).
  - `TestWorkRecordedForEditTurn`: project thread; fake replies `toolReply("file__write", {"path":"notes.txt","content":"hi\n"})` then `textReply("Done.")`; after the turn completes `work/list` has one work with status `completed`, depth `guided`, goal equal to the user text; `work/get` shows one `artifact` node titled `notes.txt` and one `file_change` evidence.
  - `TestWorkContinuesAcrossTurnsUntilResolved`: second turn in the same thread after a completed one opens a **new** work (two works, newest first); a thread whose first turn is interrupted (`turn/interrupt` mid-turn) keeps its work `open` and the next turn does not create another.
  - `TestWorkGetUnknown`: `work/get` with a bad ID → error code `invalid params`.
  - `TestChatOutputUnchanged`: the item kinds and count of a plain text turn are identical with and without the work recorder (assert the item list for a one-message turn is exactly user message + agent message).
- [ ] **Step 2: Run** `go test ./internal/server -run "Work" -v` — Expected: FAIL (unknown method).
- [ ] **Step 3: Implement** the constants, types, engine methods and two `bind(...)` routes mirroring `MethodApprovalList`; add the `GO_ENGINE.md` paragraph (what is recorded, `work/list`, `work/get`, quiet by default).
- [ ] **Step 4: Run** `go vet ./... && go test ./... -count=1` — Expected: PASS across all packages.
- [ ] **Step 5: Commit** `git add internal/protocol internal/engine internal/server GO_ENGINE.md && git commit -m "feat: expose work/list and work/get"`
