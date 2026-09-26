<script lang="ts">
  import { X, ArrowUpFromLine } from '@lucide/svelte';
  import { chat } from '$lib/stores/chat.svelte';
  import { app } from '$lib/stores/app.svelte';
  import ChatColumn from './ChatColumn.svelte';

  const active = $derived(chat.activeSide);

  /** Hand the side chat's last answer back to the main chat's composer. */
  function promote() {
    const last = [...(active?.items ?? [])].reverse().find((i) => i.kind === 'agentMessage' && i.text);
    if (!last?.text) {
      app.toast('warn', 'This side chat has no answer to bring over yet.');
      return;
    }
    window.dispatchEvent(
      new CustomEvent('umcode:compose', {
        detail: `From the side chat “${active?.title ?? ''}”:\n\n${last.text}\n\n`,
      }),
    );
    app.toast('info', 'Added to the main chat’s message box.');
  }
</script>

<div class="h-full flex flex-col bg-surface/40">
  <header class="h-12 shrink-0 border-b border-line flex items-stretch px-1 gap-1 overflow-x-auto">
    {#each chat.sides as s (s.id ?? 'pending')}
      {@const on = chat.activeSideId === s.id}
      <div class="flex items-center gap-1 px-2 my-1.5 rounded-md text-xs {on ? 'bg-raised text-ink' : 'text-muted hover:bg-raised/60'}">
        <button class="max-w-40 truncate" onclick={() => (chat.activeSideId = s.id)}>{s.title}</button>
        <button
          class="text-faint hover:text-ink"
          aria-label="Close side chat"
          onclick={() => s.id && chat.closeSide(s.id)}
        >
          <X class="w-3 h-3" />
        </button>
      </div>
    {/each}
    {#if active}
      <button class="btn-ghost btn-sm ml-auto my-1.5 shrink-0" title="Bring the answer into the main chat" onclick={promote}>
        <ArrowUpFromLine class="w-3.5 h-3.5" />Promote
      </button>
    {/if}
  </header>

  {#if active}
    {#key active.id}
      <ChatColumn view={active} variant="side" />
    {/key}
  {:else}
    <p class="p-4 text-xs text-muted">Starting a side chat…</p>
  {/if}
</div>
