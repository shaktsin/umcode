# Token Optimization Master Switch Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Provide one persistent global switch enabling all six optimizations automatically across chats without added workflow approvals.

**Architecture:** Resolve one immutable policy when a turn is admitted and carry it through engine, Work and memory operations. Keep internal readiness/evidence checks separate from action authorization and record autonomous decisions with truthful provenance. Reuse global settings persistence and RPC patterns.

**Tech Stack:** Go, SQLite, existing JSON RPC, Svelte 5 and TypeScript; no new dependencies or version changes.

**Spec:** `docs/superpowers/specs/2026-10-10-token-optimization-master-switch-design.md` (accepted by user).

## Global Constraints

- One persistent global **Token optimization** switch controls the complete optimization bundle.
- Users do not enable individual components, approve optimization activation, or approve internal plans/designs merely because optimization is enabled.
- Fresh installations keep the master switch off pending Phase 6 evaluation and rollout.
- An explicit stored master value takes precedence over legacy YAML component flags.
- Active turns retain their original snapshot.
- The existing chat approval mode continues to govern actual tool actions.
- Never label an agent decision as user-approved or fabricate approval records.
- This work does not implement the separately pending Phase 6a evaluation harness or claim release qualification.

## Review Focus

- Malformed persisted settings: expose a load error rather than silently changing policy (Task 1).
- Toggle changed during an approval wait: the active turn retains its snapshot and permission revocation still applies (Tasks 2 and 3).
- Legacy pending approvals after restart: supersede only workflow-only requests without granting action authority (Task 3).
- Autonomous decisions reused as memory: reject user-authority promotion while allowing independently verified command facts (Task 4).
- Settings responses arriving out of order: display the last confirmed saved state and prevent overlapping writes (Task 5).

## File Structure

- Create `internal/optimization/policy.go` and `policy_test.go`: value policy, legacy resolution and explicit context transport.
- Create `internal/engine/optimization.go` and `optimization_test.go`: setting resolution and engine settings API.
- Modify `internal/protocol/methods.go`; create `internal/protocol/optimization.go`; modify `internal/server/routes.go`: typed RPC contract.
- Modify `internal/engine/turn.go`, `compile.go`, `reducer.go`, `workflow.go`, `workflow_context.go`, `tool_catalog.go`, `tool_selection_runtime.go`, `engine.go`: replace runtime configuration reads with turn policy; register tools independently of startup flags.
- Modify `internal/work/work.go`, `graph.go`, `memory_source.go`, `internal/workflowgraph/readiness.go`: policy-aware graph semantics and truthful decision provenance.
- Modify `internal/store` graph/approval persistence files and add next numbered migration if provenance requires schema fields; retain existing migration history.
- Modify engine memory integration and `internal/memory/qualify.go` only where eligibility requires explicit provenance.
- Create `app/frontend/src/lib/panels/settings/TokenOptimization.svelte`; modify `Settings.svelte` and RPC types at their existing definitions.
- Add tests beside owning code; integration coverage in `internal/server/optimization_e2e_test.go`.

### Task 1: Resolve and persist the global policy

**Interfaces:** Produce `optimization.Policy` with bool fields `ContextCompiler`, `ContextRetrieval`, `ProgressiveTools`, `ToolResultReducers`, `DesignedWorkflow`, `AutoPromote`, `AutomaticWorkflow`; `optimization.All(enabled bool) Policy`; `optimization.WithPolicy(ctx context.Context, p Policy) context.Context`; `optimization.FromContext(ctx context.Context) (Policy, bool)`. Produce `Engine.TokenOptimization(ctx context.Context) (protocol.TokenOptimizationResult, error)` and `Engine.SetTokenOptimization(ctx context.Context, p protocol.TokenOptimizationParams) (protocol.TokenOptimizationResult, error)`.

RPC result fields: `Enabled bool`, `Source string` (`stored`, `legacy`, `default`), `LegacyMixed bool`. Params: `Enabled bool`. Store key: `token_optimization.enabled`; value: strict JSON boolean. For mixed legacy policy, Enabled is false and LegacyMixed true; all-on legacy is enabled, all-off is default. Legacy mode preserves existing behavior including legacy workflow approvals; only explicit master-on selects AutomaticWorkflow.

- [ ] Write `TestTokenOptimizationResolution` table tests: absent/all-off -> default/off; legacy single/mixed -> legacy/mixed with unchanged individual policy; legacy all-on -> legacy/on; explicit true -> all seven fields true; explicit false -> all false despite legacy flags. Add malformed value and unavailable store tests requiring errors and no overwritten value.
- [ ] Run `go test ./internal/optimization ./internal/engine -run 'TestTokenOptimization' -count=1`; expect failing tests before implementation.
- [ ] Implement value/context helpers and settings methods using existing Store.GetSetting/SetSetting patterns. Typed result preserves migration state; no mutable shared config writes.
- [ ] Add `settings/tokenOptimization/get` and `settings/tokenOptimization/set` constants, params/results and server routes. Add RPC tests asserting successful round trip and invalid boolean rejection.
- [ ] Run focused policy, engine and server tests; expect PASS.
- [ ] Commit: `feat: add persistent global optimization policy`.

### Task 2: Make the policy consistent throughout a turn

**Interfaces:** Consume Task 1 policy/context helpers. Produce `Engine.resolveOptimizationPolicy(ctx context.Context) (optimization.Policy, error)` for admission; `Engine.optimizationPolicy(ctx context.Context) optimization.Policy` for internal consumers, using attached policy or immutable legacy config only when invoked outside a turn. Work consumers use `optimization.FromContext`; existing direct service callers retain their configured legacy defaults when absent.

- [ ] Add `TestOptimizationTurnSnapshot`: admit turn A on, toggle off, admit turn B; assert A uses all components through model requests, tool execution and completion, B uses baseline. Use barriers and real engine/provider fixtures, not sleeps. Add a second test for initially off then on after engine construction, proving `work.update` availability and retrieval observation activate without restart.
- [ ] Run `go test ./internal/engine -run 'TestOptimizationTurn' -count=1`; expect FAIL.
- [ ] Resolve policy in `startTurn` before accepting work; preserve it on execution and detached persistence contexts. Pass it through specialist calls, Work updates, observation, recovery and completion. Replace all six runtime flag checks across listed engine files; retain startup-only static config where appropriate. Make tool registration unconditional and selection/execution policy-aware without bypassing existing tool catalog permissions.
- [ ] Make Work operations use the same context policy rather than mutating shared Service booleans. Gate memory retrieval and promotion with the policy; preserve unconditional audit/vault recording.
- [ ] Run `rg -n 'Cfg.Models.(ContextCompiler|ContextRetrieval|ProgressiveTools|ToolResultReducers|DesignedWorkflow)|Cfg.Memory.AutoPromote' internal/engine --glob '!**/*test*'`; review every remaining match and keep only documented legacy resolver/static uses.
- [ ] Run focused engine/Work tests and `go test -race ./internal/engine ./internal/work -count=1`; expect PASS with no shared policy mutation.
- [ ] Commit: `feat: apply immutable optimization policy across turns`.

### Task 3: Remove workflow-only approval friction with truthful provenance

**Interfaces:** Consume `Policy.AutomaticWorkflow`. Produce distinct persisted provenance values `agent` and `user` on decision resolution (in canonical graph metadata, not user-controlled free text). Update graph transition validation so automatic decisions may satisfy internal readiness without being interpreted as human approval. Existing legacy behavior remains when AutomaticWorkflow is false.

- [ ] Add `TestAutomaticWorkflowNoApprovalWait`: complex task classified for architecture/schema changes runs internal graph planning and verification, with zero `workflow.approval.request` events and zero fabricated user approval records. Add `TestAutomaticWorkflowActionPermission`: ask/deny/allow normal tool approvals remain effective, exactly one prompt per protected action, including revocation while awaiting approval.
- [ ] Add `TestAutomaticWorkflowPendingGateRecovery`: persisted legacy workflow-only waiter is superseded under master-on with auditable reason; graph records agent provenance, historical approval is not granted, and independent tool approval is still pending. Include restart and repeated recovery idempotence cases.
- [ ] Run focused workflow/graph tests; expect FAIL for new automatic cases.
- [ ] Implement policy-aware graph transitions and readiness. Keep required planning/evidence checks automatic and bounded; do not suppress tool policy errors or treat missing verification as successful completion. Update workflow instructions so the model makes routine internal decisions autonomously and does not request planning approval merely from classifications. Preserve explicit human instructions requiring review.
- [ ] Add transactional recovery for obsolete workflow-only requests, preserving history and not touching live action waiters. Persist provenance/revision atomically with graph transitions; use additive migration only if required by actual storage representation.
- [ ] Run `go test ./internal/work ./internal/workflowgraph ./internal/store ./internal/engine -count=1`; expect PASS, including existing legacy gate coverage.
- [ ] Commit: `feat: run optimized workflow without extra approval stages`.

### Task 4: Preserve memory evidence and fallback behavior

**Interfaces:** Consume Task 3 canonical decision provenance and Task 2 turn policy. Keep `work.MemorySourceGuidance(n protocol.WorkNode) (category, text string, ok bool)` signature; only decisions with verified user provenance can yield `MemoryCategoryApprovedDecision`. Existing legacy records must be checked against their actual approval evidence, never assumed user-authorized solely from status.

- [ ] Add `TestAutomaticDecisionCannotBecomeUserAuthority`: agent-resolved decision is rejected for approved-decision promotion; tampered provenance cannot bypass source validation. Add `TestAutomaticVerifiedCommandPromotion`: completed Work with verified command evidence promotes automatically with master-on and no opt-in prompt.
- [ ] Add `TestOptimizationOffStopsMemoryUse`: a new off turn neither retrieves optimization memory nor promotes candidates, but stored historical records remain. Add component-unavailable fixtures proving bounded fallback without activation prompts and with action permissions/verification preserved.
- [ ] Run focused engine memory and memory qualification tests; expect FAIL for missing behavior.
- [ ] Implement provenance-sensitive source projection and qualification, maintaining revision binding, redaction, scope, revocation and existing memory recovery protections. Apply policy at caller boundaries, not by mutating the shared memory Service.
- [ ] Run `go test ./internal/memory ./internal/work ./internal/engine -count=1`; expect PASS.
- [ ] Commit: `fix: preserve memory authority under automatic optimization`.

### Task 5: Expose the single global toggle

**Interfaces:** Consume Task 1 typed RPC. New settings component uses saved result Source/LegacyMixed, renders one toggle and no individual feature controls. Disable overlapping writes; retain last confirmed saved state if a save fails; surface get/save errors inline without activation dialogs.

- [ ] Add frontend tests following existing Vitest patterns: explicit saved on/off round trip; legacy mixed state clearly identified; one toggle action writes an explicit bundle state; failed save retains prior saved state; delayed responses cannot overwrite newer confirmed state. Add server e2e test verifying toggle affects new turns across two existing chats.
- [ ] Run targeted Vitest/server tests; expect FAIL before implementation.
- [ ] Implement `TokenOptimization.svelte` and mount in global Settings using existing styles. Label exactly “Token optimization”; help exactly “Automatically optimize context, tools, workflow and verified memory across chats.” Legacy copy: “Using legacy configuration. Changing this switch applies all optimizations together.” Do not add per-chat switches or confirmation dialogs.
- [ ] Run frontend `npm run check`, `npm test`, `npm run build` in `app/frontend`, plus focused server e2e tests; expect PASS. Inspect the settings screen with on, off, legacy and failure states.
- [ ] Commit: `feat: add global token optimization settings control`.

### Task 6: Verify the complete bundle and document behavior

**Interfaces:** No new public interface. Exercise the final product paths through server/engine fixtures with explicit master policy, preserving isolated evaluation ability without global changes.

- [ ] Add `TestOptimizationBundleAcrossChats`: master-on exposes all six mechanisms without activation/workflow prompts and completes a verified artifact; switching off changes the next turn, leaves prior artifacts/history intact, and survives engine restart. Assert substantive emitted requests, reductions, retrieval, tool selection, graph evidence and promoted memory rather than only flags.
- [ ] Add an isolated ablation-policy test proving explicit fixture policy affects only that fixture and never writes global settings. Confirm evaluation overrides are unavailable through ordinary product RPC.
- [ ] Run `GOCACHE=/private/tmp/umcode-go-cache go test ./... -count=1 -timeout=10m` and `GOCACHE=/private/tmp/umcode-go-cache go vet ./...` at repository root; run focused race tests from Task 2 and frontend checks from Task 5 once after final code changes. Run `git diff --check`; all must pass. Do not run paid evaluation or change rollout defaults.
- [ ] Document global switch, migration behavior, in-flight semantics, automatic workflow and permission boundary in existing user-facing settings/config documentation. Update accepted spec review status and completion record with actual commands/results.
- [ ] Commit: `test: verify automatic optimization bundle across chats`.
- [ ] Obtain whole-branch review according to selected execution method, resolve material findings with focused regression tests, then report final evidence. Publishing a new branch/PR awaits a current request; do not infer it from earlier completed Phase 4 publishing instructions.

## Execution handoff

Recommend Native execution because these six tasks share the same turn-policy and graph-provenance interfaces; keeping one implementer reduces coordination cost. One independent whole-branch review is still required. User review of this plan and execution method selection are pending. The separately pending Phase 6a spec is not implicitly approved by accepting this master-switch spec.
