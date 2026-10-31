<script lang="ts">
  import { Trash2, X } from '@lucide/svelte';
  import { definition, parseNumericSetting, shapeLabel, visibleFields, type LayerNode, type Analysis, type Config, type Field } from '$lib/workbench/graph';
  let { node, analysis, onconfig, ondelete, onclose }: { node: LayerNode | null; analysis: Analysis; onconfig: (config: Config) => void; ondelete: () => void; onclose: () => void } = $props();
  const layer = $derived(node ? definition(node.kind) : null);
  let inputError = $state('');
  $effect(() => { void node?.id; inputError = ''; });
  function numeric(field: Field, input: HTMLInputElement) {
    if (!node) return;
    try {
      const value = parseNumericSetting(field, input.value);
      inputError = ''; onconfig({ ...node.config, [field.key]: value });
    } catch (error) {
      input.value = String(node.config[field.key]);
      inputError = (error as Error).message + ' Previous value kept.';
    }
  }
</script>

<aside class="layer-inspector" aria-label="Layer properties">
  <div class="inspector-title"><span>Layer properties</span><button class="icon-button" aria-label="Close layer properties" onclick={onclose}><X size={14} /></button></div>
  {#if node && layer}
    <h3>{layer.name}</h3><p class="description">{layer.description}</p>
    {#each visibleFields(node) as field (field.key)}
      <label for={`layer-param-${field.key}`}>{field.label}</label>
      {#if field.type === 'boolean'}
        <input id={`layer-param-${field.key}`} type="checkbox" checked={!!node.config[field.key]} onchange={(e) => onconfig({ ...node!.config, [field.key]: e.currentTarget.checked })} />
      {:else if field.type === 'mode'}
        <select id={`layer-param-${field.key}`} class="select" value={String(node.config[field.key])} onchange={(e) => onconfig({ ...node!.config, [field.key]: e.currentTarget.value })}><option value="tokens">Token IDs</option><option value="image">Images</option><option value="features">Feature vectors</option></select>
      {:else}
        <input id={`layer-param-${field.key}`} class="input" type="number" min={field.min} max={field.max} step={field.step} value={Number(node.config[field.key])} onchange={(e) => numeric(field, e.currentTarget)} />
      {/if}
    {/each}
    {#if inputError}<p class="layer-error" role="alert">{inputError}</p>{/if}
    {#if !layer.fields.length}<p class="description">This layer automatically uses the dimensions from its input.</p>{/if}
    <div class="output-shape"><span>Output shape</span><code>{shapeLabel(analysis.shapes[node.id])}</code></div>
    {#each analysis.errors.filter(e => e.nodeId === node?.id) as error}<p class="layer-error" role="status">{error.message}</p>{/each}
    {#if node.kind === 'transformer'}<p class="description">Each repeated block contains layer norm, {node.config.causal ? 'causal' : 'bidirectional'} attention, and a feed-forward network with residual connections.</p>{/if}
    {#if node.kind === 'attention' || node.kind === 'transformer'}<p class="description">{node.config.causal ? 'The causal mask limits attention to earlier positions, for next-token prediction.' : 'All positions can attend to one another, for image patches and encoders.'}</p>{/if}
    <button class="btn-outline delete-layer" onclick={ondelete}><Trash2 size={13} />Remove layer</button>
  {:else}
    <div class="inspector-empty"><h3>Select a layer</h3><p>Click a layer on the canvas to change its settings and inspect the output size.</p></div>
  {/if}
</aside>

<style>
  .layer-inspector { width:250px; flex-shrink:0; min-height:0; overflow-y:auto; padding:14px; border-left:1px solid var(--color-line); background:var(--color-surface); }
  .inspector-title { display:flex; align-items:center; justify-content:space-between; color:var(--color-muted); font-size:10px; text-transform:uppercase; letter-spacing:.04em; }
  h3 { font-size:14px; font-weight:650; margin:14px 0 8px; }
  .description, .inspector-empty p { color:var(--color-muted); font-size:11px; line-height:1.7; margin:10px 0 18px; }
  label { display:block; font-size:11px; margin:14px 0 6px; color:var(--color-ink-soft); }
  input:not([type=checkbox]), select { width:100%; }
  input[type=checkbox] { accent-color:var(--color-accent); width:16px; height:16px; }
  .output-shape { margin-top:20px; padding-top:16px; border-top:1px solid var(--color-line); display:flex; flex-direction:column; gap:8px; font-size:11px; }
  code { font:10px var(--font-mono); overflow-wrap:anywhere; }
  .layer-error { font-size:11px; line-height:1.7; color:var(--color-rust); margin-top:12px; }
  .delete-layer { margin-top:24px; font-size:11px; }
</style>
