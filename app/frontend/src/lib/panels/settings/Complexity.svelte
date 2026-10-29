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

  const MAX_OUT_MIN = 1024;
  const MAX_OUT_MAX = 1_000_000;
  // A cleared number input binds to null, so a missing value is invalid too.
  const maxOutValid = $derived(
    draft != null &&
      Number.isInteger(draft.defaultMaxOutputTokens) &&
      (draft.defaultMaxOutputTokens as number) >= MAX_OUT_MIN &&
      (draft.defaultMaxOutputTokens as number) <= MAX_OUT_MAX,
  );

  // -1 is the engine's "no limit" sentinel for the time limit and token budget.
  const NO_LIMIT = -1;
  const DEFAULT_MINUTES = 120;
  const DEFAULT_TOKENS = 1_000_000;
  function setDurationUnlimited(on: boolean) {
    if (draft) draft.limits.maxDurationMinutes = on ? NO_LIMIT : DEFAULT_MINUTES;
  }
  function setTokensUnlimited(on: boolean) {
    if (draft) draft.limits.maxTokens = on ? NO_LIMIT : DEFAULT_TOKENS;
  }

  async function save() {
    if (!draft || !maxOutValid) return;
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
    {#if !maxOutValid}
      <p class="text-xs text-red-700 mt-1" role="alert">Enter a number from 1,024 to 1,000,000.</p>
    {/if}
  </div>

  <div class="card p-4 mb-4">
    <h3 class="text-sm font-medium text-ink">Turn safety limits</h3>
    <p class="text-xs text-muted mt-1 mb-3">The agent keeps working until it finishes or reaches a real safety budget. These limits apply to every reasoning level.</p>
    <div class="grid gap-3 sm:grid-cols-2 xl:grid-cols-4">
      <div>
        <label class="label">Time limit (minutes)<input class="input mt-1" type="number" min="1" max="480" step="5" disabled={draft.limits.maxDurationMinutes === NO_LIMIT} bind:value={draft.limits.maxDurationMinutes} /></label>
        <label class="flex items-center gap-2 text-xs text-muted mt-1"><input type="checkbox" checked={draft.limits.maxDurationMinutes === NO_LIMIT} onchange={(e) => setDurationUnlimited(e.currentTarget.checked)} />No limit</label>
      </div>
      <div>
        <label class="label">Token budget<input class="input mt-1" type="number" min="10000" max="10000000" step="10000" disabled={draft.limits.maxTokens === NO_LIMIT} bind:value={draft.limits.maxTokens} /></label>
        <label class="flex items-center gap-2 text-xs text-muted mt-1"><input type="checkbox" checked={draft.limits.maxTokens === NO_LIMIT} onchange={(e) => setTokensUnlimited(e.currentTarget.checked)} />No limit</label>
      </div>
      <label class="label">Cost budget (USD)<input class="input mt-1" type="number" min="0.01" max="10000" step="1" bind:value={draft.limits.maxCostUsd} /></label>
      <label class="label">Tool-round safety ceiling<input class="input mt-1" type="number" min="1" max="1000" step="10" bind:value={draft.limits.maxToolRounds} /></label>
    </div>
  </div>

  <div class="flex justify-end gap-2 mt-3">
    <button class="btn-ghost" onclick={() => (draft = app.complexity ? copy(app.complexity) : null)}>Reset</button>
    <button class="btn-primary" disabled={!maxOutValid} onclick={save}>Save</button>
  </div>
{/if}
