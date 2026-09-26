import { app } from './app.svelte';
import { errMsg } from '$lib/format';
import type { FileEntry, Project, ProjectFilesResult, ProjectTools } from '$lib/types';

const LAST_PROJECT = 'umcode.lastProject';

function remember(id: string | null) {
  try {
    if (id) localStorage.setItem(LAST_PROJECT, id);
    else localStorage.removeItem(LAST_PROJECT);
  } catch {
    /* private window, blocked storage: the project just isn't remembered */
  }
}

function lastProject(): string | null {
  try {
    return localStorage.getItem(LAST_PROJECT);
  } catch {
    return null;
  }
}

/** The projects the agent may work in, and which one is open. */
class ProjectStore {
  list = $state<Project[]>([]);
  activeId = $state<string | null>(null);
  loading = $state(false);
  /** Lazily loaded file tree, keyed by directory path ("" is the root). */
  tree = $state<Record<string, FileEntry[]>>({});
  expanded = $state<Record<string, boolean>>({});

  constructor() {
    app.rpc.on('project/updated', (p: { project: Project; deleted?: boolean }) => {
      if (p.deleted) {
        this.list = this.list.filter((x) => x.id !== p.project.id);
        if (this.activeId === p.project.id) this.open(this.list[0]?.id ?? null);
        return;
      }
      const i = this.list.findIndex((x) => x.id === p.project.id);
      if (i >= 0) this.list[i] = p.project;
      else this.list = [p.project, ...this.list];
    });
    app.onConnected(() => this.reload());
  }

  get active(): Project | undefined {
    return this.list.find((p) => p.id === this.activeId);
  }

  async reload() {
    this.loading = true;
    try {
      const r = await app.call<{ projects: Project[] }>('project/list', {});
      this.list = r.projects ?? [];
      const want = this.activeId ?? lastProject();
      const found = this.list.find((p) => p.id === want);
      await this.open(found?.id ?? this.list[0]?.id ?? null, false);
    } catch (e) {
      app.toast('error', errMsg(e));
    } finally {
      this.loading = false;
    }
  }

  /** Open a project (or none) and refresh what hangs off it. */
  async open(id: string | null, touch = true) {
    this.activeId = id;
    this.tree = {};
    this.expanded = {};
    remember(id);
    if (!id) return;
    if (touch) {
      const p = await app.try<Project>('project/open', { projectId: id });
      if (p) {
        const i = this.list.findIndex((x) => x.id === p.id);
        if (i >= 0) this.list[i] = p;
      }
    }
    await this.loadDir('');
  }

  async rename(id: string, name: string) {
    name = name.trim();
    if (!name) return;
    await this.update(id, { name });
  }

  async add(root: string, name?: string): Promise<Project | undefined> {
    const p = await app.try<Project>('project/create', { root, name });
    if (!p) return;
    if (!this.list.some((x) => x.id === p.id)) this.list = [p, ...this.list];
    await this.open(p.id);
    app.toast('info', `Opened ${p.name}.`);
    return p;
  }

  async update(id: string, patch: { name?: string; root?: string; settings?: unknown; tools?: ProjectTools; archived?: boolean }) {
    const p = await app.try<Project>('project/update', { projectId: id, ...patch });
    if (!p) return;
    const i = this.list.findIndex((x) => x.id === p.id);
    if (i >= 0) this.list[i] = p;
  }

  async remove(id: string) {
    const r = await app.try('project/delete', { projectId: id }, 'Project closed. Its folder and files are untouched.');
    if (r === undefined) return;
    this.list = this.list.filter((p) => p.id !== id);
    if (this.activeId === id) await this.open(this.list[0]?.id ?? null);
  }

  // ---- files ----

  async loadDir(path: string) {
    if (!this.activeId) return;
    const r = await app.try<ProjectFilesResult>('project/files', { projectId: this.activeId, path, depth: 1 });
    if (r) this.tree = { ...this.tree, [path]: r.entries ?? [] };
  }

  async toggleDir(path: string) {
    const open = !this.expanded[path];
    this.expanded = { ...this.expanded, [path]: open };
    if (open && !this.tree[path]) await this.loadDir(path);
  }

}

export const projects = new ProjectStore();
