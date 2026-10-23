<script lang="ts">
  import { ShieldCheck } from '@lucide/svelte';
  import { app } from '$lib/stores/app.svelte';
  import type { ThreadView } from '$lib/stores/chat.svelte';
  import { APPROVAL_MODES, approvalModeLabel } from '$lib/approvalModes';
  import PillMenu from './PillMenu.svelte';

  let { view }: { view: ThreadView } = $props();
  const current = $derived(view.thread?.approvalMode || 'normal');
  const items = $derived(APPROVAL_MODES.map((m) => ({ ...m, checked: m.id === current })));

  async function select(mode: string) {
    if (!view.id) return;
    await app.try('thread/setApprovalMode', { threadId: view.id, mode: mode as 'normal' | 'auto_workspace' | 'auto_all' });
  }
</script>

{#if view.thread}
  <PillMenu ariaLabel="Approval mode" title="Approval and auto-approve behavior for this chat" label={approvalModeLabel(view.thread.approvalMode)} {items} onselect={select}>
    {#snippet icon()}<ShieldCheck class="w-3.5 h-3.5 shrink-0" />{/snippet}
  </PillMenu>
{/if}
