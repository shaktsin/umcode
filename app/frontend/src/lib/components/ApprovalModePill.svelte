<script lang="ts">
  import { ShieldCheck } from '@lucide/svelte';
  import { app } from '$lib/stores/app.svelte';
  import type { ThreadView } from '$lib/stores/chat.svelte';
  import { APPROVAL_MODES, approvalModeLabel, rememberApprovalMode } from '$lib/approvalModes';
  import PillMenu from './PillMenu.svelte';

  let { view }: { view: ThreadView } = $props();
  // Before the first message there is no thread yet: the choice is held on the
  // view and sent with thread/start, so it applies from the very first action.
  const current = $derived((view.thread ? view.thread.approvalMode : view.approvalMode) || 'normal');
  const items = $derived(APPROVAL_MODES.map((m) => ({ ...m, checked: m.id === current })));

  async function select(mode: string) {
    const m = mode as 'normal' | 'auto_workspace' | 'auto_all';
    rememberApprovalMode(m); // the latest choice becomes the default for new chats
    if (!view.id) { view.approvalMode = m; return; }
    await app.try('thread/setApprovalMode', { threadId: view.id, mode: m });
  }
</script>

<PillMenu ariaLabel="Approval mode" title="Approval and auto-approve behavior for this chat" label={approvalModeLabel(current)} {items} onselect={select}>
  {#snippet icon()}<ShieldCheck class="w-3.5 h-3.5 shrink-0" />{/snippet}
</PillMenu>
