<script lang="ts">
  import type { RuntimeAgent } from '$lib/types';
  import { agentStateLabel } from '$lib/delegation';
  import { agentFellBack, capabilityChips } from '$lib/agentTree';
  import { inspector } from '$lib/stores/inspector.svelte';
  import { fmtUsd } from '$lib/format';
  let { agent, threadId, projectId }: { agent: RuntimeAgent; threadId: string; projectId?: string } = $props();
  const fallback = $derived(agentFellBack(agent));
  const chips = $derived(capabilityChips(agent.capabilities));
  const diffs = $derived(agent.revision ? agent.result.changedPaths ?? [] : []);
</script>

<div class="p-4 space-y-4 text-xs break-words selectable" data-agent-inspector>
  <header class="space-y-1">
    <h3 class="text-sm font-medium">{agent.name}</h3>
    <p class="text-muted">{agent.purpose} · {agentStateLabel(agent)}</p>
  </header>
  <section class="space-y-1"><h4 class="font-medium">Assignment</h4>
    <p class="whitespace-pre-wrap">{agent.assignment.prompt}</p>
    {#if agent.assignment.acceptanceChecks?.length}<ul class="list-disc pl-4 text-muted">{#each agent.assignment.acceptanceChecks as check}<li>{check}</li>{/each}</ul>{/if}
  </section>
  <section class="space-y-1"><h4 class="font-medium">Capabilities granted</h4>
    <div class="flex flex-wrap gap-1">{#each chips as c (c.label)}<span class="pill {c.prominent ? 'bg-rust-soft text-rust' : 'bg-raised text-muted'}" data-prominent={c.prominent}>{c.label}</span>{:else}<span class="text-muted">No tool access</span>{/each}</div>
    {#if agent.rejected?.length}
      <h4 class="font-medium pt-1">Rejected</h4>
      <ul class="list-disc pl-4 text-rust">{#each agent.rejected as r}<li><span class="font-medium">{r.field}</span> · {r.reason}</li>{/each}</ul>
    {/if}
  </section>
  <section class="space-y-1"><h4 class="font-medium">Model</h4>
    <p>Requested <code>{agent.modelRequested || 'not recorded'}</code></p>
    <p>Actual <code>{agent.modelActual || 'not yet run'}</code>{#if fallback} <span class="pill bg-clay-soft/40 text-amber-warm">fell back to {fallback}</span>{/if}</p>
    {#if agent.routeTrail?.length}<p class="text-muted">Route · {agent.routeTrail.join(' → ')}</p>{/if}
  </section>
  <section class="space-y-1"><h4 class="font-medium">Usage</h4>
    <p>Budget share {Math.round(agent.budgetShare * 100)}% · {agent.usage.inputTokens + agent.usage.outputTokens} tokens · {fmtUsd(agent.usage.costUsd)}</p>
  </section>
  {#if agent.result.summary || agent.result.blocker}
    <section class="space-y-1"><h4 class="font-medium">Result</h4>
      {#if agent.result.summary}<p class="whitespace-pre-wrap">{agent.result.summary}</p>{/if}
      {#if agent.result.blocker}<p class="whitespace-pre-wrap text-rust" role="status">{agent.result.blocker}</p>{/if}
    </section>
  {/if}
  {#if diffs.length}
    <section class="space-y-1"><h4 class="font-medium">Changed files</h4>
      {#each diffs as path (path)}<button class="btn-ghost btn-sm max-w-full" onclick={() => inspector.openAgentFile(path, threadId, agent.taskId, agent.id, agent.revision!, projectId)}><span class="truncate font-mono">{path}</span></button>{/each}
    </section>
  {/if}
</div>
