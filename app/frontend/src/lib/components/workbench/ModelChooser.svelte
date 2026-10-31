<script lang="ts">
  import { ChevronRight, Layers3, Plus } from '@lucide/svelte';
  import { describeWorkbenchTask, type WorkbenchModel } from '$lib/workbench/types';
  let { models, projectName, disabled, hasDraft, onopen, oncreate }: { models: WorkbenchModel[]; projectName: string; disabled: boolean; hasDraft: (id: string) => boolean; onopen: (id: string) => void; oncreate: () => void } = $props();
</script>

<section class="model-overview" aria-label="Project models">
  <header><div><h2 id="project-models-heading" tabindex="-1">Your models</h2><p>Models in {projectName}. Choose one to continue editing.</p></div><button class="btn-primary btn-sm" disabled={disabled} onclick={oncreate}><Plus size={14} />Create a model</button></header>
  {#if models.length}
    <ul class="model-list">
      {#each models as model (model.id)}
        <li><button class="model-choice" aria-label={`Open ${model.name}`} disabled={disabled} onclick={() => onopen(model.id)}><Layers3 size={18} /><span class="model-info"><strong>{model.name}</strong><span>{describeWorkbenchTask(model.task)}</span></span>{#if hasDraft(model.id)}<span class="draft-badge">Unsaved changes</span>{/if}<ChevronRight size={15} /></button></li>
      {/each}
    </ul>
    {#if models.some(model => hasDraft(model.id))}<p class="draft-note" role="status">Unsaved edits are kept in this session. Reopen the model and save it to keep them on disk.</p>{/if}
  {:else}
    <div class="empty-models"><Layers3 size={30} /><h3>No models in this project yet</h3><p>Create a model to start building.</p></div>
  {/if}
</section>

<style>
  .model-overview { flex:1; min-height:0; overflow-y:auto; padding:24px; }
  header { display:flex; align-items:center; flex-wrap:wrap; gap:16px; margin-bottom:24px; }
  header > div { flex:1; min-width:180px; } h2 { font-size:20px; font-weight:650; } h3 { font-size:15px; font-weight:600; }
  p { color:var(--color-muted); font-size:12px; line-height:1.7; margin-top:7px; }
  .model-list { display:grid; grid-template-columns:repeat(auto-fit,minmax(230px,1fr)); gap:12px; }
  .model-choice { display:flex; align-items:center; gap:12px; width:100%; text-align:left; padding:16px; background:var(--color-surface); border:1px solid var(--color-line); border-radius:10px; }
  .model-choice:hover:not(:disabled) { border-color:var(--color-accent); background:var(--color-accent-soft); }
  .model-choice:focus-visible { outline:2px solid var(--color-accent); outline-offset:2px; }
  .model-choice :global(svg) { color:var(--color-muted); flex-shrink:0; }
  .model-info { display:flex; flex:1; min-width:0; flex-direction:column; gap:5px; }
  .model-info strong { color:var(--color-ink); font-size:13px; overflow-wrap:anywhere; } .model-info > span { font-size:11px; color:var(--color-muted); }
  .draft-badge { font-size:10px; color:var(--color-amber-warm); max-width:70px; }
  .draft-note { margin-top:18px; }
  .empty-models { display:flex; flex-direction:column; align-items:center; justify-content:center; gap:12px; padding:60px 20px; text-align:center; color:var(--color-muted); }
</style>
