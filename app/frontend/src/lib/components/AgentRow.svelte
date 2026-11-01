<script lang="ts">
  import type { RuntimeAgent } from '$lib/types';
  import { agentActive, agentStateLabel, type AgentChild } from '$lib/delegation';
  import { agentFellBack, capabilityChips } from '$lib/agentTree';
  let { agent, depth, selected, pending, onselect, oncancel }: { agent: RuntimeAgent; depth: number; selected: boolean; pending: boolean; onselect: () => void; oncancel: () => void } = $props();
  const fallback = $derived(agentFellBack(agent));
  const chips = $derived(capabilityChips(agent.capabilities));
  const label = $derived(agentStateLabel({ state: agent.state, result: agent.result } as AgentChild));
</script>

<li class="flex flex-wrap items-center gap-x-2 gap-y-1 rounded-md border border-line px-2 py-1.5 min-w-0 {selected ? 'bg-raised' : ''}" style={`margin-left:${Math.min(depth, 3) * 14}px`} data-agent-row={agent.id} data-depth={depth}>
  <button class="font-medium text-left truncate max-w-[40%] hover:underline" aria-pressed={selected} onclick={onselect}>{agent.name}</button>
  {#each chips as c (c.label)}<span class="pill {c.prominent ? 'bg-rust-soft text-rust' : 'bg-raised text-muted'}" data-prominent={c.prominent}>{c.label}</span>{/each}
  <span class="text-muted break-all">{agent.modelRequested || 'Model not recorded'}</span>
  {#if fallback}<span class="pill bg-clay-soft/40 text-amber-warm" data-fallback>fell back to {fallback}</span>{/if}
  <span class:text-rust={['failed', 'interrupted', 'cancelled'].includes(agent.state)} class="ml-auto">{label}</span>
  {#if agentActive(agent.state)}<button class="btn-ghost btn-sm" aria-label={`Cancel ${agent.name}`} disabled={pending} onclick={oncancel}>Cancel</button>{/if}
</li>
