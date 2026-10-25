import { defaultApprovalMode } from '$lib/approvalModes';
import { applyCompactionUpdate,compactionActive } from '$lib/compaction';
import type { CompactionState } from '$lib/types';
import { DelegationState } from './delegation.svelte';
import type { DelegationUpdated } from '$lib/delegation';
import { app } from './app.svelte';
import { projects } from './projects.svelte';
import { errMsg } from '$lib/format';
import { inspector } from './inspector.svelte';
import type { Attachment, BrowserVerificationResult, Item, ModelRouteResult, ModelSelection, RouteChangedEvent, SearchHit, Thread, Turn } from '$lib/types';

function verificationRole(item: Item): string | undefined {
  const name = item.tool?.name;
  return name === 'verification.run' || name === 'verification.plan' || name === 'browser.verify' || name?.startsWith('visual.') ? name : undefined;
}

function sortThreads(list: Thread[]): Thread[] {
  return [...list].sort((a, b) => {
    if (a.pinned !== b.pinned) return a.pinned ? -1 : 1;
    return b.updatedAt.localeCompare(a.updatedAt);
  });
}

function isSideChat(t: Thread): boolean {
  // The title check keeps side sessions created by older app builds out of
  // the main chat list; current side sessions carry an explicit channel.
  return t.channel === 'side' || (!!t.forkedFrom && t.title === 'Side chat');
}

/** One open conversation: the main chat, or a side chat beside it. */
export class ThreadView {
  compaction = $state<CompactionState|null>(null);
  delegation = new DelegationState();
  id = $state<string | null>(null);
  thread = $state<Thread | null>(null);
  turns = $state<Turn[]>([]);
  items = $state<Item[]>([]);
  loading = $state(false);
  sending = $state(false);
  selection = $state<ModelSelection>({});
  workspaceMode = $state<'local' | 'worktree'>('local');
  /** Approval mode picked before the chat exists; sent with thread/start. */
  approvalMode = $state<'normal' | 'auto_workspace' | 'auto_all'>(defaultApprovalMode());
  /** What this side chat was opened about, shown above the first message. */
  context = $state('');
  highlightItem = $state<string | null>(null);
  /** What the next message would run on, and what it would fall back to. */
  route = $state<ModelRouteResult | null>(null);
  private routeSeq = 0;
  private compactionVersion=0;
  private terminalCompactionTurns=new Set<string>();

  get running(): Turn | undefined {
    return this.turns.find((t) => t.status === 'running');
  }

  get title(): string {
    return this.thread?.title || (this.id ? 'Untitled' : 'Chat');
  }

  reset() {
    void this.delegation.bind(null);
    this.compaction=null;this.compactionVersion++;this.terminalCompactionTurns.clear();
    this.id = null;
    this.thread = null;
    this.workspaceMode = 'local';
    this.approvalMode = defaultApprovalMode();
    this.turns = [];
    this.items = [];
    this.sending = false;
    this.context = '';
    this.highlightItem = null;
  }

  async load(id: string) {
    if (this.id !== id) {this.compaction=null;this.compactionVersion++;this.terminalCompactionTurns.clear();void this.delegation.bind(id);}
    this.id = id;
    this.loading = true;
    const compactionVersion=this.compactionVersion;
    try {
      const r = await app.call<{ thread: Thread; turns: Turn[]; items: Item[]; compaction?:CompactionState }>('thread/read', { threadId: id });
      if (this.id !== id) return;
      for(const turn of r.turns??[])if(turn.status!=='running')this.terminalCompactionTurns.add(turn.id);
      if(r.compaction)this.applyCompaction(r.compaction);
      else if(this.compactionVersion===compactionVersion)this.compaction=null;
      this.thread = r.thread;
      void this.delegation.bind(id, r.thread.projectId);
      this.workspaceMode = r.thread.workspaceMode || 'local';
      this.turns = r.turns ?? [];
      this.items = (r.items ?? []).sort((a, b) => a.seq - b.seq);
      this.selection = { ...(r.thread.settings ?? {}) };
      this.sending = this.turns.some((t) => t.status === 'running');
      void this.loadRoute();
    } catch (e) {
      app.toast('error', errMsg(e));
    } finally {
      this.loading = false;
    }
  }

  applyCompaction(state:CompactionState) {
    if(state.threadId!==this.id)return;
    const turn=this.turns.find(t=>t.id===state.turnId);
    if((this.terminalCompactionTurns.has(state.turnId)||turn&&turn.status!=='running')&&compactionActive(state))return;
    this.compactionVersion++;
    this.compaction=applyCompactionUpdate(this.compaction,state);
  }

  applyTurn(t: Turn) {
    if (t.threadId !== this.id) return;
    if(t.status!=='running'){this.terminalCompactionTurns.add(t.id);this.compactionVersion++;if(this.compaction?.turnId===t.id&&compactionActive(this.compaction))this.compaction={...this.compaction,status:'interrupted'};}
    const i = this.turns.findIndex((x) => x.id === t.id);
    if (i >= 0) this.turns[i] = t;
    else this.turns = [...this.turns, t];
    if (t.status !== 'running') this.sending = this.turns.some((x) => x.status === 'running');
  }

  applyItem(it: Item) {
    if (it.threadId !== this.id) return;
    const i = this.items.findIndex((x) => x.id === it.id);
    if (i >= 0) {
      const prev = this.items[i];
      if (!it.text && prev.text) it.text = prev.text;
      this.items[i] = it;
    } else {
      this.items = [...this.items, it].sort((a, b) => a.seq - b.seq);
    }
  }

  applyDelta(itemId: string, text?: string, output?: string) {
    const it = this.items.find((x) => x.id === itemId);
    if (!it) return;
    if (text) it.text = (it.text || '') + text;
    if (output && it.tool) it.tool.output = (it.tool.output || '') + output;
  }

  async send(text: string, attachments: Attachment[] = [], start?: () => Promise<Thread>) {
    if (this.delegation.pending) return;
    text = text.trim();
    if (!text && attachments.length === 0) return;
    this.sending = true;
    try {
      if (!this.id) {
        if (!start) throw new Error('no chat to send to');
        const th = await start();
        this.id = th.id;
        this.thread = th;
        this.turns = [];
        this.items = [];
      }
      if (this.delegation.threadId !== this.id || this.delegation.projectId !== this.thread?.projectId) await this.delegation.bind(this.id, this.thread?.projectId);
      await app.call('turn/start', { threadId: this.id, text, attachments, ...this.delegation.submission() });
      void this.delegation.hydrate();
    } catch (e) {
      this.sending = false;
      app.toast('error', errMsg(e));
    }
  }

  async interrupt() {
    const t = this.running;
    if (this.delegation.active) await this.delegation.cancel(this.delegation.active.task.id);
    if (t) await app.try('turn/interrupt', { turnId: t.id });
  }

  async setSelection(sel: ModelSelection) {
    this.selection = sel;
    if (this.id) await app.try('thread/setSettings', { threadId: this.id, settings: sel });
    void this.loadRoute();
  }

  /**
   * Asks the engine what the next message would use. It is a preview, so a
   * failure is not worth a toast: the chip simply says nothing.
   */
  async loadRoute() {
    const seq = ++this.routeSeq;
    try {
      const r = await app.call<ModelRouteResult>('model/route', {
        threadId: this.id ?? undefined,
        projectId: projects.activeId ?? undefined,
        override: this.selection,
      });
      if (seq === this.routeSeq) this.route = r;
    } catch {
      if (seq === this.routeSeq) this.route = null;
    }
  }
}

class ChatState {
  threads = $state<Thread[]>([]);
  archivedThreads = $state<Thread[]>([]);
  query = $state('');
  hits = $state<SearchHit[]>([]);
  /** The conversation in column 1. */
  main = new ThreadView();
  /** Side chats in column 2, most recent last. */
  sides = $state<ThreadView[]>([]);
  activeSideId = $state<string | null>(null);
  /** Running turns across chats, including chats not currently open. */
  workingThreads = $state<Record<string, boolean>>({});
  private mainGeneration = 0;

  constructor() {
    const r = app.rpc;
    r.on('delegation/updated', (p: DelegationUpdated) => this.each(v => v.delegation.apply(p)));
    r.on('thread/compactionUpdated',(state:CompactionState)=>this.each(v=>v.applyCompaction(state)));
    r.on('thread/updated', (p: { thread: Thread }) => this.upsertThread(p.thread));
    r.on('turn/started', (p: { turn: Turn }) => {
      this.workingThreads[p.turn.threadId] = true;
      this.each((v) => v.applyTurn(p.turn));
    });
    r.on('turn/completed', (p: { turn: Turn }) => {
      if (p.turn.status !== 'running') this.workingThreads[p.turn.threadId] = false;
      this.each((v) => v.applyTurn(p.turn));
      // A turn can leave a key cooling down, so what the next one would use
      // may have changed.
      void this.main.loadRoute();
    });
    r.on('turn/routeChanged', (p: RouteChangedEvent) => {
      this.each((v) => {
        if (v.id === p.threadId) void v.loadRoute();
      });
    });
    r.on('item/started', (p: { item: Item }) => {
      this.each((v) => v.applyItem(p.item));
      const role = verificationRole(p.item);
      if (role) inspector.openVerificationOutput(role, p.item.tool?.output || '', p.item.id, p.item.threadId, this.itemProject(p.item));
    });
    r.on('item/completed', (p: { item: Item }) => {
      this.each((v) => v.applyItem(p.item));
      const role = verificationRole(p.item);
      if (!role) return;
      let report: BrowserVerificationResult | undefined;
      if (role === 'browser.verify' || role.startsWith('visual.')) {
        try { report = JSON.parse(p.item.tool?.output || '') as BrowserVerificationResult; }
        catch { /* Keep raw output when a blocked call has no report. */ }
      }
      inspector.updateVerificationOutput(role, p.item.tool?.output || p.item.tool?.error || '', p.item.id, p.item.threadId, this.itemProject(p.item), report);
    });
    r.on('item/delta', (p: { threadId: string; itemId: string; text?: string; output?: string }) => {
      this.each((v) => {
        if (v.id === p.threadId) v.applyDelta(p.itemId, p.text, p.output);
      });
      const view = [this.main, ...this.sides].find((candidate) => candidate.id === p.threadId);
      const item = view?.items.find((candidate) => candidate.id === p.itemId);
      const role = item && verificationRole(item);
      if (item && role) inspector.updateVerificationOutput(role, item.tool?.output || '', item.id, item.threadId, this.itemProject(item));
    });
    app.onConnected(() => this.reload());
    // The approved models changed under us: re-ask what each chat would use.
    window.addEventListener('umcode:routing', () => this.each((v) => void v.loadRoute()));
  }

  private itemProject(item: Item): string | undefined {
    return [this.main, ...this.sides].find((view) => view.id === item.threadId)?.thread?.projectId
      ?? this.threads.find((thread) => thread.id === item.threadId)?.projectId;
  }

  private each(fn: (v: ThreadView) => void) {
    fn(this.main);
    for (const s of this.sides) fn(s);
  }

  get activeSide(): ThreadView | undefined {
    return this.sides.find((s) => s.id === this.activeSideId);
  }

  isWorking(threadId: string): boolean {
    return !!this.workingThreads[threadId] || this.main.id === threadId && !!this.main.running ||
      this.sides.some((view) => view.id === threadId && !!view.running);
  }

  async reload() {
    await this.loadThreads();
    if (this.main.id) await this.main.load(this.main.id);
    else void this.main.loadRoute();
    for (const side of this.sides) if(side.id) void side.load(side.id);
  }

  /** All chats, grouped by project in the rail. */
  async loadThreads() {
    try {
      const [active, archived] = await Promise.all([
        app.call<{ threads: Thread[] }>('thread/list', { archived: false, limit: 200 }),
        app.call<{ threads: Thread[] }>('thread/list', { archived: true, limit: 200 }),
      ]);
      this.threads = sortThreads(active.threads ?? []);
      this.archivedThreads = sortThreads(archived.threads ?? []);
    } catch (e) {
      app.toast('error', errMsg(e));
    }
  }

  private upsertThread(t: Thread) {
    this.threads = sortThreads(this.threads.filter((x) => x.id !== t.id && !x.archived).concat(t.archived ? [] : [t]));
    this.archivedThreads = sortThreads(this.archivedThreads.filter((x) => x.id !== t.id && x.archived).concat(t.archived ? [t] : []));
    if (this.main.id === t.id) { this.main.thread = t; void this.main.delegation.bind(t.id, t.projectId); }
    for (const s of this.sides) if (s.id === t.id) { s.thread = t; void s.delegation.bind(t.id, t.projectId); }
  }

  get rootThreads(): Thread[] {
    return this.threads.filter((t) => !isSideChat(t));
  }

  get archivedRootThreads(): Thread[] {
    return this.archivedThreads.filter((t) => !isSideChat(t));
  }

  async open(id: string, highlightItem?: string) {
    this.mainGeneration++;
    app.view = 'chat';
    const known = this.threads.find((t) => t.id === id);
    const targetProject = known?.projectId ?? null;
    if (known && projects.activeId !== targetProject) await projects.open(targetProject);
    this.main.highlightItem = highlightItem ?? null;
    await this.main.load(id);
    const loadedProject = this.main.thread?.projectId ?? null;
    if (projects.activeId !== loadedProject) await projects.open(loadedProject);
  }

  newChat(projectId: string | null = null) {
    this.mainGeneration++;
    if (projects.activeId !== projectId) void projects.open(projectId);
    app.view = 'chat';
    this.main.reset();
  }

  async newChatFor(projectId: string | null) {
    if (projects.activeId !== projectId) await projects.open(projectId);
    this.newChat(projectId);
  }

  private async startMainThread(): Promise<Thread> {
    const th = await app.call<Thread>('thread/start', {
      channel: 'app', projectId: projects.activeId ?? undefined,
      workspaceMode: this.main.workspaceMode, settings: this.main.selection,
      approvalMode: this.main.approvalMode,
    });
    this.upsertThread(th);
    return th;
  }

  /** Manual permission works before the first paid message. The pending guard
   * coalesces repeated clicks; navigation cannot attach a token to another scope. */
  async enableDelegation(view: ThreadView) {
    if (view.delegation.pending || view.sending || view.running) return;
    const projectId = view.id ? view.thread?.projectId : view === this.main ? projects.activeId : null;
    if (!projectId) { view.delegation.error = 'Agents require a committed Git project. Open a project first.'; return; }
    if (view.id) {
      if (view.delegation.threadId !== view.id || view.delegation.projectId !== projectId) await view.delegation.bind(view.id, projectId);
      await view.delegation.enable(); return;
    }
    if (view !== this.main) return;
    const generation = this.mainGeneration;
    view.delegation.pending = true; view.delegation.error = '';
    try {
      const th = await this.startMainThread();
      if (generation !== this.mainGeneration || projects.activeId !== projectId || view.id) return;
      view.id = th.id; view.thread = th;
      await view.delegation.bind(th.id, th.projectId, true);
      if (generation !== this.mainGeneration || projects.activeId !== projectId || view.id !== th.id || view.delegation.threadId !== th.id) return;
      // Transfer the pending guard synchronously to enable; no submission can
      // run between hydration and the one-use permission request.
      view.delegation.pending = false;
      await view.delegation.enable();
    } catch (e) {
      if (generation === this.mainGeneration) view.delegation.error = errMsg(e);
    } finally {
      if (generation === this.mainGeneration) { view.delegation.pending = false; }
    }
  }

  /** Send in the main chat, creating the thread in the open project on first send. */
  async send(text: string, attachments: Attachment[] = []) {
    await this.main.send(text, attachments, () => this.startMainThread());
  }

  // ---- side chats ----

  /** Start a side chat about something in the main chat. */
  async startSide(context: string, question = '') {
    if (!this.main.id) {
      app.toast('warn', 'Open a chat first, then ask about one of its messages.');
      return;
    }
    const source = this.main.thread;
    if (!source) return;
    const th = await app.try<Thread>('thread/fork', { threadId: source.id, sideChat: true });
    if (!th) {
      return;
    }
    this.upsertThread(th);
    const view = await this.openSide(th.id, context);
    if (!view) return;
    const opener = [context && `About this:\n\n${context}`, question].filter(Boolean).join('\n\n');
    if (question) await view.send(opener);
    return view;
  }

  /** Bring an existing side chat back into column 2. */
  async openSide(id: string, context = '') {
    const existing = this.sides.find((s) => s.id === id);
    if (existing) {
      this.activeSideId = id;
      return existing;
    }
    const view = new ThreadView();
    view.context = context;
    this.sides = [...this.sides, view];
    this.activeSideId = id;
    await view.load(id);
    return view;
  }

  closeSide(id: string) {
    this.sides = this.sides.filter((s) => s.id !== id);
    if (this.activeSideId === id) this.activeSideId = this.sides.at(-1)?.id ?? null;
  }

  // ---- thread management ----

  async rename(id: string, title: string) {
    await app.try('thread/rename', { threadId: id, title });
  }

  async pin(t: Thread) {
    await app.try('thread/pin', { threadId: t.id, value: !t.pinned });
  }

  async archive(t: Thread) {
    await app.try('thread/archive', { threadId: t.id, value: !t.archived });
    if (this.main.id === t.id && !t.archived) this.newChat();
  }

  async remove(t: Thread) {
    const r = await app.try('thread/delete', { threadId: t.id });
    if (r === undefined) return;
    this.threads = this.threads.filter((x) => x.id !== t.id);
    this.archivedThreads = this.archivedThreads.filter((x) => x.id !== t.id);
    if (this.main.id === t.id) this.newChat();
    this.closeSide(t.id);
  }

  async fork(t: Thread, upToItemId?: string) {
    const r = await app.try<Thread>('thread/fork', { threadId: t.id, upToItemId });
    if (r) await this.open(r.id);
  }

  async exportMarkdown(t: Thread): Promise<string | undefined> {
    const r = await app.try<{ markdown: string }>('thread/export', { threadId: t.id });
    return r?.markdown;
  }

  async search(q: string) {
    this.query = q;
    if (!q.trim()) {
      this.hits = [];
      return;
    }
    try {
      const r = await app.call<{ hits: SearchHit[] }>('thread/search', { query: q, limit: 30 });
      if (this.query === q) this.hits = r.hits ?? [];
    } catch {
      this.hits = [];
    }
  }

}

export const chat = new ChatState();
