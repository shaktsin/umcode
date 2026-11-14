import { expect, it } from 'vitest';
import { assistantDatasetToShow, workbenchActionResult, type WorkbenchActionResult } from './actionResult';
import type { Item } from '$lib/types';

const item = (status: Item['status'], name: string, output: string) => ({ status, tool: { name, output } }) as Item;
it('accepts only completed scoped Workbench action results', () => {
  const output = JSON.stringify({ action: 'save_build', projectId: 'p', modelId: 'm', modelCreatedAt: 'v', baseDraftRevision: 'a'.repeat(64), saved: true });
  expect(workbenchActionResult(item('completed', 'workbench.save_build', output))?.action).toBe('save_build');
  expect(workbenchActionResult(item('failed', 'workbench.save_build', output))).toBeNull();
  expect(workbenchActionResult(item('completed', 'file.write', output))).toBeNull();
  expect(workbenchActionResult(item('completed', 'workbench.save_build', '{broken'))).toBeNull();
  const train = JSON.stringify({ action: 'train', projectId: 'p', modelId: 'm', modelCreatedAt: 'v', runId: 'train-' + 'b'.repeat(32), operation: 'start', status: 'running' });
  expect(workbenchActionResult(item('completed', 'workbench.train', train))?.runId).toBe('train-' + 'b'.repeat(32));
  expect(workbenchActionResult(item('completed', 'workbench.train', train.replace('train-' + 'b'.repeat(32), 'invented')))).toBeNull();
});

it('accepts assistant import and script results that carry the model identity', () => {
  const scope = { projectId: 'p', modelId: 'm', modelCreatedAt: 'v' };
  const script = JSON.stringify({ action: 'run_data_script', ...scope, datasetId: 'd'.repeat(64), rows: 6696, durationMs: 4000, stdout: 'ok' });
  expect(workbenchActionResult(item('completed', 'workbench.run_data_script', script))?.datasetId).toBe('d'.repeat(64));
  const report = JSON.stringify({ action: 'run_data_script', ...scope, durationMs: 10, stdout: 'only a report' });
  expect(workbenchActionResult(item('completed', 'workbench.run_data_script', report))?.datasetId).toBeUndefined();
  const imported = JSON.stringify({ action: 'import_data', ...scope, sources: [{ ok: false, error: 'missing' }, { ok: true, datasetId: 'e'.repeat(64) }] });
  expect(workbenchActionResult(item('completed', 'workbench.import_data', imported))?.sources?.[1]?.datasetId).toBe('e'.repeat(64));
  expect(workbenchActionResult(item('completed', 'workbench.import_data', JSON.stringify({ action: 'import_data', ...scope, sources: 'nope' })))).toBeNull();
  expect(workbenchActionResult(item('completed', 'workbench.import_data', script))).toBeNull();
  expect(workbenchActionResult(item('completed', 'workbench.run_data_script', JSON.stringify({ action: 'run_data_script', projectId: 'p' })))).toBeNull();
});

it('chooses which dataset the Data tab should show after an assistant data action', () => {
  const scope = { projectId: 'p', modelId: 'm', modelCreatedAt: 'v' };
  const result = (value: Record<string, unknown>) => ({ ...scope, ...value }) as unknown as WorkbenchActionResult;
  expect(assistantDatasetToShow(result({ action: 'save_data', datasetId: 'a' }))).toEqual({ refresh: true, datasetId: 'a' });
  expect(assistantDatasetToShow(result({ action: 'run_data_script', datasetId: 'b' }))).toEqual({ refresh: true, datasetId: 'b' });
  expect(assistantDatasetToShow(result({ action: 'run_data_script' }))).toEqual({ refresh: false });
  expect(assistantDatasetToShow(result({ action: 'import_data', sources: [{ ok: false }, { ok: true, datasetId: 'c' }, { ok: true, datasetId: 'd' }] }))).toEqual({ refresh: true, datasetId: 'c' });
  expect(assistantDatasetToShow(result({ action: 'import_data', sources: [{ ok: false }] }))).toEqual({ refresh: false });
  expect(assistantDatasetToShow(result({ action: 'train', runId: 'x' }))).toEqual({ refresh: false });
});
