# Subagent development

Delegation is an explicit, bounded workflow inside one parent chat. Use **Enable
agents for next message**, then submit the assignment. The action can create an
empty chat in the selected project; enabling alone does not send a message. A
submitted canonical installed `superpowers:subagent-driven-development` skill
also enables the workflow. Selecting a skill, ordinary prose, Deep mode and
model pools do not enable it.

A committed Git project with clean inputs is required. Each implementer works in
an owned isolated checkout. One worker runs at a time, followed by an independent
reviewer of that exact committed revision. Findings return to the same
implementer, with at most two correction rounds. A corrected revision needs a
fresh independent review. Children inherit the parent's permissions and approved
models; they cannot delegate further.

The lead integrates approved changes only while the parent's files still match
the admitted baseline. Parent edits or conflicts block integration and preserve
both versions. Completion requires the lead's actual successful check receipts
for the exact integrated revision. Worker summaries or reported check strings
cannot satisfy lead verification. Failed, blocked and unrun checks stay visible.

The compact **Agents** section starts collapsed. Expand it for assignments,
models, usage, corrections, results, structured findings, review verdicts and
checks. Pending approvals identify the requesting agent and use the existing
approval controls. **Cancel task** or the child Cancel action stops execution;
the parent Stop action also cancels the delegated task. Cancellation releases
approval waits and retains recoverable work. After engine restart, unfinished
work appears interrupted and never resumes provider execution automatically.

The parent and all children share the task's usage and limits. Manual context
compaction of an unfinished task shares its budget, deadline and cancellation
owner; exhausted or unavailable task accounting blocks provider execution. Reservations are
estimates, so a request already underway can exceed an estimated billing limit.
Actual usage and budget stops appear in Agents. Unavailable models or credentials
fail before provider execution.

File links open the owned committed child snapshot in the inspector. Repeated
links reuse the same tab for a parent/task/child/revision/path. Historical links
become unavailable after a correction supersedes the retained revision or cleanup
removes the snapshot; an already-open tab keeps its previously read content.

## Current v1 limits

- Reviewers can inspect scoped files and submit structured review reports.
  Shell, script and command verification are blocked because v1 does not offer
  an enforceable read-only command sandbox. The implementer and lead perform
  executable checks.
- Required interactive visual QA blocks automatic completion when no trusted
  visual receipt is available. A passed browser command does not manufacture
  that receipt. Agents shows the criterion as blocked.
- Cleanup conservatively retains workspaces containing ignored build outputs or
  new edits. A completed verified task can therefore show a cleanup warning;
  retained files are not silently deleted.
- Delegation requires Git and committed clean inputs. General projectless or
  non-Git delegation is unavailable.

## Agent runtime (experimental, `agents.runtime_v2`)

With `agents.runtime_v2: true` the legacy `agent.*` worker/reviewer tools are
replaced by plan-bound runtime agents. Nothing above about one worker at a time
or role strings applies there; the parent chat's lead spawns several agents
whose authority comes only from an approved plan. With the flag off (the
default) the legacy flow is unchanged and none of the tools or RPCs below exist.

### Two-step activation

Agents are never enabled by the model. Admission is still decided at
`turn/start` by an activation token that only an app client can obtain.

1. **Suggest.** In an ordinary (not admitted) turn the lead may call
   `agent.suggest {reason}`. It has no authority: it stores a suggestion card
   and tells the lead to stop. Rules, each refusal naming its rule: lead only
   (never a runtime agent or legacy child); at most one per user message; only
   in a committed, clean Git project; none while a delegation task is active;
   none after the user declined in the same chat until the user enables agents
   themselves (`delegation/enable`) or answers a later card with enable.
   The user answers with `agent/suggestion/respond {suggestionId, decision:
   "enable"|"decline"}`. Enable returns `{activationToken, expiresAt}`; the
   client submits the follow-up message with that token. Repeating a decision
   is idempotent and never mints a second token.
2. **Plan.** In the admitted turn the lead calls `agent.propose_plan {agents,
   limits}`. Each entry has `name`, `purpose`, `summary`, `capabilities
   {files: none|read|read-write, shell: none|read-only|read-write, network,
   spawnMax}` and an optional `model`. The call blocks on the plan card (an
   `agent_plan` approval whose args are the card) until the user answers with
   `agent/plan/respond {planId, decision: "approve"|"decline"|"revise",
   models?, drop?, limits?, feedback?}`:
   - approve may change models (pool members only), drop entries (not all of
     them) and change limits (raised or lowered; values above the ceilings
     are clamped to them); it stores the plan `approved`, superseding the
     chat's earlier approved plan, and the lead may spawn;
   - revise returns the feedback to the lead, which proposes again; the new
     proposal supersedes the old card ("plan was superseded");
   - decline tells the lead to continue without agents. When the task has
     no approved plan and no agents, the engine also completes that empty
     task (the same happens when the card times out), so agents can be
     enabled again later in the chat.
   A plain `approval/respond` on the card runs the same validation. Every change
   is announced on the chat as `agent/plan/updated {threadId, plan,
   approvalStatus}`. An approved plan is never rewritten; a revision is always a
   new plan.

Both RPCs are admin-only (app clients); no tool can call them. Two read-only
admin RPCs return the stored state without answering anything:
`agent/suggestion/get {suggestionId}` returns the suggestion with its state
(`pending`, `enabled`, `declined` or `expired`), and `agent/plan/get {planId}`
returns `{plan, card, approvalId, approvalStatus}` (plan state `proposed`,
`approved` or `superseded`; card status `pending`, `approved`, `denied` or
`expired`). Clients render cards from these after a reload, a restart or an
answer from another client.

### Plan limits and capabilities

A plan has 1-12 entries with unique names (case-insensitive, 1-40 characters).
Limits: `maxConcurrent` at most 8, `maxDepth` at most 3, `maxTotal` at most 40,
`sliceShare` in (0, 0.5]; zero values take the defaults (8, 3, 40, 0.25).
`limits.network` is refused when the project's effective network is off.
Requested capabilities are clamped against the lead's own capabilities and the
sandbox, and every clamp is a notice on the card, for example
`worker: shell was dropped because no sandbox (macOS seatbelt / Linux
bubblewrap) is available on this machine`. Approving never grants more than the
card shows, even if a sandbox appears later. On the card each agent carries
`capabilities` (the stored grant of the entry, the ceiling) and `granted`
(what an agent of the entry can get now, never wider). Render `granted`;
treat `capabilities` only as the ceiling. Limits on approve cannot widen an
entry: setting `limits.network` to true on approve never gives an entry
network it was not granted at proposal (the re-clamp starts from the stored
grant), so a UI should not present it as a way to add network; ask the lead
for a revised plan instead.

### Models

The models offered are exactly the task's frozen approved pool (enabled
members only). An entry's model outside the pool is refused by name; a pool
model that cannot use tools is shown as incompatible and refused on approve for
an entry that needs tools. Approved assignments prefill the next plan of the
chat. At run time an agent starts on its assigned model and falls back to the
next compatible enabled pool model when that one is unavailable; the agent
records `modelRequested` and `modelActual` (and a route trail). An entry with no
model, for example when the task's frozen selection is a pool, runs on the
first compatible pool model.

### Spawning and integration

Spawn is refused until the plan is approved and only for its entries. Writers
work in isolated checkouts of the task base; read-only agents get a view.
Integration is sequential through `agent.integrate`
(`accept|drop|resolved|amend|status|complete`); each accepted state is planned
and verified by the lead's own checks before the next accept, and completion
requires passing receipts for the final integrated state.

- **Late spawns (G1).** Agents can be spawned at any time except while an
  accept is being applied. A read-only agent spawned after an accept sees the
  integrated state; a writer still starts from the task base, so a writer that
  touches integrated lines conflicts at accept. The project checkout is never
  changed by a spawn.
- **Amend (G2).** When the integrated state cannot pass its checks, the lead
  fixes the files in the project checkout, stages them and calls
  `agent.integrate {action:"amend", reason}`. The staged tree becomes the new
  head (no merge), verification starts again, and no revision changes state.
  Amend is refused with the path for unstaged or untracked files, a moved HEAD,
  conflict stages or a staged gitlink. After an amended task completes, the
  applied agents' workspaces are kept (an amend can rewrite an agent's files,
  so a workspace may hold the only copy of its original result) and the
  completion warning says so. A cancelled task's report mentions the
  amendments and the latest reason.

### Restart

Pending cards do not survive an engine restart: the plan card's approval
expires and the plan stays `proposed`, a pending suggestion becomes `expired`,
and the unfinished delegation task is interrupted, as for every task. Nothing
starts and the stale card cannot be approved. The user enables agents again
and the lead proposes a new plan; an interrupted suggestion or plan call does
not block later actions in the chat.

### Known limits

- No send-back-to-author or integrator agent for conflicts: drop, resolve by
  hand, or finish and start a new delegated task.
- Writers after the first accept start from the base revision, not the
  integrated state.
- Pending suggestion and plan decisions are kept in memory; after a restart
  repeated answers are judged conservatively.
- Not yet: budget-slice reservation display, `context` full/summary semantics,
  resource-aware promotion, context-window compatibility of models, and the UI
  cards (the cards are tool results plus the RPCs above).

## Local validation

The server acceptance tests use the real Unix RPC server, engine, Git workspaces
and verification tools with a deterministic fake provider; they make no paid API
calls. Run `go test ./internal/server -run Delegation -count=1`. Existing engine,
coordinator and workspace tests cover restart recovery, cancellation, exact review
and integration guards. On hosts with restricted local sockets, these tests need
the normal approved host test environment.

`node app/frontend/e2e/delegation.mjs` renders the real Svelte app against a
controlled RPC fixture. It checks 320, 720 and 1200 pixel widths in light and dark
themes, keyboard activation, results, approvals, cancellation, reconnection,
errors and repeated snapshot tabs. See the frontend e2e README for the existing
Playwright/Chrome runtime options. This is renderer acceptance; it does not
validate a signed native app or supply the workflow's trusted visual QA receipt.
