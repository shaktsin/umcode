<script lang="ts">
  import { dialog } from '$lib/stores/dialog.svelte';
  import { app } from '$lib/stores/app.svelte';

  async function chooseProjectFolder() {
    if (app.shell === 'browser') {
      app.toast('error', 'Use the folder picker in the UMCode desktop app, or enter the full folder path.');
      return;
    }
    try {
      const response = await fetch('/__umcode/shell/chooseFolder', { method: 'POST' });
      const result = await response.json();
      if (!response.ok) throw new Error(result.error || `HTTP ${response.status}`);
      if (dialog.current?.kind === 'project') dialog.current.path = result.path;
    } catch (error) {
      app.toast('error', error instanceof Error ? error.message : 'Could not open the folder picker.');
    }
  }

  function onKey(e: KeyboardEvent) {
    if (!dialog.current) return;
    if (e.key === 'Escape') dialog.close(false);
    if (e.key === 'Enter' && !e.shiftKey) {
      if (dialog.current.kind === 'project' && (!dialog.current.value.trim() || !dialog.current.path.trim())) return;
      e.preventDefault();
      dialog.close(true);
    }
  }
</script>

<svelte:window onkeydown={onKey} />

{#if dialog.current}
  {@const d = dialog.current}
  <div class="fixed inset-0 z-40 bg-ink/30 flex items-center justify-center p-6" role="presentation" onclick={() => dialog.close(false)}>
    <div class="card w-full max-w-md p-5 shadow-2xl" role="dialog" aria-modal="true" tabindex="-1" onclick={(e) => e.stopPropagation()} onkeydown={() => {}}>
      <p class="text-sm text-ink whitespace-pre-wrap">{d.message}</p>
      {#if d.kind === 'prompt'}
        <!-- svelte-ignore a11y_autofocus -->
        <input class="input mt-3 font-mono" type={d.secret ? 'password' : 'text'} autocomplete="off" bind:value={d.value} autofocus />
      {:else if d.kind === 'project'}
        <div class="space-y-3 mt-3 text-sm">
          <label class="block">
            <span class="label">Project name</span>
            <!-- svelte-ignore a11y_autofocus -->
            <input class="input mt-1" bind:value={d.value} placeholder="My project" autofocus />
          </label>
          <label class="block">
            <span class="label">Project folder</span>
            <div class="flex gap-2 mt-1">
              <input class="input min-w-0 flex-1 font-mono text-xs" bind:value={d.path} disabled={!d.pathEditable} placeholder="Choose or enter a folder path" />
              <button class="btn-outline shrink-0" disabled={!d.pathEditable} onclick={chooseProjectFolder}>Choose folder…</button>
            </div>
            {#if d.path}<span class="block mt-1 text-[11px] text-muted truncate" title={d.path}>{d.path}</span>{/if}
            {#if !d.pathEditable}
              <span class="block mt-1 text-[11px] text-muted">Folder changes are locked because existing chats use isolated workspaces based on this folder.</span>
            {/if}
          </label>
        </div>
      {/if}
      <div class="flex justify-end gap-2 mt-4">
        <button class="btn-ghost" onclick={() => dialog.close(false)}>Cancel</button>
        <!-- svelte-ignore a11y_autofocus -->
        <button class={d.danger ? 'btn bg-rust hover:brightness-110 text-white' : 'btn-primary'} disabled={d.kind === 'project' && (!d.value.trim() || !d.path.trim())} autofocus={d.kind === 'confirm'} onclick={() => dialog.close(true)}>{d.okLabel}</button>
      </div>
    </div>
  </div>
{/if}
