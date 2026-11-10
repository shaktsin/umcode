# Prompt-first agents in an edited Git checkout

## Intent

A user can ask for agents in an ordinary message, as in Codex. UMCode shows one
agent plan for approval; approval authorizes the plan and continues the original
request without a separate **Agents for next message** action or suggestion
card. Existing staged, unstaged and non-ignored untracked files in the selected
Git project can be the agents' starting state. Agents remain isolated, and
accepted work is merged back without silently committing, stashing, deleting
or restaging the user's files.

## Approaches

1. **Keep the current clean-checkout gate and activation menu.** Smallest change,
   but it leaves both reported problems in place.
2. **Let agents edit the selected checkout directly.** Closest to Codex's
   shared-directory behavior, but agents can overwrite one another or the
   user's uncommitted work. The user chose not to use this approach.
3. **Use an isolated snapshot and merge accepted work back (chosen).** Take a
   stable, private snapshot of the user's visible project state, run the
   existing isolated-agent workflow against that snapshot, then apply only the
   accepted difference to the selected checkout after a conflict check. This
   retains UMCode's plan-scoped capabilities and reviewable integration.

## Activation and approval

- Offer `agent.propose_plan` to an ordinary lead turn in a Git project. Its
  proposal grants no agent authority. Keep `agent.spawn`, `agent.message`,
  `agent.integrate` and all other agent tools unavailable until admission.
- The tool may propose only once per user turn; it must identify the running
  lead turn and its project. Its description asks the model to use it for an
  explicit user request or work that clearly benefits from agents. The model
  may suggest a plan, but only the user's approval grants authority.
- Validate and display the proposed agents, models, capabilities and budget in
  the existing plan card, along with the count and paths of non-ignored
  untracked inputs that agents would receive. Capture a source manifest for
  this card. If it changes before approval, refuse the decision and ask for a
  fresh proposal. A decline or timeout ends the proposal without an active
  task or workspace. Revision feedback queues a new ordinary lead turn with
  the user's feedback; it can produce a replacement card.
- A proposal from an ordinary turn persists the card and returns a stop result
  immediately, ending that turn. Unlike today's admitted `agent.propose_plan`,
  it does not block the turn while awaiting approval. Approval prepares the
  private snapshot, then one database transaction creates the task, binds its
  snapshot identity, and approves the plan. A new `plan_approval` activation
  kind distinguishes these tasks from old token-based tasks. A failed
  transaction removes the prepared snapshot; restart cleans up orphaned
  snapshots left by a crash before the transaction.
- The app queues exactly one continuation turn with the task ID and original
  request context after approval. No user-authored second message or
  activation token is required. Replayed approval responses return the same
  task and never queue another continuation. A crash between approval and
  continuation leaves an approved task visible with a **Continue** action;
  no agent runs until the lead continues.
- Remove the activation menu, token RPC, suggestion tool/card and their UI
  state after the new path is covered. Keep the installed skill as a prompt
  instruction, not as a separate admission bypass. Migrate or safely expire
  pending old suggestions and tokens on upgrade.
- Projectless, non-Git and read-only/plan-mode turns may discuss agent use but
  cannot request a writable agent plan. A plan card never overrides the
  existing sandbox, network, model or per-agent capability limits.

## Snapshot and task workspace

- At approval, require a Git repository root with a committed HEAD and no
  unresolved conflict stages, unsupported index flags, nested repositories,
  submodules, escaping symlinks or special files. A clean checkout is not
  required. Ignored files are not copied; show their exclusion in the plan
  card so the user can decide whether an agent needs them.
- Create a private, self-contained task parent checkout outside the selected
  project folder. Overlay the selected checkout's visible tracked additions,
  edits, deletions and modes, plus non-ignored untracked files, onto HEAD;
  preserve contained symlinks. Capture staged and
  unstaged content as one visible snapshot; preserve the original index bytes
  separately for conflict checking. Never run user Git hooks or filters during
  capture. Detect a changing source during capture and retry or fail before
  approving the plan.
- Commit the snapshot only in the private task checkout. Store its commit ID,
  original HEAD, selected project root and a path/content manifest durably.
  Never move a ref, add a file, or write an index entry in the selected
  checkout. Keep snapshot objects reachable across restart until task cleanup.
- Runtime agents clone the snapshot commit. The existing runtime integration
  checks and head tree operate on the private task parent. Lead verification
  and admitted lead file/shell tools run against the integrated private task
  parent, with the selected project's tool policy and the task parent's
  filesystem boundary. Keep distinct durable identities for the selected
  project root, selected checkout root and private execution root. Admission
  and tool authorization revalidate the selected project and private workspace
  ownership separately; they never mistake the private execution root for the
  user's checkout or grant access to the checkout through project identity.
  Existing active tasks retain their original root mapping.

## Merge back to the selected checkout

- At completion, compute the accepted difference from the snapshot commit to
  the integrated task head. Only these paths are candidates for writing back.
  Before writing anything, verify the selected checkout is still the same
  project and original HEAD and that each candidate path and its ancestors
  match the captured manifest (including absence for new paths). Unrelated
  edits and untracked files do not block the merge.
- If a candidate path changed, has an alias, conflict stage, unsafe link or
  untracked collision, report the exact paths and keep the integrated task
  result for manual resolution. Do not overwrite the user's file. Recheck
  immediately before applying. Apply file changes with a bounded backup and
  rollback path; if an external writer races the apply, stop and report the
  retained backup rather than silently clobbering either version.
- Preserve the selected checkout's index exactly. Agent changes appear as
  unstaged modifications or untracked additions; previously staged user
  content stays staged. Do not auto-commit. A user can review and stage the
  merged files themselves.
- Record merge intent and outcome durably so a crash cannot cause automatic
  reapplication. Under the task integration lock, write the intent and
  expected pre/post path hashes before file mutation; apply and verify the
  merge; record a merge receipt; only then mark the task completed and clean
  up applied workspaces. A failed merge leaves the task active with its
  verified private head, and `agent.integrate complete` can retry after the
  user resolves the conflict. Recovery reports whether files match the
  pre-merge or expected post-merge state and requires user action for an
  ambiguous partial state. It never reruns agents or applies the merge on
  its own. Cancellation keeps the private work available until ordinary cleanup.

## Acceptance criteria

- A prompt asking for two agents produces one plan card directly; before its
  approval no agent starts. Approval continues the task. Decline, revise,
  timeout, repeated answers and restart retain their authority boundaries.
- A checkout containing staged, unstaged and non-ignored untracked inputs is
  snapshotted without changing its index or files. Agents see those inputs.
- Accepted work on unrelated paths merges while the user's dirty files and
  staging remain intact. Overlaps, new untracked collisions, moved HEAD,
  symlink/path aliases and a changing source fail without overwriting files.
- Runtime cancellation, budget, sandbox, read-only agents, verification,
  integration recovery and the history UI still work. Frontend unit and browser
  checks cover the single-prompt flow and plan decision.

## Scope and migration

This changes agent admission, plan persistence/response, task workspace
creation, runtime parent validation, integration, recovery, frontend activation
controls and docs. Existing completed and interrupted tasks remain readable.
Tasks already active under the old clean-checkout flow retain their original
parent semantics until completion; the new task-parent mode is recorded per
task and cannot be inferred from current Git status.
