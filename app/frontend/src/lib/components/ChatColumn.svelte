<script lang="ts">
 import CompactionProgress from './CompactionProgress.svelte';
 import CapacityNotice from './CapacityNotice.svelte';
  import { tick } from 'svelte';
  import { Sparkles, KeyRound, FolderPlus, Undo2, Check, Circle, LoaderCircle, FileText, ChevronRight } from '@lucide/svelte';
  import { app } from '$lib/stores/app.svelte';
  import { chat, type ThreadView } from '$lib/stores/chat.svelte';
  import { projects } from '$lib/stores/projects.svelte';
  import { inspector } from '$lib/stores/inspector.svelte';
  import { dialog } from '$lib/stores/dialog.svelte';
  import { usageLine, fmtUsd } from '$lib/format';
  import ItemView from './ItemView.svelte';
  import ChatError from './ChatError.svelte';
  import { taskFailurePresentation, executionOutcomePresentation, isProvisionalExecution } from '$lib/taskOutcome';
  import ToolCall from './ToolCall.svelte';
  import { recordLine, recordsFor } from '$lib/approvalDock';
  import ComputerUseView from './ComputerUseView.svelte';
  import Composer from './Composer.svelte';
  import Agents from './Agents.svelte';
  import type { Item, Turn, TaskWorkspaceResult } from '$lib/types';
  import { createProject } from '$lib/createProject';
  import { formatTurnTime, progressPhrase } from '$lib/turnProgress';

  let { view, variant = 'main' }: { view: ThreadView; variant?: 'main' | 'side' } = $props();

  let scroller: HTMLDivElement | undefined = $state();
  let stick = true;

  interface Group {
    turnId: string;
    turn?: Turn;
    items: Item[];
  }

  const groups = $derived.by(() => {
    const out: Group[] = [];
    const byId = new Map<string, Group>();
    for (const it of view.items) {
      let g = byId.get(it.turnId);
      if (!g) {
        g = { turnId: it.turnId, turn: view.turns.find((t) => t.id === it.turnId), items: [] };
        byId.set(it.turnId, g);
        out.push(g);
      }
      g.items.push(it);
    }
    for (const t of view.turns) if (!byId.has(t.id)) out.push({ turnId: t.id, turn: t, items: [] });
    return out;
  });

  let progressNow = $state(Date.now());
  const hasRunningTurn = $derived(groups.some((g) => g.turn?.status === 'running'));
  $effect(() => {
    if (!hasRunningTurn) return;
    progressNow = Date.now();
    const timer = window.setInterval(() => (progressNow = Date.now()), 1000);
    return () => window.clearInterval(timer);
  });

  const hasKeys = $derived(app.credentials.some((c) => c.enabled));
  const threadProject = $derived(projects.list.find((p) => p.id === view.thread?.projectId));

  function onScroll() {
    if (!scroller) return;
    stick = scroller.scrollHeight - scroller.scrollTop - scroller.clientHeight < 80;
  }

  $effect(() => {
    void view.items.length;
    void view.items.at(-1)?.text;
    void view.turns.length;
    if (stick) tick().then(() => scroller && (scroller.scrollTop = scroller.scrollHeight));
  });

  $effect(() => {
    const id = view.highlightItem;
    if (!id || view.loading) return;
    tick().then(() => {
      document.getElementById(`item-${id}`)?.scrollIntoView({ block: 'center' });
      stick = false;
      setTimeout(() => (view.highlightItem = null), 2500);
    });
  });

  $effect(() => {
    void view.id;
    stick = true;
  });

  function modelLabel(t: Turn): string {
    const r = t.resolved || {};
    const m = app.models.find((x) => x.provider === r.provider && x.id === r.model);
    const name = m?.displayName || r.model || '';
    const cx = r.complexity ? r.complexity : '';
    return [name, cx].filter(Boolean).join(' · ');
  }

  /**
   * When a turn moved off its first model — a rate limit, an outage — the
   * footer says so, so a slower or cheaper answer is never a mystery.
   *
   * A turn calls its model once per tool-calling round, so a normal,
   * fully successful turn can easily have several trail entries that are
   * all "used" and nothing else — that is not a switch, and must not be
   * reported as one just because the trail has more than one entry.
   */
  function switchNote(t: Turn): string {
    const trail = t.routeTrail ?? [];
    if (trail.length < 2) return '';
    const first = trail[0];
    if (first.status === 'used') return ''; // the turn never actually failed over
    const names = trail.map((s) => s.displayName || s.model);
    const used = trail[trail.length - 1];
    if (first.model === used.model) return `switched key after ${statusWord(first.status)}`;
    return `switched from ${names[0]} after ${statusWord(first.status)}`;
  }

  function statusWord(status: string): string {
    return (
      {
        rate_limited: 'a rate limit',
        server_error: 'a provider error',
        unauthorized: 'a key being refused',
        timeout: 'a timeout',
        context_too_long: 'a prompt too long for it',
        unknown_model: 'it being unavailable',
      }[status] ?? 'an error'
    );
  }

  /** Files a turn changed, for the "undo this turn" control. */
  function changedFiles(g: Group): string[] {
    return g.items.filter((i) => i.kind === 'fileChange').map((i) => (i.data as { path?: string })?.path ?? '');
  }

  const latestTurn = $derived([...groups].reverse().find((g) => g.turn)?.turn);
  let reviewedTurnId = $state('');
  let changesKept = $state(false);
  let workspaceDiscarded = $state(false);

  $effect(() => {
    const id = latestTurn?.id ?? '';
    if (id && id !== reviewedTurnId) {
      reviewedTurnId = id;
      changesKept = false;
      workspaceDiscarded = false;
    }
  });

  function actionLabel(it: Item): string {
    if (it.kind === 'fileChange') {
      const change = it.data as { path?: string; action?: string };
      const verb = change.action === 'created' ? 'Created' : change.action === 'deleted' ? 'Removed' : 'Updated';
      return `${verb} ${change.path || 'a file'}`;
    }
    const name = it.tool?.name?.toLowerCase() || '';
    if (/shell|terminal|command|exec/.test(name)) return 'Ran a command';
    if (/read|open|list|search|glob|grep/.test(name)) return 'Inspected project files';
    if (/write|edit|patch|create/.test(name)) return 'Updated project files';
    return it.tool?.name ? `Used ${it.tool.name}` : 'Completed an action';
  }

  function workLogItems(g: Group): Item[] {
    const lastToolCall = Math.max(0, ...g.items.filter((i) => i.kind === 'toolCall').map((i) => i.seq));
    return g.items.filter((i) =>
      i.kind === 'toolCall' || i.kind === 'fileChange' || isProvisionalExecution(i) ||
      (i.kind === 'agentMessage' && i.seq < lastToolCall && Boolean(i.text?.trim())),
    );
  }

  function progressText(item: Item): string {
    const provisional = item.data && typeof item.data === "object" ? (item.data as { provisionalText?: unknown }).provisionalText : undefined;
    const text = (isProvisionalExecution(item) && typeof provisional === 'string' ? provisional : item.text ?? '').trim();
    return text.length > 480 ? `${text.slice(0, 480).trimEnd()}…` : text;
  }

  function isVisibleChatMessage(item: Item): boolean {
    if (isProvisionalExecution(item)) return false;
    return item.kind === 'userMessage' ||
      item.kind === 'contextCompaction' ||
      (item.kind === 'agentMessage' && Boolean(item.text?.trim()));
  }

  function hasPreviousChatMessage(g: Group, index: number): boolean {
    return g.items.slice(0, index).some(isVisibleChatMessage);
  }

  async function undoTurn(g: Group) {
    const files = changedFiles(g).filter(Boolean);
    const ok = await dialog.confirm(
      `Put ${files.length === 1 ? files[0] : `${files.length} files`} back the way this turn found it?`,
      { okLabel: 'Undo changes' },
    );
    if (ok) await inspector.revertTurn(g.turnId);
  }

  async function keepTaskChanges() {
    if (!view.id) return;
    const result = await app.try<TaskWorkspaceResult>('project/keepTaskChanges', { threadId: view.id });
    if (result) {
      app.toast('info', result.message);
      changesKept = true;
    }
  }

  async function discardTaskWorkspace() {
    if (!view.id) return;
    const ok = await dialog.confirm(
      'Permanently remove this chat’s isolated task workspace and any changes not kept in the project? The project folder will not be changed.',
      { okLabel: 'Discard workspace', danger: true },
    );
    if (!ok) return;
    const result = await app.try<TaskWorkspaceResult>('project/discardTaskWorkspace', { threadId: view.id });
    if (result) {
      app.toast('info', result.message);
      workspaceDiscarded = true;
    }
  }

  async function openProject() {
    await createProject();
  }
</script>

<div class="flex-1 min-h-0 flex flex-col">
  {#if variant === 'main'}
    <header class="shrink-0 border-b border-line px-5 py-2.5 bg-paper/90">
      <div class="min-h-9 flex items-center gap-3">
      <div class="min-w-0">
        <h1 class="text-sm font-semibold truncate tracking-[-0.01em]">{view.title}</h1>
        <div class="flex items-center gap-2 mt-0.5 text-[10px] text-muted min-w-0">
          {#if threadProject}
            <span class="font-medium text-ink-soft shrink-0">{threadProject.name}</span>
            <span class="font-mono truncate" title={threadProject.root}>{threadProject.root}</span>
            {#if view.thread?.workspaceMode === 'worktree'}<span class="shrink-0">isolated worktree</span>{:else}<span class="shrink-0">project folder</span>{/if}
            {#if threadProject.vcs?.branch}<span class="shrink-0">branch {threadProject.vcs.branch}</span>{/if}
            <span class="shrink-0">compute {threadProject.tools.compute ? 'on' : 'off'}</span>
          {:else}
            <span>No project</span><span>read-only workspace</span>
          {/if}
        </div>
      </div>
      {#if view.thread?.usage?.requests}
        <span class="ml-auto text-[11px] text-muted" title="Tokens and cost for this chat">
          {fmtUsd(view.thread.usage.costUsd)} / {view.thread.usage.requests} requests
        </span>
      {/if}

      </div>
      {#if latestTurn}
        <div class="mt-2 flex items-center gap-2 text-[10px]" aria-label="Chat lifecycle">
          <span class="text-muted mr-1">Lifecycle</span>
          {#each ['Started', 'Working', 'Done'] as phase, i}
            {@const done = latestTurn.status === 'completed' || latestTurn.status === 'failed' || latestTurn.status === 'interrupted' || (latestTurn.status === 'running' && i === 0)}
            {@const active = latestTurn.status === 'running' && (i === 0 || i === 1)}
            <span class="flex items-center gap-1 {active ? 'text-accent-strong font-medium' : done ? 'text-ink-soft' : 'text-faint'}">
              {#if latestTurn.status === 'running' && i === 1}<LoaderCircle class="w-3 h-3 animate-spin motion-reduce:animate-none" />
              {:else if done}<Check class="w-3 h-3 text-sage" />
              {:else}<Circle class="w-3 h-3" />{/if}
              {phase}
            </span>
            {#if i < 2}<span class="w-5 h-px bg-line"></span>{/if}
          {/each}
        </div>
      {/if}
    </header>
  {/if}

  {#if variant === 'main' && view.id}
    <ComputerUseView items={view.items} projectId={threadProject?.id} threadId={view.thread?.id} />
  {/if}

  <div class="flex-1 min-h-0 overflow-y-auto" bind:this={scroller} onscroll={onScroll}>
    {#if view.context}
      <div class="mx-4 mt-3 px-3 py-2 rounded-lg bg-raised/60 border border-line text-xs text-muted whitespace-pre-wrap selectable">
        {view.context}
      </div>
    {/if}

    {#if !view.id && variant === 'main'}
      <div class="h-full flex flex-col items-center justify-center text-center px-6">
        <Sparkles class="w-8 h-8 text-clay mb-3" />
        <h2 class="text-lg font-semibold">
          {projects.active ? `What should we do in ${projects.active.name}?` : 'What can I help with?'}
        </h2>
        {#if app.conn === 'open' && !hasKeys}
          <p class="text-sm text-muted mt-2 max-w-sm">Add an API key for Claude, OpenAI, Gemini or a local model server, or sign in with ChatGPT for OpenAI, to get started.</p>
          <button class="btn-primary mt-4" onclick={() => (app.view = 'settings')}><KeyRound class="w-4 h-4" />Connect a provider</button>
        {:else if !projects.active}
          <p class="text-sm text-muted mt-2 max-w-sm">
            Without a project I can read and answer, but not change files. Create a project to choose a folder and let me work in it.
          </p>
          <button class="btn-primary mt-4" onclick={openProject}><FolderPlus class="w-4 h-4" />New project</button>
        {:else}
          <p class="text-sm text-muted mt-2 max-w-md">
            I work inside <span class="font-mono text-xs">{projects.active.root}</span> and nowhere else. Ask for a change, and every
            edit shows up here with its diff.
          </p>
        {/if}
      </div>
    {:else if view.loading}
      <div class="p-8 text-sm text-muted text-center">Loading…</div>
    {:else}
      <div class="{variant === 'main' ? 'max-w-3xl' : ''} mx-auto px-4 py-4 space-y-4">
        {#each groups as g (g.turnId)}
          {@const taskFailure = taskFailurePresentation(g.items, g.turn?.error || '', g.turn?.status || '')}
          {@const executionOutcome = executionOutcomePresentation(g.items, g.turn?.status || '')}
          <section class="space-y-2.5 group/turn">
            {#each g.items as it, index (it.id)}
              {#if !isProvisionalExecution(it) && !taskFailure.hiddenItemIds.includes(it.id) && (it.kind === 'userMessage' || it.kind === 'contextCompaction' || it.kind === 'error' || it.kind === 'inboundEvent' || it.kind === 'approval' || (it.kind === 'agentMessage' && Boolean(it.text?.trim())))}
                {#if isVisibleChatMessage(it) && hasPreviousChatMessage(g, index)}
                  <div class="h-px bg-line/70 my-2" aria-hidden="true"></div>
                {/if}
                <ItemView item={it} highlight={view.highlightItem === it.id} {view} />
              {/if}
            {/each}
            {#if taskFailure.failure}
              <ChatError text={taskFailure.text} details={[taskFailure.details, executionOutcome.details].filter(Boolean).join("\n\n")} />
            {:else if executionOutcome.show}
              <p class="text-xs text-muted" role="status">{executionOutcome.text}</p>
            {/if}
            {#each recordsFor(app.approvalRecords, view.id ?? '', g.turnId) as record (record.id)}
              <div class="flex items-center gap-2 text-xs {record.decision === 'denied' ? 'text-rust' : 'text-sage'}">
                <span aria-hidden="true">{record.decision === 'denied' ? '✕' : '✓'}</span><span>{recordLine(record)}</span>
              </div>
            {/each}
            {#if g.turn?.status === 'running'}
              <details class="turn-progress text-sm text-muted">
                <summary class="block border-b border-line/70 pb-1.5 cursor-pointer select-none hover:text-ink focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent">
                  <span class="inline-flex max-w-full items-center gap-2">
                    <span class="truncate" aria-live="polite">{progressPhrase(g.items, app.approvals.some((approval) => approval.threadId === view.id && approval.turnId === g.turnId && !approval.agentChildId))}</span>
                    <ChevronRight class="turn-progress-chevron w-3.5 h-3.5 shrink-0 transition-transform" aria-hidden="true" />
                    <span class="shrink-0 tabular-nums" title="Time working">{formatTurnTime(g.turn.startedAt, progressNow)}</span>
                  </span>
                </summary>
                <div class="pt-2 text-[11px] text-muted">
                  {#if workLogItems(g).length > 0}
                    <ol class="ml-1 pl-4 border-l border-line space-y-2.5">
                      {#each workLogItems(g) as action (action.id)}
                        <li class="pl-1">
                          {#if action.kind === 'agentMessage'}
                            <div class="text-ink-soft">
                              <span class="block text-[10px] uppercase tracking-wide text-faint">{isProvisionalExecution(action) ? 'Unverified model narration' : 'Progress update'}</span>
                              <p class="mt-0.5 whitespace-pre-wrap">{progressText(action)}</p>
                            </div>
                          {:else if action.kind === 'toolCall' && action.tool}
                            <ToolCall item={action} projectId={view.thread?.projectId} />
                          {:else if action.kind === 'fileChange'}
                            <button class="text-ink-soft hover:text-accent-strong hover:underline" onclick={() => inspector.openFile((action.data as { path?: string }).path ?? '', view.id ?? undefined, view.thread?.projectId)}>
                              {actionLabel(action)}
                            </button>
                          {/if}
                        </li>
                      {/each}
                    </ol>
                  {:else}
                    <p>No activity recorded yet.</p>
                  {/if}
                  <p class="mt-2 text-[10px] text-faint">Progress updates show observed actions; private model reasoning isn’t shown.</p>
                </div>
              </details>
            {:else if g.turn}
              {#if g.turn.status === 'failed' && taskFailure.showTurnError && !g.items.some((item) => item.kind === 'error' && item.text === g.turn?.error)}
                <ChatError text={g.turn.error || ''} />
              {/if}
              <div class="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1 text-[11px] text-faint [overflow-wrap:anywhere]">
                {#if g.turn.status === 'interrupted'}<span class="text-amber-warm">Stopped</span>{/if}
                <span>{modelLabel(g.turn)}</span>
                {#if switchNote(g.turn)}
                  <span class="text-amber-warm" title={(g.turn.routeTrail ?? []).map((s) => `${s.displayName || s.model} (${s.credentialLabel}): ${s.status}${s.error ? ` — ${s.error}` : ''}`).join('\n')}>
                    {switchNote(g.turn)}
                  </span>
                {/if}
                {#if changedFiles(g).length > 0}
                  <button class="opacity-0 group-hover/turn:opacity-100 transition-opacity hover:text-ink flex items-center gap-1" onclick={() => undoTurn(g)}>
                    <Undo2 class="w-3 h-3" />undo this turn
                  </button>
                {/if}
                {#if usageLine(g.turn.usage)}
                  <span class="ml-auto" title="Tokens and cost for this turn">{usageLine(g.turn.usage)}</span>
                {/if}
              </div>
            {/if}
            {#if g.turn && g.turn.status !== 'running' && changedFiles(g).filter(Boolean).length > 0}
              <div class="flex flex-wrap items-center gap-1.5 text-[11px]">
                <span class="text-muted mr-1">Files</span>
                {#each [...new Set(changedFiles(g).filter(Boolean))] as path (path)}
                  <button class="inline-flex max-w-full items-center gap-1 rounded-md border border-line bg-surface px-2 py-1 text-ink-soft hover:border-accent/50 hover:text-accent-strong" title={`Open ${path} in inspector`} onclick={() => inspector.openFile(path, view.id ?? undefined, view.thread?.projectId)}>
                    <FileText class="w-3 h-3 shrink-0" /><span class="font-mono truncate">{path}</span>
                  </button>
                {/each}
              </div>
            {/if}
            {#if g.turn && g.turn.id === latestTurn?.id && g.turn.status !== 'running' && threadProject && view.thread?.workspaceMode === 'worktree' && view.items.some((i) => i.kind === 'fileChange') && !changesKept && !workspaceDiscarded}
              <div class="flex items-center gap-2 text-[11px]">
                <button class="btn-outline btn-sm" onclick={keepTaskChanges}><Check class="w-3 h-3" />Keep task changes in project</button>
                <button class="btn-ghost btn-sm text-rust" onclick={discardTaskWorkspace}>Discard task workspace</button>
              </div>
            {:else if g.turn && g.turn.id === latestTurn?.id && changesKept}
              <div class="text-[11px] text-sage">Task changes kept in the project checkout.</div>
            {:else if g.turn && g.turn.id === latestTurn?.id && workspaceDiscarded}
              <div class="text-[11px] text-muted">Task workspace discarded.</div>
            {:else if g.turn && g.turn.id === latestTurn?.id && threadProject && view.thread?.workspaceMode !== 'worktree' && g.items.some((i) => i.kind === 'fileChange')}
              <div class="text-[11px] text-muted">Changes are already in the project folder.</div>
            {/if}
            {#if g.turn && g.turn.status !== 'running' && workLogItems(g).length > 0}
              <details class="text-[11px] text-muted">
                <summary class="w-fit cursor-pointer select-none hover:text-ink">Work log · {workLogItems(g).length} updates and actions</summary>
                <ol class="mt-2 ml-1 pl-4 border-l border-line space-y-2.5">
                  {#each workLogItems(g) as action (action.id)}
                    <li class="pl-1">
                      {#if action.kind === 'agentMessage'}
                        <div class="text-ink-soft">
                          <span class="block text-[10px] uppercase tracking-wide text-faint">{isProvisionalExecution(action) ? 'Unverified model narration' : 'Progress update'}</span>
                          <p class="mt-0.5 whitespace-pre-wrap">{progressText(action)}</p>
                        </div>
                      {:else if action.kind === 'toolCall' && action.tool}
                        <ToolCall item={action} />
                      {:else if action.kind === 'fileChange'}
                        <button class="text-ink-soft hover:text-accent-strong hover:underline" onclick={() => inspector.openFile((action.data as { path?: string }).path ?? '', view.id ?? undefined, view.thread?.projectId)}>
                          {actionLabel(action)}
                        </button>
                      {/if}
                    </li>
                  {/each}
                </ol>
                <p class="mt-2 text-[10px] text-faint">Progress updates are concise summaries; private model reasoning isn’t shown.</p>
              </details>
            {/if}
          </section>
        {/each}
      </div>
    {/if}
  </div>

  {#if view.id}<Agents state={view.delegation} threadId={view.id} projectId={view.thread?.projectId} />{/if}
  <CompactionProgress state={view.compaction} />
  <CapacityNotice selection={view.selection} />
  <Composer {view} compact={variant === 'side'} />
</div>

<style>
  .turn-progress > summary { list-style: none; }
  .turn-progress > summary::-webkit-details-marker { display: none; }
  .turn-progress[open] :global(.turn-progress-chevron) { transform: rotate(90deg); }
</style>
