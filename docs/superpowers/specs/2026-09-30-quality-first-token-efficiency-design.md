# Quality-First Token Efficiency

**Status:** Approved conversational design

**Date:** 2026-09-30

**Project:** UMCode

## Summary

UMCode will reduce token usage by improving how work is understood, structured, executed, verified, and remembered. It will not lower quality gates, omit required evidence, or ask models to reason less merely to save tokens.

The system will add four complementary capabilities:

1. A Ponytail-inspired solution-selection policy that prefers the smallest sufficient implementation after the agent understands the affected flow.
2. A structured Work Model and Evidence Graph stored in SQLite, with large immutable evidence stored in a content-addressed vault.
3. A context compiler that sends each model the smallest sufficient, freshness-checked subgraph instead of replaying the conversation.
4. Curated project memory in `UMCODE.md`, containing only concise, verified, durable project knowledge.

The user experience remains quiet by default. The additional structure primarily improves the engine; users see it only when a decision, blocker, failure, or explicit inspection requires it.

## Goals

- Preserve or improve task correctness, safety, and verification quality.
- Reduce uncached input tokens, output tokens, repeated tool calls, and cost per successful task.
- Prevent requirements, decisions, failures, and verification evidence from disappearing during conversation compaction.
- Stop repeatedly rediscovering project architecture, commands, conventions, and existing reusable capabilities.
- Make completion claims traceable to current evidence.
- Support coding and general-purpose knowledge work through one domain-neutral graph model.
- Keep repositories and chat transcripts free of generated planning noise.
- Give users clear inspection, correction, export, retention, and deletion controls.

## Non-goals

- Minimizing tokens at the expense of correctness or safety.
- Replacing raw evidence with unrecoverable summaries.
- Adding a graph database or vector database before deterministic retrieval proves insufficient.
- Displaying the internal graph, token accounting, or task machinery in normal chat by default.
- Writing active task state, transcripts, temporary failures, or generic advice into `UMCODE.md`.
- Continuing to scan `AGENTS.md`, `CLAUDE.md`, or `~/.umcode/AGENT.md` as implicit UMCode instructions.
- Treating fewer lines of code as proof of quality.

## Quality invariant

> Compress repetition; retain requirements, constraints, decisions, and recoverable evidence.

Token pressure may remove optional background. It may not silently remove the current objective, applicable acceptance criteria, safety constraints, explicit user decisions, unresolved failures, or evidence required for the next correct action.

If mandatory context cannot fit, UMCode must narrow the active task, retrieve smaller evidence ranges, route to a larger-context configured model, or report a blocker.

## Current-state findings

UMCode already provides useful foundations:

- SQLite-backed projects, threads, turns, items, usage, approvals, and file-change history.
- Model pools, complexity presets, provider fallback, and token/cost accounting.
- Conversation compaction and old tool-result trimming.
- Verification planning and execution tools.
- A configured but currently unused `~/.umcode/vault` path.
- Root-to-leaf instruction discovery with caching.

The current token strategy is primarily reactive. It replays up to 60 history messages, exposes every allowed tool schema, sends a large system prompt, compacts whole conversations near the context limit, and trims old tool results only after the request grows large.

The instruction system also has a mismatch:

- UMCode writes project instructions only to `UMCODE.md`.
- It nevertheless concatenates `UMCODE.md`, `AGENTS.md`, and `CLAUDE.md` at every applicable directory.
- It injects a global `~/.umcode/AGENT.md`.
- Documentation and CLI text still refer to project `AGENT.md` in several places.
- `AGENT.md.template` is not referenced by runtime, build, CLI, or app code.

## Architecture

### Lean Quality Loop

Every meaningful task follows this lifecycle at an adaptive depth:

```text
understand
-> inspect existing capabilities
-> select the smallest sufficient solution
-> record design and tasks at the required depth
-> execute with targeted context
-> verify each required claim
-> promote verified durable knowledge
```

The loop has four logical components:

1. **Work Model:** goals, acceptance criteria, constraints, decisions, tasks, evidence, and memory candidates.
2. **Solution Selector:** a compact implementation-minimization policy.
3. **Context Compiler:** a freshness-aware, token-budgeted projection of the active work graph.
4. **Quality Ledger:** requirement-to-implementation-to-verification traceability.

These are logical boundaries. The initial implementation should keep them as focused Go packages or services without introducing network services or additional databases.

### Solution selection policy

Before implementing, the agent evaluates the following ladder after reading the relevant code and tracing the affected flow:

1. Does this need to exist?
2. Does the repository already contain the capability?
3. Can the standard library satisfy it?
4. Can the native platform satisfy it?
5. Can an already-installed dependency satisfy it?
6. What is the minimum new code required?

Every new abstraction, dependency, configuration option, or file must link to an acceptance criterion that necessitates it.

The policy must not simplify away:

- Root-cause analysis
- Trust-boundary validation
- Data-loss prevention
- Security controls
- Accessibility requirements
- Explicit user requirements
- Verification appropriate to the risk

The selected rung and its evidence become a compact decision node rather than repeated explanatory prose.

## Adaptive workflow depth

Workflow depth is independent of model reasoning effort.

### Direct

For trivial, low-risk work:

- Record the goal.
- Perform one focused action.
- Record verification evidence.
- Finish.

No design document or task graph is generated.

### Guided

The default for meaningful coding work:

- Intent brief and acceptance criteria
- Relevant repository-map fragment
- Solution-selection decision
- Small task graph
- Implementation
- Verification ledger
- Durable-memory evaluation

### Designed

For high-risk, ambiguous, or architectural work:

- Requirements and non-goals
- Alternatives and trade-offs
- Approved design decision
- Dependency-aware task graph
- Risk-specific verification plan
- Implementation checkpoints
- Requirement-to-evidence review
- Durable-memory promotion

Designed work is triggered by public-interface or data-model changes, security and authorization changes, money movement, database migrations, cross-subsystem work, destructive operations, unclear acceptance criteria, or meaningful architectural alternatives.

Work may escalate from Direct to Guided to Designed when new complexity appears. It must not silently downgrade to save tokens.

Classification initially uses deterministic scope, risk, and uncertainty rules. It must not require a separate LLM call. The active agent may request escalation when repository evidence reveals hidden complexity.

## Persistence model

### SQLite

The operational source of truth remains `~/.umcode/umcode.db`.

Proposed tables:

```text
works
  id, thread_id, project_id, kind, status, workflow_depth
  goal, created_at, completed_at

work_nodes
  id, work_id, kind, title, content_json
  status, confidence, revision, valid_from, valid_until
  superseded_by, created_at, updated_at

work_edges
  work_id, from_node_id, relation, to_node_id

evidence
  id, work_id, node_id, kind
  source_uri, source_revision, content_hash
  summary, confidence, observed_at, stale_at

verification_attempts
  id, work_id, criterion_node_id
  check_type, command, environment_json
  status, exit_code, evidence_id
  started_at, finished_at
```

Supported node kinds include:

- goal
- acceptance criterion
- requirement
- constraint
- design option
- decision
- task
- claim
- fact
- unknown
- artifact
- memory candidate

Supported lifecycle states are kind-specific. Decisions include proposed, approved, superseded, and rejected. Tasks include pending, ready, in progress, blocked, completed, and failed. Facts include active, stale, superseded, contradicted, and retracted.

This relational adjacency-list model is sufficient for initial graph traversal using indexed queries and recursive common-table expressions. Existing FTS5 support should handle textual retrieval before embeddings are considered.

### Content-addressed vault

Large immutable evidence is stored under the configured vault:

```text
~/.umcode/vault/objects/<hash-prefix>/<content-hash>
```

Examples include full command output, test logs, screenshots, browser traces, large diffs, retrieved documents, tool responses, and generated reports.

SQLite stores the object hash, compact summary, provenance, classification, and graph relationships. Identical objects are stored once. Missing vault objects must never be represented as valid available evidence.

Sensitive evidence receives explicit classification and retention. The engine must not persist secrets merely because they appeared in tool output.

### Repository artifacts

Ordinary designs and plans remain in SQLite and are displayed on demand. They become repository files only when the user requests a document, repository policy requires it, or the artifact has durable review value. A graph node then links to the repository artifact.

## `UMCODE.md` as curated project memory

UMCode will use only:

- `<project>/UMCODE.md`
- Applicable root-to-leaf nested `UMCODE.md` files

It will stop implicitly scanning:

- `AGENTS.md`
- `CLAUDE.md`
- `~/.umcode/AGENT.md`

`agents.context_file` and `AGENT.md.template` will be removed. Documentation, tests, CLI labels, and UI copy will use `UMCODE.md` consistently.

Existing foreign instruction files remain untouched. An explicit, user-reviewed one-time import may draft `UMCODE.md` from them. There is no silent compatibility fallback.

`UMCODE.md` contains concise, verified, project-specific knowledge:

- System map and architectural boundaries
- Existing reusable capabilities
- Important invariants
- Verified build, test, lint, and run commands
- Generated or sensitive paths
- Non-obvious project conventions

It excludes:

- Active task state
- Transcripts
- Temporary failures
- Failed guesses
- Generic engineering advice
- Full file trees
- Large command output
- Temporary branch information

The root file has a soft target of approximately 4 KB. Nested files contain only subtree-specific guidance. Detailed provenance remains in SQLite and the vault.

Memory promotion occurs after completion only when a fact is project-specific, likely to help future work, verified by code or a successful command, absent from current memory, still true at the final revision, and concise.

Memory candidates accumulate silently. Initial releases require review from Project Settings before changing `UMCODE.md`. External edits are preserved by re-reading the file and applying a minimal merge rather than overwriting it.

## Context compiler

The compiler produces a fresh request for every model call from four ordered layers.

### Stable runtime prefix

- Core safety and tool rules
- Compact skill catalog
- Root `UMCODE.md`
- Project, sandbox, and environment identity

The prefix remains byte-identical when possible to maximize provider prompt caching.

### Work packet

- Current goal
- Applicable acceptance criteria
- Approved decisions
- Current task and dependencies
- Applicable constraints
- Definition of completion

### Evidence packet

- Relevant repository symbols and excerpts
- Current file revisions
- Recent observations
- Failed checks
- Contradictions and unresolved questions
- Passing evidence still valid for the current revision

### Interaction tail

Only recent user messages necessary to interpret the active request are included. The complete transcript remains visible and stored but is not automatically replayed.

### Priority model

```text
P0: safety constraints, objective, criteria, active task,
    unresolved failure, explicit user decisions
P1: design decisions, affected interfaces, verified invariants,
    applicable UMCODE.md guidance
P2: neighboring implementation, recent passing checks,
    prior alternatives, concise conversation history
P3: old tool output, completed task details,
    superseded decisions, unrelated conversation
```

P3 and then P2 may be omitted under pressure. P0 may not be omitted. Required P1 may be replaced with a recoverable reference only when the next action does not require its raw content.

### Retrieval

The compiler starts at the active task, traverses relevant edges, retrieves applicable instructions, checks evidence freshness, selects phase-appropriate tools, fits the result into a measured budget, and records inclusion and omission reasons.

Selection considers relevance, source authority, freshness, confidence, dependency distance, and risk. Initial retrieval uses deterministic rules, graph traversal, and FTS5. Embeddings are deferred until a benchmark demonstrates a retrieval failure that these mechanisms cannot solve.

### Graph updates

The engine records most state without extra model calls:

- File tools record inspected and changed artifacts.
- Search records discovered symbols and references.
- Verification tools record attempts and evidence.
- Git state supplies revision information.
- User approvals record accepted decisions.
- Failed tools record blockers.

Guided and Designed work receives one compact internal `work.update` tool for semantic changes such as criteria, decisions, tasks, unknowns, and memory candidates. Updates are batched by phase rather than emitted after every thought.

### Progressive tool loading

Only tools required for the current phase are exposed. Core search, read, edit, and execution tools remain available for coding work. Browser, computer-use, preview, MCP, specialist, and rarely used tools are added only when applicable. The chosen set remains stable within a phase to preserve prompt caching.

Tool results use domain-specific reducers. The full result remains in the vault or transcript; the model receives the relevant failures, totals, changed hunks, symbols, or accessibility and browser diagnostics.

## Evidence freshness and knowledge lifecycle

Evidence is immutable. Facts are versioned.

A fact records scope, environment fingerprint, validity interval, status, and any superseding fact. New evidence does not delete history; it changes the active truth.

Fact scope may include:

- Project and repository revision
- Host or isolated-compute environment
- Tool and tool version
- Network and permission configuration
- Provider or model
- Retrieval time and external source

For example, a tool failure in an isolated environment does not imply that the tool is unavailable on the host. A later success in the same scope supersedes the old availability fact. The older observation remains available for diagnosing intermittent failures but is excluded from normal context.

Facts become stale through:

- File, revision, dependency, tool, permission, network, model, or configuration changes
- Newer verification attempts
- Explicit user correction
- Time-to-live expiry for volatile facts
- Dependency propagation through graph edges

When evidence conflicts, UMCode marks the active fact contradicted, retains both observations, runs the cheapest authoritative check, and promotes the supported result. High-risk dependent work pauses until the contradiction is resolved.

### Two-stage retirement

1. **Remove from active use:** stale and superseded facts immediately stop entering model context and default search results.
2. **Garbage collection:** unreferenced raw objects and transient observations are physically deleted only after retention expires and no active fact, verification, memory, transcript, or audit record references them.

Suggested initial defaults:

- Redundant raw tool output: 30 days
- Superseded transient observations: 30 days
- Large unreferenced blobs from completed work: 90 days
- Compact completed graph structure: retained
- Evidence supporting surviving claims: retained
- Approved decisions and promoted memory: retained until removed or superseded
- Sensitive temporary evidence: shortest applicable retention

Users can inspect storage, change retention, purge a project, remove specific knowledge, or clear unreferenced vault objects.

## Failure and recovery

Failures are recorded as structured state linked to affected tasks, claims, and evidence.

Failure categories include action, verification, evidence, planning, context, model, and storage failures.

Verification attempts are append-only. A later passing attempt does not overwrite an earlier failure. File changes make affected prior checks stale while unrelated evidence remains valid.

Meaningful state transitions are transactional:

```text
record action intent
-> execute action
-> store raw evidence
-> update graph
-> commit resulting state
```

After a restart, UMCode resumes from the latest committed task and checks whether an interrupted action changed external state before repeating it.

Provider or model fallback carries the structured work packet forward rather than replaying the prior transcript.

If vault storage fails, UMCode does not claim that raw evidence was retained. If SQLite writes fail, workspace mutation stops because the audit state cannot be trusted. Memory-promotion failure does not invalidate otherwise completed work; it is queued separately. External `UMCODE.md` changes are merged, never overwritten.

## Quiet user experience

The internal structure is hidden by default.

### Normal mode

- Existing tool activity remains collapsed.
- No persistent Work panel or task graph appears in chat.
- Progress updates occur only on material phase changes.
- The user is interrupted only for a required decision, permission, blocker, verification failure, or memory conflict.
- Final responses contain the outcome, verification status, and unresolved risk.

### On-demand details

A per-turn action reveals the plan, completed tasks, design decisions, failures, checks, supporting evidence, and memory candidates.

### Developer diagnostics

An explicit setting reveals context-layer token counts, included and omitted nodes, prompt-cache behavior, tool-schema cost, evidence freshness, retrieval traces, and optimization version.

### Memory review

Memory suggestions accumulate silently in Project Settings. An unobtrusive count indicates pending suggestions. Ordinary memory discovery never interrupts chat. Conflicts and stale durable facts are surfaced because they may affect future work.

## Evaluation

### Deterministic tests

- Graph updates and traversal
- Fact supersession and contradiction handling
- Dependency invalidation
- Vault deduplication and garbage collection
- Context priority enforcement
- Mandatory-context protection
- `UMCODE.md` discovery and migration
- Tool-availability changes from failing to working

### Golden context tests

Given a fixed graph, model window, and active task, assert exactly which blocks and tools are included, omitted, referenced, or rehydrated.

### Integration tests

- Tool result to evidence to claim to verification
- File change invalidates only affected checks
- Provider fallback preserves work state
- Restart resumes the correct task
- External `UMCODE.md` edits are preserved
- `AGENTS.md`, `CLAUDE.md`, and global `AGENT.md` are not implicitly scanned
- Vault failures do not create false evidence

### Adversarial tests

- Stale passing tests after code changes
- Contradictory sources
- Malformed work updates
- Missing vault objects
- Oversized mandatory context
- Prompt injection inside retrieved evidence
- Environmental scope mismatch

### Paired benchmark

Current and optimized UMCode run identical tasks against identical project snapshots, models, permissions, and budgets.

Primary quality metrics:

- Acceptance criteria satisfied
- Hidden tests passed
- Safety and security checks passed
- Regression rate
- Blind human review score
- Correct root-cause location
- Unsupported completion claims
- Required context omitted
- Stale evidence used
- User clarification turns

Efficiency metrics:

- Uncached and cached input tokens
- Output and reasoning tokens
- Tool-schema and tool-output tokens
- Requests and tool rounds
- Cost and time per successful task
- Final response length
- Repeated repository exploration

LLM judges may categorize results but do not replace deterministic checks or blind human review.

Initial release targets:

```text
quality point estimate: at or above baseline
quality uncertainty: 95% confidence lower bound no worse than 2 percentage points below baseline
safety: zero regression
unsupported completion claims: zero increase
uncached input tokens: at least 30% lower
cost per successful task: at least 25% lower
tool rounds: at least 15% lower
```

The confidence margin represents finite-sample uncertainty, not permission to accept a measured quality loss. Security, data integrity, and destructive-operation tasks require exact quality parity. Results are reported by task category so an overall average cannot conceal a serious regression.

### Ablations

Measure independently:

1. `UMCODE.md`-only instruction loading
2. Structured Work Model
3. Evidence and verification ledger
4. Context compiler
5. Progressive tool loading
6. Tool-output reducers
7. Solution selector
8. Adaptive model and reasoning routing
9. Complete system

## Delivery decomposition

This architecture is intentionally split into independently testable phases. Each phase receives its own implementation plan, and later phases may receive a narrower design specification if implementation reveals unresolved interface choices.

### Phase 1: instruction and measurement foundation

- Make `UMCODE.md` the only implicit instruction convention.
- Remove global `AGENT.md` loading, `agents.context_file`, and `AGENT.md.template`.
- Update documentation, UI, CLI, and tests.
- Add context-layer and tool-schema token accounting without changing model behavior.
- Establish paired baseline fixtures.

### Phase 2: Work Model and verification ledger

- Add SQLite work, node, edge, evidence, and verification schema.
- Record engine-observable graph events automatically.
- Add Direct and Guided workflow state.
- Expose on-demand details through protocol APIs without adding default chat noise.

### Phase 3: vault and evidence lifecycle

- Implement content-addressed vault storage.
- Add source revisions, environment fingerprints, staleness propagation, contradiction handling, retention, and garbage collection.
- Add recovery and failure-injection tests.

### Phase 4: context compiler

- Build layered context packets and priority enforcement.
- Add graph and FTS5 retrieval.
- Add progressive tool loading and tool-specific reducers.
- Preserve prompt-cache-stable prefixes.
- Compare against the existing history path behind a feature flag.

### Phase 5: solution selector and Designed workflow

- Add the solution-selection policy and decision records.
- Add Designed workflow gates and approvals.
- Add memory-candidate promotion to `UMCODE.md`.

### Phase 6: quiet UI and rollout

- Add on-demand details, project memory review, storage controls, and developer diagnostics.
- Run the full ablation benchmark.
- Roll out through opt-in, canary, and default stages with an instant fallback path.

## Rollout and compatibility

Every request records the context-builder version and a compact selection trace. The optimized path is feature-flagged until it passes release gates.

Removing foreign instruction scanning is a deliberate compatibility break. UMCode must clearly announce it in release notes and offer an explicit import preview. It must never modify or delete foreign files.

Schema migrations are additive. Older transcripts remain readable. Work graphs and vault objects are deletable through user-facing controls. A fallback to the existing context path remains available during staged rollout, but it does not restore implicit foreign instruction scanning.

## Open implementation constraints

These are constraints to resolve in phase-specific implementation plans, not undecided product behavior:

- Use the existing Go SQLite store and numbered migration mechanism.
- Preserve one-process, local-first operation.
- Keep normal chat quiet.
- Do not add embeddings until deterministic retrieval fails a measured benchmark.
- Do not persist secrets in the vault.
- Keep graph updates transactional with their observable engine events.
- Treat user-authored `UMCODE.md` content as authoritative and preserve unrelated edits.

## Acceptance criteria

The architecture is complete when:

- UMCode implicitly reads only root and applicable nested `UMCODE.md` files.
- Active work can resume after compaction, provider fallback, or restart without reconstructing state from the transcript.
- Each completion claim can be linked to current verification evidence.
- File and environment changes invalidate only affected facts and checks.
- A newly working tool supersedes an older failure in active context while retaining auditable history.
- Normal chat remains no noisier than the current experience.
- Users can inspect, correct, export, retain, and delete stored work and knowledge.
- Paired benchmarks pass the defined quality and efficiency release gates.
