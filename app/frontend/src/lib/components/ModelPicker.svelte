<script lang="ts">
  import { Check, ChevronDown, Layers3, Zap } from '@lucide/svelte';
  import PopupSurface from './PopupSurface.svelte';
  import PillMenu from './PillMenu.svelte';
  import { COMPLEXITY_LEVELS, complexityPillLabel, nextComplexity } from '$lib/complexityPill';
  import { app } from '$lib/stores/app.svelte';
  import { modelPickerOptions, poolModelIds } from '$lib/modelPickerOptions';
  import type { Complexity, ConfiguredModel, ModelPool, ModelSelection, RoutingConfig } from '$lib/types';

  let { value, onchange, compact = false }: { value: ModelSelection; onchange: (v: ModelSelection) => void; compact?: boolean } = $props();
  let open = $state(false);
  let trigger: HTMLButtonElement | undefined = $state();
  let busy = $state(false);
  let selected = $state<string[]>([]);

  const providerNames: Record<string, string> = {
    claude: 'Anthropic API', openai: 'OpenAI API', gemini: 'Google Gemini', openai_compatible: 'OpenAI-compatible',
  };
  const pools = $derived(app.routing?.pools ?? []);
  const pickerModels = $derived(modelPickerOptions(app.models, app.routing?.models ?? [], app.credentials));
  const visiblePools = $derived(pools.filter((pool) => pool.enabled && pool.models.some((id) => pickerModels.some((model) => model.id === id))));
  const configuredByProvider = $derived.by(() => {
    const groups: Record<string, ConfiguredModel[]> = {};
    for (const model of pickerModels) {
      (groups[model.provider] ??= []).push(model);
    }
    return Object.entries(groups);
  });

  const poolName = $derived(value.provider === 'pool' ? pools.find((pool) => pool.id === value.model)?.name : undefined);
  const modelLabel = $derived.by(() => {
    if (poolName) return poolName;
    if (value.provider && value.model) {
      const model = pickerModels.find((item) => item.provider === value.provider && item.model === value.model);
      return model?.name || value.model;
    }
    return 'Default model';
  });

  function syncSelected() {
    if (value.provider === 'pool') {
      selected = (pools.find((pool) => pool.id === value.model)?.models ?? []).filter((id) => pickerModels.some((model) => model.id === id));
    } else if (value.provider && value.model) {
      const model = pickerModels.find((item) => item.provider === value.provider && item.model === value.model);
      selected = model ? [model.id] : [];
    } else {
      selected = [];
    }
  }

  function usePool(pool: ModelPool) {
    selected = [...pool.models];
    onchange({ ...value, provider: 'pool', model: pool.id, credentialId: undefined });
    open = false;
  }

  async function applySelected(next: string[]) {
    selected = next;
    const available = pickerModels.filter((model) => next.includes(model.id));
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
      const selectedIds = poolModelIds(draft.models, available, () => `model_${crypto.randomUUID().replaceAll('-', '').slice(0, 12)}`);
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
    void pickerModels;
    syncSelected();
  });


</script>

<div class="model-picker-controls">
  <div class="relative" data-model-picker>
    <button bind:this={trigger} class="select inline-flex items-center gap-2 max-w-56" title="Provider and model" aria-expanded={open} onclick={() => (open = !open)}>
      {#if value.provider === 'pool'}<Layers3 class="w-3.5 h-3.5 shrink-0" />{/if}
      <span class="truncate">{compact ? modelLabel : `Model · ${modelLabel}`}</span>
      <ChevronDown class="w-3 h-3 shrink-0" />
    </button>
    <PopupSurface {open} anchor={trigger} kind="menu" preferredSide="above" onclose={() => open = false}>
      <div class="popup-list p-2 model-options" role="dialog" aria-label="Choose models">
        <div class="px-2 py-1.5 flex items-center">
          <span class="text-[10px] font-semibold uppercase tracking-wider text-muted">Provider · model</span>
          {#if busy}<span class="ml-auto text-[10px] text-muted">Saving pool…</span>{/if}
        </div>
        <button class="w-full flex items-center gap-2 px-2 py-1.5 rounded-md text-left text-xs hover:bg-raised {selected.length === 0 ? 'text-accent-strong' : 'text-ink-soft'}" onclick={() => { selected = []; onchange({ ...value, provider: undefined, model: undefined, credentialId: undefined }); open = false; }}>
          <span class="w-4 h-4 flex items-center justify-center">{#if selected.length === 0}<Check class="w-3.5 h-3.5" />{/if}</span>Default model
        </button>
        {#if visiblePools.length}
          <div class="mt-2 border-t border-line pt-2">
            <p class="px-2 py-1 text-[10px] font-semibold uppercase tracking-wider text-muted">Pools</p>
            {#each visiblePools as pool (pool.id)}
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
        {#if configuredByProvider.length === 0}<p class="px-2 py-3 text-xs text-muted">Connect and enable a provider in Settings → Providers to choose models here.</p>{/if}
        {#if selected.length > 1}<p class="px-2 pt-2 text-[10px] text-muted">{selected.length} selected · UMCode will try them in pool order if a quota is reached.</p>{/if}
      </div>
    </PopupSurface>
  </div>

  <PillMenu
    ariaLabel="Complexity"
    title="Complexity"
    label={complexityPillLabel(value.complexity, app.complexity?.default, compact)}
    items={COMPLEXITY_LEVELS.map((level) => ({ id: level.id, label: level.label, hint: level.hint, checked: (value.complexity || '') === level.id }))}
    onselect={(id) => onchange({ ...value, complexity: nextComplexity(value.complexity, id as Complexity) })}
  >
    {#snippet icon()}<Zap class="w-3.5 h-3.5 shrink-0" />{/snippet}
  </PillMenu>
</div>
