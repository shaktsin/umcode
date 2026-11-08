import { describe, expect, it } from 'vitest';
import { WorkbenchDataState } from './data.svelte';
import type { DataConfig, DataSelection, DataState, Dataset } from './dataTypes';

const dataset = (id: string): Dataset => ({ id, name: id, source: { kind: 'local', name: `${id}.csv` }, format: 'table', rowCount: 4, byteCount: 32, columns: [{ name: 'x', kind: 'number', missing: 0, uniqueCount: 4, examples: ['1'] }, { name: 'label', kind: 'text', missing: 0, uniqueCount: 2, examples: ['a', 'b'] }], preview: [{ x: '1', label: 'a' }], createdAt: '2026-10-31T00:00:00Z' });
const loaded = (id: string, selection: DataSelection | null = null): DataState => ({ datasets: [dataset(id)], dataset: dataset(id), selection });
function deferred<T>() { let resolve!: (value: T) => void, reject!: (e: Error) => void; const promise = new Promise<T>((yes, no) => { resolve = yes; reject = no; }); return { promise, resolve, reject }; }
const selection = (config: DataConfig): DataSelection => ({ version: 'prepared-one', datasetId: config.datasetId, config, counts: { train: 2, validation: 1, test: 1 }, classes: ['a', 'b'], statistics: [], createdAt: '2026-10-31T00:00:00Z', warnings: [] });

describe('data state context guards and persistence', () => {
  it('isolates Data drafts when a deleted model ID is reused', async () => {
    const cache = new Map();
    const old = new WorkbenchDataState(async () => loaded('data'), () => '', cache, () => 'old-created-at');
    await old.activate('p', 'm', 'classification', true); old.updateConfig({ seed: 987 }); old.dispose();
    const replacement = new WorkbenchDataState(async () => loaded('data'), () => '', cache, () => 'new-created-at');
    await replacement.activate('p', 'm', 'classification', true);
    expect(replacement.config?.seed).not.toBe(987);
  });

  it('does not show an imported preview until import succeeds and refreshes real metadata', async () => {
    const importing = deferred<Dataset>();
    const state = new WorkbenchDataState(async (method, params: any) => {
      if (method === 'workbench/data/import') { expect(params.projectId).toBe('p'); expect(params.modelId).toBe('m'); return importing.promise; }
      if (method === 'workbench/data/get') return params.datasetId ? loaded('actual') : { datasets: [], dataset: null, selection: null };
      throw new Error('not prepared');
    });
    await state.activate('p', 'm', 'classification', true); state.changeSource('local');
    const importingNow = state.importData({ kind: 'local', name: 'actual.csv' }, [{ name: 'actual.csv', contentBase64: 'eCx5CjEsYQ==' }]);
    expect(state.dataset).toBeNull(); expect(state.stage).toContain('Importing');
    importing.resolve(dataset('actual')); await importingNow;
    expect(state.dataset?.preview[0].x).toBe('1'); expect(state.selection).toBeNull(); expect(state.notice).toContain('imported');
    await state.prepare(); expect(state.selection).toBeNull();
  });
  it('ignores a late import after switching source while leaving the new stage alone', async () => {
    const old = deferred<Dataset>(); const current = deferred<unknown>();
    const state = new WorkbenchDataState(async (method) => method === 'workbench/data/import' ? old.promise : method === 'workbench/data/huggingface' ? current.promise : { datasets: [], dataset: null, selection: null });
    await state.activate('p', 'm', 'classification', true);
    const importing = state.importData({ kind: 'example', name: 'Old', repo: 'owner/data', revision: 'abc', files: ['a.csv'] });
    state.changeSource('huggingface'); const search = state.search('new');
    old.resolve(dataset('old')); await importing;
    expect(state.dataset).toBeNull(); expect(state.stage).toContain('Searching');
    current.resolve({ datasets: [{ id: 'owner/new', downloads: 5, likes: 1, private: false, gated: false }] }); await search;
    expect(state.results[0].id).toBe('owner/new'); expect(state.stage).toBe('');
  });
  it('ignores stale model, project and disconnected completions', async () => {
    const old = deferred<DataState>();
    const state = new WorkbenchDataState(async (method, params: any) => method === 'workbench/data/get' && params.modelId === 'old' ? old.promise : loaded(params.modelId));
    const first = state.activate('one', 'old', 'classification', true);
    await state.activate('two', 'new', 'classification', true); old.resolve(loaded('old')); await first;
    expect(state.dataset?.id).toBe('new');
    const saving = deferred<DataSelection>();
    const saveState = new WorkbenchDataState(async (method) => method === 'workbench/data/prepare' ? saving.promise : loaded('new'));
    await saveState.activate('two', 'new', 'classification', true); const save = saveState.prepare();
    await saveState.activate('two', 'new', 'classification', false);
    saving.resolve(selection(saveState.config!)); await save;
    expect(saveState.selection).toBeNull(); expect(saveState.stage).toBe('');
  });
  it('restores saved preparation and keeps later mapping edits dirty after a pending save', async () => {
    const save = deferred<DataSelection>(); let savedConfig!: DataConfig;
    const state = new WorkbenchDataState(async (method, params: any) => {
      if (method === 'workbench/data/prepare') { savedConfig = JSON.parse(JSON.stringify(params.config)); return save.promise; }
      return loaded('data');
    });
    await state.activate('p', 'm', 'classification', true); const pending = state.prepare(); state.updateConfig({ seed: 17 });
    save.resolve(selection(savedConfig)); await pending;
    expect(state.selection?.version).toBe('prepared-one'); expect(state.config?.seed).toBe(17); expect(state.dirty).toBe(true);
    const fresh = new WorkbenchDataState(async () => loaded('data', selection(savedConfig)));
    await fresh.activate('p', 'm', 'classification', true);
    expect(fresh.selection?.version).toBe('prepared-one'); expect(fresh.dirty).toBe(false);
  });
  it('preserves imported data after errors and rejects preparation while disconnected', async () => {
    const state = new WorkbenchDataState(async (method) => { if (method === 'workbench/data/import') throw new Error('Invalid CSV'); return loaded('kept'); });
    await state.activate('p', 'm', 'classification', true);
    await state.importData({ kind: 'local', name: 'bad.csv' }, [{ name: 'bad.csv', contentBase64: 'eA==' }]);
    expect(state.dataset?.id).toBe('kept'); expect(state.error).toBe('Invalid CSV');
    await state.activate('p', 'm', 'classification', false); await state.prepare(); expect(state.error).toContain('disconnected');
  });
});

describe('data request lifetimes', () => {
  it('preserves drafts during disposal after the parent clears its selected model', async () => {
    let selected: { createdAt: string } | null = { createdAt: 'original' };
    const cache = new Map();
    const state = new WorkbenchDataState(async () => loaded('data'), () => '', cache, () => selected!.createdAt);
    await state.activate('p', 'm', 'classification', true);
    state.updateConfig({ seed: 987 });
    selected = null;
    expect(() => state.dispose()).not.toThrow();
    const reopened = new WorkbenchDataState(async () => loaded('data'), () => '', cache, () => 'original');
    await reopened.activate('p', 'm', 'classification', true);
    expect(reopened.config?.seed).toBe(987);
  });

  it('keeps the old draft under its original incarnation when the parent selects a replacement', async () => {
    let createdAt = 'original';
    const cache = new Map();
    const state = new WorkbenchDataState(async () => loaded('data'), () => '', cache, () => createdAt);
    await state.activate('p', 'm', 'classification', true);
    state.updateConfig({ seed: 987 });
    createdAt = 'replacement';
    await state.activate('p', 'm', 'classification', true);
    expect(state.config?.seed).toBe(42);
    createdAt = 'original';
    await state.activate('p', 'm', 'classification', true);
    expect(state.config?.seed).toBe(987);
  });

  it('keeps unsaved mappings through refresh and reconnect', async () => {
    const state = new WorkbenchDataState(async () => loaded('data'));
    await state.activate('p', 'm', 'classification', true);
    state.updateConfig({ seed: 123, scale: 'standard' });
    await state.load();
    expect(state.config?.seed).toBe(123); expect(state.config?.scale).toBe('standard');
    await state.activate('p', 'm', 'classification', false);
    await state.activate('p', 'm', 'classification', true);
    expect(state.config?.seed).toBe(123); expect(state.dirty).toBe(true);
  });
  it('preserves an edited dataset after a source switch and isolates model draft caches', async () => {
    const cache = new Map();
    const state = new WorkbenchDataState(async () => loaded('data'), () => '', cache);
    await state.activate('p', 'm', 'classification', true);
    state.updateConfig({ seed: 123 });
    state.changeSource('local'); expect(state.dataset).toBeNull();
    await state.selectDataset('data'); expect(state.config?.seed).toBe(123);
    state.dispose();
    const reopened = new WorkbenchDataState(async () => loaded('data'), () => '', cache);
    await reopened.activate('p', 'm', 'classification', true);
    expect(reopened.config?.seed).toBe(123);
    await reopened.activate('p', 'other-model', 'classification', true);
    expect(reopened.config?.seed).toBe(42);
    await reopened.activate('p', 'm', 'classification', true);
    expect(reopened.config?.seed).toBe(123);
  });
  it('keeps edits made while a data refresh is pending', async () => {
    const refreshing = deferred<DataState>(); let calls = 0;
    const state = new WorkbenchDataState(async () => ++calls === 1 ? loaded('data') : refreshing.promise);
    await state.activate('p', 'm', 'classification', true);
    state.updateConfig({ seed: 17 }); const pending = state.load(); state.updateConfig({ seed: 19 });
    refreshing.resolve(loaded('data')); await pending;
    expect(state.config?.seed).toBe(19);
  });
  it('does not apply a response after disposal and cannot overwrite a reconnected model', async () => {
    const first = deferred<DataState>(); let reads = 0;
    const state = new WorkbenchDataState(async () => ++reads === 1 ? first.promise : loaded('after-reconnect'));
    const pending = state.activate('p', 'm', 'classification', true);
    await state.activate('p', 'm', 'classification', false); await state.activate('p', 'm', 'classification', true);
    first.resolve(loaded('before-reconnect')); await pending;
    expect(state.dataset?.id).toBe('after-reconnect');
    const late = deferred<DataState>(); const disposed = new WorkbenchDataState(async () => late.promise);
    const opening = disposed.activate('p', 'm', 'classification', true); disposed.dispose(); late.resolve(loaded('late')); await opening;
    expect(disposed.dataset).toBeNull();
  });
  it('imports only selected supported HF files pinned to the inspected commit', async () => {
    let source: unknown;
    const state = new WorkbenchDataState(async (method, params: any, timeout) => {
      expect(timeout).toBe(120000);
      if (method === 'workbench/data/huggingface') return { id: 'owner/repo', sha: 'exact-sha', private: false, gated: false, license: 'mit', files: [{ name: 'data.csv', size: 3, supported: true }, { name: 'data.parquet', size: 4, supported: false }] };
      if (method === 'workbench/data/import') { source = params.source; return dataset('hf'); }
      return params.datasetId ? loaded('hf') : { datasets: [], dataset: null, selection: null };
    });
    await state.activate('p', 'm', 'classification', true); state.changeSource('huggingface'); await state.inspect('owner/repo');
    state.toggleFile('data.csv', true); state.toggleFile('data.parquet', true); await state.importRepository();
    expect(source).toEqual({ kind: 'huggingface', name: 'owner/repo', repo: 'owner/repo', revision: 'exact-sha', files: ['data.csv'] });
    expect(state.dataset?.id).toBe('hf');
  });
});
