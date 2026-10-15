<script lang="ts">
  import type { TurnDetailsResult } from '$lib/turnDetails';
  let { details }: { details: TurnDetailsResult } = $props();
  const diagnostics = $derived(details.diagnostics);
  const format = (n?: number) => n === undefined ? 'Not recorded for this turn' : n.toLocaleString();
  const duration = $derived(details.turn.finishedAt ? Math.max(0, Date.parse(details.turn.finishedAt)-Date.parse(details.turn.startedAt)) : undefined);
</script>
<div class="p-4 space-y-5 text-xs min-w-0">
  <section class="space-y-1">
    <h2 class="text-sm font-semibold">Turn overview</h2>
    <p>Status: {details.turn.status} · Model: {details.turn.resolved.provider || '—'} / {details.turn.resolved.model || '—'}</p>
    <p>Duration: {duration === undefined ? 'Still running' : `${duration.toLocaleString()} ms`}</p>
    {#if diagnostics}<p>Optimization policy at start: {Object.entries(diagnostics.policy).filter(([key]) => key !== 'source').map(([key,value]) => `${key}: ${value}`).join(' · ') || 'Not recorded'} ({diagnostics.policy.source || 'unknown'})</p>{:else}<p>Not recorded for this turn</p>{/if}
  </section>
  <section class="space-y-1">
    <h3 class="font-medium">Tokens and cost</h3>
    <p>Input {format(details.turn.usage.inputTokens)} · Cached {format(details.turn.usage.cachedInputTokens)} · Output {format(details.turn.usage.outputTokens)} · Reasoning {format(details.turn.usage.reasoningTokens)} · Requests {format(details.turn.usage.requests)}</p>
    <p>{#if diagnostics && diagnostics.requests.length}
      {#each diagnostics.requests as request, i (request.ordinal)}{#if i > 0} · {/if}{#if request.costUsd !== undefined}${request.costUsd.toFixed(6)}{request.costState === 'partial' ? ' (partial)' : ''}{request.measurementOrigin === 'estimated' ? ' (estimated)' : ''}{:else}Cost unavailable{/if}{/each}
    {:else}Cost unavailable{/if}</p>
  </section>
  {#if diagnostics}
    <section class="space-y-2">
      <h3 class="font-medium">Context and tools</h3>
      {#each diagnostics.requests as request (request.ordinal)}
        <article class="rounded-md bg-raised p-2 space-y-1 min-w-0">
          <p class="font-medium">{request.phase} · {request.provider}/{request.model}</p>
          {#if request.stopReason}<p>Stop reason: {request.stopReason}</p>{/if}
          {#if request.fallback}<p>Fallback: {request.fallback}</p>{/if}
          {#if request.estimates}<p>Estimated context: {Object.entries(request.estimates).map(([key,value]) => `${key} ${value}`).join(' · ')}</p>{/if}
          <p>Selected sources: {request.selectedSources?.length ? request.selectedSources.map(x => `${x.kind}:${x.id}`).join(', ') : 'Not recorded for this turn'}</p>
          <p>Submitted sources: {request.submittedSources?.length ? request.submittedSources.map(x => `${x.kind}:${x.id}`).join(', ') : 'Not recorded for this turn'}</p>
          <p>Tools loaded: {request.toolNames?.length ? request.toolNames.join(', ') : 'Not recorded for this turn'}</p>
          {#if request.toolCalls}<p>Calls: {Object.entries(request.toolCalls).map(([name,count]) => `${name} × ${count}`).join(', ')}</p>{/if}
        </article>
      {/each}
    </section>
    <details><summary class="cursor-pointer font-medium">Developer details</summary><pre class="mt-2 whitespace-pre-wrap break-words rounded bg-raised p-2">{JSON.stringify(diagnostics, null, 2)}</pre></details>
    {#if !diagnostics.complete}<p class="text-muted">Some turn diagnostics were unavailable.</p>{/if}
  {:else}
    <p class="text-muted">Not recorded for this turn</p>
  {/if}
</div>
