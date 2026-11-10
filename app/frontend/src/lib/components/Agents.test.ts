import { expect, it, vi } from 'vitest';
import { render } from 'svelte/server';
vi.mock('$lib/stores/app.svelte', () => ({ app: { approvals: [], respondApproval() {}, models: [] } }));
vi.mock('$lib/stores/inspector.svelte', () => ({ inspector: {} }));
import Agents from './Agents.svelte';
it('renders earlier agent runs as read-only history beside lead verification', () => {
  const child = { id: 'child', taskId: 'task', model: { model: 'review-model' }, state: 'interrupted', assignment: { prompt: 'Inspect exact revision' }, result: { summary: 'Partial review' }, usage: { inputTokens: 10, outputTokens: 2, costUsd: .02 }, revision: 'abc' };
  const state = { tasks: [{ task: { id: 'task', threadId: 'parent', state: 'interrupted' }, children: [child], workflow: { checks: [{ label: 'Lead test', status: 'not_run', command: 'go test', reason: 'Not yet run' }] } }], reports: {}, pending: false, error: '', cancel() {} } as any;
  const { body } = render(Agents, { props: { state, threadId: 'parent', projectId: 'project' } });
  expect(body).toContain('Earlier agent run'); expect(body).toContain('Inspect exact revision'); expect(body).toContain('review-model'); expect(body).toContain('Interrupted'); expect(body).toContain('Partial review'); expect(body).toContain('not_run'); expect(body).not.toContain('Retry saving outcome'); expect(body).not.toContain('Cancel reviewer'); expect(body).not.toMatch(/<details[^>]* open/);
});

const agent = (id: string, extra: any = {}) => ({ id, taskId: 'task', name: 'isEven-implementer', purpose: '', planEntry: 'impl', assignment: { prompt: '' }, capabilities: { files: 'read-write', shell: 'read-only', network: false, spawnMax: 0 }, modelRequested: 'claude/claude-sonnet-5', budgetShare: .1, depth: 0, state: 'failed', result: { summary: '', blocker: 'no model is available' }, usage: { inputTokens: 0, outputTokens: 0, costUsd: 0 }, version: 1, createdAt: `2026-01-01T00:00:0${id.slice(-1)}Z`, updatedAt: '', ...extra });
const stoppedTask = () => {
  const agents = [1, 2, 3, 4].map(i => agent(`a${i}`)).concat([agent('a5', { result: { summary: '', blocker: 'Shared task budget reached' } }), agent('r1', { name: 'isEven-reviewer', planEntry: 'review', state: 'queued', result: { summary: '' }, capabilities: { files: 'read', shell: 'none', network: false, spawnMax: 0 } })]);
  return { tasks: [{ task: { id: 'task', threadId: 'parent', state: 'running' }, children: [], agents, budgetAvailable: true, usage: { inputTokens: 300000, outputTokens: 29546, costUsd: 0.69 }, stopReason: 'The task reached its budget limit, so its agents stopped.' }], reports: {}, pending: false, error: '', cancel() {} } as any;
};
const renderStopped = () => render(Agents, { props: { state: stoppedTask(), threadId: 'parent', projectId: 'project' } }).body;

it('shows one row per plan entry instead of one per retry', () => {
  const body = renderStopped();
  expect(body.match(/data-agent-row=/g)).toHaveLength(2);
  expect(body).toContain('5 attempts');
  expect(body).toContain('Attempt 5'); expect(body).toContain('Attempt 1');
  expect(body).toContain('Shared task budget reached'); expect(body).toContain('No model is available');
});
it('describes permissions in words, not as shouty permission pills', () => {
  const body = renderStopped();
  expect(body).toContain('Edits files · Read-only shell · claude/claude-sonnet-5');
  expect(body).toContain('Reads files · claude/claude-sonnet-5');
  expect(body).not.toMatch(/files:rw|shell:ro/i);
});
it('puts status, usage and a real Cancel task button in the header', () => {
  const body = renderStopped();
  expect(body).toContain('1 stopped'); expect(body).toContain('1 waiting');
  expect(body).toContain('330k tokens'); expect(body).toContain('$0.69');
  expect(body).toMatch(/<button[^>]*>\s*Cancel task\s*<\/button>/);
  expect(body).not.toContain('absolute');
});
it('shows a calm task-level stop notice with a way to fix it', () => {
  const body = renderStopped();
  expect(body).toContain('The task reached its budget limit, so its agents stopped.');
  expect(body).toMatch(/<button[^>]*>\s*Open settings\s*<\/button>/);
  expect(body).not.toContain('text-rust whitespace-pre-wrap');
});
it('uses red only for real failures, never for a budget stop', () => {
  const body = renderStopped();
  const stopped = body.slice(body.indexOf('Stopped by budget') - 200, body.indexOf('Stopped by budget') + 50);
  expect(stopped).not.toContain('text-rust');
  expect(body).toContain('Stopped by budget');
});
