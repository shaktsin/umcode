<script lang="ts" module>
 // Dismissals last for the session; the notice returns after a restart.
 const dismissed = $state<Record<string, boolean>>({});
</script>
<script lang="ts">
 import { app } from '$lib/stores/app.svelte';
 import { capacityIsAssumed, assumedCapacityNotice } from '$lib/modelCapacity';
 import type { ModelCapacityResult, ModelSelection } from '$lib/types';
 let { selection }: { selection: ModelSelection } = $props();
 let assumed = $state(false);
 let generation = 0;
 const provider = $derived(selection.provider);
 const model = $derived(selection.model);
 const credentialId = $derived(
  selection.credentialId ?? app.credentials.find(c => c.provider === provider && c.enabled && c.isDefault)?.id
  ?? app.credentials.find(c => c.provider === provider && c.enabled)?.id);
 const key = $derived(`${provider}/${model}/${credentialId}`);
 $effect(() => {
  const request = ++generation;
  assumed = false;
  if (!provider || provider === 'pool' || !model || !credentialId) return;
  const id = credentialId, idModel = model;
  app.call<ModelCapacityResult>('model/capacity/get', { credentialId: id, model: idModel })
   .then((r) => { if (request === generation) assumed = capacityIsAssumed(r); })
   .catch(() => { if (request === generation) assumed = false; });
 });
 function openSettings() { app.settingsTab = 'models'; app.view = 'settings'; }
</script>
{#if assumed && model && !dismissed[key]}
 <div class="capacity-notice" role="status">
  <span>{assumedCapacityNotice(model)}</span>
  <span class="actions"><button class="link" onclick={openSettings}>Set limit</button><button class="link" onclick={() => (dismissed[key] = true)}>Dismiss</button></span>
 </div>
{/if}
<style>
 .capacity-notice{display:flex;justify-content:space-between;align-items:baseline;gap:4px 12px;flex-wrap:wrap;padding:6px 16px;margin:0 0 4px;font-size:11px;color:var(--color-muted);background:var(--color-raised);border-top:1px solid var(--color-line)}
 .actions{display:flex;gap:12px}
 .link{background:none;border:0;padding:0;color:var(--color-accent);cursor:pointer;text-decoration:underline;font:inherit}
</style>
