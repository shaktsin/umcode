import { addImportedNode, bindingSummary, connectImportedNodes, importedPalette, importNodeLocked, sameImportValue } from './modelImport';
import { decoderDetails, importedLayerTitle } from './importedLayers';
import type { ImportConfig, ImportedGraph, ImportedNode } from './modelImportTypes';
import type { WorkbenchProposal } from './assistant';

const bytes = (s: string) => new TextEncoder().encode(s).length;
const record = (v: unknown): v is Record<string,unknown> => v !== null && typeof v === 'object' && !Array.isArray(v);
const originalMissing = (g: ImportedGraph) => g.source.baselineNodes.filter(b => !g.nodes.some(n => n.id === b.id || n.sourceKey === b.sourceKey));
export function importedChatSummary(g: ImportedGraph, selected: string | null) {
  const node = g.nodes.find(n => n.id === selected);
  if (selected && !node) throw new Error('Select a layer in the current model.');
  const missing = originalMissing(g).map(n => ({id:n.id, sourceKey:n.sourceKey}));
  const bindings = bindingSummary(g);
  const base = {family:g.source.family, mode:'Imported architecture metadata only; no weight values or runtime shape validation.',
    selectedLayer:node ? {id:node.id, title:importedLayerTitle(node,g.source.family), components:decoderDetails(node,g.source.family)} : null,
    missingCount:missing.length, retainedGroups:bindings.retained.length, unusedSourceTensors:bindings.unused.length,
    supportedLayers:importedPalette(g.source.family,g).map(x=>x.kind)};
  const full = JSON.stringify({...base, missingOriginalLayers:missing});
  return bytes(full) <= 16384 ? full : JSON.stringify({...base, selectedLayer:node?.id ?? null, missingOriginalLayers:missing.slice(0,12)});
}

function settings(original: ImportConfig, updates: unknown): ImportConfig {
  if (!record(updates)) throw new Error('Imported settings must be named values.');
  const config = {...original};
  for (const [key,value] of Object.entries(updates)) {
    if (!Object.hasOwn(original,key) || typeof value !== typeof original[key] || !['string','number','boolean'].includes(typeof value) ||
      ['family','readOnly','layerStart','layerEnd','blockStart','blockEnd','stage'].includes(key)) throw new Error('Unsupported imported setting: '+key);
    if (typeof value === 'number' && (!Number.isFinite(value) || Math.abs(value)>1e12 ||
      (/dropout|drop_path/i.test(key) && (value<0 || value>1)) ||
      (/size|heads|channels|features|count|stride|kernel|positions|labels|expansion/i.test(key) && (!Number.isInteger(value)||value<=0||value>1e9)) ||
      (/epsilon|theta/i.test(key) && value<=0))) throw new Error('Invalid value for '+key);
    if (typeof value === 'string' && bytes(value)>2048) throw new Error('Setting too long.');
    config[key] = value as ImportConfig[string];
  }
  return config;
}

export function previewImportedProposal(original: ImportedGraph, proposal: WorkbenchProposal) {
  const family = original.source.family, palette = importedPalette(family,original);
  if (!palette.length) throw new Error('This imported architecture is read-only.');
  let graph = original;
  const changes: string[] = [], affected = new Set<string>(), refs = new Map<string,string>();
  const restoredIds = new Set<string>();
  const title = (n:ImportedNode) => importedLayerTitle(n,family);
  function node(value:unknown) {
    if (typeof value !== 'string') throw new Error('Invalid imported layer reference.');
    const n=graph.nodes.find(n => n.id === (refs.get(value) ?? value));
    if (!n || importNodeLocked(n)) throw new Error('Missing or read-only layer: '+value);
    return n;
  }
  for (const op of proposal.operations as unknown[]) {
    if (!record(op) || typeof op.type !== 'string') throw new Error('Invalid imported operation.');
    const keys = (...fields:string[]) => {
      if (Object.keys(op).some(k => k!=='type' && !fields.includes(k))) throw new Error('Unsupported imported change fields.');
    };
    switch(op.type) {
      case 'restore': {
        keys('nodeId');
        const baseline = originalMissing(graph).find(b => b.id === op.nodeId);
        if (!baseline || baseline.kind === 'unknown' || baseline.config.readOnly === true || graph.nodes.length>=200 || refs.has(baseline.id)) throw new Error('Choose a missing original layer to restore.');
        const index=graph.source.baselineNodes.indexOf(baseline);
        const start=baseline.config.layerStart, end=baseline.config.layerEnd;
        const label = baseline.kind==='transformer' && start !== undefined
          ? (['llama','mistral','qwen2','gpt2'].includes(family)?'Transformer decoder':'Transformer') + (end!==start?' layers '+start+'–'+end:' layer '+start)
          : (baseline.sourceKey ?? baseline.kind);
        const restored:ImportedNode={...baseline,config:{...baseline.config},baselineConfig:{...baseline.config},tensorNames:[...baseline.tensorNames],label,position:{x:index%4*300,y:Math.floor(index/4)*180}};
        graph = {...graph, nodes:[...graph.nodes,restored]}; restoredIds.add(restored.id); affected.add(restored.id); changes.push('Restore '+label+' with its original tensor mappings'); break;
      }
      case 'update': {
        keys('nodeId','config'); const n=node(op.nodeId), config=settings(n.config,op.config);
        if (restoredIds.has(n.id)) throw new Error('Restore the original layer first. Edit its settings in a separate change.');
        for (const [key,value] of Object.entries(config)) if (n.config[key]!==value) changes.push(title(n)+' · '+key+': '+n.config[key]+' → '+value);
        graph={...graph,nodes:graph.nodes.map(x=>x.id===n.id?{...x,config}:x)}; affected.add(n.id); break;
      }
      case 'add': {
        keys('kind','config','ref'); const item=palette.find(p=>p.kind===op.kind);
        if (!item || typeof op.ref!=='string' || !/^[a-zA-Z][\w-]{0,63}$/.test(op.ref) || refs.has(op.ref) || graph.nodes.some(n=>n.id===op.ref) || graph.source.baselineNodes.some(n=>n.id===op.ref)) throw new Error('Unsupported new layer or duplicate reference.');
        graph=addImportedNode(graph,{...item,config:settings(item.config, op.config===undefined ? {} : op.config)});
        const n=graph.nodes.at(-1)!; refs.set(op.ref,n.id); affected.add(n.id); changes.push('Add '+item.label+' without source weights'); break;
      }
      case 'remove': {
        keys('nodeId'); const n=node(op.nodeId);
        graph={...graph,nodes:graph.nodes.filter(x=>x.id!==n.id),edges:graph.edges.filter(e=>e.source!==n.id && e.target!==n.id)};
        changes.push('Remove '+title(n)); affected.add(n.id); break;
      }
      case 'connect': {
        keys('source','target'); const a=node(op.source),b=node(op.target);
        if (a.kind==='output'||b.kind==='input') throw new Error('Invalid direction for input or output.');
        const next=connectImportedNodes(graph,a.id,b.id);
        if (next===graph || next.edges.length>1000) throw new Error('Duplicate connection or connection limit exceeded.');
        graph=next; changes.push('Connect '+title(a)+' → '+title(b)); affected.add(a.id); affected.add(b.id); break;
      }
      case 'disconnect': {
        keys('edgeId'); const e=graph.edges.find(e=>e.id===op.edgeId); if (!e) throw new Error('This connection no longer exists.');
        const a=node(e.source),b=node(e.target); graph={...graph,edges:graph.edges.filter(x=>x.id!==e.id)};
        changes.push('Disconnect '+title(a)+' → '+title(b)); affected.add(a.id); affected.add(b.id); break;
      }
      default: throw new Error('Unsupported imported model change.');
    }
  }
  if (sameImportValue({nodes:graph.nodes,edges:graph.edges},{nodes:original.nodes,edges:original.edges})) throw new Error('This suggestion does not change the model.');
  return {type:'imported' as const, graph, before:bindingSummary(original), after:bindingSummary(graph), changes, affectedNodeIds:[...affected]};
}
