<script lang="ts">
  import type { DelegationState } from '$lib/stores/delegation.svelte';
  import { agentActive, agentStateLabel } from '$lib/delegation';
  import { app } from '$lib/stores/app.svelte';
  import { inspector } from '$lib/stores/inspector.svelte';
  import { fmtUsd } from '$lib/format';
  import AgentRow from './AgentRow.svelte';
  import IntegrationStrip from './IntegrationStrip.svelte';
  import { buildAgentTree } from '$lib/agentTree';
  import { agentsSummary, flattenTree, reportable, statusReport } from '$lib/agentRuntime';
  let { state, threadId, projectId, onsend, resolveQueued = false }: { state: DelegationState; threadId: string; projectId?: string; onsend?: (text: string) => void; resolveQueued?: boolean } = $props();
  const count = $derived(state.tasks.reduce((n, task) => n + (task.agents?.length ?? 0), 0));
  const historyCount = $derived(state.tasks.reduce((n, task) => n + task.children.length, 0));
  const allAgents = $derived(state.tasks.flatMap(t => t.agents ?? []));
  const summary = $derived(agentsSummary(allAgents));
  const activeTask = $derived(state.tasks.find(s => agentActive(s.task.state)));
  // The cancel reply's report when this session cancelled; otherwise rebuilt from the persisted status (reload, second client).
  const reportFor = (s: (typeof state.tasks)[number]) => state.reports[s.task.id] ?? (s.integration && reportable(s.task.state) ? statusReport(s.integration) : undefined);
  // Keep an open agent tab current as updates arrive. syncAgent compares by agent identity and only writes
  // when it differs; it must not be changed to write unconditionally or this effect would re-trigger itself.
  $effect(() => { for (const a of allAgents) inspector.syncAgent(a, threadId, projectId); });
</script>

{#if state.tasks.length}
  <div class="relative mx-3 mb-3">
  <details class="rounded-lg border border-line bg-surface text-xs" data-agents>
    <summary class="cursor-pointer px-3 py-2 select-none text-ink-soft {activeTask ? 'pr-24' : ''}">Agents · {count}{#if historyCount} <span class="ml-2 text-muted">Earlier runs · {historyCount}</span>{/if}{#if summary.models} <span class="ml-2 text-muted break-all">{summary.models}</span>{/if}
      {#if state.tasks.some(s => agentActive(s.task.state))}<span class="ml-2 text-muted">Active{summary.active ? ` · ${summary.active} running` : ''}</span>{/if}
      {#if app.approvals.some(a => a.threadId === threadId && a.agentChildId)}<span class="ml-2 text-amber-warm">Approval needed</span>{/if}
    </summary>
    <div class="border-t border-line divide-y divide-line max-h-80 overflow-y-auto">
      {#each state.tasks as snapshot (snapshot.task.id)}
        <section class="p-3 space-y-2 min-w-0" aria-label="Delegated task">
          <div class="flex flex-wrap items-center gap-2">
            <span class="font-medium capitalize">{snapshot.task.state.replaceAll('_', ' ')}</span>
            {#if snapshot.budgetAvailable && snapshot.usage}<span class="text-muted">Shared usage · {snapshot.usage.inputTokens + snapshot.usage.outputTokens} tokens · {fmtUsd(snapshot.usage.costUsd)}</span>{:else}<span class="text-muted">Shared usage unavailable</span>{/if}
          </div>
          {#if snapshot.stopReason}<p class="text-rust whitespace-pre-wrap break-words" role="status">{snapshot.stopReason}</p>{/if}
          {#if snapshot.agents?.length}
            <ul class="space-y-1.5" aria-label="Agents">
              {#each flattenTree(buildAgentTree(snapshot.agents)) as row (row.agent.id)}
                <AgentRow agent={row.agent} depth={row.depth} selected={inspector.active?.kind === 'agent' && inspector.active.agent?.id === row.agent.id} pending={state.pending} onselect={() => inspector.openAgent(row.agent, threadId, projectId)} oncancel={() => state.cancel(snapshot.task.id, row.agent.id)} />
              {/each}
            </ul>
          {/if}
          {#if snapshot.integration || reportFor(snapshot)}
            <IntegrationStrip integration={snapshot.integration} report={reportFor(snapshot)} agents={snapshot.agents ?? []} canResolve={agentActive(snapshot.task.state) && !!onsend} resolveQueued={resolveQueued} onresolve={(text) => onsend?.(text)} />
          {/if}
          {#if snapshot.children.length}
            <div class="space-y-1.5" aria-label="Earlier agent runs">
              <p class="text-muted">Earlier agent run{snapshot.children.length === 1 ? '' : 's'}</p>
              {#each snapshot.children as child (child.id)}
                <div class="rounded-md border border-line p-2.5 min-w-0">
                  <div class="flex flex-wrap gap-x-2 gap-y-1">
                    <span class="font-medium whitespace-pre-wrap break-words">{child.assignment.prompt}</span>
                    <span class="text-muted break-all">{child.model?.model || 'Model not recorded'}</span>
                    <span class:text-rust={['failed', 'interrupted', 'cancelled'].includes(child.state)} class="ml-auto">{agentStateLabel(child)}</span>
                  </div>
                  {#if child.result.summary}<p class="mt-1 whitespace-pre-wrap break-words text-ink-soft selectable">{child.result.summary}</p>{/if}
                </div>
              {/each}
            </div>
          {/if}
          {#if snapshot.workflow}
            <details><summary class="cursor-pointer text-muted hover:text-ink">Lead verification</summary>
              <div class="mt-2 space-y-2 break-words selectable">
                {#each snapshot.workflow.checks ?? [] as check}<p><span class="font-medium">{check.status}</span> · {check.label}<code class="block text-[11px] break-all">{check.command}</code>{#if check.summary || check.reason}<span class="block text-muted whitespace-pre-wrap">{check.summary || check.reason}</span>{/if}</p>{/each}
                {#if snapshot.workflow.cleanupWarning}<p class="text-rust">{snapshot.workflow.cleanupWarning}</p>{/if}
              </div>
            </details>
          {/if}
        </section>
      {/each}
    </div>
  </details>
  {#if activeTask}<button class="btn-ghost btn-sm absolute right-2 top-1" disabled={state.pending} onclick={() => state.cancel(activeTask.task.id)}>Cancel task</button>{/if}
  </div>
{/if}
{#if state.error}<div class="mx-3 mb-2 text-xs text-rust" role="alert">{state.error}<button class="btn-ghost btn-sm" onclick={() => state.hydrate()}>Reload agents</button></div>{/if}
