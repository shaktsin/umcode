import { buildContext, graphRevision, previewProposal, type ProposalPreview, type WorkbenchProposal } from './assistant';
import { definition, readGraph, type Graph } from './graph';

export interface AssistantModel { projectId: string; modelId: string; modelName: string; text: string }
interface ActivePreview extends ProposalPreview { proposal: WorkbenchProposal; base: AssistantModel }
interface Binding { projectId: string; modelId: string; commit: (graph: Graph) => void }
const sameModel = (a: { projectId: string; modelId: string } | null, b: { projectId: string; modelId: string }) => a?.projectId === b.projectId && a.modelId === b.modelId;
const changed = () => new Error('Model changed since this suggestion. Ask for an updated suggestion.');

/** The binding commits through the mounted editor, preserving the same manual undo history. */
export class WorkbenchAssistantState {
  selectedNodeId = $state<string | null>(null);
  preview = $state<ActivePreview | null>(null);
  error = $state('');
  focusRequest = $state(0);
  revision = $state('');
  private appliedKey = $state('');
  private binding: Binding | null = null;
  private generation = 0;
  private tracking = 0;
  constructor(private readCurrent: () => AssistantModel | null, private hash = graphRevision) {}

  get current() { return this.readCurrent(); }
  get selectedLabel() {
    const current = this.current;
    if (!current || !this.selectedNodeId) return '';
    try { const node = readGraph(current.text).nodes.find(node => node.id === this.selectedNodeId); return node ? `${definition(node.kind).name} · ${node.id}` : ''; }
    catch { return ''; }
  }
  get previewStale() { const p = this.preview, current = this.current; return !!p && (!sameModel(current, p.base) || current?.text !== p.base.text || !sameModel(this.binding, p.base)); }

  /** Called when the workbench draft changes, only to update local proposal status. */
  async track() {
    const token = ++this.tracking, current = this.current;
    this.revision = '';
    if (!current) return;
    const revision = await this.hash(current.text);
    if (token === this.tracking && sameModel(this.current, current) && this.current?.text === current.text) this.revision = revision;
  }
  matches(proposal: WorkbenchProposal) { return sameModel(this.current, proposal) && this.revision === proposal.revision; }
  wasApplied(proposal: WorkbenchProposal) { return this.appliedKey === JSON.stringify(proposal); }
  bind(commit: (graph: Graph) => void) {
    const current = this.current;
    if (!current) return () => {};
    const binding = { projectId: current.projectId, modelId: current.modelId, commit };
    this.binding = binding; this.selectedNodeId = null;
    return () => { if (this.binding === binding) { this.binding = null; this.selectedNodeId = null; this.cancel(); } };
  }
  async capture() {
    const current = this.current;
    if (!current) throw new Error('Open a model before asking about its layers.');
    const context = await buildContext(current.projectId, { id: current.modelId, name: current.modelName }, current.text, this.selectedNodeId);
    if (!sameModel(this.current, current) || this.current?.text !== current.text) throw changed();
    return context;
  }
  async showProposal(proposal: WorkbenchProposal) {
    this.error = '';
    const generation = ++this.generation, base = this.current;
    try {
      if (!base || !sameModel(base, proposal)) throw new Error('Open this suggestion’s model before previewing it.');
      if (!sameModel(this.binding, base)) throw new Error('Open the Build tab to preview this suggestion.');
      const revision = await this.hash(base.text);
      if (generation !== this.generation || !sameModel(this.current, base) || this.current?.text !== base.text || revision !== proposal.revision) throw changed();
      const candidate = previewProposal(base.text, proposal);
      this.preview = { ...candidate, proposal, base: { ...base } };
    } catch (error) { this.error = (error as Error).message; throw error; }
  }
  apply() {
    const preview = this.preview;
    if (!preview) throw new Error('Preview this suggestion before applying it.');
    if (this.previewStale || !this.binding) { this.error = changed().message; throw changed(); }
    // No await between the final identity/text check and the editor commit.
    this.binding.commit(readGraph(JSON.stringify(preview.graph)));
    this.appliedKey = JSON.stringify(preview.proposal);
    this.cancel();
  }
  cancel() { this.generation++; this.preview = null; this.error = ''; }
  ask() { this.focusRequest++; }
}
