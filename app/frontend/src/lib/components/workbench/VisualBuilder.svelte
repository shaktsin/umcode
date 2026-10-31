<script lang="ts">
  import { onDestroy, tick } from 'svelte';
  import { SvelteFlowProvider } from '@xyflow/svelte';
  import { Undo2, Redo2, Maximize2, Save, RefreshCw, Code2, Download, SlidersHorizontal, X, Plus } from '@lucide/svelte';
  import { EditorState } from '$lib/workbench/editor.svelte';
  import { analyzeGraph, definition, layers, removeLayers, starterGraph, type Config, type Graph, type StarterPreset } from '$lib/workbench/graph';
  import { relevantStarterPresets, type InputModality } from '$lib/workbench/presets';
  import { generatePython } from '$lib/workbench/codegen';
  import { dialog } from '$lib/stores/dialog.svelte';
  import type { WorkbenchTask } from '$lib/workbench/types';
  import ModelCanvas from './ModelCanvas.svelte';
  import LayerInspector from './LayerInspector.svelte';
  let { text, onchange, onSave, onReload, disabled, dirty, task, name }: { text: string; onchange: (text: string) => void; onSave: () => void; onReload: () => void; disabled: boolean; dirty: boolean; task: WorkbenchTask; name: string } = $props();
  const editor = new EditorState();
  let selected = $state<string | null>(null), properties = $state(false), showCode = $state(false), search = $state(''), message = $state('');
  let issuesOpen = $state(true);
  let canvas = $state<ModelCanvas>();
  let alive = true;
  onDestroy(() => { alive = false; });
  $effect(() => { editor.sync(text); });
  const analysis = $derived(editor.graph ? analyzeGraph(editor.graph) : { errors: [], shapes: {}, parameters: 0, order: [] });
  const selectedNode = $derived(editor.graph?.nodes.find(n => n.id === selected) ?? null);
  const inputMode = $derived(editor.graph?.nodes.find(n => n.kind === 'input')?.config.mode);
  const modality = $derived<InputModality>(inputMode === 'image' ? 'image' : inputMode === 'features' ? 'features' : 'text');
  const starterOptions = $derived(relevantStarterPresets(task, modality).filter(s => s.id !== 'blank'));
  const issues = $derived(analysis.errors.map(error => {
    const node = editor.graph?.nodes.find(n => n.id === error.nodeId);
    return { ...error, layer: node ? definition(node.kind).name : undefined };
  }));
  const layerGroups = [...new Set(layers.map(l => l.group))];
  const groups = $derived(modality === 'image' ? ['Input & output', 'Vision', ...layerGroups.filter(group => group !== 'Input & output' && group !== 'Vision')] : layerGroups);
  const generated = $derived.by(() => { if (!editor.graph || analysis.errors.length) return null; return generatePython(editor.graph); });
  function apply(graph: Graph) { editor.commit(graph); onchange(editor.text); message = ''; }
  function history(redo = false) { if (redo) editor.redo(); else editor.undo(); onchange(editor.text); }
  function select(id: string | null) { selected = id; if (id && !showCode) properties = true; }
  function config(value: Config) { if (editor.graph && selected) apply({ ...editor.graph, nodes: editor.graph.nodes.map(n => n.id === selected ? { ...n, config: value } : n) }); }
  function remove() { if (editor.graph && selected) { apply(removeLayers(editor.graph, [selected])); selected = null; } }
  async function starter(preset: StarterPreset) {
    if (editor.graph?.nodes.length && !await dialog.confirm('Replace the current layers with this starter architecture?', { okLabel: 'Use starter' })) return;
    if (!alive) return;
    apply(starterGraph(preset)); selected = null; properties = false;
    await tick(); canvas?.fit();
  }
  function exportModel() {
    if (!generated) return;
    const url = URL.createObjectURL(new Blob([generated.code], { type: 'text/x-python' }));
    const anchor = document.createElement('a'); anchor.href = url; anchor.download = `${name.toLowerCase().replace(/[^a-z0-9]+/g, '-').slice(0, 60) || 'model'}.py`; anchor.click();
    setTimeout(() => URL.revokeObjectURL(url), 1000);
  }
  function shortcuts(event: KeyboardEvent) {
    if (!(event.metaKey || event.ctrlKey) || event.target instanceof HTMLElement && ['INPUT', 'TEXTAREA', 'SELECT'].includes(event.target.tagName)) return;
    if (event.key.toLowerCase() === 'z') { event.preventDefault(); history(event.shiftKey); }
    if (event.key.toLowerCase() === 's') { event.preventDefault(); if (!disabled) onSave(); }
  }
</script>

<svelte:window onkeydown={shortcuts} />
<section class="visual-builder" data-visual-builder aria-label="Visual model builder">
  {#if editor.error}
    <div class="unsupported"><h2>This model needs a different builder</h2><p>{editor.error}</p></div>
  {:else if editor.graph}
    <div class="builder-toolbar">
      <div class="toolbar-group"><button class="icon-button" aria-label="Undo model change" disabled={!editor.canUndo} onclick={() => history()}><Undo2 size={15} /></button><button class="icon-button" aria-label="Redo model change" disabled={!editor.canRedo} onclick={() => history(true)}><Redo2 size={15} /></button><button class="icon-button" aria-label="Fit model to canvas" onclick={() => canvas?.fit()}><Maximize2 size={15} /></button></div>
      <div class="starter-actions"><span>{modality === 'image' ? 'Vision' : modality === 'features' ? 'Features' : 'Start with'}</span>{#each starterOptions as option (option.id)}<button class="btn-ghost btn-sm" onclick={() => void starter(option.id as StarterPreset)}>{option.id === 'gpt2' ? 'GPT-2 size' : option.name}</button>{/each}</div>
      <div class="toolbar-group toolbar-right"><button class="icon-button" aria-label="Show layer properties" aria-pressed={properties} onclick={() => { properties = !properties; showCode = false; }}><SlidersHorizontal size={15} /></button><button class="btn-ghost btn-sm" aria-pressed={showCode} onclick={() => { showCode = !showCode; properties = false; }}><Code2 size={14} />Code</button><button class="btn-outline btn-sm" disabled={!generated} onclick={exportModel}><Download size={13} />Export .py</button></div>
    </div>
    <div class="builder-body">
      <aside class="layer-palette" aria-label="Layer palette"><label class="sr-only" for="layer-search">Search layers</label><input id="layer-search" class="input" placeholder="Search layers" bind:value={search} />
        {#each groups as group}
          {#if layers.some(l => l.group === group && (l.name + l.description).toLowerCase().includes(search.toLowerCase()))}<h3>{group}</h3>{/if}
          {#each layers.filter(l => l.group === group && (l.name + l.description).toLowerCase().includes(search.toLowerCase())) as layer (layer.kind)}
            <button class="palette-layer" draggable="true" data-palette-kind={layer.kind} title={`${layer.description}. Drag to the canvas or click to add.`} aria-label={`Add ${layer.name} layer`} ondragstart={(e) => { e.dataTransfer?.setData('application/umcode-layer', layer.kind); if (e.dataTransfer) e.dataTransfer.effectAllowed = 'copy'; }} onclick={() => { try { canvas?.add(layer.kind); } catch (e) { message = (e as Error).message; } }}><span style:background={layer.color}></span>{layer.name}<Plus size={11} /></button>
          {/each}
        {/each}
        <p class="palette-help">Drag to add.<br />Join the dots to connect.<br />Select a layer to edit.</p>
      </aside>
      <SvelteFlowProvider><ModelCanvas bind:this={canvas} graph={editor.graph} {analysis} {selected} onchange={apply} onselect={select} onerror={(error) => message = error} /></SvelteFlowProvider>
      {#if properties}<div class="properties-panel"><LayerInspector node={selectedNode} {analysis} onconfig={config} ondelete={remove} onclose={() => properties = false} /></div>{/if}
      {#if showCode}<aside class="code-panel" aria-label="Generated PyTorch code"><div class="code-heading"><strong>PyTorch architecture</strong><button class="icon-button" aria-label="Close code preview" onclick={() => showCode = false}><X size={14} /></button></div>{#if generated}<pre>{#each generated.code.split('\n') as line, index}<span class:highlight={!!selected && generated.lineNodes[index] === selected}>{line || ' '}</span>{/each}</pre>{:else}<p>Connect the layers and resolve the highlighted settings to generate your model.</p>{/if}</aside>{/if}
    </div>
    {#if issuesOpen && editor.graph.nodes.length && issues.length}
      <section class="validation-panel" aria-label="Model issues">
        <h3>Fix before exporting</h3>
        <ul>
          {#each issues as issue}
            <li>{#if issue.nodeId}<button aria-label={`${issue.layer ?? 'Layer'}: ${issue.message}`} onclick={() => { showCode = false; select(issue.nodeId!); }}><strong>{issue.layer ?? 'Layer'}:</strong> {issue.message}</button>{:else}<span>{issue.message}</span>{/if}</li>
          {/each}
        </ul>
      </section>
    {/if}
    <footer class="builder-footer">
      <div class="model-summary"><strong>{new Intl.NumberFormat('en', { notation: 'compact', maximumFractionDigits: 2 }).format(analysis.parameters)}</strong> parameters<span>·</span><span title="Float32 parameter weights only; excludes gradients, optimizer states and activations">{(analysis.parameters * 4 / 1048576).toFixed(1)} MB weights</span></div>
      {#if message}<div class="model-validity has-errors" role="status">{message}</div>
      {:else if editor.graph.nodes.length && issues.length}<button class="model-validity has-errors issues-toggle" aria-label={issuesOpen ? 'Hide model issues' : 'Show model issues'} aria-expanded={issuesOpen} onclick={() => issuesOpen = !issuesOpen}>{issues.length} {issues.length === 1 ? 'issue' : 'issues'} to fix {issuesOpen ? '▾' : '▴'}</button>
      {:else}<div class="model-validity" role="status">{editor.graph.nodes.length ? '✓ Layer sizes match' : 'Add layers or choose a starter'}</div>{/if}
      <div class="footer-actions"><button class="icon-button" aria-label="Reload model from disk" disabled={disabled} onclick={onReload}><RefreshCw size={14} /></button><button class="btn-primary btn-sm" disabled={disabled || !dirty} onclick={onSave}><Save size={14} />Save model</button></div>
    </footer>
  {/if}
</section>

<style>
  .visual-builder { display:flex; flex:1; flex-direction:column; min-width:0; min-height:0; container-type:inline-size; }
  .builder-toolbar { display:flex; align-items:center; flex-wrap:wrap; gap:6px; padding:7px 10px; border-bottom:1px solid var(--color-line); }
  .toolbar-group, .starter-actions { display:flex; align-items:center; gap:3px; } .toolbar-right { margin-left:auto; } .starter-actions > span { font-size:10px; color:var(--color-muted); margin:0 4px; }
  .builder-toolbar .btn-sm { font-size:11px; }
  .builder-body { display:flex; flex:1; position:relative; min-height:0; min-width:0; overflow:hidden; }
  .layer-palette { width:164px; flex-shrink:0; padding:12px 7px; overflow-y:auto; border-right:1px solid var(--color-line); background:var(--color-surface); }
  .layer-palette input { width:100%; font-size:11px; padding-inline:7px; }
  .layer-palette h3 { margin:20px 7px 7px; font-size:9px; color:var(--color-muted); font-weight:650; text-transform:uppercase; letter-spacing:.04em; }
  .palette-layer { display:flex; align-items:center; gap:7px; width:100%; padding:7px; font-size:11px; text-align:left; border-radius:6px; cursor:grab; line-height:1.4; }
  .palette-layer:hover { background:var(--color-accent-soft); } .palette-layer > span { width:5px; height:13px; border-radius:3px; flex-shrink:0; } .palette-layer :global(svg) { margin-left:auto; opacity:.4; flex-shrink:0; }
  .palette-help { color:var(--color-muted); font-size:10px; line-height:1.8; padding:18px 7px 8px; }
  .properties-panel { display:flex; min-height:0; }
  .code-panel { width:340px; flex-shrink:0; overflow:auto; border-left:1px solid var(--color-line); background:var(--color-paper); }
  .code-heading { display:flex; align-items:center; justify-content:space-between; padding:10px 14px; border-bottom:1px solid var(--color-line); font-size:11px; }
  pre { font:10px/1.8 var(--font-mono); padding:14px 0; } pre span { display:block; padding:0 14px; min-width:max-content; } pre .highlight { background:var(--color-accent-soft); color:var(--color-accent-strong); } .code-panel > p { padding:20px; color:var(--color-muted); font-size:12px; line-height:1.8; }
  .builder-footer { display:flex; align-items:center; flex-wrap:wrap; gap:10px; padding:9px 12px; border-top:1px solid var(--color-line); font-size:10px; background:var(--color-surface); }
  .validation-panel { max-height:130px; flex-shrink:0; overflow-y:auto; border-top:1px solid var(--color-line); background:var(--color-surface); padding:10px 14px; font-size:11px; line-height:1.65; }
  .validation-panel h3 { color:var(--color-ink); font-weight:650; margin-bottom:5px; }
  .validation-panel ul { padding-left:17px; list-style:disc; color:var(--color-rust); }
  .validation-panel li + li { margin-top:4px; }
  .validation-panel button { text-align:left; cursor:pointer; border-radius:3px; }
  .validation-panel button:hover { text-decoration:underline; }
  .validation-panel button:focus-visible { outline:2px solid var(--color-accent); outline-offset:2px; }
  .issues-toggle { text-align:left; cursor:pointer; border-radius:4px; padding:3px 5px; }
  .model-summary { display:flex; align-items:center; flex-wrap:wrap; gap:5px; color:var(--color-muted); } .model-summary strong { color:var(--color-ink); }
  .model-validity { margin-left:auto; color:var(--color-sage); } .has-errors { color:var(--color-rust); } .footer-actions { display:flex; align-items:center; gap:5px; }
  .unsupported { margin:auto; padding:40px; max-width:550px; } .unsupported h2 { font-weight:650; margin-bottom:12px; } .unsupported p { color:var(--color-muted); font-size:13px; line-height:1.8; }
  @container (max-width:900px) { .properties-panel, .code-panel { position:absolute; z-index:20; top:0; right:0; bottom:0; box-shadow:-8px 0 24px #00000016; } .starter-actions > span { display:none; } .layer-palette { width:145px; } }
  @container (max-width:600px) { .model-summary { display:none; } .model-validity { margin-left:0; flex:1; } .starter-actions { order:3; width:100%; } .layer-palette { width:125px; } .palette-layer { font-size:10px; } .code-panel { width:min(340px,100%); } }
</style>
