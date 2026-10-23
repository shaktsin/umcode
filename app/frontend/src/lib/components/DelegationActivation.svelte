<script lang="ts">
  import type { DelegationState } from '$lib/stores/delegation.svelte';
  let { state }: { state: DelegationState; busy?: boolean; onenable?: () => void } = $props();
</script>
{#if state.error && !state.threadId}<p class="px-4 pb-2 text-xs text-rust" role="alert">{state.error}</p>{/if}
