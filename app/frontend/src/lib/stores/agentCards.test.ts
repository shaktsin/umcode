import { beforeAll, beforeEach, expect, it, vi } from 'vitest';
const { call } = vi.hoisted(() => ({ call: vi.fn() }));
vi.mock('./app.svelte', () => ({ app: { call } }));
let AgentCards: typeof import('./agentCards.svelte').AgentCards;
beforeAll(async () => { ({ AgentCards } = await import('./agentCards.svelte')); });
beforeEach(() => call.mockReset());
const plan = { id: 'p1', threadId: 't', taskId: 'k', agents: [], limits: { maxConcurrent: 8, maxDepth: 3, maxTotal: 40, sliceShare: .25, network: false }, state: 'superseded', createdAt: '' };

it('refreshPlan shows an expired approval from the get RPC, never approvable', async () => {
  const c = new AgentCards(); call.mockResolvedValueOnce({ plan, approvalStatus: 'expired' });
  await c.refreshPlan('p1');
  expect(call).toHaveBeenCalledWith('agent/plan/get', { planId: 'p1' });
  expect(c.plan('p1')?.approvalStatus).toBe('expired');
});
it('respondPlan sends AgentPlanRespondParams exactly and stores the result', async () => {
  const c = new AgentCards(); const limits = { ...plan.limits, maxConcurrent: 2 };
  call.mockResolvedValueOnce({ plan: { ...plan, state: 'approved' }, approvalStatus: 'approved' });
  call.mockResolvedValue({ plan, approvalStatus: 'pending' }); // late card fetch must not clobber the decision
  await c.respondPlan({ planId: 'p1', decision: 'approve', models: { a: 'p/m' }, drop: ['b'], limits, feedback: 'ok' });
  expect(call.mock.calls[0]).toEqual(['agent/plan/respond', { planId: 'p1', decision: 'approve', models: { a: 'p/m' }, drop: ['b'], limits, feedback: 'ok' }]);
  await new Promise(r => setTimeout(r)); expect(c.plan('p1')?.approvalStatus).toBe('approved'); expect(c.plan('p1')?.plan.state).toBe('approved');
});
it('suggestions come from get/respond and keep the activation token', async () => {
  const c = new AgentCards(); const sug = { id: 's', threadId: 't', turnId: 'u', reason: 'r', state: 'pending', createdAt: '' };
  call.mockResolvedValueOnce({ suggestion: { ...sug, state: 'expired' } }); await c.refreshSuggestion('s');
  expect(call).toHaveBeenCalledWith('agent/suggestion/get', { suggestionId: 's' }); expect(c.suggestion('s')?.state).toBe('expired');
  call.mockResolvedValueOnce({ suggestion: { ...sug, state: 'enabled' }, activationToken: 'tok', expiresAt: '2030-01-01T00:00:00Z' });
  await c.respondSuggestion('s', 'enable');
  expect(call).toHaveBeenLastCalledWith('agent/suggestion/respond', { suggestionId: 's', decision: 'enable' });
  expect(c.suggestion('s')?.state).toBe('enabled'); expect(c.activation?.activationToken).toBe('tok');
});
