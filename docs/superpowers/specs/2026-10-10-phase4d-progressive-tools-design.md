# Phase 4d: progressive tool schemas

Status: proposed written spec; approach approved, awaiting written-spec approval.
Date: 2026-10-10
Parent: 2026-09-30-quality-first-token-efficiency-design.md
Base: Phase 4c integration on master after Phase 5b.
Companion: 2026-10-10-phase4c-retrieval-design.md.

## Intent and success

Finish the remaining Phase 4 tool-loading work before Phase 6. Reduce repeated schema overhead while keeping every permitted capability discoverable and preserving execution authorization. No additional model call is made by the engine to select tools. A model may request discovery when its task requires an omitted capability; that call and schema overhead count in measured comparisons.

The approved approach keeps core coding and verification tools available, adds specialist families when applicable, provides a compact discovery mechanism, and stabilizes schemas within a phase. Tool loading remains independently flagged. This spec does not introduce model routing, new capabilities, permissions, plugin installation, UI, or default rollout.

## Existing seams and configuration

`Engine.turnTools` merges the built-in/dynamic registry and the acquired per-turn plugin snapshot, resolves duplicate names, and sorts names. `runTurn` currently turns the entire permitted catalog into schemas once. `runTool` independently resolves tools and applies guards, workflow gates, assessment, approval, hooks, and project scope.

Add `models.progressive_tools: false`, independent of context compiler, retrieval, reducers and Designed workflow. Flag-off preserves the existing schema list/order, system prompt and dispatch behavior. With the flag on, selection uses the same per-turn resolved catalog that supplies execution identity; do not reacquire plugins or enumerate dynamic sources repeatedly during a loop.

## Components

- Build one immutable permitted catalog at turn start from the acquired plugin snapshot and registry. Apply existing project tool visibility and capability policies before creating discovery entries. Preserve static-over-plugin precedence and validate wire-name collisions. Reserve tools.discover for the engine; plugin entries cannot replace or shadow it.
- A pure selector maps trusted engine state plus explicit request terms to initial families and schemas. It never parses retrieved snippets or tool-output instructions as authority.
- A turn-scoped selection state tracks initial, discovered and session-required tools. Schema ordering is canonical and provider wire names remain unchanged.
- An engine-owned read-only discovery tool exposes omitted catalog entries and requests loading. It cannot execute another tool or change permissions.

Retrieval may supply typed active-task metadata but is not required. Do not couple availability to whether a context packet happened to fit a budget.

## Initial selection

Always retain registered core tools: file.list, file.search, file.read, file.edit, file.write, shell.run, exec.start, exec.write, exec.stop, verification.plan, verification.run, and skill discovery/loading tools that are present in the catalog. Include work.update exactly when its existing feature gate permits it. Include the new tools.discover tool. These tools are retained for general projectless work too so classification cannot remove basic capability.

Families: web; preview/visual/browser verification; computer; MCP per server; plugin/specialist per originating plugin; other registered tools. Grouping is explicit provenance/metadata, not a lossy split of arbitrary names. Unknown tools belong to a discoverable fallback family, not a guessed privileged family.

Deterministic initial additions:

- Explicit user mention of a tool, server or plugin loads the matching permitted entries.
- Requests clearly needing web lookup load web search/fetch.
- Frontend/visual task signals or a planned browser check load browser.verify plus available preview/visual lifecycle tools.
- Explicit desktop-app operation requests load the computer lifecycle family only when enabled by existing scope policy.
- Active engine sessions and configured pending verification checks retain all tools needed to inspect, finish and stop them.
- Approved active task metadata may add matching families, but free-form historical/retrieved text cannot activate tools.

Keyword rules are a tested conservative optimization, not proof that a family is irrelevant. No confident match still leaves core tools and discovery. Large families need not all be loaded by keyword: matching names may be discovered and loaded in bounded batches.

## Discovery protocol

Canonical name: tools.discover; wire name follows existing conversion. Strict JSON arguments: query (string, maximum 512 bytes), names (up to 8 exact canonical or wire names), cursor (opaque optional pagination token). Require query or names; reject unknown fields. Exact names select those permitted entries. Query lexically matches redacted names, descriptions, family and origin metadata; safe normalization and stable score/name ordering apply. No model/embedding ranking.

Return up to 8 entries, each with canonical name, wire name, family, a redacted description capped at 256 bytes, whether its schema is now loaded, and a next-page cursor. Do not return full schemas in the tool result: successful matches are added to request.Tools before the next provider call. The result states that loading takes effect on that next call. Count result/schema bytes in diagnostics. Empty matches return a bounded empty result with suggestions for family terms; never reveal disallowed entries or distinguish their existence from absence.

Pagination tokens bind to this turn's catalog generation and normalized query. Reject stale/foreign cursors rather than reading a different catalog. Cap loaded discovery entries at 64 additional tools per turn; hitting the cap activates full permitted-catalog fallback rather than silently making tools unreachable. Bound tool/catalog input lengths and fail to full-catalog mode if safe processing cannot be completed.

No hidden provider call occurs. If the model emits discovery and another tool in one response, normal ordering and parallel scheduling rules apply; only the next provider request receives new schemas. Dispatch does not treat unadvertised tools as unauthorized solely because their schemas were omitted: a known permitted tool can still execute through the same guards. A call to such a tool pins its schema for the next request. Unknown or forbidden tools keep their existing error/denial behavior.

Discovery is engine-owned, read-only and deterministic. Route it through normal item/result lifecycle and cancellation but not a new approval prompt. Explicitly classify it as workflow discovery so it cannot trigger write gates or create artificial successful-command facts. It must not invoke plugin actions, hooks that broaden permissions, or tool Assess methods to enumerate descriptions.

## Stability and phase transitions

Use existing persisted WorkflowDepth plus the current active-task identity when available; otherwise use a single turn phase. Freeze the initially selected set within a phase. Explicit discovery and active-session safety requirements may only add schemas; they cannot remove schemas already shown in the turn. On a phase/task transition recompute additions, but retain the union through turn end to avoid cache churn and provider-history incompatibilities. Pruning happens at the next turn boundary.

At a new turn, carry required session lifecycle families from trusted engine session state. Do not infer session existence solely from old transcript prose. The current request, active Work and currently enabled catalog determine the rest. Ordering and exact schema serialization remain stable when the inputs do not change. Tests compare bytes, not only tool counts.

Add a small static prompt instruction only when enabled: core tools are available; use tools.discover for an omitted capability, and loaded schemas appear on the next request. Do not insert the whole omitted-tool catalog into the system prompt, which would erase savings. Existing prompt instructions that mention specialist tools remain valid because discovery provides their schemas.

## Permission and snapshot invariants

Selection controls advertisement only. It cannot enable computer use, disallowed MCP servers, disabled plugins, network/compute capabilities, or bypass approval modes, workflow gates, tool guards or execution scopes. Reuse existing permission predicates; do not invent a second permissive copy.

The turn catalog must not expose schemas from a plugin outside the acquired project snapshot. A disabled or newly added plugin is visible only through the next turn's snapshot. Mid-turn dynamic disappearance produces the ordinary unavailable-tool result; never replace a schema's implementation with a different origin silently. Built-in and plugin name collisions retain the existing winner.

If current policy revokes a capability during a turn, execution still denies it. Advertising a previously loaded schema is never execution authority. Revocation must not load additional forbidden entries through discovery or fallback.

## Failure and measurements

Selector error, panic, malformed metadata, catalog collision or discovery-cap exhaustion switches monotonically to the full permitted catalog for the rest of the turn. Do not restart the model call or execute a tool as part of fallback. A malformed discovery argument returns a normal bounded tool error; internal discovery failure additionally enables full-catalog mode. If catalog construction itself fails under existing plugin behavior, preserve that behavior rather than presenting untrusted partial tools.

Diagnostics: catalog count, exposed count, full/exposed schema token estimates, discovery calls, loaded additions, selection phase ID, fallback enum, selection duration. Use hashed or stable source identities and counts; no request terms, tool arguments, secrets, or prose in logs. Tool schema estimates include discovery and prompt overhead, and provider usage remains authoritative. Compare total per-task costs, not only first-call schema savings.

## Validation and acceptance

- Flag-off exact tool-schema/prompt equivalence and independent flag combinations.
- Stable schema bytes under reordered source enumeration; explicit canonical/wire-name handling and collision tests.
- Ordinary coding excludes specialist schemas while all core/verification tools remain available.
- Explicit web, frontend/browser checks, desktop, MCP/plugin requests select the permitted families.
- Unknown intent still reaches a specialist via discovery; pagination, exact-name selection, zero hits, invalid/stale cursors and bounded results are covered.
- End-to-end streamed provider fixture: tools.discover result is stored, no target action executes during discovery, and the next request includes the schema exactly once.
- Unadvertised-but-permitted calls retain existing execution semantics and pin schema; forbidden discovery/direct execution cannot broaden permissions.
- Phase stability, turn-boundary pruning and existing exec/computer/preview session cleanup remain reachable.
- Plugin snapshots, duplicate precedence, disabled MCP servers, computer-use policy, approvals, workflow gates, cancellation and fallback are exercised through real dispatch seams.
- Error/panic injection exposes the same full permitted catalog as baseline with no extra provider call.
- Deterministic successful-task comparisons report total schema/prompt/result tokens, model calls and tool rounds for coding and specialist tasks; artifacts and verification must match baseline. Show both a direct-family match and discovery overhead case.

Full Go tests, vet and diff checks gate completion. Broad ablations, model-provider effectiveness, canary/default enablement and quiet UI remain Phase 6.

## Delivery relationship

Implement and verify Phase 4c before Phase 4d. Keep independent commits/flags and measurements so either can be disabled without changing the other. The implementation plan must pin exact interfaces, constants, migration commands, family metadata and test seams from these specs. Completion of both slices closes Phase 4; it does not imply Phase 6 release targets have been met.
