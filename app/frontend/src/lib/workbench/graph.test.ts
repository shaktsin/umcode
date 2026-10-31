import { describe, expect, it } from 'vitest';
import { addLayer, analyzeGraph, connectLayers, definition, emptyGraph, parseNumericSetting, readGraph, starterGraph } from './graph';
import { generatePython } from './codegen';

describe('visual model architecture', () => {
  it('reports one actionable missing-output error instead of counting the ending twice', () => {
    const graph = starterGraph('classifier');
    const output = graph.nodes.pop()!;
    graph.edges = graph.edges.filter(e => e.target !== output.id);
    const errors = analyzeGraph(graph).errors;
    expect(errors).toEqual([{ message: 'Add an Output layer and connect Linear to it.' }]);
  });

  it('names the last layer when an existing Output is disconnected', () => {
    const graph = starterGraph('classifier');
    graph.edges.pop();
    const errors = analyzeGraph(graph).errors;
    expect(errors).toEqual([{ nodeId: graph.nodes.at(-2)!.id, message: 'Connect Linear to the Output layer.' }]);
  });
  it('rejects invalid property input before it can replace a recoverable architecture', () => {
    const field = definition('embedding').fields.find(f => f.key === 'width')!;
    expect(() => parseNumericSetting(field, '10000000000000000')).toThrow('between 1 and 65536');
    expect(() => parseNumericSetting(field, '')).toThrow();
    expect(() => parseNumericSetting(field, '2.5')).toThrow();
    expect(() => parseNumericSetting(field, 'Infinity')).toThrow();
    expect(parseNumericSetting(field, '24')).toBe(24);
  });
  it('infers a small text model and generates an ordered forward pass', () => {
    const graph = starterGraph('text');
    const result = analyzeGraph(graph);
    expect(result.errors).toEqual([]);
    expect(result.shapes[graph.nodes.at(-1)!.id].dims).toEqual(['B', 8, 32]);
    expect(result.parameters).toBeGreaterThan(5000);
    const generated = generatePython(graph);
    expect(generated.code).toContain('class Model(nn.Module)');
    expect(generated.code).toContain('def forward(self, x):');
    expect(generated.lineNodes).toContain(graph.nodes[1].id);
  });

  it('infers a classifier and GPT-2 size starter with tied weights', () => {
    const classifier = starterGraph('classifier');
    expect(analyzeGraph(classifier).shapes[classifier.nodes.at(-1)!.id].dims).toEqual(['B', 3]);
    const gpt = starterGraph('gpt2');
    const result = analyzeGraph(gpt);
    expect(result.errors).toEqual([]);
    expect(result.parameters).toBe(124439808);
    expect(generatePython(gpt).code).toContain('.weight =');
  });

  it('rejects cycles, branches and connections into the Input layer', () => {
    const graph = starterGraph('text');
    expect(() => connectLayers(graph, graph.nodes[2].id, graph.nodes[0].id)).toThrow();
    expect(() => connectLayers(graph, graph.nodes[1].id, graph.nodes[3].id)).toThrow();
    const disconnected = { ...graph, edges: [] };
    expect(analyzeGraph(disconnected).errors.length).toBeGreaterThan(0);
    expect(() => generatePython(disconnected)).toThrow();
    const cycle = { ...graph, edges: [...graph.edges, { id: 'cycle', source: graph.nodes.at(-1)!.id, target: graph.nodes[0].id }] };
    expect(analyzeGraph(cycle).errors.length).toBeGreaterThan(0);
  });

  it('shows a layer-specific error for invalid dimensions and never exports it', () => {
    const graph = starterGraph('text');
    const block = graph.nodes.find(n => n.kind === 'transformer')!;
    block.config.heads = 3;
    expect(analyzeGraph(graph).errors).toContainEqual(expect.objectContaining({ nodeId: block.id, message: expect.stringContaining('divisible') }));
    expect(() => generatePython(graph)).toThrow();
    block.config.heads = 4; block.config.repeat = -1;
    expect(analyzeGraph(graph).errors.length).toBeGreaterThan(0);
  });

  it('loads empty bundles safely and refuses to erase unsupported saved content', () => {
    expect(readGraph('{"version":1,"nodes":[],"edges":[]}')).toEqual(emptyGraph());
    expect(() => readGraph('{"version":1,"nodes":[{"id":"legacy"}],"edges":[]}')).toThrow();
    expect(() => readGraph('{"version":1,"nodes":[],"edges":[],"seed":9007199254740993}')).toThrow();
    const graph = starterGraph('text');
    expect(readGraph(JSON.stringify(graph))).toEqual(graph);
    graph.nodes[0].config.sequence = 9007199254740992;
    expect(() => readGraph(JSON.stringify(graph))).toThrow();
  });

  it('adds a layer at the requested point without mutating the previous document', () => {
    const graph = emptyGraph();
    const added = addLayer(graph, 'linear', { x: 100, y: 200 });
    expect(graph.nodes).toEqual([]);
    expect(added.nodes[0].position).toEqual({ x: 100, y: 200 });
    expect(added.nodes[0].config.outFeatures).toBe(64);
  });
});
