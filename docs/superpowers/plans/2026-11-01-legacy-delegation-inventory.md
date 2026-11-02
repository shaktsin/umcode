# Legacy delegation inventory (Plan 5, Task 6)

The work list for Task 7 (backend deletion) and Task 8 (frontend deletion and
docs). Binding authority: spec `docs/superpowers/specs/2026-10-24-agent-runtime-design.md`
section 6 phase 5 (what is deleted, what is reused) and section 7. Inventory
taken on branch `feat/agent-runtime-ui-cutover` at `b4200f7`.

Decisions:

- **delete**: remove the symbol (and its file when every symbol in it goes).
- **keep**: stays as is (reused by the runtime, or shared engine code).
- **keep (history)**: stays only to read legacy rows; never writes them.
- **change**: stays, but its legacy branch, field or wording goes.
- **move**: the symbol is on the runtime path but lives in a legacy file; move
  it to a kept file before the file is deleted.

"Runtime path" = reachable from `cmd/*` `main` without going through a
legacy entry point (legacy tools, `workflowRunner`, `childRunner`, the
coordinator's child scheduling), verified as described in section 9.

## 1. Must-read findings (Task 7 breaks the runtime if it misses these)

1. **The coordinator is half runtime.** `delegation.Coordinator` is not only the
   legacy child scheduler. Its task registry is the runtime's task budget and
   cancellation owner: `Coordinator.Task` (lead turns `turn.go runTurn`,
   `compaction.go CompactThread`), `Coordinator.CancelTask` (`turn.go startTurn`
   and `InterruptTurn`, `delegation.go CancelDelegation`), `Coordinator.LiveBudget`
   (`delegation_read.go ListDelegation`), `Coordinator.NextSequence`
   (`agentrt_readmodel.go delegationSequence`, every runtime `delegation/updated`
   event), and the package function `active` (used by `runtime_integration.go`
   `loadTask`, `reconcileRevision`, `recordRuntimeRevision`,
   `runtimeTaskAuthority`). Keep these as a task registry (renaming is optional);
   delete only the child scheduling (`Spawn`, `Wait`, `Followup`, `Status`,
   `List`, `Snapshot`, `Cancel`, slots, policy, runner).
2. **Three lifecycle.go functions are runtime.** `InheritRunnerContext`
   (`turn.go startTurn`), `RegisterChildCancellation` (via
   `delegation_policy.go registerExecutionCancellation`, every agent turn) and
   `WithChildCancellation` (`agentrt_runner.go runtimeRunner.Run`) plus their
   types `childCancellation`, `cancellationKey`. Move them before deleting
   `lifecycle.go`; `InheritRunnerContext` then copies only `cancellationKey`
   (the `queueKey` follow-up queue goes).
3. **`tools.AgentAuthorize`, `tools.WithAgentCallID` and `agentCallKey`** live in
   the legacy `internal/tools/delegation.go` but are used by the runtime tools
   (`tools/agentrt.go NewRuntimeAgentTools`, `NewRuntimeIntegrateTool`,
   `runtimeAgentTool.Call`) and by `turn.go runTool`. Move them to
   `tools/agentrt.go`. After that `internal/tools` no longer imports
   `internal/delegation`.
4. **`managedChildWorkspaces` is runtime.** Its runtime methods
   (`ResolveRuntime`, `FinalizeRuntime`, `RecordRuntimeRevision`,
   `DiscardRuntimeView`) are in `agentrt_runner.go` and it is the default
   `runtimeWorkspaces()` resolver. Only its legacy methods `Resolve`/`Finalize`
   (in `delegation_workspace.go`) go.
5. **`delegation_workflow.go` (engine) holds shared verification code**:
   `planVerificationChecks` (runtime `planRuntimeHead`/`planRuntimeAmend`),
   `verificationTarget`, `beginDelegationVerification`/`finishDelegationVerification`
   (runtime branch: every `verification.run`/`browser.verify` of a runtime lead)
   and `recordVerificationResults`. Move these to `agentrt_verification.go`
   (dropping the `legacy` field and branch) and delete the rest of the file.
6. **Legacy workflow tools are registered even with the runtime on.**
   `engine.New` registers `tools.NewAgentWorkflowTools` (`agent.integrate`,
   `agent.complete`, `agent.review_report`) unconditionally; the runtime only
   overrides `agent.integrate`. A runtime lead is still offered the legacy
   `agent.complete`. Deleting `NewAgentWorkflowTools` removes it (runtime
   completion is `agent.integrate {action:"complete"}`).
7. **The runtime lead gets the legacy prompt.** `engine.go systemPromptLayers`
   adds `delegationWorkflowPrompt` ("use an implementer or an independent
   reviewer") to every admitted (delegated) lead turn, runtime included.
   Replace it with runtime wording (propose a plan, spawn plan entries,
   integrate, verify, complete) or drop the layer; update
   `model_driven_review_test.go TestFocusedReviewIsOptionalAndBudgeted`.
8. **`implementer`/`reviewer` are also storage values, not roles.** They are
   `CHECK` values of `agent_children.role` (migration 0022) and
   `agent_workspaces.kind` (0027/0028). The runtime writes them: its execution
   mirror row (`store/agentrt_execution.go EnsureRuntimeAgentExecution`,
   `role 'implementer'`) and its workspace kinds (`delegation/workspace.go
   prepareRuntimeLocked`, `runtime_integration.go runtimeWorkspace`,
   `CleanupRuntime`, `workspace.go DiscardRuntimeView`). **Keep** these string
   literals; they grant nothing and changing them needs a migration (out of
   scope: tables are not dropped or altered).
9. **Runtime tests depend on fixtures in legacy test files.** Move before
   deleting: `delegation_execution_test.go childExecutionTask` (used by 8 kept
   files), `childEvent`, `childSubscriber`; `delegation/workspace_test.go
   workspaceFixture` (stop creating a legacy child there; runtime tests already
   ignore it), `workspaceGit`, `workspaceWrite`; `store/delegation_test.go
   agentTaskFixture`. `delegation_activation_test.go` stays (admission tests), so
   `delegationFixture`, `delegationFixtureWith`, `delegationGit`,
   `delegationProvider`, `delegationTurn`, `liveAdmittedTurn` stay where they are.
10. **Removing the flag turns the runtime on for every test fixture.** With the
    runtime on, an admitted turn that ends without a plan closes its task
    (`agentrt_activation.go`), so tests that reuse a task after a plain
    `delegationTurn` behave differently. Section 7 lists the tests that fail
    when the default flips (measured), so Task 7 knows them up front.
11. **`tools.IsAgentControlSource`** (`tools/task_evidence.go`) matches only the
    legacy `*agentTool` and `*agentWorkflowTool` types; runtime `agent.*` tool
    calls are not marked `engine_control` (used by `task_outcomes.go` to count a
    succeeded control call as verified). **Change** it to the runtime tool
    types (`*runtimeAgentTool`, `*runtimeIntegrateTool`, `*agentSuggestTool`,
    `*agentProposePlanTool`), otherwise it stops compiling when the legacy types
    go.
12. **`childExecution.child` is the runtime agent's identity.** The runtime
    sets `child = protocol.AgentChild{ID: agent ID, ...}`
    (`agentrt_runner.go runtimeRunner.Run`) and `approvals.go requestApproval`
    puts `x.child.ID` into `Approval.AgentChildID`, which the app uses to show
    agent approvals. Keep the field (or replace it by an agent ID field); drop
    only its `Role` reads.

## 2. Backend: `internal/delegation`

| File | Symbol | Decision | Reason / runtime path | Tests that go with it |
| --- | --- | --- | --- | --- |
| `lifecycle.go` | `Service`, `WorkflowPolicy`, `WithWorkflowPolicy`, `Runner`, `WithRunner`, `BusyError`, `lifecycleStore`, `childRun`, `childOutcome`, `FinalizationError`, `FollowupQueue`, `queueKey`, `followupQueue`, `FollowupsFromContext`, `Coordinator.lifecycleStore`, `notify`, `notifyError`, `cloneChild`, `liveTask`, `busy`, `Spawn`, `launch`, `run`, `finalize`, `statusLocked`, `Status`, `Snapshot`, `List`, `Wait`, `Followup`, `Cancel`, `hasResult`, `var _ Service` | delete | legacy child scheduling, one active child, `agent.followup` queue | `lifecycle_test.go` (all 11 tests) |
| `lifecycle.go` | `EventSink`, `WithEventSink`, `CoordinatorOption` | change | needed only if the kept registry still publishes; `CancelTask`'s child `notify` goes, so these can go too | — |
| `lifecycle.go` | `Coordinator.NextSequence`, `Coordinator.LiveBudget` | move | runtime path (finding 1) | covered by runtime read-model tests |
| `lifecycle.go` | `InheritRunnerContext`, `RegisterChildCancellation`, `WithChildCancellation`, `childCancellation`, `cancellationKey` | move | runtime path (finding 2); drop `queueKey` from `InheritRunnerContext` | `agentrt_runner_test.go TestRunnerCancellationEndsPromptly`, ported cascade test |
| `coordinator.go` | `TaskStore`, `taskRuntime`, `Coordinator` (fields `tasks`, `cancelled`, `sequences`/`seqMu`/`sequence`), `NewCoordinator`, `active`, `Coordinator.Task` | keep (change) | runtime task registry (finding 1); drop fields `policy`, `runner`, `slots`, `changed`, `lifecycleMu` and the child methods from `TaskStore` (`AgentChildren`, `AgentChild`, `UpdateAgentChild`) | `coordinator_test.go` (see 6.1) |
| `coordinator.go` | `Coordinator.CancelTask` | change | keep the live cancel and the task-state CAS; delete the slot handling and the legacy `agent_children` cancel loop (after migration 0038 no legacy child is active) | `coordinator_test.go` |
| `budget.go` | everything (`Budget`, `NewBudget`, `WithStartedAt`, `Slice`, `Reserve`, `WithBudget`, `BudgetFromContext`, ...) | keep | budget is reused (spec 6.5); runtime slices it | `budget_test.go` keep |
| `context.go` | `ScopedContextBuilder`, `AssignmentLimit`, `SummaryLimit`, `ReferencesLimit`, `sensitiveReference` | keep | runtime runner builds agent context with it; `AssignmentLimit` used by `consumeChildFollowups` (legacy branch goes) | `context_test.go` keep (uses `AgentChild` only as identity) |
| `context.go` | `ContextBuilder` interface | delete | already unreferenced | — |
| `snapshot.go` | `ReadRuntimeSnapshotFile`, `readSnapshot`, `snapshotPath`, `errSnapshotRead` | keep | runtime `delegation/file` | `workspace_test.go TestWorkspaceReadSnapshotScope` (move/adapt) |
| `snapshot.go` | `ReadSnapshotFile` | keep (history) | legacy child file read for the read-only history view (`ReadDelegationFile` legacy branch); delete only if Task 8's history view opens no files | same |
| `workspace.go` | `WorkspaceRef`, `WorkspaceSnapshot`, `ParentValidator`, `WorkspaceService` (minus `eligible`), `AgentEvent`, `WithEvents`, `integrationChanged`, `takeIntegrationChanged`, `emitIntegration`, `NewWorkspaceService` (change: drop the `eligible` parameter), `ref`, `task`, `ownedCheckout`, `snapshotOwned`, `nulPaths`, `parentAuthority`, `discardVerified`, `privateGit`, `validatePrivate`, `IntegrationApplyingText`, `IntegrationApplyingAdvice`, `runtimeBaseSource`/`runtimeHeadSource` consts, `PrepareRuntime`, `PrepareRuntimeView`, `prepareRuntimeLocked`, `attachRuntime`, `DiscardRuntimeView`, `SnapshotRuntime` | keep | workspaces are reused (spec 6.5); all on the runtime path | `runtime_views_test.go`, `runtime_integration_test.go`, `runtime_amend_test.go` keep |
| `workspace.go` | `IntegrationEvidence`, `IntegrationEligibility`, field `eligible`, `attach`, `Prepare`, `prepare`, `Snapshot`, `submitted`, `Review` (already dead), `BindReviewer`, `prepareReviewer`, `reviewCopy`, `Integrate`, `integrationAuthority`, `verifyApplied`, `RecordVerification`, `Cleanup`, `ValidateIntegrated`, `prepareStandaloneReviewer`, `PrepareStandaloneReview` | delete | legacy implementer/reviewer workspaces, legacy integrate/complete; dead once `delegation_workflow.go`, `childRunner` and `workflowRunner` go | `workspace_test.go`, `workspace_review_test.go` (see 6.1) |
| `runtime_integration.go` | everything | keep | runtime integration; uses `active` from `coordinator.go` (keep it) | keep |

## 3. Backend: `internal/engine`

Every file below is in package `engine`, which the runtime path is part of;
the per-symbol column is what matters.

| File | Symbol | Decision | Reason / runtime path | Tests |
| --- | --- | --- | --- | --- |
| `engine.go` | `Engine.DelegationCoordinator` | keep (change) | registry only (finding 1); `NewCoordinator` without `WithRunner`/`WithWorkflowPolicy` | — |
| `engine.go` | `New`: `tools.NewAgentWorkflowTools(...)` loop, `if o.Config.Agents.RuntimeV2 {...} else { tools.NewAgentTools(...) }` | change | always call `e.registerAgentRuntime(reg, base)`; delete both legacy registrations (finding 6) | `agentrt_wiring_test.go TestRuntimeV2Flag*` |
| `engine.go` | `Engine.DelegationWorkspaces`, `Options.DelegationWorkspaces`, the `managedChildWorkspaces` default assignment | delete | legacy resolver only (`childRunner`, `delegationWorkflowPolicy`, `workflowRunner`); runtime uses `RuntimeWorkspaces`/`runtimeWorkspaces()` | tests that set it: `delegation_execution_test.go`, `delegation_compaction_test.go`, `delegation_activation_test.go TestDelegationWorkspaceDefaultRunner` |
| `engine.go` | `Engine.AgentRuntime` comment "set only when agents.runtime_v2 is on" | change | always set | — |
| `engine.go` | `ReconcileAgentChildren` call in `New` | keep | store only, harmless; with 0038 it finds nothing | `store/delegation_test.go TestReconcileAgentChildrenPreservesArtifacts` |
| `engine.go` | comments "whatever agents.runtime_v2 says" around `ReconcileRuntimeAgents`, `reconcileRuntimeIntegrations`, `expireAgentSuggestions` | change | wording | — |
| `engine.go` | `systemPromptLayers`: the legacy child branch (`x.child.Role == protocol.AgentRoleReviewer`, "scoped UMCode child agent" text) | delete | every child execution is a runtime agent (`x.runtime != nil`) | `agentrt_runner_test.go TestChildPromptTextIsPathSpecific` (change: drop legacy half) |
| `engine.go` | `systemPromptLayers`: `delegation_workflow` layer with `delegationWorkflowPrompt` | change | finding 7 | `model_driven_review_test.go TestFocusedReviewIsOptionalAndBudgeted` (change) |
| `engine.go` | `delegationWorkflowMu` field | keep | serialises runtime `agent.integrate` (`runtimeIntegrateCall`) and runtime verification | — |
| `delegation.go` | `delegationSkillID`, `delegationToken`, `delegationAdmission`, context keys, `WithDelegationChild`, `delegationChild`, `withDelegationTask`, `DelegationTaskFromContext`, `delegationBase`, `delegationOpen`, `PrepareDelegation`, `prepareDelegationToken`, `submittedDelegationSkill`, `prepareDelegationAdmission`, `unfinishedDelegation`, `EnableDelegation`, `authorizeDelegationTool` | keep | admission token logic (spec 6.5 keep) | `delegation_activation_test.go` admission tests, `delegation_identity_test.go` |
| `delegation.go` | `isDelegationTool` | change | drop the `agent.review_report` exception | — |
| `delegation.go` | `CancelDelegation` | change | delete the `Store.AgentChild` + `DelegationCoordinator.Cancel` branch and `DelegationCoordinator.List`; a `ChildID` names a runtime agent only; `result.Children` stays `[]` (or the read-only legacy rows) | `agentrt_wiring_test.go TestCancelDelegation*`, `agentrt_delegation_rpc_test.go`, ported cascade test |
| `delegation_execution.go` | `WorkspaceResolver`, `ChildWorkspaceFinalizer`, `childRunner`, `childRunner.Run`, `updateChildExecution` | delete | legacy child execution | `delegation_execution_test.go` |
| `delegation_execution.go` | `ChildWorkspace`, `childExecution` (field `child` stays, finding 12), `childExecutionKey`, `withChildExecution`, `childExecutionFrom`, `childProjectBase`, `childGuidance`, `childTurnOutcome`, `driveChildTurn`, `messageText` | keep | runtime runner uses them | runtime runner tests |
| `delegation_execution.go` | `setChildApprovalState`, `recordChildModel` | change | keep only the `x.runtime != nil` branch | ported approval test |
| `delegation_execution.go` | `consumeChildFollowups` | change | keep only the runtime branch (`consumeAgentMessages`); delete `delegation.FollowupsFromContext` and the follow-up item logic | `agentrt_runner_test.go TestRunnerDeliversPendingMessages` |
| `delegation_policy.go` | `childToolAllowed` | change | delete the role branch (`x.child.Role == "reviewer"` allowlist); `x.runtime` is always set: `runtimeToolAllowed` | `agentrt_policy_test.go` |
| `delegation_policy.go` | `narrowChildToolAuthority` | keep | spec 6.5 keep; simplify the last branch (always `runtimeAgentScope`) | `agentrt_policy_test.go`, `delegation_execution_test.go TestChildExecutionLivePolicyNarrowing` (env failure; replace by a runtime-agent narrowing test if wanted) |
| `delegation_policy.go` | `childToolRefusal` | change | runtime message only | — |
| `delegation_policy.go` | `childPolicyError` | delete | legacy reviewer message | — |
| `delegation_policy.go` | `publishTurnEvent`, `registerExecutionCancellation` | keep | runtime path | — |
| `delegation_read.go` | `ownedDelegation`, `ReadDelegationFile` | keep | `ReadDelegationFile` keeps its legacy branch as a history read (see `snapshot.go`) | `agentrt_delegation_rpc_test.go` |
| `delegation_read.go` | `ListDelegation` | change | replace `DelegationCoordinator.Snapshot` by `Store.AgentChildren` (history, no live `ChildErrors`/`ChildSequences`); keep `LiveBudget`, `ReadAgentTaskBudget`, `AgentWorkflow` (history checks) and `fillRuntimeSnapshot` | `agentrt_readmodel_test.go` |
| `delegation_workspace.go` | `activeDelegationParent`, `validateDelegationParent`, `managedChildWorkspaces` type, `privateParentRevision` | keep | admission and runtime | — |
| `delegation_workspace.go` | `managedChildWorkspaces.Resolve`, `.Finalize`, `var _ WorkspaceResolver`, `var _ ChildWorkspaceFinalizer` | delete | legacy (finding 4) | — |
| `delegation_workflow.go` | `delegationWorkflowPrompt` | change | finding 7 (replace or delete with the layer) | as above |
| `delegation_workflow.go` | `delegationWorkflowPolicy`, `errNoImplementer`, `workflowImplementer`, `workflowRunner`, `workflowRunner.Run`, `decodeWorkflow`, `delegationWorkflowCall`, `submitReview` | delete | legacy implementer/reviewer workflow, `agent.complete`, `agent.review_report` | `delegation_workflow_test.go`, `delegation_workflow_guards_test.go` |
| `delegation_workflow.go` | `planVerificationChecks`, `verificationTarget` (drop `legacy`), `beginDelegationVerification`, `finishDelegationVerification` (runtime branch only), `recordVerificationResults` | move | runtime path (finding 5) to `agentrt_verification.go` | `agentrt_verification_test.go` keep |
| `task_child_authority.go` | `checkChildTaskAuthority`: `(x.runtime == nil && x.child.Role == protocol.AgentRoleReviewer)` | change | delete the role term; keep `runtimeReadOnlySafe` (added in `b4200f7`) | `agentrt_readonly_authority_test.go`, `task_review_regression_test.go`, `task_outcomes_test.go` (see 6.2) |
| `turn.go` | `startTurn` line `ReadOnly: ... childExecutionFrom(ctx).runtime == nil && ...child.Role == protocol.AgentRoleReviewer` (task snapshot scope) | change | delete the role term | — |
| `turn.go` | `delegation.InheritRunnerContext`, `registerExecutionCancellation`, `DelegationCoordinator.Task`/`CancelTask`, `consumeChildFollowups` calls | keep | runtime path (findings 1, 2) | — |
| `tool_selection_runtime.go` | `permittedToolFor`: `agent.review_report` block | delete | tool gone | — |
| `workflow.go` | `workflowDiscoveryTool`: `"agent.review_report"` case | delete | tool gone | — |
| `task_authority.go` | comment mentioning `agent.spawn` | keep | runtime tool | — |
| `approvals.go` | `x.child.ID` into `AgentChildID` | keep | runtime identity (finding 12) | ported approval test |
| `compaction.go` | `DelegationCoordinator.Task` | keep | registry | `delegation_manual_compaction_test.go` |
| `agentrt_readmodel.go` | `delegationSequence` (`DelegationCoordinator.NextSequence`) | keep | registry | — |
| `agentrt_activation.go` | `suggestAgents` refusal "needs the agent runtime (agents.runtime_v2)" when `AgentRuntime == nil`; comment on `expireAgentSuggestions` | change | flag gone: drop or reword the guard | `agentrt_activation_test.go TestSuggestRequiresRuntimeV2` (delete) |
| `agentrt_plan.go` | `proposeAgentPlan` and `decideAgentPlan` refusals mentioning `agents.runtime_v2` | change | same | `agentrt_activation_e2e_test.go TestE2EFlagOffUnchanged` (delete) |
| `agentrt_wiring.go` | `registerAgentRuntime` doc ("in place of the legacy delegation agent tools"), `runtimeCaller` ("Legacy child contexts ... fail closed"), `cancelRuntimeAgents` ("no-op when the runtime is off"), `reconcileRuntimeIntegrations` comment | change | wording; `runtimeCaller`'s `childExecutionFrom(ctx) != nil` fail-closed check stays | — |
| `agentrt_integrate.go` | `registerRuntimeIntegrate` doc ("replaces the legacy agent.integrate in runtime_v2 mode"), `runtimeIntegrateCall` doc ("serialised with the legacy workflow actions") | change | wording | — |
| `agentrt_*.go` (rest), `agentrt_shell.go`, `agentrt_budget.go`, `agentrt_policy.go`, `agentrt_runner.go`, `agentrt_verification.go` | all | keep | the runtime | keep |

## 4. Backend: `internal/tools`, `internal/agentrt`, `internal/store`, `internal/protocol`, `internal/config`

| File | Symbol | Decision | Reason | Tests |
| --- | --- | --- | --- | --- |
| `tools/delegation.go` | `agentTool` (+ all methods), `NewAgentTools` | delete | legacy `agent.spawn/status/wait/followup/cancel` with the `role` enum | `tools/delegation_test.go TestAgentToolsLifecycle`, `tools/delegation_schema_test.go TestAgentToolsClaudeSchemas` |
| `tools/delegation.go` | `AgentAuthorize`, `WithAgentCallID`, `agentCallKey` | move | runtime (finding 3) to `tools/agentrt.go` | — |
| `tools/delegation_workflow.go` | `AgentWorkflowCall`, `agentWorkflowTool` (+ methods), `NewAgentWorkflowTools` | delete | legacy `agent.integrate/complete/review_report` (finding 6) | — |
| `tools/task_evidence.go` | `IsAgentControlSource` | change | finding 11 | `task_outcomes_test.go` |
| `tools/agentrt.go` | everything | keep | runtime tools | `tools/agentrt_test.go` keep (`TestSpawnToolRejectsUnknownFields` keeps its `"role"` case: a role argument is refused) |
| `tools/delegation_policy_test.go` | `TestDelegationShellBoundary`, `TestDelegationShellWrapperFailure`, `TestDelegationReviewerDirectMutationDenied` | keep | generic `Scope.ReadOnly`/`RestrictedChild` checks the runtime uses; rename "reviewer" wording if wanted | — |
| `agentrt/capabilities.go` | `AllowedTools` (`agent.spawn`, ...) | keep | runtime | keep |
| `store/delegation.go` | `validAgentState`, `scanAgentTask`, `CreateAgentTask`, `AgentTask`, `AgentTaskForRootTurn`, `ActiveAgentTask`, `UpdateAgentTaskState`, `validAgentTransition` (also used by `store/agentrt.go UpdateRuntimeAgent`), `AgentTasksForThread` | keep | tasks are shared by the runtime | keep |
| `store/delegation.go` | `scanAgentChild`, `AgentChild`, `AgentChildren` | keep (history) | read-only history (`ListDelegation`, `ReadSnapshotFile`); `scanAgentChild` changes if `AgentChild.Role` is dropped | — |
| `store/delegation.go` | `ReconcileAgentChildren` | keep | startup safety net, store only | `TestReconcileAgentChildrenPreservesArtifacts` (change: seed rows by SQL) |
| `store/delegation.go` | `CreateAgentChild`, `UpdateAgentChild` | delete | legacy writers (lifecycle, `updateChildExecution`, `CancelTask` child loop, `workspace.attach`). Tests that need legacy rows seed them with SQL (suggest a `teststore` helper `SeedLegacyAgentChild`) | `TestAgentChildPersistenceOwnershipAndIdempotency`, `TestAgentChildTransitionValidation` (delete); `TestAgentDelegationMigrationAndOwnership`, `store/agentrt_execution_test.go TestMirrorRowIsInert`, `TestRuntimeExecutionThreadHasEmptyApprovalMode`, `store/delegation_workspaces_test.go TestAgentInspectionMigrationAndOwnedUniqueness` (change: seed by SQL) |
| `store/delegation_followups.go` | `AgentChildForCall`, `AgentFollowup`, `EnqueueAgentFollowup`, `PendingAgentFollowups`, `AcknowledgeAgentFollowup`, `agentFollowupError` (whole file) | delete | `agent.followup` only (runtime messaging uses the runtime mailbox in `store/agentrt*.go`); table `agent_followups` stays | — |
| `store/delegation_execution.go` | `WithInternalThreads`, `internalThreads`, `AgentExecution` type, `AgentModelScope` type, `CreateAgentModelScope`, `AgentModelScope`, `AgentExecution`, `UpdateAgentExecutionModel`, `IsInternalThread`, `insertAgentExecution` | keep | runtime execution identity | keep |
| `store/delegation_execution.go` | `EnsureAgentExecution` | delete | `childRunner` only | — |
| `store/delegation_workflow.go` | `AgentReviewReport` alias, `SaveAgentReviewReport`, `Store.AgentReviewReport`, `AgentReviewEligible` | delete | legacy review report and reviewer eligibility | — |
| `store/delegation_workflow.go` | `AgentWorkflowCheck`, `AgentWorkflow` aliases, `Store.AgentWorkflow` | keep (history) | `ListDelegation` shows a legacy task's lead checks; aliases used by runtime verification | — |
| `store/delegation_workflow.go` | `SaveAgentWorkflow` | delete | legacy writers only | — |
| `store/delegation_workspaces.go` | `AgentWorkspace` type, `Store.AgentWorkspace`, `SaveAgentWorkspace`, `normalizeNotFound` | keep | runtime workspaces | — |
| `store/delegation_workspaces.go` | `AgentIntegration` type, `AgentWorkspaceForChild`, `BindAgentReviewer`, `AgentReviewerSource`, `Store.AgentIntegration`, `SaveAgentIntegration`, `AgentWorkspaces`, `SupersedeAgentIntegration` | delete | legacy implementer/reviewer workspaces and integration evidence (tables stay) | `store/delegation_workspaces_test.go` (change) |
| `store/delegation_budget.go` | all | keep | budget | keep |
| `store/agentrt*.go` | all, incl. the `'implementer'` mirror row | keep | runtime (finding 8) | keep |
| `store/records.go` | approvals `agent_child_id` join over `agent_children`/`agent_executions` | keep | runtime approvals reuse the mirror row | — |
| `store/migrations/0022`–`0037` | all | keep, never edit | constraint | — |
| `store/migrations/0038_interrupt_legacy_delegation.sql` | new | create (Task 7) | mark `agent_tasks`/`agent_children` rows in `queued`, `running`, `waiting_approval` as `interrupted`, data and FKs kept. Note for Task 7: a blanket task update also ends an unfinished runtime task of a user who ran with the flag on (runtime tasks have rows in `agents`; the runtime mirror rows in `agent_children` have `call_id` `rt:<agent>` and are kept in step by `UpdateRuntimeAgent`, so leave them to `ReconcileRuntimeAgents`). Restricting the task update to tasks without `agents` rows keeps such a task open for its lead; either choice is within the plan | `migration_0038_test.go` |
| `protocol/delegation.go` | `AgentRole`, `AgentRoleImplementer`, `AgentRoleReviewer` | delete | role strings (spec 6.5) | — |
| `protocol/delegation.go` | `AgentChild` | change | keep for history; drop the `Role` field (or keep it as a plain `string` only if the history view needs it; the plan's Task 8 says no) | `protocol/delegation_test.go` |
| `protocol/delegation.go` | `AgentAssignment`, `AgentResult`, `AgentReviewReport` (history), `AgentFinding`, `AgentCheckEvidence`, `AgentTask`, `AgentState`, `AgentActivation` | keep | runtime uses `AgentAssignment`/`AgentResult` | — |
| `protocol/delegation.go` | `AgentFollowup` | delete | `agent.followup` only | — |
| `protocol/delegation.go` | `DelegationUpdated.Child`, `DelegationUpdated.Error` | delete | published only by the legacy coordinator and `updateChildExecution` | — |
| `protocol/delegation.go` | `DelegationUpdated.Workflow`, `AgentWorkflow`, `AgentWorkflowCheck` | keep | runtime publishes its head's checks as `Workflow` (`agentrt_integrate.go`, `agentrt_verification.go`) | — |
| `protocol/delegation.go` | `AgentSnapshot.Children` | keep (history) | legacy rows, read-only | — |
| `protocol/delegation.go` | `AgentSnapshot.ChildErrors`, `ChildSequences` | delete | live coordinator state only | — |
| `protocol/delegation.go` | `DelegationCancelParams.ChildID`, `DelegationFileParams.ChildID` | keep | the wire name now carries a runtime agent ID (the app sends it) | — |
| `protocol/delegation.go` | `DelegationCancelResult.Children` | change | always empty or legacy rows; keep the field for wire compatibility | — |
| `protocol/types.go` | `Approval.AgentChildID` | keep | runtime agent ID (finding 12) | — |
| `config/config.go` | `AgentsConfig.RuntimeV2` (+ comment) | delete | flag removed; config loading is non-strict (`yaml.Unmarshal`), so an old `runtime_v2:` key is ignored | `server/agent_suggestion_test.go`, `server/agent_plan_test.go` (drop the config hook) |
| `config.example.yaml` | `runtime_v2: false` and its comment block (lines 155–160) | delete | flag removed | — |

## 5. Frontend: `app/frontend/src`

| File | Symbol | Decision | Reason | Tests |
| --- | --- | --- | --- | --- |
| `lib/components/Agents.svelte` | flat child cards (`{#each snapshot.children as child}` block: role, model, correction rounds, findings, checks, changed paths, per-child approvals, "Retry saving outcome") | delete | spec 6.5; replace with a read-only "Earlier agent run" list for legacy tasks (Task 8) | `Agents.test.ts` first test (delete/rewrite) |
| `lib/components/Agents.svelte` | `count` (`task.children.length` fallback), "Saving needs attention" (`childErrors`) | change | runtime agents only; history rows counted separately or not at all | — |
| `lib/components/Agents.svelte` | `snapshot.workflow` "Lead verification", tree, `IntegrationStrip`, Cancel task | keep | runtime and legacy history | `AgentRuntime.test.ts` |
| `lib/components/Agents.test.ts` | "renders collapsed compact agent evidence..." | delete (rewrite as the history label test) | flat cards | — |
| `lib/components/Agents.test.ts` | `DelegationActivation` test | keep | activation | — |
| `lib/components/AgentRuntime.test.ts` | the legacy child fixture in the mixed-tasks test (`role: 'reviewer'`, `children: [legacy]`) | change | history label instead of a flat card | — |
| `lib/delegation.ts` | `AgentChild` | change | history type; drop `role` and `correctionRounds` display needs | `delegation.test.ts` |
| `lib/delegation.ts` | `agentStateLabel(child: AgentChild)` | change | take `{ state, result }` (`AgentRow.svelte`, `AgentInspector.svelte` cast runtime agents to `AgentChild` today) | — |
| `lib/delegation.ts` | `AgentSnapshot.childErrors`, `childSequences`; `DelegationUpdated.child`, `.error` | delete | backend no longer sends them | — |
| `lib/delegation.ts` | `AgentState`, `AgentFinding`, `AgentCheck`, `AgentResult` (incl. `review`), `AgentTask`, `AgentWorkflow`, `AgentSnapshot` (`children` as history), `DelegationUpdated` (rest), `agentActive` | keep | runtime and history | — |
| `lib/stores/delegation.svelte.ts` | `Snapshot` `childErrors`/`childSequences`, the child half of `merge`, the child loop in `hydrate`, `event.child` hydrate trigger in `apply` | delete | live legacy children | `delegation.test.ts` child-merge tests (rewrite: history rows are read-only, no merge by version) |
| `lib/stores/delegation.svelte.ts` | `cancel(taskId, childId?)` | keep | `childId` carries a runtime agent ID | — |
| `lib/components/AgentRow.svelte`, `AgentInspector.svelte` | `type AgentChild` import and cast | change | follow `agentStateLabel` | — |
| `lib/types.ts` | `import type { AgentChild, ... }`, `type AgentAssignment = AgentChild['assignment']` | change | define `AgentAssignment` directly | — |
| `lib/types.ts` | `Approval.agentTaskId`, `agentChildId` | keep | runtime agent approvals | `approvalDock.test.ts`, `app.approvals.test.ts` |
| `lib/stores/inspector.svelte.ts` | `openAgentFile(..., childId, ...)` | keep | runtime agent ID on the wire | `inspector.test.ts` |
| `lib/stores/chat.svelte.ts` | `DelegationUpdated` import | keep | — | — |
| `lib/plugins.test.ts` | `name: 'reviewer'` | keep | a Claude plugin agent name, not a role | — |

## 6. Tests

### 6.1 Delete with the code they cover

| File | Tests | Replaced by |
| --- | --- | --- |
| `internal/delegation/lifecycle_test.go` (whole file) | all 11 `TestCoordinator*` | `TestPortedCancellationCascadeReturnsBudget`, `TestPortedApprovalInsideAgentSurfacesOnThread`, `agentrt/runtime_test.go` (spawn, queue, wait, cancel, replay) |
| `internal/delegation/coordinator_test.go` | `TestCoordinatorCancellationCASConflicts` (child CAS part) | registry tests below |
| `internal/delegation/coordinator_test.go` | keep and adapt: `TestCoordinatorSharedPersistenceAndCancellation`, `TestCoordinatorCancellationFailureStillStopsContexts`, `TestCoordinatorShutdownOwnsTaskContext`, `TestCoordinatorRestoredDurationAndDetachedAdmission`, `TestCoordinatorReadFailureStillStopsContexts` | they test the kept registry (`Task`, `CancelTask`); drop the child parts of `conflictTaskStore` |
| `internal/delegation/workspace_test.go` | `TestDelegationWorkspaceSafety`, `TestDelegationWorkspaceIntegration`, `TestDelegationWorkspaceIntegrationRechecks`, `TestDelegationWorkspaceConflictIsNotPartial`, `TestDelegationWorkspaceConcurrentIntegration`, `TestDelegationWorkspaceCancelledTaskCannotIntegrate`, `TestDelegationWorkspaceCleanupCrashRecovery`, `TestDelegationWorkspaceCleanupPreservesNewEdits`, `TestDelegationWorkspaceReviewerProvenanceAndRecovery`, `TestStandaloneReviewerUsesIsolatedCurrentSnapshot`, `TestDelegationWorkspaceMetadataEscape` | `runtime_integration_test.go`, `runtime_views_test.go`, `runtime_amend_test.go`; keep the fixture helpers (finding 9). `TestWorkspaceReadSnapshotScope`: keep if `ReadSnapshotFile` stays (history) |
| `internal/delegation/workspace_review_test.go` | all 5 | runtime integration tests |
| `internal/delegation/workspace_sandbox_test.go` | `TestDelegationWorkspaceSandboxCommands` | change, not delete: it runs real Git/test commands under the sandbox in an owned checkout prepared with the legacy `Prepare`; prepare it with `PrepareRuntime(..., writable=true)` instead (skips without a sandbox) |
| `internal/engine/delegation_execution_test.go` | all 11 `TestChildExecution*` | runtime runner tests (`agentrt_runner_test.go`), `TestPortedApprovalInsideAgentSurfacesOnThread` (approval), `TestPortedRestartInterruptsAndNeverApplies` and `TestPortedBudgetStop` (finalization, usage), `agentrt_policy_test.go` (scopes, narrowing), `TestRunnerReadOnlyAgentCannotWrite` (reviewer policy). Move the helpers first (finding 9) |
| `internal/engine/delegation_workflow_test.go` | all 5 | `TestPortedSequentialImplementThenReview`, `TestE2ESuggestEnablePlanApproveSpawnIntegrate` |
| `internal/engine/delegation_workflow_guards_test.go` | all 7 | `TestPortedSequentialImplementThenReview`, `TestPortedRestartInterruptsAndNeverApplies`, `agentrt_integrate*_test.go`, `agentrt_verification_test.go` (visual QA blocked check is in `planVerificationChecks`, kept) |
| `internal/engine/delegation_tools_test.go` | `TestDelegationToolAuthority` | `agentrt_wiring_test.go TestAuthorizeDelegationToolForRuntimeAgentContext`, `tools/agentrt_test.go` |
| `internal/engine/delegation_compaction_test.go` | `TestChildExecutionAutomaticCompaction` | none: no runtime-agent automatic compaction test exists (spec 8.6 open). Delete, and record the gap |
| `internal/engine/delegation_activation_test.go` | `TestDelegationWorkspaceDefaultRunner`, `TestDelegationWorkspaceParentAdmission` | runtime workspace admission is covered by `agentrt_integrate_spawn_test.go`, `runtime_views_test.go`; or port `ParentAdmission` to `PrepareRuntime` |
| `internal/engine/model_driven_review_test.go` | `TestReviewCannotWidenParentPermissions` | `TestRunnerReadOnlyAgentCannotWrite`, `TestPortedSequentialImplementThenReview` |
| `internal/engine/agentrt_activation_test.go` | `TestSuggestRequiresRuntimeV2` | — (flag gone) |
| `internal/engine/agentrt_activation_e2e_test.go` | `TestE2EFlagOffUnchanged` | — (flag gone) |
| `internal/engine/agentrt_wiring_test.go` | `TestRuntimeV2FlagOffKeepsLegacyTools`, `TestCancelDelegationWithoutRuntimeIsUnchanged` | `TestDelegationRuntimeIsOnlyPath` (Task 7) |
| `internal/tools/delegation_test.go`, `internal/tools/delegation_schema_test.go` | `TestAgentToolsLifecycle`, `TestAgentToolsClaudeSchemas` | `tools/agentrt_test.go` |
| `internal/server/delegation_e2e_test.go` | `TestDelegationRPCEndToEnd`, `TestDelegationRPCCancelReleasesApprovalAndWait` | `TestPortedSequentialImplementThenReview`, `TestPortedCancellationCascadeReturnsBudget`, `TestPortedApprovalInsideAgentSurfacesOnThread`, `agentrt_delegation_rpc_test.go`. Keep `TestDelegationRPCUnavailableRouting` (admission) |
| `internal/store/delegation_test.go` | `TestAgentChildPersistenceOwnershipAndIdempotency`, `TestAgentChildTransitionValidation` | — (writers gone) |

### 6.2 Change (stay, but stop using legacy code or the flag)

- `internal/engine/agentrt_wiring_test.go`: `runtimeV2On` (delete the helper and pass `nil`), `TestRuntimeV2FlagOnRegistersRuntimeTools` (assert the runtime set and no legacy `agent.followup`/`agent.complete`/`agent.review_report`), `agentToolNames` (drop the `agent.complete`/`agent.review_report` workflow map), `TestEngineStartReconcilesRuntimeAgentsWithFlagOff` (rename; no flag).
- `internal/engine/agentrt_recovery_test.go`: `restartLogged` sets `cfg.Agents.RuntimeV2 = false` (drop).
- `internal/engine/agentrt_runner_test.go`: `TestChildPromptTextIsPathSpecific` (drop the legacy reviewer half).
- `internal/engine/agentrt_policy_test.go`: `TestRuntimeToolAllowedFollowsCapabilities` (the `agent.review_report: false` row may stay or go).
- `internal/engine/model_driven_review_test.go`: `TestFocusedReviewIsOptionalAndBudgeted` (new lead prompt, finding 7).
- `internal/engine/task_outcomes_test.go`: `TestDelegatedTaskRecovery` builds a legacy `childExecution` with `AgentRoleReviewer`/`AgentRoleImplementer`; rebuild it with `runtime: &runtimeExecution{...}` and read/write capabilities.
- `internal/engine/task_review_regression_test.go`: `TestTaskChildRequirementRevalidatedAfterApproval` (same).
- `internal/engine/agentrt_plan_test.go`: `TestProposePlanValidation` keeps its `"role"` case (a role key is refused).
- `internal/delegation/runtime_integration_test.go`, `runtime_views_test.go`, `runtime_amend_test.go`: keep; they use `workspaceFixture`/`workspaceGit`/`workspaceWrite` (finding 9).
- `internal/store/agentrt_execution_test.go`: `TestMirrorRowIsInert`, `TestRuntimeExecutionThreadHasEmptyApprovalMode` (seed legacy rows by SQL).
- `internal/store/delegation_test.go`: `TestReconcileAgentChildrenPreservesArtifacts`, `TestAgentDelegationMigrationAndOwnership` (seed by SQL); keep `TestAgentBudgetReadDoesNotInitialize`, `agentTaskFixture`.
- `internal/store/delegation_workspaces_test.go`: `TestAgentInspectionMigrationAndOwnedUniqueness` (seed by SQL).
- `internal/server/agent_suggestion_test.go`, `agent_plan_test.go`: drop `c.Agents.RuntimeV2 = true`.
- Every `delegationFixtureWith(t, runtimeV2On)` / `newActivationE2E(t, runtimeV2On)` call: pass `nil`.
- The tests listed in section 7 (flag-off semantics).

### 6.3 Ported scenarios (this task)

`internal/engine/agentrt_ported_scenarios_test.go`, all on the runtime path with the scripted-provider fixtures:

| Test | Scenario (spec 6.5) | Replaces |
| --- | --- | --- |
| `TestPortedSequentialImplementThenReview` | sequential implement-then-review via a plan: a `files:rw` entry named `implementer`, a read-only entry named `reviewer`; accept, verify, review of the integrated head, complete; the "reviewer" cannot write | `TestDelegationWorkflowEndToEnd`, `TestDelegationWorkflowOrderingAndCorrections`, `TestDelegationWorkflowLatestReviewAuthoritative`, `TestDelegationWorkflowRequiredChecks`, `TestDelegationRPCEndToEnd`, `TestChildExecutionReviewerPolicy` |
| `TestPortedCancellationCascadeReturnsBudget` | cancel an agent through `delegation/cancel`: it, its running child and its queued child stop; the queued one never runs; the whole budget share returns to the lead | `TestCoordinatorActualChildCancellation`, `TestCoordinatorTaskCancellationStopsActualChild`, `TestCoordinatorCancelledPartialResultAndEvents`, `TestChildExecutionFollowupAndCancellation`, `TestDelegationRPCCancelReleasesApprovalAndWait` |
| `TestPortedBudgetStop` | an agent's budget slice runs out: failed with `budget slice exhausted`, spending on the task budget, work retained, task active, another agent still runs | child usage half of `TestChildExecutionFinalizationAndHistory`, the "Budget stopped" flat card (`Agents.test.ts`) |
| `TestPortedRestartInterruptsAndNeverApplies` | restart over a running agent and a submitted revision: interrupted, not rerun, nothing applied, checkout unchanged, work retained | `TestReconcileAgentChildrenPreservesArtifacts`, `TestDelegationWorkflowRestartDuringApproval`, `TestCoordinatorRestoredDurationAndDetachedAdmission`, restart part of `TestChildExecutionFinalizationAndHistory` |
| `TestPortedApprovalInsideAgentSurfacesOnThread` | an agent's unconfined shell command asks in the lead chat (attributed to task and agent), the agent waits; declined, or cancelled while waiting | `TestChildExecutionApproval`, approval half of `TestDelegationRPCCancelReleasesApprovalAndWait` |

Plus `agentrt_readonly_authority_test.go TestRuntimeReadOnlyAgentKeepsGrantedNonWriteTools`
(regression for the gap found while porting, fixed in `b4200f7`).

## 7. Tests that depend on flag-off semantics (measured)

Measured by flipping the config default to `RuntimeV2: true` in a scratch copy
and running `go test ./internal/engine ./internal/server ./internal/tools
./internal/store ./internal/delegation ./internal/agentrt`. Failures other
than the known environmental ones (no `/dev/kvm`, bwrap `execvp /bin/sh`) and
the legacy tests already marked delete:

Engine (beyond the delete rows and the env failures `TestChildExecutionLivePolicyNarrowing`, `TestDelegationWorkflowEndToEnd`, `TestDelegationWorkflowRequiredChecks`, `TestEvaluationBundleMatrix`):

| Test | Why it fails with the runtime on | Fix in Task 7 |
| --- | --- | --- |
| `delegation_activation_test.go TestDelegationAdmission` | reuses the task after a plain admitted `delegationTurn`: "delegation task is finished or interrupted" (a planless admitted turn closes its runtime task) | hold the admitted turn with `liveAdmittedTurn`, or approve a plan in it |
| `delegation_activation_test.go TestDelegationAdmissionFailedFollowupPreservesTask` | same | same |
| `delegation_activation_test.go TestDelegationAdmissionProjectChangesAndNonGit` | known env failure; also reuses the task | same |
| `delegation_budget_test.go TestDelegationBudgetInheritedAcrossFollowups`, `TestDelegationBudgetInterruptCancelsPendingApproval`, `TestDelegationBudgetChildContextCancellationReleasesTurn`, `TestDelegationBudgetTaskGeneratedTitleRetainsContext`, `TestDelegationBudgetReportedErrorUsagePreservesIncompleteFailure` | task closed after the first planless turn ("delegation task is foreign or no longer active") | same; they test the kept shared budget, so keep them |
| `delegation_manual_compaction_test.go TestManualCompactionDelegationBudget`, `TestManualCompactionDelegationCancellation` | same | same |
| `output_limit_test.go TestOutputLimitDelegationOverrunPreservesFailureAndAccounting` | same | same |
| `agentrt_runner_test.go TestRunnerCancellationEndsPromptly` | test assumption: `childExecutionTask` now holds the lead's admitted turn open, so "no active turns" sees that turn | wait for `activeTurns <= 1` (as the ported cascade test does) |
| `agentrt_wiring_test.go TestCancelDelegationWithoutRuntimeIsUnchanged`, `TestEngineStartReconcilesRuntimeAgentsWithFlagOff`, `TestRuntimeV2FlagOffKeepsLegacyTools`; `agentrt_activation_test.go TestSuggestRequiresRuntimeV2`; `agentrt_activation_e2e_test.go TestE2EFlagOffUnchanged` | assert flag-off behaviour | delete or rename (6.1/6.2) |
| `delegation_activation_test.go TestDelegationWorkspaceParentAdmission`, `delegation_tools_test.go TestDelegationToolAuthority`, `delegation_compaction_test.go`, `delegation_execution_test.go` tests | legacy (already delete rows) | 6.1 |

Server (beyond `TestShellEditAfterPassKeepsWorkOpen`, `TestWorkGetActiveOnly` env failures and the delete rows `TestDelegationRPCEndToEnd`, `TestDelegationRPCCancelReleasesApprovalAndWait`):

| Test | Why | Fix |
| --- | --- | --- |
| `delegation_test.go TestDelegationRPCCancel` | the planless admitted turn completed the task before the cancel | keep the admitted turn live (scripted provider that blocks) or approve a plan |
| `delegation_test.go TestDelegationRPCReadOwnership` | same task reuse | same |
| `compiler_e2e_test.go TestDesignedWorkflowFlagOffCompatibility` | compares the exact lead tool list; with the runtime on `agent__suggest` is offered | add `agent.suggest` to the expected list (it is offered in every non-admitted lead turn) |

`internal/tools` (only the env failure `TestEvaluationToolsConfined`), `internal/store`, `internal/delegation`, `internal/agentrt`: no failures.

## 8. Docs and config mentions (Task 8)

| File | Mention | Decision |
| --- | --- | --- |
| `docs/subagent-development.md` | whole legacy flow (implementer, reviewer, two correction rounds, `agent.followup`), section "Agent runtime (experimental, `agents.runtime_v2`)" | rewrite as the only flow (Task 8 brief) |
| `docs/model-capacity-and-compaction.md` | line 38 "standalone reviewer ... legacy integration/completion workflow APIs", line 78 ruling on `agent.integrate`/`agent.complete` | change line 38 to the runtime; line 78 is a historical ruling, leave or annotate |
| `docs/task-recovery.md` | line 45 "readonly reviewer scopes" | change to "read-only agent scopes" |
| `config.example.yaml` | `runtime_v2` block | delete (Task 7) |
| `docs/superpowers/plans/*`, `specs/*` | history | leave |
| `README.md`, `GO_ENGINE.md`, `TASKS.md`, `site/` | no mentions | — |

## 9. How this was verified

- Every `.go` file in `internal/`, `cmd/` and `app/` was type-checked with
  `go/types` (stdlib, export data from `go list -export -deps`) to record each
  use of each module-level object (function, method, type, var, const) by
  each declaration. A reachability walk from every `cmd/*` `main` (plus
  `init`), cutting the legacy entry points (`NewAgentTools`,
  `NewAgentWorkflowTools`, `workflowRunner`, `childRunner`, the coordinator's
  child scheduling, `managedChildWorkspaces.Resolve/Finalize`,
  `delegationWorkflowCall`, `submitReview`, `delegationWorkflowPolicy`), lists
  which "legacy" symbols are still reached by live code (findings 1–5, 11) and
  which symbols become dead (the `delete` rows in `workspace.go` and the store).
  Interface dispatch was modelled by linking a type to its methods whose names
  are interface methods in the module.
- `go list -deps`: `internal/agentrt` depends only on `internal/protocol`;
  `internal/tools` depends on `internal/delegation` only through
  `tools/delegation.go`; non-test files importing `internal/delegation`:
  engine `agentrt_budget.go`, `agentrt_integrate.go`, `agentrt_readmodel.go`,
  `agentrt_runner.go`, `agentrt_wiring.go`, `compaction.go`,
  `delegation_execution.go`, `delegation_policy.go`, `delegation_workspace.go`,
  `engine.go`, `title.go`, `turn.go`, `turn_budget.go`, and
  `tools/delegation.go`.
- `grep -rn` over `internal/` and `app/frontend/src` for `AgentRole`,
  `agent.followup`, `agent_children`, `delegation_workflow`,
  `DelegationWorkflow`, `agent.(spawn|status|wait|cancel|complete|review_report|integrate)`,
  `runtime_v2`, `RuntimeV2`, `implementer`, `reviewer`, `AgentChild`,
  `Children`, `DelegationCoordinator`; false positives dropped
  (`llm.Role`, `evaluation.Review.Reviewer`, plugin test data
  `internal/plugins/testdata/claude/agents/reviewer.md`, accessibility
  `"role"` JSON in `toolreduce`).
- Test classification: a test counts as legacy when its body (or a helper it
  calls, transitively within the package) uses a legacy symbol or a legacy
  tool string; every hit was then read by hand (false positives: runtime
  `Runtime.Spawn` calls, `"role"` cases that assert a role argument is refused).
- After each Task 7 step, `go build ./... && go vet ./...` is the check that
  nothing on the runtime path was removed.

## 10. Suggested order for Task 7

1. Move: tools helpers (finding 3), lifecycle cancellation helpers (finding 2),
   coordinator registry methods into `coordinator.go` (finding 1), engine
   verification helpers (finding 5), test fixtures (finding 9). Build.
2. Delete the legacy tools and their registration; always register the
   runtime; change `IsAgentControlSource` (finding 11). Build.
3. Delete the engine legacy code (`delegation_workflow.go` rest,
   `childRunner`, `updateChildExecution`, resolver interfaces and the
   `DelegationWorkspaces` field, role branches in `delegation_policy.go`,
   `task_child_authority.go`, `turn.go`, `tool_selection_runtime.go`,
   `workflow.go`, `engine.go` prompt branches; finding 7). Build.
4. Delete the coordinator's child scheduling and `lifecycle.go`, the legacy
   `workspace.go` methods, the store writers and follow-up accessors,
   `AgentRole`, `AgentFollowup`, the `DelegationUpdated.Child/Error` and
   `AgentSnapshot.ChildErrors/ChildSequences` fields. Build.
5. Remove the flag (config, example, guards, tests; section 7). Add migration
   0038. Run the full suite.
