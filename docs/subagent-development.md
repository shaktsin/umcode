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
