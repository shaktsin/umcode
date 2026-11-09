import { describe, expect, it } from 'vitest';
import { ModelImportState, ImportedEditorState } from './modelImport.svelte';
import type { ImportInspection, ImportedGraph } from './modelImportTypes';
function graph(): ImportedGraph { const baseline = { id: 'block', kind: 'transformer', config: { hidden_size: 4 }, tensorNames: [], sourceKey: 'block' }; return { version: 1, format: 'umcode.imported.v1', source: { family: 'gpt2', config: { model_type: 'gpt2' }, files: [], tensors: [], weightsVerified: false, warnings: [], baselineNodes: [baseline] }, nodes: [{ ...baseline, config: { ...baseline.config }, tensorNames: [...baseline.tensorNames], label: 'Block', position: { x: 0, y: 0 }, baselineConfig: { hidden_size: 4 } }], edges: [] }; }
function deferred<T>() { let resolve!: (value: T) => void; const promise = new Promise<T>(yes => resolve = yes); return { promise, resolve }; }
const inspection = (): ImportInspection => ({ family: 'gpt2', name: 'tiny', supported: true, weightsVerified: false, tensorCount: 0, parameterCount: 0, totalWeightBytes: 0, warnings: [], graph: graph() });
const config = { name: 'config.json', size: 2, kind: 'config' as const, content: '{}' };
describe('model import request lifetime', () => {
  it('ignores stale inspections after project change, replacement selection and disconnect', async () => {
    const pending = deferred<ImportInspection>(); const state = new ModelImportState(async () => pending.promise);
    state.activate('old', true); const work = state.inspect([config]); state.activate('new', true); pending.resolve(inspection()); await work;
    expect(state.inspection).toBeNull(); expect(state.busy).toBe(false);
    const next = deferred<ImportInspection>(); const replace = new ModelImportState(async () => next.promise); replace.activate('p', true); const old = replace.inspect([config]); replace.reset(); next.resolve(inspection()); await old; expect(replace.inspection).toBeNull();
    const off = deferred<ImportInspection>(); const disconnect = new ModelImportState(async () => off.promise); disconnect.activate('p', true); const read = disconnect.inspect([config]); disconnect.activate('p', false); off.resolve(inspection()); await read; expect(disconnect.inspection).toBeNull();
  });
  it('never publishes creates after disposal and refuses unsupported previews', async () => {
    const created = deferred<unknown>(); let createCount = 0;
    const state = new ModelImportState(async method => { if (method.endsWith('/inspect')) return inspection(); createCount++; return created.promise; }); state.activate('p', true); await state.inspect([config]);
    const creating = state.create('copy'); state.dispose(); created.resolve({ model: { id: 'late' }, graph: graph() }); expect(await creating).toBeNull();
    const unknown = new ModelImportState(async () => ({ ...inspection(), supported: false })); unknown.activate('p', true); await unknown.inspect([config]); expect(await unknown.create('bad')).toBeNull(); expect(createCount).toBe(1);
  });
  it('sends the metadata-only RPC contract and returns the newly saved model', async () => {
    const state = new ModelImportState(async (method, params) => { expect(params.projectId).toBe('p'); if (method.endsWith('/inspect')) { expect(params.files).toEqual([config]); return inspection(); } expect(method).toBe('workbench/import/create'); expect(params.name).toBe('Copy'); expect(JSON.parse(params.graphJson as string).format).toBe('umcode.imported.v1'); return { model: { id: 'new' }, graph: graph() }; });
    state.activate('p', true); await state.inspect([config]); expect((await state.create(' Copy '))?.model.id).toBe('new');
  });
});
describe('imported editor history and provenance', () => {
  it('protects read-only groups against keyboard deletion and config edits', () => {
    const g=graph(); g.nodes[0].config.readOnly=true;
    const editor=new ImportedEditorState(); editor.sync(JSON.stringify(g));
    expect(()=>editor.commit({...g,nodes:[]})).toThrow(/read.only/i);
    expect(()=>editor.commit({...g,nodes:[{...g.nodes[0],config:{hidden_size:8}}]})).toThrow(/read.only/i);
    expect(editor.canUndo).toBe(false);
  });
  it('undoes/redoes and resets history when reloading saved text', () => {
    const editor = new ImportedEditorState(); const original = JSON.stringify(graph()); editor.sync(original);
    const next = graph(); next.nodes[0].config.hidden_size = 8; editor.commit(next); expect(editor.graph?.nodes[0].config.hidden_size).toBe(8); expect(editor.canUndo).toBe(true);
    editor.undo(); expect(editor.graph?.nodes[0].config.hidden_size).toBe(4); editor.redo(); expect(editor.graph?.nodes[0].config.hidden_size).toBe(8);
    editor.sync(original); expect(editor.canUndo).toBe(false); expect(editor.graph?.nodes[0].config.hidden_size).toBe(4);
  });
  it('rejects source and original baseline changes while permitting copy edits', () => {
    const editor = new ImportedEditorState(); editor.sync(JSON.stringify(graph())); const sourceChange = graph(); sourceChange.source.config.model_type = 'fake'; expect(() => editor.commit(sourceChange)).toThrow(/source/i);
    const baselineChange = graph(); baselineChange.nodes[0].baselineConfig.hidden_size = 99; expect(() => editor.commit(baselineChange)).toThrow(/baseline/i);
    const edit = graph(); edit.nodes[0].config.hidden_size = 8; editor.commit(edit); expect(editor.graph?.source.config.model_type).toBe('gpt2');
  });
  it('shares a large immutable source through undo, redo and save without retaining it in history', () => {
    const original = graph();
    original.source.tensors = Array.from({ length: 100_000 }, (_, i) => ({ name: `layers.${i}.weight`, dtype: 'F32', shape: [4, 4], file: 'model.safetensors', bytes: 64 }));
    const editor = new ImportedEditorState(); editor.sync(JSON.stringify(original));
    const source = editor.graph!.source;
    for (let i = 1; i <= 12; i++) editor.commit({ ...editor.graph!, nodes: editor.graph!.nodes.map(n => ({ ...n, position: { x: i, y: 0 } })) });
    const history = editor as unknown as { past: string[]; future: string[] };
    expect([...history.past, ...history.future].reduce((bytes, item) => bytes + item.length * 2, 0)).toBeLessThan(100_000);
    expect(editor.graph!.source).toBe(source);
    editor.undo(); expect(editor.graph!.nodes[0].position.x).toBe(11); expect(editor.graph!.source).toBe(source);
    editor.redo(); expect(editor.graph!.nodes[0].position.x).toBe(12); expect(editor.graph!.source).toBe(source);
    expect(JSON.parse(editor.text).source.tensors).toHaveLength(100_000);
    const reopened = new ImportedEditorState(); reopened.sync(editor.text); expect(reopened.graph!.source.tensors).toHaveLength(100_000); expect(reopened.graph!.nodes[0].position.x).toBe(12);
  });
  it('bounds mutable history memory and preserves the newest available undo/redo states', () => {
    const editor = new ImportedEditorState(); editor.sync(JSON.stringify(graph()));
    const padding = 'x'.repeat(600_000);
    for (let i = 1; i <= 12; i++) editor.commit({ ...editor.graph!, nodes: editor.graph!.nodes.map(n => ({ ...n, label: padding, position: { x: i, y: 0 } })) });
    const history = editor as unknown as { past: string[]; future: string[] };
    expect([...history.past, ...history.future].reduce((bytes, item) => bytes + item.length * 2, 0)).toBeLessThanOrEqual(8 * 1024 * 1024);
    editor.undo(); expect(editor.graph!.nodes[0].position.x).toBe(11);
    editor.redo(); expect(editor.graph!.nodes[0].position.x).toBe(12);
  });
  it('freezes source keys and immutable source content while allowing architecture edits', () => {
    const editor = new ImportedEditorState(); editor.sync(JSON.stringify(graph()));
    const changed = { ...editor.graph!, nodes: editor.graph!.nodes.map(n => ({ ...n, sourceKey: 'invented.binding' })) };
    expect(() => editor.commit(changed)).toThrow(/source.*key|binding/i);
    expect(() => { editor.graph!.source.config.model_type = 'fake'; }).toThrow();
    expect(editor.graph!.source.config.model_type).toBe('gpt2');
  });
});
