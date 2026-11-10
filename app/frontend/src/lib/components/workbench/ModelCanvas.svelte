<script lang="ts">
  import { untrack } from 'svelte';
  import { SvelteFlow, Background, Controls, MiniMap, useSvelteFlow, MarkerType, type Node, type Edge, type Connection } from '@xyflow/svelte';
  import LayerCard from './LayerCard.svelte';
  import { addLayer, connectLayers, connectionError, definition, removeLayers, shapeLabel, visibleFields, type Graph, type Analysis, type LayerKind } from '$lib/workbench/graph';
  import '@xyflow/svelte/dist/style.css';
  let { graph, analysis, selected, onchange, onselect, onerror, readonly = false, highlighted = [] }: { graph: Graph; analysis: Analysis; selected: string | null; onchange: (g: Graph) => void; onselect: (id: string | null) => void; onerror: (message: string) => void; readonly?: boolean; highlighted?: string[] } = $props();
  let nodes = $state<Node[]>([]), edges = $state<Edge[]>([]);
  const flow = useSvelteFlow();
  const nodeTypes = { layer: LayerCard };
  let canvas: HTMLDivElement;
  $effect(() => {
    const previous = untrack(() => nodes);
    nodes = graph.nodes.map(n => ({ ...previous.find(existing => existing.id === n.id), id: n.id, type: 'layer', position: { ...n.position }, selected: selected === n.id, class: highlighted.includes(n.id) ? 'assistant-changed' : '', ariaLabel: `${definition(n.kind).name} layer`, data: { kind: n.kind, shape: shapeLabel(analysis.shapes[n.id]), issue: analysis.errors.find(e => e.nodeId === n.id)?.message, repeat: n.kind === 'transformer' ? n.config.repeat : undefined, summary: visibleFields(n).filter(f => f.type !== 'boolean').map(f => `${f.key}: ${n.config[f.key]}`).join(' · ') } }));
    edges = graph.edges.map(e => ({ ...e, sourceHandle: 'out', targetHandle: 'in', markerEnd: { type: MarkerType.ArrowClosed }, style: 'stroke:var(--color-muted);stroke-width:1.5' }));
  });
  export function fit() { void flow.fitView({ padding: .18, maxZoom: 1, duration: 180 }); }
  export function add(kind: LayerKind) {
    if (readonly) return;
    const box = canvas.getBoundingClientRect();
    const point = flow.screenToFlowPosition({ x: box.x + box.width / 2, y: box.y + box.height / 2 });
    const position = { x: point.x - 103, y: point.y - 40 };
    const next = addLayer(graph, kind, position);
    onchange(next); onselect(next.nodes.at(-1)!.id);
  }
  function connect(connection: Connection) {
    if (readonly) return;
    try { onchange(connectLayers(graph, connection.source, connection.target)); onerror(''); }
    catch (error) { onerror((error as Error).message); }
  }
  function persistPositions(moved: Node[]) {
    if (readonly) return;
    onchange({ ...graph, nodes: graph.nodes.map(n => { const updated = moved.find(m => m.id === n.id); return updated ? { ...n, position: { ...updated.position } } : n; }) });
  }
  function keyboardMove(event: KeyboardEvent) {
    if (readonly) return;
    if (!['ArrowUp', 'ArrowDown', 'ArrowLeft', 'ArrowRight'].includes(event.key)) return;
    if (event.target instanceof HTMLElement && canvas?.contains(event.target) && event.target.closest('.svelte-flow__node')) persistPositions(nodes);
  }
  function drop(event: DragEvent) {
    event.preventDefault();
    if (readonly) return;
    const kind = event.dataTransfer?.getData('application/umcode-layer') as LayerKind;
    if (!kind || !definition(kind)) return;
    try {
      const point = flow.screenToFlowPosition({ x: event.clientX, y: event.clientY });
      const position = { x: point.x - 103, y: point.y - 40 };
      const next = addLayer(graph, kind, position); onchange(next); onselect(next.nodes.at(-1)!.id);
    } catch (error) { onerror((error as Error).message); }
  }
</script>

<svelte:window onkeyup={keyboardMove} />

<div class="model-canvas" bind:this={canvas} data-model-canvas ondragover={(event) => { event.preventDefault(); if (event.dataTransfer) event.dataTransfer.dropEffect = 'copy'; }} ondrop={drop} role="region" aria-label="Model layer canvas">
  <SvelteFlow bind:nodes bind:edges {nodeTypes} oninit={() => { if (graph.nodes.length) fit(); }} minZoom={.15} maxZoom={2} colorMode="system" deleteKey={readonly ? null : ['Backspace', 'Delete']} nodesDraggable={!readonly} nodesConnectable={!readonly} elementsSelectable={!readonly} nodesFocusable={!readonly} edgesFocusable={!readonly} onconnect={connect}
    isValidConnection={(connection) => !connectionError(graph, connection.source, connection.target)}
    onnodeclick={({ node }) => { if (!readonly) onselect(node.id); }} onpaneclick={() => { if (!readonly) onselect(null); }}
    onnodedragstop={({ nodes: moved }) => persistPositions(moved)}
    ondelete={({ nodes: removedNodes, edges: removedEdges }) => { if (!readonly) onchange(removeLayers(graph, removedNodes.map(n => n.id), removedEdges.map(e => e.id))); }}>
    <Background patternColor="var(--color-line)" gap={20} />
    <Controls showLock={false} />
    <MiniMap pannable zoomable nodeColor={(node) => definition(node.data.kind as LayerKind).color} />
  </SvelteFlow>
  {#if !graph.nodes.length}<div class="canvas-welcome"><strong>Build your model visually</strong><p>Drag layers from the left onto the canvas.<br />Connect their dots from top to bottom.</p><span>Or choose a starter above to begin.</span></div>{/if}
</div>

<style>
  .model-canvas { position:relative; min-width:0; min-height:0; flex:1; }
  .canvas-welcome { pointer-events:none; position:absolute; top:40%; left:50%; transform:translate(-50%,-50%); width:85%; text-align:center; color:var(--color-muted); font-size:12px; line-height:1.8; }
  .canvas-welcome strong { display:block; color:var(--color-ink); font-size:17px; margin-bottom:10px; } .canvas-welcome span { font-size:11px; }
  :global(.svelte-flow) { --xy-background-color:var(--color-paper); --xy-controls-button-background-color:var(--color-paper); --xy-controls-button-background-color-hover:var(--color-surface); --xy-controls-button-color:var(--color-ink); --xy-controls-button-border-color:var(--color-line); --xy-minimap-background-color:var(--color-surface); --xy-minimap-mask-background-color:#64748b22; }
  :global(.svelte-flow__minimap) { width:110px; height:80px; border:1px solid var(--color-line); border-radius:7px; overflow:hidden; }
  :global(.svelte-flow__node.assistant-changed) { outline:2px solid var(--color-accent); outline-offset:3px; border-radius:9px; }
</style>
