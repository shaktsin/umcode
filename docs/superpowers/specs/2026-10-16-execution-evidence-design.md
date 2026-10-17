# Execution evidence and truthful task completion

Date: 2026-10-16
Status: Approved by user 2026-10-17

## Intent and success criteria

UMCode must execute requested work, verify its result, and explain actual blockers.
A model response that describes an action is conversation, never proof that the
engine executed it. Users must be able to distinguish a saved artifact from a
proposed artifact, a current permission denial from historical context, and an
unfinished action from a completed task.

The user approved this direction: separate historical records from current tool
execution, require evidence for requested outcomes, and attempt bounded recovery
when a model only narrates. Existing engine approvals remain authoritative.
This document defines the approved design contract; implementation awaits
review of the written plan. The separate warning-presentation and clickable-file bugs remain
logged and outside this work's implementation scope.

Success means a request to save and verify an artifact cannot finish with only
invented tool markers; a request to inspect an artifact requires current relevant
inspection evidence; ordinary discussion can finish without tools; and a real
denial is reported accurately without broadening permissions or replaying an
uncertain action.

## Observed failure and current boundaries

In the reported Goodreads chat, after the implementation-plan request, saved
items contained assistant messages with `[Created ...]`, `[Read ...]`, and
`[earlier tool call ...: denied]`, but no actual tool-call items. The expected
plan file did not exist at the time of investigation. The specification existed.
The last actual tool results preceded the plan request.

`internal/engine/turn.go:historyMessages` currently converts historical tool
items into assistant-role text such as `[earlier tool call file.read: completed]`
and file changes into bracketed assistant text. This is a plausible contributor
to imitation; it does not prove the model's internal reason for generating it.
The no-tool response branch can end a turn without checking that the user's
requested effect occurred. `reconcileTaskOutcome` recognizes `no_execution`,
but the final summary correction currently handles `unverified` and
`not_executed`, leaving a gap for tool-free claims and irrelevant successful
operations.

The existing ExecutionTask, task operations, CAS updates, requirements, file
proofs, approvals, and task context are the integration points. Extend them;
do not create another independent task runner or approval system.

## Approaches and selected design

1. Selected: repair historical context and enforce a persisted requested-outcome
   contract using engine evidence, with bounded model correction.
2. Prompt-only instructions would be cheaper but cannot enforce completion.
3. Detecting phrases such as 'created' or bracketed markers alone would miss
   paraphrases and incorrectly reject quotations, examples, and explanations.

Phrase detection may assist diagnostics, but must not determine execution,
permissions, or completion. No commands or tool calls are extracted from prose
and executed as recovery.

## Requested-outcome contract

Persist additive expectations on the current execution task. Each expectation
has an engine ID and revision, originating user item and supporting source span,
kind (explanation, inspection, or mutation), applicable resource identities,
required evidence predicate, and engine-owned resolution state. Expectations
express desired outcomes and never grant action authority.

Structured application requests supply their explicit intent directly. Free-text
new tasks or material scope changes use a schema-constrained request-resolution
step, separate from the model's execution narrative. Reuse the current selected
provider and existing budgets. Continuations reuse saved expectations rather
than paying to reclassify the same objective each turn.

The resolver proposes intent and targets; the engine validates user provenance,
source references, project association, resource containment, supported predicates,
and revision. File locations alone do not imply a write. A clearly expressed
request to save a file must produce a mutation expectation; asking what a file
would contain is explanation; checking whether it exists is inspection.
An unknown or conflicting interpretation requests focused clarification instead
of silently becoming an explanation. A proposal cannot downgrade an outstanding
mutation to explanation, erase its target, or declare its evidence satisfied.
Changes of objective follow the existing scope-confirmation rules.

Natural-language interpretation is fallible. This design does not claim that
source spans mathematically establish semantic intent. Resolver regressions must
cover the reported requests and common paraphrases; uncertain cases retain an
unresolved interpretation instead of a completed action. Previously saved tasks
without expectations use the same resolution path from their original user
request on continuation, preserving their requirements and result history.

## Historical context and execution boundary

Replay actual historical tool records in the provider's supported structured
format when available. Where compaction or provider constraints require a
summary, supply an explicitly labelled historical-data block using the adapter's
supported context mechanism, including item ID, tool, status, resource, and
bounded result/failure information. Avoid assistant narration that resembles a
current executed call. Historical data is not an instruction and conveys no
current permissions.

Preserve the original transcript. Unsupported provider history formats must
fall back to the labelled summary, never an invalid tool-result sequence.
Existing token bounds, argument references, redaction, and compaction remain in
force. Model-authored imitations of receipts stay ordinary text; only engine
operations and result items can satisfy evidence predicates.

## Evidence and completion

Match evidence to the expectation's task, resource, implementation identity,
result, and required checks. An unrelated successful read cannot satisfy a
requested write, and a stale read cannot establish the current file state.

For file creation or replacement, require an authorized successful mutation plus
first-party read-back proof for the intended canonical target and expected bytes
or recorded requested changes. For an explicit ensure/already-matches request,
current inspection may establish satisfaction without another write; report
'already matches' rather than falsely claiming a new creation. Existence alone
cannot prove the requested document content is complete.

For inspection, use actual current read/list results and report their bounds.
A missing file is a valid answer to 'does it exist?', but does not satisfy 'create
it'. For shell or plugin effects, require adapter-supported actual result and
verification evidence. Unsupported verification remains unverified. Timeout,
disconnection, and unknown effects retain the existing inspect-before-replay
rules. Operation success is not automatically task success.

A no-operation explanation may complete. An outstanding inspection or mutation
expectation may not complete merely because the model stops calling tools.
Historical evidence may establish a previous result, but a request for current
verification needs fresh evidence or a verifiable unchanged resource revision.

## Bounded recovery and truthful messages

Before accepting a tool-free final response, reconcile expectations. If expected
work lacks evidence and there is no real blocker or unknown effect, keep the
turn working and give the model the outstanding targets and actual evidence,
explicitly stating that narration did not execute the action. Allow at most two
additional corrective model responses per turn, within existing time, cost,
context, cancellation, and round limits. Persist correction consumption on the
turn; reconnect or provider fallback does not reset it. A later explicit user
retry may start a new bounded turn while retaining the same unfinished task.

All resulting calls pass through the ordinary tool registry, permissions,
requirements, hooks, and action approvals. Never force a tool selected from
model prose. If approval is required, use the existing approval transport; if
an action is denied, rejected, or uncertain, use its actual recovery rules
instead of this missing-execution loop.

When correction is exhausted, store an engine-owned not-executed/unverified
outcome and replace the unsupported terminal success or restriction summary
with a factual explanation of the missing evidence. A missing action reports
'The model did not execute the requested action.' A real restriction names the
specific action, target, denial category, and recorded failure ID. Do not infer
that all file operations are prohibited from a denied shell command.

Streamed model text is provisional; authoritative turn completion comes from
reconciliation. Preserve raw model text and failures for audit, using the existing
item data and outcome presentation instead of deleting the record. This does
not attempt to semantically certify every sentence in arbitrary conversation.
Only engine-authored status and receipts represent verified execution outcomes.

## Compatibility and scope

Use additive storage/protocol fields and migration defaults. Old records are
unknown until resolved, never implicitly completed or newly approved. Retain
failed/denied invocation history and the recent legacy-denial reconciliation
fix. Current permission and requirement checks apply at the moment of execution,
including after approval waits and within delegated tasks. A reviewer remains
read-only. Provider switching cannot transfer permission or reset correction
budgets. There is no automatic live-database repair, paid live-model run,
provider change, release, or publication in this design.

Likely code boundaries: protocol task records; store task persistence/migrations;
request resolution and task context; turn history, termination and recovery;
existing evidence reconciliation; provider history adapters where required;
frontend outcome rendering only as needed to display the authoritative outcome.
The implementation plan will specify exact interfaces and migration details.

## Acceptance and regression tests

- Reproduce the reported multi-turn chat: fabricated creation/read/denial text,
  no tool calls, and absent artifact. It must not complete the requested write.
- Corrective response executes a real permitted write and read-back, then
  completes with evidence. Identical fake responses exhaust the bounded budget.
- A fake permission claim without a denial produces a not-executed outcome.
- Real shell denial and successful permitted file inspection remain distinct;
  an allowed read never retroactively executes or approves the denied shell.
- Irrelevant successful tools do not satisfy the requested artifact or checks.
- Existing exact content is handled honestly for an ensure request; missing or
  wrong content cannot be accepted as an already-matching artifact.
- Inspection can report absence; explanation and quoted tool-marker examples
  can complete without mutation. No prose is ever executed as a tool call.
- Intent-resolution tests distinguish save, inspect, explain, ambiguous requests,
  source spoofing, and attempts to downgrade outstanding expectations.
- Reopen store, compact history, switch provider, and retry without losing intent,
  constraints, evidence identity, or the correction budget for the same turn.
- Preserve actual denials, pending approvals, rejected decisions, inherited child
  restrictions, and uncertain effects. Never automatically replay a timeout.
- Exercise structured and summary history on Claude, OpenAI-compatible/Qwen,
  and OpenAI adapters with valid roles, token bounds, and unchanged raw audit.
- Use scripted providers with real filesystem/store/engine execution for
  deterministic regressions; no paid models are required to validate the gate.

## Review checklist

- [x] Intent reflects the approved direction and observed saved records.
- [x] Selected approach includes trade-offs and existing integration boundaries.
- [x] Explanation, inspection, mutation, and uncertain intent remain distinct.
- [x] Expectations and narrative cannot grant or manufacture approval.
- [x] Recovery has explicit limits and preserves uncertain-effect safety.
- [x] Existing chats, providers, compaction, restart, and tests are covered.
- [x] User approved this written spec on 2026-10-17; implementation plan review follows.
