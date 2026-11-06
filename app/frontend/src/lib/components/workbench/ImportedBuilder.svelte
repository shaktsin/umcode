<script lang="ts">
  import { onMount, untrack } from 'svelte';
  import { workbenchAssistant } from '$lib/workbench/assistantStore.svelte';
  import { SvelteFlow, SvelteFlowProvider, Background, Controls, MiniMap, MarkerType, type Node, type Edge, type Connection } from '@xyflow/svelte';
  import { Undo2, Redo2, Save, RefreshCw, Plus, Trash2, X } from '@lucide/svelte';
  import { ImportedEditorState } from '$lib/workbench/modelImport.svelte';
  import { addImportedNode, bindingSummary, connectImportedNodes, importedPalette, importNodeLocked } from '$lib/workbench/modelImport';
  import type { ImportedGraph, ImportedPaletteItem, ImportConfig } from '$lib/workbench/modelImportTypes';
  import type { WorkbenchTask } from '$lib/workbench/types';
  import { decoderDetails, importedLayerTitle } from '$lib/workbench/importedLayers';
  import ImportedLayerNode from './ImportedLayerNode.svelte';
  import '@xyflow/svelte/dist/style.css';
  let { text, onchange, onSave, onReload, disabled: busy, dirty, task, name, onAsk = () => {} }: { text: string; onchange: (text: string) => void; onSave: () => void; onReload: () => void; disabled: boolean; dirty: boolean; task: WorkbenchTask; name: string; onAsk?: () => void } = $props();
  const editor = new ImportedEditorState();
  const preview = $derived(workbenchAssistant.preview?.type === 'imported' ? workbenchAssistant.preview : null);
  const disabled = $derived(busy || !!preview);
  const visibleGraph = $derived(preview?.graph ?? editor.graph);
  onMount(() => workbenchAssistant.bindImported(graph => { editor.commit(graph); onchange(editor.text); }));
  const nodeTypes = { imported: ImportedLayerNode };
  let nodes = $state<Node[]>([]), edges = $state<Edge[]>([]), selected = $state<string | null>(null), message = $state(''), search = $state(''), tensorSearch = $state(''), showSource = $state(false), showAllTensors = $state(false);
  let viewport = $state({ x: 0, y: 0, zoom: 1 });
  let canvas = $state<HTMLDivElement>();
  $effect(() => { editor.sync(text); });
  const palette = $derived(importedPalette(editor.graph?.source.family ?? '', editor.graph ?? undefined));
  const readonly = $derived(!palette.length);
  const summary = $derived(visibleGraph ? bindingSummary(visibleGraph) : { retained: [], needsWeights: [], unused: [] });
  const selectedNode = $derived(visibleGraph?.nodes.find(node => node.id === selected) ?? null);
  const selectedDetails = $derived(selectedNode && editor.graph ? decoderDetails(selectedNode, editor.graph.source.family) : null);
  function inspectLayer(id: string) { selected = id; showSource = false; showAllTensors = false; }
  const selectedLocked = $derived(readonly || (!!selectedNode && importNodeLocked(selectedNode)));
  const lockedIds = $derived(new Set(visibleGraph?.nodes.filter(importNodeLocked).map(n=>n.id) ?? []));
  function edgeLocked(e: {source:string;target:string}) { return lockedIds.has(e.source) || lockedIds.has(e.target); }
  function visibleLabel(id:string) { const n=visibleGraph?.nodes.find(n=>n.id===id); return n && visibleGraph ? importedLayerTitle(n,visibleGraph.source.family) : id; }
  const selectedTensorNames = $derived(selectedNode && !showAllTensors ? new Set(selectedNode.tensorNames) : null);
  const tensorQuery = $derived(tensorSearch.toLowerCase());
  const inspectedTensors = $derived((editor.graph?.source.tensors ?? []).filter(t => (!selectedTensorNames || selectedTensorNames.has(t.name)) && t.name.toLowerCase().includes(tensorQuery)));
  $effect(() => {
    const graph = visibleGraph; if (!graph) return;
    const previous = untrack(() => nodes);
    nodes = graph.nodes.map(node => ({ ...previous.find(n => n.id === node.id), id: node.id, type: 'imported', draggable: !disabled && !importNodeLocked(node), connectable: !disabled && !importNodeLocked(node), deletable: !disabled && !importNodeLocked(node), position: { ...node.position }, selected: node.id === selected, ariaLabel: importedLayerTitle(node, graph.source.family), data: { label: importedLayerTitle(node, graph.source.family), inspect: decoderDetails(node, graph.source.family) ? () => inspectLayer(node.id) : null, kind: node.kind, repeat: node.config.repeatCount, summary: Object.entries(node.config).filter(([, v]) => typeof v === 'number').slice(0, 3).map(([k, v]) => `${k}: ${v}`).join(' · '), binding: summary.retained.includes(node.id) ? `${node.tensorNames.length} source tensor mappings retained` : summary.needsWeights.includes(node.id) ? 'Needs weights' : 'Architecture metadata' } }));
    edges = graph.edges.map(edge => ({ ...edge, deletable: !disabled && !edgeLocked(edge), sourceHandle: 'out', targetHandle: 'in', markerEnd: { type: MarkerType.ArrowClosed }, style: 'stroke:var(--color-muted);stroke-width:1.5' }));
  });
  $effect(() => { if (selected && !visibleGraph?.nodes.some(n => n.id === selected)) selected = null; });
  $effect(() => { workbenchAssistant.selectedNodeId = editor.graph?.nodes.some(n => n.id === selected) ? selected : null; });
  function applySuggestion() {
    try { workbenchAssistant.apply(); message = 'Suggestion applied to your draft. Save model to keep it.'; }
    catch (e) { message = (e as Error).message; }
  }
  function ask() { workbenchAssistant.ask(); onAsk(); }
  function apply(graph: ImportedGraph) { try { editor.commit(graph); onchange(editor.text); message = ''; } catch (e) { message = (e as Error).message; } }
  function history(redo = false) { if (disabled || readonly) return; if (redo) editor.redo(); else editor.undo(); onchange(editor.text); message = ''; }
  function add(item: ImportedPaletteItem, position?: { x: number; y: number }) { if (!editor.graph || disabled || readonly) return; try { const graph = addImportedNode(editor.graph, item, position); apply(graph); selected = graph.nodes.at(-1)!.id; } catch (e) { message = (e as Error).message; } }
  function config(key: string, value: string | number | boolean) { if (!editor.graph || !selectedNode || selectedLocked || disabled) return; if (typeof value === 'number' && !Number.isFinite(value)) return; const next: ImportConfig = { ...selectedNode.config, [key]: value }; apply({ ...editor.graph, nodes: editor.graph.nodes.map(node => node.id === selected ? { ...node, config: next } : node) }); }
  function remove(ids: string[], edgeIds: string[] = []) { if (!editor.graph || disabled || readonly) return; if (ids.some(id=>lockedIds.has(id)) || editor.graph.edges.some(e=>edgeIds.includes(e.id) && edgeLocked(e))) return; const removed = new Set(ids); apply({ ...editor.graph, nodes: editor.graph.nodes.filter(n => !removed.has(n.id)), edges: editor.graph.edges.filter(e => !removed.has(e.source) && !removed.has(e.target) && !edgeIds.includes(e.id)) }); }
  function connect(connection: Connection) { if (!editor.graph || disabled || readonly) return; if (edgeLocked(connection)) return; try { apply(connectImportedNodes(editor.graph, connection.source, connection.target)); } catch (e) { message = (e as Error).message; } }
  function persistPositions(moved: Node[]) { if (!editor.graph || disabled || readonly) return; apply({ ...editor.graph, nodes: editor.graph.nodes.map(node => { const updated = moved.find(n => n.id === node.id); return updated ? { ...node, position: { ...updated.position } } : node; }) }); }
  function drop(event: DragEvent) { event.preventDefault(); if (disabled || readonly) return; const item = palette.find(item => item.kind === event.dataTransfer?.getData('application/umcode-imported-layer')); if (!item) return; const rect = canvas?.getBoundingClientRect(); if (!rect) return; add(item, { x: (event.clientX - rect.left - viewport.x) / viewport.zoom - 117, y: (event.clientY - rect.top - viewport.y) / viewport.zoom - 45 }); }
  function keyboard(event: KeyboardEvent) { if (event.target instanceof HTMLElement && ['INPUT', 'TEXTAREA', 'SELECT'].includes(event.target.tagName)) return; if (event.metaKey || event.ctrlKey) { if (event.key.toLowerCase() === 'z') { event.preventDefault(); history(event.shiftKey); } if (event.key.toLowerCase() === 's') { event.preventDefault(); if (!disabled && !readonly) onSave(); } } }
  function keyboardMove(event: KeyboardEvent) { if (['ArrowUp', 'ArrowDown', 'ArrowLeft', 'ArrowRight'].includes(event.key) && event.target instanceof HTMLElement && canvas?.contains(event.target) && event.target.closest('.svelte-flow__node')) persistPositions(nodes); }
</script>

<svelte:window onkeydown={keyboard} onkeyup={keyboardMove} />
<section class="imported-builder" data-imported-builder aria-label="Imported model builder">
  {#if editor.error}<div class="error" role="alert">{editor.error}</div>
  {:else if editor.graph}
    {#if preview}
      <section class="assistant-preview" aria-label="Model change preview">
        <h2 id="workbench-preview-heading" tabindex="-1">Preview · {preview.proposal.modelName}</h2>
        <p>{preview.proposal.explanation} · Draft unchanged</p>
        <ul>{#each preview.changes as change}<li>{change}</li>{/each}</ul>
        {#if workbenchAssistant.previewStale}<p role="alert">Model changed. Cancel and ask for an updated suggestion.</p>{/if}
        <button class="btn-outline btn-sm" onclick={() => workbenchAssistant.cancel()}>Cancel preview</button>
        <button class="btn-primary btn-sm" disabled={busy || workbenchAssistant.previewStale} onclick={applySuggestion}>Apply changes</button>
      </section>
    {/if}
    <div class="imported-toolbar"><div class="history"><button class="icon-button" aria-label="Undo imported model change" disabled={disabled || readonly || !editor.canUndo} onclick={() => history()}><Undo2 size={15} /></button><button class="icon-button" aria-label="Redo imported model change" disabled={disabled || readonly || !editor.canRedo} onclick={() => history(true)}><Redo2 size={15} /></button></div><strong>{editor.graph.source.family} architecture</strong><span class="metadata-status">{editor.graph.source.weightsVerified ? 'Weight metadata verified' : 'Weights unverified'}</span><button class="btn-ghost btn-sm" aria-pressed={showSource} onclick={() => showSource = !showSource}>Source metadata</button><button class="btn-ghost btn-sm" aria-pressed={showAllTensors} onclick={() => showAllTensors = !showAllTensors}>All tensors</button></div>
    <p class="runtime-note">Editing an architecture copy. Source files stay unchanged. Build edits the architecture. Data, Train and Inference use a separately validated complete model folder. Changing layers or connections gates execution until the original architecture is restored.</p>
    {#if readonly}
      <div class="unknown-family"><h3>Read-only tensor hierarchy</h3><p>No native adapter is available for this family.</p><ul>{#each editor.graph.nodes as node}<li><button class="btn-ghost btn-sm" onclick={() => selected = node.id}>{node.label} · {node.tensorNames.length} tensors</button></li>{/each}</ul></div>
    {/if}
    <div class="imported-body">
      {#if !readonly}<aside class="imported-palette" aria-label="Imported family layer palette"><h3>{editor.graph.source.family} layers</h3><input class="input" aria-label="Search imported layers" placeholder="Search layers" bind:value={search} />{#each palette.filter(item => (item.label + item.description).toLowerCase().includes(search.toLowerCase())) as item}<button class="palette-item" disabled={disabled} draggable={!disabled} aria-label={`Add ${item.label}`} title={item.description} ondragstart={(e) => { e.dataTransfer?.setData('application/umcode-imported-layer', item.kind); if (e.dataTransfer) e.dataTransfer.effectAllowed = 'copy'; }} onclick={() => add(item)}><span>{item.label}</span><Plus size={12} /><small>{item.description}</small></button>{/each}<p>Drag or click to add.<br />Join handles to connect layers.<br />Delete connections and reconnect to change layer order.</p></aside>{/if}
      {#if !readonly}<div class="imported-canvas" bind:this={canvas} data-imported-model-canvas role="region" aria-label="Imported architecture canvas" ondragover={(e) => { e.preventDefault(); if (e.dataTransfer) e.dataTransfer.dropEffect = 'copy'; }} ondrop={drop}>
        <SvelteFlowProvider><SvelteFlow bind:nodes bind:edges bind:viewport {nodeTypes} fitView fitViewOptions={{ maxZoom: 1, padding: .15 }} minZoom={.1} maxZoom={2} colorMode="system" nodesDraggable={!disabled} nodesConnectable={!disabled} deleteKey={disabled ? null : ['Backspace', 'Delete']} onconnect={connect} onnodeclick={({ node }) => inspectLayer(node.id)} onpaneclick={() => selected = null} onnodedragstop={({ nodes: moved }) => persistPositions(moved)} ondelete={({ nodes: removed, edges: removedEdges }) => remove(removed.map(n => n.id), removedEdges.map(e => e.id))}><Background patternColor="var(--color-line)" gap={20} /><Controls showLock={false} /><MiniMap pannable zoomable /></SvelteFlow></SvelteFlowProvider>
        {#if !editor.graph.nodes.length}<div class="empty">Add a family-native layer to begin.</div>{/if}
      </div>{/if}
      {#if selectedNode || showSource || showAllTensors || readonly}
        <aside class="imported-inspector" aria-label="Imported layer properties">
          <div class="inspector-heading"><strong>{showSource ? 'Immutable source metadata' : showAllTensors ? 'All source tensors' : selectedNode ? importedLayerTitle(selectedNode, editor.graph.source.family) : 'Source tensor hierarchy'}</strong><button class="icon-button" aria-label="Close imported properties" onclick={() => { selected = null; showSource = false; showAllTensors = false; }}><X size={14} /></button></div>
          {#if showSource}<dl class="source-info"><dt>Family</dt><dd>{editor.graph.source.family}</dd><dt>Original files</dt><dd>{#each editor.graph.source.files as file}<div>{file.name} · {file.size.toLocaleString()} bytes</div>{/each}</dd></dl><details><summary>Original config.json</summary><pre>{JSON.stringify(editor.graph.source.config, null, 2)}</pre></details>{#if editor.graph.source.warnings?.length}<ul class="source-warnings">{#each editor.graph.source.warnings as warning}<li>{warning}</li>{/each}</ul>{/if}
          {:else if selectedNode && !showAllTensors}
            <button class="btn-outline btn-sm" {disabled} onclick={ask}>Ask about this layer</button>
            <p class="node-status" class:needs={summary.needsWeights.includes(selectedNode.id)}>{summary.retained.includes(selectedNode.id) ? 'Original source tensor mappings retained. Runtime readiness is checked separately; changing layers or connections makes the source weights incompatible.' : summary.needsWeights.includes(selectedNode.id) ? 'This new or changed layer needs weights before future execution.' : 'Architecture metadata; no retained weight payload.'}</p>
            {#if selectedDetails}
              {#key selectedNode.id}<details class="layer-breakdown" open><summary>Inside this decoder layer</summary>
                <p>Input {selectedDetails.shape} → Output {selectedDetails.shape}</p>
                <ol>{#each selectedDetails.steps as step}<li><strong>{step.name}</strong><br />{step.detail}</li>{/each}</ol>
                <small>{selectedDetails.note} Edit the layer as one unit below.</small>
              </details>{/key}
            {/if}
            <label>Layer label<input class="input" value={importedLayerTitle(selectedNode, editor.graph.source.family)} disabled={disabled || selectedLocked} oninput={(e) => { if (editor.graph && selected) apply({ ...editor.graph, nodes: editor.graph.nodes.map(n => n.id === selected ? { ...n, label: e.currentTarget.value } : n) }); }} /></label>
            <div class="kind-label">Native grouping: <strong>{selectedNode.kind}</strong></div>
            {#each Object.entries(selectedNode.config) as [key, value]}
              <label class="property">{key}{#if typeof value === 'boolean'}<input type="checkbox" checked={value} disabled={disabled || selectedLocked} onchange={(e) => config(key, e.currentTarget.checked)} />{:else if typeof value === 'number'}<input class="input" type="number" step="any" value={value} disabled={disabled || selectedLocked} oninput={(e) => { if (e.currentTarget.value !== '') config(key, e.currentTarget.valueAsNumber); }} />{:else}<input class="input" value={value} disabled={disabled || selectedLocked} oninput={(e) => config(key, e.currentTarget.value)} />{/if}</label>
            {/each}
            {#if selectedNode.sourceKey}<p class="source-key">Source group: <code>{selectedNode.sourceKey}</code></p>{/if}
            {#if !selectedLocked}<button class="btn-ghost btn-sm delete-layer" disabled={disabled} onclick={() => remove([selectedNode!.id])}><Trash2 size={13} />Delete layer</button>{/if}
            <details><summary>Connections · reorder by reconnecting</summary>{#each (visibleGraph?.edges ?? []).filter(e => e.source === selected || e.target === selected) as edge}<div class="connection"><span>{visibleLabel(edge.source)} → {visibleLabel(edge.target)}</span><button class="icon-button" aria-label="Delete layer connection" disabled={disabled || readonly || edgeLocked(edge)} onclick={() => remove([], [edge.id])}><X size={12} /></button></div>{/each}</details>
          {/if}
          {#if !showSource}<section class="tensor-inspection"><h3>Tensor metadata · {inspectedTensors.length.toLocaleString()}</h3><input class="input" aria-label="Search layer tensors" placeholder="Search source tensors" bind:value={tensorSearch} />{#if !inspectedTensors.length}<p>No matching source tensors. Added layers have no source bindings.</p>{/if}{#each inspectedTensors.slice(0, 100) as tensor}<details class="tensor"><summary>{tensor.name}</summary><dl><dt>Shape</dt><dd>[{tensor.shape.join(', ')}]</dd><dt>Dtype</dt><dd>{tensor.dtype}</dd><dt>Shard</dt><dd>{tensor.file}</dd><dt>Payload bytes on disk</dt><dd>{tensor.bytes.toLocaleString()}</dd></dl></details>{/each}{#if inspectedTensors.length > 100}<p>Showing 100 tensors. Refine the search to inspect more.</p>{/if}</section>{/if}
        </aside>
      {/if}
    </div>
    <div class="compatibility" aria-label="Source binding compatibility"><strong>{summary.retained.length} retained mappings</strong><span>{summary.needsWeights.length} layers need weights</span><details><summary>{summary.unused.length.toLocaleString()} unused source tensors</summary><ul>{#each summary.unused.slice(0, 100) as tensor}<li>{tensor}</li>{/each}</ul>{#if summary.unused.length > 100}<span>Showing the first 100 unused tensor names.</span>{/if}</details><span class="binding-explanation">Mapping metadata only; topology and upstream shape edits need compatibility validation before execution.</span></div>
    <footer><span role="status">{message || `${editor.graph.nodes.length} native architecture groups · ${name}`}</span><button class="icon-button" aria-label="Reload imported model from disk" disabled={disabled} onclick={onReload}><RefreshCw size={14} /></button><button class="btn-primary btn-sm" disabled={disabled || readonly || !dirty} onclick={onSave}><Save size={14} />Save model</button></footer>
  {/if}
</section>

<style>
  .assistant-preview { max-height:200px; overflow:auto; background:var(--color-accent-soft); padding:12px; font-size:12px; line-height:1.65; }
  .assistant-preview ul { list-style:disc; padding-left:18px; } .assistant-preview button { margin:8px 6px 0 0; }
  .layer-breakdown { background:var(--color-surface); padding:10px; border-radius:6px; line-height:1.6; }
  .layer-breakdown summary { font-weight:650; font-size:12px; } .layer-breakdown ol { list-style:decimal; padding-left:20px; }
  .layer-breakdown li, .layer-breakdown p { margin:8px 0; } .layer-breakdown small { color:var(--color-muted); }
  .imported-builder { display:flex; flex:1; min-height:0; min-width:0; flex-direction:column; container-type:inline-size; } .imported-toolbar { display:flex; align-items:center; flex-wrap:wrap; gap:9px; padding:8px 12px; border-bottom:1px solid var(--color-line); font-size:11px; } .history { display:flex; gap:3px; } .imported-toolbar strong { margin-right:auto; } .metadata-status { color:var(--color-muted); font-size:10px; }
  .runtime-note { padding:10px 14px; background:var(--color-surface); border-bottom:1px solid var(--color-line); font-size:11px; line-height:1.65; color:var(--color-muted); } .imported-body { position:relative; display:flex; min-height:0; min-width:0; flex:1; overflow:hidden; } .imported-palette { width:180px; flex-shrink:0; padding:14px 9px; border-right:1px solid var(--color-line); overflow:auto; background:var(--color-surface); } h3 { font-size:11px; font-weight:650; margin-bottom:9px; } .imported-palette > input { width:100%; font-size:11px; } .palette-item { display:flex; flex-wrap:wrap; width:100%; gap:7px; text-align:left; padding:11px 5px; border-bottom:1px solid var(--color-line); cursor:grab; font-size:11px; } .palette-item span { flex:1; } .palette-item small { width:100%; color:var(--color-muted); font-size:10px; line-height:1.6; } .palette-item:hover { background:var(--color-accent-soft); } .imported-palette > p { font-size:10px; line-height:1.8; color:var(--color-muted); padding:15px 4px; }
  .imported-canvas { flex:1; position:relative; min-height:0; min-width:0; } .empty { position:absolute; top:45%; width:100%; text-align:center; color:var(--color-muted); pointer-events:none; font-size:13px; } .imported-inspector { width:300px; flex-shrink:0; padding:14px; overflow:auto; border-left:1px solid var(--color-line); background:var(--color-paper); font-size:11px; } .inspector-heading { display:flex; align-items:flex-start; gap:10px; margin-bottom:13px; } .inspector-heading strong { flex:1; font-size:12px; overflow-wrap:anywhere; } .node-status { color:var(--color-sage); line-height:1.7; background:var(--color-surface); border-radius:5px; padding:10px; margin-bottom:14px; } .needs, .delete-layer { color:var(--color-rust); } label { display:flex; flex-direction:column; gap:6px; margin:12px 0; overflow-wrap:anywhere; } label .input { width:100%; font-size:11px; } label:has(input[type=checkbox]) { flex-direction:row; justify-content:space-between; } .kind-label { color:var(--color-muted); margin:12px 0; } .source-key { overflow-wrap:anywhere; color:var(--color-muted); line-height:1.7; margin:15px 0; } details { margin:12px 0; } summary { cursor:pointer; font-size:10px; line-height:1.7; } .connection { display:flex; align-items:center; gap:7px; font-size:10px; margin:8px 0; } .connection span { flex:1; }
  .tensor-inspection { border-top:1px solid var(--color-line); margin-top:16px; padding-top:14px; } .tensor-inspection > input { width:100%; font-size:10px; } .tensor-inspection p { color:var(--color-muted); font-size:10px; line-height:1.8; margin-top:10px; } .tensor { border-bottom:1px solid var(--color-line); padding-bottom:7px; } .tensor summary { font-family:var(--font-mono); overflow-wrap:anywhere; } dl { font-size:10px; line-height:1.8; margin:8px 0; } dt { color:var(--color-muted); } dd { overflow-wrap:anywhere; margin-bottom:7px; } .source-warnings { padding-left:15px; list-style:disc; line-height:1.8; color:var(--color-rust); } pre { font:10px/1.8 var(--font-mono); white-space:pre-wrap; overflow-wrap:anywhere; padding:10px 0; }
  .compatibility { display:flex; align-items:center; flex-wrap:wrap; gap:12px; padding:9px 14px; border-top:1px solid var(--color-line); font-size:10px; color:var(--color-muted); } .compatibility strong { color:var(--color-ink); } .compatibility details { margin:0; position:relative; } .compatibility details ul { max-height:130px; overflow:auto; overflow-wrap:anywhere; padding:10px 0; } .binding-explanation { margin-left:auto; } footer { display:flex; align-items:center; gap:8px; padding:9px 12px; border-top:1px solid var(--color-line); } footer > span { flex:1; font-size:10px; color:var(--color-muted); } .unknown-family { padding:15px; font-size:12px; } .unknown-family p { color:var(--color-muted); } .error { padding:30px; color:var(--color-rust); }
  :global(.svelte-flow) { --xy-background-color:var(--color-paper); --xy-controls-button-background-color:var(--color-paper); --xy-controls-button-background-color-hover:var(--color-surface); --xy-controls-button-color:var(--color-ink); --xy-controls-button-border-color:var(--color-line); --xy-minimap-background-color:var(--color-surface); --xy-minimap-mask-background-color:#64748b22; } :global(.svelte-flow__minimap) { width:110px; height:80px; border:1px solid var(--color-line); border-radius:7px; overflow:hidden; } :global(.svelte-flow__handle) { width:11px; height:11px; background:var(--color-accent); border:2px solid var(--color-paper); }
  @container(max-width:900px) { .imported-inspector { position:absolute; right:0; top:0; bottom:0; z-index:15; box-shadow:-8px 0 24px #00000016; } .imported-palette { width:150px; } .binding-explanation { display:none; } } @container(max-width:500px) { .imported-palette { width:120px; } .imported-inspector { width:min(300px,100%); } }
</style>
