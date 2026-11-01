import { describe, expect, it } from 'vitest';
import { analyzeGraph, readGraph, starterGraph } from './graph';
import { generatePython } from './codegen';

describe('vision architecture', () => {
  it('builds a CNN with image shapes and counts convolution, batch norm and classifier parameters', () => {
    const g = starterGraph('cnn');
    const a = analyzeGraph(g);
    expect(a.errors).toEqual([]);
    expect(a.shapes[g.nodes[0].id].dims).toEqual(['B', 3, 32, 32]);
    expect(a.shapes[g.nodes.at(-1)!.id].dims).toEqual(['B', 10]);
    expect(a.parameters).toBe(5450);
    expect(generatePython(g).code).toContain('nn.Conv2d(3, 16');
    expect(generatePython(g).code).toContain('nn.BatchNorm2d(16)');
  });

  it('builds a vision Transformer using patch tokens and noncausal attention', () => {
    const g = starterGraph('vit');
    const a = analyzeGraph(g), patch = g.nodes.find(n => n.kind === 'patchembedding')!;
    expect(a.errors).toEqual([]);
    expect(a.shapes[patch.id].dims).toEqual(['B', 64, 32]);
    expect(a.shapes[g.nodes.at(-1)!.id].dims).toEqual(['B', 10]);
    expect(a.parameters).toBe(29418);
    expect(g.nodes.find(n => n.kind === 'transformer')!.config.causal).toBe(false);
    expect(generatePython(g).code).toContain('TransformerBlock(32, 4, 4, 0.1, False)');
  });

  it('matches convolution and pooling floor dimensions for odd rectangular images', () => {
    const g = starterGraph('cnn');
    Object.assign(g.nodes[0].config, { height: 31, width: 29 });
    const conv = g.nodes.find(n => n.kind === 'conv2d')!, pool = g.nodes.find(n => n.kind === 'maxpool2d')!;
    conv.config.stride = 2;
    let a = analyzeGraph(g);
    expect(a.errors).toEqual([]);
    expect(a.shapes[conv.id].dims).toEqual(['B', 16, 16, 15]);
    expect(a.shapes[pool.id].dims).toEqual(['B', 16, 8, 7]);
    pool.kind = 'avgpool2d';
    Object.assign(pool.config, { kernel: 3, stride: 2, padding: 1 });
    a = analyzeGraph(g);
    expect(a.errors).toEqual([]);
    expect(a.shapes[pool.id].dims).toEqual(['B', 16, 8, 8]);
  });

  it('rejects nonpositive spatial outputs and unsupported pooling padding', () => {
    const g = starterGraph('cnn'), conv = g.nodes.find(n => n.kind === 'conv2d')!, pool = g.nodes.find(n => n.kind === 'maxpool2d')!;
    Object.assign(g.nodes[0].config, { height: 2, width: 2 });
    Object.assign(conv.config, { kernel: 5, padding: 0 });
    expect(analyzeGraph(g).errors).toContainEqual(expect.objectContaining({ nodeId: conv.id, message: expect.stringContaining('too large') }));
    Object.assign(g.nodes[0].config, { height: 32, width: 32 });
    Object.assign(conv.config, { kernel: 3, padding: 1 });
    pool.config.padding = 2;
    expect(analyzeGraph(g).errors).toContainEqual(expect.objectContaining({ nodeId: pool.id, message: expect.stringContaining('padding') }));
    expect(() => generatePython(g)).toThrow();
  });

  it('requires patch size to divide both image dimensions', () => {
    const g = starterGraph('vit');
    g.nodes[0].config.width = 30;
    expect(analyzeGraph(g).errors).toContainEqual(expect.objectContaining({ message: expect.stringContaining('divisible by patch size') }));
    expect(() => generatePython(g)).toThrow();
  });

  it('requires image tensors for vision operations and conversion before Linear', () => {
    const g = starterGraph('cnn');
    const pool = g.nodes.find(n => n.kind === 'globalpool2d')!;
    pool.kind = 'relu';
    const linear = g.nodes.find(n => n.kind === 'linear')!;
    expect(analyzeGraph(g).errors).toContainEqual(expect.objectContaining({ nodeId: linear.id, message: expect.stringContaining('Flatten or Global average pool') }));
    pool.kind = 'flatten';
    expect(analyzeGraph(g).errors).toEqual([]);
    expect(analyzeGraph(g).shapes[pool.id].dims).toEqual(['B', 8192]);
    g.nodes[0].config.mode = 'features';
    expect(analyzeGraph(g).errors).toContainEqual(expect.objectContaining({ message: expect.stringContaining('image tensor') }));
  });

  it('preserves legacy causal text models and image configuration through roundtrip', () => {
    const old = starterGraph('text');
    const block = old.nodes.find(n => n.kind === 'transformer')!;
    delete block.config.causal;
    const restored = readGraph(JSON.stringify(old));
    expect(restored.nodes.find(n => n.kind === 'transformer')!.config.causal).toBe(true);
    expect(analyzeGraph(restored).errors).toEqual([]);
    const image = starterGraph('cnn');
    expect(readGraph(JSON.stringify(image))).toEqual(image);
    expect(generatePython(restored).code).toContain('TransformerBlock(16, 4, 4, 0.1, True)');
  });

  it('builds a feature-vector classifier without token embeddings', () => {
    const g = starterGraph('mlp'), a = analyzeGraph(g);
    expect(a.errors).toEqual([]);
    expect(g.nodes[0].config.mode).toBe('features');
    expect(a.shapes[g.nodes.at(-1)!.id].dims).toEqual(['B', 3]);
    expect(a.parameters).toBe(643);
  });
});
