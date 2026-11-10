<script lang="ts">
  import type { DelegationState } from '$lib/stores/delegation.svelte';
  import { agentActive, agentStateLabel } from '$lib/delegation';
  import { app } from '$lib/stores/app.svelte';
  import { inspector } from '$lib/stores/inspector.svelte';
  import { fmtTokens, fmtUsd } from '$lib/format';
  import { ChevronDown, TriangleAlert } from '@lucide/svelte';
  import { groupAgents, statusCounts, taskStopNeedsSettings } from '$lib/agentGroups';
  import AgentRow from './AgentRow.svelte';
  import IntegrationStrip from './IntegrationStrip.svelte';
  import { buildAgentTree } from '$lib/agentTree';
  import { flattenTree, reportable, statusReport } from '$lib/agentRuntime';
  let { state: delegation, threadId, projectId, onsend, resolveQueued = false }: { state: DelegationState; threadId: string; projectId?: string; onsend?: (text: string) => void; resolveQueued?: boolean } = $props();
  const count = $derived(delegation.tasks.reduce((n, task) => n + (task.agents?.length ?? 0), 0));
  const historyCount = $derived(delegation.tasks.reduce((n, task) => n + task.children.length, 0));
  const allAgents = $derived(delegation.tasks.flatMap(t => t.agents ?? []));
  const groups = $derived(delegation.tasks.flatMap(t => groupAgents(flattenTree(buildAgentTree(t.agents ?? [])))));
  const counts = $derived(statusCounts(groups));
  // Usage across tasks whose shared budget is readable; unavailable only when none is.
  const usage = $derived.by(() => {
    const known = delegation.tasks.filter(t => t.budgetAvailable && t.usage);
    if (!known.length) return undefined;
    return { tokens: known.reduce((n, t) => n + t.usage!.inputTokens + t.usage!.outputTokens, 0), cost: known.reduce((n, t) => n + (t.usage!.costUsd ?? 0), 0) };
  });
  let open = $state(false);
  const dot: Record<string, string> = { running: 'bg-accent', attention: 'bg-amber-warm', failed: 'bg-rust', stopped: 'bg-amber-warm', waiting: 'border-[1.5px] border-[#8a95a8]', done: 'bg-sage', neutral: 'bg-[#8a95a8]' };
  function openSettings() { app.settingsTab = 'models'; app.view = 'settings'; }
  const activeTask = $derived(delegation.tasks.find(s => agentActive(s.task.state)));
  // The cancel reply's report when this session cancelled; otherwise rebuilt from the persisted status (reload, second client).
  const reportFor = (s: (typeof delegation.tasks)[number]) => delegation.reports[s.task.id] ?? (s.integration && reportable(s.task.state) ? statusReport(s.integration) : undefined);
  // Keep an open agent tab current as updates arrive. syncAgent compares by agent identity and only writes
  // when it differs; it must not be changed to write unconditionally or this effect would re-trigger itself.
  $effect(() => { for (const a of allAgents) inspector.syncAgent(a, threadId, projectId); });
</script>

{#if delegation.tasks.length}
  <section class="mb-3 overflow-hidden rounded-xl border border-line bg-surface text-xs" aria-label="Agents" data-agents>
    <div class="flex flex-wrap items-center gap-x-3 gap-y-1 py-2.5 pl-3.5 pr-3">
      <button class="flex items-center gap-2 text-[13px] font-semibold leading-5" aria-expanded={open} onclick={() => (open = !open)}>
        <ChevronDown size={14} class="text-[#566378] transition-transform motion-reduce:transition-none {open ? '' : '-rotate-90'}" aria-hidden="true" />Agents
      </button>
      <span class="flex flex-wrap items-center gap-x-3.5 gap-y-1 text-[#566378]">
        {#each counts as c (c.tone)}<span class="flex items-center gap-1.5"><span class="size-2 rounded-full {dot[c.tone]}" aria-hidden="true"></span>{c.label}</span>{/each}
        {#if app.approvals.some(a => a.threadId === threadId && a.agentChildId)}<span class="text-amber-warm">Approval needed</span>{/if}
      </span>
      <span class="ml-auto tabular-nums text-[#566378]">{#if usage}{fmtTokens(usage.tokens)} tokens · {fmtUsd(usage.cost)}{:else}Usage unavailable{/if}</span>
      {#if activeTask}<button class="btn-ghost btn-sm" disabled={delegation.pending} onclick={() => delegation.cancel(activeTask.task.id)}>Cancel task</button>{/if}
    </div>
    <div class="max-h-96 overflow-y-auto" hidden={!open}>
      {#each delegation.tasks as snapshot (snapshot.task.id)}
        <section class="min-w-0 border-t border-line" aria-label="Delegated task">
          {#if delegation.tasks.length > 1}
            <p class="px-3.5 pt-3 text-[#566378]"><span class="font-medium capitalize text-ink-soft">{snapshot.task.state.replaceAll('_', ' ')}</span>{#if snapshot.budgetAvailable && snapshot.usage} · {fmtTokens(snapshot.usage.inputTokens + snapshot.usage.outputTokens)} tokens · {fmtUsd(snapshot.usage.costUsd)}{/if}</p>
          {/if}
          {#if snapshot.stopReason}
            <div class="mx-3 mt-3 flex items-center gap-2.5 rounded-lg bg-[#f6ecd9] px-2.5 py-2 text-[#6b430c]" role="status">
              <TriangleAlert size={14} class="flex-none" aria-hidden="true" />
              <span class="min-w-0 whitespace-pre-wrap break-words">{snapshot.stopReason}</span>
              {#if taskStopNeedsSettings(snapshot.stopReason)}<button class="ml-auto flex-none font-medium text-accent-strong hover:underline" onclick={openSettings}>Open settings</button>{/if}
            </div>
          {/if}
          {#if snapshot.agents?.length}
            <ul class="divide-y divide-line {snapshot.stopReason ? 'mt-3 border-t border-line' : ''}" aria-label="Agents">
              {#each groupAgents(flattenTree(buildAgentTree(snapshot.agents))) as group (group.key)}
                <AgentRow {group} selected={inspector.active?.kind === 'agent' && inspector.active.agent?.id === group.latest.id} pending={delegation.pending} onselect={() => inspector.openAgent(group.latest, threadId, projectId)} oncancel={() => delegation.cancel(snapshot.task.id, group.latest.id)} />
              {/each}
            </ul>
          {/if}
          {#if snapshot.integration || reportFor(snapshot) || snapshot.children.length || snapshot.workflow}
          <div class="space-y-2 px-3.5 py-3">
          {#if snapshot.integration || reportFor(snapshot)}
            <IntegrationStrip integration={snapshot.integration} report={reportFor(snapshot)} agents={snapshot.agents ?? []} canResolve={agentActive(snapshot.task.state) && !!onsend} resolveQueued={resolveQueued} onresolve={(text) => onsend?.(text)} />
          {/if}
          {#if snapshot.children.length}
            <div class="space-y-1.5" aria-label="Earlier agent runs">
              <p class="text-[#566378]">Earlier agent run{snapshot.children.length === 1 ? '' : 's'}</p>
              {#each snapshot.children as child (child.id)}
                <div class="rounded-md border border-line p-2.5 min-w-0">
                  <div class="flex flex-wrap gap-x-2 gap-y-1">
                    <span class="font-medium whitespace-pre-wrap break-words">{child.assignment.prompt}</span>
                    <span class="text-[#566378] break-all">{child.model?.model || 'Model not recorded'}</span>
                    <span class:text-rust={['failed', 'interrupted', 'cancelled'].includes(child.state)} class="ml-auto">{agentStateLabel(child)}</span>
                  </div>
                  {#if child.result.summary}<p class="mt-1 whitespace-pre-wrap break-words text-ink-soft selectable">{child.result.summary}</p>{/if}
                </div>
              {/each}
            </div>
          {/if}
          {#if snapshot.workflow}
            <details><summary class="cursor-pointer text-[#566378] hover:text-ink">Lead verification</summary>
              <div class="mt-2 space-y-2 break-words selectable">
                {#each snapshot.workflow.checks ?? [] as check}<p><span class="font-medium">{check.status}</span> · {check.label}<code class="block text-[11px] break-all">{check.command}</code>{#if check.summary || check.reason}<span class="block text-[#566378] whitespace-pre-wrap">{check.summary || check.reason}</span>{/if}</p>{/each}
                {#if snapshot.workflow.cleanupWarning}<p class="text-rust">{snapshot.workflow.cleanupWarning}</p>{/if}
              </div>
            </details>
          {/if}
          </div>
          {/if}
        </section>
      {/each}
    </div>
  </section>
{/if}
{#if delegation.error}<div class="mb-2 text-xs text-rust" role="alert">{delegation.error}<button class="btn-ghost btn-sm" onclick={() => delegation.hydrate()}>Reload agents</button></div>{/if}
