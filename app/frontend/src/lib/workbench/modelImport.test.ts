import { describe, expect, it } from 'vitest';
import { readModelFiles, bindingSummary, importedPalette, parseImportedGraph } from './modelImport';
import type { ImportedGraph } from './modelImportTypes';
function file(name: string, content: string | Uint8Array, size?: number, path = ''): File {
  const blob = new Blob([typeof content === 'string' ? content : content as BlobPart]);
  return { name, size: size ?? blob.size, webkitRelativePath: path, slice: (start?: number, end?: number) => blob.slice(start, end), arrayBuffer: () => { throw new Error('whole-file read forbidden'); } } as unknown as File;
}
function weight(header = '{"w":{"dtype":"F32","shape":[1],"data_offsets":[0,4]}}', declared?: bigint) {
  const bytes = new TextEncoder().encode(header); const all = new Uint8Array(bytes.length + 8);
  new DataView(all.buffer).setBigUint64(0, declared ?? BigInt(bytes.length), true); all.set(bytes, 8); return all;
}
function graph(): ImportedGraph {
  const baseline = { id: 'block', kind: 'transformer', config: { hidden_size: 4 }, tensorNames: ['transformer.h.0.w'], sourceKey: 'transformer.h.0' };
  return { version: 1, format: 'umcode.imported.v1', source: { family: 'gpt2', config: { model_type: 'gpt2' }, files: [{ name: 'model.safetensors', size: 128 }], tensors: [{ name: 'transformer.h.0.w', dtype: 'F32', shape: [1], file: 'model.safetensors', bytes: 4 }], weightsVerified: true, warnings: [], baselineNodes: [baseline] }, nodes: [{ ...baseline, config: { ...baseline.config }, tensorNames: [...baseline.tensorNames], label: 'GPT-2 decoder', position: { x: 0, y: 0 }, baselineConfig: { hidden_size: 4 } }], edges: [] };
}

describe('bounded model metadata ingestion', () => {
  it('reads only the length and header slices even for huge weights', async () => {
    const reads: [number | undefined, number | undefined][] = [];
    const w = file('model.safetensors', weight(), 90_000_000_000, 'chosen/model.safetensors');
    const original = w.slice; w.slice = (start, end) => { reads.push([start, end]); return original(start, end); };
    const result = await readModelFiles([file('config.json', '{"model_type":"gpt2"}', undefined, 'chosen/config.json'), w, file('README.md', 'ignored', undefined, 'chosen/README.md'), file('tokenizer.json', 'not read', undefined, 'chosen/tokenizer.json')]);
    expect(reads).toEqual([[0, 8], [8, 62]]);
    expect(result.map(f => f.name)).toEqual(['config.json', 'model.safetensors', 'tokenizer.json']);
    expect(result[1].size).toBe(90_000_000_000); expect(result[2]).toEqual({ name: 'tokenizer.json', size: 8, kind: 'tokenizer' });
    expect(result[1].header).toContain('data_offsets'); expect(result[1].content).toBeUndefined();
  });
  it('rejects either file alone and duplicate essential basenames', async () => {
    await expect(readModelFiles([file('config.json', '{}')])).rejects.toThrow(/weights/i);
    await expect(readModelFiles([file('model.safetensors', weight())])).rejects.toThrow(/config.json/);
    await expect(readModelFiles([file('config.json', '{}'), file('config.json', '{}', undefined, 'nested/config.json')])).rejects.toThrow(/duplicate|one config/i);
  });
  it('rejects unsafe paths, oversized JSON and bad header lengths before reading payloads', async () => {
    for (const path of ['../config.json', '/config.json', 'a\\config.json', 'a/../config.json']) await expect(readModelFiles([file('config.json', '{}', undefined, path)])).rejects.toThrow(/path/i);
    await expect(readModelFiles([file('config.json', '{}', 2 * 1024 * 1024 + 1),file('model.safetensors',weight())])).rejects.toThrow(/2 MiB/);
    for (const bytes of [weight('{}', 8_388_609n), weight('{}', 2n ** 63n), weight('not json'), new Uint8Array(7)]) await expect(readModelFiles([file('config.json', '{}'), file('model.safetensors', bytes)])).rejects.toThrow(/header|safetensors/i);
    await expect(readModelFiles([file('config.json', '{}'), ...Array.from({ length: 129 }, (_, i) => file(`${i}.safetensors`, weight()))])).rejects.toThrow(/128/);
  });
  it('requires every shard named by the index and rejects duplicate indices', async () => {
    const index = file('model.safetensors.index.json', '{"weight_map":{"w":"part-1.safetensors","v":"part-2.safetensors"}}');
    await expect(readModelFiles([file('config.json', '{}'), index, file('part-1.safetensors', weight())])).rejects.toThrow(/part-2/);
    await expect(readModelFiles([file('config.json', '{}'), file('part-1.safetensors', weight()), index, file('other.safetensors.index.json', '{}')])).rejects.toThrow(/index/i);
  });
  it('rejects aggregate headers beyond 24 MiB', async () => {
    const padded = '{"__metadata__":{"padding":"' + ' '.repeat(6 * 1024 * 1024) + '"}}';
    await expect(readModelFiles([file('config.json', '{}'), ...Array.from({ length: 4 }, (_, i) => file(`${i}.safetensors`, weight(padded)))])).rejects.toThrow(/24 MiB/);
  });
});

describe('source binding compatibility', () => {
  it.each(['CLS token selection', 'last non-padding token'])('does not request weights for stateless %s', operation => {
    const g = graph();
    g.nodes = [{ ...g.nodes[0], id: 'head_pool', kind: 'pool', sourceKey: undefined, tensorNames: [], config: { operation } }];
    expect(bindingSummary(g).needsWeights).toEqual([]);
  });
  it('does not request weights for stateless global pooling but preserves learned BERT pooler requirements', () => {
    const g = graph();
    g.nodes = [{ ...g.nodes[0], id: 'pool', kind: 'pool', sourceKey: undefined, tensorNames: [], config: { operation: 'global average pooling' } }];
    expect(bindingSummary(g).needsWeights).toEqual([]);
    g.nodes[0].config = { hiddenSize: 4, activation: 'tanh' };
    expect(bindingSummary(g).needsWeights).toEqual(['pool']);
  });
  it('retains mappings across display edits but detects changed kind, dimensions and bindings', () => {
    const g = graph(); expect(bindingSummary(g).retained).toEqual(['block']);
    g.nodes[0].label = 'renamed'; g.nodes[0].position.x = 99; expect(bindingSummary(g).retained).toEqual(['block']);
    for (const edit of [(g: ImportedGraph) => g.nodes[0].kind = 'linear', (g: ImportedGraph) => g.nodes[0].config.hidden_size = 8, (g: ImportedGraph) => g.nodes[0].sourceKey = 'fake', (g: ImportedGraph) => g.nodes[0].tensorNames = []]) { const changed = graph(); edit(changed); expect(bindingSummary(changed).needsWeights).toEqual(['block']); expect(bindingSummary(changed).retained).toEqual([]); }
  });
  it('marks new nodes and removed bindings after serialization/reload', () => {
    const g = graph(); g.nodes = [{ ...g.nodes[0], id: 'new', sourceKey: undefined, tensorNames: [], baselineConfig: {} }];
    const restored = parseImportedGraph(JSON.stringify(g)); expect(bindingSummary(restored).needsWeights).toEqual(['new']); expect(bindingSummary(restored).unused).toEqual(['transformer.h.0.w']);
  });
  it('provides family-native palettes and no guessed unknown architecture', () => {
    expect(importedPalette('gpt2').find(n => n.kind === 'transformer')?.description).toMatch(/GPT-2/);
    expect(importedPalette('resnet').find(n => n.kind === 'cnnblock')?.description).toMatch(/residual/i);
    expect(importedPalette('not-known')).toEqual([]);
  });
});
