<script lang="ts">
  import { tick } from 'svelte';
  import { app } from '$lib/stores/app.svelte';
  import { workbench } from '$lib/workbench/store.svelte';
  import { workbenchAssistant } from '$lib/workbench/assistantStore.svelte';
  import { previewProposal, type WorkbenchProposal } from '$lib/workbench/assistant';
  import { shapeLabel } from '$lib/workbench/graph';

  let { proposal }: { proposal: WorkbenchProposal } = $props();
  let error = $state('');
  let opening = $state(false);
  const matches = $derived(workbenchAssistant.matches(proposal));
  const applied = $derived(workbenchAssistant.wasApplied(proposal));
  const details = $derived.by(() => {
    if (!matches) return null;
    try { return { preview: previewProposal(workbench.draft, proposal), error: '' }; }
    catch (error) { return { preview: null, error: (error as Error).message }; }
  });
  async function show() {
    error = ''; opening = true;
    try {
      app.view = 'workbench'; workbench.tab = 'Build'; await tick();
      await workbenchAssistant.showProposal(proposal); await tick();
      document.getElementById('workbench-preview-heading')?.focus();
    } catch (reason) { error = (reason as Error).message; }
    finally { opening = false; }
  }
</script>

<section class="proposal" aria-label="Suggested model changes">
  <div class="proposal-heading"><strong>Suggested model changes</strong><span>{proposal.modelName}</span></div>
  <p>{proposal.explanation}</p>
  {#if details?.preview}
    {@const p = details.preview}
    <ul>{#each p.changes as change}<li>{change}</li>{/each}</ul>
    <p class="parameter-change">{p.before.parameters.toLocaleString()} → {p.after.parameters.toLocaleString()} parameters</p>
    <details><summary>Shapes and validation</summary>
      {#each p.affectedNodeIds as id}<p class="shape-change"><strong>{id}</strong><br />{shapeLabel(p.before.shapes[id])} → {shapeLabel(p.after.shapes[id])}</p>{/each}
      <p>{p.before.errors.length} → {p.after.errors.length} issues</p>
      {#if p.after.errors.length}<ul class="proposal-warning">{#each p.after.errors as issue}<li>{issue.message}</li>{/each}</ul>{:else}<p>Layer sizes match.</p>{/if}
    </details>
  {:else if details?.error}<p class="proposal-error" role="status">This suggestion cannot be applied: {details.error}</p>
  {:else if applied}<p class="proposal-status">Applied earlier. Undo can restore the previous draft.</p>
  {:else if !workbenchAssistant.current || workbenchAssistant.current.modelId !== proposal.modelId || workbenchAssistant.current.projectId !== proposal.projectId}<p class="proposal-warning">Open {proposal.modelName} in its project to review this suggestion.</p>
  {:else if !workbenchAssistant.revision}<p class="proposal-status">Checking the current model…</p>
  {:else}<p class="proposal-warning">Model changed since this suggestion. Ask for an updated suggestion.</p>{/if}
  {#if error}<p class="proposal-error" role="alert">{error}</p>{/if}
  <button class="btn-outline btn-sm" disabled={!matches || !details?.preview || opening || workbench.busy || !workbench.connected} onclick={() => void show()}>{opening ? 'Opening preview…' : 'Preview changes'}</button>
  <p class="proposal-status">Preview first. Apply changes updates your draft; Save model keeps it.</p>
</section>

<style>
  .proposal { border:1px solid var(--color-line); border-radius:9px; background:var(--color-surface); padding:12px; font-size:11px; line-height:1.65; overflow-wrap:anywhere; }
  .proposal-heading { display:flex; gap:8px; flex-wrap:wrap; margin-bottom:6px; } .proposal-heading span, .proposal-status { color:var(--color-muted); }
  p, ul, details { margin:8px 0; } ul { list-style:disc; padding-left:17px; }
  summary { cursor:pointer; } .shape-change { font:10px/1.7 var(--font-mono); }
  .parameter-change { font-weight:600; } .proposal-warning { color:var(--color-amber-warm); } .proposal-error { color:var(--color-rust); }
  .proposal button { margin-top:4px; } .proposal-status { font-size:10px; }
</style>
