import { inferVisionLayer, visionLayers, visionStarterEntries } from './vision';
export type LayerKind = 'input' | 'embedding' | 'position' | 'linear' | 'attention' | 'transformer' | 'layernorm' | 'gelu' | 'relu' | 'dropout' | 'meanpool' | 'output' | 'conv2d' | 'batchnorm2d' | 'maxpool2d' | 'avgpool2d' | 'globalpool2d' | 'flatten' | 'patchembedding';
export type StarterPreset = 'text' | 'classifier' | 'gpt2' | 'cnn' | 'vit' | 'mlp';
export type Config = Record<string, number | string | boolean>;
export interface LayerNode { id: string; kind: LayerKind; position: { x: number; y: number }; config: Config }
export interface LayerEdge { id: string; source: string; target: string }
export interface Graph { version: 1; format: 'umcode.layers.v1'; nodes: LayerNode[]; edges: LayerEdge[] }
export interface Field { key: string; label: string; type?: 'boolean' | 'mode'; min?: number; max?: number; step?: number }
export interface LayerDefinition { kind: LayerKind; name: string; group: string; description: string; color: string; defaults: Config; fields: Field[] }
const integer = (key: string, label: string, max = 65536): Field => ({ key, label, min: 1, max, step: 1 });
const dropout: Field = { key: 'dropout', label: 'Dropout probability', min: 0, max: .95, step: .05 };
export const layers: LayerDefinition[] = [
  { kind: 'input', name: 'Input', group: 'Input & output', description: 'Token IDs, images, or numeric feature vectors', color: '#64748b', defaults: { mode: 'tokens', sequence: 8, features: 16, channels: 3, height: 32, width: 32 }, fields: [{ key: 'mode', label: 'Input type', type: 'mode' }, integer('sequence', 'Sequence length', 32768), integer('features', 'Input features'), integer('channels', 'Image channels', 512), integer('height', 'Image height', 4096), integer('width', 'Image width', 4096)] },
  { kind: 'embedding', name: 'Embedding', group: 'Core layers', description: 'Turn token IDs into learned vectors', color: '#3b82f6', defaults: { vocab: 32, width: 16 }, fields: [integer('vocab', 'Vocabulary size', 1000000), integer('width', 'Embedding dimensions')] },
  { kind: 'position', name: 'Position embedding', group: 'Attention', description: 'Add learned token positions', color: '#8b5cf6', defaults: { length: 128 }, fields: [integer('length', 'Maximum sequence length', 32768)] },
  { kind: 'linear', name: 'Linear', group: 'Core layers', description: 'Project features into a new dimension', color: '#3b82f6', defaults: { outFeatures: 64, bias: true, tieEmbedding: false }, fields: [integer('outFeatures', 'Output features', 1000000), { key: 'bias', label: 'Include bias', type: 'boolean' }, { key: 'tieEmbedding', label: 'Share token embedding weights', type: 'boolean' }] },
  { kind: 'attention', name: 'Multi-head attention', group: 'Attention', description: 'Attention across text tokens or image patches', color: '#8b5cf6', defaults: { heads: 4, dropout: .1, causal: true }, fields: [integer('heads', 'Attention heads', 256), dropout, { key: 'causal', label: 'Causal mask', type: 'boolean' }] },
  { kind: 'transformer', name: 'Transformer block', group: 'Attention', description: 'Repeat attention + MLP with residuals', color: '#8b5cf6', defaults: { width: 16, heads: 4, expansion: 4, repeat: 2, dropout: .1, causal: true }, fields: [integer('width', 'Model dimensions'), integer('heads', 'Attention heads', 256), integer('expansion', 'MLP expansion', 16), integer('repeat', 'Repeat blocks', 48), dropout, { key: 'causal', label: 'Causal mask', type: 'boolean' }] },
  { kind: 'layernorm', name: 'Layer norm', group: 'Norm & activation', description: 'Normalize the last feature dimension', color: '#10b981', defaults: {}, fields: [] },
  { kind: 'gelu', name: 'GELU', group: 'Norm & activation', description: 'Smooth nonlinear activation', color: '#10b981', defaults: {}, fields: [] },
  { kind: 'relu', name: 'ReLU', group: 'Norm & activation', description: 'Rectified linear activation', color: '#10b981', defaults: {}, fields: [] },
  { kind: 'dropout', name: 'Dropout', group: 'Norm & activation', description: 'Regularize activations during training', color: '#10b981', defaults: { dropout: .1 }, fields: [dropout] },
  { kind: 'meanpool', name: 'Mean pool', group: 'Core layers', description: 'Average the sequence into one vector', color: '#3b82f6', defaults: {}, fields: [] },
  ...visionLayers,
  { kind: 'output', name: 'Output', group: 'Input & output', description: 'Return predictions or features', color: '#ef4444', defaults: {}, fields: [] },
];
export const definition = (kind: LayerKind) => layers.find(l => l.kind === kind)!;
export function visibleFields(node: LayerNode): Field[] {
  if (node.kind !== 'input') return definition(node.kind).fields;
  const keys = node.config.mode === 'image' ? ['mode', 'channels', 'height', 'width'] : node.config.mode === 'features' ? ['mode', 'features'] : ['mode', 'sequence'];
  return definition('input').fields.filter(field => keys.includes(field.key));
}
export function parseNumericSetting(field: Field, raw: string): number {
  const value = Number(raw);
  if (!raw.trim() || !Number.isFinite(value) || value < field.min! || value > field.max! || field.step === 1 && !Number.isInteger(value)) {
    throw new Error(`${field.label} must be ${field.step === 1 ? 'a whole number ' : ''}between ${field.min} and ${field.max}.`);
  }
  return value;
}
export const emptyGraph = (): Graph => ({ version: 1, format: 'umcode.layers.v1', nodes: [], edges: [] });
export const cloneGraph = (g: Graph): Graph => JSON.parse(JSON.stringify(g));
const keysAre = (object: object, allowed: string[]) => Object.keys(object).every(k => allowed.includes(k));
const unsupported = () => new Error('This saved architecture uses layers or settings this visual builder cannot open yet. Your saved model is unchanged. Create a new model to use the visual builder.');

export function readGraph(text: string): Graph {
  let g: any;
  try { g = JSON.parse(text); } catch { throw unsupported(); }
  if (!g || g.version !== 1 || !Array.isArray(g.nodes) || !Array.isArray(g.edges) || !keysAre(g, ['version', 'format', 'nodes', 'edges']) || g.nodes.length > 200 || g.edges.length > 200) throw unsupported();
  if (!g.format && !g.nodes.length && !g.edges.length) return emptyGraph();
  if (g.format !== 'umcode.layers.v1') throw unsupported();
  const ids = new Set<string>();
  for (const n of g.nodes) {
    if (!n || !keysAre(n, ['id', 'kind', 'position', 'config']) || typeof n.id !== 'string' || !/^[a-zA-Z][\w-]{0,63}$/.test(n.id) || ids.has(n.id) || !layers.some(l => l.kind === n.kind) || !n.position || !keysAre(n.position, ['x', 'y']) || !Number.isFinite(n.position.x) || !Number.isFinite(n.position.y) || !n.config || typeof n.config !== 'object') throw unsupported();
    ids.add(n.id);
    const spec = definition(n.kind);
    if (!keysAre(n.config, Object.keys(spec.defaults))) throw unsupported();
    for (const [k, value] of Object.entries(n.config)) {
      if (typeof value !== typeof spec.defaults[k] || typeof value === 'number' && (!Number.isFinite(value) || Math.abs(value) > Number.MAX_SAFE_INTEGER)) throw unsupported();
    }
    n.config = { ...spec.defaults, ...n.config };
  }
  const edgeIds = new Set<string>();
  for (const e of g.edges) {
    if (!e || !keysAre(e, ['id', 'source', 'target']) || typeof e.id !== 'string' || edgeIds.has(e.id) || !ids.has(e.source) || !ids.has(e.target)) throw unsupported();
    edgeIds.add(e.id);
  }
  return g as Graph;
}

export function addLayer(g: Graph, kind: LayerKind, position: { x: number; y: number }): Graph {
  if (g.nodes.length >= 200) throw new Error('This model already has 200 layers.');
  return { ...g, nodes: [...g.nodes, { id: 'layer_' + crypto.randomUUID(), kind, position, config: { ...definition(kind).defaults } }] };
}

export function connectionError(g: Graph, source: string, target: string): string | null {
  const from = g.nodes.find(n => n.id === source), to = g.nodes.find(n => n.id === target);
  if (!from || !to || source === target) return 'Connect two different layers.';
  if (to.kind === 'input' || from.kind === 'output') return 'Connect from an output port to the next layer’s input port.';
  if (g.edges.some(e => e.source === source)) return 'This output is already connected. Remove its connection first.';
  if (g.edges.some(e => e.target === target)) return 'This input is already connected. Remove its connection first.';
  const visited = new Set<string>();
  let cursor: string | undefined = target;
  while (cursor && !visited.has(cursor)) {
    if (cursor === source) return 'That connection would create a loop.';
    visited.add(cursor); cursor = g.edges.find(e => e.source === cursor)?.target;
  }
  return null;
}
export function connectLayers(g: Graph, source: string, target: string): Graph {
  const error = connectionError(g, source, target);
  if (error) throw new Error(error);
  return { ...g, edges: [...g.edges, { id: 'edge_' + crypto.randomUUID(), source, target }] };
}
export function removeLayers(g: Graph, ids: string[], edgeIds: string[] = []): Graph {
  return { ...g, nodes: g.nodes.filter(n => !ids.includes(n.id)), edges: g.edges.filter(e => !ids.includes(e.source) && !ids.includes(e.target) && !edgeIds.includes(e.id)) };
}

export interface Shape { dims: (number | 'B')[]; dtype: 'tokens' | 'float' }
export interface GraphError { nodeId?: string; message: string }
export interface Analysis { errors: GraphError[]; shapes: Record<string, Shape>; parameters: number; order: LayerNode[] }
export function analyzeGraph(g: Graph): Analysis {
  const errors: GraphError[] = [], shapes: Record<string, Shape> = {}, order: LayerNode[] = [];
  let parameters = 0;
  const issue = (message: string, nodeId?: string) => errors.push({ nodeId, message });
  const inputs = g.nodes.filter(n => n.kind === 'input'), outputs = g.nodes.filter(n => n.kind === 'output');
  if (inputs.length !== 1) issue('Add exactly one Input layer.');
  if (outputs.length > 1) issue('Keep exactly one Output layer.');
  for (const edge of g.edges) {
    const other = { ...g, edges: g.edges.filter(e => e.id !== edge.id) };
    const error = connectionError(other, edge.source, edge.target);
    if (error) issue(error, edge.target);
  }
  const visited = new Set<string>();
  let current = inputs[0];
  while (current && !visited.has(current.id)) {
    visited.add(current.id); order.push(current);
    const next = g.edges.find(e => e.source === current.id)?.target;
    current = g.nodes.find(n => n.id === next)!;
  }
  for (const node of g.nodes) if (!visited.has(node.id) && node.kind !== 'output') issue('Connect this layer to the model’s Input-to-Output path.', node.id);
  const last = order.at(-1);
  const lastName = last ? definition(last.kind).name : 'the last layer';
  if (!outputs.length) issue(`Add an Output layer and connect ${lastName} to it.`);
  else if (last?.kind !== 'output') issue(`Connect ${lastName} to the Output layer.`, last?.id);
  for (const n of order) {
    const spec = definition(n.kind), c = n.config;
    const startErrors = errors.length;
    for (const field of spec.fields) {
      const v = c[field.key];
      if (field.type === 'boolean') { if (typeof v !== 'boolean') issue(`${field.label} must be on or off.`, n.id); }
      else if (field.type === 'mode') { if (v !== 'tokens' && v !== 'features' && v !== 'image') issue('Choose token IDs, images, or feature vectors.', n.id); }
      else if (typeof v !== 'number' || !Number.isFinite(v) || v < field.min! || v > field.max! || field.step === 1 && !Number.isInteger(v)) issue(`${field.label} must be ${field.step === 1 ? 'a whole number ' : ''}between ${field.min} and ${field.max}.`, n.id);
    }
    if (errors.length > startErrors) continue;
    const previous = g.edges.find(e => e.target === n.id), input = previous && shapes[previous.source];
    const last = input?.dims.at(-1) as number;
    const width = Number(c.width), vocab = Number(c.vocab), out = Number(c.outFeatures), heads = Number(c.heads);
    if (n.kind === 'input') { shapes[n.id] = { dims: c.mode === 'tokens' ? ['B', Number(c.sequence)] : c.mode === 'image' ? ['B', Number(c.channels), Number(c.height), Number(c.width)] : ['B', Number(c.features)], dtype: c.mode === 'tokens' ? 'tokens' : 'float' }; continue; }
    if (!input) { issue('Connect a valid input to this layer.', n.id); continue; }
    let shape: Shape = { dims: [...input.dims], dtype: input.dtype };
    if (n.kind === 'embedding') {
      if (input.dtype !== 'tokens') issue('Embedding needs token IDs. Connect it directly after a token Input.', n.id);
      else { shape = { dims: [...input.dims, width], dtype: 'float' }; parameters += vocab * width; }
    } else if (n.kind !== 'output' && input.dtype !== 'float') {
      issue('This layer needs feature vectors. Add an Embedding after token Input.', n.id);
    } else if (visionLayers.some(layer => layer.kind === n.kind)) {
      const inferred = inferVisionLayer(n, input);
      if (inferred.error) issue(inferred.error, n.id);
      else { shape = inferred.shape!; parameters += inferred.parameters; }
    } else if (n.kind === 'linear') {
      if (input.dims.length === 4) { issue('Add Flatten or Global average pool before a Linear layer.', n.id); continue; }
      shape.dims[shape.dims.length - 1] = out;
      if (c.tieEmbedding) {
        const embedding = order.find(x => x.kind === 'embedding');
        if (!embedding || embedding.config.width !== last || embedding.config.vocab !== out || c.bias) issue('Shared weights need matching Embedding dimensions/vocabulary and bias turned off.', n.id);
      } else parameters += last * out + (c.bias ? out : 0);
    } else if (n.kind === 'position') {
      if (input.dims.length !== 3) issue('Position embedding needs a sequence of vectors.', n.id);
      else if (Number(input.dims[1]) > Number(c.length)) issue('Maximum sequence length is smaller than the Input sequence.', n.id);
      else parameters += Number(c.length) * last;
    } else if (n.kind === 'attention' || n.kind === 'transformer') {
      if (input.dims.length !== 3) issue('Attention needs a sequence of vectors.', n.id);
      if (last % heads !== 0) issue('Model dimensions must be divisible by attention heads.', n.id);
      if (n.kind === 'transformer') {
        if (last !== width) issue(`Model dimensions (${width}) must match the incoming features (${last}).`, n.id);
        parameters += Number(c.repeat) * ((4 + 2 * Number(c.expansion)) * last * last + (9 + Number(c.expansion)) * last);
      } else parameters += 4 * last * last + 4 * last;
    } else if (n.kind === 'layernorm') {
      if (input.dims.length === 4) issue('Use BatchNorm2d for image channels, or flatten/patch-embed before Layer norm.', n.id);
      else parameters += 2 * last;
    }
    else if (n.kind === 'meanpool') {
      if (input.dims.length !== 3) issue('Mean pool needs a sequence of vectors.', n.id);
      else shape.dims = ['B', last];
    }
    if (errors.length === startErrors) shapes[n.id] = shape;
  }
  return { errors, shapes, parameters, order };
}
export function starterGraph(preset: StarterPreset): Graph {
  const g = emptyGraph(), gpt = preset === 'gpt2';
  const entries: [LayerKind, Config?][] = preset === 'cnn' || preset === 'vit' ? visionStarterEntries(preset) : preset === 'mlp'
    ? [['input', { mode: 'features', features: 16 }], ['linear', { outFeatures: 32 }], ['relu'], ['linear', { outFeatures: 3 }], ['output']] : preset === 'classifier'
    ? [['input'], ['embedding'], ['meanpool'], ['linear', { outFeatures: 16 }], ['relu'], ['linear', { outFeatures: 3 }], ['output']]
    : [['input', { sequence: gpt ? 512 : 8 }], ['embedding', { vocab: gpt ? 50257 : 32, width: gpt ? 768 : 16 }], ['position', { length: gpt ? 1024 : 128 }], ['transformer', { width: gpt ? 768 : 16, heads: gpt ? 12 : 4, repeat: gpt ? 12 : 2 }], ['layernorm'], ['linear', { outFeatures: gpt ? 50257 : 32, bias: false, tieEmbedding: true }], ['output']];
  entries.forEach(([kind, config], i) => {
    g.nodes.push({ id: `layer_${i}`, kind, position: { x: 80, y: i * 130 }, config: { ...definition(kind).defaults, ...config } });
    if (i) g.edges.push({ id: `edge_${i}`, source: `layer_${i - 1}`, target: `layer_${i}` });
  });
  return g;
}
export const shapeLabel = (shape?: Shape) => shape ? `[${shape.dims.join(' × ')}]` : 'Not connected';
