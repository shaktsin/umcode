import type { RuntimeInfo } from './inferenceTypes';

export const checkpointByteLimit = 64 * 1024 * 1024;
export function validateCheckpointFile(file: { name: string; size: number }): string {
  if (!/\.(pt|pth|safetensors)$/i.test(file.name)) return 'Choose a .pt, .pth or .safetensors weights file.';
  if (!Number.isSafeInteger(file.size) || file.size <= 0) return 'Choose a nonempty weights file.';
  if (file.size > checkpointByteLimit) return 'Checkpoint weights must be 64 MiB or smaller.';
  return '';
}

export function inferenceRuntimes(reported: RuntimeInfo[]): RuntimeInfo[] {
  const pytorch = reported.find(runtime => runtime.id === 'pytorch');
  return [
    pytorch ?? { id: 'pytorch', name: 'PyTorch', available: false, reason: 'Connect to the engine to check the local PyTorch runtime.' },
    ...[
      { id: 'llama.cpp', name: 'llama.cpp', reason: 'Requires GGUF weights and a supported language-model architecture; custom Workbench graphs use PyTorch.' },
      { id: 'vllm', name: 'vLLM', reason: 'Requires a supported pretrained transformer architecture; custom Workbench state_dict checkpoints are incompatible.' },
      { id: 'sglang', name: 'SGLang', reason: 'Requires a supported pretrained transformer architecture; custom Workbench state_dict checkpoints are incompatible.' },
    ].map(runtime => ({ ...runtime, available: false, reason: reported.find(item => item.id.toLowerCase() === runtime.id.toLowerCase())?.reason || runtime.reason })),
  ];
}

export function inferenceBase64(buffer: ArrayBuffer): string {
  const bytes = new Uint8Array(buffer); let binary = '';
  for (let i = 0; i < bytes.length; i += 32_768) binary += String.fromCharCode(...bytes.subarray(i, i + 32_768));
  return btoa(binary);
}
export function checkpointSize(bytes: number): string { return bytes >= 1024 * 1024 ? `${(bytes / 1024 / 1024).toFixed(1)} MiB` : `${Math.ceil(bytes / 1024)} KiB`; }
