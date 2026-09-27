<script lang="ts">
  // The small floating window from the tray's "Mini Overlay" item, and shown
  // automatically when an approval needs the user (see app/mini.go and
  // Shell.SetCounts). It is a separate Wails window running this same
  // frontend bundle (see main.ts), with its own connection to the engine.
  import { X, ExternalLink, Loader2, ShieldQuestion } from '@lucide/svelte';
  import { app } from '$lib/stores/app.svelte';
  import { mini } from '$lib/stores/mini.svelte';
  import ApprovalCard from '$lib/components/ApprovalCard.svelte';

  const topApproval = $derived(app.approvals[0]);
  const moreCount = $derived(Math.max(0, app.approvals.length - 1));

  async function shellAction(name: string) {
    try {
      await fetch(`/__umcode/mini/${name}`, { method: 'POST' });
    } catch {
      /* not running inside the desktop app (e.g. a browser preview) */
    }
  }

  function openMain() {
    void shellAction('openMain');
  }
  function hide() {
    void shellAction('hide');
  }
</script>

<div class="mini-root">
  <div class="mini-titlebar">
    <span class="mini-dot" class:online={app.conn === 'open'} class:offline={app.conn !== 'open'}></span>
    <button class="mini-app-name" onclick={openMain} title="Open UMCode">UMCode</button>
    <div class="mini-drag-fill"></div>
    <button class="icon-button" title="Open UMCode" aria-label="Open UMCode" onclick={openMain}><ExternalLink class="w-3.5 h-3.5" /></button>
    <button class="icon-button" title="Hide" aria-label="Hide" onclick={hide}><X class="w-3.5 h-3.5" /></button>
  </div>

  <div class="mini-body">
    {#if app.conn !== 'open'}
      <div class="mini-empty">
        <Loader2 class="w-4 h-4 animate-spin text-muted" />
        <span class="text-xs text-muted">{app.conn === 'connecting' ? 'Connecting to the engine…' : 'Not connected'}</span>
      </div>
    {:else if topApproval}
      <div class="mini-approval">
        <div class="flex items-center gap-1.5 mb-1.5">
          <ShieldQuestion class="w-3.5 h-3.5 text-amber-warm" />
          <span class="text-[11px] font-medium text-amber-warm uppercase tracking-wide">Needs your OK</span>
          {#if moreCount > 0}<span class="text-[11px] text-muted ml-auto">+{moreCount} more</span>{/if}
        </div>
        <div class="mini-approval-card">
          <ApprovalCard approval={topApproval} compact chatContext />
        </div>
      </div>
    {:else if mini.running}
      <div class="mini-active">
        <div class="flex items-center gap-1.5 mb-1">
          <Loader2 class="w-3.5 h-3.5 animate-spin text-clay" />
          <span class="text-sm text-ink font-medium truncate">{mini.threadTitle}</span>
        </div>
        <p class="mini-preview">{mini.latestText || 'Working…'}</p>
        <div class="flex justify-end">
          <button class="btn-outline btn-sm" onclick={() => mini.stop()}>Stop</button>
        </div>
      </div>
    {:else}
      <div class="mini-empty">
        <span class="text-xs text-muted">No approvals or running work right now.</span>
      </div>
    {/if}
  </div>
</div>

<style>
  :global(body.mini) {
    background: transparent;
    overflow: hidden;
  }
  .mini-root {
    display: flex;
    flex-direction: column;
    height: 100vh;
    width: 100vw;
    background: var(--color-surface);
    color: var(--color-ink);
    border: 1px solid var(--color-line);
    box-sizing: border-box;
  }
  .mini-titlebar {
    display: flex;
    align-items: center;
    gap: 6px;
    padding: 8px 8px 8px 10px;
    border-bottom: 1px solid var(--color-line);
    /* The only draggable region: buttons below opt back out. */
    --wails-draggable: drag;
  }
  .mini-dot {
    width: 7px;
    height: 7px;
    border-radius: 999px;
    flex-shrink: 0;
  }
  .mini-dot.online { background: var(--color-sage); }
  .mini-dot.offline { background: var(--color-rust); }
  .mini-app-name {
    font-size: 12px;
    font-weight: 600;
    color: var(--color-ink-soft);
    --wails-draggable: no-drag;
  }
  .mini-drag-fill { flex: 1; }
  .mini-titlebar .icon-button { --wails-draggable: no-drag; }
  .mini-body {
    flex: 1;
    overflow: auto;
    padding: 10px;
  }
  .mini-empty {
    height: 100%;
    display: flex;
    align-items: center;
    justify-content: center;
    gap: 8px;
    text-align: center;
  }
  .mini-approval-card {
    border-radius: 10px;
    overflow: hidden;
    border: 1px solid var(--color-line);
  }
  .mini-preview {
    font-size: 12px;
    color: var(--color-muted);
    margin: 4px 0 8px;
    display: -webkit-box;
    -webkit-line-clamp: 3;
    line-clamp: 3;
    -webkit-box-orient: vertical;
    overflow: hidden;
  }
</style>
