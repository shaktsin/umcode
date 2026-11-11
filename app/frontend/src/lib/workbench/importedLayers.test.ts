import { expect, it } from 'vitest';
import { importedLayerTitle, decoderDetails } from './importedLayers';
import type { ImportedNode } from './modelImportTypes';
import { importedPalette } from './modelImport';
import { importAvailability } from './importRuntime';

const layer: ImportedNode = { id:'import-4', kind:'transformer', label:'Qwen2 block 1', sourceKey:'layers.1.1',
  config:{family:'qwen2', layerStart:1, layerEnd:1, hiddenSize:896, intermediateSize:4864, attentionHeads:14, keyValueHeads:2, attention:'causal grouped-query self-attention', normalization:'pre RMSNorm', mlp:'gated MLP (SwiGLU)', activation:'silu', residual:true},
  baselineConfig:{}, tensorNames:[], position:{x:0,y:0} };
it('clarifies a saved default block name without changing custom names or source indices', () => {
  expect(importedLayerTitle(layer,'qwen2')).toBe('Transformer decoder layer 1');
  expect(importedLayerTitle({...layer,label:'My experiment'},'qwen2')).toBe('My experiment');
  expect(importedLayerTitle({...layer,label:'Qwen2 blocks 1–3',sourceKey:'layers.1.3'},'qwen2')).toBe('Transformer decoder layers 1–3');
});
it('explains routing, shared experts and QK normalization without treating MoE as a dense MLP', () => {
  for (const family of ['mixtral','granitemoe','qwen2_moe','qwen3_moe']) {
    const node = {...layer, config:{...layer.config,family,mlp:'sparse MoE',expertCount:8,expertsPerToken:2,expertIntermediateSize:128,sharedExpertIntermediateSize:family==='qwen2_moe'?256:0,queryKeyNorm:family==='qwen3_moe',headDimension:64}};
    const info = decoderDetails(node,family)!;
    expect(info).not.toBeNull();
    expect(info.steps.find(s=>s.name==='Router / top-k selection')?.detail).toContain('2 of 8');
    expect(info.steps.find(s=>s.name==='Routed experts')?.detail).toContain('896 → 128 → 896');
    expect(info.steps.some(s=>s.name==='Shared expert')).toBe(family==='qwen2_moe');
    expect(info.steps.some(s=>s.name==='Query/key RMSNorm')).toBe(family==='qwen3_moe');
    expect(importedPalette(family).find(p=>p.kind==='transformer')?.config.mlp).toBe('sparse MoE');
    const status=importAvailability(family,['config.json','model.safetensors','tokenizer.json','tokenizer_config.json']);
    expect(status.complete).toBe(true);
    expect(status.missing).toEqual([]);
  }
});
it('shows components in execution order and infers widths from the draft, not loaded weights', () => {
  const info = decoderDetails(layer,'qwen2')!;
  expect(info.steps.map(s => s.name)).toEqual(['Input RMSNorm','Causal self-attention','Residual add','Post-attention RMSNorm','Gated feed-forward network','Residual add']);
  expect(info.shape).toContain('896'); expect(info.steps[1].detail).toContain('14 query heads'); expect(info.steps[1].detail).toContain('2 key/value heads');
  expect(info.steps[4].detail).toContain('896 → 4864 → 896');
  expect(decoderDetails({...layer,kind:'linear'},'qwen2')).toBeNull();
  expect(decoderDetails(layer,'bert')).toBeNull();
});
