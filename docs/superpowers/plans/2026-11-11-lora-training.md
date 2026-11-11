# Imported LoRA training implementation plan

> Execute inline with superpowers:executing-plans. User requested immediate implementation and minimal targeted testing.

**Goal:** Replace destructive causal output-head tuning with attention LoRA and correctly formatted answer-only instruction training; retain classifier training.
**Architecture:** Existing offline Transformers runner + PEFT 0.17.1. Frozen source model; rank 8 / alpha 16 attention adapters. Merge the selected adapter into a separate full checkpoint so existing validation, inference and chat promotion remain unchanged. Standalone adapter export/resume is not included.
**Spec:** This document records implementation choices under the user's authorization to proceed directly on point 3.

## Constraints and review focus
- No original-file writes; keep existing CPU, 1B total-parameter and timeout limits.
- Text training defaults to LoRA; CNN/BERT/ViT classification keeps its frozen-backbone head path. No claim of universal LoRA or answer-quality guarantees.
- Question/response columns stay separate; native chat template and prompt masking; reject overflow rather than silently discard answers.
- Corpus training remains explicit; old question/response preparations must be saved again.
- AdamW, gradient clipping, deterministic shuffle, accumulation/warmup, finite-loss checks and best-validation selection including baseline. Test split is evaluation only.
- Minimal new tests: answer masking/overflow, LoRA freeze/merge/reload, and preparation separation. Reuse existing focused integration checks; skip repository-wide sweep at user request.

## Task 1: Data contract and LoRA training
- [x] Add failing focused tests for question/response preservation, answer masking and LoRA merged checkpoint reload.
- [x] Implement imported-v2 preparation with separate response and stale-pair rejection.
- [x] Implement PEFT attention LoRA, answer-only training, training safeguards and baseline/best metrics. Export merged checkpoint; no inference/checkpoint security format changes.
- [x] Run targeted Python and Go checks, including classifier regression.

## Task 2: Local test UI and PR
- [x] Show LoRA vs classifier mode, safe learning-rate defaults, data instructions, best-epoch/baseline result. Permit re-saving imported preparations.
- [x] Check frontend types/build; run short real Qwen LoRA train/reload smoke in isolated temporary folder, without overwriting user's data.
- [x] One bounded final review; rebuild/restart local preview. Publish to existing PR #98 after commit. Report tested scope and limitations.
