<script lang="ts">
  import {
    BarChart3,
    Blocks,
    MessageCircle,
    PanelLeftClose,
    PanelLeftOpen,
    Repeat2,
    Settings,
    ShieldCheck,
  } from '@lucide/svelte';
  import { app, type View } from '$lib/stores/app.svelte';
  import { chat } from '$lib/stores/chat.svelte';
  import ThreadList from './ThreadList.svelte';

  const KEY = 'umcode.railCollapsed';
  let collapsed = $state(false);
  try { collapsed = localStorage.getItem(KEY) === '1'; } catch { /* use default */ }

  const nav: { id: View; label: string; icon: typeof MessageCircle }[] = [
    { id: 'chat', label: 'Chat', icon: MessageCircle },
    { id: 'approvals', label: 'Approvals', icon: ShieldCheck },
    { id: 'extensions', label: 'Plugin', icon: Blocks },
    { id: 'tasks', label: 'Periodic', icon: Repeat2 },
  ];

  function toggleRail() {
    collapsed = !collapsed;
    try { localStorage.setItem(KEY, collapsed ? '1' : '0'); } catch { /* storage is optional */ }
  }
</script>

<aside class="app-rail {collapsed ? 'is-collapsed' : ''}">
  <div class="rail-brand">
    <div class="brand-mark">UM</div>
    {#if !collapsed}<span class="brand-name">UMCode</span>{/if}
    <button class="icon-button ml-auto" title={collapsed ? 'Expand sidebar' : 'Collapse sidebar'} aria-label={collapsed ? 'Expand sidebar' : 'Collapse sidebar'} onclick={toggleRail}>
      {#if collapsed}<PanelLeftOpen class="w-4 h-4" />{:else}<PanelLeftClose class="w-4 h-4" />{/if}
    </button>
  </div>

  <nav class="rail-nav" aria-label="Main navigation">
    {#each nav as item}
      {@const active = app.view === item.id}
      <button class="rail-nav-item {active ? 'is-active' : ''} {collapsed ? 'justify-center px-0' : ''}" title={collapsed ? item.label : undefined} onclick={() => { if (item.id === 'chat' && app.view === 'chat') chat.newChat(); else app.view = item.id; }}>
        <item.icon class="w-[17px] h-[17px] shrink-0" strokeWidth={1.8} />
        {#if !collapsed}<span>{item.label}</span>{/if}
        {#if item.id === 'approvals' && app.approvals.length > 0}<span class="count-badge">{app.approvals.length}</span>{/if}
      </button>
    {/each}
  </nav>

  {#if !collapsed}<ThreadList />{:else}<div class="flex-1"></div>{/if}

  <div class="rail-footer">
    <div class="flex items-center gap-1">
      {#if !collapsed}
        <div class="engine-line flex-1 min-w-0 px-2">
          <span class="engine-indicator" class:is-online={app.conn === 'open'} class:is-connecting={app.conn === 'connecting'}></span>
          <span class="truncate">{app.conn === 'open' ? 'Engine online' : app.conn === 'connecting' ? 'Connecting' : 'Engine offline'}</span>
          {#if app.status && app.status.activeTurns > 0}<span class="ml-auto shrink-0">{app.status.activeTurns} running</span>{/if}
        </div>
        <button class="icon-button ml-auto" title="Usage" aria-label="Usage" onclick={() => (app.view = 'usage')}><BarChart3 class="w-4 h-4" strokeWidth={1.8} /></button>
        <button class="icon-button" title="Settings" aria-label="Settings" onclick={() => (app.view = 'settings')}><Settings class="w-4 h-4" strokeWidth={1.8} /></button>
      {/if}
    </div>
    {#if collapsed}
      <button class="footer-button justify-center" title="Usage" aria-label="Usage" onclick={() => (app.view = 'usage')}><BarChart3 class="w-4 h-4" /></button>
      <button class="footer-button justify-center" title="Settings" aria-label="Settings" onclick={() => (app.view = 'settings')}><Settings class="w-4 h-4" /></button>
    {/if}
  </div>
</aside>
