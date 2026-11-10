<script lang="ts">
  import { untrack } from 'svelte';
  import { Users } from '@lucide/svelte';
  import type { Item } from '$lib/types';
  import type { ThreadView } from '$lib/stores/chat.svelte';
  import { parseSuggestionResult } from '$lib/agentPlanCard';
  import { errMsg } from '$lib/format';

  let { item, view }: { item: Item; view: Pick<ThreadView, 'cards' | 'answerSuggestion' | 'queuedFollowUp'> } = $props();
  const suggestionId = $derived(parseSuggestionResult(item.tool?.output)?.suggestionId ?? '');
  const reason = $derived((item.tool?.args as { reason?: string } | null)?.reason ?? '');
  const sug = $derived(suggestionId ? view.cards.suggestion(suggestionId) : undefined);
  // Only a suggestion the engine just confirmed as pending offers buttons; the tool result text never does.
  const actionable = $derived(suggestionId !== '' && view.cards.suggestionActionable(suggestionId));
  let busy = $state(false);
  let error = $state('');

  $effect(() => { const id = suggestionId; if (id) untrack(() => void view.cards.refreshSuggestion(id)); });

  async function answer(decision: 'enable' | 'decline') {
    if (busy || !actionable) return;
    busy = true; error = '';
    try { await view.answerSuggestion(suggestionId, decision); } catch (e) { error = errMsg(e); } finally { busy = false; }
  }
  const stateLabel = $derived(({ pending: 'Waiting for your answer', enabled: 'Agents enabled', declined: 'Continued without agents', expired: 'Expired' } as Record<string, string>)[sug?.state ?? ''] ?? '');
</script>

<section class="rounded-lg border {actionable ? 'border-clay/40 bg-clay-soft/20' : 'border-line bg-surface/60'} px-3 py-2 text-xs" aria-label="Agent suggestion">
  <div class="flex items-center gap-2">
    <Users class="w-3.5 h-3.5 text-clay" aria-hidden="true" />
    <span class="font-medium text-ink">Suggestion: use agents for this message</span>
    {#if stateLabel && !actionable}<span class="pill bg-raised text-muted ml-auto">{stateLabel}</span>{/if}
  </div>
  {#if reason}<p class="mt-1.5 text-ink-soft whitespace-pre-wrap selectable">{reason}</p>{/if}
  {#if actionable}
    <div class="mt-2 flex flex-wrap justify-end gap-2">
      <button class="btn-outline btn-sm" disabled={busy} onclick={() => answer('decline')}>Continue without agents</button>
      <button class="btn-primary btn-sm" disabled={busy} onclick={() => answer('enable')}>Enable agents for this message</button>
    </div>
  {/if}
  {#if view.queuedFollowUp}<p class="mt-1.5 text-muted" role="status">Waiting for the current turn to finish, then agents continue.</p>{/if}
  {#if error || view.cards.error}<p class="mt-1.5 text-rust" role="alert">{error || view.cards.error}</p>{/if}
</section>
