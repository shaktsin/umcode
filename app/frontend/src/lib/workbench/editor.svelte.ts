import { readGraph, type Graph } from './graph';

/** History contains only persisted architecture, never canvas measurements or selection. */
export class EditorState {
  graph = $state<Graph | null>(null);
  text = $state('');
  error = $state('');
  private past = $state<string[]>([]);
  private future = $state<string[]>([]);
  get canUndo() { return this.past.length > 0; }
  get canRedo() { return this.future.length > 0; }

  private load(text: string) {
    this.text = text;
    try { this.graph = readGraph(text); this.error = ''; }
    catch (e) { this.graph = null; this.error = (e as Error).message; }
  }
  sync(text: string) {
    if (text === this.text) return;
    this.past = []; this.future = []; this.load(text);
  }
  commit(graph: Graph) {
    const next = JSON.stringify(graph);
    if (this.graph && next === JSON.stringify(this.graph)) return;
    this.past = [...this.past.slice(-99), this.text]; this.future = [];
    this.load(next);
  }
  undo() {
    const previous = this.past.at(-1);
    if (previous === undefined) return;
    this.future = [...this.future, this.text]; this.past = this.past.slice(0, -1); this.load(previous);
  }
  redo() {
    const next = this.future.at(-1);
    if (next === undefined) return;
    this.past = [...this.past, this.text]; this.future = this.future.slice(0, -1); this.load(next);
  }
}
