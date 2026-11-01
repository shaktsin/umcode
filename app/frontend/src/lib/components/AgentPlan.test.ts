import { expect, it, vi } from 'vitest';
import { render } from 'svelte/server';
vi.mock('$lib/stores/app.svelte', () => ({ app: { approvals: [{ itemId: 'i', kind: 'agent_plan', tool: 'agent.propose_plan', args: { planId: 'p1' } }] } }));
import AgentPlan from './AgentPlan.svelte';

const limits = { maxConcurrent: 4, maxDepth: 2, maxTotal: 10, sliceShare: 0.5, network: false };
const wide = { files: 'read-write', shell: 'read-write', network: true, spawnMax: 2 };
const narrow = { files: 'read-write', shell: 'none', network: false, spawnMax: 0 };
const card = {
  planId: 'p1', limits,
  agents: [
    { name: 'Builder', purpose: 'Implements', summary: 'Edits files', capabilities: wide, granted: narrow, rejected: [{ field: 'shell', reason: 'no sandbox' }] },
    { name: 'Reader', purpose: 'Reads', summary: '', capabilities: wide, granted: { files: 'read', shell: 'none', network: false, spawnMax: 0 }, rejected: null },
  ],
  models: [{ name: 'a/one', provider: 'a', toolsOk: true, available: true }, { name: 'b/two', provider: 'b', toolsOk: true, available: true }, { name: 'c/bad', provider: 'c', toolsOk: false, available: true, why: 'no tool calling' }, { name: 'd/off', provider: 'd', toolsOk: true, available: false, why: 'no credential' }],
  notices: ['shell was dropped because no sandbox is available'], previous: [{ planId: 'p0', name: 'Reader', model: 'b/two' }],
};
const plan = (state = 'proposed') => ({ id: 'p1', threadId: 't', taskId: 'k', agents: [], limits, state, createdAt: '' });
const view = (opts: { status?: string; state?: string; card?: any; active?: boolean } = {}) => {
  const entry = { plan: plan(opts.state), card: 'card' in opts ? opts.card : card, approvalStatus: opts.status ?? 'pending' };
  return {
    cards: { plan: () => entry, planActionable: () => entry.plan.state === 'proposed' && entry.approvalStatus === 'pending', respondPlan: vi.fn(), error: '' },
    delegation: { tasks: [{ task: { id: 'k' } }], active: opts.active ? { task: { id: 'k' } } : undefined, pending: false, cancel: vi.fn() },
  } as any;
};
const item = (output?: string) => ({ id: 'i', kind: 'toolCall', status: 'inProgress', tool: { name: 'agent.propose_plan', args: {}, output } }) as any;
const html = (v: any, it = item()) => render(AgentPlan, { props: { item: it, view: v } }).body;

it('renders granted chips (not the ceiling), prominent ones marked, and the notices', () => {
  const body = html(view());
  expect(body).toContain('Builder'); expect(body).toContain('Reader');
  expect(body).toContain('files:rw'); expect(body).toContain('files:r');
  expect(body).not.toContain('shell:rw'); expect(body).not.toContain('network</span>'); expect(body).not.toContain('spawn');
  expect(body).toMatch(/data-prominent="true"[^>]*>files:rw/);
  expect(body).toMatch(/data-prominent="false"[^>]*>files:r</);
  expect(body).toContain('shell was dropped because no sandbox is available');
  expect(body).toContain('Not granted: shell: no sandbox');
});
it('model dropdown lists the pool, disabling incompatible and unavailable models with why', () => {
  const body = html(view());
  expect(body).toMatch(/<option value="a\/one"(?![^>]*disabled)/);
  expect(body).toMatch(/<option value="c\/bad"[^>]*disabled[^>]*>c\/bad \(unavailable: no tool calling\)/);
  expect(body).toMatch(/<option value="d\/off"[^>]*disabled[^>]*>d\/off \(unavailable: no credential\)/);
});
it('the previous assignment preselects the entry model', () => {
  expect(html(view())).toMatch(/<option value="b\/two" selected/);
});
it('limits inputs are bounded by the ceilings', () => {
  const body = html(view());
  expect(body).toMatch(/max="8"[^>]*value="4"/); expect(body).toMatch(/max="3"[^>]*value="2"/); expect(body).toMatch(/max="40"[^>]*value="10"/); expect(body).toMatch(/max="0.5"/);
  expect(body).not.toContain('Allow network for agents');
});
it('offers the chat-model default only for entries without a model and shows an out-of-pool model explicitly', () => {
  const noModel = html(view());
  expect(noModel).toContain('Chat model (default)');
  const withModel = { ...card, agents: [{ ...card.agents[0], model: 'z/gone' }, { ...card.agents[1], model: 'a/one' }] };
  const body = html(view({ card: withModel }));
  expect(body).not.toContain('Chat model (default)');
  expect(body).toMatch(/<option value="z\/gone"[^>]*disabled[^>]*selected|<option value="z\/gone"[^>]*selected[^>]*disabled/);
  expect(body).toContain('z/gone (not in pool)');
});
it('flags maxTotal below the kept agents and disables Approve', () => {
  const body = html(view({ card: { ...card, limits: { ...limits, maxTotal: 1 } } }));
  expect(body).toContain('below the 2 kept agents'); expect(body).toMatch(/disabled[^>]*>Approve 2 agents</);
});
it('each Drop button has a unique accessible name', () => {
  const body = html(view());
  expect(body).toContain('aria-label="Drop Builder"'); expect(body).toContain('aria-label="Drop Reader"');
});
it('has Drop per agent, Approve, Decline and Revise (disabled without feedback) while pending', () => {
  const body = html(view());
  expect((body.match(/>Drop</g) ?? []).length).toBe(2);
  expect(body).toContain('Approve 2 agents'); expect(body).toContain('>Decline<');
  expect(body).toMatch(/disabled[^>]*>Revise</);
});
it.each([['approved', 'proposed', 'Approved'], ['expired', 'proposed', 'Expired'], ['pending', 'superseded', 'Superseded'], ['denied', 'proposed', 'Declined']])('shows no actions when %s/%s', (status, state, label) => {
  const body = html(view({ status, state }));
  expect(body).toContain(label);
  for (const w of ['>Drop<', 'Approve ', '>Decline<', '>Revise<', '<select', '<input']) expect(body).not.toContain(w);
  expect(body).toContain('files:rw'); // the summary stays readable
});
it('offers nothing until the get RPC has returned the card and a pending approval', () => {
  const body = html(view({ card: undefined }));
  expect(body).toContain('Loading plan'); expect(body).not.toContain('<button');
});
it('a revised result is a read-only summary with the feedback', () => {
  const v = view({ status: 'denied', card: undefined });
  const body = html(v, item('{"state":"revise","feedback":"use fewer agents"}'));
  expect(body).toContain('use fewer agents'); expect(body).not.toContain('>Revise<');
});
it('keeps Cancel task reachable while the task is active, even after a revise', () => {
  expect(html(view({ status: 'denied', active: true }))).toContain('Cancel task');
  expect(html(view({ active: false }))).not.toContain('Cancel task');
});
