<script lang="ts">
  import { Handle, Position, type NodeProps } from '@xyflow/svelte';
  import { definition, type LayerKind } from '$lib/workbench/graph';
  let { data, selected }: NodeProps = $props();
  const layer = $derived(definition(data.kind as LayerKind));
</script>

<div class="layer-card" class:selected class:invalid={!!data.issue} style:--layer-color={layer.color} data-layer-kind={layer.kind} title={String(data.issue || layer.description)}>
  {#if layer.kind !== 'input'}<Handle type="target" position={Position.Top} id="in" aria-label={`Input to ${layer.name}`} />{/if}
  <div class="layer-title"><span class="layer-stripe"></span><strong>{layer.name}</strong>{#if data.repeat}<span class="repeat">× {String(data.repeat)}</span>{/if}</div>
  <div class="layer-summary">{String(data.summary || layer.description)}</div>
  <div class="layer-shape" class:has-error={!!data.issue}>{data.issue ? '⚠ Check layer settings' : String(data.shape)}</div>
  {#if layer.kind !== 'output'}<Handle type="source" position={Position.Bottom} id="out" aria-label={`Output from ${layer.name}`} />{/if}
</div>

<style>
  .layer-card { width:206px; padding:12px 14px 9px; border:1px solid var(--color-line); border-radius:10px; background:var(--color-paper); box-shadow:0 2px 5px #00000008; color:var(--color-ink); }
  .layer-card.selected { border-color:var(--color-accent); box-shadow:0 0 0 2px var(--color-accent-soft); }
  .layer-card.invalid { border-color:var(--color-rust); }
  .layer-title { display:flex; align-items:center; gap:8px; font-size:12px; }
  .layer-stripe { width:5px; height:17px; border-radius:3px; background:var(--layer-color); }
  .repeat { margin-left:auto; color:var(--layer-color); font-size:11px; font-weight:650; }
  .layer-summary { color:var(--color-muted); font-size:10px; margin-top:6px; overflow:hidden; text-overflow:ellipsis; white-space:nowrap; }
  .layer-shape { color:var(--color-muted); font:10px var(--font-mono); margin-top:8px; padding-top:7px; border-top:1px solid var(--color-line); }
  .has-error { color:var(--color-rust); }
  :global(.svelte-flow__handle) { width:11px; height:11px; background:var(--color-accent); border:2px solid var(--color-paper); }
</style>
