import { app } from './app.svelte';
import { projects } from './projects.svelte';
import type { BrowserArtifact, BrowserVerificationResult, FileChangeData, PreviewEvent, PreviewSession, ProjectArtifactContent, ProjectFileContent, RevertResult } from '$lib/types';
import type { TurnDetailsResult } from '$lib/turnDetails';

export type TabKind = 'turnDetails' | 'file' | 'diff' | 'output' | 'preview' | 'browser' | 'artifact';

export interface InspectorTab {
  id: string;
  kind: TabKind;
  title: string;
  path?: string;
  projectId?: string;
  threadId?: string;
  turnDetails?: TurnDetailsResult;
  /** file */
  content?: string;
  truncated?: boolean;
  binary?: boolean;
  /** diff */
  change?: FileChangeData;
  /** tool output */
  text?: string;
  /** live preview */
  preview?: PreviewSession;
  reload?: number;
  browser?: BrowserVerificationResult;
  artifact?: BrowserArtifact;
  dataUrl?: string;
  loading?: boolean;
  error?: string;
  sourceItemId?: string;
}

/** Keep parent segments unresolved: collapsing them can change symlink meaning. */
function canonicalPath(path: string): string {
  const prefix = path.startsWith('//') ? '//' : path.startsWith('/') ? '/' : '';
  return prefix + path.split('/').filter((part) => part !== '' && part !== '.').join('/');
}

function scopedId(kind: string, projectId: string | undefined, threadId: string | undefined, resource: string): string {
  return JSON.stringify([kind, projectId ?? '', threadId ?? '', resource]);
}

/** The third column: files, diffs and long tool output, read-only. */
class Inspector {
  tabs = $state<InspectorTab[]>([]);
  activeId = $state<string | null>(null);
  private requests = new Map<string, symbol>();

  constructor() {
    app.rpc.on('preview/started', (event: PreviewEvent) => this.openPreview(event.preview, true));
    app.rpc.on('preview/output', (event: PreviewEvent) => this.updatePreview(event));
    app.rpc.on('preview/stopped', (event: PreviewEvent) => this.updatePreview(event));
    app.onConnected(async () => {
      const r = await app.call<{ previews: PreviewSession[] }>('preview/list').catch(() => ({ previews: [] }));
      for (const preview of r.previews ?? []) this.openPreview(preview, false);
    });
  }

  get open(): boolean {
    return this.tabs.length > 0;
  }

  get active(): InspectorTab | undefined {
    return this.tabs.find((t) => t.id === this.activeId);
  }

  private show(tab: InspectorTab) {
    const i = this.tabs.findIndex((t) => t.id === tab.id);
    if (i >= 0) this.tabs[i] = tab;
    else this.tabs = [...this.tabs, tab];
    this.activeId = tab.id;
  }

  private beginRead(tab: InspectorTab): symbol {
    const token = Symbol();
    this.requests.set(tab.id, token);
    this.show(tab);
    return token;
  }

  private finishRead(id: string, token: symbol, patch: Partial<InspectorTab>) {
    if (this.requests.get(id) !== token) return;
    this.requests.delete(id);
    this.tabs = this.tabs.map((tab) => tab.id === id ? { ...tab, ...patch, loading: false } : tab);
  }

  close(id: string) {
    this.requests.delete(id);
    const i = this.tabs.findIndex((t) => t.id === id);
    this.tabs = this.tabs.filter((t) => t.id !== id);
    if (this.activeId === id) this.activeId = this.tabs[Math.max(0, i - 1)]?.id ?? null;
  }

  closeAll() {
    this.requests.clear();
    this.tabs = [];
    this.activeId = null;
  }

  async openTurnDetails(threadId: string, turnId: string, projectId?: string): Promise<void> {
    const id = scopedId('turn-details', projectId, threadId, turnId);
    const existing = this.tabs.find((tab) => tab.id === id);
    const token = this.beginRead({ ...existing, id, kind: 'turnDetails', title: 'Turn details', threadId, projectId, loading: true, error: undefined });
    const result = await app.try<TurnDetailsResult>('turn/details', { threadId, turnId });
    this.finishRead(id, token, result
      ? { error: undefined, turnDetails: result }
      : { error: 'Turn details are unavailable. Retry to reload this turn.' });
  }

  /** Open a file from the project, at whatever it says on disk now. */
  async openFile(path: string, threadId?: string, projectId?: string) {
    projectId = projectId ?? projects.activeId ?? undefined;
    path = canonicalPath(path);
    const id = scopedId('file', projectId, threadId, path);
    const token = this.beginRead({ id, kind: 'file', title: path.split('/').pop() || path, path, threadId, projectId, loading: true });
    if (!projectId) {
      this.finishRead(id, token, { error: 'Select a project to read this file.' });
      return;
    }
    const result = await app.try<ProjectFileContent>('project/readFile', { projectId, path, threadId });
    this.finishRead(id, token, result
      ? { content: result.content, truncated: result.truncated, binary: result.binary }
      : { error: 'This file could not be read.' });
  }

  /** Open one file change (a unified diff) from a turn. */
  openDiff(change: FileChangeData, threadId?: string, projectId?: string) {
    const path = canonicalPath(change.path);
    projectId = projectId ?? projects.activeId ?? undefined;
    const id = scopedId('diff', projectId, threadId, JSON.stringify([change.turnId ?? '', path]));
    this.show({ id, kind: 'diff', title: path.split('/').pop() || path, path, change, threadId, projectId });
  }

  /** Open the full output of a tool call. */
  openOutput(title: string, text: string, id = scopedId('output', undefined, undefined, title), focus = true) {
    const previous = this.activeId;
    this.show({ id, kind: 'output', title, text });
    if (!focus) this.activeId = previous ?? this.activeId;
  }

  updateOutput(id: string, text: string) {
    this.tabs = this.tabs.map((tab) => tab.id === id ? { ...tab, text } : tab);
  }

  openBrowserReport(report: BrowserVerificationResult, itemId: string, threadId?: string, projectId?: string, focus = true) {
    const id = scopedId('browser', projectId, threadId, itemId);
    const previous = this.activeId;
    this.show({ id, kind: 'browser', title: 'Browser results', browser: report, threadId, projectId });
    if (!focus) this.activeId = previous ?? this.activeId;
  }

  /** Automatic output occupies one tab per scoped tool role. */
  openVerificationOutput(role: string, text: string, itemId: string, threadId: string, projectId?: string) {
    const id = scopedId('verification', projectId, threadId, role);
    const title = role === 'verification.plan' ? 'Verification plan' : role === 'verification.run' ? 'Test output' : 'Browser verification';
    this.show({ id, kind: 'output', title, text, threadId, projectId, sourceItemId: itemId });
  }

  updateVerificationOutput(role: string, text: string, itemId: string, threadId: string, projectId?: string, report?: BrowserVerificationResult) {
    // The originating view may have been unloaded before completion. In that
    // case the open tab retains the authoritative project scope.
    this.tabs = this.tabs.map((tab) => tab.id === scopedId('verification', tab.projectId, threadId, role)
      && (projectId === undefined || tab.projectId === projectId) && tab.sourceItemId === itemId
      ? { ...tab, text, ...(report ? { kind: 'browser' as const, browser: report } : {}) }
      : tab);
  }

  /** Explicit historical output stays separate from the automatic latest report. */
  openToolOutput(title: string, text: string, itemId: string, threadId: string, projectId?: string) {
    const id = scopedId('tool-output', projectId, threadId, itemId);
    this.show({ id, kind: 'output', title, text, threadId, projectId, sourceItemId: itemId });
  }

  async openArtifact(artifact: BrowserArtifact, threadId?: string, projectId?: string) {
    if (!artifact.mime_type?.startsWith('image/')) {
      await this.openFile(artifact.path, threadId, projectId);
      return;
    }
    projectId = projectId ?? projects.activeId ?? undefined;
    const path = canonicalPath(artifact.path);
    const id = scopedId('artifact', projectId, threadId, path);
    const token = this.beginRead({ id, kind: 'artifact', title: path.split('/').pop() || path, path, artifact, threadId, projectId, loading: true });
    if (!projectId) {
      this.finishRead(id, token, { error: 'Select a project to read this artifact.' });
      return;
    }
    const result = await app.try<ProjectArtifactContent>('project/readArtifact', { projectId, threadId, path });
    this.finishRead(id, token, result
      ? { dataUrl: `data:${result.mimeType};base64,${result.dataB64}` }
      : { error: 'This browser artifact could not be read.' });
  }

  openPreview(preview: PreviewSession, focus = true) {
    const id = scopedId('preview', preview.projectId, preview.threadId, preview.id);
    const existing = this.tabs.find((t) => t.id === id);
    const tab: InspectorTab = {
      ...existing, id, kind: 'preview', title: preview.title || 'Preview', preview, threadId: preview.threadId, projectId: preview.projectId,
      text: preview.output ?? existing?.text ?? '', reload: existing?.reload ?? 0,
    };
    const previous = this.activeId;
    this.show(tab);
    if (!focus) this.activeId = previous ?? this.activeId;
  }

  private updatePreview(event: PreviewEvent) {
    const id = scopedId('preview', event.preview.projectId, event.preview.threadId, event.preview.id);
    const existing = this.tabs.find((t) => t.id === id);
    if (!existing) return;
    const text = (existing.text ?? '') + (event.output ?? '');
    const error = event.error || existing.error;
    this.tabs = this.tabs.map((t) => t.id === id ? { ...t, preview: event.preview, text, error } : t);
  }

  reloadPreview(id: string) {
    this.tabs = this.tabs.map((t) => t.id === id ? { ...t, reload: (t.reload ?? 0) + 1 } : t);
  }

  async stopPreview(previewId: string) {
    await app.try('preview/stop', { previewId });
  }

  /** Undo the file changes of a turn, then refresh anything open. */
  async revertTurn(turnId: string, paths?: string[]) {
    const r = await app.try<RevertResult>('project/revertTurn', { turnId, paths });
    if (!r) return;
    if (r.reverted.length === 0) {
      app.toast('warn', r.reason || 'Nothing could be reverted.');
    } else {
      app.toast('info', `Put back ${r.reverted.join(', ')}.`);
    }
    if (r.reason && r.skipped?.length) app.toast('warn', r.reason);
    for (const tab of this.tabs) {
      if (tab.kind === 'file' && tab.path && r.reverted.includes(tab.path)) await this.openFile(tab.path, tab.threadId, tab.projectId);
    }
    await projects.loadDir('');
  }
}

export const inspector = new Inspector();
