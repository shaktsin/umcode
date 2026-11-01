<script lang="ts">
  import type { AgentIntegrationReport, AgentIntegrationStatus, RuntimeAgent } from '$lib/types';
  import { conflictRevisions, reportView, resolveMessage } from '$lib/agentRuntime';
  let { integration, report, agents, canResolve, onresolve }: { integration?: AgentIntegrationStatus; report?: AgentIntegrationReport; agents: RuntimeAgent[]; canResolve: boolean; onresolve: (text: string) => void } = $props();
  const conflicts = $derived(conflictRevisions(integration, agents));
  const view = $derived(report ? reportView(report, agents) : null);
  const amendReason = $derived(view ? view.amended : integration?.amendments ? (integration.amendReason || 'Amended by lead') : undefined);
  const head = $derived(report ?? integration);
</script>

{#if head}
  <div class="rounded-md border border-line bg-raised/50 px-2.5 py-2 space-y-1 text-xs min-w-0" data-integration>
    <div class="flex flex-wrap items-center gap-x-2 gap-y-1">
      <span class="font-medium">Integration</span>
      <span class="pill {head.verified ? 'bg-sage-soft text-sage' : 'bg-clay-soft/40 text-amber-warm'}">head {head.verified ? 'verified' : 'unverified'}</span>
      {#if head.headTree}<code class="text-[11px] text-muted">{head.headTree.slice(0, 7)}</code>{/if}
      <span class="text-muted">{head.appliedCount} applied</span>
      {#if conflicts.length}<span class="text-rust">{conflicts.length} {conflicts.length === 1 ? 'conflict' : 'conflicts'}</span>{/if}
    </div>
    {#if !view}
      {#each conflicts as c (c.agentId)}
        <div class="flex flex-wrap items-center gap-2" data-conflict={c.agentId}>
          <span class="text-rust break-words min-w-0">{c.name}{c.conflict ? ` · ${c.conflict}` : ''}</span>
          {#if canResolve}<button class="btn-outline btn-sm" onclick={() => onresolve(resolveMessage(c.name))}>Resolve</button>{/if}
        </div>
      {/each}
    {/if}
    {#if amendReason}<p class="text-ink-soft whitespace-pre-wrap break-words">Amended by lead · {amendReason}</p>{/if}
    {#if view}
      <p class="font-medium">{view.staged}</p>
      {#if view.unintegrated.length}<p class="text-rust break-words">Not integrated: {view.unintegrated.join(', ')}. Their work lives only in their workspaces.</p>{/if}
      {#if view.blockedAgents.length}<p class="text-rust break-words">Blocked: {view.blockedAgents.join(', ')}</p>{/if}
      {#if view.blocked}<p class="text-rust whitespace-pre-wrap break-words selectable">{view.blocked}</p>{/if}
      {#if view.message}<p class="text-muted whitespace-pre-wrap break-words">{view.message}</p>{/if}
    {:else if integration?.blocked}<p class="text-rust whitespace-pre-wrap break-words selectable">{integration.blocked}</p>{/if}
  </div>
{/if}
