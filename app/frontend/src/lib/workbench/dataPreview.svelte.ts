import { SvelteMap } from 'svelte/reactivity';
import type { WorkbenchActionResult } from './actionResult';

export const dataPreviews = new SvelteMap<string, WorkbenchActionResult>();
export function previewKey(projectId: string, modelId: string, version: string) {
  return JSON.stringify([projectId, modelId, version]);
}
