import { importNodeLocked, parseImportedGraph, sameImportValue } from './modelImport';
import type { ImportFile, ImportInspection, ImportedGraph } from './modelImportTypes';
import type { OpenWorkbenchModel } from './types';
type Call = (method: string, params: Record<string, unknown>, timeout?: number) => Promise<unknown>;
export class ModelImportState {
  inspection = $state<ImportInspection | null>(null);
  busy = $state(false);
  error = $state('');
  stage = $state('');
  private projectId = '';
  private connected = false;
  private generation = 0;
  private disposed = false;
  constructor(private call: Call) {}
  activate(projectId: string, connected: boolean) { if (this.projectId === projectId && this.connected === connected) return; this.projectId = projectId; this.connected = connected; this.reset(); }
  reset() { this.generation++; this.busy = false; this.stage = ''; this.error = ''; this.inspection = null; }
  dispose() { this.disposed = true; this.reset(); }
  private valid(ticket: number) { return !this.disposed && this.connected && this.generation === ticket; }
  async inspect(files: ImportFile[]) {
    this.reset(); if (!this.connected || this.disposed || !this.projectId) { this.error = 'Connect to the engine before inspecting a model.'; return; }
    const ticket = this.generation; this.busy = true; this.stage = 'Inspecting architecture and weight metadata…';
    try { const value = await this.call('workbench/import/inspect', { projectId: this.projectId, files }, 120000) as ImportInspection; if (this.valid(ticket)) this.inspection = value; }
    catch (e) { if (this.valid(ticket)) this.error = e instanceof Error ? e.message : 'Model inspection failed.'; }
    finally { if (this.valid(ticket)) { this.busy = false; this.stage = ''; } }
  }
  async create(name: string): Promise<OpenWorkbenchModel | null> {
    if (!this.connected || this.disposed || this.busy || !this.inspection?.supported || !name.trim()) return null;
    const ticket = ++this.generation; this.busy = true; this.error = ''; this.stage = 'Saving editable architecture copy…';
    try { const model = await this.call('workbench/import/create', { projectId: this.projectId, name: name.trim(), graphJson: JSON.stringify(this.inspection.graph) }, 120000) as OpenWorkbenchModel; return this.valid(ticket) ? model : null; }
    catch (e) { if (this.valid(ticket)) this.error = e instanceof Error ? e.message : 'Creating the model failed.'; return null; }
    finally { if (this.valid(ticket)) { this.busy = false; this.stage = ''; } }
  }
}
export class ImportedEditorState {
  // Source manifests can contain 100k tensors. Keep one frozen source object;
  // history records only editable nodes/edges, never full saved graph strings.
  graph = $state.raw<ImportedGraph | null>(null);
  text = $state('');
  error = $state('');
  private past = $state<string[]>([]);
  private future = $state<string[]>([]);
  private mutableText = '';
  private source: ImportedGraph['source'] | null = null;
  get canUndo() { return this.past.length > 0; }
  get canRedo() { return this.future.length > 0; }
  private snapshot(graph: ImportedGraph) { return JSON.stringify({ nodes: graph.nodes, edges: graph.edges }); }
  private load(text: string) {
    this.text = text;
    try {
      this.graph = freezeImportValue(parseImportedGraph(text));
      this.source = this.graph.source; this.mutableText = this.snapshot(this.graph); this.error = '';
    } catch (e) { this.graph = null; this.source = null; this.mutableText = ''; this.error = (e as Error).message; }
  }
  private restore(snapshot: string) {
    if (!this.source) return;
    const editable = JSON.parse(snapshot) as Pick<ImportedGraph, 'nodes' | 'edges'>;
    this.graph = freezeImportValue({ version: 1, format: 'umcode.imported.v1', source: this.source, ...editable });
    this.mutableText = snapshot; this.text = JSON.stringify(this.graph); this.error = '';
  }
  private trimHistory() {
    // Estimate JS string storage at two bytes/code unit; cap both directions
    // together so long names/tensor mappings cannot evade the snapshot count cap.
    let bytes = [...this.past, ...this.future].reduce((sum, text) => sum + text.length * 2, 0);
    while (this.past.length + this.future.length > 100 || bytes > 8 * 1024 * 1024) {
      const removed = this.past.length ? this.past[0] : this.future[0];
      if (removed === undefined) break;
      if (this.past.length) this.past = this.past.slice(1); else this.future = this.future.slice(1);
      bytes -= removed.length * 2;
    }
  }
  sync(text: string) { if (text === this.text) return; this.past = []; this.future = []; this.load(text); }
  commit(graph: ImportedGraph) {
    if (!this.graph || !this.source) return;
    if (graph.source !== this.source && !sameImportValue(graph.source, this.source)) throw new Error('Imported source metadata is immutable.');
    for (const locked of this.graph.nodes.filter(importNodeLocked)) {
      const next=graph.nodes.find(n=>n.id===locked.id);
      if (!next || !sameImportValue(locked,next)) throw new Error('This imported group is read-only.');
    }
    for (const node of graph.nodes) {
      const prior = this.graph.nodes.find(n => n.id === node.id);
      if (node.sourceKey !== prior?.sourceKey) {
        const b = this.source.baselineNodes.find(n => n.id === node.id && n.sourceKey === node.sourceKey);
        if (prior || !b || this.graph.nodes.some(n=>n.sourceKey===node.sourceKey) || !node.sourceKey ||
            b.kind!==node.kind || !sameImportValue(b.config,node.config) || !sameImportValue(b.config,node.baselineConfig) ||
            !sameImportValue(b.tensorNames,node.tensorNames)) throw new Error('Only original layers can restore an immutable source binding.');
      }
      if (prior && !sameImportValue(prior.tensorNames,node.tensorNames)) throw new Error('Original source tensor names are immutable.');
      if (prior && !sameImportValue(prior.baselineConfig, node.baselineConfig)) throw new Error('Original layer baseline is immutable.');
    }
    const next = this.snapshot(graph); if (next === this.mutableText) return;
    this.past = [...this.past, this.mutableText]; this.future = []; this.trimHistory(); this.restore(next);
  }
  undo() { const snapshot = this.past.at(-1); if (snapshot === undefined) return; this.future = [...this.future, this.mutableText]; this.past = this.past.slice(0, -1); this.trimHistory(); this.restore(snapshot); }
  redo() { const snapshot = this.future.at(-1); if (snapshot === undefined) return; this.past = [...this.past, this.mutableText]; this.future = this.future.slice(0, -1); this.trimHistory(); this.restore(snapshot); }
}

function freezeImportValue<T>(value: T): T {
  // Frozen source subtrees are skipped on subsequent node/edge edits.
  if (value && typeof value === 'object' && !Object.isFrozen(value)) {
    for (const child of Object.values(value)) freezeImportValue(child);
    Object.freeze(value);
  }
  return value;
}
