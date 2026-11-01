import { recommendConfig, validateConfig, validateUploads } from './data';
import type { DataConfig, DataContext, DataFile, DataSelection, DataSource, DataSourceTab, DataState, Dataset, HFAccount, HFDataset, HFRepository } from './dataTypes';
import type { WorkbenchTask } from './types';

type Call = (method: string, params: Record<string, unknown>, timeout?: number) => Promise<unknown>;
interface Ticket { epoch: number; request: number; projectId: string; modelId: string; modelCreatedAt: string }
interface ModelDataDrafts { selectedDatasetId?: string | null; source: DataSourceTab; configs: Record<string, DataConfig> }
export type DataDraftCache = Map<string, ModelDataDrafts>;
// Session-only: no raw rows, credentials, or tokens enter this cache.
export const workbenchDataDrafts: DataDraftCache = new Map();
const copy = <T>(value: T): T => JSON.parse(JSON.stringify(value));
const errorMessage = (error: unknown) => error instanceof Error ? error.message : 'The data operation failed. Try again.';

export class WorkbenchDataState {
  source = $state<DataSourceTab>('examples');
  datasets = $state<Dataset[]>([]);
  dataset = $state<Dataset | null>(null);
  selection = $state<DataSelection | null>(null);
  config = $state<DataConfig | null>(null);
  stage = $state('');
  error = $state('');
  notice = $state('');
  results = $state<HFDataset[]>([]);
  repository = $state<HFRepository | null>(null);
  selectedFiles = $state<string[]>([]);
  account = $state<HFAccount>({ connected: false, username: '' });
  accountBusy = $state(false);
  private context: DataContext | null = null;
  // Retain the mounted model identity: the parent's selection may already be
  // cleared or replaced when Svelte runs this tab's teardown.
  private activeModelCreatedAt = '';
  private epoch = 0;
  private request = 0;
  private accountRequest = 0;
  private disposed = false;
  constructor(private call: Call, private graphText: () => string = () => '', private drafts: DataDraftCache = new Map(), private modelCreatedAt: () => string = () => '') {}

  private cacheKey() { return this.context ? JSON.stringify([this.context.projectId, this.context.modelId, this.activeModelCreatedAt]) : ''; }
  private rememberDraft() {
    const key = this.cacheKey(); if (!key) return;
    const entry = this.drafts.get(key) ?? { source: this.source, configs: {} };
    entry.source = this.source;
    if (this.dataset && this.config) {
      entry.selectedDatasetId = this.dataset.id;
      if (this.dirty) entry.configs[this.dataset.id] = copy(this.config);
      else delete entry.configs[this.dataset.id];
    }
    while (Object.keys(entry.configs).length > 32) delete entry.configs[Object.keys(entry.configs)[0]];
    this.drafts.delete(key); this.drafts.set(key, entry);
    while (this.drafts.size > 32) this.drafts.delete(this.drafts.keys().next().value!);
  }

  get busy() { return !!this.stage; }
  get connected() { return !!this.context?.connected; }
  get dirty() { return !!this.config && (!this.selection || JSON.stringify(this.config) !== JSON.stringify(this.selection.config)); }
  get validation() { return this.dataset && this.config ? validateConfig(this.dataset, this.config) : []; }

  async activate(projectId: string, modelId: string, task: WorkbenchTask, connected: boolean) {
    if (this.disposed) return;
    const old = this.context;
    const createdAt = this.modelCreatedAt();
    if (old?.projectId === projectId && old.modelId === modelId && old.task === task && old.connected === connected && createdAt === this.activeModelCreatedAt) return;
    const changed = old?.projectId !== projectId || old?.modelId !== modelId || createdAt !== this.activeModelCreatedAt;
    this.rememberDraft();
    this.invalidate();
    this.activeModelCreatedAt = createdAt;
    this.context = { projectId, modelId, task, connected };
    if (changed) {
      this.datasets = []; this.dataset = null; this.config = null; this.selection = null;
      this.results = []; this.repository = null; this.selectedFiles = []; this.source = this.drafts.get(this.cacheKey())?.source ?? 'examples';
      this.account = { connected: false, username: '' };
    }
    if (connected && projectId && modelId) await this.load();
  }
  private invalidate() { this.epoch++; this.request++; this.accountRequest++; this.stage = ''; this.accountBusy = false; this.error = ''; this.notice = ''; }
  dispose() { this.rememberDraft(); this.disposed = true; this.invalidate(); }
  changeSource(source: DataSourceTab) {
    if (source === this.source) return;
    this.rememberDraft();
    this.invalidate(); this.source = source; this.dataset = null; this.selection = null; this.config = null;
    this.repository = null; this.selectedFiles = []; this.results = [];
    const entry = this.drafts.get(this.cacheKey()) ?? { source, configs: {} };
    entry.source = source; entry.selectedDatasetId = null; this.drafts.set(this.cacheKey(), entry);
  }
  private begin(stage: string): Ticket | null {
    if (!this.context?.connected || this.disposed) { this.error = 'Engine disconnected. Reconnect before importing or saving preparation.'; return null; }
    const ticket = { epoch: this.epoch, request: ++this.request, projectId: this.context.projectId, modelId: this.context.modelId, modelCreatedAt: this.activeModelCreatedAt };
    this.stage = stage; this.error = ''; this.notice = '';
    return ticket;
  }
  private current(ticket: Ticket) { return !this.disposed && ticket.epoch === this.epoch && ticket.request === this.request && this.context?.connected && ticket.projectId === this.context.projectId && ticket.modelId === this.context.modelId; }
  private params(ticket: Ticket) { return { projectId: ticket.projectId, modelId: ticket.modelId, ...(ticket.modelCreatedAt ? { modelCreatedAt: ticket.modelCreatedAt } : {}) }; }
  private async rpc<T>(method: string, ticket: Ticket, params: Record<string, unknown> = {}) { return await this.call(method, { ...this.params(ticket), ...params }, 120000) as T; }
  private failed(ticket: Ticket, error: unknown) { if (this.current(ticket)) this.error = errorMessage(error); }
  private finish(ticket: Ticket) { if (this.current(ticket)) this.stage = ''; }
  private apply(state: DataState, keepEmpty = false) {
    this.rememberDraft();
    this.datasets = state.datasets ?? []; this.dataset = keepEmpty ? null : state.dataset;
    this.selection = state.selection?.datasetId === state.dataset?.id ? state.selection : null;
    if (keepEmpty) this.selection = null;
    const draft = this.dataset && this.drafts.get(this.cacheKey())?.configs[this.dataset.id];
    this.config = draft ? copy(draft) : this.selection ? copy(this.selection.config) : this.dataset ? recommendConfig(this.dataset, this.context!.task, this.graphText()) : null;
    this.rememberDraft();
  }
  async load(datasetId?: string) {
    this.rememberDraft();
    const cached = this.drafts.get(this.cacheKey());
    const keepEmpty = datasetId === undefined && cached?.selectedDatasetId === null;
    const requested = datasetId ?? this.dataset?.id ?? cached?.selectedDatasetId;
    const ticket = this.begin('Loading imported data…'); if (!ticket) return;
    try { const result = await this.rpc<DataState>('workbench/data/get', ticket, requested ? { datasetId: requested } : {}); if (this.current(ticket)) this.apply(result, keepEmpty); }
    catch (error) { this.failed(ticket, error); } finally { this.finish(ticket); }
  }
  async selectDataset(datasetId: string) { if (datasetId) await this.load(datasetId); }
  async importData(source: DataSource, files?: DataFile[]) {
    const ticket = this.begin('Importing dataset…'); if (!ticket) return;
    await this.importWithTicket(ticket, copy(source), files);
  }
  private async importWithTicket(ticket: Ticket, source: DataSource, files?: DataFile[]) {
    try {
      this.stage = source.kind === 'local' ? 'Importing dataset…' : 'Downloading and importing dataset…';
      const imported = await this.rpc<Dataset>('workbench/data/import', ticket, { source, ...(files ? { files } : {}) });
      if (!this.current(ticket)) return;
      this.stage = 'Loading imported preview…';
      const state = await this.rpc<DataState>('workbench/data/get', ticket, { datasetId: imported.id });
      if (this.current(ticket)) { this.apply(state); this.notice = 'Dataset imported into this model. Choose mappings and save preparation next.'; }
    } catch (error) { this.failed(ticket, error); } finally { this.finish(ticket); }
  }
  async importLocal(files: File[]) {
    const validation = validateUploads(files); if (validation) { this.error = validation; return; }
    const ticket = this.begin('Reading local files…'); if (!ticket) return;
    try {
      const encoded: DataFile[] = [];
      for (const file of files) {
        const bytes = new Uint8Array(await file.arrayBuffer());
        if (!this.current(ticket)) return;
        let binary = '';
        for (let offset = 0; offset < bytes.length; offset += 8192) binary += String.fromCharCode(...bytes.subarray(offset, offset + 8192));
        encoded.push({ name: file.webkitRelativePath || file.name, contentBase64: btoa(binary) });
      }
      if (this.current(ticket)) await this.importWithTicket(ticket, { kind: 'local', name: files.length === 1 ? files[0].name : `${files.length} local files` }, encoded);
    } catch (error) { this.failed(ticket, error); } finally { this.finish(ticket); }
  }
  updateConfig(patch: Partial<DataConfig>) {
    if (!this.config) return;
    const next = { ...this.config, ...patch };
    if (patch.targetColumn !== undefined) next.inputColumns = next.inputColumns.filter(name => name !== patch.targetColumn);
    this.config = next;
    this.rememberDraft();
  }
  async prepare() {
    const ticket = this.begin('Saving preparation…'); if (!ticket) return;
    if (!this.config || !this.dataset) { this.error = 'Import a dataset before saving preparation.'; this.finish(ticket); return; }
    const problems = validateConfig(this.dataset, this.config);
    if (problems.length) { this.error = problems.join(' '); this.finish(ticket); return; }
    const submitted = copy(this.config);
    try {
      const selection = await this.rpc<DataSelection>('workbench/data/prepare', ticket, { config: submitted });
      if (this.current(ticket)) {
        this.selection = selection;
        if (JSON.stringify(this.config) === JSON.stringify(submitted)) this.config = copy(selection.config);
        this.rememberDraft();
        this.notice = 'Preparation saved in this model. Model graph Save is separate; training is not available yet.';
      }
    } catch (error) { this.failed(ticket, error); } finally { this.finish(ticket); }
  }
  async search(query: string) {
    const ticket = this.begin('Searching Hugging Face…'); if (!ticket) return;
    this.repository = null; this.selectedFiles = [];
    try { const result = await this.rpc<{ datasets: HFDataset[] }>('workbench/data/huggingface', ticket, { action: 'search', query: query.trim() }); if (this.current(ticket)) this.results = result.datasets ?? []; }
    catch (error) { this.failed(ticket, error); } finally { this.finish(ticket); }
  }
  async inspect(repo: string) {
    const ticket = this.begin('Inspecting repository files…'); if (!ticket) return;
    this.repository = null; this.selectedFiles = [];
    try {
      const repository = await this.rpc<HFRepository>('workbench/data/huggingface', ticket, { action: 'inspect', repo: repo.trim() });
      if (this.current(ticket)) { this.repository = repository; this.selectedFiles = []; }
    } catch (error) { this.failed(ticket, error); } finally { this.finish(ticket); }
  }
  toggleFile(name: string, checked: boolean) {
    if (!this.repository?.files.some(file => file.name === name && file.supported)) return;
    this.selectedFiles = checked ? [...new Set([...this.selectedFiles, name])] : this.selectedFiles.filter(file => file !== name);
  }
  async importRepository() {
    if (!this.repository || !this.selectedFiles.length) return;
    await this.importData({ kind: 'huggingface', name: this.repository.id, repo: this.repository.id, revision: this.repository.sha, files: [...this.selectedFiles] });
  }
  async accountAction(action: 'status' | 'connect' | 'disconnect', secret = '') {
    if (!this.context?.connected || this.disposed) return;
    const epoch = this.epoch, accountRequest = ++this.accountRequest;
    const { projectId, modelId } = this.context;
    this.accountBusy = true;
    const current = () => !this.disposed && epoch === this.epoch && accountRequest === this.accountRequest;
    try {
      const result = await this.call('workbench/data/huggingface', { projectId, modelId, action, ...(action === 'connect' ? { secret } : {}) }, 120000) as HFAccount;
      if (current()) { this.account = result; if (action !== 'status') this.notice = result.connected ? `Connected to Hugging Face as ${result.username}.` : 'Hugging Face token disconnected.'; }
    } catch (error) { if (current()) this.error = secret ? errorMessage(error).split(secret).join('[redacted]') : errorMessage(error); }
    finally { secret = ''; if (current()) this.accountBusy = false; }
  }
}
