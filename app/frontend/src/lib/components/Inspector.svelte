<script lang="ts">
  import { X, FileText, GitCompare, Terminal, Undo2, RefreshCw, Monitor, Square, FlaskConical, Image, ChevronLeft, ChevronRight } from '@lucide/svelte';
  import { onMount, tick } from 'svelte';
  import { inspector } from '$lib/stores/inspector.svelte';
  import DiffView from './DiffView.svelte';
  import TurnDetails from './TurnDetails.svelte';
  import { highlightSource } from '$lib/highlight';

  const tab = $derived(inspector.active);

  const icon = { file: FileText, diff: GitCompare, output: Terminal, preview: Monitor, browser: FlaskConical, artifact: Image, turnDetails: Terminal } as const;

  let strip: HTMLDivElement;
  let overflow = $state(false);
  let atStart = $state(true);
  let atEnd = $state(true);

  function updateScroll() {
    if (!strip) return;
    overflow = strip.scrollWidth > strip.clientWidth + 1;
    atStart = strip.scrollLeft <= 1;
    atEnd = strip.scrollLeft + strip.clientWidth >= strip.scrollWidth - 1;
  }

  function revealActive() {
    const active = strip?.querySelector<HTMLElement>('[aria-selected="true"]')?.parentElement;
    if (!active) return;
    const bounds = strip.getBoundingClientRect();
    const item = active.getBoundingClientRect();
    if (item.left < bounds.left) strip.scrollLeft -= bounds.left - item.left;
    else if (item.right > bounds.right) strip.scrollLeft += item.right - bounds.right;
    updateScroll();
  }

  $effect(() => {
    inspector.activeId;
    inspector.tabs.length;
    void tick().then(revealActive);
  });

  onMount(() => {
    const observer = new ResizeObserver(() => { updateScroll(); revealActive(); });
    observer.observe(strip);
    updateScroll();
    revealActive();
    return () => observer.disconnect();
  });

  async function navigate(event: KeyboardEvent, index: number) {
    let next: number;
    if (event.key === 'ArrowRight') next = (index + 1) % inspector.tabs.length;
    else if (event.key === 'ArrowLeft') next = (index - 1 + inspector.tabs.length) % inspector.tabs.length;
    else if (event.key === 'Home') next = 0;
    else if (event.key === 'End') next = inspector.tabs.length - 1;
    else return;
    event.preventDefault();
    inspector.activeId = inspector.tabs[next].id;
    await tick();
    strip.querySelector<HTMLElement>('[aria-selected="true"]')?.focus();
  }
</script>

<div class="h-full min-w-0 flex flex-col bg-surface/40">
  <header class="h-12 min-w-0 shrink-0 border-b border-line flex items-center px-1 gap-1">
    {#if overflow}
      <button class="btn-ghost btn-sm shrink-0 !px-1" aria-label="Scroll tabs left" disabled={atStart} onclick={() => strip.scrollBy({ left: -strip.clientWidth * 0.8 })}>
        <ChevronLeft class="w-3.5 h-3.5" />
      </button>
    {/if}
    <div bind:this={strip} class="flex-1 min-w-0 h-full flex items-center gap-1 overflow-x-auto" role="tablist" aria-label="Inspector tabs" onscroll={updateScroll}>
      {#each inspector.tabs as t, index (t.id)}
        {@const on = inspector.activeId === t.id}
        {@const Icon = icon[t.kind]}
        <div class="flex shrink-0 min-w-0 max-w-full items-center gap-1 px-2 py-1.5 rounded-md text-xs {on ? 'bg-raised text-ink' : 'text-muted hover:bg-raised/60'}">
          <button role="tab" aria-selected={on} tabindex={on ? 0 : -1} title={t.title} class="flex min-w-0 items-center gap-1.5 max-w-44 truncate" onclick={() => (inspector.activeId = t.id)} onkeydown={(event) => navigate(event, index)}>
            <Icon class="w-3.5 h-3.5 shrink-0" />
            <span class="truncate">{t.title}</span>
          </button>
          <button class="icon-button compact shrink-0 text-faint hover:text-ink" aria-label={`Close ${t.title}`} onclick={() => inspector.close(t.id)}>
            <X class="w-4 h-4" />
          </button>
        </div>
      {/each}
    </div>
    {#if overflow}
      <button class="btn-ghost btn-sm shrink-0 !px-1" aria-label="Scroll tabs right" disabled={atEnd} onclick={() => strip.scrollBy({ left: strip.clientWidth * 0.8 })}>
        <ChevronRight class="w-3.5 h-3.5" />
      </button>
    {/if}
    <button class="btn-ghost btn-sm shrink-0" aria-label="Close inspector" onclick={() => inspector.closeAll()}>
      <X class="w-3.5 h-3.5" />
    </button>
  </header>

  {#if tab}
    <div class="px-3 py-1.5 border-b border-line flex items-center gap-2 text-[11px] text-muted">
      <span class="font-mono truncate selectable">{tab.path ?? tab.title}</span>
      <div class="ml-auto flex items-center gap-1 shrink-0">
        {#if tab.kind === 'diff' && tab.change?.turnId && tab.change.revertable}
          <button class="btn-ghost btn-sm" onclick={() => inspector.revertTurn(tab.change!.turnId!, [tab.change!.path])}>
            <Undo2 class="w-3.5 h-3.5" />Undo
          </button>
        {/if}
        {#if tab.kind === 'file' && tab.path}
          <button class="btn-ghost btn-sm" aria-label="Reload" onclick={() => inspector.openFile(tab.path!, tab.threadId, tab.projectId)}>
            <RefreshCw class="w-3.5 h-3.5" />
          </button>
        {/if}
        {#if tab.kind === 'preview' && tab.preview}
          <span class="text-[10px] {tab.preview.status === 'ready' ? 'text-sage' : 'text-muted'}">{tab.preview.status}</span>
          {#if tab.preview.status === 'ready'}
            <button class="btn-ghost btn-sm" aria-label="Reload preview" onclick={() => inspector.reloadPreview(tab.id)}>
              <RefreshCw class="w-3.5 h-3.5" />
            </button>
            <button class="btn-ghost btn-sm" onclick={() => inspector.stopPreview(tab.preview!.id)}>
              <Square class="w-3.5 h-3.5" />Stop preview
            </button>
          {/if}
        {/if}
      </div>
    </div>

    <div class="flex-1 min-h-0 overflow-auto">
      {#if tab.error}
        <p class="p-4 text-sm text-rust">{tab.error}</p>
      {:else if tab.loading}
        <p class="p-4 text-sm text-muted">Loading…</p>
      {:else if tab.kind === 'diff' && tab.change}
        <div class="p-3">
          <p class="text-[11px] text-muted mb-2">
            {tab.change.action} · <span class="text-sage">+{tab.change.additions}</span>
            <span class="text-rust">−{tab.change.deletions}</span>
          </p>
          <DiffView change={tab.change} />
        </div>
      {:else if tab.kind === 'file'}
        {#if tab.binary}
          <p class="p-4 text-sm text-muted">This file is not text, so there is nothing to show here.</p>
        {:else}
          <pre class="code-view p-3 font-mono text-[11px] leading-[1.6] whitespace-pre selectable"><code>{@html highlightSource(tab.content ?? '', tab.path ?? '')}</code></pre>
          {#if tab.truncated}<p class="px-3 pb-3 text-[11px] text-muted">… only the first part of this file is shown.</p>{/if}
        {/if}
      {:else if tab.kind === 'preview' && tab.preview}
        {#if tab.preview.status === 'ready'}
          <iframe
            title={tab.preview.title}
            src={`${tab.preview.url}${tab.preview.url.includes('?') ? '&' : '?'}umcode_reload=${tab.reload ?? 0}`}
            class="w-full h-full border-0 bg-white"
            sandbox="allow-downloads allow-forms allow-modals allow-popups allow-same-origin allow-scripts"
          ></iframe>
        {:else}
          <div class="p-4 space-y-3">
            <p class="text-sm text-muted">The preview server has stopped.</p>
            {#if tab.text}<pre class="font-mono text-[11px] leading-[1.5] whitespace-pre-wrap selectable">{tab.text}</pre>{/if}
          </div>
        {/if}
      {:else if tab.kind === 'browser' && tab.browser}
        <div class="p-4 space-y-4 text-xs">
          <div class="flex items-center gap-2">
            <span class="rounded-full px-2 py-0.5 bg-raised {tab.browser.status === 'passed' ? 'text-sage' : tab.browser.status === 'not_run' ? 'text-muted' : 'text-rust'}">{tab.browser.status.replace('_', ' ')}</span>
            {#if tab.browser.framework}<span class="text-muted">{tab.browser.framework}</span>{/if}
            {#if tab.browser.duration_ms}<span class="text-faint">{tab.browser.duration_ms} ms</span>{/if}
          </div>
          {#if tab.browser.command}<pre class="rounded-md bg-raised p-2 font-mono whitespace-pre-wrap selectable">$ {tab.browser.command}</pre>{/if}
          {#if tab.browser.reason}<p class="text-muted">{tab.browser.reason}</p>{/if}
		  {#if tab.browser.snapshot}<details open><summary class="cursor-pointer text-muted">Rendered page state</summary><pre class="mt-2 rounded-md bg-raised p-2 font-mono whitespace-pre-wrap selectable max-h-80 overflow-auto">{tab.browser.snapshot}</pre></details>{/if}
          {#if tab.browser.diagnostics?.length}
            <section>
              <h3 class="font-medium mb-1">Console and request diagnostics</h3>
              <pre class="rounded-md bg-rust-soft text-rust p-2 whitespace-pre-wrap selectable">{tab.browser.diagnostics.join('\n')}</pre>
            </section>
          {/if}
          <section>
            <h3 class="font-medium mb-1">Artifacts ({tab.browser.artifacts?.length ?? 0})</h3>
            {#if tab.browser.artifacts?.length}
              <div class="space-y-1">
                {#each tab.browser.artifacts as artifact}
                  <button class="w-full flex items-center gap-2 rounded-md px-2 py-1.5 bg-raised hover:bg-line text-left" onclick={() => inspector.openArtifact(artifact, tab.threadId, tab.projectId)}>
                    <span class="text-clay">{artifact.kind}</span><span class="font-mono truncate">{artifact.path}</span><span class="ml-auto text-faint">{artifact.bytes} B</span>
                  </button>
                {/each}
              </div>
            {:else}<p class="text-muted">No screenshots, traces, reports, or videos were produced.</p>{/if}
          </section>
          {#if tab.browser.output}<details><summary class="cursor-pointer text-muted">Command output</summary><pre class="mt-2 font-mono whitespace-pre-wrap selectable">{tab.browser.output}</pre></details>{/if}
        </div>
      {:else if tab.kind === 'turnDetails' && tab.threadId && tab.turnDetails}
        <TurnDetails details={tab.turnDetails} />
      {:else if tab.kind === 'artifact' && tab.artifact}
        {#if tab.dataUrl}
          <div class="h-full p-3 flex items-center justify-center bg-raised"><img src={tab.dataUrl} alt={tab.artifact.path} class="max-w-full max-h-full object-contain" /></div>
        {:else}<p class="p-4 text-sm text-muted">Artifact metadata is available, but this format has no inline viewer.</p>{/if}
      {:else}
        <pre class="p-3 font-mono text-[11px] leading-[1.5] whitespace-pre-wrap selectable">{tab.text}</pre>
      {/if}
    </div>
  {/if}
</div>
