# Phase 4c: Deterministic Retrieval Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Recover relevant older context and fresh observed repository excerpts within bounded compiler packets, without extra model calls.

**Architecture:** Add a scoped redacted FTS index and typed observation provenance, then a pure graph/query/selection package. The engine validates canonical sources and filesystem hashes before the compiler renders optional retrieved entries; retrieval failures retain the existing compiler input.

**Tech Stack:** Go 1.24.0 (toolchain go1.24.7), existing ncruces SQLite/FTS5, existing redaction, no new dependencies.

**Spec:** `docs/superpowers/specs/2026-10-10-phase4c-retrieval-design.md` (approved in conversation).

## Global Constraints

- `models.context_retrieval: false`; enabling it requires `models.context_compiler: true`.
- No extra model calls, prompts, notifications, services, network calls, tool execution, vault writes, or approvals for retrieval.
- Retrieval requires an open Work; current-Work graph/evidence and current-thread transcript only. SQL scope precedes ranking and LIMIT.
- Limits: 100 ms; 16 terms of 64 bytes from 8 KiB; depth 2/128 graph nodes; 64 candidates per FTS source; 12 entries; 6,144 tokens or 5% window; 4 KiB/candidate and 512 KiB aggregate.
- Files: 4 files, 256 KiB each, 1 MiB total; excerpts at most 40 lines/4 KiB. No repository walk, symlink traversal, arbitrary query-derived file reads or secret-path reads.
- Fit all rendered retrieval overhead inside the existing packet budget. Never displace P0/required P1, instructions, current input, or live tool results.
- Keep provider usage authoritative, flag-off requests unchanged, and Go version/dependency floors unchanged.
- Implement this plan before Phase 4d. No push/merge without user authorization.

## Review Focus

1. Highly ranked foreign FTS matches must neither leak nor consume the eligible result limit (Task 2).
2. Legacy secrets must not be briefly persisted during migration/index refresh (Task 2).
3. File changes and symlink swaps between observation and request must exclude excerpts (Task 4).
4. Historical assistant assertions must not resurrect obsolete decisions or passing checks (Tasks 3/5).
5. Tiny windows, Unicode and pathological graphs must fall back without losing mandatory state or blocking cancellation (Tasks 3/5).

## File structure and shared types

Create `internal/retrieval/{types,query,graph,select}.go`: pure data/query/ranking only, importing protocol and llm but never store/work/engine. Store and engine may import retrieval. Create `internal/protocol/retrieval.go` for persisted `ObservedExcerpt`: ID, WorkID, EvidenceID, relative Path, WorkspaceRootHash, ContentHash, StartLine, EndLine, Text (redacted), ObservedAt. Root identity is a hash of the effective canonical workspace, not a user-provided workspace name. No absolute paths in stored excerpt text.

`retrieval.Scope{ThreadID,WorkID,ProjectID,TurnID string}`; `Query{Terms []string; FTS string; Paths,Symbols []string}`; `Candidate{ID,Kind,ThreadID,WorkID,ProjectID,Body,SourceRevision,ContentHash,Path string; StartLine,EndLine,Distance int; LexicalScore float64; Historical bool}`; `Entry` has the same provenance fields and body; `Report` has only counts/enumerated reasons and token totals. Kind values: requirement, decision, artifact, evidence, conversation, excerpt. Body limits are bytes before token estimation. Stable source IDs use kind plus canonical ID. Define these once; later tasks consume the exact types.

### Task 1: Define retrieval contracts, limits and safe queries

**Files:**
- Create: `internal/retrieval/types.go`, `internal/retrieval/query.go`, `internal/retrieval/query_test.go`
- Create: `internal/protocol/retrieval.go`
- Modify: `internal/config/config.go`, `internal/config/config_test.go`

**Interfaces:**
- Produces: `retrieval.BuildQuery(request string, taskTitles []string) Query`.
- Produces: shared types above and `Models.ContextRetrieval bool`; Validate enforces dependency.
- Consumes: existing llm token estimator and configuration validation patterns.

- [ ] **Step 1: Write failing tests**

`TestRetrievalConfigDependency`: default false; true without compiler is rejected; true with compiler succeeds; all other flags independent.
`TestBuildQueryBoundedAndSafe` asserts:
```go
if len(q.Terms) > 16 { t.Fatal(q.Terms) }
for _, term := range q.Terms { if len(term) > 64 { t.Fatal(term) } }
if BuildQuery(" OR NEAR() ", nil).FTS != BuildQuery(" OR NEAR() ", nil).FTS { t.Fatal("unstable") }
```
Use table cases for quoted operators, punctuation-only/empty input, invalid UTF-8, 8 KiB truncation and exact paths/symbols; execute generated queries against FTS to prove syntax safety, not only string escaping.

- [ ] **Step 2: Observe RED**

Run: `GOCACHE=/private/tmp/umcode-go-cache go test ./internal/retrieval ./internal/config -run "TestBuildQuery|TestRetrievalConfig" -count=1`. Expected: the named assertions fail because the new behavior is absent. Compilation failure while adding a new API is setup only; obtain a behavioral failure before implementing its body.

- [ ] **Step 3: Implement the deliverable**

Implement normalization/term deduplication with stable order and explicit OR-quoted FTS terms. Invalid UTF-8 or no useful terms produces an empty safe query. Keep exact path/symbol hints separately. Define all spec constants in types.go; no additional user settings.

- [ ] **Step 4: Verify GREEN**

Run the Step 2 command. Expected: exit 0 and all named tests pass. Run existing tests for the changed package before committing; record exact output in the execution ledger.

- [ ] **Step 5: Commit**

Stage only the files above and required test fixtures. Commit message: `feat(retrieval): define bounded query and configuration contracts`.

### Task 2: Add scoped canonical text retrieval and additive index migration

**Files:**
- Create: `internal/store/migrations/0017_context_retrieval.sql`, `internal/store/retrieval.go`, `internal/store/retrieval_test.go`
- Modify: `internal/store/store.go`, `internal/store/work.go`, `internal/store/vault.go` and canonical deletion commands

**Interfaces:**
- Consumes: retrieval.Scope/Query/Candidate and protocol.ObservedExcerpt.
- Produces: `(*Store).SearchRetrieval(ctx context.Context, scope retrieval.Scope, query retrieval.Query) ([]retrieval.Candidate,error)`.
- Produces: `(*Store).RecordDiscoveryObservation(ctx context.Context, evidence protocol.Evidence, excerpts []protocol.ObservedExcerpt) (protocol.Evidence,error)`; it inserts evidence, excerpt rows and redacted index documents in one transaction.
- Internal: `indexRetrievalDocument(ctx context.Context, tx *sql.Tx, kind, id, workID, text string) error`; all canonical text-writing transactions call it.

- [ ] **Step 1: Write failing tests**

`TestRetrievalScopeBeforeLimit`: insert more than 64 high-ranked hits in a foreign thread/project and a matching current hit; result contains the current hit and zero foreign IDs. Include projectless threads and identical project names.
`TestRetrievalMigrationRedactsAndReopens`: migrate legacy canonical text containing a synthetic API token, assert token absent from every indexed body and available results, then reopen and search successfully.
`TestRetrievalIndexCanonicalLifecycle`: node/evidence insert and update, supersession/staleness, deleted Work/thread, rolled-back transaction and stale index text never return an active false source.
`TestRetrievalHistoricalMessages`: only older user/assistant canonical messages in current thread; no raw historical tool output, current-turn rows, or deleted items.

- [ ] **Step 2: Observe RED**

Run: `GOCACHE=/private/tmp/umcode-go-cache go test ./internal/store -run TestRetrieval -count=1`. Expected: the named assertions fail because the new behavior is absent. Compilation failure while adding a new API is setup only; obtain a behavioral failure before implementing its body.

- [ ] **Step 3: Implement the deliverable**

Use ordinary retrieval_documents plus FTS5 shadow index and triggers on the redacted documents table; canonical deletion triggers remove documents. Store source identity/version/hash, not free-form JSON. SQL migration creates schema; a version-17 Go backfill hook in migrate runs within the same transaction before recording completion, streaming bounded batches and redacting each body before insert. Do not perform INSERT SELECT of unredacted text. Canonical writes keep document updates in the same transaction; preserve existing public APIs while extracting transaction-local helpers as needed. Status-only changes need not rewrite text, but queries always rejoin canonical liveness/revision. Query active Work/evidence and current-thread user/assistant items separately, each LIMIT 64 after identity filters. Use SQL byte-length filters and bounded projection before Go allocation; read at most 4 KiB/projected body and 512 KiB aggregate. Canonical oversized text is excluded. No change to global user-facing SearchItems semantics.

- [ ] **Step 4: Verify GREEN**

Run the Step 2 command. Expected: exit 0 and all named tests pass. Run existing tests for the changed package before committing; record exact output in the execution ledger.

- [ ] **Step 5: Commit**

Stage only the files above and required test fixtures. Commit message: `feat(store): add scoped redacted retrieval indexes`.

### Task 3: Select graph and lexical candidates deterministically

**Files:**
- Create: `internal/retrieval/graph.go`, `internal/retrieval/select.go`, corresponding `_test.go` files

**Interfaces:**
- Consumes: Task 1 types; caller-supplied active evidence and stale criterion IDs.
- Produces: `GraphCandidates(detail protocol.WorkDetail, active []protocol.Evidence, stale map[string]bool) ([]Candidate,error)`.
- Produces: `Select(query Query, candidates []Candidate, excludedIDs map[string]bool, budgetTokens int) ([]Entry,Report,error)`.

- [ ] **Step 1: Write failing tests**

`TestGraphRetrievalDepthAndIdentity`: relevant supports/requires/depends_on/implements/verifies reach depth 2, cycles terminate, foreign Work edge errors, >128 nodes yields bounded decline.
`TestSelectDeterministicPriority`: reordered candidates produce byte-identical entries; graph/source/path/symbol relevance precedes lexical rank with ID tie-break. Contradictions remain explicit unresolved data.
`TestSelectRejectsStaleAndHistoricalClaims`: stale evidence excluded; old assistant text remains historical conversation and cannot become decision/evidence kind.
`TestSelectExactBudgetAndDedupe`: <=12 entries, <=6,144 tokens and supplied remaining budget including provenance/headings; excluded IDs/ranges eliminated; conversation drops before relevant optional evidence. Test tiny budget, multibyte bodies and 512 KiB aggregate overflow.

- [ ] **Step 2: Observe RED**

Run: `GOCACHE=/private/tmp/umcode-go-cache go test ./internal/retrieval -count=1`. Expected: the named assertions fail because the new behavior is absent. Compilation failure while adding a new API is setup only; obtain a behavioral failure before implementing its body.

- [ ] **Step 3: Implement the deliverable**

Traverse only typed same-Work edges and typed canonical node content. Treat candidate eligibility before scoring. Return errors for corrupt identity/graph input, not partial trusted output. Lower FTS bm25 scores rank ahead of higher scores only within equal graph/source/path/symbol priority. Render entries through one shared deterministic `Render(entries []Entry) string` used for both estimates and compiler output; add this exact signature here. Label all entries as untrusted data and historical/current, include bounded IDs/revision/hash/path/range. Escape delimiting characters so bodies cannot forge packet headers. Do not elevate confidence based on lexical rank.

- [ ] **Step 4: Verify GREEN**

Run the Step 2 command. Expected: exit 0 and all named tests pass. Run existing tests for the changed package before committing; record exact output in the execution ledger.

- [ ] **Step 5: Commit**

Stage only the files above and required test fixtures. Commit message: `feat(retrieval): select bounded graph and lexical context`.

### Task 4: Capture actual file provenance and validate contained excerpts

**Files:**
- Modify: `internal/tools/rawsink.go`, `internal/tools/builtin.go`, `internal/tools/search.go`, `internal/work/work.go`, `internal/engine/turn.go`, `internal/engine/engine.go`
- Create: `internal/tools/retrieval_observation.go`, `internal/engine/retrieval_files.go`, platform-specific secure reader files and tests

**Interfaces:**
- Consumes: protocol.ObservedExcerpt and Store.RecordDiscoveryObservation.
- Produces: `tools.SetObservedExcerpts(ctx context.Context, excerpts []protocol.ObservedExcerpt)`; RawSink gains `Excerpts []protocol.ObservedExcerpt`.
- Observation gains `Excerpts []protocol.ObservedExcerpt`; engine forwards only built-in successful file.read/search sink metadata.
- Produces: `validateRetrievalFiles(ctx context.Context, root string, candidates []retrieval.Candidate) ([]retrieval.Candidate,error)`.

- [ ] **Step 1: Write failing tests**

`TestObservedFileProvenance`: actual file.read/search populates typed relative path/range/hash and redacted bounded text; unrelated tool output and client work.update cannot forge it. Enabled retrieval works without Designed workflow; flag-off records no new excerpt data.
`TestRetrievalFileRevalidation`: same hash includes excerpt; changed/deleted/binary/oversized file excludes it; root workspace identity mismatch excludes it.
`TestRetrievalContainment`: parent traversal, absolute/foreign instruction paths, denied secret paths, symlink intermediate/final swap, socket and directory produce no file contents.
`TestRetrievalFileLimits`: <=4 files, <=256 KiB each/1 MiB total, <=40 lines/4 KiB excerpt; cancellation stops bounded validation.

- [ ] **Step 2: Observe RED**

Run: `GOCACHE=/private/tmp/umcode-go-cache go test ./internal/tools ./internal/work ./internal/engine -run "TestObservedFile|TestRetrievalFile|TestRetrievalContainment" -count=1`. Expected: the named assertions fail because the new behavior is absent. Compilation failure while adding a new API is setup only; obtain a behavioral failure before implementing its body.

- [ ] **Step 3: Implement the deliverable**

Capture only bytes actually read and successfully returned by built-in tools, before clipping; hash full file bytes where read under the cap. file.search hits obtain bounded full-file hash during existing inspection rather than a second uncontrolled scan. Do not reopen oversized files to create provenance. Persist an observation and excerpts atomically in Work.Observe; keep this path enabled by retrieval independently of Designed workflow. Add `work.Service.ContextRetrieval bool` initialized from the engine configuration; this enables observation capture without Designed workflow. Scope root identity comes from effective execution context, never raw workspace arguments. File read validation uses descriptor-anchored no-follow operations on Darwin/Linux; unsupported platforms omit excerpts without blocking text/graph retrieval. Reuse search directory exclusions and additionally deny .env/.env.*, credentials.json, id_rsa/id_ed25519, *.pem/*.key and .aws/.ssh path components; do not alter explicit user file tool access. Reject AGENT.md/AGENTS.md/CLAUDE.md components; UMCODE.md stays owned by instruction loading and is excluded from excerpt retrieval. Record source mismatch omission, not a fresh inferred claim.

- [ ] **Step 4: Verify GREEN**

Run the Step 2 command. Expected: exit 0 and all named tests pass. Run existing tests for the changed package before committing; record exact output in the execution ledger.

- [ ] **Step 5: Commit**

Stage only the files above and required test fixtures. Commit message: `feat(retrieval): retain and validate observed file excerpts`.

### Task 5: Integrate retrieval atomically with compiler budgets and diagnostics

**Files:**
- Create: `internal/engine/retrieval.go`, `internal/engine/retrieval_test.go`
- Modify: `internal/engine/compile.go`, `internal/engine/turn.go`, `internal/engine/accounting.go`, `internal/ctxcompiler/compiler.go`, `internal/ctxcompiler/tail.go`
- Test: compiler and accounting regression files

**Interfaces:**
- Consumes: Tasks 2–4; compile receives current user request and effective workspace root explicitly.
- Produces: `(*Engine).retrieve(ctx context.Context, scope retrieval.Scope, detail protocol.WorkDetail, request, root string, items []protocol.Item, turnID string) ([]retrieval.Candidate,retrieval.Report,error)`.
- ctxcompiler.Input gains `Retrieval []retrieval.Candidate; RetrievalQuery retrieval.Query`; Result/Report include selected IDs and retrieval tokens. RequestPackets/RequestBreakdown gain Retrieval token fields.

- [ ] **Step 1: Write failing tests**

`TestCompilerRetrievalPreservesRequiredState`: under pressure mandatory state, approved decisions, unresolved failures, instructions and live suffix unchanged; optional retrieval fits the combined existing packet budget with exact overhead.
`TestCompilerRetrievalTailDeduplication`: returned historical message already retained by tail is excluded; same evidence not rendered twice. Share tail identity selection rather than guessing last ten item rows.
`TestRetrievalFallbackAtomic`: store read/timeout/malformed data/panic produces the exact base compiler/history messages; zero hits succeeds empty; no extra provider call/prompt.
`TestRetrievalFlagOffEquivalent`: complete serialized request equals baseline when disabled and no provenance I/O occurs.
`TestRetrievalAccounting`: retrieval tokens subtracted once from conversation and counted once in request; raw usage untouched; logs contain no query/snippet/synthetic secret.

- [ ] **Step 2: Observe RED**

Run: `GOCACHE=/private/tmp/umcode-go-cache go test ./internal/ctxcompiler ./internal/engine -run "TestCompilerRetrieval|TestRetrieval" -count=1`. Expected: the named assertions fail because the new behavior is absent. Compilation failure while adding a new API is setup only; obtain a behavioral failure before implementing its body.

- [ ] **Step 3: Implement the deliverable**

Coordinator imposes 100 ms context deadline, recovers panic, collects bounded candidates and validates canonical scope/revision/hash. Do not cache initially. Pass candidate data to pure compiler selector after mandatory packets reserve space; current history-smaller comparison includes retrieval. Extend compile call signatures and all tests together; reuse existing current-turn isolation. Compute tail source IDs in tail.go and supply exclusions to Select. If retrieval fails, call Compile with exactly its pre-retrieval input. If Compile refuses, preserve history fallback. Do not insert optional retrieval into the system prompt or duplicate it in live messages. Reports use fixed reason enums, source IDs/hashes and counts only.

- [ ] **Step 4: Verify GREEN**

Run the Step 2 command. Expected: exit 0 and all named tests pass. Run existing tests for the changed package before committing; record exact output in the execution ledger.

- [ ] **Step 5: Commit**

Stage only the files above and required test fixtures. Commit message: `feat(engine): compile scoped retrieval with safe fallback`.

### Task 6: Prove retrieval usefulness and document independent activation

**Files:**
- Create: `internal/engine/retrieval_benchmark_test.go` and deterministic testdata fixtures
- Modify: `GO_ENGINE.md`, retrieval-focused E2E tests

**Interfaces:**
- Consumes: full retrieval path; no new production APIs.
- Produces: reproducible baseline/treatment evidence and operator documentation.

- [ ] **Step 1: Write failing tests**

`TestRetrievalRecoversEarlierContextWithoutQualityLoss`: relevant older user exchange lies outside ten retained pairs, file.read/search creates actual provenance, treatment retrieves matching excerpt after live hash validation. A deterministic provider checks required context, performs real file edit and verification, and returns identical verified artifact. Record both baseline rediscovery and treatment total request estimates/tool/model calls, including retrieval cost; assert no engine-added model call.
`TestRetrievalMutationDetected`: disable selection temporarily; usefulness assertion must fail, then restore production bytes.
`TestRetrievalRestartAndEdit`: reopen Store, recover eligible context; edit source externally and show no old excerpt or stale pass enters next request.

- [ ] **Step 2: Observe RED**

Run: `GOCACHE=/private/tmp/umcode-go-cache go test ./internal/engine -run "TestRetrievalRecovers|TestRetrievalRestart" -count=1 -v`. Expected: the named assertions fail because the new behavior is absent. Compilation failure while adding a new API is setup only; obtain a behavioral failure before implementing its body.

- [ ] **Step 3: Implement the deliverable**

Use the existing engine streaming-provider fixture conventions from memory_benchmark_test.go. Avoid directly fabricating eligible typed observation metadata; actual tools/Work.Observe must create it. Document config dependency, same-thread limits, observed-symbol scope, platform omission, budgets, historical trust, diagnostics and fallback. Record measured fixture numbers without hosted-billing or broad quality claims.

- [ ] **Step 4: Verify GREEN**

Run the Step 2 command. Expected: exit 0 and all named tests pass. Run existing tests for the changed package before committing; record exact output in the execution ledger.

- [ ] **Step 5: Commit**

Stage only the files above and required test fixtures. Commit message: `test(retrieval): prove context recovery with real observations`.

### Task 7: Review and final verification

- [ ] Read both spec and this plan against the final diff; request one whole-slice review under the chosen execution method. Review scope isolation, authoritative-state preservation, migration secrecy, file containment and actual observation reachability.
- [ ] Address Important/Critical findings with observed RED/GREEN regressions; ledger deferred minors and explicit rulings.
- [ ] Run `GOCACHE=/private/tmp/umcode-go-cache go test ./... -count=1 -timeout=10m`, `GOCACHE=/private/tmp/umcode-go-cache go vet ./...`, and `git diff --check`. Expected: exit 0. Shell/loopback tests may require sandbox escalation; setup failures are not product failures or RED evidence.
- [ ] Save durable completion/verification notes. Phase 4d begins only after this slice is green. No default enablement or Phase 6 claim; push/PR only when requested.

## Plan self-review

Scope, index secrecy/lifecycle, actual file provenance, graph/query ranking, compiler priorities, diagnostics, fallback and real-task tests map to Tasks 1–6. Review Focus cases map to named regression tests. Store.Scope includes TurnID for excluding live items; observations/evidence/excerpts share one transaction. Pure packages import neither store nor engine. Task 7 gates execution completion; implementation has not begun.
