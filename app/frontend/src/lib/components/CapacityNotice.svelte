<script lang="ts" module>
 // Dismissals last for the session; the notice returns after a restart.
 const dismissed = $state<Record<string, boolean>>({});
</script>
<script lang="ts">
 import { app } from '$lib/stores/app.svelte';
 import { capacityIsAssumed } from '$lib/modelCapacity';
 import CapacityLine from './CapacityLine.svelte';
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
 <CapacityLine {model} onsetlimit={openSettings} ondismiss={() => (dismissed[key] = true)} />
{/if}
