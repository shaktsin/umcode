<script lang="ts">
  import { tick } from 'svelte';
  import { resolveShellLayout, type Auxiliary } from '$lib/ui/shellLayout';
  import { modalFocus } from '$lib/ui/modalFocus';
  import Rail from '$lib/components/Rail.svelte';
  import ConnectionBanner from '$lib/components/ConnectionBanner.svelte';
  import Splash from '$lib/components/Splash.svelte';
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
  let viewportWidth = $state(window.innerWidth);
  let viewportHeight = $state(window.innerHeight);
  let railCollapsedPreference = $state(false);
  try { railCollapsedPreference = localStorage.getItem('umcode.railCollapsed') === '1'; } catch { /* optional storage */ }
  let activeAuxiliary = $state<Auxiliary | null>(null);
  const layout = $derived(resolveShellLayout({width:viewportWidth,height:viewportHeight,railCollapsedPreference,sideRequested:chat.sides.length>0,inspectorRequested:inspector.open,sidePreferredWidth:sideWidth,inspectorPreferredWidth:inspectorWidth,activeAuxiliary}));
  $effect(() => { void chat.activeSideId; if(chat.activeSideId) activeAuxiliary='side'; });
  $effect(() => { void inspector.activeId; if(inspector.activeId) activeAuxiliary='inspector'; });
  $effect(() => { try { localStorage.setItem('umcode.railCollapsed',railCollapsedPreference?'1':'0'); } catch { /* optional */ } });
  function showPanel(panel:Auxiliary) { activeAuxiliary=panel; void tick().then(()=>document.querySelector<HTMLElement>(`[data-panel="${panel}"]`)?.focus()); }

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
    viewportWidth = window.innerWidth;
    viewportHeight = window.innerHeight;
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

{#if !app.booted}
  <Splash />
{:else}
<div class="flex h-full overflow-hidden">
  <Rail bind:collapsedPreference={railCollapsedPreference} autoCollapsed={viewportWidth < 960} />
  <div class="flex flex-col flex-1 min-w-0">
    <ConnectionBanner />
    {#if showChat && (chat.sides.length || inspector.open)}
      <div class="panel-switcher">
        {#if chat.sides.length}<button class="btn-ghost btn-sm" aria-label="Show side chats" onclick={() => showPanel('side')}>Side chats <span class="text-faint">{chat.sides.length}</span></button>{/if}
        {#if inspector.open}<button class="btn-ghost btn-sm" aria-label="Show inspector" onclick={() => showPanel('inspector')}>Inspector</button>{/if}
      </div>
    {/if}
    <div class="flex-1 min-h-0 flex relative">
      <!-- Column 1: the conversation, or a full-width panel -->
      <div class="flex-1 min-w-0 flex flex-col" data-main-region>
        {#if showChat}
          <ChatColumn view={chat.main} variant="main" />
        {:else}
          <main class="flex-1 overflow-y-auto">
            <div class="page-content max-w-5xl mx-auto">
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

      {#each ['side','inspector'] as name}
        {@const panel = name as Auxiliary}
        {@const requested = panel === 'side' ? showChat && chat.sides.length > 0 : inspector.open}
        {@const inlineWidth = layout.inline[panel]}
        {@const overlaid = layout.overlay?.panel === panel && (panel !== 'side' || showChat)}
        {#if requested}
          {#if overlaid}<div class="panel-backdrop" role="presentation" onclick={() => activeAuxiliary = null}></div>{/if}
          <div data-panel={panel} hidden={inlineWidth === null && !overlaid} class="auxiliary-panel {overlaid ? 'is-overlay' : ''}" style:width={`${overlaid ? layout.overlay!.width : (inlineWidth ?? 280) + 1}px`} role={overlaid ? 'dialog' : 'region'} aria-modal={overlaid ? 'true' : undefined} aria-label={panel === 'side' ? 'Side chats panel' : 'Inspector panel'} tabindex="-1" use:modalFocus={overlaid ? () => activeAuxiliary = null : undefined}>
            {#if !overlaid}<button class="panel-divider" aria-label={panel === 'side' ? 'Resize side chats' : 'Resize inspector'} onpointerdown={(e) => drag(panel,e)}></button>{/if}
            <div class="flex-1 min-w-0 min-h-0 flex flex-col">
              {#if overlaid}<div class="overlay-heading"><span>{panel === 'side' ? 'Side chats' : 'Inspector'}</span><button class="icon-button compact" aria-label="Close panel" onclick={() => activeAuxiliary = null}>✕</button></div>{/if}
              <div class="flex-1 min-h-0">{#if panel === 'side'}<SideChats />{:else}<Inspector />{/if}</div>
            </div>
          </div>
        {/if}
      {/each}
    </div>
  </div>
</div>
{/if}
<Toasts />
<Dialog />
