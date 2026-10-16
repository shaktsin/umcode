<script lang="ts">
  import type { DelegationState } from '$lib/stores/delegation.svelte';
  import { agentActive, agentStateLabel } from '$lib/delegation';
  import { app } from '$lib/stores/app.svelte';
  import { inspector } from '$lib/stores/inspector.svelte';
  import { fmtUsd } from '$lib/format';
  import ApprovalCard from './ApprovalCard.svelte';
  let { state, threadId, projectId }: { state: DelegationState; threadId: string; projectId?: string } = $props();
</script>

{#if state.tasks.length}
  <details class="mx-3 mb-3 rounded-lg border border-line bg-surface text-xs" data-agents>
    <summary class="cursor-pointer px-3 py-2 select-none text-ink-soft">Agents · {state.tasks.reduce((n, task) => n + task.children.length, 0)}
      {#if state.tasks.some(s => agentActive(s.task.state))}<span class="ml-2 text-muted">Active</span>{/if}
      {#if app.approvals.some(a => a.threadId === threadId && a.agentChildId)}<span class="ml-2 text-amber-warm">Approval needed</span>{/if}
      {#if state.tasks.some(s => Object.keys(s.childErrors).length)}<span class="ml-2 text-rust">Saving needs attention</span>{/if}
    </summary>
    <div class="border-t border-line divide-y divide-line max-h-80 overflow-y-auto">
      {#each state.tasks as snapshot (snapshot.task.id)}
        <section class="p-3 space-y-2 min-w-0" aria-label="Delegated task">
          <div class="flex flex-wrap items-center gap-2">
            <span class="font-medium capitalize">{snapshot.task.state.replaceAll('_', ' ')}</span>
            {#if snapshot.budgetAvailable && snapshot.usage}<span class="text-muted">Shared usage · {snapshot.usage.inputTokens + snapshot.usage.outputTokens} tokens · {fmtUsd(snapshot.usage.costUsd)}</span>{:else}<span class="text-muted">Shared usage unavailable</span>{/if}
            {#if agentActive(snapshot.task.state)}<button class="btn-ghost btn-sm ml-auto" disabled={state.pending} onclick={() => state.cancel(snapshot.task.id)}>Cancel task</button>{/if}
          </div>
          {#if snapshot.stopReason}<p class="text-rust whitespace-pre-wrap break-words" role="status">{snapshot.stopReason}</p>{/if}
          {#each snapshot.children as child (child.id)}
            <article class="rounded-md border border-line p-2.5 min-w-0">
              <div class="flex flex-wrap items-center gap-x-2 gap-y-1">
                <span class="font-medium capitalize">{child.role}</span>
                <span class="text-muted break-all">{child.model?.model || 'Model not recorded'}</span>
                <span class:text-rust={['failed', 'interrupted', 'cancelled'].includes(child.state)} class="ml-auto">{agentStateLabel(child)}</span>
                {#if agentActive(child.state)}<button class="btn-ghost btn-sm" aria-label={`Cancel ${child.role}: ${child.assignment.prompt}`} disabled={state.pending} onclick={() => state.cancel(snapshot.task.id, child.id)}>Cancel</button>{/if}
              </div>
              <p class="mt-1 whitespace-pre-wrap break-words text-ink-soft selectable">{child.assignment.prompt}</p>
              <p class="mt-1 text-[11px] text-muted">{child.usage.inputTokens + child.usage.outputTokens} tokens · {fmtUsd(child.usage.costUsd)} · {child.usage.requests || 0} requests · {child.correctionRounds}/2 corrections</p>
              {#if child.result.blocker}<p class="mt-2 text-rust whitespace-pre-wrap break-words selectable" role="status">{child.result.blocker}</p>{/if}
              {#if snapshot.childErrors[child.id]}<p class="mt-2 text-rust whitespace-pre-wrap break-words" role="alert">{snapshot.childErrors[child.id]}</p><button class="btn-outline btn-sm mt-1" disabled={state.pending} onclick={() => state.cancel(snapshot.task.id, child.id)}>Retry saving outcome</button>{/if}
              <details class="mt-2">
                <summary class="cursor-pointer text-muted hover:text-ink">Result and checks{child.revision ? ` · ${child.revision.slice(0, 7)}` : ''}</summary>
                <div class="mt-2 space-y-2 selectable break-words">
                  {#if child.result.summary}<p class="whitespace-pre-wrap">{child.result.summary}</p>{/if}
                  {#if child.result.review}<p class="text-ink-soft">Review: {child.result.review.verdict.replaceAll('_', ' ')}</p>{/if}
                  {#if child.assignment.acceptanceChecks?.length}<ul class="list-disc pl-4 text-muted">{#each child.assignment.acceptanceChecks as check}<li>{check}</li>{/each}</ul>{/if}
                  {#each [...(child.result.findings ?? []), ...(child.result.review?.findings ?? [])] as finding}
                    <p><span class="font-medium">{finding.severity}</span> · {finding.message}
                      {#if finding.path && child.revision}<button class="text-accent-strong underline break-all" onclick={() => inspector.openAgentFile(finding.path, threadId, snapshot.task.id, child.id, child.revision!, projectId)}>{finding.path}{finding.line ? `:${finding.line}` : ''}</button>{/if}
                    </p>
                  {/each}
                  {#each child.result.checks ?? [] as check}<p><span class="font-medium">{check.status}</span> · <code class="break-all">{check.command}</code>{#if check.summary}<span class="block text-muted whitespace-pre-wrap">{check.summary}</span>{/if}</p>{/each}
                  {#each child.result.changedPaths ?? [] as path}<button class="btn-ghost btn-sm max-w-full" disabled={!child.revision} onclick={() => inspector.openAgentFile(path, threadId, snapshot.task.id, child.id, child.revision!, projectId)}><span class="truncate font-mono">{path}</span></button>{/each}
                </div>
              </details>
              {#each app.approvals.filter(a => a.threadId === threadId && a.agentTaskId === snapshot.task.id && a.agentChildId === child.id) as approval (approval.id)}
                <div class="mt-2 border-t border-line pt-2" id={`approval-${approval.id}`}><p class="text-muted mb-1">{child.role} · {child.assignment.prompt}</p><ApprovalCard {approval} chatContext /></div>
              {/each}
            </article>
          {/each}
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
{/if}
{#if state.error}<div class="mx-3 mb-2 text-xs text-rust" role="alert">{state.error}<button class="btn-ghost btn-sm" onclick={() => state.hydrate()}>Reload agents</button></div>{/if}
