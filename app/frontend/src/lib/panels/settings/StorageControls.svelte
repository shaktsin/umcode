<script lang="ts">
  import { app } from '$lib/stores/app.svelte';
  import { StorageSettings, type RetentionDays, type StorageView } from '$lib/storageSettings';

  let view = $state<StorageView>({busy: false, error: ''});
  let draft = $state<RetentionDays>({raw: 30, stale: 30, blob: 90, redacted: 7});
  const settings = new StorageSettings((method, params) => app.call(method, params), (next) => { view = next; });
  $effect(() => {
    if (app.conn === 'open') void settings.load();
  });
  $effect(() => {
    const saved = view.result?.retention;
    if (saved) draft = {...saved};
  });

  function setDays(key: keyof RetentionDays, value: string) {
    draft[key] = value === '' ? 0 : Number(value);
  }
  function bytes(value: number) {
    if (value < 1024) return `${value} B`;
    const units = ['KB', 'MB', 'GB', 'TB'];
    let size = value / 1024;
    let unit = 0;
    while (size >= 1024 && unit < units.length - 1) { size /= 1024; unit++; }
    return `${size.toFixed(1)} ${units[unit]}`;
  }
</script>

<section class="card p-4 max-w-2xl mt-4">
  <h2 class="text-sm font-semibold">Vault storage</h2>
  <p class="text-xs text-muted mt-1 mb-4">Retention applies to unreferenced vault objects. Vault figures count shared objects once and do not represent total app disk use.</p>

  {#if view.result}
    <p class="text-xs text-muted mb-3">Settings source: {view.result.source}</p>
    <div class="grid grid-cols-1 gap-3 sm:grid-cols-2 xl:grid-cols-4">
      {#each [['raw', 'Raw output'], ['stale', 'Stale output'], ['blob', 'Large blobs'], ['redacted', 'Redacted output']] as [key, label]}
        <label class="text-xs text-muted">{label}
          <span class="mt-1 flex items-center gap-2">
            <input class="input w-full" type="number" min="1" max="3650" step="1" value={draft[key as keyof RetentionDays]}
              disabled={view.busy || app.conn !== 'open'} oninput={(event) => setDays(key as keyof RetentionDays, event.currentTarget.value)} />
            <span>days</span>
          </span>
        </label>
      {/each}
    </div>
    <div class="mt-4 text-xs text-muted" aria-live="polite">
      Vault: {view.result.stats.objects} objects · {bytes(view.result.stats.bytes)} stored · {bytes(view.result.stats.eligibleBytes)} currently reclaimable
    </div>
    <div class="flex flex-wrap gap-2 mt-4">
      <button class="btn-primary btn-sm" disabled={view.busy || app.conn !== 'open'} onclick={() => settings.save({...draft})}>Save retention</button>
      <button class="btn-ghost btn-sm" disabled={view.busy || app.conn !== 'open'} onclick={() => settings.cleanup()}>Clean up now</button>
    </div>
    {#if view.cleanup}
      <p class="text-xs text-muted mt-3" role="status">Cleanup finished: removed {view.cleanup.deleted} objects and freed {bytes(view.cleanup.freedBytes)}.</p>
    {/if}
  {:else if !view.error}
    <p class="text-xs text-muted">Loading vault storage settings…</p>
  {/if}
  {#if view.error}
    <p class="text-xs text-red-700 mt-3" role="alert">{view.error}</p>
    <button class="text-xs underline mt-2" disabled={view.busy || app.conn !== 'open'} onclick={() => settings.load()}>Retry</button>
  {/if}
</section>
