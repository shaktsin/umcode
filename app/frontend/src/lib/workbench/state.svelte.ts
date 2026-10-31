import { errMsg } from '$lib/format';
import type { OpenWorkbenchModel, WorkbenchModel, WorkbenchTask } from './types';

export type WorkbenchTab = 'Data' | 'Build' | 'Train' | 'Inference';
type SelectionStorage = Pick<Storage, 'getItem' | 'setItem' | 'removeItem'>;
type Call = (method: string, params: unknown) => Promise<any>;
interface Edit { bundle: OpenWorkbenchModel; draft: string; saved: string }
interface ProjectState {
  models: WorkbenchModel[];
  selectedId: string | null;
  edits: Record<string, Edit>;
  tab: WorkbenchTab;
  initialized: boolean;
}

/** One in-memory draft per project/model. RPC completions belong to a generation,
 * so navigation and reconnect can invalidate them without losing local edits. */
export class WorkbenchState {
  projectId = $state<string | null>(null);
  connected = $state(false);
  busy = $state(false);
  loading = $state(false);
  error = $state('');
  notice = $state('');
  private projects = $state<Record<string, ProjectState>>({});
  private generation = 0;

  constructor(private call: Call, private storage?: SelectionStorage) {}

  private get current() { return this.projectId ? this.projects[this.projectId] : undefined; }
  private get edit() { const s = this.current; return s?.selectedId ? s.edits[s.selectedId] : undefined; }
  get models() { return this.current?.models ?? []; }
  get selected() { return this.edit?.bundle ?? null; }
  get draft() { return this.edit?.draft ?? ''; }
  set draft(value: string) { if (this.edit) this.edit.draft = value; }
  get savedDraft() { return this.edit?.saved ?? ''; }
  get dirty() { return this.draft !== this.savedDraft; }
  get hasDrafts() { return Object.values(this.projects).some(s => Object.values(s.edits).some(e => e.draft !== e.saved)); }
  hasModelDraft(modelId: string) { const edit = this.current?.edits[modelId]; return !!edit && edit.draft !== edit.saved; }
  get tab() { return this.current?.tab ?? 'Build'; }
  set tab(value: WorkbenchTab) { if (this.current) this.current.tab = value; }

  private key(id: string) { return `umcode.workbench.model.${id}`; }
  private remember(id: string | null) {
    if (!this.projectId) return;
    try {
      if (id) this.storage?.setItem(this.key(this.projectId), id);
      else this.storage?.removeItem(this.key(this.projectId));
    } catch { /* Selection persistence is optional. */ }
  }
  private begin() {
    this.error = ''; this.notice = ''; this.busy = false; this.loading = false;
    return ++this.generation;
  }
  private valid(token: number) { return token === this.generation && this.connected; }

  async activate(projectId: string | null, connected: boolean) {
    if (this.projectId === projectId && this.connected === connected) return;
    this.begin();
    this.projectId = projectId;
    this.connected = connected;
    if (projectId && !this.projects[projectId]) {
      this.projects[projectId] = { models: [], selectedId: null, edits: {}, tab: 'Build', initialized: false };
    }
    if (projectId && connected) await this.refresh();
  }

  async refresh() {
    const projectId = this.projectId, session = this.current;
    if (!projectId || !session || !this.connected) return;
    const token = this.begin(); this.loading = true;
    try {
      const result = await this.call('workbench/model/list', { projectId });
      if (!this.valid(token)) return;
      session.models = result.models ?? [];
      let want = session.selectedId;
      if (!session.initialized) {
        try { want = this.storage?.getItem(this.key(projectId)) ?? null; } catch { /* optional */ }
        session.initialized = true;
      }
      if (want && !session.models.some(m => m.id === want)) {
        session.selectedId = null; this.remember(null);
        this.notice = 'The previous model is no longer available. Choose another model or create one.';
      } else if (want) {
        await this.open(want);
      }
    } catch (error) {
      if (this.valid(token)) this.error = errMsg(error);
    } finally {
      if (token === this.generation) this.loading = false;
    }
  }

  private accept(bundle: OpenWorkbenchModel, replaceDraft = false) {
    const session = this.current!;
    const id = bundle.model.id, existing = session.edits[id];
    if (!existing || replaceDraft || existing.draft === existing.saved) {
      const graph = bundle.graphJson ?? JSON.stringify(bundle.graph, null, 2);
      session.edits[id] = { bundle, draft: graph, saved: graph };
    }
    session.selectedId = id;
    this.remember(id);
  }

  async open(modelId: string) {
    if (!this.projectId || !this.connected) return;
    const token = this.begin(), projectId = this.projectId;
    this.busy = true;
    try {
      const result = await this.call('workbench/model/open', { projectId, modelId });
      if (!this.valid(token)) return;
      this.accept(result);
      this.tab = 'Build';
    } catch (error) {
      if (this.valid(token)) this.error = errMsg(error);
    } finally { if (token === this.generation) this.busy = false; }
  }

  newModel() {
    this.begin();
    if (this.current) this.current.selectedId = null;
    this.remember(null);
  }

  async create(name: string, task: WorkbenchTask) {
    if (!this.projectId || !this.connected || this.busy || !name.trim()) return;
    const token = this.begin(), projectId = this.projectId;
    this.busy = true;
    try {
      const model = await this.call('workbench/model/create', { projectId, name: name.trim(), task }) as WorkbenchModel;
      if (!this.valid(token)) return;
      this.current!.models = [model, ...this.models.filter(m => m.id !== model.id)];
      const result = await this.call('workbench/model/open', { projectId, modelId: model.id });
      if (!this.valid(token)) return;
      this.accept(result); this.tab = 'Build'; this.notice = `Created ${model.name}.`;
      return model.id;
    } catch (error) {
      if (this.valid(token)) this.error = errMsg(error);
    } finally { if (token === this.generation) this.busy = false; }
  }

  async save() {
    const edit = this.edit, projectId = this.projectId;
    if (!edit || !projectId || !this.connected || this.busy) return;
    const token = this.begin(), submitted = edit.draft;
    try { JSON.parse(submitted); }
    catch { this.error = 'Graph must contain valid JSON.'; return; }
    this.busy = true;
    try {
      await this.call('workbench/graph/save', { projectId, modelId: edit.bundle.model.id, graphJson: submitted });
      if (!this.valid(token)) return;
      edit.saved = submitted;
      this.notice = 'Model saved to your project.';
    } catch (error) {
      if (this.valid(token)) this.error = errMsg(error);
    } finally { if (token === this.generation) this.busy = false; }
  }

  /** Caller confirms discard before invoking reload. A failed read keeps the draft. */
  async reload() {
    const modelId = this.selected?.model.id, projectId = this.projectId, edit = this.edit;
    if (!modelId || !projectId || !edit || !this.connected || this.busy) return;
    const confirmedDraft = edit.draft;
    const token = this.begin(); this.busy = true;
    try {
      const result = await this.call('workbench/model/open', { projectId, modelId });
      if (this.valid(token)) {
        if (edit.draft !== confirmedDraft) {
          edit.bundle = result;
          edit.saved = result.graphJson ?? JSON.stringify(result.graph, null, 2);
          this.notice = 'Reloaded the disk version. Your newer edits are still unsaved.';
        } else {
          this.accept(result, true); this.notice = 'Reloaded from disk.';
        }
      }
    } catch (error) {
      if (this.valid(token)) this.error = errMsg(error);
    } finally { if (token === this.generation) this.busy = false; }
  }
}
