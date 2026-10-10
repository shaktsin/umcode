# Phase 4c: deterministic context retrieval

Status: proposed written spec; approach approved, awaiting written-spec approval.
Date: 2026-10-10
Parent: 2026-09-30-quality-first-token-efficiency-design.md
Base: master after Phase 5b, PR #51 (3939bb7).
Companion: 2026-10-10-phase4d-progressive-tools-design.md.

## Intent and success

Finish the retrieval portion deferred by Phases 4a/4b before Phase 6. Recover relevant older conversation and repository evidence without replaying the transcript or adding model calls. Preserve current decisions, unresolved failures, source authority, verification freshness, project isolation, and existing instruction precedence. Success means deterministic retrieval improves a task that the short tail cannot answer, excludes stale/foreign material, and fits a measured request budget. Lower tokens alone do not establish quality.

The user approved separate retrieval and progressive-tool slices, each default-off and independently measurable, with failure fallback. Phase 6 UI, broad ablation benchmarks, rollout, embeddings, model routing, autonomous model-generated queries, and semantic inference are outside this slice.

## Existing seams

`internal/engine/compile.go` loads the open Work, computes staleness, and calls the pure `internal/ctxcompiler` package on every model request. The live tool/message suffix is appended afterward. Existing compiler refusal returns to history. `items_fts` exists, but `Store.SearchItems` searches all threads; it must not be used unfiltered for context retrieval. Designed packets already project current tasks/decisions; retrieval supplements rather than replaces their authoritative state.

## Configuration and compatibility

Add `models.context_retrieval: false`. Enabling it requires `models.context_compiler: true`; invalid combinations fail configuration validation with a clear error. No dependency on Designed workflow or memory auto-promotion. Flag-off requests, prompt ordering, tool schemas, and ordinary tool behavior remain byte-identical except existing nondeterministic metadata. Retrieval requires an open Work; plain chat continues on the existing history path.

No new user prompts, notifications, background services, network calls, tool execution, vault writes, or approvals. Normal chat remains quiet. Explicit file/search tools remain available when bounded retrieval cannot find an answer.

## Components and trust boundary

1. Store commands return scoped canonical candidates, not rendered context. The active thread/Work/project identities are mandatory inputs; SQL filters precede ranking and LIMIT.
2. A retrieval coordinator in the engine builds a bounded query, gathers graph/text candidates, validates sources, and passes data to a pure selector. It owns I/O and cancellation.
3. A pure selector ranks, deduplicates, budgets, and returns typed retrieval entries plus inclusion/omission reasons.
4. The compiler renders an optional `Retrieved context` packet after mandatory Work state and before the interaction tail. The current user message and live suffix remain unchanged.

Every entry includes source kind, stable source ID, thread/Work identity, revision/hash when relevant, historical/current classification, and a concise body. Retrieved content is quoted untrusted data, never system instructions or authorization. Applicable UMCODE.md remains supplied by the existing instruction loader; retrieval does not discover foreign instruction files or duplicate those instructions.

## Query construction

Use the newest user request plus current task titles and explicit path/symbol references from the active Work. Take at most 16 unique meaningful terms, each at most 64 bytes, from at most 8 KiB of input. Normalize deterministically, strip FTS syntax, quote terms, and bind SQL parameters. Use OR matching with exact path/symbol boosts so one irrelevant term does not require an all-term match. Empty/no meaningful query yields graph candidates only. Do not use retrieved text to recursively generate another query.

Ordering is explicit: graph distance and source class, exact path/symbol match, lexical score, then stable source ID. No clock-dependent scoring or model-based reranking. Query terms and bodies are excluded from developer logs.

## Retrieval sources

### Active graph

Seed traversal from noncompleted active tasks, unresolved blocking unknowns, criteria, and current approved decisions. Traverse the typed dependency/support/implementation/verification edges in both directions where needed to reach supporting material, at most depth 2 and 128 nodes. Do not traverse arbitrary textual references. Detect cycles; enforce Work identity on nodes and edges. Mandatory compiler state is not reconstructed from this truncated traversal.

Include relevant supporting requirements, decisions, artifacts, and fresh active evidence not already represented in the mandatory packet. Reject rejected/superseded/expired nodes and stale evidence. Contradiction edges are surfaced as unresolved data, never resolved by relevance score. An invalid graph causes retrieval fallback; it does not silently drop required state.

### Scoped FTS

Add a dedicated FTS5 index over Work-node titles and redacted evidence summaries, maintained transactionally with canonical insert/update/delete commands. Index rows contain source identity and text, not secrets, raw vault bodies, or free-form node JSON. Redact existing canonical text during backfill and every new index mutation; never persist the unredacted variant temporarily. Canonical tables remain the authority; rejoin and recheck liveness on every result. Use an additive numbered migration after 0016 and bounded backfill in the migration transaction. Test all mutation and cascading-deletion paths for consistency. Retrieval correctness must not depend on index text being fresh: revalidate/re-render canonical content, or exclude a mismatched index row.

Search current-Work graph/evidence only. Search existing `items_fts` for older user/assistant messages in the current thread only, excluding current-turn items and items already in the retained tail. No sibling-thread or sibling-project search, including projectless threads. Work provenance prevents an older message from being represented as the current approved decision. Old assistant assertions are historical conversation, not current facts or verification passes. Raw historical tool output and prior completion claims are not retrieved as authoritative evidence.

Canonical messages are redacted before projection; return bounded excerpts rather than the full item. Explicit user decisions recorded in current Work outrank contradictory historical text. Historical contradictions remain labeled, not silently adopted.

### Repository symbols and excerpts

Use successful engine-observed `file.read`/`file.search` provenance to identify repository paths and line/symbol references. Extend observation storage with typed, redacted path/range/content-hash metadata; do not infer filesystem paths from arbitrary tool-output prose. This observation path is independent of Designed workflow when retrieval is enabled, and clients cannot forge observed provenance through `work.update`.

The index covers observed excerpts, not the entire repository. Existing records without typed provenance remain usable as redacted summaries, but cannot authorize filesystem reads. New observations index bounded excerpts in the same transaction as provenance. Symbol matching is lexical over observed declarations/search hits, without language-server dependencies or claims of semantic symbol resolution.

Before including a repository excerpt, reread its exact path beneath the effective turn workspace using descriptor-anchored, no-symlink traversal. Reject absolute paths, parent traversal, symlinks, nonregular files, secret paths under the existing file-search deny policy, foreign instruction conventions, binary/invalid text, and oversized files. Check exact content hash against observed provenance. Mismatch excludes the historical excerpt; a subsequent explicit inspection supplies new provenance. Include path, line range and hash; never assert an old verification pass from a matching excerpt alone.

Limit validation to 4 files, 256 KiB per file, 1 MiB total, and 40 lines/4 KiB per returned excerpt. Do not walk the repository, run rg/git, follow imports, or open arbitrary query-derived paths. This completes retrieval of observed repository symbols/excerpts; new repository discovery remains the existing file.search tool's responsibility.

## Freshness and budgets

Reuse `work.Staleness` and `work.ActiveEvidence`; never broaden active verification evidence based on an FTS match. File excerpt hashes are checked at every request where an excerpt is used. Missing/unavailable vault bodies are never opened for retrieval; a canonical summary may be included with its existing unavailable label. Retrieval never changes Work status or refreshes evidence.

Defaults are package constants: 100 ms coordinator deadline, 64 candidates per FTS source, 128 graph nodes, 12 selected entries, 6,144 estimated tokens maximum or 5% of the context window, whichever is smaller. Unknown window uses the absolute ceiling. Input/source byte limits apply before token estimation and sorting. Each store projection is capped at 4 KiB of text; aggregate candidates are capped at 512 KiB. Exclude oversized candidates rather than allocating their full bodies. Store reads honor context cancellation; filesystem loops check cancellation between bounded reads.

Fit the retrieval packet inside the existing combined packet budget, not in addition to it. Budget checks must account for the exact rendered headings, provenance and message overhead, not body text alone. Mandatory P0 and required P1 reserve space first. Retrieval cannot displace unresolved failures, approved current decisions, applicable instructions, the new user message, or live tool results. Drop historical conversation first, then less-relevant optional evidence/excerpts; use stable tie breaks. Deduplicate IDs and equivalent source ranges across existing packets and the tail. If no optional space remains, omit retrieval and use the existing compiler result.

The current history comparison must count retrieval bytes/tokens. If the complete compiled request no longer improves on history, preserve existing compiler refusal. A valid tiny-history request may therefore use history even when retrieval is enabled; diagnostics report that decision rather than savings.

## Failure and caching

Read errors, timeout, malformed candidates, containment failures, or panic discard the retrieval result atomically and proceed with the unchanged compiler input. Ordinary zero hits are successful retrieval with an empty packet. If the compiler itself refuses, retain its existing history fallback. Do not splice a partial retrieval packet into history.

Cache only immutable candidate identities/query work within a turn; revalidate canonical revisions and file hashes before reuse. New user steering, Work revision, tool observations, or workspace changes invalidate selection. Never serve a cached excerpt after a relevant file edit. Initial implementation may omit caching to simplify correctness; the limits above still apply.

## Diagnostics

Add retrieval packet token accounting and bounded counts for candidates/selected entries by source, duplicates, stale/foreign/oversized omissions, deadline/fallback reason, and duration. Logs contain enumerated reasons and IDs/hashes only, no query, prose, snippets, raw errors with sensitive paths, or secrets. Keep provider-reported usage unchanged; estimates are explicitly estimates. Count all added retrieval bytes in efficiency comparisons.

## Validation and acceptance

- Flag-off request equivalence and configuration dependency validation.
- Deterministic query/rank results, punctuation/FTS injection, empty query, excessive Unicode/input sizes, stable ties and cycles.
- SQL scoping before LIMIT: highly ranked foreign rows cannot leak or crowd out eligible hits. Include projectless and reused project names.
- Index backfill, canonical updates, supersession, stale evidence, deletion, stale index text, and reopen/restart tests.
- Recover an older relevant user exchange outside the tail; reject old assistant verification claims as current truth.
- Graph dependency/support retrieval without omitting required Work state.
- Actual file.read/search observation → typed provenance → symbol match/excerpt; reject forged observations, edited/deleted files, symlink swaps, denied paths and binary/oversized files.
- Budget exhaustion preserves mandatory state; deduplication removes repeated evidence and tail messages.
- Error/timeout/panic injection yields the same base compiler/history request without prompting or a new model call.
- Deterministic real-tool task fixture where the short-tail baseline lacks an older relevant fact but retrieval supplies it, while final artifact and verification match. Report input tokens, discovery calls, model calls and added packet cost; do not require a token reduction on every retrieval-enabled request.

Full Go tests, vet, and diff checks gate completion. Phase 6 owns broad efficacy, safety, category-level ablations and enabling defaults.
