<script lang="ts">
  import { app } from '$lib/stores/app.svelte';
  import { fmtDateTime, errMsg } from '$lib/format';

  interface ShellInfo {
    appVersion: string;
    engineMode: string; // service | child | external | stopped
    engineDetail?: string;
    launchAtLogin: boolean;
    launchAtLoginSupported: boolean;
    cliPath?: string;
    cliInstalled: boolean;
    homeDir: string;
  }

  let shell = $state<ShellInfo | null>(null);
  let busy = $state('');

  async function loadShell() {
    if (app.shell !== 'mac') return;
    try {
      const r = await fetch('/__umcode/shell', { cache: 'no-store' });
      if (r.ok) shell = await r.json();
    } catch {
      shell = null;
    }
  }

  async function action(name: string, body: Record<string, unknown> = {}) {
    busy = name;
    try {
      const r = await fetch(`/__umcode/shell/${name}`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) });
      const j = await r.json().catch(() => ({}));
      if (!r.ok) throw new Error(j.error || `HTTP ${r.status}`);
      if (j.message) app.toast('info', j.message);
      if (name === 'restartEngine') setTimeout(() => app.rpc.retry(), 1500);
    } catch (e) {
      app.toast('error', errMsg(e));
    } finally {
      busy = '';
      await loadShell();
    }
  }

  $effect(() => {
    void app.shell;
    void loadShell();
  });

  const modeText: Record<string, string> = {
    service: 'Background service (Login Items)',
    child: 'Started by the app (stops when the app quits)',
    external: 'Already running (started outside the app)',
    stopped: 'Not running',
  };
</script>

<div class="card p-4 mb-4">
  <h2 class="text-sm font-semibold mb-3">Engine</h2>
  <dl class="grid grid-cols-[10rem_1fr] gap-y-1.5 text-sm">
    <dt class="text-muted">Connection</dt><dd>{app.conn}{app.connError ? ` - ${app.connError}` : ''}</dd>
    {#if app.status}
      <dt class="text-muted">Version</dt><dd>{app.status.engineVersion} (protocol {app.status.protocolVersion})</dd>
      <dt class="text-muted">Running since</dt><dd>{fmtDateTime(app.status.startedAt)}</dd>
      <dt class="text-muted">Clients</dt><dd>{app.status.clients}</dd>
      <dt class="text-muted">Active turns</dt><dd>{app.status.activeTurns}</dd>
      <dt class="text-muted">Database</dt><dd class="font-mono text-xs selectable">{app.status.dbPath}</dd>
    {/if}
    {#if shell}
      <dt class="text-muted">Runs as</dt><dd>{modeText[shell.engineMode] ?? shell.engineMode}{shell.engineDetail ? ` - ${shell.engineDetail}` : ''}</dd>
      <dt class="text-muted">Data folder</dt><dd class="font-mono text-xs selectable">{shell.homeDir}</dd>
    {/if}
  </dl>
  {#if shell}
    <div class="flex flex-wrap gap-2 mt-4">
      <button class="btn-outline btn-sm" disabled={!!busy} onclick={() => action('restartEngine')}>Restart engine</button>
      {#if shell.engineMode !== 'service'}
        <button class="btn-outline btn-sm" disabled={!!busy} onclick={() => action('installService')}>Run in the background at login</button>
      {:else}
        <button class="btn-outline btn-sm" disabled={!!busy} onclick={() => action('uninstallService')}>Stop background service</button>
      {/if}
      <button class="btn-ghost btn-sm" onclick={() => action('openDataFolder')}>Show data folder</button>
      <button class="btn-ghost btn-sm" onclick={() => action('openLogs')}>Open logs</button>
    </div>
  {/if}
</div>

{#if shell}
  <div class="card p-4">
    <h2 class="text-sm font-semibold mb-3">App</h2>
    <div class="space-y-3 text-sm">
      <div class="flex items-center">
        <div><div>Open UMCode at login</div><div class="text-xs text-muted">Starts in the menu bar without a window.</div></div>
        <input class="ml-auto" type="checkbox" checked={shell.launchAtLogin} disabled={!shell.launchAtLoginSupported || !!busy}
          onchange={(e) => action('setLaunchAtLogin', { enabled: e.currentTarget.checked })} />
      </div>
      <div class="flex items-center">
        <div>
          <div>Command-line tool</div>
          <div class="text-xs text-muted">{shell.cliInstalled ? `Installed at ${shell.cliPath}` : 'Adds `umcode` to /usr/local/bin (asks for your password).'}</div>
        </div>
        <button class="btn-outline btn-sm ml-auto" disabled={!!busy} onclick={() => action('installCLI')}>{shell.cliInstalled ? 'Reinstall' : 'Install'}</button>
      </div>
      <div class="text-xs text-muted">UMCode {shell.appVersion}</div>
    </div>
  </div>
{:else if app.shell !== 'mac'}
  <p class="text-xs text-muted">Running in a browser. App settings (login item, background service, command-line tool) are in the Mac app.</p>
{/if}
