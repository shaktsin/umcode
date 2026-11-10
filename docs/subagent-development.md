# Subagent development

Agents are an explicit, bounded workflow inside one parent chat. Use **Enable
agents for next message**, then submit the assignment. Enabling can create an
empty chat in the selected project; it does not send a message. A submitted
canonical installed `superpowers:subagent-driven-development` skill can also
enable agents. Selecting a skill, ordinary prose, Deep mode and model pools do
not grant agent authority.

A committed Git project with clean inputs is required. The lead proposes a plan
and waits for the user's approval. Each approved plan entry bounds an agent's
files, shell, network, model and ability to spawn agents. Write-capable agents
work in isolated checkouts; read-only agents can inspect the integrated state.
Several agents can work under the shared task budget. The lead decides which
submitted revisions to accept, resolves conflicts, runs the planned checks on
each integrated head and completes through `agent.integrate`. Agent summaries
and claimed checks do not count as lead verification.

The collapsed **Agents** section shows the runtime tree, model choices,
capabilities, usage, results and integration status. Agent approvals appear in
the parent chat with their requesting agent. Cancel an agent or the task from
that section; the parent's Stop action also cancels the task. Cancellation
releases approval waits and keeps recoverable work. On restart, unfinished
agents become interrupted and do not resume automatically; submitted revisions
remain available for the lead to inspect. An earlier task created by the
retired worker flow is interrupted during upgrade if unfinished. Its records
remain in a read-only **Earlier agent run** list.

The lead and all agents share the task's usage and limits. Manual context
compaction of an unfinished lead task shares its budget, deadline and
cancellation owner. Actual usage and budget stops appear in Agents. A request
already underway can exceed an estimated billing reservation.

## Agent workflow

### Two-step activation

Agents are never enabled by the model. Admission is still decided at
`turn/start` by an activation token that only an app client can obtain.

1. **Suggest.** In an ordinary (not admitted) turn the lead may call
   `agent.suggest {reason}`. It has no authority: it stores a suggestion card
   and tells the lead to stop. Rules, each refusal naming its rule: lead only
   (never an agent); at most one per user message; only
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
and running agents become interrupted. Nothing starts and the stale card
cannot be approved. The user enables agents again
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
  resource-aware promotion and context-window compatibility of models.

## Local validation

The server acceptance tests use the real Unix RPC server, engine, Git workspaces
and verification tools with a deterministic fake provider; they make no paid API
calls. Run `go test ./internal/server -run Delegation -count=1`. Engine, store and workspace tests cover restart recovery, cancellation,
plan-bound authority and integration guards. On hosts with restricted local sockets, these tests need
the normal approved host test environment.

`node app/frontend/e2e/delegation.mjs` renders the real Svelte app against a
controlled RPC fixture. It checks 320, 720 and 1200 pixel widths in light and dark
themes, keyboard activation, results, approvals, cancellation, reconnection,
errors and repeated snapshot tabs. See the frontend e2e README for the existing
Playwright/Chrome runtime options. This is renderer acceptance; it does not
validate a signed native app or supply the workflow's trusted visual QA receipt.
