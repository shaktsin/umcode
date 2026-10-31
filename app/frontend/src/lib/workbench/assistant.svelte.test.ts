import { beforeAll, describe, expect, it, vi } from 'vitest';
import { webcrypto } from 'node:crypto';
import { WorkbenchAssistantState } from './assistant.svelte';
import { graphRevision, type WorkbenchProposal } from './assistant';
import { starterGraph } from './graph';
import { EditorState } from './editor.svelte';

beforeAll(() => vi.stubGlobal('crypto', webcrypto));
function setup() {
  const editor = new EditorState(); editor.sync(JSON.stringify(starterGraph('mlp')));
  let identity = { projectId: 'p', modelId: 'm', modelName: 'Tiny' };
  const read = () => ({ ...identity, text: editor.text });
  const state = new WorkbenchAssistantState(read);
  const commit = vi.fn((graph) => editor.commit(graph));
  const unbind = state.bind(commit);
  return { editor, state, commit, unbind, read, switchTo: (value: typeof identity) => { identity = value; } };
}
async function suggestion(text: string): Promise<WorkbenchProposal> {
  return { projectId: 'p', modelId: 'm', modelName: 'Tiny', revision: await graphRevision(text), explanation: 'Widen the first projection.', operations: [{ type: 'update', nodeId: 'layer_1', config: { outFeatures: 64 } }] };
}
describe('assistant editor bridge', () => {
  it('captures selected context and preview is separate from a single undoable apply', async () => {
    const { state, editor, commit } = setup(), original = editor.text;
    state.selectedNodeId = 'layer_1';
    expect((await state.capture())?.selectedNodeId).toBe('layer_1');
    await state.showProposal(await suggestion(original));
    expect(editor.text).toBe(original); expect(commit).not.toHaveBeenCalled();
    state.selectedNodeId = 'layer_2';
    state.apply(); expect(commit).toHaveBeenCalledTimes(1);
    expect(editor.graph?.nodes[1].config.outFeatures).toBe(64);
    expect(() => state.apply()).toThrow(); expect(commit).toHaveBeenCalledTimes(1);
    editor.undo(); expect(editor.text).toBe(original);
    editor.redo(); expect(editor.graph?.nodes[1].config.outFeatures).toBe(64);
  });
  it('cancel leaves the draft and existing undo history untouched', async () => {
    const { state, editor, commit } = setup();
    editor.commit({ ...editor.graph!, nodes: editor.graph!.nodes.map(n => n.id === 'layer_1' ? { ...n, config: { ...n.config, outFeatures: 48 } } : n) });
    const current = editor.text;
    await state.showProposal(await suggestion(current)); state.cancel();
    expect(editor.text).toBe(current); expect(editor.canUndo).toBe(true); expect(commit).not.toHaveBeenCalled();
  });
  it('rejects stale text at preview and again at Apply', async () => {
    const { state, editor, commit } = setup(), p = await suggestion(editor.text);
    editor.commit({ ...editor.graph!, nodes: editor.graph!.nodes.map(n => ({ ...n, position: { ...n.position, x: 100 } })) });
    await expect(state.showProposal(p)).rejects.toThrow(/changed/i);
    const next = await suggestion(editor.text); await state.showProposal(next);
    editor.commit({ ...editor.graph!, nodes: editor.graph!.nodes.map(n => ({ ...n, position: { ...n.position, x: 110 } })) });
    expect(() => state.apply()).toThrow(/changed/i); expect(commit).not.toHaveBeenCalled();
  });
  it.each([{ projectId: 'other', modelId: 'm', modelName: 'Other' }, { projectId: 'p', modelId: 'other', modelName: 'Other' }])('cannot retarget a proposal after navigation %j', async (identity) => {
    const { state, editor, switchTo, commit } = setup(), p = await suggestion(editor.text);
    await state.showProposal(p); switchTo(identity);
    expect(() => state.apply()).toThrow(); await expect(state.showProposal(p)).rejects.toThrow(); expect(commit).not.toHaveBeenCalled();
  });
  it('unbind invalidates previews and stale cleanups cannot clear a new binding', async () => {
    const { state, editor, unbind, commit } = setup();
    await state.showProposal(await suggestion(editor.text)); unbind();
    expect(state.preview).toBeNull(); expect(state.selectedNodeId).toBeNull();
    await expect(state.showProposal(await suggestion(editor.text))).rejects.toThrow(/Build/i);
    const next = vi.fn(); state.bind(next); unbind();
    await state.showProposal(await suggestion(editor.text)); state.apply(); expect(next).toHaveBeenCalledTimes(1); expect(commit).not.toHaveBeenCalled();
  });
  it('discards a pending preview after cancellation or a text change during hashing', async () => {
    const { editor, read } = setup();
    let release!: (s: string) => void;
    const hash = vi.fn(() => new Promise<string>(resolve => { release = resolve; }));
    const state = new WorkbenchAssistantState(read, hash); state.bind(vi.fn());
    const p = await suggestion(editor.text), pending = state.showProposal(p);
    state.cancel(); release(p.revision); await expect(pending).rejects.toThrow(); expect(state.preview).toBeNull();
    const changed = state.showProposal(p); editor.sync(JSON.stringify(starterGraph('cnn'))); release(p.revision);
    await expect(changed).rejects.toThrow(/changed/i); expect(state.preview).toBeNull();
  });
  it('ask requests focus but makes no model calls or edits', () => {
    const { state, commit } = setup(); state.ask(); expect(state.focusRequest).toBe(1); expect(commit).not.toHaveBeenCalled();
  });
});
