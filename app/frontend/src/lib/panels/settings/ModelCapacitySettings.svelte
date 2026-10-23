<script lang="ts">
 import { app } from '$lib/stores/app.svelte';
 import { errMsg } from '$lib/format';
 import { capacityFields } from '$lib/modelCapacity';
 import type { ModelCapacityResult } from '$lib/types';
 let { provider, model, name }: { provider: string; model: string; name: string } = $props();
 let credentialId = $state('');
 const credentials = $derived(app.credentials.filter(c => c.provider === provider && c.enabled));
 let result = $state<ModelCapacityResult | null>(null);
 let contextWindow = $state<number | undefined>(undefined);
 let maxOutput = $state<number | undefined>(undefined);
 let responseReserve = $state<number | undefined>(undefined);
 let backend = $state<'generic' | 'ollama'>('generic');
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
   result = r; contextWindow = r.profile.contextWindow; maxOutput = r.profile.maxOutputTokens; responseReserve = r.profile.responseReserveTokens; backend = r.profile.backend;
  } catch (e) { if (request === generation) error = errMsg(e); }
  finally { if (request === generation) busy = false; }
 }
 async function save() {
  if (!result || busy) return;
  const id = credentialId; const idModel = model; const request = ++generation;
  busy = true; error = '';
  try {
   const p = result.profile;
   const profile = { provider: p.provider, endpointId: p.endpointId, model: p.model, backend, ...capacityFields(contextWindow, maxOutput, responseReserve) };
   const r = await app.call<ModelCapacityResult>('model/capacity/set', { credentialId: id, model: idModel, profile });
   if (request === generation && credentialId === id && model === idModel) { result = r; app.toast('info', 'Model capacity saved.'); }
  } catch (e) { if (request === generation) error = errMsg(e); }
  finally { if (request === generation) busy = false; }
 }
</script>

<section class="border-t border-line pt-4 mt-4">
 <h4 class="text-sm font-medium text-ink">{name} <span class="font-mono text-xs text-muted">{model}</span></h4>
 {#if credentials.length === 0}
  <p class="text-xs text-muted mt-2">Add an enabled credential for this provider to configure its capacity.</p>
 {:else}
  <div class="flex flex-wrap gap-3 items-end mt-3">
   <label class="form-field min-w-40"><span>Endpoint credential</span><select class="select" bind:value={credentialId}>{#each credentials as c (c.id)}<option value={c.id}>{c.label || c.id}</option>{/each}</select></label>
   <button class="btn-ghost btn-sm" disabled={busy} onclick={() => load()}>Refresh capacity</button>
  </div>
  {#if result}
   <div class="grid grid-cols-1 sm:grid-cols-2 gap-3 mt-3">
    <label class="form-field"><span>Server type</span><select class="select" bind:value={backend} disabled={busy}><option value="generic">Generic / manual capacity</option>{#if provider === 'openai_compatible'}<option value="ollama">Ollama runtime discovery</option>{/if}</select></label>
    <label class="form-field"><span>Context window (tokens)</span><input class="input" type="number" min="1" step="1" placeholder="Use detected capacity" bind:value={contextWindow} disabled={busy} /></label>
    <label class="form-field"><span>Maximum response (tokens)</span><input class="input" type="number" min="1" step="1" placeholder="Use model default" bind:value={maxOutput} disabled={busy} /></label>
    <label class="form-field"><span>Response reservation (tokens)</span><input class="input" type="number" min="1" step="1" placeholder="Automatic" bind:value={responseReserve} disabled={busy} /></label>
   </div>
   <p class="text-xs text-muted mt-2">Effective context: {result.effectiveContextWindow?.toLocaleString() ?? 'Unavailable'} · {result.source}. A local server may allocate less context than the model advertises. Empty fields clear overrides.</p>
   <button class="btn-outline btn-sm mt-3" disabled={busy} onclick={save}>Save capacity</button>
  {:else if busy}<p class="text-xs text-muted mt-2" role="status">Loading model capacity…</p>{/if}
  {#if error}<p class="text-xs text-rust mt-2" role="alert">{error}</p>{/if}
 {/if}
</section>
