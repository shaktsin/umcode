<script lang="ts">
  import { AlertTriangle } from '@lucide/svelte';
  import { formatChatError } from '$lib/chatError';
  let { text }: { text: string } = $props();
  const error = $derived(formatChatError(text));
</script>

<div role="alert" class="flex min-w-0 gap-2 text-sm text-rust bg-rust-soft border border-rust/30 rounded-lg px-3 py-2 selectable">
  <AlertTriangle class="w-4 h-4 mt-0.5 shrink-0" />
  <div class="min-w-0 flex-1 [overflow-wrap:anywhere]">
    <p class="whitespace-pre-wrap">{error.message}</p>
    {#if error.resetAt}
      <p class="mt-1 text-xs">Usage resets <time datetime={new Date(error.resetAt).toISOString()}>{new Date(error.resetAt).toLocaleString(undefined, { dateStyle: 'medium', timeStyle: 'short' })}</time>.</p>
    {/if}
    <details class="mt-2 text-xs">
      <summary class="cursor-pointer">Technical details</summary>
      <pre class="mt-1 whitespace-pre-wrap font-mono">{text}</pre>
    </details>
  </div>
</div>
