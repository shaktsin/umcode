<script lang="ts">
 import { Info, X } from '@lucide/svelte';
 import { assumedCapacityDetail, assumedCapacityNotice } from '$lib/modelCapacity';
 let { model, onsetlimit, ondismiss }: { model: string; onsetlimit: () => void; ondismiss: () => void } = $props();
 const tipId = `capacity-tip-${Math.random().toString(36).slice(2, 9)}`;
</script>
<div class="capacity-line relative flex items-center gap-2 px-1 text-xs text-[#566378]" role="status">
 <span class="tip-anchor relative -ml-1.5 flex">
  <button type="button" class="flex size-7 items-center justify-center rounded-lg bg-raised text-ink-soft hover:bg-line focus-visible:outline-2 focus-visible:outline-accent" aria-label="Why is the context window assumed?" aria-describedby={tipId}>
   <Info size={14} aria-hidden="true" />
  </button>
  <span id={tipId} role="tooltip" class="tip pointer-events-none absolute bottom-[30px] left-[-2px] z-20 w-[300px] rounded-lg bg-ink px-3 py-2.5 text-xs leading-[18px] text-paper">{assumedCapacityDetail(model)}</span>
 </span>
 <span>{assumedCapacityNotice()}</span>
 <button type="button" class="ml-auto h-7 rounded-lg px-2 font-medium text-accent-strong hover:bg-raised focus-visible:outline-2 focus-visible:outline-accent" onclick={onsetlimit}>Set limit</button>
 <button type="button" class="-mr-1.5 flex size-7 items-center justify-center rounded-lg text-[#566378] hover:bg-raised focus-visible:outline-2 focus-visible:outline-accent" aria-label="Dismiss" onclick={ondismiss}>
  <X size={14} aria-hidden="true" />
 </button>
</div>
<style>
 /* Details appear only while the info button is hovered or keyboard-focused. */
 .tip{opacity:0;visibility:hidden;transition:opacity 120ms ease}
 .tip-anchor:hover .tip,.tip-anchor:focus-within .tip{opacity:1;visibility:visible}
 @media (prefers-reduced-motion: reduce){.tip{transition:none}}
</style>
