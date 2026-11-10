import { expect, it, vi } from 'vitest';
import { render } from 'svelte/server';
vi.mock('$lib/stores/app.svelte', () => ({ app: { approvals: [], models: [] } }));
vi.mock('$lib/stores/inspector.svelte', () => ({ inspector: {} }));
import AgentRow from './AgentRow.svelte';
import IntegrationStrip from './IntegrationStrip.svelte';
import AgentInspector from './AgentInspector.svelte';
import Agents from './Agents.svelte';

const caps = { files: 'read-write', shell: 'none', network: false, spawnMax: 0 };
const ag = (id: string, extra: any = {}) => ({ id, taskId: 'task', name: `Agent ${id}`, purpose: 'Does things', assignment: { prompt: 'Implement <b>it</b>', acceptanceChecks: ['go test'] }, capabilities: caps, budgetShare: 0.25, depth: 0, state: 'running', result: { summary: '' }, usage: { inputTokens: 1, outputTokens: 1, costUsd: 0 }, version: 1, createdAt: `2026-01-01T00:00:0${id.length}Z`, updatedAt: '', modelRequested: 'p/m1', ...extra });
const html = (C: any, props: any) => render(C, { props }).body;

it('row shows state, chips, requested model, fallback badge and Cancel only while active', () => {
  const b = html(AgentRow, { agent: ag('a', { modelActual: 'p/m2' }), depth: 1, pending: false, selected: false, onselect() {}, oncancel() {} });
  expect(b).toContain('Agent a'); expect(b).toContain('files:rw'); expect(b).toContain('p/m1'); expect(b).toContain('fell back to p/m2'); expect(b).toContain('Cancel Agent a'); expect(b).toContain('Running');
  const done = html(AgentRow, { agent: ag('a', { state: 'completed', modelActual: 'p/m1' }), depth: 0, pending: false, selected: false, onselect() {}, oncancel() {} });
  expect(done).not.toContain('fell back'); expect(done).not.toContain('Cancel Agent a');
});
it('Resolve appears only for a conflict revision and names the agent', () => {
  const agents = [ag('a', { name: 'Builder' })];
  const withConflict = html(IntegrationStrip, { integration: { headTree: 'abcdef123', appliedCount: 1, verified: false, revisions: [{ agentId: 'a', state: 'conflict', conflict: 'src/x.ts' }] }, agents, canResolve: true, onresolve() {} });
  expect(withConflict).toContain('Resolve'); expect(withConflict).toContain('Builder'); expect(withConflict).toContain('src/x.ts'); expect(withConflict).toContain('unverified'); expect(withConflict).toContain('1 applied');
  const clean = html(IntegrationStrip, { integration: { headTree: 'abcdef123', appliedCount: 1, verified: true, revisions: [{ agentId: 'a', state: 'applied' }] }, agents, canResolve: true, onresolve() {} });
  expect(clean).not.toContain('Resolve'); expect(clean).toContain('verified');
  expect(html(IntegrationStrip, { integration: { headTree: 'h', appliedCount: 0, verified: true, revisions: [{ agentId: 'a', state: 'conflict' }] }, agents, canResolve: false, onresolve() {} })).not.toContain('Resolve');
});
it('shows Amended by lead with the reason and blocked text', () => {
  const b = html(IntegrationStrip, { integration: { headTree: 'h', appliedCount: 1, verified: true, amendments: 2, amendReason: 'fix <i>imports</i>', blocked: 'needs approval' }, agents: [], canResolve: true, onresolve() {} });
  expect(b).toContain('Amended by lead · fix &lt;i>imports'); expect(b).toContain('needs approval');
});
it('cancel report: applied work stays staged, unintegrated and blocked agents are named', () => {
  const agents = [ag('a', { name: 'Builder' }), ag('b', { name: 'Tester' })];
  const b = html(IntegrationStrip, { report: { headTree: 'h', appliedCount: 2, verified: true, unintegrated: ['a'], blockedAgents: ['b'], blocked: 'waiting on approval', amendments: 1, amendReason: 'rebase', message: 'Task cancelled' }, agents, canResolve: false, onresolve() {} });
  expect(b).toContain('2 applied changes stay staged and kept.'); expect(b).toContain('Builder'); expect(b).toContain('only in their workspaces'); expect(b).toContain('Tester'); expect(b).toContain('waiting on approval'); expect(b).toContain('Amended by lead'); expect(b).toContain('rebase'); expect(b).toContain('Task cancelled');
});
it('inspector shows requested vs actual model, route trail, rejected capabilities, budget share and escaped blocker', () => {
  const b = html(AgentInspector, { agent: ag('a', { modelActual: 'p/m2', routeTrail: ['p/m1', 'p/m2'], rejected: [{ field: 'shell', reason: 'no sandbox' }], result: { summary: 'done', blocker: '<script>x</script> boom' }, revision: 'abcdef1234', state: 'failed' }), threadId: 't', projectId: 'p' });
  expect(b).toContain('p/m1'); expect(b).toContain('p/m2'); expect(b).toContain('fell back to p/m2'); expect(b).toContain('shell'); expect(b).toContain('no sandbox'); expect(b).toContain('25%'); expect(b).toContain('&lt;script>x'); expect(b).not.toContain('<script>x');
  expect(b).toContain('Implement &lt;b>it'); expect(b).toContain('go test');
});
it('Agents renders the runtime tree to depth 3 and labels historical runs separately', () => {
  const agents = [ag('a'), ag('bb', { parentAgentId: 'a', modelRequested: 'p/m2' }), ag('ccc', { parentAgentId: 'bb' }), ag('dddd', { parentAgentId: 'ccc', name: 'Deep' })];
  const legacy = { id: 'child', taskId: 'old', model: { model: 'legacy-model' }, state: 'completed', assignment: { prompt: 'Legacy work' }, result: { summary: '' }, usage: { inputTokens: 1, outputTokens: 1, costUsd: 0 } };
  const state = { tasks: [
    { task: { id: 'task', threadId: 't', state: 'running' }, children: [], childErrors: {}, agents, integration: { headTree: 'h', appliedCount: 0, verified: true } },
    { task: { id: 'old', threadId: 't', state: 'completed' }, children: [legacy], childErrors: {} }], reports: {}, pending: false, error: '', cancel() {} } as any;
  const b = html(Agents, { state, threadId: 't', projectId: 'p', onsend() {} });
  expect(b).toContain('Agents · 4'); expect(b).toContain('Earlier runs · 1'); expect(b).toContain('Deep'); expect(b).toContain('Cancel task'); expect(b).toContain('Legacy work'); expect(b).toContain('legacy-model'); expect(b).toContain('p/m1 ×3, p/m2');
});
it('amend note without a reason has no duplicated label; queued Resolve is shown and disables the button', () => {
  const b = html(IntegrationStrip, { integration: { headTree: 'h', appliedCount: 1, verified: true, amendments: 1, revisions: [{ agentId: 'a', state: 'conflict' }] }, agents: [ag('a')], canResolve: true, resolveQueued: true, onresolve() {} });
  expect(b).toContain('Amended by lead<'); expect(b).not.toContain('Amended by lead · Amended'); expect(b).toContain('Queued'); expect(b).toMatch(/<button[^>]*disabled[^>]*>Resolve/);
});
it('a reloaded cancelled task shows the rebuilt report with a single Cancel-free Agents bar', () => {
  const state = { tasks: [{ task: { id: 'task', threadId: 't', state: 'cancelled' }, children: [], childErrors: {}, agents: [ag('a', { name: 'Builder' })], integration: { headTree: 'h', appliedCount: 3, verified: true, revisions: [{ agentId: 'a', state: 'submitted' }] } }], reports: {}, pending: false, error: '', cancel() {} } as any;
  const b = html(Agents, { state, threadId: 't', onsend() {} });
  expect(b).toContain('3 applied changes stay staged and kept.'); expect(b).toContain('Not integrated: Builder'); expect(b).not.toContain('Cancel task');
  const active = { ...state, tasks: [{ ...state.tasks[0], task: { id: 'task', threadId: 't', state: 'running' } }] };
  expect(html(Agents, { state: active, threadId: 't', onsend() {} }).match(/Cancel task/g)).toHaveLength(1);
});
