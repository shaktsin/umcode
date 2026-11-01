import { describe, expect, it } from 'vitest';
import { modelGraph, relevantStarterPresets } from './presets';
import { readGraph, visibleFields } from './graph';

describe('input modality and architecture choices', () => {
  it('offers vision architectures under Classification and Custom, with no new task kind', () => {
    for (const task of ['classification', 'custom'] as const) expect(relevantStarterPresets(task, 'image').map(p => p.id)).toEqual(['blank', 'cnn', 'vit']);
    expect(relevantStarterPresets('text-generation', 'image').map(p => p.id)).toEqual(['blank', 'text', 'gpt2']);
    expect(relevantStarterPresets('classification', 'text').map(p => p.id)).toEqual(['blank', 'classifier']);
    expect(relevantStarterPresets('classification', 'features').map(p => p.id)).toEqual(['blank', 'mlp']);
  });

  it('seeds blank image/vector architectures with the matching Input and persists its settings', () => {
    for (const input of ['image', 'features'] as const) {
      const g = modelGraph('blank', input);
      expect(g.nodes).toHaveLength(1); expect(g.nodes[0].config.mode).toBe(input);
      expect(readGraph(JSON.stringify(g))).toEqual(g);
    }
    expect(modelGraph('blank', 'text').nodes).toEqual([]);
  });

  it('shows only settings relevant to the Input mode', () => {
    const node = modelGraph('blank', 'image').nodes[0];
    expect(visibleFields(node).map(f => f.key)).toEqual(['mode', 'channels', 'height', 'width']);
    node.config.mode = 'tokens'; expect(visibleFields(node).map(f => f.key)).toEqual(['mode', 'sequence']);
    node.config.mode = 'features'; expect(visibleFields(node).map(f => f.key)).toEqual(['mode', 'features']);
  });
});
