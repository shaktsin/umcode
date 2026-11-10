import { beforeAll, beforeEach, expect, it, vi } from 'vitest';
const { call } = vi.hoisted(() => ({ call: vi.fn() }));
vi.mock('./app.svelte', () => ({ app: { call } }));
let AgentCards: typeof import('./agentCards.svelte').AgentCards;
beforeAll(async () => { ({ AgentCards } = await import('./agentCards.svelte')); });
beforeEach(() => call.mockReset());
const base = { id: 'p1', threadId: 't', taskId: 'k', agents: [], limits: { maxConcurrent: 8, maxDepth: 3, maxTotal: 40, sliceShare: .25, network: false }, createdAt: '' };
const plan = { ...base, state: 'superseded' as const }, proposed = { ...base, state: 'proposed' as const };

it('refreshPlan shows an expired approval from the get RPC, never approvable', async () => {
  const c = new AgentCards(); call.mockResolvedValueOnce({ plan, approvalStatus: 'expired' });
  await c.refreshPlan('p1'); expect(c.planActionable('p1')).toBe(false);
  expect(call).toHaveBeenCalledWith('agent/plan/get', { planId: 'p1' });
  expect(c.plan('p1')?.approvalStatus).toBe('expired');
});
it('respondPlan sends AgentPlanRespondParams exactly and stores the result', async () => {
  const c = new AgentCards(); c.applyPlanUpdate('p1', proposed, 'pending'); const limits = { ...plan.limits, maxConcurrent: 2 };
  call.mockResolvedValueOnce({ plan: { ...base, state: 'approved' }, approvalStatus: 'approved' }); call.mockClear();
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

it('actionable only for a proposed plan with a pending approval; a missing status never keeps stale pending', async () => {
  const c = new AgentCards(); call.mockResolvedValue({ plan: proposed, approvalStatus: 'pending' });
  c.applyPlanUpdate('p1', proposed, 'pending'); expect(c.planActionable('p1')).toBe(true);
  c.applyPlanUpdate('p1', { ...base, state: 'superseded' }); // status omitted
  expect(c.planActionable('p1')).toBe(false); expect(c.plan('p1')?.approvalStatus).toBe('expired');
  c.applyPlanUpdate('p2', { ...base, id: 'p2', state: 'approved' }); expect(c.plan('p2')?.approvalStatus).toBe('approved'); expect(c.planActionable('p2')).toBe(false);
});
it('a late get or older update never regresses a newer plan', async () => {
  const c = new AgentCards(); call.mockResolvedValue({ plan: proposed, approvalStatus: 'pending' });
  c.applyPlanUpdate('p1', { ...base, state: 'approved' }, 'approved');
  call.mockResolvedValueOnce({ plan: proposed, approvalStatus: 'pending' }); await c.refreshPlan('p1');
  c.applyPlanUpdate('p1', proposed, 'pending');
  expect(c.plan('p1')?.plan.state).toBe('approved'); expect(c.plan('p1')?.approvalStatus).toBe('approved'); expect(c.planActionable('p1')).toBe(false);
  c.applyPlanUpdate('p1', { ...base, state: 'superseded' }, 'expired');
  c.applyPlanUpdate('p1', { ...base, state: 'approved' }, 'approved');
  expect(c.plan('p1')?.plan.state).toBe('superseded');
  // same state: a denied proposal is not revived by a late pending
  const d = new AgentCards(); d.applyPlanUpdate('p1', proposed, 'denied'); d.applyPlanUpdate('p1', proposed, 'pending');
  expect(d.planActionable('p1')).toBe(false);
});
it('refresh failures set error, keep prior state and never expose an actionable one', async () => {
  const c = new AgentCards(); let reply: any = new Error('boom');
  call.mockImplementation(async () => { if (reply instanceof Error) throw reply; return reply; });
  const sug = (state: string) => ({ suggestion: { id: 's', threadId: 't', turnId: 'u', reason: '', state, createdAt: '' } });
  await c.refreshPlan('p1'); await c.refreshSuggestion('s');
  expect(c.error).toMatch(/boom/); expect(c.planActionable('p1')).toBe(false); expect(c.suggestionActionable('s')).toBe(false);
  reply = { plan: proposed, approvalStatus: 'pending' }; await c.refreshPlan('p1'); expect(c.planActionable('p1')).toBe(true); expect(c.error).toBe('');
  reply = new Error('down'); await c.refreshPlan('p1'); expect(c.error).toMatch(/down/); expect(c.planActionable('p1')).toBe(true);
  reply = sug('pending'); await c.refreshSuggestion('s'); expect(c.suggestionActionable('s')).toBe(true);
  reply = sug('declined'); await c.refreshSuggestion('s'); expect(c.suggestionActionable('s')).toBe(false);
});
