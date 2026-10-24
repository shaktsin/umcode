<script lang="ts">
 import { app } from '$lib/stores/app.svelte';
 import { errMsg } from '$lib/format';
 import { capacityFields, describeCapacity } from '$lib/modelCapacity';
 import type { ModelCapacityResult } from '$lib/types';
 let { provider, model, name }: { provider: string; model: string; name: string } = $props();
 let credentialId = $state('');
 const credentials = $derived(app.credentials.filter(c => c.provider === provider && c.enabled));
 let result = $state<ModelCapacityResult | null>(null);
 let editing = $state(false);
 let contextWindow = $state<number | undefined>(undefined);
 let ollama = $state(false);
 let busy = $state(false);
 let error = $state('');
 let generation = 0;
 $effect(() => {
  if (!credentials.some(c => c.id === credentialId)) credentialId = credentials.find(c => c.isDefault)?.id ?? credentials[0]?.id ?? '';
 });
 $effect(() => { const id = credentialId; const idModel = model; if (id && idModel) void load(id, idModel); else result = null; });
 async function load(id = credentialId, idModel = model) {
  const request = ++generation;
  busy = true; error = ''; result = null;
  try {
   const r = await app.call<ModelCapacityResult>('model/capacity/get', { credentialId: id, model: idModel, refresh: true });
   if (request !== generation || credentialId !== id || model !== idModel) return;
   result = r; contextWindow = r.profile.contextWindow; ollama = r.profile.backend === 'ollama';
  } catch (e) { if (request === generation) error = errMsg(e); }
  finally { if (request === generation) busy = false; }
 }
 // Maximum response and response reservation are managed automatically; saving
 // keeps whatever an earlier version stored instead of clearing it.
 async function save(window: number | undefined) {
  if (!result || busy) return;
  const id = credentialId; const idModel = model; const request = ++generation;
  busy = true; error = '';
  try {
   const p = result.profile;
   const profile = { provider: p.provider, endpointId: p.endpointId, model: p.model, backend: ollama ? 'ollama' : 'generic', ...capacityFields(window, p.maxOutputTokens, p.responseReserveTokens) };
   const r = await app.call<ModelCapacityResult>('model/capacity/set', { credentialId: id, model: idModel, profile });
   if (request === generation && credentialId === id && model === idModel) { result = r; contextWindow = r.profile.contextWindow; editing = false; app.toast('info', 'Model capacity saved.'); }
  } catch (e) { if (request === generation) error = errMsg(e); }
  finally { if (request === generation) busy = false; }
 }
 function reset() { contextWindow = undefined; void save(undefined); }
</script>

<section class="border-t border-line pt-3 mt-3">
 <div class="flex flex-wrap items-baseline gap-x-3 gap-y-1">
  <h4 class="text-sm font-medium text-ink">{name} <span class="font-mono text-xs text-muted">{model}</span></h4>
  {#if credentials.length > 1}
   <select class="select select-sm" aria-label="Endpoint credential" bind:value={credentialId}>{#each credentials as c (c.id)}<option value={c.id}>{c.label || c.id}</option>{/each}</select>
  {/if}
 </div>
 {#if credentials.length === 0}
  <p class="text-xs text-muted mt-1">Add an enabled credential for this provider to see its context window.</p>
 {:else if result}
  <p class="text-xs text-muted mt-1" role="status">{describeCapacity(result)}
   {#if !editing}<button class="btn-ghost btn-sm ml-2" disabled={busy} onclick={() => (editing = true)}>Change</button>{/if}
  </p>
  {#if editing}
   <div class="flex flex-wrap gap-3 items-end mt-2">
    <label class="form-field"><span>Context window (tokens, optional)</span><input class="input" type="number" min="1" step="1" placeholder="Automatic" bind:value={contextWindow} disabled={busy} /></label>
    {#if provider === 'openai_compatible'}
     <label class="flex items-center gap-2 text-xs text-muted pb-2"><input type="checkbox" bind:checked={ollama} disabled={busy} /> Ollama server (read the loaded context size)</label>
    {/if}
   </div>
   <div class="flex gap-2 mt-2">
    <button class="btn-outline btn-sm" disabled={busy} onclick={() => save(contextWindow)}>Save</button>
    <button class="btn-ghost btn-sm" disabled={busy} onclick={reset}>Reset to automatic</button>
    <button class="btn-ghost btn-sm" disabled={busy} onclick={() => { editing = false; contextWindow = result?.profile.contextWindow; }}>Cancel</button>
   </div>
  {/if}
 {:else if busy}<p class="text-xs text-muted mt-1" role="status">Checking context window…</p>{/if}
 {#if error}<p class="text-xs text-rust mt-1" role="alert">{error}</p>{/if}
</section>
