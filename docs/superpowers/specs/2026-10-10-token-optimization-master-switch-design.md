# Global token optimization master switch

## Agreed product behavior

One persistent global **Token optimization** switch controls the complete optimization bundle. When enabled, context compilation, retrieval, progressive tool loading, tool-result reduction, Designed Workflow and verified memory promotion operate automatically in all chats. Users do not enable individual components, approve optimization activation, or approve internal plans/designs merely because optimization is enabled.

The purpose is lower token cost without adding friction to ordinary tasks. Optimization must preserve task quality, action permissions, verification evidence and clear reporting of failures. The user explicitly rejected separate workflow/memory opt-ins and repeated chat approvals.

Fresh installations keep the master switch off pending Phase 6 evaluation and rollout. Enabling it enables every component; this initial master default is distinct from individual component opt-ins, which do not exist in the product UI.

## Approach

Use a centrally resolved, immutable effective optimization policy for each turn, backed by the existing global settings store. This is preferable to mutating shared engine configuration because concurrent turns must not observe mixed settings. Startup-only configuration would require restarts; independent component controls would conflict with the agreed product behavior.

The engine resolves the persisted master setting at turn admission and passes the effective policy to context preparation, tool selection, Work operations and memory services. The same snapshot governs specialist calls, resumed tool execution and verification within that turn. Services must not retain conflicting startup-only feature values. Register available internal tools independently of startup state, then expose and authorize them using the turn policy and existing tool permissions.

## Settings and persistence

Add one toggle to global Settings, using the existing get/set RPC and settings-store patterns. Copy: “Token optimization” with help text “Automatically optimize context, tools, workflow and verified memory across chats.” Do not expose component toggles or add activation confirmations.

A successful write applies to subsequently admitted turns across existing and new chats without restarting. Active turns retain their original snapshot. Persistence failure returns an error and leaves the displayed saved state unchanged. A read failure must be visible and must not silently replace a saved setting with a different policy.

An explicit stored master value takes precedence over legacy YAML component flags. Before a master value exists, preserve existing configured behavior for compatibility; display a clearly identified legacy configuration state and allow one toggle action to replace it with an explicit all-on or all-off policy. New configurations with no legacy flags resolve to off. Legacy mixed configuration is a migration state, never a new set of product choices. An explicit off disables all six components even if legacy flags are true.

## Automatic workflow behavior

Enabling the bundle retains internal task classification, graph tracking, dependency checks, verification and completion accounting. These are automatic implementation details, not additional user approval stages. Simple chat tasks stay lightweight; enabling Designed Workflow does not force every task through a lengthy design process.

Separate workflow readiness from human authorization. Internally generated design, architecture, schema, contract and risk classifications must not create an optimization-specific approval waiter. Record autonomous decisions with their actual provenance; never label an agent decision as user-approved or fabricate approval records. Adapt graph transitions and memory eligibility to this provenance explicitly rather than auto-responding to approval requests.

The existing chat approval mode continues to govern actual tool actions. When an action requires permission under that mode, request it once through the normal action approval path. Workflow classification must not add a second approval for the same action. Missing information genuinely needed to fulfill the task can still require clarification; optimization activation never does.

Existing pending workflow gates must not leave enabled chats blocked by obsolete internal approval requests. Resolve or supersede optimization-only requests with an auditable migration reason, retaining history and any independent action permission requirements. Do not silently treat pending human decisions as granted authorization.

## Memory and fallback

Verified memory promotion is enabled automatically with the bundle and still requires supported evidence, scope, redaction and revocation rules. Autonomous workflow decisions must not be promoted as user-approved preferences or authority. Derived technical facts may be promoted only under their applicable evidence rules. Disabling the bundle stops optimization memory retrieval/promotion for new turns; it does not delete historical records.

Use established bounded fallback when a component is unavailable. Do not ask the user to enable a substitute component. Preserve mandatory action permissions and verification checks on fallback; report a material limitation when it affects the outcome. The switch does not disable unconditional audit/vault behavior or restore unsafe historical context gathering.

## Phase 6 integration

The Phase 6a evaluation design remains a separate pending spec. Its isolated runner may set explicit component policies for ablation, with no effect on product global settings. Evaluate the final automatic bundle, including the removal of workflow-only approval interruptions. A test-only or isolated evaluation override must not become individual user opt-ins.

This work adds the global control and consistent runtime behavior. It does not claim release qualification or enable the master by default for everyone. Subsequent rollout follows evaluation evidence.

## Acceptance and verification

- One saved toggle enables all six components across chats and survives restart; explicit off disables all six despite legacy flags.
- Concurrent and active turns retain consistent snapshots; new turns observe the new saved state.
- Ordinary direct and complex tasks complete without optimization activation, planning or design approval prompts.
- Workflow graph transitions preserve provenance and verification requirements; no fabricated user approvals or user-authority memory appear.
- Existing chat action approval modes remain effective, including denial, revocation and resumed execution, without duplicate workflow prompts.
- Pending legacy workflow approvals cannot strand a chat; history and independent action authorization remain intact.
- Memory evidence, redaction, scope and revocation tests pass with automatic promotion enabled.
- Settings persistence failures and component fallback produce consistent, visible outcomes.
- Run focused engine/Work/memory/settings tests, relevant frontend checks, the root Go suite and go vet before completion.

## Review status

Written spec and implementation plan accepted; user selected Native execution. All six implementation tasks are implemented. One independent whole-branch review identified three Important findings, reproduced with failing tests and fixed in one pass: projectless evidence verification and legacy criteria migration, missing/null settings parameters, and preserving commented user instructions when generated memory is omitted. Completion checks are recorded in the implementation plan. Phase 6a evaluation remains separately pending.
