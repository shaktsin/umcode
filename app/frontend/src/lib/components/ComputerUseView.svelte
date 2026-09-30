<script lang="ts">
  // A fixed-size, always-on view of what Computer Use is doing in this
  // chat — the live equivalent of the tiny screenshot on an approval card,
  // shown whether or not any action here is actually waiting for approval.
  // It reads the same item stream every other part of the chat already
  // gets: computer.start/inspect/act calls publish a normal tool item on
  // every step, auto-approved or not, so this needs no extra plumbing.
  import { Monitor, MousePointer2, Plus, RefreshCw, Send, Square, X } from '@lucide/svelte';
  import { app } from '$lib/stores/app.svelte';
  import type { Item, ProjectArtifactContent } from '$lib/types';

  let { items, projectId, threadId }: { items: Item[]; projectId?: string; threadId?: string } = $props();

  interface ComputerArtifact {
    path: string;
    kind: string;
  }
  interface ComputerReport {
    status?: string;
    session_id?: string;
    state?: { app?: { name?: string }; window?: { title?: string; pixel_width?: number; pixel_height?: number }; permission?: string };
    artifacts?: ComputerArtifact[];
    reason?: string;
    action_count?: number;
    observation_id?: string;
    last_action?: { type?: string; x?: number; y?: number; screenshot_width?: number; screenshot_height?: number; observation_id?: string; result?: string };
  }

  const lastItem = $derived.by(() => {
    for (let i = items.length - 1; i >= 0; i--) {
      const it = items[i];
      if (it.tool?.name?.startsWith('computer.') && it.tool.name !== 'computer.list' && (it.tool.output || it.tool.error)) return it;
    }
    return undefined;
  });

  const stopped = $derived.by(() => {
    for (let i = items.length - 1; i >= 0; i--) {
      const it = items[i];
      if (it.tool?.name === 'computer.stop') return true;
      if (it.tool?.name?.startsWith('computer.') && it.tool.name !== 'computer.list' && (it.tool.output || it.tool.error)) return false;
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
  let manualReport = $state<ComputerReport | undefined>(undefined);
  const activeReport = $derived(manualReport ?? report);

  const actionLabel = $derived.by(() => {
    const it = lastItem;
    if (!it?.tool) return '';
    if (it.tool.error) return 'Computer Use needs attention';
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
        if (action === 'move') return `Moved pointer to (${Math.round(Number(args.x) || 0)}, ${Math.round(Number(args.y) || 0)})`;
        if (action === 'click' || action === 'double_click') return `${action === 'double_click' ? 'Double-clicked' : 'Clicked'} (${Math.round(Number(args.x) || 0)}, ${Math.round(Number(args.y) || 0)})`;
        if (action === 'type') return 'Typed text into the focused app';
        if (action === 'fill') return 'Filled a field (text hidden)';
        if (action === 'key') return `Pressed ${String(args.key ?? '')}`;
        if (action === 'scroll') return `Scrolled ${Number(args.delta) > 0 ? 'down' : 'up'}`;
        return action;
      }
      default:
        return it.tool.name;
    }
  });

  const appName = $derived(activeReport?.state?.app?.name || '');
  // Keep the last target point visible even when the rolling screenshot poll
  // replaces the click-annotated artifact with a plain screenshot.
  const lastActionPoint = $derived.by(() => {
    const evidence = activeReport?.last_action;
    const evidenceWidth = evidence?.screenshot_width ?? 0;
    const evidenceHeight = evidence?.screenshot_height ?? 0;
    if (evidence && ['move', 'click', 'double_click', 'fill'].includes(String(evidence.type)) && evidenceWidth > 0 && evidenceHeight > 0) {
      return { x: Math.max(0, Math.min(evidenceWidth, Number(evidence.x) || 0)) / evidenceWidth * 100, y: Math.max(0, Math.min(evidenceHeight, Number(evidence.y) || 0)) / evidenceHeight * 100 };
    }
    for (let i = items.length - 1; i >= 0; i--) {
      const item = items[i];
      if (item.tool?.name === 'computer.stop') return undefined;
      if (item.tool?.name === 'computer.start') return undefined;
      if (item.tool?.name !== 'computer.act' || !item.tool.output || item.tool.error) continue;
      const args = (item.tool.args ?? {}) as Record<string, unknown>;
      const action = args.action;
      const x = Number(args.x);
      const y = Number(args.y);
      const width = activeReport?.state?.window?.pixel_width ?? 0;
      const height = activeReport?.state?.window?.pixel_height ?? 0;
      if (!['move', 'click', 'double_click', 'fill'].includes(String(action)) || !Number.isFinite(x) || !Number.isFinite(y) || width <= 0 || height <= 0) return undefined;
      return { x: Math.max(0, Math.min(width, x)) / width * 100, y: Math.max(0, Math.min(height, y)) / height * 100 };
    }
    return undefined;
  });

  let collapsed = $state(false);
  let dismissed = $state(false);
  let displayedSessionId = '';
  let expanded = $state(false);
  let clickMode = $state(false);
  let typing = $state('');
  let busy = $state(false);
  let userActionLabel = $state('');
  let refreshInFlight = $state(false);
  const activeScreenshotPath = $derived.by(() => {
    const arts = activeReport?.artifacts ?? [];
    return arts.find((a) => a.kind === 'screenshot_action')?.path ?? arts.find((a) => a.kind === 'screenshot_click')?.path ?? arts.find((a) => a.kind === 'screenshot')?.path;
  });
  $effect(() => {
    void lastItem?.id;
    manualReport = undefined;
  });

  // Closing the view only hides its UI. A new Computer Use session makes the
  // live view visible again; closing never stops the target app/session.
  $effect(() => {
    const sessionId = activeReport?.session_id;
    if (sessionId && sessionId !== displayedSessionId) {
      displayedSessionId = sessionId;
      dismissed = false;
    }
  });

  // Keep the embedded target view live while the agent session is open.
  // Refresh writes one rolling screenshot artifact, so this doesn't create an
  // unbounded trail of nearly identical images in the project.
  $effect(() => {
    const id = threadId;
    if (!id || stopped || collapsed || dismissed || !activeReport?.session_id) return;
    let alive = true;
    const timer = setInterval(async () => {
      if (!alive || document.visibilityState !== 'visible' || busy || refreshInFlight) return;
      refreshInFlight = true;
      try {
        const result = await app.call<string>('computer/inspect', { threadId: id }, 15_000);
        if (alive && result) {
          loadedPath = '';
          manualReport = JSON.parse(result) as ComputerReport;
        }
      } catch {
        // A stopped target or a temporary macOS permission issue is already
        // shown by the regular Computer Use tool result; don't toast per poll.
      } finally {
        refreshInFlight = false;
      }
    }, 1800);
    return () => {
      alive = false;
      clearInterval(timer);
    };
  });
  let screenshotUrl = $state('');
  let loadedPath = '';
  $effect(() => {
    const path = activeScreenshotPath;
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

  async function act(action: Record<string, unknown>) {
    if (!threadId || busy) return;
    busy = true;
    try {
      const result = await app.try<string>('computer/act', { threadId, observation_id: activeReport?.observation_id, ...action });
      if (result) {
        manualReport = JSON.parse(result) as ComputerReport;
        userActionLabel = action.action === 'move' ? 'Pointer moved in the target app' : action.action === 'click' ? 'You clicked the target app' : action.action === 'scroll' ? 'You scrolled the target app' : 'You typed into the target app';
      }
    } catch {
      // app.try already shows the useful error in the standard toast.
    } finally {
      busy = false;
    }
  }

  function clickTarget(event: MouseEvent) {
    if (!clickMode || !activeReport?.state?.window || !screenshotUrl) return;
    const img = (event.currentTarget as HTMLButtonElement).querySelector('img');
    if (!img) return;
    const box = img.getBoundingClientRect();
    const sourceW = img.naturalWidth || activeReport.state.window.pixel_width || box.width;
    const sourceH = img.naturalHeight || activeReport.state.window.pixel_height || box.height;
    const scale = Math.min(box.width / sourceW, box.height / sourceH);
    const renderedW = sourceW * scale;
    const renderedH = sourceH * scale;
    const left = box.left + (box.width - renderedW) / 2;
    const top = box.top + (box.height - renderedH) / 2;
    const x = Math.max(0, Math.min(sourceW - 1, (event.clientX - left) / scale));
    const y = Math.max(0, Math.min(sourceH - 1, (event.clientY - top) / scale));
    void act({ action: 'click', x, y });
  }
</script>

{#if lastItem && !stopped}
  {#if dismissed}
    <div class="mx-auto mt-3 flex w-[calc(100%-2rem)] max-w-4xl items-center gap-2 rounded-xl border border-line bg-surface px-3 py-2 text-xs" aria-label="Computer Use view closed">
      <span class="grid h-6 w-6 shrink-0 place-items-center rounded-md bg-accent/10 text-accent"><Monitor class="h-3.5 w-3.5" /></span>
      <span class="min-w-0 flex-1 truncate font-medium text-ink">Computer Use{#if appName}<span class="font-normal text-muted"> · {appName}</span>{/if}</span>
      {#if activeReport?.session_id}<span class="flex items-center gap-1.5 text-[10px] text-sage"><span class="h-1.5 w-1.5 animate-pulse rounded-full bg-sage"></span>Live</span>{/if}
      <button class="btn-ghost btn-sm" onclick={() => (dismissed = false)}>Show</button>
    </div>
  {:else}
  <section class="mx-auto mt-3 w-[calc(100%-2rem)] max-w-4xl overflow-hidden rounded-2xl border border-line bg-surface shadow-sm" aria-label="Computer Use live workspace">
    <header class="flex min-h-10 items-center gap-2.5 border-b border-line px-3 py-1.5">
      <span class="grid h-7 w-7 shrink-0 place-items-center rounded-lg bg-accent/10 text-accent"><Monitor class="h-3.5 w-3.5" /></span>
      <div class="min-w-0">
        <div class="flex items-center gap-2 text-xs font-semibold text-ink"><span>{activeReport?.session_id ? 'Using computer' : 'Computer Use stopped'}</span><span class="text-muted">·</span><span class="truncate font-normal text-muted">{appName || activeReport?.state?.window?.title || 'Connecting to target app'}</span></div>
      </div>
      <div class="ml-auto flex shrink-0 items-center gap-2">
        {#if activeReport?.session_id}<span class="flex items-center gap-1.5 text-[10px] text-sage"><span class="h-1.5 w-1.5 animate-pulse rounded-full bg-sage"></span>Live</span>{/if}
        <button class="btn-ghost btn-sm" disabled={busy || refreshInFlight || !activeReport?.session_id} aria-label="Refresh target app view" title="Refresh view" onclick={async () => { if (!threadId || refreshInFlight) return; refreshInFlight = true; try { const result = await app.call<string>('computer/inspect', { threadId }); if (result) { loadedPath = ''; manualReport = JSON.parse(result) as ComputerReport; } } catch {} finally { refreshInFlight = false; } }}><RefreshCw class="h-3.5 w-3.5" /></button>
        {#if activeReport?.session_id}<button class="btn-ghost btn-sm text-danger" disabled={busy || refreshInFlight} aria-label="Stop Computer Use" title="Stop controlling this app; leave it open" onclick={async () => { if (!threadId || busy || refreshInFlight) return; busy = true; try { await app.call('computer/stop', { threadId }); manualReport = { ...(activeReport ?? {}), session_id: undefined, status: 'stopped' }; clickMode = false; userActionLabel = `${appName || 'Target app'} remains open; Computer Use stopped`; } catch {} finally { busy = false; } }}><Square class="h-3.5 w-3.5" />Stop</button>{/if}
        {#if screenshotUrl}<button class="btn-ghost btn-sm" aria-expanded={expanded} onclick={() => (expanded = !expanded)}>{expanded ? 'Compact' : 'Expand'}</button>{/if}
        <button class="btn-ghost btn-sm" aria-expanded={!collapsed} onclick={() => (collapsed = !collapsed)}>{collapsed ? 'Show' : 'Hide'}</button>
        <button class="btn-ghost btn-sm" aria-label="Close Computer Use view" title="Close view; the session will keep running" onclick={() => { dismissed = true; clickMode = false; }}><X class="h-3.5 w-3.5" /></button>
      </div>
    </header>
    {#if !collapsed}
      <div class="p-3 sm:p-4">
        {#if lastItem?.tool?.error}
          <div class="mb-2 rounded-xl border border-amber-warm/40 bg-amber-warm/10 px-3 py-2 text-xs text-ink-soft selectable">{lastItem.tool.error}</div>
        {:else if activeReport?.state?.permission && activeReport.state.permission !== 'ready'}
          <div class="mb-2 rounded-xl border border-amber-warm/40 bg-amber-warm/10 px-3 py-2 text-xs text-ink-soft">Screen capture works. Enable Accessibility in System Settings → Privacy &amp; Security → Accessibility to allow clicks and typing.</div>
        {/if}
        <div class="relative flex {screenshotUrl ? expanded ? 'h-[min(64vh,42rem)] min-h-[220px]' : 'h-[clamp(120px,18vh,176px)]' : 'min-h-20'} items-center justify-center overflow-hidden rounded-xl border border-line bg-[#111318]">
          {#if screenshotUrl}
            <button type="button" class="group flex h-full min-h-0 w-full items-center justify-center {clickMode ? 'cursor-crosshair' : 'cursor-default'}" aria-label="Live target app view; enable click mode to interact" onclick={clickTarget} onwheel={(e) => { if (clickMode) { e.preventDefault(); void act({ action: 'scroll', delta: Math.round(e.deltaY) }); } }}>
              <span
                class="relative block h-full max-h-full max-w-full shrink"
                style={`aspect-ratio:${activeReport?.state?.window?.pixel_width || 16}/${activeReport?.state?.window?.pixel_height || 9};`}
              >
                <img src={screenshotUrl} alt="Live view of the selected application" class="absolute inset-0 h-full w-full object-contain" />
                {#if lastActionPoint}
                  <span class="pointer-events-none absolute z-20 transition-[left,top] duration-300 ease-out" style={`left:${lastActionPoint.x}%;top:${lastActionPoint.y}%;transform:translate(-4px,-4px)`} aria-label="UMCode pointer location">
                    <span class="absolute -left-5 -top-5 h-12 w-12 rounded-full border-2 border-sky-300/90 bg-sky-400/20 shadow-[0_0_0_5px_rgba(56,189,248,0.16),0_0_18px_rgba(56,189,248,0.75)]"></span>
                    <span class="absolute left-1 top-1 grid h-9 w-9 place-items-center rounded-full border-2 border-white bg-accent text-white shadow-[0_2px_12px_rgba(0,0,0,0.65)]"><MousePointer2 class="h-6 w-6 fill-accent stroke-white stroke-[2.5]" /></span>
                    <span class="absolute left-7 top-7 grid h-5 w-5 place-items-center rounded-full border-2 border-white bg-sky-500 text-white shadow"><Plus class="h-3.5 w-3.5 stroke-[3]" /></span>
                  </span>
                {/if}
              </span>
              {#if clickMode}<span class="pointer-events-none absolute bottom-3 left-1/2 -translate-x-1/2 rounded-full bg-ink/80 px-3 py-1.5 text-[11px] font-medium text-white shadow">Click a control in the target app</span>{/if}
              <span class="pointer-events-none absolute left-3 top-3 z-10 inline-flex items-center gap-2 rounded-full border border-sky-200/50 bg-slate-950/85 px-3 py-1.5 text-[11px] font-semibold text-white shadow-lg backdrop-blur">{#if activeReport?.session_id}<span class="relative flex h-2.5 w-2.5"><span class="absolute inline-flex h-full w-full animate-ping rounded-full bg-sky-300 opacity-70"></span><span class="relative inline-flex h-2.5 w-2.5 rounded-full bg-sky-400"></span></span>UMCode is controlling this app{:else}Computer Use stopped{/if}{#if appName}<span class="font-normal text-slate-200">· {appName}</span>{/if}</span>
            </button>
          {:else}
            <div class="max-w-sm px-5 text-center text-xs text-muted">{lastItem?.tool?.error ? 'Resolve the permission issue above, then ask UMCode to retry Computer Use.' : 'The target app view will appear here when UMCode connects.'}</div>
          {/if}
          {#if busy || refreshInFlight}<div class="absolute right-3 top-3 flex items-center gap-2 rounded-full bg-ink/75 px-3 py-1.5 text-[11px] font-medium text-white"><span class="h-1.5 w-1.5 animate-pulse rounded-full bg-sage"></span>{busy ? 'Sending action…' : 'Refreshing live view…'}</div>{/if}
        </div>
        <div class="mt-2 flex flex-wrap items-center gap-2">
          <button class="btn-outline btn-sm" disabled={busy || !screenshotUrl} aria-pressed={clickMode} onclick={() => (clickMode = !clickMode)}><MousePointer2 class="h-3.5 w-3.5" />{clickMode ? 'Click mode on' : 'Control app'}</button>
          <span class="text-[11px] text-muted">{clickMode ? 'Click or scroll in the preview to control the selected app.' : userActionLabel || actionLabel}{#if lastActionPoint}<span> · last pointer action marked in the live view</span>{/if}{#if activeReport?.observation_id}<span> · observation {activeReport.observation_id.slice(-6)}</span>{/if}{#if activeReport?.action_count}<span> · {activeReport.action_count} actions</span>{/if}</span>
        </div>
        {#if clickMode}<form class="mt-2 flex gap-2" onsubmit={(e) => { e.preventDefault(); if (typing.trim()) { void act({ action: 'type', text: typing }); typing = ''; } }}>
          <input class="input min-w-0 flex-1 py-2 text-xs" bind:value={typing} placeholder="Type into the target app’s focused field…" aria-label="Text to type into the selected app" />
          <button class="btn-outline btn-sm" type="submit" disabled={busy || !typing.trim()}><Send class="h-3.5 w-3.5" />Send</button>
          <button class="btn-ghost btn-sm" type="button" disabled={busy} aria-label="Exit app control mode" title="Exit control mode" onclick={() => (clickMode = false)}><Square class="h-3.5 w-3.5" />Exit</button>
        </form>{/if}
      </div>
    {/if}
  </section>
  {/if}
{/if}
