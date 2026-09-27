<script lang="ts">
  // Shown instead of the whole app until the engine answers for the first
  // time this launch (see app.booted in app.svelte.ts). Replaces the old
  // experience of opening straight into the full chat UI with a red banner
  // on top while the engine was still starting.
  import { app } from '$lib/stores/app.svelte';
  import { errMsg } from '$lib/format';
  import icon from '$lib/assets/appicon.png';

  // Give a normal startup — including the slower paths (registering the
  // background service, replacing a stale engine after an update) — plenty
  // of room before calling it broken. Those can legitimately take 15-20s.
  const TROUBLE_AFTER_MS = 25_000;

  let stuck = $state(false);
  let busy = $state('');

  $effect(() => {
    const timer = setTimeout(() => (stuck = true), TROUBLE_AFTER_MS);
    return () => clearTimeout(timer);
  });

  async function shellAction(name: string) {
    busy = name;
    try {
      const r = await fetch(`/__umcode/shell/${name}`, { method: 'POST' });
      const j = await r.json().catch(() => ({}));
      if (!r.ok) throw new Error(j.error || `HTTP ${r.status}`);
      if (name === 'restartEngine') {
        stuck = false;
        setTimeout(() => (stuck = true), TROUBLE_AFTER_MS);
      }
    } catch (e) {
      app.toast('error', errMsg(e));
    } finally {
      busy = '';
    }
  }

  function retry() {
    stuck = false;
    setTimeout(() => (stuck = true), TROUBLE_AFTER_MS);
    app.rpc.retry();
  }
</script>

<div class="splash">
  {#if !stuck}
    <div class="spinner-wrap">
      <svg class="spinner-ring" viewBox="0 0 100 100" width="120" height="120" aria-hidden="true">
        <circle cx="50" cy="50" r="44" fill="none" stroke="var(--color-clay)" stroke-width="5" stroke-linecap="round" stroke-dasharray="72 208" />
      </svg>
      <img src={icon} alt="" class="spinner-logo" width="64" height="64" />
    </div>
    <p class="mt-5 text-sm text-muted">Starting the UMCode engine…</p>
  {:else}
    <img src={icon} alt="" class="w-16 h-16 rounded-2xl" />
    <p class="mt-5 text-sm font-medium text-ink">Something's wrong</p>
    <p class="mt-1 text-xs text-muted max-w-sm text-center selectable">
      {app.connError || "The engine hasn't answered yet."}
    </p>
    <div class="flex flex-wrap justify-center gap-2 mt-5">
      <button class="btn-primary btn-sm" disabled={!!busy} onclick={retry}>Try again</button>
      {#if app.shell === 'mac'}
        <button class="btn-outline btn-sm" disabled={!!busy} onclick={() => shellAction('restartEngine')}>Restart engine</button>
        <button class="btn-ghost btn-sm" disabled={!!busy} onclick={() => shellAction('openLogs')}>Open logs</button>
      {/if}
    </div>
  {/if}
</div>

<style>
  .splash {
    position: fixed;
    inset: 0;
    z-index: 50;
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: center;
    /* No card, no extra fill: just the app's own background showing through. */
    background: transparent;
  }
  .spinner-wrap {
    position: relative;
    width: 120px;
    height: 120px;
    display: flex;
    align-items: center;
    justify-content: center;
  }
  .spinner-ring {
    position: absolute;
    inset: 0;
    animation: splash-spin 1.1s linear infinite;
  }
  .spinner-logo {
    width: 64px;
    height: 64px;
    border-radius: 14px;
  }
  @keyframes splash-spin {
    to {
      transform: rotate(360deg);
    }
  }
</style>
