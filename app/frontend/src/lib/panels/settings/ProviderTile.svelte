<script lang="ts">
  import type { ProviderTileModel } from '$lib/providerTiles';

  let { tile, expanded, onclick }: { tile: ProviderTileModel; expanded: boolean; onclick: () => void } = $props();
</script>

<button
  type="button"
  class="tile {tile.isAdd ? 'add' : ''} {expanded ? 'expanded' : ''}"
  aria-expanded={expanded}
  {onclick}
>
  <span class="icon" aria-hidden="true">
    {#if tile.isAdd}
      <span class="plus">+</span>
    {:else if tile.id === 'claude'}
      <svg viewBox="0 0 24 24" width="18" height="18" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><path d="M12 3v18M3 12h18M5.6 5.6l12.8 12.8M18.4 5.6L5.6 18.4" /></svg>
    {:else if tile.id === 'openai'}
      <svg viewBox="0 0 24 24" width="18" height="18" fill="none" stroke="currentColor" stroke-width="2" stroke-linejoin="round"><path d="M12 3l7.8 4.5v9L12 21l-7.8-4.5v-9z" /><circle cx="12" cy="12" r="2.5" /></svg>
    {:else if tile.id === 'gemini'}
      <svg viewBox="0 0 24 24" width="18" height="18" fill="currentColor"><path d="M12 2c.6 5.6 4.4 9.4 10 10-5.6.6-9.4 4.4-10 10-.6-5.6-4.4-9.4-10-10 5.6-.6 9.4-4.4 10-10z" /></svg>
    {:else}
      <span class="mono">{tile.name.charAt(0).toUpperCase()}</span>
    {/if}
  </span>
  <span class="text">
    <span class="name">{tile.name}</span>
    <span class="sub">{tile.subtitle}</span>
  </span>
  {#if tile.connected}<span class="badge">Connected</span>{/if}
</button>

<style>
  .tile {
    display: flex;
    align-items: center;
    gap: var(--ui-space-2);
    min-width: 0;
    padding: 10px 12px;
    text-align: left;
    background: var(--color-surface);
    border: 1px solid var(--color-line);
    border-radius: var(--ui-surface-radius);
    color: var(--color-ink);
    cursor: pointer;
  }
  .tile:hover { border-color: var(--color-line-strong); }
  .tile.add { border-style: dashed; }
  .tile.expanded { border-color: var(--color-accent); }
  .icon {
    display: inline-flex;
    flex: none;
    align-items: center;
    justify-content: center;
    width: 28px;
    height: 28px;
    border-radius: var(--ui-control-radius);
    background: var(--color-raised);
    color: var(--color-ink-soft);
  }
  .plus, .mono { font-size: 15px; font-weight: 600; line-height: 1; }
  .text { display: flex; flex-direction: column; min-width: 0; flex: 1; }
  .name { font-size: var(--ui-control-font); font-weight: 600; }
  .sub { font-size: var(--ui-secondary-font); color: var(--color-muted); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .badge {
    flex: none;
    padding: 1px 6px;
    border-radius: 999px;
    font-size: 11px;
    background: var(--color-sage-soft);
    color: var(--color-sage);
  }
</style>
