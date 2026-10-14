# Phase 6b: Quiet inspection, memory review, and storage controls

Status: Proposed written spec for user review. Conversational design accepted; implementation has not started.

## Intent and constraints

Make token optimization understandable and manageable on demand while keeping ordinary chats quiet. The user approved a response-menu “Turn details” action that opens the existing right-hand inspector. One global master switch enables all optimization components automatically. No component opt-ins or extra optimization approvals are introduced. Normal action permissions remain effective.

This builds on merged PR 55 and the existing curated memory, usage accounting, vault index, and retention collector. It does not run paid qualification, change optimization defaults, claim measured savings, or perform Phase 6c rollout.

## Chosen approach

Extend existing surfaces: the inspector for turn details, project settings for memory review, and global optimization settings for storage and retention. Keep developer diagnostics collapsed inside turn details. This provides one coherent management flow without a new dashboard framework.

A read-only dashboard alone would leave memory correction and retirement unfinished. Separate optimization dashboards would duplicate navigation and fragment controls.

## Turn details

Add “Turn details” to the response action menu. Multiple agent messages from one turn open the same inspector tab, identified by turn ID. The inspector fetches details on demand; reopening a running turn refreshes its partial snapshot. Completed details remain inspectable after restart.

Show:

- Overview: requested/resolved model, route attempts, status, duration, and the effective optimization policy captured when the turn was admitted.
- Tokens and cost: aggregate recorded input, cached input, output and reasoning tokens, request count, and recorded cost across model calls. Provider-reported and estimated values receive explicit labels.
- Context: bounded source references, retrieval selections, memory references, compilation estimates and fallback reasons captured during execution. Labels distinguish selected sources from content actually submitted to a provider; only record submitted membership when it is known.
- Tools: actual calls and counts, schemas/capabilities loaded, and reducer outcomes where measured.
- Developer details: versioned diagnostic fields, completeness, measurement origins, and technical fallback reasons.

Missing values are nullable or carry an availability state, never inferred from zero. Legacy turns show “Not recorded for this turn” for new diagnostic fields. Unknown prices show “Cost unavailable”; partially priced turns show a partial cost with that label. Running/failed/interrupted turns retain recorded partial usage. No per-turn percentage savings is fabricated from token counts.

Persist a bounded, versioned diagnostic record separate from conversational items. Snapshot policy at admission and collect facts at existing context, retrieval, tool, and accounting boundaries. Inspection must not recompute historical policy from current settings or current pricing. Older aggregate usage remains usable but does not prove that its cost was fully priced.

Expose a typed `turn/details` read method, validating the turn and its owning thread. Avoid storing raw prompts, tool output, credentials, promotion byte snapshots, or unrestricted filesystem paths in diagnostics. Cap lists and record omitted counts. Detail capture must not cause an otherwise valid turn to fail; report incomplete diagnostics honestly. Read failures show a retryable inspector error.

## Project memory review

Add a Memory section to project settings. List active generated/adopted knowledge by default, with optional historical entries. Show text, category, target file, source work/revision, evidence references, ownership, status, and promotion time. Pagination bounds reads.

Provide typed project-scoped list, correction and retirement operations through the memory service rather than direct UI file edits. Mutations carry expected row revision/hash and current target hash. Reject stale edits with a refreshable conflict; never silently overwrite changed files.

Correction creates a successor record with explicit user provenance; it must not manufacture verification or silently restore automatic ownership. Retirement excludes the entry from future generated memory selection and removes its unchanged generated bullet where ownership is proven. Neither operation alters unrelated bytes or user-owned/adopted text. When a user-owned or externally edited bullet remains in UMCODE.md, explain that it still exists as a user instruction and provide an ordinary file-opening action; do not claim full removal from model context.

Use recoverable file/database operations and compare-and-swap checks consistent with existing promotion recovery. Preserve provenance and audit history. Incomplete mutations are recoverable at startup. Explicit user corrections/retirements remain available with optimization disabled. Existing active turns retain already-admitted context; later turns observe the resulting state.

Prevent retired knowledge from immediately reappearing through automatic promotion of an old candidate: record retirement lineage and suppress unchanged candidates for that semantic key/source. New evidence or an explicit user correction can establish a successor with traceable provenance.

## Storage and retention

Extend global optimization settings with indexed vault object count, stored bytes, and currently reclaimable bytes. Label these as vault statistics rather than total application disk usage. Shared content is counted once globally; do not invent project-specific byte attribution.

Expose persisted retention get/set methods with positive whole-day values bounded to 1–3650 days. Initial values resolve from existing configuration/defaults: raw output 30 days, stale output 30 days, large blobs 90 days, redacted output 7 days. Persisted settings override configuration. Changing retention affects subsequent collector passes without restart; background and manual collection use a consistent policy snapshot.

“Clean up now” invokes the existing collector for expired, unreferenced objects and returns actual deleted objects/freed bytes, then refreshes statistics. Active evidence and evidence supporting live promoted memory must remain referenced, including after the source chat/work is deleted. Audit reference accounting before exposing cleanup and add missing durable memory protection where necessary. Collector and memory mutations serialize/recheck references so newly live evidence cannot be deleted between snapshot and removal.

No project purge or forced deletion of referenced objects is included in this slice: the current vault is shared and those operations need a separately defined ownership/deletion contract. Individual knowledge retirement and safe vault cleanup provide the controls here. Automatic retention remains quiet and requires no chat approvals; a manual cleanup click is the explicit user action.

## Interfaces and implementation boundaries

Protocol types describe availability, diagnostic versions, memory ownership/conflicts, retention values, and cleanup reports. Store migrations persist diagnostic snapshots, mutation lineage and retention overrides as needed. Engine methods orchestrate turn inspection and storage management; the memory service owns instruction-file reconciliation; the work collector owns object reclamation. Server routes expose typed methods. Svelte controllers handle loading, stale responses, disconnects, errors, and retry without optimistic claims of successful mutation.

No generic telemetry pipeline, new routing policy, or unrelated UI refactor is required. Reuse the existing inspector tab lifecycle and settings styling. Inspection reads and explicit management remain accessible when the master switch is off.

## Verification and acceptance

Verify admission-time policy survives settings changes/restart; legacy and missing measurements differ from zero; costs preserve partial/unknown states; failed turns retain partial accounting; diagnostics remain bounded and exclude sensitive bodies. Exercise response-to-turn mapping and inspector retry/stale-response behavior.

Verify project isolation, stale mutation rejection, user-owned text preservation, generated correction/retirement, crash recovery, and retired-candidate suppression. Verify retained live-memory evidence after chat/work deletion and reference changes during collection. Verify retention validation/persistence, manual/background consistency, and actual cleanup reporting.

Run focused Go and frontend tests, full Go tests/vet, and frontend typecheck/build. Inspect the resulting UI at normal and narrow inspector widths, including empty, loading, legacy, partial, and error states. Success means users can inspect a turn, review/manage project knowledge, and safely manage vault retention while ordinary chat remains free of optimization prompts.
