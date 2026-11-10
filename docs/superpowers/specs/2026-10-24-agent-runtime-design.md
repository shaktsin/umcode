# General Agent Runtime — Design Spec

Status: draft for review. Date: 2026-10-24 (revised: plan-based activation, no roles, chat-assigned models with fallback).
Mockups: https://claude.ai/artifact/6bvUZy7PrdfjPTGdiDrijK (private to the author; screens referenced below as M1–M5).

## 1. Goal

Replace UMCode's fixed two-role, one-worker delegation flow with a **general agent runtime**:

- The model spawns agents **dynamically**, with a model-chosen name and assignment.
- An agent's **authority comes only from granted capabilities**, never from its name.
- Any number of agents may exist over a task's lifetime; concurrency, depth, total count and budget are bounded by limits.
- Parallel writers work in isolated workspaces and their revisions are integrated one at a time, then verified.
- The user stays in control: agents start only after explicit user approval, offered by a model suggestion.

Non-goals (v1): cross-thread agents, agents surviving engine restart (they are marked interrupted, as today), automatic merge-conflict resolution by the scheduler, per-agent model pools beyond what the router already provides, reuse of the legacy `agent_teams` tables.

## 2. Current state (verified in code)

| Fact | Where |
| --- | --- |
| One active child per task; `Spawn` refuses with `BusyError` when any child is active | `internal/delegation/lifecycle.go` (`busy`, `Spawn`, `launch`) |
| Spawn schema hardcodes `role` enum `implementer` / `reviewer`; tools are `agent.spawn/status/wait/followup/cancel` | `internal/tools/delegation.go` |
| Tool allowlist branches on `child.Role == "reviewer"`; reviewers get no shell (`childPolicyError`: read-only sandbox not enforceable) | `internal/engine/delegation_policy.go` |
| Child authority is intersected with the parent's and rechecked every turn | `narrowChildToolAuthority` |
| Delegation is admitted per turn at `turn/start` via a user activation token or task ID; pinned to project root and revision; children cannot activate or inherit delegation | `internal/engine/delegation.go` (`prepareDelegationAdmission`) |
| Requires a committed Git project; UI switch "Agents for next message" turns off after sending | `docs/subagent-development.md`, `AgentsMenu.svelte` |
| Completion requires the lead's own successful check receipts on the exact integrated revision | `docs/subagent-development.md` |
| Storage: `agent_tasks`, `agent_children`, follow-up and workspace tables (migrations 0025, 0027, 0029) | `internal/store/migrations` |
| Sandbox policy supports a writable root and specific read-only paths; no verified whole-root read-only shell policy | `internal/sandbox/sandbox.go` |
| UI: collapsed "Agents" bar above composer, flat child cards, inline approvals, inspector for files | `app/frontend/src/lib/components/Agents.svelte`, `ChatColumn.svelte` |

Reference (OpenAI Codex, `codex-rs/core/src/agent`): hard caps (`agent_max_threads`, `agent_max_depth`), roles as config-layer files that may only narrow the parent's authority, children copy the parent's live approval policy and sandbox, per-agent mailbox messaging, `fork_context`. Codex has no isolated workspaces or revision integration; UMCode keeps those.

## 3. Decisions

1. **Build the new runtime beside the current flow, then migrate** (not an in-place rewrite). Legacy tasks keep working until cut-over.
2. **Name is cosmetic; capabilities are authority.** Granted capabilities = parent's capabilities ∩ approved plan entry ∩ request. Authority only narrows down the tree.
3. **No hardcoded or configured roles.** The model invents each agent's name, purpose and capabilities in a plan the user approves. There are no built-in role files and no Settings screens for agents or routing.
4. **Hard safety rails plus budget slices:** `maxConcurrent`, `maxDepth`, `maxTotal`, and a budget slice reserved from the parent at spawn.
5. **Two-step, user-approved activation.** (a) The model suggests using agents (`agent.suggest`); approval starts a follow-up turn carrying the activation token. (b) In that turn the model proposes a plan (`agent.propose_plan`: agents, capabilities, models, limits); nothing starts until the user approves it. Per-message activation is kept; no project-level default.
5a. **Models are assigned in chat.** The user sets which model each kind of agent uses on the plan card (dropdowns) or by telling the lead in chat ("gpt-sol for reasoning, qwen for implementation"); the lead revises the card. The approved assignment is saved on the thread and prefilled into later plans. If an assigned model is unavailable, the agent **falls back** to the next compatible model in the pool and the fallback is shown.
6. **Integration is an explicit stage**, owned by the lead (or an integrator agent), never silent in the scheduler.
7. Keep: `agent_teams` tables untouched; budget accounting, model router, approval checks, per-turn policy recheck.

Defaults (proposed, configurable): `maxConcurrent=8`, `maxDepth=3`, `maxTotal=40`. Revisit after measuring.

## 4. Architecture

### 4.1 Data model

New table `agents` (migration `0034_agent_runtime.sql`), separate from `agent_children`:

| Column | Notes |
| --- | --- |
| `id`, `task_id` (FK `agent_tasks`), `parent_agent_id` (null = lead) | |
| `name`, `assignment_json` | `AgentAssignment` reused (prompt, acceptanceChecks, contextRefs) |
| `purpose`, `plan_id` | free-text purpose written by the model; link to the approved plan entry |
| `capabilities_json` | resolved grant (see 4.2), never a reference to the role |
| `model_requested`, `model_actual`, `route_trail_json` | requested = the plan's assignment; actual = what ran; trail records fallbacks |
| `budget_slice`, `depth`, `context_mode` | `none`/`summary`/`full` |
| `workspace_id` | null for agents without file-write capability |
| `state`, `usage_json`, `revision`, `result_json`, `version`, timestamps | optimistic update via `version`, as today |

New table `agent_plans` (per thread): `id`, `thread_id`, `task_id` (nullable), `plan_json` (agents: name, purpose, capabilities, model; limits), `state` (`proposed`/`approved`/`superseded`), `created_at`, `approved_at`. The latest approved plan supplies the prefilled model assignments for later plans.

New table `agent_messages` (mailbox): `id`, `task_id`, `from_agent_id`, `to_agent_id`, `body`, `created_at`, `delivered_at`. Replaces the two-round follow-up limit with budget-based limits.

### 4.2 Capabilities

```
files:   none | read | read-write
shell:   none | read-only | read-write
network: false | true
spawn:   { maxChildren: N }   // 0 = cannot delegate
```

- Resolution: `granted = parent ∩ approved plan entry ∩ request`. Fields dropped by intersection are returned in `rejected` with a reason; the agent is still created (partial grant).
- Child tool allowlist is **derived from capabilities** (replacing the `Role == "reviewer"` branch in `childToolAllowed`): `file.read/list/search` need `files>=read`; `file.write/edit` need `read-write`; `shell.run`/`verification.run` need `shell`; `agent.*` need `spawn`.
- **`shell: read-only` must be enforced by the sandbox, not by prompt.** v1 requires a new sandbox policy that mounts the whole workspace read-only; until that exists and is tested, `shell: read-only` is rejected (downgraded to `none`) and reported. This is the same reason reviewers have no shell today.
- Approval mode: child inherits the parent's live approval mode and can only be stricter (existing rank logic in `narrowChildToolAuthority`).

### 4.3 Plan-defined agents (no roles)

An agent has no built-in type. Its name, purpose and requested capabilities come from an approved plan entry written by the model; the plan card shows capability chips prominently (files, shell, network, spawn) so the user can see exactly what each agent may do before approving. There is no name-based authority and no role file in v1. A user who wants a standing convention says it in chat (4.10).

### 4.4 Admission (`agent.spawn`)

In order; a refusal names the field.
1. Delegation admitted for this task and caller is a live agent of it (re-read from storage each time).
2. Capabilities resolved per 4.2 (partial grant allowed).
3. The agent matches an entry of the approved plan, and its model is the entry's assigned model (or a compatible fallback, 4.10). A model outside the approved plan is refused with a message telling the lead to propose a revised plan. With one model and no pool every agent inherits that model.
4. Budget slice ≤ parent's remaining budget; reserved at spawn, returned on cancel/finish.
5. `depth ≤ maxDepth`, `total ≤ maxTotal`. Exceeding `maxConcurrent` **queues** the agent instead of failing.
6. Idempotent by tool-call ID (keeps `AgentChildForCall` behaviour).
7. The approved plan's limits and capabilities (4.8) are hard ceilings here and cannot be exceeded by the model.

### 4.5 Scheduler and lifecycle

Replace the single `slots[taskID]` with a per-task set. A scheduler promotes `queued → running` as `maxConcurrent`, provider capacity and machine resources allow (FIFO per task). Cancel is per agent and cascades to descendants, retains partial results and workspace edits, and returns unspent budget. On engine restart, running agents become `interrupted` (never auto-resumed), unchanged from today.

### 4.6 Context and messaging

`context: none | summary | full` on spawn (full = fork of the parent's history). `agent.message` appends to the target's mailbox; delivery at the next safe boundary (reuses the `followupQueue` mechanism).

### 4.7 Integration

Each file-writing agent produces committed **revisions** in its isolated workspace. Integration is sequential: accept → apply on current integrated state → verify (lead runs acceptance checks on the integrated result) → next. Conflicts leave both versions intact and surface a **Resolve** action with three options: send back to author, spawn an integrator agent, or resolve manually. The task completes only when every non-cancelled revision is merged or explicitly dropped **and** the lead's own successful check receipts exist for the final revision (existing rule kept). Replaces the single-implementer assumption in `delegation_workflow.go`.

### 4.8 Two-step activation: suggest, then plan

**Step 1 — suggest agents.** Tool `agent.suggest { reason }`, always available, no authority, renders a short card (M1): *Enable agents for this message* / *Continue without agents*. Rules: at most one suggestion per user message; none after a decline in the same thread unless the user asks; only in a committed Git project. Delegation cannot be enabled mid-turn (admission is decided at `turn/start`), so the suggesting turn ends and the approval submits a follow-up turn carrying the activation token (via `chat.enableDelegation`).

**Step 2 — propose the plan.** In the enabled turn the lead calls `agent.propose_plan { agents[{name, purpose, summary, capabilities, model}], limits{maxConcurrent, sliceShare, network} }`. This renders the plan card (M1b) and waits for the user, reusing the existing approval-wait state and `ApprovalCard` plumbing. The user can:
- change the model per agent with the dropdown (choices come from the enabled models/pool);
- tell the lead in chat ("gpt-sol for reasoning, qwen for implementation"); the lead calls `agent.propose_plan` again, superseding the card;
- edit limits, drop an agent, or decline.
Approve stores the plan in `agent_plans` (state `approved`) and the lead may then spawn. Explicit user activation and installed-skill activation remain as today; the plan step still applies.

### 4.9 Tools (final set)

`agent.suggest`, `agent.propose_plan`, `agent.spawn`, `agent.list`, `agent.status`, `agent.wait` (any/all), `agent.message`, `agent.cancel`, `agent.integrate`. Legacy `agent.followup` is removed after migration.

### 4.10 Model assignment and fallback

- The unit of assignment is a plan entry's model. The model proposes a default (for example the thread's model); the user's dropdown choice or chat instruction overrides it.
- The approved assignment is saved with the plan. The lead's context for the thread includes "previously approved model assignments"; later plans are prefilled from them, but each new plan still needs approval.
- Models offered are only the enabled configured models/pool members. Compatibility (tool support, context window) comes from the existing capacity profiles; incompatible models are not offered, and a warning is shown if a chat instruction names one.
- **Fallback (chosen):** if the assigned model fails (rate limit, quota, billing, cooldown), the existing router moves to the next compatible model in the pool and the agent continues. `model_requested` stays the assignment; `model_actual` and `route_trail_json` record what ran. The UI marks the agent row "fell back to X" and the lead is told in the agent's status so it can react.
- The lead keeps the model the user picked in the composer; assignments apply to spawned agents.

## 5. UI (matches M1–M5; follows the existing chat, light theme tokens from `app.css`)

- `ChatColumn.svelte` / `Agents.svelte`: collapsed bar keeps its place; expanded view becomes a tree with capability chips, per-agent Cancel, integration strip with **Resolve**, and inline approvals as today.
- Selected agent opens in the **inspector** (assignment, granted/rejected capabilities, model requested/actual, budget slice, mailbox, diff).
- New transcript cards (`ItemView.svelte` item kinds): `AgentSuggestion.svelte` (step 1) and `AgentPlan.svelte` (step 2: agents with capability chips, model dropdown per agent, limits, Approve / Revise / Decline).
- The Agents bar shows the current model assignments and any fallback badge.
- `AgentsMenu.svelte`: toggle unchanged. No project-level default, no Settings screens.
- `lib/delegation.ts`, `stores/delegation.svelte.ts`, `types.ts`: tree model (parent ids), new states (`queued`, capacity wait), integration state.

## 6. Migration plan

1. **Phase 0 — spec and sandbox spike:** prove or reject an enforceable read-only shell policy on macOS (seatbelt) and Linux (bwrap).
2. **Phase 1 — runtime beside legacy** behind a feature flag: tables, capability resolver, plan-bound admission, scheduler, tools (without suggest). Legacy path unchanged.
3. **Phase 2 — integration stage** for multiple revisions; lead verification gate.
4. **Phase 3 — suggest-then-confirm** tool, activation follow-up turn, limits ceilings.
5. **Phase 4 — UI** tree, inspector, suggestion card.
6. **Phase 5 — cut-over, no soak period:** as soon as the new flow passes its full test suite (section 7) and the legacy scenarios worth keeping have been ported to it (sequential implement-then-review, cancellation, budget stop, restart recovery, approvals), delete the legacy code in the same change: `internal/delegation` coordinator and lifecycle, legacy `agent.*` tools and the `AgentRole` role strings, the role branch in `delegation_policy.go`, `delegation_workflow.go`, the flat child cards in `Agents.svelte`, their tests, and the `agents.runtime_v2` flag (the new runtime becomes the only path). Reused pieces stay: budget, workspaces, snapshots, admission token logic, `narrowChildToolAuthority`. Plan 5 begins by listing every legacy reference (including the role strings) so nothing is missed. Unfinished legacy tasks are marked interrupted on upgrade. The legacy `agent_children` tables and their data are **not dropped in this change**; they remain read-only history and are removed by a separate later migration, because dropping them destroys users' past task records.

## 7. Testing

- Unit: capability intersection (including "child never exceeds parent" property test), allowlist derivation, admission ordering and error messages, budget reserve/return, depth and count limits, scheduler fairness and queueing, idempotent spawn.
- Integration: concurrent writers in isolated workspaces; sequential integration with a forced conflict; cancel cascade returning budget; restart recovery marks agents interrupted; suggest → approve → follow-up turn carries token; decline suppresses repeat suggestions.
- Security: model cannot gain authority by name or role string; cannot enable delegation without a user token; children cannot activate delegation; shell read-only enforced at the sandbox (write attempts fail).
- Plan and models: plan approval gates spawn; spawn outside the plan is refused; chat instruction revises the card; approved assignments prefill the next plan; fallback records requested vs actual model and respects capacity compatibility.
- Frontend: tree rendering, suggestion and plan card actions, inspector content (vitest, as in `Agents.test.ts`).
- Migration: legacy delegation tests keep passing under the flag-off path.

## 8. Risks and open questions

1. **Read-only shell enforcement** (4.2) is unverified; Phase 0 decides whether reviewers get a shell in v1. The mockups show `shell:ro` chips on reviewers and the hunter; they assume Phase 0 succeeds, otherwise those chips read `shell:none` in v1.
2. Default limits (8/3/40) are guesses; need real usage data.
3. Integrator-agent option increases complexity; could be deferred to v1.1 (keep "send back to author" and "resolve manually").
4. `context: full` fork cost; may need a size cap.
5. Model-authored capability requests rely on the user reading the chips; the plan card must make `files:rw`, `shell:rw`, `network` and `spawn` prominent, and `shell` stays `none` until the sandbox spike passes.
6. Interaction with compaction (`compaction.go`) and shared usage accounting under many concurrent agents needs review during Phase 1.
7. Reviewed in conversation: record/admission/scheduling, two-step activation, UI, model assignment and fallback. **Capabilities (4.2), integration (4.7) and migration (6) are proposed here for the first time and need explicit review.**
