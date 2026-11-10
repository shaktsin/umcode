<script lang="ts">
  import { untrack } from 'svelte';
  import { Users } from '@lucide/svelte';
  import type { Item, PlanCard } from '$lib/types';
  import { app } from '$lib/stores/app.svelte';
  import type { ThreadView } from '$lib/stores/chat.svelte';
  import { capabilityChips } from '$lib/agentTree';
  import { approveParams, canApprove, declineParams, draftOf, kept, modelUsable, noEdits, parsePlanResult, planIdForItem, reviseParams, type PlanEdits } from '$lib/agentPlanCard';
  import { errMsg } from '$lib/format';

  let { item, view }: { item: Item; view: Pick<ThreadView, 'cards' | 'delegation'> } = $props();
  const planId = $derived(planIdForItem(item, app.approvals));
  const result = $derived(parsePlanResult(item.tool?.output));
  const entry = $derived(planId ? view.cards.plan(planId) : undefined);
  const card = $derived<PlanCard | undefined>(entry?.card);
  // The single gate for every control: a proposed plan whose approval the engine reports as pending.
  const actionable = $derived(!!planId && !!card && view.cards.planActionable(planId));
  let edits = $state<PlanEdits>(noEdits());
  let feedback = $state('');
  let busy = $state(false);
  let error = $state('');
  const draft = $derived(card ? draftOf(card, edits) : undefined);
  const task = $derived(entry && view.delegation.tasks.find(t => t.task.id === entry.plan.taskId));
  const taskActive = $derived(!!task && view.delegation.active?.task.id === task.task.id);

  $effect(() => { const id = planId; if (id) untrack(() => void view.cards.refreshPlan(id)); });

  const status = $derived.by(() => {
    if (entry?.plan.state === 'superseded') return 'Superseded';
    if (entry?.approvalStatus === 'expired') return 'Expired';
    if (entry?.plan.state === 'approved' || entry?.approvalStatus === 'approved') return 'Approved';
    if (entry?.approvalStatus === 'denied' || result?.state === 'declined') return 'Declined';
    if (result?.state === 'revise') return 'Revision requested';
    return '';
  });

  async function send(p: ReturnType<typeof declineParams> | null) {
    if (!p || busy || !actionable) return;
    busy = true; error = '';
    try { await view.cards.respondPlan(p); } catch (e) { error = errMsg(e); } finally { busy = false; }
  }
  const setModel = (name: string, v: string) => (edits = { ...edits, models: { ...edits.models, [name]: v } });
  const toggleDrop = (name: string) => (edits = { ...edits, dropped: edits.dropped.includes(name) ? edits.dropped.filter(n => n !== name) : [...edits.dropped, name] });
  const setLimit = (k: 'maxConcurrent' | 'maxDepth' | 'maxTotal' | 'sliceShare', v: number) => (edits = { ...edits, limits: { ...edits.limits, [k]: v } });
</script>

<section class="rounded-lg border {actionable ? 'border-clay/40' : 'border-line'} bg-surface/60 px-3 py-2 text-xs" aria-label="Agent plan">
  <div class="flex items-center gap-2">
    <Users class="w-3.5 h-3.5 text-clay" />
    <span class="font-medium text-ink">Agent plan</span>
    {#if status && !actionable}<span class="pill bg-raised text-muted ml-auto">{status}</span>{/if}
  </div>

  {#if result?.state === 'revise' && result.feedback}<p class="mt-1.5 text-ink-soft whitespace-pre-wrap selectable">Feedback: {result.feedback}</p>{/if}

  {#if card && draft}
    {#if card.notices?.length}
      <ul class="mt-2 space-y-1">
        {#each card.notices as n}<li class="rounded-md bg-clay-soft/30 px-2 py-1 text-amber-warm selectable">{n}</li>{/each}
      </ul>
    {/if}
    <ul class="mt-2 space-y-2">
      {#each card.agents as a (a.name)}
        {@const dropped = draft.dropped.includes(a.name)}
        <li class="rounded-md border border-line bg-paper px-2.5 py-2 {dropped ? 'opacity-50' : ''}" data-agent={a.name}>
          <div class="flex flex-wrap items-center gap-2">
            <span class="font-medium text-ink {dropped ? 'line-through' : ''}">{a.name}</span>
            {#each capabilityChips(a.granted) as c}
              <span class="pill {c.prominent ? 'bg-rust-soft text-rust' : 'bg-raised text-muted'}" data-prominent={c.prominent}>{c.label}</span>
            {/each}
            {#if actionable}
              <button class="btn-ghost btn-sm ml-auto" aria-pressed={dropped} onclick={() => toggleDrop(a.name)}>{dropped ? 'Keep' : 'Drop'}</button>
            {:else if dropped}<span class="ml-auto text-faint">Dropped</span>{/if}
          </div>
          {#if a.purpose}<p class="mt-1 text-ink-soft selectable">{a.purpose}</p>{/if}
          {#if a.summary}<p class="mt-0.5 text-muted selectable">{a.summary}</p>{/if}
          {#each a.rejected ?? [] as r}<p class="mt-0.5 text-amber-warm selectable">Not granted: {r.field}: {r.reason}</p>{/each}
          {#if actionable && !dropped}
            <label class="mt-1.5 flex items-center gap-2 text-muted">Model
              <select class="select" value={draft.models[a.name]} onchange={(e) => setModel(a.name, e.currentTarget.value)}>
                <option value="">Chat model (default)</option>
                {#each card.models ?? [] as m}
                  <option value={m.name} disabled={!modelUsable(m)}>{m.name}{modelUsable(m) ? '' : ` (unavailable${m.why ? `: ${m.why}` : ''})`}</option>
                {/each}
              </select>
            </label>
          {:else if draft.models[a.name] || a.model}<p class="mt-0.5 text-muted">Model: {draft.models[a.name] || a.model}</p>{/if}
        </li>
      {/each}
    </ul>

    {#if actionable}
      <fieldset class="mt-2 grid grid-cols-2 gap-2 sm:grid-cols-4">
        <legend class="sr-only">Limits</legend>
        <label class="label">Max concurrent (up to {card.limits.maxConcurrent})
          <input class="input" type="number" min="1" max={card.limits.maxConcurrent} value={draft.limits.maxConcurrent} onchange={(e) => setLimit('maxConcurrent', e.currentTarget.valueAsNumber)} /></label>
        <label class="label">Max depth (up to {card.limits.maxDepth})
          <input class="input" type="number" min="1" max={card.limits.maxDepth} value={draft.limits.maxDepth} onchange={(e) => setLimit('maxDepth', e.currentTarget.valueAsNumber)} /></label>
        <label class="label">Max agents (up to {card.limits.maxTotal})
          <input class="input" type="number" min="1" max={card.limits.maxTotal} value={draft.limits.maxTotal} onchange={(e) => setLimit('maxTotal', e.currentTarget.valueAsNumber)} /></label>
        <label class="label">Budget share (up to {card.limits.sliceShare})
          <input class="input" type="number" min="0.01" max={card.limits.sliceShare} step="0.01" value={draft.limits.sliceShare} onchange={(e) => setLimit('sliceShare', e.currentTarget.valueAsNumber)} /></label>
        {#if card.limits.network}
          <label class="flex items-center gap-2 text-muted col-span-2"><input type="checkbox" checked={draft.limits.network} onchange={(e) => (edits = { ...edits, limits: { ...edits.limits, network: e.currentTarget.checked } })} /> Allow network for agents</label>
        {/if}
      </fieldset>
      <label class="label mt-2">Feedback (required to revise)
        <textarea class="input" rows="2" bind:value={feedback} placeholder="What should change?"></textarea></label>
      <div class="mt-2 flex flex-wrap justify-end gap-2">
        <button class="btn-ghost btn-sm" disabled={busy} onclick={() => send(declineParams(planId!))}>Decline</button>
        <button class="btn-outline btn-sm" disabled={busy || !feedback.trim()} onclick={() => send(reviseParams(planId!, feedback))}>Revise</button>
        <button class="btn-primary btn-sm" disabled={busy || !canApprove(card, draft)} onclick={() => send(approveParams(card, draft))}>Approve {kept(card, draft).length} agent{kept(card, draft).length === 1 ? '' : 's'}</button>
      </div>
    {/if}
  {:else if planId && !result?.state}
    <p class="mt-1.5 text-muted">Loading plan…</p>
  {/if}

  {#if taskActive && task}
    <div class="mt-2 flex items-center gap-2 border-t border-line pt-2 text-muted">
      <span>This task stays active until it is cancelled.</span>
      <button class="btn-ghost btn-sm ml-auto" disabled={view.delegation.pending} onclick={() => view.delegation.cancel(task.task.id)}>Cancel task</button>
    </div>
  {/if}
  {#if error || view.cards.error}<p class="mt-1.5 text-rust" role="alert">{error || view.cards.error}</p>{/if}
</section>
