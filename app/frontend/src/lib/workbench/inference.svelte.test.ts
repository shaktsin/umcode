import { describe, expect, it } from 'vitest';
import { WorkbenchInferenceState } from './inference.svelte';
import { inferenceRuntimes, validateCheckpointFile } from './inference';
import type { Checkpoint, InferenceState, Prediction, Promotion } from './inferenceTypes';
const context = { projectId: 'p', modelId: 'm', modelCreatedAt: '2026-11-01T00:00:00Z' };
const checkpoint = (id = 'weights'): Checkpoint => ({ id, name: id, createdAt: '2026-11-01T01:00:00Z', graphRevision: 'a'.repeat(64), modelCreatedAt: context.modelCreatedAt, task: 'text-generation', format: 'pt', byteCount: 64, chatCompatible: true, input: { mode: 'text', features: [], sequenceLength: 128, imageWidth: 0, imageHeight: 0, imageChannels: 0, classes: [] } });
const loaded = (id = ''): InferenceState => ({ checkpoints: [checkpoint(), checkpoint('next')], runtimes: [{ id: 'pytorch', name: 'PyTorch', available: true }], loadedCheckpointId: id, issues: [] });
const prediction: Prediction = { checkpointId: 'weights', testId: 'server-receipt', text: 'real completion', device: 'cpu', durationMs: 12 };
const promotion: Promotion = { id: 'local-promotion', ...context, checkpointId: 'weights', name: 'Tiny', runtime: 'pytorch', createdAt: 'now' };
function deferred<T>() { let resolve!: (value: T) => void; const promise = new Promise<T>(yes => { resolve = yes; }); return { promise, resolve }; }
// Catches missing incarnation fields, fabricated predictions, and promotion without a server receipt.
describe('checkpoint inference RPC state', () => {
  it('loads saved metadata, performs an actual test and promotes with the matching server receipt', async () => {
    const calls: { method: string; params: Record<string, unknown> }[] = []; let refreshes = 0;
    const state = new WorkbenchInferenceState(async (method, params) => {
      calls.push({ method, params });
      if (method.endsWith('/get')) return loaded();
      if (method.endsWith('/load')) return loaded('weights');
      if (method.endsWith('/predict')) return prediction;
      if (method.endsWith('/promote')) return promotion;
      throw new Error('Unexpected RPC');
    }, async () => { refreshes++; });
    await state.activate(context, true); expect(state.canPromote).toBe(false);
    await state.promote(); expect(calls).toHaveLength(1);
    state.setPrompt('Hello'); await state.predict();
    expect(state.prediction?.text).toBe('real completion'); expect(state.canPromote).toBe(true);
    expect(calls[2]).toEqual({ method: 'workbench/inference/predict', params: { ...context, checkpointId: 'weights', runtime: 'pytorch', prompt: 'Hello', temperature: 0.7, maxNewTokens: 64 } });
    await state.promote();
    expect(calls[3]).toEqual({ method: 'workbench/inference/promote', params: { ...context, checkpointId: 'weights', runtime: 'pytorch', testId: 'server-receipt' } });
    expect(state.promotion?.id).toBe('local-promotion'); expect(refreshes).toBe(1);
    state.setPrompt('Changed'); expect(state.canPromote).toBe(false); expect(state.prediction).toBeNull();
  });
  it('ignores late predictions after input, checkpoint, runtime, incarnation, connectivity or disposal changes', async () => {
    for (const change of ['prompt', 'checkpoint', 'runtime', 'incarnation', 'disconnect', 'dispose']) {
      const pending = deferred<Prediction>();
      const state = new WorkbenchInferenceState(async method => method.endsWith('/predict') ? pending.promise : loaded('weights'));
      await state.activate(context, true); state.setPrompt('Before'); const testing = state.predict(); expect(state.stage, change).toContain('testing');
      if (change === 'prompt') state.setPrompt('After');
      if (change === 'checkpoint') state.selectCheckpoint('next');
      if (change === 'runtime') state.selectRuntime('llama.cpp');
      if (change === 'incarnation') await state.activate({ ...context, modelCreatedAt: 'replacement' }, true);
      if (change === 'disconnect') await state.activate(context, false);
      if (change === 'dispose') state.dispose();
      pending.resolve(prediction); await testing;
      expect(state.prediction, change).toBeNull(); expect(state.canPromote, change).toBe(false); expect(state.stage, change).toBe('');
    }
  });
  it('rejects missing or mismatched receipts and shows real runner errors', async () => {
    let result = { ...prediction, checkpointId: 'other' }; let fail = false;
    const state = new WorkbenchInferenceState(async method => {
      if (method.endsWith('/predict')) { if (fail) throw new Error('Checkpoint is missing embedding weights'); return result; }
      return loaded('weights');
    });
    await state.activate(context, true); state.setPrompt('Hi'); await state.predict(); expect(state.canPromote).toBe(false);
    result = { ...prediction, testId: '' }; await state.predict(); expect(state.canPromote).toBe(false);
    fail = true; await state.predict(); expect(state.prediction).toBeNull(); expect(state.error).toBe('Checkpoint is missing embedding weights');
  });
  it('imports bounded weights without saving graph or Data drafts', async () => {
    const calls: { method: string; params: Record<string, unknown> }[] = [];
    const state = new WorkbenchInferenceState(async (method, params) => { calls.push({ method, params }); return method.endsWith('/import') ? checkpoint('new') : loaded(); });
    await state.activate(context, true);
    await state.importCheckpoint({ name: 'trained.pth', size: 3, arrayBuffer: async () => new Uint8Array([1, 2, 3]).buffer }, 'Trained v1');
    expect(calls[1]).toEqual({ method: 'workbench/inference/import', params: { ...context, name: 'Trained v1', filename: 'trained.pth', contentBase64: 'AQID' } });
    expect(state.checkpointId).toBe('new'); expect(state.checkpoints.some(c => c.id === 'new')).toBe(true);
    expect(validateCheckpointFile({ name: 'model.py', size: 3 })).toMatch(/pt.*pth.*safetensors/i);
    expect(validateCheckpointFile({ name: 'huge.pt', size: 64 * 1024 * 1024 + 1 })).toContain('64 MiB');
    expect(validateCheckpointFile({ name: 'valid.safetensors', size: 64 * 1024 * 1024 })).toBe('');
  });
  it('does not import a file that finishes reading after switching models', async () => {
    const reading = deferred<ArrayBuffer>(); let imports = 0;
    const state = new WorkbenchInferenceState(async method => { if (method.endsWith('/import')) imports++; return loaded(); });
    await state.activate(context, true);
    const importing = state.importCheckpoint({ name: 'trained.pt', size: 3, arrayBuffer: () => reading.promise }, 'Weights');
    await state.activate({ ...context, modelId: 'other' }, true); reading.resolve(new Uint8Array([1, 2, 3]).buffer); await importing;
    expect(imports).toBe(0); expect(state.stage).toBe('');
  });
  it('restores persisted promotion and removes from chat without removing weights', async () => {
    let refreshes = 0; const methods: string[] = [];
    const state = new WorkbenchInferenceState(async method => { methods.push(method); return method.endsWith('/unpromote') ? { ok: true } : { ...loaded(), promotion }; }, async () => { refreshes++; });
    await state.activate(context, true); expect(state.promotion?.id).toBe('local-promotion');
    await state.unpromote(); expect(state.promotion).toBeNull(); expect(state.checkpoints).toHaveLength(2); expect(refreshes).toBe(1);
    expect(methods).toEqual(['workbench/inference/get', 'workbench/inference/unpromote']);
  });
  it('sends finite feature values and image bytes using the checkpoint input type', async () => {
    const requests: Record<string, unknown>[] = []; let mode = 'features';
    const state = new WorkbenchInferenceState(async (method, params) => {
      if (method.endsWith('/predict')) { requests.push(params); return { ...prediction, value: 12 }; }
      return { ...loaded('weights'), checkpoints: [{ ...checkpoint(), chatCompatible: false, input: { ...checkpoint().input, mode, features: ['age', 'score'] } }] };
    });
    await state.activate(context, true); state.setFeature('age', '42'); state.setFeature('score', '1.5'); await state.predict();
    expect(requests[0]).toEqual({ ...context, checkpointId: 'weights', runtime: 'pytorch', features: { age: 42, score: 1.5 }, temperature: 0.7, maxNewTokens: 64 });
    expect(state.canPromote).toBe(false); state.setFeature('score', 'NaN'); await state.predict(); expect(requests).toHaveLength(1); expect(state.error).toContain('finite');
    mode = 'image'; await state.refresh(); state.setImage('aW1hZ2U=', 'input.png'); await state.predict(); expect(requests[1].imageBase64).toBe('aW1hZ2U=');
  });
});

describe('untrained architecture checks', () => {
  it('runs the saved architecture without a checkpoint or promotion receipt', async () => {
    const methods: string[] = [];
    const state = new WorkbenchInferenceState(async method => {
      methods.push(method);
      if (method.endsWith('/test-architecture')) return { inputShape: [1, 8], outputShape: [1, 8, 32], parameterCount: 500, device: 'cpu', untrained: true };
      return { ...loaded(), checkpoints: [] };
    });
    await state.activate(context, true); await state.testArchitecture();
    expect(state.architecture?.untrained).toBe(true); expect(state.canPromote).toBe(false);
    expect(state.checkpoints).toEqual([]); expect(state.prediction).toBeNull();
    expect(methods).toEqual(['workbench/inference/get', 'workbench/inference/test-architecture']);
  });
});

// Catches excess byte allocations, stale file reads, and ignored backend import failures.
describe('inference input boundaries', () => {
  it('accepts numeric schema names that overlap Object prototype properties', async () => {
    const requests: Record<string, unknown>[] = [];
    const state = new WorkbenchInferenceState(async (method, params) => {
      if (method.endsWith('/predict')) { requests.push(params); return { ...prediction, value: 6 }; }
      return { ...loaded('weights'), checkpoints: [{ ...checkpoint(), chatCompatible: false, input: { ...checkpoint().input, mode: 'features', features: ['constructor', '__proto__', 'toString'] } }] };
    });
    await state.activate(context, true);
    expect(() => state.canPredict).not.toThrow();
    expect(state.canPredict).toBe(false);
    for (const key of ['constructor', '__proto__', 'toString']) expect(state.featureValue(key)).toBe('');
    state.setFeature('constructor', '1'); state.setFeature('__proto__', '2'); state.setFeature('toString', '3');
    expect(state.canPredict).toBe(true);
    await state.predict();
    expect(Object.entries(requests[0].features as Record<string, number>)).toEqual([['constructor', 1], ['__proto__', 2], ['toString', 3]]);
    expect(state.prediction?.value).toBe(6);
  });

  it('bounds UTF-8 prompt bytes before requesting inference', async () => {
    let predictions = 0;
    const state = new WorkbenchInferenceState(async method => { if (method.endsWith('/predict')) { predictions++; return prediction; } return loaded('weights'); });
    await state.activate(context, true); state.setPrompt('é'.repeat(4097)); await state.predict();
    expect(predictions).toBe(0); expect(state.error).toContain('8,192');
    state.setPrompt('é'.repeat(4096)); await state.predict(); expect(predictions).toBe(1);
  });
  it('validates images before reading and ignores a file read after changing checkpoints', async () => {
    const reading = deferred<ArrayBuffer>(); const state = new WorkbenchInferenceState(async () => loaded());
    await state.activate(context, true);
    await state.importImage({ name: 'large.png', size: 8 * 1024 * 1024 + 1, arrayBuffer: async () => { throw new Error('must not read'); } });
    expect(state.error).toContain('8 MiB');
    const importing = state.importImage({ name: 'input.png', size: 3, arrayBuffer: () => reading.promise });
    state.selectCheckpoint('next'); reading.resolve(new Uint8Array([1, 2, 3]).buffer); await importing; expect(state.imageBase64).toBe('');
    await state.importImage({ name: 'input.png', size: 3, arrayBuffer: async () => new Uint8Array([1, 2, 3]).buffer });
    expect(state.imageBase64).toBe('AQID'); expect(state.imageName).toBe('input.png');
  });
  it('keeps unavailable engines disabled with actionable reasons and no Auto option', () => {
    const runtimes = inferenceRuntimes([{ id: 'pytorch', name: 'PyTorch', available: true }, { id: 'vllm', name: 'vLLM', available: true }]);
    expect(runtimes.map(r => r.id)).toEqual(['pytorch', 'llama.cpp', 'vllm', 'sglang']);
    expect(runtimes.filter(r => r.available).map(r => r.id)).toEqual(['pytorch']);
    expect(runtimes.filter(r => !r.available).every(r => !!r.reason)).toBe(true);
  });
  it('surfaces import preparation failures without inventing a checkpoint', async () => {
    const state = new WorkbenchInferenceState(async method => { if (method.endsWith('/import')) throw new Error('Save Data preparation before importing weights.'); return loaded(); });
    await state.activate(context, true); await state.importCheckpoint({ name: 'trained.pt', size: 3, arrayBuffer: async () => new Uint8Array([1, 2, 3]).buffer }, 'My checkpoint');
    expect(state.checkpoints.map(c => c.id)).toEqual(['weights', 'next']); expect(state.error).toContain('Save Data preparation');
  });
});
