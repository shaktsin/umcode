<script lang="ts">
  import Rail from '$lib/components/Rail.svelte';
  import ConnectionBanner from '$lib/components/ConnectionBanner.svelte';
  import Toasts from '$lib/components/Toasts.svelte';
  import Dialog from '$lib/components/Dialog.svelte';
  import ChatColumn from '$lib/components/ChatColumn.svelte';
  import SideChats from '$lib/components/SideChats.svelte';
  import Inspector from '$lib/components/Inspector.svelte';
  import Approvals from '$lib/panels/Approvals.svelte';
  import Tasks from '$lib/panels/Tasks.svelte';
  import Extensions from '$lib/panels/Extensions.svelte';
  import Usage from '$lib/panels/Usage.svelte';
  import Settings from '$lib/panels/Settings.svelte';
  import ProjectSettings from '$lib/panels/ProjectSettings.svelte';
  import { app } from '$lib/stores/app.svelte';
  import { chat } from '$lib/stores/chat.svelte';
  import { inspector } from '$lib/stores/inspector.svelte';

  // Column widths are per project, and only a convenience, so a browser that
  // refuses storage simply gets the defaults.
  const KEY = 'umcode.columns';
  let sideWidth = $state(380);
  let inspectorWidth = $state(460);
  let narrow = $state(false);

  try {
    const saved = JSON.parse(localStorage.getItem(KEY) || '{}');
    if (saved.side) sideWidth = saved.side;
    if (saved.inspector) inspectorWidth = saved.inspector;
  } catch {
    /* defaults are fine */
  }

  function persist() {
    try {
      localStorage.setItem(KEY, JSON.stringify({ side: sideWidth, inspector: inspectorWidth }));
    } catch {
      /* ignore */
    }
  }

  function drag(which: 'side' | 'inspector', e: PointerEvent) {
    e.preventDefault();
    const startX = e.clientX;
    const start = which === 'side' ? sideWidth : inspectorWidth;
    const move = (ev: PointerEvent) => {
      const delta = which === 'side' ? startX - ev.clientX : startX - ev.clientX;
      const next = Math.min(720, Math.max(280, start + delta));
      if (which === 'side') sideWidth = next;
      else inspectorWidth = next;
    };
    const up = () => {
      window.removeEventListener('pointermove', move);
      window.removeEventListener('pointerup', up);
      persist();
    };
    window.addEventListener('pointermove', move);
    window.addEventListener('pointerup', up);
  }

  function onResize() {
    narrow = window.innerWidth < 1100;
  }

  function onKey(e: KeyboardEvent) {
    const mod = e.metaKey || e.ctrlKey;
    if (!mod) return;
    if (e.key === 'n') {
      e.preventDefault();
      chat.newChat();
    } else if (e.key === ',') {
      e.preventDefault();
      app.view = 'settings';
    } else if (e.key === 'k') {
      e.preventDefault();
      app.view = 'chat';
      document.getElementById('thread-search')?.focus();
    } else if (e.key === 'w') {
      if (inspector.open) {
        e.preventDefault();
        inspector.close(inspector.activeId!);
      } else if (chat.activeSideId) {
        e.preventDefault();
        chat.closeSide(chat.activeSideId);
      }
    }
  }

  const showChat = $derived(app.view === 'chat');
</script>

<svelte:window onkeydown={onKey} onresize={onResize} />

<div class="flex h-full overflow-hidden">
  <Rail />
  <div class="flex flex-col flex-1 min-w-0">
    <ConnectionBanner />
    <div class="flex-1 min-h-0 flex">
      <!-- Column 1: the conversation, or a full-width panel -->
      <div class="flex-1 min-w-0 flex flex-col">
        {#if showChat}
          <ChatColumn view={chat.main} variant="main" />
        {:else}
          <main class="flex-1 overflow-y-auto">
            <div class="max-w-5xl mx-auto px-5 py-5">
              {#if app.view === 'approvals'}<Approvals />
              {:else if app.view === 'tasks'}<Tasks />
              {:else if app.view === 'extensions'}<Extensions />
              {:else if app.view === 'usage'}<Usage />
              {:else if app.view === 'project'}<ProjectSettings />
              {:else if app.view === 'settings'}<Settings />
              {/if}
            </div>
          </main>
        {/if}
      </div>

      <!-- Column 2: side chats -->
      {#if showChat && chat.sides.length > 0}
        <div
          class="shrink-0 border-l border-line flex"
          style="width: {narrow && inspector.open ? 320 : sideWidth}px"
        >
          <button
            class="w-1 cursor-col-resize hover:bg-clay/40 shrink-0"
            aria-label="Resize side chats"
            onpointerdown={(e) => drag('side', e)}
          ></button>
          <div class="flex-1 min-w-0"><SideChats /></div>
        </div>
      {/if}

      <!-- Column 3: the inspector -->
      {#if inspector.open}
        <div
          class="shrink-0 border-l border-line flex {narrow ? 'absolute right-0 top-12 bottom-0 z-30 shadow-2xl bg-paper' : ''}"
          style="width: {inspectorWidth}px"
        >
          <button
            class="w-1 cursor-col-resize hover:bg-clay/40 shrink-0"
            aria-label="Resize inspector"
            onpointerdown={(e) => drag('inspector', e)}
          ></button>
          <div class="flex-1 min-w-0"><Inspector /></div>
        </div>
      {/if}
    </div>
  </div>
</div>
<Toasts />
<Dialog />
