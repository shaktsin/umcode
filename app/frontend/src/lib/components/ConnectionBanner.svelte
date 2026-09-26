<script lang="ts">
  import { app } from '$lib/stores/app.svelte';
</script>

{#if app.conn !== 'open'}
  <div class="px-4 py-2 text-xs flex items-center gap-3 border-b {app.conn === 'connecting' ? 'bg-clay-soft/30 border-clay/30 text-amber-warm' : 'bg-rust-soft border-rust/30 text-rust'}">
    {#if app.conn === 'connecting'}
      Connecting to the UMCode engine…
    {:else}
      <span>
        Can't reach the engine{app.connError ? `: ${app.connError}` : ''}.
        {#if app.shell === 'mac'}The app keeps trying to start it. See Settings → Engine &amp; app for details.{:else}Start it with <code class="font-mono">umcode engine</code>.{/if}
      </span>
      <button class="ml-auto btn-outline btn-sm" onclick={() => app.rpc.retry()}>Retry now</button>
    {/if}
  </div>
{/if}
