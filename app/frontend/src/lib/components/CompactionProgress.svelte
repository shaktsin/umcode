<script lang="ts">
 import type {CompactionState} from '$lib/types';
 import {compactionActive,compactionLabel} from '$lib/compaction';
 let {state}:{state:CompactionState|null}=$props();
</script>
{#if state && compactionActive(state)}
 <div class="compaction-status" aria-live="polite" aria-atomic="true">
  <div class="compaction-copy"><span>Automatically compacting context…</span><span class="phase">{compactionLabel(state)}</span></div>
  <div class="compaction-track" role="progressbar" aria-label="Automatically compacting context"><span></span></div>
 </div>
{/if}
<style>
 .compaction-status{padding:8px 16px 10px;color:var(--color-muted);font-size:11px;min-width:0}
 .compaction-copy{display:flex;justify-content:space-between;gap:4px 12px;flex-wrap:wrap;margin-bottom:6px}
 .phase{opacity:.8}
 .compaction-track{height:3px;border-radius:4px;background:var(--color-line);overflow:hidden}
 .compaction-track span{display:block;width:35%;height:100%;border-radius:inherit;background:var(--color-accent);animation:compaction-travel 1.8s ease-in-out infinite}
 @keyframes compaction-travel{from{transform:translateX(-100%)}to{transform:translateX(390%)}}
 @media(prefers-reduced-motion:reduce){.compaction-track span{animation:none;transform:none;width:100%;opacity:.55}}
</style>
