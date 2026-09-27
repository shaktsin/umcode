<script lang="ts">
  // A fixed-size, always-on view of what Computer Use is doing in this
  // chat — the live equivalent of the tiny screenshot on an approval card,
  // shown whether or not any action here is actually waiting for approval.
  // It reads the same item stream every other part of the chat already
  // gets: computer.start/inspect/act calls publish a normal tool item on
  // every step, auto-approved or not, so this needs no extra plumbing.
  import { Monitor } from '@lucide/svelte';
  import { app } from '$lib/stores/app.svelte';
  import type { Item, ProjectArtifactContent } from '$lib/types';

  let { items, projectId, threadId }: { items: Item[]; projectId?: string; threadId?: string } = $props();

  interface ComputerArtifact {
    path: string;
    kind: string;
  }
  interface ComputerReport {
    status?: string;
    state?: { app?: { name?: string }; window?: { title?: string } };
    artifacts?: ComputerArtifact[];
    reason?: string;
    action_count?: number;
  }

  const lastItem = $derived.by(() => {
    for (let i = items.length - 1; i >= 0; i--) {
      const it = items[i];
      if (it.tool?.name?.startsWith('computer.') && it.tool.name !== 'computer.list' && it.tool.output) return it;
    }
    return undefined;
  });

  const stopped = $derived.by(() => {
    for (let i = items.length - 1; i >= 0; i--) {
      const it = items[i];
      if (it.tool?.name === 'computer.stop') return true;
      if (it.tool?.name?.startsWith('computer.') && it.tool.name !== 'computer.list' && it.tool.output) return false;
    }
    return true;
  });

  const report = $derived.by<ComputerReport | undefined>(() => {
    if (!lastItem?.tool?.output) return undefined;
    try {
      return JSON.parse(lastItem.tool.output) as ComputerReport;
    } catch {
      return undefined;
    }
  });

  const screenshotPath = $derived.by(() => {
    const arts = report?.artifacts ?? [];
    return arts.find((a) => a.kind === 'screenshot_click')?.path ?? arts.find((a) => a.kind === 'screenshot')?.path;
  });

  const actionLabel = $derived.by(() => {
    const it = lastItem;
    if (!it?.tool) return '';
    const args = (it.tool.args ?? {}) as Record<string, unknown>;
    switch (it.tool.name) {
      case 'computer.start':
        return `Opened ${(args.app_name as string) || (args.bundle_id as string) || (args.app_path as string) || 'the app'}`;
      case 'computer.inspect':
        return 'Looked at the current state';
      case 'computer.stop':
        return 'Session ended';
      case 'computer.act': {
        const action = (args.action as string) || 'acted';
        if (action === 'click' || action === 'double_click') return `${action === 'double_click' ? 'Double-clicked' : 'Clicked'} (${Math.round(Number(args.x) || 0)}, ${Math.round(Number(args.y) || 0)})`;
        if (action === 'type') return `Typed “${String(args.text ?? '').slice(0, 60)}”`;
        if (action === 'fill') return `Filled a field with “${String(args.text ?? '').slice(0, 60)}”`;
        if (action === 'key') return `Pressed ${String(args.key ?? '')}`;
        if (action === 'scroll') return `Scrolled ${Number(args.delta) > 0 ? 'down' : 'up'}`;
        return action;
      }
      default:
        return it.tool.name;
    }
  });

  const appName = $derived(report?.state?.app?.name || '');

  let screenshotUrl = $state('');
  let loadedPath = '';
  $effect(() => {
    const path = screenshotPath;
    if (!path || !projectId) {
      screenshotUrl = '';
      loadedPath = '';
      return;
    }
    if (path === loadedPath) return;
    loadedPath = path;
    void app
      .try<ProjectArtifactContent>('project/readArtifact', { projectId, threadId, path })
      .then((r) => {
        if (r && path === loadedPath) screenshotUrl = `data:${r.mimeType};base64,${r.dataB64}`;
      })
      .catch(() => {});
  });

  let collapsed = $state(false);
</script>

{#if lastItem && !stopped}
  <div class="mx-4 mt-3 rounded-xl border border-line bg-raised/60 overflow-hidden">
    <button
      class="w-full flex items-center gap-2 px-3 py-1.5 text-xs text-muted hover:text-ink-soft"
      onclick={() => (collapsed = !collapsed)}
    >
      <Monitor class="w-3.5 h-3.5 shrink-0" />
      <span class="font-medium text-ink-soft">Computer Use</span>
      {#if appName}<span class="truncate">· {appName}</span>{/if}
      <span class="ml-auto shrink-0">{collapsed ? 'Show' : 'Hide'}</span>
    </button>
    {#if !collapsed}
      <div class="px-3 pb-3">
        <div class="w-full aspect-video max-h-80 rounded-lg border border-line bg-paper overflow-hidden flex items-center justify-center">
          {#if screenshotUrl}
            <img src={screenshotUrl} alt="Current state of the app Computer Use is driving" class="w-full h-full object-contain" />
          {:else}
            <span class="text-xs text-muted">Waiting for a screenshot…</span>
          {/if}
        </div>
        {#if actionLabel}
          <div class="text-[11px] text-muted mt-1.5 selectable">{actionLabel}</div>
        {/if}
      </div>
    {/if}
  </div>
{/if}
