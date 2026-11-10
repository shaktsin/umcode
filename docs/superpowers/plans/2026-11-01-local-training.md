# Local Training Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Local small-model training with honest evaluation metrics and automatic Inference checkpoints.
**Architecture:** Separate trusted CPU process reuses runner.py graph modules. Go snapshots prepared splits, persists runs, streams epoch events and publishes pinned checkpoints. Svelte polls versioned run state.
**Tech Stack:** Go, Python/PyTorch, Svelte 5, existing RPC.
**Spec:** docs/superpowers/specs/2026-11-01-local-training-design.md

## Global Constraints
- All changes local; existing worktree already isolated; preserve user data.
- CPU only; numeric/text tasks only; one active run, bounded epochs/batch/rows/time.
- Snapshot graph/preparation/splits at start; final checkpoint uses those snapshots.
- Evaluation splits stay separate; no invented scores for empty splits.

## Review Focus
- Cancellation must kill the training process and never publish incomplete weights.
- Edits/deletion/recreated names cannot redirect checkpoint publication.
- Empty/invalid splits must not produce misleading metrics.
- Model switches/disconnect must discard stale UI responses.
- Oversized models and malformed data fail before large allocations.

### Task 1: Python training and metrics
**Files:** internal/workbench/pytorch/train.py, test_train.py.
**Interfaces:** Read one JSON stdin request {graph,selection,config:{epochs,batchSize,learningRate,seed},splitPaths:{train,validation,test},weightsPath}. Import Model/analyze/prepare_selection from runner. Emit JSON lines {type:'epoch',epoch,train:{loss,...},validation:{...}|null}, then {type:'complete',train,validation,test,parameterCount}, or {type:'error',error}. Write weightsPath only at success. Ignore-label generation loss shifts inputs/logits against labels by one position.
- [x] RED tests learned regression, classification metrics, causal shift and empty eval.
- [x] Implement bounded trusted training and real metrics.
- [x] GREEN real Python tests.

### Task 2: Go lifecycle and RPC (root)
**Files:** internal/workbench/training*.go, internal/engine/workbench_training.go, protocol/workbench_training.go, server/routes.go, engine.go.
**Interfaces:** RPC workbench/training/get/start/stop; params {projectId,modelId,modelCreatedAt,runId?,config?}. State {runs:TrainingRun[],runtimeAvailable:boolean,issues:string[]}. Run {id,name,modelCreatedAt,status:'running'|'completed'|'cancelled'|'failed',config,epoch,history:[{epoch,train,validation}],train?,validation?,test?,checkpointId?,error?,createdAt,finishedAt?,durationMs,preparationVersion,parameterCount?}. Metrics {loss,mae?,rmse?,r2?,accuracy?,macroF1?,perplexity?,tokenAccuracy?,samples?}. Start returns TrainingRun; stop returns TrainingRun; get returns State. One active job across app.
- [x] RED lifecycle tests snapshots, version/auth guards, stop, checkpoint handoff.
- [x] Snapshot using pinned nonsymlink directory readers; persist runs atomically.
- [x] Async embedded subprocess, kill cancellation, publish snapshot checkpoint.
- [x] GREEN Go focused tests/race.

### Task 3: Train UI
**Files:** app/frontend/src/lib/workbench/trainingTypes.ts, training.svelte.ts/test, components/workbench/TrainTab.svelte, components/Workbench.svelte.
**Interfaces:** Consume Task 2 contract, matching InferenceTab props plus onInference callback. Poll only visible page; stop polling on destroy/context change, active backend job continues. Get on activation, poll active runs, history selector. Completed checkpoint button switches to Inference (which refreshes on mount). Disable start on dirty Build/disconnected/busy; useful save preparation guidance. Controls epochs=20, batch=16, LR=.003, seed=17. Epoch loss SVG/chart + separate final validation/test metrics; clear unavailable split labels.
- [x] RED state tests stale response, duplicate start, stop, empty eval handling.
- [x] Implement state/UI and wire Train tab.
- [x] GREEN frontend tests/check/build.

### Task 4: Integration and local acceptance
- [x] Review components and fix substantive defects.
- [x] Train real regression through RPC, verify heldout metrics and Inference response.
- [x] Train tiny text example and verify checkpoint/prompt/promotion flow.
- [x] Browser exercise start, progress, stop and handoff, capture screenshot.
- [x] Restart local test engine preserving user home and provide exact URL/instructions.
