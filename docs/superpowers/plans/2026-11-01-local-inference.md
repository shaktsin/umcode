# Local inference implementation plan

> For agentic workers: execute with TDD. Parent coordinates the Go persistence/RPC boundary and parallel agents implement the independent Python runner, UI, and local chat adapter against the shared contract.

**Goal:** Real local checkpoint prediction and persistent promotion into ordinary chat, testable on the existing local app without publishing.

**Spec:** docs/superpowers/specs/2026-11-01-local-inference-design.md.

**Architecture:** Immutable model-owned checkpoint snapshots + managed embedded PyTorch worker + explicit plain local chat provider. User's approved mock is the UI authority. Existing Data branch/worktree remains the only implementation checkout. User request authorizes implementing the reviewed mock now; do not add approval gates or push changes.

## Review focus

Checkpoint substitution/path traversal and unsafe pickle/code execution; allocation limits before graph/weights/image processing; deleting/recreating a model during inference; stale test receipts enabling promotion for wrong versions; persistent local-chat dispatch accidentally invoking cloud/tools or losing errors/cancellation.

## Task 1: Parent-owned checkpoint/RPC/runtime boundary

- [x] Add shared Go types; write failing storage tests for immutable import/read/selection, symlinks, traversal, oversize and replacement model identities.
- [x] Store graph/preparation/weights under model/checkpoints with pinned roots and atomic publication. Add Python subprocess manager, bounded IPC, timeout/unload/close and truthful runtime discovery.
- [x] Wire the eight RPCs, successful-test receipts, promotion persistence and catalog helpers. Gate on app client and modelCreatedAt. Invalidate workers/promotions when model is deleted. Run focused Go tests and vet.

## Task 2: Python runner (parallel, exclusive internal/workbench/pytorch/)

- [x] Reproduce failure with tests for real trained weights, graph errors, unsupported/malformed weights and bounded inputs.
- [x] Implement strict graph model construction matching codegen ModuleDict keys for text/feature/vision palette, saved normalization/labels/byte tokenizer, prediction and generation, and IPC loop.
- [x] Test with actual PyTorch in an isolated environment. Produce exportable trained tiny test assets outside user model directories; no seeded user models.

## Task 3: Frontend (parallel, exclusive inference client/component and model picker)

- [x] Write failing state/picker tests for version scoping, stale prediction, import errors, test-gated promotion and local models without cloud credentials.
- [x] Implement real Inference UI and types/client against shared RPCs; runtime dropdown without Auto, test/promotion adjacent, import/load/unload and appropriate modality inputs. Wire Workbench and regular chat model picker.
- [x] Run full frontend tests/check/build. Preserve existing Data/Build and current user drafts.

## Task 4: Chat adapter (parallel, exclusive new local chat files and StartTurn integration)

- [x] Write failing tests proving a workbench_local selection requires no cloud key, uses pinned promotion, streams a real response, rejects missing/invalid versions and unsupported images/tools, and has no cloud fallback.
- [x] Integrate an explicit plain-chat local path through existing transcript/turn notifications and cancellation. Consume the parent-owned promotion/generate/catalog helpers only. Do not modify parent-owned engine.go/routes.go or frontend.
- [x] Run focused engine/server regressions; tell parent needed wiring points. Do not weaken normal chat/agent behavior.

## Task 5: Integration/review/local handoff

- [x] Build managed local Python from cached dependencies and real Go engine. Review combined diff and fix material findings with RED/GREEN regressions.
- [x] Run actual RPC/browser flows using a temporary project and trained weights: empty/import/wrong weights/load/test/promote/ordinary chat/restart/unload/delete. Remove temporary project afterward, preserve user's model ere.
- [x] Leave ports 5173/5180 on the updated local engine and write exact test instructions/fixture import path. Report tested scope and real limits; no commits/push/PR changes unless later requested.

Implemented by the parent after delegated partial work stopped. A separate agent review was unavailable; parent reviewed the final boundaries and ran real Python, RPC and browser acceptance plus race checks. Untrained architecture testing added after user clarified they have no weights. No publication requested.
