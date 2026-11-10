<script lang="ts">
 import { ChevronRight } from '@lucide/svelte';
 import type { CompactionState } from '$lib/types';
 import { compactionActive, compactionLabel, formatCompactionTime } from '$lib/compaction';

 let { state, now }: { state: CompactionState | null; now: number } = $props();
 const phases = [
  { status: 'preparing', active: 'Preparing working context', done: 'Prepared working context' },
  { status: 'summarizing', active: 'Summarizing earlier context', done: 'Summarized earlier context' },
  { status: 'restoring', active: 'Restoring working context', done: 'Restored working context' }
 ];
</script>

{#if state && compactionActive(state)}
 {@const currentIndex = phases.findIndex((phase) => phase.status === state.status)}
 <details class="compaction-progress">
  <summary class="compaction-summary">
   <span class="compaction-spinner" role="progressbar" aria-label="Compacting context"></span>
   <span class="compaction-label" aria-live="polite" aria-atomic="true">{compactionLabel(state)}</span>
   <ChevronRight class="compaction-chevron" size={16} aria-hidden="true" />
   <time class="compaction-time" title="Time compacting">{formatCompactionTime(state.startedAt, now)}</time>
  </summary>
  <ol class="compaction-activity" aria-label="Compaction activity">
   {#each phases as phase, index}
    <li class:done={index < currentIndex} class:current={index === currentIndex} aria-current={index === currentIndex ? 'step' : undefined}>
     <span class="activity-dot" aria-hidden="true"></span>
     <span>{index < currentIndex ? phase.done : phase.active}</span>
    </li>
   {/each}
  </ol>
 </details>
{/if}

<style>
 .compaction-progress { min-width: 0; color: var(--color-muted); }
 .compaction-summary { display: flex; align-items: center; gap: 9px; min-height: 36px; cursor: pointer; list-style: none; user-select: none; }
 .compaction-summary::-webkit-details-marker { display: none; }
 .compaction-summary:focus-visible { outline: 2px solid var(--color-accent); outline-offset: 2px; border-radius: 4px; }
 .compaction-spinner { flex: none; width: 18px; height: 18px; border: 2px solid var(--color-line); border-top-color: var(--color-accent); border-radius: 50%; animation: compaction-spin .85s linear infinite; }
 .compaction-label { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: var(--color-ink); font-size: 14px; font-weight: 600; }
 :global(.compaction-chevron) { flex: none; color: var(--color-muted); transition: transform .18s ease; }
 .compaction-progress[open] :global(.compaction-chevron) { transform: rotate(90deg); }
 .compaction-time { flex: none; margin-left: auto; color: var(--color-muted); font-size: 12px; font-variant-numeric: tabular-nums; }
 .compaction-activity { position: relative; display: grid; gap: 7px; margin: 4px 0 2px 8px; padding: 0 0 0 21px; list-style: none; font-size: 12px; }
 .compaction-activity::before { position: absolute; left: 0; top: 6px; bottom: 6px; width: 1px; background: var(--color-line); content: ''; }
 .compaction-activity li { position: relative; min-width: 0; }
 .activity-dot { position: absolute; left: -25px; top: 50%; width: 9px; height: 9px; border-radius: 50%; background: var(--color-line-strong); transform: translateY(-50%); }
 .compaction-activity li.done .activity-dot { background: var(--color-sage); }
 .compaction-activity li.current { color: var(--color-accent-strong); font-weight: 600; }
 .compaction-activity li.current .activity-dot { background: var(--color-accent); }
 @keyframes compaction-spin { to { transform: rotate(360deg); } }
 @media (prefers-reduced-motion: reduce) {
  .compaction-spinner { animation: none; }
  :global(.compaction-chevron) { transition: none; }
 }
</style>
