<script lang="ts">
  import { ArrowDown, ArrowUp, Plus, RefreshCw, Save, Trash2 } from '@lucide/svelte';
  import { app } from '$lib/stores/app.svelte';
  import { errMsg } from '$lib/format';
  import type { ConfiguredModel, ModelHealthRow, ModelPool, RoutingConfig } from '$lib/types';

  let draft = $state<RoutingConfig>({ models: [], pools: [] });
  let loaded = $state(false);
  let dirty = $state(false);
  let saving = $state(false);
  let newModel = $state({ name: '', provider: 'claude', model: '' });
  let newPoolName = $state('');
  let health = $state<ModelHealthRow[]>([]);

  const providerNames: Record<string, string> = {
    claude: 'Claude', openai: 'OpenAI', gemini: 'Gemini', openai_compatible: 'OpenAI-compatible',
  };

  $effect(() => {
    if (!loaded && app.routing) {
      // $state.snapshot first: a reactive proxy cannot be structured-cloned.
      draft = structuredClone($state.snapshot(app.routing));
      loaded = true;
    }
  });

  $effect(() => {
    if (app.conn === 'open') void loadHealth();
  });

  /** Reads what the router has learned; with a row, wakes that one first. */
  async function loadHealth(clear?: ModelHealthRow) {
    const r = await app.try<{ rows: ModelHealthRow[] }>('model/health', {
      clear: clear ? { provider: clear.provider, model: clear.model, credentialId: clear.credentialId } : undefined,
    });
    if (r) health = r.rows ?? [];
    // Waking a key changes what a chat would run on.
    if (clear) window.dispatchEvent(new CustomEvent('umcode:routing'));
  }

  const cooling = $derived(health.filter((h) => h.cooldownEnd));

  function until(iso: string): string {
    return new Date(iso).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
  }

  const statusWords: Record<string, string> = {
    rate_limited: 'a rate limit',
    server_error: 'a provider outage',
    unauthorized: 'the key being refused',
    timeout: 'a timeout',
    unknown_model: 'the model being unavailable',
  };

  function idFor(prefix: string) {
    return `${prefix}_${crypto.randomUUID().replaceAll('-', '').slice(0, 12)}`;
  }

  function changed() { dirty = true; }

  function addModel() {
    const name = newModel.name.trim();
    const model = newModel.model.trim();
    if (!name || !model) {
      app.toast('error', 'Name and model ID are required.');
      return;
    }
    draft.models.push({ id: idFor('model'), name, provider: newModel.provider, model, enabled: true });
    newModel = { ...newModel, name: '', model: '' };
    changed();
  }

  function configuredFor(model: { provider: string; id: string }) {
    return draft.models.find((item) => item.provider === model.provider && item.model === model.id);
  }

  function toggleCatalogModel(model: { provider: string; id: string; displayName: string }) {
    const configured = configuredFor(model);
    if (configured) configured.enabled = !configured.enabled;
    else draft.models.push({ id: idFor('model'), name: model.displayName || model.id, provider: model.provider, model: model.id, enabled: true });
    changed();
  }

  async function refreshFrom(provider: string) {
    const credential = app.credentials.find((item) => item.provider === provider && item.enabled && item.isDefault)
      ?? app.credentials.find((item) => item.provider === provider && item.enabled);
    if (!credential) {
      app.toast('error', 'Add a key for this provider first.');
      return;
    }
    try {
      await app.call('model/refresh', { credentialId: credential.id }, 60_000);
      await app.refreshCatalog();
      app.toast('info', 'Provider models updated.');
    } catch (error) {
      app.toast('error', errMsg(error));
    }
  }

  const catalogByProvider = $derived.by(() => {
    const groups: Record<string, typeof app.models> = {};
    for (const model of app.models) (groups[model.provider] ??= []).push(model);
    return Object.entries(groups);
  });

  function removeModel(id: string) {
    draft.models = draft.models.filter((m) => m.id !== id);
    draft.pools = draft.pools
      .map((p) => ({ ...p, models: p.models.filter((modelID) => modelID !== id) }))
      .filter((p) => p.models.length > 0);
    if (draft.defaultPool && !draft.pools.some((p) => p.id === draft.defaultPool)) draft.defaultPool = undefined;
    changed();
  }

  function addPool() {
    const name = newPoolName.trim();
    const first = draft.models.find((m) => m.enabled);
    if (!name || !first) {
      app.toast('error', first ? 'Pool name is required.' : 'Add an enabled model before creating a pool.');
      return;
    }
    draft.pools.push({ id: idFor('pool'), name, strategy: 'priority', models: [first.id], enabled: true });
    newPoolName = '';
    changed();
  }

  function removePool(id: string) {
    draft.pools = draft.pools.filter((p) => p.id !== id);
    if (draft.defaultPool === id) draft.defaultPool = undefined;
    changed();
  }

  function addToPool(pool: ModelPool, id: string) {
    if (id && !pool.models.includes(id)) {
      pool.models.push(id);
      changed();
    }
  }

  function removeFromPool(pool: ModelPool, id: string) {
    if (pool.models.length === 1) {
      app.toast('error', 'A pool must contain at least one model.');
      return;
    }
    pool.models = pool.models.filter((modelID) => modelID !== id);
    changed();
  }

  function move(pool: ModelPool, index: number, direction: -1 | 1) {
    const next = index + direction;
    if (next < 0 || next >= pool.models.length) return;
    [pool.models[index], pool.models[next]] = [pool.models[next], pool.models[index]];
    changed();
  }

  function modelByID(id: string): ConfiguredModel | undefined {
    return draft.models.find((m) => m.id === id);
  }

  async function save() {
    saving = true;
    try {
      const result = await app.call<RoutingConfig>('routing/set', $state.snapshot(draft));
      app.routing = result;
      draft = structuredClone(result);
      dirty = false;
      // Open chats are showing what they would run on; that just changed.
      window.dispatchEvent(new CustomEvent('umcode:routing'));
      app.toast('info', 'Models and pools saved.');
    } catch (e) {
      app.toast('error', errMsg(e));
    } finally {
      saving = false;
    }
  }
</script>

<div class="flex items-start gap-4 mb-5">
  <div>
    <h2 class="text-base font-semibold text-ink">Models</h2>
    <p class="text-sm text-muted mt-1 max-w-2xl">Choose models from your provider catalog to make them available in chats. Access is verified when you use a model.</p>
  </div>
  <button class="btn-primary ml-auto" disabled={!dirty || saving} onclick={save}>
    <Save class="w-4 h-4" />{saving ? 'Saving' : 'Save changes'}
  </button>
</div>

{#if cooling.length}
  <div class="card p-3 mb-4 border-amber-warm/40">
    <h2 class="text-xs font-semibold uppercase tracking-wider text-muted mb-2">Resting</h2>
    <p class="text-sm text-muted mb-2">
      These aren't being used right now, so a turn doesn't stall on them. They come back on their own.
    </p>
    <ul class="space-y-1.5">
      {#each cooling as h (h.provider + h.model + h.credentialId)}
        <li class="flex items-center gap-2 text-sm">
          <span class="text-ink">{h.model}</span>
          <span class="text-muted">{h.credentialLabel ? `on ${h.credentialLabel}` : ''}</span>
          <span class="text-[11px] text-faint" title={h.lastError ?? ''}>
            until {until(h.cooldownEnd!)} after {statusWords[h.lastStatus ?? ''] ?? 'an error'}
          </span>
          <button class="btn-ghost btn-sm ml-auto" onclick={() => loadHealth(h)}>Try it now</button>
        </li>
      {/each}
    </ul>
  </div>
{/if}

{#if catalogByProvider.length === 0}
  <div class="card p-4 mb-5 text-sm text-muted">No provider models found yet. Add provider credentials, then refresh the provider catalog.</div>
{:else}
  <div class="space-y-4 mb-6">
    {#each catalogByProvider as [provider, models] (provider)}
      <section>
        <div class="flex items-center gap-2 mb-2">
          <h3 class="text-xs font-semibold uppercase tracking-wider text-muted">{providerNames[provider] ?? provider}</h3>
          <button class="btn-ghost btn-sm ml-auto" onclick={() => refreshFrom(provider)}><RefreshCw class="w-3.5 h-3.5" />Refresh catalog</button>
        </div>
        <div class="card divide-y divide-line overflow-hidden">
          {#each models as catalogModel (catalogModel.id)}
            {@const selectedModel = configuredFor(catalogModel)}
            <label class="flex items-center gap-3 px-3 py-2.5 cursor-pointer hover:bg-raised/60">
              <input type="checkbox" checked={!!selectedModel?.enabled} onchange={() => toggleCatalogModel(catalogModel)} />
              <span class="min-w-0 flex-1">
                <span class="block text-sm text-ink truncate">{catalogModel.displayName || catalogModel.id}</span>
                <span class="block text-[11px] text-muted font-mono truncate">{catalogModel.id}</span>
              </span>
              <span class="hidden sm:block text-[11px] text-muted">{[catalogModel.supportsTools && 'tools', catalogModel.supportsImages && 'images', catalogModel.supportsReasoning && 'thinking'].filter(Boolean).join(' · ')}</span>
              <span class="text-[11px] text-muted whitespace-nowrap" title="USD per million tokens">{catalogModel.inputPerMTok ? `$${catalogModel.inputPerMTok} in · $${catalogModel.outputPerMTok} out` : 'Pricing n/a'}</span>
              <span class="text-xs text-muted w-24 text-right">{selectedModel?.enabled ? 'In chats' : 'Add to chats'}</span>
            </label>
          {/each}
        </div>
      </section>
    {/each}
  </div>
{/if}

<details class="card p-4 mb-8">
  <summary class="cursor-pointer text-sm font-medium text-ink">Add a custom model ID</summary>
  <p class="text-xs text-muted mt-2 mb-3">For compatible providers or model IDs not listed in the catalog.</p>
  <div class="grid grid-cols-1 md:grid-cols-[minmax(9rem,1fr)_10rem_minmax(12rem,1.5fr)_auto] gap-2.5 items-end">
    <label class="form-field"><span>Name</span><input class="input" placeholder="My model" bind:value={newModel.name} /></label>
    <label class="form-field"><span>Provider</span><select class="select w-full" bind:value={newModel.provider}>{#each app.providers as provider}<option value={provider.id}>{providerNames[provider.id] ?? provider.displayName}</option>{/each}</select></label>
    <label class="form-field"><span>Model ID</span><input class="input font-mono" placeholder="Provider model ID" bind:value={newModel.model} /></label>
    <button class="btn-outline h-9 whitespace-nowrap" onclick={addModel}><Plus class="w-4 h-4" />Add</button>
  </div>
  {#if draft.models.some((item) => !app.models.some((catalogModel) => catalogModel.provider === item.provider && catalogModel.id === item.model))}
    <div class="mt-3 divide-y divide-line border-t border-line">
      {#each draft.models.filter((item) => !app.models.some((catalogModel) => catalogModel.provider === item.provider && catalogModel.id === item.model)) as model (model.id)}
        <div class="flex items-center gap-3 pt-3 text-sm">
          <span class="min-w-0 flex-1 truncate">{model.name} <span class="text-muted font-mono">{model.provider}/{model.model}</span></span>
          <label class="inline-flex items-center gap-2 text-xs text-muted"><input type="checkbox" bind:checked={model.enabled} onchange={changed} /> In chats</label>
          <button class="icon-btn" title="Remove model" aria-label="Remove model" onclick={() => removeModel(model.id)}><Trash2 class="w-4 h-4" /></button>
        </div>
      {/each}
    </div>
  {/if}
</details>

<div class="mb-4">
  <div class="grid grid-cols-1 md:grid-cols-[minmax(0,1fr)_13rem] items-end gap-3">
    <div>
      <h2 class="text-base font-semibold text-ink">Model pools</h2>
      <p class="text-sm text-muted mt-1 max-w-2xl">Pools try configured models in order. Quota and rate-limit failures move to the next provider automatically.</p>
    </div>
    <label class="form-field ml-auto w-52">
      <span>Default for new chats</span>
      <select class="select w-full" bind:value={draft.defaultPool} onchange={changed}>
        <option value={undefined}>No default pool</option>
        {#each draft.pools.filter((p) => p.enabled) as pool}<option value={pool.id}>{pool.name}</option>{/each}
      </select>
    </label>
  </div>
</div>

<div class="space-y-3 mb-4">
  {#each draft.pools as pool (pool.id)}
    <div class="card p-4">
      <div class="grid grid-cols-[minmax(10rem,1fr)_10rem_auto_auto] gap-3 items-end mb-3">
        <label class="form-field"><span>Pool name</span><input class="input" bind:value={pool.name} oninput={changed} /></label>
        <label class="form-field">
          <span>Strategy</span>
          <select class="select w-full" bind:value={pool.strategy} onchange={changed}>
            <option value="priority">Priority</option><option value="balanced">Balanced</option><option value="quality">Quality</option><option value="fast">Fast</option><option value="cheap">Low cost</option>
          </select>
        </label>
        <label class="inline-flex items-center gap-2 h-9 text-xs text-muted whitespace-nowrap"><input type="checkbox" bind:checked={pool.enabled} onchange={changed} /> Enabled</label>
        <button class="icon-btn h-9" title="Remove pool" aria-label="Remove pool" onclick={() => removePool(pool.id)}><Trash2 class="w-4 h-4" /></button>
      </div>

      <div class="rounded-lg border border-line overflow-hidden">
        {#each pool.models as modelID, index (modelID)}
          {@const model = modelByID(modelID)}
          <div class="flex items-center gap-3 px-3 py-2 border-b border-line last:border-b-0">
            <span class="w-5 text-xs tabular-nums text-muted">{index + 1}</span>
            <div class="min-w-0 flex-1">
              <div class="text-sm text-ink truncate">{model?.name ?? 'Missing model'}</div>
              <div class="text-[11px] text-muted font-mono truncate">{model ? `${model.provider}/${model.model}` : modelID}</div>
            </div>
            <button class="icon-btn" disabled={index === 0} title="Move up" aria-label="Move up" onclick={() => move(pool, index, -1)}><ArrowUp class="w-3.5 h-3.5" /></button>
            <button class="icon-btn" disabled={index === pool.models.length - 1} title="Move down" aria-label="Move down" onclick={() => move(pool, index, 1)}><ArrowDown class="w-3.5 h-3.5" /></button>
            <button class="icon-btn" title="Remove from pool" aria-label="Remove from pool" onclick={() => removeFromPool(pool, modelID)}><Trash2 class="w-3.5 h-3.5" /></button>
          </div>
        {/each}
      </div>

      <label class="form-field mt-3 max-w-sm">
        <span>Add model to pool</span>
        <select class="select w-full" value="" onchange={(e) => { addToPool(pool, e.currentTarget.value); e.currentTarget.value = ''; }}>
          <option value="">Choose a configured model</option>
          {#each draft.models.filter((m) => !pool.models.includes(m.id)) as model}
            <option value={model.id}>{model.name} ({model.provider}/{model.model})</option>
          {/each}
        </select>
      </label>
    </div>
  {/each}
</div>

<div class="card p-4">
  <h3 class="text-sm font-semibold text-ink mb-3">Add pool</h3>
  <div class="flex items-end gap-3 max-w-xl">
    <label class="form-field flex-1"><span>Pool name</span><input class="input" placeholder="Coding pool" bind:value={newPoolName} /></label>
    <button class="btn-outline h-9 whitespace-nowrap shrink-0" onclick={addPool}><Plus class="w-4 h-4" />Add pool</button>
  </div>
</div>
