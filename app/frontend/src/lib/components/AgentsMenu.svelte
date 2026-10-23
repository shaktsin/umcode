<script lang="ts">
  import { Ellipsis, X } from '@lucide/svelte';
  import type { DelegationState } from '$lib/stores/delegation.svelte';
  import { tick } from 'svelte';
  import PillMenu from './PillMenu.svelte';

  let { state: delegation, busy = false, onenable }: { state: DelegationState; busy?: boolean; onenable: () => void } = $props();
  let menu: PillMenu | undefined = $state();
  function agentsOff() {
    delegation.token = null;
    void tick().then(() => menu?.focus());
  }
  const items = $derived([{
    id: 'agents',
    label: 'Agents for next message',
    hint: 'Lets the model hand work to sub-agents. Turns off after sending.',
    checked: !!delegation.token,
    disabled: busy || delegation.pending || !!delegation.token,
  }]);
</script>

{#if !delegation.active}
  <PillMenu bind:this={menu} ariaLabel="More options" title="More options" {items} onselect={() => onenable()}>
    {#snippet icon()}<Ellipsis class="w-3.5 h-3.5 shrink-0" />{/snippet}
  </PillMenu>
  {#if delegation.pending}
    <span class="pill-button" role="status">Enabling…</span>
  {:else if delegation.token}
    <span class="pill-button" title="Agents are on for the next message">
      Agents
      <button type="button" class="inline-flex items-center" aria-label="Turn agents off" onclick={agentsOff}><X class="w-3 h-3" /></button>
    </span>
  {/if}
{/if}
