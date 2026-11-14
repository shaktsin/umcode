import { afterEach, describe, expect, it, vi } from 'vitest';
import { WorkbenchTrainingState, trainingMetricEntries } from './training.svelte';
import type { TrainingRun, TrainingState } from './trainingTypes';
const context = { projectId: 'p', modelId: 'm', modelCreatedAt: '2026-11-01T00:00:00Z' };
const run = (status: TrainingRun['status'] = 'running', id = 'run1'): TrainingRun => ({ id, name: id, modelCreatedAt: context.modelCreatedAt, status, config: { epochs: 20, batchSize: 16, learningRate: .003, seed: 17 }, epoch: status === 'completed' ? 20 : 1, history: [{ epoch: 1, train: { loss: .5, samples: 8 }, validation: null }], train: { loss: .5, samples: 8 }, validation: null, test: null, createdAt: '2026-11-01T01:00:00Z', durationMs: 100, preparationVersion: 'prepared-1', ...(status === 'completed' ? { checkpointId: 'weights' } : {}) });
const ready = (runs: TrainingRun[] = []): TrainingState => ({ runs, runtimeAvailable: true, issues: [] });
function deferred<T>() { let resolve!: (value: T) => void; const promise = new Promise<T>(yes => { resolve = yes; }); return { promise, resolve }; }
const states: WorkbenchTrainingState[] = [];
function create(call: ConstructorParameters<typeof WorkbenchTrainingState>[0]) { const state = new WorkbenchTrainingState(call); states.push(state); return state; }
afterEach(() => { states.splice(0).forEach(state => state.dispose()); vi.useRealTimers(); });

describe('local training state', () => {
  it('shows the assistant-started run even when an older run was selected', async () => {
    let runs = [run('completed', 'old')];
    const state = create(async () => ready(runs));
    await state.activate(context, true, false);
    expect(state.runId).toBe('old');
    runs = [run('running', 'new'), ...runs];
    await state.focusRun('new');
    expect(state.runId).toBe('new');
    expect(state.activeRun?.id).toBe('new');
  });
  it('blocks a batch above the saved graph limit and states the valid maximum', async () => {
    const state = create(async () => ({ ...ready(), maxBatchSize: 3 }));
    await state.activate(context, true, false);
    expect(state.canStart).toBe(false);
    expect(state.configError()).toContain('3');
    state.config.batchSize = 3;
    expect(state.canStart).toBe(true);
  });
  it('replays a refresh requested while a previous refresh is in flight', async () => {
    const first = deferred<TrainingState>(); let calls = 0;
    const state = create(async () => ++calls === 1 ? first.promise : ready([run()]));
    const activating = state.activate(context, true, false);
    await state.refresh();
    first.resolve(ready()); await activating;
    await Promise.resolve();
    expect(calls).toBe(2); expect(state.run?.id).toBe('run1');
  });
  // Removing request serialization would start two jobs; nesting config differently would break the real RPC.
  it('starts one run with the saved incarnation and flat config while rejecting duplicate clicks', async () => {
    const starting = deferred<TrainingRun>(); const requests: { method: string; params: Record<string, unknown> }[] = [];
    const state = create(async (method, params) => { requests.push({ method, params }); return method.endsWith('/start') ? starting.promise : ready(); });
    await state.activate(context, true, false); expect(state.canStart).toBe(true);
    const first = state.start(); await state.start(); expect(requests).toHaveLength(2);
    expect(requests[1]).toEqual({ method: 'workbench/training/start', params: { ...context, config: { epochs: 20, batchSize: 16, learningRate: .003, seed: 17 } } });
    starting.resolve(run()); await first; expect(state.run?.id).toBe('run1'); expect(state.canStart).toBe(false);
  });
  it('blocks unsaved Build, unavailable preparation/runtime and disconnected starts', async () => {
    let response = ready(); let starts = 0;
    const state = create(async method => { if (method.endsWith('/start')) starts++; return response; });
    await state.activate(context, true, true); await state.start(); expect(state.canStart).toBe(false);
    await state.activate(context, true, false); expect(state.canStart).toBe(true);
    response = { ...ready(), issues: ['Save Data preparation first.'] }; await state.refresh(); await state.start(); expect(state.canStart).toBe(false);
    response = { ...ready(), runtimeAvailable: false }; await state.refresh(); expect(state.canStart).toBe(false);
    await state.activate(context, false, false); await state.start(); expect(starts).toBe(0);
  });
  it('discards old start completions after model replacement, disconnect or disposal', async () => {
    for (const change of ['context', 'disconnect', 'dispose']) {
      const pending = deferred<TrainingRun>(); const state = create(async method => method.endsWith('/start') ? pending.promise : ready());
      await state.activate(context, true, false); const starting = state.start();
      if (change === 'context') await state.activate({ ...context, modelCreatedAt: 'replacement' }, true, false);
      if (change === 'disconnect') await state.activate(context, false, false);
      if (change === 'dispose') state.dispose();
      pending.resolve(run()); await starting; expect(state.runs, change).toEqual([]); expect(state.busy, change).toBe(false);
    }
  });
  it('stops the active job using its ID even when viewing an older completed run', async () => {
    const requests: Record<string, unknown>[] = [];
    const state = create(async (method, params) => { if (method.endsWith('/stop')) { requests.push(params); return run('cancelled'); } return ready([run(), run('completed', 'old')]); });
    await state.activate(context, true, false); state.selectRun('old'); await state.stop();
    expect(requests).toEqual([{ ...context, runId: 'run1' }]); expect(state.activeRun).toBeUndefined(); expect(state.run?.id).toBe('old'); expect(state.runs[0].status).toBe('cancelled');
  });
  it('polls running jobs and stops scheduling after completion, disconnect or disposal', async () => {
    vi.useFakeTimers(); let gets = 0; let status: TrainingRun['status'] = 'running';
    const state = create(async () => { gets++; return ready([run(status)]); });
    await state.activate(context, true, false); await vi.advanceTimersByTimeAsync(1000); expect(gets).toBe(2);
    status = 'completed'; await vi.advanceTimersByTimeAsync(1000); expect(state.run?.checkpointId).toBe('weights');
    await vi.advanceTimersByTimeAsync(3000); expect(gets).toBe(3);
    status = 'running'; await state.refresh(); await state.activate(context, false, false); await vi.advanceTimersByTimeAsync(2000); expect(gets).toBe(4);
    await state.activate(context, true, false); state.dispose(); await vi.advanceTimersByTimeAsync(2000); expect(gets).toBe(5);
  });
  it('preserves empty heldout splits and exposes only finite metrics actually returned', async () => {
    const state = create(async () => ready([run('completed')])); await state.activate(context, true, false);
    expect(state.run?.validation).toBeNull(); expect(state.run?.test).toBeNull(); expect(trainingMetricEntries(state.run?.test)).toEqual([]);
    expect(trainingMetricEntries({ loss: .2, mae: 0, r2: -1, accuracy: .75, macroF1: .6, perplexity: 1.2, tokenAccuracy: .8, rmse: NaN, samples: 8 })).toEqual([
      { label: 'Loss', value: '0.2000' }, { label: 'MAE', value: '0.0000' }, { label: 'R²', value: '-1.0000' }, { label: 'Accuracy', value: '75.00%' }, { label: 'Macro F1', value: '0.6000' }, { label: 'Perplexity', value: '1.2000' }, { label: 'Token accuracy', value: '80.00%' }, { label: 'Samples', value: '8' },
    ]);
  });
});
