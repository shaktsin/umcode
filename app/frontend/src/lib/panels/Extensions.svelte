<script lang="ts">
  import { Download, PackageOpen, RefreshCw, RotateCw, Search, Settings2, ShieldCheck, Trash2 } from '@lucide/svelte';
  import PluginInventory from '$lib/components/PluginInventory.svelte';
  import { errMsg } from '$lib/format';
  import { createPluginClient } from '$lib/plugins';
  import { app } from '$lib/stores/app.svelte';
  import { dialog } from '$lib/stores/dialog.svelte';
  import { projects } from '$lib/stores/projects.svelte';
  import type { PluginInfo, PluginInspection } from '$lib/types';

  const client = createPluginClient((method, params) => app.call(method, params, 300_000));
  let plugins = $state<PluginInfo[]>([]);
  let source = $state('');
  let mode = $state<'managed' | 'linked'>('managed');
  let inspection = $state<PluginInspection | null>(null);
  let busy = $state('');
  let editing = $state<string | null>(null);
  let settingsDraft = $state<Record<string, string>>({});
  let secretDraft = $state<Record<string, string>>({});

  async function load() {
    try { plugins = await client.list(projects.activeId ?? undefined); }
    catch (error) { app.toast('error', errMsg(error)); }
  }

  $effect(() => {
    const connected = app.conn === 'open';
    const project = projects.activeId;
    if (connected) void load();
    void project;
  });

  async function inspectSource() {
    busy = 'inspect'; inspection = null;
    try { inspection = await client.inspect(source.trim(), mode); }
    catch (error) { app.toast('error', errMsg(error)); }
    finally { busy = ''; }
  }

  async function installInspected() {
    if (!inspection) return;
    busy = 'install';
    try {
      const installed = await client.install(inspection.token, projects.activeId ?? undefined);
      app.toast('info', `Installed ${installed.name}.`);
      source = ''; inspection = null; await load();
    } catch (error) { app.toast('error', errMsg(error)); }
    finally { busy = ''; }
  }

  async function setEnabled(plugin: PluginInfo, enabled: boolean) {
    if (!projects.activeId) return;
    busy = plugin.id;
    try { await client.setEnabled(plugin.id, projects.activeId, enabled); await load(); }
    catch (error) { app.toast('error', errMsg(error)); }
    finally { busy = ''; }
  }

  function beginConfigure(plugin: PluginInfo) {
    if (editing === plugin.id) { editing = null; return; }
    editing = plugin.id; settingsDraft = {}; secretDraft = {};
    for (const setting of plugin.schema) {
      if (setting.secret) secretDraft[setting.name] = '';
      else settingsDraft[setting.name] = String(plugin.settings[setting.name] ?? '');
    }
  }

  async function saveConfiguration(plugin: PluginInfo) {
    if (!projects.activeId) return;
    busy = plugin.id;
    try {
      const changedSecrets = Object.fromEntries(Object.entries(secretDraft).filter(([, value]) => value !== ''));
      await client.configure(plugin.id, projects.activeId, settingsDraft, changedSecrets);
      editing = null; secretDraft = {};
      app.toast('info', `Saved ${plugin.name} configuration.`); await load();
    } catch (error) { app.toast('error', errMsg(error)); }
    finally { busy = ''; }
  }

  async function reload(plugin: PluginInfo) {
    busy = plugin.id;
    try { await client.reload(plugin.id); app.toast('info', `Reloaded ${plugin.name}.`); await load(); }
    catch (error) { app.toast('error', errMsg(error)); }
    finally { busy = ''; }
  }

  async function remove(plugin: PluginInfo) {
    const extra = plugin.enabled ? ' It will also be disabled for projects that use it.' : '';
    if (!(await dialog.confirm(`Remove “${plugin.name}”?${extra}`, { okLabel: 'Remove', danger: true }))) return;
    busy = plugin.id;
    try { await client.uninstall(plugin.id, plugin.enabled); app.toast('info', `Removed ${plugin.name}.`); await load(); }
    catch (error) { app.toast('error', errMsg(error)); }
    finally { busy = ''; }
  }

  function health(plugin: PluginInfo) {
    if (plugin.diagnostics.some((item) => item.severity === 'error')) return { label: 'Needs attention', style: 'bg-rust-soft text-rust' };
    if (plugin.diagnostics.length) return { label: 'Compatible with notes', style: 'bg-clay-soft/40 text-amber-warm' };
    return { label: 'Ready', style: 'bg-sage-soft text-sage' };
  }
</script>

<div class="mb-5 flex items-start justify-between gap-4">
  <div><h1 class="page-title">Plugins</h1><p class="mt-1 max-w-2xl text-xs text-muted">Install one package that can carry skills, MCP integrations, hooks, and compatibility metadata.</p></div>
  <button class="btn-ghost btn-sm" aria-label="Refresh plugins" onclick={load}><RefreshCw class="h-3.5 w-3.5" />Refresh</button>
</div>

<section class="card mb-6 overflow-hidden">
  <div class="border-b border-line px-4 py-3">
    <div class="flex items-center gap-2 text-sm font-semibold text-ink"><PackageOpen class="h-4 w-4 text-clay" /> Add a plugin</div>
    <p class="mt-0.5 text-xs text-muted">Inspect the package and its executable capabilities before installing it.</p>
  </div>
  <div class="grid gap-3 p-4 md:grid-cols-[minmax(0,1fr)_9rem_auto] md:items-end">
    <div><label class="label" for="plugin-source">Git URL or local folder</label><input id="plugin-source" class="input" bind:value={source} placeholder="https://github.com/team/plugin.git or /path/to/plugin" oninput={() => inspection = null} /></div>
    <div><label class="label" for="plugin-mode">Install mode</label><select id="plugin-mode" class="select h-[38px] w-full" bind:value={mode} onchange={() => inspection = null}><option value="managed">Managed copy</option><option value="linked">Linked source</option></select></div>
    <button class="btn-outline h-[38px]" disabled={!source.trim() || !!busy} onclick={inspectSource}><Search class="h-4 w-4" />{busy === 'inspect' ? 'Inspecting…' : 'Inspect'}</button>
  </div>
  {#if inspection}
    <div class="border-t border-line bg-raised/45 p-4">
      <div class="mb-3 flex flex-wrap items-start justify-between gap-3">
        <div><div class="flex items-center gap-2"><span class="font-semibold text-ink">{inspection.plugin.name}</span>{#if inspection.plugin.version}<span class="text-xs text-muted">v{inspection.plugin.version}</span>{/if}<span class="pill bg-paper text-muted">{inspection.plugin.format}</span></div><p class="mt-1 font-mono text-[10px] text-faint">{inspection.source}</p></div>
        <div class="flex items-center gap-2">{#if inspection.requiresApproval}<span class="pill bg-clay-soft/40 text-amber-warm">Executable capabilities</span>{/if}<button class="btn-primary" disabled={!!busy} onclick={installInspected}><Download class="h-4 w-4" />{busy === 'install' ? 'Installing…' : 'Install'}</button></div>
      </div>
      <PluginInventory plugin={inspection.plugin} />
      {#each inspection.diagnostics as diagnostic}<div class="mt-2 rounded-lg border border-line bg-paper px-3 py-2 text-xs text-ink-soft"><span class="font-medium">{diagnostic.message}</span>{#if diagnostic.remediation}<span class="text-muted"> — {diagnostic.remediation}</span>{/if}</div>{/each}
    </div>
  {/if}
</section>

<div class="mb-2 flex items-center justify-between"><h2 class="text-sm font-semibold text-ink-soft">Installed packages</h2>{#if projects.active}<span class="text-[11px] text-muted">Enablement for {projects.active.name}</span>{:else}<span class="text-[11px] text-amber-warm">Open a project to manage enablement</span>{/if}</div>

{#if plugins.length === 0}
  <div class="rounded-xl border border-dashed border-line-strong px-5 py-8 text-center"><ShieldCheck class="mx-auto mb-2 h-6 w-6 text-faint" /><p class="text-sm font-medium text-ink-soft">No plugins installed</p><p class="mt-1 text-xs text-muted">Add a package above. You’ll review every capability before installation.</p></div>
{:else}
  <div class="grid gap-3">
    {#each plugins as plugin (plugin.id)}
      {@const state = health(plugin)}
      <article class="card overflow-hidden">
        <div class="flex flex-wrap items-start gap-3 p-4">
          <div class="min-w-0 flex-1"><div class="flex flex-wrap items-center gap-2"><h3 class="text-sm font-semibold text-ink">{plugin.name}</h3>{#if plugin.version}<span class="text-xs text-muted">v{plugin.version}</span>{/if}<span class="pill bg-raised text-muted">{plugin.format}</span><span class="pill {state.style}">{state.label}</span></div><p class="mt-1 truncate font-mono text-[10px] text-faint" title={plugin.source}>{plugin.source}</p></div>
          <label class="flex items-center gap-2 text-xs text-muted" title={projects.activeId ? 'Enable for the current project' : 'Open a project first'}><input type="checkbox" checked={plugin.enabled} disabled={!projects.activeId || busy === plugin.id} onchange={(event) => setEnabled(plugin, event.currentTarget.checked)} /> Enabled</label>
          {#if plugin.schema.length}<button class="btn-ghost btn-sm" disabled={!projects.activeId} onclick={() => beginConfigure(plugin)}><Settings2 class="h-3.5 w-3.5" />Configure</button>{/if}
          {#if plugin.mode === 'linked'}<button class="btn-ghost btn-sm" disabled={busy === plugin.id} onclick={() => reload(plugin)}><RotateCw class="h-3.5 w-3.5" />Reload</button>{/if}
          <button class="btn-danger btn-sm" aria-label={`Remove ${plugin.name}`} disabled={busy === plugin.id} onclick={() => remove(plugin)}><Trash2 class="h-3.5 w-3.5" /></button>
        </div>
        <div class="border-t border-line bg-raised/25 p-3"><PluginInventory {plugin} /></div>
        {#if plugin.diagnostics.length}<div class="divide-y divide-line border-t border-line px-4">{#each plugin.diagnostics as diagnostic}<div class="py-2 text-xs"><span class={diagnostic.severity === 'error' ? 'text-rust' : 'text-amber-warm'}>{diagnostic.message}</span>{#if diagnostic.remediation}<span class="text-muted"> — {diagnostic.remediation}</span>{/if}</div>{/each}</div>{/if}
        {#if editing === plugin.id}
          <div class="border-t border-line p-4">
            <div class="grid gap-3 md:grid-cols-2">{#each plugin.schema as setting}<div><label class="label" for={`${plugin.id}-${setting.name}`}>{setting.name}{setting.required ? ' · required' : ''}</label>{#if setting.secret}<input id={`${plugin.id}-${setting.name}`} class="input" type="password" bind:value={secretDraft[setting.name]} placeholder="Enter a new secret" />{:else}<input id={`${plugin.id}-${setting.name}`} class="input" bind:value={settingsDraft[setting.name]} placeholder={setting.description} />{/if}</div>{/each}</div>
            <div class="mt-3 flex justify-end gap-2"><button class="btn-ghost btn-sm" onclick={() => editing = null}>Cancel</button><button class="btn-primary btn-sm" disabled={busy === plugin.id} onclick={() => saveConfiguration(plugin)}>Save configuration</button></div>
          </div>
        {/if}
      </article>
    {/each}
  </div>
{/if}
