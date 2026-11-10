import { expect, it } from 'vitest';
import { applyLimit, approveParams, canApprove, clampLimits, limitsError, modelOutsidePool, declineParams, defaultModel, draftOf, noEdits, parsePlanResult, planIdForItem, reviseParams } from './agentPlanCard';

const caps = { files: 'read' as const, shell: 'none' as const, network: false, spawnMax: 0 };
const limits = { maxConcurrent: 4, maxDepth: 2, maxTotal: 10, sliceShare: 0.5, network: false };
const card: any = {
  planId: 'p1', limits,
  agents: [{ name: 'Reviewer', purpose: '', summary: '', capabilities: caps, granted: caps, rejected: null, model: 'a/one' }, { name: 'Writer', purpose: '', summary: '', capabilities: caps, granted: caps, rejected: null }],
  models: [{ name: 'a/one', provider: 'a', toolsOk: true, available: true }, { name: 'b/two', provider: 'b', toolsOk: true, available: true }, { name: 'c/bad', provider: 'c', toolsOk: false, available: true, why: 'no tools' }],
  notices: null, previous: [{ planId: 'p0', name: 'writer', model: 'b/two' }],
};

it('parses tool results', () => {
  expect(parsePlanResult('{"planId":"p","state":"approved"}')).toMatchObject({ planId: 'p', state: 'approved' });
  expect(parsePlanResult('{"state":"revise","feedback":"smaller"}')).toMatchObject({ state: 'revise', feedback: 'smaller' });
});
it('finds the plan from the pending approval card, else the approved result', () => {
  const item: any = { id: 'i', tool: { output: '{"planId":"from-result","state":"approved"}' } };
  expect(planIdForItem(item, [{ itemId: 'i', kind: 'agent_plan', args: { planId: 'from-card' } } as any])).toBe('from-card');
  expect(planIdForItem(item, [])).toBe('from-result');
  expect(planIdForItem({ id: 'i', tool: {} } as any, [])).toBeUndefined();
});
it('previous assignment preselects the model when the entry has none; unusable models never preselect', () => {
  expect(defaultModel(card, card.agents[1])).toBe('b/two');
  expect(defaultModel(card, card.agents[0])).toBe('a/one');
  expect(defaultModel({ ...card, previous: [{ planId: 'p0', name: 'Writer', model: 'c/bad' }] }, card.agents[1])).toBe('');
  expect(defaultModel(card, { ...card.agents[0], model: 'c/bad' })).toBe('c/bad');
});
it('limits are bounded by the protocol ceilings, not the proposal', () => {
  expect(clampLimits({ maxConcurrent: 99, maxDepth: 0, maxTotal: 99, sliceShare: 0.9, network: true }, false)).toEqual({ maxConcurrent: 8, maxDepth: 1, maxTotal: 40, sliceShare: 0.5, network: false });
  // above the proposal but within the ceilings is allowed
  expect(draftOf(card, { ...noEdits(), limits: { maxConcurrent: 8, sliceShare: 0.5, maxTotal: 40 } }).limits).toMatchObject({ maxConcurrent: 8, maxTotal: 40, sliceShare: 0.5 });
  // network can be lowered, never raised
  expect(draftOf({ ...card, limits: { ...limits, network: true } }, { ...noEdits(), limits: { network: false } }).limits.network).toBe(false);
  expect(draftOf(card, { ...noEdits(), limits: { network: true } }).limits.network).toBe(false);
});
it('an empty or invalid field keeps the last valid value', () => {
  const e1 = applyLimit(noEdits(), 'maxConcurrent', 3);
  expect(draftOf(card, e1).limits.maxConcurrent).toBe(3);
  const e2 = applyLimit(e1, 'maxConcurrent', NaN);
  expect(draftOf(card, e2).limits.maxConcurrent).toBe(3);
  expect(draftOf(card, applyLimit(noEdits(), 'maxTotal', NaN)).limits.maxTotal).toBe(10);
  expect(draftOf(card, applyLimit(noEdits(), 'maxTotal', 99)).limits.maxTotal).toBe(40);
});
it('maxTotal below the kept agents is flagged and blocks Approve', () => {
  const d = draftOf(card, applyLimit(noEdits(), 'maxTotal', 1));
  expect(limitsError(card, d)).toMatch(/below the 2 kept/); expect(canApprove(card, d)).toBe(false);
  expect(canApprove(card, draftOf(card, { ...applyLimit(noEdits(), 'maxTotal', 1), dropped: ['Writer'] }))).toBe(true);
});
it('the select shows what Approve keeps: an unusable or out-of-pool own model is selected and sent as is', () => {
  const c = { ...card, agents: [{ ...card.agents[0], model: 'z/gone' }, { ...card.agents[1], model: 'c/bad' }] };
  const d = draftOf(c, noEdits());
  expect(d.models).toEqual({ Reviewer: 'z/gone', Writer: 'c/bad' });
  expect(modelOutsidePool(c, c.agents[0])).toBe(true); expect(modelOutsidePool(c, c.agents[1])).toBe(false);
  expect(approveParams(c, d).models).toBeUndefined(); // nothing edited: the engine keeps exactly what is shown
  expect(approveParams(c, draftOf(c, { ...noEdits(), models: { Reviewer: 'b/two' } })).models).toEqual({ Reviewer: 'b/two' });
});
it('approve sends only edited models, dropped names and changed limits', () => {
  const d = draftOf(card, { models: { Reviewer: 'b/two' }, dropped: ['Writer'], limits: { maxConcurrent: 2 } });
  expect(approveParams(card, d)).toEqual({ planId: 'p1', decision: 'approve', models: { Reviewer: 'b/two' }, drop: ['Writer'], limits: { ...limits, maxConcurrent: 2 } });
  expect(approveParams(card, draftOf(card, noEdits()))).toEqual({ planId: 'p1', decision: 'approve', models: { Writer: 'b/two' } });
});
it('approve is impossible when every agent is dropped', () => {
  expect(canApprove(card, draftOf(card, { ...noEdits(), dropped: ['Reviewer', 'Writer'] }))).toBe(false);
  expect(canApprove(card, draftOf(card, noEdits()))).toBe(true);
});
it('revise requires non-empty feedback; decline is bare', () => {
  expect(reviseParams('p1', '  ')).toBeNull();
  expect(reviseParams('p1', ' fewer agents ')).toEqual({ planId: 'p1', decision: 'revise', feedback: 'fewer agents' });
  expect(declineParams('p1')).toEqual({ planId: 'p1', decision: 'decline' });
});
