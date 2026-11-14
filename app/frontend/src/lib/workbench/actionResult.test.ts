import { expect, it } from 'vitest';
import { workbenchActionResult } from './actionResult';
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
