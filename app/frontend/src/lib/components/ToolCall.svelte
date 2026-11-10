<script lang="ts">
  import { ChevronRight, Check, X, Loader2, Ban, Maximize2 } from '@lucide/svelte';
  import type { Item } from '$lib/types';
  import { app } from '$lib/stores/app.svelte';
  import { inspector } from '$lib/stores/inspector.svelte';
  import { prettyJSON } from '$lib/format';
  import ApprovalCard from './ApprovalCard.svelte';

  let { item, projectId }: { item: Item; projectId?: string } = $props();
  let open = $state(false);

  const tool = $derived(item.tool!);
  const approval = $derived(app.approvals.find((a) => a.itemId === item.id));
  const args = $derived(prettyJSON(tool.args));
  const longOutput = $derived((tool.output ?? '').length > 1200);
  const summary = $derived.by(() => {
    const a = tool.args as Record<string, unknown> | null;
    if (!a || typeof a !== 'object') return '';
    const v = a.command ?? a.path ?? a.skill_name ?? a.query ?? a.name ?? a.url;
    if (typeof v === 'string') return v.length > 80 ? v.slice(0, 80) + '…' : v;
    if (a.skill && a.script) return `${a.skill}/${a.script}`;
    return '';
  });
</script>

<div class="rounded-lg border {approval ? 'border-clay/40' : 'border-line'} bg-surface/60 text-xs overflow-hidden">
  <button class="w-full flex items-center gap-2 px-3 py-1.5 text-left hover:bg-raised/50" onclick={() => (open = !open)}>
    <ChevronRight class="w-3.5 h-3.5 text-faint transition-transform {open ? 'rotate-90' : ''}" />
    {#if item.status === 'inProgress'}
      <Loader2 class="w-3.5 h-3.5 text-clay animate-spin" />
    {:else if item.status === 'completed'}
      <Check class="w-3.5 h-3.5 text-sage" />
    {:else if item.status === 'denied'}
      <Ban class="w-3.5 h-3.5 text-amber-warm" />
    {:else}
      <X class="w-3.5 h-3.5 text-rust" />
    {/if}
    <code class="font-mono text-ink">{tool.name}</code>
    {#if summary}<span class="text-muted truncate font-mono">{summary}</span>{/if}
    {#if tool.risk && tool.risk !== 'green'}<span class="ml-auto risk-{tool.risk}">{tool.risk}</span>{/if}
  </button>

  {#if approval}
    <ApprovalCard {approval} compact />
  {/if}

  {#if open}
    <div class="border-t border-line divide-y divide-line">
      {#if args && args !== '{}'}
        <div class="px-3 py-2">
          <div class="text-[10px] font-semibold uppercase tracking-wider text-muted mb-1">Arguments</div>
          <pre class="font-mono text-[11px] text-ink-soft whitespace-pre-wrap break-all max-h-64 overflow-auto selectable">{args}</pre>
        </div>
      {/if}
      {#if tool.output}
        <div class="px-3 py-2">
          <div class="flex items-center text-[10px] font-semibold uppercase tracking-wider text-muted mb-1">
            Output
            {#if longOutput}
              <button
                class="ml-auto normal-case font-normal text-faint hover:text-ink flex items-center gap-1"
                onclick={() => inspector.openToolOutput(tool.name, tool.output!, item.id, item.threadId, projectId)}
              >
                <Maximize2 class="w-3 h-3" />open in inspector
              </button>
            {/if}
          </div>
          <pre class="font-mono text-[11px] text-muted whitespace-pre-wrap break-all max-h-80 overflow-auto selectable">{tool.output}</pre>
        </div>
      {/if}
      {#if tool.error}
        <div class="px-3 py-2 text-rust selectable">{tool.error}</div>
      {/if}
    </div>
  {:else if tool.error && item.status !== 'inProgress'}
    <div class="px-3 pb-1.5 text-[11px] {item.status === 'denied' ? 'text-amber-warm' : 'text-rust'} truncate">{tool.error}</div>
  {/if}
</div>
