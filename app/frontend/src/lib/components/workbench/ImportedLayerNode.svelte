<script lang="ts">
  import { Handle, Position, type NodeProps } from '@xyflow/svelte';
  let { data, selected }: NodeProps = $props();
</script>
<div class="imported-layer" class:selected data-imported-layer-kind={String(data.kind)}>
  {#if data.kind !== 'input'}<Handle type="target" position={Position.Top} id="in" aria-label={`Input to ${data.label}`} />{/if}
  <strong>{String(data.label)}</strong><span class="kind">{String(data.kind)}{data.repeat ? ` × ${data.repeat}` : ''}</span>
  <p>{String(data.summary || 'Architecture metadata')}</p>
  {#if typeof data.inspect === 'function'}
    <button class="nodrag nopan" aria-label={'Inspect components of ' + String(data.label)} onclick={(event) => { event.stopPropagation(); if (typeof data.inspect === 'function') data.inspect(); }}>Inside layer ›</button>
  {/if}
  <span class="binding" class:needs={data.binding === 'Needs weights'}>{String(data.binding)}</span>
  {#if data.kind !== 'output'}<Handle type="source" position={Position.Bottom} id="out" aria-label={`Output from ${data.label}`} />{/if}
</div>
<style>
  button { color:var(--color-accent); font-size:10px; padding:2px 0 6px; cursor:pointer; }
  .imported-layer { width:235px; padding:13px 14px; border:1px solid var(--color-line); border-left:3px solid var(--color-accent); border-radius:9px; background:var(--color-paper); color:var(--color-ink); box-shadow:0 2px 5px #00000008; }
  .selected { border-color:var(--color-accent); box-shadow:0 0 0 2px var(--color-accent-soft); } strong { font-size:12px; font-weight:650; display:block; } .kind { display:block; font-size:10px; color:var(--color-muted); margin-top:5px; } p { font-size:10px; color:var(--color-muted); margin:8px 0; overflow:hidden; text-overflow:ellipsis; white-space:nowrap; } .binding { display:block; border-top:1px solid var(--color-line); padding-top:7px; font-size:10px; color:var(--color-sage); } .needs { color:var(--color-rust); }
</style>
