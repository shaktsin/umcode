<script lang="ts">
  import { app } from '$lib/stores/app.svelte';
  import { OptimizationSettings, type OptimizationView } from '$lib/optimizationSettings';
  let view = $state<OptimizationView>({busy: false, error: ''});
  const settings = new OptimizationSettings((method, params) => app.call(method, params), (next) => { view = next; });
  $effect(() => { if (app.conn === 'open') void settings.load(); });
</script>

<section class="card p-4 max-w-2xl">
  <h2 class="text-sm font-semibold">Token optimization</h2>
  <p class="text-xs text-muted mt-1 mb-4">Automatically optimize context, tools, workflow and verified memory across chats.</p>
  <label class="flex items-start gap-3 text-sm">
    <input class="mt-1" type="checkbox" checked={view.result?.enabled ?? false}
      disabled={view.busy || !view.result || app.conn !== 'open'}
      onchange={(event) => { const enabled = event.currentTarget.checked; event.currentTarget.checked = view.result?.enabled ?? false; void settings.save(enabled); }} />
    <span>Token optimization
      <span class="block text-xs text-muted">Changes apply to new turns. Active turns finish with their current settings.</span>
    </span>
  </label>
  {#if view.result?.source === 'legacy'}
    <p class="text-xs text-muted mt-3">Using legacy configuration. Changing this switch applies all optimizations together.</p>
    {#if view.result.legacyMixed}<p class="text-xs text-muted mt-1">Some optimizations are currently enabled by legacy configuration.</p>{/if}
  {/if}
  {#if view.error}
    <p class="text-xs text-red-700 mt-3" role="alert">{view.error}</p>
    <button class="text-xs underline mt-2" disabled={view.busy} onclick={() => settings.load()}>Reload saved setting</button>
  {/if}
</section>
