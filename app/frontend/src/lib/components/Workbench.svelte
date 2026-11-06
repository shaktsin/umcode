<script lang="ts">
  import { tick, untrack } from 'svelte';
  import { ArrowLeft, Layers3, FolderOpen, MessageCircle, Plus, RefreshCw, Trash2 } from '@lucide/svelte';
  import VisualBuilder from './workbench/VisualBuilder.svelte';
  import DataTab from './workbench/DataTab.svelte';
  import ImportedRuntimeTab from './workbench/ImportedRuntimeTab.svelte';
  import InferenceTab from './workbench/InferenceTab.svelte';
  import TrainTab from './workbench/TrainTab.svelte';
  import ImportedBuilder from './workbench/ImportedBuilder.svelte';
  import ModelImportDialog from './workbench/ModelImportDialog.svelte';
  import { isImportedGraph } from '$lib/workbench/modelImport';
  import type { OpenWorkbenchModel } from '$lib/workbench/types';
  import ModelChooser from './workbench/ModelChooser.svelte';
  import { inputModalities, modelGraph, relevantStarterPresets, type InputModality, type StartingPoint } from '$lib/workbench/presets';
  import { app } from '$lib/stores/app.svelte';
  import { projects } from '$lib/stores/projects.svelte';
  import { dialog } from '$lib/stores/dialog.svelte';
  import { createProject } from '$lib/createProject';
  import { workbench } from '$lib/workbench/store.svelte';
  import { workbenchAssistant } from '$lib/workbench/assistantStore.svelte';
  import { describeWorkbenchTask, workbenchTasks, type WorkbenchTask } from '$lib/workbench/types';
  import type { WorkbenchTab } from '$lib/workbench/state.svelte';
  import { workbenchDataDrafts } from '$lib/workbench/data.svelte';

  let { chatVisible = false, onToggleChat = () => {}, onOpenChat = () => {} }: { chatVisible?: boolean; onToggleChat?: () => void; onOpenChat?: () => void } = $props();
  $effect(() => {
    void workbench.projectId; void workbench.selected?.model.id; void workbench.draft;
    untrack(() => { void workbenchAssistant.track().catch(() => {}); });
  });
  let importOpen = $state(false);
  $effect(() => { void workbench.projectId; importOpen = false; });
  const imported = $derived(isImportedGraph(workbench.draft));
  function importedModel(projectId: string, bundle: OpenWorkbenchModel) { if (workbench.acceptImport(projectId, bundle)) { importOpen = false; screen = 'auto'; } }
  let pendingInferenceCheckpointId = $state('');
  let pendingInferenceContext = $state('');
  const inferenceContext = $derived(JSON.stringify([workbench.projectId, workbench.selected?.model.id, workbench.selected?.model.createdAt]));
  $effect(() => { if (workbench.tab !== 'Inference') pendingInferenceCheckpointId = ''; });
  let task = $state<WorkbenchTask>('text-generation');
  let name = $state('');
  let inputType = $state<InputModality>('text');
  let starting = $state<StartingPoint>('text');
  let screen = $state<'auto' | 'models' | 'create'>('auto');
  $effect(() => { void workbench.projectId; screen = 'auto'; });
  const showModels = $derived(screen !== 'create');
  $effect(() => { if (task === 'text-generation') inputType = 'text'; });
  const starters = $derived(relevantStarterPresets(task, inputType));
  $effect(() => { starting = task === 'custom' ? 'blank' : starters.find(s => s.id !== 'blank')?.id ?? 'blank'; });
  const tabs: WorkbenchTab[] = ['Data', 'Build', 'Train', 'Inference'];

  async function closeModel() {
    const project = workbench.projectId;
    workbench.newModel(); screen = 'models';
    await tick();
    if (workbench.projectId === project && !workbench.selected && screen === 'models') document.getElementById('project-models-heading')?.focus();
  }
  function startCreate() { workbench.newModel(); screen = 'create'; }

  async function deleteModel(modelId: string) {
    const projectId = workbench.projectId;
    const model = workbench.models.find(item => item.id === modelId);
    if (!projectId || !model || !workbench.connected || workbench.busy) return;
    const message = `Delete “${model.name}”?\n\nSaved model files, imported datasets, and checkpoints will move to this project's models/.trash/ folder for recovery. Any unsaved model or Data-tab changes will be discarded.`;
    if (!await dialog.confirm(message, { okLabel: 'Delete model', danger: true })) return;
    if (workbench.projectId !== projectId || workbench.models.find(item => item.id === modelId)?.createdAt !== model.createdAt) return;
    if (await workbench.deleteModel(modelId)) {
      if (workbench.projectId === projectId && !workbench.selected) screen = 'models';
      await tick(); // Let the deleted model's Data tab dispose before clearing its cache.
      workbenchDataDrafts.delete(JSON.stringify([projectId, modelId, model.createdAt]));
      await app.refreshCatalog().catch(() => {});
    }
  }

  async function create() {
    const project = workbench.projectId, preset = starting, input = inputType;
    const id = await workbench.create(name, task);
    if (id && workbench.projectId === project && workbench.selected?.model.id === id) {
      name = '';
      workbench.draft = JSON.stringify(modelGraph(preset, input));
      await workbench.save();
    }
  }
  async function reload() {
    const project = workbench.projectId, model = workbench.selected?.model.id;
    if (workbench.dirty && !await dialog.confirm('Discard your unsaved model changes and reload from disk?', { okLabel: 'Discard and reload', danger: true })) return;
    if (workbench.projectId === project && workbench.selected?.model.id === model) await workbench.reload();
  }
</script>

<main class="workbench" data-workbench aria-label="ML Workbench">
  <header class="workbench-heading">
    <button class="btn-ghost btn-sm" aria-label={chatVisible ? 'Hide workbench chat' : 'Show workbench chat'} aria-expanded={chatVisible} onclick={onToggleChat}><MessageCircle size={16} />Assistant</button>
    <div class="workbench-title"><Layers3 size={17} /><h1>ML Workbench</h1><span class="preview-badge">Visual builder</span></div>
    <div class="workbench-pickers">
      <label class="sr-only" for="workbench-project">Workbench project</label>
      <select id="workbench-project" class="select" value={projects.activeId ?? ''} onchange={(e) => void projects.open(e.currentTarget.value || null)}>
        <option value="">Choose a project</option>
        {#each projects.list as project (project.id)}<option value={project.id}>{project.name}</option>{/each}
      </select>
      <button class="icon-button" aria-label="Open a project folder" title="Open a project folder" onclick={() => void createProject()}><FolderOpen size={16} /></button>
    </div>
  </header>

  {#if !workbench.projectId}
    <section class="workbench-empty">
      <FolderOpen size={30} strokeWidth={1.4} />
      <h2>Your models live with your project</h2>
      <p>Choose a project above or open a folder to create your first model. Model bundles are saved in its <code>models/</code> folder.</p>
      <button class="btn-primary" onclick={() => void createProject()}>Open a project</button>
    </section>
  {:else}
    <div class="workbench-toolbar">
      {#if workbench.selected || !showModels}<button class="btn-ghost btn-sm" onclick={() => void closeModel()}><ArrowLeft size={14} />Back to models</button>{/if}
      <label class="sr-only" for="workbench-model">Project model</label>
      <select id="workbench-model" class="select model-picker" value={workbench.selected?.model.id ?? ''} disabled={!workbench.connected || workbench.loading || workbench.busy} onchange={(e) => e.currentTarget.value ? void workbench.open(e.currentTarget.value) : void closeModel()}>
        <option value="">Choose a model</option>
        {#each workbench.models as model (model.id)}<option value={model.id}>{model.name}</option>{/each}
      </select>
      <button class="icon-button" aria-label="Refresh project models" disabled={!workbench.connected || workbench.busy || workbench.loading} onclick={() => void workbench.refresh()}><RefreshCw size={14} /></button>
      {#if workbench.selected}<span class="task-badge">{describeWorkbenchTask(workbench.selected.model.task)}</span>{/if}
      {#if workbench.selected}<button class="btn-ghost btn-sm delete-model" disabled={!workbench.connected || workbench.busy || workbench.loading} onclick={() => void deleteModel(workbench.selected!.model.id)}><Trash2 size={14} />Delete model</button>{/if}
      <div class="model-actions" role="group" aria-label="Add a model">
        <button class="btn-outline btn-sm" disabled={!workbench.connected || workbench.busy || workbench.loading} onclick={() => importOpen = true}><FolderOpen size={14} />Import model</button>
        <button class="btn-primary btn-sm" disabled={!workbench.connected || workbench.busy || workbench.loading} onclick={startCreate}><Plus size={14} />New model</button>
      </div>
    </div>
    {#if workbench.error}<div class="workbench-feedback is-error" role="alert">{workbench.error}</div>{/if}
    {#if workbench.notice}<div class="workbench-feedback" role="status">{workbench.notice}</div>{/if}
    {#if !workbench.connected}<div class="workbench-feedback" role="status">Engine disconnected. Your unsaved edits are kept here; saving will be available when it reconnects.</div>{/if}

    {#if workbench.loading}
      <section class="workbench-empty" aria-live="polite"><p>Loading project models…</p></section>
    {:else if !workbench.selected && showModels}
      <ModelChooser models={workbench.models} projectName={projects.active?.name ?? 'this project'} disabled={!workbench.connected || workbench.busy} hasDraft={(id) => workbench.hasModelDraft(id)} onopen={(id) => void workbench.open(id)} ondelete={(id) => void deleteModel(id)} />
    {:else if !workbench.selected}
      <form class="new-model-layout" onsubmit={(e) => { e.preventDefault(); void create(); }}>
        <section class="task-selection">
          <p class="eyebrow">Step 1 · Choose the task</p>
          <h2>What does this model do?</h2>
          <p class="intro">Choose once. The task is saved with the model and will guide Build, Train, and Inference.</p>
          <div class="task-cards" role="group" aria-label="Model task">
            {#each workbenchTasks as item (item.id)}
              <button type="button" class="task-card" class:is-selected={task === item.id} aria-pressed={task === item.id} onclick={() => task = item.id}>
                <span class="task-card-title">{item.name}{#if task === item.id}<span class="chosen">Selected</span>{/if}</span>
                <span class="task-description">{item.description}</span>
                <span class="task-types"><code>{item.input}</code><span aria-hidden="true">→</span><code>{item.output}</code></span>
                <span class="task-example">{item.id === 'text-generation' ? 'For example: a model for customer support responses.' : item.id === 'classification' ? 'For example: classify tickets by topic or images by category.' : 'Choose text, images, or feature vectors and build from a blank architecture.'}</span>
              </button>
            {/each}
          </div>
          <p class="eyebrow setup-label">What this sets up</p>
          <div class="setup-cards">
            <div><h3>Build</h3><p>Drag layers onto the canvas, connect them, and adjust their settings. Export your PyTorch model.</p></div>
            <div><h3>Train</h3><p>Train numeric and text models locally on CPU, track real evaluation metrics, and save checkpoints for Inference.</p></div>
            <div><h3>Inference</h3><p>A model configured for {task === 'custom' ? 'your custom task' : describeWorkbenchTask(task).toLowerCase()}. Train locally or import weights, then try learned predictions.</p></div>
          </div>
        </section>
        <aside class="model-setup">
          <p class="eyebrow">Step 2 · Set up</p>
          <label for="workbench-name">Model name</label>
          <input id="workbench-name" class="input" bind:value={name} placeholder="e.g. customer-support" maxlength="100" required />
          <label class="field-label" for="model-input-type">Input type</label>
          <select id="model-input-type" class="select" bind:value={inputType}>
            {#each inputModalities as input (input.id)}<option value={input.id} disabled={task === 'text-generation' && input.id !== 'text'}>{input.name}</option>{/each}
          </select>
          <label class="field-label" for="model-starting-point">Starting point</label>
          <select id="model-starting-point" class="select" bind:value={starting}>
            {#each starters as starter (starter.id)}<option value={starter.id}>{starter.name}</option>{/each}
          </select>
          <p class="setup-note">Starters include connected, editable layers. Weights start from scratch; no pretrained model is downloaded.</p>
          <div class="storage-note"><FolderOpen size={16} /><p>Saved inside <strong>{projects.active?.name}</strong><br /><code>models/&lt;model-name&gt;/</code></p></div>
          <p class="setup-note">The task cannot be changed after creation. Create a new model to use a different task.</p>
          <button type="submit" class="btn-primary create-model" disabled={!workbench.connected || workbench.busy || !name.trim()}>{workbench.busy ? 'Creating…' : 'Create model'}</button>
        </aside>
      </form>
    {:else}
      <nav class="workbench-tabs" aria-label="Model sections">
        {#each tabs as tab}<button class="btn-ghost btn-sm" class:is-active={workbench.tab === tab} aria-current={workbench.tab === tab ? 'page' : undefined} onclick={() => workbench.tab = tab}>{tab}</button>{/each}
        <span class="graph-state" class:is-dirty={workbench.dirty}>{workbench.dirty ? 'Unsaved changes' : 'Saved in project'}</span>
      </nav>
      {#if workbench.tab === 'Build'}
        {#key `${workbench.projectId}/${workbench.selected.model.id}/${workbench.selected.model.createdAt}`}
          {#if imported}
          <ImportedBuilder text={workbench.draft} onchange={(text) => workbench.draft = text} onSave={() => void workbench.save()} onReload={() => void reload()} onAsk={onOpenChat} disabled={!workbench.connected || workbench.busy} dirty={workbench.dirty} task={workbench.selected.model.task} name={workbench.selected.model.name} />
          {:else}
          <VisualBuilder text={workbench.draft} onchange={(text) => workbench.draft = text} onSave={() => void workbench.save()} onReload={() => void reload()} onAsk={onOpenChat} disabled={!workbench.connected || workbench.busy} dirty={workbench.dirty} task={workbench.selected.model.task} name={workbench.selected.model.name} />
          {/if}
        {/key}
      {:else if imported}
        {#key `${workbench.projectId}/${workbench.selected.model.id}/${workbench.selected.model.createdAt}`}
          <ImportedRuntimeTab projectId={workbench.projectId} modelId={workbench.selected.model.id} modelCreatedAt={workbench.selected.model.createdAt} tab={workbench.tab} task={workbench.selected.model.task} graphText={workbench.draft} connected={workbench.connected} dirty={workbench.dirty} onBuild={() => workbench.tab = 'Build'} onData={() => workbench.tab = 'Data'} onInference={() => workbench.tab = 'Inference'} />
        {/key}
      {:else if workbench.tab === 'Data'}
        {#key `${workbench.projectId}/${workbench.selected.model.id}/${workbench.selected.model.createdAt}`}
          <DataTab projectId={workbench.projectId} modelId={workbench.selected.model.id} modelCreatedAt={workbench.selected.model.createdAt} task={workbench.selected.model.task} graphText={workbench.draft} connected={workbench.connected} onBuild={() => workbench.tab = 'Build'} />
        {/key}
      {:else if workbench.tab === 'Train'}
        {#key `${workbench.projectId}/${workbench.selected.model.id}/${workbench.selected.model.createdAt}`}
          <TrainTab projectId={workbench.projectId} modelId={workbench.selected.model.id} modelCreatedAt={workbench.selected.model.createdAt} name={workbench.selected.model.name} connected={workbench.connected} dirty={workbench.dirty} onBuild={() => workbench.tab = 'Build'} onData={() => workbench.tab = 'Data'} onInference={(checkpointId) => { pendingInferenceCheckpointId = checkpointId; pendingInferenceContext = inferenceContext; workbench.tab = 'Inference'; }} />
        {/key}
      {:else if workbench.tab === 'Inference'}
        {#key `${workbench.projectId}/${workbench.selected.model.id}/${workbench.selected.model.createdAt}`}
          <InferenceTab initialCheckpointId={pendingInferenceContext === inferenceContext ? pendingInferenceCheckpointId : ''} projectId={workbench.projectId} modelId={workbench.selected.model.id} modelCreatedAt={workbench.selected.model.createdAt} name={workbench.selected.model.name} connected={workbench.connected} dirty={workbench.dirty} onBuild={() => workbench.tab = 'Build'} onData={() => workbench.tab = 'Data'} />
        {/key}
      {/if}
    {/if}
  {/if}
  {#if importOpen && workbench.projectId}{@const importProject = workbench.projectId}{#key importProject}<ModelImportDialog projectId={importProject} connected={workbench.connected} onClose={() => importOpen = false} onImported={(bundle) => importedModel(importProject, bundle)} />{/key}{/if}
</main>

<style>
  .workbench { display:flex; flex:1; flex-direction:column; min-width:0; min-height:0; background:var(--color-paper); }
  .delete-model { color:var(--color-rust); }
  .workbench-heading, .workbench-toolbar, .workbench-tabs { display:flex; flex-wrap:wrap; align-items:center; gap:8px; border-bottom:1px solid var(--color-line); flex-shrink:0; padding:10px 16px; }
  .workbench-heading { min-height:48px; }
  .workbench-title { display:flex; align-items:center; gap:8px; flex-wrap:wrap; }
  h1 { font-size:14px; font-weight:650; }
  h2 { font-size:20px; font-weight:650; line-height:1.4; }
  h3 { font-weight:620; font-size:13px; margin-bottom:6px; }
  .preview-badge { font-size:10px; color:var(--color-muted); border:1px solid var(--color-line); border-radius:5px; padding:2px 5px; }
  .workbench-pickers { display:flex; align-items:center; gap:4px; margin-left:auto; min-width:0; }
  .workbench-pickers select { width:170px; max-width:100%; }
  .workbench-toolbar { padding-block:8px; }
  .model-picker { width:190px; min-width:0; }
  .task-badge { font-size:11px; font-weight:550; background:var(--color-accent-soft); color:var(--color-accent-strong); padding:4px 8px; border-radius:6px; }
  .model-actions { display:flex; align-items:center; gap:8px; margin-left:auto; flex-shrink:0; }
  .workbench-feedback { font-size:12px; padding:8px 16px; background:var(--color-surface); border-bottom:1px solid var(--color-line); overflow-wrap:anywhere; }
  .workbench-feedback.is-error { color:var(--color-rust); }
  .new-model-layout { flex:1; display:flex; min-height:0; overflow-y:auto; }
  .task-selection { flex:1; padding:24px; min-width:0; }
  .eyebrow { color:var(--color-muted); text-transform:uppercase; letter-spacing:.045em; font-size:10px; font-weight:650; margin-bottom:12px; }
  .intro { color:var(--color-muted); font-size:12px; line-height:1.6; margin:6px 0 20px; }
  .task-cards, .setup-cards { display:grid; grid-template-columns:repeat(3,minmax(0,1fr)); gap:12px; }
  .task-card { display:flex; flex-direction:column; align-items:stretch; gap:18px; text-align:left; border:1px solid var(--color-line); border-radius:12px; padding:16px; background:var(--color-paper); min-width:0; }
  .task-card:hover { border-color:var(--color-accent); }
  .task-card.is-selected { border-color:var(--color-accent); box-shadow:0 0 0 2px var(--color-accent-soft); outline:1px solid var(--color-accent); }
  .task-card-title { display:flex; flex-wrap:wrap; align-items:center; gap:8px; font-size:14px; font-weight:650; }
  .chosen { font-size:10px; color:var(--color-accent-strong); }
  .task-description, .task-example { font-size:12px; line-height:1.65; color:var(--color-ink-soft); }
  .task-description { min-height:60px; }
  .task-types { display:flex; flex-wrap:wrap; align-items:center; gap:6px; font-size:11px; }
  .task-types code { background:var(--color-surface); border:1px solid var(--color-line); border-radius:5px; padding:3px 6px; }
  .task-example { background:var(--color-surface); border:1px solid var(--color-line); border-radius:8px; padding:12px; margin-top:auto; }
  .setup-label { margin-top:24px; }
  .setup-cards > div { border:1px solid var(--color-line); padding:14px; border-radius:10px; }
  .setup-cards p { font-size:12px; line-height:1.6; color:var(--color-ink-soft); }
  .model-setup { width:280px; flex-shrink:0; padding:24px 18px 18px; border-left:1px solid var(--color-line); display:flex; flex-direction:column; gap:10px; background:var(--color-surface); }
  .model-setup label, .field-label { font-size:11px; color:var(--color-muted); }
  .field-label { margin-top:10px; }
  .setup-note { font-size:11px; line-height:1.7; color:var(--color-muted); }
  .storage-note { display:flex; gap:8px; border-top:1px solid var(--color-line); padding-top:18px; margin-top:10px; font-size:12px; line-height:1.8; overflow-wrap:anywhere; }
  .storage-note :global(svg) { flex-shrink:0; margin-top:3px; }
  .create-model { margin-top:auto; width:100%; min-height:32px; }
  .workbench-tabs { gap:4px; }
  .workbench-tabs .is-active { background:var(--color-accent-soft); color:var(--color-accent-strong); }
  .graph-state { margin-left:auto; font-size:11px; color:var(--color-sage); }
  .graph-state.is-dirty { color:var(--color-amber-warm); }
  .workbench-empty { display:flex; flex:1; flex-direction:column; align-items:center; justify-content:center; gap:16px; text-align:center; padding:32px; min-height:0; overflow-y:auto; }
  .workbench-empty > p:not(.eyebrow) { max-width:420px; color:var(--color-muted); font-size:13px; line-height:1.7; }
  @media (max-width:1350px) { .task-cards { grid-template-columns:1fr; } .task-card { gap:10px; } .task-description { min-height:0; } .task-example { display:none; } .setup-cards { grid-template-columns:1fr; gap:8px; } }
  @media (max-width:800px) { .new-model-layout { flex-direction:column; } .model-setup { width:auto; border-left:0; border-top:1px solid var(--color-line); } .create-model { margin-top:12px; } .task-selection { padding:18px; } .task-cards { grid-template-columns:repeat(3,minmax(0,1fr)); } .task-card { padding:12px; } .setup-cards { grid-template-columns:repeat(3,minmax(0,1fr)); } .preview-badge { display:none; } }
  @media (max-width:540px) { .task-cards, .setup-cards { grid-template-columns:1fr; } .workbench-pickers { width:100%; margin-left:0; } .workbench-pickers select { flex:1; } .graph-state { width:100%; margin-left:0; } .model-picker { width:145px; } }
</style>
