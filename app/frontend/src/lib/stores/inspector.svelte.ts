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
}

/** The third column: files, diffs and long tool output, read-only. */
class Inspector {
  tabs = $state<InspectorTab[]>([]);
  activeId = $state<string | null>(null);

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

  close(id: string) {
    const i = this.tabs.findIndex((t) => t.id === id);
    this.tabs = this.tabs.filter((t) => t.id !== id);
    if (this.activeId === id) this.activeId = this.tabs[Math.max(0, i - 1)]?.id ?? null;
  }

  closeAll() {
    this.tabs = [];
    this.activeId = null;
  }

  async openTurnDetails(threadId: string, turnId: string): Promise<void> {
    const id = `turn-details:${turnId}`;
    const existing = this.tabs.find((tab) => tab.id === id);
    this.show({ ...existing, id, kind: 'turnDetails', title: 'Turn details', threadId, loading: true, error: undefined });
    const result = await app.try<TurnDetailsResult>('turn/details', { threadId, turnId });
    // Closing the tab while the request is in flight must not resurrect it.
    if (!this.tabs.some((tab) => tab.id === id)) return;
    this.tabs = this.tabs.map((tab) => tab.id !== id ? tab : result
      ? { ...tab, loading: false, error: undefined, turnDetails: result }
      : { ...tab, loading: false, error: 'Turn details are unavailable. Retry to reload this turn.' });
  }

  /** Open a file from the project, at whatever it says on disk now. */
  async openFile(path: string, threadId?: string, projectId?: string) {
    const resolvedProjectId = projectId ?? projects.activeId ?? undefined;
    const id = `file:${threadId ?? ''}:${path}`;
    this.show({ id, kind: 'file', title: path.split('/').pop() || path, path, threadId, projectId: resolvedProjectId, loading: true });
    projectId = resolvedProjectId;
    if (!projectId) return;
    const r = await app.try<ProjectFileContent>('project/readFile', { projectId, path, threadId });
    const tab: InspectorTab = { id, kind: 'file', title: path.split('/').pop() || path, path, threadId, projectId };
    if (!r) tab.error = 'This file could not be read.';
    else Object.assign(tab, { content: r.content, truncated: r.truncated, binary: r.binary });
    this.show(tab);
  }

  /** Open one file change (a unified diff) from a turn. */
  openDiff(change: FileChangeData) {
    const id = `diff:${change.turnId ?? ''}:${change.path}`;
    this.show({ id, kind: 'diff', title: change.path.split('/').pop() || change.path, path: change.path, change });
  }

  /** Open the full output of a tool call. */
  openOutput(title: string, text: string, id = 'out:' + title + ':' + text.length, focus = true) {
    const previous = this.activeId;
    this.show({ id, kind: 'output', title, text });
    if (!focus) this.activeId = previous ?? this.activeId;
  }

  updateOutput(id: string, text: string) {
    this.tabs = this.tabs.map((tab) => tab.id === id ? { ...tab, text } : tab);
  }

  openBrowserReport(report: BrowserVerificationResult, itemId: string, threadId?: string, projectId?: string, focus = true) {
    const id = `browser:${itemId}`;
    const previous = this.activeId;
    this.show({ id, kind: 'browser', title: 'Browser results', browser: report, threadId, projectId });
    if (!focus) this.activeId = previous ?? this.activeId;
  }

  async openArtifact(artifact: BrowserArtifact, threadId?: string, projectId?: string) {
    if (!artifact.mime_type?.startsWith('image/')) {
      await this.openFile(artifact.path, threadId, projectId);
      return;
    }
    const id = `artifact:${threadId ?? ''}:${artifact.path}`;
    this.show({ id, kind: 'artifact', title: artifact.path.split('/').pop() || artifact.path, path: artifact.path, artifact, threadId, projectId, loading: true });
    if (!projectId) return;
    const result = await app.try<ProjectArtifactContent>('project/readArtifact', { projectId, threadId, path: artifact.path });
    const tab: InspectorTab = { id, kind: 'artifact', title: artifact.path.split('/').pop() || artifact.path, path: artifact.path, artifact, threadId, projectId };
    if (!result) tab.error = 'This browser artifact could not be read.';
    else tab.dataUrl = `data:${result.mimeType};base64,${result.dataB64}`;
    this.show(tab);
  }

  openPreview(preview: PreviewSession, focus = true) {
    const id = `preview:${preview.id}`;
    const existing = this.tabs.find((t) => t.id === id);
    const tab: InspectorTab = {
      ...existing, id, kind: 'preview', title: preview.title || 'Preview', preview,
      text: preview.output ?? existing?.text ?? '', reload: existing?.reload ?? 0,
    };
    const previous = this.activeId;
    this.show(tab);
    if (!focus) this.activeId = previous ?? this.activeId;
  }

  private updatePreview(event: PreviewEvent) {
    const id = `preview:${event.preview.id}`;
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
