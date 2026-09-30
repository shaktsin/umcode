<script lang="ts">
  import { RefreshCw, Trash2, Download, RotateCw, MonitorCheck } from '@lucide/svelte';
  import { app } from '$lib/stores/app.svelte';
  import { errMsg } from '$lib/format';
  import { dialog } from '$lib/stores/dialog.svelte';
  import type { MCPServer, SkillInfo, ToolInfo } from '$lib/types';

  let skills = $state<SkillInfo[]>([]);
  let dirs = $state<string[]>([]);
  let servers = $state<MCPServer[]>([]);
  let tools = $state<ToolInfo[]>([]);
  let source = $state('');
  let installName = $state('');
  let installing = $state(false);
  let detail = $state<{ name: string; text: string } | null>(null);

  async function load() {
    const [s, m, t] = await Promise.all([
      app.try<{ skills: SkillInfo[]; dirs: string[] }>('skill/list'),
      app.try<{ servers: MCPServer[] }>('mcp/list'),
      app.try<{ tools: ToolInfo[] }>('tool/list'),
    ]);
    if (s) { skills = s.skills; dirs = s.dirs; }
    if (m) servers = m.servers;
    if (t) tools = t.tools;
  }

  $effect(() => {
    if (app.conn === 'open') void load();
  });

  async function install() {
    installing = true;
    try {
      const r = await app.call<SkillInfo>('skill/install', { source: source.trim(), name: installName.trim() || undefined }, 300_000);
      app.toast('info', `Installed ${r.name}.`);
      source = installName = '';
      await load();
    } catch (e) {
      app.toast('error', errMsg(e));
    } finally {
      installing = false;
    }
  }

  async function remove(s: SkillInfo) {
    if (!(await dialog.confirm(`Remove the skill “${s.name}”? Its folder will be deleted.`, { okLabel: 'Remove', danger: true }))) return;
    await app.try('skill/remove', { name: s.name }, `Removed ${s.name}.`);
    await load();
  }

  async function showSkill(s: SkillInfo) {
    if (detail?.name === s.name) { detail = null; return; }
    const r = await app.try<{ instructions: string }>('skill/get', { name: s.name });
    if (r) detail = { name: s.name, text: r.instructions };
  }

  async function restart(name: string) {
    await app.try('mcp/restart', { name }, `Restarted ${name}.`);
    await load();
  }

  const toolGroups = $derived.by(() => {
    const g: Record<string, ToolInfo[]> = {};
    for (const t of tools) (g[t.source] ??= []).push(t);
    return Object.entries(g);
  });
</script>

<div class="flex items-center mb-4">
  <h1 class="page-title">Plugins</h1>
  <button class="btn-ghost btn-sm ml-2" aria-label="Refresh" onclick={load}><RefreshCw class="w-3.5 h-3.5" /></button>
</div>

<section class="mb-8">
  <h2 class="text-sm font-semibold text-ink-soft mb-2">Built-in plugins</h2>
  <div class="grid gap-2">
  <div class="card p-3 flex items-start gap-3">
    <div class="w-9 h-9 rounded-lg bg-raised flex items-center justify-center text-clay"><MonitorCheck class="w-5 h-5" /></div>
    <div class="flex-1 min-w-0">
      <div class="flex items-center gap-2"><span class="text-sm font-medium">Visual QA</span><span class="pill bg-raised text-muted">opt-in</span></div>
      <p class="text-xs text-muted mt-1">Controls an isolated, app-managed Chromium against a task's scoped live preview. It captures rendered screenshots, page controls, console errors, failed requests, and user-flow evidence.</p>
      <p class="text-[11px] text-faint mt-1">Enable it per project under Project settings. It never uses your normal browser profile and blocks navigation outside the preview origin.</p>
    </div>
  </div>
  <div class="card p-3 flex items-start gap-3">
    <div class="w-9 h-9 rounded-lg bg-raised flex items-center justify-center text-clay"><MonitorCheck class="w-5 h-5" /></div>
    <div class="flex-1 min-w-0">
      <div class="flex items-center gap-2"><span class="text-sm font-medium">Computer Use</span><span class="pill bg-raised text-muted">opt-in · macOS</span></div>
      <p class="text-xs text-muted mt-1">Controls an explicitly selected desktop app or browser with a visible pointer, accessibility-aware targeting, and screenshot-based click, fill, type, key, and scroll actions.</p>
      <p class="text-[11px] text-faint mt-1">Actions require approval. UMCode uses its own macOS Screen Recording and Accessibility permissions; stopping a session never quits your app.</p>
    </div>
  </div>
  </div>
</section>

<section class="mb-8">
  <h2 class="text-sm font-semibold text-ink-soft mb-2">Skills</h2>
  <div class="card p-3 mb-3 flex gap-2 items-end">
    <div class="flex-1"><label class="label" for="sk-src">Install from a Git URL or a local folder</label><input id="sk-src" class="input" bind:value={source} placeholder="https://github.com/you/my-skill.git or ~/skills/my-skill" /></div>
    <div class="w-40"><label class="label" for="sk-name">Name (optional)</label><input id="sk-name" class="input" bind:value={installName} /></div>
    <button class="btn-primary" disabled={installing || !source.trim()} onclick={install}><Download class="w-4 h-4" />{installing ? 'Installing…' : 'Install'}</button>
  </div>
  {#if skills.length === 0}
    <p class="text-sm text-muted">No skills installed. Skill folders: {dirs.join(', ') || 'none'}</p>
  {/if}
  <div class="grid gap-2">
    {#each skills as s (s.name + s.dir)}
      <div class="card p-3">
        <div class="flex items-start gap-3">
          <div class="flex-1 min-w-0">
            <div class="flex items-center gap-2">
              <button class="text-sm font-medium text-ink hover:underline" onclick={() => showSkill(s)}>{s.name}</button>
              {#if s.version}<span class="text-[10px] text-muted">v{s.version}</span>{/if}
              <span class="pill bg-raised text-muted">{s.runtime}</span>
              {#if s.riskLevel}<span class="risk-{s.riskLevel}">{s.riskLevel}</span>{/if}
            </div>
            <p class="text-xs text-muted mt-0.5">{s.description}</p>
            {#if s.scripts?.length}<p class="text-[11px] text-muted mt-1 font-mono">{s.scripts.join(' · ')}</p>{/if}
            {#if s.error}<p class="text-xs text-rust mt-1">{s.error}</p>{/if}
            <p class="text-[10px] text-faint mt-1 font-mono selectable">{s.dir}</p>
          </div>
          {#if s.removable}
            <button class="btn-danger btn-sm" aria-label={`Remove ${s.name}`} onclick={() => remove(s)}><Trash2 class="w-3.5 h-3.5" /></button>
          {/if}
        </div>
        {#if detail?.name === s.name}
          <pre class="mt-2 text-[11px] text-muted bg-paper rounded p-2 whitespace-pre-wrap max-h-80 overflow-auto">{detail.text}</pre>
        {/if}
      </div>
    {/each}
  </div>
</section>

<section class="mb-8">
  <h2 class="text-sm font-semibold text-ink-soft mb-2">MCP servers</h2>
  {#if servers.length === 0}
    <p class="text-sm text-muted">No MCP servers configured. Add them under <code class="font-mono">mcp_servers</code> in config.yaml.</p>
  {/if}
  <div class="grid gap-2">
    {#each servers as m (m.name)}
      <div class="card p-3 flex items-start gap-3">
        <span class="mt-1.5 w-2 h-2 rounded-full {m.status === 'ready' ? 'bg-sage' : m.status === 'starting' ? 'bg-amber-warm' : m.status === 'failed' ? 'bg-rust' : 'bg-faint'}"></span>
        <div class="flex-1 min-w-0">
          <div class="text-sm text-ink">{m.name} <span class="text-xs text-muted">{m.transport} · {m.status}</span></div>
          {#if m.serverName}<div class="text-xs text-muted">{m.serverName} {m.serverVersion}</div>{/if}
          {#if m.error}<div class="text-xs text-rust selectable">{m.error}</div>{/if}
          {#if m.tools?.length}<div class="text-[11px] text-muted font-mono mt-1">{m.tools.join(' · ')}</div>{/if}
        </div>
        <button class="btn-ghost btn-sm" onclick={() => restart(m.name)}><RotateCw class="w-3.5 h-3.5" />Restart</button>
      </div>
    {/each}
  </div>
</section>

<section>
  <h2 class="text-sm font-semibold text-ink-soft mb-2">Tools available to the agent ({tools.length})</h2>
  {#each toolGroups as [src, list]}
    <details class="card mb-2">
      <summary class="px-3 py-2 text-sm text-ink-soft cursor-default">{src} <span class="text-muted">({list.length})</span></summary>
      <div class="px-3 pb-2 space-y-1">
        {#each list as t}
          <div class="text-xs"><code class="font-mono text-ink">{t.name}</code> <span class="text-muted">{t.description}</span></div>
        {/each}
      </div>
    </details>
  {/each}
</section>
