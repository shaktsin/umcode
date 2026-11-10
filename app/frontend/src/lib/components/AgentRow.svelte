<script lang="ts">
  import { CircleAlert, CircleCheck, CircleDashed, CircleSlash, CircleStop, ChevronDown, ChevronUp, LoaderCircle, ShieldAlert } from '@lucide/svelte';
  import { agentActive } from '$lib/delegation';
  import { agentFellBack } from '$lib/agentTree';
  import { agentTone, attemptLabel, permissionLine, reasonOf, type AgentGroup, type AgentToneName } from '$lib/agentGroups';
  let { group, selected, pending, onselect, oncancel }: { group: AgentGroup; selected: boolean; pending: boolean; onselect: () => void; oncancel: () => void } = $props();
  let expanded = $state(false);
  const agent = $derived(group.latest);
  const status = $derived(agentTone(agent));
  const fallback = $derived(agentFellBack(agent));
  const reason = $derived(reasonOf(agent));
  const many = $derived(group.attempts.length > 1);
  // Red is reserved for real failures; a budget stop is amber, waiting is quiet grey.
  const text: Record<AgentToneName, string> = { running: 'text-accent-strong', attention: 'text-amber-warm', failed: 'text-rust', stopped: 'text-amber-warm', waiting: 'text-[#566378]', done: 'text-sage', neutral: 'text-[#566378]' };
  const pillTone: Record<AgentToneName, string> = { running: 'bg-raised text-accent-strong', attention: 'bg-[#f6ecd9] text-[#6b430c]', failed: 'bg-rust-soft text-rust', stopped: 'bg-[#f6ecd9] text-[#6b430c]', waiting: 'bg-raised text-ink-soft', done: 'bg-raised text-sage', neutral: 'bg-raised text-ink-soft' };
  const reasonText: Record<AgentToneName, string> = { ...text, stopped: 'text-[#6b430c]', failed: 'text-ink-soft' };
</script>

<li class="px-3.5 py-3 min-w-0 {selected ? 'bg-raised' : ''}" style={`padding-left:${14 + Math.min(group.depth, 3) * 14}px`} data-agent-row={agent.id} data-depth={group.depth} data-tone={status.tone}>
  <div class="flex items-start gap-2.5">
    <span class="mt-0.5 flex size-4 flex-none items-center justify-center {text[status.tone]}" aria-hidden="true">
      {#if status.tone === 'running'}<LoaderCircle size={16} class="animate-spin motion-reduce:animate-none" />
      {:else if status.tone === 'attention'}<ShieldAlert size={16} />
      {:else if status.tone === 'failed'}<CircleAlert size={16} />
      {:else if status.tone === 'stopped'}<CircleStop size={16} />
      {:else if status.tone === 'waiting'}<CircleDashed size={16} />
      {:else if status.tone === 'done'}<CircleCheck size={16} />
      {:else}<CircleSlash size={16} />{/if}
    </span>
    <div class="min-w-0 flex-1">
      <div class="flex flex-wrap items-center gap-x-2 gap-y-1">
        <button class="max-w-full truncate text-left text-[13px] font-semibold leading-5 hover:underline" aria-pressed={selected} onclick={onselect}>{agent.name}</button>
        <span class="rounded-full px-2 py-px font-medium {pillTone[status.tone]}">{status.label}</span>
        {#if fallback}<span class="rounded-full bg-clay-soft/40 px-2 py-px text-amber-warm" data-fallback>fell back to {fallback}</span>{/if}
      </div>
      <p class="mt-0.5 break-words text-[#566378]">{permissionLine(agent)}</p>
      {#if reason && (!many || !expanded)}<p class="mt-0.5 break-words {reasonText[status.tone]}">{reason}</p>{/if}
    </div>
    {#if agentActive(agent.state)}<button class="btn-ghost btn-sm" aria-label={`Cancel ${agent.name}`} disabled={pending} onclick={oncancel}>Cancel</button>{/if}
    {#if many}
      <button class="flex h-7 items-center gap-1 rounded-lg px-2 text-ink-soft hover:bg-raised" aria-expanded={expanded} aria-label={`${group.attempts.length} attempts`} onclick={() => (expanded = !expanded)}>
        {group.attempts.length} attempts{#if expanded}<ChevronUp size={14} aria-hidden="true" />{:else}<ChevronDown size={14} aria-hidden="true" />{/if}
      </button>
    {/if}
  </div>
  {#if many}
    <ul class="mt-2.5 ml-[26px] flex flex-col gap-2 border-l-2 border-line pl-3.5" aria-label="Attempts" hidden={!expanded}>
      {#each group.attempts as a (a.id)}
        <li class="flex items-baseline gap-2">
          <span class="w-16 flex-none text-[#566378]">Attempt {a.attempt}</span>
          <span class="font-medium {text[agentTone(a.agent).tone]}">{attemptLabel(a.agent)}</span>
          {#if reasonOf(a.agent)}<span class="min-w-0 break-words text-ink-soft">{reasonOf(a.agent)}</span>{/if}
        </li>
      {/each}
    </ul>
  {/if}
</li>
