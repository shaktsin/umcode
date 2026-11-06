import type { ImportFile, ImportedGraph, ImportedNode, ImportedPaletteItem } from './modelImportTypes';
const MiB = 1024 * 1024;
const jsonLimit = 2 * MiB, headerLimit = 8 * MiB, metadataLimit = 24 * MiB;
const encoder = new TextEncoder();
function safePath(name: string) {
  if (!name || name.length > 1024 || /[\\\x00-\x1f\x7f:]/.test(name) || name.startsWith('/') || name.split('/').some(part => !part || part === '.' || part === '..')) throw new Error(`Unsafe model file path: ${name}`);
  return name;
}
function kindOf(name: string): ImportFile['kind'] | null {
  const base = name.split('/').at(-1)!;
  if (base === 'config.json') return 'config';
  if (base.endsWith('.safetensors.index.json')) return 'index';
  if (base.endsWith('.safetensors')) return 'weights';
  if (['tokenizer.json', 'tokenizer_config.json', 'special_tokens_map.json', 'vocab.json', 'vocab.txt', 'merges.txt', 'added_tokens.json', 'sentencepiece.bpe.model', 'tokenizer.model'].includes(base)) return 'tokenizer';
  if (['processor_config.json', 'preprocessor_config.json', 'feature_extractor_config.json'].includes(base)) return 'processor';
  return null;
}
function objectJSON(text: string, label: string): Record<string, unknown> {
  try { const value = JSON.parse(text); if (!value || typeof value !== 'object' || Array.isArray(value)) throw new Error(); return value; }
  catch { throw new Error(`Malformed ${label}: expected a JSON object.`); }
}
/** Never reads weight payloads. Even length validation happens before the bounded second slice. */
export async function readModelFiles(files: File[], current: () => boolean = () => true): Promise<ImportFile[]> {
  if (!files.length) throw new Error('Choose config.json and safetensors weights together.');
  const names = files.map(file => safePath(file.webkitRelativePath || file.name));
  // Strip exactly one folder-picker root, never arbitrary common directory segments.
  const first = names[0].split('/')[0];
  const strip = files.every((file, i) => !!file.webkitRelativePath && names[i].startsWith(`${first}/`));
  const selected = files.map((file, i) => { const name = strip ? names[i].slice(first.length + 1) : names[i]; return { file, name, kind: kindOf(name) }; }).filter((entry): entry is { file: File; name: string; kind: ImportFile['kind'] } => !!entry.kind);
  const seen = new Set<string>();
  for (const { file, name } of selected) { if (seen.has(name)) throw new Error(`Duplicate selected file: ${name}`); seen.add(name); if (!Number.isSafeInteger(file.size) || file.size < 0) throw new Error(`Invalid file size: ${name}`); }
  const configs = selected.filter(f => f.kind === 'config');
  if (configs.length !== 1) throw new Error('Select exactly one config.json (duplicate configs are not supported).');
  if (!selected.some(f=>f.kind==='weights')) throw new Error('Missing safetensors weights. Select them together with config.json or choose the complete model folder.');
  if (selected.filter(f => f.kind === 'index').length > 1) throw new Error('Select at most one safetensors index.');
  if (selected.filter(f => f.kind === 'weights').length > 128) throw new Error('A model import supports at most 128 weight files.');
  let metadata = 0;
  const result: ImportFile[] = [];
  for (const { file, name, kind } of selected) {
    if (!current()) throw new Error('Model inspection was cancelled.');
    const entry: ImportFile = { name, size: file.size, kind };
    if (kind === 'config' || kind === 'index') {
      if (file.size > jsonLimit) throw new Error(`${name} exceeds the 2 MiB JSON limit.`);
      entry.content = new TextDecoder('utf-8', { fatal: true }).decode(await file.slice(0, file.size).arrayBuffer());
      objectJSON(entry.content, name); metadata += encoder.encode(entry.content).byteLength;
    } else if (kind === 'weights') {
      if (file.size < 8) throw new Error(`Invalid safetensors header: ${name} is shorter than eight bytes.`);
      const prefix = await file.slice(0, 8).arrayBuffer();
      if (!current()) throw new Error('Model inspection was cancelled.');
      if (prefix.byteLength !== 8) throw new Error(`Truncated safetensors header: ${name}`);
      const length = new DataView(prefix).getBigUint64(0, true);
      if (length < 2n || length > BigInt(headerLimit) || length + 8n > BigInt(file.size)) throw new Error(`Invalid safetensors header length in ${name} (limit 8 MiB).`);
      if (metadata + Number(length) > metadataLimit) throw new Error('Selected model metadata exceeds the 24 MiB limit.');
      const bytes = await file.slice(8, 8 + Number(length)).arrayBuffer();
      if (bytes.byteLength !== Number(length)) throw new Error(`Truncated safetensors header: ${name}`);
      try { entry.header = new TextDecoder('utf-8', { fatal: true }).decode(bytes); objectJSON(entry.header, 'safetensors header'); }
      catch { throw new Error(`Malformed safetensors header: ${name}`); }
      metadata += bytes.byteLength;
    }
    metadata += encoder.encode(name).byteLength + 64;
    if (metadata > metadataLimit) throw new Error('Selected model metadata exceeds the 24 MiB limit.');
    result.push(entry);
  }
  const index = result.find(f => f.kind === 'index');
  if (index) {
    const map = objectJSON(index.content!, index.name).weight_map;
    if (!map || typeof map !== 'object' || Array.isArray(map) || !Object.keys(map).length) throw new Error('Safetensors index must contain a nonempty weight_map.');
    const weights = new Set(result.filter(f => f.kind === 'weights').map(f => f.name));
    for (const name of Object.values(map)) { if (typeof name !== 'string') throw new Error('Invalid shard name in safetensors index.'); safePath(name); if (!weights.has(name)) throw new Error(`Missing shard required by index: ${name}`); }
  }
  if (!current()) throw new Error('Model inspection was cancelled.');
  return result;
}
export function isImportedGraph(text: string): boolean { try { return JSON.parse(text)?.format === 'umcode.imported.v1'; } catch { return false; } }
export function parseImportedGraph(text: string): ImportedGraph {
  const graph = JSON.parse(text) as ImportedGraph;
  if (graph?.format !== 'umcode.imported.v1' || graph.version !== 1 || !graph.source || !Array.isArray(graph.nodes) || !Array.isArray(graph.edges) || !Array.isArray(graph.source.tensors)) throw new Error('This is not a supported imported architecture.');
  if (graph.nodes.length > 200) throw new Error('Imported architectures support at most 200 nodes.');
  return graph;
}
function canonical(value: unknown): string {
  if (Array.isArray(value)) return '[' + value.map(canonical).join(',') + ']';
  if (value && typeof value === 'object') return '{' + Object.entries(value).sort(([a], [b]) => a.localeCompare(b)).map(([key, item]) => JSON.stringify(key) + ':' + canonical(item)).join(',') + '}';
  return JSON.stringify(value);
}
export const sameImportValue = (a: unknown, b: unknown) => canonical(a) === canonical(b);
export const importNodeLocked = (n: ImportedNode) => n.kind === 'unknown' || n.config.readOnly === true;
export function bindingSummary(graph: ImportedGraph) {
  const retained: string[] = [], needsWeights: string[] = []; const used = new Set<string>();
  for (const node of graph.nodes) {
    const baseline = graph.source.baselineNodes?.find(n => n.id === node.id);
    const unchanged = !!baseline && !!node.sourceKey && node.sourceKey === baseline.sourceKey && node.kind === baseline.kind && sameImportValue(node.config, baseline.config) && sameImportValue([...node.tensorNames].sort(), [...baseline.tensorNames].sort());
    if (unchanged && node.tensorNames.length) { retained.push(node.id); node.tensorNames.forEach(name => used.add(name)); }
    else if (node.kind !== 'input' && node.kind !== 'output' && !(node.kind === 'pool' && node.tensorNames.length === 0 && ['global average pooling', 'CLS token selection', 'last non-padding token'].includes(String(node.config.operation)))) needsWeights.push(node.id);
  }
  return { retained, needsWeights, unused: graph.source.tensors.filter(t => !used.has(t.name)).map(t => t.name) };
}
const displayNames: Record<string, string> = { gpt2: 'GPT-2', llama: 'Llama', mistral: 'Mistral', qwen2: 'Qwen2', bert: 'BERT', vit: 'ViT', resnet: 'ResNet', convnext: 'ConvNeXT' };
export function importedPalette(family: string, graph?: ImportedGraph): ImportedPaletteItem[] {
  const key = family.toLowerCase(), name = displayNames[key]; if (!name) return [];
  const rms = ['llama', 'mistral', 'qwen2'].includes(key);
  const palette: ImportedPaletteItem[] = key === 'resnet' || key === 'convnext' ? [
    { kind: 'conv2d', label: `${name} convolution`, description: 'Spatial convolution with explicit channels, kernel and stride.', config: { channels: 64, outChannels: 64, kernelSize: 3, stride: 1, padding: 1, bias: false } },
    { kind: 'cnnblock', label: `${name} block`, description: key === 'resnet' ? 'ResNet residual block, native BatchNorm, activation and shortcut semantics.' : 'ConvNeXT depthwise convolution, channels-last LayerNorm and channel MLP residual block.', config: key === 'resnet' ? { family: key, blockType: 'bottleneck', outChannels: 256, repeatCount: 1, normalization: 'BatchNorm2d', activation: 'relu', residual: true, downsampleInFirstStage: false, downsampleInBottleneck: false } : { family: key, outChannels: 96, depthwiseKernel: 7, mlpExpansion: 4, repeatCount: 1, normalization: 'channels-last LayerNorm', activation: 'gelu', layerScaleInit: 0.000001, residual: true } },
    { kind: 'normalization', label: key === 'resnet' ? 'BatchNorm2d' : 'LayerNorm', description: `${name} native normalization, retained as architecture metadata.`, config: { normalization: key === 'resnet' ? 'BatchNorm2d' : 'LayerNorm', hiddenSize: 64 } },
    { kind: 'pool', label: 'Global average pool', description: 'Spatial global average pooling.', config: { operation: 'global average pooling' } },
    { kind: 'linear', label: `${name} classifier`, description: 'Classification projection.', config: { inFeatures: 64, numLabels: 1000 } },
  ] : [
    { kind: 'embedding', label: key === 'vit' ? 'ViT patch embedding' : `${name} embedding`, description: key === 'vit' ? 'ViT patch projection, CLS token and learned position embeddings.' : `${name} token embedding.`, config: key === 'vit' ? { imageSize: 224, patchSize: 16, channels: 3, hiddenSize: 768 } : { vocabSize: 32000, hiddenSize: 768 } },
    { kind: 'transformer', label: `${name} block`, description: `${name} native attention, MLP, normalization and residual semantics.`, config: { family: key, hiddenSize: 768, intermediateSize: 3072, attentionHeads: 12, repeatCount: 1, residual: true, attention: key === 'bert' || key === 'vit' ? 'bidirectional multi-head self-attention' : rms ? 'causal grouped-query self-attention' : 'causal multi-head self-attention', normalization: rms ? 'pre RMSNorm' : key === 'bert' ? 'post LayerNorm' : 'pre LayerNorm', activation: key === 'gpt2' ? 'gelu_new' : rms ? 'silu' : 'gelu', ...(key === 'gpt2' ? { projection: 'Conv1D (transposed linear weights)', scaleAttentionWeights: true, layerNormEpsilon: 0.00001 } : rms ? { keyValueHeads: 12, rmsNormEpsilon: 0.000001, positions: 'rotary (RoPE)', ropeTheta: 10000, mlp: 'gated MLP (SwiGLU)', attentionBias: key === 'qwen2' } : { layerNormEpsilon: 1e-12 }) } },
    { kind: 'normalization', label: rms ? 'RMSNorm' : 'LayerNorm', description: `${name} native normalization.`, config: { hiddenSize: 768, normalization: rms ? 'RMSNorm' : 'LayerNorm' } },
    { kind: 'linear', label: key === 'vit' || key === 'bert' ? 'Classification head' : 'Language model head', description: 'Output projection; no weight payload is imported.', config: { hiddenSize: 768, tiedEmbeddings: key === 'gpt2' } },
  ];
  // Use immutable native settings from this model as templates, never the smaller
  // executable builder palette. A new node has no source key or tensor bindings.
  return palette.map(entry => {
    const item = entry.kind === 'transformer' && rms ? { ...entry, label: 'Transformer decoder layer' } : entry;
    const baseline = graph?.source.baselineNodes?.find(node => node.kind === item.kind);
    if (!baseline) return item;
    const config = { ...baseline.config };
    for (const key of ['layerStart', 'layerEnd', 'blockStart', 'blockEnd', 'stage']) delete config[key];
    if ('repeatCount' in config) config.repeatCount = 1;
    return { ...item, config };
  });
}
export function addImportedNode(graph: ImportedGraph, item: ImportedPaletteItem, position?: { x: number; y: number }): ImportedGraph {
  if (graph.nodes.length >= 200) throw new Error('Imported architectures support at most 200 nodes.');
  const id = `added-${globalThis.crypto?.randomUUID?.() ?? Math.random().toString(36).slice(2)}`;
  const node: ImportedNode = { id, kind: item.kind, label: item.label, position: position ?? { x: 80 + graph.nodes.length % 3 * 240, y: 100 + graph.nodes.length * 25 }, config: { ...item.config }, baselineConfig: {}, tensorNames: [] };
  return { ...graph, nodes: [...graph.nodes, node] };
}
export function connectImportedNodes(graph: ImportedGraph, source: string, target: string): ImportedGraph {
  if (source === target || !graph.nodes.some(n => n.id === source) || !graph.nodes.some(n => n.id === target)) throw new Error('Connect two different layers.');
  if (graph.edges.some(e => e.source === source && e.target === target)) return graph;
  const reaches = (id: string, seen = new Set<string>()): boolean => { if (id === source) return true; if (seen.has(id)) return false; seen.add(id); return graph.edges.filter(e => e.source === id).some(e => reaches(e.target, seen)); };
  if (reaches(target)) throw new Error('This connection would create a cycle.');
  return { ...graph, edges: [...graph.edges, { id: `${source}-${target}`, source, target }] };
}
