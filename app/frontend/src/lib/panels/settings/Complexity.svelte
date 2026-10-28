<script lang="ts">
  import { app } from '$lib/stores/app.svelte';
  import { COMPLEXITY_LEVELS, normalizeLevel } from '$lib/complexityPill';
  import type { ComplexityDefaults } from '$lib/types';

  function copy(d: ComplexityDefaults): ComplexityDefaults {
    const c: ComplexityDefaults = JSON.parse(JSON.stringify(d));
    c.default = normalizeLevel(c.default) ?? 'medium';
    return c;
  }
  let draft = $state<ComplexityDefaults | null>(app.complexity ? copy(app.complexity) : null);
  $effect(() => {
    if (app.complexity && !draft) draft = copy(app.complexity);
  });

  async function save() {
    if (!draft) return;
    const r = await app.try<ComplexityDefaults>('complexity/setDefaults', draft, 'Thinking settings saved.');
    if (r) {
      app.complexity = r;
      draft = copy(r);
    }
  }
</script>

{#if draft}
  <div class="card p-4 mb-4">
    <label class="label" for="cx-default">Default thinking level for new chats</label>
    <select id="cx-default" class="input w-64" bind:value={draft.default}>
      {#each COMPLEXITY_LEVELS as level}<option value={level.id}>{level.label}</option>{/each}
    </select>
    <p class="text-xs text-muted mt-2">How much the model thinks. Models that do not support a level use the nearest one they do.</p>
    <label class="label mt-4" for="cx-maxout">Default max output tokens</label>
    <input id="cx-maxout" class="input w-40" type="number" min="1024" max="1000000" step="1024" bind:value={draft.defaultMaxOutputTokens} />
    <p class="text-xs text-muted mt-2">Used for models whose own output limit is unknown (1,024 to 1,000,000).</p>
  </div>

  <div class="card p-4 mb-4">
    <h3 class="text-sm font-medium text-ink">Turn safety limits</h3>
    <p class="text-xs text-muted mt-1 mb-3">The agent keeps working until it finishes or reaches a real safety budget. These limits apply to every reasoning level.</p>
    <div class="grid gap-3 sm:grid-cols-2 xl:grid-cols-4">
      <label class="label">Time limit (minutes)<input class="input mt-1" type="number" min="1" max="480" step="5" bind:value={draft.limits.maxDurationMinutes} /></label>
      <label class="label">Token budget<input class="input mt-1" type="number" min="10000" max="10000000" step="10000" bind:value={draft.limits.maxTokens} /></label>
      <label class="label">Cost budget (USD)<input class="input mt-1" type="number" min="0.01" max="10000" step="1" bind:value={draft.limits.maxCostUsd} /></label>
      <label class="label">Tool-round safety ceiling<input class="input mt-1" type="number" min="1" max="1000" step="10" bind:value={draft.limits.maxToolRounds} /></label>
    </div>
  </div>

  <div class="flex justify-end gap-2 mt-3">
    <button class="btn-ghost" onclick={() => (draft = app.complexity ? copy(app.complexity) : null)}>Reset</button>
    <button class="btn-primary" onclick={save}>Save</button>
  </div>
{/if}
