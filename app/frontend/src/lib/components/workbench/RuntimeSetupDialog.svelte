<script lang="ts">
  import { onDestroy } from 'svelte';
  import { RefreshCw, X } from '@lucide/svelte';
  import { app } from '$lib/stores/app.svelte';
  import { modalFocus } from '$lib/ui/modalFocus';
  export interface Candidate { path: string; version?: string; missing: string[]; ready: boolean; source: string; error?: string }
  export interface RuntimeState { selected: string; candidates: Candidate[]; ready: boolean; missing: string[]; installing: boolean; installError?: string; managedPath: string }
  let { projectId, onClose, onUpdated }: { projectId: string; onClose: () => void; onUpdated: (state: RuntimeState) => void } = $props();
  let runtimeState: RuntimeState | null = $state(null);
  let path = $state('');
  let busy = $state(false);
  let error = $state('');
  let timer: ReturnType<typeof setTimeout> | undefined;
  let alive = true;
  onDestroy(() => { alive = false; if (timer) clearTimeout(timer); });
  $effect(() => { projectId; void refresh(); });
  async function refresh() {
    if (!alive) return;
    try {
      const wasInstalling = runtimeState?.installing;
      const result = await app.call('workbench/runtime/get', { projectId }) as RuntimeState;
      if (!alive) return;
      runtimeState = result; onUpdated(result);
      if ((wasInstalling && !result.installing && result.ready) || !path) path = result.selected || result.candidates.find(c => !c.error)?.path || '';
      if (timer) clearTimeout(timer);
      if (result.installing) timer = setTimeout(() => void refresh(), 2000);
    } catch (e) { if (alive) error = e instanceof Error ? e.message : 'Unable to inspect Python environments.'; }
  }
  async function select(candidatePath = path) {
    if (busy || runtimeState?.installing) return;
    busy = true; error = '';
    try {
      const result = await app.call('workbench/runtime/select', { projectId, path: candidatePath }) as RuntimeState;
      if (!alive) return;
      path = candidatePath; runtimeState = result; onUpdated(result); await refresh();
    } catch (e) { if (alive) error = e instanceof Error ? e.message : 'Unable to select Python.'; }
    finally { busy = false; }
  }
  async function install() {
    if (busy || runtimeState?.installing) return;
    busy = true; error = '';
    try {
      const result = await app.call('workbench/runtime/install', { projectId, path }) as RuntimeState;
      if (!alive) return;
      runtimeState = result; await refresh();
    } catch (e) { if (alive) error = e instanceof Error ? e.message : 'Unable to start installation.'; }
    finally { busy = false; }
  }
</script>

<div class="backdrop">
  <div class="dialog" role="dialog" aria-modal="true" aria-labelledby="runtime-title" tabindex="-1" use:modalFocus={onClose}>
    <header><div><h2 id="runtime-title">Choose Python runtime</h2><p>Select an environment with PyTorch, or create an isolated UMCode runtime.</p></div><button class="icon-button" aria-label="Close runtime setup" onclick={onClose}><X size={17} /></button></header>
    <div class="content">
      <div class="section-heading"><h3>Found on this computer</h3><button class="icon-button" aria-label="Scan Python environments" disabled={busy} onclick={() => void refresh()}><RefreshCw size={15} /></button></div>
      {#if runtimeState}
        <div class="candidates">
          {#each runtimeState.candidates as candidate}
            <button class="candidate" class:selected={runtimeState.selected === candidate.path} disabled={busy || runtimeState.installing || !!candidate.error} onclick={() => void select(candidate.path)}>
              <span><strong>{candidate.source} {candidate.version ? `· Python ${candidate.version}` : ''}</strong><small>{candidate.path}</small></span>
              <span class:good={candidate.ready} class="status">{candidate.error || (candidate.ready ? 'Ready' : `Needs ${candidate.missing.join(', ')}`)}</span>
            </button>
          {/each}
        </div>
        <label for="python-path">Python executable path</label>
        <div class="path-row"><input id="python-path" class="input" bind:value={path} placeholder="/absolute/path/to/bin/python" disabled={busy || runtimeState.installing} /><button class="btn-outline btn-sm" disabled={busy || runtimeState.installing || !path.trim()} onclick={() => void select()}>Use path</button></div>
        <p>Paste the full path to a Python executable, such as a project <code>.venv/bin/python</code>. This choice is used for Train and Inference.</p>
        <div class="install-box"><div><strong>Install PyTorch dependencies</strong><p>Creates or updates <code>{runtimeState.managedPath}</code>. Downloads torch, safetensors, Pillow and numpy into that UMCode environment. Your existing Python environments are unchanged.</p></div><button class="btn-primary btn-sm" disabled={busy || runtimeState.installing || !path.trim()} onclick={() => void install()}>{runtimeState.installing ? 'Installing…' : 'Install in UMCode runtime'}</button></div>
        {#if runtimeState.installing}<p role="status">Installing packages locally. This can take several minutes; you can close this dialog and return later.</p>{/if}
        {#if runtimeState.installError}<p class="error" role="alert">{runtimeState.installError}</p>{/if}
      {:else}<p role="status">Scanning Python environments…</p>{/if}
      {#if error}<p class="error" role="alert">{error}</p>{/if}
    </div>
    <footer><button class="btn-outline btn-sm" onclick={onClose}>Done</button></footer>
  </div>
</div>

<style>
  .backdrop{position:fixed;inset:0;z-index:80;background:#0006;display:flex;align-items:center;justify-content:center;padding:20px}.dialog{width:min(720px,100%);max-height:90dvh;display:flex;flex-direction:column;background:var(--color-paper);border:1px solid var(--color-line);border-radius:14px;box-shadow:0 18px 80px #0004}.dialog header,.dialog footer{display:flex;align-items:flex-start;justify-content:space-between;gap:16px;padding:18px 22px}.dialog header{border-bottom:1px solid var(--color-line)}.dialog footer{border-top:1px solid var(--color-line);justify-content:flex-end}.dialog h2{font-size:18px;font-weight:650}.dialog h3{font-size:13px;font-weight:650}.dialog p{font-size:12px;color:var(--color-muted);line-height:1.6;margin:6px 0}.content{overflow:auto;padding:20px 22px}.section-heading,.path-row,.install-box{display:flex;align-items:center;justify-content:space-between;gap:12px}.candidates{display:grid;gap:8px;margin:12px 0 22px}.candidate{width:100%;display:flex;justify-content:space-between;align-items:center;gap:12px;text-align:left;padding:12px;border:1px solid var(--color-line);border-radius:9px;background:var(--color-surface)}.candidate.selected{border-color:var(--color-accent-strong)}.candidate span:first-child{min-width:0}.candidate strong,.candidate small{display:block}.candidate strong{font-size:12px}.candidate small{font-size:11px;color:var(--color-muted);overflow-wrap:anywhere;margin-top:5px}.status{font-size:11px;color:var(--color-amber-warm);flex-shrink:0;max-width:180px;text-align:right}.status.good{color:var(--color-sage)}label{font-size:12px;font-weight:600}.path-row{margin:8px 0}.path-row input{flex:1;min-width:0}.install-box{align-items:flex-start;padding:14px;border:1px solid var(--color-line);border-radius:9px;margin-top:20px}.install-box p{margin-bottom:0}.install-box button{flex-shrink:0}.error{color:var(--color-rust)!important}code{font:11px var(--font-mono);overflow-wrap:anywhere}@media(max-width:600px){.candidate,.install-box{flex-direction:column;align-items:stretch}.status{text-align:left}}
</style>
