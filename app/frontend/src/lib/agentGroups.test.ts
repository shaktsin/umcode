import { expect, it } from 'vitest';
import { agentTone, attemptLabel, groupAgents, permissionLine, reasonOf, statusCounts, taskStopNeedsSettings } from './agentGroups';
import type { RuntimeAgent } from './types';

const ag = (id: string, extra: Partial<RuntimeAgent> = {}): RuntimeAgent => ({ id, taskId: 't', name: 'impl', purpose: '', assignment: { prompt: '' }, capabilities: { files: 'read-write', shell: 'read-only', network: false, spawnMax: 0 }, budgetShare: 0.1, depth: 0, state: 'failed', result: { summary: '' }, usage: { inputTokens: 0, outputTokens: 0, costUsd: 0 }, version: 1, createdAt: '', updatedAt: '', modelRequested: 'claude/claude-sonnet-5', ...extra } as RuntimeAgent);
const row = (agent: RuntimeAgent, depth = 0) => ({ agent, depth });

it('collapses retries of one plan entry into a single group, newest attempt leading', () => {
  const rows = [1, 2, 3].map(i => row(ag(`a${i}`, { planEntry: 'impl', result: { summary: '', blocker: `r${i}` } })));
  rows.push(row(ag('b1', { planEntry: 'review', name: 'reviewer' })));
  const groups = groupAgents(rows);
  expect(groups.map(g => g.key)).toEqual(['t:impl', 't:review']);
  expect(groups[0].latest.id).toBe('a3');
  expect(groups[0].attempts.map(a => a.id)).toEqual(['a3', 'a2', 'a1']);
  expect(groups[0].attempts.map(a => a.attempt)).toEqual([3, 2, 1]);
});
it('never merges agents that have no plan entry', () => {
  const groups = groupAgents([row(ag('x')), row(ag('y'))]);
  expect(groups).toHaveLength(2);
});
it('does not merge the same plan entry across different tasks', () => {
  const groups = groupAgents([row(ag('x', { planEntry: 'impl', taskId: 't1' })), row(ag('y', { planEntry: 'impl', taskId: 't2' }))]);
  expect(groups).toHaveLength(2);
});
it('keeps the first attempt depth so nested groups still indent', () => {
  expect(groupAgents([row(ag('c', { planEntry: 'e' }), 2), row(ag('d', { planEntry: 'e' }), 2)])[0].depth).toBe(2);
});
it('reserves red for real failures: budget stops are their own amber state', () => {
  expect(agentTone(ag('a', { state: 'failed', result: { summary: '', blocker: 'Shared task budget reached' } }))).toMatchObject({ tone: 'stopped', label: 'Stopped by budget' });
  expect(agentTone(ag('a', { state: 'failed', result: { summary: '', blocker: 'no model is available' } }))).toMatchObject({ tone: 'failed', label: 'Failed' });
  expect(agentTone(ag('a', { state: 'queued' }))).toMatchObject({ tone: 'waiting', label: 'Waiting to start' });
  expect(agentTone(ag('a', { state: 'running' }))).toMatchObject({ tone: 'running', label: 'Running' });
  expect(agentTone(ag('a', { state: 'waiting_approval' }))).toMatchObject({ tone: 'attention', label: 'Waiting for approval' });
  expect(agentTone(ag('a', { state: 'completed' }))).toMatchObject({ tone: 'done', label: 'Completed' });
  expect(agentTone(ag('a', { state: 'cancelled' })).tone).toBe('neutral');
  expect(agentTone(ag('a', { state: 'interrupted' })).tone).toBe('neutral');
});
it('labels an attempt briefly', () => {
  expect(attemptLabel(ag('a', { state: 'failed', result: { summary: '', blocker: 'output limit reached' } }))).toBe('Stopped');
  expect(attemptLabel(ag('a', { state: 'failed' }))).toBe('Failed');
  expect(attemptLabel(ag('a', { state: 'completed' }))).toBe('Completed');
});
it('describes permissions in plain language and appends the model', () => {
  expect(permissionLine(ag('a'))).toBe('Edits files · Read-only shell · claude/claude-sonnet-5');
  expect(permissionLine(ag('a', { capabilities: { files: 'read', shell: 'none', network: false, spawnMax: 0 } }))).toBe('Reads files · claude/claude-sonnet-5');
  expect(permissionLine(ag('a', { capabilities: { files: 'read-write', shell: 'read-write', network: true, spawnMax: 2 }, modelRequested: undefined }))).toBe('Edits files · Runs commands · Network access · Can start agents · Model not recorded');
  expect(permissionLine(ag('a', { capabilities: { files: 'none', shell: 'none', network: false, spawnMax: 0 } }))).toBe('No tool access · claude/claude-sonnet-5');
});
it('counts the latest attempt of each group, in a fixed plain order', () => {
  const groups = groupAgents([
    row(ag('a1', { planEntry: 'impl' })), row(ag('a2', { planEntry: 'impl', result: { summary: '', blocker: 'budget reached' } })),
    row(ag('b', { planEntry: 'review', state: 'queued' })), row(ag('c', { planEntry: 'x', state: 'running' })), row(ag('d', { planEntry: 'y', state: 'running' })),
  ]);
  expect(statusCounts(groups)).toEqual([{ tone: 'running', label: '2 running' }, { tone: 'stopped', label: '1 stopped' }, { tone: 'waiting', label: '1 waiting' }]);
  expect(statusCounts([])).toEqual([]);
});
it('offers Open settings only for limit-type task stops', () => {
  expect(taskStopNeedsSettings('The task reached its token budget.')).toBe(true);
  expect(taskStopNeedsSettings('Task exceeded the time limit')).toBe(true);
  expect(taskStopNeedsSettings('Cancelled by user')).toBe(false);
});
it('turns a recorded blocker into a sentence', () => {
  expect(reasonOf(ag('a', { result: { summary: '', blocker: 'no model is available' } }))).toBe('No model is available');
  expect(reasonOf(ag('a'))).toBe('');
});
