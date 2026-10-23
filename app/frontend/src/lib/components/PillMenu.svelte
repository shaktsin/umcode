<script lang="ts">
  import { Check, ChevronDown } from '@lucide/svelte';
  import type { Snippet } from 'svelte';
  import PopupSurface from './PopupSurface.svelte';
  import type { PillMenuItem } from '$lib/pillMenu';

  let { ariaLabel, label, icon, items, onselect, title, disabled = false }: {
    ariaLabel: string; label?: string; icon?: Snippet; items: PillMenuItem[]; onselect: (id: string) => void; title?: string; disabled?: boolean;
  } = $props();
  let open = $state(false);
  let trigger: HTMLButtonElement | undefined = $state();

  function choose(id: string) {
    open = false;
    onselect(id);
  }
</script>

<div class="relative">
  <button bind:this={trigger} type="button" class="pill-button" class:icon-only={!label} {disabled} {title} aria-label={label ? undefined : ariaLabel} aria-haspopup="menu" aria-expanded={open} onclick={() => (open = !open)}>
    {#if icon}{@render icon()}{/if}
    {#if label}<span class="truncate">{label}</span><ChevronDown class="w-3 h-3 shrink-0" />{/if}
  </button>
  <PopupSurface {open} anchor={trigger} kind="menu" preferredSide="above" onclose={() => (open = false)}>
    <div class="popup-list p-1" role="menu" aria-label={ariaLabel}>
      {#each items as item (item.id)}
        {#if item.separatorBefore}<div class="my-1 border-t border-line" role="separator"></div>{/if}
        <button type="button" role="menuitem" class="w-full flex items-start gap-2 px-2 py-1.5 rounded-md text-left text-xs hover:bg-raised disabled:opacity-50" disabled={item.disabled} onclick={() => choose(item.id)}>
          <span class="w-4 h-4 flex items-center justify-center shrink-0">{#if item.checked}<Check class="w-3.5 h-3.5 text-accent" />{/if}</span>
          <span class="min-w-0 flex-1">
            <span class="block font-semibold text-ink">{item.label}</span>
            {#if item.hint}<span class="block text-muted">{item.hint}</span>{/if}
          </span>
        </button>
      {/each}
    </div>
  </PopupSurface>
</div>
