# Agent development

Ask UMCode to use agents in the same message as the work: “Use one agent to implement the parser and another to review it.” In a Git project, the lead can show an **Agent plan** card. The card lists each agent's purpose, model, file and shell access, network access, limits, and any capabilities the project policy removed. The lead cannot start an agent before you approve the plan.

Review the card, adjust the models and limits, drop an agent, or provide feedback and choose **Revise**. **Decline** ends the proposal without starting agents. **Approve** authorizes the displayed plan and continues the original request automatically. If UMCode restarts after approval and the continuation has not started, the approved card offers **Continue**. It uses the approved task; no second activation step is needed.

## Edited Git checkouts

A project needs a committed Git HEAD. It may contain staged, unstaged, deleted, and non-ignored untracked files. At approval, UMCode captures the visible checkout in a private task workspace, including those edits, so the lead and agents see the same starting point. Ignored files are excluded. The original checkout and index are not changed by this capture. If files change between proposal and approval, the plan must be proposed again.

Agents work in isolated checkouts of the private task workspace. The lead reviews revisions, chooses which to integrate, and runs verification there. On completion UMCode checks that the relevant files in your checkout still match the snapshot, then merges the accepted changes as worktree edits. Existing staged state stays staged. If you changed a target path while agents ran, the merge stops and preserves both your checkout and the verified private result for review and retry.

## Task controls and history

The Agents panel shows active agents, assignments, usage, revisions, verification, and past runs. The lead can send messages to agents and integrate their revisions according to the approved plan. You can cancel an active task or agent. A cancelled or interrupted task remains in the history; completed tasks show the integrated result. The task's limits and budget apply to the whole agent run.

Plans are scoped to a chat and project. Approval does not grant capabilities beyond what the card shows. File and shell access remain subject to the project's policy and sandbox. A read-only or plan-mode turn cannot propose an executable agent plan.
