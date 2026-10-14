<script lang="ts">
  import {
    Archive, ChevronDown, ChevronRight, Folder, MessageSquarePlus, MoreHorizontal, Pin, Plus, Search, X, LoaderCircle,
  } from '@lucide/svelte';
  import PopupSurface from './PopupSurface.svelte';
  import { chat } from '$lib/stores/chat.svelte';
  import { app } from '$lib/stores/app.svelte';
  import { projects } from '$lib/stores/projects.svelte';
  import { dialog } from '$lib/stores/dialog.svelte';
  import { relTime } from '$lib/format';
  import type { Project, Thread } from '$lib/types';

  let menuAnchor: HTMLButtonElement | undefined = $state();
  let projectMenuAnchor: HTMLButtonElement | undefined = $state();
  let q = $state('');
  let timer: ReturnType<typeof setTimeout> | undefined;
  let menuFor = $state<string | null>(null);
  let projectMenuFor = $state<string | null>(null);
  let renaming = $state<string | null>(null);
  let renameText = $state('');
  let renamingProject = $state<string | null>(null);
  let projectName = $state('');
  let expanded = $state<Record<string, boolean>>({});
  let archivedExpanded = $state(false);

  const groups = $derived(projects.list.map((project) => ({
    project,
    threads: chat.rootThreads.filter((thread) => thread.projectId === project.id),
  })));
  const looseThreads = $derived(chat.rootThreads.filter((thread) => !thread.projectId));
  const archivedThreads = $derived(chat.archivedRootThreads);

  function isExpanded(id: string) { return expanded[id] ?? true; }
  function toggle(id: string) { expanded = { ...expanded, [id]: !isExpanded(id) }; }
  function onInput() {
    clearTimeout(timer);
    timer = setTimeout(() => chat.search(q), 200);
  }
  function clear() { q = ''; chat.search(''); }
  function startRename(t: Thread) { menuFor = null; renaming = t.id; renameText = t.title; }
  async function commitRename(t: Thread) {
    const title = renameText.trim();
    renaming = null;
    if (title && title !== t.title) await chat.rename(t.id, title);
  }
  function startProjectRename(project: Project) {
    renamingProject = project.id;
    projectName = project.name;
  }
  async function commitProjectRename(project: Project) {
    const name = projectName.trim();
    renamingProject = null;
    if (name && name !== project.name) await projects.rename(project.id, name);
  }
  async function openProject(project: Project) {
    await projects.open(project.id);
    app.view = 'project';
  }
  async function doExport(t: Thread) {
    menuFor = null;
    const md = await chat.exportMarkdown(t);
    if (!md) return;
    try {
      await navigator.clipboard.writeText(md);
      app.toast('info', 'Copied the conversation as Markdown.');
    } catch {
      app.toast('error', 'Could not copy to the clipboard.');
    }
  }
  async function confirmDelete(t: Thread) {
    menuFor = null;
    if (await dialog.confirm(`Delete “${t.title || 'Untitled'}”? This cannot be undone.`, { okLabel: 'Delete', danger: true })) {
      void chat.remove(t);
    }
  }
</script>



{#snippet threadRow(t: Thread)}
  {@const active = chat.main.id === t.id}
  <div class="group/thread relative rounded-lg {active ? 'bg-accent-soft' : 'hover:bg-raised/70'}">
    {#if renaming === t.id}
      <!-- svelte-ignore a11y_autofocus -->
      <input
        class="w-full bg-paper border border-accent rounded-lg px-2.5 py-1.5 text-xs focus:outline-none"
        bind:value={renameText}
        autofocus
        onkeydown={(e) => {
          if (e.key === 'Enter') commitRename(t);
          if (e.key === 'Escape') renaming = null;
        }}
        onblur={() => commitRename(t)}
      />
    {:else}
      <button class="w-full text-left px-2.5 py-1.5 pr-8" onclick={() => chat.open(t.id)} ondblclick={() => startRename(t)}>
        <div class="flex items-center gap-1.5 text-xs {active ? 'text-accent-strong font-medium' : 'text-ink-soft'}">
          {#if chat.isWorking(t.id)}<LoaderCircle class="w-3.5 h-3.5 shrink-0 text-clay animate-spin motion-reduce:animate-none" aria-label="Working" />{/if}
          {#if t.pinned}<Pin class="w-3 h-3 text-accent shrink-0" />{/if}
          <span class="truncate">{t.title || 'New chat'}</span>
        </div>
        <div class="text-[10px] text-faint flex gap-1.5 mt-0.5">
          <span>{relTime(t.updatedAt)}</span>
          {#if t.channel && t.channel !== 'app' && t.channel !== 'cli'}<span>{t.channel}</span>{/if}
        </div>
      </button>
      <button
        class="absolute right-1.5 top-1.5 p-1 rounded-md text-faint hover:text-ink hover:bg-line opacity-0 group-hover/thread:opacity-100 {menuFor === t.id ? 'opacity-100' : ''}"
        aria-label="Chat actions"
        onclick={(e) => { e.stopPropagation(); menuAnchor=e.currentTarget; projectMenuFor=null; menuFor = menuFor === t.id ? null : t.id; }}
      ><MoreHorizontal class="w-3.5 h-3.5" /></button>
      {#if menuFor === t.id}
        <PopupSurface open={true} anchor={menuAnchor} kind="menu" onclose={() => menuFor=null}>
        <div class="popup-list py-1 text-xs" role="menu" aria-label="Chat actions" tabindex="-1" onclick={(e) => {e.stopPropagation();if(!menuFor&&!renaming)menuAnchor?.focus();}} onkeydown={() => {}}>
          <button role="menuitem" class="menu-item" onclick={() => { menuFor = null; chat.pin(t); }}><Pin class="w-3.5 h-3.5" />{t.pinned ? 'Unpin' : 'Pin'}</button>
          <button role="menuitem" class="menu-item" onclick={async () => { menuFor = null; if (chat.main.id !== t.id) await chat.open(t.id); void chat.startSide(''); }}><MessageSquarePlus class="w-3.5 h-3.5" />Start side chat</button>
          <button role="menuitem" class="menu-item" onclick={() => startRename(t)}>Rename</button>
          <button role="menuitem" class="menu-item" onclick={() => { menuFor = null; chat.fork(t); }}>Fork</button>
          <button role="menuitem" class="menu-item" onclick={() => doExport(t)}>Copy as Markdown</button>
          <button role="menuitem" class="menu-item" onclick={() => { menuFor = null; chat.archive(t); }}><Archive class="w-3.5 h-3.5" />{t.archived ? 'Unarchive' : 'Archive'}</button>
          <button role="menuitem" class="menu-item text-rust" onclick={() => confirmDelete(t)}>Delete</button>
        </div>
        </PopupSurface>
      {/if}
    {/if}
  </div>
{/snippet}

<div class="flex-1 min-h-0 flex flex-col">
  <div class="px-3 pt-3 pb-2">
    <div class="relative">
      <Search class="w-3.5 h-3.5 absolute left-2.5 top-1/2 -translate-y-1/2 text-faint" />
      <input id="thread-search" class="rail-search" placeholder="Search chats" bind:value={q} oninput={onInput} />
      {#if q}
        <button class="absolute right-2 top-1/2 -translate-y-1/2 text-faint hover:text-ink" aria-label="Clear search" onclick={clear}><X class="w-3.5 h-3.5" /></button>
      {/if}
    </div>
  </div>

  <div class="flex items-center px-4 pb-1.5">
    <span class="section-label">Chats</span>
    <button class="ml-auto p-1 rounded-md text-muted hover:text-ink hover:bg-raised" aria-label="Create chat" title="Create chat" onclick={() => chat.newChat()}><Plus class="w-3.5 h-3.5" /></button>
  </div>

  <div class="flex-1 min-h-0 overflow-y-auto px-2.5 pb-3 space-y-1">
    {#if q.trim()}
      {#if chat.hits.length === 0}<p class="text-xs text-muted px-2 py-3">No matches.</p>{/if}
      {#each chat.hits as hit (hit.itemId)}
        <button class="w-full text-left px-2.5 py-2 rounded-lg hover:bg-raised/70" onclick={() => chat.open(hit.threadId, hit.itemId)}>
          <div class="text-xs text-ink truncate">{hit.title || 'Untitled'}</div>
          <div class="text-[11px] text-muted line-clamp-2 mt-0.5">{hit.snippet}</div>
        </button>
      {/each}
    {:else}
      {#each groups as group (group.project.id)}
        <section class="pb-1">
          <div class="group/project relative flex items-center gap-1 rounded-lg hover:bg-raised/50">
            <button class="p-1.5 text-faint hover:text-ink" aria-label={`Toggle ${group.project.name}`} onclick={() => toggle(group.project.id)}>
              {#if isExpanded(group.project.id)}<ChevronDown class="w-3.5 h-3.5" />{:else}<ChevronRight class="w-3.5 h-3.5" />{/if}
            </button>
            <Folder class="w-3.5 h-3.5 text-muted shrink-0" />
            {#if renamingProject === group.project.id}
              <!-- svelte-ignore a11y_autofocus -->
              <input
                class="min-w-0 flex-1 bg-paper border border-accent rounded px-1.5 py-0.5 text-xs focus:outline-none"
                bind:value={projectName}
                autofocus
                onkeydown={(e) => {
                  if (e.key === 'Enter') commitProjectRename(group.project);
                  if (e.key === 'Escape') renamingProject = null;
                }}
                onblur={() => commitProjectRename(group.project)}
              />
            {:else}
              <button class="min-w-0 flex-1 text-left text-[11px] font-semibold text-ink-soft truncate py-1.5" title={group.project.root} onclick={() => toggle(group.project.id)} ondblclick={() => startProjectRename(group.project)}>{group.project.name}</button>
            {/if}
            <span class="text-[10px] text-faint">{group.threads.length}</span>
            <button class="p-1.5 text-faint hover:text-accent opacity-0 group-hover/project:opacity-100" aria-label={`Start a new chat in ${group.project.name}`} title="Start a new chat" onclick={() => chat.newChatFor(group.project.id)}><MessageSquarePlus class="w-3.5 h-3.5" /></button>
            <button class="p-1.5 text-faint hover:text-ink opacity-0 group-hover/project:opacity-100 {projectMenuFor === group.project.id ? 'opacity-100' : ''}" aria-label={`Project actions for ${group.project.name}`} title="Project actions" onclick={(e) => { e.stopPropagation(); projectMenuAnchor=e.currentTarget; menuFor=null; projectMenuFor = projectMenuFor === group.project.id ? null : group.project.id; }}><MoreHorizontal class="w-3.5 h-3.5" /></button>
            {#if projectMenuFor === group.project.id}
              <PopupSurface open={true} anchor={projectMenuAnchor} kind="menu" onclose={() => projectMenuFor=null}>
              <div class="popup-list py-1 text-xs" role="menu" aria-label="Project actions" tabindex="-1" onclick={(e) => e.stopPropagation()} onkeydown={() => {}}>
                <button role="menuitem" class="menu-item" onclick={() => { projectMenuFor = null; void openProject(group.project); }}>Project settings</button>
              </div>
              </PopupSurface>
            {/if}
          </div>
          {#if isExpanded(group.project.id)}
            <div class="ml-3 pl-2 border-l border-line space-y-0.5">
              {#if group.threads.length === 0}<p class="text-[11px] text-faint px-2 py-1">No chats yet</p>{/if}
              {#each group.threads as thread (thread.id)}{@render threadRow(thread)}{/each}
            </div>
          {/if}
        </section>
      {/each}

      <section class="pb-1">
        <div class="group/project flex items-center gap-1 rounded-lg hover:bg-raised/50">
          <button class="p-1.5 text-faint hover:text-ink" aria-label="Toggle chats without a project" onclick={() => toggle('_loose')}>
            {#if isExpanded('_loose')}<ChevronDown class="w-3.5 h-3.5" />{:else}<ChevronRight class="w-3.5 h-3.5" />{/if}
          </button>
          <div class="min-w-0 flex-1 text-[11px] font-semibold text-ink-soft py-1.5">Without a project</div>
          <span class="text-[10px] text-faint">{looseThreads.length}</span>
          <button class="p-1.5 text-faint hover:text-accent opacity-0 group-hover/project:opacity-100" aria-label="New chat without a project" title="New chat without a project" onclick={() => chat.newChatFor(null)}><Plus class="w-3.5 h-3.5" /></button>
        </div>
        {#if isExpanded('_loose')}
          <div class="ml-3 pl-2 border-l border-line space-y-0.5">
            {#if looseThreads.length === 0}<p class="text-[11px] text-faint px-2 py-1">No chats yet</p>{/if}
            {#each looseThreads as thread (thread.id)}{@render threadRow(thread)}{/each}
          </div>
        {/if}
      </section>

    {/if}
  </div>
  <section class="shrink-0 px-2.5 pb-2 pt-1 border-t border-line bg-paper">
    <button class="w-full flex items-center gap-2 px-1.5 py-1.5 rounded-lg text-left hover:bg-raised/60" aria-expanded={archivedExpanded} onclick={() => (archivedExpanded = !archivedExpanded)}>
      {#if archivedExpanded}<ChevronDown class="w-3.5 h-3.5 text-faint" />{:else}<ChevronRight class="w-3.5 h-3.5 text-faint" />{/if}
      <Archive class="w-3.5 h-3.5 text-muted" />
      <span class="text-[11px] font-semibold text-ink-soft">Archived</span>
      <span class="ml-auto text-[10px] text-faint">{archivedThreads.length}</span>
    </button>
    {#if archivedExpanded}
      <div class="max-h-48 overflow-y-auto ml-3 pl-2 border-l border-line space-y-0.5">
        {#if archivedThreads.length === 0}<p class="text-[11px] text-faint px-2 py-1">No archived chats</p>{/if}
        {#each archivedThreads as thread (thread.id)}{@render threadRow(thread)}{/each}
      </div>
    {/if}
  </section>
</div>

<style>
  .menu-item { display: flex; align-items: center; gap: 0.5rem; width: 100%; min-height: 2rem; padding: 0.4rem 0.75rem; text-align: left; line-height: 1.3; color: var(--color-ink-soft); }
  .menu-item:hover { background: var(--color-raised); }
</style>
