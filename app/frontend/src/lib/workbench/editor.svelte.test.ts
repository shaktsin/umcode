import { describe, expect, it } from 'vitest';
import { EditorState } from './editor.svelte';
import { addLayer, emptyGraph, starterGraph } from './graph';

describe('visual editor history', () => {
  it('undoes and redoes architecture changes without recording identical snapshots', () => {
    const editor = new EditorState();
    editor.sync(JSON.stringify(emptyGraph()));
    const first = addLayer(editor.graph!, 'input', { x: 50, y: 60 });
    editor.commit(first); editor.commit(first);
    expect(editor.graph?.nodes).toHaveLength(1);
    editor.undo(); expect(editor.graph?.nodes).toHaveLength(0);
    editor.undo(); expect(editor.graph?.nodes).toHaveLength(0);
    editor.redo(); expect(editor.graph?.nodes).toHaveLength(1);
    editor.undo(); editor.commit(addLayer(editor.graph!, 'linear', { x: 5, y: 9 }));
    expect(editor.canRedo).toBe(false);
  });

  it('retains history for its own text echoes but resets on external reload', () => {
    const editor = new EditorState();
    editor.sync(JSON.stringify(emptyGraph()));
    editor.commit(starterGraph('text'));
    editor.sync(editor.text);
    expect(editor.canUndo).toBe(true);
    editor.sync(JSON.stringify(starterGraph('classifier')));
    expect(editor.canUndo).toBe(false);
    expect(editor.graph?.nodes.some(n => n.kind === 'meanpool')).toBe(true);
  });

  it('keeps unsupported documents read-only instead of replacing their content', () => {
    const editor = new EditorState();
    const original = '{"version":1,"nodes":[{"id":"custom-python"}],"edges":[]}';
    editor.sync(original);
    expect(editor.graph).toBeNull(); expect(editor.text).toBe(original);
    expect(editor.error).toContain('saved model is unchanged');
  });
});
