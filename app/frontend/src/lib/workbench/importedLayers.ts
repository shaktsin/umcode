import type { ImportedNode } from './modelImportTypes';

const rmsDecoders: Record<string, string> = { qwen2: 'Qwen2', llama: 'Llama', mistral: 'Mistral', mixtral:'Mixtral', granitemoe:'Granite MoE', qwen2_moe:'Qwen2 MoE', qwen3_moe:'Qwen3 MoE' };
export const moeFamilies = ['mixtral','granitemoe','qwen2_moe','qwen3_moe'];

/** Translate generated names at display time; never migrate a user's saved graph or overwrite a custom label. */
export function importedLayerTitle(node: ImportedNode, family: string) {
  if (node.kind !== 'transformer' || !Object.hasOwn(rmsDecoders, family)) return node.label;
  const match = /^layers\.(\d+)\.(\d+)$/.exec(node.sourceKey ?? '');
  const prefix = moeFamilies.includes(family) && node.config.mlp === 'sparse MoE' ? 'MoE transformer decoder' : 'Transformer decoder';
  if (!match) return node.label === rmsDecoders[family] + ' block' ? prefix + ' layer' : node.label;
  const [,start,end] = match;
  const plural = start !== end, range = plural ? start + '–' + end : start;
  return node.label === rmsDecoders[family] + (plural ? ' blocks ' : ' block ') + range
    ? prefix + ' ' + (plural ? 'layers ' : 'layer ') + range : node.label;
}

/** A config-derived view of a supported decoder, not measured activations or graph-editable nodes. */
export function decoderDetails(node: ImportedNode, family: string) {
  if (node.kind !== 'transformer' || !Object.hasOwn(rmsDecoders, family)) return null;
  const c = node.config;
  const dim = (value: unknown) => typeof value === 'number' && Number.isSafeInteger(value) && value > 0 ? value : '?';
  const h = dim(c.hiddenSize), inner = dim(c.intermediateSize);
  const shape = '[B, T, ' + h + ']';
  const steps: { name: string; detail: string }[] = [];
  // The inspector describes the recognized native pattern. Don't promise a
  // different architecture's ordering after arbitrary textual setting edits.
  const moe = moeFamilies.includes(family) && c.mlp === 'sparse MoE';
  if (c.normalization !== 'pre RMSNorm' || c.attention !== 'causal grouped-query self-attention' || (!moe && c.mlp !== 'gated MLP (SwiGLU)') || c.family !== family) {
    return { shape, steps, note: 'These settings differ from the recognized decoder. Internal order is not inferred.' };
  }
  const residual = () => { if (c.residual === true) steps.push({name:'Residual add', detail:'Add the sublayer input to its output' + (family==='granitemoe' ? ' scaled by ' + String(c.residualMultiplier ?? 1) : '') + '. Shape stays ' + shape + '.'}); };
  steps.push({name:'Input RMSNorm', detail:'Normalize each token across its hidden features. Shape stays ' + shape + '.'});
  if (c.queryKeyNorm === true) steps.push({name:'Query/key RMSNorm', detail:'Normalize projected Q and K across each head (' + dim(c.headDimension) + ' features), then apply rotary positions.'});
  steps.push({name:'Causal self-attention', detail:dim(c.attentionHeads) + ' query heads · ' + dim(c.keyValueHeads) + ' key/value heads. Project queries, keys and values; combine heads back to ' + h + ' features.' + (c.headDimension ? ' Q: [B, ' + dim(c.attentionHeads) + ', T, ' + dim(c.headDimension) + ']; K/V: [B, ' + dim(c.keyValueHeads) + ', T, ' + dim(c.headDimension) + '].' : '')});
  residual();
  steps.push({name:'Post-attention RMSNorm', detail:'Normalize the attention result before the feed-forward network. Shape stays ' + shape + '.'});
  if (moe) {
    steps.push({name:'Router / top-k selection', detail:'Scores [B, T, ' + dim(c.expertCount) + ']; select ' + dim(c.expertsPerToken) + ' of ' + dim(c.expertCount) + ' experts per token. Routing is input-dependent; these are configured counts, not observed activations.'});
    steps.push({name:'Routed experts', detail:String(c.expertCount) + ' gated expert networks · ' + h + ' → ' + dim(c.expertIntermediateSize) + ' → ' + h + ' features per routed token.'});
    steps.push({name:'Weighted expert combination', detail:'Combine selected expert outputs using router weights' + (c.normalizeTopK === false ? ' without top-k renormalization' : ' normalized over selected experts') + '. Output ' + shape + '.'});
    if (typeof c.sharedExpertIntermediateSize === 'number' && c.sharedExpertIntermediateSize > 0) steps.push({name:'Shared expert', detail:'Parallel always-active branch · ' + h + ' → ' + c.sharedExpertIntermediateSize + ' → ' + h + '; sigmoid-gated and added to routed output.'});
  } else steps.push({name:'Gated feed-forward network', detail: (c.activation === 'silu' ? 'SwiGLU' : 'Gated MLP (' + String(c.activation ?? 'activation unspecified') + ')') + ' · ' + h + ' → ' + inner + ' → ' + h + ' features per token.'});
  residual();
  return { shape, steps, note:'Shapes are inferred from layer settings, not a model run. B = batch size; T = sequence length.' };
}
