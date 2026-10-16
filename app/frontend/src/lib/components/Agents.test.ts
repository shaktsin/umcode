import { expect, it, vi } from 'vitest';
import { render } from 'svelte/server';
vi.mock('$lib/stores/app.svelte', () => ({ app: { approvals: [], respondApproval() {}, models: [] } }));
vi.mock('$lib/stores/inspector.svelte', () => ({ inspector: {} }));
import Agents from './Agents.svelte';
it('renders collapsed compact agent evidence with honest blocked and interrupted states', () => {
  const child = { id: 'child', taskId: 'task', role: 'reviewer', model: { model: 'review-model' }, state: 'interrupted', assignment: { prompt: 'Inspect exact revision', acceptanceChecks: ['Check ownership'] }, result: { summary: 'Partial review', blocker: 'Restart interrupted execution', findings: [{ message: 'Ownership issue', severity: 'high', path: 'a.ts', line: 2 }], checks: [{ command: 'go test', status: 'blocked', summary: 'Reviewer commands disabled' }], changedPaths: ['a.ts'] }, usage: { inputTokens: 10, outputTokens: 2, costUsd: .02 }, correctionRounds: 1, revision: 'abc' };
  const state = { tasks: [{ task: { id: 'task', threadId: 'parent', state: 'interrupted' }, children: [child], childErrors: { child: 'Storage unavailable' }, workflow: { checks: [{ label: 'Lead test', status: 'not_run', command: 'go test', reason: 'Not yet run' }] } }], pending: false, error: '', cancel() {} } as any;
  const { body } = render(Agents, { props: { state, threadId: 'parent', projectId: 'project' } });
  expect(body).toContain('Agents'); expect(body).toContain('Inspect exact revision'); expect(body).toContain('review-model'); expect(body).toContain('Interrupted'); expect(body).toContain('Storage unavailable'); expect(body).toContain('Partial review'); expect(body).toContain('Ownership issue'); expect(body).toContain('blocked'); expect(body).toContain('not_run'); expect(body).not.toMatch(/<details[^>]* open/);
});
