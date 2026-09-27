<script lang="ts">
  import { Check, ChevronDown, Layers3 } from '@lucide/svelte';
  import { app } from '$lib/stores/app.svelte';
  import type { Complexity, ConfiguredModel, ModelPool, ModelSelection, RoutingConfig } from '$lib/types';

  let { value, onchange, compact = false }: { value: ModelSelection; onchange: (v: ModelSelection) => void; compact?: boolean } = $props();
  let open = $state(false);
  let busy = $state(false);
  let selected = $state<string[]>([]);

  const levels: { id: Complexity; label: string; hint: string }[] = [
    { id: 'auto', label: 'Auto', hint: 'Pick per message' },
    { id: 'quick', label: 'Quick', hint: 'Fast, little or no thinking' },
    { id: 'standard', label: 'Standard', hint: 'Balanced' },
    { id: 'deep', label: 'Deep', hint: 'More thinking for complex work' },
  ];
  const defaultComplexity = $derived(app.complexity?.default || 'auto');
  const providerNames: Record<string, string> = {
    claude: 'Anthropic API', openai: 'OpenAI API', gemini: 'Google Gemini', openai_compatible: 'OpenAI-compatible',
  };
  const pools = $derived(app.routing?.pools ?? []);
  const configured = $derived(app.routing?.models ?? []);
  const configuredByProvider = $derived.by(() => {
    const groups: Record<string, ConfiguredModel[]> = {};
    for (const model of configured) {
      if (model.enabled) (groups[model.provider] ??= []).push(model);
    }
    return Object.entries(groups);
  });

  const poolName = $derived(value.provider === 'pool' ? pools.find((pool) => pool.id === value.model)?.name : undefined);
  const modelLabel = $derived.by(() => {
    if (poolName) return poolName;
    if (value.provider && value.model) {
      const model = configured.find((item) => item.provider === value.provider && item.model === value.model);
      return model?.name || value.model;
    }
    return 'Default model';
  });

  function syncSelected() {
    if (value.provider === 'pool') {
      selected = [...(pools.find((pool) => pool.id === value.model)?.models ?? [])];
    } else if (value.provider && value.model) {
      const model = configured.find((item) => item.provider === value.provider && item.model === value.model);
      selected = model ? [model.id] : [];
    } else {
      selected = [];
    }
  }

  function setComplexity(c: Complexity) {
    onchange({ ...value, complexity: c === value.complexity ? undefined : c });
  }

  function usePool(pool: ModelPool) {
    selected = [...pool.models];
    onchange({ ...value, provider: 'pool', model: pool.id, credentialId: undefined });
    open = false;
  }

  async function applySelected(next: string[]) {
    selected = next;
    const available = configured.filter((model) => model.enabled && next.includes(model.id));
    if (available.length === 0) {
      onchange({ ...value, provider: undefined, model: undefined, credentialId: undefined });
      return;
    }
    if (available.length === 1) {
      const model = available[0];
      onchange({ ...value, provider: model.provider, model: model.model, credentialId: undefined });
      return;
    }

    busy = true;
    try {
      const draft: RoutingConfig = structuredClone($state.snapshot(app.routing));
      const selectedIds = available.map((model) => model.id);
      let pool = draft.pools.find((item) => item.enabled && item.models.length === selectedIds.length && item.models.every((id, index) => id === selectedIds[index]));
      if (!pool) {
        pool = { id: `pool_${crypto.randomUUID().replaceAll('-', '').slice(0, 12)}`, name: `Chat pool · ${available.length} models`, strategy: 'priority', models: [], enabled: true };
        draft.pools.push(pool);
      }
      pool.models = selectedIds;
      pool.strategy = 'priority';
      pool.enabled = true;
      const result = await app.call<RoutingConfig>('routing/set', draft);
      app.routing = result;
      const saved = result.pools.find((item) => item.id === pool!.id)!;
      onchange({ ...value, provider: 'pool', model: saved.id, credentialId: undefined });
      window.dispatchEvent(new CustomEvent('umcode:routing'));
    } catch (error) {
      app.toast('error', error instanceof Error ? error.message : 'Could not save this model pool.');
      syncSelected();
    } finally {
      busy = false;
    }
  }

  function toggleModel(model: ConfiguredModel) {
    const next = selected.includes(model.id) ? selected.filter((id) => id !== model.id) : [...selected, model.id];
    void applySelected(next);
  }

  $effect(() => {
    void value.provider;
    void value.model;
    syncSelected();
  });

  $effect(() => {
    if (!open) return;
    const close = (event: MouseEvent) => {
      const target = event.target as HTMLElement;
      if (!target.closest('[data-model-picker]')) open = false;
    };
    window.addEventListener('click', close);
    return () => window.removeEventListener('click', close);
  });
</script>

<div class="flex items-center gap-1.5 flex-wrap">
  <div class="relative" data-model-picker>
    <button class="select inline-flex items-center gap-2 max-w-56" title="Provider and model" aria-expanded={open} onclick={() => (open = !open)}>
      {#if value.provider === 'pool'}<Layers3 class="w-3.5 h-3.5 shrink-0" />{/if}
      <span class="truncate">{compact ? modelLabel : `Model · ${modelLabel}`}</span>
      <ChevronDown class="w-3 h-3 shrink-0" />
    </button>
    {#if open}
      <div class="absolute bottom-full left-0 z-40 mb-2 w-80 max-h-[min(28rem,70vh)] overflow-y-auto card p-2 shadow-xl" role="group" aria-label="Choose models">
        <div class="px-2 py-1.5 flex items-center">
          <span class="text-[10px] font-semibold uppercase tracking-wider text-muted">Provider · model</span>
          {#if busy}<span class="ml-auto text-[10px] text-muted">Saving pool…</span>{/if}
        </div>
        <button class="w-full flex items-center gap-2 px-2 py-1.5 rounded-md text-left text-xs hover:bg-raised {selected.length === 0 ? 'text-accent-strong' : 'text-ink-soft'}" onclick={() => { selected = []; onchange({ ...value, provider: undefined, model: undefined, credentialId: undefined }); open = false; }}>
          <span class="w-4 h-4 flex items-center justify-center">{#if selected.length === 0}<Check class="w-3.5 h-3.5" />{/if}</span>Default model
        </button>
        {#if pools.filter((pool) => pool.enabled).length}
          <div class="mt-2 border-t border-line pt-2">
            <p class="px-2 py-1 text-[10px] font-semibold uppercase tracking-wider text-muted">Pools</p>
            {#each pools.filter((pool) => pool.enabled) as pool (pool.id)}
              <button class="w-full flex items-center gap-2 px-2 py-1.5 rounded-md text-left text-xs hover:bg-raised" onclick={() => usePool(pool)}>
                <Layers3 class="w-3.5 h-3.5 text-muted" />
                <span class="flex-1 truncate">{pool.name}</span>
                <span class="text-[10px] text-faint">{pool.models.length} models</span>
                {#if value.provider === 'pool' && value.model === pool.id}<Check class="w-3.5 h-3.5 text-accent" />{/if}
              </button>
            {/each}
          </div>
        {/if}
        {#each configuredByProvider as [provider, models] (provider)}
          <div class="mt-2 border-t border-line pt-2">
            <p class="px-2 py-1 text-[10px] font-semibold uppercase tracking-wider text-muted">{providerNames[provider] ?? provider}</p>
            {#each models as model (model.id)}
              <label class="flex items-center gap-2 px-2 py-1.5 rounded-md text-xs text-ink-soft hover:bg-raised cursor-pointer">
                <input type="checkbox" checked={selected.includes(model.id)} disabled={busy} onchange={() => toggleModel(model)} />
                <span class="min-w-0 flex-1 truncate" title={model.model}>{model.model}</span>
                {#if model.name && model.name !== model.model}<span class="max-w-24 truncate text-[10px] text-faint">{model.name}</span>{/if}
              </label>
            {/each}
          </div>
        {/each}
        {#if configuredByProvider.length === 0}<p class="px-2 py-3 text-xs text-muted">Add models in Settings → Models to choose them here.</p>{/if}
        {#if selected.length > 1}<p class="px-2 pt-2 text-[10px] text-muted">{selected.length} selected · UMCode will try them in pool order if a quota is reached.</p>{/if}
      </div>
    {/if}
  </div>

  <div class="inline-flex rounded-md border border-line-strong overflow-hidden" role="group" aria-label="Complexity">
    {#each levels as level}
      {@const on = (value.complexity || '') === level.id}
      {@const inherited = !value.complexity && defaultComplexity === level.id}
      <button class="px-2 py-1 text-xs transition-colors {on ? 'bg-clay text-white' : inherited ? 'bg-raised text-ink' : 'text-muted hover:bg-raised'}" title={`${level.hint}${inherited ? ' (default)' : ''}`} aria-pressed={on} onclick={() => setComplexity(level.id)}>
        {compact ? level.label[0] : level.label}
      </button>
    {/each}
  </div>
</div>
