import { isImportedGraph, parseImportedGraph } from './modelImport';
import { importedChatSummary, previewImportedProposal } from './importedAssistant';
import { addLayer, analyzeGraph, cloneGraph, connectLayers, definition, layers, readGraph, removeLayers, shapeLabel, type Analysis, type Config, type Graph, type LayerKind, type LayerNode } from './graph';

export interface WorkbenchContext {
  projectId: string; modelId: string; modelName: string; graphJson: string;
  revision: string; selectedNodeId?: string; summary: string;
}
export type WorkbenchOperation =
  | { type: 'update'; nodeId: string; config: Config }
  | { type: 'add'; ref: string; kind: LayerKind | 'normalization' | 'pool' | 'cnnblock'; config?: Config }
  | { type: 'restore'; nodeId: string }
  | { type: 'remove'; nodeId: string }
  | { type: 'connect'; source: string; target: string }
  | { type: 'disconnect'; edgeId: string };
export interface WorkbenchProposal {
  projectId: string; modelId: string; modelName: string; revision: string;
  explanation: string; operations: WorkbenchOperation[];
}
export interface ProposalPreview {
  graph: Graph; before: Analysis; after: Analysis; changes: string[]; affectedNodeIds: string[];
}
const bytes = (value: string) => new TextEncoder().encode(value).byteLength;
const record = (value: unknown): value is Record<string, unknown> => !!value && typeof value === 'object' && !Array.isArray(value);
const exactKeys = (value: Record<string, unknown>, keys: string[]) => Object.keys(value).every(key => keys.includes(key));
const identifier = (value: unknown): value is string => typeof value === 'string' && /^[a-zA-Z][\w-]{0,63}$/.test(value);

export async function graphRevision(text: string): Promise<string> {
  const digest = await crypto.subtle.digest('SHA-256', new TextEncoder().encode(text));
  return [...new Uint8Array(digest)].map(byte => byte.toString(16).padStart(2, '0')).join('');
}

export async function buildContext(projectId: string, model: { id: string; name: string }, text: string, selectedNodeId: string | null): Promise<WorkbenchContext> {
  if (bytes(text) > 1048576) throw new Error('The model is too large to include in chat.');
  if (isImportedGraph(text)) {
    const summary = importedChatSummary(parseImportedGraph(text), selectedNodeId);
    if (bytes(summary) > 16384) throw new Error('The model context is too large to include in chat.');
    return { projectId, modelId:model.id, modelName:model.name, graphJson:text, revision:await graphRevision(text), ...(selectedNodeId ? {selectedNodeId} : {}), summary };
  }
  const graph = readGraph(text), analysis = analyzeGraph(graph);
  if (selectedNodeId && !graph.nodes.some(node => node.id === selectedNodeId)) throw new Error('Select a layer in the current model.');
  const summary = JSON.stringify({
    parameters: analysis.parameters,
    selectedLayer: graph.nodes.find(node => node.id === selectedNodeId) ?? null,
    shapes: Object.fromEntries(Object.entries(analysis.shapes).map(([id, shape]) => [id, shapeLabel(shape)])),
    issues: analysis.errors,
    supportedLayers: layers.map(layer => ({ kind: layer.kind, defaults: layer.defaults, fields: layer.fields })),
  });
  // The exact graph is never truncated; derived context can be reconstructed from it.
  const compact = bytes(summary) <= 16384 ? summary : JSON.stringify({ parameters: analysis.parameters, selectedNodeId, issues: analysis.errors.slice(0, 8), supportedLayers: layers.map(layer => ({ kind: layer.kind, defaults: layer.defaults, fields: layer.fields })) });
  if (bytes(compact) > 16384) throw new Error('The model context is too large to include in chat.');
  return { projectId, modelId: model.id, modelName: model.name, graphJson: text, revision: await graphRevision(text), ...(selectedNodeId ? { selectedNodeId } : {}), summary: compact };
}

function settings(node: LayerNode, value: unknown): Config {
  const spec = definition(node.kind);
  if (!record(value)) throw new Error('Layer settings must be named values.');
  const next = { ...node.config };
  for (const [key, entry] of Object.entries(value)) {
    if (!Object.hasOwn(spec.defaults, key) || typeof entry !== typeof spec.defaults[key]) throw new Error(`Unsupported ${spec.name} setting: ${key}.`);
    const field = spec.fields.find(field => field.key === key);
    if (typeof entry === 'number' && (!Number.isFinite(entry) || !field || entry < field.min! || entry > field.max! || field.step === 1 && !Number.isInteger(entry))) throw new Error(`${field?.label ?? key} is outside its supported range.`);
    if (field?.type === 'mode' && !['tokens', 'image', 'features'].includes(String(entry))) throw new Error('Unsupported input type.');
    next[key] = entry as number | string | boolean;
  }
  return next;
}

function validateProposal(proposal: WorkbenchProposal) {
  if (!record(proposal) || !Array.isArray(proposal.operations) || !proposal.operations.length || proposal.operations.length > 32 || bytes(JSON.stringify(proposal)) > 65536) throw new Error('A suggestion must contain 1–32 supported changes within 64 KiB.');
}
export function previewWorkbenchProposal(text:string, proposal:WorkbenchProposal) {
  if (!isImportedGraph(text)) return { type:'native' as const, ...previewProposal(text,proposal) };
  validateProposal(proposal);
  return previewImportedProposal(parseImportedGraph(text),proposal);
}
export function previewProposal(text: string, proposal: WorkbenchProposal): ProposalPreview {
  validateProposal(proposal);
  let graph = cloneGraph(readGraph(text));
  const before = analyzeGraph(graph), changes: string[] = [], affected = new Set<string>(), refs = new Map<string, string>();
  function resolve(value: unknown): string {
    if (!identifier(value)) throw new Error('Invalid layer reference.');
    const id = refs.get(value) ?? value;
    if (!graph.nodes.some(node => node.id === id)) throw new Error(`Layer ${value} is no longer available.`);
    return id;
  }
  for (const operation of proposal.operations as unknown[]) {
    if (!record(operation) || typeof operation.type !== 'string') throw new Error('Unsupported model change.');
    if (operation.type === 'update') {
      if (!exactKeys(operation, ['type', 'nodeId', 'config'])) throw new Error('Unsupported setting change fields.');
      const id = resolve(operation.nodeId), node = graph.nodes.find(node => node.id === id)!, config = settings(node, operation.config);
      for (const [key, value] of Object.entries(config)) if (value !== node.config[key]) changes.push(`${definition(node.kind).name} · ${definition(node.kind).fields.find(field => field.key === key)?.label ?? key}: ${node.config[key]} → ${value}`);
      graph = { ...graph, nodes: graph.nodes.map(node => node.id === id ? { ...node, config } : node) }; affected.add(id);
    } else if (operation.type === 'add') {
      if (!exactKeys(operation, ['type', 'ref', 'kind', 'config']) || !identifier(operation.ref) || refs.has(operation.ref) || graph.nodes.some(node => node.id === operation.ref) || typeof operation.kind !== 'string' || !layers.some(layer => layer.kind === operation.kind)) throw new Error('Unsupported new layer or duplicate reference.');
      const kind = operation.kind as LayerKind;
      graph = addLayer(graph, kind, { x: 340, y: graph.nodes.length * 130 });
      const node = graph.nodes.at(-1)!;
      node.config = settings(node, operation.config ?? {}); refs.set(operation.ref, node.id); affected.add(node.id);
      changes.push(`Add ${definition(kind).name}`);
    } else if (operation.type === 'remove') {
      if (!exactKeys(operation, ['type', 'nodeId'])) throw new Error('Unsupported removal fields.');
      const id = resolve(operation.nodeId), node = graph.nodes.find(node => node.id === id)!;
      changes.push(`Remove ${definition(node.kind).name}`); affected.add(id); graph = removeLayers(graph, [id]);
    } else if (operation.type === 'connect') {
      if (!exactKeys(operation, ['type', 'source', 'target'])) throw new Error('Unsupported connection fields.');
      const source = resolve(operation.source), target = resolve(operation.target);
      graph = connectLayers(graph, source, target); affected.add(source); affected.add(target); changes.push('Connect layers');
    } else if (operation.type === 'disconnect') {
      if (!exactKeys(operation, ['type', 'edgeId']) || typeof operation.edgeId !== 'string') throw new Error('Unsupported disconnection fields.');
      const edge = graph.edges.find(edge => edge.id === operation.edgeId);
      if (!edge) throw new Error('This connection is no longer available.');
      graph = removeLayers(graph, [], [edge.id]); affected.add(edge.source); affected.add(edge.target); changes.push('Disconnect layers');
    } else throw new Error('Unsupported model change.');
  }
  graph = readGraph(JSON.stringify(graph));
  if (JSON.stringify(graph) === JSON.stringify(readGraph(text))) throw new Error('This suggestion does not change the model.');
  return { graph, before, after: analyzeGraph(graph), changes, affectedNodeIds: [...affected] };
}

export function proposalFromData(data: unknown): WorkbenchProposal | null {
  if (!record(data) || !record(data.workbenchProposal)) return null;
  const p = data.workbenchProposal;
  if (typeof p.projectId !== 'string' || typeof p.modelId !== 'string' || typeof p.modelName !== 'string' || typeof p.revision !== 'string' || !/^[a-f0-9]{64}$/.test(p.revision) || typeof p.explanation !== 'string' || !Array.isArray(p.operations)) return null;
  return p as unknown as WorkbenchProposal;
}
export function contextFromData(data: unknown): WorkbenchContext | null {
  if (!record(data) || !record(data.workbenchContext)) return null;
  const c = data.workbenchContext;
  return typeof c.modelName === 'string' && typeof c.modelId === 'string' ? c as unknown as WorkbenchContext : null;
}
