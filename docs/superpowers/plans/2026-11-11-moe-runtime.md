# MoE Runtime Implementation Plan

> **For agentic workers:** Use superpowers:executing-plans; implement inline to minimize credit use.

**Goal:** Deliver points 1 (imported chat promotion) and 2 (local MoE workflows) in one PR. LoRA is deferred.

**Architecture:** Reuse the existing offline Transformers runtime, dataset preparation, checkpoint publication, and imported chat promotion. Support Mixtral, Granite MoE, Qwen2 MoE and Qwen3 MoE through their native implementations. Runtime readiness, not model family, controls the tabs.

**Tech Stack:** Go, Python/PyTorch/Transformers, Svelte/TypeScript.

**Spec:** User-approved points 1 and 2 in this conversation; constraints below capture the implementation boundary.

## Global Constraints

- Local CPU execution only; no new dependencies, cloud execution, quantization support, or remote code.
- Preserve the 8 GiB bundle and one-billion-total-parameter execution limits. Count every expert, not just activated experts.
- Config/weight-only imports remain inspection-only. Complete folders require successful weight/tokenizer validation.
- Training remains output-head-only, with frozen experts/router/backbone. No quality guarantee or LoRA in this PR.
- Architecture edits continue to gate runtime execution; source weights stay unchanged.
- The user's 8 GB Mac cannot safely load the downloaded 3.3B Granite model; provide small explicitly synthetic test folders instead.

## Review Focus

- Native packed experts (Granite), shared experts (Qwen2), and QK normalization (Qwen3) must survive actual load/train/reload.
- Reject invalid top-k/expert counts before model allocation.
- Reject large parameter counts before reading weight payloads.
- Keep source weights frozen and publish separate checkpoints.
- Keep incomplete-folder and architecture-mismatch gates, removing only the blanket MoE exclusion.

## Tasks

- [x] Write failing Go, Python and frontend tests for the four MoE families.
- [x] Add native-family runtime support and expert config bounds; retain resource guards.
- [x] Remove blanket MoE frontend gates; show precise readiness/resource reasons.
- [x] Run real tiny-model data preparation, head training, inference and checkpoint reload for all four families, plus point-1 regressions.
- [ ] Run frontend check/test/build and bounded full Go/Python suites; document any unrelated failures.
- [x] Restart the local preview and provide synthetic import folders for browser testing.
- [ ] Review the combined diff, commit only scoped files, push a new branch, and create a PR against master.
