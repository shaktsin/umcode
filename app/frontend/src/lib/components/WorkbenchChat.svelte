<script lang="ts">
  import ChatColumn from './ChatColumn.svelte';
  import { app } from '$lib/stores/app.svelte';
  import { chat } from '$lib/stores/chat.svelte';
  import { projects } from '$lib/stores/projects.svelte';

  const matchesProject = $derived(!chat.main.id || !!chat.main.thread && (chat.main.thread.projectId ?? null) === projects.activeId);
  const previousProject = $derived(projects.list.find(p => p.id === chat.main.thread?.projectId)?.name ?? 'another workspace');

  async function startHere() {
    const projectId = projects.activeId;
    await chat.newChatFor(projectId);
    app.view = 'workbench';
  }
</script>

{#if matchesProject}
  <ChatColumn view={chat.main} variant="main" />
{:else}
  <section class="project-chat" aria-label="Project chat">
    <h2>Chat in {projects.active?.name ?? 'this workspace'}</h2>
    <p>Your open conversation belongs to {previousProject}. It stays available in Chat.</p>
    <button class="btn-primary" onclick={startHere}>Start a chat here</button>
    <button class="btn-outline" onclick={() => app.view = 'chat'}>View previous conversation</button>
  </section>
{/if}

<style>
  .project-chat { display:flex; flex:1; min-width:0; flex-direction:column; align-items:center; justify-content:center; gap:16px; padding:24px; text-align:center; }
  h2 { font-size:16px; font-weight:600; }
  p { font-size:13px; line-height:1.7; color:var(--color-muted); }
</style>
