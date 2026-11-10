# Agent development

Ask UMCode to use agents in the same message as the work: “Use one agent to implement the parser and another to review it.” In any selected project, UMCode starts a valid plan automatically and shows the **Agent plan** as an activity card. The card lists each agent's purpose, model, file and shell access, network access, limits, and capabilities removed by project policy.

You can cancel a running task. If UMCode restarts before its continuation begins, the card offers **Continue** for the same private task.

## Project snapshots

A project does not need Git. UMCode copies its files into an app-owned private Git workspace without changing the selected folder. For Git projects, staged, unstaged, deleted, and non-ignored untracked files are included; Git-ignored files are excluded, and the original index is preserved. Ordinary folders include their regular files and safe internal symlinks. A source change during capture causes a retry instead of admitting a mixed snapshot.

Agents work in isolated checkouts of the private task workspace. The lead reviews revisions, chooses which to integrate, and runs verification there. On completion UMCode checks that the relevant files in your checkout still match the snapshot, then merges the accepted changes as worktree edits. Existing staged state stays staged. If you changed a target path while agents ran, the merge stops and preserves both your checkout and the verified private result for review and retry.

## Task controls and history

The Agents panel shows active agents, assignments, usage, revisions, verification, and past runs. The lead can send messages to agents and integrate their revisions according to the approved plan. You can cancel an active task or agent. A cancelled or interrupted task remains in the history; completed tasks show the integrated result. The task's limits and budget apply to the whole agent run.

Plans are scoped to a chat and project. File edits inside the project and project-confined shell work run without an approval prompt. Desktop and external actions retain their own policy. A read-only or plan-mode turn cannot propose an executable agent plan.
