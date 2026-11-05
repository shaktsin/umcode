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

import DelegationActivation from './DelegationActivation.svelte';
it('shows unsupported projectless activation errors before a chat exists', () => {
  const { body } = render(DelegationActivation, { props: { state: { active: undefined, threadId: null, error: 'Agents require a committed Git project. Open a project first.', pending: false, token: null } as any, onenable() {} } });
  expect(body).toContain('role="alert"'); expect(body).toContain('Agents require a committed Git project. Open a project first.');
});
