import { beforeAll, describe, expect, it, vi } from 'vitest';
import { webcrypto } from 'node:crypto';
import { starterGraph } from './graph';
import { buildContext, graphRevision, previewProposal, type WorkbenchProposal } from './assistant';

beforeAll(() => vi.stubGlobal('crypto', webcrypto));
const graph = () => starterGraph('mlp');
const proposal = (operations: unknown[]): WorkbenchProposal => ({ projectId: 'project-a', modelId: 'model-a', modelName: 'Tiny', revision: 'a'.repeat(64), explanation: 'Try this architecture change.', operations } as WorkbenchProposal);

describe('workbench proposals', () => {
  it('updates multiple layers without mutating the source and reports shapes/parameters', () => {
    const g = graph(), linear = g.nodes.filter(n => n.kind === 'linear'), text = JSON.stringify(g);
    const result = previewProposal(text, proposal(linear.map(n => ({ type: 'update', nodeId: n.id, config: { outFeatures: 8 } }))));
    expect(JSON.stringify(g)).toBe(text);
    expect(result.graph.nodes.filter(n => n.kind === 'linear').every(n => n.config.outFeatures === 8)).toBe(true);
    expect(result.before.parameters).not.toBe(result.after.parameters);
    expect(result.changes.join(' ')).toContain('Output features');
    expect(result.affectedNodeIds).toContain(linear[0].id);
  });
  it('inserts a supported layer using references and explicit connections', () => {
    const g = graph(), e = g.edges[0];
    const result = previewProposal(JSON.stringify(g), proposal([
      { type: 'disconnect', edgeId: e.id }, { type: 'add', ref: 'new_drop', kind: 'dropout', config: { dropout: .2 } },
      { type: 'connect', source: e.source, target: 'new_drop' }, { type: 'connect', source: 'new_drop', target: e.target },
    ]));
    expect(result.graph.nodes).toHaveLength(g.nodes.length + 1);
    expect(result.after.errors).toEqual([]);
    expect(result.graph.nodes.find(n => n.kind === 'dropout')?.config.dropout).toBe(.2);
  });
  it('removes incident edges and can reconnect the neighbors', () => {
    const g = graph(), node = g.nodes.find(n => n.kind === 'relu')!;
    const from = g.edges.find(e => e.target === node.id)!.source, to = g.edges.find(e => e.source === node.id)!.target;
    const result = previewProposal(JSON.stringify(g), proposal([{ type: 'remove', nodeId: node.id }, { type: 'connect', source: from, target: to }]));
    expect(result.graph.nodes.some(n => n.id === node.id)).toBe(false);
    expect(result.after.errors).toEqual([]);
  });
  it('allows a well-formed partial model and reports its remaining issues', () => {
    const g = graph();
    const result = previewProposal(JSON.stringify(g), proposal([{ type: 'remove', nodeId: g.nodes.find(n => n.kind === 'output')!.id }]));
    expect(result.after.errors.length).toBeGreaterThan(0);
  });
  it.each([
    { outFeatures: 0 }, { outFeatures: 1.5 }, { outFeatures: Infinity }, { outFeatures: '16' },
    { outFeatures: 1000001 }, { surprise: 2 }, { bias: 'true' },
  ])('rejects unsupported/invalid settings %j', (config) => {
    const g = graph(), id = g.nodes.find(n => n.kind === 'linear')!.id;
    expect(() => previewProposal(JSON.stringify(g), proposal([{ type: 'update', nodeId: id, config }]))).toThrow();
  });
  it('rejects unknown fields, IDs, unsupported kinds and reference collisions', () => {
    const g = graph(), text = JSON.stringify(g), id = g.nodes[0].id;
    for (const op of [
      { type: 'update', nodeId: id, config: { mode: 'features' }, path: 'model.py' },
      { type: 'remove', nodeId: 'missing' }, { type: 'disconnect', edgeId: 'missing' },
      { type: 'add', ref: 'bad', kind: 'python' }, { type: 'add', ref: id, kind: 'relu' },
      { type: 'update', nodeId: id, config: { mode: 'other' } },
    ]) expect(() => previewProposal(text, proposal([op]))).toThrow();
  });
  it('rejects branching, loops, empty/no-effect and over-limit batches atomically', () => {
    const g = graph(), text = JSON.stringify(g), e = g.edges[0];
    expect(() => previewProposal(text, proposal([{ type: 'connect', source: e.source, target: g.nodes.at(-1)!.id }]))).toThrow();
    expect(() => previewProposal(text, proposal([{ type: 'disconnect', edgeId: e.id }, { type: 'connect', source: e.target, target: e.target }]))).toThrow();
    expect(() => previewProposal(text, proposal([]))).toThrow();
    expect(() => previewProposal(text, proposal([{ type: 'update', nodeId: e.source, config: {} }]))).toThrow();
    expect(() => previewProposal(text, proposal(Array(33).fill({ type: 'remove', nodeId: 'missing' })))).toThrow();
    expect(() => previewProposal(text, proposal([{ type: 'add', kind: 'relu', ref: 'a'.repeat(70000) }]))).toThrow();
    expect(JSON.stringify(g)).toBe(text);
  });
  it('rejects exceeding node limits even when no analysis can run', () => {
    const g = graph();
    while (g.nodes.length < 200) g.nodes.push({ id: `extra_${g.nodes.length}`, kind: 'relu', config: {}, position: { x: 0, y: 0 } });
    expect(() => previewProposal(JSON.stringify(g), proposal([{ type: 'add', ref: 'one_more', kind: 'relu' }]))).toThrow();
  });
  it('captures exact unsaved architecture, selected layer and supported settings as reference data', async () => {
    const g = graph(), text = JSON.stringify(g), selected = g.nodes[1].id;
    const result = await buildContext('project-a', { id: 'model-a', name: 'Tiny' }, text, selected);
    expect(result.graphJson).toBe(text);
    expect(result.selectedNodeId).toBe(selected);
    expect(result.revision).toBe(await graphRevision(text));
    expect(result.revision).toMatch(/^[a-f0-9]{64}$/);
    expect(result.summary).toContain('outFeatures');
    expect(result.summary).toContain('parameters');
    expect(new TextEncoder().encode(result.summary).length).toBeLessThanOrEqual(16384);
  });
  it('does not capture unknown selected IDs or unsupported/oversized drafts', async () => {
    const text = JSON.stringify(graph());
    await expect(buildContext('p', { id: 'm', name: 'M' }, text, 'missing')).rejects.toThrow();
    await expect(buildContext('p', { id: 'm', name: 'M' }, '{}', null)).rejects.toThrow();
    await expect(buildContext('p', { id: 'm', name: 'M' }, ' '.repeat(1048577), null)).rejects.toThrow();
  });
});
