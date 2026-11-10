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
  it('keeps a replacement model and its draft when an older deletion finishes late', async () => {
    const pending = deferred<{ ok: boolean; trashPath: string }>();
    const old = model('one'), replacement = { ...model('one'), createdAt: '2026-10-31T01:00:00Z' };
    let disk: WorkbenchModel | null = old;
    const state = new WorkbenchState(async (method, params: any) => {
      if (method === 'workbench/model/delete') { disk = null; return pending.promise; }
      if (method === 'workbench/model/list') return { models: disk ? [disk] : [] };
      if (method === 'workbench/model/create') { disk = replacement; return disk; }
      if (method === 'workbench/model/open') return { ...bundle(params.modelId), model: disk };
      return { ok: true };
    }, memoryStorage());
    await state.activate('a', true); await state.open('one');
    const removing = state.deleteModel('one');
    await state.activate('b', true); await state.activate('a', true);
    await state.create('one', 'custom'); state.draft = '{"newDraft":true}';
    pending.resolve({ ok: true, trashPath: 'models/.trash/old-one' }); await removing;
    expect(state.models).toHaveLength(1); expect(state.selected?.model.createdAt).toBe(replacement.createdAt);
    expect(state.draft).toBe('{"newDraft":true}');
  });

  it('does not reuse unsaved graph edits after a model ID is recreated externally', async () => {
    let current = model('one');
    const state = new WorkbenchState(async (method, params: any) => {
      if (method === 'workbench/model/list') return { models: [current] };
      if (method === 'workbench/model/open') return { ...bundle(params.modelId), model: current };
      return { ok: true };
    });
    await state.activate('a', true); await state.open('one'); state.draft = '{"oldDraft":true}';
    current = { ...current, createdAt: '2026-10-31T01:00:00Z' };
    await state.refresh();
    expect(state.dirty).toBe(false); expect(state.draft).not.toContain('oldDraft');
  });

  it('deletes only the requested model and its saved selection and draft', async () => {
    const storage = memoryStorage();
    const state = new WorkbenchState(async (method, params: any) => {
      if (method === 'workbench/model/delete') {
        expect(params).toEqual({ projectId: 'a', modelId: 'one', modelCreatedAt: '2026-10-30T00:00:00Z' });
        return { ok: true, trashPath: 'models/.trash/one-unique' };
      }
      return basicCall(method, params);
    }, storage);
    await state.activate('a', true); await state.open('one');
    state.draft = '{"unsaved":true}';
    expect(await state.deleteModel('one')).toBe(true);
    expect(state.models.map(model => model.id)).toEqual(['two']);
    expect(state.selected).toBeNull();
    expect(state.hasModelDraft('one')).toBe(false);
    expect(storage.getItem('umcode.workbench.model.a')).toBeNull();
    expect(state.notice).toContain('models/.trash/one-unique');
  });

  it('keeps the model and its unsaved draft when deletion fails', async () => {
    const state = new WorkbenchState((method, params) => {
      if (method === 'workbench/model/delete') throw new Error('Cannot move to Trash');
      return basicCall(method, params);
    });
    await state.activate('a', true); await state.open('one'); state.draft = '{"unsaved":true}';
    expect(await state.deleteModel('one')).toBe(false);
    expect(state.selected?.model.id).toBe('one'); expect(state.draft).toBe('{"unsaved":true}');
    expect(state.models).toHaveLength(2); expect(state.error).toBe('Cannot move to Trash');
  });

  it('finishes a deletion in its original project after navigation', async () => {
    const pending = deferred<{ ok: boolean; trashPath: string }>();
    const storage = memoryStorage();
    const state = new WorkbenchState((method, params) => method === 'workbench/model/delete' ? pending.promise : basicCall(method, params), storage);
    await state.activate('a', true); await state.open('one'); state.draft = '{"unsaved":true}';
    const deleting = state.deleteModel('one');
    await state.activate('b', true); await state.open('one'); const otherDraft = state.draft;
    pending.resolve({ ok: true, trashPath: 'models/.trash/one-unique' });
    expect(await deleting).toBe(true);
    expect(state.projectId).toBe('b'); expect(state.selected?.model.id).toBe('one'); expect(state.draft).toBe(otherDraft);
    expect(storage.getItem('umcode.workbench.model.b')).toBe('one');
    expect(storage.getItem('umcode.workbench.model.a')).toBeNull();
    expect(state.notice).toBe('');
  });

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

it('adopts an imported saved model only in its original project and keeps existing drafts', async()=>{
 const state=new WorkbenchState(basicCall,memoryStorage());await state.activate('a',true);await state.open('one');state.draft='{"existingDraft":true}';
 const imported={...bundle('imported'),graph:{version:1,format:'umcode.imported.v1',nodes:[],edges:[]}};
 expect(state.acceptImport('a',imported)).toBe(true);expect(state.selected?.model.id).toBe('imported');expect(state.tab).toBe('Build');expect(state.dirty).toBe(false);expect(state.hasModelDraft('one')).toBe(true);
 await state.activate('b',true);expect(state.acceptImport('a',imported)).toBe(false);expect(state.projectId).toBe('b');
});
