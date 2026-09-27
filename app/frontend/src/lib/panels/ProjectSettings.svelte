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

  async function setTool(key: 'shell' | 'network' | 'compute' | 'visualQa' | 'computerUse', value: boolean) {
    if (!p) return;
    await projects.update(p.id, { tools: { ...p.tools, [key]: value } });
  }

  // Comma-separated app names / bundle IDs Computer Use is allowed to
  // target in this project. Empty means unrestricted (every app allowed).
  let computerAppsDraft = $state('');
  $effect(() => {
    computerAppsDraft = (p?.tools.computerUseApps ?? []).join(', ');
  });
  async function setComputerApps() {
    if (!p) return;
    const list = computerAppsDraft
      .split(',')
      .map((s) => s.trim())
      .filter(Boolean);
    await projects.update(p.id, { tools: { ...p.tools, computerUseApps: list } });
  }

  async function setComputeLimit(key: 'computeVcpus' | 'computeMemoryMiB' | 'computeDiskMiB', value: number) {
    if (!p || !Number.isFinite(value)) return;
    await projects.update(p.id, { tools: { ...p.tools, [key]: value } });
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
    <h2 class="text-sm font-semibold mb-3">What the agent may do here</h2>
    <div class="space-y-3 text-sm">
      <label class="flex items-start gap-3">
        <input class="mt-1 shrink-0" type="checkbox" checked={p.tools.shell !== false} onchange={(e) => setTool('shell', e.currentTarget.checked)} />
        <span>
          Run shell commands
          <span class="block text-xs text-muted">They run in this folder, with no API keys in their environment.</span>
        </span>
      </label>
	  <label class="flex items-start gap-3">
		<input class="mt-1 shrink-0" type="checkbox" checked={!!p.tools.visualQa} onchange={(e) => setTool('visualQa', e.currentTarget.checked)} />
		<span>
		  Enable autonomous Visual QA
		  <span class="block text-xs text-muted">Lets the agent open this project's scoped preview in UMCode's isolated Chromium, interact with it, and capture screenshots, console errors, and failed requests. External sites and your normal browser profile are not exposed.</span>
		</span>
	  </label>
	  <label class="flex items-start gap-3">
		<input class="mt-1 shrink-0" type="checkbox" checked={!!p.tools.computerUse} onchange={(e) => setTool('computerUse', e.currentTarget.checked)} />
		<span>
		  Enable Computer Use
		  <span class="block text-xs text-muted">Lets the agent open an app you select, inspect its window, and—after approval—click, type, fill forms, press keys, and scroll. macOS keeps Screen Recording and Accessibility permission on a separately signed helper.</span>
		</span>
	  </label>
	  {#if p.tools.computerUse}
		<div class="pl-7">
		  <label class="text-xs text-muted">Allowed apps (optional)
			<input class="input mt-1 w-full py-1" type="text" placeholder="e.g. Safari, com.apple.mail" bind:value={computerAppsDraft} onblur={setComputerApps} onkeydown={(e) => e.key === 'Enter' && (e.currentTarget as HTMLInputElement).blur()} />
		  </label>
		  <span class="block text-xs text-muted mt-1">Comma-separated app names or bundle IDs. Leave blank to allow any app; the agent is refused for anything not on this list once it's set.</span>
		</div>
	  {/if}
      <label class="flex items-start gap-3">
        <input class="mt-1 shrink-0" type="checkbox" checked={!!p.tools.network} onchange={(e) => setTool('network', e.currentTarget.checked)} />
        <span>
          Allow shell commands to use the network
          <span class="block text-xs text-muted">For a hard network-off boundary, enable the microVM below; host-shell commands are blocked while network is off.</span>
        </span>
      </label>
      <label class="flex items-start gap-3">
        <input class="mt-1 shrink-0" type="checkbox" checked={!!p.tools.compute} onchange={(e) => setTool('compute', e.currentTarget.checked)} />
        <span>
          Run shell in a microVM
          <span class="block text-xs text-muted">Commands run in UMCode's bundled microVM. Only this chat's project folder or isolated worktree is mounted; the guest receives no provider credentials or host environment. Network stays off unless enabled above.</span>
        </span>
      </label>
      {#if p.tools.compute}
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

{/if}
