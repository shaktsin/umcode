import type { Item } from '$lib/types';

export interface WorkbenchActionResult {
  action: 'save_build' | 'save_data' | 'train' | 'test_checkpoint';
  projectId: string;
  modelId: string;
  modelCreatedAt: string;
  baseDraftRevision?: string;
  datasetId?: string;
  runId?: string;
  operation?: 'start' | 'stop';
  status?: string;
  saved?: boolean;
}
const names: Record<WorkbenchActionResult['action'], string> = { save_build: 'workbench.save_build', save_data: 'workbench.save_data', train: 'workbench.train', test_checkpoint: 'workbench.test_checkpoint' };
export function workbenchActionResult(item: Item): WorkbenchActionResult | null {
  if (item.status !== 'completed' || !item.tool?.output) return null;
  try {
    const value: unknown = JSON.parse(item.tool.output);
    if (!value || typeof value !== 'object' || Array.isArray(value)) return null;
    const result = value as Record<string, unknown>;
    if (typeof result.action !== 'string' || !(result.action in names) || names[result.action as WorkbenchActionResult['action']] !== item.tool.name) return null;
    if (typeof result.projectId !== 'string' || typeof result.modelId !== 'string' || typeof result.modelCreatedAt !== 'string') return null;
    if (result.action === 'save_build' && (result.saved !== true || typeof result.baseDraftRevision !== 'string' || !/^[a-f0-9]{64}$/.test(result.baseDraftRevision))) return null;
    if (result.action === 'save_data' && (result.saved !== true || typeof result.datasetId !== 'string')) return null;
    if (result.action === 'train' && (typeof result.runId !== 'string' || !/^train-[a-f0-9]{32}$/.test(result.runId) || !['start', 'stop'].includes(String(result.operation)))) return null;
    return result as unknown as WorkbenchActionResult;
  } catch { return null; }
}
