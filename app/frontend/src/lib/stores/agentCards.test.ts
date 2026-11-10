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
it('shows recovery only when the server marks an approved plan as needing continuation', async () => {
  const c = new AgentCards();
  const approved = { ...base, state: 'approved' as const };
  call.mockResolvedValueOnce({ plan: approved, approvalStatus: 'approved', continuationNeeded: true });
  await c.refreshPlan('p1');
  expect(c.approvedPlanNeedingContinuation('k')?.plan.id).toBe('p1');
  c.applyPlanUpdate('p1', approved, 'approved');
  expect(c.approvedPlanNeedingContinuation('k')?.plan.id).toBe('p1');
  call.mockResolvedValueOnce({ plan: approved, approvalStatus: 'approved', continuationNeeded: false });
  await c.refreshPlan('p1');
  expect(c.approvedPlanNeedingContinuation('k')).toBeUndefined();
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
it('refresh failure reports the error and a later successful read restores the plan', async () => {
  const c = new AgentCards(); let reply: any = new Error('boom');
  call.mockImplementation(async () => { if (reply instanceof Error) throw reply; return reply; });
  await c.refreshPlan('p1');
  expect(c.error).toMatch(/boom/); expect(c.planActionable('p1')).toBe(false);
  reply = { plan: proposed, approvalStatus: 'pending' }; await c.refreshPlan('p1'); expect(c.planActionable('p1')).toBe(true); expect(c.error).toBe('');
});

it('onPlanUpdated without approvalStatus reads the plan back; with one it does not', async () => {
  const c = new AgentCards(); call.mockReset(); call.mockResolvedValue({ plan: proposed, approvalStatus: 'pending', card: { planId: 'p1' } });
  c.onPlanUpdated({ plan: proposed }); expect(c.planActionable('p1')).toBe(false);
  await new Promise(r => setTimeout(r)); expect(call).toHaveBeenCalledWith('agent/plan/get', { planId: 'p1' }); expect(c.planActionable('p1')).toBe(true);
  call.mockClear(); c.onPlanUpdated({ plan: proposed, approvalStatus: 'pending' });
});
