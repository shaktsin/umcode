import type { Config, LayerDefinition, LayerKind, LayerNode, Shape } from './graph';

const integer = (key: string, label: string, min = 1, max = 65536) => ({ key, label, min, max, step: 1 });
const spatialFields = [integer('kernel', 'Kernel size', 1, 31), integer('stride', 'Stride', 1, 16), integer('padding', 'Padding', 0, 32)];
const color = '#e59a36';
export const visionLayers: LayerDefinition[] = [
  { kind: 'conv2d', name: 'Conv2d', group: 'Vision', description: 'Learn spatial features from image channels', color, defaults: { channels: 16, kernel: 3, stride: 1, padding: 1, bias: true }, fields: [integer('channels', 'Output channels'), ...spatialFields, { key: 'bias', label: 'Include bias', type: 'boolean' }] },
  { kind: 'batchnorm2d', name: 'BatchNorm2d', group: 'Vision', description: 'Normalize image feature channels', color, defaults: {}, fields: [] },
  { kind: 'maxpool2d', name: 'Max pool 2D', group: 'Vision', description: 'Downsample each image channel using local maxima', color, defaults: { kernel: 2, stride: 2, padding: 0 }, fields: spatialFields },
  { kind: 'avgpool2d', name: 'Average pool 2D', group: 'Vision', description: 'Downsample each image channel using local averages', color, defaults: { kernel: 2, stride: 2, padding: 0 }, fields: spatialFields },
  { kind: 'globalpool2d', name: 'Global average pool', group: 'Vision', description: 'Convert image features into one value per channel', color, defaults: {}, fields: [] },
  { kind: 'flatten', name: 'Flatten', group: 'Vision', description: 'Convert all non-batch dimensions into a feature vector', color, defaults: {}, fields: [] },
  { kind: 'patchembedding', name: 'Patch embedding', group: 'Vision', description: 'Project image patches into a sequence for a Transformer', color, defaults: { patch: 4, width: 32 }, fields: [integer('patch', 'Patch size', 1, 128), integer('width', 'Projection dimensions')] },
];

export function inferVisionLayer(node: LayerNode, input: Shape): { shape?: Shape; parameters: number; error?: string } {
  const c = node.config;
  if (node.kind === 'flatten') return { shape: { dims: ['B', input.dims.slice(1).reduce<number>((total, dim) => total * Number(dim), 1)], dtype: 'float' }, parameters: 0 };
  if (input.dims.length !== 4) return { parameters: 0, error: 'This layer needs an image tensor [B × channels × height × width]. Choose Images for Input, or connect an image feature layer.' };
  const [, channels, height, width] = input.dims.map(Number);
  if (node.kind === 'batchnorm2d') return { shape: { ...input, dims: [...input.dims] }, parameters: 2 * channels };
  if (node.kind === 'globalpool2d') return { shape: { dims: ['B', channels], dtype: 'float' }, parameters: 0 };
  if (node.kind === 'patchembedding') {
    const patch = Number(c.patch), dimensions = Number(c.width);
    if (height % patch || width % patch) return { parameters: 0, error: 'Image height and width must be divisible by patch size.' };
    return { shape: { dims: ['B', (height / patch) * (width / patch), dimensions], dtype: 'float' }, parameters: dimensions * channels * patch * patch + dimensions };
  }
  const kernel = Number(c.kernel), stride = Number(c.stride), padding = Number(c.padding);
  if (node.kind !== 'conv2d' && padding > Math.floor(kernel / 2)) return { parameters: 0, error: 'Pooling padding must be no greater than half the kernel size.' };
  const h = Math.floor((height + 2 * padding - kernel) / stride) + 1;
  const w = Math.floor((width + 2 * padding - kernel) / stride) + 1;
  if (h <= 0 || w <= 0) return { parameters: 0, error: 'The kernel is too large for the incoming image. Reduce it or increase padding.' };
  const outChannels = node.kind === 'conv2d' ? Number(c.channels) : channels;
  const parameters = node.kind === 'conv2d' ? outChannels * channels * kernel * kernel + (c.bias ? outChannels : 0) : 0;
  return { shape: { dims: ['B', outChannels, h, w], dtype: 'float' }, parameters };
}

export function visionStarterEntries(preset: 'cnn' | 'vit'): [LayerKind, Config?][] {
  const input: [LayerKind, Config] = ['input', { mode: 'image', channels: 3, height: 32, width: 32 }];
  if (preset === 'cnn') return [input, ['conv2d', { channels: 16 }], ['batchnorm2d'], ['relu'], ['maxpool2d'], ['conv2d', { channels: 32 }], ['relu'], ['globalpool2d'], ['linear', { outFeatures: 10 }], ['output']];
  return [input, ['patchembedding'], ['position', { length: 64 }], ['transformer', { width: 32, heads: 4, repeat: 2, causal: false }], ['layernorm'], ['meanpool'], ['linear', { outFeatures: 10 }], ['output']];
}
