import { describe, expect, it } from 'vitest';
import { WorkbenchState } from './state.svelte';
import type { OpenWorkbenchModel, WorkbenchModel } from './types';

const model = (id: string): WorkbenchModel => ({ id, name: id, task: 'custom', schemaVersion: 1, createdAt: '2026-10-30T00:00:00Z' });
const bundle = (id: string): OpenWorkbenchModel => ({ model: model(id), graph: { version: 1, nodes: [], edges: [] } });
function deferred<T>() {
  let resolve!: (v: T) => void;
  let reject!: (e: Error) => void;
  const promise = new Promise<T>((yes, no) => { resolve = yes; reject = no; });
  return { promise, resolve, reject };
}
function memoryStorage() {
  const data = new Map<string, string>();
  return { getItem: (k: string) => data.get(k) ?? null, setItem: (k: string, v: string) => { data.set(k, v); }, removeItem: (k: string) => { data.delete(k); } };
}
const basicCall = async (method: string, params: any) => {
  if (method === 'workbench/model/list') return { models: [model('one'), model('two')] };
  if (method === 'workbench/model/open') return bundle(params.modelId);
  if (method === 'workbench/model/create') return model(params.name);
  return { ok: true };
};

describe('workbench state', () => {
  it('restores a different last model for each project across a fresh instance', async () => {
    const storage = memoryStorage();
    const first = new WorkbenchState(basicCall, storage);
    await first.activate('a', true);
    await first.open('two');
    await first.activate('b', true);
    await first.open('one');
    const fresh = new WorkbenchState(basicCall, storage);
    await fresh.activate('a', true);
    expect(fresh.selected?.model.id).toBe('two');
    await fresh.activate('b', true);
    expect(fresh.selected?.model.id).toBe('one');
  });

  it('ignores a late open from a previous project and keeps new requests busy', async () => {
    const old = deferred<OpenWorkbenchModel>();
    const next = deferred<OpenWorkbenchModel>();
    const state = new WorkbenchState(async (m, p: any) => m === 'workbench/model/open' ? (p.projectId === 'a' ? old.promise : next.promise) : basicCall(m, p), memoryStorage());
    await state.activate('a', true);
    const a = state.open('one');
    await state.activate('b', true);
    const b = state.open('two');
    old.resolve(bundle('one')); await a;
    expect(state.selected).toBeNull();
    expect(state.busy).toBe(true);
    next.resolve(bundle('two')); await b;
    expect(state.selected?.model.id).toBe('two');
    expect(state.error).toBe('');
  });

  it('ignores a stale failure when another model is already open', async () => {
    const late = deferred<OpenWorkbenchModel>();
    const state = new WorkbenchState((m, p: any) => m === 'workbench/model/open' && p.modelId === 'one' ? late.promise : basicCall(m, p), memoryStorage());
    await state.activate('a', true);
    const first = state.open('one');
    await state.open('two');
    late.reject(new Error('stale error')); await first;
    expect(state.selected?.model.id).toBe('two');
    expect(state.error).toBe('');
  });

  it('retains each unsaved draft across model/project navigation and reconnect', async () => {
    const state = new WorkbenchState(basicCall, memoryStorage());
    await state.activate('a', true); await state.open('one');
    state.draft = '{"version":1,"nodes":[{"id":"draft"}],"edges":[]}';
    const draft = state.draft;
    await state.open('two'); await state.open('one');
    expect(state.draft).toBe(draft);
    await state.activate('b', true); await state.activate('a', true);
    expect(state.draft).toBe(draft);
    await state.activate('a', false); await state.activate('a', true);
    expect(state.draft).toBe(draft);
    expect(state.dirty).toBe(true);
  });

  it('acknowledges only the submitted draft when the user edits during a save', async () => {
    const pending = deferred<{ ok: boolean }>();
    let disk: unknown;
    const state = new WorkbenchState(async (m, p: any) => {
      if (m === 'workbench/graph/save') { disk = JSON.parse(p.graphJson); return pending.promise; }
      return basicCall(m, p);
    }, memoryStorage());
    await state.activate('a', true); await state.open('one');
    const submitted = '{"version":1,"nodes":[{"id":"first"}],"edges":[]}';
    state.draft = submitted;
    const save = state.save();
    state.draft = '{"version":1,"nodes":[{"id":"second"}],"edges":[]}';
    pending.resolve({ ok: true }); await save;
    expect(disk).toEqual({ version: 1, nodes: [{ id: 'first' }], edges: [] });
    expect(state.draft).toContain('second');
    expect(state.savedDraft).toBe(submitted);
    expect(state.dirty).toBe(true);
  });

  it('keeps drafts after save/reload failures and rejects invalid JSON before sending', async () => {
    let fail = false;
    let saves = 0;
    const state = new WorkbenchState(async (m, p) => {
      if (m === 'workbench/graph/save') { saves++; throw new Error('disk unavailable'); }
      if (fail && m === 'workbench/model/open') throw new Error('reload unavailable');
      return basicCall(m, p);
    }, memoryStorage());
    await state.activate('a', true); await state.open('one');
    state.draft = 'broken'; await state.save();
    expect(saves).toBe(0); expect(state.error).toContain('JSON');
    state.draft = '{"version":1,"nodes":[],"edges":[],"value":2}'; await state.save();
    expect(state.dirty).toBe(true); expect(state.error).toBe('disk unavailable');
    fail = true; await state.reload();
    expect(state.draft).toContain('"value":2'); expect(state.error).toBe('reload unavailable');
  });

  it('preserves exact JSON numbers when opening and saving through RPC', async () => {
    const graphJson = '{"version":1,"nodes":[],"edges":[],"seed":9007199254740993,"weight":1.0000000000000000001}';
    let submitted: any;
    const state = new WorkbenchState(async (m, p) => {
      if (m === 'workbench/model/open') return { ...bundle('one'), graphJson };
      if (m === 'workbench/graph/save') { submitted = p; return { ok: true }; }
      return basicCall(m, p);
    });
    await state.activate('a', true); await state.open('one');
    expect(state.draft).toBe(graphJson);
    state.draft = graphJson + '\n'; await state.save();
    expect(submitted.graphJson).toBe(graphJson + '\n');
    expect(submitted.graph).toBeUndefined();
  });

  it('retains edits typed after a reload was confirmed and started', async () => {
    const pending = deferred<OpenWorkbenchModel>();
    let reloading = false;
    const state = new WorkbenchState((m, p) => reloading && m === 'workbench/model/open' ? pending.promise : basicCall(m, p));
    await state.activate('a', true); await state.open('one');
    state.draft = '{"version":1,"nodes":[],"edges":[],"old":true}';
    reloading = true; const reload = state.reload();
    const newer = '{"version":1,"nodes":[],"edges":[],"new":true}';
    state.draft = newer;
    pending.resolve(bundle('one')); await reload;
    expect(state.draft).toBe(newer);
    expect(state.dirty).toBe(true);
  });

  it('falls back to New model when a remembered model was deleted', async () => {
    const storage = memoryStorage();
    const before = new WorkbenchState(basicCall, storage);
    await before.activate('a', true); await before.open('two');
    const after = new WorkbenchState(async () => ({ models: [] }), storage);
    await after.activate('a', true);
    expect(after.selected).toBeNull(); expect(after.loading).toBe(false); expect(after.models).toEqual([]);
  });

  it('works when browser storage is blocked and shows create errors without losing selection', async () => {
    const blocked = { getItem() { throw new Error('blocked'); }, setItem() { throw new Error('blocked'); }, removeItem() { throw new Error('blocked'); } };
    const state = new WorkbenchState(async (m, p) => {
      if (m === 'workbench/model/create') throw new Error('model already exists');
      return basicCall(m, p);
    }, blocked);
    await state.activate('a', true); await state.open('one');
    await state.create('one', 'custom');
    expect(state.error).toBe('model already exists'); expect(state.selected?.model.id).toBe('one');
    expect(state.busy).toBe(false);
  });
});
