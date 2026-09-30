<script lang="ts">
  import { FolderGit2, AlertTriangle, Plus, Trash2, Pencil } from '@lucide/svelte';
  import { projects } from '$lib/stores/projects.svelte';
  import { chat } from '$lib/stores/chat.svelte';
  import { app } from '$lib/stores/app.svelte';
  import { dialog } from '$lib/stores/dialog.svelte';
  import ModelPicker from '$lib/components/ModelPicker.svelte';
  import type { ModelSelection } from '$lib/types';
  import { createProject, editProject } from '$lib/createProject';

  const p = $derived(projects.active);
  let computerUseDefault = $state(true);

  $effect(() => {
    void app.conn;
    if (app.conn === 'open') {
      void app.try<{ enabled: boolean }>('settings/computerUse/getDefault', {}).then((r) => {
        if (r) computerUseDefault = r.enabled;
      });
    }
  });

  async function setTool(key: 'network' | 'compute', value: boolean) {
    if (!p) return;
    await projects.update(p.id, { tools: { ...p.tools, [key]: value } });
  }

  async function setComputeLimit(key: 'computeVcpus' | 'computeMemoryMiB' | 'computeDiskMiB', value: number) {
    if (!p || !Number.isFinite(value)) return;
    await projects.update(p.id, { tools: { ...p.tools, [key]: value } });
  }

  async function setComputerUse(value: string) {
    if (!p) return;
    const tools = { ...p.tools };
    if (value === 'default') delete tools.computerUse;
    else tools.computerUse = value === 'enabled';
    await projects.update(p.id, { tools });
  }

  async function setSettings(sel: ModelSelection) {
    if (!p) return;
    await projects.update(p.id, { settings: sel });
  }

  async function close() {
    if (!p) return;
    const ok = await dialog.confirm(
      `Close the project “${p.name}”? Its chats stay, and nothing on disk is touched.`,
      { okLabel: 'Close project', danger: true },
    );
    if (ok) {
      await projects.remove(p.id);
      chat.newChat();
      await chat.loadThreads();
      app.view = 'chat';
    }
  }

  async function addProject() {
    await createProject();
    await chat.loadThreads();
  }

  async function editDetails() {
    if (p) await editProject(p);
  }
</script>

{#if !p}
  <div class="flex items-center gap-3 mb-5">
    <div>
      <h1 class="page-title">Projects</h1>
      <p class="text-xs text-muted mt-1">Each project is a folder with its own chats, tools, and instructions.</p>
    </div>
    <button class="btn-primary ml-auto" onclick={addProject}><Plus class="w-4 h-4" />New project</button>
  </div>
  {#if projects.list.length === 0}
    <div class="card p-10 text-center">
      <FolderGit2 class="w-8 h-8 mx-auto text-muted mb-3" />
      <p class="text-sm text-ink-soft">No projects yet</p>
      <p class="text-xs text-muted mt-1">Choose a folder and name it. Chats edit this folder by default; Git projects can opt into isolated worktrees.</p>
    </div>
  {:else}
    <div class="grid grid-cols-1 sm:grid-cols-2 gap-3">
      {#each projects.list as project (project.id)}
        <button class="card p-4 text-left hover:border-accent transition-colors" onclick={() => projects.open(project.id)}>
          <div class="flex items-center gap-2">
            <FolderGit2 class="w-4 h-4 text-accent" />
            <span class="font-medium text-sm truncate">{project.name}</span>
            <span class="ml-auto text-xs text-muted">{project.threads} chats</span>
          </div>
          <p class="text-[11px] text-muted font-mono truncate mt-2">{project.root}</p>
          {#if project.vcs?.branch}<p class="text-[11px] text-faint mt-1">branch {project.vcs.branch}</p>{/if}
        </button>
      {/each}
    </div>
  {/if}
{:else}
  <div class="flex items-center gap-3 mb-5">
    <div class="w-9 h-9 rounded-lg bg-clay text-white flex items-center justify-center font-semibold">
      {p.name.slice(0, 1).toUpperCase()}
    </div>
    <div class="min-w-0">
      <div class="text-[10px] font-semibold uppercase tracking-[0.08em] text-muted">Project settings</div>
      <h1 class="page-title truncate">{p.name}</h1>
      <p class="text-xs text-muted font-mono truncate selectable">{p.root}</p>
    </div>
    <div class="ml-auto flex shrink-0 items-center gap-2">
      <button class="btn-outline btn-sm" onclick={editDetails}><Pencil class="w-3.5 h-3.5" />Edit details</button>
      <button class="btn-danger btn-sm" onclick={close}><Trash2 class="w-3.5 h-3.5" />Close project</button>
    </div>
  </div>

  {#if p.missing}
    <div class="card p-3 mb-4 flex items-center gap-2 text-sm text-rust">
      <AlertTriangle class="w-4 h-4" />This folder is gone from disk. Chats in it can still be read, but nothing can run.
    </div>
  {/if}

  <div class="grid grid-cols-2 gap-3 mb-5">
    <div class="card p-3">
      <div class="text-xs text-muted mb-1">Version control</div>
      {#if p.vcs}
        <div class="flex items-center gap-2 text-sm">
          <FolderGit2 class="w-4 h-4 text-muted" />
          <span>{p.vcs.branch || 'git'}</span>
          {#if p.vcs.dirty > 0}<span class="text-amber-warm">· {p.vcs.dirty} changed files</span>{/if}
        </div>
        {#if p.vcs.remote}<div class="text-[11px] text-faint truncate mt-1 selectable">{p.vcs.remote}</div>{/if}
      {:else}
        <div class="text-sm text-muted">Not a git repository.</div>
      {/if}
    </div>
    <div class="card p-3">
      <div class="text-xs text-muted mb-1">Chats in this project</div>
      <div class="text-sm">{p.threads}</div>
    </div>
  </div>

  <section class="card p-4 mb-5">
    <h2 class="text-sm font-semibold mb-3">Compute</h2>
    <p class="text-xs text-muted mb-3">First-party tools are available in chats. Risky actions ask for approval or follow the autonomy choice in that chat.</p>
    <div class="space-y-3 text-sm">
      <label class="flex items-start gap-3">
        <input class="mt-1 shrink-0" type="checkbox" checked={!!p.tools.compute} onchange={(e) => setTool('compute', e.currentTarget.checked)} />
        <span>
          Run commands in the isolated microVM
          <span class="block text-xs text-muted">Only this chat's project folder or isolated worktree is mounted; the guest receives no provider credentials or host environment.</span>
        </span>
      </label>
      {#if p.tools.compute}
        <label class="flex items-start gap-3 pl-7">
          <input class="mt-1 shrink-0" type="checkbox" checked={!!p.tools.network} onchange={(e) => setTool('network', e.currentTarget.checked)} />
          <span>
            Allow this microVM to use the network
            <span class="block text-xs text-muted">Network access is off unless enabled here.</span>
          </span>
        </label>
        <div class="grid grid-cols-1 sm:grid-cols-3 gap-3 pl-7">
          <label class="min-w-0 text-xs text-muted">vCPU (1–8)
            <input class="input mt-1 w-full py-1" type="number" min="1" max="8" step="1" value={p.tools.computeVcpus ?? 4} onchange={(e) => setComputeLimit('computeVcpus', Number(e.currentTarget.value))} />
          </label>
          <label class="min-w-0 text-xs text-muted">Memory MiB (512–8192)
            <input class="input mt-1 w-full py-1" type="number" min="512" max="8192" step="512" value={p.tools.computeMemoryMiB ?? 4096} onchange={(e) => setComputeLimit('computeMemoryMiB', Number(e.currentTarget.value))} />
          </label>
          <label class="min-w-0 text-xs text-muted">Workspace MiB (1024–16384)
            <input class="input mt-1 w-full py-1" type="number" min="1024" max="16384" step="1024" value={p.tools.computeDiskMiB ?? 2048} onchange={(e) => setComputeLimit('computeDiskMiB', Number(e.currentTarget.value))} />
          </label>
        </div>
        <p class="pl-7 text-[11px] text-muted">Memory and CPU are enforced by the VM. Workspace size is monitored during commands and stops the VM at the configured ceiling.</p>
      {/if}
      <div>
        <span class="label">Default model for chats here</span>
        <ModelPicker value={p.settings} onchange={setSettings} />
      </div>
    </div>
  </section>

  <section class="card p-4 mb-5">
    <h2 class="text-sm font-semibold mb-2">Computer Use</h2>
    <p class="text-xs text-muted mb-3">Interact with a selected desktop app. Agent actions follow this chat’s approval choice; your direct clicks in the live view are explicit user actions.</p>
    <div class="flex flex-wrap items-center gap-3">
      <label class="text-xs text-muted" for="computer-use-project">Availability in this project</label>
      <select id="computer-use-project" class="input py-1 w-auto" value={p.tools.computerUse === undefined ? 'default' : p.tools.computerUse ? 'enabled' : 'disabled'} onchange={(e) => setComputerUse(e.currentTarget.value)}>
        <option value="default">Use global default ({computerUseDefault ? 'enabled' : 'disabled'})</option>
        <option value="enabled">Enabled for this project</option>
        <option value="disabled">Disabled for this project</option>
      </select>
    </div>
  </section>

{/if}
