import type { Item } from '$lib/types';

import type { DataConfig } from './dataTypes';

export interface WorkbenchActionResult {
  action: 'save_build' | 'save_data' | 'train' | 'test_checkpoint' | 'import_data' | 'run_data_script';
  projectId: string;
  modelId: string;
  modelCreatedAt: string;
  baseDraftRevision?: string;
  datasetId?: string;
  runId?: string;
  operation?: 'start' | 'stop';
  status?: string;
  saved?: boolean;
  preview?: DataConfig;
  baseVersion?: string;
  sources?: { ok: boolean; datasetId?: string; error?: string }[];
}
const names: Record<WorkbenchActionResult['action'], string> = { save_build: 'workbench.save_build', save_data: 'workbench.save_data', train: 'workbench.train', test_checkpoint: 'workbench.test_checkpoint', import_data: 'workbench.import_data', run_data_script: 'workbench.run_data_script' };
export function workbenchActionResult(item: Item): WorkbenchActionResult | null {
  if (item.status !== 'completed' || !item.tool?.output) return null;
  try {
    const value: unknown = JSON.parse(item.tool.output);
    if (!value || typeof value !== 'object' || Array.isArray(value)) return null;
    const result = value as Record<string, unknown>;
    if (typeof result.action !== 'string' || !(result.action in names) || names[result.action as WorkbenchActionResult['action']] !== item.tool.name) return null;
    if (typeof result.projectId !== 'string' || typeof result.modelId !== 'string' || typeof result.modelCreatedAt !== 'string') return null;
    if (result.action === 'save_build' && (result.saved !== true || typeof result.baseDraftRevision !== 'string' || !/^[a-f0-9]{64}$/.test(result.baseDraftRevision))) return null;
    if (result.action === 'save_data' && (typeof result.datasetId !== 'string' || (result.saved !== true && !(result.saved === false && typeof result.baseVersion === 'string' && result.preview && typeof result.preview === 'object' && !Array.isArray(result.preview))))) return null;
    if (result.action === 'train' && (typeof result.runId !== 'string' || !/^train-[a-f0-9]{32}$/.test(result.runId) || !['start', 'stop'].includes(String(result.operation)))) return null;
    if (result.action === 'import_data' && (!Array.isArray(result.sources) || result.sources.some(source => !source || typeof source !== 'object' || typeof (source as Record<string, unknown>).ok !== 'boolean'))) return null;
    if (result.action === 'run_data_script' && result.datasetId !== undefined && typeof result.datasetId !== 'string') return null;
    return result as unknown as WorkbenchActionResult;
  } catch { return null; }
}

/** After an assistant data action, whether the Data tab should reload and which dataset to select. */
export function assistantDatasetToShow(result: WorkbenchActionResult): { refresh: boolean; datasetId?: string } {
  if (result.preview) return { refresh: false };
  if ((result.action === 'save_data' || result.action === 'run_data_script') && result.datasetId) return { refresh: true, datasetId: result.datasetId };
  if (result.action === 'import_data') {
    const first = result.sources?.find(source => source.ok && source.datasetId);
    if (first) return { refresh: true, datasetId: first.datasetId };
  }
  return { refresh: false };
}
