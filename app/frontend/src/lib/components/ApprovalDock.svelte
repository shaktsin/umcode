<script lang="ts">
  import { ShieldCheck } from '@lucide/svelte';
  import type { Approval } from '$lib/types';
  import { dockState } from '$lib/approvalDock';
  import ApprovalCard from './ApprovalCard.svelte';

  let { approvals, threadId }: { approvals: Approval[]; threadId: string | null } = $props();
  const state = $derived(dockState(approvals, threadId));
  const current = $derived(state.current);
</script>

{#if current}
  <div
    id="approval-dock"
    role="region"
    aria-live="assertive"
    aria-label="Approval needed"
    class="approval-dock rounded-t-2xl border border-b-0 border-amber-warm/50 bg-clay-soft/30 px-3 pt-2.5 pb-2"
  >
    <div class="flex items-center gap-2 mb-1.5">
      <ShieldCheck class="w-4 h-4 text-amber-warm" />
      <h3 class="text-sm font-semibold text-ink">Approval needed</h3>
      <span class="ml-auto flex items-center gap-2 text-[10px] uppercase tracking-wide text-amber-warm">
        <span>{current.risk} risk</span>
        {#if state.total > 1}<span>1 of {state.total}</span>{/if}
      </span>
    </div>
    <div id={`approval-${current.id}`}>
      <ApprovalCard approval={current} chatContext />
    </div>
  </div>
{/if}
