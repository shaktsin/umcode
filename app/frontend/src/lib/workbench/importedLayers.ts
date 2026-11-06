import type { ImportedNode } from './modelImportTypes';

const rmsDecoders: Record<string, string> = { qwen2: 'Qwen2', llama: 'Llama', mistral: 'Mistral' };

/** Translate generated names at display time; never migrate a user's saved graph or overwrite a custom label. */
export function importedLayerTitle(node: ImportedNode, family: string) {
  if (node.kind !== 'transformer' || !Object.hasOwn(rmsDecoders, family)) return node.label;
  const match = /^layers\.(\d+)\.(\d+)$/.exec(node.sourceKey ?? '');
  if (!match) return node.label === rmsDecoders[family] + ' block' ? 'Transformer decoder layer' : node.label;
  const [,start,end] = match;
  const plural = start !== end, range = plural ? start + '–' + end : start;
  return node.label === rmsDecoders[family] + (plural ? ' blocks ' : ' block ') + range
    ? 'Transformer decoder ' + (plural ? 'layers ' : 'layer ') + range : node.label;
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
  if (c.normalization !== 'pre RMSNorm' || c.attention !== 'causal grouped-query self-attention' || c.mlp !== 'gated MLP (SwiGLU)' || c.family !== family) {
    return { shape, steps, note: 'These settings differ from the recognized decoder. Internal order is not inferred.' };
  }
  const residual = () => { if (c.residual === true) steps.push({name:'Residual add', detail:'Add the sublayer input to its output. Shape stays ' + shape + '.'}); };
  steps.push({name:'Input RMSNorm', detail:'Normalize each token across its hidden features. Shape stays ' + shape + '.'});
  steps.push({name:'Causal self-attention', detail:dim(c.attentionHeads) + ' query heads · ' + dim(c.keyValueHeads) + ' key/value heads. Project queries, keys and values; combine heads back to ' + h + ' features.'});
  residual();
  steps.push({name:'Post-attention RMSNorm', detail:'Normalize the attention result before the feed-forward network. Shape stays ' + shape + '.'});
  steps.push({name:'Gated feed-forward network', detail: (c.activation === 'silu' ? 'SwiGLU' : 'Gated MLP (' + String(c.activation ?? 'activation unspecified') + ')') + ' · ' + h + ' → ' + inner + ' → ' + h + ' features per token.'});
  residual();
  return { shape, steps, note:'Shapes are inferred from layer settings, not a model run. B = batch size; T = sequence length.' };
}
