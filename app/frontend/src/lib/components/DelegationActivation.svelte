<script lang="ts">
  import type { DelegationState } from '$lib/stores/delegation.svelte';
  let { state, busy = false, onenable }: { state: DelegationState; busy?: boolean; onenable: () => void } = $props();
</script>
{#if !state.active}
  <div class="flex flex-wrap items-center gap-2 px-4 pb-2 text-[11px] text-muted">
    {#if state.token}
      <span role="status">Agents enabled for the next message</span>
      <button class="btn-ghost btn-sm" onclick={() => state.token = null}>Remove</button>
    {:else}
      <button class="btn-ghost btn-sm" disabled={busy || state.pending} onclick={onenable}>{state.pending ? 'Enabling agents…' : 'Enable agents for next message'}</button>
    {/if}
  </div>
{/if}

{#if state.error && !state.threadId}<p class="px-4 pb-2 text-xs text-rust" role="alert">{state.error}</p>{/if}
