<script lang="ts">
  import { Brain, Inbox, FilePlus2, FilePen, FileX2, Copy, Check } from '@lucide/svelte';
  import type { Item, FileChangeData } from '$lib/types';
  import { app } from '$lib/stores/app.svelte';
  import { renderMarkdown } from '$lib/markdown';
  import { type ThreadView } from '$lib/stores/chat.svelte';
  import { inspector } from '$lib/stores/inspector.svelte';
  import ToolCall from './ToolCall.svelte';
  import ChatError from './ChatError.svelte';

  let { item, highlight = false, view }: { item: Item; highlight?: boolean; view?: ThreadView } = $props();
  let showThinking = $state(false);
  let showSummary = $state(false);
  let copied = $state(false);

  const html = $derived(item.kind === 'agentMessage' ? renderMarkdown(item.text || '') : '');
  const change = $derived(item.kind === 'fileChange' ? (item.data as FileChangeData) : null);

  async function copyMessage() {
    try {
      await navigator.clipboard.writeText(item.text || '');
      copied = true;
      setTimeout(() => (copied = false), 1400);
    } catch {
      app.toast('error', 'Could not copy this message to the clipboard.');
    }
  }
</script>

<div
  id={`item-${item.id}`}
  class="group/item {highlight ? 'rounded-lg ring-1 ring-clay/60 ring-offset-4 ring-offset-paper' : ''}"
>
  {#if item.kind === 'userMessage'}
    <div class="flex justify-end gap-1 items-start">
      <div class="max-w-[80%] bg-raised rounded-2xl rounded-br-md px-3.5 py-2 text-sm whitespace-pre-wrap selectable">{item.text}</div>
      <button class="mt-1 p-1 text-faint hover:text-ink opacity-70 sm:opacity-0 group-hover/item:opacity-100 focus:opacity-100 transition-opacity" aria-label={copied ? 'Copied message' : 'Copy message'} title={copied ? 'Copied' : 'Copy message'} onclick={copyMessage}>
        {#if copied}<Check class="w-3.5 h-3.5 text-sage" />{:else}<Copy class="w-3.5 h-3.5" />{/if}
      </button>
    </div>
  {:else if item.kind === 'agentMessage'}
    <div class="relative">
      <div class="prose-chat">
        {@html html}
        {#if item.status === 'inProgress'}<span class="inline-block w-1.5 h-4 bg-clay animate-pulse align-text-bottom"></span>{/if}
      </div>
      {#if item.text?.trim()}
        <div class="flex justify-end mt-1">
          <button class="p-1 text-faint hover:text-ink opacity-70 sm:opacity-0 group-hover/item:opacity-100 focus:opacity-100 transition-opacity" aria-label={copied ? 'Copied message' : 'Copy message'} title={copied ? 'Copied' : 'Copy message'} onclick={copyMessage}>
            {#if copied}<Check class="w-3.5 h-3.5 text-sage" />{:else}<Copy class="w-3.5 h-3.5" />{/if}
          </button>
        </div>
      {/if}
    </div>
  {:else if item.kind === 'reasoning'}
    <div class="text-xs">
      <button class="flex items-center gap-1.5 text-muted hover:text-ink" onclick={() => (showThinking = !showThinking)}>
        <Brain class="w-3.5 h-3.5 {item.status === 'inProgress' ? 'animate-pulse text-clay' : ''}" />
        {item.status === 'inProgress' ? 'Thinking…' : 'Thought'}
        <span class="text-faint">{showThinking ? 'hide' : 'show'}</span>
      </button>
      {#if showThinking && item.text}
        <div class="mt-1.5 pl-5 border-l border-line text-muted whitespace-pre-wrap selectable">{item.text}</div>
      {/if}
    </div>
  {:else if item.kind === 'contextCompaction'}
    <section class="rounded-lg border border-line bg-raised/50 px-3 py-2 text-xs">
      <button class="flex w-full items-center gap-2 text-left text-muted hover:text-ink" aria-expanded={showSummary} onclick={() => (showSummary = !showSummary)}>
        <span class="font-medium text-ink-soft">Chat context compacted</span>
        <span class="text-faint">Earlier messages remain in the transcript</span>
        <span class="ml-auto shrink-0">{showSummary ? 'Hide summary' : 'Show summary'}</span>
      </button>
      {#if showSummary}<p class="mt-2 whitespace-pre-wrap selectable text-ink-soft">{item.text}</p>{/if}
    </section>
  {:else if item.kind === 'toolCall' && item.tool}
    <ToolCall {item} />
  {:else if change}
    <div class="rounded-lg border border-line bg-surface/60 text-xs overflow-hidden">
      <div class="flex items-center gap-2 px-3 py-1.5 min-w-0">
        {#if change.action === 'created'}
          <FilePlus2 class="w-3.5 h-3.5 text-sage" />
        {:else if change.action === 'deleted'}
          <FileX2 class="w-3.5 h-3.5 text-rust" />
        {:else}
          <FilePen class="w-3.5 h-3.5 text-clay" />
        {/if}
        <button class="min-w-0 flex-1 text-left font-mono text-ink hover:underline truncate" title="Open file in inspector" onclick={() => inspector.openFile(change.path, view?.id ?? undefined, view?.thread?.projectId)}>
          {change.path}
        </button>
        <span class="text-sage shrink-0">+{change.additions}</span>
        <span class="text-rust shrink-0">−{change.deletions}</span>
        <button class="text-faint hover:text-ink shrink-0" onclick={() => inspector.openDiff(change)}>Diff</button>
      </div>
    </div>
  {:else if item.kind === 'inboundEvent'}
    <div class="flex gap-2 text-xs text-muted border border-line rounded-lg px-3 py-2 selectable">
      <Inbox class="w-3.5 h-3.5 mt-0.5 shrink-0" /><span class="whitespace-pre-wrap">{item.text}</span>
    </div>
  {:else if item.kind === 'error'}
    <ChatError text={item.text || ''} />
  {:else if item.text}
    <div class="text-xs text-muted whitespace-pre-wrap">{item.text}</div>
  {/if}
</div>
